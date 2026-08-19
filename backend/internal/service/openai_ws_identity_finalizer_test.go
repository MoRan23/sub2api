package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestFinalizeOpenAIOAuthWSWirePlanRegularAndRouting(t *testing.T) {
	svc := &OpenAIGatewayService{}
	account := &Account{Type: AccountTypeOAuth, Platform: PlatformOpenAI}
	base := codexWireProjectionTestPlan(t)
	payload := []byte(`{"type":"response.create","model":"gpt-final","service_tier":"fast","input":"hi"}`)

	plan, err := svc.finalizeOpenAIOAuthWSWirePlan(nil, account, base, payload, openAIOAuthWSWireFinalizeOptions{})
	require.NoError(t, err)
	require.Equal(t, CodexWireRequestTurn, plan.WireProfile.RequestKind)
	require.Equal(t, "model=gpt-final;tier=priority", plan.WireProfile.RoutingHint)

	projected, err := svc.projectOpenAIOAuthWSFrame(nil, account, plan, payload)
	require.NoError(t, err)
	metadata := gjson.GetBytes(projected, "client_metadata.x-codex-turn-metadata")
	require.True(t, metadata.Exists())
	require.Equal(t, "turn", gjson.Get(metadata.String(), "request_kind").String())
	require.Equal(t, plan.RequestTurn.ID, gjson.Get(metadata.String(), "turn_id").String())
	require.False(t, gjson.GetBytes(projected, "client_metadata."+openAICodexWSStreamRequestStartMSKey).Exists())

	headers := http.Header{openAICodexRoutingHintHeader: {"model=caller"}}
	applyOpenAICodexWSRoutingHint(headers, account, plan)
	require.Equal(t, plan.WireProfile.RoutingHint, headers.Get(openAICodexRoutingHintHeader))
}

func TestFinalizeOpenAIOAuthWSWirePlanInlineCompaction(t *testing.T) {
	svc := &OpenAIGatewayService{}
	account := &Account{Type: AccountTypeOAuth, Platform: PlatformOpenAI}
	base := codexWireProjectionTestPlan(t)
	payload := []byte(`{"type":"response.create","model":"gpt-5.4","input":[{"type":"compaction_trigger"}]}`)

	plan, err := svc.finalizeOpenAIOAuthWSWirePlan(nil, account, base, payload, openAIOAuthWSWireFinalizeOptions{})
	require.NoError(t, err)
	require.Equal(t, CodexWireRequestCompaction, plan.WireProfile.RequestKind)
	var compaction CodexCompactionTurnMetadata
	require.NoError(t, json.Unmarshal(plan.WireProfile.Compaction, &compaction))
	require.Equal(t, CodexCompactionImplementationRemoteV2, compaction.Implementation)
	require.Equal(t, "auto", compaction.Trigger)
	require.Equal(t, "context_limit", compaction.Reason)
	require.Equal(t, "mid_turn", compaction.Phase)

	projected, err := svc.projectOpenAIOAuthWSFrame(nil, account, plan, payload)
	require.NoError(t, err)
	metadata := gjson.GetBytes(projected, "client_metadata.x-codex-turn-metadata").String()
	require.Equal(t, "compaction", gjson.Get(metadata, "request_kind").String())
	require.Equal(t, "responses_compaction_v2", gjson.Get(metadata, "compaction.implementation").String())
	require.Equal(t, "mid_turn", gjson.Get(metadata, "compaction.phase").String())

	stamped, err := stampOpenAICodexWSStreamRequestStart(projected, time.UnixMilli(1712345678901))
	require.NoError(t, err)
	require.Equal(t, "1712345678901", gjson.GetBytes(stamped, "client_metadata."+openAICodexWSStreamRequestStartMSKey).String())
}

func TestFinalizeOpenAIOAuthWSWirePlanPrewarmClearsDynamicTurnFields(t *testing.T) {
	svc := &OpenAIGatewayService{}
	account := &Account{Type: AccountTypeOAuth, Platform: PlatformOpenAI}
	base := codexWireProjectionTestPlan(t)
	base.WireProfile.Workspaces = json.RawMessage(`{"D:/Code/sub2api":{"trust_level":"trusted"}}`)
	base.WireProfile.TurnLineage.ParentTurnID = base.RequestTurn.TypedID
	base.WireProfile.TurnLineage.RootTurnID = base.RequestTurn.TypedID
	payload := []byte(`{"type":"response.create","generate":false,"model":"gpt-5.4","client_metadata":{"x-codex-turn-metadata":"{\"workspaces\":{\"D:/Code/sub2api\":{\"trust_level\":\"trusted\"}}}"}}`)

	plan, err := svc.finalizeOpenAIOAuthWSWirePlan(nil, account, base, payload, openAIOAuthWSWireFinalizeOptions{
		RequestKind: CodexWireRequestPrewarm,
	})
	require.NoError(t, err)
	require.Equal(t, CodexWireRequestPrewarm, plan.WireProfile.RequestKind)
	require.Empty(t, plan.RequestTurn.ID)
	require.Empty(t, plan.WireProfile.TurnID.Value)
	require.Empty(t, plan.WireProfile.TurnLineage.ParentTurnID.Value)
	require.Empty(t, plan.WireProfile.TurnLineage.RootTurnID.Value)
	require.False(t, plan.WireProfile.TurnStartedAtSet)
	require.Empty(t, plan.WireProfile.Workspaces)
	require.Equal(t, base.TurnIdentity.SessionID, plan.WireProfile.SessionID)
	require.Equal(t, base.TurnIdentity.ThreadID, plan.WireProfile.ThreadID)
	require.Equal(t, base.Window.WindowID(), plan.WireProfile.WindowID)

	projected, err := svc.projectOpenAIOAuthWSFrame(nil, account, plan, payload)
	require.NoError(t, err)
	projected, err = stripOpenAICodexPrewarmWorkspaces(projected)
	require.NoError(t, err)
	metadata := gjson.GetBytes(projected, "client_metadata.x-codex-turn-metadata").String()
	require.Equal(t, "prewarm", gjson.Get(metadata, "request_kind").String())
	require.Equal(t, base.TurnIdentity.SessionID, gjson.Get(metadata, "session_id").String())
	require.Equal(t, base.TurnIdentity.ThreadID, gjson.Get(metadata, "thread_id").String())
	for _, key := range []string{"turn_id", "parent_turn_id", "root_turn_id", "turn_started_at_unix_ms", "workspaces"} {
		require.False(t, gjson.Get(metadata, key).Exists(), key)
	}
}

func TestFinalizeOpenAIOAuthWSWirePlanMemoryAndPhysicalConflicts(t *testing.T) {
	svc := &OpenAIGatewayService{}
	account := &Account{Type: AccountTypeOAuth, Platform: PlatformOpenAI}
	base := openAIMemoryRoutingTestPlan(t)
	base.WireProfile.SubagentHeader = "memory_consolidation"
	base.Capture.WireProfile.SubagentHeader = "memory_consolidation"
	payload := []byte(`{"type":"response.create","model":"gpt-5.4","client_metadata":{"x-codex-turn-metadata":"{\"request_kind\":\"memory\",\"sandbox\":\"workspace-write\"}"},"input":"hi"}`)

	plan, err := svc.finalizeOpenAIOAuthWSWirePlan(nil, account, base, payload, openAIOAuthWSWireFinalizeOptions{})
	require.NoError(t, err)
	require.Equal(t, CodexWireRequestMemory, plan.WireProfile.RequestKind)
	require.Empty(t, plan.RequestTurn.ID)
	require.Empty(t, plan.WireProfile.TurnID.Value)
	require.Empty(t, plan.WireProfile.TurnLineage)
	require.False(t, plan.WireProfile.TurnStartedAtSet)
	require.Equal(t, base.TurnIdentity.SessionID, plan.WireProfile.SessionID)
	require.Equal(t, base.TurnIdentity.ThreadID, plan.WireProfile.ThreadID)
	require.Equal(t, base.Window.WindowID(), plan.WireProfile.WindowID)

	projected, err := svc.projectOpenAIOAuthWSFrame(nil, account, plan, payload)
	require.NoError(t, err)
	nested := gjson.GetBytes(projected, "client_metadata.x-codex-turn-metadata").String()
	require.Equal(t, "memory", gjson.Get(nested, "request_kind").String())
	for _, key := range []string{
		"installation_id", "session_id", "thread_id", "agent_name", "turn_id", "window_id",
		"forked_from_thread_id", "parent_thread_id", "parent_turn_id", "root_turn_id",
		"turn_started_at_unix_ms", "compaction",
	} {
		require.False(t, gjson.Get(nested, key).Exists(), key)
	}
	require.Equal(t, base.TurnIdentity.SessionID, gjson.GetBytes(projected, "client_metadata.session_id").String())
	require.Equal(t, base.TurnIdentity.ThreadID, gjson.GetBytes(projected, "client_metadata.thread_id").String())
	require.Equal(t, base.Window.WindowID(), gjson.GetBytes(projected, "client_metadata.x-codex-window-id").String())
	require.False(t, gjson.GetBytes(projected, "client_metadata.turn_id").Exists())

	conflicts := []struct {
		name    string
		payload []byte
	}{
		{name: "prewarm", payload: []byte(`{"type":"response.create","generate":false,"model":"gpt-5.4"}`)},
		{name: "compaction", payload: []byte(`{"type":"response.create","model":"gpt-5.4","input":[{"type":"compaction_trigger"}]}`)},
	}
	for _, conflict := range conflicts {
		t.Run(conflict.name, func(t *testing.T) {
			_, conflictErr := svc.finalizeOpenAIOAuthWSWirePlan(nil, account, base, conflict.payload, openAIOAuthWSWireFinalizeOptions{})
			require.ErrorIs(t, conflictErr, ErrOpenAICodexRequestKindConflict)
		})
	}
}

func TestFinalizeOpenAIOAuthWSWirePlanMemgenSubagentDoesNotImplyMemory(t *testing.T) {
	svc := &OpenAIGatewayService{}
	account := &Account{Type: AccountTypeOAuth, Platform: PlatformOpenAI}
	base := codexWireProjectionTestPlan(t)
	base.WireProfile.SubagentHeader = "memory_consolidation"
	payload := []byte(`{"type":"response.create","model":"gpt-5.4","input":"consolidate"}`)

	plan, err := svc.finalizeOpenAIOAuthWSWirePlan(nil, account, base, payload, openAIOAuthWSWireFinalizeOptions{})
	require.NoError(t, err)
	require.Equal(t, CodexWireRequestTurn, plan.WireProfile.RequestKind)
	require.NotEmpty(t, plan.RequestTurn.ID)
	require.Equal(t, "memory_consolidation", plan.WireProfile.SubagentHeader)
}

func TestFinalizeOpenAIOAuthWSWirePlanIgnoresSpoofedInternalRequestKind(t *testing.T) {
	svc := &OpenAIGatewayService{}
	account := &Account{Type: AccountTypeOAuth, Platform: PlatformOpenAI}
	payload := []byte(`{"type":"response.create","model":"gpt-5.4","input":"ordinary turn"}`)

	for _, spoofed := range []CodexWireRequestKind{CodexWireRequestCompaction, CodexWireRequestPrewarm} {
		t.Run(string(spoofed), func(t *testing.T) {
			base := codexWireProjectionTestPlan(t)
			base.WireProfile.RequestKind = spoofed
			base.Capture.WireProfile.RequestKind = spoofed
			plan, err := svc.finalizeOpenAIOAuthWSWirePlan(nil, account, base, payload, openAIOAuthWSWireFinalizeOptions{})
			require.NoError(t, err)
			require.Equal(t, CodexWireRequestTurn, plan.WireProfile.RequestKind)
			require.NotEmpty(t, plan.RequestTurn.ID)
			require.Nil(t, openAICodexWSCompactionDeliveryForPlan(account, plan), "spoofed metadata must not arm compaction CAS")
		})
	}
}

func TestOpenAICodexWSWireRequestKindPhysicalCompactionPrecedesGenerateFalse(t *testing.T) {
	plan := codexWireProjectionTestPlan(t)
	payload := []byte(`{"type":"response.create","generate":false,"input":[{"type":"compaction_trigger"}]}`)
	kind, err := openAICodexWSWireRequestKind(payload, plan)
	require.NoError(t, err)
	require.Equal(t, CodexWireRequestCompaction, kind)
}

func TestCaptureOpenAIWSFrameIdentityUsesCurrentFramePromptCacheSnapshot(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Set("api_key", &APIKey{ID: int64(994)})
	svc := &OpenAIGatewayService{cfg: &config.Config{JWT: config.JWTConfig{Secret: "ws-prompt-cache-secret"}}}
	account := &Account{ID: 9194, Type: AccountTypeOAuth, Platform: PlatformOpenAI}
	options := OpenAIOAuthIdentityPlanOptions{
		TurnIdentityEnabled: true,
		ProjectionMode:      OpenAIOAuthIdentityProjectionPassthrough,
		InstallationPolicy:  OpenAIOAuthInstallationPreserve,
	}
	initialBody := []byte(`{"type":"response.create","model":"gpt-5.4","client_metadata":{"session_id":"ws-prompt-root"},"prompt_cache_key":"ws-prompt-root","input":"first"}`)
	initialCapture := CaptureOpenAIOAuthIdentity(c, initialBody, "")
	current, err := svc.ResolveOpenAIOAuthIdentityPlan(context.Background(), c, account, initialCapture, options)
	require.NoError(t, err)
	require.Equal(t, current.TurnIdentity.SessionID, current.PromptCacheKey.Value)
	initialDigest := openAIWSOutboundIdentityPlanDigest(http.Header{}, current)

	overrideBody := []byte(`{"type":"response.create","model":"gpt-5.4","prompt_cache_key":"review-scope","input":"override"}`)
	overrideCapture := captureOpenAIWSFrameIdentity(
		overrideBody,
		&current,
	)
	require.Equal(t, current.Capture.Logical, overrideCapture.Logical, "the socket tuple remains pinned")
	require.Equal(t, "review-scope", overrideCapture.PromptCacheKey.Value)
	require.Equal(t, OpenAICodexPromptCacheKeyOverride, overrideCapture.PromptCacheKey.Kind)
	require.True(t, overrideCapture.PromptCacheKey.Present)
	require.True(t, overrideCapture.PromptCacheKey.Valid)
	overridePlan, err := svc.ResolveOpenAIOAuthIdentityPlan(context.Background(), c, account, overrideCapture, options)
	require.NoError(t, err)
	require.Equal(t, current.TurnIdentity, overridePlan.TurnIdentity)
	require.Len(t, overridePlan.PromptCacheKey.Value, 46)
	require.Equal(t, "pc_", overridePlan.PromptCacheKey.Value[:3])
	overrideProjected, err := ApplyOpenAIOAuthIdentityPlan(http.Header{}, overrideBody, overridePlan)
	require.NoError(t, err)
	require.Equal(t, overridePlan.PromptCacheKey.Value, gjson.GetBytes(overrideProjected, "prompt_cache_key").String())
	require.Equal(t, initialDigest, openAIWSOutboundIdentityPlanDigest(http.Header{}, overridePlan), "per-frame override must not change socket affinity")

	defaultBody := []byte(`{"type":"response.create","model":"gpt-5.4","prompt_cache_key":"ws-prompt-root","input":"default"}`)
	defaultCapture := captureOpenAIWSFrameIdentity(defaultBody, &current)
	require.Equal(t, current.Capture.Logical, defaultCapture.Logical)
	require.Equal(t, OpenAICodexPromptCacheKeyDefault, defaultCapture.PromptCacheKey.Kind)
	defaultPlan, err := svc.ResolveOpenAIOAuthIdentityPlan(context.Background(), c, account, defaultCapture, options)
	require.NoError(t, err)
	require.Equal(t, current.TurnIdentity, defaultPlan.TurnIdentity)
	require.Equal(t, current.TurnIdentity.SessionID, defaultPlan.PromptCacheKey.Value)
	defaultProjected, err := ApplyOpenAIOAuthIdentityPlan(http.Header{}, defaultBody, defaultPlan)
	require.NoError(t, err)
	require.Equal(t, current.TurnIdentity.SessionID, gjson.GetBytes(defaultProjected, "prompt_cache_key").String())
	require.Equal(t, initialDigest, openAIWSOutboundIdentityPlanDigest(http.Header{}, defaultPlan))
}

func TestCaptureOpenAIWSFrameIdentityKeepsFrameWireProfile(t *testing.T) {
	base := codexWireProjectionTestPlan(t)
	base.Capture.WireProfile.AgentName = "connection-agent"
	base.Capture.WireProfile.SandboxMode = "danger-full-access"
	payload := []byte(`{"type":"response.create","input":[{"type":"compaction_trigger"}],"client_metadata":{"x-codex-turn-metadata":"{\"request_kind\":\"compaction\",\"compaction\":{\"trigger\":\"auto\",\"reason\":\"context_limit\",\"implementation\":\"responses_compaction_v2\",\"phase\":\"mid_turn\",\"strategy\":\"memento\"},\"thread_source\":\"frame-source\"}"}}`)

	capture := captureOpenAIWSFrameIdentity(payload, &base)
	require.Equal(t, CodexWireRequestCompaction, capture.WireProfile.RequestKind)
	require.Equal(t, "frame-source", capture.WireProfile.ThreadSource)
	require.Equal(t, "connection-agent", capture.WireProfile.AgentName)
	require.Equal(t, "danger-full-access", capture.WireProfile.SandboxMode)
	require.JSONEq(t,
		`{"trigger":"auto","reason":"context_limit","implementation":"responses_compaction_v2","phase":"mid_turn","strategy":"memento"}`,
		string(capture.WireProfile.Compaction),
	)
	require.Equal(t, base.RequestTurn.ID, capture.RequestTurn.ID, "inline compaction continues the current request turn")
	require.Equal(t, base.RequestTurn.ID, capture.WireProfile.TurnID.Value)
}

func TestOpenAIWSContinuationResolveFinalizeApplyKeepsCurrentTurn(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Set("api_key", &APIKey{ID: 991})
	svc := &OpenAIGatewayService{cfg: &config.Config{JWT: config.JWTConfig{Secret: "ws-wire-continuation-secret"}}}
	account := &Account{ID: 9191, Type: AccountTypeOAuth, Platform: PlatformOpenAI}
	options := OpenAIOAuthIdentityPlanOptions{
		TurnIdentityEnabled: true,
		ProjectionMode:      OpenAIOAuthIdentityProjectionPassthrough,
		InstallationPolicy:  OpenAIOAuthInstallationPreserve,
	}
	firstBody := []byte(`{"type":"response.create","model":"gpt-5.4","client_metadata":{"session_id":"ws-continuation"},"input":"hi"}`)
	firstCapture := CaptureOpenAIOAuthIdentity(c, firstBody, "")
	firstPlan, err := svc.GetOrResolveOpenAIOAuthOutboundIdentity(context.Background(), c, account, firstCapture, options, nil)
	require.NoError(t, err)
	firstPlan, err = svc.finalizeOpenAIOAuthWSWirePlan(c, account, firstPlan, firstBody, openAIOAuthWSWireFinalizeOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, firstPlan.RequestTurn.ID)

	continuationBody := []byte(`{"type":"response.create","model":"gpt-5.4","input":[{"type":"function_call_output","call_id":"call_1","output":"ok"},{"type":"compaction_trigger"}]}`)
	continuationCapture := captureOpenAIWSFrameIdentity(continuationBody, &firstPlan)
	continuationPlan, err := svc.GetOrResolveOpenAIOAuthOutboundIdentity(context.Background(), c, account, continuationCapture, options, &firstPlan)
	require.NoError(t, err)
	continuationPlan, err = svc.finalizeOpenAIOAuthWSWirePlan(c, account, continuationPlan, continuationBody, openAIOAuthWSWireFinalizeOptions{})
	require.NoError(t, err)
	require.Equal(t, firstPlan.RequestTurn.ID, continuationPlan.RequestTurn.ID)
	require.Equal(t, firstPlan.RequestTurn.ID, continuationPlan.WireProfile.TurnID.Value)

	projected, err := svc.projectOpenAIOAuthWSFrame(c, account, continuationPlan, continuationBody)
	require.NoError(t, err)
	metadata := gjson.GetBytes(projected, "client_metadata.x-codex-turn-metadata").String()
	require.Equal(t, firstPlan.RequestTurn.ID, gjson.Get(metadata, "turn_id").String())
	require.Equal(t, "compaction", gjson.Get(metadata, "request_kind").String())
	require.Equal(t, "responses_compaction_v2", gjson.Get(metadata, "compaction.implementation").String())
}

func TestFinalizeOpenAIOAuthWSWirePlanAPIKeyNoop(t *testing.T) {
	svc := &OpenAIGatewayService{}
	account := &Account{Type: AccountTypeAPIKey, Platform: PlatformOpenAI}
	base := codexWireProjectionTestPlan(t)
	payload := []byte(`{"type":"response.create","model":"gpt-other"}`)

	plan, err := svc.finalizeOpenAIOAuthWSWirePlan(nil, account, base, payload, openAIOAuthWSWireFinalizeOptions{})
	require.NoError(t, err)
	require.True(t, codexWireProfilesEqual(base.WireProfile, plan.WireProfile))
	projected, err := svc.projectOpenAIOAuthWSFrame(nil, account, plan, payload)
	require.NoError(t, err)
	require.Equal(t, payload, projected)
}

func TestFinalizeOpenAIOAuthWSWirePlanTurnIdentityDisabledDoesNotClassifyMemory(t *testing.T) {
	svc := &OpenAIGatewayService{}
	account := &Account{Type: AccountTypeOAuth, Platform: PlatformOpenAI}
	base := openAIMemoryRoutingTestPlan(t)
	base.TurnIdentityRequested = false
	base.TurnIdentityEnabled = false
	base.TurnIdentity = OpenAICodexTurnIdentity{}
	base.InstallationEnabled = false
	base.ClientIdentityEnabled = false
	base.PromptCacheKey = OpenAICodexPromptCacheKeyPlan{}

	tests := []struct {
		name    string
		payload []byte
		options openAIOAuthWSWireFinalizeOptions
	}{
		{name: "ordinary turn", payload: []byte(`{"type":"response.create","model":"gpt-5.6-sol"}`)},
		{name: "compaction trigger", payload: []byte(`{"type":"response.create","input":[{"type":"compaction_trigger"}]}`)},
		{name: "prewarm", payload: []byte(`{"type":"response.create","generate":false}`)},
		{name: "explicit compaction", payload: []byte(`{"type":"response.create"}`), options: openAIOAuthWSWireFinalizeOptions{RequestKind: CodexWireRequestCompaction}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan, err := svc.finalizeOpenAIOAuthWSWirePlan(nil, account, base, test.payload, test.options)
			require.NoError(t, err)
			require.Equal(t, base, plan)
			projected, err := svc.projectOpenAIOAuthWSFrame(nil, account, plan, test.payload)
			require.NoError(t, err)
			require.Equal(t, test.payload, projected)
		})
	}
}

func TestFinalizeOpenAIOAuthWSWirePlanUsesEffectiveResponsesLiteCapability(t *testing.T) {
	account := &Account{ID: 42, Type: AccountTypeOAuth, Platform: PlatformOpenAI}
	const model = "gpt-5.6-sol"
	newPlan := func(t *testing.T) OpenAIOAuthIdentityPlan {
		plan := codexWireProjectionTestPlan(t)
		plan.CredentialOwnerNamespace = "account:42"
		return plan
	}
	newPayload := func(marker string) []byte {
		if marker == "" {
			return []byte(`{"type":"response.create","model":"gpt-5.6-sol","reasoning":{"context":"current_turn"},"tools":[{"type":"namespace","name":"collaboration"}],"input":"hi"}`)
		}
		return []byte(`{"type":"response.create","model":"gpt-5.6-sol","reasoning":{"context":"current_turn"},"tools":[{"type":"namespace","name":"collaboration"}],"input":"hi","client_metadata":{"ws_request_header_x_openai_internal_codex_responses_lite":` + marker + `}}`)
	}

	t.Run("unknown manifest honors explicit string marker", func(t *testing.T) {
		svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{
			CodexMetadata: config.GatewayCodexMetadataConfig{TurnMetadataIncludesToolInfo: true},
		}}}
		payload := newPayload(`"true"`)
		plan, err := svc.finalizeOpenAIOAuthWSWirePlan(nil, account, newPlan(t), payload, openAIOAuthWSWireFinalizeOptions{})
		require.NoError(t, err)
		require.True(t, plan.WireProfile.ToolNamespacesAllowed)

		projected, err := svc.projectOpenAIOAuthWSFrame(nil, account, plan, payload)
		require.NoError(t, err)
		require.Equal(t, "true", gjson.GetBytes(projected, "client_metadata."+responsesLiteWSMetadataKey).String())
		require.Equal(t, "all_turns", gjson.GetBytes(projected, "reasoning.context").String())
		require.False(t, gjson.GetBytes(projected, `tools.#(type=="namespace")`).Exists())
		require.Equal(t, "collaboration", gjson.GetBytes(projected, `input.#(type=="additional_tools").tools.0.name`).String())
		nested := gjson.GetBytes(projected, "client_metadata.x-codex-turn-metadata").String()
		require.True(t, gjson.Get(nested, "tool_namespaces_info").IsObject())
	})

	t.Run("configuration is frozen into plan before projection", func(t *testing.T) {
		cfg := &config.Config{Gateway: config.GatewayConfig{CodexMetadata: config.GatewayCodexMetadataConfig{
			AgentName: "snapshot-agent", Sandbox: "snapshot-sandbox", SandboxMode: "snapshot-mode",
			AutoReviewEnabled: true,
		}}}
		svc := &OpenAIGatewayService{cfg: cfg}
		base := newPlan(t)
		base.WireProfile.AgentName = ""
		base.WireProfile.Sandbox = ""
		base.WireProfile.SandboxMode = ""
		base.WireProfile.AutoReviewEnabled = nil
		payload := newPayload("")

		plan, err := svc.finalizeOpenAIOAuthWSWirePlan(nil, account, base, payload, openAIOAuthWSWireFinalizeOptions{})
		require.NoError(t, err)
		cfg.Gateway.CodexMetadata.AgentName = "mutated-after-finalize"

		projected, err := svc.projectOpenAIOAuthWSFrame(nil, account, plan, payload)
		require.NoError(t, err)
		nested := gjson.GetBytes(projected, "client_metadata.x-codex-turn-metadata").String()
		require.Equal(t, "snapshot-agent", gjson.Get(nested, "agent_name").String())
		require.Equal(t, "snapshot-sandbox", gjson.Get(nested, "sandbox").String())
		require.Equal(t, "snapshot-mode", gjson.Get(nested, "sandbox_mode").String())
		require.True(t, gjson.Get(nested, "auto_review_enabled").Bool())
	})

	t.Run("known false overrides explicit marker", func(t *testing.T) {
		svc := &OpenAIGatewayService{}
		svc.codexModelCapabilities.observeManifest("account:42", []byte(`{"models":[{"slug":"gpt-5.6-sol","use_responses_lite":false}]}`), time.Now())
		payload := newPayload(`"true"`)
		plan, err := svc.finalizeOpenAIOAuthWSWirePlan(nil, account, newPlan(t), payload, openAIOAuthWSWireFinalizeOptions{})
		require.NoError(t, err)
		require.False(t, plan.WireProfile.ToolNamespacesAllowed)

		projected, err := svc.projectOpenAIOAuthWSFrame(nil, account, plan, payload)
		require.NoError(t, err)
		require.False(t, gjson.GetBytes(projected, "client_metadata."+responsesLiteWSMetadataKey).Exists())
		require.Equal(t, "current_turn", gjson.GetBytes(projected, "reasoning.context").String())
		require.True(t, gjson.GetBytes(projected, `tools.#(type=="namespace")`).Exists())
		nested := gjson.GetBytes(projected, "client_metadata.x-codex-turn-metadata").String()
		require.False(t, gjson.Get(nested, "tool_namespaces_info").Exists())
	})

	t.Run("known true enables Lite without marker", func(t *testing.T) {
		svc := &OpenAIGatewayService{}
		svc.codexModelCapabilities.observeManifest("account:42", []byte(`{"models":[{"slug":"gpt-5.6-sol","use_responses_lite":true}]}`), time.Now())
		payload := newPayload("")
		plan, err := svc.finalizeOpenAIOAuthWSWirePlan(nil, account, newPlan(t), payload, openAIOAuthWSWireFinalizeOptions{})
		require.NoError(t, err)
		require.True(t, plan.WireProfile.ToolNamespacesAllowed)

		projected, err := svc.projectOpenAIOAuthWSFrame(nil, account, plan, payload)
		require.NoError(t, err)
		require.Equal(t, "true", gjson.GetBytes(projected, "client_metadata."+responsesLiteWSMetadataKey).String())
		require.Equal(t, "all_turns", gjson.GetBytes(projected, "reasoning.context").String())
	})

	t.Run("boolean marker is not an explicit wire capability", func(t *testing.T) {
		svc := &OpenAIGatewayService{}
		payload := newPayload("true")
		plan, err := svc.finalizeOpenAIOAuthWSWirePlan(nil, account, newPlan(t), payload, openAIOAuthWSWireFinalizeOptions{})
		require.NoError(t, err)
		require.False(t, plan.WireProfile.ToolNamespacesAllowed)

		projected, err := svc.projectOpenAIOAuthWSFrame(nil, account, plan, payload)
		require.NoError(t, err)
		require.False(t, gjson.GetBytes(projected, "client_metadata."+responsesLiteWSMetadataKey).Exists())
		require.Equal(t, "current_turn", gjson.GetBytes(projected, "reasoning.context").String())
	})
}
