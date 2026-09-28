// Marketplace catalog sync. The registry API is designed for downstream
// aggregators to scrape on a regular but infrequent basis, so Prism persists
// what it fetches instead of querying per keystroke: search stays instant, it
// works with no network, and an incremental pass can ask only for what changed.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"ollama-proxy/internal/config"
	"ollama-proxy/internal/db"
)

const (
	// catalogPageSize is the registry's own maximum.
	catalogPageSize = 100
	// catalogMaxPages bounds one sync so a misbehaving registry that never
	// stops handing back a cursor cannot spin forever.
	catalogMaxPages = 200
	// catalogPageDelay keeps a full sync polite. The registry asks aggregators
	// to poll infrequently, and a small pause costs little.
	catalogPageDelay = 50 * time.Millisecond
)

// CatalogSyncResult is the outcome of syncing one source.
type CatalogSyncResult struct {
	SourceID    string `json:"source_id"`
	SourceName  string `json:"source_name"`
	Added       int    `json:"added"`
	Incremental bool   `json:"incremental"`
	Error       string `json:"error,omitempty"`
}

// CatalogStatus describes one source's cached state for the UI.
type CatalogStatus struct {
	SourceID     string `json:"source_id"`
	SourceName   string `json:"source_name"`
	LastSyncedAt int64  `json:"last_synced_at"`
	ServerCount  int    `json:"server_count"`
	LastError    string `json:"last_error,omitempty"`
}

// SyncCatalog refreshes the cached catalog for the given sources. A source
// with a previous successful sync is fetched incrementally via updated_since;
// otherwise the full catalog is pulled and stale entries are pruned.
//
// A failing source does not abort the others: each result records its own
// error, because one unreachable private registry should not stop the official
// one from refreshing.
func SyncCatalog(ctx context.Context, sources []*config.MCPRegistrySource) ([]CatalogSyncResult, error) {
	if len(sources) == 0 {
		return nil, errors.New("no registry sources are enabled")
	}
	states, err := db.MCPCatalogSyncStates()
	if err != nil {
		return nil, err
	}

	results := make([]CatalogSyncResult, 0, len(sources))
	for _, src := range sources {
		if src == nil {
			continue
		}
		res := CatalogSyncResult{SourceID: src.ID, SourceName: src.Name}
		state := states[src.ID]
		// A previous run that only recorded an error has no usable watermark.
		incremental := state.LastSyncedAt > 0
		res.Incremental = incremental

		entries, syncErr := fetchSourceCatalog(ctx, src, incremental, state.LastSyncedAt)
		if syncErr != nil {
			res.Error = syncErr.Error()
			// Keep the last good sync time so the UI still shows when the
			// cached data was actually fresh.
			_ = db.RecordMCPCatalogSync(src.ID, state.ServerCount, syncErr.Error())
			results = append(results, res)
			continue
		}

		if incremental {
			err = db.UpsertMCPCatalogEntries(src.ID, entries)
		} else {
			err = db.ReplaceMCPCatalogSource(src.ID, entries)
		}
		if err != nil {
			res.Error = err.Error()
			results = append(results, res)
			continue
		}
		if !incremental {
			_ = db.RecordMCPCatalogSync(src.ID, len(entries), "")
		}
		res.Added = len(entries)
		results = append(results, res)
	}
	return results, nil
}

// fetchSourceCatalog walks every page of one source. With incremental set it
// asks only for servers updated since the watermark, and upserts the result
// without pruning.
func fetchSourceCatalog(ctx context.Context, src *config.MCPRegistrySource, incremental bool, since int64) ([]db.MCPCatalogEntry, error) {
	client := NewRegistryClient(src)
	entries := make([]db.MCPCatalogEntry, 0, catalogPageSize)
	cursor := ""
	params := ListParams{Limit: catalogPageSize}
	if incremental && since > 0 {
		// updated_since filters on a timestamp, and a server updated in the
		// same second as the watermark could otherwise be missed, so step back
		// slightly. Re-fetching a few entries is harmless because the write is
		// an upsert.
		params.UpdatedSince = time.Unix(since-60, 0).UTC().Format(time.RFC3339)
	}

	for page := 0; page < catalogMaxPages; page++ {
		params.Cursor = cursor
		batch, next, err := client.ListServers(ctx, params)
		if err != nil {
			if len(entries) > 0 {
				// Partial results are worth keeping; report why it stopped.
				log.Printf("[mcp] catalog sync for %s stopped early: %v", src.ID, err)
				break
			}
			return nil, err
		}
		for _, raw := range batch {
			entry, ok := catalogEntryFromRaw(src.ID, raw)
			if !ok {
				continue // servers Prism cannot install are not worth caching
			}
			entries = append(entries, entry)
		}
		if next == "" || len(batch) == 0 {
			return entries, nil
		}
		cursor = next
		if catalogPageDelay > 0 {
			select {
			case <-ctx.Done():
				return entries, ctx.Err()
			case <-time.After(catalogPageDelay):
			}
		}
	}
	// Hitting the page cap is not a failure, but it is worth recording.
	log.Printf("[mcp] catalog sync for %s hit the %d page cap; cached %d servers", src.ID, catalogMaxPages, len(entries))
	return entries, nil
}

// catalogEntryFromRaw converts a raw registry entry into a catalog row. Entries
// Prism could not install are skipped so the cached catalog only holds servers
// that can actually be added.
func catalogEntryFromRaw(sourceID string, raw RegistryEntry) (db.MCPCatalogEntry, bool) {
	item, err := RegistryItemFromEntry(raw)
	if err != nil || item.Server == nil {
		return db.MCPCatalogEntry{}, false
	}
	item.SourceID = sourceID
	payload, err := json.Marshal(item)
	if err != nil {
		return db.MCPCatalogEntry{}, false
	}
	return db.MCPCatalogEntry{
		SourceID:    sourceID,
		Name:        item.Name,
		Version:     item.Version,
		Title:       item.Title,
		Description: item.Description,
		Repository:  item.Repository,
		Transport:   item.Transport,
		Publisher:   item.Publisher,
		Status:      item.Status,
		NamespaceOK: item.NamespaceMatch,
		Trusted:     item.Trusted,
		Deleted:     item.Deleted,
		Payload:     string(payload),
	}, true
}

// SearchCatalog serves a search from the local cache.
func SearchCatalog(query, sourceID string, limit int) ([]RegistrySearchItem, error) {
	payloads, err := db.SearchMCPCatalog(query, sourceID, false, limit)
	if err != nil {
		return nil, err
	}
	items := make([]RegistrySearchItem, 0, len(payloads))
	for _, p := range payloads {
		var item RegistrySearchItem
		if err := json.Unmarshal([]byte(p), &item); err != nil {
			continue
		}
		items = append(items, item)
	}
	return items, nil
}

// CatalogStatuses reports each source's cached state, merged with the config so
// a source that has never synced still appears.
func CatalogStatuses(sources []*config.MCPRegistrySource) ([]CatalogStatus, error) {
	states, err := db.MCPCatalogSyncStates()
	if err != nil {
		return nil, err
	}
	out := make([]CatalogStatus, 0, len(sources))
	for _, src := range sources {
		if src == nil {
			continue
		}
		st := CatalogStatus{SourceID: src.ID, SourceName: src.Name}
		if s, ok := states[src.ID]; ok {
			st.LastSyncedAt = s.LastSyncedAt
			st.ServerCount = s.ServerCount
			st.LastError = s.LastError
		}
		out = append(out, st)
	}
	return out, nil
}

// CatalogIsEmpty reports whether nothing has been cached yet, so the UI can
// tell "no results" apart from "never synced".
func CatalogIsEmpty() bool {
	return db.MCPCatalogCount("") == 0
}

// ResolveCatalogItem returns a cached registry item by name, preferring the
// copy from the named source and falling back to any source when sourceID is
// empty. It lets a saved server be re-resolved offline.
func ResolveCatalogItem(sourceID, name string) (*RegistrySearchItem, bool) {
	if strings.TrimSpace(name) == "" {
		return nil, false
	}
	search := func(sid string) (*RegistrySearchItem, bool) {
		// An empty version means "whatever is cached as this server's latest".
		payload, ok := db.GetMCPCatalogPayload(sid, name, "")
		if !ok {
			return nil, false
		}
		var item RegistrySearchItem
		if err := json.Unmarshal([]byte(payload), &item); err != nil {
			return nil, false
		}
		return &item, true
	}
	if sourceID != "" {
		if item, ok := search(sourceID); ok {
			return item, true
		}
	}
	states, err := db.MCPCatalogSyncStates()
	if err != nil {
		return nil, false
	}
	for sid := range states {
		if sid == sourceID {
			continue
		}
		if item, ok := search(sid); ok {
			return item, true
		}
	}
	return nil, false
}

// FetchVersionItem fetches one exact server version from a source and converts
// it for installation. It is used when the user pins a version that the cached
// row does not cover.
func FetchVersionItem(ctx context.Context, src *config.MCPRegistrySource, name, version string) (*RegistrySearchItem, error) {
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("a server name is required")
	}
	client := NewRegistryClient(src)
	entries, _, err := client.ListServers(ctx, ListParams{Query: name, Limit: 20, Version: version})
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !strings.EqualFold(e.Server.Name, name) {
			continue
		}
		item, err := RegistryItemFromEntry(e)
		if err != nil {
			return nil, err
		}
		item.SourceID = src.ID
		item.SourceName = src.Name
		return &item, nil
	}
	return nil, fmt.Errorf("%s has no version %s", name, firstNonEmpty(version, "latest"))
}
