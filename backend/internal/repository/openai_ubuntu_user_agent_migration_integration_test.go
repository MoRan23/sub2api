//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	dbmigrations "github.com/Wei-Shaw/sub2api/migrations"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestMigration273NormalizesStoredOpenAIUbuntuUserAgents(t *testing.T) {
	const oldUA = "codex-tui/0.163.0 (Ubuntu 24.04.4; x86_64) unknown (codex-tui; 0.163.0)"
	const newUA = "codex-tui/0.163.0 (Ubuntu 24.4.0; x86_64) unknown (codex-tui; 0.163.0)"
	const windowsUA = "codex-tui/0.163.0 (Windows 10.0.26200; x86_64) WindowsTerminal (codex-tui; 0.163.0)"
	ctx := context.Background()
	tx := testTx(t)
	migrationSQL, err := dbmigrations.FS.ReadFile("273_normalize_openai_ubuntu_user_agents.sql")
	require.NoError(t, err)
	oldTime := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

	type fixture struct {
		name, platform, accountType string
		ua, wantUA                  any
		profileUA, wantProfileUA    string
		deleted, changed            bool
		id                          int64
		installation, syncRoot      string
		wantCredentials, extra      string
	}
	fixtures := []fixture{
		{name: "oauth-mirror", platform: "openai", accountType: "oauth", ua: oldUA, wantUA: newUA,
			profileUA: oldUA, wantProfileUA: newUA, changed: true},
		{name: "profile-only", platform: "openai", accountType: "oauth", ua: windowsUA, wantUA: windowsUA,
			profileUA: oldUA, wantProfileUA: newUA, changed: true},
		{name: "api-key", platform: "openai", accountType: "apikey", ua: oldUA, wantUA: newUA, changed: true},
		{name: "custom-client-terminal", platform: "openai", accountType: "apikey",
			ua:     "codex_vscode/0.162.0 (Ubuntu 24.04.4; arm64) screen-256color (codex_vscode; 0.162.0)",
			wantUA: "codex_vscode/0.162.0 (Ubuntu 24.4.0; arm64) screen-256color (codex_vscode; 0.162.0)", changed: true},
		{name: "already-normalized", platform: "openai", accountType: "oauth", ua: newUA, wantUA: newUA,
			profileUA: newUA, wantProfileUA: newUA},
		{name: "longer-version", platform: "openai", accountType: "apikey",
			ua: strings.ReplaceAll(oldUA, "24.04.4;", "24.04.40;"), wantUA: strings.ReplaceAll(oldUA, "24.04.4;", "24.04.40;")},
		{name: "other-version", platform: "openai", accountType: "apikey",
			ua: strings.ReplaceAll(oldUA, "24.04.4;", "24.04.0;"), wantUA: strings.ReplaceAll(oldUA, "24.04.4;", "24.04.0;")},
		{name: "client-version", platform: "openai", accountType: "apikey",
			ua: "codex-tui/24.04.4 (Ubuntu 24.4.0; x86_64) unknown", wantUA: "codex-tui/24.04.4 (Ubuntu 24.4.0; x86_64) unknown"},
		{name: "other-platform", platform: "anthropic", accountType: "apikey", ua: oldUA, wantUA: oldUA},
		{name: "deleted", platform: "openai", accountType: "oauth", ua: oldUA, wantUA: oldUA,
			profileUA: oldUA, wantProfileUA: oldUA, deleted: true},
		{name: "missing", platform: "openai", accountType: "apikey"},
		{name: "non-string", platform: "openai", accountType: "apikey",
			ua: map[string]string{"value": oldUA}, wantUA: map[string]string{"value": oldUA}},
	}
	for i := range fixtures {
		f := &fixtures[i]
		credentials := map[string]any{"api_key": "synthetic-key", "model_mapping": map[string]string{"alias": "target"}}
		if f.ua != nil {
			credentials["user_agent"] = f.ua
		}
		payload, err := json.Marshal(credentials)
		require.NoError(t, err)
		var deletedAt any
		if f.deleted {
			deletedAt = oldTime
		}
		require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO accounts
			(name, platform, type, credentials, updated_at, deleted_at, schedulable)
			VALUES ($1,$2,$3,$4::jsonb,$5,$6,false) RETURNING id, extra`,
			"migration-273-"+f.name, f.platform, f.accountType, string(payload), oldTime, deletedAt).Scan(&f.id, &f.extra))
		if f.wantUA != nil {
			credentials["user_agent"] = f.wantUA
		}
		payload, err = json.Marshal(credentials)
		require.NoError(t, err)
		f.wantCredentials = string(payload)
		if f.profileUA != "" {
			f.installation = uuid.NewString()
			root, err := uuid.NewV7()
			require.NoError(t, err)
			f.syncRoot = root.String()
			_, err = tx.ExecContext(ctx, `INSERT INTO account_openai_oauth_os_profiles
				(account_id,os_family,installation_id,sync_session_id,user_agent,is_default,updated_at)
				VALUES ($1,'linux',$2::uuid,$3::uuid,$4,false,$5)`, f.id, f.installation, f.syncRoot, f.profileUA, oldTime)
			require.NoError(t, err)
		}
	}

	for run := 1; run <= 2; run++ {
		_, err = tx.ExecContext(ctx, string(migrationSQL))
		require.NoError(t, err)
		for _, f := range fixtures {
			var credentials, extra string
			var updatedAt time.Time
			var schedulable bool
			require.NoError(t, tx.QueryRowContext(ctx, `SELECT credentials,extra,updated_at,schedulable FROM accounts WHERE id=$1`,
				f.id).Scan(&credentials, &extra, &updatedAt, &schedulable))
			require.JSONEq(t, f.wantCredentials, credentials, "%s run %d", f.name, run)
			require.JSONEq(t, f.extra, extra, "%s extra", f.name)
			require.False(t, schedulable, f.name)
			if f.changed {
				require.True(t, updatedAt.After(oldTime), f.name)
			} else {
				require.True(t, updatedAt.Equal(oldTime), f.name)
			}
			var events int
			require.NoError(t, tx.QueryRowContext(ctx, `SELECT count(*) FROM scheduler_outbox
				WHERE event_type='account_changed' AND account_id=$1`, f.id).Scan(&events))
			wantEvents := 0
			if f.changed {
				wantEvents = 1
			}
			require.Equal(t, wantEvents, events, "%s run %d", f.name, run)
			if f.profileUA == "" {
				continue
			}
			var ua, installation, syncRoot string
			var isDefault bool
			require.NoError(t, tx.QueryRowContext(ctx, `SELECT user_agent,installation_id::text,sync_session_id::text,is_default
				FROM account_openai_oauth_os_profiles WHERE account_id=$1 AND os_family='linux'`,
				f.id).Scan(&ua, &installation, &syncRoot, &isDefault))
			require.Equal(t, f.wantProfileUA, ua, f.name)
			require.Equal(t, f.installation, installation, f.name)
			require.Equal(t, f.syncRoot, syncRoot, f.name)
			require.False(t, isDefault, f.name)
		}
	}
}
