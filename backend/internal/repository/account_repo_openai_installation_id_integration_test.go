//go:build integration

package repository

import (
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func (s *AccountRepoSuite) TestEnsureOpenAIInstallationIDSupportsSetupTokenOwnerOnly() {
	const (
		generated = "11111111-2222-4333-8444-555555555555"
		loser     = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	)
	setupToken := mustCreateAccount(s.T(), s.client, &service.Account{
		Name:     "installation-setup-token",
		Platform: service.PlatformOpenAI,
		Type:     service.AccountTypeSetupToken,
		Extra:    map[string]any{},
	})

	resolved, err := s.repo.EnsureOpenAIInstallationID(s.ctx, setupToken.ID, "", generated)
	s.Require().NoError(err)
	s.Require().Equal(generated, resolved)
	resolved, err = s.repo.EnsureOpenAIInstallationID(s.ctx, setupToken.ID, "", loser)
	s.Require().NoError(err)
	s.Require().Equal(generated, resolved, "concurrent loser must read the persisted winner")
	stored, err := s.repo.GetByID(s.ctx, setupToken.ID)
	s.Require().NoError(err)
	s.Require().Equal(generated, stored.Extra["openai_pinned_installation_id"])

	foreignOAuth := mustCreateAccount(s.T(), s.client, &service.Account{
		Name:     "installation-foreign-oauth",
		Platform: service.PlatformAnthropic,
		Type:     service.AccountTypeOAuth,
		Extra:    map[string]any{},
	})
	_, err = s.repo.EnsureOpenAIInstallationID(s.ctx, foreignOAuth.ID, "", loser)
	s.Require().ErrorIs(err, service.ErrAccountNotFound)

	parentID := setupToken.ID
	shadow := mustCreateAccount(s.T(), s.client, &service.Account{
		Name:            "installation-shadow",
		Platform:        service.PlatformOpenAI,
		Type:            service.AccountTypeSetupToken,
		ParentAccountID: &parentID,
		Extra:           map[string]any{},
	})
	_, err = s.repo.EnsureOpenAIInstallationID(s.ctx, shadow.ID, "", loser)
	s.Require().ErrorIs(err, service.ErrAccountNotFound)
}
