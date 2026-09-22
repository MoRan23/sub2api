package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCodexTelemetryOSAuthorizationSeparatesReauthorizedTurnsAndMetrics(t *testing.T) {
	store := NewMemoryCodexTelemetryStore().(*MemoryCodexTelemetryStore)
	now := time.Now().UTC()
	old := runtimeTestAttempt(now, "windows", "same-turn", "same-sampling")
	old.profile.input.CredentialOS, old.profile.input.AuthorizationGeneration = "windows", "old-generation"
	runtimeTestMutation(t, store, old, nil, false)
	pool := store.pools[old.poolKey]
	result := CodexTelemetryResult{Status: "completed", HTTPStatus: 200, InputTokens: 11, OutputTokens: 3, FinishedAt: now.Add(time.Second)}
	runtimeTestMutation(t, store, old, &result, false)

	fresh := runtimeTestAttempt(now.Add(2*time.Second), "windows", "same-turn", "same-sampling")
	fresh.profile.input.CredentialOS, fresh.profile.input.AuthorizationGeneration = "windows", "new-generation"
	runtimeTestMutation(t, store, fresh, nil, false)
	result.InputTokens, result.FinishedAt = 29, now.Add(3*time.Second)
	runtimeTestMutation(t, store, fresh, &result, false)
	require.Equal(t, pool.ID, store.pools[fresh.poolKey].ID)
	require.Equal(t, pool.Seed, store.pools[fresh.poolKey].Seed)
	require.EqualValues(t, 11, runtimeTestReadTurn(t, store, old).Result.InputTokens)
	require.EqualValues(t, 29, runtimeTestReadTurn(t, store, fresh).Result.InputTokens)
	require.Equal(t, 1, runtimeTestReadTurn(t, store, fresh).Samples)
	_, err := store.TransactPool(context.Background(), fresh.poolKey, now, func(tx *CodexTelemetryPoolTransaction) error {
		metrics, err := loadRuntimeMetrics(tx)
		if err != nil {
			return err
		}
		require.Len(t, metrics.clients, 1, "reauthorization does not invent another client startup")
		generations := map[string]bool{}
		for _, state := range metrics.states {
			generations[state.profile.input.AuthorizationGeneration] = true
		}
		require.Equal(t, map[string]bool{"old-generation": true, "new-generation": true}, generations)
		return nil
	})
	require.NoError(t, err)
}

func TestCodexTelemetryOSAuthorizationDoesNotChangePoolOnTokenRefresh(t *testing.T) {
	a := runtimeTestAttempt(time.Now(), "linux", "turn", "sampling")
	a.profile.input.CredentialOS, a.profile.input.AuthorizationGeneration = "linux", "same-generation"
	b := a.profile
	b.input.AccessToken = "refreshed-secret"
	b.client.accessToken = "refreshed-secret"
	require.Equal(t, codexMetricClientKey(a.profile), codexMetricClientKey(b))
	require.Equal(t, codexMetricStateKey(a.profile), codexMetricStateKey(b))
	require.Equal(t, codexRuntimeTurnKey(a.profile, a.attemptID), codexRuntimeTurnKey(b, a.attemptID))
}
