//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func createAccountCleanupTelemetry(t *testing.T, store service.CodexTelemetryStore, key service.CodexTelemetryPoolKey, now time.Time) string {
	t.Helper()
	pool, err := store.TransactPool(context.Background(), key, now, func(tx *service.CodexTelemetryPoolTransaction) error {
		activity := telemetryStoreActivity("maintenance", "cleanup", `{}`, now)
		tx.Activities[service.CodexTelemetryActivityMapKey(activity.Kind, activity.Key)] = activity
		tx.Batches = append(tx.Batches, telemetryStoreBatch(uuid.NewString(), now))
		return nil
	})
	require.NoError(t, err)
	return pool.ID
}

func assertAccountCleanupTelemetryCounts(t *testing.T, poolID string, want int) {
	t.Helper()
	var pools, activities, batches int
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), `SELECT
		(SELECT count(*) FROM codex_telemetry_pools WHERE id=$1::uuid),
		(SELECT count(*) FROM codex_telemetry_activities WHERE pool_id=$1::uuid),
		(SELECT count(*) FROM codex_telemetry_batches WHERE pool_id=$1::uuid)`, poolID).Scan(&pools, &activities, &batches))
	require.Equal(t, want, pools, "pool")
	require.Equal(t, want, activities, "activities")
	require.Equal(t, want, batches, "batches")
}

func TestAccountDeleteClearsOnlyOwnedTelemetry(t *testing.T) {
	store, key, now := telemetryStoreFixture(t)
	firstPool := createAccountCleanupTelemetry(t, store, key, now)
	otherOS := key
	otherOS.OSFamily = "linux"
	secondPool := createAccountCleanupTelemetry(t, store, otherOS, now)
	var otherOwner int64
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), `INSERT INTO accounts(name,platform,type,credentials,extra)
		VALUES($1,'openai','oauth','{}','{}') RETURNING id`, t.Name()+"-other").Scan(&otherOwner))
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM accounts WHERE id=$1`, otherOwner)
	})
	otherKey := key
	otherKey.OwnerAccountID = otherOwner
	otherPool := createAccountCleanupTelemetry(t, store, otherKey, now)
	repo := newAccountRepositoryWithSQL(testEntClient(t), integrationDB, nil)

	require.NoError(t, repo.Delete(context.Background(), key.OwnerAccountID))
	var deleted bool
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), `SELECT deleted_at IS NOT NULL FROM accounts WHERE id=$1`, key.OwnerAccountID).Scan(&deleted))
	require.True(t, deleted, "account deletion remains a soft deletion")
	assertAccountCleanupTelemetryCounts(t, firstPool, 0)
	assertAccountCleanupTelemetryCounts(t, secondPool, 0)
	assertAccountCleanupTelemetryCounts(t, otherPool, 1)

	require.NoError(t, repo.Delete(context.Background(), key.OwnerAccountID), "repeated deletion remains idempotent")
	_, err := store.TransactPool(context.Background(), key, now, func(*service.CodexTelemetryPoolTransaction) error {
		t.Fatal("deleted owner admitted a telemetry writer")
		return nil
	})
	require.Error(t, err)
	assertAccountCleanupTelemetryCounts(t, firstPool, 0)
}

func TestAccountDeleteTelemetryCleanupRollsBackWithAccount(t *testing.T) {
	store, key, now := telemetryStoreFixture(t)
	poolID := createAccountCleanupTelemetry(t, store, key, now)
	tx := testEntTx(t)
	repo := newAccountRepositoryWithSQL(tx.Client(), tx, nil)
	require.NoError(t, repo.Delete(context.Background(), key.OwnerAccountID))
	rows, err := tx.QueryContext(context.Background(), `SELECT count(*) FROM codex_telemetry_pools WHERE id=$1::uuid`, poolID)
	require.NoError(t, err)
	require.True(t, rows.Next())
	var remaining int
	require.NoError(t, rows.Scan(&remaining))
	require.NoError(t, rows.Close())
	require.Zero(t, remaining, "cleanup participates in the caller's transaction")
	require.NoError(t, tx.Rollback())

	var deleted bool
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), `SELECT deleted_at IS NOT NULL FROM accounts WHERE id=$1`, key.OwnerAccountID).Scan(&deleted))
	require.False(t, deleted)
	assertAccountCleanupTelemetryCounts(t, poolID, 1)
}

func TestAccountDeleteTelemetryWriterCannotRecreatePool(t *testing.T) {
	store, key, now := telemetryStoreFixture(t)
	poolID := createAccountCleanupTelemetry(t, store, key, now)
	tx := testEntTx(t)
	repo := newAccountRepositoryWithSQL(tx.Client(), tx, nil)
	require.NoError(t, repo.Delete(context.Background(), key.OwnerAccountID))

	// A writer arriving while deletion is uncommitted must wait for the account
	// lock, then observe the deletion instead of resurrecting private telemetry.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	started, done := make(chan struct{}), make(chan error, 1)
	go func() {
		close(started)
		_, err := store.TransactPool(ctx, key, now, func(*service.CodexTelemetryPoolTransaction) error {
			return nil
		})
		done <- err
	}()
	<-started
	require.NoError(t, tx.Commit())
	require.Error(t, <-done)
	assertAccountCleanupTelemetryCounts(t, poolID, 0)
}
