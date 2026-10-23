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

func TestMergeV0218GeminiImageMigrationUpgradeAndRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	container, err := tcpostgres.Run(ctx, selectDockerImage(ctx, postgresImageTag),
		tcpostgres.WithDatabase("merge_v0218_upgrade"), tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword("postgres"), tcpostgres.BasicWaitStrategies())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, container.Terminate(context.Background())) })
	dsn, err := container.ConnectionString(ctx, "sslmode=disable", "TimeZone=UTC")
	require.NoError(t, err)
	db, err := openSQLWithRetry(ctx, dsn, 30*time.Second)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	const incoming = "246_enable_gemini_image_generation_groups.sql"
	baseline := fstest.MapFS{}
	files, err := fs.Glob(dbmigrations.FS, "*.sql")
	require.NoError(t, err)
	for _, name := range files {
		if name == incoming {
			continue
		}
		body, readErr := dbmigrations.FS.ReadFile(name)
		require.NoError(t, readErr)
		baseline[name] = &fstest.MapFile{Data: body}
	}
	require.NoError(t, applyMigrationsFS(ctx, db, baseline))
	before := mergeV028MigrationLedger(t, db)
	require.NotContains(t, before, incoming)
	// The local migration sharing prefix 246 and the published UA migration
	// must retain their identities/checksums through this upstream upgrade.
	require.Contains(t, before, "273_normalize_openai_ubuntu_user_agents.sql")

	type fixture struct {
		platform string
		want     bool
		id       int64
	}
	groups := []fixture{{platform: "gemini", want: true}, {platform: "antigravity", want: true},
		{platform: "openai"}, {platform: "composite"}, {platform: "anthropic"}, {platform: "grok"}}
	for i := range groups {
		f := &groups[i]
		require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO groups
			(name, platform, allow_image_generation, openai_daybreak_blue_enabled, openai_daybreak_red_enabled, i18n)
			VALUES ($1,$2,false,true,true,'{"en":{"name":"Preserved"}}') RETURNING id`,
			"merge-v0218-"+f.platform, f.platform).Scan(&f.id))
	}
	require.NoError(t, ApplyMigrations(ctx, db))
	after := mergeV028MigrationLedger(t, db)
	require.Len(t, after, len(before)+1)
	for name, row := range before {
		require.Equal(t, row, after[name], "existing migration must not replay: %s", name)
	}
	for _, f := range groups {
		var enabled, blue, red bool
		var translations string
		require.NoError(t, db.QueryRowContext(ctx, `SELECT allow_image_generation,
			openai_daybreak_blue_enabled, openai_daybreak_red_enabled, i18n::text FROM groups WHERE id=$1`,
			f.id).Scan(&enabled, &blue, &red, &translations))
		require.Equal(t, f.want, enabled, f.platform)
		require.True(t, blue)
		require.True(t, red)
		require.JSONEq(t, `{"en":{"name":"Preserved"}}`, translations)
	}

	// An administrator can turn Gemini images off after upgrading. Repeated
	// application starts must not re-enable them or rewrite other migrations.
	_, err = db.ExecContext(ctx, `UPDATE groups SET allow_image_generation=false WHERE id=$1`, groups[0].id)
	require.NoError(t, err)
	for range 2 {
		require.NoError(t, ApplyMigrations(ctx, db))
		require.Equal(t, after, mergeV028MigrationLedger(t, db))
		var enabled bool
		require.NoError(t, db.QueryRowContext(ctx, `SELECT allow_image_generation FROM groups WHERE id=$1`, groups[0].id).Scan(&enabled))
		require.False(t, enabled)
	}
}
