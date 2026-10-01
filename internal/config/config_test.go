package config

import "testing"

func TestRemoveModelsForProviders(t *testing.T) {
	remap := &ModelRemapping{
		DefaultModel: "removed-model",
		KnownModels: []ModelEntry{
			{ID: "removed-model", Provider: "custom_removed"},
			{ID: "kept-model", Provider: "ollama_cloud"},
			{ID: "same-id", Provider: "custom_removed"},
			{ID: "same-id", Provider: "ollama_cloud"},
		},
		Aliases: map[string]string{
			"removed-alias": "removed-model:cloud",
			"kept-alias":    "kept-model",
			"same-id-alias": "same-id",
		},
	}

	if !RemoveModelsForProviders(remap, map[string]struct{}{"custom_removed": {}}) {
		t.Fatal("expected remapping to change")
	}
	if remap.DefaultModel != "" {
		t.Fatalf("default model = %q, want empty", remap.DefaultModel)
	}
	if len(remap.KnownModels) != 2 {
		t.Fatalf("known models = %d, want 2", len(remap.KnownModels))
	}
	if _, ok := remap.Aliases["removed-alias"]; ok {
		t.Fatal("removed model alias was retained")
	}
	if _, ok := remap.Aliases["kept-alias"]; !ok {
		t.Fatal("alias for kept model was removed")
	}
	if _, ok := remap.Aliases["same-id-alias"]; !ok {
		t.Fatal("alias for model retained by another provider was removed")
	}
}

func TestRemoveModelsForProvidersNoMatch(t *testing.T) {
	remap := &ModelRemapping{
		KnownModels: []ModelEntry{{ID: "model", Provider: "ollama_cloud"}},
		Aliases:     map[string]string{"alias": "model"},
	}
	if RemoveModelsForProviders(remap, map[string]struct{}{"custom_missing": {}}) {
		t.Fatal("expected no change")
	}
}

// A default_model that is not in known_models is substituted by ResolveModel
// but resolves to an empty provider, so the request is sent to
// cfg.DefaultProvider. With an OAuth provider such as Codex that fails as an
// upstream 400 naming a model the caller never requested.
func TestResolvableDefaultModel(t *testing.T) {
	known := []ModelEntry{
		{ID: "glm-5.3:cloud", Provider: "ollama_cloud"},
		{ID: "gpt-5.6-luna", Provider: "codex_abc"},
	}

	tests := []struct {
		name  string
		remap *ModelRemapping
		want  string
	}{
		{
			name:  "keeps a resolvable bare id",
			remap: &ModelRemapping{DefaultModel: "gpt-5.6-luna", KnownModels: known},
			want:  "gpt-5.6-luna",
		},
		{
			name:  "keeps a resolvable provider-qualified route key",
			remap: &ModelRemapping{DefaultModel: "ollama_cloud/glm-5.3:cloud", KnownModels: known},
			want:  "ollama_cloud/glm-5.3:cloud",
		},
		{
			name:  "replaces a default that is absent from known_models",
			remap: &ModelRemapping{DefaultModel: "glm-5.1:cloud", KnownModels: known},
			want:  "glm-5.3:cloud",
		},
		{
			name:  "seeds a default when unset",
			remap: &ModelRemapping{KnownModels: known},
			want:  "glm-5.3:cloud",
		},
		{
			name:  "stays empty when there are no known models",
			remap: &ModelRemapping{DefaultModel: "glm-5.1:cloud"},
			want:  "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.remap.ResolvableDefaultModel(); got != tc.want {
				t.Fatalf("default model = %q, want %q", got, tc.want)
			}
		})
	}
}

// The value chosen must actually resolve, otherwise the guard is pointless.
func TestResolvableDefaultModelResolves(t *testing.T) {
	remap := &ModelRemapping{
		DefaultModel: "glm-5.1:cloud", // stale: not in known_models
		KnownModels: []ModelEntry{
			{ID: "glm-5.3:cloud", Provider: "ollama_cloud"},
		},
	}
	remap.DefaultModel = remap.ResolvableDefaultModel()

	model, provider := ResolveModel(remap, "some-unknown-model")
	if model != "glm-5.3:cloud" {
		t.Fatalf("model = %q, want %q", model, "glm-5.3:cloud")
	}
	if provider != "ollama_cloud" {
		t.Fatalf("provider = %q, want %q (empty provider falls back to DefaultProvider)", provider, "ollama_cloud")
	}
}
