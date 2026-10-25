package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/imroc/req/v3"
	"github.com/stretchr/testify/require"
)

type accountProxyResolutionRepo struct {
	ProxyRepository
	proxy *Proxy
	err   error
	calls int
}

func (r *accountProxyResolutionRepo) GetByID(context.Context, int64) (*Proxy, error) {
	r.calls++
	return r.proxy, r.err
}

type accountProxyResolutionAccountRepo struct {
	AccountRepository
	account *Account
}

func (r *accountProxyResolutionAccountRepo) GetByID(context.Context, int64) (*Account, error) {
	return r.account, nil
}

func TestResolveAccountProxyURLFailsClosed(t *testing.T) {
	id := int64(42)
	valid := &Proxy{ID: id, Protocol: "http", Host: "proxy.example", Port: 8080, Username: "user", Password: "secret"}
	for _, tc := range []struct {
		name   string
		repo   ProxyRepository
		loaded *Proxy
	}{
		{name: "missing repository"},
		{name: "lookup error", repo: &accountProxyResolutionRepo{err: errors.New("database error with secret")}},
		{name: "missing record", repo: &accountProxyResolutionRepo{}},
		{name: "wrong record", repo: &accountProxyResolutionRepo{proxy: &Proxy{ID: id + 1, Protocol: "http", Host: "proxy.example", Port: 8080}}},
		{name: "wrong loaded record", loaded: &Proxy{ID: id + 1, Protocol: "http", Host: "proxy.example", Port: 8080}},
		{name: "empty loaded host", loaded: &Proxy{ID: id, Protocol: "http", Port: 8080}},
		{name: "invalid loaded port", loaded: &Proxy{ID: id, Protocol: "http", Host: "proxy.example", Port: 0}},
		{name: "invalid loaded scheme", loaded: &Proxy{ID: id, Protocol: "unknown", Host: "proxy.example", Port: 8080}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			value, err := ResolveAccountProxyURL(context.Background(), tc.repo, &Account{ProxyID: &id, Proxy: tc.loaded})
			require.Empty(t, value)
			require.ErrorIs(t, err, ErrAccountProxyUnavailable)
			require.NotContains(t, err.Error(), "secret")
			require.False(t, isNonRetryableRefreshError(err))
		})
	}

	t.Run("unbound account remains direct", func(t *testing.T) {
		repo := &accountProxyResolutionRepo{proxy: valid}
		value, err := ResolveAccountProxyURL(context.Background(), repo, &Account{Proxy: valid})
		require.NoError(t, err)
		require.Empty(t, value)
		require.Zero(t, repo.calls)
	})
	t.Run("loaded relation avoids lookup", func(t *testing.T) {
		repo := &accountProxyResolutionRepo{err: errors.New("unused")}
		value, err := ResolveAccountProxyURL(context.Background(), repo, &Account{ProxyID: &id, Proxy: valid})
		require.NoError(t, err)
		require.Equal(t, valid.URL(), value)
		require.Zero(t, repo.calls)
	})
	t.Run("resolve missing relation", func(t *testing.T) {
		repo := &accountProxyResolutionRepo{proxy: valid}
		value, err := ResolveAccountProxyURL(context.Background(), repo, &Account{ProxyID: &id})
		require.NoError(t, err)
		require.Equal(t, valid.URL(), value)
		require.Equal(t, 1, repo.calls)
	})
	t.Run("canceled lookup", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		repo := &accountProxyResolutionRepo{proxy: valid}
		value, err := ResolveConfiguredProxyURL(ctx, repo, &id)
		require.ErrorIs(t, err, context.Canceled)
		require.Empty(t, value)
		require.Zero(t, repo.calls)
	})
}

func TestOAuthAuxiliaryRequestsRejectUnavailableProxy(t *testing.T) {
	// Clients and persistence dependencies are deliberately nil. Reaching an
	// outbound call or updating the account would fail the test immediately.
	id := int64(42)
	ctx := context.Background()
	for _, tc := range []struct {
		name     string
		platform string
		call     func(*Account) error
	}{
		{name: "OpenAI refresh", platform: PlatformOpenAI, call: func(a *Account) error {
			_, err := (&OpenAIOAuthService{}).RefreshAccountToken(ctx, a)
			return err
		}},
		{name: "Claude refresh", platform: PlatformAnthropic, call: func(a *Account) error {
			_, err := (&OAuthService{}).RefreshAccountToken(ctx, a)
			return err
		}},
		{name: "Gemini refresh", platform: PlatformGemini, call: func(a *Account) error {
			_, err := (&GeminiOAuthService{}).RefreshAccountToken(ctx, a)
			return err
		}},
		{name: "Antigravity refresh", platform: PlatformAntigravity, call: func(a *Account) error {
			_, err := (&AntigravityOAuthService{}).RefreshAccountToken(ctx, a)
			return err
		}},
		{name: "Antigravity project", platform: PlatformAntigravity, call: func(a *Account) error {
			_, err := (&AntigravityOAuthService{}).FillProjectID(ctx, a, "access")
			return err
		}},
		{name: "Antigravity quota", platform: PlatformAntigravity, call: func(a *Account) error {
			_, err := (&AntigravityQuotaFetcher{}).GetProxyURL(ctx, a)
			return err
		}},
		{name: "Claude usage", platform: PlatformAnthropic, call: func(a *Account) error {
			_, err := (&AccountUsageService{}).fetchOAuthUsageRaw(ctx, a)
			return err
		}},
		{name: "Gemini project", platform: PlatformGemini, call: func(a *Account) error {
			provider := &GeminiTokenProvider{geminiOAuthService: &GeminiOAuthService{}}
			_, err := provider.GetAccessToken(ctx, a)
			return err
		}},
		{name: "Gemini Drive quota", platform: PlatformGemini, call: func(a *Account) error {
			a.Credentials["oauth_type"] = "google_one"
			_, _, _, err := (&GeminiOAuthService{}).RefreshAccountGoogleOneTier(ctx, a)
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &Account{ID: 7, Platform: tc.platform, Type: AccountTypeOAuth, Status: StatusActive, ProxyID: &id,
				Credentials: map[string]any{"access_token": "access", "refresh_token": "refresh", "auto_detect_project_id": "true", "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339)},
			}
			require.ErrorIs(t, tc.call(a), ErrAccountProxyUnavailable)
			require.Equal(t, StatusActive, a.Status)
			require.Equal(t, "access", a.GetCredential("access_token"))
			require.Equal(t, "refresh", a.GetCredential("refresh_token"))
			require.Empty(t, a.GetCredential("project_id"))
		})
	}
}

func TestOpenAIQuotaRejectsUnavailableConfiguredProxy(t *testing.T) {
	id := int64(42)
	account := &Account{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeOAuth, ProxyID: &id,
		Credentials: map[string]any{"auth_mode": OpenAIAuthModeAgentIdentity, "chatgpt_account_id": "test-org"},
	}
	for _, repo := range []ProxyRepository{nil, &accountProxyResolutionRepo{err: errors.New("database unavailable")}, &accountProxyResolutionRepo{}} {
		svc := &OpenAIQuotaService{accountRepo: &accountProxyResolutionAccountRepo{account: account}, proxyRepo: repo,
			privacyClientFactory: func(string) (*req.Client, error) {
				t.Fatal("quota HTTP client must not be created for unavailable proxy")
				return nil, nil
			},
		}
		call, err := svc.prepareUpstreamCall(context.Background(), account.ID)
		require.Nil(t, call)
		require.ErrorIs(t, err, ErrAccountProxyUnavailable)
	}
}

func TestPrivacyRequestsSkipUnavailableConfiguredProxy(t *testing.T) {
	id := int64(42)
	factory := func(string) (*req.Client, error) {
		t.Fatal("privacy HTTP client must not be created for unavailable proxy")
		return nil, nil
	}
	admin := &adminServiceImpl{privacyClientFactory: factory}
	refresh := &TokenRefreshService{privacyClientFactory: factory}
	account := &Account{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeOAuth, ProxyID: &id,
		Credentials: map[string]any{"access_token": "access"},
	}
	require.Empty(t, admin.EnsureOpenAIPrivacy(context.Background(), account))
	require.Empty(t, admin.ForceOpenAIPrivacy(context.Background(), account))
	refresh.ensureOpenAIPrivacy(context.Background(), account)
	account.Platform = PlatformAntigravity
	require.Empty(t, admin.EnsureAntigravityPrivacy(context.Background(), account))
	require.Empty(t, admin.ForceAntigravityPrivacy(context.Background(), account))
	refresh.ensureAntigravityPrivacy(context.Background(), account)
	require.Empty(t, account.Extra)
}

func TestProxyResolutionFailureDoesNotTripProviderCircuit(t *testing.T) {
	state := &tokenRefreshProviderState{service: &TokenRefreshService{cfg: &config.TokenRefreshConfig{}}}
	for range 20 {
		state.recordResult(ErrAccountProxyUnavailable)
	}
	require.Zero(t, state.consecutiveFailures)
	require.False(t, state.tripped)
}
