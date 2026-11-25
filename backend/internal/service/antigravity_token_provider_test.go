//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAntigravityTokenProvider_GetAccessToken_Upstream(t *testing.T) {
	provider := &AntigravityTokenProvider{}

	t.Run("upstream account with valid api_key", func(t *testing.T) {
		account := &Account{
			Platform: PlatformAntigravity,
			Type:     AccountTypeUpstream,
			Credentials: map[string]any{
				"api_key": "sk-test-key-12345",
			},
		}
		token, err := provider.GetAccessToken(context.Background(), account)
		require.NoError(t, err)
		require.Equal(t, "sk-test-key-12345", token)
	})

	t.Run("upstream account missing api_key", func(t *testing.T) {
		account := &Account{
			Platform:    PlatformAntigravity,
			Type:        AccountTypeUpstream,
			Credentials: map[string]any{},
		}
		token, err := provider.GetAccessToken(context.Background(), account)
		require.Error(t, err)
		require.Contains(t, err.Error(), "upstream account missing api_key")
		require.Empty(t, token)
	})

	t.Run("upstream account with empty api_key", func(t *testing.T) {
		account := &Account{
			Platform: PlatformAntigravity,
			Type:     AccountTypeUpstream,
			Credentials: map[string]any{
				"api_key": "",
			},
		}
		token, err := provider.GetAccessToken(context.Background(), account)
		require.Error(t, err)
		require.Contains(t, err.Error(), "upstream account missing api_key")
		require.Empty(t, token)
	})

	t.Run("upstream account with nil credentials", func(t *testing.T) {
		account := &Account{
			Platform: PlatformAntigravity,
			Type:     AccountTypeUpstream,
		}
		token, err := provider.GetAccessToken(context.Background(), account)
		require.Error(t, err)
		require.Contains(t, err.Error(), "upstream account missing api_key")
		require.Empty(t, token)
	})
}

func TestAntigravityTokenProvider_GetAccessToken_Guards(t *testing.T) {
	provider := &AntigravityTokenProvider{}

	t.Run("nil account", func(t *testing.T) {
		token, err := provider.GetAccessToken(context.Background(), nil)
		require.Error(t, err)
		require.Contains(t, err.Error(), "account is nil")
		require.Empty(t, token)
	})

	t.Run("non-antigravity platform", func(t *testing.T) {
		account := &Account{
			Platform: PlatformAnthropic,
			Type:     AccountTypeOAuth,
		}
		token, err := provider.GetAccessToken(context.Background(), account)
		require.Error(t, err)
		require.Contains(t, err.Error(), "not an antigravity account")
		require.Empty(t, token)
	})

	t.Run("unsupported account type", func(t *testing.T) {
		account := &Account{
			Platform: PlatformAntigravity,
			Type:     AccountTypeAPIKey,
		}
		token, err := provider.GetAccessToken(context.Background(), account)
		require.Error(t, err)
		require.Contains(t, err.Error(), "not an antigravity oauth account")
		require.Empty(t, token)
	})
}

func TestAntigravityTokenProvider_RefreshFailureOnlyCoolsUpstreamRejections(t *testing.T) {
	for _, tt := range []struct {
		name     string
		err      error
		cooldown int
	}{
		{"network", fmt.Errorf("refresh request: %w", &net.DNSError{Name: "oauth.example", Err: "no such host"}), 0},
		{"timeout", context.DeadlineExceeded, 0},
		{"canceled", context.Canceled, 0},
		{"marked transport", MarkUpstreamTransportFailure(errors.New("proxy connection failed")), 0},
		{"HTTP 503", errors.New("token 刷新失败 (HTTP 503): dial tcp: connection refused"), 1},
		{"invalid grant", errors.New("invalid_grant: revoked refresh token"), 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			account := &Account{ID: 21, Platform: PlatformAntigravity, Type: AccountTypeOAuth, Status: StatusActive,
				Credentials: map[string]any{"access_token": "old-token", "refresh_token": "refresh-token", "expires_at": time.Now().Add(-time.Minute).Unix()}}
			repo := &tokenRefreshAccountRepo{}
			cache := &tempUnschedCacheStub{}
			provider := NewAntigravityTokenProvider(repo, nil, nil)
			provider.SetTempUnschedCache(cache)
			executor := &refreshAPIExecutorStub{needsRefresh: true, err: tt.err}
			apiRepo := &refreshAPIAccountRepo{account: account}
			provider.SetRefreshAPI(NewOAuthRefreshAPI(apiRepo, nil), executor)

			_, err := provider.GetAccessToken(context.Background(), account)

			require.ErrorIs(t, err, tt.err)
			require.Equal(t, 1, executor.refreshCalls)
			require.Equal(t, tt.cooldown, repo.setTempUnschedCalls)
			require.Equal(t, tt.cooldown, cache.setCalls)
			require.Zero(t, repo.setErrorCalls+repo.clearTempCalls+cache.deleteCalls)
		})
	}
}
