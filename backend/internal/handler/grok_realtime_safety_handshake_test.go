//go:build unit

package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type grokRealtimeSafetyHandshakeAccountRepo struct {
	openAIIdentityContinuityAccountRepo
	healthWrites atomic.Int32
}

func (r *grokRealtimeSafetyHandshakeAccountRepo) SetTempUnschedulable(context.Context, int64, time.Time, string) error {
	r.healthWrites.Add(1)
	return nil
}

// Exercise the real pre-accept handler and WebSocket dialer: a rejected HTTP
// handshake must never be retried, upgraded, or billed as successful audio.
func TestGrokRealtimeSafetyHandshakePreservesBodyBillsOnceAndReleasesSlot(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const body = `{"error":{"code":"safety_review_rejected","type":"permission_error","message":"blocked","param":"input","details":{"policy":"fixture"}},"usage":{"input_tokens":1000,"output_tokens":200},"vendor_extension":{"retained":true}}`
	var dials atomic.Int32
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dials.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-Id", "realtime-handshake-refusal")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(upstream.Close)
	// The default dialer clones this transport, including the local fixture's
	// trusted certificate. This test intentionally does not run in parallel.
	previousHTTPClient := http.DefaultClient
	http.DefaultClient = upstream.Client()
	t.Cleanup(func() { http.DefaultClient = previousHTTPClient })

	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Default.RateMultiplier = 1
	cfg.Gateway.Scheduling.FallbackWaitTimeout = time.Second
	accounts := &grokRealtimeSafetyHandshakeAccountRepo{openAIIdentityContinuityAccountRepo: openAIIdentityContinuityAccountRepo{
		account: service.Account{
			ID: 94109, Name: "realtime-refusal", Platform: service.PlatformGrok,
			Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Concurrency: 1,
			Credentials: map[string]any{"api_key": "fixture-only", "base_url": upstream.URL + "/v1"},
		},
	}}
	var acquisitions atomic.Int32
	cache := &chatConversionHandlerConcurrency{}
	cache.acquireAccountSlotFn = func(context.Context, int64, int, string) (bool, error) {
		acquisitions.Add(1)
		return true, nil
	}
	concurrency := service.NewConcurrencyService(cache)
	settlements := &grokSafetyQueueBillingRepo{}
	pricing := service.NewBillingService(cfg, nil)
	gateway := service.NewOpenAIGatewayService(
		accounts, &frozenUsageLogRepo{}, settlements, nil, nil, nil, nil,
		cfg, nil, concurrency, pricing, nil, nil, nil, &service.DeferredService{}, nil, nil,
		service.NewModelPricingResolver(nil, pricing), nil, nil, nil, nil,
	)
	billing := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billing.Stop)
	h := NewOpenAIGatewayHandler(gateway, concurrency, billing,
		service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg), nil, nil, nil, nil, cfg)

	fee, inputPrice, outputPrice := .4, .0002, .0005
	groupID := int64(94109)
	key := &service.APIKey{
		ID: 94110, GroupID: &groupID, User: &service.User{ID: 94111},
		Group: &service.Group{ID: groupID, Platform: service.PlatformGrok, RateMultiplier: 2,
			GrokSafetyFeeEnabled: true, GrokSafetyFee: &fee,
			ModelPricing: []service.ChannelModelPricing{{Models: []string{"grok-voice-latest"},
				BillingMode: service.BillingModeToken, InputPrice: &inputPrice, OutputPrice: &outputPrice}}},
	}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/realtime?model=grok-voice-latest", nil)
	c.Request.Header.Set("Connection", "Upgrade")
	c.Request.Header.Set("Upgrade", "websocket")
	c.Request.Header.Set("Sec-WebSocket-Version", "13")
	c.Request.Header.Set("Sec-WebSocket-Key", "Zml4dHVyZS1vbmx5LWtleQ==")
	c.Set(string(middleware.ContextKeyAPIKey), key)

	h.GrokRealtime(c)

	require.Equal(t, http.StatusForbidden, recorder.Code, recorder.Body.String())
	require.Equal(t, body, recorder.Body.String(), "preserve extension fields, error type and final usage byte-for-byte")
	require.Empty(t, recorder.Header().Get("Upgrade"), "the rejected upstream must not accept the downstream WebSocket")
	require.Equal(t, int32(1), dials.Load(), "a safety refusal must stop account retry")
	require.Equal(t, int32(1), acquisitions.Load())
	require.Equal(t, int32(1), atomic.LoadInt32(&cache.releaseAccountCalled))
	require.Zero(t, accounts.healthWrites.Load())
	require.True(t, accounts.account.Schedulable)
	require.Nil(t, accounts.account.TempUnschedulableUntil)
	observed, ordinary := settlements.observations()
	require.Len(t, observed, 1, "the handshake has exactly one durable refusal settlement")
	require.Zero(t, ordinary)
	require.NotNil(t, observed[0].row.GrokSafetyRejection)
	require.Equal(t, "realtime-handshake-refusal", observed[0].row.GrokSafetyRejection.UpstreamRequestID)
	require.Equal(t, http.StatusForbidden, observed[0].row.GrokSafetyRejection.HTTPStatus)
	require.NotEmpty(t, observed[0].row.GrokSafetyRejection.CallID)
	require.Equal(t, 1000, observed[0].row.InputTokens)
	require.Equal(t, 200, observed[0].row.OutputTokens)
	require.InDelta(t, .7, observed[0].row.TotalCost, 1e-12)
	require.InDelta(t, 1, observed[0].row.ActualCost, 1e-12)
	require.InDelta(t, fee, observed[0].row.GrokSafetyRejection.Fee, 1e-12)
	require.Zero(t, observed[0].command.BalanceCost, "simple-mode fixture records usage without debiting a wallet")
	require.Zero(t, observed[0].row.ImageCount)
	require.Zero(t, observed[0].row.VideoCount)
}
