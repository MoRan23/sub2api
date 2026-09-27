package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/httpsendobserver"
	pluginv1 "github.com/Wei-Shaw/sub2api/pkg/pluginapi/v1"
	hcplugin "github.com/hashicorp/go-plugin"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Use the real plugin manager/runtime framing boundary with synthetic data.
type pluginHTTPBoundaryClient struct {
	pluginv1.TransportPluginClient
	mu                     sync.Mutex
	frames                 []*pluginv1.ForwardResponse
	start                  *pluginv1.ForwardRequestStart
	body                   []byte
	sendErr                error
	returnFrameAfterCancel bool
	cancelRPC              bool
	calls                  atomic.Int64
}

func (c *pluginHTTPBoundaryClient) Forward(ctx context.Context, _ ...grpc.CallOption) (grpc.BidiStreamingClient[pluginv1.ForwardRequest, pluginv1.ForwardResponse], error) {
	c.calls.Add(1)
	return &pluginHTTPBoundaryStream{client: c, ctx: ctx, sent: make(chan struct{})}, nil
}

type pluginHTTPBoundaryStream struct {
	grpc.BidiStreamingClient[pluginv1.ForwardRequest, pluginv1.ForwardResponse]
	client    *pluginHTTPBoundaryClient
	ctx       context.Context
	sent      chan struct{}
	closeOnce sync.Once
	next      int
}

func (s *pluginHTTPBoundaryStream) Context() context.Context { return s.ctx }
func (s *pluginHTTPBoundaryStream) Send(frame *pluginv1.ForwardRequest) error {
	s.client.mu.Lock()
	defer s.client.mu.Unlock()
	if start := frame.GetStart(); start != nil {
		s.client.start = start
		return s.client.sendErr
	}
	if chunk := frame.GetBodyChunk(); len(chunk) > 0 {
		s.client.body = append(s.client.body, chunk...)
	}
	return nil
}
func (s *pluginHTTPBoundaryStream) CloseSend() error {
	s.closeOnce.Do(func() { close(s.sent) })
	return nil
}
func (s *pluginHTTPBoundaryStream) Recv() (*pluginv1.ForwardResponse, error) {
	if s.client.cancelRPC {
		<-s.ctx.Done()
		return nil, status.Error(codes.Canceled, "context canceled")
	}
	if s.client.returnFrameAfterCancel {
		<-s.ctx.Done()
	} else {
		select {
		case <-s.sent:
		case <-s.ctx.Done():
			return nil, s.ctx.Err()
		}
	}
	if s.next == len(s.client.frames) {
		return nil, io.EOF
	}
	frame := s.client.frames[s.next]
	s.next++
	return frame, nil
}

func pluginHTTPBoundaryManager(client *pluginHTTPBoundaryClient) *PluginManager {
	manager := &PluginManager{}
	manager.route.Store(&pluginRoute{pluginID: 71, rolloutPercent: 100, runtime: &pluginRuntime{client: hcplugin.NewClient(&hcplugin.ClientConfig{}), api: client}})
	return manager
}

func pluginHTTPBoundaryFrames(t *testing.T, response *http.Response) []*pluginv1.ForwardResponse {
	t.Helper()
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	return []*pluginv1.ForwardResponse{
		{Frame: &pluginv1.ForwardResponse_Start{Start: &pluginv1.ForwardResponseStart{StatusCode: int32(response.StatusCode), Status: response.Status, Headers: headersToPlugin(response.Header), ContentLength: -1}}},
		{Frame: &pluginv1.ForwardResponse_BodyChunk{BodyChunk: body}},
		{Frame: &pluginv1.ForwardResponse_End{End: &pluginv1.ForwardResponseEnd{}}},
	}
}

func pluginHTTPBoundaryAccount() *Account {
	return &Account{ID: 31, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 1,
		Credentials:                  map[string]any{"access_token": "synthetic"},
		OpenAIOAuthCredentialOwnerID: 31, OpenAIOAuthCredentialOS: OpenAIOSWindows, OpenAIOAuthAuthorizationGeneration: "shared-grant"}
}

func TestOpenAIPluginHTTPObserverAndOpaqueHeaders(t *testing.T) {
	response := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Set-Cookie": {"__oailb=ignored; Secure; Path=/"}}, Body: io.NopCloser(strings.NewReader(`{"status":"completed"}`))}
	client := &pluginHTTPBoundaryClient{frames: pluginHTTPBoundaryFrames(t, response)}
	manager := pluginHTTPBoundaryManager(client)
	observations := 0
	ctx := httpsendobserver.WithObserver(context.Background(), func(request *http.Request) {
		observations++
		require.Equal(t, "opaque-original", request.Header.Get(openAICodexTurnStateHeader))
		require.Empty(t, request.Header.Get("Cookie"), "transport cannot add pooled cookies")
	})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, chatgptCodexURL, strings.NewReader(`{"model":"gpt-test"}`))
	require.NoError(t, err)
	request.Header.Set("Authorization", "Bearer synthetic")
	request.Header.Set(openAICodexTurnStateHeader, "opaque-original")
	got, handled, err := manager.RoundTripOpenAIOAuth(ctx, request, "", pluginHTTPBoundaryAccount())
	require.NoError(t, err)
	require.True(t, handled)
	_, err = io.Copy(io.Discard, got.Body)
	require.NoError(t, err)
	require.NoError(t, got.Body.Close())
	require.Equal(t, 1, observations)
	require.EqualValues(t, 1, client.calls.Load())
	client.mu.Lock()
	headers := headersFromPlugin(client.start.Headers)
	client.mu.Unlock()
	require.Equal(t, "opaque-original", headers.Get(openAICodexTurnStateHeader))
	require.Empty(t, headers.Get("Cookie"))
}

func TestOpenAIPluginHTTPUnadmittedDispatchDoesNotObserveSend(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		manager := &PluginManager{}
		if unavailable {
			manager.route.Store(&pluginRoute{pluginID: 71, rolloutPercent: 100, unavailable: "test unavailable"})
		}
		calls := 0
		ctx := httpsendobserver.WithObserver(context.Background(), func(*http.Request) { calls++ })
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, chatgptCodexURL, nil)
		require.NoError(t, err)
		_, handled, err := manager.RoundTripOpenAIOAuth(ctx, request, "", pluginHTTPBoundaryAccount())
		require.Equal(t, unavailable, handled)
		if unavailable {
			require.Error(t, err)
		} else {
			require.NoError(t, err)
		}
		require.Zero(t, calls)
	}
}

func TestOpenAIPluginHTTPUncertainSendIsNotReplayed(t *testing.T) {
	for _, failure := range []string{"send_error", "plugin_error", "invalid_headers"} {
		t.Run(failure, func(t *testing.T) {
			client := &pluginHTTPBoundaryClient{}
			switch failure {
			case "send_error":
				client.sendErr = errors.New("synthetic RPC send error")
			case "plugin_error":
				client.frames = []*pluginv1.ForwardResponse{{Frame: &pluginv1.ForwardResponse_Error{Error: &pluginv1.ForwardResponseError{Code: "TEST_SENT", Message: "synthetic disconnect", RequestSent: true}}}}
			case "invalid_headers":
				client.frames = []*pluginv1.ForwardResponse{{Frame: &pluginv1.ForwardResponse_Start{Start: &pluginv1.ForwardResponseStart{StatusCode: 99}}}}
			}
			upstream := &pluginRoutingHTTPUpstream{}
			gateway := &OpenAIGatewayService{pluginManager: pluginHTTPBoundaryManager(client), httpUpstream: upstream}
			request, err := http.NewRequest(http.MethodPost, chatgptCodexURL, strings.NewReader(`{"model":"gpt-test"}`))
			require.NoError(t, err)
			_, err = gateway.doOpenAIUpstream(request, "", pluginHTTPBoundaryAccount())
			require.Error(t, err)
			var transportErr *PluginTransportError
			require.ErrorAs(t, err, &transportErr)
			require.True(t, transportErr.RequestSent)
			require.EqualValues(t, 1, client.calls.Load())
			require.Zero(t, upstream.doCalls)
		})
	}
}
