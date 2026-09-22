package service

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type oauthOSCredentialTestRepository struct {
	AccountRepository
	accounts map[int64]*Account
	slots    map[string]*OpenAIOAuthOSCredential
}

func (r *oauthOSCredentialTestRepository) GetByID(_ context.Context, id int64) (*Account, error) {
	return r.accounts[id], nil
}
func (r *oauthOSCredentialTestRepository) GetOpenAIOAuthOSCredential(_ context.Context, id int64, os string) (*OpenAIOAuthOSCredential, error) {
	slot := r.slots[os]
	if slot != nil && slot.OwnerAccountID == id {
		projection := *slot
		projection.OSFamily = os
		projection.Credentials = OpenAIOAuthProviderCredentials(r.accounts[id].Credentials)
		projection.StateGeneration = "account-state"
		projection.CredentialEpoch = "account-epoch"
		return &projection, nil
	}
	return nil, nil
}
func (r *oauthOSCredentialTestRepository) ListOpenAIOAuthOSCredentials(_ context.Context, id int64) ([]*OpenAIOAuthOSCredential, error) {
	var out []*OpenAIOAuthOSCredential
	for _, slot := range r.slots {
		if slot.OwnerAccountID == id {
			out = append(out, slot)
			break
		}
	}
	return out, nil
}

func oauthOSCredentialFixture(t *testing.T) (*Account, *oauthOSCredentialTestRepository) {
	t.Helper()
	account := &Account{ID: 10, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "shared-token", "model_mapping": map[string]any{"gpt": "model"}}}
	require.NoError(t, PrepareOpenAIOAuthOSProfilesForCreate(account))
	grant := &OpenAIOAuthOSCredential{OwnerAccountID: account.ID, OSFamily: OpenAIOSWindows,
		Credentials: map[string]any{"access_token": "shared-token"}, AuthorizationGeneration: "shared-generation", Revision: 3, Status: OpenAIOAuthAuthorizationAuthorized}
	account.OpenAIOAuthOSProfiles.Authorization = &OpenAIOAuthOSAuthorizationSummary{Status: OpenAIOAuthAuthorizationAuthorized}
	repo := &oauthOSCredentialTestRepository{accounts: map[int64]*Account{account.ID: account}, slots: map[string]*OpenAIOAuthOSCredential{}}
	for _, os := range OpenAIOAuthOSFamilies() {
		repo.slots[os] = grant // All compatibility lookups project the same grant.
	}
	return account, repo
}

func TestOpenAIOAuthSharedCredentialsSelectOnlyRequestedOrDefaultIdentity(t *testing.T) {
	account, repo := oauthOSCredentialFixture(t)
	linux, err := ResolveOpenAIOAuthCredentialAccount(context.Background(), repo, account, OpenAIOSLinux)
	require.NoError(t, err)
	profile, err := ResolveOpenAIOAuthOSProfile(context.Background(), repo, linux, "")
	require.NoError(t, err)
	require.Equal(t, OpenAIOSLinux, profile.OSFamily)
	_, err = ResolveOpenAIOAuthOSProfile(context.Background(), repo, linux, OpenAIOSWindows)
	require.ErrorIs(t, err, ErrOpenAIOAuthOSAuthorizationChanged)
	require.Equal(t, "shared-token", linux.GetCredential("access_token"))
	require.Equal(t, account.ID, linux.ID)
	unknown, err := ResolveOpenAIOAuthCredentialAccount(context.Background(), repo, account, "")
	require.NoError(t, err)
	require.Equal(t, "shared-token", unknown.GetCredential("access_token"))
	require.Equal(t, OpenAIOSWindows, unknown.OpenAIOAuthCredentialOS)
	mac, err := ResolveOpenAIOAuthCredentialAccount(context.Background(), repo, account, OpenAIOSMacOS)
	require.NoError(t, err, "an old unauthorized OS summary must not exclude a shared grant")
	require.Equal(t, OpenAIOSMacOS, mac.OpenAIOAuthCredentialOS)
	require.Equal(t, linux.OpenAIOAuthAuthorizationGeneration, mac.OpenAIOAuthAuthorizationGeneration)
	require.Equal(t, linux.OpenAIOAuthCredentialRevision, mac.OpenAIOAuthCredentialRevision)
	require.Equal(t, linux.OpenAIOAuthCredentialStateGeneration, mac.OpenAIOAuthCredentialStateGeneration)
	require.NotEqual(t, linux.GetCredential("user_agent"), mac.GetCredential("user_agent"))
	linux.Credentials["model_mapping"].(map[string]any)["gpt"] = "changed"
	require.Equal(t, "model", account.Credentials["model_mapping"].(map[string]any)["gpt"])
	require.Equal(t, "shared-token", account.GetCredential("access_token"))
}

func TestOpenAIOAuthOSCredentialsReloadFencesReauthorization(t *testing.T) {
	account, repo := oauthOSCredentialFixture(t)
	scoped, err := ResolveOpenAIOAuthCredentialAccount(context.Background(), repo, account, OpenAIOSLinux)
	require.NoError(t, err)
	repo.slots[OpenAIOSLinux].Revision++
	fresh, err := ReloadOpenAIOAuthCredentialAccount(context.Background(), repo, scoped)
	require.NoError(t, err)
	require.Equal(t, int64(4), fresh.OpenAIOAuthCredentialRevision)
	repo.slots[OpenAIOSLinux].AuthorizationGeneration = "new-authorization"
	_, err = ReloadOpenAIOAuthCredentialAccount(context.Background(), repo, scoped)
	require.ErrorIs(t, err, ErrOpenAIOAuthOSAuthorizationChanged)
	_, err = ResolveOpenAIOAuthCredentialAccount(context.Background(), repo, scoped, OpenAIOSWindows)
	require.ErrorIs(t, err, ErrOpenAIOAuthOSAuthorizationChanged)
}

func TestOpenAIOAuthAccountCredentialsResolveLatestAccountWithoutAuthorizationSummary(t *testing.T) {
	account, repo := oauthOSCredentialFixture(t)
	stale := *account
	stale.Credentials = map[string]any{"access_token": "stale-token", "model_mapping": map[string]any{"gpt": "old-model"}}
	account.Credentials["access_token"] = "current-account-token"
	account.Credentials["model_mapping"] = map[string]any{"gpt": "current-model"}
	account.OpenAIOAuthOSProfiles.Authorization = nil
	for _, os := range OpenAIOAuthOSFamilies() {
		require.True(t, OpenAIOAuthOSAuthorizationAvailable(account, os))
		resolved, err := ResolveOpenAIOAuthCredentialAccount(context.Background(), repo, &stale, os)
		require.NoError(t, err)
		require.Equal(t, "current-account-token", resolved.GetOpenAIAccessToken())
		require.Equal(t, "current-model", resolved.Credentials["model_mapping"].(map[string]any)["gpt"])
		require.Equal(t, os, resolved.OpenAIOAuthCredentialOS)
	}
	account.Credentials = map[string]any{"model_mapping": map[string]any{"gpt": "current-model"}}
	require.False(t, OpenAIOAuthOSAuthorizationAvailable(account, OpenAIOSWindows), "configuration alone is not an OAuth credential")
}

func TestOpenAIOAuthOSCredentialsSparkPreservesBusinessIdentity(t *testing.T) {
	owner, repo := oauthOSCredentialFixture(t)
	shadow := &Account{ID: 20, Platform: PlatformOpenAI, Type: AccountTypeOAuth, ParentAccountID: &owner.ID, Concurrency: 7}
	repo.accounts[shadow.ID] = shadow
	scoped, err := ResolveOpenAIOAuthCredentialAccount(context.Background(), repo, shadow, OpenAIOSLinux)
	require.NoError(t, err)
	require.Equal(t, shadow.ID, scoped.ID)
	require.Equal(t, 7, scoped.Concurrency)
	require.Equal(t, owner.ID, scoped.OpenAIOAuthCredentialOwnerID)
	require.Equal(t, "shared-token", scoped.GetCredential("access_token"))
}

func TestOpenAIOAuthCredentialsIgnoreLegacyAuthorizationGates(t *testing.T) {
	account, repo := oauthOSCredentialFixture(t)
	identity, err := ResolveOpenAIOAuthCredentialAccount(context.Background(), nil, account, OpenAIOSWindows)
	require.NoError(t, err)
	require.Equal(t, account.GetOpenAIAccessToken(), identity.GetOpenAIAccessToken())
	require.Empty(t, identity.OpenAIOAuthAuthorizationGeneration)
	encoded, err := json.Marshal(repo.slots[OpenAIOSWindows])
	require.NoError(t, err)
	require.JSONEq(t, "{}", string(encoded))
	scoped, err := ResolveOpenAIOAuthCredentialAccount(context.Background(), repo, account, OpenAIOSLinux)
	require.NoError(t, err)
	encoded, err = json.Marshal(scoped)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "shared-generation")
	require.NotContains(t, string(encoded), "OpenAIOAuthCredential")
	until := time.Now().Add(time.Minute)
	repo.slots[OpenAIOSWindows].RefreshRetryAfter = &until
	for _, status := range []string{OpenAIOAuthAuthorizationUnauthorized, OpenAIOAuthAuthorizationReauthRequired, OpenAIOAuthAuthorizationAuthorized} {
		repo.slots[OpenAIOSWindows].Status = status
		for _, os := range OpenAIOAuthOSFamilies() {
			resolved, err := ResolveOpenAIOAuthCredentialAccount(context.Background(), repo, account, os)
			require.NoError(t, err, "account scheduling and token provider own eligibility")
			require.Equal(t, "shared-token", resolved.GetOpenAIAccessToken())
			require.Equal(t, os, resolved.OpenAIOAuthCredentialOS)
		}
	}
}

func TestOpenAIOAuthIdentitySelectionDoesNotReadAuthorization(t *testing.T) {
	account, repo := oauthOSCredentialFixture(t)
	repo.slots = nil
	account.Credentials = nil
	for _, os := range OpenAIOAuthOSFamilies() {
		identity, err := ResolveOpenAIOAuthIdentityAccount(context.Background(), repo, account, os)
		require.NoError(t, err)
		require.Equal(t, os, identity.OpenAIOAuthCredentialOS)
		require.Equal(t, account.OpenAIOAuthOSProfiles.Profiles[os].UserAgent, identity.GetOpenAIUserAgent())
		require.Equal(t, account.OpenAIOAuthOSProfiles.Profiles[os].InstallationID, identity.GetPinnedOpenAIInstallationID())
		require.Empty(t, identity.OpenAIOAuthAuthorizationGeneration)
		require.Empty(t, identity.GetOpenAIAccessToken())
		require.Equal(t, "openai:account:10", OpenAITokenCacheKey(identity))
	}
	require.Nil(t, account.Credentials)
}

func TestOpenAIOAuthOSCredentialsProviderFieldsProtected(t *testing.T) {
	current := map[string]any{"access_token": "latest", "refresh_token": "latest-refresh", "chatgpt_user_id": "u"}
	stale := map[string]any{"access_token": "stale", "refresh_token": "stale-refresh", "chatgpt_user_id": "other", "model_mapping": map[string]any{"a": "b"}}
	protected := PreserveOpenAIOAuthProviderCredentials(current, stale)
	require.Equal(t, "latest", protected["access_token"])
	require.Equal(t, "u", protected["chatgpt_user_id"])
	require.Equal(t, stale["model_mapping"], protected["model_mapping"])
	require.Equal(t, "stale", stale["access_token"])
}

func TestOpenAIOAuthOSCredentialsConcurrentProjectionIsolation(t *testing.T) {
	account, repo := oauthOSCredentialFixture(t)
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			projected, err := ResolveOpenAIOAuthCredentialAccount(context.Background(), repo, account, OpenAIOSLinux)
			if err != nil {
				t.Error(err)
				return
			}
			projected.Credentials["model_mapping"].(map[string]any)["gpt"] = "local"
			projected.Credentials["access_token"] = "local"
		}()
	}
	wg.Wait()
	require.Equal(t, "shared-token", account.GetCredential("access_token"))
	require.Equal(t, "shared-token", repo.slots[OpenAIOSLinux].Credentials["access_token"])
	require.Equal(t, "model", account.Credentials["model_mapping"].(map[string]any)["gpt"])
}

func TestOpenAIOAuthOSCredentialsCreateUsesExplicitInitialOS(t *testing.T) {
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, OpenAIOAuthInitialOS: OpenAIOSMacOS, Credentials: map[string]any{"access_token": "initial"}}
	require.NoError(t, PrepareOpenAIOAuthOSProfilesForCreate(account))
	require.Equal(t, OpenAIOSMacOS, account.OpenAIOAuthOSProfiles.DefaultOS)
	for _, profile := range account.OpenAIOAuthOSProfiles.Profiles {
		require.Equal(t, OpenAIOAuthAuthorizationUnauthorized, profile.Authorization.Status, "installation creation must not imply authorization")
	}
	account.OpenAIOAuthInitialOS = "unsupported-os"
	require.ErrorIs(t, PrepareOpenAIOAuthOSProfilesForCreate(account), ErrOpenAIOAuthOSUnauthorized)
}

func TestOpenAIOAuthSharedAuthorizationSummaryIgnoresRequestedOS(t *testing.T) {
	until := time.Now().Add(time.Hour)
	for _, test := range []struct {
		name          string
		shared        *OpenAIOAuthOSAuthorizationSummary
		defaultStatus string
		otherStatus   string
		available     bool
	}{
		{"shared authorized supersedes old slots", &OpenAIOAuthOSAuthorizationSummary{Status: OpenAIOAuthAuthorizationAuthorized}, OpenAIOAuthAuthorizationUnauthorized, OpenAIOAuthAuthorizationUnauthorized, true},
		{"old shared error does not override account credentials", &OpenAIOAuthOSAuthorizationSummary{Status: OpenAIOAuthAuthorizationReauthRequired}, OpenAIOAuthAuthorizationAuthorized, OpenAIOAuthAuthorizationAuthorized, true},
		{"old shared cooldown does not override account credentials", &OpenAIOAuthOSAuthorizationSummary{Status: OpenAIOAuthAuthorizationAuthorized, RefreshRetryAfter: &until}, OpenAIOAuthAuthorizationAuthorized, OpenAIOAuthAuthorizationAuthorized, true},
		{"legacy default grant remains usable", nil, OpenAIOAuthAuthorizationAuthorized, OpenAIOAuthAuthorizationUnauthorized, true},
		{"legacy unauthorized default does not gate account credentials", nil, OpenAIOAuthAuthorizationUnauthorized, OpenAIOAuthAuthorizationAuthorized, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "account-token"}, OpenAIOAuthOSProfiles: &OpenAIOAuthOSProfiles{
				DefaultOS: OpenAIOSWindows, Authorization: test.shared, Profiles: map[string]OpenAIOAuthOSProfile{
					OpenAIOSWindows: {Authorization: OpenAIOAuthOSAuthorizationSummary{Status: test.defaultStatus}},
					OpenAIOSLinux:   {Authorization: OpenAIOAuthOSAuthorizationSummary{Status: test.otherStatus}},
				},
			}}
			for _, os := range []string{"", OpenAIOSWindows, OpenAIOSMacOS, OpenAIOSLinux} {
				require.Equal(t, test.available, OpenAIOAuthOSAuthorizationAvailable(account, os), os)
			}
		})
	}
}

func TestOpenAIOAuthSharedAuthorizationFencesEveryFrozenIdentity(t *testing.T) {
	account, repo := oauthOSCredentialFixture(t)
	var scoped []*Account
	for _, os := range OpenAIOAuthOSFamilies() {
		resolved, err := ResolveOpenAIOAuthCredentialAccount(context.Background(), repo, account, os)
		require.NoError(t, err)
		scoped = append(scoped, resolved)
	}
	repo.slots[OpenAIOSWindows].AuthorizationGeneration = "shared-replacement"
	for _, frozen := range scoped {
		_, err := ReloadOpenAIOAuthCredentialAccount(context.Background(), repo, frozen)
		require.ErrorIs(t, err, ErrOpenAIOAuthOSAuthorizationChanged, frozen.OpenAIOAuthCredentialOS)
	}
	repo.slots[OpenAIOSWindows].Status = OpenAIOAuthAuthorizationUnauthorized
	for _, os := range OpenAIOAuthOSFamilies() {
		resolved, err := ResolveOpenAIOAuthCredentialAccount(context.Background(), repo, account, os)
		require.NoError(t, err, "legacy private status is not an account admission condition")
		require.Equal(t, "shared-replacement", resolved.OpenAIOAuthAuthorizationGeneration)
	}
}

func TestOpenAIOAuthIdentityDoesNotValidateTokensOrInventMissingProfiles(t *testing.T) {
	account, repo := oauthOSCredentialFixture(t)
	account.Credentials["access_token"] = "  "
	resolved, err := ResolveOpenAIOAuthCredentialAccount(context.Background(), repo, account, OpenAIOSLinux)
	require.NoError(t, err, "token provider owns final token validation")
	require.Equal(t, "  ", resolved.GetOpenAIAccessToken())
	account.Credentials["access_token"] = "shared-token"
	delete(account.OpenAIOAuthOSProfiles.Profiles, OpenAIOSLinux)
	_, err = ResolveOpenAIOAuthCredentialAccount(context.Background(), repo, account, OpenAIOSLinux)
	require.ErrorIs(t, err, ErrOpenAIOAuthOSProfileUnavailable, "shared auth must not invent an unpersisted OS identity")
}

func TestOpenAIOAuthSharedAuthorizationAllowsRefreshOnlyGrant(t *testing.T) {
	account, repo := oauthOSCredentialFixture(t)
	account.Credentials = map[string]any{"refresh_token": "refresh-only"}
	for _, os := range OpenAIOAuthOSFamilies() {
		resolved, err := ResolveOpenAIOAuthCredentialAccount(context.Background(), repo, account, os)
		require.NoError(t, err, os)
		require.Empty(t, resolved.GetOpenAIAccessToken(), "resolver must not manufacture an access token")
		require.Equal(t, "refresh-only", resolved.GetCredential("refresh_token"))
		require.Equal(t, os, resolved.OpenAIOAuthCredentialOS)
		require.Equal(t, "shared-generation", resolved.OpenAIOAuthAuthorizationGeneration)
	}
}

func TestCloneOpenAIOAuthOSProfilesIsolatesSharedAuthorizationSummary(t *testing.T) {
	until := time.Now().Add(time.Hour)
	original := &OpenAIOAuthOSProfiles{Authorization: &OpenAIOAuthOSAuthorizationSummary{Status: OpenAIOAuthAuthorizationAuthorized, RefreshRetryAfter: &until}}
	copy := CloneOpenAIOAuthOSProfiles(original)
	copy.Authorization.Status = OpenAIOAuthAuthorizationUnauthorized
	*copy.Authorization.RefreshRetryAfter = until.Add(time.Hour)
	require.Equal(t, OpenAIOAuthAuthorizationAuthorized, original.Authorization.Status)
	require.Equal(t, until, *original.Authorization.RefreshRetryAfter)
}
