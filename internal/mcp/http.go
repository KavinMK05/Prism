package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// maxHTTPBody caps an upstream response body (tool results from HTTP servers
// can be large, but not unbounded).
const maxHTTPBody = 32 << 20

// httpTransport speaks the MCP Streamable HTTP transport: one POST per
// JSON-RPC message, answered either with a JSON body or with a single
// request-scoped SSE stream. It also implements the deprecated HTTP+SSE
// transport (transport "sse"), where a long-lived GET stream carries replies
// and announces a message endpoint to POST to.
type httpTransport struct {
	url       string
	static    map[string]string
	dyn       dynamicHeaders
	params    toolParamsFunc
	legacySSE bool
	client    *http.Client

	idMu   sync.Mutex
	nextID int64

	protoMu      sync.Mutex
	protoVersion string

	sessMu    sync.Mutex
	sessionID string

	// legacy SSE state (transport "sse" only)
	pend        *pending
	streamMu    sync.Mutex
	streamOnce  bool
	streamStart chan struct{}
	streamErr   error
	endpoint    string
	streamStop  context.CancelFunc
}

func newHTTPTransport(url string, static map[string]string, dyn dynamicHeaders, params toolParamsFunc, legacySSE bool) *httpTransport {
	t := &httpTransport{
		url:       url,
		static:    static,
		dyn:       dyn,
		params:    params,
		legacySSE: legacySSE,
		client: &http.Client{
			// No global timeout: tool calls can legitimately run for minutes.
			// Callers bound each request with their own context.
			Transport: &http.Transport{
				Proxy:               http.ProxyFromEnvironment,
				MaxIdleConnsPerHost: 4,
				IdleConnTimeout:     90 * time.Second,
			},
		},
	}
	if legacySSE {
		t.pend = newPending()
		t.streamStart = make(chan struct{})
	}
	return t
}

func (t *httpTransport) nextRequestID() json.RawMessage {
	t.idMu.Lock()
	t.nextID++
	id := t.nextID
	t.idMu.Unlock()
	raw, _ := json.Marshal(id)
	return raw
}

func (t *httpTransport) RoundTrip(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
	if t.legacySSE {
		return t.legacyRoundTrip(ctx, method, params)
	}
	paramsJSON, err := marshalParams(params)
	if err != nil {
		return nil, err
	}
	id := t.nextRequestID()
	req := rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: t.metaParams(method, paramsJSON)}
	raw, err := t.do(ctx, req)
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, rpcErrorf(CodeInternalError, "empty response from %s", t.url)
	}
	var resp rpcResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("invalid JSON-RPC response from %s: %w", t.url, err)
	}
	if perr := responseError(&resp); perr != nil {
		return nil, perr
	}
	t.rememberProtocolVersion(method, resp.Result)
	return resp.Result, nil
}

func (t *httpTransport) Notify(ctx context.Context, method string, params interface{}) error {
	if t.legacySSE {
		// Notifications need no reply; POST and forget.
		paramsJSON, err := marshalParams(params)
		if err != nil {
			return err
		}
		_, err = t.legacyPost(ctx, rpcRequest{JSONRPC: "2.0", Method: method, Params: injectMeta(paramsJSON)})
		return err
	}
	paramsJSON, err := marshalParams(params)
	if err != nil {
		return err
	}
	_, err = t.do(ctx, rpcRequest{JSONRPC: "2.0", Method: method, Params: t.metaParams(method, paramsJSON)})
	return err
}

// metaParams adds the stateless revision's `_meta` block to a request's params.
// The legacy initialize exchange carries its protocol version and client
// identity at the top level of params instead, and the stateless revision does
// not define `_meta` at all, so that one request is left alone.
func (t *httpTransport) metaParams(method string, params json.RawMessage) json.RawMessage {
	if method == "initialize" {
		return params
	}
	return injectMetaVersion(params, t.protocolVersionFor(method))
}

// protocolVersionFor returns the protocol version header value for a request.
// The header has to agree with the version the body advertises: the legacy
// handshake negotiates it in the initialize params, the stateless revision in
// `_meta`.
func (t *httpTransport) protocolVersionFor(method string) string {
	if method == "initialize" {
		return LegacyProtocolVersion
	}
	t.protoMu.Lock()
	defer t.protoMu.Unlock()
	if t.protoVersion != "" {
		return t.protoVersion
	}
	return ProtocolVersion
}

// rememberProtocolVersion records the revision the server settled on, so later
// requests advertise the version both sides agreed to.
func (t *httpTransport) rememberProtocolVersion(method string, result json.RawMessage) {
	if len(result) == 0 {
		return
	}
	var r struct {
		ProtocolVersion   string   `json:"protocolVersion"`
		SupportedVersions []string `json:"supportedVersions"`
	}
	if json.Unmarshal(result, &r) != nil {
		return
	}
	version := r.ProtocolVersion
	if version == "" && method == "server/discover" {
		for _, candidate := range r.SupportedVersions {
			if candidate == ProtocolVersion {
				version = candidate
				break
			}
		}
	}
	if version == "" {
		return
	}
	t.protoMu.Lock()
	t.protoVersion = version
	t.protoMu.Unlock()
}

// do sends one JSON-RPC message and returns the raw reply payload.
func (t *httpTransport) do(ctx context.Context, msg rpcRequest) (json.RawMessage, error) {
	body, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if err := t.applyHeaders(ctx, req); err != nil {
		return nil, err
	}
	t.applyRequestMetadata(req, msg)
	resp, err := t.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if err := t.checkAuth(resp); err != nil {
		return nil, err
	}
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		t.sessMu.Lock()
		t.sessionID = sid
		t.sessMu.Unlock()
	}
	if resp.StatusCode == http.StatusAccepted || resp.StatusCode == http.StatusNoContent {
		return nil, nil
	}
	if resp.StatusCode >= 400 {
		return nil, upstreamHTTPError(t.url, resp)
	}
	if strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		return readSSEForResponse(ctx, resp.Body, string(msg.ID))
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxHTTPBody))
	if err != nil {
		return nil, err
	}
	return data, nil
}

// applyRequestMetadata mirrors the JSON-RPC message into the HTTP headers the
// stateless revision defines, so servers and intermediaries can route and
// validate a request without parsing its body: MCP-Protocol-Version always,
// Mcp-Method for every request, Mcp-Name for the methods that address one
// named item, and Mcp-Param-* for tool parameters the tool's schema annotates
// with x-mcp-header. Servers that predate the revision ignore all of them.
func (t *httpTransport) applyRequestMetadata(req *http.Request, msg rpcRequest) {
	req.Header.Set("MCP-Protocol-Version", t.protocolVersionFor(msg.Method))
	if msg.Method != "" {
		req.Header.Set("Mcp-Method", msg.Method)
	}
	if name, ok := mcpRoutingName(msg.Method, msg.Params); ok {
		req.Header.Set("Mcp-Name", mirrorHeaderValue(name))
	}
	if msg.Method == "tools/call" && t.params != nil {
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if json.Unmarshal(msg.Params, &p) == nil && p.Name != "" {
			for k, v := range mcpParamHeaders(t.params(p.Name), p.Arguments) {
				req.Header.Set(k, v)
			}
		}
	}
}

// applyHeaders sets the transport headers, static config headers, then dynamic
// ones (so a refreshed bearer token always wins over a stale static value).
func (t *httpTransport) applyHeaders(ctx context.Context, req *http.Request) error {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("User-Agent", ServerName+"/"+mcpClientVersion)
	for k, v := range t.static {
		req.Header.Set(k, v)
	}
	if t.dyn != nil {
		extra, err := t.dyn(ctx)
		if err != nil {
			return err
		}
		for k, v := range extra {
			req.Header.Set(k, v)
		}
	}
	t.sessMu.Lock()
	sid := t.sessionID
	t.sessMu.Unlock()
	if sid != "" {
		req.Header.Set("Mcp-Session-Id", sid)
	}
	return nil
}

// checkAuth maps an upstream 401/403 into *AuthRequiredError, keeping the
// WWW-Authenticate challenge so the OAuth client can find the resource
// metadata document.
func (t *httpTransport) checkAuth(resp *http.Response) error {
	if resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusForbidden {
		return nil
	}
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<10))
	return &AuthRequiredError{
		Status:    resp.StatusCode,
		Challenge: resp.Header.Get("WWW-Authenticate"),
		Message: fmt.Sprintf("%s requires authorization (HTTP %d): %s",
			t.url, resp.StatusCode, strings.TrimSpace(string(snippet))),
	}
}

// httpStatusError is an upstream HTTP failure whose body was not a JSON-RPC
// error: a plain status, a proxy error page, an HTML 404. It is kept as its own
// type so the handshake can tell "this server does not know the method" apart
// from "the endpoint is not there at all".
type httpStatusError struct {
	URL    string
	Status int
	Body   string
}

func (e *httpStatusError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("%s returned HTTP %d", e.URL, e.Status)
	}
	return fmt.Sprintf("%s returned HTTP %d: %s", e.URL, e.Status, e.Body)
}

// isHTTPStatusError reports whether err is an upstream HTTP failure with one of
// the given status codes.
func isHTTPStatusError(err error, statuses ...int) bool {
	var statusErr *httpStatusError
	if !errors.As(err, &statusErr) {
		return false
	}
	for _, status := range statuses {
		if statusErr.Status == status {
			return true
		}
	}
	return false
}

// upstreamHTTPError turns a failed HTTP response into an error, keeping a
// JSON-RPC error body intact: a modern server answers an unsupported method
// with 404 and code -32601, which is how the handshake detects the era a server
// speaks.
func upstreamHTTPError(url string, resp *http.Response) error {
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	body := strings.TrimSpace(string(snippet))
	if rpcErr, ok := jsonRPCErrorFromBody(body); ok {
		enriched := *rpcErr
		enriched.Message = fmt.Sprintf("%s returned HTTP %d: %s", url, resp.StatusCode, rpcErr.Message)
		return &enriched
	}
	return &httpStatusError{URL: url, Status: resp.StatusCode, Body: body}
}

// jsonRPCErrorFromBody extracts a JSON-RPC error object from a response body,
// so an HTTP status does not hide the error code inside it.
func jsonRPCErrorFromBody(body string) (*rpcError, bool) {
	trimmed := strings.TrimSpace(body)
	if !strings.HasPrefix(trimmed, "{") {
		return nil, false
	}
	var envelope struct {
		Error *rpcError `json:"error"`
	}
	if json.Unmarshal([]byte(trimmed), &envelope) != nil || envelope.Error == nil {
		return nil, false
	}
	return envelope.Error, true
}

func (t *httpTransport) Close() error {
	t.streamMu.Lock()
	stop := t.streamStop
	t.streamMu.Unlock()
	if stop != nil {
		stop()
	}
	if t.pend != nil {
		t.pend.clear()
	}
	return nil
}

// —— legacy HTTP+SSE transport ——

func (t *httpTransport) legacyRoundTrip(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
	if err := t.ensureStream(ctx); err != nil {
		return nil, err
	}
	paramsJSON, err := marshalParams(params)
	if err != nil {
		return nil, err
	}
	id := t.nextRequestID()
	ch, remove := t.pend.add(id)
	defer remove()
	if _, err := t.legacyPost(ctx, rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: injectMeta(paramsJSON)}); err != nil {
		return nil, err
	}
	resp, err := awaitResponse(ctx, ch)
	if err != nil {
		return nil, err
	}
	if perr := responseError(resp); perr != nil {
		return nil, perr
	}
	return resp.Result, nil
}

// ensureStream opens the server's event stream once and waits for the endpoint
// announcement that tells Prism where to POST requests.
func (t *httpTransport) ensureStream(ctx context.Context) error {
	t.streamMu.Lock()
	if t.streamOnce {
		started, err := t.streamStart, t.streamErr
		t.streamMu.Unlock()
		select {
		case <-started:
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	t.streamOnce = true
	streamCtx, cancel := context.WithCancel(context.Background())
	t.streamStop = cancel
	started := t.streamStart
	t.streamMu.Unlock()

	err := t.openStream(streamCtx, started)
	t.streamMu.Lock()
	t.streamErr = err
	t.streamMu.Unlock()
	if err != nil {
		close(started)
		return err
	}
	return nil
}

func (t *httpTransport) openStream(ctx context.Context, started chan struct{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("MCP-Protocol-Version", ProtocolVersion)
	for k, v := range t.static {
		req.Header.Set(k, v)
	}
	if t.dyn != nil {
		extra, err := t.dyn(ctx)
		if err == nil {
			for k, v := range extra {
				req.Header.Set(k, v)
			}
		}
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return err
	}
	if err := t.checkAuth(resp); err != nil {
		resp.Body.Close()
		return err
	}
	if resp.StatusCode >= 400 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<10))
		resp.Body.Close()
		return fmt.Errorf("%s returned HTTP %d: %s", t.url, resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	go t.streamLoop(ctx, resp.Body, started)
	return nil
}

// streamLoop consumes the server's SSE stream: the first event announces the
// message endpoint, everything after that is a JSON-RPC reply.
func (t *httpTransport) streamLoop(ctx context.Context, body io.ReadCloser, started chan struct{}) {
	defer body.Close()
	announced := false
	events := make(chan sseEvent, 8)
	go readSSEStream(ctx, body, events)
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			if !announced {
				announced = true
				t.streamMu.Lock()
				t.endpoint = resolveEndpoint(t.url, ev.Data)
				t.streamMu.Unlock()
				close(started)
				continue
			}
			var resp rpcResponse
			if err := json.Unmarshal([]byte(ev.Data), &resp); err != nil {
				continue
			}
			t.pend.deliver(&resp)
		}
	}
}

func (t *httpTransport) legacyPost(ctx context.Context, msg rpcRequest) (json.RawMessage, error) {
	t.streamMu.Lock()
	endpoint := t.endpoint
	t.streamMu.Unlock()
	if endpoint == "" {
		return nil, fmt.Errorf("no message endpoint announced by %s", t.url)
	}
	body, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if err := t.applyHeaders(ctx, req); err != nil {
		return nil, err
	}
	// The deprecated HTTP+SSE transport predates the request-metadata headers;
	// the protocol version is still advertised so a dual-era server can tell
	// which revision is speaking.
	req.Header.Set("MCP-Protocol-Version", ProtocolVersion)
	resp, err := t.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := t.checkAuth(resp); err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<10))
		return nil, fmt.Errorf("%s returned HTTP %d: %s", endpoint, resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, maxHTTPBody))
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil
	}
	return data, nil
}

// resolveEndpoint turns the relative endpoint an SSE server announces into an
// absolute URL, per the legacy transport's rules.
func resolveEndpoint(base, endpoint string) string {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return ""
	}
	if strings.HasPrefix(endpoint, "http://") || strings.HasPrefix(endpoint, "https://") {
		return endpoint
	}
	u, err := parseURL(base)
	if err != nil {
		return endpoint
	}
	if strings.HasPrefix(endpoint, "/") {
		return u.Scheme + "://" + u.Host + endpoint
	}
	dir := u.Path
	if i := strings.LastIndex(dir, "/"); i >= 0 {
		dir = dir[:i+1]
	} else {
		dir = "/"
	}
	return u.Scheme + "://" + u.Host + dir + endpoint
}

// —— SSE parsing ——

type sseEvent struct {
	Event string
	Data  string
}

// readSSEStream parses `event:`/`data:` frames until the body ends.
func readSSEStream(ctx context.Context, body io.Reader, out chan<- sseEvent) {
	defer close(out)
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64<<10), maxHTTPBody)
	var event sseEvent
	haveData := false
	emit := func() {
		if !haveData {
			return
		}
		select {
		case out <- event:
		case <-ctx.Done():
		}
		event = sseEvent{}
		haveData = false
	}
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "":
			emit()
		case strings.HasPrefix(line, ":"):
			// comment / keep-alive
		case strings.HasPrefix(line, "event:"):
			event.Event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			chunk := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if haveData {
				event.Data += "\n" + chunk
			} else {
				event.Data = chunk
				haveData = true
			}
		}
	}
	emit()
}

// readSSEForResponse reads a request-scoped SSE stream and returns the payload
// whose JSON-RPC id matches wantID. Server notifications are skipped.
func readSSEForResponse(ctx context.Context, body io.Reader, wantID string) (json.RawMessage, error) {
	events := make(chan sseEvent, 8)
	go readSSEStream(ctx, body, events)
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case ev, ok := <-events:
			if !ok {
				return nil, fmt.Errorf("SSE stream ended before a reply arrived")
			}
			if ev.Data == "" {
				continue
			}
			var resp rpcResponse
			if err := json.Unmarshal([]byte(ev.Data), &resp); err != nil {
				continue
			}
			if wantID != "" && string(resp.ID) != wantID {
				continue
			}
			return []byte(ev.Data), nil
		}
	}
}

func parseURL(raw string) (*urlParts, error) {
	req, err := http.NewRequest(http.MethodGet, raw, nil)
	if err != nil {
		return nil, err
	}
	return &urlParts{Scheme: req.URL.Scheme, Host: req.URL.Host, Path: req.URL.Path}, nil
}

type urlParts struct {
	Scheme string
	Host   string
	Path   string
}
