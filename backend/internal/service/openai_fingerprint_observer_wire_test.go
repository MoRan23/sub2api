package service

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestFingerprintObservationWindowMetadataFinalWireHTTPAndWS(t *testing.T) {
	metadata := map[string]any{
		"session_id": fingerprintObserverSessionV7, "thread_id": fingerprintObserverThreadV7,
		"window_id": fingerprintObserverThreadV7 + ":0", "window_number": 0,
		"context_window_id":                    codexWireTestContextWindow,
		"guardian_classifier_source_thread_id": codexWireTestFork,
		"turn_id":                              codexWireTestTurn, "turn_started_at_unix_ms": 123,
		"parent_turn_id": codexWireTestParentTurn, "root_turn_id": codexWireTestRootTurn,
		"forked_from_ordinal_exclusive": 0, "agent_name": "wire-agent",
		"thread_source": "guardian_classifier", "subagent_kind": "review",
		"sandbox": "workspace-write", "auto_review_enabled": false,
		"node_repl_auto_review_required": false, "node_repl_disabled": true,
		"request_kind": "turn", "history_ingest_requested": false,
		"annotation": "outgoing",
	}
	nested, err := json.Marshal(metadata)
	require.NoError(t, err)
	flat := map[string]any{
		"session_id": fingerprintObserverSessionV7, "thread_id": fingerprintObserverThreadV7,
		"window_id": metadata["window_id"], "window_number": 0,
		"context_window_id":                    codexWireTestContextWindow,
		"guardian_classifier_source_thread_id": codexWireTestFork,
	}
	flatBody, err := json.Marshal(map[string]any{"type": "response.create", "client_metadata": flat})
	require.NoError(t, err)
	rootBody, err := json.Marshal(map[string]any{openAIWSTurnMetadataHeader: metadata})
	require.NoError(t, err)
	for _, transport := range []string{"http", "ws"} {
		for _, test := range []struct {
			name   string
			body   []byte
			header string
			full   bool
		}{
			{"canonical", fingerprintMetadataTestBody(t, metadata), "", true},
			{"flat", flatBody, "", false},
			{"compatibility", rootBody, "", true},
			{"header", nil, string(nested), true},
		} {
			t.Run(transport+"/"+test.name, func(t *testing.T) {
				SetFingerprintObservationEnabled(false)
				SetFingerprintObservationEnabled(true)
				t.Cleanup(func() { SetFingerprintObservationEnabled(false) })
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
				account := newOpenAIOAuthPinAccount(19021, nil)
				identity := OpenAICodexTurnIdentity{
					SessionID: fingerprintObserverSessionV7, ThreadID: fingerprintObserverThreadV7,
					GuardianClassifierSourceThreadID: codexWireTestFork,
				}
				setFingerprintObservationOutboundIdentity(c, identity)
				headers := http.Header{"User-Agent": {"wire-user-agent"}}
				if test.header != "" {
					headers.Set(openAIWSTurnMetadataHeader, test.header)
				}
				before := append([]byte(nil), test.body...)
				svc := &OpenAIGatewayService{}
				if transport == "http" {
					svc.recordFingerprintObservationWithBody(c, account, installationIDResolution{}, headers, test.body)
				} else {
					svc.recordFingerprintObservationWSFrame(c, account, nil, test.body, headers,
						&OpenAIOAuthIdentityPlan{TurnIdentityEnabled: true, TurnIdentity: identity})
				}
				entries := SnapshotFingerprintObservations(1)
				require.Len(t, entries, 1)
				entry := entries[0]
				require.Equal(t, uint64Pointer(0), entry.WindowNumber)
				require.Equal(t, metadata["window_id"], entry.WindowID)
				require.Equal(t, codexWireTestContextWindow, entry.ContextWindowID)
				require.Equal(t, codexWireTestFork, entry.GuardianClassifierSourceThreadID)
				require.Equal(t, "wire-user-agent", entry.UserAgent)
				if test.full {
					require.Equal(t, codexWireTestTurn, entry.TurnID)
					require.Equal(t, int64(123), entry.TurnStartedAtUnixMS)
					require.Equal(t, codexWireTestParentTurn, entry.ParentTurnID)
					require.Equal(t, codexWireTestRootTurn, entry.RootTurnID)
					require.Equal(t, uint64Pointer(0), entry.ForkedFromOrdinalExclusive)
					require.Equal(t, "wire-agent", entry.AgentName)
					require.Equal(t, boolPointer(false), entry.AutoReviewEnabled)
					require.Equal(t, boolPointer(false), entry.NodeREPLAutoReviewRequired)
					require.Equal(t, boolPointer(true), entry.NodeREPLDisabled)
					require.Equal(t, CodexWireRequestTurn, entry.RequestKind)
					require.Equal(t, boolPointer(false), entry.HistoryIngestRequested)
					require.Equal(t, map[string]string{"annotation": fingerprintExtraMetadataRedacted}, entry.ExtraMetadata)
				}
				encoded, err := json.Marshal(entry)
				require.NoError(t, err)
				require.Contains(t, string(encoded), `"window_number":0`)
				require.Equal(t, before, test.body, "observation must not modify the outgoing body")
			})
		}
	}
}

func TestFingerprintObservationWindowCarrierPrecedence(t *testing.T) {
	for _, test := range []struct {
		name, canonical, compatibility, header string
		flat, root, want                       uint64
	}{
		{"canonical zero wins", `{"window_number":0}`, `{"window_number":1}`, `{"window_number":2}`, 3, 4, 0},
		{"compatibility body fills missing", `{}`, `{"window_number":1}`, `{"window_number":2}`, 3, 4, 1},
		{"final header precedes flat", `{}`, `{}`, `{"window_number":2}`, 3, 4, 2},
		{"flat client precedes flat root", `{}`, `{}`, `{}`, 3, 4, 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			body, err := json.Marshal(map[string]any{
				"client_metadata":          map[string]any{openAIWSTurnMetadataHeader: test.canonical, "window_number": test.flat},
				openAIWSTurnMetadataHeader: json.RawMessage(test.compatibility), "window_number": test.root,
			})
			require.NoError(t, err)
			profile := finalFingerprintCodexWireProfile(http.Header{openAIWSTurnMetadataHeader: {test.header}}, body)
			require.Equal(t, uint64Pointer(test.want), profile.WindowNumber)
		})
	}
	body := codexWireTestBody(t, `{"context_window_id":"`+codexWireTestContextWindow+`","agent_name":"canonical","annotation":"canonical"}`, map[string]any{
		"context_window_id": codexWireTestThread,
	})
	profile := finalFingerprintCodexWireProfile(http.Header{openAIWSTurnMetadataHeader: {
		`{"context_window_id":"` + codexWireTestFork + `","agent_name":"header","annotation":"header"}`,
	}}, body)
	require.Equal(t, codexWireTestContextWindow, profile.ContextWindowID)
	require.Equal(t, "canonical", profile.AgentName)
	require.Equal(t, map[string]string{"annotation": "canonical"}, profile.ExtraMetadata)
}

func TestFingerprintObservationBodyOnlyWindowMetadata(t *testing.T) {
	body := fingerprintMetadataTestBody(t, map[string]any{
		"window_number": 0, "context_window_id": codexWireTestContextWindow,
		"guardian_classifier_source_thread_id": codexWireTestFork, "annotation": "outgoing",
	})
	entry := buildFingerprintObservationEntry(nil, newOpenAIOAuthPinAccount(19022, nil), installationIDResolution{},
		nil, body, OpenAICodexTurnIdentity{SessionID: codexWireTestSession, ThreadID: codexWireTestThread}, true, false)
	require.Equal(t, uint64Pointer(0), entry.WindowNumber)
	require.Equal(t, codexWireTestContextWindow, entry.ContextWindowID)
	require.Equal(t, codexWireTestFork, entry.GuardianClassifierSourceThreadID)
	require.Equal(t, map[string]string{"annotation": fingerprintExtraMetadataRedacted}, entry.ExtraMetadata)
}

func TestFingerprintObservationSafeTurnMetadataPreservesBoundedWireFields(t *testing.T) {
	metadata := map[string]any{
		"window_id": codexWireTestThread + ":0", "window_number": 0, "context_window_id": codexWireTestContextWindow,
		"guardian_classifier_source_thread_id": codexWireTestFork, "turn_id": codexWireTestTurn,
		"parent_turn_id": codexWireTestParentTurn, "root_turn_id": codexWireTestRootTurn,
		"turn_started_at_unix_ms": 123, "forked_from_ordinal_exclusive": 0,
		"agent_name": "wire-agent", "subagent_kind": "review", "thread_source": "guardian_classifier",
		"turn_trigger": "retry", "sandbox": "workspace-write", "sandbox_mode": "workspace-write",
		"auto_review_enabled": false, "node_repl_auto_review_required": false, "node_repl_disabled": true,
		"workspaces":   map[string]any{"/workspace": map[string]any{"arbitrary": "must-not-retain"}},
		"request_kind": "turn", "history_ingest_requested": false,
		"complex": map[string]any{"credentials": "must-not-retain"}, "oversized": strings.Repeat("x", 129),
	}
	for index := 0; index < 18; index++ {
		metadata[fmt.Sprintf("extra_%02d", index)] = fmt.Sprintf("value-%02d", index)
	}
	encoded, err := json.Marshal(metadata)
	require.NoError(t, err)
	headers := http.Header{"Authorization": {"must-not-retain"}, openAIWSTurnMetadataHeader: {string(encoded)}, "x-openai-subagent": {"review"}}
	frozen := cloneFingerprintObservationHeaders(headers)
	profile := finalFingerprintCodexWireProfile(frozen, nil)
	require.Equal(t, uint64Pointer(0), profile.WindowNumber)
	require.Equal(t, codexWireTestContextWindow, profile.ContextWindowID)
	require.Equal(t, codexWireTestFork, profile.GuardianClassifierSourceThreadID)
	require.Equal(t, codexWireTestTurn, profile.TurnID.Value)
	require.Equal(t, codexWireTestParentTurn, profile.TurnLineage.ParentTurnID.Value)
	require.Equal(t, codexWireTestRootTurn, profile.TurnLineage.RootTurnID.Value)
	require.Equal(t, int64(123), profile.TurnStartedAtUnixMS)
	require.Equal(t, uint64Pointer(0), profile.TurnLineage.ForkedFromOrdinalExclusive)
	require.Equal(t, "wire-agent", profile.AgentName)
	require.Equal(t, "review", profile.SubagentHeader)
	require.Equal(t, boolPointer(false), profile.AutoReviewEnabled)
	require.Equal(t, boolPointer(false), profile.NodeREPLAutoReviewRequired)
	require.Equal(t, boolPointer(true), profile.NodeREPLDisabled)
	require.Equal(t, []string{"/workspace"}, displayableCodexWorkspaces(profile.Workspaces))
	require.Len(t, profile.ExtraMetadata, 16)
	require.NotContains(t, profile.ExtraMetadata, "extra_16")
	require.NotContains(t, profile.ExtraMetadata, "oversized")
	retained, err := json.Marshal(frozen)
	require.NoError(t, err)
	require.NotContains(t, string(retained), "must-not-retain")
	require.Equal(t, string(encoded), headers[openAIWSTurnMetadataHeader][0])
}

func TestFingerprintObservationWireMetadataSnapshotOwnership(t *testing.T) {
	observer := &fingerprintObserver{ring: make([]FingerprintObservationEntry, 1)}
	observer.enabled.Store(true)
	entry := FingerprintObservationEntry{
		WindowNumber: uint64Pointer(0), ForkedFromOrdinalExclusive: uint64Pointer(0),
		AutoReviewEnabled: boolPointer(false), NodeREPLAutoReviewRequired: boolPointer(false), NodeREPLDisabled: boolPointer(false),
		Workspaces: []string{"/workspace"}, ExtraMetadata: map[string]string{"annotation": "outgoing"},
	}
	observer.record(entry)
	*entry.WindowNumber, *entry.ForkedFromOrdinalExclusive = 99, 99
	*entry.AutoReviewEnabled, *entry.NodeREPLAutoReviewRequired, *entry.NodeREPLDisabled = true, true, true
	entry.Workspaces[0], entry.ExtraMetadata["annotation"] = "mutated", "mutated"
	snapshot := observer.snapshot(1)
	require.Equal(t, uint64Pointer(0), snapshot[0].WindowNumber)
	require.Equal(t, uint64Pointer(0), snapshot[0].ForkedFromOrdinalExclusive)
	require.Equal(t, boolPointer(false), snapshot[0].AutoReviewEnabled)
	require.Equal(t, boolPointer(false), snapshot[0].NodeREPLAutoReviewRequired)
	require.Equal(t, boolPointer(false), snapshot[0].NodeREPLDisabled)
	require.Equal(t, []string{"/workspace"}, snapshot[0].Workspaces)
	require.Equal(t, map[string]string{"annotation": fingerprintExtraMetadataRedacted}, snapshot[0].ExtraMetadata)
	*snapshot[0].WindowNumber = 100
	snapshot[0].Workspaces[0], snapshot[0].ExtraMetadata["annotation"] = "changed snapshot", "changed snapshot"
	require.Equal(t, uint64Pointer(0), observer.snapshot(1)[0].WindowNumber)
	require.Equal(t, []string{"/workspace"}, observer.snapshot(1)[0].Workspaces)
	require.Equal(t, map[string]string{"annotation": fingerprintExtraMetadataRedacted}, observer.snapshot(1)[0].ExtraMetadata)
}

func TestFingerprintObservationExtraMetadataRedaction(t *testing.T) {
	metadata := map[string]any{
		"request_kind": "turn", "window_number": 0,
		"access_token": "raw-secret-access-token", "authorization": "raw-secret-authorization",
		"prompt": "raw-private-prompt", "annotation": "raw-unreviewed-value",
	}
	nested, err := json.Marshal(metadata)
	require.NoError(t, err)
	body := fingerprintMetadataTestBody(t, metadata)
	originalBody := append([]byte(nil), body...)
	headers := http.Header{openAIWSTurnMetadataHeader: {string(nested)}}
	originalHeaders := headers.Clone()
	want := map[string]string{
		"access_token": fingerprintExtraMetadataRedacted, "authorization": fingerprintExtraMetadataRedacted,
		"prompt": fingerprintExtraMetadataRedacted, "annotation": fingerprintExtraMetadataRedacted,
	}
	for _, test := range []struct {
		name    string
		headers http.Header
		body    []byte
	}{
		{"http body", nil, body},
		{"ws frame", headers, body},
		{"ws frozen header", cloneFingerprintObservationHeaders(headers), nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			entry := buildFingerprintObservationEntry(nil, newOpenAIOAuthPinAccount(19023, nil), installationIDResolution{},
				test.headers, test.body, OpenAICodexTurnIdentity{}, false, false)
			require.Equal(t, want, entry.ExtraMetadata)
			observer := &fingerprintObserver{ring: make([]FingerprintObservationEntry, 1)}
			observer.enabled.Store(true)
			observer.record(entry)
			entry.ExtraMetadata["prompt"] = "caller-mutated-prompt"
			snapshot := observer.snapshot(1)
			require.Equal(t, want, snapshot[0].ExtraMetadata)
			api, err := json.Marshal(FingerprintObservationEntryPage{Items: snapshot, Total: 1})
			require.NoError(t, err)
			frozen, err := json.Marshal(test.headers)
			require.NoError(t, err)
			for _, value := range []string{"raw-secret-access-token", "raw-secret-authorization", "raw-private-prompt", "raw-unreviewed-value", "caller-mutated-prompt"} {
				require.NotContains(t, string(api), value)
				if test.name == "ws frozen header" {
					require.NotContains(t, string(frozen), value)
				}
			}
		})
	}
	// Recording a caller-owned entry also applies redaction at the ring boundary.
	observer := &fingerprintObserver{ring: make([]FingerprintObservationEntry, 1)}
	observer.enabled.Store(true)
	observer.record(FingerprintObservationEntry{ExtraMetadata: map[string]string{"prompt": "raw-private-prompt"}})
	require.Equal(t, map[string]string{"prompt": fingerprintExtraMetadataRedacted}, observer.snapshot(1)[0].ExtraMetadata)
	require.Equal(t, originalBody, body)
	require.Equal(t, originalHeaders, headers, "observation redaction must not rewrite actual outbound headers")
	require.Contains(t, string(body), "raw-private-prompt")
	require.Contains(t, headers[openAIWSTurnMetadataHeader][0], "raw-secret-access-token")
}

func TestFingerprintObservationSafeMetadataScalarBounds(t *testing.T) {
	for _, field := range []string{"agent_name", "window_id", "context_window_id", "guardian_classifier_source_thread_id", "turn_id", "parent_turn_id", "root_turn_id", "subagent_kind", "thread_source", "turn_trigger", "sandbox", "sandbox_mode"} {
		for _, value := range []any{strings.Repeat("long-sensitive-value", 20), "invalid\x01control", nil, map[string]any{"prompt": "must-not-retain"}} {
			raw, err := json.Marshal(map[string]any{field: value, "turn_started_at_unix_ms": nil})
			require.NoError(t, err)
			safe := fingerprintObservationSafeTurnMetadata(string(raw))
			var decoded map[string]json.RawMessage
			require.NoError(t, json.Unmarshal([]byte(safe), &decoded))
			require.Equal(t, "null", string(decoded[field]), field)
			require.NotContains(t, decoded, "turn_started_at_unix_ms", "null must not manufacture a valid timestamp")
			require.NotContains(t, safe, "long-sensitive-value")
			require.NotContains(t, safe, "must-not-retain")
		}
	}
	for _, field := range []string{"context_window_id", "guardian_classifier_source_thread_id"} {
		raw, err := json.Marshal(map[string]any{field: "not-a-uuid"})
		require.NoError(t, err)
		require.NotContains(t, fingerprintObservationSafeTurnMetadata(string(raw)), "not-a-uuid")
	}
	safe := fingerprintObservationSafeTurnMetadata(`{"request_kind":"compaction","turn_id":"internal:turn","turn_started_at_unix_ms":0}`)
	profile := ParseCodexWireProfile(safe)
	require.Equal(t, "internal:turn", profile.TurnID.Value)
	require.True(t, profile.TurnStartedAtSet)
	for _, name := range []string{"x-codex-window-id", "x-openai-subagent"} {
		frozen := cloneFingerprintObservationHeaders(http.Header{name: {strings.Repeat("x", 129), "invalid\x01control", "review"}})
		require.Equal(t, []string{"", "", "review"}, frozen[name])
	}
}
