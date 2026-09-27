package repository

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAccountConfigurationPatchRejectsRetiredStateAndStaleIdentity(t *testing.T) {
	stale := map[string]any{
		"codex_turn_state":                map[string]any{"enabled": true, "use_ticket_proxy": true},
		"codex_turn_state_generation":     "obsolete",
		"openai_pinned_installation_id":   "stale-installation",
		"openai_installation_pin_enabled": false,
		"enable_tls_fingerprint":          false,
		"tls_fingerprint_profile_id":      int64(99),
		"note":                            "updated",
	}
	patch := accountConfigurationExtraPatch(context.Background(), []int64{1}, stale)
	require.Equal(t, map[string]any{"note": "updated"}, patch)
	require.Contains(t, stale, "codex_turn_state")
}

func TestOAuthCredentialOwnerGuardSurvivesCacheRetirement(t *testing.T) {
	expression := guardedAccountCredentialsExpression("$1::jsonb")
	for _, invariant := range []string{"platform = 'openai'", "type = 'oauth'", "parent_account_id IS NULL", "'personalaccesstoken'", "'agentidentity'", "'access_token'", "'refresh_token'", "'user_agent'"} {
		require.Contains(t, expression, invariant)
	}
	require.NotContains(t, expression, "codex_turn_state")
}
