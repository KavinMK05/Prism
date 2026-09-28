package db

import (
	"encoding/json"
	"os"
	"testing"

	"ollama-proxy/internal/platform"
)

func initCatalogTestDB(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("APPDATA", dir)
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	if err := os.MkdirAll(platform.ConfigDir(), 0755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(Close)
}

func entry(sourceID, name string, trusted, deleted bool) MCPCatalogEntry {
	payload := `{"name":"` + name + `","version":"1.0.0"}`
	return MCPCatalogEntry{
		SourceID:  sourceID,
		Name:      name,
		Version:   "1.0.0",
		Title:     "T " + name,
		Trusted:   trusted,
		Deleted:   deleted,
		Payload:   payload,
		Transport: "stdio",
	}
}

func TestReplaceAndSearchMCPCatalog(t *testing.T) {
	initCatalogTestDB(t)

	entries := []MCPCatalogEntry{
		entry("a", "io.github.alice/one", true, false),
		entry("a", "io.github.alice/two", false, false),
		entry("a", "io.github.alice/gone", false, true),
	}
	if err := ReplaceMCPCatalogSource("a", entries); err != nil {
		t.Fatalf("ReplaceMCPCatalogSource: %v", err)
	}
	if n := MCPCatalogCount("a"); n != 3 {
		t.Fatalf("count = %d, want 3", n)
	}

	// Deleted entries are hidden by default.
	got, err := SearchMCPCatalog("", "a", false, 20)
	if err != nil {
		t.Fatalf("SearchMCPCatalog: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("expected 2 visible entries, got %d", len(got))
	}

	// A trusted entry ranks ahead of an untrusted one.
	if len(got) > 0 {
		var first map[string]any
		if err := json.Unmarshal([]byte(got[0]), &first); err != nil {
			t.Fatalf("payload is not JSON: %v", err)
		}
		if first["name"] != "io.github.alice/one" {
			t.Errorf("trusted entry should rank first, got %v", first["name"])
		}
	}

	// includeDeleted surfaces them for callers that ask.
	all, err := SearchMCPCatalog("", "a", true, 20)
	if err != nil {
		t.Fatalf("SearchMCPCatalog (includeDeleted): %v", err)
	}
	if len(all) != 3 {
		t.Errorf("expected 3 entries with deleted included, got %d", len(all))
	}
}

func TestReplaceMCPCatalogSourcePrunes(t *testing.T) {
	initCatalogTestDB(t)
	if err := ReplaceMCPCatalogSource("a", []MCPCatalogEntry{
		entry("a", "one", false, false),
		entry("a", "two", false, false),
	}); err != nil {
		t.Fatalf("ReplaceMCPCatalogSource: %v", err)
	}

	// Replacing with a smaller set drops what is no longer published.
	if err := ReplaceMCPCatalogSource("a", []MCPCatalogEntry{entry("a", "one", false, false)}); err != nil {
		t.Fatalf("ReplaceMCPCatalogSource (second): %v", err)
	}
	if n := MCPCatalogCount("a"); n != 1 {
		t.Errorf("count = %d, want 1", n)
	}

	// Another source's rows are untouched by a replacement.
	if err := ReplaceMCPCatalogSource("b", []MCPCatalogEntry{entry("b", "three", false, false)}); err != nil {
		t.Fatalf("ReplaceMCPCatalogSource (b): %v", err)
	}
	if n := MCPCatalogCount(""); n != 2 {
		t.Errorf("total count = %d, want 2", n)
	}
}

func TestUpsertMCPCatalogEntriesKeepsOthers(t *testing.T) {
	initCatalogTestDB(t)
	if err := ReplaceMCPCatalogSource("a", []MCPCatalogEntry{
		entry("a", "one", false, false),
		entry("a", "two", false, false),
	}); err != nil {
		t.Fatalf("ReplaceMCPCatalogSource: %v", err)
	}
	// An incremental upsert must not prune the entries it did not mention.
	if err := UpsertMCPCatalogEntries("a", []MCPCatalogEntry{entry("a", "three", true, false)}); err != nil {
		t.Fatalf("UpsertMCPCatalogEntries: %v", err)
	}
	if n := MCPCatalogCount("a"); n != 3 {
		t.Errorf("count = %d, want 3", n)
	}
}

func TestSearchMCPCatalogEscapesWildcards(t *testing.T) {
	initCatalogTestDB(t)
	if err := ReplaceMCPCatalogSource("a", []MCPCatalogEntry{
		entry("a", "alpha", false, false),
		entry("a", "beta", false, false),
	}); err != nil {
		t.Fatalf("ReplaceMCPCatalogSource: %v", err)
	}
	// "%" must be a literal, not "match everything".
	got, err := SearchMCPCatalog("%", "", false, 20)
	if err != nil {
		t.Fatalf("SearchMCPCatalog: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("a literal %% matched %d entries", len(got))
	}
	// "_" likewise.
	got, err = SearchMCPCatalog("_", "", false, 20)
	if err != nil {
		t.Fatalf("SearchMCPCatalog: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("a literal _ matched %d entries", len(got))
	}
	// A normal substring still works.
	got, err = SearchMCPCatalog("alph", "", false, 20)
	if err != nil {
		t.Fatalf("SearchMCPCatalog: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("substring search returned %d entries, want 1", len(got))
	}
}

func TestRecordMCPCatalogSyncKeepsLastGoodState(t *testing.T) {
	initCatalogTestDB(t)
	if err := ReplaceMCPCatalogSource("a", []MCPCatalogEntry{entry("a", "one", false, false)}); err != nil {
		t.Fatalf("ReplaceMCPCatalogSource: %v", err)
	}
	states, err := MCPCatalogSyncStates()
	if err != nil {
		t.Fatalf("MCPCatalogSyncStates: %v", err)
	}
	good := states["a"]
	if good.LastSyncedAt == 0 || good.ServerCount != 1 {
		t.Fatalf("unexpected state after a good sync: %+v", good)
	}

	// A failed attempt records the error but preserves the last good values.
	if err := RecordMCPCatalogSync("a", 0, "registry unreachable"); err != nil {
		t.Fatalf("RecordMCPCatalogSync: %v", err)
	}
	states, _ = MCPCatalogSyncStates()
	failed := states["a"]
	if failed.LastError != "registry unreachable" {
		t.Errorf("error not recorded: %+v", failed)
	}
	if failed.LastSyncedAt != good.LastSyncedAt || failed.ServerCount != good.ServerCount {
		t.Errorf("a failed sync clobbered the good state: %+v", failed)
	}

	// A later success clears the error.
	if err := RecordMCPCatalogSync("a", 2, ""); err != nil {
		t.Fatalf("RecordMCPCatalogSync (recovery): %v", err)
	}
	states, _ = MCPCatalogSyncStates()
	if recovered := states["a"]; recovered.LastError != "" || recovered.ServerCount != 2 {
		t.Errorf("recovery not recorded: %+v", recovered)
	}
}

func TestGetMCPCatalogPayloadAndClear(t *testing.T) {
	initCatalogTestDB(t)
	if err := ReplaceMCPCatalogSource("a", []MCPCatalogEntry{entry("a", "one", false, false)}); err != nil {
		t.Fatalf("ReplaceMCPCatalogSource: %v", err)
	}
	// An empty version means the cached latest.
	if _, ok := GetMCPCatalogPayload("a", "one", ""); !ok {
		t.Error("cached entry not found by name")
	}
	if _, ok := GetMCPCatalogPayload("a", "one", "1.0.0"); !ok {
		t.Error("cached entry not found by exact version")
	}
	if _, ok := GetMCPCatalogPayload("a", "absent", ""); ok {
		t.Error("an uncached name must not resolve")
	}

	// Clearing drops both the rows and the sync state.
	if err := ClearMCPCatalogSource("a"); err != nil {
		t.Fatalf("ClearMCPCatalogSource: %v", err)
	}
	if n := MCPCatalogCount("a"); n != 0 {
		t.Errorf("count after clear = %d", n)
	}
	states, _ := MCPCatalogSyncStates()
	if _, ok := states["a"]; ok {
		t.Error("sync state should be cleared with the entries")
	}
}

func TestMCPCatalogWithoutDB(t *testing.T) {
	Close()
	if n := MCPCatalogCount(""); n != 0 {
		t.Errorf("count = %d without a database", n)
	}
	if _, err := SearchMCPCatalog("x", "", false, 10); err != nil {
		t.Errorf("SearchMCPCatalog: %v", err)
	}
	if _, ok := GetMCPCatalogPayload("a", "b", ""); ok {
		t.Error("a payload lookup must fail without a database")
	}
	if err := ReplaceMCPCatalogSource("a", nil); err != nil {
		t.Errorf("ReplaceMCPCatalogSource: %v", err)
	}
}
