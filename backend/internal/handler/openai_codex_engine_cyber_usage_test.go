//go:build unit

package handler

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Exercise the real Engine forwarder and cyber audit together. Public usage on
// a failure never creates a second bill; confirmed receipt events settle later.
func TestCodexEngineCyberPolicyRecordsUsageOnce(t *testing.T) {
	const failedResponse = `{"id":"resp_engine_cyber","model":"gpt-5.1","service_tier":"priority","status":"failed","error":{"code":"cyber_policy","type":"invalid_request_error","message":"upstream policy refusal","status_code":400},"usage":{"input_tokens":1200,"output_tokens":300,"input_tokens_details":{"cached_tokens":700,"cache_write_tokens":100,"image_tokens":80},"output_tokens_details":{"image_tokens":25}}}`
	for _, tc := range []struct {
		name, contentType, wire string
		status                  int
		stream, metered         bool
	}{
		{"HTTP rejection without usage", "application/json", `{"error":{"code":"cyber_policy","type":"invalid_request_error","message":"upstream policy refusal"}}`, http.StatusBadRequest, false, false},
		{"HTTP rejection with usage", "application/json", failedResponse, http.StatusBadRequest, false, true},
		{"SSE failure with usage", "text/event-stream", "event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":" + failedResponse + "}\n\n", http.StatusOK, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			setupOpsErrorLogTestQueue(t, 4)
			upstream := newAstraProCapturedUpstream(&http.Response{
				StatusCode: tc.status,
				Header:     http.Header{"Content-Type": {tc.contentType}, "X-Request-Id": {"req_engine_cyber_usage"}},
				Body:       io.NopCloser(strings.NewReader(tc.wire)),
			}, astra200())
			usageRepo := &openAIWSUsageHandlerUsageLogRepoStub{created: make(chan *service.UsageLog, 4)}
			h := newEngineCyberUsageHandler(t, upstream, usageRepo)
			body := `{"model":"gpt-5.1","service_tier":"priority","reasoning":{"effort":"high"},"input":"hello","stream":false}`
			if tc.stream {
				body = strings.Replace(body, "false", "true", 1)
			}
			c, rec := newAstraProFailoverContext(t, body)
			h.Responses(c)
			require.Equal(t, tc.status, rec.Code)
			require.Equal(t, tc.wire, rec.Body.String())
			_, attempts, _ := upstream.snapshot()
			require.Equal(t, []int64{1}, attempts, "a policy refusal must not be replayed")
			require.NotNil(t, service.GetOpsCyberPolicy(c))

			// Dedicated Ops enqueue is last in the asynchronous cyber recorder. Once
			// observed, both it and the handler's synchronous usage submission ended.
			select {
			case job := <-opsErrorLogQueue:
				require.Equal(t, "cyber_policy", job.entry.ErrorType)
			case <-time.After(3 * time.Second):
				t.Fatal("cyber record did not finish")
			}
			require.Empty(t, usageRepo.created, "public usage and the cyber marker must not bypass independent receipt-event settlement")
		})
	}
}

func TestCodexEngineWSCyberAuditUsesCurrentTurnRequestID(t *testing.T) {
	for _, requestID := range []string{"req_engine_cyber_round_2", ""} {
		name := "explicit_round_id"
		if requestID == "" {
			name = "missing_round_id"
		}
		t.Run(name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			setupOpsErrorLogTestQueue(t, 4)
			failureHeaders := http.Header{"Content-Type": {"text/event-stream"}}
			if requestID != "" {
				failureHeaders.Set("X-Request-Id", requestID)
			}
			upstream := newAstraProCapturedUpstream(
				&http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}, "X-Request-Id": {"req_engine_round_1"}}, Body: io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_engine_round_1\",\"usage\":{\"input_tokens\":10,\"output_tokens\":1}}}\n\n"))},
				&http.Response{StatusCode: http.StatusOK, Header: failureHeaders, Body: io.NopCloser(strings.NewReader("data: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp_engine_round_2\",\"error\":{\"code\":\"cyber_policy\",\"message\":\"policy refusal\",\"status_code\":400},\"usage\":{\"input_tokens\":20,\"output_tokens\":2}}}\n\n"))},
			)
			usageRepo := &openAIWSUsageHandlerUsageLogRepoStub{created: make(chan *service.UsageLog, 4)}
			h := newEngineCyberUsageHandler(t, upstream, usageRepo)
			h.cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
			h.cfg.Gateway.OpenAIWS.IngressModeDefault = service.OpenAIWSIngressModeHTTPBridge
			authContext, _ := newAstraProFailoverContext(t, `{}`)
			key, ok := middleware.GetAPIKeyFromContext(authContext)
			require.True(t, ok)
			done := make(chan string, 1)
			router := gin.New()
			router.GET("/v1/responses", func(c *gin.Context) {
				c.Set(string(middleware.ContextKeyAPIKey), key)
				c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 100})
				c.Writer.Header().Set("X-Request-Id", "req_handshake")
				h.ResponsesWebSocket(c)
				done <- c.Writer.Header().Get("X-Request-Id")
			})
			server := httptest.NewServer(router)
			defer server.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			client, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses", nil)
			require.NoError(t, err)
			defer client.CloseNow()
			for _, eventType := range []string{"response.completed", "response.failed"} {
				require.NoError(t, client.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","input":"hello"}`)))
				_, event, err := client.Read(ctx)
				require.NoError(t, err)
				require.Equal(t, eventType, gjson.GetBytes(event, "type").String())
			}
			select {
			case job := <-opsErrorLogQueue:
				require.Equal(t, "cyber_policy", job.entry.ErrorType)
				require.Equal(t, requestID, job.entry.RequestID, "audit must not reuse handshake or previous turn headers")
			case <-ctx.Done():
				t.Fatal("WS cyber audit did not finish")
			}
			_ = client.CloseNow()
			select {
			case restoredID := <-done:
				require.Equal(t, "req_handshake", restoredID, "the HTTP handshake writer must be restored after the callback")
			case <-ctx.Done():
				t.Fatal("WS handler did not finish")
			}
			_, attempts, _ := upstream.snapshot()
			require.Equal(t, []int64{1, 1}, attempts)
			require.Empty(t, opsErrorLogQueue, "one refused turn must produce exactly one cyber audit")
			require.Empty(t, usageRepo.created, "WS audit must not produce a second charge outside receipt settlement")
		})
	}
}

func newEngineCyberUsageHandler(t *testing.T, upstream service.HTTPUpstream, usageRepo service.UsageLogRepository) *OpenAIGatewayHandler {
	t.Helper()
	accounts := []service.Account{{
		ID: 1, Name: "engine-cyber", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
		Status: service.StatusActive, Schedulable: true,
		Credentials: map[string]any{"api_key": "engine", "base_url": "https://engine.example", "pool_mode": true, "pool_mode_retry_count": 3},
		Extra:       map[string]any{service.OpenAIAPIKeyModeExtraKey: "codex_engine"},
	}}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Default.RateMultiplier = 1
	gateway := newEngineBillingHandlerGateway(t, openAIImagesFailoverAccountRepo{accounts: accounts}, usageRepo, cfg, upstream)
	billing := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billing.Stop)
	h := NewOpenAIGatewayHandler(gateway, service.NewConcurrencyService(nil), billing,
		service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg), nil, nil, nil,
		service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil), cfg)
	h.maxAccountSwitches = 3
	return h
}

func TestCodexEngineAuxiliaryCyberPolicyRecordsWithoutReplay(t *testing.T) {
	const refusal = `{"error":{"code":"cyber_policy","type":"invalid_request_error","message":"upstream policy refusal"}}`
	for _, tc := range []struct {
		name, path, body string
		run              func(*OpenAIGatewayHandler, *gin.Context)
		metered          bool
	}{
		{"chat", "/v1/chat/completions", `{"model":"gpt-5.1","messages":[{"role":"user","content":"test"}]}`, (*OpenAIGatewayHandler).ChatCompletions, false},
		{"chat with usage", "/v1/chat/completions", `{"model":"gpt-5.1","messages":[{"role":"user","content":"test"}]}`, (*OpenAIGatewayHandler).ChatCompletions, true},
		{"messages", "/v1/messages", `{"model":"gpt-5.1","max_tokens":100,"messages":[{"role":"user","content":"test"}]}`, (*OpenAIGatewayHandler).Messages, false},
		{"messages with usage", "/v1/messages", `{"model":"gpt-5.1","max_tokens":100,"messages":[{"role":"user","content":"test"}]}`, (*OpenAIGatewayHandler).Messages, true},
		{"images", "/v1/images/generations", `{"model":"gpt-image-1","prompt":"test"}`, (*OpenAIGatewayHandler).Images, false},
		{"images with usage", "/v1/images/generations", `{"model":"gpt-image-1","prompt":"test"}`, (*OpenAIGatewayHandler).Images, true},
		{"search", "/v1/alpha/search", `{"model":"gpt-5.1","query":"test"}`, (*OpenAIGatewayHandler).AlphaSearch, false},
		{"search with usage", "/v1/alpha/search", `{"model":"gpt-5.1","query":"test"}`, (*OpenAIGatewayHandler).AlphaSearch, true},
		{"count tokens", "/v1/messages/count_tokens", `{"model":"gpt-5.1","messages":[{"role":"user","content":"test"}]}`, (*OpenAIGatewayHandler).CountTokens, false},
		{"count tokens with usage", "/v1/messages/count_tokens", `{"model":"gpt-5.1","messages":[{"role":"user","content":"test"}]}`, (*OpenAIGatewayHandler).CountTokens, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			setupOpsErrorLogTestQueue(t, 4)
			wire := refusal
			if tc.metered {
				wire = strings.TrimSuffix(refusal, "}") + `,"usage":{"input_tokens":100,"output_tokens":30,"input_tokens_details":{"cached_tokens":60,"cache_write_tokens":10}}}`
			}
			upstream := newAstraProCapturedUpstream(&http.Response{
				StatusCode: http.StatusBadRequest,
				Header:     http.Header{"Content-Type": {"application/json"}, "X-Request-Id": {"req_engine_cyber_aux"}},
				Body:       io.NopCloser(strings.NewReader(wire)),
			}, astra200())
			usageRepo := &openAIWSUsageHandlerUsageLogRepoStub{created: make(chan *service.UsageLog, 4)}
			h := newEngineCyberUsageHandler(t, upstream, usageRepo)
			c, rec := newAstraProFailoverContext(t, tc.body)
			c.Request.URL.Path = tc.path
			key, ok := middleware.GetAPIKeyFromContext(c)
			require.True(t, ok)
			key.Group.AllowMessagesDispatch = true
			key.Group.AllowImageGeneration = true
			tc.run(h, c)
			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			require.Equal(t, wire, rec.Body.String())
			_, attempts, _ := upstream.snapshot()
			require.Equal(t, []int64{1}, attempts)
			select {
			case job := <-opsErrorLogQueue:
				require.Equal(t, "cyber_policy", job.entry.ErrorType)
			case <-time.After(3 * time.Second):
				t.Fatal("auxiliary cyber record did not finish")
			}
			require.Empty(t, usageRepo.created, "only the persisted receipt path may bill Engine requests; count_tokens remains free")
		})
	}
}
