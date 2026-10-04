//go:build unit

package handler

import (
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestCodexEngineMixedPoolKeepsOriginalCompactWire(t *testing.T) {
	// First account takes the legacy bridge and fails; the Engine attempt must
	// receive the original /responses body, including fields legacy compact drops.
	body := `{"model":"gpt-6-astra","stream":false,"unknown":9007199254740993123,"input":[{"type":"reasoning","id":"rs_original","encrypted_content":"opaque-original"},{"type":"message","role":"user","content":"hello"},{"type":"compaction_trigger"}],"tools":[{"type":"namespace","name":"native","tools":[]}]}`
	upstream := newAstraProCapturedUpstream(astra403(), astra200())
	h := newOpenAIResponsesFailoverTestHandler(t, upstream, func(accounts []service.Account) {
		accounts[0].Extra = map[string]any{"openai_compact_supported": true}
		accounts[1].Type = service.AccountTypeAPIKey
		accounts[1].Credentials = map[string]any{"api_key": "engine", "base_url": "https://engine.example/prefix/v1"}
		accounts[1].Extra = map[string]any{service.OpenAIAPIKeyModeExtraKey: "codex_engine", "openai_compact_supported": false, "openai_responses_mode": "force_chat_completions"}
	})
	c, rec := newAstraProFailoverContext(t, body)
	h.Responses(c)
	urls, ids, bodies := upstream.snapshot()
	require.Equal(t, []int64{1, 2}, ids, "response: %s", rec.Body.String())
	require.Equal(t, "https://engine.example/prefix/v1/responses", urls[1])
	require.Equal(t, body, string(bodies[1]))
	require.Equal(t, http.StatusOK, rec.Code)
}

func TestCodexEngineHandlerDoesNotReplayOrAppendBusinessError(t *testing.T) {
	for _, stream := range []bool{false, true} {
		wire := `{"error":{"code":"native_error","message":"kept"}}`
		status, contentType := 429, "application/json"
		if stream {
			wire = "event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"native_error\"}}}\n\n"
			status = 200
			contentType = "text/event-stream"
		}
		upstream := newAstraProCapturedUpstream(&http.Response{StatusCode: status, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(wire))})
		h := newOpenAIResponsesFailoverTestHandler(t, upstream, func(accounts []service.Account) {
			for i := range accounts {
				accounts[i].Type = service.AccountTypeAPIKey
				accounts[i].Credentials = map[string]any{"api_key": "engine", "base_url": "https://engine.example", "pool_mode": true, "pool_mode_retry_count": 3}
				accounts[i].Extra = map[string]any{service.OpenAIAPIKeyModeExtraKey: "codex_engine"}
			}
		})
		// Even upstream-invalid item IDs must remain untouched in dedicated mode.
		// Engine decides whether it can replay the client's original history.
		body := `{"model":"gpt-6-astra","input":[{"type":"reasoning","id":"item_original","encrypted_content":"opaque-original","summary":[]},{"role":"user","content":"hi"}],"tools":[{"type":"namespace","name":"native","tools":[]}],"unknown":9007199254740993123,"stream":false}`
		if stream {
			body = strings.Replace(body, "false", "true", 1)
		}
		c, rec := newAstraProFailoverContext(t, body)
		h.Responses(c)
		urls, ids, bodies := upstream.snapshot()
		require.Len(t, ids, 1)
		require.Equal(t, "https://engine.example/v1/responses", urls[0])
		require.Equal(t, body, string(bodies[0]), "selected Engine account must preserve client history, IDs and ciphertext")
		require.Equal(t, status, rec.Code)
		require.Equal(t, wire, rec.Body.String())
	}
}
