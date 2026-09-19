package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigration245BackfillsCredentialEpochWithoutEnablingTurnState(t *testing.T) {
	content, err := FS.ReadFile("245_openai_codex_state_credential_epoch.sql")
	require.NoError(t, err)
	sql := strings.ToLower(string(content))
	statements := strings.Split(sql, ";\n")
	require.Len(t, statements, 3)
	backfill, cleanup := statements[0], statements[1]

	for _, statement := range []string{backfill, cleanup} {
		require.Contains(t, statement, "where deleted_at is null")
		require.Contains(t, statement, "platform = 'openai'")
		require.Contains(t, statement, "type = 'oauth'")
		require.Contains(t, statement, "parent_account_id is null")
		for _, key := range []string{"auth_mode", "openai_auth_mode"} {
			require.Contains(t, statement, "lower(btrim(coalesce(credentials ->> '"+key+"', ''))) not in ('personalaccesstoken', 'personal_access_token', 'agentidentity')")
		}
		require.NotContains(t, statement, "status =")
		require.NotContains(t, statement, "credentials =")
		require.NotContains(t, statement, "'codex_turn_state'")
		require.NotContains(t, statement, "'codex_turn_state_generation'")
	}

	require.Contains(t, backfill, "jsonb_set(\n    coalesce(extra, '{}'::jsonb)")
	require.Contains(t, backfill, "'{codex_turn_state_credential_epoch}'")
	require.Contains(t, backfill, "to_jsonb(gen_random_uuid()::text)")
	// Missing/null/non-string/blank epochs are repaired; opaque non-empty string
	// values have no UUID-format requirement and must survive migration reruns.
	require.Contains(t, backfill, "jsonb_typeof(extra -> 'codex_turn_state_credential_epoch') is distinct from 'string'")
	require.Contains(t, backfill, "or btrim(coalesce(extra ->> 'codex_turn_state_credential_epoch', '')) = ''")
	require.NotContains(t, backfill, "~")

	require.Contains(t, cleanup, "set extra = extra - 'codex_turn_state_credential_epoch'")
	require.Contains(t, cleanup, "and extra ? 'codex_turn_state_credential_epoch'")
	require.Contains(t, cleanup, "and not (")
	require.NotContains(t, cleanup, "gen_random_uuid()")
}
