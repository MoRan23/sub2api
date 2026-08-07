package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenAIEnvironmentFingerprintMigration(t *testing.T) {
	content, err := FS.ReadFile("198_backfill_openai_environment_user_agents.sql")
	require.NoError(t, err)
	sql := strings.ToLower(string(content))

	require.Contains(t, sql, "credentials ->> 'user_agent'")
	require.Contains(t, sql, "btrim(coalesce")
	require.Contains(t, sql, "a.type in ('oauth', 'apikey')")
	require.Contains(t, sql, "a.parent_account_id is null")
	require.Contains(t, sql, "parent_account_id is not null")
	require.Contains(t, sql, "credentials ? 'user_agent'")
	for _, fingerprint := range []string{
		"(ubuntu 22.4.0; x86_64) xterm-256color",
		"(ubuntu 22.4.0; x86_64) screen-256color",
		"(ubuntu 24.04.0; x86_64) xterm-256color",
		"(ubuntu 24.04.0; arm64) xterm-256color",
		"(mac os x 14.7.0; arm64) iterm.app",
		"(mac os x 15.1.0; arm64) iterm.app",
		"(windows 10.0.19045; x86_64) windowsterminal",
		"(windows 11.0.26100; x86_64) windowsterminal",
	} {
		require.Contains(t, sql, fingerprint)
	}
}
