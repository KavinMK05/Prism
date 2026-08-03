package search

// ProviderConfig is the per-provider settings block under config.search.providers.
// APIKey is write-only from the admin API's perspective: GET /admin/search/config
// never echoes it back (it reports hasKey/keyFromEnv instead).
type ProviderConfig struct {
	Enabled bool   `json:"enabled"`
	APIKey  string `json:"api_key,omitempty"`
	BaseURL string `json:"base_url,omitempty"` // searxng/ollama only
}

// CustomProviderConfig is a user-defined REST search API declared without
// code (design doc §4). Body and Params support {{query}}, {{numResults}},
// {{allowedDomains}}, {{blockedDomains}} templates. ResultsJSONPath is a dot
// path into the JSON response; FieldMap maps each result item's fields.
// APIKey is write-only from the admin API (empty = keep existing).
type CustomProviderConfig struct {
	ID              string                 `json:"id"`
	Name            string                 `json:"name"`
	Endpoint        string                 `json:"endpoint"`
	Method          string                 `json:"method,omitempty"` // GET or POST (default POST)
	AuthHeader      string                 `json:"authHeader,omitempty"`
	APIKey          string                 `json:"apiKey,omitempty"`
	KeyEnv          string                 `json:"keyEnv,omitempty"`
	QueryParam      string                 `json:"queryParam,omitempty"`
	Body            map[string]interface{} `json:"body,omitempty"`
	Params          map[string]string      `json:"params,omitempty"`
	ResultsJSONPath string                 `json:"resultsJSONPath"`
	FieldMap        map[string]string      `json:"fieldMap"`
	Enabled         bool                   `json:"enabled"`
}

// Config is the top-level search block. It drives the SearchRunner that
// the per-agent web-search interception calls through.
type Config struct {
	Active            string                     `json:"active"`
	Fallback          []string                   `json:"fallback,omitempty"`
	MaxPerTurn        int                        `json:"max_per_turn,omitempty"`
	TimeoutMs         int                        `json:"timeout_ms,omitempty"`
	DefaultNumResults int                        `json:"default_num_results,omitempty"`
	Providers         map[string]*ProviderConfig `json:"providers,omitempty"`
	CustomProviders   []*CustomProviderConfig    `json:"custom_providers,omitempty"`
}

// Clone returns a deep copy of the Config's custom providers block.
func (sc *Config) Clone() *Config {
	cp := *sc
	cp.CustomProviders = make([]*CustomProviderConfig, len(sc.CustomProviders))
	for i, p := range sc.CustomProviders {
		pc := *p
		if p.Body != nil {
			body := make(map[string]interface{}, len(p.Body))
			for k, v := range p.Body {
				body[k] = v
			}
			pc.Body = body
		}
		if p.Params != nil {
			params := make(map[string]string, len(p.Params))
			for k, v := range p.Params {
				params[k] = v
			}
			pc.Params = params
		}
		if p.FieldMap != nil {
			fm := make(map[string]string, len(p.FieldMap))
			for k, v := range p.FieldMap {
				fm[k] = v
			}
			pc.FieldMap = fm
		}
		cp.CustomProviders[i] = &pc
	}
	return &cp
}
