//go:build integration

package repository

import (
	"maps"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func (s *AccountRepoSuite) TestCodexTurnStateConfigIntentAndStaleSnapshots() {
	account := mustCreateAccount(s.T(), s.client, &service.Account{Name: "codex-config", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "initial", "plan_type": "plus"}})
	admin := service.NewAdminService(nil, nil, nil, s.repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, s.client, nil, nil, nil, nil, nil, nil, nil, nil)
	stale, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	updated, err := admin.UpdateAccount(s.ctx, account.ID, &service.UpdateAccountInput{CodexTurnState: &service.CodexTurnStateConfig{Enabled: true, AccountType: "personal"}})
	s.Require().NoError(err)
	generation := service.CodexTurnStateGenerationForAccount(updated)
	s.Require().NotEmpty(generation)
	stale.Extra = map[string]any{service.CodexTurnStateExtraKey: map[string]any{"enabled": false}, service.CodexTurnStateGenerationExtraKey: "spoof"}
	s.Require().NoError(s.repo.Update(s.ctx, stale))
	s.Require().NoError(s.repo.UpdateExtra(s.ctx, account.ID, map[string]any{service.CodexTurnStateExtraKey: map[string]any{"enabled": false}, service.CodexTurnStateGenerationExtraKey: "spoof"}))
	_, err = s.repo.BulkUpdate(s.ctx, []int64{account.ID}, service.AccountBulkUpdate{Extra: map[string]any{service.CodexTurnStateExtraKey: map[string]any{"enabled": false}, service.CodexTurnStateGenerationExtraKey: "spoof"}})
	s.Require().NoError(err)
	stored, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().True(service.CodexTurnStateConfigForAccount(stored).Enabled)
	s.Require().Equal(generation, service.CodexTurnStateGenerationForAccount(stored))
	_, err = admin.BulkUpdateAccounts(s.ctx, &service.BulkUpdateAccountsInput{AccountIDs: []int64{account.ID}, CodexTurnState: &service.CodexTurnStateConfig{AccountType: "team_business"}})
	s.Require().NoError(err)
	stored, err = s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().False(service.CodexTurnStateConfigForAccount(stored).Enabled)
	s.Require().Equal("team_business", service.CodexTurnStateConfigForAccount(stored).AccountType)
	s.Require().NotEqual(generation, service.CodexTurnStateGenerationForAccount(stored))
}

func (s *AccountRepoSuite) TestCodexTurnStateGenerationTracksCredentialWrites() {
	account := mustCreateAccount(s.T(), s.client, &service.Account{Name: "codex-epoch", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "initial", "refresh_token": "refresh", "plan_type": "plus"},
		Extra:       map[string]any{service.CodexTurnStateExtraKey: map[string]any{"enabled": true, "account_type": "auto"}}})
	s.Require().NoError(s.repo.Update(s.ctx, account))
	generation := service.CodexTurnStateGenerationForAccount(account)
	usageCredentials := maps.Clone(account.Credentials)
	usageCredentials["usage"] = "metadata"
	s.Require().NoError(s.repo.UpdateCredentials(s.ctx, account.ID, usageCredentials))
	stored, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().Equal(generation, service.CodexTurnStateGenerationForAccount(stored))
	usageCredentials["access_token"] = "next"
	s.Require().NoError(s.repo.UpdateCredentials(s.ctx, account.ID, usageCredentials))
	stored, err = s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().NotEqual(generation, service.CodexTurnStateGenerationForAccount(stored))
	generation = service.CodexTurnStateGenerationForAccount(stored)
	_, err = s.repo.BulkUpdate(s.ctx, []int64{account.ID}, service.AccountBulkUpdate{Credentials: map[string]any{"refresh_token": "new-refresh"}})
	s.Require().NoError(err)
	stored, err = s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().NotEqual(generation, service.CodexTurnStateGenerationForAccount(stored))
	generation = service.CodexTurnStateGenerationForAccount(stored)
	applied, err := s.repo.PatchOpenAIOAuthCredentialsIfUnchanged(s.ctx, account.ID, map[string]any{"access_token": "next"}, nil, map[string]any{"access_token": "refreshed"}, nil)
	s.Require().NoError(err)
	s.Require().True(applied)
	stored, err = s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().NotEqual(generation, service.CodexTurnStateGenerationForAccount(stored))
}
