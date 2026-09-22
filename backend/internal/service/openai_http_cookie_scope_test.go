package service

import (
	"context"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/codexnative"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openaicookies"
	"github.com/stretchr/testify/require"
)

func TestOpenAICookieScopeUsesFrozenAuthorization(t *testing.T) {
	owner := &Account{ID: 71, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		OpenAIOAuthCredentialOwnerID: 71, OpenAIOAuthCredentialOS: OpenAIOSLinux,
		OpenAIOAuthAuthorizationGeneration: "linux-grant"}
	ctx := WithOpenAINativeHTTPScope(context.Background(), owner, "Windows")
	scope, ok := openaicookies.ScopeFromContext(ctx)
	require.True(t, ok)
	require.Equal(t, openaicookies.Scope{OwnerAccountID: 71, OSFamily: "linux", AuthorizationGeneration: "linux-grant"}, scope)
	shadow := *owner
	shadow.ID = 72
	shadow.ParentAccountID = &owner.ID
	shadowCtx := WithOpenAINativeHTTPScope(context.Background(), &shadow, "Darwin")
	shadowScope, ok := openaicookies.ScopeFromContext(shadowCtx)
	require.True(t, ok)
	require.Equal(t, scope, shadowScope)
	for _, account := range []*Account{
		{ID: 71, Platform: PlatformOpenAI, Type: AccountTypeOAuth},
		{ID: 71, Platform: PlatformOpenAI, Type: AccountTypeAPIKey},
		{ID: 71, Platform: PlatformAnthropic, Type: AccountTypeOAuth},
	} {
		_, inherited := openaicookies.ScopeFromContext(WithOpenAINativeHTTPScope(ctx, account, "Linux"))
		require.False(t, inherited, "missing credential proof cannot borrow previous account cookies")
	}
	flow := openaicookies.WithScope(context.Background(), openaicookies.Scope{EphemeralID: "authorization-flow"})
	flowScope, ok := openaicookies.ScopeFromContext(WithOpenAINativeHTTPScope(flow, nil, ""))
	require.True(t, ok)
	require.Equal(t, "authorization-flow", flowScope.EphemeralID)
}

func TestOpenAIAPIKeyModelsClearsInheritedCookieScope(t *testing.T) {
	ctx := openaicookies.WithScope(context.Background(), openaicookies.Scope{OwnerAccountID: 71, OSFamily: "linux", AuthorizationGeneration: "previous-oauth-grant"})
	ctx = codexnative.WithScope(ctx, codexnative.Scope{AccountID: 71, Purpose: "models"})
	upstream := &nativeScopeHTTPUpstream{}
	gateway := &OpenAIGatewayService{httpUpstream: upstream}
	_, err := gateway.fetchOpenAIModelsUpstream(ctx, openAIModelsRequest{
		url: "https://chatgpt.com/backend-api/codex/models", headers: make(http.Header),
		accountID: 72, useAPIKeyUpstream: true,
		credentialAccount: &Account{ID: 72, Platform: PlatformOpenAI, Type: AccountTypeAPIKey},
	}, "")
	require.NoError(t, err)
	require.Len(t, upstream.requests, 1)
	_, inherited := openaicookies.ScopeFromContext(upstream.requests[0].Context())
	require.False(t, inherited)
	_, native := codexnative.ScopeFromContext(upstream.requests[0].Context())
	require.False(t, native)
}
