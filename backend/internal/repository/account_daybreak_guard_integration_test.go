//go:build integration

package repository

import (
	"context"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type daybreakIntegrationReader func(context.Context, *service.Account) (*service.OpenAIDaybreakCapabilities, error)

func (f daybreakIntegrationReader) GetOpenAIDaybreakCapabilities(ctx context.Context, a *service.Account) (*service.OpenAIDaybreakCapabilities, error) {
	return f(ctx, a)
}

func (s *AccountRepoSuite) TestDaybreakAdminGrantCASAndStaleSnapshots() {
	account := mustCreateAccount(s.T(), s.client, &service.Account{Name: "daybreak-cas", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Credentials: oauthOSTestGrant("daybreak")})
	_, err := s.repo.EnsureOpenAIOAuthOSProfiles(s.ctx, account.ID)
	s.Require().NoError(err)
	admin := service.NewAdminService(nil, nil, nil, s.repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, s.client, nil, nil, nil, nil, nil, nil, nil, nil)
	reader := daybreakIntegrationReader(func(ctx context.Context, a *service.Account) (*service.OpenAIDaybreakCapabilities, error) {
		slot, err := s.repo.GetOpenAIOAuthOSCredential(ctx, a.ID, "")
		if err != nil {
			return nil, err
		}
		return &service.OpenAIDaybreakCapabilities{BlueAvailable: true, RedAvailable: true, CredentialOwnerID: a.ID, CredentialOS: slot.OSFamily, AuthorizationGeneration: slot.AuthorizationGeneration}, nil
	})
	setter := admin.(interface {
		SetOpenAIDaybreakCapabilityReader(service.OpenAIDaybreakCapabilityReader)
	})
	setter.SetOpenAIDaybreakCapabilityReader(reader)
	stale, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	_, err = admin.UpdateAccount(s.ctx, account.ID, &service.UpdateAccountInput{Extra: map[string]any{service.OpenAIDaybreakBlueEnabledKey: true, service.OpenAIDaybreakRedEnabledKey: true}})
	s.Require().NoError(err)
	stale.Extra[service.OpenAIDaybreakBlueEnabledKey] = false
	s.Require().NoError(s.repo.Update(s.ctx, stale))
	s.Require().NoError(s.repo.UpdateExtra(s.ctx, account.ID, map[string]any{service.OpenAIDaybreakRedEnabledKey: false}))
	stored, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().Equal(true, stored.Extra[service.OpenAIDaybreakBlueEnabledKey])
	s.Require().Equal(true, stored.Extra[service.OpenAIDaybreakRedEnabledKey])
	s.Require().NoError(admin.UpdateAccountExtra(s.ctx, account.ID, map[string]any{service.OpenAIDaybreakBlueEnabledKey: false}))
	stored, err = s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().Equal(false, stored.Extra[service.OpenAIDaybreakBlueEnabledKey])
	s.Require().Equal(false, stored.Extra[service.OpenAIDaybreakRedEnabledKey])
	setter.SetOpenAIDaybreakCapabilityReader(daybreakIntegrationReader(func(ctx context.Context, a *service.Account) (*service.OpenAIDaybreakCapabilities, error) {
		capability, err := reader(ctx, a)
		if err != nil {
			return nil, err
		}
		_, err = s.client.ExecContext(ctx, `UPDATE account_openai_oauth_credentials SET authorization_generation=gen_random_uuid() WHERE account_id=$1`, a.ID)
		return capability, err
	}))
	err = admin.UpdateAccountExtra(s.ctx, account.ID, map[string]any{service.OpenAIDaybreakBlueEnabledKey: true})
	s.Require().Equal("DAYBREAK_CAPABILITY_STALE", infraerrors.Reason(err))
}

func (s *AccountRepoSuite) TestDaybreakAdminAllWritesRecheckConcurrentBlueDisable() {
	account := mustCreateAccount(s.T(), s.client, &service.Account{Name: "daybreak-dependency-race", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Credentials: oauthOSTestGrant("daybreak-race")})
	_, err := s.repo.EnsureOpenAIOAuthOSProfiles(s.ctx, account.ID)
	s.Require().NoError(err)
	admin := service.NewAdminService(nil, nil, nil, s.repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, s.client, nil, nil, nil, nil, nil, nil, nil, nil)
	admin.(interface {
		SetOpenAIDaybreakCapabilityReader(service.OpenAIDaybreakCapabilityReader)
	}).SetOpenAIDaybreakCapabilityReader(daybreakIntegrationReader(func(ctx context.Context, a *service.Account) (*service.OpenAIDaybreakCapabilities, error) {
		slot, err := s.repo.GetOpenAIOAuthOSCredential(ctx, a.ID, "")
		if err != nil {
			return nil, err
		}
		// Another administrator disables Blue after the outer request read it.
		_, err = s.client.ExecContext(ctx, `UPDATE accounts SET extra=extra || '{"openai_daybreak_blue_enabled":false,"openai_daybreak_red_enabled":false}'::jsonb WHERE id=$1`, a.ID)
		return &service.OpenAIDaybreakCapabilities{BlueAvailable: true, RedAvailable: true, CredentialOwnerID: a.ID, CredentialOS: slot.OSFamily, AuthorizationGeneration: slot.AuthorizationGeneration}, err
	}))
	for _, write := range []func() error{
		func() error {
			_, err := admin.UpdateAccount(s.ctx, account.ID, &service.UpdateAccountInput{Extra: map[string]any{service.OpenAIDaybreakRedEnabledKey: true}})
			return err
		},
		func() error {
			return admin.UpdateAccountExtra(s.ctx, account.ID, map[string]any{service.OpenAIDaybreakRedEnabledKey: true})
		},
		func() error {
			_, err := admin.BulkUpdateAccounts(s.ctx, &service.BulkUpdateAccountsInput{AccountIDs: []int64{account.ID}, Extra: map[string]any{service.OpenAIDaybreakRedEnabledKey: true}})
			return err
		},
	} {
		_, err := s.client.ExecContext(s.ctx, `UPDATE accounts SET extra=extra || '{"openai_daybreak_blue_enabled":true,"openai_daybreak_red_enabled":false}'::jsonb WHERE id=$1`, account.ID)
		s.Require().NoError(err)
		s.Require().Equal("DAYBREAK_BLUE_REQUIRED", infraerrors.Reason(write()))
		stored, err := s.repo.GetByID(s.ctx, account.ID)
		s.Require().NoError(err)
		s.Require().Equal(false, stored.Extra[service.OpenAIDaybreakRedEnabledKey])
	}
}

func (s *AccountRepoSuite) TestDaybreakRepositoryCreateCannotImportEnabledFlags() {
	account := &service.Account{Name: "new-daybreak-off", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive,
		Credentials: oauthOSTestGrant("created"), Extra: map[string]any{service.OpenAIDaybreakBlueEnabledKey: true, service.OpenAIDaybreakRedEnabledKey: true}}
	s.Require().NoError(s.repo.Create(s.ctx, account))
	stored, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().NotContains(stored.Extra, service.OpenAIDaybreakBlueEnabledKey)
	s.Require().NotContains(stored.Extra, service.OpenAIDaybreakRedEnabledKey)
}

func (s *AccountRepoSuite) TestDaybreakBulkLeavingOAuthClearsPreferences() {
	account := mustCreateAccount(s.T(), s.client, &service.Account{Name: "daybreak-auth-conversion", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
		Credentials: oauthOSTestGrant("convert"), Extra: map[string]any{service.OpenAIDaybreakBlueEnabledKey: true, service.OpenAIDaybreakRedEnabledKey: true}})
	_, err := s.repo.EnsureOpenAIOAuthOSProfiles(s.ctx, account.ID)
	s.Require().NoError(err)
	admin := service.NewAdminService(nil, nil, nil, s.repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, s.client, nil, nil, nil, nil, nil, nil, nil, nil)
	_, err = admin.BulkUpdateAccounts(s.ctx, &service.BulkUpdateAccountsInput{AccountIDs: []int64{account.ID}, OpenAIAuthModeChange: true,
		Credentials: map[string]any{"auth_mode": service.OpenAIAuthModePersonalAccessToken, "access_token": "test-pat"}})
	s.Require().NoError(err)
	stored, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().True(stored.IsOpenAIPersonalAccessToken())
	s.Require().NotContains(stored.Extra, service.OpenAIDaybreakBlueEnabledKey)
	s.Require().NotContains(stored.Extra, service.OpenAIDaybreakRedEnabledKey)
}
