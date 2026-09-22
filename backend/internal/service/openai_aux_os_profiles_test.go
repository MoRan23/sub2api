package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type auxOAuthUnavailableProfileRepository struct {
	AccountRepository
	incompleteProfile bool
}

func (r auxOAuthUnavailableProfileRepository) GetByID(ctx context.Context, id int64) (*Account, error) {
	account, err := r.AccountRepository.GetByID(ctx, id)
	if err != nil || account == nil || !r.incompleteProfile {
		return account, err
	}
	copy := *account
	copy.OpenAIOAuthOSProfiles = CloneOpenAIOAuthOSProfiles(account.OpenAIOAuthOSProfiles)
	if copy.OpenAIOAuthOSProfiles != nil {
		delete(copy.OpenAIOAuthOSProfiles.Profiles, OpenAIOSLinux)
	}
	return &copy, nil
}

func (r auxOAuthUnavailableProfileRepository) GetOpenAIOAuthOSCredential(ctx context.Context, id int64, os string) (*OpenAIOAuthOSCredential, error) {
	if reader, ok := r.AccountRepository.(OpenAIOAuthOSCredentialsReader); ok {
		return reader.GetOpenAIOAuthOSCredential(ctx, id, os)
	}
	return nil, ErrOpenAIOAuthOSUnauthorized
}

func (r auxOAuthUnavailableProfileRepository) ListOpenAIOAuthOSCredentials(ctx context.Context, id int64) ([]*OpenAIOAuthOSCredential, error) {
	if reader, ok := r.AccountRepository.(OpenAIOAuthOSCredentialsReader); ok {
		return reader.ListOpenAIOAuthOSCredentials(ctx, id)
	}
	return nil, ErrOpenAIOAuthOSUnauthorized
}

func (auxOAuthUnavailableProfileRepository) EnsureOpenAIOAuthOSProfiles(context.Context, int64) (*OpenAIOAuthOSProfiles, error) {
	return nil, errors.New("profile store unavailable")
}

func auxOAuthProfileFixture(t *testing.T, account *Account, defaultOS string) *OpenAIOAuthOSProfiles {
	t.Helper()
	profiles, err := BuildOpenAIOAuthOSProfiles(account, &OpenAIOAuthOSProfiles{DefaultOS: defaultOS})
	require.NoError(t, err)
	account.OpenAIOAuthOSProfiles = profiles
	// A stale legacy mirror must not override the persisted default profile.
	account.Credentials["user_agent"] = "codex-tui/0.144.1 (Windows 10.0.26200; x86_64) WindowsTerminal"
	return profiles
}

func TestCodexTurnStateCollectorUsesDefaultOSProfileWithoutTurnIdentity(t *testing.T) {
	account, proxy := codexCollectorTransportFixture()
	profiles := auxOAuthProfileFixture(t, account, OpenAIOSMacOS)
	upstream := &codexCollectorTransportUpstream{}
	do := ProvideCodexTurnStateCollectorHTTPDo(codexCollectorTransportAccounts{account: account}, codexCollectorTransportProxies{proxy: proxy}, upstream)
	const body = `{"model":"gpt-5.4","input":[],"session_id":"collector-session"}`
	request, err := http.NewRequest(http.MethodPost, chatgptCodexURL, strings.NewReader(body))
	require.NoError(t, err)
	request.Header.Set("session_id", "collector-session")
	response, err := do(context.Background(), CodexTurnStateCollectRequest{
		Account: account, Model: "gpt-5.4", ProxyID: proxy.ID, validateModelPolicy: allowCodexCollectorTestModelPolicy,
	}, request)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	want := resolveCodexClientIdentityPlan(CodexClientIdentityNormalize, profiles.Profiles[OpenAIOSMacOS].UserAgent)
	require.Equal(t, want.UserAgent, upstream.request.Header.Get("User-Agent"))
	require.Equal(t, "collector-session", upstream.request.Header.Get("session_id"))
	require.Empty(t, upstream.request.Header.Get("thread_id"))
	require.Empty(t, upstream.request.Header.Get("x-codex-installation-id"))
	forwardedBody, err := io.ReadAll(upstream.request.Body)
	require.NoError(t, err)
	require.Equal(t, body, string(forwardedBody))
}

func TestPluginDirectoryUsesDefaultOSProfileWithoutTurnIdentity(t *testing.T) {
	account := Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive,
		Credentials: map[string]any{"access_token": "local-test-token"}}
	profiles := auxOAuthProfileFixture(t, &account, OpenAIOSLinux)
	gateway := &OpenAIGatewayService{accountRepo: &pluginAccountDirectoryRepository{accounts: []Account{account}}}
	identity, err := gateway.ResolvePluginOutboundIdentity(context.Background(), account.ID)
	require.NoError(t, err)
	want := resolveCodexClientIdentityPlan(CodexClientIdentityNormalize, profiles.Profiles[OpenAIOSLinux].UserAgent)
	require.Equal(t, want.UserAgent, identity.Headers.Get("User-Agent"))
	require.Empty(t, identity.Headers.Get("session_id"))
	require.Empty(t, identity.Headers.Get("thread_id"))
	require.Empty(t, identity.Headers.Get("x-codex-installation-id"))
}

func TestCodexTurnStateCollectorStopsWhenProfileStorageFails(t *testing.T) {
	account, proxy := codexCollectorTransportFixture()
	upstream := &codexCollectorTransportUpstream{}
	repo := auxOAuthUnavailableProfileRepository{AccountRepository: codexCollectorTransportAccounts{account: account}, incompleteProfile: true}
	do := ProvideCodexTurnStateCollectorHTTPDo(repo, codexCollectorTransportProxies{proxy: proxy}, upstream)
	request, err := http.NewRequest(http.MethodPost, chatgptCodexURL, strings.NewReader(`{}`))
	require.NoError(t, err)
	response, err := do(context.Background(), CodexTurnStateCollectRequest{
		Account: account, Model: "gpt-5.4", ProxyID: proxy.ID, validateModelPolicy: allowCodexCollectorTestModelPolicy,
	}, request)
	require.ErrorIs(t, err, ErrOpenAIOAuthOSProfileUnavailable)
	require.Nil(t, response)
	require.Zero(t, upstream.calls)
}

func TestPluginDirectoryStopsWhenProfileStorageFails(t *testing.T) {
	repo := auxOAuthUnavailableProfileRepository{AccountRepository: &pluginAccountDirectoryRepository{accounts: []Account{
		{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Credentials: map[string]any{"access_token": "local-test-token"}},
	}}}
	gateway := &OpenAIGatewayService{accountRepo: repo}
	identity, err := gateway.ResolvePluginOutboundIdentity(context.Background(), 1)
	require.ErrorIs(t, err, ErrOpenAIOAuthOSProfileUnavailable)
	require.Nil(t, identity)
}

type auxOAuthDailyOSRepository struct {
	OAuthDailySessionRepository
	pool      OAuthDailySessionPool
	ownerID   int64
	defaultOS string
	received  time.Time
}

func (r *auxOAuthDailyOSRepository) GetOrCreateOAuthDailySessionPoolForOS(_ context.Context, ownerID int64, defaultOS string, at time.Time) (OAuthDailySessionPool, error) {
	r.ownerID, r.defaultOS, r.received = ownerID, defaultOS, at
	return r.pool, nil
}

func TestAccountTestRootUsesSelectedOSAndFrozenReceiveTime(t *testing.T) {
	owner := Account{ID: 17, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{}}
	profiles := auxOAuthProfileFixture(t, &owner, OpenAIOSMacOS)
	shadow := &Account{ID: 18, Platform: PlatformOpenAI, Type: AccountTypeOAuth, ParentAccountID: &owner.ID}
	repo := newAuthorizedOpenAIOAuthTestRepo(&owner)
	selectedRoot := "018f5c3c-6e3a-7abf-8def-1234567890ae"
	daily := &auxOAuthDailyOSRepository{pool: OAuthDailySessionPool{OSRoots: map[string]OAuthDailyOSRoots{
		OpenAIOSLinux: {SyncSessionID: selectedRoot},
		OpenAIOSMacOS: {SyncSessionID: "018f5c3c-6e3a-7abe-8def-1234567890ad"},
	}}}
	received := time.Date(2026, 9, 11, 15, 59, 59, 0, time.UTC)
	svc := &AccountTestService{accountRepo: repo, oauthDailySessionRepo: daily,
		settingService: NewSettingService(&dailyRotationSettingRepo{values: map[string]string{
			SettingKeyEnableOpenAIOAuthDailySessionRotation: "true",
		}}, nil)}
	plan := &OpenAIOAuthIdentityPlan{OSFamily: OpenAIOSLinux, ReceivedAt: received}
	ctx := ContextWithOpenAIRequestOS(context.Background(), OpenAIRequestOS{Family: OpenAIOSLinux})
	require.NoError(t, svc.applyOAuthAccountTestRootSession(ctx, shadow, plan))
	require.Equal(t, owner.ID, daily.ownerID)
	require.Equal(t, profiles.DefaultOS, daily.defaultOS)
	require.Equal(t, received, daily.received)
	require.Equal(t, selectedRoot, plan.TurnIdentity.SessionID)
	require.Equal(t, selectedRoot, plan.TurnIdentity.ThreadID)
	require.Empty(t, plan.TurnIdentity.ParentThreadID)
	require.Empty(t, plan.TurnIdentity.ForkedFromThreadID)
}

func TestAccountTestRootUsesStableProfileWhenDailyRotationDisabled(t *testing.T) {
	account := &Account{ID: 17, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{}}
	profiles := auxOAuthProfileFixture(t, account, OpenAIOSMacOS)
	svc := &AccountTestService{}
	for _, osFamily := range []string{"", OpenAIOSLinux} {
		plan := &OpenAIOAuthIdentityPlan{OSFamily: osFamily}
		require.NoError(t, svc.applyOAuthAccountTestRootSession(context.Background(), account, plan))
		if osFamily == "" {
			osFamily = profiles.DefaultOS
		}
		require.Equal(t, profiles.Profiles[osFamily].SyncSessionID, plan.TurnIdentity.SessionID)
		require.Equal(t, plan.TurnIdentity.SessionID, plan.TurnIdentity.ThreadID)
	}
}
