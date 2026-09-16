//go:build integration

package repository

import (
	"context"
	"testing"

	dbmigrations "github.com/Wei-Shaw/sub2api/migrations"
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
