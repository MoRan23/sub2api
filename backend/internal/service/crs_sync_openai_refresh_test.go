//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/stretchr/testify/require"
)

type crsOpenAIRefreshRepo struct {
	AccountRepository
	OpenAIOAuthOSCredentialsRepository
	stored       *Account
	shadow       *Account
	slots        map[string]*OpenAIOAuthOSCredential
	boundOS      []string
	patchErr     error
	patchCalls   int
	genericCalls int
}

func (r *crsOpenAIRefreshRepo) Create(_ context.Context, account *Account) error {
	account.ID = 7
	r.stored = snapshotOAuthRefreshAccount(account)
	return r.ensureDefaultSlot()
}

func (r *crsOpenAIRefreshRepo) Update(_ context.Context, account *Account) error {
	if account.IsCredentialShadow() {
		r.shadow = snapshotOAuthRefreshAccount(account)
	} else {
		r.stored = snapshotOAuthRefreshAccount(account)
	}
	return nil
}

func (r *crsOpenAIRefreshRepo) GetByCRSAccountID(context.Context, string) (*Account, error) {
	if err := r.ensureDefaultSlot(); err != nil {
		return nil, err
	}
	return snapshotOAuthRefreshAccount(r.stored), nil
}

func (r *crsOpenAIRefreshRepo) GetByID(context.Context, int64) (*Account, error) {
	if err := r.ensureDefaultSlot(); err != nil {
		return nil, err
	}
	return snapshotOAuthRefreshAccount(r.stored), nil
}

func (r *crsOpenAIRefreshRepo) ensureDefaultSlot() error {
	if r.stored == nil || r.slots != nil {
		return nil
	}
	profiles, err := BuildOpenAIOAuthOSProfiles(r.stored, r.stored.OpenAIOAuthOSProfiles)
	if err != nil {
		return err
	}
	ApplyOpenAIOAuthOSProfiles(r.stored, profiles)
	os := profiles.DefaultOS
	r.slots = map[string]*OpenAIOAuthOSCredential{os: {
		OwnerAccountID: r.stored.ID, OSFamily: os, Credentials: OpenAIOAuthProviderCredentials(r.stored.Credentials),
		AuthorizationGeneration: "crs-default-generation", Revision: 1, Status: OpenAIOAuthAuthorizationAuthorized,
	}}
	return nil
}

func (r *crsOpenAIRefreshRepo) GetOpenAIOAuthOSCredential(_ context.Context, _ int64, os string) (*OpenAIOAuthOSCredential, error) {
	if err := r.ensureDefaultSlot(); err != nil {
		return nil, err
	}
	return r.slots[os], nil
}

func (r *crsOpenAIRefreshRepo) ListOpenAIOAuthOSCredentials(_ context.Context, _ int64) ([]*OpenAIOAuthOSCredential, error) {
	if err := r.ensureDefaultSlot(); err != nil {
		return nil, err
	}
	slots := make([]*OpenAIOAuthOSCredential, 0, len(r.slots))
	for _, slot := range r.slots {
		slots = append(slots, slot)
	}
	return slots, nil
}

func (r *crsOpenAIRefreshRepo) BindOpenAIOAuthOSCredentialsIfGeneration(_ context.Context, id int64, os, generation string, credentials map[string]any, _ string) (*OpenAIOAuthOSCredential, error) {
	current := r.slots[os]
	if current == nil || current.AuthorizationGeneration != generation {
		return nil, ErrOpenAIOAuthOSAuthorizationChanged
	}
	if credentialString(credentials, "chatgpt_account_id") != "workspace" || credentialString(credentials, "chatgpt_user_id") != "user" {
		return nil, ErrOpenAIOAuthOSSubjectMismatch
	}
	slot := &OpenAIOAuthOSCredential{OwnerAccountID: id, OSFamily: os, Credentials: maps.Clone(credentials), AuthorizationGeneration: generation + "-new", Revision: 1, Status: OpenAIOAuthAuthorizationAuthorized}
	r.slots[os] = slot
	r.boundOS = append(r.boundOS, os)
	if r.stored.OpenAIOAuthOSProfiles.DefaultOS == os {
		r.stored.Credentials = PreserveOpenAIOAuthProviderCredentials(credentials, r.stored.Credentials)
	}
	return slot, nil
}

func (r *crsOpenAIRefreshRepo) PatchOpenAIOAuthOSCredentialsIfUnchanged(_ context.Context, id int64, os, generation string, revision int64, proxyID *int64, patch map[string]any, removed []string) (bool, error) {
	r.patchCalls++
	if r.patchErr != nil {
		return false, r.patchErr
	}
	slot := r.slots[os]
	if slot == nil || slot.OwnerAccountID != id || slot.AuthorizationGeneration != generation || slot.Revision != revision || !reflect.DeepEqual(proxyID, r.stored.ProxyID) {
		return false, nil
	}
	for key, value := range patch {
		slot.Credentials[key] = value
	}
	for _, key := range removed {
		delete(slot.Credentials, key)
	}
	slot.Revision++
	if r.stored.OpenAIOAuthOSProfiles.DefaultOS == os {
		r.stored.Credentials = PreserveOpenAIOAuthProviderCredentials(slot.Credentials, r.stored.Credentials)
	}
	return true, nil
}

func (r *crsOpenAIRefreshRepo) ListShadowsByParent(context.Context, int64) ([]*Account, error) {
	if r.shadow == nil {
		return nil, nil
	}
	return []*Account{snapshotOAuthRefreshAccount(r.shadow)}, nil
}

func (r *crsOpenAIRefreshRepo) UpdateCredentials(context.Context, int64, map[string]any) error {
	r.genericCalls++
	return errors.New("CRS OpenAI refresh must not replace the full credential document")
}

func (r *crsOpenAIRefreshRepo) PatchOpenAIOAuthCredentialsIfUnchanged(_ context.Context, id int64, expected map[string]any, proxyID *int64, patch map[string]any, removed []string) (bool, error) {
	r.patchCalls++
	if r.patchErr != nil {
		return false, r.patchErr
	}
	return applyOpenAIRefreshTestPatch(r.stored, id, expected, proxyID, patch, removed), nil
}

type crsOpenAIRefreshClient struct {
	OpenAIOAuthClient
	onRefresh  func()
	err        error
	response   *openai.TokenResponse
	tokens     []string
	userAgents []string
}

func (c *crsOpenAIRefreshClient) RefreshTokenWithClientID(ctx context.Context, token string, _ string, _ string) (*openai.TokenResponse, error) {
	c.tokens = append(c.tokens, token)
	ua, _ := OpenAIOAuthAuthIdentity(ctx)
	c.userAgents = append(c.userAgents, ua)
	if c.onRefresh != nil {
		c.onRefresh()
	}
	if c.err != nil {
		return nil, c.err
	}
	if c.response != nil {
		return c.response, nil
	}
	return &openai.TokenResponse{AccessToken: "refreshed-access", RefreshToken: "refreshed-token", ExpiresIn: 3600}, nil
}

func TestCRSSyncOpenAIRefreshPreservesConcurrentModelRestrictions(t *testing.T) {
	for _, update := range []bool{false, true} {
		name := "created"
		if update {
			name = "updated"
		}
		t.Run(name, func(t *testing.T) {
			repo := &crsOpenAIRefreshRepo{}
			if update {
				repo.stored = openAIRefreshMappingAccount()
				repo.stored.Extra = map[string]any{"crs_account_id": "crs-openai-1"}
			}
			client := &crsOpenAIRefreshClient{onRefresh: func() { changeOpenAIRefreshMapping(repo.stored) }}
			result := runCRSOpenAIRefreshSync(t, repo, client)
			require.Len(t, result.Items, 1)
			require.Equal(t, name, result.Items[0].Action)
			require.Equal(t, 1, repo.patchCalls)
			require.Zero(t, repo.genericCalls)
			require.Equal(t, "refreshed-access", repo.stored.GetCredential("access_token"))
			require.NotZero(t, repo.stored.Credentials["_token_version"])
			require.False(t, repo.stored.IsModelSupported("gpt-6-astra"))
			require.True(t, repo.stored.IsModelSupported("gpt-5.6-sol"))
			require.Equal(t, float64(20), repo.stored.Credentials["quota_limit"])
			require.False(t, repo.stored.Schedulable)
		})
	}
}

func TestCRSSyncOpenAIRefreshUsesDurableProxyAndKeepsImportSuccess(t *testing.T) {
	for _, failure := range []string{"reauthorized", "refresh", "persist"} {
		t.Run(failure, func(t *testing.T) {
			repo := &crsOpenAIRefreshRepo{stored: openAIRefreshMappingAccount()}
			repo.stored.Extra = map[string]any{"crs_account_id": "crs-openai-1"}
			repo.shadow = &Account{ID: 70, Platform: PlatformOpenAI, Type: AccountTypeOAuth, ParentAccountID: new(int64(7))}
			client := &crsOpenAIRefreshClient{onRefresh: func() {
				repo.stored.ProxyID = new(int64(99))
				changeOpenAIRefreshMapping(repo.stored)
				if failure == "reauthorized" {
					repo.stored.Credentials["access_token"] = "reauthorized-access"
					slot := repo.slots[repo.stored.OpenAIOAuthOSProfiles.DefaultOS]
					slot.Credentials["access_token"] = "reauthorized-access"
					slot.Revision++
				}
			}}
			if failure == "refresh" {
				client.err = errors.New("refresh unavailable")
			}
			if failure == "persist" {
				repo.patchErr = errors.New("database unavailable")
			}
			result := runCRSOpenAIRefreshSync(t, repo, client)
			require.Equal(t, "updated", result.Items[0].Action)
			require.Equal(t, 1, result.Updated)
			require.Zero(t, result.Failed)
			require.Zero(t, repo.genericCalls)
			require.False(t, repo.stored.IsModelSupported("gpt-6-astra"))
			if failure == "persist" {
				require.Nil(t, repo.shadow.ProxyID, "unavailable durable state must not propagate the old proxy")
			} else {
				require.Equal(t, new(int64(99)), repo.shadow.ProxyID)
			}
			if failure == "reauthorized" {
				require.Equal(t, "reauthorized-access", repo.stored.GetCredential("access_token"))
			}
		})
	}
}

func TestCRSSyncOpenAIImportAuthorizesOnlyPersistedDefaultOS(t *testing.T) {
	repo := &crsOpenAIRefreshRepo{stored: openAIRefreshMappingAccount()}
	profiles, err := BuildOpenAIOAuthOSProfiles(repo.stored, &OpenAIOAuthOSProfiles{DefaultOS: OpenAIOSLinux})
	require.NoError(t, err)
	ApplyOpenAIOAuthOSProfiles(repo.stored, profiles)
	require.NoError(t, repo.ensureDefaultSlot())
	repo.slots[OpenAIOSWindows] = &OpenAIOAuthOSCredential{OwnerAccountID: repo.stored.ID, OSFamily: OpenAIOSWindows, Credentials: map[string]any{"access_token": "windows-access", "refresh_token": "windows-refresh"}, AuthorizationGeneration: "windows-generation", Status: OpenAIOAuthAuthorizationAuthorized}
	client := &crsOpenAIRefreshClient{response: &openai.TokenResponse{AccessToken: "verified-access", RefreshToken: "verified-refresh", ExpiresIn: 3600, IDToken: authorizationTestIDToken("workspace", "user")}}
	result := runCRSOpenAIRefreshSyncCredentials(t, repo, client, map[string]any{
		"access_token": "untrusted-access", "refresh_token": "imported-refresh", "chatgpt_account_id": "untrusted-workspace",
		"user_agent": profiles.Profiles[OpenAIOSMacOS].UserAgent, "quota_limit": float64(42),
	})
	require.Equal(t, "updated", result.Items[0].Action, result.Items[0].Error)
	require.Equal(t, []string{OpenAIOSLinux}, repo.boundOS)
	require.Equal(t, []string{"imported-refresh"}, client.tokens, "authorization already produced a fresh token")
	require.Equal(t, profiles.Profiles[OpenAIOSLinux].UserAgent, client.userAgents[0])
	require.Equal(t, "verified-access", repo.slots[OpenAIOSLinux].Credentials["access_token"])
	require.Equal(t, "workspace", repo.slots[OpenAIOSLinux].Credentials["chatgpt_account_id"])
	require.Equal(t, "windows-refresh", repo.slots[OpenAIOSWindows].Credentials["refresh_token"])
	require.Equal(t, "verified-access", repo.stored.GetOpenAIAccessToken())
	require.Equal(t, float64(42), repo.stored.Credentials["quota_limit"])
}

func TestCRSSyncOpenAIImportRejectsChangedCredentialsWithoutRefreshToken(t *testing.T) {
	repo := &crsOpenAIRefreshRepo{stored: openAIRefreshMappingAccount()}
	client := &crsOpenAIRefreshClient{}
	result := runCRSOpenAIRefreshSyncCredentials(t, repo, client, map[string]any{"access_token": "different-access", "quota_limit": float64(42)})
	require.Equal(t, "failed", result.Items[0].Action)
	require.Contains(t, result.Items[0].Error, "require an imported refresh token")
	require.Empty(t, client.tokens)
	require.Empty(t, repo.boundOS)
	require.Equal(t, "old-access", repo.stored.GetOpenAIAccessToken())
	require.Equal(t, float64(100), repo.stored.Credentials["quota_limit"])
}

func TestCRSSyncOpenAIImportPreservesUnchangedCredentialsWithoutRefreshToken(t *testing.T) {
	repo := &crsOpenAIRefreshRepo{stored: openAIRefreshMappingAccount()}
	client := &crsOpenAIRefreshClient{err: errors.New("refresh unavailable")}
	result := runCRSOpenAIRefreshSyncCredentials(t, repo, client, map[string]any{"access_token": "old-access", "quota_limit": float64(42)})
	require.Equal(t, "updated", result.Items[0].Action, result.Items[0].Error)
	require.Empty(t, repo.boundOS)
	require.Equal(t, "old-refresh", repo.stored.GetOpenAIRefreshToken())
	require.Equal(t, "old-access", repo.stored.GetOpenAIAccessToken())
	require.Equal(t, float64(42), repo.stored.Credentials["quota_limit"])
}

func TestCRSSyncOpenAIImportRejectsDifferentUpstreamSubject(t *testing.T) {
	repo := &crsOpenAIRefreshRepo{stored: openAIRefreshMappingAccount()}
	client := &crsOpenAIRefreshClient{response: &openai.TokenResponse{AccessToken: "other-access", RefreshToken: "other-refresh", ExpiresIn: 3600, IDToken: authorizationTestIDToken("other-workspace", "user")}}
	result := runCRSOpenAIRefreshSyncCredentials(t, repo, client, map[string]any{"access_token": "imported-access", "refresh_token": "imported-refresh", "quota_limit": float64(42)})
	require.Equal(t, "failed", result.Items[0].Action)
	require.Empty(t, repo.boundOS)
	require.Equal(t, "old-refresh", repo.stored.GetOpenAIRefreshToken())
	require.Equal(t, float64(100), repo.stored.Credentials["quota_limit"])
}

func TestCRSSyncOpenAIRefreshUsesDefaultSlotAndPreservesConcurrentDefaultChange(t *testing.T) {
	repo := &crsOpenAIRefreshRepo{stored: openAIRefreshMappingAccount()}
	profiles, err := BuildOpenAIOAuthOSProfiles(repo.stored, &OpenAIOAuthOSProfiles{DefaultOS: OpenAIOSLinux})
	require.NoError(t, err)
	ApplyOpenAIOAuthOSProfiles(repo.stored, profiles)
	require.NoError(t, repo.ensureDefaultSlot())
	repo.slots[OpenAIOSLinux].Credentials["refresh_token"] = "private-linux-refresh"
	repo.slots[OpenAIOSWindows] = &OpenAIOAuthOSCredential{OwnerAccountID: repo.stored.ID, OSFamily: OpenAIOSWindows, Credentials: map[string]any{"access_token": "windows-access", "refresh_token": "windows-refresh"}, AuthorizationGeneration: "windows-generation", Status: OpenAIOAuthAuthorizationAuthorized}
	client := &crsOpenAIRefreshClient{onRefresh: func() {
		repo.stored.OpenAIOAuthOSProfiles.DefaultOS = OpenAIOSWindows
		repo.stored.Credentials = PreserveOpenAIOAuthProviderCredentials(repo.slots[OpenAIOSWindows].Credentials, repo.stored.Credentials)
	}}
	oauth := NewOpenAIOAuthService(nil, client)
	oauth.SetAccountRepository(repo)
	t.Cleanup(oauth.Stop)
	svc := NewCRSSyncService(repo, nil, nil, oauth, nil, nil)
	durable := svc.refreshOpenAIOAuthAfterSync(context.Background(), snapshotOAuthRefreshAccount(repo.stored))
	require.NotNil(t, durable)
	require.Equal(t, []string{"private-linux-refresh"}, client.tokens)
	require.Equal(t, "refreshed-access", repo.slots[OpenAIOSLinux].Credentials["access_token"])
	require.Equal(t, "windows-refresh", repo.slots[OpenAIOSWindows].Credentials["refresh_token"])
	require.Equal(t, OpenAIOSWindows, repo.stored.OpenAIOAuthOSProfiles.DefaultOS)
	require.Equal(t, "windows-access", repo.stored.GetOpenAIAccessToken())
}

func runCRSOpenAIRefreshSync(t *testing.T, repo AccountRepository, oauthClient OpenAIOAuthClient) *SyncFromCRSResult {
	return runCRSOpenAIRefreshSyncCredentials(t, repo, oauthClient, openAIRefreshMappingAccount().Credentials)
}

func runCRSOpenAIRefreshSyncCredentials(t *testing.T, repo AccountRepository, oauthClient OpenAIOAuthClient, credentials map[string]any) *SyncFromCRSResult {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/web/auth/login" {
			_, _ = w.Write([]byte(`{"success":true,"token":"sync-token"}`))
			return
		}
		require.Equal(t, "/admin/sync/export-accounts", r.URL.Path)
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"data": map[string]any{"openaiOAuthAccounts": []any{map[string]any{
				"id": "crs-openai-1", "kind": "openai", "name": "OpenAI sync",
				"isActive": true, "schedulable": true, "credentials": credentials,
			}}},
		}))
	}))
	t.Cleanup(server.Close)
	oauthService := NewOpenAIOAuthService(nil, oauthClient)
	oauthService.SetAccountRepository(repo)
	t.Cleanup(oauthService.Stop)
	cfg := &config.Config{}
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	svc := NewCRSSyncService(repo, nil, nil, oauthService, nil, cfg)
	result, err := svc.SyncFromCRS(context.Background(), SyncFromCRSInput{BaseURL: server.URL, Username: "admin", Password: "password"})
	require.NoError(t, err)
	return result
}
