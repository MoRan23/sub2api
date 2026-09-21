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

const (
	telemetryWSRoot     = "01989f44-7c00-7000-8000-000000000011"
	telemetryWSChildA   = "01989f44-7c00-7000-8000-000000000012"
	telemetryWSChildB   = "01989f44-7c00-7000-8000-000000000013"
	telemetryWSTurnID   = "01989f44-7c00-7000-8000-000000000014"
	telemetryWSSyncRoot = "01989f44-7c00-7000-8000-000000000015"
)

func telemetryWSWireFixture(t *testing.T) (*OpenAIGatewayService, *Account, http.Header, http.Header, func(string) []byte) {
	t.Helper()
	service, _ := telemetryCaptureService(t)
	gateway := &OpenAIGatewayService{codexTelemetry: service}
	account := &Account{ID: 12, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	physical := http.Header{"User-Agent": {"codex-tui/0.154.0 (Ubuntu 24.04; x86_64)"}, "Session-Id": {telemetryWSRoot}, "Thread-Id": {telemetryWSRoot}}
	auth := http.Header{"Authorization": {"Bearer synthetic-token"}, "Chatgpt-Account-Id": {"synthetic-account"}, "Thread-Id": {telemetryWSChildB}}
	body := func(thread string) []byte {
		return []byte(`{"type":"response.create","model":"gpt-6-astra","client_metadata":{"session_id":"` + telemetryWSRoot + `","thread_id":"` + thread + `","turn_id":"` + telemetryWSTurnID + `"}}`)
	}
	return gateway, account, physical, auth, body
}

func TestCodexTelemetryWSFinalFrameIdentityIsIndependentOfObservation(t *testing.T) {
	gateway, account, physical, auth, body := telemetryWSWireFixture(t)
	SetFingerprintObservationEnabled(false)
	frame := body(telemetryWSChildA)
	before := append([]byte(nil), frame...)
	turn := gateway.beginCodexTelemetryWS(context.Background(), account, physical, auth, frame)
	require.NotNil(t, turn)
	input := turn.attempt.profile.input
	require.Equal(t, telemetryWSRoot, input.SessionID)
	require.Equal(t, telemetryWSChildA, input.ThreadID, "per-frame identity must win over reused socket and unrelated auth headers")
	require.Equal(t, telemetryWSTurnID, input.TurnID)
	require.True(t, input.WebSocket)
	require.Equal(t, "synthetic-token", input.AccessToken)
	require.Equal(t, "synthetic-account", input.ChatGPTAccountID)
	require.Equal(t, before, frame)
	require.Empty(t, physical.Get("Authorization"), "do not place secrets in the physical observation snapshot")
	physical.Set("Thread-Id", "changed")
	auth.Set("Authorization", "Bearer changed")
	frame[0] = ' '
	require.Equal(t, telemetryWSChildA, turn.attempt.profile.input.ThreadID)
	require.Equal(t, "synthetic-token", turn.attempt.profile.input.AccessToken)
	turn.observe([]byte(`{"type":"response.completed","response":{"id":"real-response","status":"completed","usage":{"input_tokens":19,"output_tokens":5}}}`), "response.completed")
	turn.finish(false)
	require.Equal(t, "completed", turn.result.Status)
	require.EqualValues(t, 19, turn.result.InputTokens)
	require.True(t, turn.result.FirstTokenAt.IsZero(), "completion without a delta has no measured first token")
	require.False(t, turn.result.ExplicitClientInterrupt)
}

func TestCodexTelemetryWSSharedRootChildrenDoNotMerge(t *testing.T) {
	gateway, account, physical, auth, body := telemetryWSWireFixture(t)
	left := gateway.beginCodexTelemetryWS(context.Background(), account, physical, auth, body(telemetryWSChildA))
	right := gateway.beginCodexTelemetryWS(context.Background(), account, physical, auth, body(telemetryWSChildB))
	require.NotNil(t, left)
	require.NotNil(t, right)
	require.NotEqual(t, left.attempt.profile.threadID, right.attempt.profile.threadID)
	require.NotEqual(t, left.attempt.attemptID, right.attempt.attemptID)
	left.observe([]byte(`{"type":"response.failed","response":{"status":"failed"}}`), "response.failed")
	right.observe([]byte(`{"type":"response.completed","response":{"status":"completed"}}`), "response.completed")
	left.finish(false)
	right.finish(false)
	require.Equal(t, "failed", left.result.Status)
	require.Equal(t, "completed", right.result.Status)
}

func TestCodexTelemetryWSRetryDoesNotCommitEarlyFailure(t *testing.T) {
	gateway, account, physical, auth, body := telemetryWSWireFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := gateway.beginCodexTelemetryWS(ctx, account, physical, auth, body(telemetryWSChildA))
	first.observe([]byte(`{"type":"error","error":{"code":"server_error","type":"server_error","message":"private error text"}}`), "error")
	first.finish(true)
	require.True(t, first.done)
	second := gateway.beginCodexTelemetryWS(ctx, account, physical, auth, body(telemetryWSChildA))
	require.Equal(t, first.attempt.profile.turnID, second.attempt.profile.turnID)
	require.NotEqual(t, first.attempt.attemptID, second.attempt.attemptID)
	second.observe([]byte(`{"type":"response.output_text.delta","delta":"private output"}`), "response.output_text.delta")
	second.observe([]byte(`{"type":"response.completed","response":{"id":"retry-response","status":"completed","usage":{"input_tokens":7,"output_tokens":2}}}`), "response.completed")
	second.finish(false)
	require.True(t, second.done)
	require.Equal(t, "completed", second.result.Status)
	require.False(t, second.result.FirstTokenAt.IsZero())
	require.Equal(t, "retry-response", second.result.ResponseID)
	snapshot, err := json.Marshal(gateway.codexTelemetry.Observations(CodexTelemetryObservationQuery{}))
	require.NoError(t, err)
	require.NotContains(t, string(snapshot), "private error text")
	require.NotContains(t, string(snapshot), "private output")
}

func TestCodexTelemetryWSExcludedRequestsAndNoIdentityFabrication(t *testing.T) {
	gateway, account, physical, auth, body := telemetryWSWireFixture(t)
	account.Type = AccountTypeAPIKey
	require.Nil(t, gateway.beginCodexTelemetryWS(context.Background(), account, physical, auth, body(telemetryWSChildA)))
	account.Type = AccountTypeOAuth
	for _, frame := range []string{
		`{"type":"response.create","model":"gpt-6-astra","generate":false}`,
		`{"type":"response.create","model":"gpt-image-1","input":"test"}`,
	} {
		require.Nil(t, gateway.beginCodexTelemetryWS(context.Background(), account, physical, auth, []byte(frame)), frame)
	}
	physical.Del("Session-Id")
	physical.Del("Thread-Id")
	turn := gateway.beginCodexTelemetryWS(context.Background(), account, physical, auth, []byte(`{"type":"response.create","model":"gpt-6-astra"}`))
	require.NotNil(t, turn)
	require.Empty(t, turn.attempt.profile.input.SessionID)
	require.Empty(t, turn.attempt.profile.input.ThreadID)
	require.Empty(t, turn.attempt.profile.input.TurnID)
	turn.finish(false)
	telemetryWaitDrained(t, gateway.codexTelemetry)
	got := gateway.codexTelemetry.Observations(CodexTelemetryObservationQuery{})
	require.Greater(t, got.Counters.Skipped, uint64(0))
}

func TestCodexTelemetryWSDisconnectIsNotUserCancel(t *testing.T) {
	gateway, account, physical, auth, body := telemetryWSWireFixture(t)
	turn := gateway.beginCodexTelemetryWS(context.Background(), account, physical, auth, body(telemetryWSChildA))
	turn.observe([]byte(`{"type":"response.created","response":{"id":"partial-response"}}`), "response.created")
	turn.finish(false)
	require.False(t, turn.result.ExplicitClientInterrupt)
	require.True(t, turn.result.FirstTokenAt.IsZero())
	// The status normalization is passed to the attempt without fabricating
	// a response terminal in the observed result.
	require.True(t, turn.done)
	other := &codexTelemetryWSTurn{attempt: gateway.codexTelemetry.Begin(context.Background(), telemetryTestInput())}
	other.requestCancel()
	other.observe([]byte(`{"type":"response.cancelled","response":{"status":"cancelled"}}`), "response.cancelled")
	require.True(t, other.result.ExplicitClientInterrupt)
	other.finish(false)
}

type telemetryWSPhysicalFrameStub struct {
	written []byte
	err     error
}

func (*telemetryWSPhysicalFrameStub) ReadFrame(context.Context) (coderws.MessageType, []byte, error) {
	return coderws.MessageText, nil, errors.New("no read")
}
func (s *telemetryWSPhysicalFrameStub) WriteFrame(_ context.Context, _ coderws.MessageType, body []byte) error {
	s.written = append([]byte(nil), body...)
	return s.err
}
func (*telemetryWSPhysicalFrameStub) Close() error { return nil }

func TestCodexTelemetryWSFinalizingFrameObserverSeesPhysicalWriteResult(t *testing.T) {
	upstream := &telemetryWSPhysicalFrameStub{err: errors.New("socket closed")}
	final := []byte(`{"type":"response.create","client_metadata":{"thread_id":"final-thread"}}`)
	var observed []byte
	var observedErr error
	conn := &openAIWSFinalizingUpstreamFrameConn{
		inner:    upstream,
		finalize: func(_ coderws.MessageType, _ []byte) ([]byte, error) { return final, nil },
		afterWrite: func(_ coderws.MessageType, body []byte, err error) {
			observed, observedErr = append([]byte(nil), body...), err
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.ErrorIs(t, conn.WriteFrame(ctx, coderws.MessageText, []byte(`{"type":"response.create"}`)), upstream.err)
	require.Equal(t, final, upstream.written)
	require.Equal(t, upstream.written, observed)
	require.ErrorIs(t, observedErr, upstream.err)
}

func TestCodexTelemetryWSGatewayTransportsUseFinalWireIdentity(t *testing.T) {
	cases := []struct {
		name              string
		mode              string
		daily             bool
		stream            bool
		seedStreamContext bool
	}{
		{"http_to_ws", "http_to_ws", false, false, false},
		{"ctx_pool", OpenAIWSIngressModeCtxPool, false, false, false},
		{"passthrough", OpenAIWSIngressModePassthrough, false, false, false},
		{"daily_http_to_ws", "http_to_ws", true, true, false},
		{"daily_ctx_pool", OpenAIWSIngressModeCtxPool, true, true, true},
		{"daily_passthrough", OpenAIWSIngressModePassthrough, true, true, true},
		{"daily_ctx_pool_without_stream_context", OpenAIWSIngressModeCtxPool, true, true, false},
		{"daily_passthrough_without_stream_context", OpenAIWSIngressModePassthrough, true, true, false},
		{"daily_sync_http_to_ws", "http_to_ws", true, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mode := tc.mode
			gin.SetMode(gin.TestMode)
			telemetry, calls := telemetryCaptureService(t)
			cfg := &config.Config{}
			cfg.JWT.Secret = "telemetry-wire-test"
			cfg.Gateway.OpenAIWS.Enabled = true
			cfg.Gateway.OpenAIWS.OAuthEnabled = true
			cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
			cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
			cfg.Gateway.OpenAIWS.IngressModeDefault = OpenAIWSIngressModeCtxPool
			cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
			cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
			cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
			cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
			cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3
			upstream := &openAIWSCaptureConn{events: [][]byte{[]byte(`{"type":"response.completed","response":{"id":"real-wire-response","status":"completed","model":"gpt-5.1","usage":{"input_tokens":12,"output_tokens":4}}}`)}}
			dialer := &openAIWSQueueDialer{conns: []openAIWSClientConn{upstream}}
			pool := newOpenAIWSConnPool(cfg)
			pool.setClientDialerForTest(dialer)
			t.Cleanup(pool.Close)
			gateway := &OpenAIGatewayService{
				cfg: cfg, codexTelemetry: telemetry, cache: &stubGatewayCache{},
				openaiWSResolver: NewOpenAIWSProtocolResolver(cfg), toolCorrector: NewCodexToolCorrector(),
				openaiWSPool: pool, openaiWSPassthroughDialer: dialer,
			}
			if tc.daily {
				configureCodexTelemetryWSDailyFixture(gateway)
			}
			account := &Account{ID: 531, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1,
				Credentials: map[string]any{"access_token": "synthetic-token", "chatgpt_account_id": "synthetic-account"},
				Extra:       map[string]any{"responses_websockets_v2_enabled": true, "openai_oauth_responses_websockets_v2_mode": mode, openAIPinnedInstallationIDKey: transportTestPinnedInstallationID},
			}
			body := []byte(`{"model":"gpt-5.1","stream":false,"input":[{"type":"input_text","text":"synthetic request"}]}`)
			if tc.stream {
				body = []byte(strings.Replace(string(body), `"stream":false`, `"stream":true`, 1))
			}
			newContext := func(req *http.Request) *gin.Context {
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = req
				c.Set("api_key", &APIKey{ID: 98})
				return c
			}
			if mode == "http_to_ws" {
				delete(account.Extra, "openai_oauth_responses_websockets_v2_mode")
				c := newContext(httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
				result, err := gateway.Forward(context.Background(), c, account, body)
				require.NoError(t, err)
				require.NotNil(t, result)
			} else {
				serverDone := make(chan error, 1)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					conn, err := coderws.Accept(w, r, nil)
					if err != nil {
						serverDone <- err
						return
					}
					defer func() { _ = conn.CloseNow() }()
					ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
					defer cancel()
					_, first, err := conn.Read(ctx)
					if err != nil {
						serverDone <- err
						return
					}
					c := newContext(r)
					// Mirror the handler's immutable capture and scheduling setup.
					// The daily transport cases separately supply the stream context
					// consumed by identity resolution; the compatibility cases keep
					// the existing WS entry behavior when that marker is absent.
					if tc.seedStreamContext {
						setOpenAIClientRequestedStream(c, gjson.GetBytes(first, "stream").Bool())
					}
					SetOpenAIOAuthIdentityCapture(c, CaptureOpenAIOAuthIdentity(c, first, "telemetry_ws_connection:"+tc.name))
					gateway.GenerateSessionHashForOpenAIOAuthIdentity(c, first, "telemetry_ws_connection:"+tc.name)
					serverDone <- gateway.ProxyResponsesWebSocketFromClient(ctx, c, conn, account, "synthetic-token", first, nil)
				}))
				t.Cleanup(server.Close)
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				client, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses", nil)
				require.NoError(t, err)
				defer func() { _ = client.CloseNow() }()
				frame := append([]byte(`{"type":"response.create",`), body[1:]...)
				require.NoError(t, client.Write(ctx, coderws.MessageText, frame))
				_, reply, err := client.Read(ctx)
				require.NoError(t, err)
				require.Equal(t, "real-wire-response", gjson.GetBytes(reply, "response.id").String())
				_ = client.CloseNow()
				select {
				case <-serverDone:
				case <-ctx.Done():
					t.Fatal("gateway did not exit after closing local client")
				}
			}
			telemetryWaitDrained(t, telemetry)
			upstream.mu.Lock()
			wire, err := json.Marshal(upstream.lastWrite)
			upstream.mu.Unlock()
			require.NoError(t, err)
			actualThread := gjson.GetBytes(wire, "client_metadata.thread_id").String()
			actualSession := gjson.GetBytes(wire, "client_metadata.session_id").String()
			actualTurn := gjson.GetBytes(wire, "client_metadata.turn_id").String()
			physicalHeaders := dialer.DialHeaders(0)
			if actualSession == "" {
				actualSession = physicalHeaders.Get("session_id")
				if actualSession == "" {
					actualSession = physicalHeaders.Get("session-id")
				}
			}
			if actualThread == "" {
				actualThread = physicalHeaders.Get("thread_id")
				if actualThread == "" {
					actualThread = physicalHeaders.Get("thread-id")
				}
			}
			// The physical wire may omit session/thread even when the local
			// plan has them. Telemetry must leave omitted values empty rather
			// than fabricate IDs; turn metadata remains observable when present.
			if mode == "http_to_ws" && !tc.stream {
				// The existing HTTP-to-WS synchronous path does not materialize
				// the ordinary HTTP builder's sync root, even with daily rotation.
				require.Empty(t, actualSession, "the existing synchronous HTTP-to-WS path sends no session on the frame or handshake")
				require.Empty(t, actualThread, "a generated request turn is not a session/thread identity")
			} else {
				require.NotEmpty(t, actualSession)
				require.NotEmpty(t, actualThread)
			}
			require.NotEmpty(t, actualTurn)
			if tc.daily && tc.stream && (mode == "http_to_ws" || tc.seedStreamContext) {
				require.Equal(t, telemetryWSRoot, actualSession, "the actual frame must carry the root resolved from the daily pool")
			}
			for _, key := range []string{"session_id", "session-id"} {
				if value := physicalHeaders.Get(key); value != "" {
					require.Equal(t, actualSession, value, "physical session header and final frame must agree")
				}
			}
			for _, key := range []string{"thread_id", "thread-id"} {
				if value := physicalHeaders.Get(key); value != "" {
					require.Equal(t, actualThread, value, "physical thread header and final frame must agree")
				}
			}
			mainEvents := 0
			for _, sent := range calls() {
				if sent.metrics {
					continue
				}
				require.Equal(t, actualSession, sent.input.SessionID)
				require.Equal(t, actualThread, sent.input.ThreadID)
				require.Equal(t, actualTurn, sent.input.TurnID)
				var payload struct {
					Events []codexAnalyticsEvent `json:"events"`
				}
				require.NoError(t, json.Unmarshal(sent.body, &payload))
				for _, event := range payload.Events {
					if event.EventType == "codex_turn_event" && (actualThread == "" || event.EventParams["thread_id"] == actualThread) {
						mainEvents++
						require.Equal(t, "completed", event.EventParams["status"])
					}
				}
			}
			wantEvents := 0 // One Responses completion alone does not seal a client turn.
			if actualSession == "" || actualThread == "" {
				// This fixture intentionally has no physical session/thread headers;
				// the service records the attempt but must skip turn attribution.
				wantEvents = 0
				foundMissingIdentity := false
				for _, entry := range telemetry.Observations(CodexTelemetryObservationQuery{}).Items {
					if entry.Error == "missing_outbound_session_or_thread" {
						foundMissingIdentity = true
					}
				}
				require.True(t, foundMissingIdentity, "missing wire identity must have an explicit skip reason")
			}
			require.Equal(t, wantEvents, mainEvents)
			require.EqualValues(t, 1, telemetry.Observations(CodexTelemetryObservationQuery{}).Counters.Attempts)
		})
	}
}

func configureCodexTelemetryWSDailyFixture(gateway *OpenAIGatewayService) {
	gateway.settingService = NewSettingService(&dailyRotationSettingRepo{values: map[string]string{
		SettingKeyEnableOpenAICodexFingerprintNormalization: "true",
		SettingKeyEnableOpenAIUUIDv7SessionIdentity:         "true",
		SettingKeyEnableOpenAIOAuthDailySessionRotation:     "true",
	}}, nil)
	gateway.oauthDailySessionRepo = &fakeOAuthDailyAffinityRepository{
		pool: OAuthDailySessionPool{AccountID: 531, BusinessDate: OAuthDailyBusinessDate(time.Now()), Generation: telemetryWSTurnID,
			StreamSessionIDs: [OAuthDailyStreamSessionCount]string{telemetryWSRoot, telemetryWSChildA, telemetryWSChildB}, SyncSessionID: telemetryWSSyncRoot},
		affinity: OAuthDailySessionAffinity{AccountID: 531, APIKeyID: 98, LogicalSessionKey: "logical", BusinessDate: OAuthDailyBusinessDate(time.Now()),
			Generation: telemetryWSTurnID, SlotIndex: 0, StreamSessionID: telemetryWSRoot},
	}
}

type telemetryWSConcurrentCaptureConn struct {
	*openAIWSCaptureConn
	writtenOnce sync.Once
	afterWrite  func()
	ready       <-chan struct{}
}

func (c *telemetryWSConcurrentCaptureConn) WriteJSON(ctx context.Context, value any) error {
	if err := c.openAIWSCaptureConn.WriteJSON(ctx, value); err != nil {
		return err
	}
	c.writtenOnce.Do(c.afterWrite)
	return nil
}

func (c *telemetryWSConcurrentCaptureConn) WriteFrame(ctx context.Context, _ coderws.MessageType, payload []byte) error {
	return c.WriteJSON(ctx, json.RawMessage(payload))
}

func (c *telemetryWSConcurrentCaptureConn) ReadMessage(ctx context.Context) ([]byte, error) {
	select {
	case <-c.ready:
		return c.openAIWSCaptureConn.ReadMessage(ctx)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *telemetryWSConcurrentCaptureConn) ReadFrame(ctx context.Context) (coderws.MessageType, []byte, error) {
	payload, err := c.ReadMessage(ctx)
	return coderws.MessageText, payload, err
}

func TestCodexTelemetryWSDailyRootConcurrentChildrenUseActualWire(t *testing.T) {
	telemetry, calls := telemetryCaptureService(t)
	cfg := &config.Config{}
	cfg.JWT.Secret = "daily-telemetry-concurrency"
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 2
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 2
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3
	// Neither turn may complete before both writes: otherwise scheduler timing
	// can allow the second request to reuse a completed connection.
	ready := make(chan struct{})
	var writtenMu sync.Mutex
	written := 0
	afterWrite := func() {
		writtenMu.Lock()
		defer writtenMu.Unlock()
		written++
		if written == 2 {
			close(ready)
		}
	}
	newConn := func(id string) *telemetryWSConcurrentCaptureConn {
		return &telemetryWSConcurrentCaptureConn{
			openAIWSCaptureConn: &openAIWSCaptureConn{events: [][]byte{[]byte(`{"type":"response.completed","response":{"id":"` + id + `","status":"completed","usage":{"input_tokens":3,"output_tokens":1}}}`)}},
			afterWrite:          afterWrite, ready: ready,
		}
	}
	first, second := newConn("daily-response-a"), newConn("daily-response-b")
	pool := newOpenAIWSConnPool(cfg)
	dialer := &openAIWSQueueDialer{conns: []openAIWSClientConn{first, second}}
	pool.setClientDialerForTest(dialer)
	t.Cleanup(pool.Close)
	gateway := &OpenAIGatewayService{cfg: cfg, codexTelemetry: telemetry, openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector: NewCodexToolCorrector(), openaiWSPool: pool}
	configureCodexTelemetryWSDailyFixture(gateway)
	account := &Account{ID: 531, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 2,
		Credentials: map[string]any{"access_token": "synthetic-token", "chatgpt_account_id": "synthetic-account"},
		Extra:       map[string]any{"responses_websockets_v2_enabled": true, openAIPinnedInstallationIDKey: transportTestPinnedInstallationID}}
	var wg sync.WaitGroup
	errorsCh := make(chan error, 2)
	for _, child := range []string{"child-a", "child-b"} {
		wg.Add(1)
		go func(child string) {
			defer wg.Done()
			localAccount := *account
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			c.Set("api_key", &APIKey{ID: 98})
			body := []byte(`{"model":"gpt-5.1","stream":true,"input":"synthetic request","client_metadata":{"session_id":"client-root","thread_id":"` + child + `"}}`)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, err := gateway.Forward(ctx, c, &localAccount, body)
			errorsCh <- err
		}(child)
	}
	wg.Wait()
	close(errorsCh)
	for err := range errorsCh {
		require.NoError(t, err)
	}
	telemetryWaitDrained(t, telemetry)
	require.Equal(t, 2, dialer.DialCount(), "each concurrent child must use its own connection without retries")
	actualThreads := map[string]bool{}
	for _, conn := range []*telemetryWSConcurrentCaptureConn{first, second} {
		conn.mu.Lock()
		wire, err := json.Marshal(conn.lastWrite)
		writes := len(conn.writes)
		conn.mu.Unlock()
		require.NoError(t, err)
		require.Equal(t, 1, writes, "one physical create frame per concurrent child")
		require.Equal(t, telemetryWSRoot, gjson.GetBytes(wire, "client_metadata.session_id").String())
		thread := gjson.GetBytes(wire, "client_metadata.thread_id").String()
		require.NotEmpty(t, thread)
		require.NotEqual(t, telemetryWSRoot, thread)
		actualThreads[thread] = true
	}
	require.Len(t, actualThreads, 2)
	observedThreads := map[string]bool{}
	for _, sent := range calls() {
		if sent.metrics {
			continue
		}
		require.Equal(t, telemetryWSRoot, sent.input.SessionID)
		require.True(t, actualThreads[sent.input.ThreadID], "telemetry must use a physically sent child identity")
		var payload struct {
			Events []codexAnalyticsEvent `json:"events"`
		}
		require.NoError(t, json.Unmarshal(sent.body, &payload))
		for _, event := range payload.Events {
			if event.EventType == "codex_thread_initialized" && event.EventParams["thread_id"] == sent.input.ThreadID {
				require.False(t, observedThreads[sent.input.ThreadID], "one initialization event per actual child")
				observedThreads[sent.input.ThreadID] = true
			}
		}
	}
	require.Equal(t, actualThreads, observedThreads)
}
