package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

const (
	codexWireTestSession    = "01989f44-7c00-7000-8000-000000000001"
	codexWireTestThread     = "01989f44-7c00-7000-8000-000000000002"
	codexWireTestTurn       = "01989f44-7c00-7000-8000-000000000003"
	codexWireTestParentTurn = "01989f44-7c00-7000-8000-000000000004"
	codexWireTestRootTurn   = "01989f44-7c00-7000-8000-000000000005"
	codexWireTestFork       = "01989f44-7c00-7000-8000-000000000006"
)

func readCodex3929Golden(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "codex_3929c99a", name))
	require.NoError(t, err)
	return strings.TrimSpace(string(raw))
}

func codexWireTestBody(t *testing.T, nested string, flat map[string]any) []byte {
	t.Helper()
	if flat == nil {
		flat = make(map[string]any)
	}
	flat[openAIWSTurnMetadataHeader] = nested
	body, err := json.Marshal(map[string]any{"model": "gpt-5.4", "client_metadata": flat})
	require.NoError(t, err)
	return body
}

func codexWireProjectionTestPlan(t *testing.T) OpenAIOAuthIdentityPlan {
	t.Helper()
	raw := fmt.Sprintf(`{
		"installation_id":"client-installation",
		"session_id":"%s",
		"thread_id":"%s",
		"agent_name":"custom-agent",
		"turn_id":"%s",
		"window_id":"client-window",
		"request_kind":"turn",
		"forked_from_thread_id":"%s",
		"parent_thread_id":"%s",
		"parent_turn_id":"%s",
		"root_turn_id":"%s",
		"subagent_kind":"review",
		"thread_source":"subagent",
		"sandbox":"workspace-write",
		"sandbox_mode":"workspace-write",
		"auto_review_enabled":true,
		"node_repl_auto_review_required":false,
		"node_repl_disabled":true,
		"workspaces":{"/tmp/\u6771\u4eac":{"associated_remote_urls":{"origin":"https://\u4f8b\u5b50.test/repo"},"latest_git_commit_hash":"abc","has_changes":true}},
		"tool_namespaces_info":{"shell":{"name":"shell","functions":{"run":{"name":"run","direct":true,"code_mode_name":null,"deferred":false,"source":{"kind":"harness"}}}}},
		"turn_started_at_unix_ms":1777777777123,
		"zeta":"\ud83d\ude80",
		"alpha":"\u4f60\u597d"
	}`, codexWireTestSession, codexWireTestThread, codexWireTestTurn, codexWireTestFork,
		codexWireTestSession, codexWireTestParentTurn, codexWireTestRootTurn)
	profile := ParseCodexWireProfile(raw)
	require.NoError(t, profile.Validate())
	profile.SubagentHeader = "review"
	return OpenAIOAuthIdentityPlan{
		RequestTurn: OpenAICodexRequestTurnSnapshot{
			ID: codexWireTestTurn, TypedID: CodexTurnID{Kind: CodexTurnIDUserUUIDv7, Value: codexWireTestTurn},
			StartedAtUnixMS: 1777777777123, Explicit: true,
		},
		WireProfile: profile,
		Window:      OpenAICodexWindowSnapshot{ThreadID: codexWireTestThread},
		TurnIdentity: OpenAICodexTurnIdentity{
			SessionID: codexWireTestSession, ThreadID: codexWireTestThread,
			ParentThreadID: codexWireTestSession, ForkedFromThreadID: codexWireTestFork,
			Relation: OpenAICodexTurnRelationDescendant,
		},
		TurnIdentityEnabled:   true,
		TurnIdentityRequested: true,
		InstallationID:        "installation-pin",
		InstallationEnabled:   true,
		InstallationPolicy:    OpenAIOAuthInstallationAccountPin,
		ProjectionMode:        OpenAIOAuthIdentityProjectionRegular,
	}
}

func TestCodexWireProfileOfficialGoldenProjection(t *testing.T) {
	plan := codexWireProjectionTestPlan(t)
	finalPlan, err := FinalizeOpenAICodexWirePlanWithOptions(plan, FinalizeOpenAICodexWirePlanOptions{
		RequestKind:       string(CodexWireRequestTurn),
		ModelCapabilities: CodexModelCapabilities{Known: true, UseResponsesLite: true},
		MetadataProfile:   CodexMetadataProfile{TurnMetadataIncludesToolInfo: true},
		FinalModel:        "gpt-5.4",
		FinalServiceTier:  "fast",
	})
	require.NoError(t, err)
	require.False(t, plan.WireProfile.Finalized)
	require.Empty(t, plan.WireProfile.RoutingHint)
	require.Equal(t, "model=gpt-5.4;tier=priority", finalPlan.WireProfile.RoutingHint)

	headers := http.Header{
		"Session-Id":               {"stale-session"},
		"Thread-Id":                {"stale-thread"},
		"X-Codex-Parent-Thread-Id": {"stale-parent"},
		"X-Codex-Turn-Metadata":    {`{"installation_id":"attacker","tool_namespaces_info":{"evil":{}},"header_extra":"\u5934"}`},
	}
	body := codexWireTestBody(t,
		`{"installation_id":"attacker","tool_namespaces_info":{"evil":{}},"late_extra":"\u540e\u7f6e"}`,
		map[string]any{
			"installation_id":                "forbidden",
			"agent_name":                     "forbidden",
			"request_kind":                   "forbidden",
			"compaction":                     map[string]any{"trigger": "auto"},
			"forked_from_thread_id":          "forbidden",
			"parent_thread_id":               "forbidden",
			"subagent_kind":                  "forbidden",
			"turn_started_at_unix_ms":        1,
			"sandbox":                        "forbidden",
			"workspaces":                     map[string]any{"forbidden": true},
			"tool_namespaces_info":           map[string]any{"forbidden": true},
			"node_repl_auto_review_required": true,
		},
	)
	out, err := ApplyOpenAIOAuthIdentityPlan(headers, body, finalPlan)
	require.NoError(t, err)

	var root map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(out, &root))
	var clientMetadata map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(root["client_metadata"], &clientMetadata))
	keys := make([]string, 0, len(clientMetadata))
	for key := range clientMetadata {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var wantKeys []string
	require.NoError(t, json.Unmarshal([]byte(readCodex3929Golden(t, "turn_flat_keys.golden.json")), &wantKeys))
	require.Equal(t, wantKeys, keys)

	var bodyNested string
	require.NoError(t, json.Unmarshal(clientMetadata[openAIWSTurnMetadataHeader], &bodyNested))
	require.Equal(t, readCodex3929Golden(t, "turn_body_nested.golden.json"), bodyNested)
	require.True(t, isASCIIString(bodyNested))
	require.NotContains(t, bodyNested, "routing_hint")

	headerNested := headers.Get(openAIWSTurnMetadataHeader)
	require.Equal(t, readCodex3929Golden(t, "turn_header_nested.golden.json"), headerNested)
	require.True(t, isASCIIString(headerNested))
	require.NotContains(t, headerNested, "tool_namespaces_info")
	require.Equal(t, codexWireTestSession, headers.Get("session-id"))
	require.Equal(t, codexWireTestThread, headers.Get("thread-id"))
	require.Equal(t, codexWireTestThread+":0", headers.Get("x-codex-window-id"))
	require.Equal(t, "review", headers.Get("x-openai-subagent"))
}

func isASCIIString(value string) bool {
	for index := 0; index < len(value); index++ {
		if value[index] >= 0x80 {
			return false
		}
	}
	return true
}

func TestCodexWireProfileRejectsOpaqueNormalLineageWithoutSilentReplacement(t *testing.T) {
	parsed := ParseCodexWireProfile(`{"turn_id":"opaque-user-turn"}`)
	require.Empty(t, parsed.RequestKind, "the parser must not synthesize request_kind")
	require.Empty(t, parsed.InvalidReason, "parser validation waits for a request kind")
	require.Empty(t, parsed.TurnID.Value)

	lowerPriorityTurn := codexWireTestTurn
	body := codexWireTestBody(t, `{"turn_id":"opaque-user-turn"}`, map[string]any{"turn_id": lowerPriorityTurn})
	capture := CaptureOpenAIOAuthIdentity(nil, body, "")
	require.Contains(t, capture.WireProfile.InvalidReason, "turn_id is invalid for request_kind turn")
	require.NotContains(t, capture.WireProfile.InvalidReason, "opaque-user-turn")
	require.Empty(t, capture.RequestTurn.ID, "a lower-priority UUID must not replace invalid explicit lineage")

	account := &Account{ID: 3901, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	plan, err := (&OpenAIGatewayService{}).ResolveOpenAIOAuthIdentityPlan(
		context.Background(), nil, account, capture, OpenAIOAuthIdentityPlanOptions{
			TurnIdentityEnabled: true, ProjectionMode: OpenAIOAuthIdentityProjectionRegular,
			InstallationPolicy: OpenAIOAuthInstallationPreserve,
		},
	)
	require.ErrorIs(t, err, ErrInvalidOpenAICodexWireProfile)
	require.False(t, plan.TurnIdentityEnabled)
	require.Equal(t, OpenAICodexWindowResolveNone, plan.WindowResolveOutcome)
}

func TestCodexWireProfileRejectsInvalidParentAndRootTurnIDs(t *testing.T) {
	for _, field := range []string{"parent_turn_id", "root_turn_id"} {
		t.Run(field, func(t *testing.T) {
			nested := fmt.Sprintf(`{"request_kind":"turn","turn_id":"%s","%s":"opaque-lineage"}`, codexWireTestTurn, field)
			capture := CaptureOpenAIOAuthIdentity(nil, codexWireTestBody(t, nested, nil), "")
			require.Contains(t, capture.WireProfile.InvalidReason, field+" is invalid")
			require.NotContains(t, capture.WireProfile.InvalidReason, "opaque-lineage")
			_, err := FinalizeOpenAICodexWirePlan(OpenAIOAuthIdentityPlan{
				WireProfile: capture.WireProfile, RequestTurn: capture.RequestTurn, TurnIdentityRequested: true,
			}, string(CodexWireRequestTurn), CodexModelCapabilities{})
			require.ErrorIs(t, err, ErrInvalidOpenAICodexWireProfile)
		})
	}
}

func TestCodexWireProfilePreservesRestrictedOpaqueInternalLineage(t *testing.T) {
	nested := `{"request_kind":"compaction","turn_id":"internal:turn","parent_turn_id":"internal:parent","root_turn_id":"internal:root","turn_started_at_unix_ms":-9}`
	capture := CaptureOpenAIOAuthIdentity(nil, codexWireTestBody(t, nested, nil), "")
	require.NoError(t, capture.WireProfile.Validate())
	require.Equal(t, CodexTurnIDOpaqueInternal, capture.WireProfile.TurnID.Kind)
	require.Equal(t, "internal:turn", capture.RequestTurn.ID)
	require.True(t, openAICodexRequestTurnSnapshotValidForWire(capture.RequestTurn, CodexWireRequestCompaction))
	require.False(t, openAICodexRequestTurnSnapshotValid(capture.RequestTurn))

	plan, err := FinalizeOpenAICodexWirePlan(OpenAIOAuthIdentityPlan{
		Capture: capture, WireProfile: capture.WireProfile, RequestTurn: capture.RequestTurn,
		TurnIdentityRequested: true, ProjectionMode: OpenAIOAuthIdentityProjectionRegular,
	}, string(CodexWireRequestCompaction), CodexModelCapabilities{})
	require.NoError(t, err)
	require.Equal(t, "internal:turn", plan.WireProfile.TurnID.Value)
	require.Equal(t, "internal:parent", plan.WireProfile.TurnLineage.ParentTurnID.Value)
	require.Equal(t, "internal:root", plan.WireProfile.TurnLineage.RootTurnID.Value)
	encoded, err := plan.WireProfile.MarshalNestedJSON(true)
	require.NoError(t, err)
	require.Equal(t, int64(-9), jsonInt64Field(t, encoded, "turn_started_at_unix_ms"))
}

func jsonInt64Field(t *testing.T, raw, field string) int64 {
	t.Helper()
	var object map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(raw), &object))
	var value int64
	require.NoError(t, json.Unmarshal(object[field], &value))
	return value
}

func TestFinalizeCodexWireProfileDefaultsAndRootLineage(t *testing.T) {
	requestTurn := OpenAICodexRequestTurnSnapshot{
		ID: codexWireTestTurn, TypedID: CodexTurnID{Kind: CodexTurnIDUserUUIDv7, Value: codexWireTestTurn},
		StartedAtUnixMS: 1777777777123, Generated: true,
	}
	rootPlan, err := FinalizeOpenAICodexWirePlan(OpenAIOAuthIdentityPlan{
		RequestTurn: requestTurn, TurnIdentityRequested: true, TurnIdentityEnabled: true,
		TurnIdentity: OpenAICodexTurnIdentity{
			SessionID: codexWireTestSession, ThreadID: codexWireTestSession, Relation: OpenAICodexTurnRelationRoot,
		},
		Window: OpenAICodexWindowSnapshot{ThreadID: codexWireTestSession},
	}, string(CodexWireRequestTurn), CodexModelCapabilities{})
	require.NoError(t, err)
	require.Equal(t, "/root", rootPlan.WireProfile.AgentName)
	require.Equal(t, "none", rootPlan.WireProfile.Sandbox)
	require.Equal(t, "danger-full-access", rootPlan.WireProfile.SandboxMode)
	require.False(t, *rootPlan.WireProfile.AutoReviewEnabled)
	require.False(t, *rootPlan.WireProfile.NodeREPLAutoReviewRequired)
	require.False(t, *rootPlan.WireProfile.NodeREPLDisabled)
	require.Equal(t, rootPlan.WireProfile.TurnID, rootPlan.WireProfile.TurnLineage.RootTurnID)
	require.Equal(t, codexWireTestSession+":0", rootPlan.WireProfile.WindowID)

	descendantPlan, err := FinalizeOpenAICodexWirePlan(OpenAIOAuthIdentityPlan{
		RequestTurn: requestTurn, TurnIdentityRequested: true, TurnIdentityEnabled: true,
		TurnIdentity: OpenAICodexTurnIdentity{
			SessionID: codexWireTestSession, ThreadID: codexWireTestThread, Relation: OpenAICodexTurnRelationDescendant,
		},
	}, string(CodexWireRequestTurn), CodexModelCapabilities{})
	require.NoError(t, err)
	require.Empty(t, descendantPlan.WireProfile.TurnLineage.RootTurnID.Value)
	require.Empty(t, descendantPlan.WireProfile.TurnLineage.ParentTurnID.Value)
}

func TestFinalizeCodexWireProfileUsesExplicitMetadataProfileWithoutOverwritingInbound(t *testing.T) {
	requestTurn := OpenAICodexRequestTurnSnapshot{
		ID: codexWireTestTurn, TypedID: CodexTurnID{Kind: CodexTurnIDUserUUIDv7, Value: codexWireTestTurn},
		StartedAtUnixMS: 1777777777123, Generated: true,
	}
	inbound := ParseCodexWireProfile(`{
		"agent_name":"inbound-agent",
		"sandbox":"inbound-sandbox",
		"sandbox_mode":"inbound-mode",
		"auto_review_enabled":false,
		"node_repl_auto_review_required":false,
		"node_repl_disabled":true,
		"workspaces":{"repo":{"has_changes":true}},
		"tool_namespaces_info":{"shell":{"name":"shell","functions":{}}}
	}`)
	plan, err := FinalizeOpenAICodexWirePlanWithOptions(OpenAIOAuthIdentityPlan{
		WireProfile: inbound, RequestTurn: requestTurn, TurnIdentityRequested: true,
	}, FinalizeOpenAICodexWirePlanOptions{
		RequestKind: string(CodexWireRequestTurn),
		ModelCapabilities: CodexModelCapabilities{
			Known: true, UseResponsesLite: true, NodeREPLAutoReviewRequired: true, NodeREPLDisabled: false,
		},
		MetadataProfile: CodexMetadataProfile{
			AgentName: "configured-agent", Sandbox: "configured-sandbox", SandboxMode: "configured-mode",
			AutoReviewEnabled: true, TurnMetadataIncludesToolInfo: true,
		},
	})
	require.NoError(t, err)
	require.Equal(t, "inbound-agent", plan.WireProfile.AgentName)
	require.Equal(t, "inbound-sandbox", plan.WireProfile.Sandbox)
	require.Equal(t, "inbound-mode", plan.WireProfile.SandboxMode)
	require.False(t, *plan.WireProfile.AutoReviewEnabled)
	require.True(t, *plan.WireProfile.NodeREPLAutoReviewRequired, "manifest node flags are authoritative")
	require.False(t, *plan.WireProfile.NodeREPLDisabled, "manifest node flags are authoritative")
	require.JSONEq(t, `{"repo":{"has_changes":true}}`, string(plan.WireProfile.Workspaces))
	require.JSONEq(t, `{"shell":{"name":"shell","functions":{}}}`, string(plan.WireProfile.ToolNamespacesInfo))
	require.True(t, plan.WireProfile.ToolNamespacesAllowed)
	require.True(t, plan.WireProfile.ToolNamespacesInfoAllowed)
}

func TestFinalizeCodexWireProfileMetadataProfileFillsOnlyGeneratedCompatFields(t *testing.T) {
	plan, err := FinalizeOpenAICodexWirePlanWithOptions(OpenAIOAuthIdentityPlan{
		RequestTurn: OpenAICodexRequestTurnSnapshot{
			ID: codexWireTestTurn, TypedID: CodexTurnID{Kind: CodexTurnIDUserUUIDv7, Value: codexWireTestTurn},
			StartedAtUnixMS: 1777777777123, Generated: true,
		},
		TurnIdentityRequested: true,
	}, FinalizeOpenAICodexWirePlanOptions{
		RequestKind:       string(CodexWireRequestTurn),
		ModelCapabilities: CodexModelCapabilities{Known: true, UseResponsesLite: true},
		MetadataProfile: CodexMetadataProfile{
			AgentName: "configured-agent", Sandbox: "configured-sandbox", SandboxMode: "configured-mode",
			AutoReviewEnabled: true,
		},
	})
	require.NoError(t, err)
	require.Equal(t, "configured-agent", plan.WireProfile.AgentName)
	require.Equal(t, "configured-sandbox", plan.WireProfile.Sandbox)
	require.Equal(t, "configured-mode", plan.WireProfile.SandboxMode)
	require.True(t, *plan.WireProfile.AutoReviewEnabled)
	require.Empty(t, plan.WireProfile.Workspaces, "workspace metadata is never generated")
	require.Empty(t, plan.WireProfile.ToolNamespacesInfo, "tool inventory is never generated")
	require.True(t, plan.WireProfile.ToolNamespacesAllowed, "Lite transport remains enabled")
	require.False(t, plan.WireProfile.ToolNamespacesInfoAllowed, "tool inventory requires explicit config opt-in")
}

func TestFinalizeCodexWireProfilePrewarmOmitsRequestTurnLineage(t *testing.T) {
	profile := ParseCodexWireProfile(fmt.Sprintf(
		`{"request_kind":"prewarm","turn_id":"internal:prewarm","parent_turn_id":"internal:parent","root_turn_id":"internal:root","turn_started_at_unix_ms":123}`,
	))
	plan, err := FinalizeOpenAICodexWirePlan(OpenAIOAuthIdentityPlan{
		WireProfile: profile,
		RequestTurn: OpenAICodexRequestTurnSnapshot{
			ID: "internal:prewarm", TypedID: CodexTurnID{Kind: CodexTurnIDOpaqueInternal, Value: "internal:prewarm"},
			StartedAtUnixMS: 123, Explicit: true,
		},
		TurnIdentityRequested: true,
	}, string(CodexWireRequestPrewarm), CodexModelCapabilities{})
	require.NoError(t, err)
	encoded, err := plan.WireProfile.MarshalNestedJSON(true)
	require.NoError(t, err)
	for _, field := range []string{"turn_id", "parent_turn_id", "root_turn_id", "turn_started_at_unix_ms"} {
		require.Empty(t, jsonRawField(t, encoded, field), field)
	}
	require.Equal(t, "internal:prewarm", plan.RequestTurn.ID, "compatible lifecycle snapshot remains available")
}

func TestFinalizeCodexWireProfileCompactionMetadataByProjection(t *testing.T) {
	validInbound := `{"trigger":"auto","reason":"context_limit","implementation":"responses","phase":"mid_turn","strategy":"prefix_compaction"}`
	tests := []struct {
		name           string
		mode           OpenAIOAuthIdentityProjectionMode
		compaction     string
		implementation string
		trigger        string
	}{
		{name: "regular default", mode: OpenAIOAuthIdentityProjectionRegular, implementation: CodexCompactionImplementationRemoteV2, trigger: "manual"},
		{name: "passthrough partial reserved object", mode: OpenAIOAuthIdentityProjectionPassthrough, compaction: `{"trigger":"auto"}`, implementation: CodexCompactionImplementationRemoteV2, trigger: "manual"},
		{name: "compact invalid reserved object", mode: OpenAIOAuthIdentityProjectionCompact, compaction: `{"trigger":"auto","reason":"context_limit","implementation":"unknown","phase":"mid_turn","strategy":"memento"}`, implementation: CodexCompactionImplementationLegacy, trigger: "manual"},
		{name: "unknown field rebuilds", mode: OpenAIOAuthIdentityProjectionRegular, compaction: `{"trigger":"auto","reason":"context_limit","implementation":"responses","phase":"mid_turn","strategy":"prefix_compaction","unknown":true}`, implementation: CodexCompactionImplementationRemoteV2, trigger: "manual"},
		{name: "fully valid inbound preserved", mode: OpenAIOAuthIdentityProjectionRegular, compaction: validInbound, implementation: CodexCompactionImplementationResponses, trigger: "auto"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := `{"request_kind":"compaction","turn_id":"internal:compact"`
			if test.compaction != "" {
				raw += `,"compaction":` + test.compaction
			}
			raw += `}`
			profile := ParseCodexWireProfile(raw)
			plan, err := FinalizeOpenAICodexWirePlan(OpenAIOAuthIdentityPlan{
				WireProfile: profile,
				RequestTurn: OpenAICodexRequestTurnSnapshot{
					ID: "internal:compact", TypedID: CodexTurnID{Kind: CodexTurnIDOpaqueInternal, Value: "internal:compact"}, Explicit: true,
				},
				TurnIdentityRequested: true, ProjectionMode: test.mode,
			}, string(CodexWireRequestCompaction), CodexModelCapabilities{})
			require.NoError(t, err)
			var metadata CodexCompactionTurnMetadata
			require.NoError(t, json.Unmarshal(plan.WireProfile.Compaction, &metadata))
			require.True(t, metadata.Valid())
			require.Equal(t, test.implementation, metadata.Implementation)
			require.Equal(t, test.trigger, metadata.Trigger)
			if test.trigger == "manual" {
				require.Equal(t, CodexCompactionDefaultReason, metadata.Reason)
				require.Equal(t, CodexCompactionDefaultPhase, metadata.Phase)
				require.Equal(t, CodexCompactionDefaultStrategy, metadata.Strategy)
			}
		})
	}
}

func TestCodexWireProfileToolPolicyUnknownFallbackAndFinalizedExtras(t *testing.T) {
	plan := codexWireProjectionTestPlan(t)
	finalPlan, err := FinalizeOpenAICodexWirePlan(plan, string(CodexWireRequestTurn), CodexModelCapabilities{})
	require.NoError(t, err)
	require.Empty(t, finalPlan.WireProfile.ToolNamespacesInfo)
	require.False(t, finalPlan.WireProfile.ToolNamespacesAllowed)

	projection := openAICodexMetadataProjectionFromPlan(finalPlan)
	rewritten, err := rewriteOpenAICodexTurnMetadataProjectionForCarrier(
		`{"installation_id":"attacker","late":"kept","tool_namespaces_info":{"evil":{}}}`,
		projection,
		true,
		true,
	)
	require.NoError(t, err)
	require.Equal(t, "kept", jsonStringField(t, rewritten, "late"))
	require.Empty(t, jsonRawField(t, rewritten, "tool_namespaces_info"))
	require.Equal(t, "installation-pin", jsonStringField(t, rewritten, "installation_id"))
}

func jsonStringField(t *testing.T, raw, field string) string {
	t.Helper()
	var object map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(raw), &object))
	var value string
	require.NoError(t, json.Unmarshal(object[field], &value))
	return value
}

func jsonRawField(t *testing.T, raw, field string) json.RawMessage {
	t.Helper()
	var object map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(raw), &object))
	return object[field]
}

func TestCodexWireCaptureAndPlanContextAreDeeplyImmutable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	nested := `{"request_kind":"compaction","turn_id":"internal:immutable","compaction":{"trigger":"manual","reason":"user_requested","implementation":"responses","phase":"standalone_turn","strategy":"memento"},"workspaces":{"repo":{"has_changes":true}},"tool_namespaces_info":{"shell":{"name":"shell","functions":{}}},"alpha":"original"}`
	capture := CaptureOpenAIOAuthIdentity(c, codexWireTestBody(t, nested, nil), "")
	wantProfile := cloneCodexWireProfile(capture.WireProfile)
	SetOpenAIOAuthIdentityCapture(c, capture)

	capture.WireProfile.ExtraMetadata["alpha"] = "mutated"
	capture.WireProfile.Compaction[0] = 'X'
	capture.WireProfile.Workspaces[0] = 'X'
	capture.WireProfile.ToolNamespacesInfo[0] = 'X'
	storedCapture, ok := OpenAIOAuthIdentityCaptureFromContext(c)
	require.True(t, ok)
	require.True(t, codexWireProfilesEqual(wantProfile, storedCapture.WireProfile))

	plan := OpenAIOAuthIdentityPlan{Capture: storedCapture, WireProfile: storedCapture.WireProfile}
	SetOpenAIOAuthIdentityPlan(c, plan)
	plan.WireProfile.ExtraMetadata["alpha"] = "plan-mutated"
	plan.WireProfile.Workspaces[0] = 'Y'
	storedPlan, ok := OpenAIOAuthIdentityPlanFromContext(c)
	require.True(t, ok)
	require.True(t, codexWireProfilesEqual(wantProfile, storedPlan.WireProfile))
	require.True(t, codexWireProfilesEqual(wantProfile, storedPlan.Capture.WireProfile))

	storedPlan.WireProfile.ExtraMetadata["alpha"] = "read-mutated"
	storedPlan.WireProfile.ToolNamespacesInfo[0] = 'Z'
	secondRead, ok := OpenAIOAuthIdentityPlanFromContext(c)
	require.True(t, ok)
	require.True(t, codexWireProfilesEqual(wantProfile, secondRead.WireProfile))
}

func TestCodexWireRoutingHintIsFrozenNonSerializedAndSoftForPlanDigest(t *testing.T) {
	base := codexWireProjectionTestPlan(t)
	priority, err := FinalizeOpenAICodexWirePlanWithOptions(base, FinalizeOpenAICodexWirePlanOptions{
		RequestKind: string(CodexWireRequestTurn), FinalModel: "gpt-5.4", FinalServiceTier: "fast",
	})
	require.NoError(t, err)
	flex, err := FinalizeOpenAICodexWirePlanWithOptions(base, FinalizeOpenAICodexWirePlanOptions{
		RequestKind: string(CodexWireRequestTurn), FinalModel: "gpt-5.4", FinalServiceTier: "flex",
	})
	require.NoError(t, err)
	require.Equal(t, "model=gpt-5.4;tier=priority", priority.WireProfile.RoutingHint)
	require.Equal(t, "model=gpt-5.4;tier=flex", flex.WireProfile.RoutingHint)
	require.Equal(t,
		openAIWSOutboundIdentityPlanDigest(http.Header{}, priority),
		openAIWSOutboundIdentityPlanDigest(http.Header{}, flex),
		"routing is soft affinity and must not force a new compatible socket",
	)
	encoded, err := priority.WireProfile.MarshalNestedJSON(true)
	require.NoError(t, err)
	require.NotContains(t, encoded, "routing")
	require.Empty(t, BuildOpenAICodexRoutingHint("bad=model", "priority"))
}

func TestCodexWireFlagOffProjectionRemainsByteExact(t *testing.T) {
	body := []byte(" { \"client_metadata\" : { \"agent_name\" : \"client\" } } ")
	headers := http.Header{"X-Codex-Turn-Metadata": {`{"agent_name":"client"}`}}
	wantHeaders := headers.Clone()
	plan, err := FinalizeOpenAICodexWirePlanWithOptions(OpenAIOAuthIdentityPlan{
		WireProfile: ParseCodexWireProfile(`{"agent_name":"client"}`),
	}, FinalizeOpenAICodexWirePlanOptions{
		RequestKind: string(CodexWireRequestTurn), FinalModel: "gpt-5.4", FinalServiceTier: "priority",
	})
	require.NoError(t, err)
	out, err := ApplyOpenAIOAuthIdentityPlan(headers, body, plan)
	require.NoError(t, err)
	require.Equal(t, body, out)
	require.Equal(t, wantHeaders, headers)
	require.False(t, plan.WireProfile.Finalized)
}
