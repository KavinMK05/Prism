package admin

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// modelsDevMatchProviders fuzzy-matches a Prism provider ID against models.dev provider keys.
// Returns matching models.dev provider keys, or nil to search all.
func modelsDevMatchProviders(prismProviderID string, allProviderKeys []string) []string {
	if prismProviderID == "" {
		return nil
	}
	// Normalize: lowercase, replace _ with - for comparison
	searchNorm := strings.ToLower(strings.ReplaceAll(prismProviderID, "_", "-"))
	// First pass: exact match after normalization
	for _, pk := range allProviderKeys {
		pkNorm := strings.ToLower(strings.ReplaceAll(pk, "_", "-"))
		if pkNorm == searchNorm {
			return []string{pk}
		}
	}
	// Second pass: provider key contains the search term or vice versa
	var matches []string
	for _, pk := range allProviderKeys {
		pkNorm := strings.ToLower(strings.ReplaceAll(pk, "_", "-"))
		if strings.Contains(pkNorm, searchNorm) || strings.Contains(searchNorm, pkNorm) {
			matches = append(matches, pk)
		}
	}
	if len(matches) > 0 {
		return matches
	}
	// Third pass: strip common suffixes and retry
	base := searchNorm
	base = strings.TrimSuffix(base, "-cloud")
	base = strings.TrimSuffix(base, "-go")
	if base != searchNorm && base != "" {
		for _, pk := range allProviderKeys {
			pkNorm := strings.ToLower(strings.ReplaceAll(pk, "_", "-"))
			if strings.Contains(pkNorm, base) || strings.Contains(base, pkNorm) {
				matches = append(matches, pk)
			}
		}
	}
	if len(matches) > 0 {
		return matches
	}
	// No match found - search all providers
	return nil
}

// modelsDevResult holds a parsed models.dev model entry.
type modelsDevResult struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	ContextLength    int      `json:"context_length"`
	MaxOutputTokens  int      `json:"max_output_tokens"`
	Reasoning        bool     `json:"reasoning"`
	ToolCall         bool     `json:"tool_calling"`
	StructuredOutput bool     `json:"structured_output"`
	Vision           bool     `json:"vision"`
	ReasoningEffort  []string `json:"reasoning_effort,omitempty"`
	ProviderID       string   `json:"provider_id"`
}

// modelsDevClient is the HTTP client used for models.dev requests. The
// provider index is ~3 MB, so a timeout guards against a stalled download
// hanging the admin UI's "Fetch" indefinitely.
var modelsDevClient = &http.Client{Timeout: 30 * time.Second}

// fetchModelsDevAPI downloads and parses the models.dev provider index into a
// raw map of provider-key -> provider JSON.
func fetchModelsDevAPI() (map[string]json.RawMessage, error) {
	resp, err := modelsDevClient.Get("https://models.dev/api.json")
	if err != nil {
		return nil, fmt.Errorf("failed to fetch models.dev: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("models.dev returned status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read models.dev response")
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("failed to parse models.dev response")
	}
	return raw, nil
}

// fetchModelsDevModel fetches a model's metadata from models.dev. When
// prismProvider is set (a Prism provider id like "ollama_cloud"), the result
// is scoped to that provider on models.dev so the returned limits/capabilities
// reflect the provider the model is actually routed through; if that provider
// does not list the model, the search falls back to all providers.
func FetchModelsDevModel(modelID, prismProvider string) (*modelsDevResult, error) {
	raw, err := fetchModelsDevAPI()
	if err != nil {
		return nil, err
	}
	return matchModelsDevModel(raw, modelID, prismProvider), nil
}

// matchModelsDevModel is the pure, I/O-free matcher over a parsed models.dev
// index. Split out so it can be unit-tested with fixtures.
//
// Two bugs existed in the prior single-match implementation:
//  1. Selection: sort.Slice put exact matches first but the code took
//     matches[len-1] — the LAST/worst (partial) match. So searching "glm-5.2"
//     under Ollama Cloud returned "glm-5" (202752) instead of the exact
//     "glm-5.2" (976000), because "glm-5.2" contains the substring "glm-5".
//  2. Greedy reverse-substring: strings.Contains(searchID, mID) let a shorter
//     model id ("glm-5") match a longer search ("glm-5.2").
//
// This picks the best match directly (exact > native > deterministic provider
// id) and only accepts a reverse-substring when the search id is a namespaced
// form ending in "/"+mID (e.g. "z-ai/glm-5.1" -> "glm-5.1").
func matchModelsDevModel(raw map[string]json.RawMessage, modelID, prismProvider string) *modelsDevResult {
	type modelInfo struct {
		Name  string `json:"name"`
		ID    string `json:"id"`
		Limit struct {
			Context int `json:"context"`
			Output  int `json:"output"`
		} `json:"limit"`
		Reasoning        bool `json:"reasoning"`
		ToolCall         bool `json:"tool_call"`
		StructuredOutput bool `json:"structured_output"`
		Modalities       *struct {
			Input  []string `json:"input"`
			Output []string `json:"output"`
		} `json:"modalities"`
	}
	type providerInfo struct {
		ID     string               `json:"id"`
		Name   string               `json:"name"`
		Models map[string]modelInfo `json:"models"`
	}

	allProviderKeys := make([]string, 0, len(raw))
	for k := range raw {
		allProviderKeys = append(allProviderKeys, k)
	}
	scopedKeys := modelsDevMatchProviders(prismProvider, allProviderKeys)

	// Strip a Prism routing suffix like ":cloud" or ":free".
	searchBase := modelID
	if idx := strings.Index(modelID, ":"); idx > 0 {
		searchBase = modelID[:idx]
	}
	searchID := strings.ToLower(searchBase)

	type cand struct {
		m       modelInfo
		provID  string
		provKey string
		exact   bool
		native  bool
	}
	inScope := func(provID, provKey string) bool {
		if len(scopedKeys) == 0 {
			return false
		}
		for _, pk := range scopedKeys {
			if strings.EqualFold(provID, pk) || strings.EqualFold(provKey, pk) {
				return true
			}
		}
		return false
	}

	var scoped, all []cand
	for provKey, provRaw := range raw {
		var prov providerInfo
		if json.Unmarshal(provRaw, &prov) != nil {
			continue
		}
		provIDLower := strings.ToLower(prov.ID)
		provKeyLower := strings.ToLower(provKey)
		isScoped := inScope(prov.ID, provKey)
		for _, m := range prov.Models {
			mID := strings.ToLower(m.ID)
			mName := strings.ToLower(m.Name)
			var exact, partial bool
			switch {
			case mID == searchID:
				exact = true
			case strings.Contains(mID, searchID),
				strings.Contains(mName, searchID),
				strings.HasSuffix(searchID, "/"+mID):
				partial = true
			}
			if !exact && !partial {
				continue
			}
			c := cand{
				m:       m,
				provID:  prov.ID,
				provKey: provKey,
				exact:   exact,
				native:  strings.HasPrefix(searchID, provIDLower) || strings.HasPrefix(searchID, provKeyLower),
			}
			all = append(all, c)
			if isScoped {
				scoped = append(scoped, c)
			}
		}
	}
	// Prefer matches from the selected provider; fall back to all providers
	// only when the selected provider does not list the model.
	pool := scoped
	if len(pool) == 0 {
		pool = all
	}
	if len(pool) == 0 {
		return nil
	}
	// Pick the BEST match (not the worst): exact > native > deterministic
	// (alphabetical provider id) so repeated lookups are stable regardless of
	// Go's randomized map iteration.
	best := pool[0]
	for _, c := range pool[1:] {
		switch {
		case c.exact != best.exact:
			if c.exact {
				best = c
			}
		case c.native != best.native:
			if c.native {
				best = c
			}
		case c.provID < best.provID:
			best = c
		}
	}
	vision := false
	if best.m.Modalities != nil {
		for _, mod := range best.m.Modalities.Input {
			if mod == "image" {
				vision = true
			}
		}
	}
	result := &modelsDevResult{
		ID:               best.m.ID,
		Name:             best.m.Name,
		ProviderID:       best.provID,
		ContextLength:    best.m.Limit.Context,
		MaxOutputTokens:  best.m.Limit.Output,
		Reasoning:        best.m.Reasoning,
		ToolCall:         best.m.ToolCall,
		StructuredOutput: best.m.StructuredOutput,
		Vision:           vision,
	}
	if best.m.Reasoning {
		efforts := []string{"low", "medium", "high"}
		if strings.Contains(searchID, "deepseek-v4") {
			efforts = append(efforts, "max")
		}
		result.ReasoningEffort = efforts
	}
	return result
}

// writeModelsDevResult writes a modelsDevResult (or {"found":false}) as the
// JSON shape the admin UI expects. Shared by the admin and proxy endpoints.
func WriteModelsDevResult(w http.ResponseWriter, result *modelsDevResult) {
	w.Header().Set("Content-Type", "application/json")
	if result == nil {
		json.NewEncoder(w).Encode(map[string]interface{}{"found": false})
		return
	}
	json.NewEncoder(w).Encode(map[string]interface{}{
		"found":              true,
		"id":                 result.ID,
		"name":               result.Name,
		"provider_id":        result.ProviderID,
		"context_length":     result.ContextLength,
		"max_output_tokens":  result.MaxOutputTokens,
		"reasoning":          result.Reasoning,
		"tool_calling":       result.ToolCall,
		"structured_outputs": result.StructuredOutput,
		"vision":             result.Vision,
		"reasoning_effort":   result.ReasoningEffort,
	})
}

// fetchModelsDevSearch returns matching model IDs from a specific models.dev provider.
func fetchModelsDevSearch(query, prismProvider string) ([]map[string]string, error) {
	raw, err := fetchModelsDevAPI()
	if err != nil {
		return nil, err
	}
	type modelInfo struct {
		Name string `json:"name"`
		ID   string `json:"id"`
	}
	type providerInfo struct {
		ID     string               `json:"id"`
		Name   string               `json:"name"`
		Models map[string]modelInfo `json:"models"`
	}
	// Fuzzy-match Prism provider to models.dev provider keys
	allProviderKeys := make([]string, 0, len(raw))
	for k := range raw {
		allProviderKeys = append(allProviderKeys, k)
	}
	providerKeys := modelsDevMatchProviders(prismProvider, allProviderKeys)
	q := strings.ToLower(query)
	var results []map[string]string
	for provKey, provRaw := range raw {
		var prov providerInfo
		if json.Unmarshal(provRaw, &prov) != nil {
			continue
		}
		if len(providerKeys) > 0 {
			found := false
			for _, pk := range providerKeys {
				if strings.EqualFold(prov.ID, pk) || strings.EqualFold(provKey, pk) {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}
		for _, m := range prov.Models {
			mID := strings.ToLower(m.ID)
			mName := strings.ToLower(m.Name)
			if q == "" || strings.Contains(mID, q) || strings.Contains(mName, q) {
				results = append(results, map[string]string{
					"id":   m.ID,
					"name": m.Name,
				})
			}
		}
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i]["id"] < results[j]["id"]
	})
	if len(results) > 50 {
		results = results[:50]
	}
	return results, nil
}

func handleModelInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	modelID := r.URL.Query().Get("id")
	if modelID == "" {
		writeJSONError(w, "missing id parameter", 400)
		return
	}
	prismProvider := r.URL.Query().Get("provider")
	result, err := FetchModelsDevModel(modelID, prismProvider)
	if err != nil {
		writeJSONError(w, err.Error(), 502)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if result == nil {
		json.NewEncoder(w).Encode(map[string]interface{}{"found": false})
		return
	}
	json.NewEncoder(w).Encode(map[string]interface{}{
		"found":              true,
		"id":                 result.ID,
		"name":               result.Name,
		"provider_id":        result.ProviderID,
		"context_length":     result.ContextLength,
		"max_output_tokens":  result.MaxOutputTokens,
		"reasoning":          result.Reasoning,
		"tool_calling":       result.ToolCall,
		"structured_outputs": result.StructuredOutput,
		"vision":             result.Vision,
		"reasoning_effort":   result.ReasoningEffort,
	})
}

func handleModelSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	query := r.URL.Query().Get("q")
	prismProvider := r.URL.Query().Get("provider")
	results, err := fetchModelsDevSearch(query, prismProvider)
	if err != nil {
		writeJSONError(w, err.Error(), 502)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if results == nil {
		results = []map[string]string{}
	}
	json.NewEncoder(w).Encode(results)
}
