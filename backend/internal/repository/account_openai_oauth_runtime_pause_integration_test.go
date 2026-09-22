//go:build integration

package repository

import "github.com/Wei-Shaw/sub2api/internal/service"

type openAIOAuthRuntimePauseTestState struct {
	status              string
	schedulable         bool
	errorMessage        string
	grantStatus         string
	owned               bool
	previousStatus      string
	previousSchedulable bool
	previousMessage     string
}

func (s *AccountRepoSuite) openAIOAuthRuntimePauseState(accountID int64) openAIOAuthRuntimePauseTestState {
	s.T().Helper()
	var state openAIOAuthRuntimePauseTestState
	s.Require().NoError(scanSingleRow(s.ctx, s.repo.sql, `SELECT a.status,a.schedulable,COALESCE(a.error_message,''),c.status,
		c.auth_pause_owned,COALESCE(c.auth_pause_previous_status,''),COALESCE(c.auth_pause_previous_schedulable,false),
		COALESCE(c.auth_pause_previous_error_message,'')
		FROM accounts a JOIN account_openai_oauth_credentials c ON c.account_id=a.id WHERE a.id=$1`,
		[]any{accountID}, &state.status, &state.schedulable, &state.errorMessage, &state.grantStatus,
		&state.owned, &state.previousStatus, &state.previousSchedulable, &state.previousMessage))
	return state
}

func (s *AccountRepoSuite) TestOpenAIOAuthAccountState_AuthFailureOwnsPauseUntilReauthorization() {
	account, snapshot := s.newOpenAIOAuthRuntimeAccount("runtime-auth-pause-recovery")
	before := s.openAIOAuthRuntimePauseState(account.ID)
	s.Require().Equal(service.OpenAIOAuthAuthorizationAuthorized, before.grantStatus)
	s.Require().False(before.owned)

	for _, message := range []string{"OAuth token has been revoked", "OAuth token invalidated by another login"} {
		result, err := s.repo.MutateOpenAIOAuthAccountStateIfUnchanged(s.ctx, account.ID, snapshot,
			service.OpenAIOAuthAccountStateChange{Kind: service.OpenAIOAuthAccountStateError, ErrorMessage: message, AuthFailure: true})
		s.Require().NoError(err)
		s.Require().True(result.Applied)
		failed := s.openAIOAuthRuntimePauseState(account.ID)
		s.Require().Equal(service.StatusError, failed.status)
		s.Require().False(failed.schedulable)
		s.Require().Equal(message, failed.errorMessage, "retain the original runtime failure message")
		s.Require().Equal(before.grantStatus, failed.grantStatus, "runtime failure must not change private authorization status")
		s.Require().True(failed.owned)
		s.Require().Equal(before.status, failed.previousStatus)
		s.Require().Equal(before.schedulable, failed.previousSchedulable)
		s.Require().Equal(before.errorMessage, failed.previousMessage, "repeated failures retain the state from before the first pause")
	}

	bound, err := s.repo.BindOpenAIOAuthOSCredentials(s.ctx, account.ID, service.OpenAIOSLinux, oauthOSTestGrant("runtime-auth-renewed"), "reauthorization")
	s.Require().NoError(err)
	s.Require().NotEqual(snapshot.AuthorizationGeneration, bound.AuthorizationGeneration)
	restored := s.openAIOAuthRuntimePauseState(account.ID)
	s.Require().Equal(service.StatusActive, restored.status)
	s.Require().True(restored.schedulable)
	s.Require().Empty(restored.errorMessage)
	s.Require().False(restored.owned)
}

func (s *AccountRepoSuite) TestOpenAIOAuthAccountState_AuthFailurePreservesPriorManualPause() {
	account, snapshot := s.newOpenAIOAuthRuntimeAccount("runtime-auth-prior-manual-pause")
	_, err := s.repo.sql.ExecContext(s.ctx, `UPDATE accounts SET schedulable=false,error_message='manual capacity pause' WHERE id=$1`, account.ID)
	s.Require().NoError(err)
	before := s.openAIOAuthRuntimePauseState(account.ID)
	result, err := s.repo.MutateOpenAIOAuthAccountStateIfUnchanged(s.ctx, account.ID, snapshot,
		service.OpenAIOAuthAccountStateChange{Kind: service.OpenAIOAuthAccountStateError, ErrorMessage: "OAuth token expired without a refresh token", AuthFailure: true})
	s.Require().NoError(err)
	s.Require().True(result.Applied)
	failed := s.openAIOAuthRuntimePauseState(account.ID)
	s.Require().True(failed.owned)
	s.Require().Equal(service.StatusActive, failed.previousStatus)
	s.Require().False(failed.previousSchedulable)
	s.Require().Equal("manual capacity pause", failed.previousMessage)
	s.Require().Equal(before.grantStatus, failed.grantStatus)

	_, err = s.repo.BindOpenAIOAuthOSCredentials(s.ctx, account.ID, service.OpenAIOSLinux, oauthOSTestGrant("runtime-manual-renewed"), "reauthorization")
	s.Require().NoError(err)
	restored := s.openAIOAuthRuntimePauseState(account.ID)
	s.Require().Equal(before.status, restored.status)
	s.Require().Equal(before.schedulable, restored.schedulable)
	s.Require().Equal(before.errorMessage, restored.errorMessage)
	s.Require().False(restored.owned)
}

func (s *AccountRepoSuite) TestOpenAIOAuthAccountState_GenericErrorIsNotRecoveredByReauthorization() {
	account, snapshot := s.newOpenAIOAuthRuntimeAccount("runtime-generic-error-reauthorization")
	before := s.openAIOAuthRuntimePauseState(account.ID)
	result, err := s.repo.MutateOpenAIOAuthAccountStateIfUnchanged(s.ctx, account.ID, snapshot,
		service.OpenAIOAuthAccountStateChange{Kind: service.OpenAIOAuthAccountStateError, ErrorMessage: "manual review required", AuthFailure: false})
	s.Require().NoError(err)
	s.Require().True(result.Applied)
	failed := s.openAIOAuthRuntimePauseState(account.ID)
	s.Require().False(failed.owned)
	s.Require().Equal(before.grantStatus, failed.grantStatus)
	s.Require().Equal(service.StatusError, failed.status)
	s.Require().False(failed.schedulable)
	s.Require().Equal("manual review required", failed.errorMessage)

	_, err = s.repo.BindOpenAIOAuthOSCredentials(s.ctx, account.ID, service.OpenAIOSLinux, oauthOSTestGrant("runtime-generic-renewed"), "reauthorization")
	s.Require().NoError(err)
	s.Require().Equal(failed, s.openAIOAuthRuntimePauseState(account.ID), "a new grant must not clear an unowned runtime error")
}

func (s *AccountRepoSuite) TestOpenAIOAuthAccountState_ManualChangeRelinquishesAuthPauseOwnership() {
	for _, manual := range []string{"same-schedulable-value", "set-error"} {
		s.Run(manual, func() {
			account, snapshot := s.newOpenAIOAuthRuntimeAccount("runtime-auth-manual-" + manual)
			const failureMessage = "OAuth access token revoked"
			result, err := s.repo.MutateOpenAIOAuthAccountStateIfUnchanged(s.ctx, account.ID, snapshot,
				service.OpenAIOAuthAccountStateChange{Kind: service.OpenAIOAuthAccountStateError, ErrorMessage: failureMessage, AuthFailure: true})
			s.Require().NoError(err)
			s.Require().True(result.Applied)
			s.Require().True(s.openAIOAuthRuntimePauseState(account.ID).owned)
			wantMessage := failureMessage
			if manual == "same-schedulable-value" {
				s.Require().NoError(s.repo.SetSchedulable(s.ctx, account.ID, false))
			} else {
				wantMessage = "manual review after authentication failure"
				s.Require().NoError(s.repo.SetError(s.ctx, account.ID, wantMessage))
			}
			changed := s.openAIOAuthRuntimePauseState(account.ID)
			s.Require().False(changed.owned, "an explicit manual write relinquishes ownership even when its value is unchanged")
			s.Require().Equal(wantMessage, changed.errorMessage)

			_, err = s.repo.BindOpenAIOAuthOSCredentials(s.ctx, account.ID, service.OpenAIOSLinux, oauthOSTestGrant("runtime-manual-change-renewed"), "reauthorization")
			s.Require().NoError(err)
			restored := s.openAIOAuthRuntimePauseState(account.ID)
			s.Require().Equal(service.StatusError, restored.status)
			s.Require().False(restored.schedulable)
			s.Require().Equal(wantMessage, restored.errorMessage)
			s.Require().False(restored.owned)
		})
	}
}

func (s *AccountRepoSuite) TestOpenAIOAuthAccountState_ShadowAuthFailureDoesNotOwnOwnerPause() {
	owner, snapshot := s.newOpenAIOAuthRuntimeAccount("runtime-auth-shadow-owner")
	before := s.openAIOAuthRuntimePauseState(owner.ID)
	shadow := mustCreateAccount(s.T(), s.client, &service.Account{
		Name: "runtime-auth-spark-shadow", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
		Status: service.StatusActive, Schedulable: true,
	})
	_, err := s.repo.sql.ExecContext(s.ctx, `UPDATE accounts SET parent_account_id=$2,quota_dimension='spark' WHERE id=$1`, shadow.ID, owner.ID)
	s.Require().NoError(err)
	const failureMessage = "Spark OAuth token revoked"
	result, err := s.repo.MutateOpenAIOAuthAccountStateIfUnchanged(s.ctx, shadow.ID, snapshot,
		service.OpenAIOAuthAccountStateChange{Kind: service.OpenAIOAuthAccountStateError, ErrorMessage: failureMessage, AuthFailure: true})
	s.Require().NoError(err)
	s.Require().True(result.Applied)
	s.Require().Equal(before, s.openAIOAuthRuntimePauseState(owner.ID), "a shadow failure must not claim the owner's auth pause")

	_, err = s.repo.BindOpenAIOAuthOSCredentials(s.ctx, owner.ID, service.OpenAIOSLinux, oauthOSTestGrant("runtime-shadow-owner-renewed"), "reauthorization")
	s.Require().NoError(err)
	s.Require().Equal(before, s.openAIOAuthRuntimePauseState(owner.ID))
	var status, message string
	var schedulable bool
	s.Require().NoError(scanSingleRow(s.ctx, s.repo.sql, `SELECT status,error_message,schedulable FROM accounts WHERE id=$1`,
		[]any{shadow.ID}, &status, &message, &schedulable))
	s.Require().Equal(service.StatusError, status)
	s.Require().Equal(failureMessage, message)
	s.Require().False(schedulable)
}
