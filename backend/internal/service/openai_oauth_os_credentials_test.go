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
		return slot, nil
	}
	return nil, nil
}
func (r *oauthOSCredentialTestRepository) ListOpenAIOAuthOSCredentials(_ context.Context, id int64) ([]*OpenAIOAuthOSCredential, error) {
	var out []*OpenAIOAuthOSCredential
	for _, slot := range r.slots {
		if slot.OwnerAccountID == id {
			out = append(out, slot)
		}
	}
	return out, nil
}

func oauthOSCredentialFixture(t *testing.T) (*Account, *oauthOSCredentialTestRepository) {
	t.Helper()
	account := &Account{ID: 10, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "default-only", "model_mapping": map[string]any{"gpt": "model"}}}
	require.NoError(t, PrepareOpenAIOAuthOSProfilesForCreate(account))
	repo := &oauthOSCredentialTestRepository{accounts: map[int64]*Account{account.ID: account}, slots: map[string]*OpenAIOAuthOSCredential{
		OpenAIOSWindows: {OwnerAccountID: account.ID, OSFamily: OpenAIOSWindows, Credentials: map[string]any{"access_token": "windows-token"}, AuthorizationGeneration: "windows-generation", Revision: 2, StateGeneration: "windows-state", CredentialEpoch: "windows-epoch", Status: OpenAIOAuthAuthorizationAuthorized},
		OpenAIOSLinux:   {OwnerAccountID: account.ID, OSFamily: OpenAIOSLinux, Credentials: map[string]any{"access_token": "linux-token"}, AuthorizationGeneration: "linux-generation", Revision: 3, StateGeneration: "linux-state", CredentialEpoch: "linux-epoch", Status: OpenAIOAuthAuthorizationAuthorized},
	}}
	return account, repo
}

func TestOpenAIOAuthOSCredentialsSelectOnlyRequestedOrDefaultSlot(t *testing.T) {
	account, repo := oauthOSCredentialFixture(t)
	linux, err := ResolveOpenAIOAuthCredentialAccount(context.Background(), repo, account, OpenAIOSLinux)
	require.NoError(t, err)
	profile, err := ResolveOpenAIOAuthOSProfile(context.Background(), repo, linux, "")
	require.NoError(t, err)
	require.Equal(t, OpenAIOSLinux, profile.OSFamily)
	_, err = ResolveOpenAIOAuthOSProfile(context.Background(), repo, linux, OpenAIOSWindows)
	require.ErrorIs(t, err, ErrOpenAIOAuthOSAuthorizationChanged)
	require.Equal(t, "linux-token", linux.GetCredential("access_token"))
	require.Equal(t, account.ID, linux.ID)
	unknown, err := ResolveOpenAIOAuthCredentialAccount(context.Background(), repo, account, "")
	require.NoError(t, err)
	require.Equal(t, "windows-token", unknown.GetCredential("access_token"))
	_, err = ResolveOpenAIOAuthCredentialAccount(context.Background(), repo, account, OpenAIOSMacOS)
	require.ErrorIs(t, err, ErrOpenAIOAuthOSUnauthorized)
	linux.Credentials["model_mapping"].(map[string]any)["gpt"] = "changed"
	require.Equal(t, "model", account.Credentials["model_mapping"].(map[string]any)["gpt"])
	require.Equal(t, "default-only", account.GetCredential("access_token"))
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

func TestOpenAIOAuthOSCredentialsSparkPreservesBusinessIdentity(t *testing.T) {
	owner, repo := oauthOSCredentialFixture(t)
	shadow := &Account{ID: 20, Platform: PlatformOpenAI, Type: AccountTypeOAuth, ParentAccountID: &owner.ID, Concurrency: 7}
	repo.accounts[shadow.ID] = shadow
	scoped, err := ResolveOpenAIOAuthCredentialAccount(context.Background(), repo, shadow, OpenAIOSLinux)
	require.NoError(t, err)
	require.Equal(t, shadow.ID, scoped.ID)
	require.Equal(t, 7, scoped.Concurrency)
	require.Equal(t, owner.ID, scoped.OpenAIOAuthCredentialOwnerID)
	require.Equal(t, "linux-token", scoped.GetCredential("access_token"))
}

func TestOpenAIOAuthOSCredentialsNoReaderNoSecretJSONAndCooldown(t *testing.T) {
	account, repo := oauthOSCredentialFixture(t)
	_, err := ResolveOpenAIOAuthCredentialAccount(context.Background(), nil, account, OpenAIOSWindows)
	require.ErrorIs(t, err, ErrOpenAIOAuthOSUnauthorized)
	encoded, err := json.Marshal(repo.slots[OpenAIOSWindows])
	require.NoError(t, err)
	require.JSONEq(t, "{}", string(encoded))
	scoped, err := ResolveOpenAIOAuthCredentialAccount(context.Background(), repo, account, OpenAIOSLinux)
	require.NoError(t, err)
	encoded, err = json.Marshal(scoped)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "linux-generation")
	require.NotContains(t, string(encoded), "OpenAIOAuthCredential")
	until := time.Now().Add(time.Minute)
	repo.slots[OpenAIOSWindows].RefreshRetryAfter = &until
	_, err = ResolveOpenAIOAuthCredentialAccount(context.Background(), repo, account, OpenAIOSWindows)
	require.ErrorIs(t, err, ErrOpenAIOAuthOSUnauthorized)
	_, err = ResolveOpenAIOAuthCredentialAccount(context.Background(), repo, account, OpenAIOSLinux)
	require.NoError(t, err)
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
	require.Equal(t, "default-only", account.GetCredential("access_token"))
	require.Equal(t, "linux-token", repo.slots[OpenAIOSLinux].Credentials["access_token"])
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
