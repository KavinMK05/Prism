package main

import (
	"context"
	"net/http"
	"testing"
)

// fakeProvider is a test SearchProvider injected via a temp registry swap.
type fakeProvider struct {
	id      string
	needsKey bool
	results []SearchResult
	err     error
}

func (p *fakeProvider) ID() string          { return p.id }
func (p *fakeProvider) DisplayName() string { return p.id }
func (p *fakeProvider) NeedsKey() bool      { return p.needsKey }
func (p *fakeProvider) IsManaged() bool     { return false }
func (p *fakeProvider) Search(ctx context.Context, q SearchQuery) ([]SearchResult, error) {
	return p.results, p.err
}
func (p *fakeProvider) Ping(ctx context.Context) error { return nil }

func TestMergeSearchConfigFillsDefaults(t *testing.T) {
	c := &SearchConfig{Active: "exa"} // minimal, missing providers/limits
	got := mergeSearchConfig(c)
	if got.MaxPerTurn != 5 {
		t.Errorf("MaxPerTurn = %d, want 5", got.MaxPerTurn)
	}
	if got.TimeoutMs != 8000 {
		t.Errorf("TimeoutMs = %d, want 8000", got.TimeoutMs)
	}
	if got.Providers["searxng"] == nil || !got.Providers["searxng"].Enabled {
		t.Errorf("searxng should be enabled by default")
	}
	if got.Providers["exa"] == nil {
		t.Errorf("exa provider should exist after merge")
	}
}

func TestSearchRunnerFallbackChain(t *testing.T) {
	// Save and swap the registry so we can inject fakes.
	orig := searchRegistry
	defer func() { searchRegistry = orig }()

	searchRegistry = map[string]func(pc *SearchProviderConfig, client *http.Client) SearchProvider{
		"a": func(_ *SearchProviderConfig, _ *http.Client) SearchProvider {
			return &fakeProvider{id: "a", results: nil, err: errSearchAllFailed("a down")}
		},
		"b": func(_ *SearchProviderConfig, _ *http.Client) SearchProvider {
			return &fakeProvider{id: "b", results: []SearchResult{{Title: "B1", URL: "https://b.example/1"}}}
		},
	}

	r := &SearchRunner{}
	r.Reload(&SearchConfig{
		Active:            "a",
		Fallback:          []string{"b"},
		MaxPerTurn:        5,
		TimeoutMs:         1000,
		DefaultNumResults: 3,
		Providers: map[string]*SearchProviderConfig{
			"a": {Enabled: true},
			"b": {Enabled: true},
		},
	})

	out, err := r.Search(context.Background(), SearchQuery{Query: "hello"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Provider != "b" {
		t.Errorf("expected fallback to provider b, got %s", out.Provider)
	}
	if len(out.Results) != 1 || out.Results[0].URL != "https://b.example/1" {
		t.Errorf("unexpected results: %+v", out.Results)
	}
}

func TestSearchRunnerSkipsUnconfiguredKeyProviders(t *testing.T) {
	orig := searchRegistry
	defer func() { searchRegistry = orig }()

	called := false
	searchRegistry = map[string]func(pc *SearchProviderConfig, client *http.Client) SearchProvider{
		"needskey": func(_ *SearchProviderConfig, _ *http.Client) SearchProvider {
			called = true
			return &fakeProvider{id: "needskey", needsKey: true, results: []SearchResult{{URL: "https://x"}}}
		},
		"free": func(_ *SearchProviderConfig, _ *http.Client) SearchProvider {
			return &fakeProvider{id: "free", results: []SearchResult{{Title: "Free", URL: "https://free.example"}}}
		},
	}

	// Temporarily mark "needskey" as a real catalog entry so hasKey() checks it.
	origCat := searchCatalog
	searchCatalog = append([]providerMeta{
		{ID: "needskey", Name: "NeedsKey", NeedsKey: true, EnvVar: "FAKE_KEY_XYZ_NEVER_SET"},
		{ID: "free", Name: "Free", NeedsKey: false},
	}, searchCatalog...)
	defer func() { searchCatalog = origCat }()

	r := &SearchRunner{}
	r.Reload(&SearchConfig{
		Active:            "needskey",
		Fallback:          []string{"free"},
		DefaultNumResults: 2,
		Providers: map[string]*SearchProviderConfig{
			"needskey": {Enabled: true}, // no key, no env
			"free":     {Enabled: true},
		},
	})

	out, err := r.Search(context.Background(), SearchQuery{Query: "hello"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if called {
		t.Errorf("needskey provider should have been skipped (no key), but was called")
	}
	if out.Provider != "free" {
		t.Errorf("expected free provider to answer, got %s", out.Provider)
	}
}

func TestAdminSearchConfigViewMasksKeys(t *testing.T) {
	c := &SearchConfig{
		Active:            "searxng",
		MaxPerTurn:        5,
		TimeoutMs:         8000,
		DefaultNumResults: 5,
		Providers: map[string]*SearchProviderConfig{
			"searxng": {Enabled: true, BaseURL: "http://127.0.0.1:8888"},
			"exa":     {Enabled: true, APIKey: "super-secret-key"},
		},
	}
	view := adminSearchConfigView(c)
	if view.Providers["exa"].HasKey != true {
		t.Errorf("exa should report hasKey=true")
	}
	// The view type has no APIKey field at all (compile-time guarantee), but
	// ensure the masked state doesn't carry the secret anywhere observable.
	if view.Providers["searxng"].BaseURL != "http://127.0.0.1:8888" {
		t.Errorf("searxng baseURL should be echoed")
	}
}