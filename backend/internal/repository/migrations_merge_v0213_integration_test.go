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

var mergeV0213Migrations = []string{
	"241_add_payment_order_bonus_amount.sql",
	"241_add_typesafe_platform.sql",
}

func TestMergeV0213MigrationsFreshInstallAndRestart(t *testing.T) {
	before := mergeV028MigrationLedger(t, integrationDB)
	requireMergeV0213Schema(t, integrationDB)
	for range 2 {
		require.NoError(t, ApplyMigrations(context.Background(), integrationDB))
		require.Equal(t, before, mergeV028MigrationLedger(t, integrationDB))
	}
}

func TestMergeV0213MigrationsUpgradePreservesDualWallet(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	container, err := tcpostgres.Run(ctx, selectDockerImage(ctx, postgresImageTag),
		tcpostgres.WithDatabase("merge_v0213_upgrade"), tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword("postgres"), tcpostgres.BasicWaitStrategies())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, container.Terminate(context.Background())) })
	dsn, err := container.ConnectionString(ctx, "sslmode=disable", "TimeZone=UTC")
	require.NoError(t, err)
	db, err := openSQLWithRetry(ctx, dsn, 30*time.Second)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	// Reproduce the complete dev migration bundle, omitting only the two incoming
	// filenames. Numeric prefixes overlap and are not migration identities.
	baseline := fstest.MapFS{}
	files, err := fs.Glob(dbmigrations.FS, "*.sql")
	require.NoError(t, err)
	for _, name := range files {
		if name == mergeV0213Migrations[0] || name == mergeV0213Migrations[1] {
			continue
		}
		body, readErr := dbmigrations.FS.ReadFile(name)
		require.NoError(t, readErr)
		baseline[name] = &fstest.MapFile{Data: body}
	}
	require.NoError(t, applyMigrationsFS(ctx, db, baseline))
	before := mergeV028MigrationLedger(t, db)
	require.Contains(t, before, "241_codex_telemetry_enabled.sql")
	var userID, orderID, groupID int64
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO users
		(email, password_hash, balance, gift_balance) VALUES
		('merge-v0213@example.invalid', 'synthetic', 80.12345678, 12.87654321) RETURNING id`).Scan(&userID))
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO payment_orders
		(user_id, amount, pay_amount, gift_ratio, gift_amount, status, expires_at)
		VALUES ($1, 100, 100, 0.1234, 12.34000001, 'COMPLETED', NOW()) RETURNING id`, userID).Scan(&orderID))
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO groups (name, platform)
		VALUES ('merge-v0213-group', 'openai') RETURNING id`).Scan(&groupID))
	_, err = db.ExecContext(ctx, `INSERT INTO user_platform_quotas (user_id, platform, daily_limit_usd)
		VALUES ($1, 'openai', 3)`, userID)
	require.NoError(t, err)

	require.NoError(t, ApplyMigrations(ctx, db))
	requireMergeV0213Schema(t, db)
	after := mergeV028MigrationLedger(t, db)
	require.Len(t, after, len(before)+2)
	for name, row := range before {
		require.Equal(t, row, after[name], "published migration changed: %s", name)
	}
	for range 2 {
		var balance, giftBalance, amount, giftRatio, giftAmount, bonusAmount string
		require.NoError(t, db.QueryRowContext(ctx, `SELECT u.balance::text, u.gift_balance::text,
			p.amount::text, p.gift_ratio::text, p.gift_amount::text, p.bonus_amount::text
			FROM users u JOIN payment_orders p ON p.user_id=u.id WHERE p.id=$1`, orderID).
			Scan(&balance, &giftBalance, &amount, &giftRatio, &giftAmount, &bonusAmount))
		require.Equal(t, "80.12345678", balance)
		require.Equal(t, "12.87654321", giftBalance)
		require.Equal(t, "100.00", amount)
		require.Equal(t, "0.1234", giftRatio)
		require.Equal(t, "12.34000001", giftAmount)
		require.Equal(t, "0.00", bonusAmount)
		require.NoError(t, ApplyMigrations(ctx, db))
		require.Equal(t, after, mergeV028MigrationLedger(t, db))
	}
	_, err = db.ExecContext(ctx, `INSERT INTO user_platform_quotas (user_id, platform)
		VALUES ($1, 'typesafe')`, userID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO composite_model_routes
		(group_id, public_model, target_platform) VALUES ($1, 'system-one', 'typesafe')`, groupID)
	require.NoError(t, err)
	var existingLimit int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT daily_limit_usd::int FROM user_platform_quotas
		WHERE user_id=$1 AND platform='openai'`, userID).Scan(&existingLimit))
	require.Equal(t, 3, existingLimit)
}

func requireMergeV0213Schema(t *testing.T, db *sql.DB) {
	t.Helper()
	ledger := mergeV028MigrationLedger(t, db)
	for _, name := range mergeV0213Migrations {
		require.Contains(t, ledger, name)
	}
	require.Contains(t, ledger, "241_codex_telemetry_enabled.sql")
	for _, column := range []string{"gift_ratio", "gift_amount", "bonus_amount"} {
		var found bool
		require.NoError(t, db.QueryRow(`SELECT EXISTS (SELECT 1 FROM information_schema.columns
			WHERE table_schema='public' AND table_name='payment_orders' AND column_name=$1)`, column).Scan(&found))
		require.True(t, found, column)
	}
}
