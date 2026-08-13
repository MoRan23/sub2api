package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigration223InstallsMissingCodexFingerprintPolicyAndNormalizesAccountOwnership(t *testing.T) {
	content, err := FS.ReadFile("223_enable_openai_codex_fingerprint_normalization.sql")
	require.NoError(t, err)
	sql := strings.ToLower(string(content))

	for _, key := range []string{
		"enable_openai_codex_fingerprint_normalization",
		"enable_openai_codex_installation_id_normalization",
		"enable_openai_uuidv7_session_identity",
		"enable_openai_codex_client_identity_normalization",
	} {
		require.Contains(t, sql, "'"+key+"', 'true'")
	}
	require.Contains(t, sql, "on conflict (key) do nothing")
	require.NotContains(t, sql, "on conflict (key) do update")
	require.Contains(t, sql, "status = 'active'")
	require.Contains(t, sql, "parent_account_id is null")
	require.Contains(t, sql, "openai_installation_pin_enabled")
	require.Contains(t, sql, "jsonb_typeof(coalesce(a.extra, '{}'::jsonb) -> 'openai_installation_pin_enabled') = 'boolean'")
	require.Contains(t, sql, "then (a.extra ->> 'openai_installation_pin_enabled')::boolean")
	require.Contains(t, sql, "gen_random_uuid()")
	require.Contains(t, sql, "partition by lower(extra ->> 'openai_pinned_installation_id')")
	require.Contains(t, sql, "row_number() over")
	require.Contains(t, sql, "inactive.status is distinct from 'active'")
	require.Contains(t, sql, "inactive.extra ->> 'openai_pinned_installation_id' = active.canonical_id")
	require.Contains(t, sql, "then lower(extra ->> 'openai_pinned_installation_id')")
	require.Contains(t, sql, "codex_fingerprint_mode")
	require.Contains(t, sql, "openai_installation_rotate_enabled")
	require.NotContains(t, sql, "openai_codex_version_auto_sync_enabled")
}
