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
	epoch := service.CodexTurnStateCredentialEpochForAccount(updated)
	s.Require().NotEmpty(epoch)
	stale.Extra = map[string]any{service.CodexTurnStateExtraKey: map[string]any{"enabled": false}, service.CodexTurnStateGenerationExtraKey: "spoof", service.CodexTurnStateCredentialEpochExtraKey: "spoof"}
	s.Require().NoError(s.repo.Update(s.ctx, stale))
	s.Require().NoError(s.repo.UpdateExtra(s.ctx, account.ID, map[string]any{service.CodexTurnStateExtraKey: map[string]any{"enabled": false}, service.CodexTurnStateGenerationExtraKey: "spoof", service.CodexTurnStateCredentialEpochExtraKey: "spoof"}))
	_, err = s.repo.BulkUpdate(s.ctx, []int64{account.ID}, service.AccountBulkUpdate{Extra: map[string]any{service.CodexTurnStateExtraKey: map[string]any{"enabled": false}, service.CodexTurnStateGenerationExtraKey: "spoof", service.CodexTurnStateCredentialEpochExtraKey: "spoof"}})
	s.Require().NoError(err)
	stored, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().True(service.CodexTurnStateConfigForAccount(stored).Enabled)
	s.Require().Equal(generation, service.CodexTurnStateGenerationForAccount(stored))
	s.Require().Equal(epoch, service.CodexTurnStateCredentialEpochForAccount(stored))
	_, err = admin.BulkUpdateAccounts(s.ctx, &service.BulkUpdateAccountsInput{AccountIDs: []int64{account.ID}, CodexTurnState: &service.CodexTurnStateConfig{AccountType: "team_business"}})
	s.Require().NoError(err)
	stored, err = s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().False(service.CodexTurnStateConfigForAccount(stored).Enabled)
	s.Require().Equal("team_business", service.CodexTurnStateConfigForAccount(stored).AccountType)
	s.Require().NotEqual(generation, service.CodexTurnStateGenerationForAccount(stored))
	s.Require().Equal(epoch, service.CodexTurnStateCredentialEpochForAccount(stored))
}

func (s *AccountRepoSuite) TestCodexTurnStateGenerationTracksCredentialWrites() {
	account := mustCreateAccount(s.T(), s.client, &service.Account{Name: "codex-epoch", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "initial", "refresh_token": "refresh", "plan_type": "plus"},
		Extra:       map[string]any{service.CodexTurnStateExtraKey: map[string]any{"enabled": true, "account_type": "auto"}}})
	s.Require().NoError(s.repo.Update(s.ctx, account))
	slot, err := s.repo.GetOpenAIOAuthOSCredential(s.ctx, account.ID, service.OpenAIOSWindows)
	s.Require().NoError(err)
	generation := slot.StateGeneration
	usageCredentials := maps.Clone(account.Credentials)
	usageCredentials["usage"] = "metadata"
	s.Require().NoError(s.repo.UpdateCredentials(s.ctx, account.ID, usageCredentials))
	stored, err := service.ReloadOpenAIOAuthCredentialAccount(s.ctx, s.repo, account)
	s.Require().NoError(err)
	s.Require().Equal(generation, service.CodexTurnStateGenerationForAccount(stored))
	usageCredentials["access_token"] = "next"
	s.Require().NoError(s.repo.UpdateCredentials(s.ctx, account.ID, usageCredentials))
	stored, err = service.ReloadOpenAIOAuthCredentialAccount(s.ctx, s.repo, account)
	s.Require().NoError(err)
	s.Require().Equal(generation, service.CodexTurnStateGenerationForAccount(stored), "ordinary credential writes preserve provider-owned auth")
	s.Require().Equal("initial", stored.GetCredential("access_token"))
	generation = service.CodexTurnStateGenerationForAccount(stored)
	_, err = s.repo.BulkUpdate(s.ctx, []int64{account.ID}, service.AccountBulkUpdate{Credentials: map[string]any{"refresh_token": "new-refresh"}})
	s.Require().NoError(err)
	stored, err = service.ReloadOpenAIOAuthCredentialAccount(s.ctx, s.repo, account)
	s.Require().NoError(err)
	s.Require().Equal(generation, service.CodexTurnStateGenerationForAccount(stored), "bulk snapshots cannot replace private refresh tokens")
	generation = service.CodexTurnStateGenerationForAccount(stored)
	applied, err := s.repo.PatchOpenAIOAuthCredentialsIfUnchanged(s.ctx, account.ID, map[string]any{"access_token": "initial"}, nil, map[string]any{"access_token": "refreshed"}, nil)
	s.Require().NoError(err)
	s.Require().True(applied)
	stored, err = service.ReloadOpenAIOAuthCredentialAccount(s.ctx, s.repo, account)
	s.Require().NoError(err)
	s.Require().Equal(generation, service.CodexTurnStateGenerationForAccount(stored), "normal OAuth refresh preserves the shared runtime fence")
}

func (s *AccountRepoSuite) TestCodexTurnStateCredentialEpochBeforeConfiguration() {
	account := mustCreateAccount(s.T(), s.client, &service.Account{Name: "codex-passive-epoch", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "initial", "refresh_token": "refresh", "plan_type": "plus"}})
	// Existing/legacy accounts initialize an absent epoch on the next locked write.
	s.Require().NoError(s.repo.UpdateCredentials(s.ctx, account.ID, account.Credentials))
	_, err := s.repo.EnsureOpenAIOAuthOSProfiles(s.ctx, account.ID)
	s.Require().NoError(err)
	stored, err := service.ReloadOpenAIOAuthCredentialAccount(s.ctx, s.repo, account)
	s.Require().NoError(err)
	epoch := service.CodexTurnStateCredentialEpochForAccount(stored)
	s.Require().NotEmpty(epoch)
	s.Require().NotEmpty(service.CodexTurnStateGenerationForAccount(stored), "the private slot has a state fence before collector configuration")
	s.Require().NotContains(stored.Extra, service.CodexTurnStateExtraKey)

	metadata := maps.Clone(stored.Credentials)
	metadata["usage"] = "new metadata"
	metadata["plan_type"] = "team"
	s.Require().NoError(s.repo.UpdateCredentials(s.ctx, account.ID, metadata))
	stored, err = service.ReloadOpenAIOAuthCredentialAccount(s.ctx, s.repo, account)
	s.Require().NoError(err)
	s.Require().Equal(epoch, service.CodexTurnStateCredentialEpochForAccount(stored))
	admin := service.NewAdminService(nil, nil, nil, s.repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, s.client, nil, nil, nil, nil, nil, nil, nil, nil)
	_, err = admin.UpdateAccount(s.ctx, account.ID, &service.UpdateAccountInput{CodexTurnState: &service.CodexTurnStateConfig{Enabled: true, AccountType: "personal"}})
	s.Require().NoError(err)
	stored, err = service.ReloadOpenAIOAuthCredentialAccount(s.ctx, s.repo, account)
	s.Require().NoError(err)
	s.Require().Equal(epoch, service.CodexTurnStateCredentialEpochForAccount(stored))
	s.Require().NotEmpty(service.CodexTurnStateGenerationForAccount(stored))
	_, err = admin.BulkUpdateAccounts(s.ctx, &service.BulkUpdateAccountsInput{AccountIDs: []int64{account.ID}, CodexTurnState: &service.CodexTurnStateConfig{AccountType: "team_business"}})
	s.Require().NoError(err)
	stored, err = service.ReloadOpenAIOAuthCredentialAccount(s.ctx, s.repo, account)
	s.Require().NoError(err)
	s.Require().Equal(epoch, service.CodexTurnStateCredentialEpochForAccount(stored))

	_, err = s.repo.BulkUpdate(s.ctx, []int64{account.ID}, service.AccountBulkUpdate{Credentials: map[string]any{"access_token": "bulk-new"}})
	s.Require().NoError(err)
	stored, err = service.ReloadOpenAIOAuthCredentialAccount(s.ctx, s.repo, account)
	s.Require().NoError(err)
	s.Require().Equal(epoch, service.CodexTurnStateCredentialEpochForAccount(stored), "bulk snapshots preserve the private grant")
	epoch = service.CodexTurnStateCredentialEpochForAccount(stored)
	applied, err := s.repo.PatchOpenAIOAuthCredentialsIfUnchanged(s.ctx, account.ID, map[string]any{"access_token": "initial"}, nil, map[string]any{"access_token": "refreshed"}, nil)
	s.Require().NoError(err)
	s.Require().True(applied)
	stored, err = service.ReloadOpenAIOAuthCredentialAccount(s.ctx, s.repo, account)
	s.Require().NoError(err)
	s.Require().NotEqual(epoch, service.CodexTurnStateCredentialEpochForAccount(stored))

	credentials := maps.Clone(stored.Credentials)
	credentials["auth_mode"] = " AgentIdentity "
	modeCtx := service.WithOpenAIOAuthCredentialModeChangeIntent(s.ctx, account.ID)
	s.Require().NoError(s.repo.UpdateCredentials(modeCtx, account.ID, credentials))
	stored, err = s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().NotContains(stored.Extra, service.CodexTurnStateCredentialEpochExtraKey)
	delete(credentials, "auth_mode")
	s.Require().NoError(s.repo.UpdateCredentials(modeCtx, account.ID, credentials))
	stored, err = s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().NotEmpty(service.CodexTurnStateCredentialEpochForAccount(stored))
	s.Require().NotEqual(epoch, service.CodexTurnStateCredentialEpochForAccount(stored))
	slot, err := s.repo.GetOpenAIOAuthOSCredential(s.ctx, account.ID, service.OpenAIOSWindows)
	s.Require().NoError(err)
	s.Require().Equal(service.OpenAIOAuthAuthorizationUnauthorized, slot.Status, "converting back does not resurrect a previous OAuth grant")
}
