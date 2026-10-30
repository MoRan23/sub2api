package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func guardian166TransportFrames() []string {
	valid := `{"type":"response.metadata","response_id":"resp_guardian","metadata":{"guardian":{"item_id":"fc_guardian","requires_review":false},"other":{"keep":900719925474099312345}}}`
	return []string{
		valid,
		valid,
		`{"type":"response.metadata","response_id":"resp_guardian","metadata":{"guardian":{"item_id":"fc_guardian","requires_review":"invalid"}}}`,
		`{"type":"response.completed","response":{"id":"resp_guardian","model":"gpt-5.5","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":11,"output_tokens":3}}}`,
	}
}

func guardian166SSE() string {
	var out strings.Builder
	for _, frame := range guardian166TransportFrames() {
		_, _ = out.WriteString("data: " + frame + "\n\n")
	}
	return out.String()
}

func requireGuardian166Forwarded(t *testing.T, frames [][]byte) {
	t.Helper()
	want := guardian166TransportFrames()
	require.Len(t, frames, len(want), "metadata must not synthesize or consume terminal events")
	for i := 0; i < len(want)-1; i++ {
		require.Equal(t, want[i], string(frames[i]), "valid, duplicate and invalid metadata remain byte-for-byte unchanged")
	}
	require.Equal(t, "response.completed", gjson.GetBytes(frames[len(frames)-1], "type").String())
}

func requireGuardian166Usage(t *testing.T, result *OpenAIForwardResult) {
	t.Helper()
	require.NotNil(t, result)
	require.Equal(t, 11, result.Usage.InputTokens)
	require.Equal(t, 3, result.Usage.OutputTokens)
}

func guardian166SSEFrames(raw string) [][]byte {
	var frames [][]byte
	for _, line := range strings.Split(raw, "\n") {
		if strings.HasPrefix(line, "data: ") {
			payload := strings.TrimPrefix(line, "data: ")
			if gjson.Get(payload, "type").String() != "" {
				frames = append(frames, []byte(payload))
			}
		}
	}
	return frames
}

func guardian166WSConfig() *config.Config {
	cfg := passthroughLifecycleConfig()
	cfg.Gateway.OpenAIFirstOutputTimeoutSeconds = 5
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 5
	cfg.Gateway.OpenAIWS.IngressInterTurnIdleTimeoutSeconds = 5
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	return cfg
}

func TestOpenAI166GuardianTransportHTTP(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, transport := range []string{"http_sse", "http_to_ws", "ws_to_http"} {
		t.Run(transport, func(t *testing.T) {
			cfg := &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(guardian166SSE()))}}
			svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: upstream, toolCorrector: NewCodexToolCorrector(), cache: &stubGatewayCache{}}
			account := passthroughLifecycleAccount()
			account.Extra = map[string]any{}
			if transport == "http_to_ws" {
				cfg = guardian166WSConfig()
				cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = false
				var events [][]byte
				for _, frame := range guardian166TransportFrames() {
					events = append(events, []byte(frame))
				}
				pool := newOpenAIWSConnPool(cfg)
				pool.setClientDialerForTest(&openAIWSCaptureDialer{conn: &openAIWSCaptureConn{events: events}})
				t.Cleanup(pool.Close)
				svc.cfg, svc.openaiWSPool, svc.openaiWSResolver = cfg, pool, NewOpenAIWSProtocolResolver(cfg)
				account.Extra["responses_websockets_v2_enabled"] = true
			}
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			var result *OpenAIForwardResult
			var err error
			var frames [][]byte
			if transport == "ws_to_http" {
				payload := []byte(`{"type":"response.create","model":"gpt-5.5","input":"hello"}`)
				result, err = svc.proxyOpenAIWSHTTPBridgeTurn(context.Background(), c, account, "sk-test", payload, len(payload), "gpt-5.5", "", "", "", "", 1, func(frame []byte) error {
					frames = append(frames, append([]byte(nil), frame...))
					return nil
				})
			} else {
				result, err = svc.Forward(context.Background(), c, account, []byte(`{"model":"gpt-5.5","stream":true,"input":"hello"}`))
				frames = guardian166SSEFrames(rec.Body.String())
			}
			require.NoError(t, err)
			requireGuardian166Usage(t, result)
			requireGuardian166Forwarded(t, frames)
			if transport == "http_to_ws" {
				require.True(t, result.OpenAIWSMode)
			}
		})
	}
}

func TestOpenAI166GuardianTransportNativeWS(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, mode := range []string{OpenAIWSIngressModeCtxPool, OpenAIWSIngressModePassthrough} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			cfg := guardian166WSConfig()
			upstream := newStagedPassthroughConn()
			for _, frame := range guardian166TransportFrames() {
				upstream.Send(frame)
			}
			svc := newPassthroughLifecycleService(cfg, upstream)
			account := passthroughLifecycleAccount()
			account.Extra["openai_apikey_responses_websockets_v2_mode"] = mode
			if mode == OpenAIWSIngressModeCtxPool {
				pool := newOpenAIWSConnPool(cfg)
				pool.setClientDialerForTest(&stagedPassthroughDialer{conn: upstream})
				t.Cleanup(pool.Close)
				svc.openaiWSPool = pool
			}
			var settled atomic.Int32
			type completion struct {
				result *OpenAIForwardResult
				err    error
			}
			completed := make(chan completion, 4)
			server, stopped := startPassthroughLifecycleServerWithHooks(t, ctx, svc, account, func(*gin.Context) *OpenAIWSIngressHooks {
				return &OpenAIWSIngressHooks{AfterTurn: func(_ int, result *OpenAIForwardResult, err error) {
					settled.Add(1)
					completed <- completion{result, err}
				}}
			})
			defer server.Close()
			client := dialPassthroughLifecycleClientWithPayload(t, server, `{"type":"response.create","model":"gpt-5.5","input":"hello"}`)
			defer func() { _ = client.CloseNow() }()
			var frames [][]byte
			for range guardian166TransportFrames() {
				frame, err := readPassthroughLifecycleFrame(t, client, 3*time.Second)
				require.NoError(t, err)
				frames = append(frames, frame)
			}
			requireGuardian166Forwarded(t, frames)
			select {
			case done := <-completed:
				require.NoError(t, done.err)
				requireGuardian166Usage(t, done.result)
			case <-time.After(3 * time.Second):
				t.Fatal("turn did not settle")
			}
			_ = client.Close(coderws.StatusNormalClosure, "done")
			select {
			case err := <-stopped:
				require.NoError(t, err)
			case <-time.After(3 * time.Second):
				t.Fatal("websocket did not stop")
			}
			require.Equal(t, int32(1), settled.Load(), "Guardian notifications are not independent turns or settlements")
		})
	}
}
