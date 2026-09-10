package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestSyntheticOpenAIOAuthProbeScopesAreIndependentAndRetainedOnAccountFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, seed := range []string{"account-test", "compact-probe", "account-image-test"} {
		t.Run(seed, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/1/test", nil)
			body := []byte(`{"model":"gpt-5.6"}`)
			capture := captureOpenAIOAuthSyntheticRequest(c, body, seed)
			require.Equal(t, "synthetic:"+capture.ContextWindowIDCandidate, capture.syntheticScope)
			require.Equal(t, capture, captureOpenAIOAuthSyntheticRequest(c, body, seed))

			gateway := &OpenAIGatewayService{cfg: &config.Config{JWT: config.JWTConfig{Secret: "synthetic-probe-" + seed}}}
			options := OpenAIOAuthIdentityPlanOptions{
				TurnIdentityEnabled: true,
				ProjectionMode:      OpenAIOAuthIdentityProjectionRegular,
				InstallationPolicy:  OpenAIOAuthInstallationAccountPin,
			}
			firstAccount := &Account{ID: 91301, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{
				openAIPinnedInstallationIDKey: "33333333-4444-4555-8666-777777777777",
			}}
			secondAccount := &Account{ID: 91302, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{
				openAIPinnedInstallationIDKey: "44444444-5555-4666-8777-888888888888",
			}}
			first, err := gateway.GetOrResolveOpenAIOAuthOutboundIdentity(context.Background(), c, firstAccount, capture, options, nil)
			require.NoError(t, err)
			require.Zero(t, first.APIKeyID)
			require.True(t, first.TurnIdentityEnabled)
			require.NotEmpty(t, first.TurnIdentity.SessionID)
			require.NotEmpty(t, first.TurnIdentity.ThreadID)
			require.NotEmpty(t, first.Window.WindowID())
			require.NotEmpty(t, first.Window.ContextWindowID)

			retry, err := gateway.GetOrResolveOpenAIOAuthOutboundIdentity(context.Background(), c, firstAccount, capture, options, nil)
			require.NoError(t, err)
			require.Equal(t, first.TurnIdentity, retry.TurnIdentity)
			require.Equal(t, first.Window, retry.Window)
			failover, err := gateway.GetOrResolveOpenAIOAuthOutboundIdentity(context.Background(), c, secondAccount, capture, options, nil)
			require.NoError(t, err)
			require.Equal(t, first.TurnIdentity, failover.TurnIdentity)
			require.Equal(t, first.Window, failover.Window)
			require.Equal(t, first.WindowMappingKey, failover.WindowMappingKey)
			require.NotEqual(t, first.CredentialOwnerNamespace, failover.CredentialOwnerNamespace)
			require.NotEqual(t, first.InstallationID, failover.InstallationID)

			freshCapture := captureOpenAIOAuthSyntheticRequest(nil, body, seed)
			require.NotEqual(t, capture.syntheticScope, freshCapture.syntheticScope)
			fresh, err := gateway.GetOrResolveOpenAIOAuthOutboundIdentity(context.Background(), nil, firstAccount, freshCapture, options, nil)
			require.NoError(t, err)
			require.NotEqual(t, first.TurnIdentity.SessionID, fresh.TurnIdentity.SessionID)
			require.NotEqual(t, first.TurnIdentity.ThreadID, fresh.TurnIdentity.ThreadID)
			require.NotEqual(t, first.Window.WindowID(), fresh.Window.WindowID())
			require.NotEqual(t, first.Window.ContextWindowID, fresh.Window.ContextWindowID)
		})
	}
}

func TestCaptureSyntheticOpenAIOAuthProbeDoesNotReuseOrdinaryCapture(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/1/test", nil)
	body := []byte(`{"model":"gpt-5.6"}`)
	ordinary := CaptureOpenAIOAuthIdentity(nil, body, "account-test")
	SetOpenAIOAuthIdentityCapture(c, ordinary)

	synthetic := captureOpenAIOAuthSyntheticRequest(c, body, "account-test")
	require.NotEmpty(t, synthetic.syntheticScope)
	require.NotEqual(t, ordinary.ContextWindowIDCandidate, synthetic.ContextWindowIDCandidate)
	require.NotEqual(t, ordinary.RequestTurn.ID, synthetic.RequestTurn.ID)
}

func TestCaptureSyntheticOpenAIOAuthProbeWithoutWindowStillHasIndependentScope(t *testing.T) {
	body := []byte(`{"client_metadata":{"x-codex-turn-metadata":"{\"request_kind\":\"memory\"}"}}`)
	first := captureOpenAIOAuthSyntheticRequest(nil, body, "internal-memory-probe")
	second := captureOpenAIOAuthSyntheticRequest(nil, body, "internal-memory-probe")
	require.Empty(t, first.ContextWindowIDCandidate)
	require.Empty(t, second.ContextWindowIDCandidate)
	require.NotEmpty(t, first.syntheticScope)
	require.NotEqual(t, first.syntheticScope, second.syntheticScope)
}

func TestSyntheticOpenAIOAuthProbeDoesNotInheritSharedLegacyProbeIdentity(t *testing.T) {
	gateway := &OpenAIGatewayService{cfg: &config.Config{JWT: config.JWTConfig{Secret: "synthetic-probe-legacy-isolation"}}}
	account := &Account{ID: 91303, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	body := []byte(`{"model":"gpt-5.6"}`)
	legacyCapture := CaptureOpenAIOAuthIdentity(nil, body, "account-test")
	legacy, enabled, _, err := gateway.resolveOpenAICodexLegacyTurnIdentityWithAliasesDetailed(
		context.Background(), nil, account, legacyCapture.Logical, legacyCapture.Aliases,
	)
	require.NoError(t, err)
	require.True(t, enabled)

	options := OpenAIOAuthIdentityPlanOptions{TurnIdentityEnabled: true}
	first, err := gateway.ResolveOpenAIOAuthIdentityPlan(context.Background(), nil, account,
		captureOpenAIOAuthSyntheticRequest(nil, body, "account-test"), options)
	require.NoError(t, err)
	second, err := gateway.ResolveOpenAIOAuthIdentityPlan(context.Background(), nil, account,
		captureOpenAIOAuthSyntheticRequest(nil, body, "account-test"), options)
	require.NoError(t, err)
	require.NotEqual(t, legacy.SessionID, first.TurnIdentity.SessionID)
	require.NotEqual(t, legacy.SessionID, second.TurnIdentity.SessionID)
	require.NotEqual(t, first.TurnIdentity.SessionID, second.TurnIdentity.SessionID)
}
