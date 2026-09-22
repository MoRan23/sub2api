//go:build integration

package repository

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func (s *AccountRepoSuite) TestOAuthMetadataPermanentFailureAndOwnedRecovery() {
	for _, manual := range []string{"none", "same-pause", "status-edit", "bulk-pause", "typed-snapshot"} {
		s.Run(manual, func() {
			account := &service.Account{Name: "pause-" + manual, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, Credentials: oauthOSTestGrant("before")}
			s.Require().NoError(s.repo.Create(s.ctx, account))
			stale, err := s.repo.GetByID(s.ctx, account.ID)
			s.Require().NoError(err)
			slot, err := s.repo.GetOpenAIOAuthOSCredential(s.ctx, account.ID, service.OpenAIOSLinux)
			s.Require().NoError(err)
			applied, err := s.repo.SetOpenAIOAuthOSCredentialErrorIfUnchanged(s.ctx, account.ID, service.OpenAIOSLinux, slot.AuthorizationGeneration, slot.Revision, "secret-failure")
			s.Require().NoError(err)
			s.Require().True(applied)
			failed, err := s.repo.GetByID(s.ctx, account.ID)
			s.Require().NoError(err)
			s.Require().Equal(service.StatusError, failed.Status)
			s.Require().False(failed.Schedulable)
			s.Require().Equal(openAIOAuthPauseMessage, failed.ErrorMessage)
			stale.Name = "edited-old-snapshot"
			s.Require().NoError(s.repo.Update(s.ctx, stale))
			failed, err = s.repo.GetByID(s.ctx, account.ID)
			s.Require().NoError(err)
			s.Require().Equal(service.StatusError, failed.Status)
			s.Require().False(failed.Schedulable)
			switch manual {
			case "same-pause":
				s.Require().NoError(s.repo.SetSchedulable(s.ctx, account.ID, false))
			case "status-edit":
				s.Require().NoError(s.repo.SetError(s.ctx, account.ID, "manual-review"))
			case "bulk-pause":
				paused := false
				_, err = s.repo.BulkUpdate(s.ctx, []int64{account.ID}, service.AccountBulkUpdate{Schedulable: &paused})
				s.Require().NoError(err)
			case "typed-snapshot":
				s.Require().NoError(s.repo.Update(service.WithOpenAIOAuthAccountStateIntent(s.ctx, account.ID), failed))
			}
			bound, err := s.repo.BindOpenAIOAuthOSCredentials(s.ctx, account.ID, service.OpenAIOSMacOS, oauthOSTestGrant("renewed"), "reauthorization")
			s.Require().NoError(err)
			s.Require().NotEqual(slot.AuthorizationGeneration, bound.AuthorizationGeneration)
			fresh, err := s.repo.GetByID(s.ctx, account.ID)
			s.Require().NoError(err)
			s.Require().Equal("renewed", fresh.GetCredential("access_token"))
			if manual == "none" {
				s.Require().Equal(service.StatusActive, fresh.Status)
				s.Require().True(fresh.Schedulable)
				s.Require().Empty(fresh.ErrorMessage)
			} else {
				s.Require().Equal(service.StatusError, fresh.Status)
				s.Require().False(fresh.Schedulable)
			}
			applied, err = s.repo.SetOpenAIOAuthOSCredentialErrorIfUnchanged(s.ctx, account.ID, service.OpenAIOSWindows, slot.AuthorizationGeneration, slot.Revision, "late")
			s.Require().NoError(err)
			s.Require().False(applied)
		})
	}
}

func (s *AccountRepoSuite) TestOAuthMetadataRecoveryPreservesPriorManualPauseAndSharedCooldown() {
	account := &service.Account{Name: "prior-manual-pause", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: false, Credentials: oauthOSTestGrant("before")}
	s.Require().NoError(s.repo.Create(s.ctx, account))
	slot, err := s.repo.GetOpenAIOAuthOSCredential(s.ctx, account.ID, service.OpenAIOSWindows)
	s.Require().NoError(err)
	applied, err := s.repo.SetOpenAIOAuthOSCredentialErrorIfUnchanged(s.ctx, account.ID, service.OpenAIOSWindows, slot.AuthorizationGeneration, slot.Revision, "bad")
	s.Require().NoError(err)
	s.Require().True(applied)
	_, err = s.repo.BindOpenAIOAuthOSCredentials(s.ctx, account.ID, service.OpenAIOSWindows, oauthOSTestGrant("renewed"), "reauthorization")
	s.Require().NoError(err)
	fresh, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().Equal(service.StatusActive, fresh.Status)
	s.Require().False(fresh.Schedulable)
	slot, err = s.repo.GetOpenAIOAuthOSCredential(s.ctx, account.ID, service.OpenAIOSWindows)
	s.Require().NoError(err)
	until := time.Now().Add(10 * time.Minute).UTC().Truncate(time.Microsecond)
	applied, err = s.repo.SetOpenAIOAuthOSCredentialCooldownIfUnchanged(s.ctx, account.ID, service.OpenAIOSWindows, slot.AuthorizationGeneration, slot.Revision, until, "retry")
	s.Require().NoError(err)
	s.Require().True(applied)
	fresh, err = s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().Equal(until, *fresh.TempUnschedulableUntil)
	slot, err = s.repo.GetOpenAIOAuthOSCredential(s.ctx, account.ID, service.OpenAIOSWindows)
	s.Require().NoError(err)
	applied, err = s.repo.PatchOpenAIOAuthOSCredentialsIfUnchanged(s.ctx, account.ID, service.OpenAIOSLinux, slot.AuthorizationGeneration, slot.Revision, nil, map[string]any{"access_token": "retry-success"}, nil)
	s.Require().NoError(err)
	s.Require().True(applied)
	fresh, err = s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().Nil(fresh.TempUnschedulableUntil, "successful refresh clears only its own retry pause")
	slot, err = s.repo.GetOpenAIOAuthOSCredential(s.ctx, account.ID, service.OpenAIOSWindows)
	s.Require().NoError(err)
	applied, err = s.repo.SetOpenAIOAuthOSCredentialCooldownIfUnchanged(s.ctx, account.ID, service.OpenAIOSWindows, slot.AuthorizationGeneration, slot.Revision, until, "retry")
	s.Require().NoError(err)
	s.Require().True(applied)
	slot, err = s.repo.GetOpenAIOAuthOSCredential(s.ctx, account.ID, service.OpenAIOSWindows)
	s.Require().NoError(err)
	otherUntil := until.Add(time.Hour)
	s.Require().NoError(s.repo.SetTempUnschedulable(s.ctx, account.ID, otherUntil, "transport-backoff"))
	applied, err = s.repo.PatchOpenAIOAuthOSCredentialsIfUnchanged(s.ctx, account.ID, service.OpenAIOSLinux, slot.AuthorizationGeneration, slot.Revision, nil, map[string]any{"access_token": "rotated"}, nil)
	s.Require().NoError(err)
	s.Require().True(applied)
	fresh, err = s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().Equal(otherUntil, *fresh.TempUnschedulableUntil)
	s.Require().Equal("transport-backoff", fresh.TempUnschedulableReason)
}

func (s *AccountRepoSuite) TestOAuthMetadataRevokeAndCASAreAtomic() {
	account := &service.Account{Name: "revoke-shared", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, Credentials: map[string]any{"refresh_token": "refresh-only"}}
	s.Require().NoError(s.repo.Create(s.ctx, account))
	old, err := s.repo.GetOpenAIOAuthOSCredential(s.ctx, account.ID, service.OpenAIOSWindows)
	s.Require().NoError(err)
	s.Require().NoError(s.repo.RevokeOpenAIOAuthOSCredentials(s.ctx, account.ID, service.OpenAIOSLinux))
	revoked, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().Equal(service.StatusError, revoked.Status)
	s.Require().False(revoked.Schedulable)
	s.Require().Empty(revoked.GetCredential("refresh_token"))
	bound, err := s.repo.BindOpenAIOAuthOSCredentials(s.ctx, account.ID, service.OpenAIOSMacOS, map[string]any{"access_token": "access-only", "chatgpt_user_id": "new-user"}, "reauthorization")
	s.Require().NoError(err)
	s.Require().NotEqual(old.AuthorizationGeneration, bound.AuthorizationGeneration)
	countOutbox := func() int {
		rows, queryErr := s.client.QueryContext(s.ctx, `SELECT COUNT(*) FROM scheduler_outbox WHERE account_id=$1`, account.ID)
		s.Require().NoError(queryErr)
		defer rows.Close()
		s.Require().True(rows.Next())
		var count int
		s.Require().NoError(rows.Scan(&count))
		return count
	}
	before := countOutbox()
	applied, err := s.repo.SetOpenAIOAuthOSCredentialErrorIfUnchanged(s.ctx, account.ID, service.OpenAIOSWindows, old.AuthorizationGeneration, old.Revision, "late-error")
	s.Require().NoError(err)
	s.Require().False(applied)
	s.Require().Equal(before, countOutbox(), "CAS loser must not publish a pause")
	fresh, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().Equal(service.StatusActive, fresh.Status)
	s.Require().True(fresh.Schedulable)
	s.Require().Equal("access-only", fresh.GetCredential("access_token"))
	current, err := s.repo.GetOpenAIOAuthOSCredential(s.ctx, account.ID, service.OpenAIOSLinux)
	s.Require().NoError(err)
	s.Require().Equal(bound.AuthorizationGeneration, current.AuthorizationGeneration)
	s.Require().Equal(bound.Revision, current.Revision)
}

func TestOpenAIOAuthMetadataMigrationUsesAccountAsOnlyCredentialSource(t *testing.T) {
	ctx := context.Background()
	tx := testTx(t)
	schema := "oauth_meta_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err := tx.ExecContext(ctx, `CREATE SCHEMA `+schema+`; SET LOCAL search_path TO `+schema+`,pg_catalog;
	CREATE TABLE accounts(id bigint PRIMARY KEY,platform text,type text,parent_account_id bigint,deleted_at timestamptz,credentials jsonb,extra jsonb,proxy_id bigint,status text,schedulable boolean,error_message text,updated_at timestamptz,temp_unschedulable_until timestamptz,temp_unschedulable_reason text);
	CREATE TABLE account_openai_oauth_os_profiles(account_id bigint,os_family text,is_default boolean);
	INSERT INTO accounts(id,platform,type,credentials,extra,status,schedulable,error_message) SELECT id,'openai','oauth','{"access_token":"old-mirror","model_mapping":{"gpt":"preserved"}}','{}',CASE WHEN id IN (4,5) THEN 'disabled' ELSE 'active' END,id NOT IN (2,4,5,6),CASE WHEN id IN (4,5) THEN 'manual-disabled' ELSE '' END FROM generate_series(1,6) id;
	INSERT INTO account_openai_oauth_os_profiles SELECT id,'windows',true FROM generate_series(1,6) id;`)
	require.NoError(t, err)
	for _, name := range []string{"251_openai_oauth_os_credentials.sql", "255_openai_oauth_shared_credentials.sql"} {
		raw, err := os.ReadFile("../../migrations/" + name)
		require.NoError(t, err)
		_, err = tx.ExecContext(ctx, string(raw))
		require.NoError(t, err)
	}
	_, err = tx.ExecContext(ctx, `UPDATE account_openai_oauth_credentials SET credentials='{"access_token":"authoritative","refresh_token":"whole-refresh"}' WHERE account_id=1;
	UPDATE account_openai_oauth_credentials SET status='reauth_required',credentials='{"access_token":"failed-complete","refresh_token":"failed-refresh"}' WHERE account_id=2;
	UPDATE account_openai_oauth_credentials SET status='unauthorized',credentials='{}' WHERE account_id IN (3,4,6);
	UPDATE account_openai_oauth_credentials SET status='reauth_required' WHERE account_id=5;`)
	require.NoError(t, err)
	raw, err := os.ReadFile("../../migrations/256_openai_oauth_account_credential_source.sql")
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, string(raw))
	require.NoError(t, err)
	var token, refresh, model, status string
	var schedulable, previousSchedulable, owned bool
	var count int
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT credentials->>'access_token',credentials->>'refresh_token',credentials#>>'{model_mapping,gpt}' FROM accounts WHERE id=1`).Scan(&token, &refresh, &model))
	require.Equal(t, "authoritative", token)
	require.Equal(t, "whole-refresh", refresh)
	require.Equal(t, "preserved", model)
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT a.status,a.schedulable,c.auth_pause_owned,c.auth_pause_previous_schedulable FROM accounts a JOIN account_openai_oauth_credentials c ON c.account_id=a.id WHERE a.id=2`).Scan(&status, &schedulable, &owned, &previousSchedulable))
	require.Equal(t, "error", status)
	require.True(t, owned)
	require.False(t, schedulable)
	require.False(t, previousSchedulable)
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT count(*) FROM accounts WHERE id=3 AND NOT credentials ? 'access_token' AND NOT credentials ? 'refresh_token'`).Scan(&count))
	require.Equal(t, 1, count)
	for _, id := range []int{3, 6} {
		var authStatus, message string
		require.NoError(t, tx.QueryRowContext(ctx, `SELECT a.status,a.schedulable,a.error_message,c.status,c.auth_pause_owned,c.auth_pause_previous_schedulable FROM accounts a JOIN account_openai_oauth_credentials c ON c.account_id=a.id WHERE a.id=$1`, id).Scan(&status, &schedulable, &message, &authStatus, &owned, &previousSchedulable))
		require.Equal(t, "error", status)
		require.False(t, schedulable)
		require.Equal(t, openAIOAuthPauseMessage, message)
		require.Equal(t, "unauthorized", authStatus, "missing grants stay unauthorized")
		require.True(t, owned)
		require.Equal(t, id == 3, previousSchedulable, "retain an earlier manual scheduling pause")
	}
	for _, id := range []int{4, 5} {
		var message string
		require.NoError(t, tx.QueryRowContext(ctx, `SELECT a.status,a.schedulable,a.error_message,c.auth_pause_owned FROM accounts a JOIN account_openai_oauth_credentials c ON c.account_id=a.id WHERE a.id=$1`, id).Scan(&status, &schedulable, &message, &owned))
		require.Equal(t, "disabled", status)
		require.False(t, schedulable)
		require.Equal(t, "manual-disabled", message)
		require.False(t, owned, "migration must not own a manual disable")
	}
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT count(*) FROM information_schema.columns WHERE table_schema=current_schema() AND table_name IN ('account_openai_oauth_credentials','account_openai_oauth_os_credentials') AND column_name='credentials'`).Scan(&count))
	require.Zero(t, count)
	_, err = tx.ExecContext(ctx, `UPDATE accounts SET status='disabled',credentials=credentials||'{"access_token":"new-current"}'::jsonb WHERE id=2`)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, string(raw))
	require.NoError(t, err)
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT status,credentials->>'access_token' FROM accounts WHERE id=2`).Scan(&status, &token))
	require.Equal(t, "disabled", status)
	require.Equal(t, "new-current", token)
	var generation, authGeneration string
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT state_generation::text,authorization_generation::text FROM account_openai_oauth_credentials WHERE account_id=1`).Scan(&generation, &authGeneration))
	for _, enabled := range []bool{true, false, true} {
		_, err = tx.ExecContext(ctx, `UPDATE accounts SET extra=jsonb_build_object('codex_turn_state',jsonb_build_object('enabled',$1::boolean)) WHERE id=1`, enabled)
		require.NoError(t, err)
		var next, nextAuth, previous string
		require.NoError(t, tx.QueryRowContext(ctx, `SELECT state_generation::text,authorization_generation::text,previous_state_generation::text FROM account_openai_oauth_credentials WHERE account_id=1`).Scan(&next, &nextAuth, &previous))
		require.NotEqual(t, generation, next)
		require.Equal(t, generation, previous)
		require.Equal(t, authGeneration, nextAuth, "runtime toggle does not reauthorize the account")
		generation = next
	}
}
