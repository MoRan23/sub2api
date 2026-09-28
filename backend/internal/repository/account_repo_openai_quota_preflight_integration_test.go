//go:build integration

package repository

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func newQuotaPreflightFixture(t *testing.T) (*accountRepository, *service.Account) {
	t.Helper()
	ctx := context.Background()
	repo := newAccountRepositoryWithSQL(testEntClient(t), integrationDB, nil)
	account := &service.Account{
		Name: "quota-preflight-" + t.Name(), Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
		Status: service.StatusActive, Schedulable: true, Credentials: openAIRefreshExpectedAuthForRepoTest(),
		Extra: map[string]any{"auto_reset_credit_enabled": true, "unrelated": "preserved"},
	}
	require.NoError(t, repo.Create(ctx, account))
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM scheduler_outbox WHERE account_id=$1", account.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM accounts WHERE id=$1", account.ID)
	})
	slot, err := repo.GetOpenAIOAuthOSCredential(ctx, account.ID, "")
	require.NoError(t, err)
	require.NotNil(t, slot)
	account.OpenAIOAuthCredentialOwnerID = slot.OwnerAccountID
	account.OpenAIOAuthAuthorizationGeneration = slot.AuthorizationGeneration
	account.OpenAIOAuthCredentialRevision = slot.Revision
	_, err = integrationDB.ExecContext(ctx, "DELETE FROM scheduler_outbox WHERE account_id=$1", account.ID)
	require.NoError(t, err)
	return repo, account
}

func quotaPreflightFailureUpdates() map[string]any {
	return map[string]any{service.OpenAIAutoResetCreditStateExtraKey: &service.OpenAIAutoResetCreditState{
		Status: service.OpenAIAutoResetStatusFailed, ErrorCode: "query_failed", LastResultAt: time.Now().UTC().Format(time.RFC3339),
	}}
}

func TestQuotaPreflightCASPublishesFailureToScheduler(t *testing.T) {
	ctx := context.Background()
	repo, account := newQuotaPreflightFixture(t)
	cache := NewSchedulerCache(testRedis(t))
	repo.schedulerCache = cache
	require.NoError(t, cache.SetAccount(ctx, account))
	updates := quotaPreflightFailureUpdates()
	updated, err := repo.CompareAndUpdateOpenAIAutoResetPreflight(ctx, account.ID, account, nil, updates)
	require.NoError(t, err)
	require.True(t, updated)
	stored, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.Equal(t, "preserved", stored.Extra["unrelated"])
	require.Equal(t, "failed", stored.Extra[service.OpenAIAutoResetCreditStateExtraKey].(map[string]any)["status"])
	cached, err := cache.GetAccount(ctx, account.ID)
	require.NoError(t, err)
	require.NotNil(t, cached)
	require.Equal(t, stored.Extra[service.OpenAIAutoResetCreditStateExtraKey], cached.Extra[service.OpenAIAutoResetCreditStateExtraKey])
	var outboxCount int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT count(*) FROM scheduler_outbox WHERE account_id=$1", account.ID).Scan(&outboxCount))
	require.Equal(t, 1, outboxCount)
}

func TestQuotaPreflightCASRejectsLateFailure(t *testing.T) {
	for _, change := range []string{"authorization", "state", "disabled", "paused", "missing_generation", "wrong_owner"} {
		t.Run(change, func(t *testing.T) {
			ctx := context.Background()
			repo, account := newQuotaPreflightFixture(t)
			var err error
			switch change {
			case "authorization":
				_, err = integrationDB.ExecContext(ctx, "UPDATE account_openai_oauth_credentials SET authorization_generation=gen_random_uuid() WHERE account_id=$1", account.ID)
			case "state":
				_, err = integrationDB.ExecContext(ctx, `UPDATE accounts SET extra=extra || '{"codex_auto_reset_credit_state":{"status":"success"}}'::jsonb WHERE id=$1`, account.ID)
			case "disabled":
				_, err = integrationDB.ExecContext(ctx, `UPDATE accounts SET extra=extra || '{"auto_reset_credit_enabled":false}'::jsonb WHERE id=$1`, account.ID)
			case "paused":
				_, err = integrationDB.ExecContext(ctx, "UPDATE accounts SET schedulable=false WHERE id=$1", account.ID)
			case "missing_generation":
				account.OpenAIOAuthAuthorizationGeneration = ""
				account.OpenAIOAuthCredentialOwnerID = 0
			case "wrong_owner":
				account.OpenAIOAuthCredentialOwnerID = account.ID + 1
			}
			require.NoError(t, err)
			var before, after string
			require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT extra::text FROM accounts WHERE id=$1", account.ID).Scan(&before))
			updated, err := repo.CompareAndUpdateOpenAIAutoResetPreflight(ctx, account.ID, account, nil, quotaPreflightFailureUpdates())
			require.NoError(t, err)
			require.False(t, updated)
			require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT extra::text FROM accounts WHERE id=$1", account.ID).Scan(&after))
			require.Equal(t, before, after)
			var outboxCount int
			require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT count(*) FROM scheduler_outbox WHERE account_id=$1", account.ID).Scan(&outboxCount))
			require.Zero(t, outboxCount)
		})
	}
}

func TestQuotaPreflightCASAllowsOrdinaryRefresh(t *testing.T) {
	ctx := context.Background()
	repo, account := newQuotaPreflightFixture(t)
	updated, err := repo.PatchOpenAIOAuthOSCredentialsIfUnchanged(ctx, account.ID, "windows", account.OpenAIOAuthAuthorizationGeneration,
		account.OpenAIOAuthCredentialRevision, nil, map[string]any{"access_token": "refreshed-synthetic-access", "refresh_token": "refreshed-synthetic-refresh"}, nil)
	require.NoError(t, err)
	require.True(t, updated)
	updated, err = repo.CompareAndUpdateOpenAIAutoResetPreflight(ctx, account.ID, account, nil, quotaPreflightFailureUpdates())
	require.NoError(t, err)
	require.True(t, updated, "ordinary token rotation must not invalidate authorization attribution")
}

func TestQuotaPreflightCASConcurrentInstancesHaveOneWinner(t *testing.T) {
	ctx := context.Background()
	repo, account := newQuotaPreflightFixture(t)
	other := newAccountRepositoryWithSQL(testEntClient(t), integrationDB, nil)
	type result struct {
		applied bool
		err     error
	}
	results := make(chan result, 12)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range 12 {
		workerRepo := repo
		if i%2 != 0 {
			workerRepo = other
		}
		wg.Go(func() {
			<-start
			applied, err := workerRepo.CompareAndUpdateOpenAIAutoResetPreflight(ctx, account.ID, account, nil, quotaPreflightFailureUpdates())
			results <- result{applied, err}
		})
	}
	close(start)
	wg.Wait()
	close(results)
	winners := 0
	for result := range results {
		require.NoError(t, result.err)
		if result.applied {
			winners++
		}
	}
	require.Equal(t, 1, winners)
	var outboxCount int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT count(*) FROM scheduler_outbox WHERE account_id=$1", account.ID).Scan(&outboxCount))
	require.Equal(t, 1, outboxCount)
}

func TestQuotaPreflightCASSeesAuthorizationCommittedDuringLockWait(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	repo, account := newQuotaPreflightFixture(t)
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	var id int64
	require.NoError(t, tx.QueryRowContext(ctx, "SELECT id FROM accounts WHERE id=$1 FOR NO KEY UPDATE", account.ID).Scan(&id))
	_, err = tx.ExecContext(ctx, "UPDATE account_openai_oauth_credentials SET authorization_generation=gen_random_uuid() WHERE account_id=$1", account.ID)
	require.NoError(t, err)
	type result struct {
		applied bool
		err     error
	}
	done := make(chan result, 1)
	go func() {
		applied, err := repo.CompareAndUpdateOpenAIAutoResetPreflight(ctx, account.ID, account, nil, quotaPreflightFailureUpdates())
		done <- result{applied, err}
	}()
	require.Eventually(t, func() bool {
		var waiting int
		err := integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM pg_stat_activity WHERE wait_event_type='Lock' AND query LIKE 'SELECT id FROM accounts WHERE id=$1 AND deleted_at IS NULL%'`).Scan(&waiting)
		return err == nil && waiting > 0
	}, 3*time.Second, 10*time.Millisecond)
	require.NoError(t, tx.Commit())
	completed := <-done
	require.NoError(t, completed.err)
	require.False(t, completed.applied, "a blocked query must not persist a failure under a replaced authorization")
}
