package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func timingTestHeaders(values ...string) http.Header {
	h := make(http.Header)
	if values != nil {
		h[http.CanonicalHeaderKey(openAITimingMetricsHeader)] = values
	}
	return h
}

func TestOpenAITimingMetricsForwardingAndObservation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name    string
		values  []string
		status  string
		enabled bool
	}{
		{"absent", nil, "absent", false},
		{"enabled", []string{"true"}, "valid", true},
		{"disabled", []string{"false"}, "valid", false},
		{"invalid", []string{"private-value"}, "invalid", false},
		{"duplicate", []string{"true", "true"}, "invalid", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := &Account{ID: 6001, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
			svc := &OpenAIGatewayService{cfg: &config.Config{}}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			c.Request.Header = timingTestHeaders(tc.values...)
			body := []byte(`{"model":"gpt-6.1-sol","stream":true,"input":"hello"}`)
			regular, err := svc.buildUpstreamRequest(context.Background(), c, account, body, "token", true, "", false)
			require.NoError(t, err)
			passthrough, err := svc.buildUpstreamRequestOpenAIPassthrough(context.Background(), c, account, body, "token")
			require.NoError(t, err)
			ws, _, err := svc.buildOpenAIWSHeadersWithBody(context.Background(), c, account, "token", OpenAIWSProtocolDecision{Transport: OpenAIUpstreamTransportResponsesWebsocketV2}, false, "", "", "", body, true)
			require.NoError(t, err)
			for _, headers := range []http.Header{regular.Header, passthrough.Header, ws} {
				values, _ := openAITimingMetricsValues(headers)
				require.Equal(t, tc.values, values)
				observed := observeOpenAITimingMetrics(headers, FingerprintObservationEventHTTP)
				require.Equal(t, tc.status, observed.Status)
				if tc.status == "valid" {
					require.NotNil(t, observed.Enabled)
					require.Equal(t, tc.enabled, *observed.Enabled)
				} else {
					require.Nil(t, observed.Enabled)
				}
				encoded, err := json.Marshal(cloneFingerprintObservationHeaders(headers))
				require.NoError(t, err)
				require.NotContains(t, string(encoded), "private-value")
				require.Equal(t, observed, observeOpenAITimingMetrics(cloneFingerprintObservationHeaders(headers), FingerprintObservationEventHTTP))
			}
		})
	}
}

func TestOpenAITimingMetricsAccountOverrideWins(t *testing.T) {
	account := &Account{ID: 6002, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"header_override_enabled": true, "header_overrides": map[string]any{openAITimingMetricsHeader: "false"}}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request.Header = timingTestHeaders("true")
	body := []byte(`{"model":"gpt-6.1-sol","stream":true,"input":"hello"}`)
	regular, err := svc.buildUpstreamRequest(context.Background(), c, account, body, "token", true, "", false)
	require.NoError(t, err)
	passthrough, err := svc.buildUpstreamRequestOpenAIPassthrough(context.Background(), c, account, body, "token")
	require.NoError(t, err)
	ws, _, err := svc.buildOpenAIWSHeadersWithBody(context.Background(), c, account, "token", OpenAIWSProtocolDecision{Transport: OpenAIUpstreamTransportResponsesWebsocketV2}, false, "", "", "", body, true)
	require.NoError(t, err)
	for _, headers := range []http.Header{regular.Header, passthrough.Header, ws} {
		values, present := openAITimingMetricsValues(headers)
		require.True(t, present)
		require.Equal(t, []string{"false"}, values)
	}
	require.NotEqual(t, openAITimingMetricsCompatibility(c.Request.Header), openAITimingMetricsCompatibility(ws))
}

func TestOpenAITimingMetricsCompatibilityAndPrewarm(t *testing.T) {
	variants := []http.Header{timingTestHeaders(), timingTestHeaders("true"), timingTestHeaders("false"), timingTestHeaders("invalid"), timingTestHeaders("true", "true")}
	for i, left := range variants {
		for j, right := range variants {
			a, b := openAIWSAcquireRequest{Headers: left}, openAIWSAcquireRequest{Headers: right}
			require.Equal(t, i == j, sameOpenAIWSPrewarmTarget(a, b))
		}
	}
	require.Equal(t, openAITimingMetricsCompatibility(timingTestHeaders("true")),
		openAITimingMetricsCompatibility(http.Header{openAITimingMetricsHeader: {"true"}}))
	duplicateCase := timingTestHeaders("true")
	duplicateCase[openAITimingMetricsHeader] = []string{"false"}
	require.Equal(t, "invalid", observeOpenAITimingMetrics(duplicateCase, "ws_handshake").Status)
	require.NotEqual(t, openAITimingMetricsCompatibility(timingTestHeaders("true")), openAITimingMetricsCompatibility(duplicateCase))
}

func TestOpenAITimingMetricsPoolAndPinnedContinuation(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 4
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 4
	pool := newOpenAIWSConnPool(cfg)
	defer pool.Close()
	dialer := &openAIWSCountingDialer{}
	pool.setClientDialerForTest(dialer)
	account := &Account{ID: 6003, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	base := openAIWSAcquireRequest{Account: account, WSURL: "wss://example.com/v1/responses"}
	var connIDs []string
	for _, headers := range []http.Header{timingTestHeaders(), timingTestHeaders("true"), timingTestHeaders("false")} {
		req := base
		req.Headers = headers
		lease, err := pool.Acquire(context.Background(), req)
		require.NoError(t, err)
		require.False(t, lease.Reused())
		connIDs = append(connIDs, lease.ConnID())
		lease.Release()
		lease, err = pool.Acquire(context.Background(), req)
		require.NoError(t, err)
		require.True(t, lease.Reused())
		require.Equal(t, connIDs[len(connIDs)-1], lease.ConnID())
		lease.Release()
	}
	require.Equal(t, 3, dialer.DialCount())
	req := base
	req.Headers, req.PreferredConnID = timingTestHeaders("false"), connIDs[1]
	lease, err := pool.Acquire(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, connIDs[2], lease.ConnID(), "ordinary soft preference still requires timing compatibility")
	lease.Release()
	req.ResponseContinuation = true
	lease, err = pool.Acquire(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, connIDs[1], lease.ConnID(), "bound response soft preference permits timing-only difference")
	lease.Release()
	req.Headers, req.PreferredConnID, req.ForcePreferredConn = timingTestHeaders("false"), connIDs[1], true
	lease, err = pool.Acquire(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, connIDs[1], lease.ConnID())
	require.Equal(t, []string{"true"}, lease.FingerprintObservationHeaders().Values(openAITimingMetricsHeader), "pinned continuation retains physical handshake")
	lease.Release()
	for _, change := range []func(*openAIWSAcquireRequest){
		func(r *openAIWSAcquireRequest) { r.IdentityDigest = "different-identity" },
		func(r *openAIWSAcquireRequest) { r.Headers.Set("x-openai-internal-codex-residency", "us") },
		func(r *openAIWSAcquireRequest) { r.Headers.Set("x-codex-beta-features", "changed") },
		func(r *openAIWSAcquireRequest) { r.ProxyURL = "http://different-proxy:8080" },
	} {
		changed := cloneOpenAIWSAcquireRequest(req)
		change(&changed)
		_, err := pool.Acquire(context.Background(), changed)
		require.ErrorIs(t, err, errOpenAIWSPreferredConnUnavailable)
	}
	require.Equal(t, 3, dialer.DialCount())
}

func TestOpenAITimingMetricsObservationUsesPhysicalHandshake(t *testing.T) {
	enableOpenAIIdentityPathFingerprintObservation(t)
	account := &Account{ID: 6004, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	c.Request.Header = timingTestHeaders("false")
	physical := timingTestHeaders("true")
	freezeFingerprintObservationWSHandshake(c, account)(physical)
	svc := &OpenAIGatewayService{}
	record := svc.freezeFingerprintObservationWSFrame(c, account, nil, []byte(`{"type":"response.create","model":"gpt-6.1-sol"}`), physical, nil)
	physical.Set(openAITimingMetricsHeader, "false")
	record()
	entries := SnapshotFingerprintObservations(10)
	require.Len(t, entries, 2)
	for _, entry := range entries {
		require.Equal(t, "ws_handshake", entry.RuntimeTimingMetrics.Source)
		require.Equal(t, "valid", entry.RuntimeTimingMetrics.Status)
		require.True(t, *entry.RuntimeTimingMetrics.Enabled)
	}
	copy := cloneFingerprintObservationEntry(entries[0])
	*copy.RuntimeTimingMetrics.Enabled = false
	require.True(t, *entries[0].RuntimeTimingMetrics.Enabled)
	encoded, err := json.Marshal(FingerprintObservationEntry{})
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "runtime_timing_metrics", "legacy records remain unrecorded")
}

func TestOpenAITimingMetricsProtocolBridges(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, value := range []string{"", "true", "false"} {
		t.Run(value, func(t *testing.T) {
			headers := timingTestHeaders()
			if value != "" {
				headers = timingTestHeaders(value)
			}
			terminal := []byte(`{"type":"response.completed","response":{"id":"resp_timing","model":"gpt-5.5","status":"completed","usage":{"input_tokens":2,"output_tokens":1}}}`)
			account := &Account{ID: 6005, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1,
				Credentials: map[string]any{"api_key": "test-key"}, Extra: map[string]any{"responses_websockets_v2_enabled": true}}
			cfg := newOpenAIWSV2TestConfig()
			cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
			cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
			cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
			dialer := &openAIWSQueueDialer{conns: []openAIWSClientConn{&openAIWSCaptureConn{events: [][]byte{terminal}}}}
			pool := newOpenAIWSConnPool(cfg)
			defer pool.Close()
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusOK,
				Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: " + string(terminal) + "\n\n"))}}
			pool.setClientDialerForTest(dialer)
			svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: upstream, cache: &stubGatewayCache{},
				openaiWSResolver: NewOpenAIWSProtocolResolver(cfg), toolCorrector: NewCodexToolCorrector(), openaiWSPool: pool}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			c.Request.Header = cloneHeader(headers)
			result, err := svc.Forward(context.Background(), c, account, []byte(`{"model":"gpt-5.5","stream":true,"input":"hi"}`))
			require.NoError(t, err)
			require.True(t, result.OpenAIWSMode)
			require.Equal(t, 2, result.Usage.InputTokens)
			got, _ := openAITimingMetricsValues(dialer.DialHeaders(0))
			want, _ := openAITimingMetricsValues(headers)
			require.Equal(t, want, got, "HTTP to WS must preserve explicit timing request")

			c, _ = gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
			c.Request.Header = cloneHeader(headers)
			payload := []byte(`{"type":"response.create","model":"gpt-5.5","stream":true,"input":"hi"}`)
			result, err = svc.proxyOpenAIWSHTTPBridgeTurn(context.Background(), c, account, "test-key", payload, len(payload),
				"gpt-5.5", "", "", "", "", 1, func([]byte) error { return nil })
			require.NoError(t, err)
			require.Equal(t, 2, result.Usage.InputTokens)
			got, _ = openAITimingMetricsValues(upstream.lastReq.Header)
			require.Equal(t, want, got, "WS to HTTP must preserve explicit timing request")
		})
	}
}

func TestOpenAITimingMetricsHTTPWSBoundResponseContinuation(t *testing.T) {
	enableOpenAIIdentityPathFingerprintObservation(t)
	cfg := newOpenAIWSV2TestConfig()
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	conn := &openAIWSCaptureConn{events: [][]byte{
		[]byte(`{"type":"response.completed","response":{"id":"resp_first","model":"gpt-5.5","status":"completed","usage":{"input_tokens":2,"output_tokens":1}}}`),
		[]byte(`{"type":"response.completed","response":{"id":"resp_second","model":"gpt-5.5","status":"completed","usage":{"input_tokens":3,"output_tokens":1}}}`),
	}}
	dialer := &openAIWSQueueDialer{conns: []openAIWSClientConn{conn}}
	pool := newOpenAIWSConnPool(cfg)
	defer pool.Close()
	pool.setClientDialerForTest(dialer)
	svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: &httpUpstreamRecorder{}, cache: &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg), toolCorrector: NewCodexToolCorrector(), openaiWSPool: pool}
	account := &Account{ID: 6006, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1,
		Credentials: map[string]any{"api_key": "test-key"}, Extra: map[string]any{"responses_websockets_v2_enabled": true}}
	for i, value := range []string{"true", "false"} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		c.Request.Header = timingTestHeaders(value)
		body := []byte(`{"model":"gpt-5.5","stream":true,"input":"first"}`)
		if i == 1 {
			body = []byte(`{"model":"gpt-5.5","stream":true,"previous_response_id":"resp_first","input":"second"}`)
		}
		result, err := svc.Forward(context.Background(), c, account, body)
		require.NoError(t, err)
		require.True(t, result.OpenAIWSMode)
		require.Equal(t, i+2, result.Usage.InputTokens)
	}
	require.Equal(t, 1, dialer.DialCount())
	conn.mu.Lock()
	require.Len(t, conn.writes, 2)
	require.Equal(t, "resp_first", conn.writes[1]["previous_response_id"])
	conn.mu.Unlock()
	for _, entry := range SnapshotFingerprintObservations(10) {
		require.Equal(t, "ws_handshake", entry.RuntimeTimingMetrics.Source)
		require.True(t, *entry.RuntimeTimingMetrics.Enabled, "second turn reports original physical timing setting")
	}
}

func TestOpenAITimingMetricsBoundContinuationAtCapacity(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 2
	pool := newOpenAIWSConnPool(cfg)
	defer pool.Close()
	pool.setClientDialerForTest(&openAIWSCountingDialer{})
	req := openAIWSAcquireRequest{Account: &Account{ID: 6007, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, WSURL: "wss://example.com/v1/responses", Headers: timingTestHeaders("true")}
	first, err := pool.Acquire(context.Background(), req)
	require.NoError(t, err)
	req.Headers, req.PreferredConnID, req.ResponseContinuation = timingTestHeaders("false"), first.ConnID(), true
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	type outcome struct {
		lease *openAIWSConnLease
		err   error
	}
	done := make(chan outcome, 1)
	go func() { lease, err := pool.Acquire(ctx, req); done <- outcome{lease, err} }()
	require.Eventually(t, func() bool { return first.conn.waiters.Load() == 1 }, time.Second, time.Millisecond)
	first.Release()
	result := <-done
	require.NoError(t, result.err)
	require.Equal(t, first.ConnID(), result.lease.ConnID())
	result.lease.Release()
}
