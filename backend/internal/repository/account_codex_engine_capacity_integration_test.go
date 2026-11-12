//go:build integration

package repository

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func engineCapacityFixture(t *testing.T, cache service.SchedulerCache) (*accountRepository, *service.Account) {
	t.Helper()
	client := testEntClient(t)
	a := mustCreateAccount(t, client, &service.Account{
		Name: "engine-capacity-integration", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
		Concurrency: 50, Status: service.StatusActive, Schedulable: true,
		Credentials: map[string]any{"base_url": "https://engine.invalid/v1", "api_key": "fixture-only-key"},
		Extra:       map[string]any{service.OpenAIAPIKeyModeExtraKey: "codex_engine"},
	})
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM scheduler_outbox WHERE account_id=$1`, a.ID)
		_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM accounts WHERE id=$1`, a.ID)
	})
	repo := newAccountRepositoryWithSQL(client, integrationDB, cache)
	loaded, err := repo.GetByID(t.Context(), a.ID)
	require.NoError(t, err)
	return repo, loaded
}

type engineCapacityDBState struct {
	Concurrency int
	Schedulable bool
	Revision    int64
	UpdatedAt   time.Time
	Extra       string
	Outbox      int
}

func engineCapacityState(t *testing.T, id int64) engineCapacityDBState {
	t.Helper()
	var state engineCapacityDBState
	require.NoError(t, integrationDB.QueryRowContext(t.Context(), `SELECT concurrency,schedulable,scheduling_revision,updated_at,extra::text,
		(SELECT count(*) FROM scheduler_outbox WHERE account_id=$1) FROM accounts WHERE id=$1`, id).
		Scan(&state.Concurrency, &state.Schedulable, &state.Revision, &state.UpdatedAt, &state.Extra, &state.Outbox))
	return state
}

func TestCodexEngineCapacityIntegrationTransitionsAndCache(t *testing.T) {
	cache := NewSchedulerCache(testRedis(t))
	repo, account := engineCapacityFixture(t, cache)
	for _, limit := range []int{30, 20, 0, 30} {
		before := engineCapacityState(t, account.ID)
		changed, err := repo.ApplyCodexEngineCapacity(t.Context(), account, limit)
		require.NoError(t, err)
		require.True(t, changed)
		current, err := repo.GetByID(t.Context(), account.ID)
		require.NoError(t, err)
		if limit == 0 {
			require.False(t, current.Schedulable)
			require.False(t, current.IsSchedulable())
			require.Equal(t, 20, current.Concurrency, "zero must never become unlimited concurrency")
		} else {
			require.True(t, current.Schedulable)
			require.Equal(t, limit, current.Concurrency)
		}
		wantRevision := before.Revision
		if before.Schedulable != current.Schedulable {
			wantRevision++
		}
		require.Equal(t, wantRevision, current.SchedulingRevision, "real migration 272 trigger runs only for scheduling changes")
		cached, err := cache.GetAccount(t.Context(), account.ID)
		require.NoError(t, err)
		require.NotNil(t, cached)
		require.Equal(t, current.Schedulable, cached.Schedulable)
		require.Equal(t, current.Concurrency, cached.Concurrency)
		require.Greater(t, engineCapacityState(t, account.ID).Outbox, 0)

		unchanged := engineCapacityState(t, account.ID)
		changed, err = repo.ApplyCodexEngineCapacity(t.Context(), current, limit)
		require.NoError(t, err)
		require.False(t, changed, "repeated observations must not write")
		require.Equal(t, unchanged, engineCapacityState(t, account.ID))
		account = current
	}
}

func TestCodexEngineCapacityIntegrationManualPauseOwnership(t *testing.T) {
	for _, alreadyPaused := range []bool{false, true} {
		t.Run(fmt.Sprintf("initially_paused_%t", alreadyPaused), func(t *testing.T) {
			repo, account := engineCapacityFixture(t, nil)
			if alreadyPaused {
				require.NoError(t, repo.SetSchedulable(t.Context(), account.ID, false))
				var err error
				account, err = repo.GetByID(t.Context(), account.ID)
				require.NoError(t, err)
			}
			changed, err := repo.ApplyCodexEngineCapacity(t.Context(), account, 0)
			require.NoError(t, err)
			require.True(t, changed)
			paused, err := repo.GetByID(t.Context(), account.ID)
			require.NoError(t, err)
			require.False(t, paused.Schedulable)
			require.NoError(t, repo.SetSchedulable(t.Context(), account.ID, false))
			manual, err := repo.GetByID(t.Context(), account.ID)
			require.NoError(t, err)
			require.Equal(t, paused.SchedulingRevision+1, manual.SchedulingRevision)
			changed, err = repo.ApplyCodexEngineCapacity(t.Context(), paused, 30)
			require.NoError(t, err)
			require.False(t, changed, "manual repeat pause rejects the in-flight response")
			changed, err = repo.ApplyCodexEngineCapacity(t.Context(), manual, 30)
			require.NoError(t, err)
			require.True(t, changed)
			after := engineCapacityState(t, account.ID)
			require.False(t, after.Schedulable, "new observations do not recover a manual pause")
			require.Equal(t, 30, after.Concurrency)
			require.Equal(t, manual.SchedulingRevision, after.Revision)
		})
	}
}

func TestCodexEngineCapacityIntegrationRejectsLateIdentity(t *testing.T) {
	for _, tc := range []struct{ name, query string }{
		{"url", `UPDATE accounts SET credentials=jsonb_set(credentials,'{base_url}','"https://replacement.invalid"') WHERE id=$1`},
		{"key", `UPDATE accounts SET credentials=jsonb_set(credentials,'{api_key}','"replacement-key"') WHERE id=$1`},
		{"type", `UPDATE accounts SET type='oauth' WHERE id=$1`},
		{"mode", `UPDATE accounts SET extra=jsonb_set(extra,'{openai_api_key_mode}','"generic"') WHERE id=$1`},
		{"manual_schedule", `UPDATE accounts SET schedulable=schedulable WHERE id=$1`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, account := engineCapacityFixture(t, nil)
			_, err := integrationDB.ExecContext(t.Context(), tc.query, account.ID)
			require.NoError(t, err)
			before := engineCapacityState(t, account.ID)
			changed, err := repo.ApplyCodexEngineCapacity(t.Context(), account, 0)
			require.NoError(t, err)
			require.False(t, changed)
			require.Equal(t, before, engineCapacityState(t, account.ID))
		})
	}
}

func TestCodexEngineCapacityIntegrationConcurrentCAS(t *testing.T) {
	repo, snapshot := engineCapacityFixture(t, nil)
	type result struct {
		changed bool
		err     error
	}
	results := make(chan result, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, limit := range []int{20, 30} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			changed, err := repo.ApplyCodexEngineCapacity(t.Context(), snapshot, limit)
			results <- result{changed, err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	writes := 0
	for result := range results {
		require.NoError(t, result.err)
		if result.changed {
			writes++
		}
	}
	require.Equal(t, 1, writes, "only one response to the same account snapshot may commit")
	require.Contains(t, []int{20, 30}, engineCapacityState(t, snapshot.ID).Concurrency)
}

func TestCodexEngineCapacityIntegrationRejectsObservationABA(t *testing.T) {
	repo, initial := engineCapacityFixture(t, nil)
	_, err := repo.ApplyCodexEngineCapacity(t.Context(), initial, 20)
	require.NoError(t, err)
	stale, err := repo.GetByID(t.Context(), initial.ID)
	require.NoError(t, err)
	changed, err := repo.ApplyCodexEngineCapacity(t.Context(), stale, 30)
	require.NoError(t, err)
	require.True(t, changed)
	latest, err := repo.GetByID(t.Context(), initial.ID)
	require.NoError(t, err)
	changed, err = repo.ApplyCodexEngineCapacity(t.Context(), latest, 20)
	require.NoError(t, err)
	require.True(t, changed)
	before := engineCapacityState(t, initial.ID)
	changed, err = repo.ApplyCodexEngineCapacity(t.Context(), stale, 0)
	require.NoError(t, err)
	require.False(t, changed, "20 -> 30 -> 20 does not make the original 20 observation current again")
	require.Equal(t, before, engineCapacityState(t, initial.ID))
}

func TestCodexEngineCapacityIntegrationIdentityEditWaitsForFreshRecovery(t *testing.T) {
	for _, path := range []string{"full", "credentials", "bulk"} {
		for _, field := range []string{"base_url", "api_key"} {
			t.Run(path+"/"+field, func(t *testing.T) {
				repo, account := engineCapacityFixture(t, nil)
				changed, err := repo.ApplyCodexEngineCapacity(t.Context(), account, 0)
				require.NoError(t, err)
				require.True(t, changed)
				stale, err := repo.GetByID(t.Context(), account.ID)
				require.NoError(t, err)
				edit, err := repo.GetByID(t.Context(), account.ID)
				require.NoError(t, err)
				if field == "base_url" {
					edit.Credentials[field] = "https://replacement.invalid/v1"
				} else {
					edit.Credentials[field] = "new-fixture-key"
				}
				switch path {
				case "full":
					err = repo.Update(t.Context(), edit)
				case "credentials":
					err = repo.UpdateCredentials(t.Context(), account.ID, edit.Credentials)
				case "bulk":
					_, err = repo.BulkUpdate(t.Context(), []int64{account.ID}, service.AccountBulkUpdate{Credentials: edit.Credentials})
				}
				require.NoError(t, err)
				require.False(t, engineCapacityState(t, account.ID).Schedulable, "editing an endpoint must not enable it before a fresh observation")
				before := engineCapacityState(t, account.ID)
				changed, err = repo.ApplyCodexEngineCapacity(t.Context(), stale, 30)
				require.NoError(t, err)
				require.False(t, changed, "the previous endpoint's result is discarded")
				require.Equal(t, before, engineCapacityState(t, account.ID))
				fresh, err := repo.GetByID(t.Context(), account.ID)
				require.NoError(t, err)
				changed, err = repo.ApplyCodexEngineCapacity(t.Context(), fresh, 30)
				require.NoError(t, err)
				require.True(t, changed)
				require.True(t, engineCapacityState(t, account.ID).Schedulable, "fresh capacity may recover only this sync's pause")
			})
		}
	}
}

func TestCodexEngineCapacityIntegrationMappingEditRetainsOwnership(t *testing.T) {
	for _, path := range []string{"full", "credentials", "bulk"} {
		t.Run(path, func(t *testing.T) {
			repo, account := engineCapacityFixture(t, nil)
			_, err := repo.ApplyCodexEngineCapacity(t.Context(), account, 0)
			require.NoError(t, err)
			stale, err := repo.GetByID(t.Context(), account.ID)
			require.NoError(t, err)
			edit, err := repo.GetByID(t.Context(), account.ID)
			require.NoError(t, err)
			edit.Credentials["model_mapping"] = map[string]any{"client-model": "upstream-model"}
			adminCtx := service.WithAccountModelMappingWriteIntent(t.Context(), []int64{account.ID})
			switch path {
			case "full":
				err = repo.Update(adminCtx, edit)
			case "credentials":
				err = repo.UpdateCredentials(adminCtx, account.ID, edit.Credentials)
			case "bulk":
				_, err = repo.BulkUpdate(adminCtx, []int64{account.ID}, service.AccountBulkUpdate{Credentials: edit.Credentials})
			}
			require.NoError(t, err)
			changed, err := repo.ApplyCodexEngineCapacity(t.Context(), stale, 30)
			require.NoError(t, err)
			require.True(t, changed, "unrelated model mapping is not an Engine capacity identity")
			after, err := repo.GetByID(t.Context(), account.ID)
			require.NoError(t, err)
			require.True(t, after.Schedulable)
			require.Equal(t, "upstream-model", after.Credentials["model_mapping"].(map[string]any)["client-model"])
		})
	}
}

func TestCodexEngineCapacityIntegrationProxyEditRejectsLateResult(t *testing.T) {
	repo, account := engineCapacityFixture(t, nil)
	proxy := mustCreateProxy(t, integrationEntClient, &service.Proxy{Name: "capacity-proxy", Host: "original.invalid", Port: 8080})
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM proxies WHERE id=$1`, proxy.ID)
	})
	account.ProxyID = &proxy.ID
	require.NoError(t, repo.Update(t.Context(), account))
	account, err := repo.GetByID(t.Context(), account.ID)
	require.NoError(t, err)
	_, err = repo.ApplyCodexEngineCapacity(t.Context(), account, 0)
	require.NoError(t, err)
	stale, err := repo.GetByID(t.Context(), account.ID)
	require.NoError(t, err)
	proxy.Host = "replacement.invalid"
	require.NoError(t, newProxyRepositoryWithSQL(integrationEntClient, integrationDB).Update(t.Context(), proxy))
	before := engineCapacityState(t, account.ID)
	require.False(t, before.Schedulable)
	changed, err := repo.ApplyCodexEngineCapacity(t.Context(), stale, 30)
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, before, engineCapacityState(t, account.ID))
	fresh, err := repo.GetByID(t.Context(), account.ID)
	require.NoError(t, err)
	changed, err = repo.ApplyCodexEngineCapacity(t.Context(), fresh, 30)
	require.NoError(t, err)
	require.True(t, changed)
	require.True(t, engineCapacityState(t, account.ID).Schedulable)
}

func TestCodexEngineCapacityIntegrationTransactionAndOutboxRollback(t *testing.T) {
	cache := NewSchedulerCache(testRedis(t))
	repo, account := engineCapacityFixture(t, cache)
	before := engineCapacityState(t, account.ID)
	tx, err := integrationEntClient.Tx(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })
	changed, err := repo.ApplyCodexEngineCapacity(dbent.NewTxContext(t.Context(), tx), account, 0)
	require.NoError(t, err)
	require.True(t, changed)
	cached, err := cache.GetAccount(t.Context(), account.ID)
	require.NoError(t, err)
	require.Nil(t, cached, "cache is refreshed only after commit")
	require.NoError(t, tx.Rollback())
	require.Equal(t, before, engineCapacityState(t, account.ID))

	constraint := fmt.Sprintf("capacity_test_outbox_%d", account.ID)
	_, err = integrationDB.ExecContext(t.Context(), fmt.Sprintf(`ALTER TABLE scheduler_outbox ADD CONSTRAINT %s CHECK (account_id IS DISTINCT FROM %d)`, constraint, account.ID))
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "ALTER TABLE scheduler_outbox DROP CONSTRAINT IF EXISTS "+constraint)
	})
	changed, err = repo.ApplyCodexEngineCapacity(t.Context(), account, 0)
	require.Error(t, err, "outbox write failure must roll back the scheduling update as well")
	require.False(t, changed)
	require.Equal(t, before, engineCapacityState(t, account.ID))
	cached, err = cache.GetAccount(t.Context(), account.ID)
	require.NoError(t, err)
	require.Nil(t, cached)
}

func TestCodexEngineCapacityIntegrationCallerCommitPublishesCache(t *testing.T) {
	cache := NewSchedulerCache(testRedis(t))
	repo, account := engineCapacityFixture(t, cache)
	tx, err := integrationEntClient.Tx(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })
	changed, err := repo.ApplyCodexEngineCapacity(dbent.NewTxContext(t.Context(), tx), account, 0)
	require.NoError(t, err)
	require.True(t, changed)
	cached, err := cache.GetAccount(t.Context(), account.ID)
	require.NoError(t, err)
	require.Nil(t, cached)
	require.NoError(t, tx.Commit())
	cached, err = cache.GetAccount(t.Context(), account.ID)
	require.NoError(t, err)
	require.NotNil(t, cached)
	require.False(t, cached.Schedulable)
	require.Equal(t, 50, cached.Concurrency)
}

func TestCodexEngineCapacityIntegrationStaleFullSnapshot(t *testing.T) {
	repo, account := engineCapacityFixture(t, nil)
	stale, err := repo.GetByID(t.Context(), account.ID)
	require.NoError(t, err)
	_, err = repo.ApplyCodexEngineCapacity(t.Context(), account, 20)
	require.NoError(t, err)
	current, err := repo.GetByID(t.Context(), account.ID)
	require.NoError(t, err)
	_, err = repo.ApplyCodexEngineCapacity(t.Context(), current, 0)
	require.NoError(t, err)
	paused := engineCapacityState(t, account.ID)
	stale.Name = "background snapshot unrelated edit"
	require.NoError(t, repo.Update(t.Context(), stale))
	after, err := repo.GetByID(t.Context(), account.ID)
	require.NoError(t, err)
	require.Equal(t, 20, after.Concurrency)
	require.False(t, after.Schedulable)
	require.Equal(t, paused.Revision, after.SchedulingRevision)
	changed, err := repo.ApplyCodexEngineCapacity(t.Context(), after, 30)
	require.NoError(t, err)
	require.True(t, changed)
	require.True(t, engineCapacityState(t, account.ID).Schedulable)
}
