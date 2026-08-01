package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// customSearchProvider is the declarative, no-code search backend (design doc
// §4). It renders a templated HTTP request from a CustomSearchProviderConfig,
// sends it with the resolved key, walks the response via ResultsJSONPath, and
// maps each item through FieldMap into normalized SearchResults.
type customSearchProvider struct {
	cfg    *CustomSearchProviderConfig
	client *http.Client
}

// needsKey reports whether the provider is declared to require an API key (it
// defines an auth header). Used by the runner to skip unconfigured providers.
func (cp *CustomSearchProviderConfig) needsKey() bool {
	return cp != nil && cp.AuthHeader != ""
}

func newCustomSearchProvider(cp *CustomSearchProviderConfig, client *http.Client) SearchProvider {
	return &customSearchProvider{cfg: cp, client: client}
}

func (p *customSearchProvider) ID() string          { return p.cfg.ID }
func (p *customSearchProvider) DisplayName() string { return p.cfg.Name }
func (p *customSearchProvider) IsManaged() bool     { return false }

func (p *customSearchProvider) NeedsKey() bool {
	return p.cfg.needsKey()
}

// resolvedKey returns the API key: config value first, then the KeyEnv env var.
func (p *customSearchProvider) resolvedKey() string {
	if p.cfg.APIKey != "" {
		return p.cfg.APIKey
	}
	if p.cfg.KeyEnv != "" {
		return os.Getenv(p.cfg.KeyEnv)
	}
	return ""
}

// method returns the HTTP verb, defaulting to POST.
func (p *customSearchProvider) method() string {
	m := strings.ToUpper(p.cfg.Method)
	if m == "GET" {
		return http.MethodGet
	}
	return http.MethodPost
}

func (p *customSearchProvider) Search(ctx context.Context, q SearchQuery) ([]SearchResult, error) {
	if p.cfg == nil || p.cfg.Endpoint == "" {
		return nil, fmt.Errorf("custom provider: endpoint is not configured")
	}
	tpl := map[string]string{
		"query":          q.Query,
		"numResults":     strconv.Itoa(q.NumResults),
		"allowedDomains": strings.Join(q.AllowedDomains, ","),
		"blockedDomains": strings.Join(q.BlockedDomains, ","),
	}

	if p.method() == http.MethodGet {
		uv := url.Values{}
		if p.cfg.QueryParam != "" {
			uv.Set(p.cfg.QueryParam, q.Query)
		} else {
			uv.Set("q", q.Query)
		}
		for k, v := range p.cfg.Params {
			uv.Set(k, substitute(v, tpl))
		}
		if q.NumResults > 0 {
			uv.Set("num", strconv.Itoa(q.NumResults))
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.cfg.Endpoint+"?"+uv.Encode(), nil)
		if err != nil {
			return nil, fmt.Errorf("custom provider: %w", err)
		}
		p.applyAuth(req)
		resp, err := p.client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("custom provider: %w", err)
		}
		defer resp.Body.Close()
		return p.decode(resp, q.NumResults)
	}

	bodyMap := map[string]interface{}{}
	for k, v := range p.cfg.Body {
		bodyMap[k] = substituteValue(v, tpl)
	}
	buf, err := json.Marshal(bodyMap)
	if err != nil {
		return nil, fmt.Errorf("custom provider: marshal body: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.Endpoint, strings.NewReader(string(buf)))
	if err != nil {
		return nil, fmt.Errorf("custom provider: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	p.applyAuth(req)
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("custom provider: %w", err)
	}
	defer resp.Body.Close()
	return p.decode(resp, q.NumResults)
}

// applyAuth sets the Authorization-style header declared in AuthHeader. It is
// parsed as "HeaderName Prefix" (e.g. "Authorization: Bearer") or just a header
// name (e.g. "X-API-KEY" → value = key). No auth header configured = no header.
func (p *customSearchProvider) applyAuth(req *http.Request) {
	ah := strings.TrimSpace(p.cfg.AuthHeader)
	if ah == "" {
		return
	}
	parts := strings.SplitN(ah, " ", 2)
	name := parts[0]
	val := p.resolvedKey()
	if len(parts) == 2 && val != "" {
		val = parts[1] + " " + val
	}
	req.Header.Set(name, val)
}

func (p *customSearchProvider) decode(resp *http.Response, numResults int) ([]SearchResult, error) {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("custom provider: read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("custom provider: HTTP %d: %s", resp.StatusCode, truncate(string(body), 500))
	}
	var root interface{}
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, fmt.Errorf("custom provider: decode: %w", err)
	}
	items, ok := getPath(root, p.cfg.ResultsJSONPath)
	if !ok {
		// Fall back to the root if no results path is configured.
		if p.cfg.ResultsJSONPath == "" {
			items = root
		} else {
			return nil, fmt.Errorf("custom provider: results path %q not found", p.cfg.ResultsJSONPath)
		}
	}
	arr, ok := items.([]interface{})
	if !ok {
		return nil, fmt.Errorf("custom provider: results path %q is not an array", p.cfg.ResultsJSONPath)
	}
	out := make([]SearchResult, 0, len(arr))
	for _, it := range arr {
		res, ok := p.mapItem(it)
		if !ok || res.URL == "" {
			continue
		}
		out = append(out, res)
	}
	return capResults(out, numResults), nil
}

// mapItem projects one response item through FieldMap into a SearchResult.
// FieldMap keys are SearchResult field names; values are dot paths into the item.
func (p *customSearchProvider) mapItem(item interface{}) (SearchResult, bool) {
	var res SearchResult
	field := func(name string) (interface{}, bool) {
		path, ok := p.cfg.FieldMap[name]
		if !ok || path == "" {
			return nil, false
		}
		return getPath(item, path)
	}
	if v, ok := field("title"); ok {
		res.Title = asString(v)
	}
	if v, ok := field("url"); ok {
		res.URL = asString(v)
	}
	if v, ok := field("snippet"); ok {
		res.Snippet = asString(v)
	}
	if v, ok := field("pageAge"); ok {
		res.PageAge = asString(v)
	}
	if v, ok := field("score"); ok {
		res.Score, _ = toFloat(v)
	}
	if v, ok := field("highlights"); ok {
		if arr, ok := v.([]interface{}); ok {
			for _, e := range arr {
				if s := asString(e); s != "" {
					res.Highlights = append(res.Highlights, s)
				}
			}
			if res.Snippet == "" {
				res.Snippet = joinStrings(res.Highlights)
			}
		} else if s := asString(v); s != "" {
			res.Highlights = []string{s}
			if res.Snippet == "" {
				res.Snippet = s
			}
		}
	}
	return res, res.URL != "" || res.Title != ""
}

func (p *customSearchProvider) Ping(ctx context.Context) error {
	_, err := p.Search(ctx, SearchQuery{Query: "test", NumResults: 1})
	return err
}

// substitute replaces {{key}} placeholders in s from the template map.
func substitute(s string, tpl map[string]string) string {
	for k, v := range tpl {
		s = strings.ReplaceAll(s, "{{"+k+"}}", v)
	}
	return s
}

// substituteValue recursively substitutes {{key}} placeholders into string
// leaves of a body value, so nested body templates work too.
func substituteValue(v interface{}, tpl map[string]string) interface{} {
	switch t := v.(type) {
	case string:
		return substitute(t, tpl)
	case map[string]interface{}:
		out := make(map[string]interface{}, len(t))
		for k, sub := range t {
			out[k] = substituteValue(sub, tpl)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(t))
		for i, sub := range t {
			out[i] = substituteValue(sub, tpl)
		}
		return out
	default:
		return v
	}
}

// getPath walks a dot path (e.g. "web.results", "data.0.title") through decoded
// JSON. Empty path returns the root. Returns ok=false when any segment is missing.
func getPath(v interface{}, path string) (interface{}, bool) {
	path = strings.TrimSpace(path)
	if path == "" {
		return v, true
	}
	cur := v
	for _, seg := range strings.Split(path, ".") {
		switch node := cur.(type) {
		case map[string]interface{}:
			next, ok := node[seg]
			if !ok {
				return nil, false
			}
			cur = next
		case []interface{}:
			idx, err := strconv.Atoi(seg)
			if err != nil || idx < 0 || idx >= len(node) {
				return nil, false
			}
			cur = node[idx]
		default:
			return nil, false
		}
	}
	return cur, true
}

func asString(v interface{}) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case json.Number:
		return t.String()
	case bool:
		return strconv.FormatBool(t)
	default:
		return ""
	}
}

func toFloat(v interface{}) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case json.Number:
		f, err := t.Float64()
		return f, err == nil
	case int:
		return float64(t), true
	case string:
		f, err := strconv.ParseFloat(t, 64)
		return f, err == nil
	default:
		return 0, false
	}
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

// mergeCustomProviderInput converts the admin-submitted custom provider list
// into config storage, preserving stored API keys for unchanged providers
// (empty apiKey = keep existing) and validating ids/endpoints.
func mergeCustomProviderInput(existing []*CustomSearchProviderConfig, incoming []adminCustomProviderInput) ([]*CustomSearchProviderConfig, error) {
	byID := map[string]*CustomSearchProviderConfig{}
	for _, cp := range existing {
		if cp != nil {
			byID[cp.ID] = cp
		}
	}
	seen := map[string]bool{}
	var out []*CustomSearchProviderConfig
	for _, in := range incoming {
		id := strings.TrimSpace(in.ID)
		if id == "" {
			return nil, fmt.Errorf("invalid custom provider: id is required")
		}
		if searchCatalogMeta(id) != nil {
			return nil, fmt.Errorf("invalid custom provider: id %q collides with a built-in provider", id)
		}
		if seen[id] {
			return nil, fmt.Errorf("invalid custom provider: duplicate id %q", id)
		}
		seen[id] = true
		cp := &CustomSearchProviderConfig{
			ID:              id,
			Name:            strings.TrimSpace(in.Name),
			Endpoint:        strings.TrimSpace(in.Endpoint),
			Method:          strings.TrimSpace(in.Method),
			AuthHeader:      strings.TrimSpace(in.AuthHeader),
			KeyEnv:          strings.TrimSpace(in.KeyEnv),
			QueryParam:      strings.TrimSpace(in.QueryParam),
			Body:            in.Body,
			Params:          in.Params,
			ResultsJSONPath: strings.TrimSpace(in.ResultsJSONPath),
			FieldMap:        in.FieldMap,
			Enabled:         in.Enabled,
		}
		if cp.Endpoint == "" {
			return nil, fmt.Errorf("invalid custom provider: endpoint is required for %q", id)
		}
		if in.APIKey != "" {
			cp.APIKey = in.APIKey
		} else if prev := byID[id]; prev != nil {
			cp.APIKey = prev.APIKey
		}
		out = append(out, cp)
	}
	return out, nil
}

var _ SearchProvider = (*customSearchProvider)(nil)
