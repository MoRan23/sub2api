//go:build integration

package repository

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestCodexTelemetryMaintenanceDeletedBacklogCannotStarveLiveOwner(t *testing.T) {
	store, deleted, now := telemetryStoreFixture(t)
	ctx := context.Background()
	policy, err := store.ReadPolicy(ctx)
	require.NoError(t, err)
	// Reproduce an existing backlog larger than the per-tick selection limit.
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO codex_telemetry_pools(id,owner_account_id,os_family,installation_id,seed,next_due_at)
		SELECT gen_random_uuid(),$1,'windows',i::text,gen_random_uuid(),$2 FROM generate_series(1,101) i`, deleted.OwnerAccountID, now.Add(-time.Hour))
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO codex_telemetry_activities(pool_id,kind,activity_key,state,due_at)
		SELECT id,'metrics','old','{}',$2 FROM codex_telemetry_pools WHERE owner_account_id=$1`, deleted.OwnerAccountID, now.Add(-time.Hour))
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO codex_telemetry_batches(id,pool_id,policy_epoch,account_id,type,source,payload,not_before,created_at)
		SELECT gen_random_uuid(),id,$2,$1,'metrics','observed','{}',$3,$3 FROM codex_telemetry_pools WHERE owner_account_id=$1`, deleted.OwnerAccountID, policy.Epoch, now.Add(-time.Hour))
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE accounts SET deleted_at=NOW() WHERE id=$1`, deleted.OwnerAccountID)
	require.NoError(t, err)
	var liveID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO accounts(name,platform,type,credentials,extra) VALUES('telemetry-live','openai','oauth','{}','{}') RETURNING id`).Scan(&liveID))
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM accounts WHERE id=$1`, liveID)
	})
	live := service.CodexTelemetryPoolKey{OwnerAccountID: liveID, OSFamily: "windows", InstallationID: uuid.NewString()}
	batchID := uuid.NewString()
	_, err = store.TransactPool(ctx, live, now, func(tx *service.CodexTelemetryPoolTransaction) error {
		a := telemetryStoreActivity("metrics", "current", `{"count":1}`, now)
		tx.Activities[service.CodexTelemetryActivityMapKey(a.Kind, a.Key)] = a
		tx.Batches = append(tx.Batches, telemetryStoreBatch(batchID, now))
		return nil
	})
	require.NoError(t, err)
	keys, err := store.ListDuePools(ctx, now, 100)
	require.NoError(t, err)
	require.Equal(t, []service.CodexTelemetryPoolKey{live}, keys)
	batches, err := store.ClaimBatches(ctx, now, 100)
	require.NoError(t, err)
	require.Len(t, batches, 1)
	require.Equal(t, batchID, batches[0].ID)
	_, err = store.SyncPolicy(ctx, false, true, true)
	require.NoError(t, err)
	require.NoError(t, store.Maintain(ctx, now))
	var remaining int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM codex_telemetry_pools WHERE owner_account_id=$1`, deleted.OwnerAccountID).Scan(&remaining))
	require.Equal(t, 1, remaining, "repair must be bounded to 100 pools per maintenance run")
	require.NoError(t, store.Maintain(ctx, now))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM codex_telemetry_pools WHERE owner_account_id=$1`, deleted.OwnerAccountID).Scan(&remaining))
	require.Zero(t, remaining)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM codex_telemetry_batches WHERE account_id=$1`, deleted.OwnerAccountID).Scan(&remaining))
	require.Zero(t, remaining)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM codex_telemetry_pools WHERE owner_account_id=$1`, liveID).Scan(&remaining))
	require.Equal(t, 1, remaining)
}

func TestCodexTelemetryMaintenanceFiltersConvertedOwners(t *testing.T) {
	for _, test := range []struct{ name, mutation string }{
		{"api_key", `type='apikey'`},
		{"other_platform", `platform='anthropic'`},
		{"personal_token", `credentials='{"auth_mode":"personal_access_token"}'`},
		{"agent_identity", `credentials='{"openai_auth_mode":"agentidentity"}'`},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, key, now := telemetryStoreFixture(t)
			ctx := context.Background()
			_, err := store.TransactPool(ctx, key, now, func(tx *service.CodexTelemetryPoolTransaction) error {
				a := telemetryStoreActivity("metrics", "current", `{}`, now)
				tx.Activities[service.CodexTelemetryActivityMapKey(a.Kind, a.Key)] = a
				tx.Batches = append(tx.Batches, telemetryStoreBatch(uuid.NewString(), now))
				return nil
			})
			require.NoError(t, err)
			_, err = integrationDB.ExecContext(ctx, `UPDATE accounts SET `+test.mutation+` WHERE id=$1`, key.OwnerAccountID)
			require.NoError(t, err)
			keys, err := store.ListDuePools(ctx, now, 1)
			require.NoError(t, err)
			require.Empty(t, keys)
			batches, err := store.ClaimBatches(ctx, now, 1)
			require.NoError(t, err)
			require.Empty(t, batches)
			_, err = store.TransactPool(ctx, key, now, func(*service.CodexTelemetryPoolTransaction) error {
				t.Fatal("unavailable owner reached callback")
				return nil
			})
			require.ErrorIs(t, err, service.ErrCodexTelemetryOwnerUnavailable)
			require.NoError(t, store.Maintain(ctx, now))
			var count int
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM codex_telemetry_pools WHERE owner_account_id=$1`, key.OwnerAccountID).Scan(&count))
			require.Zero(t, count)
		})
	}
}

func TestCodexTelemetryMaintenanceDueSummaryIsAtomic(t *testing.T) {
	store, key, now := telemetryStoreFixture(t)
	ctx := context.Background()
	pool, err := store.TransactPool(ctx, key, now, func(tx *service.CodexTelemetryPoolTransaction) error {
		for _, a := range []service.CodexTelemetryActivity{
			telemetryStoreActivity("metrics", "first", `{"count":1}`, now),
			telemetryStoreActivity("metrics", "later", `{"count":2}`, now.Add(time.Hour)),
		} {
			tx.Activities[service.CodexTelemetryActivityMapKey(a.Kind, a.Key)] = a
		}
		return nil
	})
	require.NoError(t, err)
	assertDue := func(want time.Time) {
		t.Helper()
		var due sql.NullTime
		require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT next_due_at FROM codex_telemetry_pools WHERE id=$1`, pool.ID).Scan(&due))
		require.Equal(t, !want.IsZero(), due.Valid)
		if due.Valid {
			require.True(t, want.Equal(due.Time), "want %s got %s", want, due.Time)
		}
	}
	assertDue(now)
	_, err = store.TransactPool(ctx, key, now, func(tx *service.CodexTelemetryPoolTransaction) error {
		clear(tx.Activities)
		return errors.New("rollback")
	})
	require.Error(t, err)
	assertDue(now)
	_, err = store.TransactPool(ctx, key, now, func(tx *service.CodexTelemetryPoolTransaction) error {
		delete(tx.Activities, service.CodexTelemetryActivityMapKey("metrics", "first"))
		return nil
	})
	require.NoError(t, err)
	assertDue(now.Add(time.Hour))
	keys, err := store.ListDuePools(ctx, now, 100)
	require.NoError(t, err)
	require.Empty(t, keys)
	_, err = store.TransactPool(ctx, key, now, func(tx *service.CodexTelemetryPoolTransaction) error { clear(tx.Activities); return nil })
	require.NoError(t, err)
	assertDue(time.Time{})
}

func TestCodexTelemetryMaintenanceRevalidatesInPlaceHistoryMutation(t *testing.T) {
	store, key, now := telemetryStoreFixture(t)
	ctx := context.Background()
	pool, err := store.TransactPool(ctx, key, now, func(tx *service.CodexTelemetryPoolTransaction) error {
		a := telemetryStoreActivity("metrics", "current", `{"counter":"value"}`, now)
		tx.Activities[service.CodexTelemetryActivityMapKey(a.Kind, a.Key)] = a
		return nil
	})
	require.NoError(t, err)
	_, err = store.TransactPool(ctx, key, now, func(tx *service.CodexTelemetryPoolTransaction) error {
		a := tx.Activities[service.CodexTelemetryActivityMapKey("metrics", "current")]
		copy(a.Data, `{"api_key":"value"}`)
		return nil
	})
	require.Error(t, err, "an in-place edit must not pass the unchanged-history shortcut")
	var original bool
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT state ? 'counter' AND NOT state ? 'api_key'
		FROM codex_telemetry_activities WHERE pool_id=$1 AND kind='metrics' AND activity_key='current'`, pool.ID).Scan(&original))
	require.True(t, original, "rejected history edits must roll back atomically")
}

func TestCodexTelemetryMaintenanceMigrationBackfillsSchedule(t *testing.T) {
	store, key, now := telemetryStoreFixture(t)
	ctx := context.Background()
	pool, err := store.TransactPool(ctx, key, now, func(tx *service.CodexTelemetryPoolTransaction) error {
		a := telemetryStoreActivity("metrics", "current", `{}`, now)
		tx.Activities[service.CodexTelemetryActivityMapKey(a.Kind, a.Key)] = a
		return nil
	})
	require.NoError(t, err)
	tx := testTx(t)
	_, err = tx.ExecContext(ctx, `UPDATE codex_telemetry_pools SET next_due_at=NULL WHERE id=$1`, pool.ID)
	require.NoError(t, err)
	body, err := migrations.FS.ReadFile("271_codex_telemetry_pool_due.sql")
	require.NoError(t, err)
	for i := 0; i < 2; i++ {
		_, err = tx.ExecContext(ctx, string(body))
		require.NoError(t, err)
		var due time.Time
		require.NoError(t, tx.QueryRowContext(ctx, `SELECT next_due_at FROM codex_telemetry_pools WHERE id=$1`, pool.ID).Scan(&due))
		require.True(t, now.Equal(due))
	}
	var plan string
	require.NoError(t, tx.QueryRowContext(ctx, `EXPLAIN (FORMAT JSON) `+codexTelemetryDuePoolsQuery, now, 100).Scan(&plan))
	require.NotContains(t, plan, "codex_telemetry_activities", "the recurring scheduler must not scan activity history")
	var fullPoolIndex bool
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT i.indpred IS NULL AND i.indisvalid
		FROM pg_index i JOIN pg_class c ON c.oid=i.indexrelid
		WHERE c.relname='codex_telemetry_batches_pool_idx'`).Scan(&fullPoolIndex))
	require.True(t, fullPoolIndex, "pool cleanup must index terminal batches as well as pending batches")
}
