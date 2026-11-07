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

const mergeMCPMigration = "249_mcp_oauth.sql"

func TestMergeMCPMigrationFreshInstallAndRestart(t *testing.T) {
	requireMergeMCPSchema(t, integrationDB)
	before := mergeV028MigrationLedger(t, integrationDB)
	for range 2 {
		require.NoError(t, ApplyMigrations(context.Background(), integrationDB))
		require.Equal(t, before, mergeV028MigrationLedger(t, integrationDB))
	}
}

func TestMergeMCPMigrationUpgradeAfterLocal278(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	container, err := tcpostgres.Run(ctx, selectDockerImage(ctx, postgresImageTag),
		tcpostgres.WithDatabase("merge_mcp_upgrade"), tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword("postgres"), tcpostgres.BasicWaitStrategies())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, container.Terminate(context.Background())) })
	dsn, err := container.ConnectionString(ctx, "sslmode=disable", "TimeZone=UTC")
	require.NoError(t, err)
	db, err := openSQLWithRetry(ctx, dsn, 30*time.Second)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	// Reproduce the deployed local bundle, which already has its own migration
	// numbered 249 and has advanced through 278. Full filenames are identities.
	baseline := fstest.MapFS{}
	files, err := fs.Glob(dbmigrations.FS, "*.sql")
	require.NoError(t, err)
	for _, name := range files {
		if name == mergeMCPMigration {
			continue
		}
		body, readErr := dbmigrations.FS.ReadFile(name)
		require.NoError(t, readErr)
		baseline[name] = &fstest.MapFile{Data: body}
	}
	require.NoError(t, applyMigrationsFS(ctx, db, baseline))
	before := mergeV028MigrationLedger(t, db)
	require.NotContains(t, before, mergeMCPMigration)
	require.Contains(t, before, "249_openai_oauth_daily_os_roots.sql")
	require.Contains(t, before, "278_codex_engine_billing_received_events.sql")

	var userID, groupID, oauthID int64
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO users (email, password_hash, balance, gift_balance)
		VALUES ('merge-mcp@example.invalid', 'synthetic', 80.12345678, 12.87654321) RETURNING id`).Scan(&userID))
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO groups
		(name, platform, openai_daybreak_blue_enabled, openai_daybreak_red_enabled)
		VALUES ('merge-mcp', 'openai', true, true) RETURNING id`).Scan(&groupID))
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO accounts (name, platform, type, credentials, extra)
		VALUES ('merge-mcp-oauth', 'openai', 'oauth',
		'{"access_token":"synthetic-access","refresh_token":"synthetic-refresh"}'::jsonb,
		'{"openai_daybreak_blue_enabled":true,"openai_daybreak_red_enabled":true}'::jsonb) RETURNING id`).Scan(&oauthID))
	_, err = db.ExecContext(ctx, `INSERT INTO account_openai_oauth_credentials
		(account_id, status, source, revision) VALUES ($1, 'authorized', 'merge-mcp', 7)`, oauthID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO account_openai_oauth_os_credentials
		(account_id, os_family, status, source, revision) VALUES ($1, 'linux', 'authorized', 'shared_runtime', 9)`, oauthID)
	require.NoError(t, err)
	localSnapshot := func() string {
		var snapshot string
		require.NoError(t, db.QueryRowContext(ctx, `SELECT jsonb_build_object(
			'user', (SELECT to_jsonb(u) FROM users u WHERE id=$1),
			'group', (SELECT to_jsonb(g) FROM groups g WHERE id=$2),
			'account', (SELECT to_jsonb(a) FROM accounts a WHERE id=$3),
			'authorization', (SELECT to_jsonb(c) FROM account_openai_oauth_credentials c WHERE account_id=$3),
			'os_authorization', (SELECT to_jsonb(c) FROM account_openai_oauth_os_credentials c WHERE account_id=$3 AND os_family='linux'))::text`,
			userID, groupID, oauthID).Scan(&snapshot))
		return snapshot
	}
	preserved := localSnapshot()
	require.NoError(t, ApplyMigrations(ctx, db))
	requireMergeMCPSchema(t, db)
	after := mergeV028MigrationLedger(t, db)
	require.Len(t, after, len(before)+1)
	for name, row := range before {
		require.Equal(t, row, after[name], "existing migration must not replay: %s", name)
	}
	for range 2 {
		require.Equal(t, preserved, localSnapshot())
		require.NoError(t, ApplyMigrations(ctx, db))
		require.Equal(t, after, mergeV028MigrationLedger(t, db))
	}
	require.Equal(t, preserved, localSnapshot())
}

func requireMergeMCPSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	ledger := mergeV028MigrationLedger(t, db)
	require.Contains(t, ledger, mergeMCPMigration)
	require.Contains(t, ledger, "249_openai_oauth_daily_os_roots.sql")
	for _, table := range []string{"mcp_oauth_grants", "mcp_oauth_codes", "mcp_oauth_credentials"} {
		var exists bool
		require.NoError(t, db.QueryRow(`SELECT EXISTS (SELECT 1 FROM information_schema.tables
			WHERE table_schema='public' AND table_name=$1)`, table).Scan(&exists))
		require.True(t, exists, table)
		var foreignKeys int
		require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM pg_constraint WHERE contype='f'
			AND confdeltype='c' AND conrelid=$1::regclass`, table).Scan(&foreignKeys))
		require.Equal(t, 1, foreignKeys, table)
	}
}
