package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
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

// These transport fixtures model the same physical-send activity boundary as
// the runtime repository. Merely preparing a request does not renew a token.
func (r *codexWSStateTestRepo) MarkBusinessSent(_ context.Context, key CodexTurnStateKey, sentAt time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if record, ok := r.records[key]; ok && sentAt.After(record.LastBusinessAt) {
		record.LastBusinessAt = sentAt
		r.records[key] = record
	}
	return nil
}

func (r *codexStatePassthroughRepository) MarkBusinessSent(_ context.Context, key CodexTurnStateKey, sentAt time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if record, ok := r.records[key]; ok && sentAt.After(record.LastBusinessAt) {
		record.LastBusinessAt = sentAt
		r.records[key] = record
	}
	return nil
}

func requireCodexEnabledSummary(t *testing.T, state *CodexTurnStateService, accountID int64, model, token, responseSource, requestSource string, outboundLength int) CodexTurnStateModelObservation {
	t.Helper()
	require.False(t, IsFingerprintObservationEnabled())
	// Native WS fixtures intentionally expose only runtime writes. A read-only
	// status repository keeps the assertion on the real management read path.
	reader := NewCodexTurnStateService(&codexStateBatchRecords{}, state.accounts, nil, nil)
	reader.modelPolicy = &codexStateBatchPolicy{models: []string{model}}
	var status *CodexTurnStateStatus
	require.Eventually(t, func() bool {
		var err error
		status, err = reader.GetStatus(context.Background(), accountID)
		return err == nil && len(status.Observations) == 1
	}, time.Second, time.Millisecond)
	require.True(t, status.Enabled)
	require.True(t, status.ObservationEnabled)
	require.Equal(t, "instance", status.ObservationScope)
	observation := status.Observations[0]
	require.Equal(t, model, observation.Model)
	require.Equal(t, len(token), observation.ResponseLength)
	require.Equal(t, responseSource, observation.ResponseSource)
	require.Equal(t, requestSource, observation.RequestSource)
	require.Equal(t, outboundLength, observation.OutboundLength)
	require.False(t, observation.ObservedAt.IsZero())
	require.Empty(t, SnapshotFingerprintObservations(0), "compact summaries must not enable full fingerprint diagnostics")
	encoded, err := json.Marshal(status)
	require.NoError(t, err)
	for _, secret := range []string{token, "encrypted_token", "test-token", "test-access", "private-client-frame"} {
		require.NotContains(t, string(encoded), secret)
	}
	return observation
}

func seedCodexEnabledObservation(t *testing.T, state *CodexTurnStateService, account *Account, model string) string {
	t.Helper()
	token := codexStateTestToken(10, state.now().Add(-10*time.Minute))
	attempt, err := state.Prepare(context.Background(), account, model)
	require.NoError(t, err)
	require.NotNil(t, attempt)
	require.True(t, attempt.Enabled)
	state.Observe(attempt, token)
	require.NoError(t, state.Finish(context.Background(), attempt, true))
	return token
}

func TestCodexStateEnabledObservationHTTPAllPathsFingerprintOff(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, path := range []string{"responses", "passthrough", "chat", "messages"} {
		for _, stream := range []bool{false, true} {
			for _, carrier := range []string{"header", "metadata"} {
				t.Run(path+"/stream="+strconv.FormatBool(stream)+"/"+carrier, func(t *testing.T) {
					isolateCodexTurnStateSummaryStore(t)
					state, _, account := newCodexStateTestService(t)
					account.Concurrency = 1
					if path == "passthrough" {
						account.Extra["openai_passthrough"] = true
					}
					cached := seedCodexEnabledObservation(t, state, account, "gpt-5.4")
					token := codexStateTestToken(10, state.now())
					upstream := &httpUpstreamRecorder{resp: codexStateHTTPIntegrationResponse(token, carrier, false)}
					gateway := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream, codexTurnStateService: state}
					result, recorder, err := codexStateHTTPIntegrationForward(t, gateway, account, path, codexStateHTTPIntegrationBody(path, stream))
					require.NoError(t, err, recorder.Body.String())
					require.NotNil(t, result)
					require.Len(t, upstream.requests, 1)
					require.Equal(t, cached, upstream.lastReq.Header.Get(openAICodexTurnStateHeader))
					observation := requireCodexEnabledSummary(t, state, account.ID, "gpt-5.4", token, carrier, "business", len(cached))
					require.Equal(t, "target", observation.ResponseShape)
					require.Equal(t, CodexTurnStateObservedPersonalTarget, observation.ResponseObservedShape)
					require.Equal(t, 10, observation.ResponseCipherBlocks)
				})
			}
		}
	}
}

type codexEnabledObservationBeginRepo struct {
	*codexStateMemoryRepo
	err   error
	calls atomic.Int64
}

func (r *codexEnabledObservationBeginRepo) BeginBusiness(context.Context, CodexTurnStateKey, string, time.Time, time.Time) (*CodexTurnStateRecord, error) {
	r.calls.Add(1)
	return nil, r.err
}

func TestCodexStateEnabledObservationRealBeginFallback(t *testing.T) {
	for _, mode := range []string{"begin_error", "begin_nil", "missing_generation"} {
		for _, path := range []string{"responses", "passthrough", "chat", "messages"} {
			t.Run(mode+"/"+path, func(t *testing.T) {
				isolateCodexTurnStateSummaryStore(t)
				state, repo, account := newCodexStateTestService(t)
				account.Concurrency = 1
				if path == "passthrough" {
					account.Extra["openai_passthrough"] = true
				}
				failing := &codexEnabledObservationBeginRepo{codexStateMemoryRepo: repo}
				state.repo = failing
				reason := "generation_changed"
				if mode == "begin_error" {
					failing.err, reason = errors.New("synthetic begin unavailable"), "maintenance_unavailable"
				}
				if mode == "missing_generation" {
					delete(account.Extra, CodexTurnStateGenerationExtraKey)
					reason = "generation_unavailable"
				}
				attempt, err := state.Prepare(context.Background(), account, "gpt-5.4")
				require.NoError(t, err)
				require.NotNil(t, attempt)
				require.True(t, attempt.AccountEnabled)
				require.False(t, attempt.Enabled)
				require.Equal(t, reason, attempt.MaintenanceReason)
				require.Empty(t, attempt.id)
				require.Empty(t, attempt.Snapshot.Token)
				require.NoError(t, state.Finish(context.Background(), attempt, false))
				token := codexStateTestToken(10, state.now())
				upstream := &httpUpstreamRecorder{resp: codexStateHTTPIntegrationResponse(token, "metadata", false)}
				gateway := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream, codexTurnStateService: state}
				result, recorder, err := codexStateHTTPIntegrationForward(t, gateway, account, path, codexStateHTTPIntegrationBody(path, false))
				require.NoError(t, err, recorder.Body.String())
				require.NotNil(t, result)
				require.Len(t, upstream.requests, 1)
				require.Empty(t, upstream.lastReq.Header.Get(openAICodexTurnStateHeader))
				requireCodexEnabledSummary(t, state, account.ID, "gpt-5.4", token, "metadata", "business", 0)
				if mode == "missing_generation" {
					require.Zero(t, failing.calls.Load())
				} else {
					require.GreaterOrEqual(t, failing.calls.Load(), int64(2))
				}
				require.Empty(t, repo.records)
				require.Empty(t, repo.leases)
				require.Empty(t, state.business)
				require.Empty(t, state.queue)
			})
		}
	}
}

func TestCodexStateEnabledObservationHTTPToWSBridgeFingerprintOff(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(strconv.FormatBool(stream), func(t *testing.T) {
			isolateCodexTurnStateSummaryStore(t)
			svc, account, _, _ := newCodexWSStateTestGateway(t, "personal")
			cached := seedCodexEnabledObservation(t, svc.codexTurnStateService, account, "gpt-5.1")
			cfg := newOpenAIWSV2TestConfig()
			cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
			cfg.Gateway.OpenAIWS.OAuthEnabled = true
			svc.cfg, svc.httpUpstream = cfg, &httpUpstreamRecorder{}
			svc.openaiWSResolver = NewOpenAIWSProtocolResolver(cfg)
			token := makeCodexWSStateTestToken(10, time.Now().Add(-time.Minute))
			conn := &openAIWSCaptureConn{events: [][]byte{
				[]byte(fmt.Sprintf(`{"type":"response.metadata","headers":{"x-codex-turn-state":%q}}`, token)),
				[]byte(`{"type":"response.completed","response":{"id":"resp_enabled_bridge","model":"gpt-5.1","status":"completed","usage":{"input_tokens":1,"output_tokens":1}}}`),
			}}
			svc.openaiWSPool = newOpenAIWSConnPool(cfg)
			svc.openaiWSPool.setClientDialerForTest(&openAIWSCaptureDialer{conn: conn})
			defer svc.openaiWSPool.Close()
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader("{}"))
			var recovered bool
			result, err := svc.forwardOpenAIWSV2(context.Background(), c, account,
				map[string]any{"model": "gpt-5.1", "stream": stream, "input": "hi"}, "", "", "test-access",
				OpenAIWSProtocolDecision{Transport: OpenAIUpstreamTransportResponsesWebsocketV2}, false, stream,
				"gpt-5.1", "gpt-5.1", time.Now(), 0, "", &recovered)
			require.NoError(t, err)
			require.NotNil(t, result)
			conn.mu.Lock()
			frame, marshalErr := json.Marshal(conn.lastWrite)
			conn.mu.Unlock()
			require.NoError(t, marshalErr)
			require.Equal(t, cached, gjson.GetBytes(frame, "client_metadata.x-codex-turn-state").String())
			requireCodexEnabledSummary(t, svc.codexTurnStateService, account.ID, "gpt-5.1", token, "metadata", "business", len(cached))
		})
	}
}

func TestCodexStateEnabledObservationWSToHTTPBridgeFingerprintOff(t *testing.T) {
	isolateCodexTurnStateSummaryStore(t)
	state, _, account := newCodexStateTestService(t)
	account.Concurrency = 1
	cached := seedCodexEnabledObservation(t, state, account, "gpt-5.4")
	token := codexStateTestToken(10, state.now())
	upstream := &httpUpstreamRecorder{resp: codexStateHTTPIntegrationResponse(token, "metadata", false)}
	svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}, httpUpstream: upstream, codexTurnStateService: state}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	payload := []byte(`{"type":"response.create","model":"gpt-5.4","input":"hi"}`)
	var writes [][]byte
	result, err := svc.proxyOpenAIWSHTTPBridgeTurn(context.Background(), c, account, "test-token", payload, len(payload),
		"gpt-5.4", "", "", "", "", 1, func(frame []byte) error { writes = append(writes, append([]byte(nil), frame...)); return nil })
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotEmpty(t, writes)
	require.Len(t, upstream.requests, 1)
	require.Equal(t, cached, upstream.lastReq.Header.Get(openAICodexTurnStateHeader))
	requireCodexEnabledSummary(t, state, account.ID, "gpt-5.4", token, "metadata", "business", len(cached))
}

func TestCodexStateEnabledObservationNativeWSFingerprintOff(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		t.Run(fmt.Sprintf("passthrough_%t", passthrough), func(t *testing.T) {
			isolateCodexTurnStateSummaryStore(t)
			var svc *OpenAIGatewayService
			var account *Account
			var upstream *stagedPassthroughConn
			var request chan http.Header
			var response http.Header
			var cached string
			if passthrough {
				var repo *codexStatePassthroughRepository
				var dialer *codexStatePassthroughDialer
				svc, account, _, repo, dialer = newCodexStatePassthroughHarness(t, true)
				upstream, request, response = dialer.conn, dialer.request, dialer.headers
				cached = makeCodexWSStateTestToken(10, time.Now().Add(-10*time.Minute))
				seedCodexStatePassthroughModel(t, repo, account, "gpt-5.5", cached)
			} else {
				var dialer *codexWSStatePooledDialer
				svc, account, _, _, dialer = newCodexWSStateIngressHarness(t)
				upstream, request, response = dialer.conn.stagedPassthroughConn, dialer.request, dialer.response
				cached = seedCodexEnabledObservation(t, svc.codexTurnStateService, account, "gpt-5.5")
			}
			token := makeCodexWSStateTestToken(10, time.Now().Add(-time.Minute))
			response.Set(openAIWSTurnStateHeader, token)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			server, serverErr := startPassthroughLifecycleServer(t, ctx, svc, account)
			defer server.Close()
			client, _, err := coderws.Dial(ctx, "ws"+server.URL[4:], nil)
			require.NoError(t, err)
			defer client.CloseNow()
			require.NoError(t, client.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.5","input":[]}`)))
			frame := requirePassthroughUpstreamWrite(t, upstream, 3*time.Second)
			require.Equal(t, cached, gjson.GetBytes(frame, "client_metadata.x-codex-turn-state").String())
			require.Empty(t, (<-request).Get(openAIWSTurnStateHeader))
			upstream.Send(`{"type":"response.completed","response":{"id":"resp_enabled_native","model":"gpt-5.5","usage":{"input_tokens":1,"output_tokens":1}}}`)
			readCodexStatePassthroughFrame(t, ctx, client)
			requireCodexEnabledSummary(t, svc.codexTurnStateService, account.ID, "gpt-5.5", token, "header", "business", len(cached))
			_ = client.CloseNow()
			select {
			case <-serverErr:
			case <-ctx.Done():
				t.Fatal("local enabled summary connection did not finish")
			}
		})
	}
}

func TestCodexStateEnabledObservationCollectorOriginFromActualResponse(t *testing.T) {
	for _, tc := range []struct {
		name, carrier, shape string
		blocks               int
		failed               bool
	}{
		{"header_target", "header", "target", 10, false},
		{"metadata_extended", "metadata", "suspect", 11, false},
		{"failed_metadata_extended", "metadata", "suspect", 11, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateCodexTurnStateSummaryStore(t)
			state, repo, account := newCodexStateTestService(t)
			state.now = time.Now
			key := CodexTurnStateKey{OwnerAccountID: account.ID, Model: "gpt-5.4", Generation: CodexTurnStateGenerationForAccount(account)}
			repo.records[key] = CodexTurnStateRecord{OwnerAccountID: key.OwnerAccountID, Model: key.Model, Generation: key.Generation,
				Version: 1, LastBusinessAt: state.now(), DemandReason: "extended_shape", DemandAt: state.now(), CollectionStatus: "pending"}
			token := codexStateTestToken(tc.blocks, state.now().Add(-time.Minute))
			var calls atomic.Int64
			state.collector = NewCodexTurnStateHTTPCollector(func(_ context.Context, input CodexTurnStateCollectRequest, req *http.Request) (*http.Response, error) {
				calls.Add(1)
				require.Equal(t, int64(2), input.ProxyID)
				require.Equal(t, "gpt-5.4", input.Model)
				require.Empty(t, req.Header.Get(openAICodexTurnStateHeader))
				return codexStateHTTPIntegrationResponse(token, tc.carrier, tc.failed), nil
			})
			state.collect(context.Background(), key)
			require.EqualValues(t, 1, calls.Load())
			observation := requireCodexEnabledSummary(t, state, account.ID, "gpt-5.4", token, tc.carrier, "collector", 0)
			require.Equal(t, tc.shape, observation.ResponseShape)
			require.Equal(t, tc.blocks, observation.ResponseCipherBlocks)
			require.Empty(t, state.business)
		})
	}
}
