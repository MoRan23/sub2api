package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type bridgeControlHTTPUpstream struct {
	*httpUpstreamRecorder
	do func(*http.Request) (*http.Response, error)
}

func (u *bridgeControlHTTPUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	return u.do(req)
}

type bridgeControlBody struct {
	*io.PipeReader
	closed chan struct{}
	once   sync.Once
}

func (b *bridgeControlBody) Close() error {
	b.once.Do(func() { close(b.closed) })
	return b.PipeReader.Close()
}

type bridgeControlHTTPRequest struct {
	ctx    context.Context
	writer *io.PipeWriter
	closed <-chan struct{}
}

func (r bridgeControlHTTPRequest) send(t *testing.T, payload string) {
	t.Helper()
	_, err := fmt.Fprintf(r.writer, "data: %s\n\n", payload)
	require.NoError(t, err)
}

func startBridgeControlTest(t *testing.T, beforeHeaders bool) (*coderws.Conn, <-chan bridgeControlHTTPRequest, <-chan error, <-chan nativeControlTestSettlement, <-chan *gin.Context) {
	t.Helper()
	cfg := passthroughLifecycleConfig()
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 5
	cfg.Gateway.OpenAIWS.IngressInterTurnIdleTimeoutSeconds = 5
	requests := make(chan bridgeControlHTTPRequest, 4)
	upstream := &bridgeControlHTTPUpstream{httpUpstreamRecorder: &httpUpstreamRecorder{}}
	upstream.do = func(req *http.Request) (*http.Response, error) {
		if beforeHeaders {
			requests <- bridgeControlHTTPRequest{ctx: req.Context()}
			<-req.Context().Done()
			return nil, req.Context().Err()
		}
		reader, writer := io.Pipe()
		body := &bridgeControlBody{PipeReader: reader, closed: make(chan struct{})}
		requests <- bridgeControlHTTPRequest{ctx: req.Context(), writer: writer, closed: body.closed}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"text/event-stream"}},
			Body:       body,
		}, nil
	}
	svc := &OpenAIGatewayService{
		cfg: cfg, httpUpstream: upstream, cache: &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg), toolCorrector: NewCodexToolCorrector(),
	}
	account := passthroughLifecycleAccount()
	account.Extra["openai_apikey_responses_websockets_v2_mode"] = OpenAIWSIngressModeHTTPBridge
	settled := make(chan nativeControlTestSettlement, 4)
	contexts := make(chan *gin.Context, 1)
	server, done := startPassthroughLifecycleServerWithHooks(t, context.Background(), svc, account, func(c *gin.Context) *OpenAIWSIngressHooks {
		contexts <- c
		return &OpenAIWSIngressHooks{AfterTurn: func(_ int, result *OpenAIForwardResult, err error) {
			settled <- nativeControlTestSettlement{result: result, err: err}
		}}
	})
	t.Cleanup(server.Close)
	client := dialPassthroughLifecycleClient(t, server)
	t.Cleanup(func() { _ = client.CloseNow() })
	return client, requests, done, settled, contexts
}

func awaitBridgeControlRequest(t *testing.T, requests <-chan bridgeControlHTTPRequest) bridgeControlHTTPRequest {
	t.Helper()
	select {
	case request := <-requests:
		return request
	case <-time.After(3 * time.Second):
		t.Fatal("bridge HTTP request was not started")
		return bridgeControlHTTPRequest{}
	}
}

func TestOpenAIWSHTTPBridgeInterruptFailsWithoutEndingActiveTurn(t *testing.T) {
	client, requests, done, settled, contexts := startBridgeControlTest(t, false)
	first := awaitBridgeControlRequest(t, requests)
	first.send(t, `{"type":"response.created","response":{"id":"resp_bridge_1"}}`)
	first.send(t, `{"type":"response.output_text.delta","delta":"working"}`)
	nativeControlRead(t, client, "response.created")
	nativeControlRead(t, client, "response.output_text.delta")

	// A queued create must not prevent control frames from being read. It must
	// also remain queued until the real HTTP terminal, not the failed interrupt.
	nativeControlWrite(t, client, `{"type":"response.create","model":"gpt-5.1","input":[]}`)
	for _, responseID := range []string{"resp_bridge_1", "resp_unrelated"} {
		nativeControlWrite(t, client, fmt.Sprintf(`{"type":"response.interrupt","response_id":%q,"mode":"discard_partial_items"}`, responseID))
		failed := nativeControlRead(t, client, "response.interrupt.failed")
		require.Equal(t, responseID, gjson.GetBytes(failed, "response_id").String())
	}
	select {
	case <-first.ctx.Done():
		t.Fatal("failed interrupt canceled the HTTP turn")
	case <-requests:
		t.Fatal("queued create ran before the HTTP terminal")
	case <-settled:
		t.Fatal("control frame settled the active turn")
	default:
	}

	first.send(t, `{"type":"response.completed","response":{"id":"resp_bridge_1","usage":{"input_tokens":7,"output_tokens":3}}}`)
	nativeControlRead(t, client, "response.completed")
	finish := <-settled
	require.NoError(t, finish.err)
	require.Equal(t, 7, finish.result.Usage.InputTokens)
	require.Equal(t, 3, finish.result.Usage.OutputTokens)
	second := awaitBridgeControlRequest(t, requests)
	second.send(t, `{"type":"response.completed","response":{"id":"resp_bridge_2","usage":{"input_tokens":2,"output_tokens":1}}}`)
	nativeControlRead(t, client, "response.completed")
	require.Equal(t, "resp_bridge_2", (<-settled).result.RequestID)
	_ = client.Close(coderws.StatusNormalClosure, "done")
	require.NoError(t, <-done)
	requireNativeReaderStopped(t, contexts)
	select {
	case extra := <-settled:
		t.Fatalf("extra turn settlement: %+v", extra)
	default:
	}
}

func TestOpenAIWSHTTPBridgeDisconnectCancelsPhysicalRequest(t *testing.T) {
	for _, beforeHeaders := range []bool{true, false} {
		t.Run(fmt.Sprintf("before_headers_%t", beforeHeaders), func(t *testing.T) {
			client, requests, done, settled, contexts := startBridgeControlTest(t, beforeHeaders)
			request := awaitBridgeControlRequest(t, requests)
			if !beforeHeaders {
				request.send(t, `{"type":"response.output_text.delta","delta":"partial"}`)
				nativeControlRead(t, client, "response.output_text.delta")
			}
			// Interrupt itself fails; the client's following close is what cancels
			// HTTP, including when it has not produced any response headers yet.
			nativeControlWrite(t, client, `{"type":"response.interrupt","response_id":"resp_bridge","mode":"discard_partial_items"}`)
			nativeControlRead(t, client, "response.interrupt.failed")
			start := time.Now()
			_ = client.CloseNow()
			select {
			case <-request.ctx.Done():
			case <-time.After(openAIWSNativeControlDrainTimeout):
				t.Fatal("downstream close did not cancel HTTP")
			}
			select {
			case err := <-done:
				require.NoError(t, err)
			case <-time.After(openAIWSNativeControlDrainTimeout):
				t.Fatal("bridge did not clean up after downstream close")
			}
			require.Less(t, time.Since(start), 2*openAIWSNativeControlDrainTimeout)
			if request.closed != nil {
				select {
				case <-request.closed:
				default:
					t.Fatal("upstream response body was not closed")
				}
			}
			finish := <-settled
			require.Error(t, finish.err)
			var failover *UpstreamFailoverError
			require.NotErrorAs(t, finish.err, &failover)
			if finish.result != nil {
				require.Zero(t, finish.result.Usage.InputTokens)
				require.Zero(t, finish.result.Usage.OutputTokens)
			}
			requireNativeReaderStopped(t, contexts)
			select {
			case extra := <-settled:
				t.Fatalf("extra canceled turn settlement: %+v", extra)
			default:
			}
		})
	}
}

func TestOpenAIWSHTTPBridgeOnlyOnePendingCreate(t *testing.T) {
	client, requests, done, settled, contexts := startBridgeControlTest(t, false)
	request := awaitBridgeControlRequest(t, requests)
	for i := 0; i < 2; i++ {
		nativeControlWrite(t, client, `{"type":"response.create","model":"gpt-5.1","input":[]}`)
	}
	select {
	case <-request.ctx.Done():
	case <-time.After(openAIWSNativeControlDrainTimeout):
		t.Fatal("pending create overflow left the HTTP request running")
	}
	select {
	case err := <-done:
		var policy *OpenAIWSClientCloseError
		require.ErrorAs(t, err, &policy)
		require.Equal(t, coderws.StatusPolicyViolation, policy.StatusCode())
	case <-time.After(openAIWSNativeControlDrainTimeout):
		t.Fatal("pending create overflow did not terminate the bridge")
	}
	require.Error(t, (<-settled).err)
	requireNativeReaderStopped(t, contexts)
	select {
	case <-requests:
		t.Fatal("overflow started another HTTP request")
	default:
	}
}

func TestOpenAIWSHTTPBridgeFailedWriterBoundsCleanup(t *testing.T) {
	// Some internal callers use the bridge without the shared socket reader.
	// A failed downstream write still closes a stalled response body within the
	// cleanup budget, and must not turn a disconnect into account failover.
	gin.SetMode(gin.TestMode)
	reader, writer := io.Pipe()
	body := &bridgeControlBody{PipeReader: reader, closed: make(chan struct{})}
	t.Cleanup(func() { _ = writer.Close() })
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": {"text/event-stream"}},
		Body:       body,
	}}
	svc := &OpenAIGatewayService{cfg: passthroughLifecycleConfig(), httpUpstream: upstream}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	payload := []byte(`{"type":"response.create","model":"gpt-5.1","input":[]}`)
	finished := make(chan nativeControlTestSettlement, 1)
	wrote := make(chan struct{})
	go func() {
		result, err := svc.proxyOpenAIWSHTTPBridgeTurn(
			context.Background(), c, passthroughLifecycleAccount(), "sk-test", payload, len(payload),
			"gpt-5.1", "", "", "", "", 1,
			func([]byte) error { close(wrote); return io.EOF },
		)
		finished <- nativeControlTestSettlement{result: result, err: err}
	}()
	_, err := io.WriteString(writer, "data: {\"type\":\"keepalive\"}\n\n")
	require.NoError(t, err)
	<-wrote
	select {
	case result := <-finished:
		require.Error(t, result.err)
		var failover *UpstreamFailoverError
		require.NotErrorAs(t, result.err, &failover)
		require.NotNil(t, result.result)
		require.Zero(t, result.result.Usage.InputTokens)
	case <-time.After(2 * openAIWSNativeControlDrainTimeout):
		t.Fatal("failed writer left HTTP cleanup unbounded")
	}
	select {
	case <-body.closed:
	default:
		t.Fatal("cleanup did not close response body")
	}
}
