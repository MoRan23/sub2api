//go:build integration

package repository

import (
	"context"
	"sync"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

const legacyProfileUA = "codex-tui/0.154.0 (Ubuntu 24.04.0; x86_64) xterm-256color"

func (s *AccountRepoSuite) TestOAuthOSProfilesMigrateLegacyIdentityAndReadWithoutWrites() {
	installation := uuid.NewString()
	syncRoot, err := uuid.NewV7()
	s.Require().NoError(err)
	account := mustCreateAccount(s.T(), s.client, &service.Account{Name: "os-profile-legacy", Platform: service.PlatformOpenAI,
		Type: service.AccountTypeOAuth, Credentials: map[string]any{"user_agent": legacyProfileUA},
		Extra: map[string]any{"openai_pinned_installation_id": installation, "openai_installation_pin_enabled": false}})
	_, err = s.client.ExecContext(s.ctx, `INSERT INTO openai_oauth_sync_sessions(account_id,session_id) VALUES ($1,$2)`, account.ID, syncRoot.String())
	s.Require().NoError(err)
	missing, err := s.repo.GetOpenAIOAuthOSProfiles(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().Nil(missing, "a read-only export must not initialize identity")
	first, err := s.repo.EnsureOpenAIOAuthOSProfiles(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().True(service.OpenAIOAuthOSProfilesComplete(first))
	s.Require().Equal(service.OpenAIOSLinux, first.DefaultOS)
	s.Require().Equal(installation, first.Profiles[service.OpenAIOSLinux].InstallationID)
	s.Require().Equal(legacyProfileUA, first.Profiles[service.OpenAIOSLinux].UserAgent)
	s.Require().Equal(syncRoot.String(), first.Profiles[service.OpenAIOSLinux].SyncSessionID)
	second, err := s.repo.EnsureOpenAIOAuthOSProfiles(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().Equal(first, second, "disabled normalization still gets stable complete profiles")
	stored, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().Equal(first, stored.OpenAIOAuthOSProfiles)
	s.Require().Equal(installation, stored.GetPinnedOpenAIInstallationID())
	s.Require().Equal(legacyProfileUA, stored.GetOpenAIUserAgent())
	accounts, err := s.repo.GetByIDs(s.ctx, []int64{account.ID})
	s.Require().NoError(err)
	s.Require().Equal(first, accounts[0].OpenAIOAuthOSProfiles)
}

func (s *AccountRepoSuite) TestOAuthOSProfilesNewAccountsDefaultToWindows() {
	account := &service.Account{Name: "os-profile-new", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
		Status: service.StatusActive, Credentials: map[string]any{"user_agent": legacyProfileUA},
		Extra: map[string]any{"openai_pinned_installation_id": uuid.NewString()}}
	s.Require().NoError(s.repo.Create(s.ctx, account))
	s.Require().True(service.OpenAIOAuthOSProfilesComplete(account.OpenAIOAuthOSProfiles))
	s.Require().Equal(service.OpenAIOSWindows, account.OpenAIOAuthOSProfiles.DefaultOS)
	stored, err := s.repo.GetOpenAIOAuthOSProfiles(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().Equal(account.OpenAIOAuthOSProfiles, stored)
	legacySync, err := loadLegacyOpenAIOAuthSyncSession(s.ctx, s.client, account.ID)
	s.Require().NoError(err)
	s.Require().Equal(stored.Profiles[stored.DefaultOS].SyncSessionID, legacySync)
}

func (s *AccountRepoSuite) TestOAuthOSProfilesLegacyInstallationRepairRestoresDefault() {
	account := mustCreateAccount(s.T(), s.client, &service.Account{Name: "os-profile-legacy-repair", Platform: service.PlatformOpenAI,
		Type: service.AccountTypeOAuth, Credentials: map[string]any{"user_agent": legacyProfileUA},
		Extra: map[string]any{"openai_pinned_installation_id": uuid.NewString()}})
	profiles, err := s.repo.EnsureOpenAIOAuthOSProfiles(s.ctx, account.ID)
	s.Require().NoError(err)
	defaultProfile := profiles.Profiles[profiles.DefaultOS]
	_, err = s.client.ExecContext(s.ctx, `UPDATE accounts SET extra=extra-'openai_pinned_installation_id' WHERE id=$1`, account.ID)
	s.Require().NoError(err)
	resolved, err := s.repo.EnsureOpenAIInstallationID(s.ctx, account.ID, "", uuid.NewString())
	s.Require().NoError(err)
	s.Require().Equal(defaultProfile.InstallationID, resolved, "a legacy repair must not rotate the profile identity")
	stored, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().Equal(profiles, stored.OpenAIOAuthOSProfiles)
	s.Require().Equal(defaultProfile.InstallationID, stored.GetPinnedOpenAIInstallationID())
	s.Require().Equal(defaultProfile.UserAgent, stored.GetOpenAIUserAgent())
}

func (s *AccountRepoSuite) TestOAuthOSProfilesRegenerateOneSlotAndFenceStaleWrites() {
	account := mustCreateAccount(s.T(), s.client, &service.Account{Name: "os-profile-stale", Platform: service.PlatformOpenAI,
		Type: service.AccountTypeOAuth, Credentials: map[string]any{"user_agent": legacyProfileUA},
		Extra: map[string]any{"openai_pinned_installation_id": uuid.NewString()}})
	first, err := s.repo.EnsureOpenAIOAuthOSProfiles(s.ctx, account.ID)
	s.Require().NoError(err)
	stale, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	regenerated, err := s.repo.RegenerateOpenAIOAuthOSProfileInstallationID(s.ctx, account.ID, service.OpenAIOSWindows)
	s.Require().NoError(err)
	s.Require().NotEqual(first.Profiles[service.OpenAIOSWindows].InstallationID, regenerated.Profiles[service.OpenAIOSWindows].InstallationID)
	s.Require().Equal(first.Profiles[service.OpenAIOSLinux], regenerated.Profiles[service.OpenAIOSLinux])
	s.Require().Equal(first.Profiles[service.OpenAIOSMacOS], regenerated.Profiles[service.OpenAIOSMacOS])
	s.Require().Equal(first.Profiles[service.OpenAIOSWindows].SyncSessionID, regenerated.Profiles[service.OpenAIOSWindows].SyncSessionID)
	stale.Credentials["user_agent"] = "stale forged environment"
	stale.Extra["openai_pinned_installation_id"] = uuid.NewString()
	stale.OpenAIOAuthOSProfiles = first
	s.Require().NoError(s.repo.Update(s.ctx, stale))
	s.Require().NoError(s.repo.UpdateCredentials(s.ctx, account.ID, map[string]any{"access_token": "synthetic-test-token", "user_agent": "stale refresh UA"}))
	stored, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().Equal(regenerated, stored.OpenAIOAuthOSProfiles)
	s.Require().Equal(regenerated.Profiles[regenerated.DefaultOS].InstallationID, stored.GetPinnedOpenAIInstallationID())
	s.Require().Equal(regenerated.Profiles[regenerated.DefaultOS].UserAgent, stored.GetOpenAIUserAgent())
	_, err = s.client.ExecContext(s.ctx, `UPDATE accounts SET extra=extra || '{"openai_installation_pin_enabled":false}'::jsonb WHERE id=$1`, account.ID)
	s.Require().NoError(err)
	_, err = s.repo.RegenerateOpenAIOAuthOSProfileInstallationID(s.ctx, account.ID, service.OpenAIOSLinux)
	s.Require().Equal("OPENAI_INSTALLATION_REGENERATE_PIN_DISABLED", infraerrors.Reason(err))
	unchanged, err := s.repo.GetOpenAIOAuthOSProfiles(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().Equal(regenerated, unchanged)
}

func (s *AccountRepoSuite) TestOAuthOSProfilesRejectOtherCredentialKinds() {
	for _, candidate := range []*service.Account{
		{Name: "setup", Platform: service.PlatformOpenAI, Type: service.AccountTypeSetupToken},
		{Name: "apikey", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey},
		{Name: "pat", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Credentials: map[string]any{"auth_mode": "personalAccessToken"}},
		{Name: "agent", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Credentials: map[string]any{"openai_auth_mode": "agentIdentity"}},
	} {
		account := mustCreateAccount(s.T(), s.client, candidate)
		_, err := s.repo.EnsureOpenAIOAuthOSProfiles(s.ctx, account.ID)
		s.Require().Equal("OPENAI_OAUTH_OS_PROFILES_UNSUPPORTED", infraerrors.Reason(err), candidate.Name)
		profiles, err := s.repo.GetOpenAIOAuthOSProfiles(s.ctx, account.ID)
		s.Require().NoError(err)
		s.Require().Nil(profiles)
	}
	parent := mustCreateAccount(s.T(), s.client, &service.Account{Name: "os-profile-parent", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth})
	shadow := mustCreateAccount(s.T(), s.client, &service.Account{Name: "os-profile-shadow", Platform: service.PlatformOpenAI,
		Type: service.AccountTypeOAuth, ParentAccountID: &parent.ID, QuotaDimension: service.QuotaDimensionSpark})
	_, err := s.repo.EnsureOpenAIOAuthOSProfiles(s.ctx, shadow.ID)
	s.Require().Equal("OPENAI_OAUTH_OS_PROFILES_UNSUPPORTED", infraerrors.Reason(err))
}

func (s *AccountRepoSuite) TestOAuthOSProfilesFollowCredentialModeTransitions() {
	account := mustCreateAccount(s.T(), s.client, &service.Account{Name: "os-profile-mode-transition", Platform: service.PlatformOpenAI,
		Type: service.AccountTypeOAuth, Credentials: map[string]any{"auth_mode": "personalAccessToken", "user_agent": legacyProfileUA}})
	modeCtx := service.WithOpenAIOAuthCredentialModeChangeIntent(s.ctx, account.ID)
	// UpdateCredentials replaces the map. Omitting an old mode is a real
	// eligibility transition and must initialize profiles in the same transaction.
	s.Require().NoError(s.repo.UpdateCredentials(modeCtx, account.ID, map[string]any{"access_token": "synthetic-regular-token"}))
	profiles, err := s.repo.GetOpenAIOAuthOSProfiles(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().True(service.OpenAIOAuthOSProfilesComplete(profiles))
	s.Require().NoError(s.repo.UpdateCredentials(modeCtx, account.ID, map[string]any{"auth_mode": "personalAccessToken", "access_token": "synthetic-pat"}))
	profiles, err = s.repo.GetOpenAIOAuthOSProfiles(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().Nil(profiles)
	rows, err := s.repo.BulkUpdate(modeCtx, []int64{account.ID}, service.AccountBulkUpdate{
		Credentials: map[string]any{"auth_mode": nil},
	})
	s.Require().NoError(err)
	s.Require().Equal(int64(1), rows)
	profiles, err = s.repo.GetOpenAIOAuthOSProfiles(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().True(service.OpenAIOAuthOSProfilesComplete(profiles))
	_, err = s.repo.BulkUpdate(modeCtx, []int64{account.ID}, service.AccountBulkUpdate{
		Credentials: map[string]any{"openai_auth_mode": "agentIdentity"},
	})
	s.Require().NoError(err)
	profiles, err = s.repo.GetOpenAIOAuthOSProfiles(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().Nil(profiles)
}

func TestOAuthOSProfilesConcurrentInitialization(t *testing.T) {
	client := testEntClient(t)
	repo := newAccountRepositoryWithSQL(client, integrationDB, nil)
	account := mustCreateAccount(t, client, &service.Account{Name: "os-profile-concurrent", Platform: service.PlatformOpenAI,
		Type: service.AccountTypeOAuth, Credentials: map[string]any{"user_agent": legacyProfileUA},
		Extra: map[string]any{"openai_pinned_installation_id": uuid.NewString()}})
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM scheduler_outbox WHERE account_id=$1", account.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM accounts WHERE id=$1", account.ID)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	const writers = 8
	profiles := make([]*service.OpenAIOAuthOSProfiles, writers)
	errs := make([]error, writers)
	var wg sync.WaitGroup
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			profiles[i], errs[i] = repo.EnsureOpenAIOAuthOSProfiles(ctx, account.ID)
		}()
	}
	wg.Wait()
	for i := range writers {
		require.NoError(t, errs[i])
		require.Equal(t, profiles[0], profiles[i])
	}
	require.True(t, service.OpenAIOAuthOSProfilesComplete(profiles[0]))
}

var _ service.OpenAIOAuthOSProfilesRepository = (*accountRepository)(nil)
var _ service.OpenAIOAuthOSProfilesBackfiller = (*accountRepository)(nil)
var _ service.OpenAIOAuthOSProfileInstallationRegenerator = (*accountRepository)(nil)
