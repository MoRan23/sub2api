package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type engineDisconnectUpstream struct {
	do func(*http.Request) (*http.Response, error)
}

func (u *engineDisconnectUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	return u.do(req)
}

func (u *engineDisconnectUpstream) DoWithTLS(req *http.Request, _ string, _ int64, _ int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.do(req)
}

type engineDisconnectWriter struct {
	gin.ResponseWriter
	cancel     context.CancelFunc
	failWrite  bool
	writes     int
	flushes    int
	disconnect chan struct{}
}

func (w *engineDisconnectWriter) Write(data []byte) (int, error) {
	w.writes++
	if w.writes == 1 && w.cancel != nil {
		if !w.failWrite {
			w.cancel()
		}
		close(w.disconnect)
	}
	if w.failWrite {
		return 0, io.ErrClosedPipe
	}
	return w.ResponseWriter.Write(data)
}

func (w *engineDisconnectWriter) Flush() {
	w.flushes++
	w.ResponseWriter.Flush()
}

func engineDisconnectTestContext(t *testing.T, path string, failWrite bool) (context.Context, *gin.Context, *httptest.ResponseRecorder, *engineDisconnectWriter) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, path, nil).WithContext(ctx)
	writer := &engineDisconnectWriter{ResponseWriter: c.Writer, cancel: cancel, failWrite: failWrite, disconnect: make(chan struct{})}
	c.Writer = writer
	return ctx, c, rec, writer
}

func TestCodexEngineDisconnectCollectsDelayedUsageWithoutCancelingUpstream(t *testing.T) {
	for _, path := range []string{"/v1/responses", "/v1/chat/completions", "/v1/messages"} {
		for _, writeFailure := range []bool{false, true} {
			t.Run(path+map[bool]string{false: "/cancel", true: "/write_failure"}[writeFailure], func(t *testing.T) {
				ctx, c, rec, writer := engineDisconnectTestContext(t, path, writeFailure)
				preamble := "event: response.created\ndata: {\"type\":\"response.created\"}\n\n"
				terminal := "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_drain\",\"usage\":{\"input_tokens\":11,\"output_tokens\":4}}}\n\n"
				if path == "/v1/chat/completions" {
					preamble = "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n"
					terminal = "data: {\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":4}}\n\ndata: [DONE]\n\n"
				} else if path == "/v1/messages" {
					preamble = "event: message_start\ndata: {\"message\":{\"usage\":{\"input_tokens\":11}}}\n\n"
					terminal = "event: message_delta\ndata: {\"usage\":{\"output_tokens\":4}}\n\nevent: message_stop\ndata: {}\n\n"
				}
				var attempts atomic.Int32
				upstreamOutcome := make(chan error, 1)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
					attempts.Add(1)
					w.Header().Set("Content-Type", "text/event-stream")
					w.Header().Set("X-Request-Id", "req_drain")
					_, _ = io.WriteString(w, preamble)
					w.(http.Flusher).Flush()
					select {
					case <-writer.disconnect:
					case <-req.Context().Done():
						upstreamOutcome <- req.Context().Err()
						return
					}
					select {
					case <-time.After(80 * time.Millisecond):
					case <-req.Context().Done():
						upstreamOutcome <- req.Context().Err()
						return
					}
					_, err := io.WriteString(w, terminal)
					upstreamOutcome <- err
				}))
				t.Cleanup(server.Close)
				account := engineAccount()
				account.Credentials["base_url"] = server.URL
				cfg := &config.Config{}
				cfg.Security.URLAllowlist.AllowInsecureHTTP = true
				svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: &engineDisconnectUpstream{do: server.Client().Do}}
				result, err := svc.forwardCodexEngine(ctx, c, account, []byte(`{"model":"alias","stream":true}`), path, "")
				require.NoError(t, err)
				require.NoError(t, <-upstreamOutcome, "downstream cancellation must not cancel the unfinished upstream")
				require.True(t, result.ClientDisconnect)
				require.Equal(t, 11, result.Usage.InputTokens)
				require.Equal(t, 4, result.Usage.OutputTokens)
				require.Equal(t, int32(1), attempts.Load(), "a disconnected attempt must never replay")
				require.Equal(t, 1, writer.writes)
				require.Zero(t, writer.flushes, "do not flush after downstream cancellation")
				if writeFailure {
					require.NoError(t, ctx.Err(), "write failure must drain even before request context cancellation")
					require.Empty(t, rec.Body.String())
				} else {
					require.Equal(t, preamble, rec.Body.String(), "stop writing before the delayed terminal")
				}
			})
		}
	}
}

func TestCodexEngineBareErrorDrainsAuthoritativeFailedUsage(t *testing.T) {
	prefix := "event: error\ndata: {\"type\":\"error\",\"code\":\"native_error\",\"message\":\"initial failure\"}\n\n"
	terminal := "event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"usage\":{\"input_tokens\":19,\"output_tokens\":3},\"error\":{\"code\":\"native_error\",\"message\":\"terminal failure\"}}}\n\n"
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(prefix + terminal))}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	result, err := svc.Forward(context.Background(), c, engineAccount(), []byte(`{"model":"alias","stream":true}`))
	var failure *codexEngineResponseError
	require.ErrorAs(t, err, &failure)
	require.Equal(t, "terminal failure", failure.message)
	require.NotNil(t, result)
	require.Equal(t, 19, result.Usage.InputTokens)
	require.Equal(t, 3, result.Usage.OutputTokens)
	require.Equal(t, prefix+terminal, rec.Body.String())
}

func TestCodexEngineBareErrorDiagnosisSurvivesTerminalWithoutErrorFields(t *testing.T) {
	prefix := "event: error\ndata: {\"type\":\"error\",\"code\":\"native_error\",\"message\":\"original diagnosis\",\"status\":409}\n\n"
	terminal := "event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"usage\":{\"input_tokens\":19}}}\n\n"
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(prefix + terminal))}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	result, err := svc.Forward(context.Background(), c, engineAccount(), []byte(`{"model":"alias","stream":true}`))
	var failure *codexEngineResponseError
	require.ErrorAs(t, err, &failure)
	require.Equal(t, "original diagnosis", failure.message)
	require.Equal(t, "native_error", failure.code)
	require.Equal(t, 409, failure.status)
	require.NotNil(t, result)
	require.Equal(t, 19, result.Usage.InputTokens)
	require.Equal(t, prefix+terminal, rec.Body.String())
}

func TestCodexEngineBareErrorSurvivesOversizedFollowingFrame(t *testing.T) {
	prefix := "data: {\"type\":\"error\",\"code\":\"native_error\",\"message\":\"original failure\"}\n\n"
	oversized := "data: {\"type\":\"response.failed\",\"padding\":\"" + strings.Repeat("x", 512) + "\"}\n\n"
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(prefix + oversized))}}
	svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{MaxLineSize: 256}}, httpUpstream: upstream}
	result, err := svc.Forward(context.Background(), c, engineAccount(), []byte(`{"model":"alias","stream":true}`))
	require.Nil(t, result)
	var failure *codexEngineResponseError
	require.ErrorAs(t, err, &failure)
	require.Equal(t, "native_error", failure.code)
	require.Equal(t, "original failure", failure.message)
	require.ErrorContains(t, err, "configured limit")
	require.Equal(t, prefix, rec.Body.String())
}

func TestCodexEngineTerminalEndsWithoutWaitingForEOF(t *testing.T) {
	for _, terminal := range []string{
		"data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":7}}}\n\n",
		"data: {\"type\":\"response.failed\",\"response\":{\"usage\":{\"input_tokens\":7},\"error\":{\"code\":\"native_error\"}}}\n\n",
		"event: message_stop\ndata: {}\n\n",
		"data: [DONE]\n\n",
	} {
		t.Run(terminal, func(t *testing.T) {
			reader, writer := io.Pipe()
			t.Cleanup(func() { _ = writer.Close() })
			go func() { _, _ = io.WriteString(writer, terminal) }()
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: reader}}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
			done := make(chan error, 1)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			go func() {
				_, err := svc.Forward(context.Background(), c, engineAccount(), []byte(`{"model":"alias","stream":true}`))
				done <- err
			}()
			select {
			case err := <-done:
				require.Equal(t, strings.Contains(terminal, "response.failed"), isCodexEngineResponseError(err))
			case <-time.After(time.Second):
				_ = writer.Close()
				t.Fatal("protocol terminal did not release the stream while EOF remained pending")
			}
			require.Equal(t, terminal, rec.Body.String())
		})
	}
}

func TestCodexEngineDisconnectDrainWindowIsBoundedDespiteHeartbeats(t *testing.T) {
	ctx, c, _, writer := engineDisconnectTestContext(t, "/v1/responses", false)
	reader, upstreamWriter := io.Pipe()
	t.Cleanup(func() { _ = upstreamWriter.Close() })
	producerDone := make(chan struct{})
	go func() {
		defer close(producerDone)
		if _, err := io.WriteString(upstreamWriter, "data: {\"type\":\"response.created\"}\n\n"); err != nil {
			return
		}
		for {
			time.Sleep(100 * time.Millisecond)
			if _, err := io.WriteString(upstreamWriter, ": heartbeat\n\n"); err != nil {
				return
			}
		}
	}()
	upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: reader}}
	svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{StreamDataIntervalTimeout: 1}}, httpUpstream: upstream}
	start := time.Now()
	result, err := svc.Forward(ctx, c, engineAccount(), []byte(`{"model":"alias","stream":true}`))
	require.ErrorIs(t, err, errCodexEngineUsageDrainTimeout)
	require.Less(t, time.Since(start), 2*time.Second)
	require.True(t, result.ClientDisconnect)
	require.Zero(t, result.Usage.InputTokens)
	require.Zero(t, result.Usage.OutputTokens)
	require.Equal(t, 1, writer.writes)
	require.Zero(t, writer.flushes)
	select {
	case <-producerDone:
	case <-time.After(time.Second):
		t.Fatal("drain timeout did not close the upstream reader")
	}
	events := c.MustGet(OpsUpstreamErrorsKey).([]*OpsUpstreamErrorEvent)
	require.Len(t, events, 1)
	require.Equal(t, "usage_drain_timeout", events[0].Kind)
}

func TestCodexEngineFirstOutputTimeoutHasDistinctCause(t *testing.T) {
	for _, responseHeaders := range []bool{false, true} {
		t.Run(map[bool]string{false: "headers", true: "stream"}[responseHeaders], func(t *testing.T) {
			var upstream *engineDisconnectUpstream
			if responseHeaders {
				reader, writer := io.Pipe()
				t.Cleanup(func() { _ = writer.Close() })
				upstream = &engineDisconnectUpstream{do: func(*http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: reader}, nil
				}}
			} else {
				upstream = &engineDisconnectUpstream{do: func(req *http.Request) (*http.Response, error) {
					<-req.Context().Done()
					return nil, req.Context().Err()
				}}
			}
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{OpenAIFirstOutputTimeoutSeconds: 1}}, httpUpstream: upstream}
			result, err := svc.Forward(context.Background(), c, engineAccount(), []byte(`{"model":"alias","stream":true}`))
			require.ErrorIs(t, err, errCodexEngineFirstOutputTimeout)
			require.False(t, errors.Is(err, context.Canceled))
			if result != nil {
				require.False(t, result.ClientDisconnect)
			}
			events := c.MustGet(OpsUpstreamErrorsKey).([]*OpsUpstreamErrorEvent)
			require.Len(t, events, 1)
			require.Equal(t, "first_output_timeout", events[0].Kind)
			require.Equal(t, http.StatusGatewayTimeout, events[0].UpstreamStatusCode)
		})
	}
}

func TestCodexEngineDisconnectDrainWindowAlsoBoundsResponseHeaderWait(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	upstream := &engineDisconnectUpstream{do: func(req *http.Request) (*http.Response, error) {
		cancel()
		<-req.Context().Done()
		return nil, req.Context().Err()
	}}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
	svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{StreamDataIntervalTimeout: 1}}, httpUpstream: upstream}
	start := time.Now()
	result, err := svc.Forward(ctx, c, engineAccount(), []byte(`{"model":"alias","stream":true}`))
	require.Nil(t, result)
	require.ErrorIs(t, err, errCodexEngineUsageDrainTimeout)
	require.Less(t, time.Since(start), 2*time.Second)
	events := c.MustGet(OpsUpstreamErrorsKey).([]*OpsUpstreamErrorEvent)
	require.Len(t, events, 1)
	require.Equal(t, "usage_drain_timeout", events[0].Kind)
}

func TestCodexEngineBareErrorSurvivesDisconnectedDrainTimeout(t *testing.T) {
	ctx, c, rec, _ := engineDisconnectTestContext(t, "/v1/responses", false)
	reader, writer := io.Pipe()
	t.Cleanup(func() { _ = writer.Close() })
	prefix := "event: error\ndata: {\"type\":\"error\",\"code\":\"native_error\",\"message\":\"original failure\",\"status\":409}\n\n"
	go func() { _, _ = io.WriteString(writer, prefix) }()
	upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: reader}}
	svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{StreamDataIntervalTimeout: 1}}, httpUpstream: upstream}
	result, err := svc.Forward(ctx, c, engineAccount(), []byte(`{"model":"alias","stream":true}`))
	require.Nil(t, result, "unmetered business rejection must not enter billing after a drain timeout")
	var failure *codexEngineResponseError
	require.ErrorAs(t, err, &failure)
	require.Equal(t, "native_error", failure.code)
	require.Equal(t, "original failure", failure.message)
	require.Equal(t, 409, failure.status)
	require.Equal(t, 409, c.GetInt(OpsUpstreamStatusCodeKey), "business diagnosis keeps precedence over collection timeout")
	require.Equal(t, prefix, rec.Body.String())
}

func TestCodexEngineSynchronousJSONStillUsesCallerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	upstream := &engineDisconnectUpstream{do: func(req *http.Request) (*http.Response, error) {
		t.Fatal("already canceled request must not start an upstream attempt")
		return nil, context.Canceled
	}}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	result, err := svc.Forward(ctx, c, engineAccount(), []byte(`{"model":"alias","stream":false}`))
	require.Nil(t, result)
	require.ErrorIs(t, err, context.Canceled)
	_, recorded := c.Get(OpsUpstreamErrorsKey)
	require.False(t, recorded, "caller cancellation is not an upstream timeout")
}

func TestCodexEngineAlreadyDisconnectedStreamDoesNotStartUpstream(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	upstream := &engineDisconnectUpstream{do: func(*http.Request) (*http.Response, error) {
		t.Fatal("detached context must not start a fresh request for a disconnected caller")
		return nil, context.Canceled
	}}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	result, err := svc.Forward(ctx, c, engineAccount(), []byte(`{"model":"alias","stream":true}`))
	require.Nil(t, result)
	require.ErrorIs(t, err, context.Canceled)
}
