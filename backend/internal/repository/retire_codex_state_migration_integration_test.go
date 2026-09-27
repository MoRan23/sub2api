//go:build integration

package repository

import (
	"context"
	"testing"

	dbmigrations "github.com/Wei-Shaw/sub2api/migrations"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestRetireCodexStateMigrationPreservesAuthorizationAndIdentity(t *testing.T) {
	ctx := context.Background()
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	schema := "retire_state_" + uuid.NewString()
	_, err = tx.ExecContext(ctx, `CREATE SCHEMA "`+schema+`"; SET LOCAL search_path TO "`+schema+`"`)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `
		CREATE TABLE accounts (
			id bigint PRIMARY KEY, extra jsonb, credentials jsonb, status text,
			schedulable boolean, rate_limit_reset_at timestamptz,
			codex_turn_state_retry_after timestamptz);
		CREATE TABLE account_openai_oauth_credentials (
			account_id bigint PRIMARY KEY REFERENCES accounts(id), authorization_generation text,
			state_generation text, revision bigint, auth_pause_owned boolean);
		CREATE TABLE account_openai_oauth_os_profiles (account_id bigint, os_family text, installation_id text, user_agent text, sync_session_id text);
		CREATE TABLE openai_oauth_daily_os_roots (pool_id bigint, os_family text, stream_session_id text);
		CREATE TABLE proxies (id bigint, route_generation bigint);
		CREATE TABLE settings (key text PRIMARY KEY, value text);
		CREATE TABLE openai_codex_state (owner_account_id bigint PRIMARY KEY REFERENCES accounts(id), encrypted_token text);
		CREATE TABLE openai_codex_state_business_leases (owner_account_id bigint REFERENCES openai_codex_state(owner_account_id));
		CREATE SEQUENCE openai_http_cookie_revision_seq;
		CREATE TABLE openai_http_cookies (revision bigint DEFAULT nextval('openai_http_cookie_revision_seq'), encrypted_value text);
		CREATE FUNCTION fence_openai_oauth_os_credential_configuration() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN UPDATE account_openai_oauth_credentials SET state_generation='unexpected-change' WHERE account_id=NEW.id; RETURN NEW; END $$;
		CREATE TRIGGER account_openai_oauth_os_configuration_fence AFTER UPDATE OF extra ON accounts FOR EACH ROW EXECUTE FUNCTION fence_openai_oauth_os_credential_configuration();
		INSERT INTO accounts VALUES (1,'{"codex_turn_state":{"enabled":true},"codex_turn_state_token":"old-secret","codex_turn_state_credential_epoch":"old-epoch","openai_pinned_installation_id":"keep-installation","note":"keep"}','{"access_token":"synthetic-access"}','error',false,'2030-01-01','2030-01-02');
		INSERT INTO account_openai_oauth_credentials VALUES (1,'auth-generation','state-generation',9,true);
		INSERT INTO account_openai_oauth_os_profiles VALUES (1,'linux','installation','UA','root');
		INSERT INTO openai_oauth_daily_os_roots VALUES (1,'linux','daily-root');
		INSERT INTO proxies VALUES (1,7);
		INSERT INTO settings VALUES ('codex_turn_state_models','["model"]'),('codex_turn_state_models_revision','revision'),('codex_telemetry_enabled','true');
		INSERT INTO openai_codex_state VALUES (1,'synthetic-encrypted-ticket');
		INSERT INTO openai_codex_state_business_leases VALUES (1);
		INSERT INTO openai_http_cookies (encrypted_value) VALUES ('synthetic-encrypted-cookie');
	`)
	require.NoError(t, err)
	migration, err := dbmigrations.FS.ReadFile("260_retire_codex_turn_state.sql")
	require.NoError(t, err)
	for range 2 {
		_, err = tx.ExecContext(ctx, string(migration))
		require.NoError(t, err)
	}
	for _, name := range []string{"openai_codex_state_business_leases", "openai_codex_state", "openai_http_cookies", "openai_http_cookie_revision_seq"} {
		var exists bool
		require.NoError(t, tx.QueryRowContext(ctx, "SELECT to_regclass($1) IS NOT NULL", name).Scan(&exists))
		require.False(t, exists, name)
	}
	var extra, credentials, status, authGeneration, stateGeneration string
	var schedulable, pauseOwned bool
	var revision int64
	require.NoError(t, tx.QueryRowContext(ctx, `
		SELECT a.extra::text,a.credentials::text,a.status,a.schedulable,c.authorization_generation,c.state_generation,c.revision,c.auth_pause_owned
		FROM accounts a JOIN account_openai_oauth_credentials c ON c.account_id=a.id
	`).Scan(&extra, &credentials, &status, &schedulable, &authGeneration, &stateGeneration, &revision, &pauseOwned))
	require.JSONEq(t, `{"openai_pinned_installation_id":"keep-installation","note":"keep"}`, extra)
	require.JSONEq(t, `{"access_token":"synthetic-access"}`, credentials)
	require.Equal(t, "error", status)
	require.False(t, schedulable)
	require.Equal(t, "auth-generation", authGeneration)
	require.Equal(t, "state-generation", stateGeneration, "cleanup must remove the configuration fence before changing extra")
	require.EqualValues(t, 9, revision)
	require.True(t, pauseOwned)
	var preserved bool
	require.NoError(t, tx.QueryRowContext(ctx, `
		SELECT a.rate_limit_reset_at='2030-01-01'::timestamptz
			AND EXISTS (SELECT 1 FROM account_openai_oauth_os_profiles WHERE installation_id='installation' AND sync_session_id='root')
			AND EXISTS (SELECT 1 FROM openai_oauth_daily_os_roots WHERE stream_session_id='daily-root')
			AND EXISTS (SELECT 1 FROM proxies WHERE route_generation=7)
			AND EXISTS (SELECT 1 FROM settings WHERE key='codex_telemetry_enabled' AND value='true')
			AND NOT EXISTS (SELECT 1 FROM settings WHERE key LIKE 'codex_turn_state%')
			AND NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='accounts' AND column_name='codex_turn_state_retry_after')
		FROM accounts a WHERE id=1
	`).Scan(&preserved))
	require.True(t, preserved)
}
