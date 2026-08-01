package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCustomSearchProviderPOST(t *testing.T) {
	var gotMethod, gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotAuth = r.Header.Get("X-API-KEY")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"results":[{"title":"One","link":"https://one.example","content":"first"},{"title":"Two","link":"https://two.example","content":"second"}]}`)
	}))
	defer srv.Close()

	p := newCustomSearchProvider(&CustomSearchProviderConfig{
		ID:              "linkup",
		Name:            "Linkup",
		Endpoint:        srv.URL,
		Method:          "POST",
		AuthHeader:      "X-API-KEY",
		APIKey:          "sekret",
		Body:            map[string]interface{}{"query": "{{query}}", "num": "{{numResults}}"},
		ResultsJSONPath: "results",
		FieldMap:        map[string]string{"title": "title", "url": "link", "snippet": "content"},
	}, http.DefaultClient)

	res, err := p.Search(context.Background(), SearchQuery{Query: "hello world", NumResults: 2})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s, want POST", gotMethod)
	}
	if gotAuth != "sekret" {
		t.Errorf("auth = %q, want sekret", gotAuth)
	}
	var body map[string]interface{}
	if err := json.Unmarshal([]byte(gotBody), &body); err != nil {
		t.Fatalf("bad body %q: %v", gotBody, err)
	}
	if body["query"] != "hello world" {
		t.Errorf("body query = %v, want substituted hello world", body["query"])
	}
	if body["num"] != "2" {
		t.Errorf("body num = %v, want 2", body["num"])
	}
	if len(res) != 2 || res[0].URL != "https://one.example" || res[0].Snippet != "first" {
		t.Errorf("unexpected results: %+v", res)
	}
}

func TestCustomSearchProviderGET(t *testing.T) {
	var gotRaw string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRaw = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"web":{"results":[{"title":"Brave-ish","url":"https://b.example","description":"desc"}]}}`)
	}))
	defer srv.Close()

	p := newCustomSearchProvider(&CustomSearchProviderConfig{
		ID:              "mysvc",
		Name:            "My Service",
		Endpoint:        srv.URL,
		Method:          "GET",
		QueryParam:      "q",
		Params:          map[string]string{"lang": "en", "count": "{{numResults}}"},
		ResultsJSONPath: "web.results",
		FieldMap:        map[string]string{"title": "title", "url": "url", "snippet": "description"},
	}, http.DefaultClient)

	res, err := p.Search(context.Background(), SearchQuery{Query: "cat videos", NumResults: 3})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	for _, want := range []string{"q=cat+videos", "lang=en", "count=3"} {
		if !strings.Contains(gotRaw, want) {
			t.Errorf("raw query %q missing %q", gotRaw, want)
		}
	}
	if len(res) != 1 || res[0].Title != "Brave-ish" {
		t.Errorf("unexpected results: %+v", res)
	}
}

func TestCustomSearchProviderNestedArrayPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"title":"A","url":"https://a.example"}]}`)
	}))
	defer srv.Close()

	p := newCustomSearchProvider(&CustomSearchProviderConfig{
		ID:              "x",
		Name:            "X",
		Endpoint:        srv.URL,
		Method:          "POST",
		ResultsJSONPath: "data",
		FieldMap:        map[string]string{"title": "title", "url": "url"},
	}, http.DefaultClient)

	res, err := p.Search(context.Background(), SearchQuery{Query: "q", NumResults: 5})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(res) != 1 || res[0].URL != "https://a.example" {
		t.Errorf("unexpected results: %+v", res)
	}
}

func TestCustomSearchProviderKeyFromEnv(t *testing.T) {
	t.Setenv("MY_SEARCH_KEY", "env-secret")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer env-secret" {
			t.Errorf("auth = %q, want Bearer env-secret", got)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"results":[]}`)
	}))
	defer srv.Close()

	p := newCustomSearchProvider(&CustomSearchProviderConfig{
		ID:              "x",
		Name:            "X",
		Endpoint:        srv.URL,
		Method:          "POST",
		AuthHeader:      "Authorization Bearer",
		KeyEnv:          "MY_SEARCH_KEY",
		ResultsJSONPath: "results",
		FieldMap:        map[string]string{"title": "title", "url": "url"},
	}, http.DefaultClient)

	if _, err := p.Search(context.Background(), SearchQuery{Query: "q", NumResults: 1}); err != nil {
		t.Fatalf("Search failed: %v", err)
	}
}

func TestCustomProviderRunnerFallback(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"results":[{"title":"R1","url":"https://r.example/1"}]}`)
	}))
	defer ok.Close()

	r := &SearchRunner{}
	r.Reload(&SearchConfig{
		Active:    "linkup",
		Fallback:  []string{"other"},
		MaxPerTurn: 5,
		TimeoutMs:  8000,
		DefaultNumResults: 3,
		CustomProviders: []*CustomSearchProviderConfig{
			{
				ID: "linkup", Name: "Linkup", Endpoint: ok.URL, Method: "POST",
				Enabled: true, ResultsJSONPath: "results",
				FieldMap: map[string]string{"title": "title", "url": "url"},
			},
			{
				ID: "other", Name: "Other", Endpoint: "http://127.0.0.1:1", Method: "POST",
				Enabled: false, ResultsJSONPath: "results",
				FieldMap: map[string]string{"title": "title", "url": "url"},
			},
		},
	})

	out, err := r.Search(context.Background(), SearchQuery{Query: "hello"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Provider != "linkup" {
		t.Errorf("expected provider linkup, got %s", out.Provider)
	}
	if len(out.Results) != 1 || out.Results[0].URL != "https://r.example/1" {
		t.Errorf("unexpected results: %+v", out.Results)
	}
}

func TestAdminSearchConfigViewCustomMasksKeys(t *testing.T) {
	c := &SearchConfig{
		Active: "searxng",
		CustomProviders: []*CustomSearchProviderConfig{
			{
				ID: "linkup", Name: "Linkup", Endpoint: "https://api.example/search",
				AuthHeader: "X-API-KEY", APIKey: "super-secret", Enabled: true,
				ResultsJSONPath: "results", FieldMap: map[string]string{"title": "title", "url": "url"},
			},
			{
				ID: "public", Name: "Public", Endpoint: "https://pub.example/search",
				Enabled: true, ResultsJSONPath: "results",
				FieldMap: map[string]string{"title": "title", "url": "url"},
			},
		},
	}
	view := adminSearchConfigView(c)
	if len(view.CustomProviders) != 2 {
		t.Fatalf("expected 2 custom providers, got %d", len(view.CustomProviders))
	}
	linkup := view.CustomProviders[0]
	if !linkup.HasKey || linkup.KeyFromEnv {
		t.Errorf("linkup should report hasKey=true, keyFromEnv=false: %+v", linkup)
	}
	// The admin view type has no APIKey field (compile-time guarantee keys never echo).
	if _, has := any(linkup).(interface{ APIKey() string }); has {
		t.Error("admin view must not expose APIKey")
	}
	if !view.CustomProviders[1].HasKey {
		t.Errorf("public provider (no auth) should report hasKey=true")
	}
}

func TestMergeCustomProviderInput(t *testing.T) {
	existing := []*CustomSearchProviderConfig{
		{ID: "keep", Name: "Keep", Endpoint: "https://a.example", APIKey: "stored-key", ResultsJSONPath: "r", FieldMap: map[string]string{"title": "title"}},
		{ID: "gone", Name: "Gone", Endpoint: "https://b.example", APIKey: "will-drop", ResultsJSONPath: "r", FieldMap: map[string]string{"title": "title"}},
	}
	// Update "keep" with an empty key (should preserve) and add a new one.
	incoming := []adminCustomProviderInput{
		{ID: "keep", Name: "Keep", Endpoint: "https://a.example", ResultsJSONPath: "r", FieldMap: map[string]string{"title": "title"}},
		{ID: "fresh", Name: "Fresh", Endpoint: "https://c.example", APIKey: "new-key", ResultsJSONPath: "r", FieldMap: map[string]string{"title": "title"}},
	}
	out, err := mergeCustomProviderInput(existing, incoming)
	if err != nil {
		t.Fatalf("merge failed: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 providers, got %d", len(out))
	}
	if out[0].APIKey != "stored-key" {
		t.Errorf("empty apiKey should preserve stored key, got %q", out[0].APIKey)
	}
	if out[1].APIKey != "new-key" {
		t.Errorf("apiKey should be updated, got %q", out[1].APIKey)
	}

	// Collision with a built-in id is rejected.
	if _, err := mergeCustomProviderInput(nil, []adminCustomProviderInput{{ID: "exa", Endpoint: "https://x"}}); err == nil {
		t.Error("expected error for id collision with built-in provider")
	}
	// Empty id is rejected.
	if _, err := mergeCustomProviderInput(nil, []adminCustomProviderInput{{ID: "", Endpoint: "https://x"}}); err == nil {
		t.Error("expected error for empty id")
	}
	// Empty endpoint is rejected.
	if _, err := mergeCustomProviderInput(nil, []adminCustomProviderInput{{ID: "x"}}); err == nil {
		t.Error("expected error for empty endpoint")
	}
}
