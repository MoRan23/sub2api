package migrations

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigration196BackfillsAndCleansOpenAIInstallationIDs(t *testing.T) {
	content, err := FS.ReadFile("196_backfill_openai_installation_ids.sql")
	require.NoError(t, err)
	sql := string(content)
	require.Contains(t, sql, "gen_random_uuid()")
	require.Contains(t, sql, "deleted_at IS NULL")
	require.Contains(t, sql, "platform = 'openai'")
	require.Contains(t, sql, "type = 'oauth'")
	require.Contains(t, sql, "parent_account_id IS NULL")
	require.Contains(t, sql, "openai_pinned_installation_id")
	require.Contains(t, sql, "openai_installation_rotate_enabled")
}

func TestMigration197AddsPartialUniqueInstallationIDIndex(t *testing.T) {
	content, err := FS.ReadFile("197_unique_openai_installation_id_notx.sql")
	require.NoError(t, err)
	sql := string(content)
	require.Contains(t, sql, "CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS")
	require.Contains(t, sql, "extra ->> 'openai_pinned_installation_id'")
	require.Contains(t, sql, "parent_account_id IS NULL")
}
