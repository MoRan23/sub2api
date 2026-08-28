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
	"github.com/tidwall/gjson"
)

const (
	codexWireTestSession       = "01989f44-7c00-7000-8000-000000000001"
	codexWireTestThread        = "01989f44-7c00-7000-8000-000000000002"
	codexWireTestTurn          = "01989f44-7c00-7000-8000-000000000003"
	codexWireTestParentTurn    = "01989f44-7c00-7000-8000-000000000004"
	codexWireTestRootTurn      = "01989f44-7c00-7000-8000-000000000005"
	codexWireTestFork          = "01989f44-7c00-7000-8000-000000000006"
	codexWireTestContextWindow = "01989f44-7c00-7000-8000-000000000007"
	codexWireTestLocalCompact  = `{"request_kind":"compaction","turn_id":"01989f44-7c00-7000-8000-000000000003","compaction":{"trigger":"auto","reason":"model_downshift","implementation":"responses","phase":"pre_turn","strategy":"prefix_compaction"}}`
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
		"context_window_id":"01989f44-7c00-7000-8000-000000000099",
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
		Window: OpenAICodexWindowSnapshot{
			ThreadID: codexWireTestThread, ContextWindowID: codexWireTestContextWindow,
		},
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
			"context_window_id":              "attacker-flat",
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
	require.Equal(t, codexWireTestContextWindow, jsonStringField(t, bodyNested, "context_window_id"))
	require.NotContains(t, clientMetadata, "context_window_id", "context_window_id is nested-only")

	headerNested := headers.Get(openAIWSTurnMetadataHeader)
	require.Equal(t, readCodex3929Golden(t, "turn_header_nested.golden.json"), headerNested)
	require.True(t, isASCIIString(headerNested))
	require.NotContains(t, headerNested, "tool_namespaces_info")
	require.Equal(t, codexWireTestContextWindow, jsonStringField(t, headerNested, "context_window_id"))
	require.Equal(t, codexWireTestSession, headers.Get("session-id"))
	require.Equal(t, codexWireTestThread, headers.Get("thread-id"))
	require.Equal(t, codexWireTestThread+":0", headers.Get("x-codex-window-id"))
	require.Equal(t, "review", headers.Get("x-openai-subagent"))
}

func TestCodexWireContextWindowIDIsServerOwnedReservedAndNestedOnly(t *testing.T) {
	metadata := map[string]any{
		"request_kind":              "turn",
		"context_window_id":         "01989f44-7c00-7000-8000-000000000099",
		"context-window-id":         "attacker-alias",
		"x-codex-context-window-id": "attacker-header-alias",
		"x-codex-context_window_id": "attacker-mixed-alias",
	}
	for index := 0; index < 16; index++ {
		metadata[fmt.Sprintf("extra_%02d", index)] = fmt.Sprintf("value-%02d", index)
	}
	raw, err := json.Marshal(metadata)
	require.NoError(t, err)

	profile := ParseCodexWireProfile(string(raw))
	require.Empty(t, profile.ContextWindowID, "client metadata cannot seed the server-owned field")
	for _, key := range []string{
		"context_window_id", "context-window-id", "x-codex-context-window-id", "x-codex-context_window_id",
	} {
		require.NotContains(t, profile.ExtraMetadata, key)
	}
	require.Len(t, profile.ExtraMetadata, 16, "the reserved key must not consume the extra metadata budget")

	plan := OpenAIOAuthIdentityPlan{
		WireProfile: profile,
		TurnIdentity: OpenAICodexTurnIdentity{
			SessionID: codexWireTestSession, ThreadID: codexWireTestThread,
		},
		TurnIdentityEnabled: true,
	}
	bound, err := BindOpenAICodexWindowToPlan(plan, OpenAICodexWindowSnapshot{
		ThreadID: codexWireTestThread, ContextWindowID: codexWireTestContextWindow,
	}, strings.Repeat("a", 64))
	require.NoError(t, err)
	require.Equal(t, codexWireTestContextWindow, bound.WireProfile.ContextWindowID)

	encoded, err := bound.WireProfile.MarshalNestedJSON(true)
	require.NoError(t, err)
	require.Equal(t, codexWireTestContextWindow, jsonStringField(t, encoded, "context_window_id"))

	for _, kind := range []CodexWireRequestKind{CodexWireRequestPrewarm, CodexWireRequestMemory} {
		withoutContext := cloneCodexWireProfile(bound.WireProfile)
		withoutContext.RequestKind = kind
		encoded, err = withoutContext.MarshalNestedJSON(true)
		require.NoError(t, err)
		require.Empty(t, jsonRawField(t, encoded, "context_window_id"), string(kind))
	}

	malformed := cloneCodexWireProfile(bound.WireProfile)
	malformed.ContextWindowID = "not-a-uuid"
	encoded, err = malformed.MarshalNestedJSON(true)
	require.NoError(t, err)
	require.Empty(t, jsonRawField(t, encoded, "context_window_id"))
}

func TestCodexWireGuardianReviewProjection(t *testing.T) {
	plan := codexWireProjectionTestPlan(t)
	plan.WireProfile.ThreadSource = "guardian_review"
	plan.WireProfile.SubagentHeader = "guardian"
	plan.WireProfile.SubagentKind = ""
	finalPlan, err := FinalizeOpenAICodexWirePlan(
		plan,
		string(CodexWireRequestTurn),
		CodexModelCapabilities{Known: true, UseResponsesLite: true},
	)
	require.NoError(t, err)

	headers := make(http.Header)
	body := []byte(`{"model":"gpt-5.4","client_metadata":{}}`)
	out, err := ApplyOpenAIOAuthIdentityPlan(headers, body, finalPlan)
	require.NoError(t, err)
	require.Equal(t, "guardian", headers.Get("x-openai-subagent"))
	nested := gjson.GetBytes(out, "client_metadata.x-codex-turn-metadata").String()
	require.Equal(t, "guardian_review", gjson.Get(nested, "thread_source").String())
	require.Equal(t, codexWireTestContextWindow, gjson.Get(nested, "context_window_id").String())
}

func TestCodexWireCaptureArmsLocalResponsesOnlyFromStrictCanonicalCarrier(t *testing.T) {
	canonicalBody := codexWireTestBody(t, codexWireTestLocalCompact, nil)
	capture := CaptureOpenAIOAuthIdentity(nil, canonicalBody, "")
	require.Equal(t, CodexWireRequestCompaction, capture.WireProfile.RequestKind)
	require.Equal(t, CodexCompactionModeLocalResponses, capture.WireProfile.CompactionMode)
	metadata, valid := capture.WireProfile.localResponsesCompactionCandidate()
	require.True(t, valid)
	require.Equal(t, "auto", metadata.Trigger)
	require.Equal(t, "model_downshift", metadata.Reason)
	require.Equal(t, "pre_turn", metadata.Phase)
	require.Equal(t, "prefix_compaction", metadata.Strategy)

	t.Run("compatibility carriers cannot arm", func(t *testing.T) {
		newContext := func() *gin.Context {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			return c
		}
		tests := []struct {
			name    string
			capture func() OpenAIOAuthIdentityCapture
		}{
			{name: "header", capture: func() OpenAIOAuthIdentityCapture {
				c := newContext()
				c.Request.Header.Set(openAIWSTurnMetadataHeader, codexWireTestLocalCompact)
				return CaptureOpenAIOAuthIdentity(c, []byte(`{"model":"gpt-5.4"}`), "")
			}},
			{name: "root", capture: func() OpenAIOAuthIdentityCapture {
				body, err := json.Marshal(map[string]any{openAIWSTurnMetadataHeader: codexWireTestLocalCompact})
				require.NoError(t, err)
				return CaptureOpenAIOAuthIdentity(nil, body, "")
			}},
			{name: "explicit websocket", capture: func() OpenAIOAuthIdentityCapture {
				return CaptureOpenAIOAuthIdentityWithTurnMetadata(nil, []byte(`{"model":"gpt-5.4"}`), "", codexWireTestLocalCompact)
			}},
			{name: "flat", capture: func() OpenAIOAuthIdentityCapture {
				return CaptureOpenAIOAuthIdentity(nil, []byte(`{"client_metadata":{"request_kind":"compaction","compaction":{"trigger":"auto","reason":"model_downshift","implementation":"responses","phase":"pre_turn","strategy":"prefix_compaction"}}}`), "")
			}},
		}
		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				spoofed := test.capture()
				require.Equal(t, CodexCompactionModeNone, spoofed.WireProfile.CompactionMode)
				_, armed := spoofed.WireProfile.localResponsesCompactionCandidate()
				require.False(t, armed)
			})
		}
	})

	t.Run("canonical schema is exact", func(t *testing.T) {
		invalid := []struct {
			name   string
			nested string
		}{
			{name: "missing field", nested: `{"request_kind":"compaction","compaction":{"trigger":"auto","reason":"context_limit","implementation":"responses","phase":"mid_turn"}}`},
			{name: "unknown field", nested: `{"request_kind":"compaction","compaction":{"trigger":"auto","reason":"context_limit","implementation":"responses","phase":"mid_turn","strategy":"memento","extra":true}}`},
			{name: "wrong implementation", nested: `{"request_kind":"compaction","compaction":{"trigger":"auto","reason":"context_limit","implementation":"responses_compaction_v2","phase":"mid_turn","strategy":"memento"}}`},
			{name: "invalid enum", nested: `{"request_kind":"compaction","compaction":{"trigger":"automatic","reason":"context_limit","implementation":"responses","phase":"mid_turn","strategy":"memento"}}`},
			{name: "missing request kind", nested: `{"compaction":{"trigger":"auto","reason":"context_limit","implementation":"responses","phase":"mid_turn","strategy":"memento"}}`},
		}
		for _, test := range invalid {
			t.Run(test.name, func(t *testing.T) {
				candidate := CaptureOpenAIOAuthIdentity(nil, codexWireTestBody(t, test.nested, nil), "")
				require.Equal(t, CodexCompactionModeNone, candidate.WireProfile.CompactionMode)
			})
		}
		objectCarrier := []byte(`{"client_metadata":{"x-codex-turn-metadata":{"request_kind":"compaction","compaction":{"trigger":"auto","reason":"context_limit","implementation":"responses","phase":"mid_turn","strategy":"memento"}}}}`)
		require.Equal(t, CodexCompactionModeNone, CaptureOpenAIOAuthIdentity(nil, objectCarrier, "").WireProfile.CompactionMode)
	})

	t.Run("compatibility conflicts fail closed", func(t *testing.T) {
		newCapture := func(header string) OpenAIOAuthIdentityCapture {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			c.Request.Header.Set(openAIWSTurnMetadataHeader, header)
			return CaptureOpenAIOAuthIdentity(c, canonicalBody, "")
		}
		require.Equal(t, CodexCompactionModeLocalResponses, newCapture(codexWireTestLocalCompact).WireProfile.CompactionMode)
		require.Equal(t, CodexCompactionModeLocalResponses, newCapture(`{"thread_source":"guardian_review"}`).WireProfile.CompactionMode)
		for _, header := range []string{
			`{"request_kind":"compaction","compaction":{"trigger":"manual","reason":"model_downshift","implementation":"responses","phase":"pre_turn","strategy":"prefix_compaction"}}`,
			`{"request_kind":"compaction"}`,
			`not-json`,
		} {
			require.Equal(t, CodexCompactionModeNone, newCapture(header).WireProfile.CompactionMode, header)
		}

		var canonicalRoot map[string]any
		require.NoError(t, json.Unmarshal(canonicalBody, &canonicalRoot))
		clientMetadata := canonicalRoot["client_metadata"].(map[string]any)
		clientMetadata["request_kind"] = "compaction"
		clientMetadata["compaction"] = map[string]any{
			"trigger": "auto", "reason": "model_downshift", "implementation": "responses",
			"phase": "pre_turn", "strategy": "prefix_compaction",
		}
		consistentFlat, err := json.Marshal(canonicalRoot)
		require.NoError(t, err)
		require.Equal(t, CodexCompactionModeLocalResponses, CaptureOpenAIOAuthIdentity(nil, consistentFlat, "").WireProfile.CompactionMode)

		clientMetadata["compaction"].(map[string]any)["reason"] = "context_limit"
		conflictingFlat, err := json.Marshal(canonicalRoot)
		require.NoError(t, err)
		require.Equal(t, CodexCompactionModeNone, CaptureOpenAIOAuthIdentity(nil, conflictingFlat, "").WireProfile.CompactionMode)
		delete(clientMetadata, "compaction")
		incompleteFlat, err := json.Marshal(canonicalRoot)
		require.NoError(t, err)
		require.Equal(t, CodexCompactionModeNone, CaptureOpenAIOAuthIdentity(nil, incompleteFlat, "").WireProfile.CompactionMode)
	})

	t.Run("cross carrier assembly cannot arm", func(t *testing.T) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		c.Request.Header.Set(openAIWSTurnMetadataHeader, `{"compaction":{"trigger":"auto","reason":"model_downshift","implementation":"responses","phase":"pre_turn","strategy":"prefix_compaction"}}`)
		requestKindOnly := codexWireTestBody(t, `{"request_kind":"compaction"}`, nil)
		require.Equal(t, CodexCompactionModeNone, CaptureOpenAIOAuthIdentity(c, requestKindOnly, "").WireProfile.CompactionMode)

		c.Request.Header.Set(openAIWSTurnMetadataHeader, `{"request_kind":"compaction"}`)
		compactionOnly := codexWireTestBody(t, `{"compaction":{"trigger":"auto","reason":"model_downshift","implementation":"responses","phase":"pre_turn","strategy":"prefix_compaction"}}`, nil)
		require.Equal(t, CodexCompactionModeNone, CaptureOpenAIOAuthIdentity(c, compactionOnly, "").WireProfile.CompactionMode)
	})
}

func TestCodexWireMemoryRequestKindUsesNestedCarrierPriority(t *testing.T) {
	canonical := CaptureOpenAIOAuthIdentity(nil, codexWireTestBody(t,
		`{"request_kind":"memory","sandbox":"workspace-write"}`, nil), "")
	require.Equal(t, CodexWireRequestMemory, canonical.WireProfile.RequestKind)
	require.Empty(t, canonical.RequestTurn.ID)

	newContext := func() *gin.Context {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		return c
	}
	assertMemory := func(t *testing.T, capture OpenAIOAuthIdentityCapture) {
		t.Helper()
		require.Equal(t, CodexWireRequestMemory, capture.WireProfile.RequestKind)
		require.Empty(t, capture.RequestTurn.ID)
	}

	t.Run("header nested", func(t *testing.T) {
		c := newContext()
		c.Request.Header.Set(openAIWSTurnMetadataHeader, `{"request_kind":"memory","sandbox":"danger-full-access"}`)
		capture := CaptureOpenAIOAuthIdentity(c, []byte(`{"model":"gpt-5.4"}`), "")
		assertMemory(t, capture)
		require.Equal(t, "danger-full-access", capture.WireProfile.Sandbox)
	})
	t.Run("root nested", func(t *testing.T) {
		capture := CaptureOpenAIOAuthIdentity(nil,
			[]byte(`{"model":"gpt-5.4","x-codex-turn-metadata":"{\"request_kind\":\"memory\"}"}`), "")
		assertMemory(t, capture)
	})
	t.Run("flat client metadata", func(t *testing.T) {
		capture := CaptureOpenAIOAuthIdentity(nil,
			[]byte(`{"model":"gpt-5.4","client_metadata":{"request_kind":"memory"}}`), "")
		require.NotEqual(t, CodexWireRequestMemory, capture.WireProfile.RequestKind)
		require.NotEmpty(t, capture.RequestTurn.ID)
	})
	t.Run("explicit websocket nested", func(t *testing.T) {
		capture := CaptureOpenAIOAuthIdentityWithTurnMetadata(nil, []byte(`{"model":"gpt-5.4"}`), "",
			`{"request_kind":"memory"}`)
		assertMemory(t, capture)
	})
	t.Run("body nested wins over header nested", func(t *testing.T) {
		c := newContext()
		c.Request.Header.Set(openAIWSTurnMetadataHeader, `{"request_kind":"memory"}`)
		capture := CaptureOpenAIOAuthIdentity(c, codexWireTestBody(t,
			`{"request_kind":"turn","turn_id":"`+codexWireTestTurn+`"}`, nil), "")
		require.Equal(t, CodexWireRequestTurn, capture.WireProfile.RequestKind)
		require.Equal(t, codexWireTestTurn, capture.RequestTurn.ID)
	})
	t.Run("stage two memgen remains a turn", func(t *testing.T) {
		c := newContext()
		c.Request.Header.Set("x-openai-memgen-request", "true")
		c.Request.Header.Set("x-openai-subagent", "memory_consolidation")
		capture := CaptureOpenAIOAuthIdentity(c, codexWireTestBody(t,
			`{"request_kind":"turn","thread_source":"subagent"}`, nil), "")
		require.Equal(t, CodexWireRequestTurn, capture.WireProfile.RequestKind)
		require.Equal(t, "memory_consolidation", capture.WireProfile.SubagentHeader)
		require.NotEmpty(t, capture.RequestTurn.ID)
	})
}

func TestResolveOpenAICodexWireRequestKindMemoryConflicts(t *testing.T) {
	kind, err := resolveOpenAICodexWireRequestKind(CodexWireRequestMemory, CodexWireRequestTurn, "")
	require.NoError(t, err)
	require.Equal(t, CodexWireRequestMemory, kind)

	for _, forced := range []CodexWireRequestKind{
		CodexWireRequestTurn,
		CodexWireRequestPrewarm,
		CodexWireRequestCompaction,
	} {
		t.Run(string(forced), func(t *testing.T) {
			_, kindErr := resolveOpenAICodexWireRequestKind(CodexWireRequestMemory, CodexWireRequestTurn, forced)
			require.ErrorIs(t, kindErr, ErrOpenAICodexRequestKindConflict)
		})
	}

	kind, err = resolveOpenAICodexWireRequestKind("", CodexWireRequestTurn, CodexWireRequestCompaction)
	require.NoError(t, err)
	require.Equal(t, CodexWireRequestCompaction, kind)
}

func TestCodexWireMemoryProjectionKeepsStableTupleWithoutTurnIdentity(t *testing.T) {
	plan := codexWireProjectionTestPlan(t)
	plan.WireProfile.RequestKind = CodexWireRequestMemory
	plan.WireProfile.SubagentHeader = "memory_consolidation"
	plan.WireProfile.SubagentKind = ""
	plan.WireProfile.Compaction = json.RawMessage(`{"trigger":"auto"}`)
	finalPlan, err := FinalizeOpenAICodexWirePlanWithOptions(plan, FinalizeOpenAICodexWirePlanOptions{
		RequestKind:       string(CodexWireRequestMemory),
		ModelCapabilities: CodexModelCapabilities{Known: true, UseResponsesLite: true, NodeREPLAutoReviewRequired: true, NodeREPLDisabled: true},
		MetadataProfile:   CodexMetadataProfile{TurnMetadataIncludesToolInfo: true},
		FinalModel:        "gpt-5.4",
	})
	require.NoError(t, err)
	require.Empty(t, finalPlan.RequestTurn.ID)
	require.Equal(t, CodexWireRequestMemory, finalPlan.WireProfile.RequestKind)
	require.Equal(t, codexWireTestSession, finalPlan.WireProfile.SessionID)
	require.Equal(t, codexWireTestThread, finalPlan.WireProfile.ThreadID)
	require.Equal(t, codexWireTestThread+":0", finalPlan.WireProfile.WindowID)
	require.Empty(t, finalPlan.WireProfile.ContextWindowID)
	require.Empty(t, finalPlan.WireProfile.AgentName)
	require.Empty(t, finalPlan.WireProfile.TurnID.Value)
	require.Empty(t, finalPlan.WireProfile.TurnLineage)
	require.False(t, finalPlan.WireProfile.TurnStartedAtSet)
	require.Empty(t, finalPlan.WireProfile.Compaction)

	headers := http.Header{
		"Session-Id":               {"stale-session"},
		"Thread-Id":                {"stale-thread"},
		"X-Client-Request-Id":      {"stale-request"},
		"X-Codex-Parent-Thread-Id": {"stale-parent"},
		"X-Openai-Memgen-Request":  {"true"},
		"X-Openai-Subagent":        {"memory_consolidation"},
		openAIWSTurnMetadataHeader: {`{"request_kind":"memory","turn_id":"internal:stale","header_extra":"keep"}`},
	}
	body := codexWireTestBody(t,
		`{"request_kind":"memory","turn_id":"internal:stale","turn_started_at_unix_ms":1,"parent_turn_id":"internal:parent","incoming_extra":"keep"}`,
		map[string]any{
			"turn_id":                       "stale-flat-turn",
			"turn_started_at_unix_ms":       1,
			"parent_turn_id":                "stale-flat-parent",
			"root_turn_id":                  "stale-flat-root",
			"x-codex-parent-thread-id":      "stale-flat-parent-thread",
			"unrelated_compatibility_field": "keep",
		},
	)
	out, err := ApplyOpenAIOAuthIdentityPlan(headers, body, finalPlan)
	require.NoError(t, err)

	var root map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(out, &root))
	var flat map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(root["client_metadata"], &flat))
	require.Equal(t, "installation-pin", jsonStringRaw(t, flat[codexInstallationIDKey]))
	require.Equal(t, codexWireTestSession, jsonStringRaw(t, flat["session_id"]))
	require.Equal(t, codexWireTestThread, jsonStringRaw(t, flat["thread_id"]))
	require.Equal(t, codexWireTestThread+":0", jsonStringRaw(t, flat["x-codex-window-id"]))
	require.Equal(t, "memory_consolidation", jsonStringRaw(t, flat["x-openai-subagent"]))
	require.Equal(t, "keep", jsonStringRaw(t, flat["unrelated_compatibility_field"]))
	for _, key := range []string{"turn_id", "turn_started_at_unix_ms", "parent_turn_id", "root_turn_id", "x-codex-parent-thread-id"} {
		require.NotContains(t, flat, key)
	}

	var nestedRaw string
	require.NoError(t, json.Unmarshal(flat[openAIWSTurnMetadataHeader], &nestedRaw))
	var nested map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(nestedRaw), &nested))
	require.Equal(t, "memory", jsonStringRaw(t, nested["request_kind"]))
	require.Equal(t, "keep", jsonStringRaw(t, nested["incoming_extra"]))
	for _, key := range []string{
		"installation_id", "session_id", "thread_id", "agent_name", "turn_id", "window_id", "context_window_id",
		"forked_from_thread_id", "parent_thread_id", "parent_turn_id", "root_turn_id",
		"turn_started_at_unix_ms", "compaction",
	} {
		require.NotContains(t, nested, key)
	}

	require.Equal(t, "installation-pin", headers.Get(codexInstallationIDKey))
	require.Equal(t, codexWireTestSession, headers.Get("session-id"))
	require.Equal(t, codexWireTestThread, headers.Get("thread-id"))
	require.Equal(t, codexWireTestThread, headers.Get("x-client-request-id"))
	require.Equal(t, codexWireTestThread+":0", headers.Get("x-codex-window-id"))
	require.Empty(t, headers.Get("x-codex-parent-thread-id"))
	require.Equal(t, "true", headers.Get("x-openai-memgen-request"))
	require.Equal(t, "memory_consolidation", headers.Get("x-openai-subagent"))
	var headerNested map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(headers.Get(openAIWSTurnMetadataHeader)), &headerNested))
	require.Equal(t, "memory", jsonStringRaw(t, headerNested["request_kind"]))
	for _, key := range []string{"installation_id", "session_id", "thread_id", "agent_name", "turn_id", "window_id", "context_window_id", "parent_thread_id", "turn_started_at_unix_ms"} {
		require.NotContains(t, headerNested, key)
	}
}

func jsonStringRaw(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var value string
	require.NoError(t, json.Unmarshal(raw, &value))
	return value
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
		Window: OpenAICodexWindowSnapshot{
			ThreadID: codexWireTestSession, ContextWindowID: codexWireTestContextWindow,
		},
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
	require.Equal(t, codexWireTestContextWindow, rootPlan.WireProfile.ContextWindowID)

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
		TurnIdentityRequested: true, TurnIdentityEnabled: true,
		TurnIdentity: OpenAICodexTurnIdentity{
			SessionID: codexWireTestSession, ThreadID: codexWireTestThread,
		},
		Window: OpenAICodexWindowSnapshot{
			ThreadID: codexWireTestThread, ContextWindowID: codexWireTestContextWindow,
		},
	}, string(CodexWireRequestPrewarm), CodexModelCapabilities{})
	require.NoError(t, err)
	encoded, err := plan.WireProfile.MarshalNestedJSON(true)
	require.NoError(t, err)
	for _, field := range []string{"turn_id", "parent_turn_id", "root_turn_id", "turn_started_at_unix_ms"} {
		require.Empty(t, jsonRawField(t, encoded, field), field)
	}
	require.Equal(t, codexWireTestThread+":0", jsonStringField(t, encoded, "window_id"))
	require.Empty(t, jsonRawField(t, encoded, "context_window_id"))
	require.Empty(t, plan.WireProfile.ContextWindowID)
	require.Equal(t, "internal:prewarm", plan.RequestTurn.ID, "compatible lifecycle snapshot remains available")
}

func TestFinalizeCodexWireProfileCompactionMetadataByProjection(t *testing.T) {
	validInbound := `{"trigger":"auto","reason":"context_limit","implementation":"responses","phase":"mid_turn","strategy":"prefix_compaction"}`
	tests := []struct {
		name           string
		mode           OpenAIOAuthIdentityProjectionMode
		compactionMode CodexCompactionMode
		compaction     string
		implementation string
		trigger        string
	}{
		{name: "regular default", mode: OpenAIOAuthIdentityProjectionRegular, compactionMode: CodexCompactionModeRemoteV2, implementation: CodexCompactionImplementationRemoteV2, trigger: "manual"},
		{name: "passthrough partial reserved object", mode: OpenAIOAuthIdentityProjectionPassthrough, compactionMode: CodexCompactionModeRemoteV2, compaction: `{"trigger":"auto"}`, implementation: CodexCompactionImplementationRemoteV2, trigger: "manual"},
		{name: "compact invalid reserved object", mode: OpenAIOAuthIdentityProjectionCompact, compactionMode: CodexCompactionModeLegacy, compaction: `{"trigger":"auto","reason":"context_limit","implementation":"unknown","phase":"mid_turn","strategy":"memento"}`, implementation: CodexCompactionImplementationLegacy, trigger: "manual"},
		{name: "unknown field rebuilds", mode: OpenAIOAuthIdentityProjectionRegular, compactionMode: CodexCompactionModeRemoteV2, compaction: `{"trigger":"auto","reason":"context_limit","implementation":"responses","phase":"mid_turn","strategy":"prefix_compaction","unknown":true}`, implementation: CodexCompactionImplementationRemoteV2, trigger: "manual"},
		{name: "fully valid local inbound preserved", mode: OpenAIOAuthIdentityProjectionRegular, compactionMode: CodexCompactionModeLocalResponses, compaction: validInbound, implementation: CodexCompactionImplementationResponses, trigger: "auto"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := `{"request_kind":"compaction","turn_id":"internal:compact"`
			if test.compaction != "" {
				raw += `,"compaction":` + test.compaction
			}
			raw += `}`
			profile := ParseCodexWireProfile(raw)
			plan, err := FinalizeOpenAICodexWirePlanWithOptions(OpenAIOAuthIdentityPlan{
				WireProfile: profile,
				RequestTurn: OpenAICodexRequestTurnSnapshot{
					ID: "internal:compact", TypedID: CodexTurnID{Kind: CodexTurnIDOpaqueInternal, Value: "internal:compact"}, Explicit: true,
				},
				TurnIdentityRequested: true, ProjectionMode: test.mode,
			}, FinalizeOpenAICodexWirePlanOptions{
				RequestKind: string(CodexWireRequestCompaction), CompactionMode: test.compactionMode,
			})
			require.NoError(t, err)
			require.Equal(t, test.compactionMode, plan.WireProfile.CompactionMode)
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

func TestFinalizeCodexWireProfileAcceptsAllCompactionMetadataValues(t *testing.T) {
	implementations := []struct {
		implementation string
		mode           CodexCompactionMode
	}{
		{implementation: CodexCompactionImplementationResponses, mode: CodexCompactionModeLocalResponses},
		{implementation: CodexCompactionImplementationRemoteV2, mode: CodexCompactionModeRemoteV2},
		{implementation: CodexCompactionImplementationLegacy, mode: CodexCompactionModeLegacy},
	}
	for _, implementationCase := range implementations {
		implementation := implementationCase.implementation
		for _, trigger := range []string{"manual", "auto"} {
			for _, reason := range []string{"user_requested", "context_limit", "model_downshift", "comp_hash_changed"} {
				for _, phase := range []string{"standalone_turn", "pre_turn", "mid_turn"} {
					for _, strategy := range []string{"memento", "prefix_compaction"} {
						metadata := CodexCompactionTurnMetadata{
							Trigger: trigger, Reason: reason, Implementation: implementation,
							Phase: phase, Strategy: strategy,
						}
						name := strings.Join([]string{implementation, trigger, reason, phase, strategy}, "/")
						t.Run(name, func(t *testing.T) {
							require.True(t, metadata.Valid())
							raw := marshalCodexCompactionMetadata(metadata)
							profile := ParseCodexWireProfile(
								`{"request_kind":"compaction","turn_id":"internal:compact-enum","compaction":` + string(raw) + `}`,
							)
							plan, err := FinalizeOpenAICodexWirePlanWithOptions(OpenAIOAuthIdentityPlan{
								WireProfile: profile,
								RequestTurn: OpenAICodexRequestTurnSnapshot{
									ID:       "internal:compact-enum",
									TypedID:  CodexTurnID{Kind: CodexTurnIDOpaqueInternal, Value: "internal:compact-enum"},
									Explicit: true,
								},
								TurnIdentityRequested: true,
							}, FinalizeOpenAICodexWirePlanOptions{
								RequestKind: string(CodexWireRequestCompaction), CompactionMode: implementationCase.mode,
							})
							require.NoError(t, err)
							require.Equal(t, implementationCase.mode, plan.WireProfile.CompactionMode)
							var projected CodexCompactionTurnMetadata
							require.NoError(t, json.Unmarshal(plan.WireProfile.Compaction, &projected))
							require.Equal(t, metadata, projected)
						})
					}
				}
			}
		}
	}
}

func TestFinalizeCodexWireProfileDoesNotArmCompactionWithoutValidServerMode(t *testing.T) {
	profile := ParseCodexWireProfile(codexWireTestLocalCompact)
	base := OpenAIOAuthIdentityPlan{
		WireProfile: profile,
		RequestTurn: OpenAICodexRequestTurnSnapshot{
			ID: codexWireTestTurn, TypedID: CodexTurnID{Kind: CodexTurnIDUserUUIDv7, Value: codexWireTestTurn}, Explicit: true,
		},
		TurnIdentityRequested: true,
	}
	for _, mode := range []CodexCompactionMode{CodexCompactionModeNone, CodexCompactionMode("invalid")} {
		name := string(mode)
		if name == "" {
			name = "missing"
		}
		t.Run(name, func(t *testing.T) {
			plan, err := FinalizeOpenAICodexWirePlanWithOptions(base, FinalizeOpenAICodexWirePlanOptions{
				RequestKind: string(CodexWireRequestCompaction), CompactionMode: mode,
			})
			require.NoError(t, err)
			require.Equal(t, CodexCompactionModeNone, plan.WireProfile.CompactionMode)
			_, armed := OpenAICodexCompactionModeForFinalizedPlan(plan)
			require.False(t, armed)
		})
	}

	armed, err := FinalizeOpenAICodexWirePlanWithOptions(base, FinalizeOpenAICodexWirePlanOptions{
		RequestKind: string(CodexWireRequestCompaction), CompactionMode: CodexCompactionModeLocalResponses,
	})
	require.NoError(t, err)
	mode, valid := OpenAICodexCompactionModeForFinalizedPlan(armed)
	require.True(t, valid)
	require.Equal(t, CodexCompactionModeLocalResponses, mode)

	armed.WireProfile.Compaction = marshalCodexCompactionMetadata(DefaultCodexCompactionTurnMetadata(CodexCompactionImplementationRemoteV2))
	_, valid = OpenAICodexCompactionModeForFinalizedPlan(armed)
	require.False(t, valid, "mode and nested implementation must agree")
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
