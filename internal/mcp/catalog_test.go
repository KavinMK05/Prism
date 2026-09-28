package mcp

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"sync/atomic"
	"testing"

	"ollama-proxy/internal/config"
	"ollama-proxy/internal/db"
	"ollama-proxy/internal/platform"
)

// initTestDB opens the stats database in a temp config dir so catalog tests
// have real SQLite persistence without touching the user's data.
func initTestDB(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("APPDATA", dir)
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	if err := os.MkdirAll(platform.ConfigDir(), 0755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := db.Init(); err != nil {
		t.Fatalf("db.Init: %v", err)
	}
	t.Cleanup(db.Close)
}

// registryPage builds a server.json entry body for the fake registry.
func registryEntry(name, description, version, status, repo string) string {
	repository := ""
	if repo != "" {
		repository = fmt.Sprintf(`"repository":{"url":%q,"source":"github"},`, repo)
	}
	statusField := ""
	if status != "" {
		statusField = fmt.Sprintf(`"status":%q,`, status)
	}
	return fmt.Sprintf(`{"server":{"name":%q,"title":"Title %s","description":%q,%s%s
		"version":%q,
		"packages":[{"registryType":"npm","identifier":"%s","version":%q,"transport":{"type":"stdio"}}]}}`,
		name, name, description, repository, statusField, version, name, version)
}

func TestSyncCatalogFullAndIncremental(t *testing.T) {
	initTestDB(t)

	var requests int32
	var sawUpdatedSince atomic.Bool
	srv := newFakeRegistry(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		if r.URL.Query().Get("updated_since") != "" {
			sawUpdatedSince.Store(true)
		}
		_, _ = w.Write([]byte(`{"servers":[` +
			registryEntry("io.github.alice/weather", "Weather data", "1.0.0", "", "https://github.com/alice/weather") + `,` +
			registryEntry("io.github.bob/notes", "Notes", "2.0.0", "", "https://github.com/elsewhere/notes") +
			`],"metadata":{"count":2}}`))
	})
	src := &config.MCPRegistrySource{ID: "test", Name: "Test", BaseURL: srv.URL, Enabled: true}

	results, err := SyncCatalog(context.Background(), []*config.MCPRegistrySource{src})
	if err != nil {
		t.Fatalf("SyncCatalog: %v", err)
	}
	if len(results) != 1 || results[0].Error != "" {
		t.Fatalf("unexpected result: %+v", results)
	}
	if results[0].Incremental {
		t.Error("the first sync must be a full pull")
	}
	if results[0].Added != 2 {
		t.Errorf("added = %d, want 2", results[0].Added)
	}
	if sawUpdatedSince.Load() {
		t.Error("a full sync must not send updated_since")
	}
	if n := db.MCPCatalogCount("test"); n != 2 {
		t.Errorf("catalog count = %d, want 2", n)
	}

	// A second sync uses the watermark recorded by the first.
	results, err = SyncCatalog(context.Background(), []*config.MCPRegistrySource{src})
	if err != nil {
		t.Fatalf("SyncCatalog (second): %v", err)
	}
	if !results[0].Incremental {
		t.Error("the second sync should be incremental")
	}
	if !sawUpdatedSince.Load() {
		t.Error("an incremental sync must send updated_since")
	}
}

func TestSearchCatalogFiltersAndRanks(t *testing.T) {
	initTestDB(t)

	srv := newFakeRegistry(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"servers":[` +
			// Trusted: namespace and repository agree.
			registryEntry("io.github.alice/weather", "Weather data for cities", "1.0.0", "", "https://github.com/alice/weather") + `,` +
			// Untrusted: namespace says alice, repository says someone else.
			registryEntry("io.github.alice/weathercopy", "A weather clone", "1.0.0", "", "https://github.com/mallory/weathercopy") + `,` +
			// Deleted: must be hidden by default.
			registryEntry("io.github.alice/malware", "Weather malware", "1.0.0", "deleted", "https://github.com/alice/malware") +
			`],"metadata":{"count":3}}`))
	})
	src := &config.MCPRegistrySource{ID: "test", Name: "Test", BaseURL: srv.URL, Enabled: true}
	if _, err := SyncCatalog(context.Background(), []*config.MCPRegistrySource{src}); err != nil {
		t.Fatalf("SyncCatalog: %v", err)
	}

	all, err := SearchCatalog("weather", "", 20)
	if err != nil {
		t.Fatalf("SearchCatalog: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 non-deleted matches, got %d", len(all))
	}
	for _, item := range all {
		if item.Deleted {
			t.Errorf("a deleted server must not be returned: %s", item.Name)
		}
	}
	// The trusted server should rank first.
	if !all[0].Trusted {
		t.Errorf("trusted server should rank first, got %s", all[0].Name)
	}
	if all[0].Name != "io.github.alice/weather" {
		t.Errorf("unexpected first result: %s", all[0].Name)
	}

	// A source filter that matches nothing returns nothing.
	none, err := SearchCatalog("weather", "other-source", 20)
	if err != nil {
		t.Fatalf("SearchCatalog (filtered): %v", err)
	}
	if len(none) != 0 {
		t.Errorf("source filter ignored: %d results", len(none))
	}

	// A query with LIKE wildcards must match literally, not as a pattern.
	wild, err := SearchCatalog("%", "", 20)
	if err != nil {
		t.Fatalf("SearchCatalog (wildcard): %v", err)
	}
	if len(wild) != 0 {
		t.Errorf("a literal %% should not match everything: %d results", len(wild))
	}
}

func TestCatalogStatusesAndEmpty(t *testing.T) {
	initTestDB(t)
	if !CatalogIsEmpty() {
		t.Error("a fresh catalog should report empty")
	}
	src := &config.MCPRegistrySource{ID: "test", Name: "Test", BaseURL: "https://example.com"}

	// A source that has never synced still reports, with no timestamp.
	statuses, err := CatalogStatuses([]*config.MCPRegistrySource{src})
	if err != nil {
		t.Fatalf("CatalogStatuses: %v", err)
	}
	if len(statuses) != 1 || statuses[0].LastSyncedAt != 0 || statuses[0].ServerCount != 0 {
		t.Errorf("unexpected status: %+v", statuses)
	}
}

func TestSyncCatalogKeepsLastGoodSyncOnError(t *testing.T) {
	initTestDB(t)

	var fail atomic.Bool
	srv := newFakeRegistry(t, func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"servers":[` +
			registryEntry("io.github.alice/weather", "Weather", "1.0.0", "", "https://github.com/alice/weather") +
			`],"metadata":{"count":1}}`))
	})
	src := &config.MCPRegistrySource{ID: "test", Name: "Test", BaseURL: srv.URL, Enabled: true}
	results, err := SyncCatalog(context.Background(), []*config.MCPRegistrySource{src})
	if err != nil {
		t.Fatalf("SyncCatalog: %v", err)
	}
	if results[0].Added != 1 {
		t.Fatalf("first sync added = %d", results[0].Added)
	}
	statuses, _ := CatalogStatuses([]*config.MCPRegistrySource{src})
	if statuses[0].LastSyncedAt == 0 {
		t.Fatal("a successful sync should record a timestamp")
	}

	// Now the source breaks. The cached data and its freshness must survive,
	// with the error reported alongside.
	fail.Store(true)
	results, err = SyncCatalog(context.Background(), []*config.MCPRegistrySource{src})
	if err != nil {
		t.Fatalf("SyncCatalog (failing): %v", err)
	}
	if results[0].Error == "" {
		t.Error("the failure should be reported")
	}
	statuses, _ = CatalogStatuses([]*config.MCPRegistrySource{src})
	if statuses[0].LastSyncedAt == 0 {
		t.Error("a failed sync must not erase the last good sync time")
	}
	if statuses[0].ServerCount != 1 {
		t.Errorf("the cached count should survive, got %d", statuses[0].ServerCount)
	}
	if statuses[0].LastError == "" {
		t.Error("the error should be recorded")
	}
	if n := db.MCPCatalogCount("test"); n != 1 {
		t.Errorf("cached servers should survive a failed sync, got %d", n)
	}
}

func TestSyncCatalogRejectsEmptySourceList(t *testing.T) {
	initTestDB(t)
	if _, err := SyncCatalog(context.Background(), nil); err == nil {
		t.Error("an empty source list should be an error")
	}
}

func TestSyncCatalogPrunesOnFullSync(t *testing.T) {
	initTestDB(t)

	var includeSecond atomic.Bool
	includeSecond.Store(true)
	srv := newFakeRegistry(t, func(w http.ResponseWriter, r *http.Request) {
		body := registryEntry("io.github.alice/weather", "Weather", "1.0.0", "", "https://github.com/alice/weather")
		if includeSecond.Load() {
			body += "," + registryEntry("io.github.alice/gone", "A server withdrawn upstream", "1.0.0", "", "https://github.com/alice/gone")
		}
		_, _ = w.Write([]byte(`{"servers":[` + body + `],"metadata":{"count":2}}`))
	})
	src := &config.MCPRegistrySource{ID: "test", Name: "Test", BaseURL: srv.URL, Enabled: true}
	if _, err := SyncCatalog(context.Background(), []*config.MCPRegistrySource{src}); err != nil {
		t.Fatalf("SyncCatalog: %v", err)
	}
	if n := db.MCPCatalogCount("test"); n != 2 {
		t.Fatalf("catalog count = %d, want 2", n)
	}

	// The upstream server disappears and the source is synced from scratch.
	// Clear the watermark so the second sync is a full pull rather than an
	// incremental one, which is the path that prunes.
	includeSecond.Store(false)
	if err := db.RecordMCPCatalogSync("test", 0, ""); err != nil {
		t.Fatalf("RecordMCPCatalogSync: %v", err)
	}
	if err := db.ClearMCPCatalogSource("test"); err != nil {
		t.Fatalf("ClearMCPCatalogSource: %v", err)
	}
	if _, err := SyncCatalog(context.Background(), []*config.MCPRegistrySource{src}); err != nil {
		t.Fatalf("SyncCatalog (second): %v", err)
	}
	if n := db.MCPCatalogCount("test"); n != 1 {
		t.Errorf("catalog count after full sync = %d, want 1", n)
	}
}

func TestFetchVersionItemMatchesExactName(t *testing.T) {
	initTestDB(t)
	srv := newFakeRegistry(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"servers":[` +
			// A loosely related result that the search may also return.
			registryEntry("io.github.alice/weather-extra", "Other", "1.0.0", "", "") + `,` +
			registryEntry("io.github.alice/weather", "Weather", "0.9.0", "", "https://github.com/alice/weather") +
			`],"metadata":{"count":2}}`))
	})
	src := &config.MCPRegistrySource{ID: "test", Name: "Test", BaseURL: srv.URL}

	item, err := FetchVersionItem(context.Background(), src, "io.github.alice/weather", "0.9.0")
	if err != nil {
		t.Fatalf("FetchVersionItem: %v", err)
	}
	if item.Name != "io.github.alice/weather" || item.Version != "0.9.0" {
		t.Errorf("wrong entry returned: %s@%s", item.Name, item.Version)
	}
	if item.SourceID != "test" {
		t.Errorf("source not recorded: %q", item.SourceID)
	}

	if _, err := FetchVersionItem(context.Background(), src, "io.github.alice/absent", "1.0.0"); err == nil {
		t.Error("a missing server should be an error")
	}
	if _, err := FetchVersionItem(context.Background(), src, "  ", "1.0.0"); err == nil {
		t.Error("an empty name should be an error")
	}
}

func TestResolveCatalogItem(t *testing.T) {
	initTestDB(t)
	srv := newFakeRegistry(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"servers":[` +
			registryEntry("io.github.alice/weather", "Weather", "1.0.0", "", "https://github.com/alice/weather") +
			`],"metadata":{"count":1}}`))
	})
	src := &config.MCPRegistrySource{ID: "test", Name: "Test", BaseURL: srv.URL, Enabled: true}
	if _, err := SyncCatalog(context.Background(), []*config.MCPRegistrySource{src}); err != nil {
		t.Fatalf("SyncCatalog: %v", err)
	}

	item, ok := ResolveCatalogItem("test", "io.github.alice/weather")
	if !ok {
		t.Fatal("cached entry not resolved")
	}
	if item.Title != "Title io.github.alice/weather" {
		t.Errorf("payload round trip lost data: %+v", item)
	}
	// An empty source id falls back to searching every synced source.
	if _, ok := ResolveCatalogItem("", "io.github.alice/weather"); !ok {
		t.Error("fallback across sources failed")
	}
	if _, ok := ResolveCatalogItem("test", "io.github.alice/absent"); ok {
		t.Error("an uncached server must not resolve")
	}
	if _, ok := ResolveCatalogItem("test", ""); ok {
		t.Error("an empty name must not resolve")
	}
}
