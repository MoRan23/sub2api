//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestOpenAIOAuthSharedMigrationSelectsWholeGrantAndFencesLegacy(t *testing.T) {
	ctx := context.Background()
	tx := testTx(t)
	schema := "oauth_shared_migration_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err := tx.ExecContext(ctx, `CREATE SCHEMA `+schema+`; SET LOCAL search_path TO `+schema+`,pg_catalog;
	CREATE TABLE accounts(id bigint PRIMARY KEY,platform text,type text,parent_account_id bigint,deleted_at timestamptz,credentials jsonb,extra jsonb,proxy_id bigint);
	CREATE TABLE account_openai_oauth_os_profiles(account_id bigint,os_family text,is_default boolean);
	INSERT INTO accounts(id,platform,type,credentials,extra) SELECT id,'openai','oauth','{"access_token":"obsolete-mirror","refresh_token":"obsolete-rt","model_mapping":{"gpt":"mapped"},"user_agent":"keep-UA"}'::jsonb,'{}'::jsonb FROM generate_series(1,8) id;
	INSERT INTO account_openai_oauth_os_profiles SELECT id,os,(CASE WHEN id=1 THEN os='macos' ELSE os='windows' END) FROM generate_series(1,8) id CROSS JOIN unnest(ARRAY['windows','macos','linux']) os WHERE id NOT IN (6,7);`)
	require.NoError(t, err)
	legacy, err := os.ReadFile("../../migrations/251_openai_oauth_os_credentials.sql")
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, string(legacy))
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `UPDATE account_openai_oauth_os_credentials SET status='unauthorized',credentials='{}'; INSERT INTO account_openai_oauth_authorization_migrations(account_id) VALUES(7)`)
	require.NoError(t, err)
	seed := func(id int, family, status, token, date string, expiredOnly bool) {
		t.Helper()
		credentials := oauthOSTestGrant(token)
		credentials["id_token"] = "id-" + token
		if expiredOnly {
			delete(credentials, "refresh_token")
			credentials["expires_at"] = "2000-01-01T00:00:00Z"
		}
		payload, err := json.Marshal(credentials)
		require.NoError(t, err)
		_, err = tx.ExecContext(ctx, `INSERT INTO account_openai_oauth_os_credentials(account_id,os_family,status,credentials,authorized_at) VALUES($1,$2,$3,$4::jsonb,$5::timestamptz) ON CONFLICT(account_id,os_family) DO UPDATE SET status=EXCLUDED.status,credentials=EXCLUDED.credentials,authorized_at=EXCLUDED.authorized_at`, id, family, status, string(payload), date)
		require.NoError(t, err)
	}
	seed(1, "macos", "authorized", "default-whole", "2020-01-01", false)
	seed(1, "windows", "authorized", "newer-other", "2025-01-01", false)
	seed(2, "macos", "authorized", "older-other", "2020-01-01", false)
	seed(2, "linux", "authorized", "latest-other", "2025-01-01", false)
	seed(3, "macos", "reauth_required", "failed-whole", "2025-01-01", false)
	seed(5, "windows", "authorized", "expired-default", "2025-01-01", true)
	seed(5, "linux", "authorized", "refreshable-other", "2020-01-01", false)
	seed(8, "windows", "authorized", "expired-only", "2025-01-01", true)
	var oldGeneration string
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT authorization_generation::text FROM account_openai_oauth_os_credentials WHERE account_id=1 AND os_family='macos'`).Scan(&oldGeneration))
	migration, err := os.ReadFile("../../migrations/255_openai_oauth_shared_credentials.sql")
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, string(migration))
	require.NoError(t, err)
	for _, expected := range []struct {
		id            int
		token, status string
	}{{1, "default-whole", "authorized"}, {2, "latest-other", "authorized"}, {3, "failed-whole", "reauth_required"}, {4, "", "unauthorized"}, {5, "refreshable-other", "authorized"}, {6, "obsolete-mirror", "authorized"}, {7, "", "unauthorized"}, {8, "expired-only", "reauth_required"}} {
		var credentials map[string]any
		var payload []byte
		var status string
		require.NoError(t, tx.QueryRowContext(ctx, `SELECT credentials,status FROM account_openai_oauth_credentials WHERE account_id=$1`, expected.id).Scan(&payload, &status))
		require.NoError(t, json.Unmarshal(payload, &credentials))
		require.Equal(t, expected.status, status, "account %d", expected.id)
		require.Equal(t, expected.token, service.OpenAIOAuthCredentialSubject(credentials, "access_token"), "account %d", expected.id)
		if expected.token != "" && expected.id != 6 {
			require.Equal(t, "id-"+expected.token, credentials["id_token"])
		}
		require.NotContains(t, credentials, "model_mapping")
	}
	var newGeneration string
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT authorization_generation::text FROM account_openai_oauth_credentials WHERE account_id=1`).Scan(&newGeneration))
	require.NotEqual(t, oldGeneration, newGeneration)
	var count int
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT count(*) FROM account_openai_oauth_os_credentials WHERE credentials<>'{}'::jsonb`).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT count(*) FROM account_openai_oauth_os_credentials s JOIN account_openai_oauth_credentials c USING(account_id) WHERE s.authorization_generation<>c.authorization_generation OR s.revision<>c.revision OR s.status<>c.status`).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT count(*) FROM account_openai_oauth_os_credentials`).Scan(&count))
	require.Equal(t, 24, count)
	var mirrorToken, model, ua string
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT credentials->>'access_token',credentials#>>'{model_mapping,gpt}',credentials->>'user_agent' FROM accounts WHERE id=1`).Scan(&mirrorToken, &model, &ua))
	require.Equal(t, "default-whole", mirrorToken)
	require.Equal(t, "mapped", model)
	require.Equal(t, "keep-UA", ua)
	var stateBefore, stateAfter string
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT state_generation::text FROM account_openai_oauth_os_credentials WHERE account_id=1 AND os_family='linux'`).Scan(&stateBefore))
	_, err = tx.ExecContext(ctx, string(migration))
	require.NoError(t, err)
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT state_generation::text FROM account_openai_oauth_os_credentials WHERE account_id=1 AND os_family='linux'`).Scan(&stateAfter))
	require.Equal(t, stateBefore, stateAfter)
	_, err = tx.ExecContext(ctx, `UPDATE account_openai_oauth_credentials SET credentials='{}',status='unauthorized',authorization_generation=gen_random_uuid() WHERE account_id=1`)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, string(migration))
	require.NoError(t, err)
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT count(*) FROM account_openai_oauth_credentials WHERE account_id=1 AND status='unauthorized' AND credentials='{}'`).Scan(&count))
	require.Equal(t, 1, count)
}

func (s *AccountRepoSuite) TestOAuthSharedCredentialsAllOSReadOneGrantAndRefreshFencesEveryIdentity() {
	account := &service.Account{Name: "one-shared-oauth", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, Credentials: oauthOSTestGrant("original")}
	s.Require().NoError(s.repo.Create(s.ctx, account))
	initial := make(map[string]*service.OpenAIOAuthOSCredential)
	for _, os := range service.OpenAIOAuthOSFamilies() {
		slot, err := s.repo.GetOpenAIOAuthOSCredential(s.ctx, account.ID, os)
		s.Require().NoError(err)
		s.Require().Equal(os, slot.OSFamily)
		s.Require().Equal("original", slot.Credentials["access_token"])
		initial[os] = slot
	}
	windows := initial[service.OpenAIOSWindows]
	applied, err := s.repo.PatchOpenAIOAuthOSCredentialsIfUnchanged(s.ctx, account.ID, service.OpenAIOSLinux, windows.AuthorizationGeneration, windows.Revision, nil, map[string]any{"access_token": "rotated", "refresh_token": "rotated-refresh"}, nil)
	s.Require().NoError(err)
	s.Require().True(applied)
	for _, os := range service.OpenAIOAuthOSFamilies() {
		slot, err := s.repo.GetOpenAIOAuthOSCredential(s.ctx, account.ID, os)
		s.Require().NoError(err)
		s.Require().Equal(windows.AuthorizationGeneration, slot.AuthorizationGeneration)
		s.Require().Equal(windows.Revision+1, slot.Revision)
		s.Require().Equal("rotated", slot.Credentials["access_token"])
		s.Require().Equal(initial[os].StateGeneration, slot.StateGeneration, "normal token refresh preserves the owner runtime generation")
	}
	slots, err := s.repo.ListOpenAIOAuthOSCredentials(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().Len(slots, 1)
	var tokenColumns int
	s.Require().NoError(scanSingleRow(s.ctx, s.repo.sql, `SELECT count(*) FROM information_schema.columns WHERE table_schema=current_schema() AND table_name IN ('account_openai_oauth_os_credentials','account_openai_oauth_credentials') AND column_name='credentials'`, nil, &tokenColumns))
	s.Require().Zero(tokenColumns)
}
