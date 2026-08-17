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
