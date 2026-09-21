package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"

	"ollama-proxy/internal/config"
)

// maxStdioMessage caps a single newline-delimited JSON-RPC message so a
// runaway server cannot exhaust memory.
const maxStdioMessage = 16 << 20

// stdioTransport speaks newline-delimited JSON-RPC over a child process's
// stdin/stdout, which is how local MCP servers (npx, uvx, docker, plain
// binaries) are run.
type stdioTransport struct {
	cmd      *commandHandle
	stdin    io.WriteCloser
	writeMu  sync.Mutex
	pend     *pending
	nextID   int64
	idMu     sync.Mutex
	done     chan struct{}
	closeOne sync.Once

	errMu   sync.Mutex
	procErr error
	stderr  *tailBuffer
}

// newStdioTransport spawns the configured command. It returns
// *RuntimeMissingError when the executable cannot be resolved, so the manager
// can report runtime_missing instead of a generic failure.
func newStdioTransport(s *config.MCPServerConfig) (*stdioTransport, error) {
	resolved, ok := resolveCommand(s.Command)
	if !ok {
		return nil, &RuntimeMissingError{Command: s.Command}
	}

	t := &stdioTransport{
		pend:   newPending(),
		done:   make(chan struct{}),
		stderr: newTailBuffer(8 << 10),
	}

	cmd := commandRunner(resolved, s.Args)
	cmd.Env = mergedEnv(s.Env)
	if s.Cwd != "" {
		cmd.Dir = s.Cwd
	} else if home, err := os.UserHomeDir(); err == nil && home != "" {
		// A GUI-launched Prism has an arbitrary working directory (often
		// C:\Windows\System32 on Windows); MCP servers that resolve relative
		// paths expect something user-owned.
		cmd.Dir = home
	}
	cmd.Stderr = t.stderr
	cmd.Stdin = nil

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to open stdin for %s: %w", s.Command, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return nil, fmt.Errorf("failed to open stdout for %s: %w", s.Command, err)
	}
	t.stdin = stdin

	if err := cmd.Start(); err != nil {
		stdin.Close()
		return nil, &RuntimeMissingError{Command: s.Command}
	}
	t.cmd = newCommandHandle(cmd)

	go t.readLoop(stdout)
	return t, nil
}

// readLoop dispatches each complete line the server writes. Responses are
// routed to their waiter by id; server-initiated notifications are dropped
// (Prism only forwards tools/list_changed, which it re-learns on next list).
func (t *stdioTransport) readLoop(stdout io.Reader) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64<<10), maxStdioMessage)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var resp rpcResponse
		if err := json.Unmarshal(line, &resp); err != nil {
			continue
		}
		if len(resp.ID) == 0 {
			continue // server notification or request: not something Prism awaits
		}
		if !t.pend.deliver(&resp) && resp.Error == nil {
			// Nothing waiting: either a late reply or a server-initiated
			// request with an id. Both are safe to ignore here.
			continue
		}
	}
	err := scanner.Err()
	if err == nil {
		err = io.EOF
	}
	t.fail(fmt.Errorf("server process exited: %w", err))
}

// fail marks the transport dead and unblocks every waiter.
func (t *stdioTransport) fail(err error) {
	t.errMu.Lock()
	if t.procErr == nil {
		t.procErr = err
	}
	t.errMu.Unlock()
	t.pend.clear()
	t.closeOne.Do(func() { close(t.done) })
}

func (t *stdioTransport) lastError() error {
	t.errMu.Lock()
	defer t.errMu.Unlock()
	return t.procErr
}

func (t *stdioTransport) nextRequestID() (json.RawMessage, int64) {
	t.idMu.Lock()
	t.nextID++
	id := t.nextID
	t.idMu.Unlock()
	raw, _ := json.Marshal(id)
	return raw, id
}

func (t *stdioTransport) RoundTrip(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
	if err := t.lastError(); err != nil {
		return nil, err
	}
	paramsJSON, err := marshalParams(params)
	if err != nil {
		return nil, err
	}
	id, _ := t.nextRequestID()
	ch, remove := t.pend.add(id)
	defer remove()

	if err := t.write(rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: paramsJSON}); err != nil {
		return nil, err
	}
	return t.await(ctx, ch)
}

// await waits for the reply, failing fast when the process dies. A reply that
// the server wrote before exiting is always preferred over the transport
// error, so a crash after a completed call is not mistaken for a lost call.
func (t *stdioTransport) await(ctx context.Context, ch chan *rpcResponse) (json.RawMessage, error) {
	reply := func(resp *rpcResponse) (json.RawMessage, error) {
		if perr := responseError(resp); perr != nil {
			return nil, perr
		}
		return resp.Result, nil
	}
	select {
	case resp := <-ch:
		return reply(resp)
	default:
	}
	select {
	case resp := <-ch:
		return reply(resp)
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-t.done:
		// The read loop may have delivered the reply on its way out.
		select {
		case resp := <-ch:
			return reply(resp)
		default:
		}
		if err := t.lastError(); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("MCP transport closed before the server replied")
	}
}

func (t *stdioTransport) Notify(ctx context.Context, method string, params interface{}) error {
	if err := t.lastError(); err != nil {
		return err
	}
	paramsJSON, err := marshalParams(params)
	if err != nil {
		return err
	}
	return t.write(rpcRequest{JSONRPC: "2.0", Method: method, Params: paramsJSON})
}

// write serializes one message onto the server's stdin. stdio framing is
// newline-delimited JSON, so the payload must not contain raw newlines.
func (t *stdioTransport) write(req rpcRequest) error {
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	if _, err := t.stdin.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("write to server failed: %w", err)
	}
	return nil
}

// Stderr returns the tail of the process's stderr, used in error messages.
func (t *stdioTransport) Stderr() string {
	if t.stderr == nil {
		return ""
	}
	return t.stderr.String()
}

func (t *stdioTransport) Close() error {
	t.fail(fmt.Errorf("transport closed"))
	if t.stdin != nil {
		t.stdin.Close()
	}
	if t.cmd != nil {
		t.cmd.Kill()
	}
	return nil
}

// tailBuffer keeps the last n bytes written to it, so a crashed server's
// stderr is available for the error message without unbounded growth.
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func newTailBuffer(max int) *tailBuffer {
	return &tailBuffer{max: max}
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	if len(b.buf) > b.max {
		b.buf = b.buf[len(b.buf)-b.max:]
	}
	return len(p), nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}
