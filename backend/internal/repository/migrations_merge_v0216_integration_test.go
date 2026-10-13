//go:build integration

package repository

import (
	"context"
	"database/sql"
	"io/fs"
	"slices"
	"testing"
	"testing/fstest"
	"time"

	dbmigrations "github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

var mergeV0216Migrations = []string{
	"242_group_i18n.sql",
	"243_gemini_cached_contents.sql",
	"244_gemini_cached_contents_channel_usage.sql",
	"245_gemini_cached_contents_mutation_lease.sql",
}

func TestMergeV0216MigrationsFreshInstallAndRestart(t *testing.T) {
	// TestMain applies the full bundle to a new, isolated PostgreSQL database.
	before := mergeV028MigrationLedger(t, integrationDB)
	requireMergeV0216Schema(t, integrationDB)
	for range 2 {
		require.NoError(t, ApplyMigrations(context.Background(), integrationDB))
		require.Equal(t, before, mergeV028MigrationLedger(t, integrationDB))
	}
}

func TestMergeV0216MigrationsUpgradePreservesDaybreakAndOAuth(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	container, err := tcpostgres.Run(ctx, selectDockerImage(ctx, postgresImageTag),
		tcpostgres.WithDatabase("merge_v0216_upgrade"), tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword("postgres"), tcpostgres.BasicWaitStrategies())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, container.Terminate(context.Background())) })
	dsn, err := container.ConnectionString(ctx, "sslmode=disable", "TimeZone=UTC")
	require.NoError(t, err)
	db, err := openSQLWithRetry(ctx, dsn, 30*time.Second)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	// Reproduce the deployed fork without the four incoming filenames. A shared
	// numeric prefix must not suppress either branch's migration or replay it.
	baseline := fstest.MapFS{}
	files, err := fs.Glob(dbmigrations.FS, "*.sql")
	require.NoError(t, err)
	for _, name := range files {
		if slices.Contains(mergeV0216Migrations, name) {
			continue
		}
		body, readErr := dbmigrations.FS.ReadFile(name)
		require.NoError(t, readErr)
		baseline[name] = &fstest.MapFile{Data: body}
	}
	require.NoError(t, applyMigrationsFS(ctx, db, baseline))
	before := mergeV028MigrationLedger(t, db)
	for _, name := range mergeV0216Migrations {
		require.NotContains(t, before, name)
	}

	var groupID, oauthID, engineID int64
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO groups
		(name, description, platform, openai_daybreak_blue_enabled, openai_daybreak_red_enabled)
		VALUES ('merge-v0216', 'Keep original description', 'openai', true, true) RETURNING id`).Scan(&groupID))
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO accounts (name, platform, type, credentials, extra)
		VALUES ('merge-v0216-oauth', 'openai', 'oauth',
		'{"access_token":"synthetic-access","refresh_token":"synthetic-refresh","client_id":"preserved-client"}'::jsonb,
		'{"openai_daybreak_blue_enabled":true,"openai_daybreak_red_enabled":true}'::jsonb) RETURNING id`).Scan(&oauthID))
	_, err = db.ExecContext(ctx, `INSERT INTO account_openai_oauth_credentials
		(account_id, status, source, revision) VALUES ($1, 'authorized', 'merge-v0216', 7)`, oauthID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO account_openai_oauth_os_credentials
		(account_id, os_family, status, source, revision) VALUES ($1, 'linux', 'authorized', 'shared_runtime', 9)`, oauthID)
	require.NoError(t, err)
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO accounts (name, platform, type, credentials, extra)
		VALUES ('merge-v0216-engine', 'openai', 'apikey',
		'{"base_url":"http://engine.invalid/prefix/v1","api_key":"synthetic-engine-key"}'::jsonb,
		'{"openai_api_key_mode":"codex_engine"}'::jsonb) RETURNING id`).Scan(&engineID))
	dataBefore := mergeV0216LocalConfiguration(t, db, groupID, oauthID, engineID)

	require.NoError(t, ApplyMigrations(ctx, db))
	requireMergeV0216Schema(t, db)
	after := mergeV028MigrationLedger(t, db)
	require.Len(t, after, len(before)+len(mergeV0216Migrations))
	for name, row := range before {
		require.Equal(t, row, after[name], "published migration must not replay or change: %s", name)
	}
	require.Equal(t, dataBefore, mergeV0216LocalConfiguration(t, db, groupID, oauthID, engineID))
	var translations string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT i18n::text FROM groups WHERE id=$1`, groupID).Scan(&translations))
	require.JSONEq(t, `{}`, translations)

	// Later administrator choices and active cache mutation leases survive a
	// repeated startup; migrations must not replace them with defaults.
	_, err = db.ExecContext(ctx, `UPDATE groups SET i18n='{"en":{"name":"Translated","description":"Retained"}}'::jsonb WHERE id=$1`, groupID)
	require.NoError(t, err)
	var cacheID int64
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO gemini_cached_contents
		(public_id, user_id, api_key_id, group_id, account_id, upstream_name, model, upstream_model,
		expire_time, channel_usage, mutation_token, mutation_lease_until)
		VALUES ('merge-v0216-cache', 1, 1, $1, $2, 'cachedContents/test', 'gemini-test', 'models/gemini-test',
		NOW()+INTERVAL '1 hour', '{"channel_id":7}'::jsonb, 'active-lease', NOW()+INTERVAL '2 minutes') RETURNING id`,
		groupID, engineID).Scan(&cacheID))
	var cacheBefore string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT to_jsonb(c)::text FROM gemini_cached_contents c WHERE id=$1`, cacheID).Scan(&cacheBefore))
	dataAfterChoices := mergeV0216LocalConfiguration(t, db, groupID, oauthID, engineID)
	for range 2 {
		require.NoError(t, ApplyMigrations(ctx, db))
		require.Equal(t, after, mergeV028MigrationLedger(t, db))
		require.Equal(t, dataAfterChoices, mergeV0216LocalConfiguration(t, db, groupID, oauthID, engineID))
		require.NoError(t, db.QueryRowContext(ctx, `SELECT i18n::text FROM groups WHERE id=$1`, groupID).Scan(&translations))
		require.JSONEq(t, `{"en":{"name":"Translated","description":"Retained"}}`, translations)
		var cacheAfter string
		require.NoError(t, db.QueryRowContext(ctx, `SELECT to_jsonb(c)::text FROM gemini_cached_contents c WHERE id=$1`, cacheID).Scan(&cacheAfter))
		require.Equal(t, cacheBefore, cacheAfter)
	}
}

func requireMergeV0216Schema(t *testing.T, db *sql.DB) {
	t.Helper()
	ledger := mergeV028MigrationLedger(t, db)
	for _, name := range append(append([]string(nil), mergeV0216Migrations...),
		"242_openai_request_integrity_observe_enabled.sql", "243_openai_codex_state.sql",
		"244_proxy_geo_snapshots.sql", "245_openai_codex_state_credential_epoch.sql", "269_group_daybreak.sql") {
		require.Contains(t, ledger, name)
	}
	for _, pair := range [][2]string{
		{"groups", "i18n"}, {"groups", "openai_daybreak_blue_enabled"}, {"groups", "openai_daybreak_red_enabled"},
		{"gemini_cached_contents", "public_id"}, {"gemini_cached_contents", "channel_usage"},
		{"gemini_cached_contents", "mutation_token"}, {"gemini_cached_contents", "mutation_lease_until"},
	} {
		var exists bool
		require.NoError(t, db.QueryRow(`SELECT EXISTS (SELECT 1 FROM information_schema.columns
			WHERE table_schema='public' AND table_name=$1 AND column_name=$2)`, pair[0], pair[1]).Scan(&exists))
		require.True(t, exists, "%s.%s", pair[0], pair[1])
	}
	for name, fragments := range map[string][]string{
		"idx_gemini_cached_contents_public_id":        {"UNIQUE", "public_id"},
		"idx_gemini_cached_contents_api_key_active":   {"api_key_id", "deleted_at IS NULL"},
		"idx_gemini_cached_contents_account":          {"account_id"},
		"idx_gemini_cached_contents_pending_mutation": {"mutation_lease_until", "mutation_token IS NOT NULL", "deleted_at IS NULL"},
	} {
		var definition string
		require.NoError(t, db.QueryRow(`SELECT indexdef FROM pg_indexes WHERE schemaname='public' AND indexname=$1`, name).Scan(&definition))
		for _, fragment := range fragments {
			require.Contains(t, definition, fragment, name)
		}
	}
}

func mergeV0216LocalConfiguration(t *testing.T, db *sql.DB, groupID, oauthID, engineID int64) string {
	t.Helper()
	var snapshot string
	require.NoError(t, db.QueryRow(`SELECT jsonb_build_object(
		'group', (SELECT to_jsonb(g)-'i18n' FROM groups g WHERE id=$1),
		'accounts', (SELECT jsonb_agg(to_jsonb(a) ORDER BY id) FROM accounts a WHERE id IN ($2,$3)),
		'authorization', (SELECT to_jsonb(c) FROM account_openai_oauth_credentials c WHERE account_id=$2),
		'slots', (SELECT jsonb_agg(to_jsonb(s) ORDER BY os_family) FROM account_openai_oauth_os_credentials s WHERE account_id=$2)
		)::text`, groupID, oauthID, engineID).Scan(&snapshot))
	return snapshot
}
