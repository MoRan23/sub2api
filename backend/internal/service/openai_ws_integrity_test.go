package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type integrityStagedWSConn struct{ *stagedPassthroughConn }

type integrityWSDialer struct{ traffic *stagedPassthroughConn }

func (dialer *integrityWSDialer) Dial(context.Context, string, http.Header, string) (openAIWSClientConn, int, http.Header, error) {
	conn := newStagedPassthroughConn()
	conn.frames, conn.writes = dialer.traffic.frames, dialer.traffic.writes
	return &integrityStagedWSConn{conn}, http.StatusSwitchingProtocols, nil, nil
}

func (conn *integrityStagedWSConn) WriteJSON(ctx context.Context, value any) error {
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return conn.WriteFrame(ctx, coderws.MessageText, body)
}

func TestOpenAIWSIntegrityCapturesEachClientTurnBeforeAdaptation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	originalObservation := IsFingerprintObservationEnabled()
	t.Cleanup(func() { SetFingerprintObservationEnabled(originalObservation) })
	for _, mode := range []string{OpenAIWSIngressModeCtxPool, OpenAIWSIngressModePassthrough} {
		for _, daily := range []bool{false, true} {
			for _, collect := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/daily=%t/collect=%t", mode, daily, collect), func(t *testing.T) {
					SetFingerprintObservationEnabled(false)
					SetFingerprintObservationEnabled(collect)
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()
					staged := newStagedPassthroughConn()
					cfg := passthroughLifecycleConfig()
					cfg.JWT.Secret = "integrity-local-ws-test"
					cfg.Gateway.OpenAIWS.OAuthEnabled = true
					cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
					cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
					cfg.Gateway.OpenAIWS.PoolTargetUtilization = 1
					svc := newPassthroughLifecycleService(cfg, staged)
					dialer := &integrityWSDialer{traffic: staged}
					svc.openaiWSPassthroughDialer = dialer
					svc.openaiWSPool = newOpenAIWSConnPool(cfg)
					svc.openaiWSPool.setClientDialerForTest(dialer)
					defer svc.openaiWSPool.Close()
					if daily {
						configureCodexTelemetryWSDailyFixture(svc)
					} else {
						svc.settingService = NewSettingService(&dailyRotationSettingRepo{values: map[string]string{
							SettingKeyEnableOpenAICodexFingerprintNormalization: "true",
							SettingKeyEnableOpenAIUUIDv7SessionIdentity:         "true",
							SettingKeyEnableOpenAIOAuthDailySessionRotation:     "false",
						}}, nil)
					}
					svc.settingService.publishOpenAIRequestIntegrityObserveEnabled("true")
					account := &Account{ID: 531, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
						Status: StatusActive, Schedulable: true, Concurrency: 1,
						Credentials: map[string]any{"access_token": "synthetic-token", "model_mapping": map[string]any{"client-model": "gpt-5.4"}},
						Extra: map[string]any{"responses_websockets_v2_enabled": true, "openai_oauth_responses_websockets_v2_mode": mode,
							openAIPinnedInstallationIDKey: transportTestPinnedInstallationID},
					}
					svc.accountRepo = newAuthorizedOpenAIOAuthTestRepo(account)
					contexts := make(chan *gin.Context, 1)
					server, done := startPassthroughLifecycleServerWithHooks(t, ctx, svc, account, func(c *gin.Context) *OpenAIWSIngressHooks {
						c.Set("api_key", &APIKey{ID: 98})
						setOpenAIClientRequestedStream(c, true)
						contexts <- c
						return &OpenAIWSIngressHooks{MapRequestModel: func(int, string) (string, error) { return "gpt-5.4", nil }}
					})
					defer server.Close()
					client, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses", nil)
					require.NoError(t, err)
					defer client.CloseNow()
					var requestContext *gin.Context
					var previousState *OpenAIRequestIntegrityState
					for turn := 1; turn <= 2; turn++ {
						baseline := []byte(fmt.Sprintf(`{"type":"response.create","model":"client-model","stream":true,"instructions":"instruction","input":[{"role":"user","content":[{"type":"input_text","text":"turn %d"}]}]}`, turn))
						require.NoError(t, client.Write(ctx, coderws.MessageText, baseline))
						wire := requirePassthroughUpstreamWrite(t, staged, 3*time.Second)
						if requestContext == nil {
							requestContext = <-contexts
						}
						capture := openAIIntegrityCaptureFromContext(requestContext)
						require.NotNil(t, capture)
						capture.mu.Lock()
						state, expectedModel := capture.state, capture.model
						capture.mu.Unlock()
						require.NotNil(t, state)
						require.NotSame(t, previousState, state, "each accepted turn resets the frozen baseline")
						require.JSONEq(t, string(baseline), string(state.baseline), "baseline is the client frame, before model/identity adaptation")
						require.Equal(t, "gpt-5.4", expectedModel)
						require.Equal(t, int64(1), state.attempt.Load(), "fingerprint collection must not control integrity checks")
						require.Equal(t, "gpt-5.4", gjson.GetBytes(wire, "model").String())
						require.Equal(t, fmt.Sprintf("turn %d", turn), gjson.GetBytes(wire, "input.0.content.0.text").String())
						previousState = state
						staged.Send(fmt.Sprintf(`{"type":"response.completed","response":{"id":"resp_integrity_%d","status":"completed","model":"gpt-5.4","usage":{"input_tokens":1,"output_tokens":1}}}`, turn))
						_, err := readPassthroughLifecycleFrame(t, client, 3*time.Second)
						require.NoError(t, err)
					}
					var frames []FingerprintObservationEntry
					for _, entry := range SnapshotFingerprintObservations(20) {
						if entry.AccountID == account.ID && entry.EventKind == FingerprintObservationEventWSFrame {
							frames = append(frames, entry)
						}
					}
					if collect {
						require.Len(t, frames, 2)
						for _, entry := range frames {
							require.NotNil(t, entry.RequestIntegrity)
							require.Equal(t, int64(1), entry.RequestIntegrity.Attempt)
							require.Equal(t, "ingress", entry.RequestIntegrity.BaselineStage)
							require.Equal(t, "expected_transform", entry.RequestIntegrity.Status)
							require.Contains(t, entry.RequestIntegrity.RuleCodes, "account_model_mapping")
						}
					} else {
						require.Empty(t, frames)
					}
					require.NoError(t, client.Close(coderws.StatusNormalClosure, "done"))
					select {
					case <-done:
					case <-ctx.Done():
						t.Fatal("local WS gateway did not exit")
					}
				})
			}
		}
	}
}

func TestOpenAIWSHTTPBridgeIntegrityKeepsClientBaselineAcrossReplay(t *testing.T) {
	gin.SetMode(gin.TestMode)
	originalObservation := IsFingerprintObservationEnabled()
	SetFingerprintObservationEnabled(false)
	SetFingerprintObservationEnabled(true)
	defer SetFingerprintObservationEnabled(originalObservation)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cfg := passthroughLifecycleConfig()
	cfg.JWT.Secret = "integrity-local-bridge-test"
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	upstream := &httpUpstreamRecorder{}
	for turn := 1; turn <= 2; turn++ {
		completed := fmt.Sprintf("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_integrity_bridge_%d\",\"status\":\"completed\",\"model\":\"gpt-5.4\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n", turn)
		upstream.responses = append(upstream.responses, &http.Response{StatusCode: http.StatusOK,
			Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(completed))})
	}
	svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: upstream, cache: &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg), toolCorrector: NewCodexToolCorrector()}
	account := &Account{ID: 539, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Status: StatusActive, Schedulable: true, Concurrency: 1,
		Credentials: map[string]any{"access_token": "synthetic-token"},
		Extra: map[string]any{"responses_websockets_v2_enabled": true,
			"openai_oauth_responses_websockets_v2_mode": OpenAIWSIngressModeHTTPBridge,
			openAIPinnedInstallationIDKey:               transportTestPinnedInstallationID},
	}
	svc.accountRepo = newAuthorizedOpenAIOAuthTestRepo(account)
	contexts := make(chan *gin.Context, 1)
	server, done := startPassthroughLifecycleServerWithHooks(t, ctx, svc, account, func(c *gin.Context) *OpenAIWSIngressHooks {
		contexts <- c
		return nil
	})
	defer server.Close()
	client, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses", nil)
	require.NoError(t, err)
	defer client.CloseNow()
	baselines := []string{
		`{"type":"response.create","model":"gpt-5.4","instructions":"instruction","input":[{"role":"user","content":"first"}]}`,
		`{"type":"response.create","model":"gpt-5.4","instructions":"instruction","previous_response_id":"resp_integrity_bridge_1","input":[{"role":"user","content":"second"}]}`,
	}
	var requestContext *gin.Context
	for _, baseline := range baselines {
		require.NoError(t, client.Write(ctx, coderws.MessageText, []byte(baseline)))
		response, err := readPassthroughLifecycleFrame(t, client, 3*time.Second)
		require.NoError(t, err)
		require.Equal(t, "response.completed", gjson.GetBytes(response, "type").String())
		if requestContext == nil {
			requestContext = <-contexts
		}
		capture := openAIIntegrityCaptureFromContext(requestContext)
		require.NotNil(t, capture)
		capture.mu.Lock()
		state := capture.state
		capture.mu.Unlock()
		require.JSONEq(t, baseline, string(state.baseline), "HTTP bridge must retain the original WS frame")
		require.Equal(t, int64(1), state.attempt.Load())
	}
	require.NoError(t, client.Close(coderws.StatusNormalClosure, "done"))
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("local bridge did not exit")
	}
	require.Len(t, upstream.bodies, 2)
	require.Len(t, gjson.GetBytes(upstream.bodies[1], "input").Array(), 2, "ordinary continuation replay still occurs")
	var replay *RequestIntegrityObservation
	for _, entry := range SnapshotFingerprintObservations(10) {
		if entry.AccountID == account.ID && entry.RequestIntegrity != nil && entry.RequestIntegrity.Status == "difference" {
			replay = entry.RequestIntegrity
			break
		}
	}
	require.NotNil(t, replay, "known recovery remains observable as a content difference")
	require.Contains(t, replay.RuleCodes, "continuation_recovery")
	require.Equal(t, "ingress", replay.BaselineStage)
}
