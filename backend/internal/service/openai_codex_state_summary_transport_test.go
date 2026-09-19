package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func requireCodexSummaryWithBothSwitchesOff(t *testing.T, state *CodexTurnStateService, accountID int64, model, token, source string, outboundLength int) {
	t.Helper()
	require.False(t, IsFingerprintObservationEnabled())
	// The WS capture repositories intentionally implement no status reads. Use
	// the same account resolver with a read-only status repository; any runtime
	// mutation through this reader would panic via its embedded nil interface.
	reader := NewCodexTurnStateService(&codexStateBatchRecords{}, state.accounts, nil, nil)
	reader.modelPolicy = &codexStateBatchPolicy{models: []string{model}}
	var status *CodexTurnStateStatus
	require.Eventually(t, func() bool {
		var err error
		status, err = reader.GetStatus(context.Background(), accountID)
		return err == nil && len(status.Observations) == 1
	}, time.Second, time.Millisecond)
	require.False(t, status.Enabled)
	require.True(t, status.ObservationEnabled)
	require.Equal(t, "instance", status.ObservationScope)
	require.Empty(t, status.Models)
	observation := status.Observations[0]
	require.Equal(t, model, observation.Model)
	require.Equal(t, len(token), observation.ResponseLength)
	require.Equal(t, "target", observation.ResponseShape)
	require.Equal(t, CodexTurnStateObservedPersonalTarget, observation.ResponseObservedShape)
	require.Equal(t, 10, observation.ResponseCipherBlocks)
	require.Equal(t, source, observation.ResponseSource)
	require.Equal(t, outboundLength, observation.OutboundLength)
	require.False(t, observation.ObservedAt.IsZero())
	require.Empty(t, SnapshotFingerprintObservations(0), "account summaries must not enable full fingerprint collection")
	encoded, err := json.Marshal(status)
	require.NoError(t, err)
	for _, secret := range []string{token, "access_token", "encrypted_token", "test-access", "private-client-frame"} {
		require.NotContains(t, string(encoded), secret)
	}
	requirePassiveWSNoMaintenance(t, state)
}

func TestCodexTurnStateSummaryHTTPAllPathsWithBothSwitchesOff(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, path := range []string{"responses", "passthrough", "chat", "messages"} {
		for _, stream := range []bool{false, true} {
			t.Run(path+"/stream="+strconv.FormatBool(stream), func(t *testing.T) {
				isolateCodexTurnStateSummaryStore(t)
				state, repo, account := newCodexStateTestService(t)
				account.Extra[CodexTurnStateExtraKey] = CodexTurnStateConfig{AccountType: "personal"}
				account.Concurrency = 1
				if path == "passthrough" {
					account.Extra["openai_passthrough"] = true
				}
				token := codexStateTestToken(10, state.now())
				upstream := &httpUpstreamRecorder{resp: codexStateHTTPIntegrationResponse(token, "metadata", false)}
				gateway := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream, codexTurnStateService: state}
				result, recorder, err := codexStateHTTPIntegrationForward(t, gateway, account, path, codexStateHTTPIntegrationBody(path, stream))
				require.NoError(t, err, recorder.Body.String())
				require.NotNil(t, result)
				require.Len(t, upstream.requests, 1)
				require.Equal(t, "gpt-5.4", gjson.GetBytes(upstream.lastBody, "model").String())
				require.Empty(t, upstream.lastReq.Header.Get(openAICodexTurnStateHeader))
				require.False(t, gjson.GetBytes(upstream.lastBody, "client_metadata.x-codex-turn-state").Exists())
				requireCodexSummaryWithBothSwitchesOff(t, state, account.ID, "gpt-5.4", token, "metadata", 0)
				repo.mu.Lock()
				defer repo.mu.Unlock()
				require.Empty(t, repo.records)
				require.Empty(t, repo.leases)
			})
		}
	}
}

func TestCodexTurnStateSummaryWSBridgeWithBothSwitchesOff(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			isolateCodexTurnStateSummaryStore(t)
			svc, account, repo, accounts := newCodexWSStateTestGateway(t, "personal")
			account.Extra[CodexTurnStateExtraKey] = CodexTurnStateConfig{AccountType: "personal"}
			accounts.update(func(a *Account) { a.Extra[CodexTurnStateExtraKey] = account.Extra[CodexTurnStateExtraKey] })
			cfg := newOpenAIWSV2TestConfig()
			cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
			cfg.Gateway.OpenAIWS.OAuthEnabled = true
			svc.cfg, svc.httpUpstream = cfg, &httpUpstreamRecorder{}
			svc.openaiWSResolver = NewOpenAIWSProtocolResolver(cfg)
			token := makeCodexWSStateTestToken(10, time.Now().Add(-time.Minute))
			conn := &openAIWSCaptureConn{events: [][]byte{
				[]byte(fmt.Sprintf(`{"type":"response.metadata","headers":{"x-codex-turn-state":%q}}`, token)),
				[]byte(`{"type":"response.completed","response":{"id":"resp_summary_bridge","model":"gpt-5.1","status":"completed","usage":{"input_tokens":1,"output_tokens":1}}}`),
			}}
			dialer := &openAIWSCaptureDialer{conn: conn}
			svc.openaiWSPool = newOpenAIWSConnPool(cfg)
			svc.openaiWSPool.setClientDialerForTest(dialer)
			defer svc.openaiWSPool.Close()
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader("{}"))
			var recovered bool
			result, err := svc.forwardOpenAIWSV2(context.Background(), c, account, map[string]any{"model": "gpt-5.1", "stream": stream, "input": "hi", "client_metadata": map[string]any{openAICodexTurnStateHeader: "private-client-frame"}}, "", "", "test-access", OpenAIWSProtocolDecision{Transport: OpenAIUpstreamTransportResponsesWebsocketV2}, false, stream, "gpt-5.1", "gpt-5.1", time.Now(), 0, "", &recovered)
			require.NoError(t, err)
			require.NotNil(t, result)
			conn.mu.Lock()
			frame, err := json.Marshal(conn.lastWrite)
			conn.mu.Unlock()
			require.NoError(t, err)
			require.Equal(t, "private-client-frame", gjson.GetBytes(frame, "client_metadata.x-codex-turn-state").String())
			requireCodexSummaryWithBothSwitchesOff(t, svc.codexTurnStateService, account.ID, "gpt-5.1", token, "metadata", len("private-client-frame"))
			repo.mu.Lock()
			defer repo.mu.Unlock()
			require.Empty(t, repo.records)
			require.Empty(t, repo.active)
		})
	}
}

func TestCodexTurnStateSummaryNativeWSWithBothSwitchesOff(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		t.Run(fmt.Sprintf("passthrough_%t", passthrough), func(t *testing.T) {
			isolateCodexTurnStateSummaryStore(t)
			var svc *OpenAIGatewayService
			var account *Account
			var upstream *stagedPassthroughConn
			var request chan http.Header
			var response http.Header
			var assertNoWrites func()
			if passthrough {
				var repo *codexStatePassthroughRepository
				var dialer *codexStatePassthroughDialer
				svc, account, _, repo, dialer = newCodexStatePassthroughHarness(t, false)
				upstream, request, response = dialer.conn, dialer.request, dialer.headers
				assertNoWrites = func() {
					repo.mu.Lock()
					defer repo.mu.Unlock()
					require.Empty(t, repo.records)
					require.Zero(t, repo.ended)
				}
			} else {
				var repo *codexWSStateTestRepo
				var accounts *codexWSStateTestAccounts
				var dialer *codexWSStatePooledDialer
				svc, account, repo, accounts, dialer = newCodexWSStateIngressHarness(t)
				account.Extra[CodexTurnStateExtraKey] = CodexTurnStateConfig{AccountType: "personal"}
				accounts.update(func(a *Account) { a.Extra[CodexTurnStateExtraKey] = account.Extra[CodexTurnStateExtraKey] })
				upstream, request, response = dialer.conn.stagedPassthroughConn, dialer.request, dialer.response
				assertNoWrites = func() {
					repo.mu.Lock()
					defer repo.mu.Unlock()
					require.Empty(t, repo.records)
					require.Empty(t, repo.active)
				}
			}
			token := makeCodexWSStateTestToken(10, time.Now().Add(-time.Minute))
			response.Set(openAIWSTurnStateHeader, token)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			server, serverErr := startPassthroughLifecycleServer(t, ctx, svc, account)
			defer server.Close()
			client, _, err := coderws.Dial(ctx, "ws"+server.URL[4:], &coderws.DialOptions{HTTPHeader: http.Header{"X-Codex-Turn-State": {"private-client-frame"}}})
			require.NoError(t, err)
			defer client.CloseNow()
			require.NoError(t, client.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.5","input":[]}`)))
			frame := requirePassthroughUpstreamWrite(t, upstream, 3*time.Second)
			require.False(t, gjson.GetBytes(frame, "client_metadata.x-codex-turn-state").Exists())
			require.Equal(t, "private-client-frame", (<-request).Get(openAIWSTurnStateHeader))
			upstream.Send(`{"type":"response.completed","response":{"id":"resp_summary_native","model":"gpt-5.5","usage":{"input_tokens":1,"output_tokens":1}}}`)
			readCodexStatePassthroughFrame(t, ctx, client)
			requireCodexSummaryWithBothSwitchesOff(t, svc.codexTurnStateService, account.ID, "gpt-5.5", token, "header", len("private-client-frame"))
			_ = client.CloseNow()
			select {
			case <-serverErr:
			case <-ctx.Done():
				t.Fatal("local summary connection did not finish")
			}
			assertNoWrites()
			require.Empty(t, request)
			requirePassiveWSNoMaintenance(t, svc.codexTurnStateService)
		})
	}
}
