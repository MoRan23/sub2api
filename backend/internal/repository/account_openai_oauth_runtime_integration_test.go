//go:build integration

package repository

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func (s *AccountRepoSuite) newOpenAIOAuthRuntimeAccount(name string) (*service.Account, service.OpenAIOAuthAccountStateSnapshot) {
	account := mustCreateAccount(s.T(), s.client, &service.Account{
		Name: name, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
		Status: service.StatusActive, Schedulable: true, Credentials: openAIRefreshExpectedAuthForRepoTest(),
	})
	_, err := s.repo.EnsureOpenAIOAuthOSProfiles(s.ctx, account.ID)
	s.Require().NoError(err)
	snapshot := service.OpenAIOAuthAccountStateSnapshot{OwnerAccountID: account.ID}
	s.Require().NoError(scanSingleRow(s.ctx, s.repo.sql,
		`SELECT authorization_generation::text,revision FROM account_openai_oauth_credentials WHERE account_id=$1`,
		[]any{account.ID}, &snapshot.AuthorizationGeneration, &snapshot.CredentialRevision))
	return account, snapshot
}

func (s *AccountRepoSuite) TestOpenAIOAuthAccountState_StaleGrantHasNoSideEffects() {
	for _, stale := range []string{"generation", "revision"} {
		s.Run(stale, func() {
			account, snapshot := s.newOpenAIOAuthRuntimeAccount("runtime-stale-" + stale)
			_, err := s.repo.sql.ExecContext(s.ctx, `UPDATE accounts SET status='error',schedulable=false,
				error_message='current error',temp_unschedulable_until=NOW()+INTERVAL '1 hour',temp_unschedulable_reason='current penalty',
				extra='{"model_rate_limits":{"existing":{"reason":"current"}},"keep":"value"}'::jsonb WHERE id=$1`, account.ID)
			s.Require().NoError(err)
			if stale == "generation" {
				_, err = s.repo.sql.ExecContext(s.ctx, `UPDATE account_openai_oauth_credentials SET authorization_generation=gen_random_uuid() WHERE account_id=$1`, account.ID)
			} else {
				_, err = s.repo.sql.ExecContext(s.ctx, `UPDATE account_openai_oauth_credentials SET revision=revision+1 WHERE account_id=$1`, account.ID)
			}
			s.Require().NoError(err)
			_, err = s.repo.sql.ExecContext(s.ctx, `TRUNCATE scheduler_outbox`)
			s.Require().NoError(err)
			cache := &schedulerCacheRecorder{}
			s.repo.schedulerCache = cache
			var before string
			s.Require().NoError(scanSingleRow(s.ctx, s.repo.sql,
				`SELECT jsonb_build_object('account',to_jsonb(a),'grant',to_jsonb(c))::text FROM accounts a JOIN account_openai_oauth_credentials c ON c.account_id=a.id WHERE a.id=$1`,
				[]any{account.ID}, &before))
			for _, kind := range []service.OpenAIOAuthAccountStateChangeKind{
				service.OpenAIOAuthAccountStateError, service.OpenAIOAuthAccountStateCooldown,
				service.OpenAIOAuthAccountStateModelCooldown, service.OpenAIOAuthAccountStateRecover,
				service.OpenAIOAuthAccountStateClearTemp, service.OpenAIOAuthAccountStateClearError,
			} {
				result, err := s.repo.MutateOpenAIOAuthAccountStateIfUnchanged(s.ctx, account.ID, snapshot,
					service.OpenAIOAuthAccountStateChange{Kind: kind, ErrorMessage: "stale error", Until: time.Now().Add(2 * time.Hour), Reason: "stale penalty", ModelKey: "gpt-5"})
				s.Require().NoError(err)
				s.Require().False(result.Applied, string(kind))
				s.Require().False(result.ClearedError)
				s.Require().False(result.ClearedRateLimit)
			}
			var after string
			s.Require().NoError(scanSingleRow(s.ctx, s.repo.sql,
				`SELECT jsonb_build_object('account',to_jsonb(a),'grant',to_jsonb(c))::text FROM accounts a JOIN account_openai_oauth_credentials c ON c.account_id=a.id WHERE a.id=$1`,
				[]any{account.ID}, &after))
			s.Require().Equal(before, after)
			var outbox int
			s.Require().NoError(scanSingleRow(s.ctx, s.repo.sql, `SELECT COUNT(*) FROM scheduler_outbox`, nil, &outbox))
			s.Require().Zero(outbox)
			s.Require().Empty(cache.setAccounts)
		})
	}
}

func (s *AccountRepoSuite) TestOpenAIOAuthAccountState_RecoveryKeepsCredentialsAndPrivateGrant() {
	account, snapshot := s.newOpenAIOAuthRuntimeAccount("runtime-recovery")
	_, err := s.repo.sql.ExecContext(s.ctx, `UPDATE accounts SET status='error',schedulable=false,error_message='expired token',
		rate_limited_at=NOW(),rate_limit_reset_at=NOW()+INTERVAL '1 hour',overload_until=NOW()+INTERVAL '1 hour',
		temp_unschedulable_until=NOW()+INTERVAL '1 hour',temp_unschedulable_reason='penalty',
		extra='{"model_rate_limits":{"gpt-5":{"reason":"limit"}},"antigravity_quota_scopes":{"scope":"limit"},"keep":{"value":true}}'::jsonb WHERE id=$1`, account.ID)
	s.Require().NoError(err)
	_, err = s.repo.sql.ExecContext(s.ctx, `UPDATE account_openai_oauth_credentials SET status='reauth_required',last_error='private grant error',refresh_retry_after=NOW()+INTERVAL '1 hour' WHERE account_id=$1`, account.ID)
	s.Require().NoError(err)
	var credentialsBefore, grantBefore string
	s.Require().NoError(scanSingleRow(s.ctx, s.repo.sql,
		`SELECT a.credentials::text,to_jsonb(c)::text FROM accounts a JOIN account_openai_oauth_credentials c ON c.account_id=a.id WHERE a.id=$1`,
		[]any{account.ID}, &credentialsBefore, &grantBefore))
	_, err = s.repo.sql.ExecContext(s.ctx, `TRUNCATE scheduler_outbox`)
	s.Require().NoError(err)
	cache := &schedulerCacheRecorder{}
	s.repo.schedulerCache = cache

	result, err := s.repo.MutateOpenAIOAuthAccountStateIfUnchanged(s.ctx, account.ID, snapshot,
		service.OpenAIOAuthAccountStateChange{Kind: service.OpenAIOAuthAccountStateRecover})
	s.Require().NoError(err)
	s.Require().True(result.Applied)
	s.Require().True(result.ClearedError)
	s.Require().True(result.ClearedRateLimit)
	got, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().Equal(service.StatusActive, got.Status)
	s.Require().Empty(got.ErrorMessage)
	s.Require().False(got.Schedulable, "ClearError does not change schedulable")
	s.Require().Nil(got.RateLimitedAt)
	s.Require().Nil(got.RateLimitResetAt)
	s.Require().Nil(got.OverloadUntil)
	s.Require().Nil(got.TempUnschedulableUntil)
	s.Require().Empty(got.TempUnschedulableReason)
	s.Require().Equal(map[string]any{"value": true}, got.Extra["keep"])
	s.Require().NotContains(got.Extra, "model_rate_limits")
	s.Require().NotContains(got.Extra, "antigravity_quota_scopes")
	var credentialsAfter, grantAfter string
	s.Require().NoError(scanSingleRow(s.ctx, s.repo.sql,
		`SELECT a.credentials::text,to_jsonb(c)::text FROM accounts a JOIN account_openai_oauth_credentials c ON c.account_id=a.id WHERE a.id=$1`,
		[]any{account.ID}, &credentialsAfter, &grantAfter))
	s.Require().Equal(credentialsBefore, credentialsAfter)
	s.Require().Equal(grantBefore, grantAfter)
	var outbox int
	s.Require().NoError(scanSingleRow(s.ctx, s.repo.sql, `SELECT COUNT(*) FROM scheduler_outbox WHERE account_id=$1`, []any{account.ID}, &outbox))
	s.Require().Equal(1, outbox)
	s.Require().Empty(cache.setAccounts, "caller-owned transaction cannot publish an uncommitted snapshot")

	result, err = s.repo.MutateOpenAIOAuthAccountStateIfUnchanged(s.ctx, account.ID, snapshot,
		service.OpenAIOAuthAccountStateChange{Kind: service.OpenAIOAuthAccountStateRecover})
	s.Require().NoError(err)
	s.Require().False(result.Applied)
}

func (s *AccountRepoSuite) TestOpenAIOAuthAccountState_CooldownAndClearTempPreserveOtherState() {
	account, snapshot := s.newOpenAIOAuthRuntimeAccount("runtime-cooldown")
	longUntil := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	shortUntil := longUntil.Add(-30 * time.Minute)
	_, err := s.repo.sql.ExecContext(s.ctx, `UPDATE accounts SET rate_limited_at=NOW(),rate_limit_reset_at=$2,
		extra='{"model_rate_limits":{"existing":{"reason":"keep existing model"}},"antigravity_quota_scopes":{"scope":"keep quota"},"keep":true}'::jsonb WHERE id=$1`, account.ID, longUntil)
	s.Require().NoError(err)
	result, err := s.repo.MutateOpenAIOAuthAccountStateIfUnchanged(s.ctx, account.ID, snapshot,
		service.OpenAIOAuthAccountStateChange{Kind: service.OpenAIOAuthAccountStateCooldown, Until: longUntil, Reason: "long penalty"})
	s.Require().NoError(err)
	s.Require().True(result.Applied)
	result, err = s.repo.MutateOpenAIOAuthAccountStateIfUnchanged(s.ctx, account.ID, snapshot,
		service.OpenAIOAuthAccountStateChange{Kind: service.OpenAIOAuthAccountStateCooldown, Until: shortUntil, Reason: "short penalty"})
	s.Require().NoError(err)
	s.Require().False(result.Applied)
	got, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().True(got.TempUnschedulableUntil.Equal(longUntil))
	s.Require().Equal("long penalty", got.TempUnschedulableReason)
	s.Require().Equal(service.StatusActive, got.Status)
	s.Require().True(got.Schedulable)

	result, err = s.repo.MutateOpenAIOAuthAccountStateIfUnchanged(s.ctx, account.ID, snapshot,
		service.OpenAIOAuthAccountStateChange{Kind: service.OpenAIOAuthAccountStateModelCooldown, ModelKey: "gpt-5", Until: shortUntil, Reason: "  model penalty  "})
	s.Require().NoError(err)
	s.Require().True(result.Applied)
	got, err = s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	limits := got.Extra["model_rate_limits"].(map[string]any)
	s.Require().Equal(map[string]any{"reason": "keep existing model"}, limits["existing"])
	model := limits["gpt-5"].(map[string]any)
	s.Require().Equal("model penalty", model["reason"])
	s.Require().Equal(shortUntil.Format(time.RFC3339), model["rate_limit_reset_at"])
	s.Require().NotEmpty(model["rate_limited_at"])

	result, err = s.repo.MutateOpenAIOAuthAccountStateIfUnchanged(s.ctx, account.ID, snapshot,
		service.OpenAIOAuthAccountStateChange{Kind: service.OpenAIOAuthAccountStateClearTemp})
	s.Require().NoError(err)
	s.Require().True(result.Applied)
	got, err = s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().Nil(got.TempUnschedulableUntil)
	s.Require().Empty(got.TempUnschedulableReason)
	s.Require().NotContains(got.Extra, "model_rate_limits")
	s.Require().Contains(got.Extra, "antigravity_quota_scopes")
	s.Require().Equal(true, got.Extra["keep"])
	s.Require().NotNil(got.RateLimitedAt)
	s.Require().True(got.RateLimitResetAt.Equal(longUntil))

	result, err = s.repo.MutateOpenAIOAuthAccountStateIfUnchanged(s.ctx, account.ID, snapshot,
		service.OpenAIOAuthAccountStateChange{Kind: service.OpenAIOAuthAccountStateError, ErrorMessage: "old account error"})
	s.Require().NoError(err)
	s.Require().True(result.Applied)
	result, err = s.repo.MutateOpenAIOAuthAccountStateIfUnchanged(s.ctx, account.ID, snapshot,
		service.OpenAIOAuthAccountStateChange{Kind: service.OpenAIOAuthAccountStateClearError})
	s.Require().NoError(err)
	s.Require().True(result.Applied)
	s.Require().True(result.ClearedError)
	s.Require().False(result.ClearedRateLimit)
	got, err = s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().Equal(service.StatusActive, got.Status)
	s.Require().Empty(got.ErrorMessage)
	s.Require().False(got.Schedulable)
	s.Require().NotNil(got.RateLimitedAt)
	s.Require().True(got.RateLimitResetAt.Equal(longUntil))
	s.Require().Contains(got.Extra, "antigravity_quota_scopes")
}

func (s *AccountRepoSuite) TestOpenAIOAuthAccountState_ShadowUsesCurrentOwner() {
	owner, snapshot := s.newOpenAIOAuthRuntimeAccount("runtime-shadow-owner")
	other, otherSnapshot := s.newOpenAIOAuthRuntimeAccount("runtime-other-owner")
	shadow := mustCreateAccount(s.T(), s.client, &service.Account{
		Name: "runtime-spark-shadow", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
		Status: service.StatusActive, Schedulable: true,
	})
	_, err := s.repo.sql.ExecContext(s.ctx, `UPDATE accounts SET parent_account_id=$2,quota_dimension='spark' WHERE id=$1`, shadow.ID, owner.ID)
	s.Require().NoError(err)
	result, err := s.repo.MutateOpenAIOAuthAccountStateIfUnchanged(s.ctx, shadow.ID, snapshot,
		service.OpenAIOAuthAccountStateChange{Kind: service.OpenAIOAuthAccountStateError, ErrorMessage: "shadow error"})
	s.Require().NoError(err)
	s.Require().True(result.Applied)
	var status, message string
	var schedulable bool
	s.Require().NoError(scanSingleRow(s.ctx, s.repo.sql, `SELECT status,error_message,schedulable FROM accounts WHERE id=$1`, []any{shadow.ID}, &status, &message, &schedulable))
	s.Require().Equal(service.StatusError, status)
	s.Require().Equal("shadow error", message)
	s.Require().False(schedulable)
	s.Require().NoError(scanSingleRow(s.ctx, s.repo.sql, `SELECT status FROM accounts WHERE id=$1`, []any{owner.ID}, &status))
	s.Require().Equal(service.StatusActive, status)

	_, err = s.repo.sql.ExecContext(s.ctx, `UPDATE accounts SET parent_account_id=$2 WHERE id=$1`, shadow.ID, other.ID)
	s.Require().NoError(err)
	result, err = s.repo.MutateOpenAIOAuthAccountStateIfUnchanged(s.ctx, shadow.ID, snapshot,
		service.OpenAIOAuthAccountStateChange{Kind: service.OpenAIOAuthAccountStateRecover})
	s.Require().NoError(err)
	s.Require().False(result.Applied, "a former parent's in-flight test cannot recover the reparented shadow")
	result, err = s.repo.MutateOpenAIOAuthAccountStateIfUnchanged(s.ctx, shadow.ID, otherSnapshot,
		service.OpenAIOAuthAccountStateChange{Kind: service.OpenAIOAuthAccountStateRecover})
	s.Require().NoError(err)
	s.Require().True(result.Applied)
	s.Require().True(result.ClearedError)
	s.Require().False(result.ClearedRateLimit)
}

func TestOpenAIOAuthAccountState_PublishesCommittedCanonicalSnapshot(t *testing.T) {
	ctx := context.Background()
	repo := newAccountRepositoryWithSQL(integrationEntClient, integrationDB, nil)
	account := mustCreateAccount(t, integrationEntClient, &service.Account{
		Name: "runtime-committed-snapshot", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
		Status: service.StatusActive, Schedulable: true, Credentials: openAIRefreshExpectedAuthForRepoTest(),
	})
	_, err := repo.EnsureOpenAIOAuthOSProfiles(ctx, account.ID)
	require.NoError(t, err)
	snapshot := service.OpenAIOAuthAccountStateSnapshot{OwnerAccountID: account.ID}
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`SELECT authorization_generation::text,revision FROM account_openai_oauth_credentials WHERE account_id=$1`, account.ID).
		Scan(&snapshot.AuthorizationGeneration, &snapshot.CredentialRevision))
	cache := &schedulerCacheRecorder{}
	repo.schedulerCache = cache
	until := time.Now().UTC().Add(time.Minute).Truncate(time.Second)
	result, err := repo.MutateOpenAIOAuthAccountStateIfUnchanged(ctx, account.ID, snapshot,
		service.OpenAIOAuthAccountStateChange{Kind: service.OpenAIOAuthAccountStateCooldown, Until: until, Reason: "committed penalty"})
	require.NoError(t, err)
	require.True(t, result.Applied)
	require.Len(t, cache.setAccounts, 1)
	require.Equal(t, account.ID, cache.setAccounts[0].ID)
	require.Equal(t, "committed penalty", cache.setAccounts[0].TempUnschedulableReason)
	require.True(t, cache.setAccounts[0].TempUnschedulableUntil.Equal(until))
	var stored sql.NullTime
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT temp_unschedulable_until FROM accounts WHERE id=$1`, account.ID).Scan(&stored))
	require.True(t, stored.Valid)
	require.True(t, stored.Time.Equal(until))
}
