//go:build integration

package repository

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	dbmigrations "github.com/Wei-Shaw/sub2api/migrations"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestMigration241CodexTelemetryDefaultAndExplicitPreference(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()
	body, err := dbmigrations.FS.ReadFile("241_codex_telemetry_enabled.sql")
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `DELETE FROM settings WHERE key = 'codex_telemetry_enabled'`)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, string(body))
	require.NoError(t, err)
	var value string
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = 'codex_telemetry_enabled'`).Scan(&value))
	require.Equal(t, "true", value)
	_, err = tx.ExecContext(ctx, `UPDATE settings SET value = 'false' WHERE key = 'codex_telemetry_enabled'`)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, string(body))
	require.NoError(t, err)
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = 'codex_telemetry_enabled'`).Scan(&value))
	require.Equal(t, "false", value, "upgrades must preserve an explicit disabled preference")
}

func telemetryMigrationSnapshot(t *testing.T, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, poolID string) string {
	t.Helper()
	var snapshot string
	require.NoError(t, q.QueryRowContext(context.Background(), `SELECT jsonb_build_object(
		'pool',(SELECT to_jsonb(p) FROM codex_telemetry_pools p WHERE id=$1::uuid),
		'activities',(SELECT jsonb_agg(to_jsonb(a) ORDER BY kind,activity_key) FROM codex_telemetry_activities a WHERE pool_id=$1::uuid),
		'batches',(SELECT jsonb_agg(to_jsonb(b) ORDER BY sequence) FROM codex_telemetry_batches b WHERE pool_id=$1::uuid))::text`, poolID).Scan(&snapshot))
	return snapshot
}

func TestMigration275CodexTelemetryUpgradePreservesState(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()
	schema := "telemetry_upgrade_" + uuid.NewString()
	_, err := tx.ExecContext(ctx, `CREATE SCHEMA "`+schema+`"; SET LOCAL search_path TO "`+schema+`";
		CREATE TABLE accounts(id bigint PRIMARY KEY);
		CREATE TABLE settings(key text PRIMARY KEY,value text,updated_at timestamptz);
		INSERT INTO accounts VALUES(1)`)
	require.NoError(t, err)
	apply := func(name string) {
		body, err := dbmigrations.FS.ReadFile(name)
		require.NoError(t, err)
		_, err = tx.ExecContext(ctx, string(body))
		require.NoError(t, err)
	}
	apply("250_codex_telemetry_pools.sql")
	poolID := uuid.NewString()
	_, err = tx.ExecContext(ctx, `INSERT INTO codex_telemetry_pools(id,owner_account_id,os_family,installation_id,seed,version)
		VALUES($1::uuid,1,'windows','existing-installation',$2::uuid,17)`, poolID, uuid.NewString())
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `INSERT INTO codex_telemetry_activities(pool_id,kind,activity_key,state,due_at,updated_at)
		VALUES($1::uuid,'thread','permanent','{"initialized":true}',NULL,'2026-01-01'),
		($1::uuid,'metrics','permanent','{"samples":2}',NULL,'2026-01-01'),
		($1::uuid,'policy','epoch','{"epoch":1}',NULL,'2026-01-01'),
		($1::uuid,'turn','active','{"inflight":{}}','2026-11-01','2026-10-31'),
		($1::uuid,'attempt','finished','{"finished":true}','2026-11-07','2026-10-31')`, poolID)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `INSERT INTO codex_telemetry_batches(id,pool_id,policy_epoch,account_id,type,source,payload,metadata,not_before,created_at,updated_at)
		VALUES($1::uuid,$2::uuid,1,1,'metrics','observed','{"resourceMetrics":[]}','{}','2026-11-01','2026-10-31','2026-10-31')`, uuid.NewString(), poolID)
	require.NoError(t, err)
	apply("271_codex_telemetry_pool_due.sql")
	before := telemetryMigrationSnapshot(t, tx, poolID)
	for range 2 {
		apply("275_codex_telemetry_activity_queries.sql")
	}
	require.Equal(t, before, telemetryMigrationSnapshot(t, tx, poolID), "index upgrade must preserve every pool, marker, transient and batch field")
	for name, columns := range map[string]string{
		"codex_telemetry_activities_pool_due_idx":    `(pool_id, due_at, kind COLLATE "C", activity_key COLLATE "C")`,
		"codex_telemetry_activities_runtime_age_idx": "(pool_id, updated_at, kind, activity_key)",
	} {
		var definition string
		require.NoError(t, tx.QueryRowContext(ctx, `SELECT indexdef FROM pg_indexes WHERE schemaname=current_schema() AND indexname=$1`, name).Scan(&definition))
		require.Contains(t, definition, columns)
		if name == "codex_telemetry_activities_pool_due_idx" {
			require.Contains(t, definition, "due_at IS NOT NULL")
		} else {
			for _, kind := range []string{"turn", "attempt", "sampling"} {
				require.Contains(t, definition, "'"+kind+"'")
			}
			require.NotContains(t, definition, "'thread'")
		}
	}
}

func TestMigration275CodexTelemetryRepeatedStartup(t *testing.T) {
	store, key, now := telemetryStoreFixture(t)
	ctx := context.Background()
	pool, err := store.TransactPool(ctx, key, now, func(tx *service.CodexTelemetryPoolTransaction) error {
		for _, item := range []service.CodexTelemetryActivity{
			telemetryStoreActivity("thread", "marker", `{"initialized":true}`, time.Time{}),
			telemetryStoreActivity("metrics", "marker", `{"samples":2}`, time.Time{}),
			telemetryStoreActivity("turn", "live", `{"inflight":{}}`, now.Add(time.Minute)),
		} {
			if err := tx.Activities.Put(item); err != nil {
				return err
			}
		}
		tx.Batches = append(tx.Batches, telemetryStoreBatch(uuid.NewString(), now))
		return nil
	})
	require.NoError(t, err)
	before := telemetryMigrationSnapshot(t, integrationDB, pool.ID)
	for range 2 {
		require.NoError(t, ApplyMigrations(ctx, integrationDB))
	}
	require.Equal(t, before, telemetryMigrationSnapshot(t, integrationDB, pool.ID), "repeated startup must preserve existing telemetry state")
}
