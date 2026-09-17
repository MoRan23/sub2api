package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type chatConversionHandlerUpstream struct {
	service.HTTPUpstream
	calls atomic.Int64
}

func (u *chatConversionHandlerUpstream) Do(*http.Request, string, int64, int) (*http.Response, error) {
	u.calls.Add(1)
	return nil, errors.New("test upstream reached")
}

func (u *chatConversionHandlerUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, concurrency)
}

type chatConversionHandlerConcurrency struct {
	concurrencyCacheMock
	waits atomic.Int64
}

func (m *chatConversionHandlerConcurrency) IncrementAccountWaitCount(context.Context, int64, int) (bool, error) {
	m.waits.Add(1)
	return true, nil
}

func newChatConversionHandler(t *testing.T, accountType string, cache *chatConversionHandlerConcurrency) (*OpenAIGatewayHandler, *chatConversionHandlerUpstream) {
	t.Helper()
	account := service.Account{
		ID: 94100, Name: "conversion-check", Platform: service.PlatformOpenAI,
		Type: accountType, Status: service.StatusActive, Schedulable: true, Concurrency: 1,
		Credentials: map[string]any{
			"access_token": "test-oauth-token", "chatgpt_account_id": "test-chatgpt-account",
			"api_key": "test-api-key", "base_url": "https://api.openai.com",
		},
		Extra: map[string]any{"openai_pinned_installation_id": openAIIdentityContinuityInstallationID},
	}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Gateway.Scheduling.FallbackWaitTimeout = time.Second
	concurrency := service.NewConcurrencyService(cache)
	upstream := &chatConversionHandlerUpstream{}
	gateway := service.NewOpenAIGatewayService(
		&openAIIdentityContinuityAccountRepo{account: account}, nil, nil, nil, nil, nil, nil,
		cfg, nil, concurrency, nil, nil, nil, upstream, nil, nil, nil, nil, nil, nil, nil, nil,
	)
	billing := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billing.Stop)
	h := NewOpenAIGatewayHandler(gateway, concurrency, billing,
		service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg), nil, nil, nil, nil, cfg)
	h.concurrencyHelper = NewConcurrencyHelper(concurrency, SSEPingFormatComment, time.Millisecond)
	return h, upstream
}

func TestOpenAIChatConversionRejectsBeforeAccountQueueAndForward(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, acquired := range []bool{false, true} {
		t.Run(fmt.Sprintf("scheduler_acquired_%t", acquired), func(t *testing.T) {
			var accountAcquisitions atomic.Int64
			cache := &chatConversionHandlerConcurrency{}
			cache.acquireAccountSlotFn = func(context.Context, int64, int, string) (bool, error) {
				accountAcquisitions.Add(1)
				return acquired, nil
			}
			h, upstream := newChatConversionHandler(t, service.AccountTypeOAuth, cache)
			c, recorder := newOpenAIIdentityContinuityContext(t, "/v1/chat/completions", []byte(
				`{"model":"gpt-5.4","messages":[{"role":"sensitive-unknown-role","content":"private input"}]}`), nil)

			h.ChatCompletions(c)

			require.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
			require.Equal(t, "invalid_request_error", gjson.Get(recorder.Body.String(), "error.type").String())
			require.Contains(t, gjson.Get(recorder.Body.String(), "error.message").String(), "messages.0.role")
			require.NotContains(t, recorder.Body.String(), "sensitive-unknown-role")
			require.NotContains(t, recorder.Body.String(), "private input")
			require.Zero(t, upstream.calls.Load(), "semantic rejection must not send an upstream request")
			require.Zero(t, cache.waits.Load(), "semantic rejection must not enter the account waiting queue")
			require.Equal(t, int64(1), accountAcquisitions.Load(), "only the scheduler's initial acquisition is allowed")
			if acquired {
				require.Equal(t, int32(1), atomic.LoadInt32(&cache.releaseAccountCalled), "release the scheduler's acquired account slot")
			} else {
				require.Zero(t, atomic.LoadInt32(&cache.releaseAccountCalled))
			}
		})
	}
}

func TestOpenAIChatConversionRejectsAfterUserQueueKeepalive(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var userAcquisitions atomic.Int64
	cache := &chatConversionHandlerConcurrency{}
	cache.acquireUserSlotFn = func(context.Context, int64, int, string) (bool, error) {
		return userAcquisitions.Add(1) > 1, nil
	}
	cache.acquireAccountSlotFn = func(context.Context, int64, int, string) (bool, error) { return true, nil }
	h, upstream := newChatConversionHandler(t, service.AccountTypeOAuth, cache)
	c, recorder := newOpenAIIdentityContinuityContext(t, "/v1/chat/completions", []byte(
		`{"model":"gpt-5.4","stream":true,"messages":[{"role":"unknown-role","content":"private input"}]}`), nil)
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 7401, Concurrency: 1})

	h.ChatCompletions(c)

	require.Equal(t, http.StatusOK, recorder.Code, "the queue already sent response headers")
	require.Contains(t, recorder.Header().Get("Content-Type"), "text/event-stream")
	require.Contains(t, recorder.Body.String(), "event: error\n")
	require.Contains(t, recorder.Body.String(), `"type":"invalid_request_error"`)
	require.NotContains(t, recorder.Body.String(), "private input")
	require.Zero(t, upstream.calls.Load())
	require.Zero(t, cache.waits.Load())
	require.Equal(t, int32(1), atomic.LoadInt32(&cache.releaseUserCalled))
	require.Equal(t, int32(1), atomic.LoadInt32(&cache.releaseAccountCalled))
}

func TestOpenAIChatConversionDoesNotBlockOtherRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name, accountType, body string
	}{
		{"api_key_chat", service.AccountTypeAPIKey, `{"model":"gpt-5.4","messages":[{"role":"unknown-role","content":"hello"}]}`},
		{"oauth_responses_shape", service.AccountTypeOAuth, `{"model":"gpt-5.4","input":[{"role":"user","content":"hello"}],"n":2}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cache := &chatConversionHandlerConcurrency{}
			cache.acquireAccountSlotFn = func(context.Context, int64, int, string) (bool, error) { return true, nil }
			h, upstream := newChatConversionHandler(t, tc.accountType, cache)
			c, recorder := newOpenAIIdentityContinuityContext(t, "/v1/chat/completions", []byte(tc.body), nil)

			h.ChatCompletions(c)

			require.Positive(t, upstream.calls.Load(), recorder.Body.String())
			require.NotEqual(t, "invalid_request_error", gjson.Get(recorder.Body.String(), "error.type").String())
		})
	}
}

func TestOpenAIChatConversionForwardErrorUsesClientError(t *testing.T) {
	for _, streamStarted := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream_started_%t", streamStarted), func(t *testing.T) {
			c, recorder := newGinContextForEndpoint(t, EndpointChatCompletions)
			h := &OpenAIGatewayHandler{}
			err := fmt.Errorf("convert before send: %w", &apicompat.ChatConversionError{Path: "messages.0.role", Reason: "unsupported_role"})
			require.True(t, h.handleOpenAIChatConversionError(c, err, streamStarted))
			require.Contains(t, recorder.Body.String(), "invalid_request_error")
			require.NotContains(t, recorder.Body.String(), "convert before send")
			if streamStarted {
				require.Contains(t, recorder.Body.String(), "event: error\n")
			} else {
				require.Equal(t, http.StatusBadRequest, recorder.Code)
			}
		})
	}
	c, recorder := newGinContextForEndpoint(t, EndpointChatCompletions)
	require.False(t, (&OpenAIGatewayHandler{}).handleOpenAIChatConversionError(c, errors.New("ordinary transport error"), false))
	require.Empty(t, recorder.Body.String())
}
