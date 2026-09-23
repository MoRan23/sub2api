//go:build integration

package repository

import (
	"context"
	"database/sql"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	dbmigrations "github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

var mergeV028Migrations = []string{
	"238b_content_moderation_engine_meta.sql",
	"239_channel_reasoning_effort_multipliers.sql",
	"240_affiliate_ledger_operation_id.sql",
}

func TestMergeV028MigrationsFreshInstallAndRestart(t *testing.T) {
	// TestMain creates an empty PostgreSQL container and applies the entire bundle.
	// Re-enter the real startup runner and assert that its ledger is unchanged.
	ctx := context.Background()
	before := mergeV028MigrationLedger(t, integrationDB)
	requireMergeV028Schema(t, integrationDB)
	for range 2 {
		require.NoError(t, ApplyMigrations(ctx, integrationDB))
		require.Equal(t, before, mergeV028MigrationLedger(t, integrationDB))
	}
}

func TestMergeV028MigrationsUpgradeAfterLocal259(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	container, err := tcpostgres.Run(ctx, selectDockerImage(ctx, postgresImageTag),
		tcpostgres.WithDatabase("merge_v028_upgrade"), tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword("postgres"), tcpostgres.BasicWaitStrategies())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, container.Terminate(context.Background())) })
	dsn, err := container.ConnectionString(ctx, "sslmode=disable", "TimeZone=UTC")
	require.NoError(t, err)
	db, err := openSQLWithRetry(ctx, dsn, 30*time.Second)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	// Preserve the original SQL bytes. Only the three incoming filenames are
	// absent from the deployed fork; their numeric prefixes are not versions.
	baseline := fstest.MapFS{}
	files, err := fs.Glob(dbmigrations.FS, "*.sql")
	require.NoError(t, err)
	for _, name := range files {
		if name > "259_openai_codex_ticket_proxy_policy.sql" || mergeV028Incoming(name) {
			continue
		}
		body, readErr := dbmigrations.FS.ReadFile(name)
		require.NoError(t, readErr)
		baseline[name] = &fstest.MapFile{Data: body}
	}
	require.NoError(t, applyMigrationsFS(ctx, db, baseline))
	before := mergeV028MigrationLedger(t, db)
	require.Contains(t, before, "259_openai_codex_ticket_proxy_policy.sql")
	for _, name := range mergeV028Migrations {
		require.NotContains(t, before, name)
	}

	var accountID, groupID, channelID, pricingID int64
	credentials := `{"access_token":"synthetic-access","refresh_token":"synthetic-refresh","client_id":"preserved-client"}`
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO accounts (name, platform, type, credentials, extra)
		VALUES ('merge-v028-identity', 'openai', 'oauth', $1::jsonb,
		'{"codex_turn_state_use_ticket_proxy":false}'::jsonb) RETURNING id`, credentials).Scan(&accountID))
	_, err = db.ExecContext(ctx, `INSERT INTO account_openai_oauth_os_profiles
		(account_id, os_family, installation_id, sync_session_id, user_agent, is_default)
		VALUES ($1, 'linux', 'deaf0000-0000-4000-8000-000000000028',
		'019a0000-0000-7000-8000-000000000028', 'codex_cli/0.155.1 (Linux 6.8; x86_64)', true)`, accountID)
	require.NoError(t, err)
	var poolID int64
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO openai_oauth_daily_session_pools
		(account_id, business_date, generation, stream_session_0, stream_session_1, stream_session_2, sync_session)
		VALUES ($1, '2026-09-24', 'merge-v028-generation', 'merge-v028-windows-stream',
		'merge-v028-macos-stream', 'merge-v028-linux-stream', 'merge-v028-linux-sync') RETURNING id`, accountID).Scan(&poolID))
	_, err = db.ExecContext(ctx, `INSERT INTO openai_oauth_daily_os_roots
		(pool_id, os_family, stream_session_id, sync_session_id)
		VALUES ($1, 'linux', 'merge-v028-linux-stream', 'merge-v028-linux-sync')`, poolID)
	require.NoError(t, err)
	identityBefore := mergeV028IdentitySnapshot(t, db, accountID)
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO groups (name, platform, model_pricing)
		VALUES ('merge-v028-pricing', 'openai', '[
		{"models":["legacy"],"max_reasoning_effort_multiplier":2.5},
		{"models":["configured"],"max_reasoning_effort_multiplier":3,"reasoning_effort_multipliers":{"high":1.5}},
		{"models":["cleared"],"max_reasoning_effort_multiplier":4,"reasoning_effort_multipliers":{}}
		]'::jsonb) RETURNING id`).Scan(&groupID))
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO channels (name) VALUES ('merge-v028-channel') RETURNING id`).Scan(&channelID))
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO channel_model_pricing
		(channel_id, models, max_reasoning_effort_multiplier) VALUES ($1, '["legacy"]', 2.5) RETURNING id`, channelID).Scan(&pricingID))

	require.NoError(t, ApplyMigrations(ctx, db))
	requireMergeV028Schema(t, db)
	after := mergeV028MigrationLedger(t, db)
	for name, row := range before {
		require.Equal(t, row, after[name], "existing migration must not replay or rewrite its checksum: %s", name)
	}
	require.Equal(t, identityBefore, mergeV028IdentitySnapshot(t, db, accountID))
	var pricing string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT model_pricing::text FROM groups WHERE id=$1`, groupID).Scan(&pricing))
	require.JSONEq(t, `[
		{"models":["legacy"],"reasoning_effort_multipliers":{"max":2.5}},
		{"models":["configured"],"reasoning_effort_multipliers":{"high":1.5}},
		{"models":["cleared"],"reasoning_effort_multipliers":{}}
	]`, pricing)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT reasoning_effort_multipliers::text FROM channel_model_pricing WHERE id=$1`, pricingID).Scan(&pricing))
	require.JSONEq(t, `{"max":2.5}`, pricing)

	// A later explicit empty map is a real choice, not a reason to backfill again.
	_, err = db.ExecContext(ctx, `UPDATE channel_model_pricing SET reasoning_effort_multipliers='{}'::jsonb WHERE id=$1`, pricingID)
	require.NoError(t, err)
	for range 2 {
		require.NoError(t, ApplyMigrations(ctx, db))
		require.Equal(t, after, mergeV028MigrationLedger(t, db))
		require.Equal(t, identityBefore, mergeV028IdentitySnapshot(t, db, accountID))
		require.NoError(t, db.QueryRowContext(ctx, `SELECT reasoning_effort_multipliers::text FROM channel_model_pricing WHERE id=$1`, pricingID).Scan(&pricing))
		require.JSONEq(t, `{}`, pricing)
	}
}

func mergeV028Incoming(name string) bool {
	for _, incoming := range mergeV028Migrations {
		if name == incoming {
			return true
		}
	}
	return false
}

func mergeV028MigrationLedger(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	rows, err := db.Query(`SELECT filename, checksum || ' ' || applied_at::text FROM schema_migrations ORDER BY filename`)
	require.NoError(t, err)
	defer rows.Close()
	ledger := map[string]string{}
	for rows.Next() {
		var name, value string
		require.NoError(t, rows.Scan(&name, &value))
		ledger[name] = value
	}
	require.NoError(t, rows.Err())
	return ledger
}

func requireMergeV028Schema(t *testing.T, db *sql.DB) {
	t.Helper()
	ledger := mergeV028MigrationLedger(t, db)
	for _, name := range mergeV028Migrations {
		require.Contains(t, ledger, name)
	}
	// Same-number local migrations continue to coexist with incoming filenames.
	require.Contains(t, ledger, "240_enable_openai_oauth_daily_session_rotation_default.sql")
	for _, pair := range [][2]string{
		{"content_moderation_logs", "engine_meta"},
		{"channel_model_pricing", "reasoning_effort_multipliers"},
		{"channel_account_stats_model_pricing", "reasoning_effort_multipliers"},
		{"user_affiliate_ledger", "operation_id"},
	} {
		var found bool
		require.NoError(t, db.QueryRow(`SELECT EXISTS (SELECT 1 FROM information_schema.columns
			WHERE table_schema='public' AND table_name=$1 AND column_name=$2)`, pair[0], pair[1]).Scan(&found))
		require.True(t, found, strings.Join(pair[:], "."))
	}
	var indexDefinition string
	require.NoError(t, db.QueryRow(`SELECT indexdef FROM pg_indexes WHERE schemaname='public'
		AND indexname='idx_user_affiliate_ledger_operation_id'`).Scan(&indexDefinition))
	require.Contains(t, indexDefinition, "UNIQUE")
	require.Contains(t, indexDefinition, "operation_id IS NOT NULL")
}

func mergeV028IdentitySnapshot(t *testing.T, db *sql.DB, accountID int64) string {
	t.Helper()
	var snapshot string
	require.NoError(t, db.QueryRow(`SELECT jsonb_build_object('credentials', a.credentials, 'extra', a.extra,
		'profiles', (SELECT jsonb_agg(to_jsonb(p) ORDER BY p.os_family)
		FROM account_openai_oauth_os_profiles p WHERE p.account_id=a.id),
		'daily_pools', (SELECT jsonb_agg(to_jsonb(p) ORDER BY p.id)
		FROM openai_oauth_daily_session_pools p WHERE p.account_id=a.id),
		'daily_roots', (SELECT jsonb_agg(to_jsonb(r) ORDER BY r.pool_id, r.os_family)
		FROM openai_oauth_daily_os_roots r JOIN openai_oauth_daily_session_pools p ON p.id=r.pool_id
		WHERE p.account_id=a.id))::text
		FROM accounts a WHERE a.id=$1`, accountID).Scan(&snapshot))
	return snapshot
}
