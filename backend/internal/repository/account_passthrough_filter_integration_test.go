//go:build integration

package repository

import "github.com/Wei-Shaw/sub2api/internal/service"

func (s *AccountRepoSuite) TestPassthroughFilterAdminIntentAndStaleWriters() {
	const key = "openai_passthrough_model_filter"
	account := mustCreateAccount(s.T(), s.client, &service.Account{Name: "passthrough-filter", Platform: service.PlatformOpenAI,
		Type: service.AccountTypeAPIKey, Extra: map[string]any{"openai_passthrough": true, key: false}})
	admin := service.NewAdminService(nil, nil, nil, s.repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, s.client, nil, nil, nil, nil, nil, nil, nil, nil)
	stale, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().NoError(admin.UpdateAccountExtra(s.ctx, account.ID, map[string]any{key: true}))
	stale.Name = "background snapshot"
	s.Require().NoError(s.repo.Update(s.ctx, stale))
	s.Require().NoError(s.repo.UpdateExtra(s.ctx, account.ID, map[string]any{key: false, "unrelated": true}))
	_, err = s.repo.BulkUpdate(s.ctx, []int64{account.ID}, service.AccountBulkUpdate{Extra: map[string]any{key: false}})
	s.Require().NoError(err)
	stored, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().True(stored.IsOpenAIPassthroughModelFilterEnabled())
	s.Require().Equal(true, stored.Extra["unrelated"])
	_, err = admin.BulkUpdateAccounts(s.ctx, &service.BulkUpdateAccountsInput{AccountIDs: []int64{account.ID}, Extra: map[string]any{key: false}})
	s.Require().NoError(err)
	stored, err = s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().False(stored.IsOpenAIPassthroughModelFilterEnabled())
}
