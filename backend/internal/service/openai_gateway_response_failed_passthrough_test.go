//go:build unit

package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func buildContextLengthFailedSSE() string {
	failed := `{"type":"response.failed","response":{"id":"resp_err","object":"response","status":"failed","error":{"code":"context_length_exceeded","type":"invalid_request_error","message":"Your input exceeds the context window of this model. Please adjust your input and try again."},"output":[],"usage":{"input_tokens":100000,"output_tokens":0,"total_tokens":100000}}}`
	return fmt.Sprintf("data: %s\n\n", failed)
}

func bindPassthroughRule(c *gin.Context, platform string, keywords []string, responseCode int) {
	svc := &ErrorPassthroughService{}
	rules := make([]*cachedPassthroughRule, 0, len(keywords))
	for i, kw := range keywords {
		code := responseCode
		rules = append(rules, &cachedPassthroughRule{
			ErrorPassthroughRule: &model.ErrorPassthroughRule{
				ID:              int64(i + 1),
				Enabled:         true,
				Platforms:       []string{platform},
				MatchMode:       model.MatchModeAny,
				Keywords:        []string{kw},
				ResponseCode:    &code,
				PassthroughBody: true,
			},
			lowerKeywords:  []string{strings.ToLower(kw)},
			lowerPlatforms: []string{strings.ToLower(platform)},
		})
	}
	svc.localCacheMu.Lock()
	svc.localCache = rules
	svc.localCacheMu.Unlock()
	BindErrorPassthroughService(c, svc)
}

type shortOpenAIPassthroughRuleWriter struct {
	gin.ResponseWriter
	maxBytes int
}

func (w *shortOpenAIPassthroughRuleWriter) Write(p []byte) (int, error) {
	if w.maxBytes >= len(p) {
		return w.ResponseWriter.Write(p)
	}
	if w.maxBytes <= 0 {
		return 0, nil
	}
	return w.ResponseWriter.Write(p[:w.maxBytes])
}

type errorOpenAIPassthroughRuleWriter struct {
	gin.ResponseWriter
}

func (w *errorOpenAIPassthroughRuleWriter) Write([]byte) (int, error) {
	w.ResponseWriter.WriteHeaderNow()
	return 0, errors.New("write failed after headers committed")
}

type passthroughRuleTurnStateCache struct {
	GatewayCache
	setCalls int
}

var _ OpenAICodexTurnStateOriginStore = (*passthroughRuleTurnStateCache)(nil)

func (c *passthroughRuleTurnStateCache) SetOpenAICodexTurnStateOrigin(context.Context, string, OpenAICodexTurnStateOrigin, time.Duration) error {
	c.setCalls++
	return nil
}

func (c *passthroughRuleTurnStateCache) GetOpenAICodexTurnStateOrigin(context.Context, string) (OpenAICodexTurnStateOrigin, error) {
	return OpenAICodexTurnStateOrigin{}, ErrOpenAICodexTurnStateOriginNotFound
}

func (c *passthroughRuleTurnStateCache) DeleteOpenAICodexTurnStateOrigin(context.Context, string) error {
	return nil
}

func TestHandleStreamingResponsePassthroughRuleTurnStateCommitRequiresDelivery(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name       string
		wrapWriter func(gin.ResponseWriter) gin.ResponseWriter
		wantCommit bool
	}{
		{name: "full write commits", wantCommit: true},
		{
			name: "short write does not commit",
			wrapWriter: func(writer gin.ResponseWriter) gin.ResponseWriter {
				return &shortOpenAIPassthroughRuleWriter{ResponseWriter: writer, maxBytes: 1}
			},
		},
		{
			name: "write error does not commit",
			wrapWriter: func(writer gin.ResponseWriter) gin.ResponseWriter {
				return &errorOpenAIPassthroughRuleWriter{ResponseWriter: writer}
			},
		},
	}

	for index, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			if tt.wrapWriter != nil {
				c.Writer = tt.wrapWriter(c.Writer)
			}
			bindPassthroughRule(c, PlatformOpenAI, []string{"context_length_exceeded"}, http.StatusBadRequest)

			plan := OpenAIOAuthIdentityPlan{
				APIKeyID:                 int64(7100 + index),
				CredentialOwnerNamespace: fmt.Sprintf("account:%d", 7200+index),
				TurnIdentityRequested:    true,
				TurnIdentityEnabled:      true,
				TurnIdentity: OpenAICodexTurnIdentity{
					SessionID: "018f5c3c-6e3a-7abc-8def-1234567890ab",
					ThreadID:  "018f5c3c-6e3a-7abc-8def-1234567890ab",
					Relation:  OpenAICodexTurnRelationRoot,
				},
			}
			SetOpenAIOAuthIdentityPlan(c, plan)

			turnStateCache := &passthroughRuleTurnStateCache{}
			svc := &OpenAIGatewayService{
				cfg:   &config.Config{JWT: config.JWTConfig{Secret: "passthrough-rule-delivery-secret"}},
				cache: turnStateCache,
			}
			require.Same(t, turnStateCache, svc.primaryOpenAICodexTurnStateOriginStore())
			account := &Account{
				ID:       int64(7200 + index),
				Platform: PlatformOpenAI,
				Type:     AccountTypeOAuth,
			}
			upstreamHeaders := http.Header{"Content-Type": {"text/event-stream"}}
			upstreamHeaders.Set(openAIWSTurnStateHeader, fmt.Sprintf("passthrough-rule-state-%d", index))
			resp := &http.Response{
				StatusCode: http.StatusOK,
				Header:     upstreamHeaders,
				Body:       io.NopCloser(strings.NewReader(buildContextLengthFailedSSE())),
			}

			_, err := svc.handleStreamingResponsePassthrough(c.Request.Context(), resp, c, account, time.Now(), "gpt-5.4", "gpt-5.4")
			require.Error(t, err)
			require.Contains(t, err.Error(), "passthrough rule matched")
			require.True(t, c.Writer.Written(), "fixture must commit headers so Written alone cannot prove delivery")
			require.Equal(t, http.StatusBadRequest, recorder.Code)
			if tt.wantCommit {
				require.NotEmpty(t, recorder.Body.Bytes())
			}
			require.Equal(t, fmt.Sprintf("passthrough-rule-state-%d", index), recorder.Header().Get(openAIWSTurnStateHeader))
			storedPlan, ok := OpenAIOAuthIdentityPlanFromContext(c)
			require.True(t, ok)
			_, originOK := openAICodexTurnStateRequestOriginFromPlan(account, storedPlan)
			require.True(t, originOK)
			require.Equal(t, map[bool]int{false: 0, true: 1}[tt.wantCommit], turnStateCache.setCalls)
		})
	}
}

func TestForwardAsChatCompletions_ResponseFailed_PassthroughRule(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hello"}],"stream":false}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	bindPassthroughRule(c, "openai", []string{"context_length_exceeded"}, 400)

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(buildContextLengthFailedSSE())),
	}}
	svc := &OpenAIGatewayService{
		cfg:          rawChatCompletionsTestConfig(),
		httpUpstream: upstream,
	}

	account := rawChatCompletionsTestAccount()
	_, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "")

	require.Error(t, err)
	require.Contains(t, err.Error(), "passthrough")
	require.Equal(t, 400, rec.Code, "passthrough rule should override 502 to 400")

	respBody := rec.Body.String()
	errType := gjson.Get(respBody, "error.type").String()
	require.Equal(t, "upstream_error", errType)
	errMsg := gjson.Get(respBody, "error.message").String()
	require.NotEmpty(t, errMsg, "passthrough should preserve error message")
	require.Contains(t, errMsg, "context window")
}

func TestResponsesStreamAccessStateFailoverPrecedesPassthroughRule(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stream := "event: response.failed\n" +
		`data: {"type":"response.failed","response":{"status":"failed","error":{"code":"account_disabled","message":"Your account is disabled"}}}` + "\n\n"
	tests := []struct {
		name string
		run  func(*OpenAIGatewayService, *gin.Context, *http.Response, *Account) error
	}{
		{
			name: "native",
			run: func(svc *OpenAIGatewayService, c *gin.Context, resp *http.Response, account *Account) error {
				_, err := svc.handleStreamingResponse(c.Request.Context(), resp, c, account, time.Now(), "gpt-5", "gpt-5")
				return err
			},
		},
		{
			name: "passthrough",
			run: func(svc *OpenAIGatewayService, c *gin.Context, resp *http.Response, account *Account) error {
				_, err := svc.handleStreamingResponsePassthrough(c.Request.Context(), resp, c, account, time.Now(), "gpt-5", "gpt-5")
				return err
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			bindPassthroughRule(c, PlatformOpenAI, []string{"account is disabled"}, http.StatusTeapot)
			resp := &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:       io.NopCloser(strings.NewReader(stream)),
			}
			svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}}
			err := tt.run(svc, c, resp, &Account{ID: 11, Platform: PlatformOpenAI, Type: AccountTypeOAuth})

			var failoverErr *UpstreamFailoverError
			require.ErrorAs(t, err, &failoverErr)
			require.True(t, failoverErr.IsCredentialFailure())
			require.Equal(t, OpenAIUpstreamAccessStateReason, failoverErr.Reason)
			require.False(t, failoverErr.RetryableOnSameAccount)
			require.Equal(t, http.StatusBadGateway, failoverErr.ClientStatusCode)
			require.False(t, c.Writer.Written(), "passthrough rule must not commit a response before account failover")
		})
	}
}

func TestResponsesStreamCyberPolicyPrecedesPassthroughRule(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stream := "event: error\n" +
		`data: {"type":"error","error":{"code":"cyber_policy","message":"blocked by cyber policy"}}` + "\n\n"
	tests := []struct {
		name string
		run  func(*OpenAIGatewayService, *gin.Context, *http.Response, *Account) error
	}{
		{
			name: "native",
			run: func(svc *OpenAIGatewayService, c *gin.Context, resp *http.Response, account *Account) error {
				_, err := svc.handleStreamingResponse(c.Request.Context(), resp, c, account, time.Now(), "gpt-5", "gpt-5")
				return err
			},
		},
		{
			name: "passthrough",
			run: func(svc *OpenAIGatewayService, c *gin.Context, resp *http.Response, account *Account) error {
				_, err := svc.handleStreamingResponsePassthrough(c.Request.Context(), resp, c, account, time.Now(), "gpt-5", "gpt-5")
				return err
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			bindPassthroughRule(c, PlatformOpenAI, []string{"cyber policy"}, http.StatusTeapot)
			resp := &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:       io.NopCloser(strings.NewReader(stream)),
			}
			svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}}
			err := tt.run(svc, c, resp, &Account{ID: 12, Platform: PlatformOpenAI, Type: AccountTypeOAuth})

			require.Error(t, err)
			var failoverErr *UpstreamFailoverError
			require.False(t, errors.As(err, &failoverErr))
			require.NotNil(t, GetOpsCyberPolicy(c))
			require.NotEqual(t, http.StatusTeapot, rec.Code)
			require.Contains(t, rec.Body.String(), "cyber_policy")
		})
	}
}

func TestForwardAsAnthropic_ResponseFailed_PassthroughRule(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"gpt-5.4","max_tokens":32,"messages":[{"role":"user","content":"hello"}],"stream":false}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	bindPassthroughRule(c, "openai", []string{"context_length_exceeded"}, 400)

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(buildContextLengthFailedSSE())),
	}}
	svc := &OpenAIGatewayService{
		cfg:          rawChatCompletionsTestConfig(),
		httpUpstream: upstream,
	}

	account := rawChatCompletionsTestAccount()
	_, err := svc.ForwardAsAnthropic(context.Background(), c, account, body, "", "")

	require.Error(t, err)
	require.Contains(t, err.Error(), "passthrough")
	require.Equal(t, 400, rec.Code, "passthrough rule should override 502 to 400")
	respBody := rec.Body.String()
	errMsg := gjson.Get(respBody, "error.message").String()
	require.NotEmpty(t, errMsg, "passthrough should preserve error message")
}

func TestForwardAsChatCompletions_ResponseFailed_NoRule_Still502(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hello"}],"stream":false}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(buildContextLengthFailedSSE())),
	}}
	svc := &OpenAIGatewayService{
		cfg:          rawChatCompletionsTestConfig(),
		httpUpstream: upstream,
	}

	account := rawChatCompletionsTestAccount()
	_, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "")

	require.Error(t, err)
	require.Equal(t, http.StatusBadGateway, rec.Code, "without passthrough rule should still be 502")
}

// bindStatusCodePassthroughRule 绑定一条按错误码+关键词双条件(MatchModeAll)匹配的规则。
// 此类规则依赖语义状态码推断才能在协议转换路径命中（response.failed 无真实 HTTP 状态码）。
func bindStatusCodePassthroughRule(c *gin.Context, platform string, statusCode int, keyword string, responseCode int) {
	rule := &model.ErrorPassthroughRule{
		ID:              1,
		Name:            "status-code-rule",
		Enabled:         true,
		Priority:        1,
		Platforms:       []string{platform},
		ErrorCodes:      []int{statusCode},
		Keywords:        []string{keyword},
		MatchMode:       model.MatchModeAll,
		ResponseCode:    &responseCode,
		PassthroughBody: true,
	}
	svc := &ErrorPassthroughService{}
	svc.setLocalCache([]*model.ErrorPassthroughRule{rule})
	BindErrorPassthroughService(c, svc)
}

func TestForwardAsChatCompletions_ResponseFailed_ErrorCodeRuleMatchesViaSemanticStatus(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hello"}],"stream":false}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	bindStatusCodePassthroughRule(c, "openai", http.StatusBadRequest, "context_length_exceeded", http.StatusBadRequest)

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(buildContextLengthFailedSSE())),
	}}
	svc := &OpenAIGatewayService{
		cfg:          rawChatCompletionsTestConfig(),
		httpUpstream: upstream,
	}

	account := rawChatCompletionsTestAccount()
	_, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "")

	require.Error(t, err)
	require.Equal(t, http.StatusBadRequest, rec.Code, "error-code-conditioned rule should match via semantic status inference")
	respBody := rec.Body.String()
	require.Equal(t, "upstream_error", gjson.Get(respBody, "error.type").String())
	require.Contains(t, gjson.Get(respBody, "error.message").String(), "context window")
}

func TestForwardAsAnthropic_ResponseFailed_ErrorCodeRuleMatchesViaSemanticStatus(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"gpt-5.4","max_tokens":32,"messages":[{"role":"user","content":"hello"}],"stream":false}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	bindStatusCodePassthroughRule(c, "openai", http.StatusBadRequest, "context_length_exceeded", http.StatusBadRequest)

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(buildContextLengthFailedSSE())),
	}}
	svc := &OpenAIGatewayService{
		cfg:          rawChatCompletionsTestConfig(),
		httpUpstream: upstream,
	}

	account := rawChatCompletionsTestAccount()
	_, err := svc.ForwardAsAnthropic(context.Background(), c, account, body, "", "")

	require.Error(t, err)
	require.Equal(t, http.StatusBadRequest, rec.Code, "error-code-conditioned rule should match via semantic status inference")
	respBody := rec.Body.String()
	require.NotEmpty(t, gjson.Get(respBody, "error.message").String())
}
