//go:build integration

package repository

import (
	"context"
	"maps"
	"sync"
	"testing"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestCodexEngineModelsIntegrationTransitionsAndCache(t *testing.T) {
	cache := NewSchedulerCache(testRedis(t))
	repo, a := engineCapacityFixture(t, cache)
	ctx := t.Context()
	_, err := integrationDB.ExecContext(ctx, `UPDATE accounts SET credentials=credentials || '{"model_mapping":{"local alias":"remote","old":"old"},"other":"keep"}'::jsonb WHERE id=$1`, a.ID)
	require.NoError(t, err)
	a, err = repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	for _, models := range [][]string{{"remote", "Name with spaces"}, {}, {"restored"}} {
		original := a
		changed, err := repo.ApplyCodexEngineModels(ctx, a, models)
		require.NoError(t, err)
		require.True(t, changed)
		a, err = repo.GetByID(ctx, a.ID)
		require.NoError(t, err)
		expected := map[string]string{}
		for _, m := range models {
			expected[m] = m
		}
		require.True(t, sameEngineModelJSON(expected, a.Credentials["model_mapping"]))
		require.Equal(t, "keep", a.Credentials["other"])
		require.True(t, sameEngineModelJSON(a.Extra[service.CodexEngineCapacitySyncExtraKey], original.Extra[service.CodexEngineCapacitySyncExtraKey]))
		cached, err := cache.GetAccount(ctx, a.ID)
		require.NoError(t, err)
		require.NotNil(t, cached)
		require.True(t, sameEngineModelJSON(a.Extra[service.CodexEngineModelsSyncExtraKey], cached.Extra[service.CodexEngineModelsSyncExtraKey]))
		before := engineCapacityState(t, a.ID)
		changed, err = repo.ApplyCodexEngineModels(ctx, a, models)
		require.NoError(t, err)
		require.False(t, changed)
		require.Equal(t, before, engineCapacityState(t, a.ID))
		changed, err = repo.ApplyCodexEngineModels(ctx, original, []string{"late"})
		require.NoError(t, err)
		require.False(t, changed)
	}
}

func TestCodexEngineModelsIntegrationIndependentCapacityAndConcurrentCAS(t *testing.T) {
	repo, a := engineCapacityFixture(t, nil)
	changed, err := repo.ApplyCodexEngineCapacity(t.Context(), a, 0)
	require.NoError(t, err)
	require.True(t, changed)
	changed, err = repo.ApplyCodexEngineModels(t.Context(), a, []string{"initial"})
	require.NoError(t, err)
	require.True(t, changed, "capacity pause must not invalidate model observation")
	a, err = repo.GetByID(t.Context(), a.ID)
	require.NoError(t, err)
	var wg sync.WaitGroup
	results := make(chan bool, 2)
	errors := make(chan error, 2)
	for _, model := range []string{"one", "two"} {
		wg.Go(func() {
			ok, e := repo.ApplyCodexEngineModels(t.Context(), a, []string{model})
			results <- ok
			errors <- e
		})
	}
	wg.Wait()
	close(results)
	close(errors)
	winners := 0
	for ok := range results {
		if ok {
			winners++
		}
	}
	for err := range errors {
		require.NoError(t, err)
	}
	require.Equal(t, 1, winners)
	current, err := repo.GetByID(t.Context(), a.ID)
	require.NoError(t, err)
	require.False(t, current.Schedulable)
}

func TestCodexEngineModelsIntegrationSnapshotProtectionAndManualABA(t *testing.T) {
	repo, a := engineCapacityFixture(t, nil)
	stale := *a
	stale.Credentials = maps.Clone(a.Credentials)
	stale.Extra = maps.Clone(a.Extra)
	changed, err := repo.ApplyCodexEngineModels(t.Context(), a, []string{"synced"})
	require.NoError(t, err)
	require.True(t, changed)
	stale.Name = "background name"
	require.NoError(t, repo.Update(t.Context(), &stale))
	current, err := repo.GetByID(t.Context(), a.ID)
	require.NoError(t, err)
	require.True(t, sameEngineModelJSON(map[string]string{"synced": "synced"}, current.Credentials["model_mapping"]))
	expectedCopy := *current
	expected := &expectedCopy
	adminCtx := service.WithAccountModelMappingWriteIntent(t.Context(), []int64{a.ID})
	current.Credentials = maps.Clone(current.Credentials)
	current.Credentials["model_mapping"] = map[string]string{"manual": "manual"}
	require.NoError(t, repo.Update(adminCtx, current))
	current, err = repo.GetByID(t.Context(), a.ID)
	require.NoError(t, err)
	current.Credentials["model_mapping"] = map[string]string{"synced": "synced"}
	require.NoError(t, repo.Update(adminCtx, current))
	changed, err = repo.ApplyCodexEngineModels(t.Context(), expected, []string{"late"})
	require.NoError(t, err)
	require.False(t, changed)
	current, err = repo.GetByID(t.Context(), a.ID)
	require.NoError(t, err)
	changed, err = repo.ApplyCodexEngineModels(t.Context(), current, []string{"next round"})
	require.NoError(t, err)
	require.True(t, changed)
}

func TestCodexEngineModelsIntegrationCredentialPatchGuards(t *testing.T) {
	for _, bulk := range []bool{false, true} {
		t.Run(map[bool]string{false: "credentials", true: "bulk"}[bulk], func(t *testing.T) {
			repo, a := engineCapacityFixture(t, nil)
			changed, err := repo.ApplyCodexEngineModels(t.Context(), a, []string{"synced"})
			require.NoError(t, err)
			require.True(t, changed)
			a, err = repo.GetByID(t.Context(), a.ID)
			require.NoError(t, err)
			expected := a
			creds := maps.Clone(a.Credentials)
			creds["model_mapping"] = map[string]string{"manual": "manual"}
			update := func(ctx context.Context) {
				if bulk {
					_, err = repo.BulkUpdate(ctx, []int64{a.ID}, service.AccountBulkUpdate{Credentials: creds})
					require.NoError(t, err)
				} else {
					require.NoError(t, repo.UpdateCredentials(ctx, a.ID, creds))
				}
			}
			update(t.Context())
			current, err := repo.GetByID(t.Context(), a.ID)
			require.NoError(t, err)
			require.True(t, sameEngineModelJSON(current.Credentials["model_mapping"], a.Credentials["model_mapping"]))
			update(service.WithAccountModelMappingWriteIntent(t.Context(), []int64{a.ID}))
			changed, err = repo.ApplyCodexEngineModels(t.Context(), expected, []string{"late"})
			require.NoError(t, err)
			require.False(t, changed)
			current, err = repo.GetByID(t.Context(), a.ID)
			require.NoError(t, err)
			require.True(t, sameEngineModelJSON(creds["model_mapping"], current.Credentials["model_mapping"]))
		})
	}
}

func TestCodexEngineModelsIntegrationRollbackAndIdentity(t *testing.T) {
	repo, a := engineCapacityFixture(t, nil)
	before := engineCapacityState(t, a.ID)
	tx, err := repo.client.Tx(t.Context())
	require.NoError(t, err)
	changed, err := repo.ApplyCodexEngineModels(dbent.NewTxContext(t.Context(), tx), a, []string{"rollback"})
	require.NoError(t, err)
	require.True(t, changed)
	require.NoError(t, tx.Rollback())
	require.Equal(t, before, engineCapacityState(t, a.ID))
	_, err = integrationDB.ExecContext(t.Context(), `UPDATE accounts SET credentials=jsonb_set(credentials,'{api_key}','"changed"'::jsonb) WHERE id=$1`, a.ID)
	require.NoError(t, err)
	changed, err = repo.ApplyCodexEngineModels(t.Context(), a, []string{"late"})
	require.NoError(t, err)
	require.False(t, changed)
}

func TestCodexEngineModelsIntegrationInitialManualABA(t *testing.T) {
	repo, a := engineCapacityFixture(t, nil)
	adminCtx := service.WithAccountModelMappingWriteIntent(t.Context(), []int64{a.ID})
	for _, mapping := range []any{map[string]string{"temporary": "temporary"}, nil} {
		current, err := repo.GetByID(t.Context(), a.ID)
		require.NoError(t, err)
		if mapping == nil {
			delete(current.Credentials, "model_mapping")
		} else {
			current.Credentials["model_mapping"] = mapping
		}
		require.NoError(t, repo.Update(adminCtx, current))
	}
	changed, err := repo.ApplyCodexEngineModels(t.Context(), a, []string{"late"})
	require.NoError(t, err)
	require.False(t, changed)
	current, err := repo.GetByID(t.Context(), a.ID)
	require.NoError(t, err)
	changed, err = repo.ApplyCodexEngineModels(t.Context(), current, []string{"fresh"})
	require.NoError(t, err)
	require.True(t, changed)
	before := engineCapacityState(t, a.ID)
	require.NoError(t, repo.UpdateExtra(t.Context(), a.ID, map[string]any{service.CodexEngineModelsSyncExtraKey: map[string]any{"known": true, "model_count": 0, "identity": "forged"}}))
	require.Equal(t, before, engineCapacityState(t, a.ID))
}
