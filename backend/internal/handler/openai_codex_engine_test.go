//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestCodexEngineTerminalDiagnosticsSurviveCaptureLimits(t *testing.T) {
	for _, size := range []int{opsCaptureWriterLimit * 2, opsTerminalSSEFrameProbeLimit * 2} {
		t.Run(strconvItoa(size), func(t *testing.T) {
			setupOpsErrorLogTestQueue(t, 2)
			gin.SetMode(gin.TestMode)
			ops := service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
			router := gin.New()
			router.Use(OpsErrorLoggerMiddleware(ops))
			message := "Encrypted content item_id did not match the target item id"
			wire := "event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"output\":[{\"text\":\"private-output-" + strings.Repeat("x", size) + "\"}],\"error\":{\"code\":\"native_error\",\"message\":\"" + message + "\"}}}\n\n"
			router.POST("/v1/responses", func(c *gin.Context) {
				c.Set(opsAccountIDKey, int64(7))
				c.Set(opsStreamKey, true)
				c.Status(http.StatusOK)
				_, _ = c.Writer.WriteString(wire)
				c.Set("codex_engine_response_written", true)
				service.SetOpsUpstreamError(c, http.StatusBadGateway, message, "")
				c.Set(service.OpsUpstreamErrorsKey, []*service.OpsUpstreamErrorEvent{{AccountID: 7, Kind: "stream_error", Reason: "native_error", Message: message, UpstreamStatusCode: http.StatusBadGateway, UpstreamRequestID: "req_engine"}})
				c.Set("codex_engine_terminal_error", []byte("event: response.failed\ndata: {\"error\":{\"code\":\"native_error\",\"message\":\""+message+"\"}}\n\n"))
			})
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
			require.Equal(t, http.StatusOK, rec.Code)
			require.Equal(t, wire, rec.Body.String())
			require.Equal(t, int64(1), OpsErrorLogQueueLength())
			entry := (<-opsErrorLogQueue).entry
			require.Equal(t, http.StatusBadGateway, entry.StatusCode)
			require.Equal(t, message, entry.ErrorMessage)
			require.Equal(t, message, *entry.UpstreamErrorMessage)
			require.Contains(t, entry.ErrorBody, `"code":"native_error"`)
			require.NotContains(t, entry.ErrorBody, "private-output")
			require.NotNil(t, entry.UpstreamErrorsJSON)
			var events []service.OpsUpstreamErrorEvent
			require.NoError(t, json.Unmarshal([]byte(*entry.UpstreamErrorsJSON), &events))
			require.Len(t, events, 1)
			require.Equal(t, "req_engine", events[0].UpstreamRequestID)
		})
	}
}

func TestCodexEngineTerminalDiagnosticsKeepErrorClassification(t *testing.T) {
	for _, tc := range []struct {
		name, detail, errorType string
		wireStatus, status      int
	}{
		{"rate limit", `{"error":{"code":"rate_limit_exceeded","message":"wait"}}`, "rate_limit_error", 200, 429},
		{"explicit status", `{"error":{"status_code":403,"code":"native_error","message":"denied"}}`, "upstream_error", 200, 403},
		{"explicit overrides code", `{"error":{"status_code":502,"code":"rate_limit_exceeded","message":"failed"}}`, "rate_limit_error", 200, 502},
		{"HTTP failure", `{"error":{"code":"native_error","message":"conflict"}}`, "upstream_error", 409, 409},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setupOpsErrorLogTestQueue(t, 2)
			gin.SetMode(gin.TestMode)
			ops := service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
			router := gin.New()
			router.Use(OpsErrorLoggerMiddleware(ops))
			router.POST("/v1/responses", func(c *gin.Context) {
				c.Status(tc.wireStatus)
				_, _ = c.Writer.WriteString("event: response.failed\ndata: {\"payload_truncated\":true}\n\n")
				c.Set("codex_engine_terminal_error", []byte("event: response.failed\ndata: "+tc.detail+"\n\n"))
			})
			router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
			require.Equal(t, int64(1), OpsErrorLogQueueLength())
			entry := (<-opsErrorLogQueue).entry
			require.Equal(t, tc.status, entry.StatusCode)
			require.Equal(t, tc.errorType, entry.ErrorType)
			require.NotEqual(t, "upstream stream failed", entry.ErrorMessage)
		})
	}
}

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
		body := `{"model":"gpt-6-astra","previous_response_id":"resp_original","input":[{"type":"reasoning","id":"item_original","encrypted_content":"opaque-original","summary":[]},{"type":"reasoning","encrypted_content":"opaque-missing-id"},{"type":"reasoning","id":null,"encrypted_content":"opaque-null-id"},{"type":"reasoning","id":"","encrypted_content":"opaque-empty-id"},{"type":"function_call_output","call_id":"call_original","output":"tool result"},{"role":"user","content":"hi"}],"tools":[{"type":"namespace","name":"native","tools":[]}],"unknown":9007199254740993123,"stream":false}`
		if stream {
			body = strings.Replace(body, "false", "true", 1)
		}
		c, rec := newAstraProFailoverContext(t, body)
		require.NoError(t, h.gatewayService.BindOpenAIHTTPResponseOwner(context.Background(), 3132, "resp_original", 100, 99))
		h.Responses(c)
		urls, ids, bodies := upstream.snapshot()
		require.Len(t, ids, 1)
		require.Equal(t, "https://engine.example/v1/responses", urls[0])
		require.Equal(t, body, string(bodies[0]), "selected Engine account must preserve client history, IDs and ciphertext")
		require.Equal(t, status, rec.Code)
		require.Equal(t, wire, rec.Body.String())
	}
}
