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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func engineWSFixture(t *testing.T, svc *OpenAIGatewayService, account *Account, hooksFor func(*gin.Context) *OpenAIWSIngressHooks) (*coderws.Conn, <-chan error) {
	t.Helper()
	done := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, nil)
		if err != nil {
			done <- err
			return
		}
		defer conn.CloseNow()
		_, first, err := conn.Read(r.Context())
		if err != nil {
			done <- err
			return
		}
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = r.Clone(r.Context())
		var hooks *OpenAIWSIngressHooks
		if hooksFor != nil {
			hooks = hooksFor(c)
		}
		done <- svc.ProxyResponsesWebSocketFromClient(r.Context(), c, conn, account, account.GetOpenAIApiKey(), first, hooks)
	}))
	t.Cleanup(server.Close)
	client, _, err := coderws.Dial(t.Context(), "ws"+strings.TrimPrefix(server.URL, "http"), &coderws.DialOptions{HTTPHeader: http.Header{CodexEngineBillingClientRequestHeader: {"handshake-must-not-be-reused"}}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.CloseNow() })
	return client, done
}

func TestCodexEngineWSUsesDedicatedHTTPAndDurableIDPerTurn(t *testing.T) {
	for _, mode := range []string{OpenAIWSIngressModeCtxPool, OpenAIWSIngressModePassthrough, OpenAIWSIngressModeHTTPBridge} {
		t.Run(mode, func(t *testing.T) {
			account := engineAccount()
			account.ID, account.Concurrency = 44, 10
			account.Credentials["model_mapping"] = map[string]any{}
			account.Extra["openai_apikey_responses_websockets_v2_mode"] = mode
			cfg := &config.Config{}
			cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
			cfg.Security.URLAllowlist.AllowInsecureHTTP = true
			var mu sync.Mutex
			var pending []CodexEngineBillingPending
			var sentIDs []string
			var sentBodies [][]byte
			var dispatchErr error
			store := &engineBillingTestStore{create: func(_ context.Context, p *CodexEngineBillingPending) (*CodexEngineBillingJob, error) {
				mu.Lock()
				defer mu.Unlock()
				pending = append(pending, *p)
				return &CodexEngineBillingJob{CodexEngineBillingPending: *p, ID: int64(len(pending))}, nil
			}}
			engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/engine/capacity" {
					_, _ = io.WriteString(w, `{"object":"codex_engine.capacity","instance_id":"`+billingTestInstance+`","capabilities":{"usage_receipts":true,"billing_contract":"confirmed-events"}}`)
					return
				}
				body, _ := io.ReadAll(r.Body)
				id := r.Header.Get(CodexEngineBillingClientRequestHeader)
				mu.Lock()
				if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" || len(pending) != len(sentIDs)+1 || pending[len(pending)-1].ClientRequestID != id {
					dispatchErr = errors.New("request did not persist a unique pending record before HTTP dispatch")
				}
				sentIDs = append(sentIDs, id)
				sentBodies = append(sentBodies, body)
				turn := len(sentIDs)
				mu.Unlock()
				w.Header().Set("Content-Type", "text/event-stream")
				w.Header().Set("X-Request-Id", fmt.Sprintf("req_engine_round_%d", turn))
				w.Header().Set(CodexEngineBillingContractHeader, CodexEngineBillingContract)
				w.Header().Set(CodexEngineBillingInstanceHeader, billingTestInstance)
				w.Header().Set(codexEngineUsageIDHeader, fmt.Sprintf("ceu_%032x", turn))
				_, _ = fmt.Fprintf(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_%d\",\"usage\":{\"input_tokens\":%d,\"output_tokens\":1}}}\n\n", turn, turn*10)
			}))
			defer engine.Close()
			account.Credentials["base_url"] = engine.URL
			upstream := &engineDisconnectUpstream{do: engine.Client().Do}
			accounts := &engineBillingTestAccounts{get: func(context.Context, int64) (*Account, error) { return account, nil }}
			billing := NewCodexEngineBillingService(store, accounts, upstream, cfg)
			svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: upstream, codexEngineBilling: billing,
				billingService: &BillingService{fallbackPrices: map[string]*ModelPricing{"gpt-5.4": {InputPricePerToken: .01, OutputPricePerToken: .01}}}}
			after := make(chan *OpenAIForwardResult, 2)
			afterRequestIDs := make(chan string, 2)
			client, done := engineWSFixture(t, svc, account, func(c *gin.Context) *OpenAIWSIngressHooks {
				c.Writer.Header().Set("X-Request-Id", "req_handshake")
				return &OpenAIWSIngressHooks{BeforeEngineDispatch: func(turn int, original []byte, model string) error {
					SetCodexEngineBillingInput(c, &OpenAIRecordUsageInput{Account: account, User: &User{ID: 1}, APIKey: &APIKey{ID: 2}, PricingAt: time.Now()})
					return nil
				}, AfterTurn: func(_ int, result *OpenAIForwardResult, _ error) {
					afterRequestIDs <- c.Writer.Header().Get("X-Request-Id")
					after <- result
				}}
			})
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			for turn := 1; turn <= 2; turn++ {
				body := fmt.Sprintf(`{"type":"response.create","model":"gpt-5.4","previous_response_id":"resp_previous_%d","tools":[{"type":"code_interpreter","opaque":"unchanged"}],"input":[{"type":"reasoning","encrypted_content":"ciphertext"}],"metadata":{"sequence":900719925474099312345},"client_metadata":{"round":%d}}`, turn, turn)
				require.NoError(t, client.Write(ctx, coderws.MessageText, []byte(body)))
				_, event, err := client.Read(ctx)
				require.NoError(t, err)
				require.Equal(t, fmt.Sprintf("resp_%d", turn), gjson.GetBytes(event, "response.id").String())
				result := <-after
				require.NotNil(t, result)
				require.True(t, result.OpenAIWSMode)
				require.Equal(t, turn*10, result.Usage.InputTokens)
				require.Equal(t, fmt.Sprintf("req_engine_round_%d", turn), <-afterRequestIDs)
			}
			_ = client.CloseNow()
			require.ErrorIs(t, <-done, context.Canceled)
			mu.Lock()
			defer mu.Unlock()
			require.NoError(t, dispatchErr)
			require.Len(t, pending, 2)
			require.Len(t, sentIDs, 2)
			require.NotEqual(t, sentIDs[0], sentIDs[1])
			for i, body := range sentBodies {
				require.NotEqual(t, "handshake-must-not-be-reused", sentIDs[i])
				require.Equal(t, fmt.Sprintf("resp_previous_%d", i+1), gjson.GetBytes(body, "previous_response_id").String())
				require.Equal(t, "code_interpreter", gjson.GetBytes(body, "tools.0.type").String())
				require.Equal(t, "ciphertext", gjson.GetBytes(body, "input.0.encrypted_content").String())
				require.Equal(t, "900719925474099312345", gjson.GetBytes(body, "metadata.sequence").Raw)
				require.False(t, gjson.GetBytes(body, "instructions").Exists())
				require.False(t, gjson.GetBytes(body, "type").Exists())
				require.True(t, gjson.GetBytes(body, "stream").Bool())
			}
		})
	}
}

func TestCodexEngineWSDisconnectImmediatelyCancelsHTTP(t *testing.T) {
	started, canceled := make(chan struct{}), make(chan struct{})
	var requests atomic.Int32
	upstream := &engineDisconnectUpstream{do: func(r *http.Request) (*http.Response, error) {
		requests.Add(1)
		close(started)
		<-r.Context().Done()
		close(canceled)
		return nil, r.Context().Err()
	}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	client, done := engineWSFixture(t, svc, engineAccount(), nil)
	require.NoError(t, client.Write(t.Context(), coderws.MessageText, []byte(`{"type":"response.create","model":"alias"}`)))
	<-started
	_ = client.CloseNow()
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("Engine HTTP execution was not canceled immediately")
	}
	require.ErrorIs(t, <-done, context.Canceled)
	require.EqualValues(t, 1, requests.Load())
}

func TestCodexEngineWSBusinessErrorNotReplayed(t *testing.T) {
	var requests atomic.Int32
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: &engineDisconnectUpstream{do: func(r *http.Request) (*http.Response, error) {
		requests.Add(1)
		return &http.Response{StatusCode: 409, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"code":"task_cooling","message":"wait for checkpoint"}}`))}, nil
	}}}
	client, done := engineWSFixture(t, svc, engineAccount(), nil)
	require.NoError(t, client.Write(t.Context(), coderws.MessageText, []byte(`{"type":"response.create","model":"alias"}`)))
	_, data, err := client.Read(t.Context())
	require.NoError(t, err)
	require.Equal(t, "task_cooling", gjson.GetBytes(data, "error.code").String())
	require.EqualValues(t, 409, gjson.GetBytes(data, "status").Int())
	require.Equal(t, "error", gjson.GetBytes(data, "type").String())
	err = <-done
	var failover *UpstreamFailoverError
	require.False(t, errors.As(err, &failover))
	require.EqualValues(t, 1, requests.Load())
}

func TestCodexEngineWSInterruptStaysControlOnly(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var requests atomic.Int32
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: &engineDisconnectUpstream{do: func(r *http.Request) (*http.Response, error) {
		requests.Add(1)
		close(started)
		select {
		case <-release:
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_current\"}}\n\n"))}, nil
	}}}
	client, done := engineWSFixture(t, svc, engineAccount(), nil)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	require.NoError(t, client.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"alias"}`)))
	<-started
	require.NoError(t, client.Write(ctx, coderws.MessageText, []byte(`{"type":"response.interrupt","response_id":"resp_current"}`)))
	_, data, err := client.Read(ctx)
	require.NoError(t, err)
	require.Equal(t, "response.interrupt.failed", gjson.GetBytes(data, "type").String())
	require.Equal(t, "resp_current", gjson.GetBytes(data, "response_id").String())
	close(release)
	_, data, err = client.Read(ctx)
	require.NoError(t, err)
	require.Equal(t, "response.completed", gjson.GetBytes(data, "type").String())
	_ = client.CloseNow()
	require.ErrorIs(t, <-done, context.Canceled)
	require.EqualValues(t, 1, requests.Load())
}

func TestCodexEngineWSWriterPreservesEventAndWriteFailure(t *testing.T) {
	var events [][]byte
	w := &codexEngineWSWriter{header: http.Header{"Content-Type": {"text/event-stream"}}, status: 200, write: func(data []byte) error {
		events = append(events, append([]byte(nil), data...))
		return nil
	}}
	for _, frame := range []string{": heartbeat\n\n", "event: response.failed\r\ndata: {\"error\":{\"code\":\"blocked\"},\n" + "data: \"status_code\":403}\n\n", "data: [DONE]\n\n"} {
		_, err := w.Write([]byte(frame))
		require.NoError(t, err)
	}
	require.Len(t, events, 1)
	var event map[string]any
	require.NoError(t, json.Unmarshal(events[0], &event))
	require.Equal(t, "response.failed", event["type"])
	w.write = func([]byte) error { return io.ErrClosedPipe }
	_, err := w.Write([]byte("data: {\"type\":\"response.completed\"}\n\n"))
	require.ErrorIs(t, err, io.ErrClosedPipe)
	require.ErrorIs(t, w.FlushError(), io.ErrClosedPipe)
}
