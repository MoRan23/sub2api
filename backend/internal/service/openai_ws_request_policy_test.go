package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func timezoneWSBody(text string) []byte {
	body, _ := json.Marshal(map[string]any{"type": "response.create", "model": "gpt-5.1", "input": text})
	return body
}

const timezoneWSEnvironment = "<environment_context>\n<current_date>2026-01-02</current_date>\n<timezone>Asia/Shanghai</timezone>\n</environment_context>"

func TestOpenAIWSRequestTimezoneFrozenRetryAndNewFrame(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	svc := &OpenAIGatewayService{}
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	ctx := openai.WithRequestPolicy(context.Background(), openai.DefaultRequestPolicy())
	accepted := time.Date(2026, 1, 2, 7, 59, 0, 0, time.UTC)
	body := timezoneWSBody(timezoneWSEnvironment)
	c.Set(openAIRequestTimezoneCaptureKey, &openAIRequestTimezoneCapture{acceptedAt: accepted})
	first, state := svc.prepareOpenAIWSFrameTimezone(ctx, c, account, body, false, true, accepted.Add(time.Hour))
	require.Equal(t, accepted, state.AcceptedAt, "queueing cannot replace ingress time")
	require.Contains(t, gjson.GetBytes(first, "input").String(), "2026-01-01")
	require.Contains(t, gjson.GetBytes(first, "input").String(), OpenAIRequestTimezone)
	retry, retryState := svc.prepareOpenAIWSFrameTimezone(ctx, c, account, body, false, true, accepted.Add(2*time.Hour))
	require.Equal(t, first, retry)
	require.Equal(t, state.AcceptedAt, retryState.AcceptedAt)
	next, nextState := svc.prepareOpenAIWSFrameTimezone(ctx, c, account, body, false, false, accepted.Add(2*time.Hour))
	require.Contains(t, gjson.GetBytes(next, "input").String(), "2026-01-02")
	require.NotEqual(t, state.AcceptedAt, nextState.AcceptedAt, "tool continuation is also a newly accepted frame")
	require.Contains(t, string(body), "Asia/Shanghai", "original identity/audit input remains unchanged")
}

func TestOpenAIWSRequestPolicyChangesOnlyForNewAcceptedFrame(t *testing.T) {
	settings := NewSettingService(&openAIUUIDv7RuntimeRepo{}, nil)
	enabled := openai.DefaultRequestPolicy()
	settings.PublishOpenAIRequestPolicy(enabled)
	svc := &OpenAIGatewayService{settingService: settings}
	ctx := openai.WithRequestPolicy(context.Background(), enabled)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil).WithContext(ctx)
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	body := timezoneWSBody(timezoneWSEnvironment)
	accepted := time.Date(2026, 1, 2, 7, 59, 0, 0, time.UTC)
	first, firstState := svc.prepareOpenAIWSFrameTimezone(ctx, c, account, body, false, true, accepted)
	disabled := openai.RequestPolicy{}
	settings.PublishOpenAIRequestPolicy(disabled)
	retry, retryState := svc.prepareOpenAIWSFrameTimezone(ctx, c, account, body, false, true, accepted.Add(time.Hour))
	require.Equal(t, first, retry)
	require.Equal(t, firstState.Policy, retryState.Policy)
	next, nextState := svc.prepareOpenAIWSFrameTimezone(ctx, c, account, body, false, false, accepted.Add(time.Hour))
	require.Equal(t, body, next)
	require.Equal(t, disabled, nextState.Policy)
	ctx = openAIWSContextForTimezoneState(ctx, c, nextState)
	restoredErr := withOpenAIWSCurrentTurnRetryTimezoneState(newOpenAIWSCurrentTurnFailoverError(errors.New("retry"), next), nextState)
	settings.PublishOpenAIRequestPolicy(enabled)
	restored, ok := OpenAIWSCurrentTurnRetryTimezoneState(restoredErr)
	require.True(t, ok)
	SetRequestTimezoneState(c, restored)
	replay, replayState := svc.prepareOpenAIWSFrameTimezone(ctx, c, account, body, false, true, accepted.Add(2*time.Hour))
	require.Equal(t, next, replay)
	require.Equal(t, disabled, replayState.Policy)
	third, thirdState := svc.prepareOpenAIWSFrameTimezone(ctx, c, account, body, false, false, accepted.Add(2*time.Hour))
	require.Contains(t, gjson.GetBytes(third, "input").String(), OpenAIRequestTimezone)
	require.Equal(t, enabled, thirdState.Policy)
}

func TestOpenAIWSRequestTimezoneFailoverClonePreservesExpandedReplay(t *testing.T) {
	accepted := time.Date(2026, 1, 2, 7, 59, 0, 0, time.UTC)
	body := timezoneWSBody(timezoneWSEnvironment)
	prepared, state := PrepareOpenAIRequestTimezone(body, openai.DefaultRequestPolicy(), accepted, false, true)
	replay, err := json.Marshal(map[string]any{"type": "response.create", "model": "gpt-5.1", "input": []any{
		map[string]any{"role": "assistant", "content": "retained replay history"},
		map[string]any{"role": "user", "content": gjson.GetBytes(prepared, "input").String()},
	}})
	require.NoError(t, err)
	err = withOpenAIWSCurrentTurnRetryTimezoneState(newOpenAIWSCurrentTurnFailoverError(errors.New("limited"), replay), state)
	state.Conversions[0].Output = "mutated"
	restored, ok := OpenAIWSCurrentTurnRetryTimezoneState(err)
	require.True(t, ok)
	require.Equal(t, OpenAIRequestTimezone, restored.Conversions[0].Output)
	restored.Conversions[0].Output = "returned mutation"
	restoredAgain, _ := OpenAIWSCurrentTurnRetryTimezoneState(err)
	require.Equal(t, OpenAIRequestTimezone, restoredAgain.Conversions[0].Output)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	SetRequestTimezoneState(c, restoredAgain)
	result, reused := (&OpenAIGatewayService{}).prepareOpenAIWSFrameTimezone(context.Background(), c,
		&Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, replay, false, true, accepted.Add(24*time.Hour))
	require.JSONEq(t, string(replay), string(result))
	require.Equal(t, accepted, reused.AcceptedAt)
}

func TestOpenAIWSRequestTimezoneFirstFrameSelectsFromOriginalIngress(t *testing.T) {
	original, err := json.Marshal(map[string]any{"type": "response.create", "model": "gpt-5.1", "input": []any{
		map[string]any{"type": "function_call_output", "call_id": "call_1", "output": timezoneWSEnvironment},
		map[string]any{"role": "user", "content": "next question"},
	}})
	require.NoError(t, err)
	adapted, err := json.Marshal(map[string]any{"type": "response.create", "model": "gpt-5.1", "input": []any{
		map[string]any{"role": "user", "content": timezoneWSEnvironment},
		map[string]any{"role": "user", "content": "next question"},
	}})
	require.NoError(t, err)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Set(openAIRequestTimezoneCaptureKey, &openAIRequestTimezoneCapture{acceptedAt: time.Now(), body: original})
	result, _ := (&OpenAIGatewayService{}).prepareOpenAIWSFrameTimezone(context.Background(), c,
		&Account{Platform: PlatformOpenAI}, adapted, false, true, time.Now())
	require.Equal(t, adapted, result, "a handler-created user message must not turn tool output into current environment")
}

func TestOpenAIWSResidencyBuilderOverridesAndPoolIsolation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := &OpenAIGatewayService{}
	account := &Account{ID: 5001, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"header_override_enabled": true, "header_overrides": map[string]any{openai.CodexResidencyHeaderName: "eu"}}}
	build := func(enabled bool) http.Header {
		policy := openai.DefaultRequestPolicy()
		policy.CodexResidencyUS = enabled
		ctx := openai.WithRequestPolicy(context.Background(), policy)
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil).WithContext(ctx)
		c.Request.Header["X-OPENAI-INTERNAL-CODEX-RESIDENCY"] = []string{"eu", "ap"}
		headers, _, err := svc.buildOpenAIWSHeadersWithBody(ctx, c, account, "test", OpenAIWSProtocolDecision{Transport: OpenAIUpstreamTransportResponsesWebsocketV2}, false, "", "", "", nil, false)
		require.NoError(t, err)
		return headers
	}
	enabled, disabled := build(true), build(false)
	require.Equal(t, []string{"us"}, enabled.Values(openai.CodexResidencyHeaderName))
	require.Equal(t, `["eu"]`, normalizeOpenAIWSResidency(disabled))
	require.NotEqual(t, normalizeOpenAIWSHandshakeCompatibility(enabled), normalizeOpenAIWSHandshakeCompatibility(disabled))
	require.Equal(t, normalizeOpenAIWSResidency(http.Header{"X-OPENAI-INTERNAL-CODEX-RESIDENCY": {" US ", "us"}}), normalizeOpenAIWSResidency(enabled))

	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 2
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 2
	pool := newOpenAIWSConnPool(cfg)
	t.Cleanup(pool.Close)
	pool.setClientDialerForTest(&openAIWSFakeDialer{})
	first, err := pool.Acquire(context.Background(), openAIWSAcquireRequest{Account: account, WSURL: "wss://example.test/responses", Headers: enabled})
	require.NoError(t, err)
	second, err := pool.Acquire(context.Background(), openAIWSAcquireRequest{Account: account, WSURL: "wss://example.test/responses", Headers: disabled})
	require.NoError(t, err)
	require.NotEqual(t, first.ConnID(), second.ConnID())
	require.NoError(t, first.WriteJSON(map[string]any{"type": "response.create"}, time.Second), "changing policy must not interrupt active lease")
	first.Release()
	second.Release()
	third, err := pool.Acquire(context.Background(), openAIWSAcquireRequest{Account: account, WSURL: "wss://example.test/responses", Headers: enabled})
	require.NoError(t, err)
	require.Equal(t, first.ConnID(), third.ConnID())
	third.Release()
}

func TestOpenAIWSRequestPolicyReconnectUsesAcceptedFrameSnapshot(t *testing.T) {
	for _, original := range []string{"", "eu"} {
		t.Run("original_"+original, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			serverCtx := ctx
			headersReceived := make(chan http.Header, 3)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				headersReceived <- r.Header.Clone()
				conn, err := coderws.Accept(w, r, nil)
				if err != nil {
					return
				}
				defer conn.CloseNow()
				<-conn.CloseRead(serverCtx).Done()
			}))
			defer upstream.Close()
			cfg := &config.Config{}
			cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 3
			cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 3
			pool := newOpenAIWSConnPool(cfg)
			defer pool.Close()
			settings := NewSettingService(&openAIUUIDv7RuntimeRepo{}, nil)
			enabled := openai.DefaultRequestPolicy()
			settings.PublishOpenAIRequestPolicy(enabled)
			svc := &OpenAIGatewayService{settingService: settings}
			account := &Account{ID: 5002, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
			if original != "" {
				account.Credentials = map[string]any{"header_override_enabled": true, "header_overrides": map[string]any{openai.CodexResidencyHeaderName: original}}
			}
			ctx = openai.WithRequestPolicy(ctx, enabled)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil).WithContext(ctx)
			headers, resolution, err := svc.buildOpenAIWSHeadersWithBody(ctx, c, account, "test", OpenAIWSProtocolDecision{Transport: OpenAIUpstreamTransportResponsesWebsocketV2}, false, "", "", "", nil, false)
			require.NoError(t, err)
			firstFactory := svc.openAIWSHeadersFactory(ctx, account)
			req := openAIWSAcquireRequest{Account: account, WSURL: "ws" + strings.TrimPrefix(upstream.URL, "http"), Headers: headers, HeadersFactory: firstFactory}
			first, err := pool.Acquire(ctx, req)
			require.NoError(t, err)
			require.Equal(t, []string{"us"}, (<-headersReceived).Values(openai.CodexResidencyHeaderName))
			settings.PublishOpenAIRequestPolicy(openai.RequestPolicy{})
			_, nextState := svc.prepareOpenAIWSFrameTimezone(ctx, c, account, timezoneWSBody(timezoneWSEnvironment), false, false, time.Now())
			ctx = openAIWSContextForTimezoneState(ctx, c, nextState)
			svc.resetOpenAIWSResidencyHeaders(ctx, account, headers, resolution.ResidencyBeforePolicy)
			req.HeadersFactory = svc.openAIWSHeadersFactory(ctx, account)
			// A settings update after acceptance cannot change this frame's retry.
			settings.PublishOpenAIRequestPolicy(enabled)
			first.MarkBroken()
			first.Release()
			second, err := pool.Acquire(ctx, req)
			require.NoError(t, err)
			require.NotEqual(t, first.ConnID(), second.ConnID())
			require.Equal(t, original, (<-headersReceived).Get(openai.CodexResidencyHeaderName))
			require.NotEqual(t, normalizeOpenAIWSHandshakeCompatibility(first.FingerprintObservationHeaders()), normalizeOpenAIWSHandshakeCompatibility(second.FingerprintObservationHeaders()))
			lateOriginal, err := firstFactory(context.Background(), resolution.ResidencyBeforePolicy.Clone())
			require.NoError(t, err)
			require.Equal(t, []string{"us"}, lateOriginal.Values(openai.CodexResidencyHeaderName), "a delayed prewarm retains the first frame's captured policy")
			_, thirdState := svc.prepareOpenAIWSFrameTimezone(ctx, c, account, timezoneWSBody(timezoneWSEnvironment), false, false, time.Now())
			ctx = openAIWSContextForTimezoneState(ctx, c, thirdState)
			svc.resetOpenAIWSResidencyHeaders(ctx, account, headers, resolution.ResidencyBeforePolicy)
			req.HeadersFactory = svc.openAIWSHeadersFactory(ctx, account)
			second.MarkBroken()
			second.Release()
			third, err := pool.Acquire(ctx, req)
			require.NoError(t, err)
			require.Equal(t, []string{"us"}, (<-headersReceived).Values(openai.CodexResidencyHeaderName))
			third.Release()
			cancel()
		})
	}
}

func TestOpenAIWSRequestPolicyActualHandshakeAndFrames(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, mode := range []string{OpenAIWSIngressModeCtxPool, OpenAIWSIngressModePassthrough} {
		t.Run(mode, func(t *testing.T) {
			testCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			headersReceived := make(chan http.Header, 2)
			framesReceived := make(chan []byte, 2)
			upstreamErrors := make(chan error, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				headersReceived <- r.Header.Clone()
				conn, err := coderws.Accept(w, r, nil)
				if err != nil {
					upstreamErrors <- err
					return
				}
				defer conn.CloseNow()
				for turn := 1; turn <= 2; turn++ {
					_, frame, err := conn.Read(testCtx)
					if err != nil {
						upstreamErrors <- err
						return
					}
					framesReceived <- frame
					event := fmt.Sprintf(`{"type":"response.completed","response":{"id":"resp_timezone_%d","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`, turn)
					if err := conn.Write(testCtx, coderws.MessageText, []byte(event)); err != nil {
						upstreamErrors <- err
						return
					}
				}
				<-testCtx.Done()
			}))
			defer upstream.Close()
			cfg := passthroughLifecycleConfig()
			cfg.Gateway.OpenAIWS.IngressInterTurnIdleTimeoutSeconds = 0
			cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 2
			cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 2
			settings := NewSettingService(&openAIUUIDv7RuntimeRepo{}, nil)
			settings.PublishOpenAIRequestPolicy(openai.DefaultRequestPolicy())
			svc := &OpenAIGatewayService{cfg: cfg, settingService: settings, httpUpstream: &httpUpstreamRecorder{}, cache: &stubGatewayCache{}, toolCorrector: NewCodexToolCorrector()}
			account := passthroughLifecycleAccount()
			account.Credentials["base_url"] = upstream.URL
			account.Extra["openai_apikey_responses_websockets_v2_mode"] = mode
			accepted := time.Date(2026, 1, 2, 7, 59, 0, 0, time.UTC)
			serverErrors := make(chan error, 1)
			acceptedStates := make(chan *RequestTimezoneState, 1)
			downstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := coderws.Accept(w, r, nil)
				if err != nil {
					serverErrors <- err
					return
				}
				defer conn.CloseNow()
				_, first, err := conn.Read(testCtx)
				if err != nil {
					serverErrors <- err
					return
				}
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = r.Clone(testCtx)
				c.Set(openAIRequestTimezoneCaptureKey, &openAIRequestTimezoneCapture{acceptedAt: accepted})
				hooks := &OpenAIWSIngressHooks{BeforeRequest: func(_ int, _ []byte, _ string) error {
					state, _ := RequestTimezoneStateFromContext(c)
					acceptedStates <- CloneRequestTimezoneState(state)
					return nil
				}}
				serverErrors <- svc.ProxyResponsesWebSocketFromClient(testCtx, c, conn, account, "test", first, hooks)
			}))
			defer downstream.Close()
			defer func() {
				if svc.openaiWSPool != nil {
					svc.openaiWSPool.Close()
				}
			}()
			client, _, err := coderws.Dial(testCtx, "ws"+strings.TrimPrefix(downstream.URL, "http"), nil)
			require.NoError(t, err)
			defer client.CloseNow()
			for turn := 1; turn <= 2; turn++ {
				require.NoError(t, client.Write(testCtx, coderws.MessageText, timezoneWSBody(timezoneWSEnvironment)))
				_, event, err := client.Read(testCtx)
				require.NoError(t, err)
				require.Equal(t, "response.completed", gjson.GetBytes(event, "type").String())
				var frame []byte
				select {
				case frame = <-framesReceived:
				case err := <-upstreamErrors:
					t.Fatal(err)
				case <-testCtx.Done():
					t.Fatal(testCtx.Err())
				}
				text := gjson.GetBytes(frame, "input").String()
				require.NotContains(t, string(frame), openai.CodexResidencyHeaderName)
				if turn == 1 {
					require.Contains(t, text, OpenAIRequestTimezone)
					require.Contains(t, text, "2026-01-01")
					settings.PublishOpenAIRequestPolicy(openai.RequestPolicy{})
				} else {
					state := <-acceptedStates
					require.True(t, state.AcceptedAt.After(accepted))
					require.Equal(t, openai.RequestPolicy{}, state.Policy)
					require.Equal(t, timezoneWSEnvironment, text, "new frame observes the setting change without reconnecting")
				}
			}
			require.Equal(t, []string{"us"}, (<-headersReceived).Values(openai.CodexResidencyHeaderName))
			require.Empty(t, headersReceived, "two frames reuse one physical handshake")
			client.CloseNow()
			cancel()
			select {
			case <-serverErrors:
			case <-time.After(3 * time.Second):
				t.Fatal("WS proxy did not exit")
			}
		})
	}
}

func TestOpenAIWSPooledObservationUsesPhysicalSafeHeaders(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	pool := newOpenAIWSConnPool(cfg)
	defer pool.Close()
	pool.setClientDialerForTest(&openAIWSFakeDialer{})
	req := openAIWSAcquireRequest{Account: &Account{ID: 5002, Platform: PlatformOpenAI, Type: AccountTypeAPIKey},
		WSURL: "wss://example.test/responses", Headers: http.Header{"X-Openai-Internal-Codex-Residency": {"us"}},
		HeadersFactory: func(_ context.Context, headers http.Header) (http.Header, error) {
			headers.Set(openai.CodexResidencyHeaderName, "US")
			headers.Set("Authorization", "Bearer must-not-be-retained")
			headers.Set("User-Agent", "physical-agent")
			return headers, nil
		}}
	lease, err := pool.Acquire(context.Background(), req)
	require.NoError(t, err)
	actual := lease.FingerprintObservationHeaders()
	require.Equal(t, "US", actual.Get(openai.CodexResidencyHeaderName))
	require.Equal(t, "physical-agent", actual.Get("User-Agent"))
	require.Empty(t, actual.Get("Authorization"))
	actual.Set(openai.CodexResidencyHeaderName, "mutated")
	lease.Release()
	req.HeadersFactory = nil
	reused, err := pool.Acquire(context.Background(), req)
	require.NoError(t, err)
	defer reused.Release()
	require.Equal(t, lease.ConnID(), reused.ConnID())
	require.Equal(t, "US", reused.FingerprintObservationHeaders().Get(openai.CodexResidencyHeaderName), "compatible current-request headers cannot replace the physical wire snapshot")
}

func TestOpenAIWSResidencyCrossOriginRedirectAndActualSnapshot(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	targetHeaders := make(chan http.Header, 1)
	targetErrors := make(chan error, 1)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHeaders <- r.Header.Clone()
		conn, err := coderws.Accept(w, r, nil)
		if err != nil {
			targetErrors <- err
			return
		}
		defer conn.CloseNow()
		<-ctx.Done()
	}))
	defer target.Close()
	sourceHeaders := make(chan http.Header, 1)
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sourceHeaders <- r.Header.Clone()
		http.Redirect(w, r, "ws"+strings.TrimPrefix(target.URL, "http"), http.StatusFound)
	}))
	defer source.Close()
	dialer := newDefaultOpenAIWSClientDialer()
	headers := http.Header{}
	headers.Set(openai.CodexResidencyHeaderName, "us")
	headers.Set("Authorization", "Bearer synthetic-test")
	conn, _, _, err := dialer.Dial(ctx, "ws"+strings.TrimPrefix(source.URL, "http"), headers, "")
	require.NoError(t, err)
	defer conn.Close()
	require.Equal(t, "us", (<-sourceHeaders).Get(openai.CodexResidencyHeaderName))
	require.Empty(t, (<-targetHeaders).Get(openai.CodexResidencyHeaderName))
	actual := openAIWSPhysicalObservationHeaders(conn, headers)
	require.Empty(t, actual.Get(openai.CodexResidencyHeaderName), "observation describes the actual final handshake after redirect")
	require.Empty(t, actual.Get("Authorization"))
	require.Empty(t, targetErrors)
	cancel()
}

func TestOpenAIWSRequestPolicyHTTPV2MapAndBridge(t *testing.T) {
	gin.SetMode(gin.TestMode)
	accepted := time.Date(2026, 1, 2, 7, 59, 0, 0, time.UTC)
	account := &Account{ID: 5901, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1,
		Credentials: map[string]any{"api_key": "test"}, Extra: map[string]any{"responses_websockets_v2_enabled": true}}
	cfg := newOpenAIWSV2TestConfig()
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	completed := `{"type":"response.completed","response":{"id":"resp_policy_bridge","model":"gpt-5.1","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}}`
	t.Run("http_to_ws_map", func(t *testing.T) {
		conn := &openAIWSCaptureConn{events: [][]byte{[]byte(completed)}}
		dialer := &openAIWSCaptureDialer{conn: conn}
		pool := newOpenAIWSConnPool(cfg)
		pool.setClientDialerForTest(dialer)
		defer pool.Close()
		svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: &httpUpstreamRecorder{}, cache: &stubGatewayCache{}, toolCorrector: NewCodexToolCorrector(), openaiWSPool: pool}
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		c.Set(openAIRequestTimezoneCaptureKey, &openAIRequestTimezoneCapture{acceptedAt: accepted})
		result, err := svc.forwardOpenAIWSV2(context.Background(), c, account,
			map[string]any{"model": "gpt-5.1", "input": timezoneWSEnvironment}, "", "test",
			OpenAIWSProtocolDecision{Transport: OpenAIUpstreamTransportResponsesWebsocketV2}, false, false, "gpt-5.1", "gpt-5.1", accepted, 1, "", nil)
		require.NoError(t, err)
		require.NotNil(t, result)
		require.Contains(t, conn.lastWrite["input"], OpenAIRequestTimezone)
		require.Contains(t, conn.lastWrite["input"], "2026-01-01")
		require.Equal(t, []string{"us"}, dialer.lastHeaders.Values(openai.CodexResidencyHeaderName))
	})
	t.Run("ws_to_http_keeps_replay", func(t *testing.T) {
		upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusOK,
			Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: " + completed + "\n\n"))}}
		svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: upstream}
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
		SetOpenAIClientTransport(c, OpenAIClientTransportWS)
		prepared, state := PrepareOpenAIRequestTimezone(timezoneWSBody(timezoneWSEnvironment), openai.DefaultRequestPolicy(), accepted, false, true)
		SetRequestTimezoneState(c, state)
		replay, err := json.Marshal(map[string]any{"type": "response.create", "model": "gpt-5.1", "input": []any{
			map[string]any{"role": "assistant", "content": "retained"}, map[string]any{"role": "user", "content": gjson.GetBytes(prepared, "input").String()},
		}})
		require.NoError(t, err)
		result, err := svc.proxyOpenAIWSHTTPBridgeTurn(context.Background(), c, account, "test", replay, len(replay), "gpt-5.1", "", "", "", "", 1, func([]byte) error { return nil })
		require.NoError(t, err)
		require.NotNil(t, result)
		require.Equal(t, "retained", gjson.GetBytes(upstream.lastBody, "input.0.content").String())
		require.Contains(t, gjson.GetBytes(upstream.lastBody, "input.1.content").String(), "2026-01-01")
		require.Equal(t, "us", upstream.lastReq.Header.Get(openai.CodexResidencyHeaderName))
	})
}
