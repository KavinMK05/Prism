package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"ollama-proxy/internal/config"
	"ollama-proxy/internal/search"
)

func handleSearchProviders(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	encodeJSON(w, search.Catalog())
}

func handleSearchConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		cfg := config.Load()
		encodeJSON(w, search.AdminConfigView(cfg.Search))
	case http.MethodPut:
		var in search.AdminConfigInput
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeJSONError(w, "invalid JSON: "+err.Error(), 400)
			return
		}
		c := config.Load()
		sc := search.MergeConfig(c.Search)
		if in.Active != "" {
			sc.Active = in.Active
		}
		if in.Fallback != nil {
			sc.Fallback = in.Fallback
		}
		if in.MaxPerTurn > 0 {
			sc.MaxPerTurn = in.MaxPerTurn
		}
		if in.TimeoutMs > 0 {
			sc.TimeoutMs = in.TimeoutMs
		}
		if in.DefaultNumResults > 0 {
			sc.DefaultNumResults = in.DefaultNumResults
		}
		for id, incoming := range in.Providers {
			pc := sc.Providers[id]
			if pc == nil {
				pc = &search.ProviderConfig{}
				sc.Providers[id] = pc
			}
			pc.Enabled = incoming.Enabled
			if incoming.BaseURL != "" {
				pc.BaseURL = incoming.BaseURL
			}
			// APIKey is write-only: empty string = keep existing key.
			if incoming.APIKey != "" {
				pc.APIKey = incoming.APIKey
			}
		}
		if in.CustomProviders != nil {
			newList, err := search.MergeCustomProviderInput(sc.CustomProviders, in.CustomProviders)
			if err != nil {
				writeJSONError(w, err.Error(), 400)
				return
			}
			sc.CustomProviders = newList
		}
		c.Search = sc
		if err := config.Save(c); err != nil {
			writeJSONError(w, "save failed: "+err.Error(), 500)
			return
		}
		config.UpdateCurrent(func(c *config.Config) {
			if c != nil {
				c.Search = sc
			}
		})
		search.Global.Reload(sc)
		encodeJSON(w, map[string]string{"status": "ok"})
	default:
		http.Error(w, "method not allowed", 405)
	}
}

func handleSearchTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var req struct {
		Provider string `json:"provider"`
		Query    string `json:"query"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, "invalid JSON: "+err.Error(), 400)
		return
	}
	c := config.Load()
	search.Global.Reload(c.Search) // ensure latest keys
	if search.CatalogMeta(req.Provider) == nil && search.Global.CustomConfig(req.Provider) == nil {
		writeJSONError(w, "unknown provider", 400)
		return
	}
	p, err := search.Global.Build(req.Provider)
	if err != nil {
		writeJSONError(w, err.Error(), 500)
		return
	}
	q := strings.TrimSpace(req.Query)
	if q == "" {
		q = "hello world"
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	res, err := p.Search(ctx, search.SearchQuery{Query: q, NumResults: 3})
	if err != nil {
		encodeJSON(w, map[string]interface{}{"ok": false, "error": err.Error(), "provider": req.Provider})
		return
	}
	sample := make([]map[string]string, 0, len(res))
	for _, r := range res {
		sample = append(sample, map[string]string{"title": r.Title, "url": r.URL})
	}
	encodeJSON(w, map[string]interface{}{
		"ok":          true,
		"provider":    req.Provider,
		"resultCount": len(res),
		"sample":      sample,
	})
}
