package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRetireCodexStateMigrationHasNarrowRemovalBoundary(t *testing.T) {
	body, err := FS.ReadFile("260_retire_codex_turn_state.sql")
	require.NoError(t, err)
	sql := strings.ToLower(string(body))
	require.Less(t, strings.Index(sql, "drop trigger"), strings.Index(sql, "drop function"))
	require.Less(t, strings.Index(sql, "drop function"), strings.Index(sql, "update accounts"))
	for _, table := range []string{"openai_codex_state_business_leases", "openai_codex_state", "openai_http_cookies"} {
		require.Contains(t, sql, "drop table if exists "+table+";")
	}
	require.NotContains(t, sql, "cascade")
	for _, retained := range []string{"account_openai_oauth_credentials", "account_openai_oauth_os_credentials", "account_openai_oauth_os_profiles", "openai_oauth_daily_os_roots", "proxies"} {
		require.NotContains(t, sql, "drop table if exists "+retained)
		require.NotContains(t, sql, "delete from "+retained)
	}
	for _, field := range []string{"status", "schedulable", "credentials", "rate_limit_reset_at"} {
		require.NotContains(t, sql, "set "+field+" =")
		require.NotContains(t, sql, "drop column if exists "+field)
	}
}
