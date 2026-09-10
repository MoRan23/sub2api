package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func downstreamFacadeContext(apiKeyID int64) *gin.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Set("api_key", &APIKey{ID: apiKeyID})
	return c
}

func TestDownstreamFacadeAccountFailoverPreservesFrozenWindow(t *testing.T) {
	ctx := context.Background()
	svc := &OpenAIGatewayService{cfg: &config.Config{JWT: config.JWTConfig{Secret: t.Name()}}}
	accountA := &Account{ID: 720001, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	accountB := &Account{ID: 720002, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	options := OpenAIOAuthIdentityPlanOptions{TurnIdentityEnabled: true, InstallationPolicy: OpenAIOAuthInstallationPreserve}
	body := []byte(`{"client_metadata":{"session_id":"` + uuid.NewString() + `"},"prompt_cache_key":"explicit-override"}`)
	c := downstreamFacadeContext(720003)
	capture := CaptureOpenAIOAuthIdentity(c, body, "")
	first, err := svc.GetOrResolveOpenAIOAuthOutboundIdentity(ctx, c, accountA, capture, options, nil)
	require.NoError(t, err)
	require.NoError(t, ValidateOpenAICodexWindowSnapshot(first.Window))
	require.True(t, first.TurnIdentityCreated)

	// Another request on B completes compaction while A's request is in flight.
	otherContext := downstreamFacadeContext(720003)
	otherCapture := CaptureOpenAIOAuthIdentity(otherContext, body, "")
	other, err := svc.GetOrResolveOpenAIOAuthOutboundIdentity(ctx, otherContext, accountB, otherCapture, options, nil)
	require.NoError(t, err)
	require.Equal(t, first.TurnIdentity, other.TurnIdentity)
	require.Equal(t, first.Window, other.Window)
	require.Equal(t, first.WindowMappingKey, other.WindowMappingKey)
	digest, err := OpenAICodexCompactTurnDigest(svc.cfg.JWT.Secret, other.TurnIdentityNamespace, other.APIKeyID, other.Window, other.RequestTurn.ID)
	require.NoError(t, err)
	advanced, err := svc.CommitOpenAICodexWindowSnapshot(ctx, other.WindowMappingKey, other.Window, digest)
	require.NoError(t, err)
	require.Equal(t, OpenAICodexWindowCommitAdvanced, advanced.Status)

	for _, account := range []*Account{accountB, accountA} {
		retry, err := svc.GetOrResolveOpenAIOAuthOutboundIdentity(ctx, c, account, capture, options, nil)
		require.NoError(t, err)
		require.Equal(t, first.TurnIdentity, retry.TurnIdentity)
		require.Equal(t, first.Window, retry.Window)
		require.Equal(t, first.WireProfile.WindowID, retry.WireProfile.WindowID)
		require.Equal(t, first.WireProfile.ContextWindowID, retry.WireProfile.ContextWindowID)
		require.Equal(t, openAIOutboundSessionIdentityNamespace(account), retry.CredentialOwnerNamespace)
		require.True(t, IsNewOpenAICodexSession(c, first.TurnIdentity.SessionID))
		if account == accountB {
			require.NotEqual(t, first.PromptCacheKey.Value, retry.PromptCacheKey.Value, "explicit cache overrides retain credential scope")
		}
	}
	// A pinned plan on a new transport context retains the same capture as well.
	pinned, err := svc.GetOrResolveOpenAIOAuthOutboundIdentity(ctx, downstreamFacadeContext(720003), accountB, capture, options, &first)
	require.NoError(t, err)
	require.Equal(t, first.Window, pinned.Window)

	nextContext := downstreamFacadeContext(720003)
	next, err := svc.GetOrResolveOpenAIOAuthOutboundIdentity(ctx, nextContext, accountB, CaptureOpenAIOAuthIdentity(nextContext, body, ""), options, nil)
	require.NoError(t, err)
	require.Equal(t, first.TurnIdentity, next.TurnIdentity)
	require.Equal(t, advanced.Snapshot, next.Window)
	require.False(t, next.TurnIdentityCreated)

	isolated, err := svc.GetOrResolveOpenAIOAuthOutboundIdentity(ctx, downstreamFacadeContext(720004), accountB, capture, options, &first)
	require.NoError(t, err)
	require.NotEqual(t, first.TurnIdentity.SessionID, isolated.TurnIdentity.SessionID)
	require.NotEqual(t, first.WindowMappingKey, isolated.WindowMappingKey)
}

func TestDownstreamFacadeRequiresAuthenticatedBusinessScope(t *testing.T) {
	svc := &OpenAIGatewayService{cfg: &config.Config{JWT: config.JWTConfig{Secret: t.Name()}}}
	account := &Account{ID: 720005, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	c := downstreamFacadeContext(0)
	body := []byte(`{"client_metadata":{"session_id":"public-session","syntheticScope":"synthetic:01989f44-7c00-7000-8000-000000000091"}}`)
	capture := CaptureOpenAIOAuthIdentity(c, body, "")
	_, err := svc.ResolveOpenAIOAuthIdentityPlan(context.Background(), c, account, capture, OpenAIOAuthIdentityPlanOptions{
		TurnIdentityEnabled: true, InstallationPolicy: OpenAIOAuthInstallationPreserve,
	})
	require.ErrorIs(t, err, ErrOpenAICodexDownstreamIdentityScopeMissing)
	_, _, err = svc.resolveOpenAICodexLogicalIdentityForTransport(context.Background(), c, account, capture.Logical, true)
	require.ErrorIs(t, err, ErrOpenAICodexDownstreamIdentityScopeMissing)
	_, err = svc.ResolveOpenAIOAuthProfileIdentityPlan(context.Background(), c, account, OpenAIOAuthInstallationPreserve)
	require.NoError(t, err)
}

func TestDownstreamFacadePublishingMaterializedFramePreservesPlan(t *testing.T) {
	svc := &OpenAIGatewayService{cfg: &config.Config{JWT: config.JWTConfig{Secret: t.Name()}}}
	account := &Account{ID: 720006, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	c := downstreamFacadeContext(720007)
	body := []byte(`{"client_metadata":{"session_id":"` + uuid.NewString() + `"}}`)
	first := CaptureOpenAIOAuthIdentity(c, body, "")
	SetOpenAIOAuthIdentityCapture(c, first)
	second := CaptureOpenAIOAuthIdentity(c, body, "")
	plan, err := svc.GetOrResolveOpenAIOAuthOutboundIdentity(context.Background(), c, account, second, OpenAIOAuthIdentityPlanOptions{
		TurnIdentityEnabled: true, InstallationPolicy: OpenAIOAuthInstallationPreserve,
	}, nil)
	require.NoError(t, err)
	require.False(t, openAIOAuthIdentityCapturesEqual(first, second))
	SetOpenAIOAuthIdentityCapture(c, second)
	preserved, ok := OpenAIOAuthIdentityPlanFromContext(c)
	require.True(t, ok)
	require.Equal(t, plan.Window, preserved.Window)
	require.True(t, openAIOAuthIdentityCapturesEqual(preserved.Capture, second))
	SetOpenAIOAuthIdentityCapture(c, CaptureOpenAIOAuthIdentity(c, body, ""))
	_, ok = OpenAIOAuthIdentityPlanFromContext(c)
	require.False(t, ok, "an unrelated capture must still invalidate the previous plan")
}
