//go:build integration

package repository

import (
	"context"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
)

func (s *AccountRepoSuite) TestConfigurationGuardPreservesRegeneratedIdentityAcrossStaleWriters() {
	client := testEntClient(s.T())
	repo := newAccountRepositoryWithSQL(client, integrationDB, nil)
	account := mustCreateAccount(s.T(), client, &service.Account{Name: "configuration-guard", Platform: service.PlatformOpenAI,
		Type: service.AccountTypeOAuth, Credentials: map[string]any{"user_agent": legacyProfileUA},
		Extra: map[string]any{"openai_pinned_installation_id": uuid.NewString(), "enable_tls_fingerprint": true, "tls_fingerprint_profile_id": 12}})
	s.T().Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM scheduler_outbox WHERE account_id = $1", account.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM accounts WHERE id = $1", account.ID)
	})
	stale, err := repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	latest := uuid.NewString()
	_, err = repo.RegenerateOpenAIInstallationID(s.ctx, account.ID, latest)
	s.Require().NoError(err)
	stale.Name = "unrelated edit"
	stale.Credentials["user_agent"] = "old or spoofed UA"
	stale.Extra["enable_tls_fingerprint"] = false
	s.Require().NoError(repo.Update(s.ctx, stale))
	s.Require().NoError(repo.UpdateCredentials(s.ctx, account.ID, map[string]any{"access_token": "new token", "user_agent": "stale async UA"}))
	s.Require().NoError(repo.UpdateExtra(s.ctx, account.ID, map[string]any{"openai_pinned_installation_id": uuid.NewString(), "enable_tls_fingerprint": false, "custom": "accepted"}))
	_, err = repo.BulkUpdate(s.ctx, []int64{account.ID}, service.AccountBulkUpdate{
		Credentials: map[string]any{"user_agent": "stale bulk UA"}, Extra: map[string]any{"tls_fingerprint_profile_id": 99}})
	s.Require().NoError(err)
	stored, err := repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().Equal(latest, stored.GetPinnedOpenAIInstallationID())
	s.Require().Equal(legacyProfileUA, stored.GetOpenAIUserAgent())
	s.Require().Equal(true, stored.Extra["enable_tls_fingerprint"])
	s.Require().Equal(float64(12), stored.Extra["tls_fingerprint_profile_id"])
	s.Require().NotContains(stored.Credentials, "access_token", "ordinary snapshots cannot establish or overwrite an OAuth grant")
	s.Require().Equal("accepted", stored.Extra["custom"])
}

func (s *AccountRepoSuite) TestRegenerateRechecksPinAfterConcurrentCommit() {
	client := testEntClient(s.T())
	repo := newAccountRepositoryWithSQL(client, integrationDB, nil)
	account := mustCreateAccount(s.T(), client, &service.Account{Name: "regenerate-pin-race", Platform: service.PlatformOpenAI,
		Type: service.AccountTypeOAuth, Extra: map[string]any{"openai_pinned_installation_id": uuid.NewString()}})
	s.T().Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM scheduler_outbox WHERE account_id = $1", account.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM accounts WHERE id = $1", account.ID)
	})
	tx, err := client.Tx(s.ctx)
	s.Require().NoError(err)
	defer func() { _ = tx.Rollback() }()
	_, err = tx.Client().ExecContext(s.ctx, `UPDATE accounts SET extra = extra || '{"openai_installation_pin_enabled":false}'::jsonb WHERE id=$1`, account.ID)
	s.Require().NoError(err)
	ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := repo.RegenerateOpenAIInstallationID(ctx, account.ID, uuid.NewString())
		done <- err
	}()
	s.Require().NoError(tx.Commit())
	s.Require().Equal("OPENAI_INSTALLATION_REGENERATE_PIN_DISABLED", infraerrors.Reason(<-done),
		"regeneration must see the committed pin disable, even if its admin read was earlier")
	stored, err := repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().Equal(account.Extra["openai_pinned_installation_id"], stored.GetPinnedOpenAIInstallationID())
}

func (s *AccountRepoSuite) TestAdminConfigurationExplicitEditsAndStaleForm() {
	account := mustCreateAccount(s.T(), s.client, &service.Account{Name: "admin-config-intent", Platform: service.PlatformOpenAI,
		Type: service.AccountTypeOAuth, Credentials: map[string]any{"user_agent": legacyProfileUA},
		Extra: map[string]any{"openai_pinned_installation_id": uuid.NewString(), "enable_tls_fingerprint": true, "tls_fingerprint_profile_id": 12}})
	admin := service.NewAdminService(nil, nil, nil, s.repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, s.client, nil, nil, nil, nil, nil, nil, nil, nil)
	environment := "(Ubuntu 24.04.4; x86_64) screen-256color"
	updated, err := admin.UpdateAccount(s.ctx, account.ID, &service.UpdateAccountInput{
		OpenAIEnvironmentFingerprint: &environment,
		Extra:                        map[string]any{"openai_installation_pin_enabled": false, "enable_tls_fingerprint": false, "tls_fingerprint_profile_id": nil},
	})
	s.Require().NoError(err)
	s.Require().Equal(false, updated.Extra["openai_installation_pin_enabled"])
	s.Require().Equal(false, updated.Extra["enable_tls_fingerprint"])
	s.Require().Contains(updated.Extra, "tls_fingerprint_profile_id")
	s.Require().Nil(updated.Extra["tls_fingerprint_profile_id"])
	s.Require().Equal(legacyProfileUA, updated.GetOpenAIUserAgent(), "regular OAuth environment is owned by its default OS profile")
	_, err = admin.UpdateAccount(s.ctx, account.ID, &service.UpdateAccountInput{
		Name: "stale ordinary form", Credentials: account.Credentials, Extra: map[string]any{},
	})
	s.Require().NoError(err)
	stored, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().Equal(updated.GetOpenAIUserAgent(), stored.GetOpenAIUserAgent())
	s.Require().Equal(false, stored.Extra["enable_tls_fingerprint"])
	s.Require().Equal(false, stored.Extra["openai_installation_pin_enabled"])
	_, err = admin.RegenerateOpenAIInstallationID(s.ctx, account.ID)
	s.Require().Error(err)
}

func (s *AccountRepoSuite) TestIgnoredOpenAIUserAgentWriteKeepsProbeSnapshot() {
	account := mustCreateAccount(s.T(), s.client, &service.Account{Name: "ignored-ua-probe", Platform: service.PlatformOpenAI,
		Type: service.AccountTypeAPIKey, Credentials: map[string]any{"api_key": "unchanged", "user_agent": "current UA"},
		Extra: map[string]any{service.UpstreamBillingProbeExtraKey: map[string]any{"status": "ok"}}})
	s.Require().NoError(s.repo.UpdateCredentials(s.ctx, account.ID, map[string]any{"api_key": "unchanged", "user_agent": "stale UA"}))
	stored, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().Equal("current UA", stored.GetOpenAIUserAgent())
	s.Require().Contains(stored.Extra, service.UpstreamBillingProbeExtraKey)
}

func (s *AccountRepoSuite) TestAdminTLSPatchAndBulkRemainExplicit() {
	account := mustCreateAccount(s.T(), s.client, &service.Account{Name: "tls-admin-bulk", Platform: service.PlatformAnthropic,
		Type: service.AccountTypeOAuth, Extra: map[string]any{"enable_tls_fingerprint": true, "tls_fingerprint_profile_id": 12}})
	admin := service.NewAdminService(nil, nil, nil, s.repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, s.client, nil, nil, nil, nil, nil, nil, nil, nil)
	s.Require().NoError(admin.UpdateAccountExtra(s.ctx, account.ID, map[string]any{"enable_tls_fingerprint": false, "tls_fingerprint_profile_id": nil}))
	stored, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().Equal(false, stored.Extra["enable_tls_fingerprint"])
	s.Require().Nil(stored.Extra["tls_fingerprint_profile_id"])
	_, err = admin.BulkUpdateAccounts(s.ctx, &service.BulkUpdateAccountsInput{AccountIDs: []int64{account.ID}, Extra: map[string]any{"enable_tls_fingerprint": true, "tls_fingerprint_profile_id": 24}})
	s.Require().NoError(err)
	stored, err = s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().Equal(true, stored.Extra["enable_tls_fingerprint"])
	s.Require().Equal(float64(24), stored.Extra["tls_fingerprint_profile_id"])
}

func (s *AccountRepoSuite) TestRegenerateRejectsChangedTypeAndShadow() {
	client := testEntClient(s.T())
	repo := newAccountRepositoryWithSQL(client, integrationDB, nil)
	account := mustCreateAccount(s.T(), client, &service.Account{Name: "regenerate-converted", Platform: service.PlatformOpenAI,
		Type: service.AccountTypeOAuth, Extra: map[string]any{"openai_pinned_installation_id": uuid.NewString()}})
	s.T().Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM accounts WHERE parent_account_id=$1", account.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM accounts WHERE id=$1", account.ID)
	})
	_, err := client.Account.UpdateOneID(account.ID).SetType(service.AccountTypeAPIKey).Save(s.ctx)
	s.Require().NoError(err)
	_, err = repo.RegenerateOpenAIInstallationID(s.ctx, account.ID, uuid.NewString())
	s.Require().Equal("OPENAI_INSTALLATION_REGENERATE_UNSUPPORTED", infraerrors.Reason(err))
	shadow := mustCreateAccount(s.T(), client, &service.Account{Name: "regenerate-shadow", Platform: service.PlatformOpenAI,
		Type: service.AccountTypeOAuth, ParentAccountID: &account.ID, QuotaDimension: service.QuotaDimensionSpark})
	_, err = repo.RegenerateOpenAIInstallationID(s.ctx, shadow.ID, uuid.NewString())
	s.Require().Equal("OPENAI_INSTALLATION_REGENERATE_UNSUPPORTED", infraerrors.Reason(err))
}

// Keep this compile-time assertion next to the integration coverage: generation
// is a narrow optional capability rather than another method on all mocks.
var _ service.AccountInstallationRegenerator = (*accountRepository)(nil)
