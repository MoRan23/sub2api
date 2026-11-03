package service

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestCodexEngineTransportClosedPortIsDefinitelyUnsent(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	target := listener.Addr().String()
	require.NoError(t, listener.Close())
	attempt := &codexEngineTransportAttempt{}
	req, err := http.NewRequest(http.MethodPost, "http://"+target, nil)
	require.NoError(t, err)
	client := &http.Client{Transport: &http.Transport{}, Timeout: 5 * time.Second}
	defer client.CloseIdleConnections()
	_, err = client.Do(attempt.traceRequest(req))
	require.Error(t, err)
	require.True(t, attempt.definitelyUnsent(err), "acquiring=%v may_send=%v error=%T %v", attempt.acquiring.Load(), attempt.maySend.Load(), err, err)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	got := (&OpenAIGatewayService{}).handleCodexEngineTransportError(context.Background(), c, engineAccount(), err, attempt)
	var failover *UpstreamFailoverError
	require.ErrorAs(t, got, &failover)
	require.Equal(t, http.StatusBadGateway, failover.StatusCode)
	events, exists := c.Get(OpsUpstreamErrorsKey)
	require.True(t, exists)
	require.Equal(t, engineAccount().ID, events.([]*OpsUpstreamErrorEvent)[0].AccountID)
}

func TestCodexEngineTransportOnlyConfirmedUnsentCanFailover(t *testing.T) {
	dialErr := &url.Error{Op: "Post", URL: "https://engine.example", Err: &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("refused")}}
	dnsErr := &net.DNSError{Name: "engine.example", Err: "not found", IsNotFound: true}
	for _, tc := range []struct {
		name   string
		trace  func(*httptrace.ClientTrace)
		err    error
		unsent bool
	}{
		{"untraced dial", nil, dialErr, false},
		{"untraced EOF", nil, io.EOF, false},
		{"failed dial", func(t *httptrace.ClientTrace) { t.GetConn("engine.example:443") }, dialErr, true},
		{"failed DNS", func(t *httptrace.ClientTrace) { t.GetConn("engine.example:443") }, dnsErr, true},
		{"unclassified before connection", func(t *httptrace.ClientTrace) { t.GetConn("engine.example:443") }, io.EOF, false},
		{"connection acquired", func(t *httptrace.ClientTrace) { t.GetConn("engine.example:443"); t.GotConn(httptrace.GotConnInfo{}) }, dialErr, false},
		{"partial header", func(t *httptrace.ClientTrace) {
			t.GetConn("engine.example:443")
			t.WroteHeaderField("Host", []string{"engine.example"})
		}, dialErr, false},
		{"headers sent", func(t *httptrace.ClientTrace) { t.GetConn("engine.example:443"); t.WroteHeaders() }, dialErr, false},
		{"failed body write", func(t *httptrace.ClientTrace) {
			t.GetConn("engine.example:443")
			t.WroteRequest(httptrace.WroteRequestInfo{Err: io.ErrUnexpectedEOF})
		}, dialErr, false},
		{"redirect dial failed after send", func(t *httptrace.ClientTrace) {
			t.GetConn("engine.example:443")
			t.GotConn(httptrace.GotConnInfo{})
			t.GetConn("redirect.example:443")
		}, dnsErr, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			attempt := &codexEngineTransportAttempt{}
			req := attempt.traceRequest(httptest.NewRequest(http.MethodPost, "/", nil))
			if tc.trace != nil {
				tc.trace(httptrace.ContextClientTrace(req.Context()))
			}
			require.Equal(t, tc.unsent, attempt.definitelyUnsent(tc.err))
		})
	}
}

func TestCodexEngineTransportTraceComposesAndIsConcurrentSafe(t *testing.T) {
	var existing atomic.Int64
	ctx := httptrace.WithClientTrace(context.Background(), &httptrace.ClientTrace{GotConn: func(httptrace.GotConnInfo) { existing.Add(1) }})
	attempt := &codexEngineTransportAttempt{}
	req := attempt.traceRequest(httptest.NewRequest(http.MethodPost, "/", nil).WithContext(ctx))
	trace := httptrace.ContextClientTrace(req.Context())
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			trace.GetConn("engine.example")
			trace.GotConn(httptrace.GotConnInfo{})
			trace.WroteHeaders()
			_ = attempt.definitelyUnsent(io.EOF)
		}()
	}
	wg.Wait()
	require.Equal(t, int64(20), existing.Load())
	require.True(t, attempt.maySend.Load())
}

func TestCodexEngineTransportUnknownKeepsErrorWithoutHealthMutation(t *testing.T) {
	for _, original := range []error{io.EOF, errors.New("connection reset by peer"), errors.New("socks authentication failed")} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		svc := &OpenAIGatewayService{accountRepo: &codexEngineTransportNoMutationRepo{}}
		got := svc.handleCodexEngineTransportError(context.Background(), c, engineAccount(), original, &codexEngineTransportAttempt{})
		require.Same(t, original, got)
		var failover *UpstreamFailoverError
		require.False(t, errors.As(got, &failover))
		_, _, healthEligible := classifyOpenAIAPIKeyHealthFailure(got)
		require.False(t, healthEligible)
		raw, exists := c.Get(OpsUpstreamErrorsKey)
		require.True(t, exists)
		events := raw.([]*OpsUpstreamErrorEvent)
		require.Len(t, events, 1)
		require.Equal(t, "request_error", events[0].Kind)
		require.Equal(t, original.Error(), events[0].Message)
	}
}

// An unexpected repository call panics, so the test checks the absence of health writes.
type codexEngineTransportNoMutationRepo struct{ AccountRepository }

func TestCodexEngineTransportClientCancellationRecordsNoUpstreamFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
	err := (&OpenAIGatewayService{}).handleCodexEngineTransportError(ctx, c, engineAccount(), context.Canceled, &codexEngineTransportAttempt{})
	require.ErrorIs(t, err, context.Canceled)
	_, exists := c.Get(OpsUpstreamErrorsKey)
	require.False(t, exists)
}
