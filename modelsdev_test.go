package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestMatchModelsDevModelPicksExactOverPartial is the regression test for the
// models.dev lookup bug: searching "glm-5.2" under the Ollama Cloud provider
// must return the exact "glm-5.2" entry (ctx 976000), NOT the partial "glm-5"
// entry (ctx 202752) that matches only because "glm-5.2" contains "glm-5".
//
// The old code sorted exact matches to the front but selected matches[len-1]
// (the worst/partial), and used greedy reverse-substring matching, so it
// returned "glm-5".
func TestMatchModelsDevModelPicksExactOverPartial(t *testing.T) {
	// Fixture mirrors the real ollama-cloud provider plus a second provider.
	providers := map[string]interface{}{
		"ollama-cloud": map[string]interface{}{
			"id":   "ollama-cloud",
			"name": "Ollama Cloud",
			"models": map[string]interface{}{
				"glm-5": map[string]interface{}{
					"id":   "glm-5",
					"name": "glm-5",
					"limit": map[string]interface{}{"context": 202752, "output": 131072},
				},
				"glm-5.2": map[string]interface{}{
					"id":   "glm-5.2",
					"name": "GLM-5.2",
					"limit": map[string]interface{}{"context": 976000, "output": 131072},
				},
			},
		},
		"zai": map[string]interface{}{
			"id":   "zai",
			"name": "Z.AI",
			"models": map[string]interface{}{
				"glm-5.2": map[string]interface{}{
					"id":   "glm-5.2",
					"name": "GLM-5.2",
					"limit": map[string]interface{}{"context": 1000000, "output": 131072},
				},
			},
		},
	}
	raw := map[string]json.RawMessage{}
	for k, v := range providers {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal %s: %v", k, err)
		}
		raw[k] = b
	}

	r := matchModelsDevModel(raw, "glm-5.2:cloud", "ollama_cloud")
	if r == nil {
		t.Fatal("expected a match, got nil")
	}
	if r.ID != "glm-5.2" {
		t.Fatalf("ID = %q, want \"glm-5.2\" (exact match must beat partial glm-5)", r.ID)
	}
	if r.ContextLength != 976000 {
		t.Fatalf("ContextLength = %d, want 976000 (the scoped ollama-cloud glm-5.2 value)", r.ContextLength)
	}
	if r.ProviderID != "ollama-cloud" {
		t.Fatalf("ProviderID = %q, want \"ollama-cloud\"", r.ProviderID)
	}
}

// TestMatchModelsDevModelFallbackToAllProviders verifies that when the scoped
// provider does not list the model, the lookup falls back to all providers
// instead of returning "not found".
func TestMatchModelsDevModelFallbackToAllProviders(t *testing.T) {
	providers := map[string]interface{}{
		"ollama-cloud": map[string]interface{}{
			"id":     "ollama-cloud",
			"name":   "Ollama Cloud",
			"models": map[string]interface{}{},
		},
		"zai": map[string]interface{}{
			"id":   "zai",
			"name": "Z.AI",
			"models": map[string]interface{}{
				"glm-5.2": map[string]interface{}{
					"id":   "glm-5.2",
					"name": "GLM-5.2",
					"limit": map[string]interface{}{"context": 1000000, "output": 131072},
				},
			},
		},
	}
	raw := map[string]json.RawMessage{}
	for k, v := range providers {
		b, _ := json.Marshal(v)
		raw[k] = b
	}
	r := matchModelsDevModel(raw, "glm-5.2:cloud", "ollama_cloud")
	if r == nil {
		t.Fatal("expected fallback match from all providers, got nil")
	}
	if r.ContextLength != 1000000 {
		t.Fatalf("ContextLength = %d, want 1000000 from fallback provider", r.ContextLength)
	}
}

// TestMatchModelsDevModelNotGreedyReverseSubstring verifies that searching
// "glm-5.2" does NOT match a model whose id is "glm-5" via reverse substring.
func TestMatchModelsDevModelNotGreedyReverseSubstring(t *testing.T) {
	providers := map[string]interface{}{
		"p": map[string]interface{}{
			"id":   "p",
			"name": "P",
			"models": map[string]interface{}{
				"glm-5": map[string]interface{}{
					"id":   "glm-5",
					"name": "glm-5",
					"limit": map[string]interface{}{"context": 202752, "output": 131072},
				},
			},
		},
	}
	raw := map[string]json.RawMessage{}
	for k, v := range providers {
		b, _ := json.Marshal(v)
		raw[k] = b
	}
	// No exact "glm-5.2" exists; "glm-5" must NOT match via reverse substring,
	// so the result is not found.
	if r := matchModelsDevModel(raw, "glm-5.2", ""); r != nil {
		t.Fatalf("expected nil (glm-5 must not match glm-5.2 via reverse substring), got %+v", r)
	}
}

// TestModelsDevLiveGLM52Scoped is a live canary against models.dev: searching
// GLM-5.2 under Ollama Cloud must return the glm-5.2 entry, not glm-5.
func TestModelsDevLiveGLM52Scoped(t *testing.T) {
	r, err := fetchModelsDevModel("glm-5.2:cloud", "ollama_cloud")
	if err != nil {
		t.Skipf("live models.dev fetch failed: %v", err)
	}
	if r == nil {
		t.Fatal("expected a match, got nil")
	}
	if !strings.Contains(r.ID, "5.2") {
		t.Fatalf("ID = %q, want a glm-5.2 entry (must not return glm-5)", r.ID)
	}
	if r.ContextLength == 202752 {
		t.Fatalf("ContextLength = 202752 (the glm-5 partial value); the exact glm-5.2 match was not selected")
	}
}