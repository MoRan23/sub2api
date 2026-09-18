package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func seedOpenAIWSEgressLocation(t *testing.T, resolver *OpenAIEgressLocationService, account *Account, zone, country, region, city string) {
	t.Helper()
	route := OpenAIEgressRoute{}
	if account.ProxyID != nil && account.Proxy != nil {
		route.ProxyID, route.ProxyURL = *account.ProxyID, account.Proxy.URL()
	}
	snapshot := defaultOpenAIEgressLocation(route, resolver.instance)
	snapshot.Timezone, snapshot.CountryCode, snapshot.Region, snapshot.City = zone, country, region, city
	snapshot.Status, snapshot.Source = "resolved", "exit_ip"
	now := resolver.now()
	resolver.mu.Lock()
	resolver.entries[snapshot.RouteKey] = &openAIEgressLocationEntry{good: snapshot, goodAt: now, lastUsed: now}
	resolver.mu.Unlock()
}

func TestOpenAIWSFrameEgressTargetFrozenPerRouteAndTurn(t *testing.T) {
	accepted := time.Date(2026, 1, 2, 7, 59, 0, 0, time.UTC)
	resolver := NewOpenAIEgressLocationService(nil)
	t.Cleanup(resolver.Stop)
	svc := &OpenAIGatewayService{egressLocationService: resolver}
	proxyID := int64(881)
	firstAccount := &Account{ID: 8801, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		ProxyID: &proxyID, Proxy: &Proxy{ID: proxyID, Protocol: "http", Host: "proxy-a.invalid", Port: 8080}}
	secondProxyID := int64(882)
	secondAccount := &Account{ID: 8802, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		ProxyID: &secondProxyID, Proxy: &Proxy{ID: secondProxyID, Protocol: "http", Host: "proxy-b.invalid", Port: 8080}}
	seedOpenAIWSEgressLocation(t, resolver, firstAccount, "America/Los_Angeles", "US", "Washington", "Seattle")
	seedOpenAIWSEgressLocation(t, resolver, secondAccount, "Asia/Tokyo", "JP", "Tokyo", "Tokyo")
	ctx := openai.WithRequestPolicy(context.Background(), openai.DefaultRequestPolicy())
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil).WithContext(ctx)
	body := timezoneWSBody(timezoneWSEnvironment)
	body, err := sjson.SetRawBytes(body, "tools", []byte(`[{"type":"web_search","user_location":{"timezone":"UTC","extra":9007199254740993}}]`))
	require.NoError(t, err)
	first, state := svc.prepareOpenAIWSFrameTimezone(ctx, c, firstAccount, body, false, true, accepted)
	require.Equal(t, "Seattle", gjson.GetBytes(first, "tools.0.user_location.city").String())
	require.Contains(t, gjson.GetBytes(first, "input.0.content.0.text").String(), "2026-01-01")
	seedOpenAIWSEgressLocation(t, resolver, firstAccount, "Europe/Paris", "FR", "Ile-de-France", "Paris")
	retry, repeated := svc.prepareOpenAIWSFrameTimezone(ctx, c, firstAccount, first, false, true, accepted.Add(24*time.Hour))
	require.JSONEq(t, string(first), string(retry), "a refreshed IP cache cannot change the accepted turn's same-route target")
	require.Equal(t, state.AcceptedAt, repeated.AcceptedAt)
	adapted, err := sjson.SetBytes(first, "model", "preserved-adapted-model")
	require.NoError(t, err)
	failedOver, alternate := svc.prepareOpenAIWSFrameTimezone(ctx, c, secondAccount, adapted, false, true, accepted.Add(24*time.Hour))
	require.Equal(t, accepted, alternate.AcceptedAt)
	require.Equal(t, "preserved-adapted-model", gjson.GetBytes(failedOver, "model").String())
	require.Equal(t, "Tokyo", gjson.GetBytes(failedOver, "tools.0.user_location.city").String())
	require.Contains(t, gjson.GetBytes(failedOver, "input.0.content.0.text").String(), "<timezone>Asia/Tokyo</timezone>")
	require.Contains(t, gjson.GetBytes(failedOver, "input.0.content.0.text").String(), "<current_date>2026-01-02</current_date>")
	require.Contains(t, string(body), "Asia/Shanghai")
	newTurn, _ := svc.prepareOpenAIWSFrameTimezone(ctx, c, firstAccount, body, false, false, accepted.Add(time.Hour))
	require.Equal(t, "Paris", gjson.GetBytes(newTurn, "tools.0.user_location.city").String())
}

func TestOpenAIWSFrozenRouteKeepsActualProxyURL(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	proxyID := int64(991)
	account := &Account{ID: 9901, Platform: PlatformOpenAI, ProxyID: &proxyID,
		Proxy: &Proxy{ID: proxyID, Protocol: "http", Host: "frozen.invalid", Port: 8080}}
	frozen := FreezeOpenAIOutboundRoute(c, account)
	account.Proxy.Host = "mutated.invalid"
	require.Equal(t, frozen, OpenAIOutboundRouteForAccount(c, account))
	require.Equal(t, frozen, FreezeOpenAIOutboundRoute(c, account), "later WS turns keep the physical connection's route")
}

type openAIWSEgressCaptureDialer struct {
	conn     *openAIWSCaptureConn
	proxyURL string
}

func (d *openAIWSEgressCaptureDialer) Dial(_ context.Context, _ string, _ http.Header, proxyURL string) (openAIWSClientConn, int, http.Header, error) {
	d.proxyURL = proxyURL
	return d.conn, 0, nil, nil
}

func TestOpenAIWSHTTPAdaptedMapUsesFrozenSourceAndRoute(t *testing.T) {
	accepted := time.Date(2026, 1, 2, 7, 59, 0, 0, time.UTC)
	resolver := NewOpenAIEgressLocationService(nil)
	t.Cleanup(resolver.Stop)
	proxyID := int64(883)
	account := &Account{ID: 8803, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 1,
		Credentials: map[string]any{"access_token": "test", "chatgpt_account_id": "egress-mock"}, Extra: map[string]any{"responses_websockets_v2_enabled": true},
		ProxyID: &proxyID, Proxy: &Proxy{ID: proxyID, Protocol: "http", Host: "wire.invalid", Port: 8080}}
	seedOpenAIWSEgressLocation(t, resolver, account, "Asia/Tokyo", "JP", "Tokyo", "Tokyo")
	cfg := newOpenAIWSV2TestConfig()
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	conn := &openAIWSCaptureConn{events: [][]byte{[]byte(`{"type":"response.completed","response":{"id":"resp_egress_map","model":"gpt-5.1","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}}`)}}
	dialer := &openAIWSEgressCaptureDialer{conn: conn}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(dialer)
	t.Cleanup(pool.Close)
	svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: &httpUpstreamRecorder{}, cache: &stubGatewayCache{},
		toolCorrector: NewCodexToolCorrector(), openaiWSPool: pool, egressLocationService: resolver}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	original := timezoneTestBody(t, map[string]any{"model": "gpt-5.1", "input": []any{
		map[string]any{"type": "function_call_output", "call_id": "call_original", "output": timezoneWSEnvironment},
		timezoneTestMessage(timezoneWSEnvironment),
	}})
	c.Set(openAIRequestTimezoneCaptureKey, &openAIRequestTimezoneCapture{acceptedAt: accepted, body: original})
	frozenRoute := FreezeOpenAIOutboundRoute(c, account)
	account.Proxy.Host = "must-not-be-redialed.invalid"
	_, err := svc.forwardOpenAIWSV2(context.Background(), c, account,
		map[string]any{"model": "gpt-5.1", "input": []any{
			map[string]any{"role": "user", "content": timezoneWSEnvironment},
			timezoneTestMessage(timezoneWSEnvironment),
		}}, "", "", "test", OpenAIWSProtocolDecision{Transport: OpenAIUpstreamTransportResponsesWebsocketV2},
		false, false, "gpt-5.1", "gpt-5.1", accepted, 1, "", nil)
	require.NoError(t, err)
	wire, err := json.Marshal(conn.lastWrite)
	require.NoError(t, err)
	require.Equal(t, frozenRoute.ProxyURL, dialer.proxyURL)
	require.Contains(t, gjson.GetBytes(wire, "input.0.content").String(), "Asia/Shanghai", "adapted tool output cannot become a new environment baseline")
	require.Contains(t, gjson.GetBytes(wire, "input.1.content.0.text").String(), "Asia/Tokyo")
	require.Contains(t, gjson.GetBytes(wire, "input.1.content.0.text").String(), "2026-01-02")
}
