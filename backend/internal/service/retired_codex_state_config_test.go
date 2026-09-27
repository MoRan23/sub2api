package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStripRetiredCodexStateExtra(t *testing.T) {
	input := map[string]any{
		"codex_turn_state":              map[string]any{"enabled": true},
		"codex_turn_state_generation":   "old-generation",
		"codex_turn_state_token":        "obsolete-secret",
		"openai_pinned_installation_id": "keep-installation",
		"note":                          "keep",
		"codex_turn_states_other":       true,
	}
	actual := StripRetiredCodexStateExtra(input)
	require.Equal(t, map[string]any{"openai_pinned_installation_id": "keep-installation", "note": "keep", "codex_turn_states_other": true}, actual)
	require.Contains(t, input, "codex_turn_state", "cleaning a stale snapshot must not mutate its caller")
	require.Equal(t, actual, StripRetiredCodexStateExtra(actual), "cleanup must be idempotent")
	require.Nil(t, StripRetiredCodexStateExtra(nil))
}
