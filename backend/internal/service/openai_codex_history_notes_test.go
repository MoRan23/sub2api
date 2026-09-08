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
	body := []byte(`{"session_id":"client-session","context":{"thread_id":"client-thread","window_number":9,"note":"keep"},"item_id":"client-item","content":"client text"}`)
	out := rewriteCodexAuxiliaryJSON(body, plan)
	var got map[string]any
	require.NoError(t, json.Unmarshal(out, &got))
	require.Equal(t, "server-session", got["session_id"])
	require.Equal(t, "server-thread", got["thread_id"])
	require.Equal(t, "server-thread:3", got["window_id"])
	require.Equal(t, float64(3), got["window_number"])
	require.Equal(t, "client-item", got["item_id"])
	require.Equal(t, "client text", got["content"])
	context := got["context"].(map[string]any)
	require.Equal(t, "server-thread", context["thread_id"])
	require.Equal(t, float64(3), context["window_number"])
	require.Equal(t, "keep", context["note"])
}

func TestCodexAuxiliaryStickyKeyIsolatedByAPIKey(t *testing.T) {
	first := &APIKey{ID: 1, UserID: 7}
	second := &APIKey{ID: 2, UserID: 7}
	require.NotEqual(t, codexAuxiliaryStickyKey(first, nil, []byte(`{"thread_id":"same"}`)), codexAuxiliaryStickyKey(second, nil, []byte(`{"thread_id":"same"}`)))
}

func TestCodexAuxiliaryStickyKeyUsesNestedContextSession(t *testing.T) {
	first := &APIKey{ID: 1, UserID: 2}
	second := &APIKey{ID: 1, UserID: 2}
	listBody := []byte(`{"context":{"session_id":"same"},"operation":"list_windows"}`)
	readBody := []byte(`{"context":{"session_id":"same"},"operation":"read_item"}`)
	require.Equal(t, codexAuxiliaryStickyKey(first, nil, listBody), codexAuxiliaryStickyKey(second, nil, readBody))
}
