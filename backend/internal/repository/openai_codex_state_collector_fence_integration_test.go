//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func collectorPublicationFixture(t *testing.T, ctx context.Context) (service.CodexTurnStateKey, service.CodexTurnStateRecord) {
	t.Helper()
	key := createCodexStateFixture(t)
	repo := NewOpenAICodexStateRepository(integrationDB, integrationRedis)
	now := time.Now().UTC().Truncate(time.Microsecond)
	record, err := repo.BeginBusiness(ctx, key, "seed", now, now.Add(time.Minute))
	require.NoError(t, err)
	require.NoError(t, repo.MarkBusinessSent(ctx, key, now))
	require.NoError(t, repo.EndBusiness(ctx, key, "seed"))
	record.ModelPolicyRevision = codexStateModelPolicyRevisionForTest(t)
	record.CollectorPublication = true
	record.EncryptedToken, record.Source = "collector-target", "collector"
	record.IssuedAt, record.ExpiresAt = now, now.Add(time.Hour)
	record.TokenLength, record.CipherBlocks = 292, 10
	return key, *record
}

func TestCodexCollectorPublicationPostgresRejectsBusinessLeaseWithoutVersionChange(t *testing.T) {
	ctx := context.Background()
	key, result := collectorPublicationFixture(t, ctx)
	repo := NewOpenAICodexStateRepository(integrationDB, integrationRedis)
	now := time.Now().UTC()
	business, err := repo.BeginBusiness(ctx, key, "natural", now, now.Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, result.Version, business.Version, "business activity alone does not publish a new token version")
	saved, err := repo.SaveCAS(ctx, result, result.Version)
	require.NoError(t, err)
	require.False(t, saved, "losing a Redis cancellation cannot permit collector publication during natural business")
	current, err := repo.Get(ctx, key)
	require.NoError(t, err)
	require.Empty(t, current.EncryptedToken)
	require.Equal(t, result.Version, current.Version)
	require.NoError(t, repo.EndBusiness(ctx, key, "natural"))
	saved, err = repo.SaveCAS(ctx, result, result.Version)
	require.NoError(t, err)
	require.True(t, saved, "a completed business lease does not permanently block future collector publication")
}

func TestCodexCollectorPublicationPostgresFreshLeaseSnapshotAfterStateLock(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	key, result := collectorPublicationFixture(t, ctx)
	repo := NewOpenAICodexStateRepository(integrationDB, integrationRedis)
	// Reproduce BeginBusiness's state-row-before-lease order, pausing it before
	// its lease insert. The collector must wait for this transaction and then
	// take a new statement snapshot to see the lease committed while it waited.
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	var blockerPID int
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&blockerPID))
	var version int64
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT version FROM openai_codex_state
		WHERE owner_account_id=$1 AND model=$2 FOR UPDATE`, key.OwnerAccountID, key.Model).Scan(&version))
	type publication struct {
		saved bool
		err   error
	}
	finished := make(chan publication, 1)
	go func() {
		saved, err := repo.SaveCAS(ctx, result, result.Version)
		finished <- publication{saved: saved, err: err}
	}()
	require.Eventually(t, func() bool {
		var blocked bool
		err := integrationDB.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity
			WHERE wait_event_type='Lock' AND $1 = ANY(pg_blocking_pids(pid))
			AND query LIKE 'SELECT version FROM openai_codex_state%')`, blockerPID).Scan(&blocked)
		return err == nil && blocked
	}, 3*time.Second, 10*time.Millisecond, "collector must serialize behind the pending natural request's state lock")
	_, err = tx.ExecContext(ctx, `INSERT INTO openai_codex_state_business_leases
		(owner_account_id,model,generation,attempt_id,lease_until) VALUES ($1,$2,$3,'late-natural',$4)`,
		key.OwnerAccountID, key.Model, key.Generation, time.Now().Add(time.Minute))
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
	outcome := <-finished
	require.NoError(t, outcome.err)
	require.False(t, outcome.saved, "a stale pre-lock snapshot must not hide the newly committed business lease")
	current, err := repo.Get(ctx, key)
	require.NoError(t, err)
	require.Empty(t, current.EncryptedToken)
	require.Equal(t, version, current.Version)
}
