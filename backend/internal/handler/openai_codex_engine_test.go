//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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

func TestCodexEngineMixedPoolKeepsOriginalPageContextWire(t *testing.T) {
	// Native OAuth requests need the page-context compatibility conversion. An
	// Engine failover must still start from the original client representation.
	body := `{"model":"gpt-6-astra","stream":false,"unknown":9007199254740993123,"input":[{"type":"function_call_output","id":"fco_page","namespace":"agent","name":"codex_apps_open_page","output":"{\"page_id\":null}"},{"role":"user","content":"hello"}]}`
	upstream := newAstraProCapturedUpstream(astra403(), astra200())
	h := newOpenAIResponsesFailoverTestHandler(t, upstream, func(accounts []service.Account) {
		accounts[1].Type = service.AccountTypeAPIKey
		accounts[1].Credentials = map[string]any{"api_key": "engine", "base_url": "https://engine.example/v1"}
		accounts[1].Extra = map[string]any{service.OpenAIAPIKeyModeExtraKey: "codex_engine"}
	})
	c, rec := newAstraProFailoverContext(t, body)
	h.Responses(c)
	_, ids, bodies := upstream.snapshot()
	require.Equal(t, []int64{1, 2}, ids, "response: %s", rec.Body.String())
	require.NotContains(t, string(bodies[0]), `"name":"codex_apps_open_page"`)
	require.Contains(t, string(bodies[0]), `page_id`)
	require.Equal(t, body, string(bodies[1]))
	require.Equal(t, http.StatusOK, rec.Code)
}

func TestCodexEngineHandlerDoesNotReplayOrAppendBusinessError(t *testing.T) {
	const taskStateUnconfirmed = `{"error":{"code":"task_state_unconfirmed","type":"server_error","message":"The original accepted execution could not be confirmed"}}`
	for _, tc := range []struct {
		name, contentType, wire string
		status                  int
		stream                  bool
	}{
		{"rate limit", "application/json", `{"error":{"code":"native_error","message":"kept"}}`, http.StatusTooManyRequests, false},
		{"failed event", "text/event-stream", "event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"native_error\"}}}\n\n", http.StatusOK, true},
		{"task state unconfirmed", "application/json", taskStateUnconfirmed, http.StatusServiceUnavailable, false},
		{"task state unconfirmed with stream requested", "application/json", taskStateUnconfirmed, http.StatusServiceUnavailable, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := newAstraProCapturedUpstream(&http.Response{StatusCode: tc.status, Header: http.Header{"Content-Type": {tc.contentType}}, Body: io.NopCloser(strings.NewReader(tc.wire))}, astra200())
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
			if tc.stream {
				body = strings.Replace(body, "false", "true", 1)
			}
			c, rec := newAstraProFailoverContext(t, body)
			require.NoError(t, h.gatewayService.BindOpenAIHTTPResponseOwner(context.Background(), 3132, "resp_original", 100, 99))
			h.Responses(c)
			urls, ids, bodies := upstream.snapshot()
			require.Len(t, ids, 1)
			require.Equal(t, "https://engine.example/v1/responses", urls[0])
			require.Equal(t, body, string(bodies[0]), "selected Engine account must preserve client history, IDs and ciphertext")
			require.Equal(t, tc.status, rec.Code)
			require.Equal(t, tc.contentType, rec.Header().Get("Content-Type"))
			require.Equal(t, tc.wire, rec.Body.String(), "the original error must not be replaced by fallback success or appended SSE")
		})
	}
}

type codexEngineTransportTestUpstream struct {
	service.HTTPUpstream
	client   *http.Client
	target   string
	err      error
	accounts []int64
}

// Real refused dials can enter the shared transport helper's persistent
// cooldown path. Support that repository write in this in-memory fixture.
func (r openAIImagesFailoverAccountRepo) SetTempUnschedulable(_ context.Context, id int64, until time.Time, reason string) error {
	for i := range r.accounts {
		if r.accounts[i].ID == id {
			r.accounts[i].TempUnschedulableUntil = &until
			r.accounts[i].TempUnschedulableReason = reason
			return nil
		}
	}
	return service.ErrNoAvailableAccounts
}

func (u *codexEngineTransportTestUpstream) Do(req *http.Request, _ string, accountID int64, _ int) (*http.Response, error) {
	u.accounts = append(u.accounts, accountID)
	if accountID == 2 {
		return astra200(), nil
	}
	if u.err != nil {
		return nil, u.err
	}
	clone := req.Clone(req.Context())
	clone.URL.Scheme, clone.URL.Host = "http", u.target
	return u.client.Do(clone)
}

func TestCodexEngineHandlerDoesNotReplayAcceptedTransportFailure(t *testing.T) {
	var requests atomic.Int64
	received := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		data, err := io.ReadAll(r.Body)
		if err == nil {
			select {
			case received <- string(data):
			default:
			}
		}
		// Engine has consumed the complete request, but the response headers are lost.
		conn, _, hijackErr := w.(http.Hijacker).Hijack()
		if hijackErr == nil {
			_ = conn.Close()
		}
	}))
	defer server.Close()
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}, Timeout: 5 * time.Second}
	defer client.CloseIdleConnections()
	upstream := &codexEngineTransportTestUpstream{client: client, target: strings.TrimPrefix(server.URL, "http://")}
	h := newOpenAIResponsesFailoverTestHandler(t, upstream, configureEngineTransportTestAccounts)
	c, rec := newAstraProFailoverContext(t, astraProRequestBody)
	h.Responses(c)
	require.Equal(t, []int64{1}, upstream.accounts, "an accepted request must not be retried on account 2")
	require.Equal(t, int64(1), requests.Load())
	select {
	case body := <-received:
		require.Equal(t, astraProRequestBody, body)
	case <-time.After(3 * time.Second):
		t.Fatal("Engine did not receive the complete request")
	}
	require.Equal(t, http.StatusBadGateway, rec.Code)
	raw, exists := c.Get(service.OpsUpstreamErrorsKey)
	require.True(t, exists)
	require.Len(t, raw.([]*service.OpsUpstreamErrorEvent), 1)
}

func TestCodexEngineHandlerStillFailsOverConfirmedDialFailure(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	target := listener.Addr().String()
	require.NoError(t, listener.Close())
	client := &http.Client{Transport: &http.Transport{}, Timeout: 5 * time.Second}
	defer client.CloseIdleConnections()
	upstream := &codexEngineTransportTestUpstream{client: client, target: target}
	h := newOpenAIResponsesFailoverTestHandler(t, upstream, configureEngineTransportTestAccounts)
	c, rec := newAstraProFailoverContext(t, astraProRequestBody)
	h.Responses(c)
	require.Equal(t, []int64{1, 2}, upstream.accounts, "response: %s", rec.Body.String())
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

func TestCodexEngineHandlerDoesNotReplayUntracedEOF(t *testing.T) {
	upstream := &codexEngineTransportTestUpstream{err: io.EOF}
	h := newOpenAIResponsesFailoverTestHandler(t, upstream, configureEngineTransportTestAccounts)
	c, rec := newAstraProFailoverContext(t, astraProRequestBody)
	h.Responses(c)
	require.Equal(t, []int64{1}, upstream.accounts)
	require.Equal(t, http.StatusBadGateway, rec.Code)
}

func configureEngineTransportTestAccounts(accounts []service.Account) {
	for i := range accounts {
		accounts[i].Type = service.AccountTypeAPIKey
		accounts[i].Credentials = map[string]any{"api_key": "engine", "base_url": "https://engine.example", "pool_mode": true, "pool_mode_retry_count": 3}
		accounts[i].Extra = map[string]any{service.OpenAIAPIKeyModeExtraKey: "codex_engine"}
	}
}
