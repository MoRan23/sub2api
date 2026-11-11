//go:build integration

package repository

import (
	"context"
	"database/sql"
	"io/fs"
	"testing"
	"testing/fstest"
	"time"

	dbmigrations "github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

var mergeV0221IncomingMigrations = []string{
	"249_add_usage_logs_api_key_cost_index_notx.sql",
	"250_openai_cache_write_ttl.sql",
	"281_prompt_audit_enforcement.sql",
}

func TestMergeV0221FreshInstallAndRestart(t *testing.T) {
	ctx := context.Background()
	before := mergeV028MigrationLedger(t, integrationDB)
	for _, name := range mergeV0221IncomingMigrations {
		require.Contains(t, before, name)
	}
	assertMergeV0221Schema(t, ctx, integrationDB)
	for range 2 {
		require.NoError(t, ApplyMigrations(ctx, integrationDB))
		require.Equal(t, before, mergeV028MigrationLedger(t, integrationDB))
	}
}

func TestMergeV0221UpgradePreservesWalletsPermanentQuotaAndDaybreak(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	container, err := tcpostgres.Run(ctx, selectDockerImage(ctx, postgresImageTag),
		tcpostgres.WithDatabase("merge_v0221_upgrade"), tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword("postgres"), tcpostgres.BasicWaitStrategies())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, container.Terminate(context.Background())) })
	dsn, err := container.ConnectionString(ctx, "sslmode=disable", "TimeZone=UTC")
	require.NoError(t, err)
	db, err := openSQLWithRetry(ctx, dsn, 30*time.Second)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	baseline := fstest.MapFS{}
	excluded := make(map[string]bool, len(mergeV0221IncomingMigrations))
	for _, name := range mergeV0221IncomingMigrations {
		excluded[name] = true
	}
	files, err := fs.Glob(dbmigrations.FS, "*.sql")
	require.NoError(t, err)
	for _, name := range files {
		if excluded[name] {
			continue
		}
		body, err := dbmigrations.FS.ReadFile(name)
		require.NoError(t, err)
		baseline[name] = &fstest.MapFile{Data: body}
	}
	require.NoError(t, applyMigrationsFS(ctx, db, baseline))
	before := mergeV028MigrationLedger(t, db)
	for _, name := range mergeV0221IncomingMigrations {
		require.NotContains(t, before, name)
	}
	// Published local migrations sharing numeric prefixes retain their full names.
	require.Contains(t, before, "249_mcp_oauth.sql")
	require.Contains(t, before, "250_codex_telemetry_pools.sql")

	var userID, groupID, subID, quotaID, accountID, keyID, usageID int64
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO users
		(email,password_hash,balance,gift_balance,frozen_balance,frozen_gift_balance)
		VALUES ('merge-v0221@example.test','fixture',12.12345678,7.87654321,1.25,2.5) RETURNING id`).Scan(&userID))
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO groups
		(name,platform,subscription_type,total_quota_no_expiry,openai_daybreak_blue_enabled,openai_daybreak_red_enabled,i18n)
		VALUES ('merge-v0221','openai','total_quota',true,true,true,'{"en":{"name":"Preserved"}}') RETURNING id`).Scan(&groupID))
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO user_subscriptions
		(user_id,group_id,starts_at,expires_at,status) VALUES ($1,$2,NOW(),NULL,'active') RETURNING id`, userID, groupID).Scan(&subID))
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO user_subscription_quota_events
		(user_subscription_id,quota_total_usd,quota_used_usd,starts_at,expires_at,source_kind)
		VALUES ($1,30.12345678,7.25,NOW(),NULL,'admin') RETURNING id`, subID).Scan(&quotaID))
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO accounts
		(name,platform,type,extra) VALUES ('merge-v0221','openai','oauth',
		'{"openai_daybreak_blue_enabled":true,"openai_daybreak_red_enabled":true}') RETURNING id`).Scan(&accountID))
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO api_keys
		(user_id,key,name,group_id) VALUES ($1,'merge-v0221-key','fixture',$2) RETURNING id`, userID, groupID).Scan(&keyID))
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO usage_logs
		(user_id,api_key_id,account_id,request_id,model,cache_creation_tokens,actual_cost)
		VALUES ($1,$2,$3,'merge-v0221-old-usage','gpt-6-luna',10,0.12345678) RETURNING id`, userID, keyID, accountID).Scan(&usageID))

	fixtures := []struct {
		table string
		id    int64
	}{
		{"users", userID}, {"groups", groupID}, {"user_subscriptions", subID},
		{"user_subscription_quota_events", quotaID}, {"accounts", accountID}, {"api_keys", keyID},
	}
	snapshots := make([]string, len(fixtures))
	for i, f := range fixtures {
		require.NoError(t, db.QueryRowContext(ctx, `SELECT row_to_json(r)::text FROM `+f.table+` r WHERE id=$1`, f.id).Scan(&snapshots[i]))
	}
	require.NoError(t, ApplyMigrations(ctx, db))
	after := mergeV028MigrationLedger(t, db)
	require.Len(t, after, len(before)+len(mergeV0221IncomingMigrations))
	for name, row := range before {
		require.Equal(t, row, after[name], "published migration must not replay: %s", name)
	}
	assertMergeV0221Schema(t, ctx, db)
	var aggregate, short, long int
	var cost string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT cache_creation_tokens,cache_creation_30m_tokens,
		cache_creation_12h_tokens,actual_cost::text FROM usage_logs WHERE id=$1`, usageID).Scan(&aggregate, &short, &long, &cost))
	require.Equal(t, 10, aggregate)
	require.Zero(t, short)
	require.Zero(t, long)
	require.Equal(t, "0.1234567800", cost)
	_, err = db.ExecContext(ctx, `UPDATE usage_logs SET cache_creation_30m_tokens=4,cache_creation_12h_tokens=6 WHERE id=$1`, usageID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO prompt_audit_enforcement_states (user_id) VALUES ($1)`, userID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO prompt_audit_enforcement_hits
		(user_id,epoch,request_key,kind,violation_count) VALUES ($1,1,'merge-v0221-hit','combination',1)`, userID)
	require.NoError(t, err)
	for range 2 {
		require.NoError(t, ApplyMigrations(ctx, db))
		require.Equal(t, after, mergeV028MigrationLedger(t, db))
		for i, f := range fixtures {
			var got string
			require.NoError(t, db.QueryRowContext(ctx, `SELECT row_to_json(r)::text FROM `+f.table+` r WHERE id=$1`, f.id).Scan(&got))
			require.JSONEq(t, snapshots[i], got, "fixture preserved: %s", f.table)
		}
		require.NoError(t, db.QueryRowContext(ctx, `SELECT cache_creation_30m_tokens,cache_creation_12h_tokens
			FROM usage_logs WHERE id=$1`, usageID).Scan(&short, &long))
		require.Equal(t, 4, short)
		require.Equal(t, 6, long)
		var hits int
		require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM prompt_audit_enforcement_hits WHERE user_id=$1`, userID).Scan(&hits))
		require.Equal(t, 1, hits)
	}
}

func assertMergeV0221Schema(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	var valid, ready bool
	var definition string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT indisvalid,indisready,pg_get_indexdef(indexrelid)
		FROM pg_index WHERE indexrelid='idx_usage_logs_api_key_created_cost'::regclass`).Scan(&valid, &ready, &definition))
	require.True(t, valid)
	require.True(t, ready)
	require.Contains(t, definition, "(api_key_id, created_at) INCLUDE (actual_cost)")
	for _, table := range []string{"channel_model_pricing", "channel_pricing_intervals", "channel_account_stats_model_pricing", "channel_account_stats_pricing_intervals"} {
		var nullable string
		require.NoError(t, db.QueryRowContext(ctx, `SELECT is_nullable FROM information_schema.columns
			WHERE table_schema='public' AND table_name=$1 AND column_name='cache_write_12h_price'`, table).Scan(&nullable))
		require.Equal(t, "YES", nullable)
	}
	for _, table := range []string{"prompt_audit_enforcement_states", "prompt_audit_enforcement_hits", "prompt_audit_enforcement_notifications"} {
		var exists bool
		require.NoError(t, db.QueryRowContext(ctx, `SELECT to_regclass($1) IS NOT NULL`, table).Scan(&exists))
		require.True(t, exists, table)
	}
}
