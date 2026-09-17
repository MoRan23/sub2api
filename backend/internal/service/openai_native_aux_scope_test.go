package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/codexnative"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/imroc/req/v3"
	"github.com/stretchr/testify/require"
)

type nativeAuthScopeRecorder struct {
	scope   codexnative.Scope
	enabled bool
}

func (r *nativeAuthScopeRecorder) ExchangeCode(context.Context, string, string, string, string, string) (*openai.TokenResponse, error) {
	return &openai.TokenResponse{AccessToken: "test", ExpiresIn: 3600}, nil
}

func (r *nativeAuthScopeRecorder) RefreshToken(ctx context.Context, token, proxy string) (*openai.TokenResponse, error) {
	return r.RefreshTokenWithClientID(ctx, token, proxy, "")
}

func (r *nativeAuthScopeRecorder) RefreshTokenWithClientID(ctx context.Context, _, _, _ string) (*openai.TokenResponse, error) {
	r.scope, r.enabled = codexnative.ScopeFromContext(ctx)
	return &openai.TokenResponse{AccessToken: "test", ExpiresIn: 3600}, nil
}

func TestNativeAuxRefreshAndPrivacyRetainOwnerScope(t *testing.T) {
	recorder := &nativeAuthScopeRecorder{}
	service := NewOpenAIOAuthService(nil, recorder)
	defer service.Stop()
	var scopes []codexnative.Scope
	var userAgents []string
	service.SetPrivacyClientFactory(func(string) (*req.Client, error) {
		client := req.C().ImpersonateFirefox()
		client.GetClient().Transport = req.HttpRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			scope, enabled := codexnative.ScopeFromContext(request.Context())
			require.True(t, enabled)
			scopes = append(scopes, scope)
			userAgents = append(userAgents, request.Header.Get("User-Agent"))
			body := `{}`
			if strings.Contains(request.URL.Path, "accounts/check") {
				body = `{"accounts":{"owner":{"account":{"account_id":"owner","plan_type":"plus"}}}}`
			}
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
		})
		return client, nil
	})
	account := &Account{ID: 741, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{
		"refresh_token": "test", "user_agent": "codex-tui/0.154.0 (Windows 10.0.26100; x86_64)",
	}}
	ctx := codexnative.WithScope(context.Background(), codexnative.Scope{SourceUserAgent: "codex-tui/0.154.0 (Ubuntu 24.04.3; x86_64)"})
	_, err := service.RefreshAccountToken(ctx, account)
	require.NoError(t, err)
	require.True(t, recorder.enabled)
	require.Equal(t, account.ID, recorder.scope.AccountID)
	require.NotEmpty(t, scopes)
	for index, scope := range scopes {
		require.Equal(t, recorder.scope, scope)
		require.Contains(t, userAgents[index], "Firefox/", "keep the existing privacy application UA")
		require.Equal(t, codexnative.MacOS, codexnative.Resolve(userAgents[index], scope).Platform, "final Firefox UA wins over owner/source hints")
	}
}

func TestNativeAuxBareRefreshUsesAuthenticationScope(t *testing.T) {
	recorder := &nativeAuthScopeRecorder{}
	service := NewOpenAIOAuthService(nil, recorder)
	defer service.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := service.RefreshToken(ctx, "test", "")
	require.NoError(t, err)
	require.True(t, recorder.enabled)
	require.Zero(t, recorder.scope.AccountID)
	require.Equal(t, "auth", recorder.scope.Purpose)
	require.Equal(t, CodexCanonicalUserAgent(), recorder.scope.CanonicalUserAgent)
}
