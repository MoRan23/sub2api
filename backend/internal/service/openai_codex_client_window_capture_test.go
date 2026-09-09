package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func clientWindowCaptureFixture(t *testing.T) map[string]any {
	t.Helper()
	var root map[string]any
	require.NoError(t, json.Unmarshal(contextWindowBodyPathPayload(t, false, contextWindowBodyPathClientID, contextWindowBodyPathClientID, ""), &root))
	metadata := root["client_metadata"].(map[string]any)
	metadata[openAIWSTurnMetadataHeader] = map[string]any{
		"session_id": codexWireTestSession, "thread_id": codexWireTestSession,
		"request_kind": "turn", "window_id": codexWireTestSession + ":0",
		"window_number": 0, "context_window_id": contextWindowBodyPathClientID,
	}
	return root
}

func TestOpenAICodexClientWindowCaptureRequiresNativeConsistentSignals(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(map[string]any, map[string]any)
		path string
		want bool
	}{
		{name: "http_turn", want: true},
		{name: "ws_turn", edit: func(root, _ map[string]any) { root["type"] = "response.create"; delete(root, "stream") }, want: true},
		{name: "metadata_only", edit: func(root, _ map[string]any) { delete(root, "input") }},
		{name: "user_block_only", edit: func(root, _ map[string]any) { root["input"].([]any)[0].(map[string]any)["role"] = "user" }},
		{name: "wrong_path", path: "/v1/chat/completions"},
		{name: "legacy_compact", path: "/v1/responses/compact"},
		{name: "local_compact", edit: func(_, meta map[string]any) { meta["request_kind"] = "compaction" }},
		{name: "native_compact", edit: func(root, _ map[string]any) {
			root["input"] = append(root["input"].([]any), map[string]any{"type": "compaction_trigger"})
		}},
		{name: "memory", edit: func(_, meta map[string]any) { meta["request_kind"] = "memory" }},
		{name: "prewarm", edit: func(root, _ map[string]any) { root["generate"] = false }},
		{name: "non_stream_http", edit: func(root, _ map[string]any) { root["stream"] = false }},
		{name: "number_string", edit: func(_, meta map[string]any) { meta["window_number"] = "0" }},
		{name: "number_negative", edit: func(_, meta map[string]any) { meta["window_number"] = -1 }},
		{name: "number_fractional", edit: func(_, meta map[string]any) { meta["window_number"] = 0.5 }},
		{name: "number_exceeds_exact_range", edit: func(_, meta map[string]any) { meta["window_number"] = OpenAICodexWindowMaxNumber + 1 }},
		{name: "suffix_conflict", edit: func(_, meta map[string]any) { meta["window_id"] = codexWireTestSession + ":1" }},
		{name: "metadata_body_uuid_conflict", edit: func(_, meta map[string]any) { meta["context_window_id"] = "01989f44-7c00-7000-8000-000000000902" }},
		{name: "non_v7_uuid", edit: func(_, meta map[string]any) { meta["context_window_id"] = "01989f44-7c00-4000-8000-000000000902" }},
		{name: "agent_conflict", edit: func(_, meta map[string]any) { meta["agent_name"] = "different-agent" }},
		{name: "body_ordinal_conflict", edit: func(_, meta map[string]any) {
			meta["window_number"] = 1
			meta["window_id"] = codexWireTestSession + ":1"
		}},
		{name: "missing_first", edit: func(root, _ map[string]any) {
			root["input"].([]any)[0].(map[string]any)["content"] = "<context_window>\nAgent name: Codex\nCurrent context window id: " + contextWindowBodyPathClientID + "\n</context_window>"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := clientWindowCaptureFixture(t)
			metadata := root["client_metadata"].(map[string]any)
			meta := metadata[openAIWSTurnMetadataHeader].(map[string]any)
			if tc.edit != nil {
				tc.edit(root, meta)
			}
			encoded, err := json.Marshal(meta)
			require.NoError(t, err)
			metadata[openAIWSTurnMetadataHeader] = string(encoded)
			body, err := json.Marshal(root)
			require.NoError(t, err)
			path := tc.path
			if path == "" {
				path = "/v1/responses"
			}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, path, nil)
			capture := CaptureOpenAIOAuthIdentity(c, body, "")
			require.Equal(t, tc.want, capture.ClientWindow.Valid)
			require.False(t, CaptureOpenAIOAuthIdentity(nil, body, "").ClientWindow.Valid, "context-free general capture is not a native transport entrypoint")
			if tc.want {
				require.Equal(t, contextWindowBodyPathClientID, capture.ClientWindow.Current)
				require.Equal(t, contextWindowBodyPathClientID, capture.ClientWindow.First)
				require.Zero(t, capture.ClientWindow.Number)
				require.NotEqual(t, capture.ClientWindow.Current, capture.ContextWindowIDCandidate)
				compat := CaptureOpenAIOAuthIdentityForCompatTurn(c, body, "")
				require.False(t, compat.ClientWindow.Valid)
			}
		})
	}
}

func TestOpenAICodexClientWindowCaptureWSFrameTransport(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(map[string]any, map[string]any)
		want bool
	}{
		{name: "response_create", want: true},
		{name: "http_body", edit: func(root, _ map[string]any) { delete(root, "type") }},
		{name: "session_update", edit: func(root, _ map[string]any) { root["type"] = "session.update" }},
		{name: "memory", edit: func(_, meta map[string]any) { meta["request_kind"] = "memory" }},
		{name: "prewarm", edit: func(root, _ map[string]any) { root["generate"] = false }},
		{name: "unmarked", edit: func(root, _ map[string]any) { delete(root, "input") }},
		{name: "missing_session", edit: func(_, meta map[string]any) { delete(meta, "session_id") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := clientWindowCaptureFixture(t)
			root["type"] = "response.create"
			metadata := root["client_metadata"].(map[string]any)
			meta := metadata[openAIWSTurnMetadataHeader].(map[string]any)
			if tc.edit != nil {
				tc.edit(root, meta)
			}
			encoded, err := json.Marshal(meta)
			require.NoError(t, err)
			metadata[openAIWSTurnMetadataHeader] = string(encoded)
			body, err := json.Marshal(root)
			require.NoError(t, err)
			require.False(t, CaptureOpenAIOAuthIdentity(nil, body, "").ClientWindow.Valid)
			capture := captureOpenAIWSFrameIdentity(body, nil)
			require.Equal(t, tc.want, capture.ClientWindow.Valid)
		})
	}
}

func TestOpenAICodexClientWindowCaptureWSFrameDoesNotInheritTransition(t *testing.T) {
	first := codexClientWindowPathUUID(t)
	body := codexClientWindowPathBody(t, true, codexWireTestSession, 0, first, first, "", true)
	initial := captureOpenAIWSFrameIdentity(body, nil)
	require.True(t, initial.ClientWindow.Valid)
	plan := OpenAIOAuthIdentityPlan{Capture: initial, RequestTurn: initial.RequestTurn, WireProfile: initial.WireProfile}
	for _, tc := range []struct {
		name, body string
		memory     bool
	}{
		{name: "ordinary", body: `{"type":"response.create","input":"next"}`},
		{name: "tool_continuation", body: `{"type":"response.create","input":[{"type":"function_call_output","call_id":"call_1","output":"done"}]}`},
		{name: "memory", body: `{"type":"response.create","client_metadata":{"x-codex-turn-metadata":"{\"request_kind\":\"memory\"}"},"input":"consolidate"}`, memory: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			capture := captureOpenAIWSFrameIdentity([]byte(tc.body), &plan)
			require.Equal(t, initial.Logical, capture.Logical)
			require.Zero(t, capture.ClientWindow)
			require.NotEqual(t, initial.ContextWindowIDCandidate, capture.ContextWindowIDCandidate)
			if tc.memory {
				require.Empty(t, capture.ContextWindowIDCandidate)
			} else {
				_, err := canonicalUUIDv7(capture.ContextWindowIDCandidate)
				require.NoError(t, err)
			}
		})
	}
}

func TestOpenAICodexClientWindowCaptureRequiresCanonicalCarrier(t *testing.T) {
	for _, carrier := range []string{"header", "root", "flat", "object"} {
		t.Run(carrier, func(t *testing.T) {
			root := clientWindowCaptureFixture(t)
			metadata := root["client_metadata"].(map[string]any)
			meta := metadata[openAIWSTurnMetadataHeader].(map[string]any)
			encoded, err := json.Marshal(meta)
			require.NoError(t, err)
			delete(metadata, openAIWSTurnMetadataHeader)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			switch carrier {
			case "header":
				c.Request.Header.Set(openAIWSTurnMetadataHeader, string(encoded))
			case "root":
				root[openAIWSTurnMetadataHeader] = string(encoded)
			case "flat":
				for k, v := range meta {
					metadata[k] = v
				}
			case "object":
				metadata[openAIWSTurnMetadataHeader] = meta
			}
			body, err := json.Marshal(root)
			require.NoError(t, err)
			require.False(t, CaptureOpenAIOAuthIdentity(c, body, "").ClientWindow.Valid)
		})
	}
}

func TestOpenAIOAuthIdentityCaptureEqualityIncludesClientWindow(t *testing.T) {
	left := OpenAIOAuthIdentityCapture{ClientWindow: OpenAICodexClientWindowSignal{Valid: true, Number: 1, Current: "current"}}
	right := left
	require.True(t, openAIOAuthIdentityCapturesEqual(left, right))
	right.ClientWindow.Number++
	require.False(t, openAIOAuthIdentityCapturesEqual(left, right))
}
