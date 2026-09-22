package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/codexnative"
	pluginv1 "github.com/Wei-Shaw/sub2api/pkg/pluginapi/v1"
	"github.com/gin-gonic/gin"
	hcplugin "github.com/hashicorp/go-plugin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Exercise the real manager/runtime framing boundary, not a gateway transport
// stub. The stream retains only test credentials and synchronizes its request
// capture with response delivery, including the asynchronous body sender.
type pluginTurnStateMergeClient struct {
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

func (c *pluginTurnStateMergeClient) Forward(ctx context.Context, _ ...grpc.CallOption) (grpc.BidiStreamingClient[pluginv1.ForwardRequest, pluginv1.ForwardResponse], error) {
	c.calls.Add(1)
	return &pluginTurnStateMergeStream{client: c, ctx: ctx, sent: make(chan struct{})}, nil
}

type pluginTurnStateMergeStream struct {
	grpc.BidiStreamingClient[pluginv1.ForwardRequest, pluginv1.ForwardResponse]
	client    *pluginTurnStateMergeClient
	ctx       context.Context
	sent      chan struct{}
	closeOnce sync.Once
	next      int
}

func (s *pluginTurnStateMergeStream) Context() context.Context { return s.ctx }
func (s *pluginTurnStateMergeStream) Send(frame *pluginv1.ForwardRequest) error {
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
func (s *pluginTurnStateMergeStream) CloseSend() error {
	s.closeOnce.Do(func() { close(s.sent) })
	return nil
}
func (s *pluginTurnStateMergeStream) Recv() (*pluginv1.ForwardResponse, error) {
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

func pluginTurnStateMergeManager(client *pluginTurnStateMergeClient) *PluginManager {
	manager := &PluginManager{}
	manager.route.Store(&pluginRoute{pluginID: 71, rolloutPercent: 100, runtime: &pluginRuntime{client: hcplugin.NewClient(&hcplugin.ClientConfig{}), api: client}})
	return manager
}

func pluginTurnStateMergeFrames(t *testing.T, response *http.Response) []*pluginv1.ForwardResponse {
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

func TestPluginTurnStateMergeDeliveredHTTPResponsePublishes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, path := range []string{"responses", "passthrough", "chat", "messages"} {
		for _, stream := range []bool{false, true} {
			for _, carrier := range []string{"header", "metadata"} {
				name := path + "/" + carrier
				if stream {
					name += "/stream"
				} else {
					name += "/nonstream"
				}
				t.Run(name, func(t *testing.T) {
					state, repo, account := newCodexStateTestService(t)
					// This exercises the real physical cookie boundary, whose clock
					// validates the target deadline before producing a frozen bundle.
					now := time.Now().UTC().Truncate(time.Second)
					state.now = func() time.Time { return now }
					account.Concurrency = 1
					if path == "passthrough" {
						account.Extra["openai_passthrough"] = true
					}
					token := codexStateTestToken(10, state.now())
					client := &pluginTurnStateMergeClient{frames: pluginTurnStateMergeFrames(t, codexStateHTTPIntegrationResponse(token, carrier, false))}
					upstream := &pluginRoutingHTTPUpstream{}
					gateway := &OpenAIGatewayService{cfg: &config.Config{}, pluginManager: pluginTurnStateMergeManager(client), httpUpstream: upstream, codexTurnStateService: state}
					var collected atomic.Int64
					state.collector = codexStateTestCollector(func(context.Context, CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error) {
						collected.Add(1)
						return CodexTurnStateCollectResult{}, nil
					})
					result, recorder, err := codexStateHTTPIntegrationForward(t, gateway, account, path, codexStateHTTPIntegrationBody(path, stream))
					require.NoError(t, err, recorder.Body.String())
					require.NotNil(t, result)
					require.Equal(t, int64(1), client.calls.Load())
					require.Zero(t, upstream.doCalls, "plugin-selected business requests must not fall back to builtin HTTP")
					client.mu.Lock()
					finalModel := gjson.GetBytes(client.body, "model").String()
					client.mu.Unlock()
					require.Equal(t, "gpt-5.4", finalModel)
					key := CodexTurnStateKey{OwnerAccountID: account.ID, Model: finalModel, Generation: CodexTurnStateGenerationForAccount(account)}
					record, getErr := repo.Get(context.Background(), key)
					require.NoError(t, getErr)
					require.NotNil(t, record)
					plain, decryptErr := state.encryptor.Decrypt(record.EncryptedToken)
					require.NoError(t, decryptErr)
					require.Equal(t, token, plain)
					state.collect(context.Background(), key)
					require.Zero(t, collected.Load(), "plugin natural state suppresses standalone collection")
				})
			}
		}
	}
}

func TestPluginTurnStateMergeUncertainSendDoesNotPublishOrReplay(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, failure := range []string{"uncertain_send", "plugin_error", "invalid_headers", "first_output_header_timeout", "first_output_header_cancel"} {
		t.Run(failure, func(t *testing.T) {
			state, repo, account := newCodexStateTestService(t)
			client := &pluginTurnStateMergeClient{}
			switch failure {
			case "uncertain_send":
				client.sendErr = errors.New("RPC send failed after server may have received request")
			case "plugin_error":
				client.frames = []*pluginv1.ForwardResponse{{Frame: &pluginv1.ForwardResponse_Error{Error: &pluginv1.ForwardResponseError{Code: "TEST_SENT", Message: "sent then disconnected", RequestSent: true}}}}
			case "invalid_headers":
				client.frames = []*pluginv1.ForwardResponse{{Frame: &pluginv1.ForwardResponse_Start{Start: &pluginv1.ForwardResponseStart{StatusCode: 99}}}}
			case "first_output_header_timeout":
				// Reproduce the deadline race where the guard has fired while the
				// plugin concurrently reports that the physical request may have
				// been accepted. The no-replay signal must win.
				client.returnFrameAfterCancel = true
				client.frames = []*pluginv1.ForwardResponse{{Frame: &pluginv1.ForwardResponse_Error{Error: &pluginv1.ForwardResponseError{Code: "TEST_SENT_AT_DEADLINE", Message: "sent at response-header deadline", RequestSent: true}}}}
			case "first_output_header_cancel":
				// Match a real canceled gRPC Recv: no plugin error frame arrives,
				// and the runtime must retain its own physical-send knowledge.
				client.cancelRPC = true
			}
			upstream := &pluginRoutingHTTPUpstream{}
			gateway := &OpenAIGatewayService{cfg: &config.Config{}, pluginManager: pluginTurnStateMergeManager(client), httpUpstream: upstream, codexTurnStateService: state}
			stream := strings.HasPrefix(failure, "first_output_header_")
			if stream {
				gateway.cfg.Gateway.OpenAIFirstOutputTimeoutSeconds = 1
			}
			_, _, err := codexStateHTTPIntegrationForward(t, gateway, account, "responses", codexStateHTTPIntegrationBody("responses", stream))
			require.Error(t, err)
			var transportErr *PluginTransportError
			require.ErrorAs(t, err, &transportErr)
			require.True(t, transportErr.RequestSent)
			if failure == "first_output_header_cancel" {
				require.ErrorIs(t, err, context.Canceled)
				client.mu.Lock()
				requestStart := client.start
				client.mu.Unlock()
				require.NotNil(t, requestStart, "the plugin received request metadata before cancellation")
			}
			var failoverErr *UpstreamFailoverError
			require.False(t, errors.As(err, &failoverErr), "an uncertain physical send cannot be replayed on another account")
			require.Equal(t, int64(1), client.calls.Load())
			require.Zero(t, upstream.doCalls)
			key := CodexTurnStateKey{OwnerAccountID: account.ID, Model: "gpt-5.4", Generation: CodexTurnStateGenerationForAccount(account)}
			record, getErr := repo.Get(context.Background(), key)
			require.NoError(t, getErr)
			require.NotNil(t, record)
			require.Empty(t, record.EncryptedToken)
			active, leaseErr := repo.HasBusiness(context.Background(), key, state.now())
			require.NoError(t, leaseErr)
			require.False(t, active, "failed plugin calls must release the natural-request reservation")
		})
	}
}

func TestPluginTurnStateMergeRejectedAndAbandonedResponseDoesNotPublish(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, failure := range []string{"failed_response", "metadata_then_transport_error", "closed_before_parsing"} {
		t.Run(failure, func(t *testing.T) {
			state, repo, account := newCodexStateTestService(t)
			token := codexStateTestToken(10, state.now())
			response := codexStateHTTPIntegrationResponse(token, "metadata", failure == "failed_response")
			if failure == "metadata_then_transport_error" {
				response.Body = io.NopCloser(strings.NewReader("data: {\"type\":\"response.metadata\",\"headers\":{\"x-codex-turn-state\":\"" + token + "\"}}\n\n"))
			}
			response.Header.Set(openAICodexTurnStateHeader, token)
			frames := pluginTurnStateMergeFrames(t, response)
			if failure == "metadata_then_transport_error" {
				frames[len(frames)-1] = &pluginv1.ForwardResponse{Frame: &pluginv1.ForwardResponse_Error{Error: &pluginv1.ForwardResponseError{Code: "TEST_BODY_ERROR", Message: "stream abandoned", RequestSent: true}}}
			}
			client := &pluginTurnStateMergeClient{frames: frames}
			upstream := &pluginRoutingHTTPUpstream{}
			gateway := &OpenAIGatewayService{cfg: &config.Config{}, pluginManager: pluginTurnStateMergeManager(client), httpUpstream: upstream, codexTurnStateService: state}
			if failure == "closed_before_parsing" {
				request, err := http.NewRequest(http.MethodPost, chatgptCodexURL, bytes.NewReader(codexStateHTTPIntegrationBody("responses", false)))
				require.NoError(t, err)
				request.Header.Set("Authorization", "Bearer "+account.GetCredential("access_token"))
				request = gateway.prepareOpenAICodexStateHTTPRequest(nil, account, request)
				response, err := gateway.doOpenAIUpstream(request, "", account)
				require.NoError(t, err)
				require.NoError(t, response.Body.Close())
			} else {
				_, _, err := codexStateHTTPIntegrationForward(t, gateway, account, "responses", codexStateHTTPIntegrationBody("responses", false))
				require.Error(t, err)
			}
			key := CodexTurnStateKey{OwnerAccountID: account.ID, Model: "gpt-5.4", Generation: CodexTurnStateGenerationForAccount(account)}
			record, getErr := repo.Get(context.Background(), key)
			require.NoError(t, getErr)
			require.NotNil(t, record)
			require.Empty(t, record.EncryptedToken)
			require.Zero(t, upstream.doCalls)
			active, leaseErr := repo.HasBusiness(context.Background(), key, state.now())
			require.NoError(t, leaseErr)
			require.False(t, active)
		})
	}
}

func TestPluginTurnStateMergeCollectorStaysOnBuiltinExplicitProxy(t *testing.T) {
	account, proxy := codexCollectorTransportFixture()
	client := &pluginTurnStateMergeClient{sendErr: errors.New("collector must not invoke business plugin")}
	manager := pluginTurnStateMergeManager(client)
	require.True(t, manager.ShouldRouteOpenAIOAuth(account), "the same account is actively selected by the business plugin")
	token := codexStateTestToken(10, time.Now())
	upstream := &httpUpstreamRecorder{resp: codexStateHTTPIntegrationResponse(token, "header", false)}
	accounts := codexCollectorTransportAccounts{account: account}
	gateway := &OpenAIGatewayService{pluginManager: manager, httpUpstream: upstream, accountRepo: accounts}
	do := ProvideCodexTurnStateCollectorHTTPDo(gateway.accountRepo, codexCollectorTransportProxies{proxy: proxy}, gateway.httpUpstream)
	collector := NewCodexTurnStateHTTPCollector(do)
	result, err := collector.Collect(context.Background(), CodexTurnStateCollectRequest{Account: account, Model: "gpt-5.4", ProxyID: proxy.ID, validateModelPolicy: allowCodexCollectorTestModelPolicy})
	require.NoError(t, err)
	require.Contains(t, result.Tokens, token)
	require.Zero(t, client.calls.Load(), "collector bypasses all business plugins even for an actively bound account")
	require.Len(t, upstream.requests, 1)
	request := upstream.requests[0]
	scope, ok := codexnative.ScopeFromContext(request.Context())
	require.True(t, ok)
	require.Equal(t, "turn_state_collector", scope.Purpose)
	require.Equal(t, HTTPUpstreamProfileCodexAuxiliary, HTTPUpstreamProfileFromContext(request.Context()))
	require.True(t, HTTPUpstreamRedirectsDisabled(request.Context()))
	require.Empty(t, request.Header.Get(openAICodexTurnStateHeader))
	require.False(t, gjson.GetBytes(upstream.lastBody, "previous_response_id").Exists())
}
