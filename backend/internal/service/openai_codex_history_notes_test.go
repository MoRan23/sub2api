package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRewriteCodexAuxiliaryJSONReplacesProtocolIdentityOnly(t *testing.T) {
	n := uint64(3)
	plan := OpenAIOAuthIdentityPlan{
		WireProfile: CodexWireProfile{
			SessionID:    "server-session",
			ThreadID:     "server-thread",
			WindowID:     "server-thread:3",
			WindowNumber: &n,
		},
		Window: OpenAICodexWindowSnapshot{ContextWindowID: "018f4f65-8f4e-7a8e-8d7f-5b0f7d4c8e91"},
	}
	body := []byte(`{"session_id":"client-session","context":{"session_id":"client-session","current_agent_name":"/root/worker","thread_id":"client-thread","window_number":9,"note":"keep"},"window_id":"historical-window","item_id":"client-item","content":"client text","metadata":{"thread_id":"client-thread","keep":true},"client_metadata":{"session_id":"client-session"},"x-codex-turn-metadata":"client-metadata"}`)
	out := rewriteCodexAuxiliaryJSON(body, plan)
	var got map[string]any
	require.NoError(t, json.Unmarshal(out, &got))
	require.NotContains(t, got, "session_id")
	require.NotContains(t, got, "thread_id")
	require.NotContains(t, got, "window_number")
	require.NotContains(t, got, "context_window_id")
	require.NotContains(t, got, "client_metadata")
	require.NotContains(t, got, "x-codex-turn-metadata")
	require.Equal(t, "historical-window", got["window_id"])
	require.Equal(t, "client-item", got["item_id"])
	require.Equal(t, "client text", got["content"])
	context := got["context"].(map[string]any)
	require.Equal(t, "server-session", context["session_id"])
	require.Equal(t, "/root/worker", context["current_agent_name"])
	require.NotContains(t, context, "thread_id")
	require.NotContains(t, context, "window_number")
	require.Equal(t, "keep", context["note"])
	require.Equal(t, map[string]any{"keep": true}, got["metadata"])
}

func TestRewriteCodexAuxiliaryJSONPreservesHistorySelectorsAndNotes(t *testing.T) {
	plan := OpenAIOAuthIdentityPlan{WireProfile: CodexWireProfile{SessionID: "server-session", WindowID: "server-current:9"}}
	for _, selector := range []string{"", `,"window_id":null`, `,"window_id":"old-window"`} {
		t.Run(selector, func(t *testing.T) {
			body := []byte(`{"context":{"session_id":"client-session","current_agent_name":"/root/worker"},"item_id":"opaque-item","text":"<context_window>Current context window id: client-uuid</context_window>","query":"session_id:client-session","path":"notes/old-window","agent_name":"keep-agent"` + selector + `}`)
			var original, got map[string]any
			require.NoError(t, json.Unmarshal(body, &original))
			require.NoError(t, json.Unmarshal(rewriteCodexAuxiliaryJSON(body, plan), &got))
			original["context"].(map[string]any)["session_id"] = "server-session"
			require.Equal(t, original, got)
		})
	}
}

func TestCaptureCodexAuxiliaryIdentityUsesOnlyProtocolSession(t *testing.T) {
	body := []byte(`{"context":{"session_id":"real-session","current_agent_name":"/root/worker"},"client_metadata":{"session_id":"spoof-session","thread_id":"spoof-thread"},"session_id":"root-spoof","window_id":"historical-window"}`)
	capture, err := captureCodexAuxiliaryIdentity(body)
	require.NoError(t, err)
	require.Equal(t, "real-session", capture.Logical.SessionKey)
	require.Equal(t, "real-session", capture.Logical.ThreadKey)
	require.Empty(t, capture.Logical.ParentThreadKey)
	for _, invalid := range []string{`{`, `{}`, `{"context":null}`, `{"context":{"session_id":null}}`, `{"context":{"session_id":4}}`, `{"context":{"session_id":""}}`} {
		_, err := captureCodexAuxiliaryIdentity([]byte(invalid))
		require.ErrorIs(t, err, ErrCodexHistoryNotesInvalidContext)
	}
}
