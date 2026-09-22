package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openaicookies"
	"github.com/stretchr/testify/require"
)

func pluginCookieBundleAccount() *Account {
	return &Account{ID: 31, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 1,
		Credentials:                  map[string]any{"access_token": "synthetic"},
		OpenAIOAuthCredentialOwnerID: 31, OpenAIOAuthCredentialOS: OpenAIOSWindows, OpenAIOAuthAuthorizationGeneration: "shared-grant"}
}

func pluginCookieBundleFixture(now time.Time) openaicookies.Bundle {
	entry := openaicookies.Entry{Key: openaicookies.CookieKey("__oailb", "chatgpt.com", "/"), Name: "__oailb", Value: "ticket-route", Domain: "chatgpt.com", Path: "/", HostOnly: true, Secure: true, ExpiresAt: now.Add(time.Minute), CreatedAt: now, UpdatedAt: now}
	return openaicookies.Bundle{Entries: []openaicookies.Entry{entry}, ExpiresAt: entry.ExpiresAt}
}

func pluginCookieBundleResponse() *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Set-Cookie": {"__cflb=response-session; Secure; Path=/"}}, Body: io.NopCloser(strings.NewReader(`{"status":"completed"}`))}
}

func TestOpenAIPluginCookieBundleHandledAtPhysicalRPCBoundary(t *testing.T) {
	now := time.Now()
	account := pluginCookieBundleAccount()
	client := &pluginTurnStateMergeClient{frames: pluginTurnStateMergeFrames(t, pluginCookieBundleResponse())}
	upstream := &pluginRoutingHTTPUpstream{}
	gateway := &OpenAIGatewayService{pluginManager: pluginTurnStateMergeManager(client), httpUpstream: upstream}
	ctx, attempt := openaicookies.WithAttempt(openaicookies.WithBundle(context.Background(), pluginCookieBundleFixture(now)))
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, chatgptCodexURL, bytes.NewReader([]byte(`{"model":"gpt-test"}`)))
	require.NoError(t, err)
	request.Header.Set("Authorization", "Bearer synthetic")
	request.Header.Set(openAICodexTurnStateHeader, "matching-ticket")
	request.Header.Set("Cookie", "caller=must-not-enter-bundle")
	response, err := gateway.doOpenAIUpstream(request, "", account)
	require.NoError(t, err)
	_, err = io.Copy(io.Discard, response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	client.mu.Lock()
	headers := headersFromPlugin(client.start.Headers)
	client.mu.Unlock()
	require.Equal(t, "matching-ticket", headers.Get(openAICodexTurnStateHeader))
	require.Equal(t, "__oailb=ticket-route", headers.Get("Cookie"))
	require.Zero(t, upstream.doCalls)
	bundle, err := attempt.Snapshot(now.Add(openaicookies.BundleLifetime))
	require.NoError(t, err)
	require.Len(t, bundle.Entries, 2, "the candidate contains the exact sent base plus response cookies")
	require.Equal(t, "caller=must-not-enter-bundle", request.Header.Get("Cookie"), "plugin preparation cannot mutate the fallback request")
}

func TestOpenAIPluginCookieBundleDisabledPreservesFilteredHeaders(t *testing.T) {
	account := pluginCookieBundleAccount()
	client := &pluginTurnStateMergeClient{frames: pluginTurnStateMergeFrames(t, pluginCookieBundleResponse())}
	gateway := &OpenAIGatewayService{pluginManager: pluginTurnStateMergeManager(client), httpUpstream: &pluginRoutingHTTPUpstream{}}
	ctx, attempt := openaicookies.WithAttempt(openaicookies.Bypass(openaicookies.WithBundle(context.Background(), pluginCookieBundleFixture(time.Now()))))
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, chatgptCodexURL, nil)
	require.NoError(t, err)
	request.Header.Set(openAICodexTurnStateHeader, "original-ticket")
	response, err := gateway.doOpenAIUpstream(request, "", account)
	require.NoError(t, err)
	_, err = io.Copy(io.Discard, response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	client.mu.Lock()
	headers := headersFromPlugin(client.start.Headers)
	client.mu.Unlock()
	require.Equal(t, "original-ticket", headers.Get(openAICodexTurnStateHeader))
	require.Empty(t, headers.Get("Cookie"), "disabled mode cannot create a cookie absent from the gateway-filtered request")
	_, err = attempt.Snapshot(time.Now().Add(openaicookies.BundleLifetime))
	require.ErrorIs(t, err, openaicookies.ErrNoSnapshot)
}

func TestOpenAIPluginCookieBundleDisableGuardRestoresBeforeRPC(t *testing.T) {
	account := pluginCookieBundleAccount()
	client := &pluginTurnStateMergeClient{frames: pluginTurnStateMergeFrames(t, pluginCookieBundleResponse())}
	gateway := &OpenAIGatewayService{pluginManager: pluginTurnStateMergeManager(client), httpUpstream: &pluginRoutingHTTPUpstream{}}
	ctx := openaicookies.WithBundle(context.Background(), pluginCookieBundleFixture(time.Now()))
	guardCalls := 0
	ctx = openaicookies.WithSendGuard(ctx, func(request *http.Request) bool {
		guardCalls++
		request.Header.Set(openAICodexTurnStateHeader, "original-ticket")
		return false
	})
	ctx, attempt := openaicookies.WithAttempt(ctx)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, chatgptCodexURL, nil)
	require.NoError(t, err)
	request.Header.Set(openAICodexTurnStateHeader, "stale-cached-ticket")
	request.Header.Set("Authorization", "Bearer synthetic")
	response, err := gateway.doOpenAIUpstream(request, "", account)
	require.NoError(t, err)
	_, err = io.Copy(io.Discard, response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	client.mu.Lock()
	headers := headersFromPlugin(client.start.Headers)
	client.mu.Unlock()
	require.Equal(t, 1, guardCalls)
	require.Equal(t, "original-ticket", headers.Get(openAICodexTurnStateHeader))
	require.Equal(t, "Bearer synthetic", headers.Get("Authorization"))
	require.Empty(t, headers.Get("Cookie"))
	_, err = attempt.Snapshot(time.Now().Add(openaicookies.BundleLifetime))
	require.ErrorIs(t, err, openaicookies.ErrNoSnapshot)
}

type pluginCookieBundleFallbackUpstream struct {
	HTTPUpstream
	calls int
	t     *testing.T
}

func (u *pluginCookieBundleFallbackUpstream) Do(request *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.calls++
	require.Empty(u.t, request.Header.Get("Cookie"), "unhandled plugin preparation must not alter fallback request")
	return openaicookies.NewManager().Wrap(openAIPluginRoundTripFunc(func(outbound *http.Request) (*http.Response, error) {
		require.Equal(u.t, "__oailb=ticket-route", outbound.Header.Get("Cookie"))
		return pluginCookieBundleResponse(), nil
	})).RoundTrip(request)
}

func TestOpenAIPluginCookieBundleUnhandledFallsBackWithoutDoubleCapture(t *testing.T) {
	account := pluginCookieBundleAccount()
	upstream := &pluginCookieBundleFallbackUpstream{t: t}
	gateway := &OpenAIGatewayService{pluginManager: &PluginManager{}, httpUpstream: upstream}
	ctx := openaicookies.WithBundle(context.Background(), pluginCookieBundleFixture(time.Now()))
	guards, staged := 0, 0
	ctx = openaicookies.WithSendGuard(ctx, func(*http.Request) bool { guards++; return true })
	ctx = openaicookies.WithObserver(ctx, func(diagnostic openaicookies.Diagnostic) {
		if diagnostic.Reason == "cookie_staged" {
			staged++
		}
	})
	ctx, attempt := openaicookies.WithAttempt(ctx)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, chatgptCodexURL, nil)
	require.NoError(t, err)
	request.Header.Set(openAICodexTurnStateHeader, "cached-ticket")
	response, err := gateway.doOpenAIUpstream(request, "", account)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	require.Equal(t, 1, upstream.calls)
	require.Equal(t, 1, guards, "only the actual fallback send checks authority")
	require.Equal(t, 1, staged, "a plugin that did not send cannot contribute a candidate")
	bundle, err := attempt.Snapshot(time.Now().Add(openaicookies.BundleLifetime))
	require.NoError(t, err)
	require.Len(t, bundle.Entries, 2)
}
