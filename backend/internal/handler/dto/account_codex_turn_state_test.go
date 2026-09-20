package dto

import (
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAccountCodexTurnStateDTOHidesManagedRuntime(t *testing.T) {
	account := &service.Account{ID: 1, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
		Extra: map[string]any{
			service.CodexTurnStateExtraKey:           map[string]any{"enabled": true, "account_type": "personal", "collector_proxy_id": float64(8)},
			service.CodexTurnStateGenerationExtraKey: "server-generation", "codex_turn_state_token": "secret-token", "usage": 3,
			service.CodexTurnStateCredentialEpochExtraKey: "private-credential-epoch",
		}}
	dto := AccountFromServiceShallow(account)
	require.True(t, dto.CodexTurnState.Enabled)
	require.Equal(t, int64(8), *dto.CodexTurnState.CollectorProxyID)
	require.Equal(t, map[string]any{"usage": 3}, dto.Extra)
	encoded, err := json.Marshal(dto)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "server-generation")
	require.NotContains(t, string(encoded), "secret-token")
	require.NotContains(t, string(encoded), "private-credential-epoch")
	compact := AccountListItemFromAccount(dto)
	require.Equal(t, dto.CodexTurnState, compact.CodexTurnState)
}

func TestAccountCodexTurnStateDTOListsOrderedProxiesWithoutPrivateRotationState(t *testing.T) {
	account := &service.Account{ID: 1, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
		Extra: map[string]any{
			service.CodexTurnStateExtraKey:              map[string]any{"enabled": true, "account_type": "personal", "collector_proxy_ids": []any{float64(9), float64(4)}},
			service.CodexTurnStateGenerationExtraKey:    "private-generation",
			"codex_turn_state_collector_attempt_id":     "private-attempt",
			"codex_turn_state_collector_extended_count": 2,
		}}
	full := AccountFromServiceShallow(account)
	require.Equal(t, []int64{9, 4}, full.CodexTurnState.CollectorProxyIDs)
	compact := AccountListItemFromAccount(full)
	require.Equal(t, full.CodexTurnState, compact.CodexTurnState)
	for _, value := range []any{full, compact} {
		encoded, err := json.Marshal(value)
		require.NoError(t, err)
		require.NotContains(t, string(encoded), "private-generation")
		require.NotContains(t, string(encoded), "private-attempt")
		require.NotContains(t, string(encoded), "collector_extended_count")
	}
}
