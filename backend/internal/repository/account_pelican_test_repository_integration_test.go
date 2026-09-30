//go:build integration

package repository

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestPelicanRepositoryVersionIsolationAndRetention(t *testing.T) {
	ctx := context.Background()
	repo := NewAccountCandyTestRepository(integrationDB)
	id := candyIntegrationAccount(t)
	old := candyIntegrationBatch(t, repo, id)
	key := uuid.NewString()
	// Simulate a pre-upgrade snapshot (which did not contain prompt_version).
	_, err := integrationDB.ExecContext(ctx, `UPDATE account_candy_test_batches SET prompt_version='candy-v1', idempotency_key=$2, request_snapshot=jsonb_build_object('account_ids',jsonb_build_array($3::bigint),'model','fixture','reasoning_effort',''),finished_at=NOW(),counts='{"normal":1}' WHERE id=$1`, old.ID, key, id)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE account_candy_test_items SET prompt_version='candy-v1',status='normal',response_text='old answer',finished_at=NOW()+INTERVAL '1 day' WHERE batch_id=$1`, old.ID)
	require.NoError(t, err)
	_, err = repo.GetBatch(ctx, old.ID, 1, 1)
	require.ErrorIs(t, err, service.ErrCandyTestNotFound)
	require.ErrorIs(t, repo.Cancel(ctx, old.ID, nil), service.ErrCandyTestNotFound)
	_, err = repo.Create(ctx, &service.CandyTestCreateRequest{AccountIDs: []int64{id}, Model: "fixture", IdempotencyKey: key}, nil)
	require.ErrorIs(t, err, service.ErrCandyTestIdempotencyConflict)
	for range 7 {
		candyIntegrationBatch(t, repo, id)
		claimed, claimErr := repo.Claim(ctx)
		require.NoError(t, claimErr)
		require.NotNil(t, claimed)
		claimed.Status = "generated"
		claimed.ResponseText = "```html\n<html><body><svg></svg></body></html>\n```"
		ok, completeErr := repo.Complete(ctx, claimed)
		require.NoError(t, completeErr)
		require.True(t, ok)
	}
	history, err := repo.History(ctx, id)
	require.NoError(t, err)
	require.Len(t, history, 5, "filter before LIMIT, regardless of the newer old-version row")
	for _, row := range history {
		require.Equal(t, service.CandyTestPromptVersion, row.PromptVersion)
		require.Equal(t, "<html><body><svg></svg></body></html>", row.HTML)
	}
	page, err := repo.GetBatch(ctx, history[0].BatchID, 1, 1)
	require.NoError(t, err)
	require.Equal(t, history[0].HTML, page.Items[0].HTML)
	summary, err := repo.Summaries(ctx, []int64{id})
	require.NoError(t, err)
	require.Equal(t, history[0].ID, summary[id].Latest.ID, "filter before ranking")
	require.Empty(t, summary[id].Latest.ResponseText)
	require.Empty(t, summary[id].Latest.HTML)
	var status, response string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT status,response_text FROM account_candy_test_items WHERE batch_id=$1`, old.ID).Scan(&status, &response))
	require.Equal(t, "normal", status)
	require.Equal(t, "old answer", response, "old history must not be regraded or pruned")
}

func TestPelicanMigrationCancelsOldWorkWithoutReplay(t *testing.T) {
	ctx := context.Background()
	repo := NewAccountCandyTestRepository(integrationDB)
	old := candyIntegrationBatch(t, repo, candyIntegrationAccount(t), candyIntegrationAccount(t))
	_, err := integrationDB.ExecContext(ctx, `UPDATE account_candy_test_batches SET prompt_version='candy-v1' WHERE id=$1`, old.ID)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE account_candy_test_items SET prompt_version='candy-v1' WHERE batch_id=$1`, old.ID)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE account_candy_test_items SET status='running',started_at=NOW(),claim_id=$2,lease_until=NOW()+INTERVAL '30 seconds' WHERE id=$1`, old.Items[0].ID, uuid.NewString())
	require.NoError(t, err)
	claimed, err := repo.Claim(ctx)
	require.NoError(t, err)
	require.Nil(t, claimed, "new workers cannot claim old queued tests even before migration")
	active := candyIntegrationBatch(t, repo, candyIntegrationAccount(t))
	// Apply in a transaction, as the migration runner does. This is a disposable
	// integration database, with all workers stopped before the upgrade.
	body, err := migrations.FS.ReadFile("262_account_pelican_tests.sql")
	require.NoError(t, err)
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, string(body))
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
	var cancelled int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM account_candy_test_items WHERE batch_id=$1 AND status='cancelled' AND failure_code='test_replaced' AND claim_id IS NULL AND lease_until IS NULL AND finished_at IS NOT NULL`, old.ID).Scan(&cancelled))
	require.Equal(t, 2, cancelled)
	claimed, err = repo.Claim(ctx)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	require.Equal(t, active.ID, claimed.BatchID)
}
