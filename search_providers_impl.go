package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// ── SearXNG (managed local instance) ────────────────────────────────────────

type searxngSearchProvider struct {
	baseURL string
	client  *http.Client
}

func newSearXNGProvider(pc *SearchProviderConfig, client *http.Client) SearchProvider {
	base := strings.TrimRight(pc.BaseURL, "/")
	if base == "" {
		base = "http://127.0.0.1:8888"
	}
	return &searxngSearchProvider{baseURL: base, client: client}
}

func (p *searxngSearchProvider) ID() string         { return "searxng" }
func (p *searxngSearchProvider) DisplayName() string { return "SearXNG (managed)" }
func (p *searxngSearchProvider) NeedsKey() bool      { return false }
func (p *searxngSearchProvider) IsManaged() bool     { return true }

func (p *searxngSearchProvider) Search(ctx context.Context, q SearchQuery) ([]SearchResult, error) {
	v := url.Values{}
	v.Set("q", q.Query)
	v.Set("format", "json")
	v.Set("pageno", "1")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/search?"+v.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("searxng: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("searxng: HTTP %d: %s", resp.StatusCode, string(b))
	}
	var raw struct {
		Results []struct {
			URL          string `json:"url"`
			Title        string `json:"title"`
			Content      string `json:"content"`
			PublishedDate string `json:"publishedDate"`
			Score        float64 `json:"score"`
			Engine       string `json:"engine"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("searxng: decode: %w", err)
	}
	out := make([]SearchResult, 0, len(raw.Results))
	for _, r := range raw.Results {
		if r.URL == "" {
			continue
		}
		out = append(out, SearchResult{
			Title:    r.Title,
			URL:      r.URL,
			Snippet:  r.Content,
			PageAge:  r.PublishedDate,
			Score:    r.Score,
		})
	}
	return capResults(out, q.NumResults), nil
}

func (p *searxngSearchProvider) Ping(ctx context.Context) error {
	v := url.Values{}
	v.Set("q", "test")
	v.Set("format", "json")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/search?"+v.Encode(), nil)
	if err != nil {
		return err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("searxng not reachable: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("searxng: HTTP %d", resp.StatusCode)
	}
	return nil
}

// ── Exa ─────────────────────────────────────────────────────────────────────

type exaSearchProvider struct {
	apiKey string
	client *http.Client
}

func newExaProvider(pc *SearchProviderConfig, client *http.Client) SearchProvider {
	return &exaSearchProvider{apiKey: pc.APIKey, client: client}
}

func (p *exaSearchProvider) ID() string         { return "exa" }
func (p *exaSearchProvider) DisplayName() string { return "Exa" }
func (p *exaSearchProvider) NeedsKey() bool      { return true }
func (p *exaSearchProvider) IsManaged() bool     { return false }

func (p *exaSearchProvider) Search(ctx context.Context, q SearchQuery) ([]SearchResult, error) {
	if p.apiKey == "" {
		return nil, fmt.Errorf("exa: EXA_API_KEY is not set")
	}
	body := map[string]interface{}{
		"query":       q.Query,
		"numResults":  q.NumResults,
		"type":        "auto",
		"contents":    map[string]interface{}{"highlights": true},
	}
	if len(q.AllowedDomains) > 0 {
		body["includeDomains"] = q.AllowedDomains
	}
	if len(q.BlockedDomains) > 0 {
		body["excludeDomains"] = q.BlockedDomains
	}
	var raw struct {
		Results []struct {
			Title         string   `json:"title"`
			URL           string   `json:"url"`
			PublishedDate string   `json:"publishedDate"`
			Highlights    []string `json:"highlights"`
			Score         float64  `json:"score"`
		} `json:"results"`
	}
	if err := postJSON(ctx, p.client, "https://api.exa.ai/search", "x-api-key", p.apiKey, body, &raw); err != nil {
		return nil, err
	}
	out := make([]SearchResult, 0, len(raw.Results))
	for _, r := range raw.Results {
		out = append(out, SearchResult{
			Title:      r.Title,
			URL:        r.URL,
			PageAge:    r.PublishedDate,
			Highlights: r.Highlights,
			Score:      r.Score,
			Snippet:    joinStrings(r.Highlights),
		})
	}
	return out, nil
}

func (p *exaSearchProvider) Ping(ctx context.Context) error {
	_, err := p.Search(ctx, SearchQuery{Query: "test", NumResults: 1})
	return err
}

// ── Tavily ──────────────────────────────────────────────────────────────────

type tavilySearchProvider struct {
	apiKey string
	client *http.Client
}

func newTavilyProvider(pc *SearchProviderConfig, client *http.Client) SearchProvider {
	return &tavilySearchProvider{apiKey: pc.APIKey, client: client}
}

func (p *tavilySearchProvider) ID() string         { return "tavily" }
func (p *tavilySearchProvider) DisplayName() string { return "Tavily" }
func (p *tavilySearchProvider) NeedsKey() bool      { return true }
func (p *tavilySearchProvider) IsManaged() bool     { return false }

func (p *tavilySearchProvider) Search(ctx context.Context, q SearchQuery) ([]SearchResult, error) {
	if p.apiKey == "" {
		return nil, fmt.Errorf("tavily: TAVILY_API_KEY is not set")
	}
	body := map[string]interface{}{
		"query":          q.Query,
		"max_results":    q.NumResults,
		"search_depth":   "basic",
		"include_answer": false,
		"topic":          "general",
	}
	if len(q.AllowedDomains) > 0 {
		body["include_domains"] = q.AllowedDomains
	}
	if len(q.BlockedDomains) > 0 {
		body["exclude_domains"] = q.BlockedDomains
	}
	var raw struct {
		Results []struct {
			Title   string  `json:"title"`
			URL     string  `json:"url"`
			Content string  `json:"content"`
			Score   float64 `json:"score"`
		} `json:"results"`
	}
	if err := postJSON(ctx, p.client, "https://api.tavily.com/search", "Authorization", "Bearer "+p.apiKey, body, &raw); err != nil {
		return nil, err
	}
	out := make([]SearchResult, 0, len(raw.Results))
	for _, r := range raw.Results {
		out = append(out, SearchResult{Title: r.Title, URL: r.URL, Snippet: r.Content, Score: r.Score})
	}
	return out, nil
}

func (p *tavilySearchProvider) Ping(ctx context.Context) error {
	_, err := p.Search(ctx, SearchQuery{Query: "test", NumResults: 1})
	return err
}

// ── Brave ───────────────────────────────────────────────────────────────────

type braveSearchProvider struct {
	apiKey string
	client *http.Client
}

func newBraveProvider(pc *SearchProviderConfig, client *http.Client) SearchProvider {
	return &braveSearchProvider{apiKey: pc.APIKey, client: client}
}

func (p *braveSearchProvider) ID() string         { return "brave" }
func (p *braveSearchProvider) DisplayName() string { return "Brave" }
func (p *braveSearchProvider) NeedsKey() bool      { return true }
func (p *braveSearchProvider) IsManaged() bool     { return false }

func (p *braveSearchProvider) Search(ctx context.Context, q SearchQuery) ([]SearchResult, error) {
	if p.apiKey == "" {
		return nil, fmt.Errorf("brave: BRAVE_SEARCH_API_KEY is not set")
	}
	v := url.Values{}
	v.Set("q", q.Query)
	v.Set("count", strconv.Itoa(q.NumResults))
	v.Set("country", "us")
	v.Set("search_lang", "en")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.search.brave.com/res/v1/web/search?"+v.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Subscription-Token", p.apiKey)
	req.Header.Set("Accept", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("brave: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("brave: HTTP %d: %s", resp.StatusCode, string(b))
	}
	var raw struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
				Age         string `json:"age"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("brave: decode: %w", err)
	}
	out := make([]SearchResult, 0, len(raw.Web.Results))
	for _, r := range raw.Web.Results {
		out = append(out, SearchResult{Title: r.Title, URL: r.URL, Snippet: r.Description, PageAge: r.Age})
	}
	return out, nil
}

func (p *braveSearchProvider) Ping(ctx context.Context) error {
	_, err := p.Search(ctx, SearchQuery{Query: "test", NumResults: 1})
	return err
}

// ── Serper ──────────────────────────────────────────────────────────────────

type serperSearchProvider struct {
	apiKey string
	client *http.Client
}

func newSerperProvider(pc *SearchProviderConfig, client *http.Client) SearchProvider {
	return &serperSearchProvider{apiKey: pc.APIKey, client: client}
}

func (p *serperSearchProvider) ID() string         { return "serper" }
func (p *serperSearchProvider) DisplayName() string { return "Serper" }
func (p *serperSearchProvider) NeedsKey() bool      { return true }
func (p *serperSearchProvider) IsManaged() bool     { return false }

func (p *serperSearchProvider) Search(ctx context.Context, q SearchQuery) ([]SearchResult, error) {
	if p.apiKey == "" {
		return nil, fmt.Errorf("serper: SERPER_API_KEY is not set")
	}
	body := map[string]interface{}{
		"q":   q.Query,
		"num": q.NumResults,
		"gl":  "us",
		"hl":  "en",
	}
	var raw struct {
		Organic []struct {
			Title    string  `json:"title"`
			Link     string  `json:"link"`
			Snippet  string  `json:"snippet"`
			Date     string  `json:"date"`
			Position float64 `json:"position"`
		} `json:"organic"`
	}
	if err := postJSON(ctx, p.client, "https://google.serper.dev/search", "X-API-KEY", p.apiKey, body, &raw); err != nil {
		return nil, err
	}
	out := make([]SearchResult, 0, len(raw.Organic))
	for _, r := range raw.Organic {
		score := 0.0
		if r.Position > 0 {
			score = 1.0 / r.Position
		}
		out = append(out, SearchResult{Title: r.Title, URL: r.Link, Snippet: r.Snippet, PageAge: r.Date, Score: score})
	}
	return out, nil
}

func (p *serperSearchProvider) Ping(ctx context.Context) error {
	_, err := p.Search(ctx, SearchQuery{Query: "test", NumResults: 1})
	return err
}

// ── shared helpers ──────────────────────────────────────────────────────────

// postJSON sends a POST with a single auth header (headerName="Authorization",
// headerValue="Bearer <key>") or a custom header (e.g. "x-api-key"). It decodes
// the response into target.
func postJSON(ctx context.Context, client *http.Client, urlStr, authHeader, authValue string, body interface{}, target interface{}) error {
	buf, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, urlStr, strings.NewReader(string(buf)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if authHeader != "" && authValue != "" {
		req.Header.Set(authHeader, authValue)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", hostOf(urlStr), err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("%s: HTTP %d: %s", hostOf(urlStr), resp.StatusCode, string(b))
	}
	if err := json.NewDecoder(resp.Body).Decode(target); err != nil {
		return fmt.Errorf("%s: decode: %w", hostOf(urlStr), err)
	}
	return nil
}

func hostOf(urlStr string) string {
	if u, err := url.Parse(urlStr); err == nil {
		return u.Host
	}
	return urlStr
}

func capResults(r []SearchResult, n int) []SearchResult {
	if n > 0 && len(r) > n {
		return r[:n]
	}
	return r
}