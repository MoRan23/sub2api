package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigration222EnablesMissingOpenAIUUIDv7IdentitySetting(t *testing.T) {
	content, err := FS.ReadFile("222_enable_openai_uuidv7_session_identity_by_default.sql")
	require.NoError(t, err)
	sql := strings.ToLower(string(content))

	require.Contains(t, sql, "insert into settings")
	require.Contains(t, sql, "'enable_openai_uuidv7_session_identity', 'true'")
	require.Contains(t, sql, "on conflict (key) do nothing")
	require.NotContains(t, sql, "do update")
}
