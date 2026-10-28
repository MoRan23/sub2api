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
	for id, displayName := range map[string]string{"gpt-6.1-sol": "GPT-6.1 Sol", "gpt-6.1-terra": "GPT-6.1 Terra", "gpt-6-sol": "GPT-6 Sol", "gpt-6-luna": "GPT-6 Luna"} {
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
	require.Equal(t, DefaultModels[0].ID, DefaultTestModel)
	require.NotContains(t, DefaultModelIDs(), "gpt-5.4")
	require.NotContains(t, DefaultModelIDs(), "gpt-5.4-mini")
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
	require.False(t, IsGPT6SolOrLunaModelSpelling("gpt-6.1-sol"), "a new model must not inherit GPT-6 Sol/Luna pricing or sampling exceptions")
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

func TestGPT61SolIdentityAndEffort(t *testing.T) {
	require.Contains(t, DefaultModelIDs(), "gpt-6.1-sol")
	for _, id := range []string{"gpt-6.1-sol", "openai/gpt-6.1-sol-max", "GPT_6.1_SOL", "gpt-6.1-sol-openai-compact"} {
		require.True(t, IsGPT61SolModelSpelling(id), id)
		require.False(t, IsGPT6SolOrLunaModelSpelling(id), id)
	}
	for _, id := range []string{"gpt-6.1", "gpt-6.1-solitude", "gpt-6.1-sol-preview", "gpt-6-sol"} {
		require.False(t, IsGPT61SolModelSpelling(id), id)
	}
	for _, effort := range []string{"", "low", "medium", "high", "xhigh", "max"} {
		require.NoError(t, ValidateGPT61SolReasoningEffort("gpt-6.1-sol", effort))
	}
	for _, effort := range []string{"none", "minimal"} {
		require.Error(t, ValidateGPT61SolReasoningEffort("gpt-6.1-sol", effort))
		require.NoError(t, ValidateGPT61SolReasoningEffort("gpt-6-sol", effort))
	}
	require.Contains(t, CodexBaseInstructionsForModel("gpt-6.1-sol"), "based on GPT-6")
}

func TestGPT61TerraModelVariantCompatibility(t *testing.T) {
	require.Contains(t, DefaultModelIDs(), "gpt-6.1-terra")
	for _, suffix := range []string{"", "-low", "-medium", "-high", "-xhigh", "-max", "-ultra", "-2026-10-28", "-openai-compact", "-ultra-openai-compact", "-2026-10-28-openai-compact"} {
		model := "OPENAI/" + strings.ToUpper(strings.ReplaceAll("gpt-6.1-terra"+suffix, "-", "_"))
		require.True(t, IsGPT61TerraModelSpelling(model), model)
		require.False(t, IsGPT61SolModelSpelling(model), model)
		require.False(t, IsGPT6SolOrLunaModelSpelling(model), model)
	}
	for _, model := range []string{"gpt-6.1", "gpt-6.1-terrain", "gpt-6.1-terra-custom", "gpt-6.1-terra-latest", "gpt-6.1-terra-preview", "gpt-6.1-terra-2026-99-28", "gpt-6.1-terra-2026-02-29", "gpt-6.1-terra-high-low", "gpt-6.1-terra-openai-compact-openai-compact", "gpt-6-astra", "gpt-6.1-sol"} {
		require.False(t, IsGPT61TerraModelSpelling(model), model)
	}
}
