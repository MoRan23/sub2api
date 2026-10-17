//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAttributionAPIKeyAutomaticOptIn(t *testing.T) {
	ctx := context.Background()
	r, ar, oauth := attributionFixture(t)
	expected := []int64{oauth.ID}
	for _, tc := range []struct {
		name, extra, change string
		eligible            bool
	}{
		{"missing", `{}`, "", false},
		{"off", `{"openai_api_key_auto_attribution_enabled":false}`, "", false},
		{"on", `{"openai_api_key_auto_attribution_enabled":true}`, "", true},
		{"generic on", `{"openai_api_key_mode":"generic","openai_api_key_auto_attribution_enabled":true}`, "", true},
		{"string true", `{"openai_api_key_auto_attribution_enabled":"true"}`, "", false},
		{"null", `{"openai_api_key_auto_attribution_enabled":null}`, "", false},
		{"unknown mode", `{"openai_api_key_mode":"future","openai_api_key_auto_attribution_enabled":true}`, "", false},
		{"null mode", `{"openai_api_key_mode":null,"openai_api_key_auto_attribution_enabled":true}`, "", false},
		{"engine off", `{"openai_api_key_mode":"codex_engine","openai_api_key_auto_attribution_enabled":false}`, "", true},
		{"inactive", `{"openai_api_key_auto_attribution_enabled":true}`, ",status='disabled'", false},
		{"unscheduled", `{"openai_api_key_auto_attribution_enabled":true}`, ",schedulable=false", false},
		{"expired", `{"openai_api_key_auto_attribution_enabled":true}`, ",expires_at=NOW()-interval '1 minute'", false},
		{"rate limited", `{"openai_api_key_auto_attribution_enabled":true}`, ",rate_limit_reset_at=NOW()+interval '1 hour'", false},
		{"deleted", `{"openai_api_key_auto_attribution_enabled":true}`, ",deleted_at=NOW()", false},
		{"other platform", `{"openai_api_key_auto_attribution_enabled":true}`, ",platform='anthropic'", false},
	} {
		a := attributionAPIKeyAccount(t, ar)
		_, err := integrationDB.ExecContext(ctx, `UPDATE accounts SET extra=$2`+tc.change+` WHERE id=$1`, a.ID, tc.extra)
		require.NoError(t, err, tc.name)
		if tc.eligible {
			expected = append(expected, a.ID)
		}
		var initial int
		require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM account_initial_tests WHERE account_id=$1`, a.ID).Scan(&initial))
		require.Zero(t, initial, "API-key opt-in must not create OAuth first-test events")
	}
	jobs, err := r.Enqueue(ctx, nil, false)
	require.NoError(t, err)
	var selected []int64
	for _, j := range jobs {
		selected = append(selected, j.AccountID)
		require.Equal(t, "queued", j.Status)
	}
	require.ElementsMatch(t, expected, selected)
	jobs, err = r.Enqueue(ctx, nil, false)
	require.NoError(t, err)
	require.Empty(t, jobs, "active jobs must not be duplicated")
}

func TestAttributionAPIKeyOptOutPreservesActiveTasks(t *testing.T) {
	for _, phase := range []string{"queued", "running", "retry_wait"} {
		t.Run(phase, func(t *testing.T) {
			ctx := context.Background()
			r, ar, oauth := attributionFixture(t)
			_, err := integrationDB.ExecContext(ctx, `UPDATE accounts SET schedulable=false WHERE id=$1`, oauth.ID)
			require.NoError(t, err)
			a := attributionAPIKeyAccount(t, ar)
			_, err = integrationDB.ExecContext(ctx, `UPDATE accounts SET extra='{"openai_api_key_auto_attribution_enabled":true}' WHERE id=$1`, a.ID)
			require.NoError(t, err)
			jobs, err := r.Enqueue(ctx, nil, false)
			require.NoError(t, err)
			require.Len(t, jobs, 1)
			j := jobs[0]
			if phase != "queued" {
				j, err = r.Claim(ctx)
				require.NoError(t, err)
				require.NotNil(t, j)
			}
			if phase == "retry_wait" {
				j.Checkpoint = attributionRetryCheckpoint()
				ok, err := r.DeferRetry(ctx, j, time.Now().Add(-time.Second))
				require.NoError(t, err)
				require.True(t, ok)
			}
			_, err = integrationDB.ExecContext(ctx, `UPDATE accounts SET extra='{"openai_api_key_auto_attribution_enabled":false}' WHERE id=$1`, a.ID)
			require.NoError(t, err)
			if phase != "running" {
				j, err = r.Claim(ctx)
				require.NoError(t, err)
				require.NotNil(t, j)
			}
			valid, err := r.Validate(ctx, j)
			require.NoError(t, err)
			require.True(t, valid, "opting out only affects subsequent enqueue")
			j.Status = "passed"
			attributionSuccessfulProbeEvidence(j, "")
			require.NoError(t, r.Finish(ctx, j))
			require.Equal(t, "awaiting_confirmation", j.Result.Action)
			_, err = integrationDB.ExecContext(ctx, `UPDATE model_attribution_state SET next_due_at=NOW()-interval '1 second' WHERE account_id=$1`, a.ID)
			require.NoError(t, err)
			jobs, err = r.Enqueue(ctx, nil, false)
			require.NoError(t, err)
			require.Empty(t, jobs)
			require.Equal(t, "high", attributionRun(t, r, a.ID, "passed").Result.Action, "manual result and unchanged baseline remain valid")
		})
	}
}

func (s *AccountRepoSuite) TestAPIKeyAutoAttributionAdminGuard() {
	account := mustCreateAccount(s.T(), s.client, &service.Account{Name: "api-key-auto-attribution", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "synthetic", "base_url": "https://synthetic.invalid/v1"}, Extra: map[string]any{service.OpenAIAPIKeyAutoAttributionEnabledExtraKey: false}})
	admin := service.NewAdminService(nil, nil, nil, s.repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, s.client, nil, nil, nil, nil, nil, nil, nil, nil)
	stale, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().NoError(admin.UpdateAccountExtra(s.ctx, account.ID, map[string]any{service.OpenAIAPIKeyAutoAttributionEnabledExtraKey: true}))
	s.Require().NoError(s.repo.Update(s.ctx, stale))
	s.Require().NoError(s.repo.UpdateExtra(s.ctx, account.ID, map[string]any{service.OpenAIAPIKeyAutoAttributionEnabledExtraKey: false}))
	stored, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().True(stored.IsOpenAIAPIKeyAutoAttributionEnabled())
	stored, err = admin.UpdateAccount(s.ctx, account.ID, &service.UpdateAccountInput{Extra: map[string]any{"unrelated": "retained"}})
	s.Require().NoError(err)
	s.Require().True(stored.IsOpenAIAPIKeyAutoAttributionEnabled())
	_, err = admin.BulkUpdateAccounts(s.ctx, &service.BulkUpdateAccountsInput{AccountIDs: []int64{account.ID}, Extra: map[string]any{service.OpenAIAPIKeyAutoAttributionEnabledExtraKey: false}})
	s.Require().NoError(err)
	stored, err = s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().False(stored.IsOpenAIAPIKeyAutoAttributionEnabled())
}
