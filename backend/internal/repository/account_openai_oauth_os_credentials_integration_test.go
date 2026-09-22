//go:build integration

package repository

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func oauthOSTestGrant(token string) map[string]any {
	return map[string]any{"access_token": token, "refresh_token": "refresh-" + token, "chatgpt_account_id": "workspace-1", "chatgpt_user_id": "user-1", "expires_at": "2030-01-01T00:00:00Z"}
}

func (s *AccountRepoSuite) TestOAuthOSCredentialsSharedGrantAndNoResurrection() {
	account := mustCreateAccount(s.T(), s.client, &service.Account{Name: "legacy-os-grant", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Credentials: oauthOSTestGrant("legacy")})
	account.Credentials["user_agent"] = legacyProfileUA
	_, err := s.client.ExecContext(s.ctx, `UPDATE accounts SET credentials=credentials || jsonb_build_object('user_agent',$2::text) WHERE id=$1`, account.ID, legacyProfileUA)
	s.Require().NoError(err)
	profiles, err := s.repo.EnsureOpenAIOAuthOSProfiles(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().Equal(service.OpenAIOSLinux, profiles.DefaultOS)
	slots, err := s.repo.ListOpenAIOAuthOSCredentials(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().Len(slots, 1)
	s.Require().Equal(service.OpenAIOSLinux, slots[0].OSFamily)
	s.Require().Equal(service.OpenAIOAuthAuthorizationAuthorized, profiles.Profiles[service.OpenAIOSWindows].Authorization.Status)
	first := slots[0].AuthorizationGeneration
	_, err = s.repo.EnsureOpenAIOAuthOSProfiles(s.ctx, account.ID)
	s.Require().NoError(err)
	slot, err := s.repo.GetOpenAIOAuthOSCredential(s.ctx, account.ID, service.OpenAIOSLinux)
	s.Require().NoError(err)
	s.Require().Equal(first, slot.AuthorizationGeneration)
	s.Require().NoError(s.repo.RevokeOpenAIOAuthOSCredentials(s.ctx, account.ID, service.OpenAIOSLinux))
	s.Require().NoError(s.repo.UpdateCredentials(s.ctx, account.ID, map[string]any{"access_token": "stale-snapshot"}))
	_, err = s.repo.EnsureOpenAIOAuthOSProfiles(s.ctx, account.ID)
	s.Require().NoError(err)
	slot, err = s.repo.GetOpenAIOAuthOSCredential(s.ctx, account.ID, service.OpenAIOSLinux)
	s.Require().NoError(err)
	s.Require().Equal(service.OpenAIOAuthAuthorizationUnauthorized, slot.Status)
	s.Require().Empty(slot.Credentials)
}

func (s *AccountRepoSuite) TestOAuthOSCredentialsBindDefaultAndStaleSnapshots() {
	account := &service.Account{Name: "scoped-grants", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, Credentials: oauthOSTestGrant("windows")}
	s.Require().NoError(s.repo.Create(s.ctx, account))
	stale, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	linux, err := s.repo.BindOpenAIOAuthOSCredentials(s.ctx, account.ID, service.OpenAIOSLinux, oauthOSTestGrant("linux"), "test")
	s.Require().NoError(err)
	linux, err = s.repo.BindOpenAIOAuthOSCredentials(s.ctx, account.ID, service.OpenAIOSMacOS, oauthOSTestGrant("linux"), "test")
	s.Require().NoError(err, "another OS identity uses the same account authorization")
	other := oauthOSTestGrant("other")
	other["chatgpt_user_id"] = "different-user"
	other["access_token"] = "linux"
	linux, err = s.repo.BindOpenAIOAuthOSCredentials(s.ctx, account.ID, service.OpenAIOSMacOS, other, "test")
	s.Require().NoError(err, "explicit reauthorization may replace the account subject")
	_, err = s.repo.SetDefaultOpenAIOAuthOS(s.ctx, account.ID, service.OpenAIOSLinux)
	s.Require().NoError(err)
	stale.Credentials["auth_mode"] = "personalAccessToken"
	s.Require().NoError(s.repo.Update(s.ctx, stale))
	staleCredentials := oauthOSTestGrant("stale")
	staleCredentials["auth_mode"] = "agentIdentity"
	s.Require().NoError(s.repo.UpdateCredentials(s.ctx, account.ID, staleCredentials))
	staleBulk := oauthOSTestGrant("stale-bulk")
	staleBulk["openai_auth_mode"] = "agentIdentity"
	_, err = s.repo.BulkUpdate(s.ctx, []int64{account.ID}, service.AccountBulkUpdate{Credentials: staleBulk})
	s.Require().NoError(err)
	fresh, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().Equal("linux", fresh.GetCredential("access_token"))
	s.Require().True(service.IsOpenAIOAuthOSProfileOwner(fresh), "stale auth_mode snapshots cannot convert the current credential owner")
	s.Require().Equal(service.OpenAIOSLinux, fresh.OpenAIOAuthOSProfiles.DefaultOS)
	slot, err := s.repo.GetOpenAIOAuthOSCredential(s.ctx, account.ID, service.OpenAIOSLinux)
	s.Require().NoError(err)
	s.Require().Equal(linux.AuthorizationGeneration, slot.AuthorizationGeneration)
	_, err = s.repo.SetDefaultOpenAIOAuthOS(s.ctx, account.ID, service.OpenAIOSMacOS)
	s.Require().NoError(err)
}

func (s *AccountRepoSuite) TestOAuthOSCredentialsCASRejectsRevokeAndConcurrentRefresh() {
	s.client = testEntClient(s.T())
	s.repo = newAccountRepositoryWithSQL(s.client, integrationDB, nil)
	account := &service.Account{Name: "scoped-cas", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, Credentials: oauthOSTestGrant("windows")}
	s.Require().NoError(s.repo.Create(s.ctx, account))
	slot, err := s.repo.GetOpenAIOAuthOSCredential(s.ctx, account.ID, service.OpenAIOSWindows)
	s.T().Cleanup(func() {
		_, _ = s.client.ExecContext(context.Background(), `DELETE FROM scheduler_outbox WHERE account_id=$1`, account.ID)
		_, _ = s.client.ExecContext(context.Background(), `DELETE FROM accounts WHERE id=$1`, account.ID)
	})
	s.Require().NoError(err)
	var wg sync.WaitGroup
	successes := make(chan bool, 2)
	failures := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		osFamily := []string{service.OpenAIOSWindows, service.OpenAIOSLinux}[i]
		go func() {
			defer wg.Done()
			ok, err := s.repo.PatchOpenAIOAuthOSCredentialsIfUnchanged(context.Background(), account.ID, osFamily, slot.AuthorizationGeneration, slot.Revision, nil, map[string]any{"access_token": "refreshed"}, nil)
			successes <- ok
			failures <- err
		}()
	}
	wg.Wait()
	close(successes)
	close(failures)
	count := 0
	for ok := range successes {
		if ok {
			count++
		}
	}
	for err := range failures {
		s.Require().NoError(err)
	}
	s.Require().Equal(1, count)
	s.Require().NoError(s.repo.RevokeOpenAIOAuthOSCredentials(s.ctx, account.ID, service.OpenAIOSWindows))
	ok, err := s.repo.PatchOpenAIOAuthOSCredentialsIfUnchanged(s.ctx, account.ID, service.OpenAIOSWindows, slot.AuthorizationGeneration, slot.Revision, nil, map[string]any{"access_token": "late"}, nil)
	s.Require().NoError(err)
	s.Require().False(ok)
	_, err = s.repo.BindOpenAIOAuthOSCredentialsIfGeneration(s.ctx, account.ID, service.OpenAIOSWindows, slot.AuthorizationGeneration, oauthOSTestGrant("late-auth"), "oauth")
	s.Require().ErrorIs(err, service.ErrOpenAIOAuthOSAuthorizationChanged)
}

func TestOAuthOSCredentialProviderGuardContainsAllKeys(t *testing.T) {
	expression := guardedAccountCredentialsExpression("$1::jsonb")
	for _, key := range service.OpenAIOAuthProviderCredentialKeys() {
		require.Contains(t, expression, "'"+key+"'")
	}
}

func (s *AccountRepoSuite) TestOAuthOSCredentialsInitialImportIsAtomic() {
	s.client = testEntClient(s.T())
	s.repo = newAccountRepositoryWithSQL(s.client, integrationDB, nil)
	account := &service.Account{Name: "atomic-slot-import", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, OpenAIOAuthInitialOS: service.OpenAIOSMacOS, Credentials: oauthOSTestGrant("macos"), OpenAIOAuthInitialCredentials: map[string]map[string]any{service.OpenAIOSMacOS: oauthOSTestGrant("macos"), service.OpenAIOSLinux: oauthOSTestGrant("macos")}}
	account.OpenAIOAuthInitialCredentials[service.OpenAIOSMacOS] = map[string]any{}
	err := s.repo.Create(s.ctx, account)
	s.Require().ErrorIs(err, service.ErrOpenAIOAuthOSUnauthorized)
	_, err = s.repo.GetByID(s.ctx, account.ID)
	s.Require().ErrorIs(err, service.ErrAccountNotFound)
	account.ID = 0
	account.OpenAIOAuthInitialCredentials[service.OpenAIOSMacOS] = oauthOSTestGrant("macos")
	account.OpenAIOAuthInitialCredentials[service.OpenAIOSLinux] = oauthOSTestGrant("linux")
	s.Require().NoError(s.repo.Create(s.ctx, account))
	s.T().Cleanup(func() {
		_, _ = s.client.ExecContext(context.Background(), `DELETE FROM scheduler_outbox WHERE account_id=$1`, account.ID)
		_, _ = s.client.ExecContext(context.Background(), `DELETE FROM accounts WHERE id=$1`, account.ID)
	})
	slots, err := s.repo.ListOpenAIOAuthOSCredentials(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().Len(slots, 1)
	s.Require().Equal(service.OpenAIOSMacOS, account.OpenAIOAuthOSProfiles.DefaultOS)
	s.Require().Equal(service.OpenAIOAuthAuthorizationAuthorized, account.OpenAIOAuthOSProfiles.Profiles[service.OpenAIOSWindows].Authorization.Status)
}

func TestOAuthOSCredentialMigrationSQLDefaultOnlyAndIdempotent(t *testing.T) {
	ctx := context.Background()
	tx := testTx(t)
	schema := "oauth_os_migration_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err := tx.ExecContext(ctx, `CREATE SCHEMA `+schema+`; SET LOCAL search_path TO `+schema+`,pg_catalog;
	CREATE TABLE accounts(id bigint PRIMARY KEY,platform text,type text,parent_account_id bigint,deleted_at timestamptz,credentials jsonb,extra jsonb,proxy_id bigint);
	CREATE TABLE account_openai_oauth_os_profiles(account_id bigint,os_family text,is_default boolean);
	INSERT INTO accounts(id,platform,type,credentials,extra) VALUES(1,'openai','oauth','{"access_token":"legacy","refresh_token":"legacy-refresh","chatgpt_account_id":"a","chatgpt_user_id":"u","model_mapping":{"gpt":"mapped"}}','{}');
	INSERT INTO account_openai_oauth_os_profiles VALUES(1,'macos',true),(1,'windows',false),(1,'linux',false);`)
	require.NoError(t, err)
	migration, err := os.ReadFile("../../migrations/251_openai_oauth_os_credentials.sql")
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, string(migration))
	require.NoError(t, err)
	var count int
	var family, generation string
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM account_openai_oauth_os_credentials`).Scan(&count))
	require.Equal(t, 1, count)
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT os_family,authorization_generation::text FROM account_openai_oauth_os_credentials`).Scan(&family, &generation))
	require.Equal(t, service.OpenAIOSMacOS, family)
	var configCopied bool
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT credentials ? 'model_mapping' FROM account_openai_oauth_os_credentials`).Scan(&configCopied))
	require.False(t, configCopied)
	_, err = tx.ExecContext(ctx, `UPDATE account_openai_oauth_os_credentials SET credentials='{}',status='unauthorized'`)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, string(migration))
	require.NoError(t, err)
	var afterGeneration, status string
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT authorization_generation::text,status FROM account_openai_oauth_os_credentials`).Scan(&afterGeneration, &status))
	require.Equal(t, generation, afterGeneration)
	require.Equal(t, service.OpenAIOAuthAuthorizationUnauthorized, status)
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM account_openai_oauth_os_credentials`).Scan(&count))
	require.Equal(t, 1, count)
	_, err = tx.ExecContext(ctx, `UPDATE accounts SET extra='{"codex_turn_state":{"enabled":true,"collector_proxy_id":12}}'`)
	require.NoError(t, err)
	var stateGeneration string
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT state_generation::text FROM account_openai_oauth_os_credentials`).Scan(&stateGeneration))
	_, err = tx.ExecContext(ctx, `UPDATE accounts SET extra='{"codex_turn_state":{"enabled":true,"collector_proxy_ids":[12]}}'`)
	require.NoError(t, err)
	var afterStateGeneration string
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT state_generation::text FROM account_openai_oauth_os_credentials`).Scan(&afterStateGeneration))
	require.Equal(t, stateGeneration, afterStateGeneration, "format-only proxy list normalization preserves every slot state fence")
}

func (s *AccountRepoSuite) TestOAuthOSCredentialsErrorsAndRevokeApplyToSharedAuthorization() {
	account := &service.Account{Name: "isolated-slot-errors", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, Credentials: oauthOSTestGrant("windows")}
	s.Require().NoError(s.repo.Create(s.ctx, account))
	_, err := s.repo.BindOpenAIOAuthOSCredentials(s.ctx, account.ID, service.OpenAIOSLinux, oauthOSTestGrant("linux"), "test")
	s.Require().NoError(err)
	_, err = s.repo.SetDefaultOpenAIOAuthOS(s.ctx, account.ID, service.OpenAIOSLinux)
	s.Require().NoError(err)
	windows, err := s.repo.GetOpenAIOAuthOSCredential(s.ctx, account.ID, service.OpenAIOSWindows)
	s.Require().NoError(err)
	applied, err := s.repo.SetOpenAIOAuthOSCredentialErrorIfUnchanged(s.ctx, account.ID, service.OpenAIOSWindows, windows.AuthorizationGeneration, windows.Revision, "secret-provider-token-error")
	s.Require().NoError(err)
	s.Require().True(applied)
	fresh, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().Equal(service.OpenAIOAuthAuthorizationReauthRequired, fresh.OpenAIOAuthOSProfiles.Profiles[service.OpenAIOSLinux].Authorization.Status)
	s.Require().Equal(service.OpenAIOAuthAuthorizationReauthRequired, fresh.OpenAIOAuthOSProfiles.Profiles[service.OpenAIOSWindows].Authorization.Status)
	s.Require().NotContains(fresh.OpenAIOAuthOSProfiles.Profiles[service.OpenAIOSWindows].Authorization.LastError, "secret-provider-token")
	windows, err = s.repo.GetOpenAIOAuthOSCredential(s.ctx, account.ID, service.OpenAIOSWindows)
	s.Require().NoError(err)
	applied, err = s.repo.PatchOpenAIOAuthOSCredentialsIfUnchanged(s.ctx, account.ID, service.OpenAIOSWindows, windows.AuthorizationGeneration, windows.Revision, nil, map[string]any{"access_token": "refreshed"}, nil)
	s.Require().NoError(err)
	s.Require().True(applied, "a refresh of current account credentials is not gated by legacy authorization metadata")
	fresh, err = s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().Equal("refreshed", fresh.GetOpenAIAccessToken())
	frozen, err := service.ResolveOpenAIOAuthCredentialAccount(s.ctx, s.repo, fresh, service.OpenAIOSWindows)
	s.Require().NoError(err)
	s.Require().NoError(s.repo.RevokeOpenAIOAuthOSCredentials(s.ctx, account.ID, service.OpenAIOSLinux))
	fresh, err = s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().Equal(service.OpenAIOSLinux, fresh.OpenAIOAuthOSProfiles.DefaultOS)
	_, err = service.ReloadOpenAIOAuthCredentialAccount(s.ctx, s.repo, frozen)
	s.Require().ErrorIs(err, service.ErrOpenAIOAuthOSAuthorizationChanged)
	for _, os := range []string{"", service.OpenAIOSWindows} {
		resolved, err := service.ResolveOpenAIOAuthCredentialAccount(s.ctx, s.repo, fresh, os)
		s.Require().NoError(err, "identity selection does not decide authorization")
		s.Require().Empty(resolved.GetOpenAIAccessToken())
		s.Require().Empty(resolved.GetOpenAIRefreshToken())
		token, err := service.NewOpenAITokenProvider(s.repo, nil, nil).GetAccessToken(s.ctx, resolved)
		s.Require().Error(err, "the original token provider still rejects missing credentials")
		s.Require().Empty(token)
	}
	_, err = s.repo.BindOpenAIOAuthOSCredentials(s.ctx, account.ID, service.OpenAIOSMacOS, oauthOSTestGrant("renewed"), "test")
	s.Require().NoError(err)
	windows, err = s.repo.GetOpenAIOAuthOSCredential(s.ctx, account.ID, service.OpenAIOSWindows)
	s.Require().NoError(err)
	applied, err = s.repo.SetOpenAIOAuthOSCredentialCooldownIfUnchanged(s.ctx, account.ID, service.OpenAIOSWindows, windows.AuthorizationGeneration, windows.Revision, time.Now().Add(time.Minute), "secret-transient-error")
	s.Require().NoError(err)
	s.Require().True(applied)
	_, err = s.repo.SetDefaultOpenAIOAuthOS(s.ctx, account.ID, service.OpenAIOSWindows)
	s.Require().NoError(err, "default identity remains independently selectable during a shared cooldown")
}

func (s *AccountRepoSuite) TestOAuthOSCredentialsRestorePreservesDefaultIdentityAndSharesGrant() {
	account := &service.Account{Name: "restore-revoked-default", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, OpenAIOAuthInitialOS: service.OpenAIOSWindows, Credentials: map[string]any{}, OpenAIOAuthInitialCredentials: map[string]map[string]any{service.OpenAIOSMacOS: oauthOSTestGrant("macos")}}
	s.Require().NoError(s.repo.Create(s.ctx, account))
	s.Require().Equal(service.OpenAIOSWindows, account.OpenAIOAuthOSProfiles.DefaultOS)
	s.Require().Equal(service.OpenAIOAuthAuthorizationAuthorized, account.OpenAIOAuthOSProfiles.Profiles[service.OpenAIOSWindows].Authorization.Status)
	s.Require().Equal(service.OpenAIOAuthAuthorizationAuthorized, account.OpenAIOAuthOSProfiles.Profiles[service.OpenAIOSMacOS].Authorization.Status)
	_, err := service.ResolveOpenAIOAuthCredentialAccount(s.ctx, s.repo, account, "")
	s.Require().NoError(err)
	resolved, err := service.ResolveOpenAIOAuthCredentialAccount(s.ctx, s.repo, account, service.OpenAIOSMacOS)
	s.Require().NoError(err)
	s.Require().Equal("macos", resolved.GetCredential("access_token"))
}
