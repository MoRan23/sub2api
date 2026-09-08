//go:build integration

package repository

import (
	"encoding/json"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func (s *AccountRepoSuite) TestPatchOpenAIOAuthCredentialsIfUnchanged_PreservesConcurrentModelRestrictions() {
	credentials := openAIRefreshExpectedAuthForRepoTest()
	credentials["id_token"] = "old-id"
	credentials["model_mapping"] = map[string]any{"gpt-5": "gpt-5", "gpt-6-astra": "gpt-6-astra"}
	account := mustCreateAccount(s.T(), s.client, &service.Account{
		Name: "openai-refresh-model-restrictions", Platform: service.PlatformOpenAI,
		Type: service.AccountTypeOAuth, Status: service.StatusActive,
		Schedulable: true, Credentials: credentials,
	})
	expected := openAIRefreshExpectedAuthForRepoTest()
	expected["id_token"] = "old-id"
	// The administrator disables gpt-5 while the provider refresh is in flight.
	credentials["model_mapping"] = map[string]any{"gpt-6-astra": "gpt-6-astra"}
	credentials["base_url"] = "https://admin-configured.example"
	s.Require().NoError(s.repo.UpdateCredentials(s.ctx, account.ID, credentials))
	cache := &schedulerCacheRecorder{}
	s.repo.schedulerCache = cache
	_, err := s.repo.sql.ExecContext(s.ctx, "TRUNCATE scheduler_outbox")
	s.Require().NoError(err)

	applied, err := s.repo.PatchOpenAIOAuthCredentialsIfUnchanged(s.ctx, account.ID, expected, nil,
		map[string]any{"access_token": "new-access", "refresh_token": "new-refresh", "_token_version": 13}, []string{"id_token"})

	s.Require().NoError(err)
	s.Require().True(applied)
	got, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().Equal("new-access", got.Credentials["access_token"])
	s.Require().Equal("new-refresh", got.Credentials["refresh_token"])
	s.Require().NotContains(got.Credentials, "id_token")
	s.Require().Equal("https://admin-configured.example", got.Credentials["base_url"])
	s.Require().Equal(map[string]any{"gpt-6-astra": "gpt-6-astra"}, got.Credentials["model_mapping"])
	s.Require().Len(cache.setAccounts, 1)
	s.Require().Equal(got.Credentials, cache.setAccounts[0].Credentials)
	var outboxCount int
	s.Require().NoError(scanSingleRow(s.ctx, s.repo.sql,
		"SELECT COUNT(*) FROM scheduler_outbox WHERE account_id = $1", []any{account.ID}, &outboxCount))
	s.Require().Equal(1, outboxCount)

	applied, err = s.repo.PatchOpenAIOAuthCredentialsIfUnchanged(s.ctx, account.ID, expected, nil,
		map[string]any{"access_token": "duplicate-access", "_token_version": 14}, nil)
	s.Require().NoError(err)
	s.Require().False(applied, "a repeated refresh based on old auth must not overwrite the winner")
	s.Require().Len(cache.setAccounts, 1)
	s.Require().NoError(scanSingleRow(s.ctx, s.repo.sql,
		"SELECT COUNT(*) FROM scheduler_outbox WHERE account_id = $1", []any{account.ID}, &outboxCount))
	s.Require().Equal(1, outboxCount)
}

func (s *AccountRepoSuite) TestPatchOpenAIOAuthCredentialsIfUnchanged_RejectsChangedAuthAndShadow() {
	for key := range openAIRefreshExpectedAuthForRepoTest() {
		s.Run(key, func() {
			credentials := openAIRefreshExpectedAuthForRepoTest()
			account := mustCreateAccount(s.T(), s.client, &service.Account{
				Name: "openai-refresh-cas-" + key, Platform: service.PlatformOpenAI,
				Type: service.AccountTypeOAuth, Status: service.StatusActive,
				Schedulable: true, Credentials: credentials,
			})
			expected := openAIRefreshExpectedAuthForRepoTest()
			credentials[key] = "reauthorized"
			updatedJSON, err := json.Marshal(credentials)
			s.Require().NoError(err)
			_, err = s.repo.sql.ExecContext(s.ctx, "UPDATE accounts SET credentials = $1::jsonb WHERE id = $2", string(updatedJSON), account.ID)
			s.Require().NoError(err)
			_, err = s.repo.sql.ExecContext(s.ctx, "TRUNCATE scheduler_outbox")
			s.Require().NoError(err)

			applied, err := s.repo.PatchOpenAIOAuthCredentialsIfUnchanged(s.ctx, account.ID, expected, nil,
				map[string]any{"access_token": "stale-refresh-access", "_token_version": 13}, nil)

			s.Require().NoError(err)
			s.Require().False(applied)
			got, err := s.repo.GetByID(s.ctx, account.ID)
			s.Require().NoError(err)
			s.Require().Equal("reauthorized", got.Credentials[key])
			var outboxCount int
			s.Require().NoError(scanSingleRow(s.ctx, s.repo.sql,
				"SELECT COUNT(*) FROM scheduler_outbox WHERE account_id = $1", []any{account.ID}, &outboxCount))
			s.Require().Zero(outboxCount)
		})
	}

	account := mustCreateAccount(s.T(), s.client, &service.Account{
		Name: "openai-refresh-proxy-shadow-cas", Platform: service.PlatformOpenAI,
		Type: service.AccountTypeOAuth, Status: service.StatusActive,
		Schedulable: true, Credentials: openAIRefreshExpectedAuthForRepoTest(),
	})
	wrongProxyID := int64(99)
	applied, err := s.repo.PatchOpenAIOAuthCredentialsIfUnchanged(s.ctx, account.ID,
		openAIRefreshExpectedAuthForRepoTest(), &wrongProxyID, map[string]any{"access_token": "wrong-proxy"}, nil)
	s.Require().NoError(err)
	s.Require().False(applied)
	parent := mustCreateAccount(s.T(), s.client, &service.Account{
		Name: "openai-refresh-parent", Platform: service.PlatformOpenAI,
		Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true,
	})
	_, err = s.repo.sql.ExecContext(s.ctx, "UPDATE accounts SET parent_account_id = $1 WHERE id = $2", parent.ID, account.ID)
	s.Require().NoError(err)
	applied, err = s.repo.PatchOpenAIOAuthCredentialsIfUnchanged(s.ctx, account.ID,
		openAIRefreshExpectedAuthForRepoTest(), nil, map[string]any{"access_token": "shadow-refresh"}, nil)
	s.Require().NoError(err)
	s.Require().False(applied)
}
