package openai

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDefaultModelsIncludeBareGPT56Alias(t *testing.T) {
	require.Contains(t, DefaultModelIDs(), "gpt-5.6")
}

func TestDefaultModelsIncludeGPT6Astra(t *testing.T) {
	require.Contains(t, DefaultModelIDs(), "gpt-6-astra")
	require.Contains(t, DefaultModelIDs(), "gpt-6")
	var displayName string
	for _, model := range DefaultModels {
		if model.ID == "gpt-6-astra" {
			displayName = model.DisplayName
			break
		}
	}
	require.Equal(t, "GPT-6 Astra", displayName)
}

func TestDefaultModelsIncludeGPT6SolAndLuna(t *testing.T) {
	for id, displayName := range map[string]string{"gpt-6-sol": "GPT-6 Sol", "gpt-6-luna": "GPT-6 Luna"} {
		matches := 0
		for _, model := range DefaultModels {
			if model.ID == id {
				matches++
				require.Equal(t, displayName, model.DisplayName)
			}
		}
		require.Equal(t, 1, matches, "model %s must appear exactly once", id)
	}
}

func TestDefaultModelsPreferConcreteGPT56SolForAccountTests(t *testing.T) {
	require.NotEmpty(t, DefaultModels)
	require.Equal(t, "gpt-5.6-sol", DefaultModels[0].ID)
}

func TestDefaultModelsIncludeGPTImage25(t *testing.T) {
	require.Contains(t, DefaultModelIDs(), "gpt-image-2.5-flare")
	require.Contains(t, DefaultModelIDs(), "gpt-image-2.5-sunburst")
}

func TestGPT6SolLunaModelIdentity(t *testing.T) {
	for _, model := range []string{"gpt-6-sol", "gpt-6-luna"} {
		require.Contains(t, DefaultModelIDs(), model)
		require.True(t, IsGPT6SolOrLunaModelSpelling(model))
	}
	require.False(t, IsGPT6SolOrLunaModelSpelling("gpt-6-astra"))
	require.False(t, IsGPT6SolOrLunaModelSpelling("gpt-6-solitude"))
	require.False(t, IsGPT6SolOrLunaModelSpelling("gpt-6-luna-preview"))
}

func TestDefaultModelIDsAreUnique(t *testing.T) {
	seen := make(map[string]bool)
	for _, model := range DefaultModelIDs() {
		require.False(t, seen[model], "duplicate default model %s", model)
		seen[model] = true
	}
}

func TestGPT6SolLunaModelVariantCompatibility(t *testing.T) {
	for _, family := range []string{"gpt-6-sol", "gpt-6-luna"} {
		for _, suffix := range []string{"", "-minimal", "-ultra", "-2026-09-23", "-openai-compact", "-ultra-openai-compact", "-2026-09-23-openai-compact"} {
			require.True(t, IsGPT6SolOrLunaModelSpelling("OPENAI/"+strings.ToUpper(family+suffix)), family+suffix)
		}
		for _, suffix := range []string{"-custom", "-latest", "-preview", "-2026-99-23", "-high-low", "-openai-compact-openai-compact"} {
			require.False(t, IsGPT6SolOrLunaModelSpelling(family+suffix), family+suffix)
		}
	}
}
