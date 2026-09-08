package service

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCodexImageInstructionMarkersAreNeutralAndIdempotent(t *testing.T) {
	tests := []struct {
		name       string
		model      string
		marker     string
		text       string
		apply      func(map[string]any) bool
		legacyName string
	}{
		{
			name:       "image bridge",
			model:      "gpt-5.4",
			marker:     codexImageGenerationBridgeMarker,
			text:       codexImageGenerationBridgeText,
			apply:      applyCodexImageGenerationBridgeInstructions,
			legacyName: "sub2api-codex-image-generation",
		},
		{
			name:       "spark limitation",
			model:      "gpt-5.3-codex-spark",
			marker:     codexSparkImageUnsupportedMarker,
			text:       codexSparkImageUnsupportedText,
			apply:      applyCodexSparkImageUnsupportedInstructions,
			legacyName: "sub2api-codex-spark-image-unsupported",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.NotContains(t, strings.ToLower(tt.text), "sub2api")
			newBody := func(instructions string) map[string]any {
				return map[string]any{
					"model":        tt.model,
					"instructions": instructions,
					"tools":        []any{map[string]any{"type": "image_generation"}},
				}
			}
			t.Run("new guidance", func(t *testing.T) {
				body := newBody("existing instructions")
				require.True(t, tt.apply(body))
				require.Equal(t, "existing instructions\n\n"+tt.text, body["instructions"])
				require.False(t, tt.apply(body))
				require.Equal(t, 1, strings.Count(body["instructions"].(string), tt.marker))
			})
			t.Run("replayed legacy guidance", func(t *testing.T) {
				neutralName := strings.Trim(tt.marker, "<>")
				legacyText := strings.ReplaceAll(tt.text, neutralName, tt.legacyName)
				body := newBody("existing instructions\n\n" + legacyText)
				require.True(t, tt.apply(body))
				require.Equal(t, "existing instructions\n\n"+tt.text, body["instructions"])
				require.False(t, tt.apply(body))
				require.NotContains(t, body["instructions"], "sub2api")
			})
		})
	}
}

func TestCodexImageInstructionMarkersNormalizeWithoutInjectingSkippedGuidance(t *testing.T) {
	legacy := "before\n<sub2api-codex-image-generation>retained guidance</sub2api-codex-image-generation>\nafter"
	body := map[string]any{
		"model":        "gpt-5.4",
		"instructions": legacy,
		"tools": []any{
			map[string]any{"type": "function", "name": "image_gen.imagegen"},
		},
	}
	require.True(t, applyCodexImageGenerationBridgeInstructions(body))
	require.Equal(t, "before\n<codex-image-generation>retained guidance</codex-image-generation>\nafter", body["instructions"])
	require.False(t, applyCodexImageGenerationBridgeInstructions(body))
	require.Len(t, body["tools"], 1)
}

func TestCodexImageInstructionMarkerNormalizationPreservesUnrelatedContent(t *testing.T) {
	for _, body := range []map[string]any{
		nil,
		{},
		{"instructions": 123},
		{"instructions": "inspect sub2api-codex-other without changing user text"},
	} {
		require.False(t, normalizeCodexImageInstructionMarkers(body))
	}
}
