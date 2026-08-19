package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func resolvePromptCacheIdentityPlan(
	t *testing.T,
	svc *OpenAIGatewayService,
	account *Account,
	body []byte,
	mode OpenAIOAuthIdentityProjectionMode,
	apiKeyID int64,
) (OpenAIOAuthIdentityCapture, OpenAIOAuthIdentityPlan) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Set("api_key", &APIKey{ID: apiKeyID})
	capture := CaptureOpenAIOAuthIdentity(c, body, "")
	plan, err := svc.ResolveOpenAIOAuthIdentityPlan(context.Background(), c, account, capture, OpenAIOAuthIdentityPlanOptions{
		TurnIdentityEnabled: true,
		ProjectionMode:      mode,
		InstallationPolicy:  OpenAIOAuthInstallationPreserve,
	})
	require.NoError(t, err)
	return capture, plan
}

func TestCaptureOpenAICodexPromptCacheKeySemantics(t *testing.T) {
	parent := "logical-parent"
	tests := []struct {
		name       string
		body       string
		wantKind   OpenAICodexPromptCacheKeyKind
		wantValid  bool
		wantValue  string
		applicable bool
	}{
		{
			name: "missing", body: `{"client_metadata":{"session_id":"root"}}`,
			wantKind: OpenAICodexPromptCacheKeyMissing, applicable: true,
		},
		{
			name: "default", body: `{"client_metadata":{"session_id":"root"},"prompt_cache_key":"root"}`,
			wantKind: OpenAICodexPromptCacheKeyDefault, wantValid: true, wantValue: "root", applicable: true,
		},
		{
			name: "generic override", body: `{"client_metadata":{"session_id":"root"},"prompt_cache_key":"review-scope"}`,
			wantKind: OpenAICodexPromptCacheKeyOverride, wantValid: true, wantValue: "review-scope", applicable: true,
		},
		{
			name: "official guardian review", body: `{"client_metadata":{"x-openai-subagent":"review","x-codex-turn-metadata":"{\"session_id\":\"root\",\"thread_id\":\"child\",\"parent_thread_id\":\"` + parent + `\"}"},"prompt_cache_key":"guardian:` + parent + `"}`,
			wantKind: OpenAICodexPromptCacheKeyGuardian, wantValid: true, wantValue: "guardian:" + parent, applicable: true,
		},
		{
			name: "guardian compatibility spelling", body: `{"client_metadata":{"x-openai-subagent":"guardian","x-codex-turn-metadata":"{\"session_id\":\"root\",\"thread_id\":\"child\",\"parent_thread_id\":\"` + parent + `\"}"},"prompt_cache_key":"guardian:` + parent + `"}`,
			wantKind: OpenAICodexPromptCacheKeyGuardian, wantValid: true, wantValue: "guardian:" + parent, applicable: true,
		},
		{
			name: "review suffix mismatch", body: `{"client_metadata":{"x-openai-subagent":"review","x-codex-turn-metadata":"{\"session_id\":\"root\",\"thread_id\":\"child\",\"parent_thread_id\":\"` + parent + `\"}"},"prompt_cache_key":"guardian:other-parent"}`,
			wantKind: OpenAICodexPromptCacheKeyOverride, wantValid: true, wantValue: "guardian:other-parent", applicable: true,
		},
		{
			name: "review without parent", body: `{"client_metadata":{"session_id":"root","x-openai-subagent":"review"},"prompt_cache_key":"guardian:` + parent + `"}`,
			wantKind: OpenAICodexPromptCacheKeyOverride, wantValid: true, wantValue: "guardian:" + parent, applicable: true,
		},
		{
			name: "guardian shape without guardian signal", body: `{"client_metadata":{"session_id":"root","parent_thread_id":"` + parent + `"},"prompt_cache_key":"guardian:` + parent + `"}`,
			wantKind: OpenAICodexPromptCacheKeyOverride, wantValid: true, wantValue: "guardian:" + parent, applicable: true,
		},
		{
			name: "non string", body: `{"client_metadata":{"session_id":"root"},"prompt_cache_key":7}`,
			wantKind: OpenAICodexPromptCacheKeyInvalid, applicable: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			capture := CaptureOpenAIOAuthIdentity(nil, []byte(tt.body), "")
			require.Equal(t, tt.wantKind, capture.PromptCacheKey.Kind)
			require.Equal(t, tt.wantValid, capture.PromptCacheKey.Valid)
			require.Equal(t, tt.wantValue, capture.PromptCacheKey.Value)
			require.Equal(t, tt.applicable, capture.PromptCacheKey.Applicable)
		})
	}

	alpha := CaptureOpenAIOAuthIdentityForAlphaSearch(nil, []byte(`{"prompt_cache_key":"unsupported-alpha-key"}`), "alpha-id")
	require.Equal(t, "alpha-id", alpha.Logical.SessionKey)
	require.False(t, alpha.PromptCacheKey.Applicable)
	require.Equal(t, OpenAICodexPromptCacheKeyOverride, alpha.PromptCacheKey.Kind)
}

func TestOpenAIOAuthIdentityCaptureEqualityIncludesPromptCacheSnapshot(t *testing.T) {
	first := CaptureOpenAIOAuthIdentity(nil, []byte(`{"client_metadata":{"session_id":"root"},"prompt_cache_key":"override-a"}`), "")
	second := CaptureOpenAIOAuthIdentity(nil, []byte(`{"client_metadata":{"session_id":"root"},"prompt_cache_key":"override-b"}`), "")
	require.Equal(t, first.Logical, second.Logical)
	require.False(t, openAIOAuthIdentityCapturesEqual(first, second))
	require.True(t, openAIOAuthIdentityCapturesEqual(first, cloneOpenAIOAuthIdentityCapture(first)))
}

func TestOpenAICodexPromptCacheOverrideKeyIsScopedHMAC(t *testing.T) {
	first, err := OpenAICodexPromptCacheOverrideKey("secret-a", "account:1", 7, "review-scope")
	require.NoError(t, err)
	require.Len(t, first, 46)
	require.True(t, strings.HasPrefix(first, "pc_"))
	require.NotContains(t, first, "review-scope")

	same, err := OpenAICodexPromptCacheOverrideKey("secret-a", "account:1", 7, "review-scope")
	require.NoError(t, err)
	require.Equal(t, first, same)
	for _, changed := range []struct {
		secret    string
		namespace string
		apiKeyID  int64
		override  string
	}{
		{"secret-b", "account:1", 7, "review-scope"},
		{"secret-a", "account:2", 7, "review-scope"},
		{"secret-a", "account:1", 8, "review-scope"},
		{"secret-a", "account:1", 7, "other-scope"},
	} {
		got, keyErr := OpenAICodexPromptCacheOverrideKey(changed.secret, changed.namespace, changed.apiKeyID, changed.override)
		require.NoError(t, keyErr)
		require.NotEqual(t, first, got)
	}
}

func TestOpenAICodexPromptCacheProjectionDefaultAndCompactContinuity(t *testing.T) {
	svc := &OpenAIGatewayService{cfg: &config.Config{JWT: config.JWTConfig{Secret: "prompt-cache-test-secret"}}}
	account := &Account{ID: 73001, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	body := []byte(`{"model":"gpt-5.6","client_metadata":{"session_id":"logical-root"},"prompt_cache_key":"logical-root"}`)

	_, regular := resolvePromptCacheIdentityPlan(t, svc, account, body, OpenAIOAuthIdentityProjectionRegular, 71)
	require.True(t, regular.PromptCacheKey.Enabled)
	require.Equal(t, regular.TurnIdentity.SessionID, regular.PromptCacheKey.Value)
	regularBody, err := ApplyOpenAIOAuthIdentityPlan(http.Header{}, body, regular)
	require.NoError(t, err)
	require.Equal(t, regular.TurnIdentity.SessionID, gjson.GetBytes(regularBody, "prompt_cache_key").String())

	_, compact := resolvePromptCacheIdentityPlan(t, svc, account, body, OpenAIOAuthIdentityProjectionCompact, 71)
	require.Equal(t, regular.PromptCacheKey.Value, compact.PromptCacheKey.Value)
	compactBody, err := ApplyOpenAIOAuthIdentityPlan(http.Header{}, body, compact)
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(compactBody, "client_metadata").Exists())
	require.Equal(t, regular.TurnIdentity.SessionID, gjson.GetBytes(compactBody, "prompt_cache_key").String())

	// The real legacy compact schema has no client_metadata. Its endpoint turn
	// id remains a lowest-priority alias; the carried prompt key must resolve to
	// the same session/cache identity as the preceding Responses request.
	legacyBody := []byte(`{"model":"gpt-5.6","prompt_cache_key":"logical-root"}`)
	legacyCapture := CaptureOpenAIOAuthIdentityWithEndpointAlias(nil, legacyBody, "compact-turn-id")
	require.Equal(t, OpenAIOutboundSessionLogicalKeySourcePromptCacheKey, legacyCapture.Logical.Source)
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/compact", nil)
	c.Set("api_key", &APIKey{ID: int64(71)})
	legacy, err := svc.ResolveOpenAIOAuthIdentityPlan(context.Background(), c, account, legacyCapture, OpenAIOAuthIdentityPlanOptions{
		TurnIdentityEnabled: true, ProjectionMode: OpenAIOAuthIdentityProjectionCompact,
		InstallationPolicy: OpenAIOAuthInstallationPreserve,
	})
	require.NoError(t, err)
	require.Equal(t, regular.TurnIdentity.SessionID, legacy.TurnIdentity.SessionID)
	require.Equal(t, regular.PromptCacheKey.Value, legacy.PromptCacheKey.Value)
}

func TestOpenAICodexLegacyCompactEndpointAliasPreservesExplicitOverride(t *testing.T) {
	svc := &OpenAIGatewayService{cfg: &config.Config{JWT: config.JWTConfig{Secret: "prompt-cache-legacy-compact-secret"}}}
	account := &Account{ID: 73008, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	body := []byte(`{"model":"gpt-5.6","client_metadata":{"session_id":"logical-root"},"prompt_cache_key":"review-scope"}`)
	capture := CaptureOpenAIOAuthIdentityWithEndpointAlias(nil, body, "legacy-compact-id")
	require.True(t, capture.PromptCacheKey.Applicable)
	require.Equal(t, OpenAICodexPromptCacheKeyOverride, capture.PromptCacheKey.Kind)

	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/compact", nil)
	c.Set("api_key", &APIKey{ID: int64(77)})
	compact, err := svc.ResolveOpenAIOAuthIdentityPlan(context.Background(), c, account, capture, OpenAIOAuthIdentityPlanOptions{
		TurnIdentityEnabled: true, ProjectionMode: OpenAIOAuthIdentityProjectionCompact,
		InstallationPolicy: OpenAIOAuthInstallationPreserve,
	})
	require.NoError(t, err)
	require.True(t, compact.PromptCacheKey.Enabled)
	require.Equal(t, OpenAICodexPromptCacheKeyOverride, compact.PromptCacheKey.Kind)
	require.Len(t, compact.PromptCacheKey.Value, 46)
	out, err := ApplyOpenAIOAuthIdentityPlan(http.Header{}, body, compact)
	require.NoError(t, err)
	require.Equal(t, compact.PromptCacheKey.Value, gjson.GetBytes(out, "prompt_cache_key").String())
	require.NotEqual(t, compact.TurnIdentity.SessionID, compact.PromptCacheKey.Value)
}

func TestOpenAICodexPromptCacheProjectionMapsOverrideAndPreservesPassthroughBytes(t *testing.T) {
	svc := &OpenAIGatewayService{cfg: &config.Config{JWT: config.JWTConfig{Secret: "prompt-cache-override-secret"}}}
	account := &Account{ID: 73002, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	body := []byte(`{"untouched":1.2300,"client_metadata":{"session_id":"logical-root","unknown":"keep"},"prompt_cache_key":"review-scope"}`)

	_, plan := resolvePromptCacheIdentityPlan(t, svc, account, body, OpenAIOAuthIdentityProjectionPassthrough, 72)
	require.Equal(t, OpenAICodexPromptCacheKeyOverride, plan.PromptCacheKey.Kind)
	require.Len(t, plan.PromptCacheKey.Value, 46)
	out, err := ApplyOpenAIOAuthIdentityPlan(http.Header{}, body, plan)
	require.NoError(t, err)
	require.Contains(t, string(out), `"untouched":1.2300`)
	require.Equal(t, "keep", gjson.GetBytes(out, "client_metadata.unknown").String())
	require.Equal(t, plan.PromptCacheKey.Value, gjson.GetBytes(out, "prompt_cache_key").String())
	require.NotEqual(t, "review-scope", plan.PromptCacheKey.Value)

	otherAccount := &Account{ID: 73003, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	_, failover := resolvePromptCacheIdentityPlan(t, svc, otherAccount, body, OpenAIOAuthIdentityProjectionPassthrough, 72)
	require.NotEqual(t, plan.PromptCacheKey.Value, failover.PromptCacheKey.Value)

	_, otherAPIKey := resolvePromptCacheIdentityPlan(t, svc, account, body, OpenAIOAuthIdentityProjectionPassthrough, 73)
	require.NotEqual(t, plan.PromptCacheKey.Value, otherAPIKey.PromptCacheKey.Value)
}

func TestOpenAICodexPromptCacheProjectionGuardianAndAlphaFallback(t *testing.T) {
	svc := &OpenAIGatewayService{cfg: &config.Config{JWT: config.JWTConfig{Secret: "prompt-cache-guardian-secret"}}}
	account := &Account{ID: 73004, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	body := []byte(`{"client_metadata":{"x-openai-subagent":"review","x-codex-turn-metadata":"{\"session_id\":\"root\",\"thread_id\":\"guardian-child\",\"parent_thread_id\":\"parent\"}"},"prompt_cache_key":"guardian:parent"}`)
	_, guardian := resolvePromptCacheIdentityPlan(t, svc, account, body, OpenAIOAuthIdentityProjectionRegular, 74)
	require.Equal(t, OpenAICodexPromptCacheKeyGuardian, guardian.PromptCacheKey.Kind)
	require.Equal(t, "guardian:"+guardian.TurnIdentity.ParentThreadID, guardian.PromptCacheKey.Value)

	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Set("api_key", &APIKey{ID: int64(74)})
	alphaBody := []byte(`{"prompt_cache_key":"unsupported-alpha-key"}`)
	alphaCapture := CaptureOpenAIOAuthIdentityForAlphaSearch(c, alphaBody, "alpha-id")
	alphaFallback, err := svc.ResolveOpenAIOAuthIdentityPlan(context.Background(), c, account, alphaCapture, OpenAIOAuthIdentityPlanOptions{
		TurnIdentityEnabled: true, ProjectionMode: OpenAIOAuthIdentityProjectionRegular,
		InstallationPolicy: OpenAIOAuthInstallationPreserve,
	})
	require.NoError(t, err)
	require.Equal(t, alphaFallback.TurnIdentity.SessionID, alphaFallback.PromptCacheKey.Value)
	require.NotEqual(t, "unsupported-alpha-key", alphaFallback.PromptCacheKey.Value)
}

func TestOpenAICodexPromptCacheFallbackWithoutJWTSecretAndDisabledBoundaries(t *testing.T) {
	account := &Account{ID: 73005, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	body := []byte(`{"client_metadata":{"session_id":"root"},"prompt_cache_key":"override"}`)
	before := SnapshotOpenAIOutboundSessionIdentityRuntimeMetrics().PromptCacheFallbackTotal
	_, plan := resolvePromptCacheIdentityPlan(t, &OpenAIGatewayService{}, account, body, OpenAIOAuthIdentityProjectionRegular, 75)
	require.True(t, plan.PromptCacheKey.Enabled)
	require.Len(t, plan.PromptCacheKey.Value, 46)
	require.Equal(t, before+1, SnapshotOpenAIOutboundSessionIdentityRuntimeMetrics().PromptCacheFallbackTotal)

	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Set(openAICodexFingerprintPolicyContextKey, CodexFingerprintPolicySnapshot{
		MasterEnabled: true, InstallationIDEnabled: true, ClientIdentityEnabled: true,
	})
	capture := CaptureOpenAIOAuthIdentity(c, body, "")
	disabled, err := (&OpenAIGatewayService{}).ResolveOpenAIOAuthIdentityPlan(context.Background(), c, account, capture, OpenAIOAuthIdentityPlanOptions{
		TurnIdentityEnabled: true, ProjectionMode: OpenAIOAuthIdentityProjectionRegular,
		InstallationPolicy: OpenAIOAuthInstallationPreserve,
	})
	require.NoError(t, err)
	require.False(t, disabled.PromptCacheKey.Enabled)
	out, err := ApplyOpenAIOAuthIdentityPlan(http.Header{}, body, disabled)
	require.NoError(t, err)
	require.Equal(t, "override", gjson.GetBytes(out, "prompt_cache_key").String())

	apiKey := &Account{ID: 73006, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	_, apiKeyPlan := resolvePromptCacheIdentityPlan(t, &OpenAIGatewayService{}, apiKey, body, OpenAIOAuthIdentityProjectionRegular, 75)
	require.False(t, apiKeyPlan.PromptCacheKey.Enabled)
}

func TestOpenAICodexMemoryCaptureAndProjectionHaveNoRequestTurnOrLineage(t *testing.T) {
	parentTurn := uuid.Must(uuid.NewV7()).String()
	rootTurn := uuid.Must(uuid.NewV7()).String()
	body := []byte(`{"client_metadata":{"session_id":"root","thread_id":"memory-thread","x-codex-turn-metadata":"{\"request_kind\":\"memory\"}"},"prompt_cache_key":"root"}`)
	svc := &OpenAIGatewayService{cfg: &config.Config{JWT: config.JWTConfig{Secret: "memory-identity-secret"}}}
	account := &Account{ID: 73007, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	capture, plan := resolvePromptCacheIdentityPlan(t, svc, account, body, OpenAIOAuthIdentityProjectionRegular, 76)
	require.Equal(t, CodexWireRequestMemory, capture.WireProfile.RequestKind)
	require.Empty(t, capture.RequestTurn)
	require.Empty(t, plan.RequestTurn)
	plan.RequestTurn = OpenAICodexRequestTurnSnapshot{
		ID: uuid.Must(uuid.NewV7()).String(), StartedAtUnixMS: 123, Generated: true,
	}
	plan.TurnIdentity.ParentThreadID = uuid.Must(uuid.NewV7()).String()
	plan.TurnIdentity.ForkedFromThreadID = uuid.Must(uuid.NewV7()).String()
	plan.WireProfile.TurnLineage.ParentThreadID = plan.TurnIdentity.ParentThreadID
	plan.WireProfile.TurnLineage.ForkedFromThreadID = plan.TurnIdentity.ForkedFromThreadID
	plan.WireProfile.TurnLineage.ParentTurnID = CodexTurnID{Kind: CodexTurnIDOpaqueInternal, Value: parentTurn}
	plan.WireProfile.TurnLineage.RootTurnID = CodexTurnID{Kind: CodexTurnIDOpaqueInternal, Value: rootTurn}

	headers := http.Header{}
	out, err := ApplyOpenAIOAuthIdentityPlan(headers, body, plan)
	require.NoError(t, err)
	require.Equal(t, plan.TurnIdentity.SessionID, headers.Get("session-id"))
	require.Equal(t, plan.TurnIdentity.ThreadID, headers.Get("thread-id"))
	require.Empty(t, headers.Get("x-codex-parent-thread-id"))
	require.Equal(t, plan.TurnIdentity.SessionID, gjson.GetBytes(out, "client_metadata.session_id").String())
	require.Equal(t, plan.TurnIdentity.ThreadID, gjson.GetBytes(out, "client_metadata.thread_id").String())
	nested := gjson.GetBytes(out, "client_metadata.x-codex-turn-metadata").String()
	require.Equal(t, "memory", gjson.Get(nested, "request_kind").String())
	for _, field := range []string{"session_id", "thread_id", "turn_id", "turn_started_at_unix_ms", "parent_thread_id", "forked_from_thread_id", "parent_turn_id", "root_turn_id"} {
		require.False(t, gjson.Get(nested, field).Exists(), field)
	}

	variant := plan
	variant.RequestTurn.ID = uuid.Must(uuid.NewV7()).String()
	require.Equal(t, OpenAICodexTurnStateIdentityDigest(plan), OpenAICodexTurnStateIdentityDigest(variant))
	relationVariant := plan
	if relationVariant.TurnIdentity.Relation == OpenAICodexTurnRelationRoot {
		relationVariant.TurnIdentity.Relation = OpenAICodexTurnRelationDescendant
	} else {
		relationVariant.TurnIdentity.Relation = OpenAICodexTurnRelationRoot
	}
	require.Equal(t, OpenAICodexTurnStateIdentityDigest(plan), OpenAICodexTurnStateIdentityDigest(relationVariant))
}
