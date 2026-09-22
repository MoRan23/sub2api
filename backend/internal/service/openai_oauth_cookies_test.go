package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openaicookies"
	"github.com/imroc/req/v3"
	"github.com/stretchr/testify/require"
)

type cookieFlowOAuthClient func(context.Context) (*openai.TokenResponse, error)

func (f cookieFlowOAuthClient) ExchangeCode(ctx context.Context, _, _, _, _, _ string) (*openai.TokenResponse, error) {
	return f(ctx)
}
func (f cookieFlowOAuthClient) RefreshToken(ctx context.Context, _, _ string) (*openai.TokenResponse, error) {
	return f(ctx)
}
func (f cookieFlowOAuthClient) RefreshTokenWithClientID(ctx context.Context, _, _, _ string) (*openai.TokenResponse, error) {
	return f(ctx)
}

func TestOpenAIOAuthCookiesOneEphemeralScopeSpansEnrichmentAndEnds(t *testing.T) {
	manager := openaicookies.NewManager(nil)
	var tokenScopes []openaicookies.Scope
	svc := NewOpenAIOAuthService(nil, cookieFlowOAuthClient(func(ctx context.Context) (*openai.TokenResponse, error) {
		scope, ok := openaicookies.ScopeFromContext(ctx)
		require.True(t, ok)
		require.True(t, scope.Valid())
		require.NotEmpty(t, scope.EphemeralID)
		tokenScopes = append(tokenScopes, scope)
		return &openai.TokenResponse{AccessToken: "test-at", RefreshToken: "test-rt", ExpiresIn: 3600, IDToken: authorizationTestIDToken("workspace", "user")}, nil
	}))
	t.Cleanup(svc.Stop)
	svc.SetCookieManager(manager)
	var requests []*http.Request
	client := req.C()
	client.GetClient().Transport = manager.Wrap(req.HttpRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests = append(requests, request.Clone(request.Context()))
		header := http.Header{"Content-Type": {"application/json"}}
		if strings.Contains(request.URL.Path, "accounts/check") {
			header.Set("Set-Cookie", "__cf_bm=flow-cookie; Secure; Path=/")
		}
		return &http.Response{StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(strings.NewReader(`{}`)), Request: request}, nil
	}))
	svc.SetPrivacyClientFactory(func(string) (*req.Client, error) { return client, nil })
	prior := openaicookies.WithScope(context.Background(), openaicookies.Scope{OwnerAccountID: 42, OSFamily: "windows", AuthorizationGeneration: "old-grant"})
	for range 2 {
		start := len(requests)
		info, err := svc.ExchangeCode(prior, authorizationTestInput(t, svc, 0, OpenAIOSLinux))
		require.NoError(t, err)
		require.Equal(t, PrivacyModeTrainingOff, info.PrivacyMode)
		current := tokenScopes[len(tokenScopes)-1]
		flowRequests := requests[start:]
		require.GreaterOrEqual(t, len(flowRequests), 3)
		for index, request := range flowRequests {
			scope, ok := openaicookies.ScopeFromContext(request.Context())
			require.True(t, ok)
			require.Equal(t, current, scope)
			if index == 0 {
				require.Empty(t, request.Header.Get("Cookie"))
			} else {
				require.Equal(t, "__cf_bm=flow-cookie", request.Header.Get("Cookie"))
			}
		}
		probe, err := http.NewRequestWithContext(openaicookies.WithScope(context.Background(), current), http.MethodGet, "https://chatgpt.com/probe", nil)
		require.NoError(t, err)
		response, err := client.GetClient().Transport.RoundTrip(probe)
		require.NoError(t, err)
		require.NoError(t, response.Body.Close())
		require.Empty(t, requests[len(requests)-1].Header.Get("Cookie"), "completed flow must discard its temporary cookies")
	}
	require.NotEqual(t, tokenScopes[0].EphemeralID, tokenScopes[1].EphemeralID)
}

func TestOpenAIOAuthCookiesNewAuthorizationReplacesOldGrantAndCleansUpOnError(t *testing.T) {
	for _, flow := range []string{"exchange", "reauthorize", "import", "unbound-refresh"} {
		t.Run(flow, func(t *testing.T) {
			svc, _, _ := authorizationTestSetup(t)
			manager := openaicookies.NewManager(nil)
			svc.SetCookieManager(manager)
			var usedScope openaicookies.Scope
			var cookies []string
			transport := manager.Wrap(req.HttpRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				cookies = append(cookies, request.Header.Get("Cookie"))
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Set-Cookie": {"__cf_bm=temporary; Secure; Path=/"}}, Body: io.NopCloser(strings.NewReader(`{}`)), Request: request}, nil
			}))
			svc.oauthClient = cookieFlowOAuthClient(func(ctx context.Context) (*openai.TokenResponse, error) {
				usedScope, _ = openaicookies.ScopeFromContext(ctx)
				require.True(t, usedScope.Valid())
				require.NotEmpty(t, usedScope.EphemeralID)
				request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://chatgpt.com/test", nil)
				require.NoError(t, err)
				response, err := transport.RoundTrip(request)
				require.NoError(t, err)
				require.NoError(t, response.Body.Close())
				return nil, errors.New("synthetic token failure")
			})
			ctx := openaicookies.WithScope(context.Background(), openaicookies.Scope{OwnerAccountID: 42, OSFamily: "windows", AuthorizationGeneration: "old-grant"})
			var err error
			switch flow {
			case "exchange":
				_, err = svc.ExchangeCode(ctx, authorizationTestInput(t, svc, 42, OpenAIOSWindows))
			case "reauthorize":
				_, err = svc.AuthorizeAccountWithRefreshToken(ctx, 42, OpenAIOSLinux, "new-rt", "")
			case "import":
				_, err = svc.RefreshTokenForOS(ctx, "new-rt", "", "", OpenAIOSLinux)
			case "unbound-refresh":
				_, err = svc.RefreshToken(context.Background(), "new-rt", "")
			}
			require.ErrorContains(t, err, "synthetic token failure")
			probe, err := http.NewRequestWithContext(openaicookies.WithScope(context.Background(), usedScope), http.MethodGet, "https://chatgpt.com/probe", nil)
			require.NoError(t, err)
			response, err := transport.RoundTrip(probe)
			require.NoError(t, err)
			require.NoError(t, response.Body.Close())
			require.Equal(t, []string{"", ""}, cookies)
		})
	}
}

func TestOpenAIOAuthCookiesBoundRefreshRetainsCredentialScope(t *testing.T) {
	svc, _, repo := authorizationTestSetup(t)
	var actual openaicookies.Scope
	svc.oauthClient = cookieFlowOAuthClient(func(ctx context.Context) (*openai.TokenResponse, error) {
		actual, _ = openaicookies.ScopeFromContext(ctx)
		return &openai.TokenResponse{AccessToken: "new-at", ExpiresIn: 3600}, nil
	})
	_, err := svc.RefreshAccountToken(context.Background(), repo.account)
	require.NoError(t, err)
	require.Equal(t, openaicookies.Scope{OwnerAccountID: 42, OSFamily: OpenAIOSWindows, AuthorizationGeneration: "windows-generation"}, actual)
}

func TestOpenAIOAuthCookiesConcurrentFlowsShareTransportWithoutSharingCookies(t *testing.T) {
	manager := openaicookies.NewManager(nil)
	svc := NewOpenAIOAuthService(nil, cookieFlowOAuthClient(func(context.Context) (*openai.TokenResponse, error) {
		return &openai.TokenResponse{AccessToken: "test-at", ExpiresIn: 3600, IDToken: authorizationTestIDToken("workspace", "user")}, nil
	}))
	t.Cleanup(svc.Stop)
	svc.SetCookieManager(manager)
	var mu sync.Mutex
	counts := make(map[string]int)
	client := req.C()
	client.GetClient().Transport = manager.Wrap(req.HttpRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		scope, ok := openaicookies.ScopeFromContext(request.Context())
		if !ok || scope.EphemeralID == "" {
			t.Error("request lost its temporary flow")
			return nil, errors.New("request lost its temporary flow")
		}
		mu.Lock()
		count := counts[scope.EphemeralID]
		counts[scope.EphemeralID]++
		mu.Unlock()
		expected := ""
		if count > 0 {
			expected = "__cf_bm=" + scope.EphemeralID
		}
		if got := request.Header.Get("Cookie"); got != expected {
			t.Errorf("flow received cookies %q, expected %q", got, expected)
			return nil, fmt.Errorf("flow received cookies %q, expected %q", got, expected)
		}
		header := http.Header{"Content-Type": {"application/json"}, "Set-Cookie": {"__cf_bm=" + scope.EphemeralID + "; Secure; Path=/"}}
		return &http.Response{StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(strings.NewReader(`{}`)), Request: request}, nil
	}))
	svc.SetPrivacyClientFactory(func(string) (*req.Client, error) { return client, nil })
	const flows = 12
	errorsFound := make(chan error, flows)
	var wg sync.WaitGroup
	for range flows {
		wg.Add(1)
		go func() {
			defer wg.Done()
			info, err := svc.RefreshToken(context.Background(), "test-rt", "")
			if err == nil && info.PrivacyMode != PrivacyModeTrainingOff {
				err = errors.New("privacy request failed within its flow")
			}
			errorsFound <- err
		}()
	}
	wg.Wait()
	close(errorsFound)
	for err := range errorsFound {
		require.NoError(t, err)
	}
	require.Len(t, counts, flows)
	for _, count := range counts {
		require.Equal(t, 3, count)
	}
}
