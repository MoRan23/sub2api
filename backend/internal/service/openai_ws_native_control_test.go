package service

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type nativeControlTestConn struct{ *stagedPassthroughConn }

func (c *nativeControlTestConn) WriteJSON(ctx context.Context, value any) error {
	if raw, ok := value.(json.RawMessage); ok {
		return c.WriteFrame(ctx, coderws.MessageText, raw)
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return c.WriteFrame(ctx, coderws.MessageText, payload)
}

type nativeControlTestSettlement struct {
	result *OpenAIForwardResult
	err    error
}

func startNativeControlTest(t *testing.T) (*coderws.Conn, *stagedPassthroughConn, *OpenAIGatewayService, <-chan error, <-chan nativeControlTestSettlement, <-chan *gin.Context) {
	t.Helper()
	upstream := newStagedPassthroughConn()
	cfg := passthroughLifecycleConfig()
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.IngressInterTurnIdleTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(&stagedPassthroughDialer{conn: &nativeControlTestConn{upstream}})
	svc := newPassthroughLifecycleService(cfg, upstream)
	svc.openaiWSPool = pool
	account := passthroughLifecycleAccount()
	account.Extra["openai_apikey_responses_websockets_v2_mode"] = OpenAIWSIngressModeCtxPool
	settled := make(chan nativeControlTestSettlement, 4)
	contexts := make(chan *gin.Context, 1)
	server, done := startPassthroughLifecycleServerWithHooks(t, context.Background(), svc, account, func(c *gin.Context) *OpenAIWSIngressHooks {
		contexts <- c
		return &OpenAIWSIngressHooks{AfterTurn: func(_ int, result *OpenAIForwardResult, err error) {
			settled <- nativeControlTestSettlement{result, err}
		}}
	})
	t.Cleanup(server.Close)
	client := dialPassthroughLifecycleClient(t, server)
	t.Cleanup(func() { _ = client.CloseNow(); _ = upstream.Close() })
	require.Equal(t, "response.create", gjson.GetBytes(requirePassthroughUpstreamWrite(t, upstream, time.Second), "type").String())
	return client, upstream, svc, done, settled, contexts
}

func nativeControlWrite(t *testing.T, client *coderws.Conn, payload string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, client.Write(ctx, coderws.MessageText, []byte(payload)))
}

func nativeControlRead(t *testing.T, client *coderws.Conn, kind string) []byte {
	t.Helper()
	payload, err := readPassthroughLifecycleFrame(t, client, 3*time.Second)
	require.NoError(t, err)
	require.Equal(t, kind, gjson.GetBytes(payload, "type").String())
	return payload
}

func requireNativeReaderStopped(t *testing.T, contexts <-chan *gin.Context) {
	t.Helper()
	c := <-contexts
	value, ok := c.Get(openAIWSNativeControlContextKey)
	require.True(t, ok)
	control, ok := value.(*openAIWSNativeControl)
	require.True(t, ok)
	select {
	case <-control.done:
	case <-time.After(time.Second):
		t.Fatal("native downstream reader did not join")
	}
}

func TestOpenAIWSNativeInterruptAckOrdersAndNextTurn(t *testing.T) {
	for _, ackFirst := range []bool{true, false} {
		t.Run(map[bool]string{true: "ack_first", false: "terminal_first"}[ackFirst], func(t *testing.T) {
			client, upstream, svc, done, settled, contexts := startNativeControlTest(t)
			upstream.Send(`{"type":"response.created","response":{"id":"resp_native_1"}}`)
			nativeControlRead(t, client, "response.created")
			interrupt := `{"type":"response.interrupt","response_id":"resp_native_1","mode":"discard_partial_items","extension":"preserved"}`
			nativeControlWrite(t, client, interrupt)
			require.Equal(t, interrupt, string(requirePassthroughUpstreamWrite(t, upstream, time.Second)))
			ack := `{"type":"response.interrupt.accepted","response_id":"resp_native_1","extra":true}`
			completed := `{"type":"response.completed","response":{"id":"resp_native_1","usage":{"input_tokens":7,"output_tokens":3}}}`
			if ackFirst {
				upstream.Send(ack)
				upstream.Send(completed)
				require.Equal(t, ack, string(nativeControlRead(t, client, "response.interrupt.accepted")))
				nativeControlRead(t, client, "response.completed")
			} else {
				upstream.Send(completed)
				upstream.Send(ack)
				nativeControlRead(t, client, "response.completed")
				require.Equal(t, ack, string(nativeControlRead(t, client, "response.interrupt.accepted")))
			}
			first := <-settled
			require.NoError(t, first.err)
			require.Equal(t, 7, first.result.Usage.InputTokens)
			nativeControlWrite(t, client, `{"type":"response.create","model":"gpt-5.1","input":[]}`)
			require.Equal(t, "response.create", gjson.GetBytes(requirePassthroughUpstreamWrite(t, upstream, time.Second), "type").String())
			upstream.Send(`{"type":"response.completed","response":{"id":"resp_native_2","usage":{"input_tokens":2,"output_tokens":1}}}`)
			nativeControlRead(t, client, "response.completed")
			require.Equal(t, "resp_native_2", (<-settled).result.RequestID)
			_ = client.Close(coderws.StatusNormalClosure, "done")
			require.NoError(t, <-done)
			requireNativeReaderStopped(t, contexts)
			require.Equal(t, int64(1), svc.SnapshotOpenAIWSPoolMetrics().AcquireTotal)
			select {
			case <-upstream.closed:
				t.Fatal("clean interrupted connection was evicted")
			default:
			}
			select {
			case extra := <-settled:
				t.Fatalf("extra settlement: %+v", extra)
			default:
			}
		})
	}
}

func TestOpenAIWSNativeInterruptCleanupRetainsTerminalUsage(t *testing.T) {
	for _, ack := range []string{"", `{"type":"response.interrupt.failed","response_id":"resp_native_1"}`, `{"type":"response.interrupt.accepted","response_id":"resp_wrong"}`} {
		t.Run(ack, func(t *testing.T) {
			client, upstream, _, done, settled, contexts := startNativeControlTest(t)
			upstream.Send(`{"type":"response.created","response":{"id":"resp_native_1"}}`)
			nativeControlRead(t, client, "response.created")
			nativeControlWrite(t, client, `{"type":"response.interrupt","response_id":"resp_native_1","mode":"discard_partial_items"}`)
			requirePassthroughUpstreamWrite(t, upstream, time.Second)
			if ack != "" {
				upstream.Send(ack)
				nativeControlRead(t, client, gjson.Get(ack, "type").String())
			}
			upstream.Send(`{"type":"response.completed","response":{"id":"resp_native_1","usage":{"input_tokens":7,"output_tokens":3}}}`)
			nativeControlRead(t, client, "response.completed")
			_, err := readPassthroughLifecycleFrame(t, client, 3*time.Second)
			require.Error(t, err)
			var closeErr coderws.CloseError
			require.ErrorAs(t, err, &closeErr)
			require.Equal(t, coderws.StatusTryAgainLater, closeErr.Code)
			require.Error(t, <-done)
			result := <-settled
			require.NoError(t, result.err)
			require.Equal(t, 7, result.result.Usage.InputTokens)
			requireNativeReaderStopped(t, contexts)
			select {
			case <-upstream.closed:
			default:
				t.Fatal("unsettled interrupt returned to pool")
			}
			select {
			case extra := <-settled:
				t.Fatalf("extra settlement: %+v", extra)
			default:
			}
			select {
			case replay := <-upstream.writes:
				t.Fatalf("interrupted turn replayed: %s", replay)
			default:
			}
		})
	}
}

func TestOpenAIWSNativeInterruptControlErrorsAndDisconnectDrain(t *testing.T) {
	client, upstream, _, done, settled, contexts := startNativeControlTest(t)
	upstream.Send(`{"type":"response.created","response":{"id":"resp_native_1"}}`)
	nativeControlRead(t, client, "response.created")
	nativeControlWrite(t, client, `{"type":"response.interrupt","response_id":"wrong"}`)
	require.Equal(t, "wrong", gjson.GetBytes(nativeControlRead(t, client, "response.interrupt.failed"), "response_id").String())
	nativeControlWrite(t, client, `{"type":"response.interrupt","response_id":1}`)
	require.Equal(t, int64(400), gjson.GetBytes(nativeControlRead(t, client, "error"), "status").Int())
	select {
	case frame := <-upstream.writes:
		t.Fatalf("invalid control forwarded: %s", frame)
	default:
	}
	select {
	case result := <-settled:
		t.Fatalf("control error settled turn: %+v", result)
	default:
	}
	_ = client.CloseNow()
	upstream.Send(`{"type":"response.completed","response":{"id":"resp_native_1","usage":{"input_tokens":7,"output_tokens":3}}}`)
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("disconnect drain did not exit")
	}
	result := <-settled
	require.NoError(t, result.err)
	require.Equal(t, 7, result.result.Usage.InputTokens)
	requireNativeReaderStopped(t, contexts)
	select {
	case <-upstream.closed:
	default:
		t.Fatal("disconnected upstream lease returned to pool")
	}
}

func TestOpenAIWSNativeInterruptTimeoutDoesNotReplay(t *testing.T) {
	client, upstream, _, done, settled, contexts := startNativeControlTest(t)
	upstream.Send(`{"type":"response.created","response":{"id":"resp_native_1"}}`)
	nativeControlRead(t, client, "response.created")
	nativeControlWrite(t, client, `{"type":"response.interrupt","response_id":"resp_native_1"}`)
	requirePassthroughUpstreamWrite(t, upstream, time.Second)
	upstream.Send(`{"type":"response.interrupt.failed","response_id":"resp_native_1"}`)
	nativeControlRead(t, client, "response.interrupt.failed")
	_, err := readPassthroughLifecycleFrame(t, client, 3*time.Second)
	require.Error(t, err)
	serverErr := <-done
	var failover *UpstreamFailoverError
	require.False(t, errors.As(serverErr, &failover))
	require.Error(t, serverErr)
	result := <-settled
	require.Nil(t, result.result)
	require.Error(t, result.err)
	requireNativeReaderStopped(t, contexts)
	select {
	case replay := <-upstream.writes:
		t.Fatalf("interrupted turn replayed: %s", replay)
	default:
	}
}

func TestOpenAIWSNativeQueuedCreateDoesNotBlockInterrupt(t *testing.T) {
	client, upstream, _, done, settled, contexts := startNativeControlTest(t)
	upstream.Send(`{"type":"response.created","response":{"id":"resp_native_1"}}`)
	nativeControlRead(t, client, "response.created")
	nativeControlWrite(t, client, `{"type":"response.create","model":"gpt-5.1","input":[]}`)
	nativeControlWrite(t, client, `{"type":"response.interrupt","response_id":"resp_native_1"}`)
	frame := requirePassthroughUpstreamWrite(t, upstream, time.Second)
	require.Equal(t, "response.interrupt", gjson.GetBytes(frame, "type").String())
	select {
	case frame := <-upstream.writes:
		t.Fatalf("queued create overlapped active response: %s", frame)
	default:
	}
	upstream.Send(`{"type":"response.completed","response":{"id":"resp_native_1","usage":{"input_tokens":7,"output_tokens":3}}}`)
	upstream.Send(`{"type":"response.interrupt.accepted","response_id":"resp_native_1"}`)
	nativeControlRead(t, client, "response.completed")
	nativeControlRead(t, client, "response.interrupt.accepted")
	require.Equal(t, "response.create", gjson.GetBytes(requirePassthroughUpstreamWrite(t, upstream, time.Second), "type").String())
	require.Equal(t, "resp_native_1", (<-settled).result.RequestID)
	upstream.Send(`{"type":"response.completed","response":{"id":"resp_native_2","usage":{"input_tokens":2,"output_tokens":1}}}`)
	nativeControlRead(t, client, "response.completed")
	require.Equal(t, "resp_native_2", (<-settled).result.RequestID)
	// An idle control frame receives a control acknowledgement without opening
	// another response, changing the previous usage, or reaching the pool socket.
	nativeControlWrite(t, client, `{"type":"response.interrupt","response_id":"resp_native_2"}`)
	require.Equal(t, "resp_native_2", gjson.GetBytes(nativeControlRead(t, client, "response.interrupt.failed"), "response_id").String())
	_ = client.Close(coderws.StatusNormalClosure, "done")
	require.NoError(t, <-done)
	requireNativeReaderStopped(t, contexts)
	select {
	case frame := <-upstream.writes:
		t.Fatalf("idle control reached upstream: %s", frame)
	default:
	}
}

func TestOpenAIWSNativeDisconnectedActiveTurnHasBoundedDrain(t *testing.T) {
	client, upstream, _, done, settled, contexts := startNativeControlTest(t)
	upstream.Send(`{"type":"response.created","response":{"id":"resp_native_1"}}`)
	nativeControlRead(t, client, "response.created")
	_ = client.CloseNow()
	select {
	case err := <-done:
		require.True(t, IsOpenAIWSInterruptCleanupError(err))
	case <-time.After(2 * time.Second):
		t.Fatal("disconnected response did not stop within cleanup budget")
	}
	require.Nil(t, (<-settled).result)
	requireNativeReaderStopped(t, contexts)
	select {
	case <-upstream.closed:
	default:
		t.Fatal("incomplete response returned to pool")
	}
}

type blockingNativeControlWriteConn struct {
	*stagedPassthroughConn
	writeStarted      chan struct{}
	allowWrite        chan struct{}
	writing           atomic.Bool
	closedDuringWrite atomic.Bool
}

func (c *blockingNativeControlWriteConn) WriteJSON(ctx context.Context, value any) error {
	c.writing.Store(true)
	defer c.writing.Store(false)
	close(c.writeStarted)
	select {
	case <-c.allowWrite:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *blockingNativeControlWriteConn) Close() error {
	if c.writing.Load() {
		c.closedDuringWrite.Store(true)
	}
	return c.stagedPassthroughConn.Close()
}

func TestOpenAIWSNativeRetireWaitsForControlWrite(t *testing.T) {
	upstream := &blockingNativeControlWriteConn{stagedPassthroughConn: newStagedPassthroughConn(), writeStarted: make(chan struct{}), allowWrite: make(chan struct{})}
	conn := newOpenAIWSConn("control_barrier", 1, upstream, nil)
	pool := &openAIWSConnPool{}
	pool.accounts.Store(int64(1), &openAIWSAccountPool{conns: map[string]*openAIWSConn{conn.id: conn}})
	lease := &openAIWSConnLease{conn: conn, pool: pool, accountID: 1}
	localAcks := make(chan []byte, 1)
	control := &openAIWSNativeControl{creates: make(chan []byte, 1), writeTimeout: time.Second, writer: func(payload []byte) error { localAcks <- payload; return nil }}
	control.begin(context.Background(), lease)
	control.observeCreated("resp_barrier")
	writeDone := make(chan error, 1)
	go func() {
		writeDone <- control.interrupt([]byte(`{"type":"response.interrupt","response_id":"resp_barrier"}`))
	}()
	<-upstream.writeStarted
	retired := make(chan struct{})
	go func() { control.retireLease(lease); close(retired) }()
	select {
	case <-retired:
		t.Fatal("pool eviction overtook the active control write")
	case <-time.After(30 * time.Millisecond):
	}
	close(upstream.allowWrite)
	require.NoError(t, <-writeDone)
	select {
	case <-retired:
	case <-time.After(time.Second):
		t.Fatal("retirement did not join the writer")
	}
	require.False(t, upstream.closedDuringWrite.Load())
	require.True(t, control.interruptedTurn(), "detachment must retain the no-replay decision")
	require.True(t, control.mustRetire(), "unsettled acknowledgement must survive detachment")
	require.NoError(t, control.interrupt([]byte(`{"type":"response.interrupt","response_id":"resp_barrier"}`)))
	require.Equal(t, "response.interrupt.failed", gjson.GetBytes(<-localAcks, "type").String())
}
