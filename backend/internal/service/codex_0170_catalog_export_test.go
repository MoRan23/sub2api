package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCodex0170CatalogPreservesCustomEffortsAndAudio(t *testing.T) {
	ids, metadata, err := extractUpstreamModelCatalog([]byte(`{"models":[{"slug":"custom-model","default_reasoning_level":"providerBudget","supported_reasoning_levels":[{"effort":"persistent"},{"effort":"disabled"},{"effort":"providerBudget"},{"effort":"32768"}],"input_modalities":["text","audio","image","audio"]}]}`), false)
	require.NoError(t, err)
	require.Equal(t, []string{"custom-model"}, ids)
	require.Equal(t, "providerBudget", metadata["custom-model"].DefaultReasoningLevel)
	require.Equal(t, []string{"persistent", "disabled", "providerBudget", "32768"}, metadata["custom-model"].SupportedReasoningLevels)
	require.Equal(t, []string{"text", "audio", "image"}, metadata["custom-model"].InputModalities)
}

func TestCodex0170ExportPreservesOpaqueEfforts(t *testing.T) {
	for _, value := range []string{"providerBudget", "disabled", "persistent", "18446744073709551615", "custom_value_longer_than_twenty_characters"} {
		require.Equal(t, value, usageExportEffort(value))
	}
}
