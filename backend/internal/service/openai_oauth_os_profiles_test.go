package service

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func legacyOSProfileAccount() *Account {
	return &Account{ID: 71, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Credentials: map[string]any{"user_agent": "codex-tui/0.154.0 (Mac OS X 15.1.0; arm64) iTerm.app"},
		Extra:       map[string]any{openAIPinnedInstallationIDKey: "11111111-2222-4333-8444-555555555555", openAIInstallationPinEnabledKey: false}}
}

func TestBuildOpenAIOAuthOSProfilesMigratesLegacyPairAndSync(t *testing.T) {
	account := legacyOSProfileAccount()
	legacySync := "019141aa-1111-7000-8000-000000000001"
	profiles, err := BuildOpenAIOAuthOSProfiles(account, nil, legacySync)
	require.NoError(t, err)
	require.True(t, OpenAIOAuthOSProfilesComplete(profiles))
	require.Equal(t, OpenAIOSMacOS, profiles.DefaultOS)
	require.Equal(t, account.GetPinnedOpenAIInstallationID(), profiles.Profiles[OpenAIOSMacOS].InstallationID)
	require.Equal(t, account.GetOpenAIUserAgent(), profiles.Profiles[OpenAIOSMacOS].UserAgent)
	require.Equal(t, legacySync, profiles.Profiles[OpenAIOSMacOS].SyncSessionID)
	require.False(t, account.IsOpenAIInstallationPinEnabled(), "generating profiles must not enable projection")
	require.Nil(t, account.OpenAIOAuthOSProfiles, "building a repair must not mutate the account snapshot")

	repaired, err := BuildOpenAIOAuthOSProfiles(account, profiles, "019141aa-1111-7000-8000-000000000002")
	require.NoError(t, err)
	require.Equal(t, profiles, repaired, "an existing complete profile set wins over stale legacy mirrors")
	repaired.Profiles[OpenAIOSMacOS] = OpenAIOAuthOSProfile{}
	require.NotEmpty(t, profiles.Profiles[OpenAIOSMacOS].InstallationID, "profile maps must not alias")
}

func TestBuildOpenAIOAuthOSProfilesRepairsOnlyInvalidFields(t *testing.T) {
	account := legacyOSProfileAccount()
	profiles, err := BuildOpenAIOAuthOSProfiles(account, nil)
	require.NoError(t, err)
	before := CloneOpenAIOAuthOSProfiles(profiles)
	broken := profiles.Profiles[OpenAIOSWindows]
	broken.InstallationID = "invalid"
	broken.SyncSessionID = uuid.NewString()
	broken.UserAgent = before.Profiles[OpenAIOSLinux].UserAgent
	profiles.Profiles[OpenAIOSWindows] = broken
	profiles.Profiles["foreign"] = OpenAIOAuthOSProfile{}
	repaired, err := BuildOpenAIOAuthOSProfiles(account, profiles)
	require.NoError(t, err)
	require.True(t, OpenAIOAuthOSProfilesComplete(repaired))
	require.Equal(t, before.Profiles[OpenAIOSMacOS], repaired.Profiles[OpenAIOSMacOS])
	require.Equal(t, before.Profiles[OpenAIOSLinux], repaired.Profiles[OpenAIOSLinux])
	require.NotEqual(t, "invalid", repaired.Profiles[OpenAIOSWindows].InstallationID)
}

func TestPrepareOpenAIOAuthOSProfilesForCreateUsesFreshWindowsDefault(t *testing.T) {
	account := legacyOSProfileAccount()
	oldID := account.GetPinnedOpenAIInstallationID()
	require.NoError(t, PrepareOpenAIOAuthOSProfilesForCreate(account))
	require.True(t, OpenAIOAuthOSProfilesComplete(account.OpenAIOAuthOSProfiles))
	require.Equal(t, OpenAIOSWindows, account.OpenAIOAuthOSProfiles.DefaultOS)
	require.NotEqual(t, oldID, account.GetPinnedOpenAIInstallationID())
	require.Equal(t, account.OpenAIOAuthOSProfiles.Profiles[OpenAIOSWindows].UserAgent, account.GetOpenAIUserAgent())
	require.False(t, account.IsOpenAIInstallationPinEnabled())
}

func TestOpenAIOAuthOSProfilesOwnerBoundaries(t *testing.T) {
	for _, test := range []struct {
		name    string
		account Account
	}{
		{"apikey", Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}},
		{"setup token", Account{Platform: PlatformOpenAI, Type: AccountTypeSetupToken}},
		{"foreign", Account{Platform: PlatformAnthropic, Type: AccountTypeOAuth}},
		{"implicit", Account{Type: AccountTypeOAuth}},
		{"PAT", Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"auth_mode": "personalAccessToken"}}},
		{"agent", Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"auth_mode": "agentIdentity"}}},
		{"legacy agent", Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"openai_auth_mode": " agentIdentity "}}},
		{"shadow", Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, ParentAccountID: func() *int64 { id := int64(71); return &id }()}},
	} {
		t.Run(test.name, func(t *testing.T) {
			require.False(t, IsOpenAIOAuthOSProfileOwner(&test.account))
			_, err := BuildOpenAIOAuthOSProfiles(&test.account, nil)
			require.Error(t, err)
		})
	}
}

type osProfileResolverRepo struct {
	AccountRepository
	owner    *Account
	profiles *OpenAIOAuthOSProfiles
	ensures  int
	err      error
}

func (r *osProfileResolverRepo) GetByID(context.Context, int64) (*Account, error) {
	return r.owner, nil
}
func (r *osProfileResolverRepo) EnsureOpenAIOAuthOSProfiles(context.Context, int64) (*OpenAIOAuthOSProfiles, error) {
	r.ensures++
	return CloneOpenAIOAuthOSProfiles(r.profiles), r.err
}

func TestResolveOpenAIOAuthOSProfileRequiresPersistedIdentity(t *testing.T) {
	owner := legacyOSProfileAccount()
	profile, err := ResolveOpenAIOAuthOSProfile(context.Background(), nil, owner, OpenAIOSWindows)
	require.ErrorIs(t, err, ErrOpenAIOAuthOSProfileUnavailable)
	require.Empty(t, profile.InstallationID)
	shadow := &Account{ID: 72, Platform: PlatformOpenAI, Type: AccountTypeOAuth, ParentAccountID: &owner.ID}
	profile, err = ResolveOpenAIOAuthOSProfile(context.Background(), nil, shadow, OpenAIOSWindows)
	require.ErrorIs(t, err, ErrOpenAIOAuthOSProfileUnavailable)
	require.Empty(t, profile.InstallationID)
	repo := &osProfileResolverRepo{owner: owner, err: errors.New("database parameters contain test-secret-token")}
	profile, err = ResolveOpenAIOAuthOSProfile(context.Background(), repo, owner, OpenAIOSWindows)
	require.ErrorIs(t, err, ErrOpenAIOAuthOSProfileUnavailable)
	require.NotContains(t, err.Error(), "test-secret-token")
	require.Empty(t, profile.InstallationID)
	repo.err = nil
	profile, err = ResolveOpenAIOAuthOSProfile(context.Background(), repo, owner, OpenAIOSWindows)
	require.ErrorIs(t, err, ErrOpenAIOAuthOSProfileUnavailable)
	require.Empty(t, profile.InstallationID)
}

func TestResolveOpenAIOAuthOSProfileUsesOwnerAndDefault(t *testing.T) {
	owner := legacyOSProfileAccount()
	profiles, err := BuildOpenAIOAuthOSProfiles(owner, nil)
	require.NoError(t, err)
	repo := &osProfileResolverRepo{owner: owner, profiles: profiles}
	shadow := &Account{ID: 72, Platform: PlatformOpenAI, Type: AccountTypeOAuth, ParentAccountID: &owner.ID, QuotaDimension: QuotaDimensionSpark}
	selected, err := ResolveOpenAIOAuthOSProfile(context.Background(), repo, shadow, "unknown")
	require.NoError(t, err)
	require.Equal(t, profiles.Profiles[OpenAIOSMacOS], selected)
	require.Equal(t, 1, repo.ensures)
	owner.OpenAIOAuthOSProfiles = profiles
	selected, err = ResolveOpenAIOAuthOSProfile(context.Background(), repo, owner, "windows")
	require.NoError(t, err)
	require.Equal(t, profiles.Profiles[OpenAIOSWindows], selected)
	require.Equal(t, 1, repo.ensures, "loaded complete metadata needs no persistence read")
}

func TestPreserveAccountConfigurationIgnoresLegacyOAuthEnvironmentEdit(t *testing.T) {
	current := legacyOSProfileAccount()
	profiles, err := BuildOpenAIOAuthOSProfiles(current, nil)
	require.NoError(t, err)
	ApplyOpenAIOAuthOSProfiles(current, profiles)
	target := *current
	target.Credentials = map[string]any{"access_token": "test-new-token", "user_agent": "caller UA"}
	environment := "(Windows 10.0.26200; x86_64) WindowsTerminal"
	require.NoError(t, PreserveAccountConfiguration(current, &target, AccountConfigurationIntent{Environment: &environment}))
	require.Equal(t, current.GetOpenAIUserAgent(), target.GetOpenAIUserAgent())
	require.Equal(t, "test-new-token", target.GetCredential("access_token"))
	require.Equal(t, current.OpenAIOAuthOSProfiles, target.OpenAIOAuthOSProfiles)
	target.OpenAIOAuthOSProfiles.Profiles[OpenAIOSWindows] = OpenAIOAuthOSProfile{}
	require.NotEmpty(t, current.OpenAIOAuthOSProfiles.Profiles[OpenAIOSWindows].InstallationID)
}
