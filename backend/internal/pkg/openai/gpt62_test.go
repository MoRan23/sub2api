package openai

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGPT62KnownVariantsAndInstructionTemplates(t *testing.T) {
	for _, base := range []string{"gpt-6.2-sol", "gpt-6.2-astra"} {
		for _, suffix := range []string{"", "-max", "-openai-compact", "-max-openai-compact", "-2026-11-17", "-2026-11-17-openai-compact"} {
			model := "openai/" + base + suffix
			require.Equal(t, base, GPT62ModelID(model))
			require.Equal(t, CodexBaseInstructionsForModel(base), CodexBaseInstructionsForModel(model))
			require.True(t, RequiresResponsesForToolCalls(model))
		}
		for _, suffix := range []string{"-preview", "-unknown", "-2026-99-99"} {
			require.Empty(t, GPT62ModelID(base+suffix))
		}
		for _, effort := range []string{"none", "minimal", " NONE "} {
			require.Error(t, ValidateGPT62ReasoningEffort(base, effort))
		}
		for _, effort := range []string{"low", "medium", "high", "xhigh", "max"} {
			require.NoError(t, ValidateGPT62ReasoningEffort(base, effort))
		}
	}
	require.NotEqual(t, CodexBaseInstructionsForModel("gpt-6.2-sol"), CodexBaseInstructionsForModel("gpt-6.1-sol"))
}

func BenchmarkGPT62Instructions(b *testing.B) {
	for b.Loop() {
		_ = CodexBaseInstructionsForModel("gpt-6.2-sol")
	}
}
