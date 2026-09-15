//go:build integration

package repository

import (
	"context"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	dbmigrations "github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

// The fork already deployed 238-240 before upstream added two differently named
// 238 migrations. Upgrading must apply those files without replaying the roots.
func TestMigrationsUpstream025UpgradePreservesFixedRoots(t *testing.T) {
	ctx := context.Background()
	container, err := tcpostgres.Run(ctx, selectDockerImage(ctx, postgresImageTag),
		tcpostgres.WithDatabase("merge_upgrade"), tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword("postgres"), tcpostgres.BasicWaitStrategies())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, container.Terminate(ctx)) })
	dsn, err := container.ConnectionString(ctx, "sslmode=disable", "TimeZone=UTC")
	require.NoError(t, err)
	db, err := openSQLWithRetry(ctx, dsn, 30*time.Second)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	baseline := fstest.MapFS{}
	entries, err := fs.ReadDir(dbmigrations.FS, ".")
	require.NoError(t, err)
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".sql") || name > "240_enable_openai_oauth_daily_session_rotation_default.sql" ||
			name == "225_backfill_codex_fingerprint_seed.sql" ||
			name == "238_opencode_go_platform.sql" || name == "238_purge_unlimited_user_platform_quotas.sql" {
			continue
		}
		body, readErr := dbmigrations.FS.ReadFile(name)
		require.NoError(t, readErr)
		baseline[name] = &fstest.MapFile{Data: body}
	}
	require.NoError(t, applyMigrationsFS(ctx, db, baseline))
	var enabled string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = $1`, service.SettingKeyEnableOpenAIOAuthDailySessionRotation).Scan(&enabled))
	require.Equal(t, "false", enabled, "fresh installations must keep fixed roots opt-in")
	_, err = db.ExecContext(ctx, `UPDATE settings SET value = 'true' WHERE key = $1`, service.SettingKeyEnableOpenAIOAuthDailySessionRotation)
	require.NoError(t, err)
	var accountID int64
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO accounts (name, platform, type, credentials)
		VALUES ('merge-upgrade-fixture', 'openai', 'oauth', '{}') RETURNING id`).Scan(&accountID))
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	repo := NewOpenAIOAuthDailySessionRepository(client)
	now := time.Now().UTC()
	before, err := repo.GetOrCreateOAuthDailySessionPool(ctx, accountID, now)
	require.NoError(t, err)
	legacyRepo := NewOpenAIOAuthSyncSessionRepository(client)
	legacyRoot, err := legacyRepo.GetOrCreateOAuthSyncSession(ctx, accountID)
	require.NoError(t, err)

	require.NoError(t, ApplyMigrations(ctx, db))
	require.NoError(t, ApplyMigrations(ctx, db), "upgraded migrations must remain idempotent")
	after, err := repo.GetOrCreateOAuthDailySessionPool(ctx, accountID, now)
	require.NoError(t, err)
	require.Equal(t, before, after)
	root, err := legacyRepo.GetOrCreateOAuthSyncSession(ctx, accountID)
	require.NoError(t, err)
	require.Equal(t, legacyRoot, root)
	var count int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations WHERE filename LIKE '238_%'`).Scan(&count))
	require.Equal(t, 3, count, "all three 238 filenames must coexist")
	require.NoError(t, db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = $1`, service.SettingKeyEnableOpenAIOAuthDailySessionRotation).Scan(&enabled))
	require.Equal(t, "true", enabled)
	_, err = db.ExecContext(ctx, `UPDATE settings SET value = 'false' WHERE key = $1`, service.SettingKeyEnableOpenAIOAuthDailySessionRotation)
	require.NoError(t, err)
	require.NoError(t, ApplyMigrations(ctx, db))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = $1`, service.SettingKeyEnableOpenAIOAuthDailySessionRotation).Scan(&enabled))
	require.Equal(t, "false", enabled, "restarting after an upgrade must preserve a disabled switch too")
	var constraint string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT pg_get_constraintdef(oid) FROM pg_constraint
		WHERE conname = 'user_platform_quotas_platform_check'`).Scan(&constraint))
	require.Contains(t, constraint, "minimax")
	require.Contains(t, constraint, "opencode_go")
}
