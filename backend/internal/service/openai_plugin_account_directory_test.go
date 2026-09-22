package service

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"testing"

	"github.com/stretchr/testify/require"
)

type pluginAccountDirectoryRepository struct {
	AccountRepository
	accounts []Account
	err      error
	lists    int
}

func (r *pluginAccountDirectoryRepository) ListByPlatform(context.Context, string) ([]Account, error) {
	r.lists++
	return r.accounts, r.err
}

func (r *pluginAccountDirectoryRepository) GetByID(_ context.Context, id int64) (*Account, error) {
	if r.err != nil {
		return nil, r.err
	}
	for i := range r.accounts {
		if r.accounts[i].ID == id {
			return &r.accounts[i], nil
		}
	}
	return nil, nil
}

func (r *pluginAccountDirectoryRepository) GetOpenAIOAuthOSCredential(ctx context.Context, id int64, family string) (*OpenAIOAuthOSCredential, error) {
	account, err := r.GetByID(ctx, id)
	if err != nil || account == nil || account.OpenAIOAuthOSProfiles == nil || !OpenAIOAuthOSAuthorizationAvailable(account, "") {
		return nil, err
	}
	if _, exists := account.OpenAIOAuthOSProfiles.Profiles[family]; !exists {
		return nil, nil
	}
	return &OpenAIOAuthOSCredential{
		OwnerAccountID: account.ID, OSFamily: family, Credentials: maps.Clone(account.Credentials),
		Status: OpenAIOAuthAuthorizationAuthorized, AuthorizationGeneration: fmt.Sprintf("test-shared-generation-%d", account.ID),
		Revision: 1, StateGeneration: fmt.Sprintf("test-state-%d/%s", account.ID, family),
	}, nil
}

func (r *pluginAccountDirectoryRepository) ListOpenAIOAuthOSCredentials(ctx context.Context, id int64) ([]*OpenAIOAuthOSCredential, error) {
	account, err := r.GetByID(ctx, id)
	if err != nil || account == nil || account.OpenAIOAuthOSProfiles == nil {
		return nil, err
	}
	grant, err := r.GetOpenAIOAuthOSCredential(ctx, id, account.OpenAIOAuthOSProfiles.DefaultOS)
	if err != nil || grant == nil {
		return nil, err
	}
	return []*OpenAIOAuthOSCredential{grant}, nil
}

func TestOpenAIPluginAccountDirectoryUsesSameScopeForListAndResolve(t *testing.T) {
	parentID := int64(1)
	repo := &pluginAccountDirectoryRepository{accounts: []Account{
		{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Credentials: map[string]any{"access_token": "local-test-token"}},
		{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: "disabled"},
		{ID: 3, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, ParentAccountID: &parentID},
		{ID: 4, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive},
		{ID: 5, Platform: PlatformOpenAI, Type: AccountTypeSetupToken, Status: StatusActive},
		{ID: 6, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Status: StatusActive},
	}}
	authorizeOpenAIOAuthTestAccount(&repo.accounts[0], OpenAIOSWindows)
	gateway := &OpenAIGatewayService{accountRepo: repo}
	ctx := context.Background()
	for _, filters := range [][2]string{{"", ""}, {" openai ", " oauth "}} {
		ids, err := gateway.ListPluginAccounts(ctx, filters[0], filters[1])
		require.NoError(t, err)
		require.Equal(t, []int64{1}, ids)
	}
	identity, err := gateway.ResolvePluginOutboundIdentity(ctx, 1)
	require.NoError(t, err)
	require.NotNil(t, identity)
	require.Equal(t, "local-test-token", identity.Token)
	for _, id := range []int64{-1, 0, 2, 3, 4, 5, 6, 999} {
		identity, err := gateway.ResolvePluginOutboundIdentity(ctx, id)
		require.NoError(t, err, "out-of-scope accounts must be rejected before token resolution")
		require.Nil(t, identity)
	}
	// A previously enumerated account can be disabled before credential lookup.
	repo.accounts[0].Status = "disabled"
	identity, err = gateway.ResolvePluginOutboundIdentity(ctx, 1)
	require.NoError(t, err)
	require.Nil(t, identity)
	listCalls := repo.lists
	for _, filters := range [][2]string{{"anthropic", "oauth"}, {"openai", "apikey"}, {"openai", "setup-token"}} {
		ids, err := gateway.ListPluginAccounts(ctx, filters[0], filters[1])
		require.NoError(t, err)
		require.Empty(t, ids)
	}
	require.Equal(t, listCalls, repo.lists, "unsupported filters must not enumerate accounts")
}

func TestOpenAIPluginAccountDirectoryPreservesAccountIdentityWithoutTurnState(t *testing.T) {
	codexCanonicalUAMu.RLock()
	oldResolver := codexCanonicalUAResolver
	codexCanonicalUAMu.RUnlock()
	SetCodexCanonicalUserAgentResolver(func() string { return "codex-tui/0.200.1 (Ubuntu 22.4.0; x86_64) xterm-256color (codex-tui; 0.200.1)" })
	t.Cleanup(func() { SetCodexCanonicalUserAgentResolver(oldResolver) })
	proxy := &Proxy{ID: 9, Protocol: "http", Host: "proxy.invalid", Port: 8080}
	repo := &pluginAccountDirectoryRepository{accounts: []Account{
		{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, ProxyID: &proxy.ID, Proxy: proxy,
			Credentials: map[string]any{"access_token": "account-one-token", "chatgpt_account_id": "org-one", "chatgpt_account_is_fedramp": true,
				"user_agent": "codex_cli_rs/0.144.1 (Windows 11.0.26100; x86_64) WindowsTerminal"},
			Extra: map[string]any{"codex_turn_state": map[string]any{"enabled": true}, "codex_turn_state_generation": "test-generation"}},
		{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive,
			Credentials: map[string]any{"access_token": "account-two-token", "chatgpt_account_id": "org-two", "user_agent": "codex-tui/0.144.1 (Mac OS X 15.1.0; arm64) Terminal"}},
	}}
	// No turn-state store, identity resolver, or transport is installed. A passive
	// identity lookup must not require them or allocate any request/turn identity.
	gateway := &OpenAIGatewayService{accountRepo: repo}
	for i := range repo.accounts {
		account := &repo.accounts[i]
		authorizeOpenAIOAuthTestAccount(account, OpenAIOAuthOSFamilies()...)
		identity, err := gateway.ResolvePluginOutboundIdentity(context.Background(), account.ID)
		require.NoError(t, err)
		want := resolveCodexClientIdentityPlan(CodexClientIdentityNormalize, account.GetOpenAIUserAgent())
		require.Equal(t, want.UserAgent, identity.Headers.Get("User-Agent"))
		require.Equal(t, want.Version, identity.Headers.Get("Version"))
		require.Equal(t, want.Originator, identity.Headers.Get("Originator"))
		require.Equal(t, account.GetChatGPTAccountID(), identity.Headers.Get("ChatGPT-Account-Id"))
		require.Empty(t, identity.Headers.Get("x-codex-turn-state"))
		require.Empty(t, identity.Headers.Get("session_id"))
		require.Empty(t, identity.Headers.Get("thread_id"))
		if account.ID == 1 {
			require.Contains(t, identity.Headers.Get("User-Agent"), "Windows")
			require.Equal(t, "true", identity.Headers.Get("x-openai-fedramp"))
			require.Equal(t, proxy.URL(), identity.ProxyURL)
		} else {
			require.Contains(t, identity.Headers.Get("User-Agent"), "Mac OS X")
			require.Empty(t, identity.Headers.Get("x-openai-fedramp"))
			require.Empty(t, identity.ProxyURL)
		}
	}
}

func TestOpenAIPluginAccountDirectoryPropagatesRepositoryFailure(t *testing.T) {
	want := errors.New("directory lookup failed")
	gateway := &OpenAIGatewayService{accountRepo: &pluginAccountDirectoryRepository{err: want}}
	_, err := gateway.ListPluginAccounts(context.Background(), "", "")
	require.ErrorIs(t, err, want)
	_, err = gateway.ResolvePluginOutboundIdentity(context.Background(), 1)
	require.ErrorIs(t, err, want)
}

func TestOpenAIPluginAccountDirectoryUsesSharedAuthorizationForEveryOSIdentity(t *testing.T) {
	account := Account{ID: 71, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive,
		Credentials: map[string]any{"access_token": "windows-access", "chatgpt_account_id": "plugin-owner"}}
	authorizeOpenAIOAuthTestAccount(&account, OpenAIOSWindows, OpenAIOSLinux)
	repo := &pluginAccountDirectoryRepository{accounts: []Account{account}}
	gateway := &OpenAIGatewayService{accountRepo: repo}
	for _, family := range []string{"", OpenAIOSWindows, OpenAIOSLinux, OpenAIOSMacOS} {
		ctx := ContextWithOpenAIRequestOS(context.Background(), OpenAIRequestOS{Family: family, Captured: true})
		ids, err := gateway.ListPluginAccounts(ctx, "", "")
		require.NoError(t, err)
		require.Equal(t, []int64{account.ID}, ids)
		identity, err := gateway.ResolvePluginOutboundIdentity(ctx, account.ID)
		require.NoError(t, err)
		require.Equal(t, "windows-access", identity.Token)
		profile := account.OpenAIOAuthOSProfiles.Profiles[family]
		if family == "" {
			profile = account.OpenAIOAuthOSProfiles.Profiles[OpenAIOSWindows]
		}
		require.Equal(t, resolveCodexClientIdentityPlan(CodexClientIdentityNormalize, profile.UserAgent).UserAgent, identity.Headers.Get("User-Agent"))
	}
	require.Empty(t, repo.accounts[0].OpenAIOAuthCredentialOS, "lookup must not scope the shared account")
	require.Equal(t, "windows-access", repo.accounts[0].GetOpenAIAccessToken())
	repo.accounts[0].OpenAIOAuthOSProfiles.Authorization = &OpenAIOAuthOSAuthorizationSummary{Status: OpenAIOAuthAuthorizationUnauthorized}
	for _, family := range OpenAIOAuthOSFamilies() {
		ctx := ContextWithOpenAIRequestOS(context.Background(), OpenAIRequestOS{Family: family, Captured: true})
		ids, err := gateway.ListPluginAccounts(ctx, "", "")
		require.NoError(t, err)
		require.Empty(t, ids)
		_, err = gateway.ResolvePluginOutboundIdentity(ctx, account.ID)
		require.ErrorIs(t, err, ErrOpenAIOAuthOSUnauthorized)
	}
}
