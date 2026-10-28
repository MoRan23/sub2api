//go:build integration

package repository

import (
	"context"
	"io/fs"
	"testing"
	"testing/fstest"
	"time"

	dbmigrations "github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

func TestMergeV0219UpgradePreservesGroupPoliciesAndRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	container, err := tcpostgres.Run(ctx, selectDockerImage(ctx, postgresImageTag),
		tcpostgres.WithDatabase("merge_v0219_upgrade"), tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword("postgres"), tcpostgres.BasicWaitStrategies())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, container.Terminate(context.Background())) })
	dsn, err := container.ConnectionString(ctx, "sslmode=disable", "TimeZone=UTC")
	require.NoError(t, err)
	db, err := openSQLWithRetry(ctx, dsn, 30*time.Second)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	baseline := fstest.MapFS{}
	files, err := fs.Glob(dbmigrations.FS, "*.sql")
	require.NoError(t, err)
	for _, name := range files {
		if name == "247_scoped_admin_api_keys.sql" || name == "248_group_image_count_billing.sql" {
			continue
		}
		body, readErr := dbmigrations.FS.ReadFile(name)
		require.NoError(t, readErr)
		baseline[name] = &fstest.MapFile{Data: body}
	}
	require.NoError(t, applyMigrationsFS(ctx, db, baseline))
	before := mergeV028MigrationLedger(t, db)
	var groupID int64
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO groups
		(name, platform, openai_daybreak_blue_enabled, openai_daybreak_red_enabled, i18n)
		VALUES ('merge-v0219', 'composite', true, true, '{"en":{"name":"Preserved"}}') RETURNING id`).Scan(&groupID))
	require.NoError(t, ApplyMigrations(ctx, db))
	after := mergeV028MigrationLedger(t, db)
	require.Len(t, after, len(before)+2)
	for name, row := range before {
		require.Equal(t, row, after[name], "published migration must not replay: %s", name)
	}
	var count, blue, red bool
	var translations string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT image_count_billing,
		openai_daybreak_blue_enabled, openai_daybreak_red_enabled, i18n::text FROM groups WHERE id=$1`,
		groupID).Scan(&count, &blue, &red, &translations))
	require.False(t, count)
	require.True(t, blue)
	require.True(t, red)
	require.JSONEq(t, `{"en":{"name":"Preserved"}}`, translations)
	_, err = db.ExecContext(ctx, `UPDATE groups SET image_count_billing=true WHERE id=$1`, groupID)
	require.NoError(t, err)
	for range 2 {
		require.NoError(t, ApplyMigrations(ctx, db))
		require.Equal(t, after, mergeV028MigrationLedger(t, db))
		require.NoError(t, db.QueryRowContext(ctx, `SELECT image_count_billing FROM groups WHERE id=$1`, groupID).Scan(&count))
		require.True(t, count)
	}
	var scopedCount int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM scoped_admin_api_keys`).Scan(&scopedCount))
	require.Zero(t, scopedCount)
}
