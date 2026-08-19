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
