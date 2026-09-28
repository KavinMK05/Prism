package admin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"ollama-proxy/internal/config"
)

// Ollama Cloud model discovery.
//
// Ollama Cloud publishes a live catalog at GET /api/tags and per-model metadata
// at POST /api/show. Those are the authoritative source for what the cloud
// actually serves: the catalog drops retired models (models.dev keeps listing a
// few, and calling them returns 410 Gone) and /api/show reports the context
// window of the deployed weights, which drifts from models.dev's static index.
// Context length, capabilities and reasoning-effort levels therefore come from
// the server; models.dev is consulted only for fields /api/show does not
// report (human display name, max output tokens, structured-output support).
//
// The native endpoints stay on the native wire format - this is metadata only.
// Ollama Cloud inference itself goes over the OpenAI-compatible Chat
// Completions API (see config.GetProviderByID).

const (
	// ollamaCloudProviderID is the Prism provider id for Ollama's hosted cloud.
	ollamaCloudProviderID = "ollama_cloud"
	// ollamaCloudDefaultBaseURL is used when the provider has no base_url set.
	ollamaCloudDefaultBaseURL = "https://ollama.com"

	// TTLs keep the admin UI's per-keystroke search from hammering the cloud.
	ollamaTagsCacheTTL = 2 * time.Minute
	ollamaShowCacheTTL = 10 * time.Minute
)

// ollamaCloudClient is the HTTP client used for Ollama Cloud metadata requests.
var ollamaCloudClient = &http.Client{Timeout: 30 * time.Second}

// isOllamaCloudProvider reports whether a Prism provider id is Ollama Cloud,
// which is the only provider whose discovery comes from Ollama itself.
func isOllamaCloudProvider(prismProviderID string) bool {
	return strings.EqualFold(prismProviderID, ollamaCloudProviderID)
}

// ollamaCloudCredentials resolves the base URL and API key for Ollama Cloud
// from the live config, falling back to the environment like the proxy does.
func ollamaCloudCredentials() (baseURL, apiKey string) {
	baseURL, apiKey = ollamaCloudDefaultBaseURL, os.Getenv("OLLAMA_API_KEY")
	if c := config.Current(); c != nil && c.OllamaCloud != nil {
		if c.OllamaCloud.BaseURL != "" {
			baseURL = c.OllamaCloud.BaseURL
		}
		if c.OllamaCloud.APIKey != "" {
			apiKey = c.OllamaCloud.APIKey
		}
	}
	return strings.TrimRight(baseURL, "/"), apiKey
}

func ollamaCloudAuthorization(apiKey string) string {
	if apiKey == "" {
		return ""
	}
	return "Bearer " + apiKey
}

// ollamaTag is one entry of GET /api/tags. Cloud entries carry blank
// details/format fields; name and model are the same id.
type ollamaTag struct {
	Name       string `json:"name"`
	Model      string `json:"model"`
	Digest     string `json:"digest"`
	ModifiedAt string `json:"modified_at"`
}

func (t ollamaTag) id() string {
	if t.Model != "" {
		return t.Model
	}
	return t.Name
}

// ollamaThinking is the "thinking" block of POST /api/show. Values are mixed
// booleans and strings ("low"/"high"/"max", or false/true for models that only
// toggle thinking), so they are kept as raw JSON.
type ollamaThinking struct {
	Default json.RawMessage   `json:"default"`
	Values  []json.RawMessage `json:"values"`
}

// ollamaShowInfo is the subset of POST /api/show consumed for discovery.
type ollamaShowInfo struct {
	Capabilities []string                   `json:"capabilities"`
	Thinking     *ollamaThinking            `json:"thinking"`
	ModelInfo    map[string]json.RawMessage `json:"model_info"`
}

func newOllamaRequest(method, url, apiKey string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if auth := ollamaCloudAuthorization(apiKey); auth != "" {
		req.Header.Set("Authorization", auth)
	}
	return req, nil
}

func upstreamSnippet(body []byte) string {
	s := strings.TrimSpace(string(body))
	if len(s) > 200 {
		s = s[:200] + "..."
	}
	if s == "" {
		return "(empty response)"
	}
	return s
}

// fetchOllamaTags lists the models the cloud currently serves.
func fetchOllamaTags(baseURL, apiKey string) ([]ollamaTag, error) {
	req, err := newOllamaRequest(http.MethodGet, baseURL+"/api/tags", apiKey, nil)
	if err != nil {
		return nil, err
	}
	resp, err := ollamaCloudClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to reach Ollama Cloud: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("failed to read Ollama Cloud model list")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Ollama Cloud /api/tags returned status %d: %s", resp.StatusCode, upstreamSnippet(body))
	}
	var parsed struct {
		Models []ollamaTag `json:"models"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("failed to parse Ollama Cloud model list")
	}
	return parsed.Models, nil
}

// fetchOllamaShow fetches POST /api/show for a single model.
func fetchOllamaShow(baseURL, apiKey, model string) (*ollamaShowInfo, error) {
	payload, err := json.Marshal(map[string]string{"model": model})
	if err != nil {
		return nil, err
	}
	req, err := newOllamaRequest(http.MethodPost, baseURL+"/api/show", apiKey, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	resp, err := ollamaCloudClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to reach Ollama Cloud: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("failed to read Ollama Cloud model info")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Ollama Cloud /api/show for %q returned status %d: %s", model, resp.StatusCode, upstreamSnippet(body))
	}
	var info ollamaShowInfo
	if err := json.Unmarshal(body, &info); err != nil {
		return nil, fmt.Errorf("failed to parse Ollama Cloud model info for %q", model)
	}
	return &info, nil
}

type ollamaCacheEntry[T any] struct {
	value  T
	expiry time.Time
}

var (
	ollamaTagsCacheMu sync.Mutex
	ollamaTagsCache   = map[string]ollamaCacheEntry[[]ollamaTag]{}

	ollamaShowCacheMu sync.Mutex
	ollamaShowCache   = map[string]ollamaCacheEntry[*ollamaShowInfo]{}
)

func cachedOllamaTags(baseURL, apiKey string) ([]ollamaTag, error) {
	ollamaTagsCacheMu.Lock()
	if e, ok := ollamaTagsCache[baseURL]; ok && time.Now().Before(e.expiry) {
		ollamaTagsCacheMu.Unlock()
		return e.value, nil
	}
	ollamaTagsCacheMu.Unlock()

	tags, err := fetchOllamaTags(baseURL, apiKey)
	if err != nil {
		return nil, err
	}
	ollamaTagsCacheMu.Lock()
	ollamaTagsCache[baseURL] = ollamaCacheEntry[[]ollamaTag]{value: tags, expiry: time.Now().Add(ollamaTagsCacheTTL)}
	ollamaTagsCacheMu.Unlock()
	return tags, nil
}

func cachedOllamaShow(baseURL, apiKey, model string) (*ollamaShowInfo, error) {
	key := baseURL + "|" + model
	ollamaShowCacheMu.Lock()
	if e, ok := ollamaShowCache[key]; ok && time.Now().Before(e.expiry) {
		ollamaShowCacheMu.Unlock()
		return e.value, nil
	}
	ollamaShowCacheMu.Unlock()

	info, err := fetchOllamaShow(baseURL, apiKey, model)
	if err != nil {
		return nil, err
	}
	ollamaShowCacheMu.Lock()
	ollamaShowCache[key] = ollamaCacheEntry[*ollamaShowInfo]{value: info, expiry: time.Now().Add(ollamaShowCacheTTL)}
	ollamaShowCacheMu.Unlock()
	return info, nil
}

// ollamaContextLength extracts the deployed context window. The key is
// arch-scoped ("gptoss.context_length", "deepseek_v41.context_length"), and
// models whose architecture string is empty report the degenerate key
// ".context_length", so match on the suffix rather than an exact key.
func ollamaContextLength(info *ollamaShowInfo) int {
	if info == nil {
		return 0
	}
	best := 0
	for key, raw := range info.ModelInfo {
		if !strings.HasSuffix(key, "context_length") {
			continue
		}
		var n int
		if json.Unmarshal(raw, &n) == nil && n > best {
			best = n
		}
	}
	return best
}

// ollamaHasCapability reports whether POST /api/show listed a capability
// (e.g. "tools", "vision", "thinking").
func ollamaHasCapability(info *ollamaShowInfo, capability string) bool {
	if info == nil {
		return false
	}
	for _, c := range info.Capabilities {
		if strings.EqualFold(strings.TrimSpace(c), capability) {
			return true
		}
	}
	return false
}

// ollamaReasoningEfforts returns the graded effort levels the model accepts, in
// server order, dropping the boolean entries used by models that only toggle
// thinking on or off.
func ollamaReasoningEfforts(info *ollamaShowInfo) []string {
	if info == nil || info.Thinking == nil {
		return nil
	}
	var efforts []string
	for _, raw := range info.Thinking.Values {
		var s string
		if json.Unmarshal(raw, &s) != nil {
			continue // booleans (false/true) are a toggle, not a graded level
		}
		s = strings.ToLower(strings.TrimSpace(s))
		if s == "" || s == "false" || s == "true" {
			continue
		}
		efforts = append(efforts, s)
	}
	return efforts
}

// ollamaReasoning reports whether the model can emit thinking, either because it
// advertises the thinking capability or because it publishes effort values.
func ollamaReasoning(info *ollamaShowInfo) bool {
	if ollamaHasCapability(info, "thinking") {
		return true
	}
	return info != nil && info.Thinking != nil && len(info.Thinking.Values) > 0
}

// fetchOllamaCloudSearch returns the cloud's live catalog filtered by query.
func fetchOllamaCloudSearch(query string) ([]map[string]string, error) {
	baseURL, apiKey := ollamaCloudCredentials()
	if apiKey == "" {
		return nil, fmt.Errorf("Ollama Cloud API key not configured")
	}
	tags, err := cachedOllamaTags(baseURL, apiKey)
	if err != nil {
		return nil, err
	}
	q := strings.ToLower(strings.TrimSpace(query))
	results := []map[string]string{}
	for _, tag := range tags {
		id := tag.id()
		if id == "" {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(id), q) {
			continue
		}
		results = append(results, map[string]string{"id": id, "name": ""})
	}
	sort.Slice(results, func(i, j int) bool { return results[i]["id"] < results[j]["id"] })
	if len(results) > 50 {
		results = results[:50]
	}
	return results, nil
}

// FetchModelInfo resolves model metadata for the admin UI's Fetch button.
// Ollama Cloud is read from Ollama itself (/api/tags + /api/show); every other
// provider comes from models.dev.
func FetchModelInfo(modelID, prismProvider string) (*modelsDevResult, error) {
	if isOllamaCloudProvider(prismProvider) {
		return fetchOllamaCloudModelInfo(modelID)
	}
	return FetchModelsDevModel(modelID, prismProvider)
}

// fetchOllamaCloudModelInfo resolves one model's metadata for the admin UI.
// Server-reported values win; models.dev fills the gaps.
func fetchOllamaCloudModelInfo(modelID string) (*modelsDevResult, error) {
	baseURL, apiKey := ollamaCloudCredentials()
	if apiKey == "" {
		return nil, fmt.Errorf("Ollama Cloud API key not configured")
	}
	info, err := cachedOllamaShow(baseURL, apiKey, modelID)
	if err != nil {
		return nil, err
	}
	return mergeOllamaShowResult(modelID, info, modelsDevExactModel(modelID, ollamaCloudProviderID)), nil
}

// mergeOllamaShowResult combines POST /api/show with the models.dev entry for
// the same model. The server is authoritative for the fields it reports
// (context window, capabilities, reasoning levels); models.dev supplies only
// what /api/show does not publish: display name, max output tokens and
// structured-output support. An exact models.dev id match is required - Ollama
// Cloud tag names carry the size in the id itself ("gpt-oss:20b"), which the
// fuzzy matcher mistakes for a routing suffix and resolves to a sibling entry.
func mergeOllamaShowResult(modelID string, info *ollamaShowInfo, dev *modelsDevResult) *modelsDevResult {
	result := &modelsDevResult{
		ID:              modelID,
		Name:            modelID,
		ProviderID:      "ollama-cloud",
		ContextLength:   ollamaContextLength(info),
		Reasoning:       ollamaReasoning(info),
		ReasoningEffort: ollamaReasoningEfforts(info),
		ToolCall:        ollamaHasCapability(info, "tools"),
		Vision:          ollamaHasCapability(info, "vision"),
	}
	if dev != nil {
		result.Name = dev.Name
		result.MaxOutputTokens = dev.MaxOutputTokens
		result.StructuredOutput = dev.StructuredOutput
		if len(result.ReasoningEffort) == 0 {
			result.ReasoningEffort = dev.ReasoningEffort
		}
	}
	return result
}
