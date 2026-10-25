//go:build unit

package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type proxyFailureTokenProviderRepo struct {
	accountProxyResolutionAccountRepo
	cooldownCalls int
}

func (r *proxyFailureTokenProviderRepo) SetTempUnschedulable(context.Context, int64, time.Time, string) error {
	r.cooldownCalls++
	return nil
}

type proxyFailureTempUnschedCache struct {
	TempUnschedCache
	writes int
}

func (c *proxyFailureTempUnschedCache) SetTempUnsched(context.Context, int64, *TempUnschedState) error {
	c.writes++
	return nil
}

func TestTokenProvidersNeverUseOldTokenAfterProxyResolutionFailure(t *testing.T) {
	policy := ProviderRefreshPolicy{OnRefreshError: ProviderRefreshErrorUseExistingToken}
	for _, platform := range []string{PlatformAnthropic, PlatformOpenAI, PlatformGemini, PlatformAntigravity, PlatformGrok} {
		t.Run(platform, func(t *testing.T) {
			account := &Account{ID: 72, Platform: platform, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true,
				Credentials: map[string]any{"access_token": "still-valid-old-token", "refresh_token": "refresh-token", "expires_at": time.Now().Add(time.Minute).Format(time.RFC3339)},
			}
			if platform == PlatformOpenAI {
				// Isolate the refresh error policy from the separately tested OS
				// authorization resolver; no authorization lookup is needed here.
				account.Credentials[openAIAuthModeCredentialKey] = OpenAIAuthModeAgentIdentity
			}
			repo := &proxyFailureTokenProviderRepo{accountProxyResolutionAccountRepo: accountProxyResolutionAccountRepo{account: account}}
			api := NewOAuthRefreshAPI(repo, nil)
			executor := &refreshAPIExecutorStub{needsRefresh: true, err: fmt.Errorf("resolve route: %w", ErrAccountProxyUnavailable)}
			var getToken func(context.Context, *Account) (string, error)
			cache := &proxyFailureTempUnschedCache{}
			switch platform {
			case PlatformAnthropic:
				p := NewClaudeTokenProvider(repo, nil, nil)
				p.SetRefreshPolicy(policy)
				p.SetRefreshAPI(api, executor)
				getToken = p.GetAccessToken
			case PlatformOpenAI:
				p := NewOpenAITokenProvider(repo, nil, nil)
				p.SetRefreshPolicy(policy)
				p.SetRefreshAPI(api, executor)
				getToken = p.GetAccessToken
			case PlatformGemini:
				p := NewGeminiTokenProvider(repo, nil, nil)
				p.SetRefreshPolicy(policy)
				p.SetRefreshAPI(api, executor)
				getToken = p.GetAccessToken
			case PlatformAntigravity:
				p := NewAntigravityTokenProvider(repo, nil, nil)
				p.SetRefreshPolicy(policy)
				p.SetRefreshAPI(api, executor)
				p.SetTempUnschedCache(cache)
				getToken = p.GetAccessToken
			case PlatformGrok:
				p := NewGrokTokenProvider(repo, nil)
				p.SetRefreshPolicy(policy)
				p.SetRefreshAPI(api, executor)
				getToken = p.GetAccessToken
			}
			token, err := getToken(context.Background(), account)
			require.ErrorIs(t, err, ErrAccountProxyUnavailable)
			require.Empty(t, token)
			require.Equal(t, 1, executor.refreshCalls)
			require.Zero(t, repo.cooldownCalls)
			require.Zero(t, cache.writes)
			require.Equal(t, "still-valid-old-token", account.GetCredential("access_token"))
			require.Equal(t, StatusActive, account.Status)
		})
	}
}

func TestAntigravityTokenProviderProxyFailureNeverChangesHealth(t *testing.T) {
	id := int64(42)
	for _, action := range []ProviderRefreshErrorAction{ProviderRefreshErrorReturn, ProviderRefreshErrorUseExistingToken} {
		t.Run(fmt.Sprint(action), func(t *testing.T) {
			account := &Account{ID: 73, Platform: PlatformAntigravity, Type: AccountTypeOAuth, Status: StatusActive, ProxyID: &id,
				Credentials: map[string]any{"access_token": "old", "refresh_token": "refresh", "expires_at": time.Now().Add(time.Minute).Format(time.RFC3339)},
			}
			repo := &proxyFailureTokenProviderRepo{accountProxyResolutionAccountRepo: accountProxyResolutionAccountRepo{account: account}}
			cache := &proxyFailureTempUnschedCache{}
			oauth := &AntigravityOAuthService{proxyRepo: &accountProxyResolutionRepo{}}
			provider := NewAntigravityTokenProvider(repo, nil, oauth)
			provider.SetRefreshPolicy(ProviderRefreshPolicy{OnRefreshError: action})
			provider.SetRefreshAPI(NewOAuthRefreshAPI(repo, nil), NewAntigravityTokenRefresher(oauth))
			provider.SetTempUnschedCache(cache)
			token, err := provider.GetAccessToken(context.Background(), account)
			require.ErrorIs(t, err, ErrAccountProxyUnavailable)
			require.Empty(t, token)
			require.Zero(t, repo.cooldownCalls)
			require.Zero(t, cache.writes)
			require.Nil(t, account.TempUnschedulableUntil)
			require.Equal(t, StatusActive, account.Status)
		})
	}
}

func TestAntigravityProjectBackfillProxyFailureDoesNotUseOldTokenOnNextAttempt(t *testing.T) {
	id := int64(42)
	account := &Account{ID: 74, Platform: PlatformAntigravity, Type: AccountTypeOAuth, Status: StatusActive, ProxyID: &id,
		Credentials: map[string]any{"access_token": "old", "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339)},
	}
	repo := &proxyFailureTokenProviderRepo{accountProxyResolutionAccountRepo: accountProxyResolutionAccountRepo{account: account}}
	provider := NewAntigravityTokenProvider(repo, nil, &AntigravityOAuthService{})
	for range 2 {
		token, err := provider.GetAccessToken(context.Background(), account)
		require.ErrorIs(t, err, ErrAccountProxyUnavailable)
		require.Empty(t, token)
		require.True(t, provider.shouldAttemptBackfill(account.ID))
	}
	require.Zero(t, repo.cooldownCalls)
}

func TestGrokManualTestDoesNotSwallowProxyResolutionFailure(t *testing.T) {
	account := &Account{ID: 75, Platform: PlatformGrok, Type: AccountTypeOAuth, Status: StatusActive,
		Credentials: map[string]any{"access_token": "old", "refresh_token": "refresh", "expires_at": time.Now().Add(time.Minute).Format(time.RFC3339)},
	}
	repo := &accountProxyResolutionAccountRepo{account: account}
	provider := NewGrokTokenProvider(repo, nil)
	provider.SetRefreshAPI(NewOAuthRefreshAPI(repo, nil), &refreshAPIExecutorStub{needsRefresh: true, err: ErrAccountProxyUnavailable})
	token, err := provider.GetAccessTokenForManualTest(context.Background(), account)
	require.ErrorIs(t, err, ErrAccountProxyUnavailable)
	require.Empty(t, token)
}
