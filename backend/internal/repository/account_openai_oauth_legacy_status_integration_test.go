//go:build integration

package repository

import (
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func (s *AccountRepoSuite) TestOAuthCredentialCASUsesCurrentAccountCredentialsDespiteLegacyStatus() {
	for _, status := range []string{service.OpenAIOAuthAuthorizationUnauthorized, service.OpenAIOAuthAuthorizationReauthRequired} {
		s.Run(status, func() {
			account := &service.Account{
				Name: "legacy-auth-status-" + status, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
				Status: service.StatusActive, Schedulable: true, Credentials: oauthOSTestGrant("current"),
			}
			s.Require().NoError(s.repo.Create(s.ctx, account))
			_, err := s.client.ExecContext(s.ctx, `UPDATE account_openai_oauth_credentials SET status=$2,refresh_retry_after=NOW()+INTERVAL '1 hour' WHERE account_id=$1`, account.ID, status)
			s.Require().NoError(err)
			before, err := s.repo.GetOpenAIOAuthOSCredential(s.ctx, account.ID, service.OpenAIOSLinux)
			s.Require().NoError(err)
			s.Require().Equal(status, before.Status)
			s.Require().Equal("current", before.Credentials["access_token"])

			page, err := s.repo.ListOAuthRefreshCandidatePage(s.ctx, service.OAuthRefreshPageOptions{
				Platforms: []string{service.PlatformOpenAI}, AfterID: account.ID - 1, Limit: 1,
				ActiveOnly: true, RequireRefreshToken: true, ExcludeRetryCooldown: true,
			})
			s.Require().NoError(err)
			s.Require().Len(page.Accounts, 1, "stale metadata status and retry time do not exclude an active account refresh token")
			s.Require().Equal(account.ID, page.Accounts[0].ID)

			applied, err := s.repo.PatchOpenAIOAuthOSCredentialsIfUnchanged(s.ctx, account.ID, service.OpenAIOSWindows,
				before.AuthorizationGeneration, before.Revision, nil, map[string]any{"access_token": "refreshed", "refresh_token": "refresh-rotated"}, nil)
			s.Require().NoError(err)
			s.Require().True(applied)
			current, err := s.repo.GetOpenAIOAuthOSCredential(s.ctx, account.ID, service.OpenAIOSMacOS)
			s.Require().NoError(err)
			s.Require().Equal(before.AuthorizationGeneration, current.AuthorizationGeneration)
			s.Require().Equal(before.Revision+1, current.Revision)
			s.Require().Equal(service.OpenAIOAuthAuthorizationAuthorized, current.Status)
			s.Require().Nil(current.RefreshRetryAfter)
			s.Require().Equal("refreshed", current.Credentials["access_token"])
			s.Require().Equal("refresh-rotated", current.Credentials["refresh_token"])

			applied, err = s.repo.PatchOpenAIOAuthOSCredentialsIfUnchanged(s.ctx, account.ID, service.OpenAIOSLinux,
				before.AuthorizationGeneration, before.Revision, nil, map[string]any{"access_token": "stale-refresh"}, nil)
			s.Require().NoError(err)
			s.Require().False(applied, "legacy status tolerance must preserve revision fencing")
			s.Require().NoError(s.repo.RevokeOpenAIOAuthOSCredentials(s.ctx, account.ID, service.OpenAIOSLinux))
			applied, err = s.repo.PatchOpenAIOAuthOSCredentialsIfUnchanged(s.ctx, account.ID, service.OpenAIOSWindows,
				current.AuthorizationGeneration, current.Revision, nil, map[string]any{"access_token": "late-refresh"}, nil)
			s.Require().NoError(err)
			s.Require().False(applied, "revocation rotates generation and rejects the in-flight refresh")
			revoked, err := s.repo.GetOpenAIOAuthOSCredential(s.ctx, account.ID, service.OpenAIOSLinux)
			s.Require().NoError(err)
			s.Require().Empty(revoked.Credentials)
		})
	}
}

func (s *AccountRepoSuite) TestOAuthCredentialStateCASIgnoresLegacyStatusAndPreservesAccountCooldown() {
	for _, operation := range []string{"error", "cooldown"} {
		s.Run(operation, func() {
			account := &service.Account{
				Name: "legacy-state-cas-" + operation, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
				Status: service.StatusActive, Schedulable: true, Credentials: oauthOSTestGrant("current"),
			}
			s.Require().NoError(s.repo.Create(s.ctx, account))
			_, err := s.client.ExecContext(s.ctx, `UPDATE account_openai_oauth_credentials SET status='unauthorized' WHERE account_id=$1`, account.ID)
			s.Require().NoError(err)
			before, err := s.repo.GetOpenAIOAuthOSCredential(s.ctx, account.ID, service.OpenAIOSLinux)
			s.Require().NoError(err)
			until := time.Now().UTC().Add(time.Hour).Truncate(time.Microsecond)
			var applied bool
			if operation == "error" {
				applied, err = s.repo.SetOpenAIOAuthOSCredentialErrorIfUnchanged(s.ctx, account.ID, service.OpenAIOSWindows,
					before.AuthorizationGeneration, before.Revision, "current-account-failure")
			} else {
				applied, err = s.repo.SetOpenAIOAuthOSCredentialCooldownIfUnchanged(s.ctx, account.ID, service.OpenAIOSWindows,
					before.AuthorizationGeneration, before.Revision, until, "current-account-retry")
			}
			s.Require().NoError(err)
			s.Require().True(applied, "legacy metadata status cannot suppress a current credential failure")
			fresh, err := s.repo.GetByID(s.ctx, account.ID)
			s.Require().NoError(err)
			if operation == "error" {
				s.Require().Equal(service.StatusError, fresh.Status)
				s.Require().False(fresh.Schedulable)
			} else {
				s.Require().Equal(until, *fresh.TempUnschedulableUntil)
				s.Require().NoError(s.repo.SetTempUnschedulable(s.ctx, account.ID, until.Add(time.Minute), "token refresh retry exhausted: current-account-retry"))
				page, err := s.repo.ListOAuthRefreshCandidatePage(s.ctx, service.OAuthRefreshPageOptions{
					Platforms: []string{service.PlatformOpenAI}, AfterID: account.ID - 1, Limit: 1,
					ActiveOnly: true, RequireRefreshToken: true, ExcludeRetryCooldown: true,
				})
				s.Require().NoError(err)
				s.Require().Empty(page.Accounts, "the account retry cooldown still excludes OpenAI refresh candidates")
			}
			applied, err = s.repo.SetOpenAIOAuthOSCredentialErrorIfUnchanged(s.ctx, account.ID, service.OpenAIOSLinux,
				before.AuthorizationGeneration, before.Revision, "stale-failure")
			s.Require().NoError(err)
			s.Require().False(applied, "stale revisions cannot overwrite a newer credential result")
		})
	}
}
