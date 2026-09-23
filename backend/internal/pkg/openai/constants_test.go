package openai

import (
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
