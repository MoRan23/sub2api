//go:build integration

package repository

import (
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func (s *AccountRepoSuite) TestExcelRouteConfigurationIsAtomicAndSnapshotProtected() {
	account := mustCreateAccount(s.T(), s.client, &service.Account{Name: "excel-route-config", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth})
	admin := service.NewAdminService(nil, nil, nil, s.repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, s.client, nil, nil, nil, nil, nil, nil, nil, nil)
	stale, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	patch := map[string]any{service.OpenAIExcelUpstreamEnabledExtraKey: true, service.OpenAIUpstreamRouteGenerationExtraKey: "forged"}
	s.Require().NoError(admin.UpdateAccountExtra(s.ctx, account.ID, patch))
	first, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().True(first.IsOpenAIExcelUpstreamEnabled())
	generation := first.OpenAIUpstreamRouteGeneration()
	s.Require().NotEmpty(generation)
	s.Require().NotEqual("forged", generation)
	s.Require().NoError(s.repo.Update(s.ctx, stale))
	s.Require().NoError(s.repo.UpdateExtra(s.ctx, account.ID, map[string]any{service.OpenAIExcelUpstreamEnabledExtraKey: false, "observation": true}))
	_, err = s.repo.BulkUpdate(s.ctx, []int64{account.ID}, service.AccountBulkUpdate{Extra: map[string]any{service.OpenAIExcelUpstreamEnabledExtraKey: false}})
	s.Require().NoError(err)
	current, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().True(current.IsOpenAIExcelUpstreamEnabled())
	s.Require().Equal(generation, current.OpenAIUpstreamRouteGeneration())
	_, err = admin.BulkUpdateAccounts(s.ctx, &service.BulkUpdateAccountsInput{AccountIDs: []int64{account.ID}, Extra: map[string]any{service.OpenAIExcelUpstreamEnabledExtraKey: false}})
	s.Require().NoError(err)
	disabled, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().False(disabled.IsOpenAIExcelUpstreamEnabled())
	s.Require().NotEqual(generation, disabled.OpenAIUpstreamRouteGeneration())
	s.Require().NoError(admin.UpdateAccountExtra(s.ctx, account.ID, map[string]any{service.OpenAIExcelUpstreamEnabledExtraKey: false}))
	stable, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().Equal(disabled.OpenAIUpstreamRouteGeneration(), stable.OpenAIUpstreamRouteGeneration())
}

func (s *AccountRepoSuite) TestExcelRouteBulkRejectsMixedCredentialTypesBeforeWriting() {
	regular := mustCreateAccount(s.T(), s.client, &service.Account{Name: "excel-route-regular", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth})
	pat := mustCreateAccount(s.T(), s.client, &service.Account{Name: "excel-route-pat", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Credentials: map[string]any{"auth_mode": "personalAccessToken"}})
	admin := service.NewAdminService(nil, nil, nil, s.repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, s.client, nil, nil, nil, nil, nil, nil, nil, nil)
	_, err := admin.BulkUpdateAccounts(s.ctx, &service.BulkUpdateAccountsInput{AccountIDs: []int64{regular.ID, pat.ID}, Extra: map[string]any{service.OpenAIExcelUpstreamEnabledExtraKey: true}})
	s.Require().Error(err)
	stored, err := s.repo.GetByID(s.ctx, regular.ID)
	s.Require().NoError(err)
	s.Require().False(stored.IsOpenAIExcelUpstreamEnabled())
}

func (s *AccountRepoSuite) TestExcelRouteCredentialModeTransitionAdvancesRouteFence() {
	account := mustCreateAccount(s.T(), s.client, &service.Account{Name: "excel-route-credential-transition", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth})
	admin := service.NewAdminService(nil, nil, nil, s.repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, s.client, nil, nil, nil, nil, nil, nil, nil, nil)
	s.Require().NoError(admin.UpdateAccountExtra(s.ctx, account.ID, map[string]any{service.OpenAIExcelUpstreamEnabledExtraKey: true}))
	enabled, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	first := enabled.OpenAIUpstreamRouteGeneration()
	s.Require().NotEmpty(first)

	modeCtx := service.WithOpenAIOAuthCredentialModeChangeIntent(s.ctx, account.ID)
	s.Require().NoError(s.repo.UpdateCredentials(modeCtx, account.ID, map[string]any{"auth_mode": "personalAccessToken", "access_token": "synthetic-pat"}))
	pat, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().False(pat.IsOpenAIExcelUpstreamEnabled())
	s.Require().NotEmpty(pat.OpenAIUpstreamRouteGeneration(), "leaving Excel must retain a private route fence")
	s.Require().NotEqual(first, pat.OpenAIUpstreamRouteGeneration())

	s.Require().NoError(s.repo.UpdateCredentials(modeCtx, account.ID, map[string]any{"auth_mode": "oauth", "access_token": "synthetic-oauth"}))
	restored, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().False(restored.IsOpenAIExcelUpstreamEnabled())
	s.Require().NotEmpty(restored.OpenAIUpstreamRouteGeneration(), "returning to Codex must retain a private route fence")
	s.Require().NotEqual(pat.OpenAIUpstreamRouteGeneration(), restored.OpenAIUpstreamRouteGeneration())
}
