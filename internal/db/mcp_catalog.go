// MCP marketplace catalog persistence. Registry metadata is cached locally so
// browse and search work offline and instantly, and so an incremental sync can
// ask only for what changed since the last run.
package db

import (
	"database/sql"
	"encoding/json"
	"log"
	"strings"
	"time"
)

// MCPCatalogEntry is one cached registry server. Payload holds the full
// RegistrySearchItem JSON so the UI can render a result without another
// network call, while the columns beside it exist for filtering.
//
// The catalog only ever holds each server's latest version, because that is
// what a sync requests; a pinned older version is fetched on demand instead of
// being cached.
type MCPCatalogEntry struct {
	SourceID    string
	Name        string
	Version     string
	Title       string
	Description string
	Repository  string
	Transport   string
	Publisher   string
	Status      string
	NamespaceOK bool
	Trusted     bool
	Deleted     bool
	Payload     string
	SyncedAt    int64
}

// MCPCatalogSync is the per-source sync state.
type MCPCatalogSync struct {
	SourceID     string `json:"source_id"`
	LastSyncedAt int64  `json:"last_synced_at"`
	ServerCount  int    `json:"server_count"`
	LastError    string `json:"last_error,omitempty"`
}

const catalogInsert = `INSERT OR REPLACE INTO mcp_catalog
	(source_id, name, version, title, description, repository, transport, publisher, status,
	 namespace_match, trusted, is_deleted, is_latest, payload, synced_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?)`

// catalogEntryArgs flattens an entry into the insert's argument list.
func catalogEntryArgs(sourceID string, e MCPCatalogEntry, syncedAt int64) []interface{} {
	version := e.Version
	if version == "" {
		version = "latest"
	}
	return []interface{}{
		sourceID, e.Name, version, e.Title, e.Description, e.Repository, e.Transport, e.Publisher, e.Status,
		boolToInt(e.NamespaceOK), boolToInt(e.Trusted), boolToInt(e.Deleted), e.Payload, syncedAt,
	}
}

// ReplaceMCPCatalogSource swaps in a fresh set of entries for one source. The
// write is transactional so a failed sync can never leave the catalog half
// updated, and pruning happens in the same transaction so a server withdrawn
// upstream disappears here too.
func ReplaceMCPCatalogSource(sourceID string, entries []MCPCatalogEntry) error {
	if db == nil {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec("DELETE FROM mcp_catalog WHERE source_id = ?", sourceID); err != nil {
		return err
	}
	stmt, err := tx.Prepare(catalogInsert)
	if err != nil {
		return err
	}
	defer stmt.Close()

	now := time.Now().Unix()
	for _, e := range entries {
		if _, err := stmt.Exec(catalogEntryArgs(sourceID, e, now)...); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT INTO mcp_catalog_sync (source_id, last_synced_at, server_count, last_error)
		VALUES (?, ?, ?, '')
		ON CONFLICT(source_id) DO UPDATE SET last_synced_at = excluded.last_synced_at,
			server_count = excluded.server_count, last_error = ''`,
		sourceID, now, len(entries)); err != nil {
		return err
	}
	return tx.Commit()
}

// UpsertMCPCatalogEntries adds or refreshes entries without pruning, for an
// incremental sync that only fetched what changed.
func UpsertMCPCatalogEntries(sourceID string, entries []MCPCatalogEntry) error {
	if db == nil {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.Prepare(catalogInsert)
	if err != nil {
		return err
	}
	defer stmt.Close()

	now := time.Now().Unix()
	for _, e := range entries {
		if _, err := stmt.Exec(catalogEntryArgs(sourceID, e, now)...); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// RecordMCPCatalogSync stores the outcome of one sync attempt.
func RecordMCPCatalogSync(sourceID string, count int, syncErr string) error {
	if db == nil {
		return nil
	}
	_, err := db.Exec(`INSERT INTO mcp_catalog_sync (source_id, last_synced_at, server_count, last_error)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(source_id) DO UPDATE SET
			last_synced_at = CASE WHEN excluded.last_error = '' THEN excluded.last_synced_at ELSE mcp_catalog_sync.last_synced_at END,
			server_count = CASE WHEN excluded.last_error = '' THEN excluded.server_count ELSE mcp_catalog_sync.server_count END,
			last_error = excluded.last_error`,
		sourceID, time.Now().Unix(), count, syncErr)
	return err
}

// SearchMCPCatalog queries the cache. sourceID empty means every source.
// includeDeleted is off by default because the registry marks servers it
// removed for spam or malware as "deleted".
func SearchMCPCatalog(query, sourceID string, includeDeleted bool, limit int) ([]string, error) {
	if db == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 20
	}
	q := `SELECT payload FROM mcp_catalog WHERE is_latest = 1`
	args := []interface{}{}
	if !includeDeleted {
		q += " AND is_deleted = 0"
	}
	if sourceID != "" {
		q += " AND source_id = ?"
		args = append(args, sourceID)
	}
	if term := strings.TrimSpace(query); term != "" {
		// Escape LIKE wildcards so a query containing % or _ matches literally.
		escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(strings.ToLower(term))
		like := "%" + escaped + "%"
		q += ` AND (LOWER(name) LIKE ? ESCAPE '\' OR LOWER(title) LIKE ? ESCAPE '\' OR LOWER(description) LIKE ? ESCAPE '\' OR LOWER(publisher) LIKE ? ESCAPE '\')`
		args = append(args, like, like, like, like)
	}
	// Prefer trusted servers, then ones whose name or title matches the query
	// most directly, so an exact hit is not buried behind a fuzzy one.
	q += " ORDER BY trusted DESC, namespace_match DESC, LENGTH(name) ASC, name ASC LIMIT ?"
	args = append(args, limit)

	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]string, 0, limit)
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			log.Printf("[DB] failed to scan catalog row: %v", err)
			continue
		}
		out = append(out, payload)
	}
	return out, rows.Err()
}

// GetMCPCatalogPayload returns the cached payload for one exact server version,
// so a saved server can be re-resolved without a network call. ok is false when
// the entry is not cached.
func GetMCPCatalogPayload(sourceID, name, version string) (string, bool) {
	if db == nil {
		return "", false
	}
	var payload string
	// The sync only stores each server's latest version, so an empty version
	// means "whatever is cached for this server".
	if version == "" {
		err := db.QueryRow(
			`SELECT payload FROM mcp_catalog WHERE source_id = ? AND name = ? AND is_latest = 1`,
			sourceID, name,
		).Scan(&payload)
		if err == sql.ErrNoRows {
			return "", false
		}
		if err != nil {
			log.Printf("[DB] failed to read catalog payload: %v", err)
			return "", false
		}
		return payload, true
	}
	err := db.QueryRow(
		`SELECT payload FROM mcp_catalog WHERE source_id = ? AND name = ? AND version = ?`,
		sourceID, name, version,
	).Scan(&payload)
	if err == sql.ErrNoRows {
		return "", false
	}
	if err != nil {
		log.Printf("[DB] failed to read catalog payload: %v", err)
		return "", false
	}
	return payload, true
}

// MCPCatalogCount returns how many servers are cached, optionally per source.
func MCPCatalogCount(sourceID string) int {
	if db == nil {
		return 0
	}
	q := "SELECT COUNT(*) FROM mcp_catalog"
	args := []interface{}{}
	if sourceID != "" {
		q += " WHERE source_id = ?"
		args = append(args, sourceID)
	}
	var n int
	if err := db.QueryRow(q, args...).Scan(&n); err != nil {
		log.Printf("[DB] failed to count catalog: %v", err)
		return 0
	}
	return n
}

// MCPCatalogSyncStates returns the sync state of every source that has synced.
func MCPCatalogSyncStates() (map[string]MCPCatalogSync, error) {
	if db == nil {
		return nil, nil
	}
	rows, err := db.Query("SELECT source_id, last_synced_at, server_count, last_error FROM mcp_catalog_sync")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]MCPCatalogSync{}
	for rows.Next() {
		var s MCPCatalogSync
		if err := rows.Scan(&s.SourceID, &s.LastSyncedAt, &s.ServerCount, &s.LastError); err != nil {
			continue
		}
		out[s.SourceID] = s
	}
	return out, rows.Err()
}

// ClearMCPCatalogSource drops one source's cached servers and sync state,
// used when the source is removed from the config.
func ClearMCPCatalogSource(sourceID string) error {
	if db == nil {
		return nil
	}
	if _, err := db.Exec("DELETE FROM mcp_catalog WHERE source_id = ?", sourceID); err != nil {
		return err
	}
	_, err := db.Exec("DELETE FROM mcp_catalog_sync WHERE source_id = ?", sourceID)
	return err
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

// MarshalMCPCatalogPayload serializes an item for storage. It is a thin
// wrapper so callers outside this package do not need to think about the
// column set.
func MarshalMCPCatalogPayload(v interface{}) (string, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
