package service

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestMergedGPT6AliasesShareCatalogAndWireSemantics(t *testing.T) {
	for _, family := range []string{"gpt-6-astra", "gpt-6-sol", "gpt-6-luna"} {
		for _, suffix := range []string{"-minimal", "-ultra", "-2026-09-23", "-openai-compact"} {
			model := family + suffix
			t.Run(model, func(t *testing.T) {
				require.Equal(t, family, normalizeCodexModel(model))
				descriptor := newConfiguredCodexModelDescriptor(model)
				require.EqualValues(t, 272000, descriptor.ContextWindow)
				require.EqualValues(t, 872000, descriptor.MaxContextWindow)
				require.Equal(t, openai.CodexBaseInstructionsForModel(family), descriptor.ModelMessages.InstructionsTemplate)

				body := []byte(`{"model":"public","reasoning":{"mode":"pro","effort":"ultra"},"temperature":0.7,"include":["message.output_text.logprobs","reasoning.encrypted_content"]}`)
				wire, changed, err := normalizeOpenAIResponsesReasoningMode(body, model)
				require.NoError(t, err)
				require.Equal(t, "public", gjson.GetBytes(wire, "model").String())
				require.Equal(t, "pro", gjson.GetBytes(wire, "reasoning.mode").String())
				require.Equal(t, "ultra", gjson.GetBytes(wire, "reasoning.effort").String())
				require.Equal(t, family != "gpt-6-astra", changed)
				require.Equal(t, family == "gpt-6-astra", gjson.GetBytes(wire, "temperature").Exists())
				// Catalog support does not change the local Ultra forwarding rule.
				require.Equal(t, normalizeOpenAIReasoningEffort("ultra"), normalizeOpenAIReasoningEffortForModel("ultra", model))
			})
		}
	}
}

func TestMergedGPT6NonePreservesSamplingAndUnknownFamilyStaysConservative(t *testing.T) {
	for _, model := range []string{"gpt-6-sol-minimal", "gpt-6-luna-2026-09-23"} {
		body := []byte(`{"reasoning":{"mode":"pro","effort":"none"},"temperature":0.7,"top_p":0.9,"include":["message.output_text.logprobs"]}`)
		wire, changed, err := normalizeOpenAIResponsesReasoningMode(body, model)
		require.NoError(t, err)
		require.False(t, changed)
		require.Equal(t, body, wire)
	}
	for _, model := range []string{"gpt-6-sol-custom", "gpt-6-astra-preview", "gpt-6-luna-2026-99-23"} {
		require.Empty(t, normalizeKnownOpenAICodexModel(model))
		require.Nil(t, bundledCodexModelDefault(model))
	}
}

func TestMergedRequestIntegrityPreservesAllGPT6ReasoningModes(t *testing.T) {
	for _, model := range []string{"gpt-6-astra", "gpt-6-sol-minimal", "gpt-6-luna-openai-compact"} {
		reasoning := map[string]any{"mode": "pro"}
		body := map[string]any{"model": model, "reasoning": reasoning}
		rules := make(map[string]bool)
		canonicalizeRequestIntegrityCodex(body, rules)
		require.Equal(t, map[string]any{"mode": "pro"}, reasoning)
		require.NotContains(t, rules, "reasoning_mode_alias")
	}
}
