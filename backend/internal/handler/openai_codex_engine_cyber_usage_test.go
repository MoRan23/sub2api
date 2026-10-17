//go:build unit

package handler

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// Exercise the real Engine forwarder and handler settlement together: a failed
// response may carry a complete result, which must not race a reduced cyber bill.
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
			require.Len(t, usageRepo.created, 1, "a partial result and cyber marker must produce exactly one bill")
			usage := <-usageRepo.created
			require.Equal(t, service.RequestTypeCyberBlocked, usage.RequestType)
			require.Equal(t, "req_engine_cyber_usage", usage.RequestID)
			require.Equal(t, "gpt-5.1", usage.Model)
			require.Equal(t, tc.stream, usage.Stream)
			if !tc.metered {
				require.Zero(t, usage.TotalTokens())
				require.Zero(t, usage.TotalCost)
				return
			}
			require.Equal(t, 400, usage.InputTokens, "cached and cache-write tokens must not be billed as uncached input")
			require.Equal(t, 700, usage.CacheReadTokens)
			require.Equal(t, 100, usage.CacheCreationTokens)
			require.Equal(t, 300, usage.OutputTokens)
			require.Equal(t, 80, usage.ImageInputTokens)
			require.Equal(t, 25, usage.ImageOutputTokens)
			require.NotNil(t, usage.UpstreamModel)
			require.Equal(t, "gpt-5.1", *usage.UpstreamModel)
			require.NotNil(t, usage.UpstreamResponseModel)
			require.Equal(t, "gpt-5.1", *usage.UpstreamResponseModel)
			require.NotNil(t, usage.ServiceTier)
			require.Equal(t, "priority", *usage.ServiceTier)
			require.NotNil(t, usage.ReasoningEffort)
			require.Equal(t, "high", *usage.ReasoningEffort)
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
	gateway := service.NewOpenAIGatewayService(
		openAIImagesFailoverAccountRepo{accounts: accounts}, usageRepo, nil, nil, nil, nil, nil, cfg, nil, nil,
		service.NewBillingService(cfg, nil), nil, nil, upstream, &service.DeferredService{},
		nil, nil, nil, nil, nil, nil, nil,
	)
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
		usageRows        int
		metered          bool
	}{
		{"chat", "/v1/chat/completions", `{"model":"gpt-5.1","messages":[{"role":"user","content":"test"}]}`, (*OpenAIGatewayHandler).ChatCompletions, 1, false},
		{"chat with usage", "/v1/chat/completions", `{"model":"gpt-5.1","messages":[{"role":"user","content":"test"}]}`, (*OpenAIGatewayHandler).ChatCompletions, 1, true},
		{"messages", "/v1/messages", `{"model":"gpt-5.1","max_tokens":100,"messages":[{"role":"user","content":"test"}]}`, (*OpenAIGatewayHandler).Messages, 1, false},
		{"messages with usage", "/v1/messages", `{"model":"gpt-5.1","max_tokens":100,"messages":[{"role":"user","content":"test"}]}`, (*OpenAIGatewayHandler).Messages, 1, true},
		{"images", "/v1/images/generations", `{"model":"gpt-image-1","prompt":"test"}`, (*OpenAIGatewayHandler).Images, 1, false},
		{"images with usage", "/v1/images/generations", `{"model":"gpt-image-1","prompt":"test"}`, (*OpenAIGatewayHandler).Images, 1, true},
		{"search", "/v1/alpha/search", `{"model":"gpt-5.1","query":"test"}`, (*OpenAIGatewayHandler).AlphaSearch, 1, false},
		{"search with usage", "/v1/alpha/search", `{"model":"gpt-5.1","query":"test"}`, (*OpenAIGatewayHandler).AlphaSearch, 1, true},
		{"count tokens", "/v1/messages/count_tokens", `{"model":"gpt-5.1","messages":[{"role":"user","content":"test"}]}`, (*OpenAIGatewayHandler).CountTokens, 0, false},
		{"count tokens with usage", "/v1/messages/count_tokens", `{"model":"gpt-5.1","messages":[{"role":"user","content":"test"}]}`, (*OpenAIGatewayHandler).CountTokens, 0, true},
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
			require.Len(t, usageRepo.created, tc.usageRows)
			if tc.usageRows > 0 {
				usage := <-usageRepo.created
				require.Equal(t, service.RequestTypeCyberBlocked, usage.RequestType)
				if tc.metered {
					require.Equal(t, 30, usage.InputTokens)
					require.Equal(t, 60, usage.CacheReadTokens)
					require.Equal(t, 10, usage.CacheCreationTokens)
					require.Equal(t, 30, usage.OutputTokens)
				} else {
					require.Zero(t, usage.TotalCost, "an unmetered rejection must not fabricate image/search charges")
				}
			}
		})
	}
}
