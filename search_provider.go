package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// SearchResult is the normalized shape every search provider returns. The
// per-agent interception layers (Anthropic server-tool emulation, OpenAI
// Responses web_search_call, etc.) consume this and format it for the wire.
type SearchResult struct {
	Title      string   `json:"title"`
	URL        string   `json:"url"`
	Snippet    string   `json:"snippet,omitempty"`
	PageAge    string   `json:"pageAge,omitempty"`
	Score      float64  `json:"score,omitempty"`
	Highlights []string `json:"highlights,omitempty"`
}

// SearchQuery is the normalized request. NumResults defaults to 5, capped at 10.
type SearchQuery struct {
	Query          string
	AllowedDomains []string
	BlockedDomains []string
	NumResults     int
}

// SearchProvider is the pluggable backend interface. Adding a provider = one
// implementation + one entry in searchCatalog/searchRegistry.
type SearchProvider interface {
	ID() string
	DisplayName() string
	NeedsKey() bool   // true if an API key is required (false for managed/local)
	IsManaged() bool  // true for the bundled SearXNG instance Prism runs
	Search(ctx context.Context, q SearchQuery) ([]SearchResult, error)
	Ping(ctx context.Context) error
}

// SearchOutcome is what SearchRunner.Search returns: the results plus which
// provider actually answered (for stats/logs/transparency).
type SearchOutcome struct {
	Provider string
	Results  []SearchResult
}

// providerMeta is the static catalog entry surfaced to the admin UI.
type providerMeta struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	NeedsKey bool   `json:"needsKey"`
	IsManaged bool  `json:"isManaged"`
	EnvVar   string `json:"envVar,omitempty"`
	SignupURL string `json:"signupUrl,omitempty"`
}

// searchCatalog is the canonical list of built-in providers, in display order.
var searchCatalog = []providerMeta{
	{ID: "searxng", Name: "SearXNG (managed)", NeedsKey: false, IsManaged: true, SignupURL: "https://docs.searxng.org"},
	{ID: "exa", Name: "Exa", NeedsKey: true, EnvVar: "EXA_API_KEY", SignupURL: "https://exa.ai"},
	{ID: "tavily", Name: "Tavily", NeedsKey: true, EnvVar: "TAVILY_API_KEY", SignupURL: "https://tavily.com"},
	{ID: "brave", Name: "Brave", NeedsKey: true, EnvVar: "BRAVE_SEARCH_API_KEY", SignupURL: "https://brave.com/search/api/"},
	{ID: "serper", Name: "Serper", NeedsKey: true, EnvVar: "SERPER_API_KEY", SignupURL: "https://serper.dev"},
}

// searchRegistry maps a provider id to a constructor that builds a live
// SearchProvider from the resolved config (key + baseURL).
var searchRegistry = map[string]func(pc *SearchProviderConfig, client *http.Client) SearchProvider{
	"searxng": newSearXNGProvider,
	"exa":     newExaProvider,
	"tavily":  newTavilyProvider,
	"brave":   newBraveProvider,
	"serper":  newSerperProvider,
}

// defaultSearchConfig returns the out-of-the-box search config: managed SearXNG
// active (free, no key), popular paid providers present but disabled.
func defaultSearchConfig() *SearchConfig {
	return &SearchConfig{
		Active:            "searxng",
		Fallback:          []string{},
		MaxPerTurn:        5,
		TimeoutMs:         8000,
		DefaultNumResults: 5,
		Providers: map[string]*SearchProviderConfig{
			"searxng": {Enabled: true, BaseURL: "http://127.0.0.1:8888"},
			"exa":     {Enabled: false},
			"tavily":  {Enabled: false},
			"brave":   {Enabled: false},
			"serper":  {Enabled: false},
		},
	}
}

// mergeSearchConfig fills in any missing providers/defaults from an existing
// config (so new built-ins appear after an upgrade without resetting keys).
func mergeSearchConfig(c *SearchConfig) *SearchConfig {
	if c == nil {
		return defaultSearchConfig()
	}
	if c.Active == "" {
		c.Active = "searxng"
	}
	if c.MaxPerTurn == 0 {
		c.MaxPerTurn = 5
	}
	if c.TimeoutMs == 0 {
		c.TimeoutMs = 8000
	}
	if c.DefaultNumResults == 0 {
		c.DefaultNumResults = 5
	}
	if c.Fallback == nil {
		c.Fallback = []string{}
	}
	if c.Providers == nil {
		c.Providers = map[string]*SearchProviderConfig{}
	}
	for _, m := range searchCatalog {
		if _, ok := c.Providers[m.ID]; !ok {
			pc := &SearchProviderConfig{Enabled: false}
			if m.IsManaged {
				pc.Enabled = true
				pc.BaseURL = "http://127.0.0.1:8888"
			}
			c.Providers[m.ID] = pc
		}
	}
	return c
}

// SearchRunner holds the live search config and runs the active+fallback chain.
// It is reloadable: admin handlers call Reload after saving config so the proxy
// picks up provider/key changes without a restart.
type SearchRunner struct {
	mu     sync.RWMutex
	cfg    *SearchConfig
	client *http.Client
}

// globalSearchRunner is the singleton used by the interception layers.
var globalSearchRunner = newSearchRunner()

func newSearchRunner() *SearchRunner {
	r := &SearchRunner{cfg: defaultSearchConfig()}
	r.Reload(r.cfg)
	return r
}

// Reload swaps in a new config (defensively merged) and rebuilds the HTTP client.
func (r *SearchRunner) Reload(c *SearchConfig) {
	c = mergeSearchConfig(c)
	timeout := time.Duration(c.TimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	r.mu.Lock()
	r.cfg = c
	r.client = &http.Client{Timeout: timeout}
	r.mu.Unlock()
}

func (r *SearchRunner) config() *SearchConfig {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.cfg
}

func (r *SearchRunner) providerConfig(id string) *SearchProviderConfig {
	c := r.config()
	if c.Providers == nil {
		return nil
	}
	return c.Providers[id]
}

// resolveKey returns the API key for a provider: config value first, then the
// provider's conventional env var (so existing EXA_API_KEY etc. just work).
func (r *SearchRunner) resolveKey(id, envVar string) string {
	if pc := r.providerConfig(id); pc != nil && pc.APIKey != "" {
		return pc.APIKey
	}
	if envVar != "" {
		return os.Getenv(envVar)
	}
	return ""
}

func (r *SearchRunner) enabled(id string) bool {
	if pc := r.providerConfig(id); pc != nil {
		return pc.Enabled
	}
	return false
}

// hasKey reports whether a provider is usable: either no key needed, or a key
// is present in config/env. Used by the admin UI status badges and the runner.
func (r *SearchRunner) hasKey(id string) (has, fromEnv bool) {
	m := searchCatalogMeta(id)
	if m == nil || !m.NeedsKey {
		return true, false
	}
	if pc := r.providerConfig(id); pc != nil && pc.APIKey != "" {
		return true, false
	}
	if m.EnvVar != "" && os.Getenv(m.EnvVar) != "" {
		return true, true
	}
	return false, false
}

// build constructs a live provider instance for the given id from current config.
func (r *SearchRunner) build(id string) (SearchProvider, error) {
	ctor, ok := searchRegistry[id]
	if !ok {
		return nil, errUnknownSearchProvider(id)
	}
	r.mu.RLock()
	client := r.client
	r.mu.RUnlock()
	pc := r.providerConfig(id)
	if pc == nil {
		pc = &SearchProviderConfig{}
	}
	// Inject resolved key so providers don't each have to do env lookup.
	merged := *pc
	m := searchCatalogMeta(id)
	if m != nil && m.NeedsKey && merged.APIKey == "" && m.EnvVar != "" {
		merged.APIKey = os.Getenv(m.EnvVar)
	}
	return ctor(&merged, client), nil
}

// Search runs the active provider, then the fallback chain, skipping
// unconfigured/disabled providers. Returns the first success, or an error
// aggregating failures if all providers fail.
func (r *SearchRunner) Search(ctx context.Context, q SearchQuery) (*SearchOutcome, error) {
	if q.NumResults <= 0 {
		q.NumResults = r.config().DefaultNumResults
	}
	if q.NumResults > 10 {
		q.NumResults = 10
	}
	if q.Query == "" {
		return nil, errEmptySearchQuery()
	}

	c := r.config()
	chain := append([]string{c.Active}, c.Fallback...)
	seen := map[string]bool{}
	var errs []string
	for _, id := range chain {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		if !r.enabled(id) {
			continue
		}
		if m := searchCatalogMeta(id); m != nil && m.NeedsKey {
			if has, _ := r.hasKey(id); !has {
				errs = append(errs, id+": missing key")
				continue
			}
		}
		p, err := r.build(id)
		if err != nil {
			errs = append(errs, id+": "+err.Error())
			continue
		}
		res, err := p.Search(ctx, q)
		if err == nil && len(res) > 0 {
			return &SearchOutcome{Provider: id, Results: res}, nil
		}
		if err != nil {
			log.Printf("[search] provider %s failed: %v", id, err)
			errs = append(errs, id+": "+err.Error())
		} else {
			log.Printf("[search] provider %s returned 0 results", id)
			errs = append(errs, id+": 0 results")
		}
	}
	if len(errs) == 0 {
		return nil, errNoSearchProvider()
	}
	return nil, errSearchAllFailed(strings.Join(errs, "; "))
}

// searchCatalogMeta returns the catalog entry for an id, or nil.
func searchCatalogMeta(id string) *providerMeta {
	for i := range searchCatalog {
		if searchCatalog[i].ID == id {
			return &searchCatalog[i]
		}
	}
	return nil
}

// runSearch is the single entry point the interception layers call.
func runSearch(ctx context.Context, q SearchQuery) (*SearchOutcome, error) {
	return globalSearchRunner.Search(ctx, q)
}

// ── error helpers ──

type searchError struct{ msg string }

func (e *searchError) Error() string { return e.msg }

func errUnknownSearchProvider(id string) error    { return &searchError{"unknown search provider: " + id} }
func errEmptySearchQuery() error                  { return &searchError{"empty search query"} }
func errNoSearchProvider() error                  { return &searchError{"no search provider configured"} }
func errSearchAllFailed(detail string) error      { return &searchError{"all search providers failed: " + detail} }

// ── admin JSON shapes (keys never echoed) ──

// adminSearchProviderState is the per-provider view returned to the UI. APIKey
// is omitted; hasKey/keyFromEnv report status instead.
type adminSearchProviderState struct {
	Enabled    bool   `json:"enabled"`
	BaseURL    string `json:"baseURL,omitempty"`
	HasKey     bool   `json:"hasKey"`
	KeyFromEnv bool   `json:"keyFromEnv,omitempty"`
}

// adminSearchProviderInput is the per-provider block the UI sends on PUT.
// APIKey is write-only: empty string = keep existing key.
type adminSearchProviderInput struct {
	Enabled bool   `json:"enabled"`
	BaseURL string `json:"baseURL,omitempty"`
	APIKey  string `json:"apiKey,omitempty"`
}

type adminSearchConfigInput struct {
	Active            string                              `json:"active"`
	Fallback          []string                            `json:"fallback"`
	MaxPerTurn        int                                 `json:"maxPerTurn"`
	TimeoutMs         int                                 `json:"timeoutMs"`
	DefaultNumResults int                                 `json:"defaultNumResults"`
	Providers         map[string]adminSearchProviderInput `json:"providers"`
}

type adminSearchConfig struct {
	Active            string                              `json:"active"`
	Fallback          []string                            `json:"fallback"`
	MaxPerTurn        int                                 `json:"maxPerTurn"`
	TimeoutMs         int                                 `json:"timeoutMs"`
	DefaultNumResults int                                 `json:"defaultNumResults"`
	Providers         map[string]adminSearchProviderState `json:"providers"`
}

// adminSearchConfigView builds the masked view of the current search config.
func adminSearchConfigView(c *SearchConfig) adminSearchConfig {
	c = mergeSearchConfig(c)
	out := adminSearchConfig{
		Active:            c.Active,
		Fallback:          c.Fallback,
		MaxPerTurn:        c.MaxPerTurn,
		TimeoutMs:         c.TimeoutMs,
		DefaultNumResults: c.DefaultNumResults,
		Providers:         map[string]adminSearchProviderState{},
	}
	for _, m := range searchCatalog {
		pc := c.Providers[m.ID]
		st := adminSearchProviderState{Enabled: pc != nil && pc.Enabled}
		if pc != nil {
			st.BaseURL = pc.BaseURL
		}
		if m.NeedsKey {
			has := pc != nil && pc.APIKey != ""
			env := false
			if !has && m.EnvVar != "" && os.Getenv(m.EnvVar) != "" {
				has, env = true, true
			}
			st.HasKey = has
			st.KeyFromEnv = env
		} else {
			st.HasKey = true
		}
		out.Providers[m.ID] = st
	}
	return out
}

// encodeJSON is a tiny helper used by the admin handlers.
func encodeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}