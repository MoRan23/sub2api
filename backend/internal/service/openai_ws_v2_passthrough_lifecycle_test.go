package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type stagedPassthroughFrame struct {
	messageType coderws.MessageType
	payload     []byte
}

type stagedPassthroughConn struct {
	frames    chan stagedPassthroughFrame
	writes    chan []byte
	closed    chan struct{}
	closeOnce sync.Once
}

func newStagedPassthroughConn() *stagedPassthroughConn {
	return &stagedPassthroughConn{
		frames: make(chan stagedPassthroughFrame, 4),
		writes: make(chan []byte, 4),
		closed: make(chan struct{}),
	}
}

func (c *stagedPassthroughConn) Send(payload string) {
	c.frames <- stagedPassthroughFrame{messageType: coderws.MessageText, payload: []byte(payload)}
}

func (c *stagedPassthroughConn) WriteJSON(context.Context, any) error { return nil }

func (c *stagedPassthroughConn) ReadMessage(ctx context.Context) ([]byte, error) {
	_, payload, err := c.ReadFrame(ctx)
	return payload, err
}

func (c *stagedPassthroughConn) Ping(context.Context) error { return nil }

func (c *stagedPassthroughConn) ReadFrame(ctx context.Context) (coderws.MessageType, []byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return coderws.MessageText, nil, ctx.Err()
	case <-c.closed:
		return coderws.MessageText, nil, errOpenAIWSConnClosed
	case frame := <-c.frames:
		return frame.messageType, append([]byte(nil), frame.payload...), nil
	}
}

func (c *stagedPassthroughConn) WriteFrame(ctx context.Context, _ coderws.MessageType, payload []byte) error {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.closed:
		return errOpenAIWSConnClosed
	default:
	}
	var parsed any
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return err
	}
	select {
	case c.writes <- append([]byte(nil), payload...):
	case <-ctx.Done():
		return ctx.Err()
	case <-c.closed:
		return errOpenAIWSConnClosed
	}
	return nil
}

func (c *stagedPassthroughConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return nil
}

type stagedPassthroughDialer struct {
	conn    openAIWSClientConn
	headers http.Header
}

type passthroughTurnStateGatewayCache struct {
	*stubGatewayCache
	mu      sync.Mutex
	origins map[string]OpenAICodexTurnStateOrigin
}

func (c *passthroughTurnStateGatewayCache) GetOpenAICodexTurnStateOrigin(_ context.Context, key string) (OpenAICodexTurnStateOrigin, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	origin, ok := c.origins[key]
	if !ok {
		return OpenAICodexTurnStateOrigin{}, ErrOpenAICodexTurnStateOriginNotFound
	}
	return origin, nil
}

func (c *passthroughTurnStateGatewayCache) SetOpenAICodexTurnStateOrigin(_ context.Context, key string, origin OpenAICodexTurnStateOrigin, _ time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.origins == nil {
		c.origins = make(map[string]OpenAICodexTurnStateOrigin)
	}
	c.origins[key] = origin
	return nil
}

func (c *passthroughTurnStateGatewayCache) DeleteOpenAICodexTurnStateOrigin(_ context.Context, key string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.origins, key)
	return nil
}

func (d *stagedPassthroughDialer) Dial(context.Context, string, http.Header, string) (openAIWSClientConn, int, http.Header, error) {
	return d.conn, http.StatusSwitchingProtocols, cloneHeader(d.headers), nil
}

func newPassthroughLifecycleService(cfg *config.Config, upstream *stagedPassthroughConn) *OpenAIGatewayService {
	return &OpenAIGatewayService{
		cfg:                       cfg,
		httpUpstream:              &httpUpstreamRecorder{},
		cache:                     &stubGatewayCache{},
		openaiWSResolver:          NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:             NewCodexToolCorrector(),
		openaiWSPassthroughDialer: &stagedPassthroughDialer{conn: upstream},
	}
}

func passthroughLifecycleConfig() *config.Config {
	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIFirstOutputTimeoutSeconds = 1
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
	cfg.Gateway.OpenAIWS.IngressModeDefault = OpenAIWSIngressModeCtxPool
	cfg.Gateway.OpenAIWS.IngressInterTurnIdleTimeoutSeconds = 1
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 1
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3
	return cfg
}

func passthroughLifecycleAccount() *Account {
	return &Account{
		ID:          901,
		Name:        "passthrough-lifecycle",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{"api_key": "sk-test"},
		Extra: map[string]any{
			"openai_apikey_responses_websockets_v2_mode": OpenAIWSIngressModePassthrough,
		},
	}
}

func startPassthroughLifecycleServer(
	t *testing.T,
	controlCtx context.Context,
	svc *OpenAIGatewayService,
	account *Account,
) (*httptest.Server, <-chan error) {
	t.Helper()
	serverErr := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{CompressionMode: coderws.CompressionContextTakeover})
		if err != nil {
			serverErr <- err
			return
		}
		defer func() { _ = conn.CloseNow() }()

		msgType, firstMessage, err := ReadOpenAIWSClientMessage(
			controlCtx,
			conn,
			3*time.Second,
			coderws.StatusPolicyViolation,
			"missing first response.create message",
		)
		if err != nil {
			serverErr <- err
			return
		}
		if msgType != coderws.MessageText {
			serverErr <- errors.New("first message was not text")
			return
		}

		recorder := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(recorder)
		req := r.Clone(controlCtx)
		req.Header = req.Header.Clone()
		ginCtx.Request = req
		serverErr <- svc.ProxyResponsesWebSocketFromClient(controlCtx, ginCtx, conn, account, "sk-test", firstMessage, nil)
	}))
	return server, serverErr
}

func dialPassthroughLifecycleClient(t *testing.T, server *httptest.Server) *coderws.Conn {
	t.Helper()
	return dialPassthroughLifecycleClientWithPayload(t, server, `{"type":"response.create","model":"gpt-5.1","stream":false}`)
}

func dialPassthroughLifecycleClientWithPayload(t *testing.T, server *httptest.Server, payload string) *coderws.Conn {
	t.Helper()
	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
	err = clientConn.Write(writeCtx, coderws.MessageText, []byte(payload))
	cancelWrite()
	require.NoError(t, err)
	return clientConn
}

func readPassthroughLifecycleFrame(t *testing.T, clientConn *coderws.Conn, timeout time.Duration) ([]byte, error) {
	t.Helper()
	readCtx, cancelRead := context.WithTimeout(context.Background(), timeout)
	_, payload, err := clientConn.Read(readCtx)
	cancelRead()
	return payload, err
}

func requirePassthroughUpstreamWrite(t *testing.T, upstream *stagedPassthroughConn, timeout time.Duration) []byte {
	t.Helper()
	select {
	case payload := <-upstream.writes:
		return payload
	case <-time.After(timeout):
		t.Fatal("passthrough request was not forwarded upstream")
		return nil
	}
}

func TestPassthroughLifecycle_ResponsesLiteFirstFramePinsParallelToolCalls(t *testing.T) {
	gin.SetMode(gin.TestMode)
	controlCtx, cancelControl := context.WithCancelCause(context.Background())
	defer cancelControl(context.Canceled)
	upstream := newStagedPassthroughConn()
	upstream.Send(`{"type":"response.completed","response":{"id":"resp_lite","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`)
	server, serverErr := startPassthroughLifecycleServer(t, controlCtx, newPassthroughLifecycleService(passthroughLifecycleConfig(), upstream), passthroughLifecycleAccount())
	defer server.Close()
	clientConn := dialPassthroughLifecycleClientWithPayload(t, server, `{
		"type":"response.create","model":"gpt-5.1","stream":false,
		"parallel_tool_calls":true,
		"client_metadata":{"ws_request_header_x_openai_internal_codex_responses_lite":"true"}
	}`)
	defer func() { _ = clientConn.CloseNow() }()

	upstreamBody := requirePassthroughUpstreamWrite(t, upstream, 3*time.Second)
	require.Equal(t, gjson.False, gjson.GetBytes(upstreamBody, "parallel_tool_calls").Type, string(upstreamBody))

	event, err := readPassthroughLifecycleFrame(t, clientConn, 3*time.Second)
	require.NoError(t, err)
	require.Equal(t, "response.completed", gjson.GetBytes(event, "type").String())
	require.NoError(t, clientConn.Close(coderws.StatusNormalClosure, "done"))
	select {
	case <-serverErr:
	case <-time.After(3 * time.Second):
		t.Fatal("Lite 首帧测试等待 passthrough 退出超时")
	}
}

func TestOpenAIWSPassthroughTurnLifecycle_SerializesTerminalCommitAndNextTurn(t *testing.T) {
	clientFrameConn := &openAIWSClientFrameConn{interTurnStarted: make(chan struct{}, 1)}
	clientFrameConn.markTurnCompleted()
	lifecycle := newOpenAIWSPassthroughTurnLifecycle(true)
	lifecycle.beginTerminalWrite()

	admitted := make(chan bool, 1)
	go func() {
		admitted <- lifecycle.beginResponseCreate(clientFrameConn.markTurnStarted)
	}()
	select {
	case <-admitted:
		t.Fatal("next response.create was admitted before terminal commit completed")
	case <-time.After(50 * time.Millisecond):
	}

	lifecycle.finishTerminalWrite(true, clientFrameConn.markTurnCompleted)
	select {
	case ok := <-admitted:
		require.True(t, ok)
	case <-time.After(time.Second):
		t.Fatal("next response.create remained blocked after terminal commit")
	}
	require.False(t, clientFrameConn.waitingForNextTurn.Load(), "accepted next turn must win over terminal idle state")

	lifecycle = newOpenAIWSPassthroughTurnLifecycle(true)
	lifecycle.beginTerminalWrite()
	admitted = make(chan bool, 1)
	go func() {
		admitted <- lifecycle.beginResponseCreate(nil)
	}()
	lifecycle.finishTerminalWrite(false, func() {
		t.Error("failed terminal write must not commit idle state")
	})
	require.False(t, <-admitted, "failed terminal write must keep the current turn in flight")
}

func TestPassthroughLifecycle_TurnStateCommitsOnlyAfterFirstDeliveredOutput(t *testing.T) {
	const (
		sessionID = "direct-turn-state-session"
		oldState  = "direct-turn-state-old"
		newState  = "direct-turn-state-new"
	)

	newHarness := func(t *testing.T, handshakeState string) (*OpenAIGatewayService, *stagedPassthroughConn, *httptest.Server, <-chan error, string, *passthroughTurnStateGatewayCache) {
		t.Helper()
		upstream := newStagedPassthroughConn()
		cfg := passthroughLifecycleConfig()
		cfg.JWT.Secret = "direct-turn-state-secret"
		cfg.Gateway.OpenAIWS.OAuthEnabled = true
		svc := newPassthroughLifecycleService(cfg, upstream)
		cache := &passthroughTurnStateGatewayCache{stubGatewayCache: &stubGatewayCache{}}
		svc.cache = cache
		dialer := svc.openaiWSPassthroughDialer.(*stagedPassthroughDialer)
		if handshakeState != "" {
			dialer.headers = make(http.Header)
			dialer.headers.Set(openAICodexTurnStateHeader, handshakeState)
		}
		account := &Account{
			ID: 902, Name: "passthrough-turn-state", Platform: PlatformOpenAI,
			Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1,
			Credentials: map[string]any{"access_token": "oauth-token"},
			Extra: map[string]any{
				"openai_oauth_responses_websockets_v2_mode": OpenAIWSIngressModePassthrough,
				openAIPinnedInstallationIDKey:               transportTestPinnedInstallationID,
			},
		}
		server, serverErr := startPassthroughLifecycleServer(t, context.Background(), svc, account)
		sessionHash, _ := deriveOpenAISessionHashes(sessionID)
		svc.getOpenAIWSStateStore().BindSessionTurnState(0, sessionHash, oldState, time.Minute)
		return svc, upstream, server, serverErr, sessionHash, cache
	}

	dialAndWrite := func(t *testing.T, server *httptest.Server) *coderws.Conn {
		t.Helper()
		dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
		client, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(server.URL, "http"), &coderws.DialOptions{
			HTTPHeader: http.Header{"session_id": []string{sessionID}},
		})
		cancelDial()
		require.NoError(t, err)
		writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
		require.NoError(t, client.Write(writeCtx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","stream":false,"client_metadata":{"session_id":"direct-turn-state-session","thread_id":"direct-turn-state-session"}}`)))
		cancelWrite()
		return client
	}

	t.Run("successful first output binds raw state and provenance", func(t *testing.T) {
		svc, upstream, server, serverErr, sessionHash, provenanceStore := newHarness(t, newState)
		defer server.Close()
		client := dialAndWrite(t, server)
		defer func() { _ = client.CloseNow() }()
		require.NotEmpty(t, requirePassthroughUpstreamWrite(t, upstream, 3*time.Second))
		state, ok := svc.getOpenAIWSStateStore().GetSessionTurnState(0, sessionHash)
		require.True(t, ok)
		require.Equal(t, oldState, state, "handshake state must not bind before downstream delivery")

		upstream.Send(`{"type":"response.completed","response":{"id":"resp_state_bound","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`)
		payload, readErr := readPassthroughLifecycleFrame(t, client, 3*time.Second)
		require.NoError(t, readErr)
		require.Equal(t, "resp_state_bound", gjson.GetBytes(payload, "response.id").String())
		require.Eventually(t, func() bool {
			state, ok = svc.getOpenAIWSStateStore().GetSessionTurnState(0, sessionHash)
			return ok && state == newState
		}, time.Second, 10*time.Millisecond)
		key, keyErr := OpenAICodexTurnStateProvenanceKey(svc.cfg.JWT.Secret, 0, newState)
		require.NoError(t, keyErr)
		require.Eventually(t, func() bool {
			_, provenanceErr := provenanceStore.GetOpenAICodexTurnStateOrigin(context.Background(), key)
			return provenanceErr == nil
		}, time.Second, 10*time.Millisecond)
		require.NoError(t, client.Close(coderws.StatusNormalClosure, "done"))
		select {
		case <-serverErr:
		case <-time.After(3 * time.Second):
			t.Fatal("successful direct turn-state session did not exit")
		}
	})

	t.Run("no downstream output retains old state", func(t *testing.T) {
		svc, upstream, server, serverErr, sessionHash, provenanceStore := newHarness(t, newState)
		defer server.Close()
		client := dialAndWrite(t, server)
		require.NotEmpty(t, requirePassthroughUpstreamWrite(t, upstream, 3*time.Second))
		require.NoError(t, client.CloseNow())
		select {
		case <-serverErr:
		case <-time.After(3 * time.Second):
			t.Fatal("failed direct turn-state session did not exit")
		}
		state, ok := svc.getOpenAIWSStateStore().GetSessionTurnState(0, sessionHash)
		require.True(t, ok)
		require.Equal(t, oldState, state)
		key, keyErr := OpenAICodexTurnStateProvenanceKey(svc.cfg.JWT.Secret, 0, newState)
		require.NoError(t, keyErr)
		_, provenanceErr := provenanceStore.GetOpenAICodexTurnStateOrigin(context.Background(), key)
		require.ErrorIs(t, provenanceErr, ErrOpenAICodexTurnStateOriginNotFound)
	})

	t.Run("failed terminal output retains old state", func(t *testing.T) {
		svc, upstream, server, serverErr, sessionHash, provenanceStore := newHarness(t, newState)
		defer server.Close()
		client := dialAndWrite(t, server)
		defer func() { _ = client.CloseNow() }()
		require.NotEmpty(t, requirePassthroughUpstreamWrite(t, upstream, 3*time.Second))

		upstream.Send(`{"type":"response.failed","response":{"id":"resp_state_failed","model":"gpt-5.1","error":{"message":"failed"}}}`)
		payload, readErr := readPassthroughLifecycleFrame(t, client, 3*time.Second)
		require.NoError(t, readErr)
		require.Equal(t, "response.failed", gjson.GetBytes(payload, "type").String())
		state, ok := svc.getOpenAIWSStateStore().GetSessionTurnState(0, sessionHash)
		require.True(t, ok)
		require.Equal(t, oldState, state)
		key, keyErr := OpenAICodexTurnStateProvenanceKey(svc.cfg.JWT.Secret, 0, newState)
		require.NoError(t, keyErr)
		_, provenanceErr := provenanceStore.GetOpenAICodexTurnStateOrigin(context.Background(), key)
		require.ErrorIs(t, provenanceErr, ErrOpenAICodexTurnStateOriginNotFound)

		require.NoError(t, client.Close(coderws.StatusNormalClosure, "done"))
		select {
		case <-serverErr:
		case <-time.After(3 * time.Second):
			t.Fatal("failed-output direct turn-state session did not exit")
		}
	})

	t.Run("successful output with empty handshake clears old state", func(t *testing.T) {
		svc, upstream, server, serverErr, sessionHash, _ := newHarness(t, "")
		defer server.Close()
		client := dialAndWrite(t, server)
		defer func() { _ = client.CloseNow() }()
		require.NotEmpty(t, requirePassthroughUpstreamWrite(t, upstream, 3*time.Second))
		upstream.Send(`{"type":"response.completed","response":{"id":"resp_state_cleared","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`)
		_, readErr := readPassthroughLifecycleFrame(t, client, 3*time.Second)
		require.NoError(t, readErr)
		require.Eventually(t, func() bool {
			_, ok := svc.getOpenAIWSStateStore().GetSessionTurnState(0, sessionHash)
			return !ok
		}, time.Second, 10*time.Millisecond)
		require.NoError(t, client.Close(coderws.StatusNormalClosure, "done"))
		select {
		case <-serverErr:
		case <-time.After(3 * time.Second):
			t.Fatal("empty-handshake direct turn-state session did not exit")
		}
	})
}

func TestOpenAIWSPassthroughOutputCommitsTurnState(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload string
		want    bool
	}{
		{name: "text delta", payload: `{"type":"response.output_text.delta","delta":"ok"}`, want: true},
		{name: "completed", payload: `{"type":"response.completed"}`, want: true},
		{name: "done", payload: `{"type":"response.done"}`, want: true},
		{name: "protocol error", payload: `{"type":"error","error":{"message":"failed"}}`},
		{name: "failed", payload: `{"type":"response.failed"}`},
		{name: "incomplete", payload: `{"type":"response.incomplete"}`},
		{name: "cancelled", payload: `{"type":"response.cancelled"}`},
		{name: "canceled", payload: `{"type":"response.canceled"}`},
		{name: "created only", payload: `{"type":"response.created"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, openAIWSPassthroughOutputCommitsTurnState([]byte(tc.payload)))
		})
	}
}

func TestPassthroughLifecycle_UUIDv7PromptCacheChangeKeepsPinnedTuple(t *testing.T) {
	gin.SetMode(gin.TestMode)
	controlCtx := context.Background()
	upstream := newStagedPassthroughConn()
	cfg := passthroughLifecycleConfig()
	cfg.JWT.Secret = "ws-passthrough-identity-test-secret"
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	svc := newPassthroughLifecycleService(cfg, upstream)
	svc.settingService = NewSettingService(&openAIUUIDv7RuntimeRepo{values: map[string]string{
		SettingKeyEnableOpenAIUUIDv7SessionIdentity: "true",
	}}, nil)
	account := &Account{
		ID:          810012,
		Name:        "passthrough-uuidv7-frame-key",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{"access_token": "oauth-token"},
		Extra: map[string]any{
			"openai_oauth_responses_websockets_v2_mode": OpenAIWSIngressModePassthrough,
			openAIPinnedInstallationIDKey:               transportTestPinnedInstallationID,
		},
	}
	server, serverErr := startPassthroughLifecycleServer(t, controlCtx, svc, account)
	defer server.Close()

	dialHeaders := http.Header{"session_id": []string{"handshake-H"}}
	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(
		dialCtx,
		"ws"+strings.TrimPrefix(server.URL, "http"),
		&coderws.DialOptions{HTTPHeader: dialHeaders},
	)
	cancelDial()
	require.NoError(t, err)
	defer func() { _ = clientConn.CloseNow() }()

	writeClient := func(payload string) {
		writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
		require.NoError(t, clientConn.Write(writeCtx, coderws.MessageText, []byte(payload)))
		cancelWrite()
	}
	readCompleted := func(wantID string) {
		payload, readErr := readPassthroughLifecycleFrame(t, clientConn, 3*time.Second)
		require.NoError(t, readErr)
		require.Equal(t, wantID, gjson.GetBytes(payload, "response.id").String())
	}

	writeClient(`{"type":"response.create","model":"gpt-5.1","stream":false,"prompt_cache_key":"P"}`)
	firstForwarded := requirePassthroughUpstreamWrite(t, upstream, 3*time.Second)
	firstMappedPromptCacheKey := gjson.GetBytes(firstForwarded, "prompt_cache_key").String()
	require.True(t, strings.HasPrefix(firstMappedPromptCacheKey, "pc_"))
	require.NotEqual(t, "P", firstMappedPromptCacheKey)
	firstPhysicalStamp := gjson.GetBytes(firstForwarded, "client_metadata."+openAICodexWSStreamRequestStartMSKey)
	require.True(t, firstPhysicalStamp.Exists())
	require.Equal(t, gjson.String, firstPhysicalStamp.Type)
	upstream.Send(`{"type":"response.completed","response":{"id":"resp_passthrough_identity_p_1","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`)
	readCompleted("resp_passthrough_identity_p_1")

	writeClient(`{"type":"response.create","model":"gpt-5.1","stream":false,"prompt_cache_key":"P"}`)
	repeatedForwarded := requirePassthroughUpstreamWrite(t, upstream, 3*time.Second)
	require.Equal(t, firstMappedPromptCacheKey, gjson.GetBytes(repeatedForwarded, "prompt_cache_key").String())
	repeatedPhysicalStamp := gjson.GetBytes(repeatedForwarded, "client_metadata."+openAICodexWSStreamRequestStartMSKey)
	require.True(t, repeatedPhysicalStamp.Exists())
	require.Equal(t, gjson.String, repeatedPhysicalStamp.Type)
	upstream.Send(`{"type":"response.completed","response":{"id":"resp_passthrough_identity_p_2","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`)
	readCompleted("resp_passthrough_identity_p_2")
	require.Equal(
		t,
		gjson.GetBytes(firstForwarded, "client_metadata.session_id").String(),
		gjson.GetBytes(repeatedForwarded, "client_metadata.session_id").String(),
		"repeating P after a header-pinned first frame must retain the H identity",
	)

	writeClient(`{"type":"response.create","model":"gpt-5.1","stream":false,"prompt_cache_key":"Q"}`)
	cacheChangedForwarded := requirePassthroughUpstreamWrite(t, upstream, 3*time.Second)
	cacheChangedPromptKey := gjson.GetBytes(cacheChangedForwarded, "prompt_cache_key").String()
	require.True(t, strings.HasPrefix(cacheChangedPromptKey, "pc_"))
	require.NotEqual(t, firstMappedPromptCacheKey, cacheChangedPromptKey)
	cacheChangedPhysicalStamp := gjson.GetBytes(cacheChangedForwarded, "client_metadata."+openAICodexWSStreamRequestStartMSKey)
	require.True(t, cacheChangedPhysicalStamp.Exists())
	require.Equal(t, gjson.String, cacheChangedPhysicalStamp.Type)
	upstream.Send(`{"type":"response.completed","response":{"id":"resp_passthrough_identity_q_1","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`)
	readCompleted("resp_passthrough_identity_q_1")
	firstSessionID := gjson.GetBytes(firstForwarded, "client_metadata.session_id").String()
	firstThreadID := gjson.GetBytes(firstForwarded, "client_metadata.thread_id").String()
	require.NotEmpty(t, firstSessionID)
	require.NotEmpty(t, firstThreadID)
	require.Equal(t, firstSessionID, gjson.GetBytes(cacheChangedForwarded, "client_metadata.session_id").String())
	require.Equal(t, firstThreadID, gjson.GetBytes(cacheChangedForwarded, "client_metadata.thread_id").String())

	require.NoError(t, clientConn.Close(coderws.StatusNormalClosure, "done"))
	select {
	case err := <-serverErr:
		var closeErr *OpenAIWSClientCloseError
		if err != nil {
			require.ErrorAs(t, err, &closeErr)
			require.Equal(t, coderws.StatusNormalClosure, closeErr.StatusCode())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("passthrough UUIDv7 prompt-cache test did not exit")
	}
}

func TestPassthroughLifecycle_FirstFramePinsInstallationWhenTurnIdentityDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	controlCtx := context.Background()
	upstream := newStagedPassthroughConn()
	cfg := passthroughLifecycleConfig()
	cfg.JWT.Secret = "ws-passthrough-installation-only-test-secret"
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	svc := newPassthroughLifecycleService(cfg, upstream)
	svc.settingService = NewSettingService(&openAIUUIDv7RuntimeRepo{values: map[string]string{
		SettingKeyEnableOpenAIUUIDv7SessionIdentity: "false",
	}}, nil)
	account := &Account{
		ID:          810014,
		Name:        "passthrough-installation-only",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{"access_token": "oauth-token"},
		Extra: map[string]any{
			"openai_oauth_responses_websockets_v2_mode": OpenAIWSIngressModePassthrough,
			openAIPinnedInstallationIDKey:               transportTestPinnedInstallationID,
		},
	}
	server, serverErr := startPassthroughLifecycleServer(t, controlCtx, svc, account)
	defer server.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() { _ = clientConn.CloseNow() }()

	firstPayload := `{"type":"response.create","model":"gpt-5.1","stream":false,"client_metadata":{"x-codex-installation-id":"client-flat","x-codex-turn-metadata":"{\"installation_id\":\"client-nested\",\"label\":\"nested-keep\"}"},"x-codex-turn-metadata":"{\"installation_id\":\"client-top\",\"label\":\"top-keep\"}"}`
	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
	require.NoError(t, clientConn.Write(writeCtx, coderws.MessageText, []byte(firstPayload)))
	cancelWrite()

	forwarded := requirePassthroughUpstreamWrite(t, upstream, 3*time.Second)
	require.Equal(t, transportTestPinnedInstallationID, gjson.GetBytes(forwarded, "client_metadata.x-codex-installation-id").String())
	nested := gjson.GetBytes(forwarded, "client_metadata.x-codex-turn-metadata").String()
	require.Equal(t, transportTestPinnedInstallationID, gjson.Get(nested, "installation_id").String())
	require.Equal(t, "nested-keep", gjson.Get(nested, "label").String())
	topLevel := gjson.GetBytes(forwarded, openAIWSTurnMetadataHeader).String()
	require.Equal(t, transportTestPinnedInstallationID, gjson.Get(topLevel, "installation_id").String())
	require.Equal(t, "top-keep", gjson.Get(topLevel, "label").String())
	require.False(t, gjson.GetBytes(forwarded, "client_metadata.session_id").Exists())
	require.False(t, gjson.GetBytes(forwarded, "client_metadata.thread_id").Exists())

	upstream.Send(`{"type":"response.completed","response":{"id":"resp_passthrough_installation_only","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`)
	completed, readErr := readPassthroughLifecycleFrame(t, clientConn, 3*time.Second)
	require.NoError(t, readErr)
	require.Equal(t, "resp_passthrough_installation_only", gjson.GetBytes(completed, "response.id").String())
	require.NoError(t, clientConn.Close(coderws.StatusNormalClosure, "done"))
	select {
	case proxyErr := <-serverErr:
		var closeErr *OpenAIWSClientCloseError
		if proxyErr != nil {
			require.ErrorAs(t, proxyErr, &closeErr)
			require.Equal(t, coderws.StatusNormalClosure, closeErr.StatusCode())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("passthrough installation-only test did not exit")
	}
}

func TestPassthroughLifecycle_UUIDv7LatePromptCacheInheritsUntilExplicitSessionChange(t *testing.T) {
	gin.SetMode(gin.TestMode)
	controlCtx := context.Background()
	upstream := newStagedPassthroughConn()
	cfg := passthroughLifecycleConfig()
	cfg.JWT.Secret = "ws-passthrough-late-identity-test-secret"
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	svc := newPassthroughLifecycleService(cfg, upstream)
	svc.settingService = NewSettingService(&openAIUUIDv7RuntimeRepo{values: map[string]string{
		SettingKeyEnableOpenAIUUIDv7SessionIdentity: "true",
	}}, nil)
	account := &Account{
		ID:          810013,
		Name:        "passthrough-uuidv7-late-prompt",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{"access_token": "oauth-token"},
		Extra: map[string]any{
			"openai_oauth_responses_websockets_v2_mode": OpenAIWSIngressModePassthrough,
			openAIPinnedInstallationIDKey:               transportTestPinnedInstallationID,
		},
	}
	server, serverErr := startPassthroughLifecycleServer(t, controlCtx, svc, account)
	defer server.Close()

	clientConn := dialPassthroughLifecycleClient(t, server)
	defer func() { _ = clientConn.CloseNow() }()
	firstForwarded := requirePassthroughUpstreamWrite(t, upstream, 3*time.Second)
	firstSessionID := gjson.GetBytes(firstForwarded, "client_metadata.session_id").String()
	firstThreadID := gjson.GetBytes(firstForwarded, "client_metadata.thread_id").String()
	require.NotEmpty(t, firstSessionID)
	require.NotEmpty(t, firstThreadID)
	upstream.Send(`{"type":"response.completed","response":{"id":"resp_passthrough_identityless_1","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`)
	firstCompleted, readErr := readPassthroughLifecycleFrame(t, clientConn, 3*time.Second)
	require.NoError(t, readErr)
	require.Equal(t, "resp_passthrough_identityless_1", gjson.GetBytes(firstCompleted, "response.id").String())

	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
	require.NoError(t, clientConn.Write(writeCtx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","stream":false,"prompt_cache_key":"late-P"}`)))
	cancelWrite()
	lateForwarded := requirePassthroughUpstreamWrite(t, upstream, 3*time.Second)
	require.Equal(t, firstSessionID, gjson.GetBytes(lateForwarded, "client_metadata.session_id").String())
	require.Equal(t, firstThreadID, gjson.GetBytes(lateForwarded, "client_metadata.thread_id").String())
	upstream.Send(`{"type":"response.completed","response":{"id":"resp_passthrough_late_prompt","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`)
	lateCompleted, readErr := readPassthroughLifecycleFrame(t, clientConn, 3*time.Second)
	require.NoError(t, readErr)
	require.Equal(t, "resp_passthrough_late_prompt", gjson.GetBytes(lateCompleted, "response.id").String())

	writeCtx, cancelWrite = context.WithTimeout(context.Background(), 3*time.Second)
	require.NoError(t, clientConn.Write(writeCtx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","stream":false,"client_metadata":{"session_id":"explicit-root-2","thread_id":"explicit-root-2"}}`)))
	cancelWrite()
	_, readErr = readPassthroughLifecycleFrame(t, clientConn, 3*time.Second)
	var closeErr coderws.CloseError
	require.ErrorAs(t, readErr, &closeErr)
	require.Equal(t, coderws.StatusPolicyViolation, closeErr.Code)
	require.Equal(t, "websocket outbound session logical key changed on passthrough connection", closeErr.Reason)
	select {
	case payload := <-upstream.writes:
		t.Fatalf("explicit session transition reached the old passthrough socket: %s", payload)
	case <-time.After(100 * time.Millisecond):
	}
	select {
	case err := <-serverErr:
		var clientCloseErr *OpenAIWSClientCloseError
		require.ErrorAs(t, err, &clientCloseErr)
		require.Equal(t, coderws.StatusPolicyViolation, clientCloseErr.StatusCode())
	case <-time.After(3 * time.Second):
		t.Fatal("passthrough late identity test did not exit")
	}
}

func TestPassthroughLifecycle_LeaseLossSendsRetryClose(t *testing.T) {
	gin.SetMode(gin.TestMode)
	controlCtx, cancelControl := context.WithCancelCause(context.Background())
	upstream := newStagedPassthroughConn()
	upstream.Send(`{"type":"response.created","response":{"id":"resp_lease","model":"gpt-5.1"}}`)
	server, serverErr := startPassthroughLifecycleServer(t, controlCtx, newPassthroughLifecycleService(passthroughLifecycleConfig(), upstream), passthroughLifecycleAccount())
	defer server.Close()
	clientConn := dialPassthroughLifecycleClient(t, server)
	defer func() { _ = clientConn.CloseNow() }()

	event, err := readPassthroughLifecycleFrame(t, clientConn, 3*time.Second)
	require.NoError(t, err)
	require.Equal(t, "response.created", gjson.GetBytes(event, "type").String())
	cancelControl(ErrOpenAIWSIngressLeaseLost)

	_, err = readPassthroughLifecycleFrame(t, clientConn, 3*time.Second)
	var closeErr coderws.CloseError
	require.ErrorAs(t, err, &closeErr)
	require.Equal(t, coderws.StatusTryAgainLater, closeErr.Code)
	require.Equal(t, "websocket ingress capacity lease lost; please reconnect", closeErr.Reason)
	select {
	case <-serverErr:
	case <-time.After(3 * time.Second):
		t.Fatal("passthrough lease-loss reader did not exit")
	}
}

func TestPassthroughLifecycle_CompletedTurnStartsInterTurnIdle(t *testing.T) {
	gin.SetMode(gin.TestMode)
	controlCtx, cancelControl := context.WithCancelCause(context.Background())
	defer cancelControl(context.Canceled)
	upstream := newStagedPassthroughConn()
	upstream.Send(`{"type":"response.completed","response":{"id":"resp_idle","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`)
	server, serverErr := startPassthroughLifecycleServer(t, controlCtx, newPassthroughLifecycleService(passthroughLifecycleConfig(), upstream), passthroughLifecycleAccount())
	defer server.Close()
	clientConn := dialPassthroughLifecycleClient(t, server)
	defer func() { _ = clientConn.CloseNow() }()

	event, err := readPassthroughLifecycleFrame(t, clientConn, 3*time.Second)
	require.NoError(t, err)
	require.Equal(t, "response.completed", gjson.GetBytes(event, "type").String())
	_, err = readPassthroughLifecycleFrame(t, clientConn, 3*time.Second)
	var closeErr coderws.CloseError
	require.ErrorAs(t, err, &closeErr)
	require.Equal(t, coderws.StatusNormalClosure, closeErr.Code)
	require.Equal(t, "websocket idle timeout", closeErr.Reason)
	select {
	case <-serverErr:
	case <-time.After(3 * time.Second):
		t.Fatal("passthrough idle reader did not exit")
	}
}

func TestPassthroughLifecycle_ActiveTurnInactivityUsesReadTimeout(t *testing.T) {
	gin.SetMode(gin.TestMode)
	controlCtx, cancelControl := context.WithCancelCause(context.Background())
	defer cancelControl(context.Canceled)
	upstream := newStagedPassthroughConn()
	upstream.Send(`{"type":"response.output_text.delta","response_id":"resp_active","delta":"hello"}`)
	server, serverErr := startPassthroughLifecycleServer(t, controlCtx, newPassthroughLifecycleService(passthroughLifecycleConfig(), upstream), passthroughLifecycleAccount())
	defer server.Close()
	clientConn := dialPassthroughLifecycleClient(t, server)
	defer func() { _ = clientConn.CloseNow() }()

	delta, err := readPassthroughLifecycleFrame(t, clientConn, 3*time.Second)
	require.NoError(t, err)
	require.Equal(t, "response.output_text.delta", gjson.GetBytes(delta, "type").String())
	_, err = readPassthroughLifecycleFrame(t, clientConn, 2500*time.Millisecond)
	var websocketCloseErr coderws.CloseError
	require.ErrorAs(t, err, &websocketCloseErr)
	require.Equal(t, coderws.StatusGoingAway, websocketCloseErr.Code)
	require.Equal(t, "upstream websocket read timeout; please reconnect", websocketCloseErr.Reason)
	select {
	case err := <-serverErr:
		var closeErr *OpenAIWSClientCloseError
		require.ErrorAs(t, err, &closeErr)
		require.Equal(t, coderws.StatusGoingAway, closeErr.StatusCode())
		require.Equal(t, "upstream websocket read timeout; please reconnect", closeErr.Reason())
	case <-time.After(2500 * time.Millisecond):
		t.Fatal("passthrough active turn remained unbounded after upstream activity stopped")
	}
}

func TestPassthroughLifecycle_PreambleAllowsPromptClientCancel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	controlCtx, cancelControl := context.WithCancelCause(context.Background())
	defer cancelControl(context.Canceled)
	cfg := passthroughLifecycleConfig()
	cfg.Gateway.OpenAIFirstOutputTimeoutSeconds = 3
	upstream := newStagedPassthroughConn()
	upstream.Send(`{"type":"response.created","response":{"id":"resp_cancel","model":"gpt-5.1"}}`)
	server, serverErr := startPassthroughLifecycleServer(t, controlCtx, newPassthroughLifecycleService(cfg, upstream), passthroughLifecycleAccount())
	defer server.Close()
	clientConn := dialPassthroughLifecycleClient(t, server)
	defer func() { _ = clientConn.CloseNow() }()
	require.Equal(t, "response.create", gjson.GetBytes(requirePassthroughUpstreamWrite(t, upstream, time.Second), "type").String())

	created, err := readPassthroughLifecycleFrame(t, clientConn, time.Second)
	require.NoError(t, err)
	require.Equal(t, "response.created", gjson.GetBytes(created, "type").String())
	writeCtx, cancelWrite := context.WithTimeout(context.Background(), time.Second)
	err = clientConn.Write(writeCtx, coderws.MessageText, []byte(`{"type":"response.cancel","response_id":"resp_cancel"}`))
	cancelWrite()
	require.NoError(t, err)
	cancelFrame := requirePassthroughUpstreamWrite(t, upstream, 500*time.Millisecond)
	require.Equal(t, "response.cancel", gjson.GetBytes(cancelFrame, "type").String())

	require.NoError(t, clientConn.Close(coderws.StatusNormalClosure, "done"))
	select {
	case <-serverErr:
	case <-time.After(3 * time.Second):
		t.Fatal("passthrough cancel test did not exit")
	}
}

func TestPassthroughLifecycle_RejectsOverlappingResponseCreate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	controlCtx, cancelControl := context.WithCancelCause(context.Background())
	defer cancelControl(context.Canceled)
	cfg := passthroughLifecycleConfig()
	cfg.Gateway.OpenAIFirstOutputTimeoutSeconds = 3
	upstream := newStagedPassthroughConn()
	upstream.Send(`{"type":"response.created","response":{"id":"resp_overlap_first","model":"gpt-5.1"}}`)
	server, serverErr := startPassthroughLifecycleServer(t, controlCtx, newPassthroughLifecycleService(cfg, upstream), passthroughLifecycleAccount())
	defer server.Close()
	clientConn := dialPassthroughLifecycleClient(t, server)
	defer func() { _ = clientConn.CloseNow() }()
	require.Equal(t, "response.create", gjson.GetBytes(requirePassthroughUpstreamWrite(t, upstream, time.Second), "type").String())

	created, err := readPassthroughLifecycleFrame(t, clientConn, time.Second)
	require.NoError(t, err)
	require.Equal(t, "response.created", gjson.GetBytes(created, "type").String())
	writeCtx, cancelWrite := context.WithTimeout(context.Background(), time.Second)
	err = clientConn.Write(writeCtx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1"}`))
	cancelWrite()
	require.NoError(t, err)

	_, err = readPassthroughLifecycleFrame(t, clientConn, time.Second)
	var websocketCloseErr coderws.CloseError
	require.ErrorAs(t, err, &websocketCloseErr)
	require.Equal(t, coderws.StatusPolicyViolation, websocketCloseErr.Code)
	require.Equal(t, "overlapping response.create is not supported", websocketCloseErr.Reason)
	select {
	case err := <-serverErr:
		var closeErr *OpenAIWSClientCloseError
		require.ErrorAs(t, err, &closeErr)
		require.Equal(t, coderws.StatusPolicyViolation, closeErr.StatusCode())
		require.Equal(t, "overlapping response.create is not supported", closeErr.Reason())
	case <-time.After(3 * time.Second):
		t.Fatal("overlapping response.create did not terminate passthrough")
	}
}

func TestPassthroughLifecycle_ActiveTurnActivityRefreshesReadTimeout(t *testing.T) {
	gin.SetMode(gin.TestMode)
	controlCtx, cancelControl := context.WithCancelCause(context.Background())
	defer cancelControl(context.Canceled)
	upstream := newStagedPassthroughConn()
	upstream.Send(`{"type":"response.output_text.delta","response_id":"resp_active_refresh","delta":"one"}`)
	go func() {
		for _, event := range []string{
			`{"type":"response.output_text.delta","response_id":"resp_active_refresh","delta":"two"}`,
			`{"type":"response.output_text.delta","response_id":"resp_active_refresh","delta":"three"}`,
			`{"type":"response.completed","response":{"id":"resp_active_refresh","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":3}}}`,
		} {
			timer := time.NewTimer(600 * time.Millisecond)
			<-timer.C
			timer.Stop()
			upstream.Send(event)
		}
	}()
	server, serverErr := startPassthroughLifecycleServer(t, controlCtx, newPassthroughLifecycleService(passthroughLifecycleConfig(), upstream), passthroughLifecycleAccount())
	defer server.Close()
	clientConn := dialPassthroughLifecycleClient(t, server)
	defer func() { _ = clientConn.CloseNow() }()

	for _, wantType := range []string{
		"response.output_text.delta",
		"response.output_text.delta",
		"response.output_text.delta",
		"response.completed",
	} {
		frame, err := readPassthroughLifecycleFrame(t, clientConn, 3*time.Second)
		require.NoError(t, err)
		require.Equal(t, wantType, gjson.GetBytes(frame, "type").String())
	}
	require.NoError(t, clientConn.Close(coderws.StatusNormalClosure, "done"))
	select {
	case <-serverErr:
	case <-time.After(3 * time.Second):
		t.Fatal("passthrough active-turn refresh test did not exit")
	}
}

func TestPassthroughLifecycle_TerminalSwitchesToInterTurnIdleTimeout(t *testing.T) {
	gin.SetMode(gin.TestMode)
	controlCtx, cancelControl := context.WithCancelCause(context.Background())
	defer cancelControl(context.Canceled)
	cfg := passthroughLifecycleConfig()
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 1
	cfg.Gateway.OpenAIWS.IngressInterTurnIdleTimeoutSeconds = 2
	upstream := newStagedPassthroughConn()
	upstream.Send(`{"type":"response.completed","response":{"id":"resp_idle_first","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`)
	server, serverErr := startPassthroughLifecycleServer(t, controlCtx, newPassthroughLifecycleService(cfg, upstream), passthroughLifecycleAccount())
	defer server.Close()
	clientConn := dialPassthroughLifecycleClient(t, server)
	defer func() { _ = clientConn.CloseNow() }()
	require.Equal(t, "response.create", gjson.GetBytes(requirePassthroughUpstreamWrite(t, upstream, 3*time.Second), "type").String())

	completed, err := readPassthroughLifecycleFrame(t, clientConn, 3*time.Second)
	require.NoError(t, err)
	require.Equal(t, "resp_idle_first", gjson.GetBytes(completed, "response.id").String())
	time.Sleep(1300 * time.Millisecond)
	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
	err = clientConn.Write(writeCtx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","previous_response_id":"resp_idle_first"}`))
	cancelWrite()
	require.NoError(t, err)
	require.Equal(t, "response.create", gjson.GetBytes(requirePassthroughUpstreamWrite(t, upstream, 3*time.Second), "type").String())
	upstream.Send(`{"type":"response.completed","response":{"id":"resp_idle_second","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`)
	completed, err = readPassthroughLifecycleFrame(t, clientConn, 3*time.Second)
	require.NoError(t, err)
	require.Equal(t, "resp_idle_second", gjson.GetBytes(completed, "response.id").String())
	_, err = readPassthroughLifecycleFrame(t, clientConn, 3*time.Second)
	var websocketCloseErr coderws.CloseError
	require.ErrorAs(t, err, &websocketCloseErr)
	require.Equal(t, coderws.StatusNormalClosure, websocketCloseErr.Code)
	require.Equal(t, "websocket idle timeout", websocketCloseErr.Reason)

	select {
	case err := <-serverErr:
		var closeErr *OpenAIWSClientCloseError
		require.ErrorAs(t, err, &closeErr)
		require.Equal(t, coderws.StatusNormalClosure, closeErr.StatusCode())
		require.Equal(t, "websocket idle timeout", closeErr.Reason())
	case <-time.After(3 * time.Second):
		t.Fatal("passthrough terminal turn did not use inter-turn idle timeout")
	}
}

func TestPassthroughLifecycle_FirstOutputTimeoutRemainsBounded(t *testing.T) {
	gin.SetMode(gin.TestMode)
	controlCtx, cancelControl := context.WithCancelCause(context.Background())
	defer cancelControl(context.Canceled)
	upstream := newStagedPassthroughConn()
	server, serverErr := startPassthroughLifecycleServer(t, controlCtx, newPassthroughLifecycleService(passthroughLifecycleConfig(), upstream), passthroughLifecycleAccount())
	defer server.Close()
	clientConn := dialPassthroughLifecycleClient(t, server)
	defer func() { _ = clientConn.CloseNow() }()

	select {
	case err := <-serverErr:
		var failoverErr *UpstreamFailoverError
		require.ErrorAs(t, err, &failoverErr)
		require.Equal(t, http.StatusGatewayTimeout, failoverErr.StatusCode)
		require.Contains(t, string(failoverErr.ResponseBody), "first_output_timeout")
	case <-time.After(2500 * time.Millisecond):
		t.Fatal("passthrough first output was left unbounded")
	}
}

func TestPassthroughLifecycle_ResponseCreatedTimeoutClosesWithoutFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)
	controlCtx, cancelControl := context.WithCancelCause(context.Background())
	defer cancelControl(context.Canceled)
	upstream := newStagedPassthroughConn()
	upstream.Send(`{"type":"response.created","response":{"id":"resp_preamble","model":"gpt-5.1"}}`)
	server, serverErr := startPassthroughLifecycleServer(t, controlCtx, newPassthroughLifecycleService(passthroughLifecycleConfig(), upstream), passthroughLifecycleAccount())
	defer server.Close()
	clientConn := dialPassthroughLifecycleClient(t, server)
	defer func() { _ = clientConn.CloseNow() }()

	created, err := readPassthroughLifecycleFrame(t, clientConn, 3*time.Second)
	require.NoError(t, err)
	require.Equal(t, "response.created", gjson.GetBytes(created, "type").String())
	_, err = readPassthroughLifecycleFrame(t, clientConn, 2500*time.Millisecond)
	var websocketCloseErr coderws.CloseError
	require.ErrorAs(t, err, &websocketCloseErr)
	require.Equal(t, coderws.StatusGoingAway, websocketCloseErr.Code)
	require.Equal(t, "upstream produced no semantic output; please reconnect", websocketCloseErr.Reason)
	select {
	case err := <-serverErr:
		var failoverErr *UpstreamFailoverError
		require.NotErrorAs(t, err, &failoverErr)
		var closeErr *OpenAIWSClientCloseError
		require.ErrorAs(t, err, &closeErr)
		require.Equal(t, coderws.StatusGoingAway, closeErr.StatusCode())
		require.Equal(t, "upstream produced no semantic output; please reconnect", closeErr.Reason())
	case <-time.After(2500 * time.Millisecond):
		t.Fatal("response.created timeout did not close the passthrough connection")
	}
}

func TestPassthroughLifecycle_SecondTurnTimeoutIsNotFailoverSafe(t *testing.T) {
	gin.SetMode(gin.TestMode)
	controlCtx, cancelControl := context.WithCancelCause(context.Background())
	defer cancelControl(context.Canceled)
	upstream := newStagedPassthroughConn()
	upstream.Send(`{"type":"response.completed","response":{"id":"resp_first","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`)
	server, serverErr := startPassthroughLifecycleServer(t, controlCtx, newPassthroughLifecycleService(passthroughLifecycleConfig(), upstream), passthroughLifecycleAccount())
	defer server.Close()
	clientConn := dialPassthroughLifecycleClient(t, server)
	defer func() { _ = clientConn.CloseNow() }()

	completed, err := readPassthroughLifecycleFrame(t, clientConn, 3*time.Second)
	require.NoError(t, err)
	require.Equal(t, "response.completed", gjson.GetBytes(completed, "type").String())
	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
	err = clientConn.Write(writeCtx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","previous_response_id":"resp_first"}`))
	cancelWrite()
	require.NoError(t, err)
	upstream.Send(`{"type":"response.created","response":{"id":"resp_second","model":"gpt-5.1"}}`)

	created, err := readPassthroughLifecycleFrame(t, clientConn, 3*time.Second)
	require.NoError(t, err)
	require.Equal(t, "response.created", gjson.GetBytes(created, "type").String())
	_, err = readPassthroughLifecycleFrame(t, clientConn, 2500*time.Millisecond)
	var websocketCloseErr coderws.CloseError
	require.ErrorAs(t, err, &websocketCloseErr)
	require.Equal(t, coderws.StatusGoingAway, websocketCloseErr.Code)
	require.Equal(t, "upstream produced no semantic output; please reconnect", websocketCloseErr.Reason)
	select {
	case err := <-serverErr:
		var failoverErr *UpstreamFailoverError
		require.NotErrorAs(t, err, &failoverErr, "handler must not replay the initial request on another account for a later-turn timeout")
		var closeErr *OpenAIWSClientCloseError
		require.ErrorAs(t, err, &closeErr)
		require.Equal(t, coderws.StatusGoingAway, closeErr.StatusCode())
	case <-time.After(2500 * time.Millisecond):
		t.Fatal("second turn first semantic output was left unbounded")
	}
}
