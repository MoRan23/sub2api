package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func enablePassiveWSFingerprintObservation(t *testing.T) {
	t.Helper()
	SetFingerprintObservationEnabled(false)
	SetFingerprintObservationEnabled(true)
	t.Cleanup(func() { SetFingerprintObservationEnabled(false) })
}

func passiveWSStateRows(accountID int64) []FingerprintObservationEntry {
	var rows []FingerprintObservationEntry
	for _, row := range SnapshotFingerprintObservations(0) {
		if row.AccountID == accountID && row.EventKind == FingerprintObservationEventWSFrame && row.CodexTurnState != nil {
			rows = append(rows, row)
		}
	}
	return rows
}

func requireNativeWSNoStateObservation(t *testing.T, accountID int64) {
	t.Helper()
	require.Empty(t, passiveWSStateRows(accountID), "native WS must not create ticket-specific fingerprint observations")
}

func requirePassiveWSNoMaintenance(t *testing.T, service *CodexTurnStateService) {
	t.Helper()
	service.mu.Lock()
	defer service.mu.Unlock()
	require.Empty(t, service.business)
	require.Empty(t, service.queued)
	require.Empty(t, service.queue)
	require.Empty(t, service.running)
}

func TestCodexStatePassiveWSHandshakeObservationUsesActualSendOnce(t *testing.T) {
	fallback := http.Header{"X-Codex-Turn-State": {"stale-fallback-token"}}
	physical := &coderOpenAIWSClientConn{codexStateOutboundHeaderLength: 292}
	require.Equal(t, 292, openAIWSCodexStateOutboundHeaderLength(physical, fallback))
	physical.codexStateOutboundHeaderLength = 0
	require.Zero(t, openAIWSCodexStateOutboundHeaderLength(physical, fallback), "an absent actual header must not be replaced by a stale fallback")
	require.Equal(t, len("stale-fallback-token"), openAIWSCodexStateOutboundHeaderLength(&openAIWSCaptureConn{}, fallback))
	response := http.Header{"X-Codex-Turn-State": {"first-model-response"}}
	conn := newOpenAIWSConn("passive-prewarm", 1, physical, response)
	conn.codexStateOutboundHeaderLength = 292
	prewarm := &openAIWSConnLease{conn: conn}
	require.Equal(t, "first-model-response", prewarm.ClaimCodexStateHandshakeHeaders().Get(openAIWSTurnStateHeader))
	reused := &openAIWSConnLease{conn: conn}
	claimed, length := reused.ClaimCodexStateHandshakeObservation()
	require.Nil(t, claimed)
	require.Zero(t, length, "a prewarm consuming the response candidate must also consume the outbound handshake summary")
}

func TestCodexStatePassiveHTTPToWSBridgeBypassesTurnState(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, source := range []string{"header", "metadata"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s_stream_%t", source, stream), func(t *testing.T) {
				enablePassiveWSFingerprintObservation(t)
				svc, account, repo, accounts := newCodexWSStateTestGateway(t, "personal")
				account.Extra[CodexTurnStateExtraKey] = CodexTurnStateConfig{AccountType: "personal"}
				accounts.update(func(a *Account) { a.Extra[CodexTurnStateExtraKey] = account.Extra[CodexTurnStateExtraKey] })
				cfg := newOpenAIWSV2TestConfig()
				cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
				cfg.Gateway.OpenAIWS.OAuthEnabled = true
				svc.cfg, svc.httpUpstream = cfg, &httpUpstreamRecorder{}
				svc.openaiWSResolver = NewOpenAIWSProtocolResolver(cfg)
				token := makeCodexWSStateTestToken(10, time.Now().Add(-time.Minute))
				conn := &openAIWSCaptureConn{}
				dialer := &openAIWSCaptureDialer{conn: conn, handshake: make(http.Header)}
				if source == "header" {
					dialer.handshake.Set(openAIWSTurnStateHeader, token)
				} else {
					conn.events = append(conn.events, []byte(fmt.Sprintf(`{"type":"response.metadata","headers":{"X-CoDeX-TuRn-StAtE":%q}}`, token)))
				}
				conn.events = append(conn.events, []byte(`{"type":"response.completed","response":{"id":"resp_passive_bridge","model":"gpt-5.1","status":"completed","usage":{"input_tokens":1,"output_tokens":1}}}`))
				svc.openaiWSPool = newOpenAIWSConnPool(cfg)
				svc.openaiWSPool.setClientDialerForTest(dialer)
				defer svc.openaiWSPool.Close()
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader("{}"))
				c.Request.Header.Set(openAIWSTurnStateHeader, "guarded-client-header")
				var recovered bool
				result, err := svc.forwardOpenAIWSV2(context.Background(), c, account, map[string]any{"model": "gpt-5.1", "stream": stream, "input": "hi", "client_metadata": map[string]any{openAICodexTurnStateHeader: "guarded-client-frame"}}, "", "", "test-access", OpenAIWSProtocolDecision{Transport: OpenAIUpstreamTransportResponsesWebsocketV2}, false, stream, "gpt-5.1", "gpt-5.1", time.Now(), 0, "", &recovered)
				require.NoError(t, err)
				require.NotNil(t, result)
				conn.mu.Lock()
				frame, err := json.Marshal(conn.lastWrite)
				conn.mu.Unlock()
				require.NoError(t, err)
				require.Equal(t, "guarded-client-frame", gjson.GetBytes(frame, "client_metadata.x-codex-turn-state").String())
				dialer.mu.Lock()
				handshakeState := dialer.lastHeaders.Get(openAIWSTurnStateHeader)
				dialer.mu.Unlock()
				require.Equal(t, "guarded-client-header", handshakeState, "disabled caching must preserve the existing handshake carrier")
				requireNativeWSNoStateObservation(t, account.ID)
				repo.mu.Lock()
				recordCount, activeCount := len(repo.records), len(repo.active)
				repo.mu.Unlock()
				require.Zero(t, recordCount)
				require.Zero(t, activeCount)
				requirePassiveWSNoMaintenance(t, svc.codexTurnStateService)
			})
		}
	}
}

func TestCodexStatePassiveWSIngressPreservesModelsAndBypassesResponseState(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		t.Run(fmt.Sprintf("passthrough_%t", passthrough), func(t *testing.T) {
			enablePassiveWSFingerprintObservation(t)
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
					requireCodexStatePassthroughUntouched(t, repo, nil)
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
			firstToken := makeCodexWSStateTestToken(10, time.Now().Add(-time.Minute))
			secondToken := makeCodexWSStateTestToken(11, time.Now().Add(-time.Minute))
			response.Set(openAIWSTurnStateHeader, firstToken)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			server, serverErr := startPassthroughLifecycleServer(t, ctx, svc, account)
			defer server.Close()
			client, _, err := coderws.Dial(ctx, "ws"+server.URL[4:], &coderws.DialOptions{HTTPHeader: http.Header{"X-Codex-Turn-State": {"client-header"}}})
			require.NoError(t, err)
			defer client.CloseNow()
			require.NoError(t, client.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.5","input":[]}`)))
			first := requirePassthroughUpstreamWrite(t, upstream, 3*time.Second)
			require.False(t, gjson.GetBytes(first, "client_metadata.x-codex-turn-state").Exists(), "passive observation must not migrate a handshake value into the frame")
			require.Equal(t, "client-header", (<-request).Get(openAIWSTurnStateHeader))
			upstream.Send(`{"type":"response.completed","response":{"id":"resp_passive_first","model":"gpt-5.5","usage":{"input_tokens":1,"output_tokens":1}}}`)
			readCodexStatePassthroughFrame(t, ctx, client)
			require.NoError(t, client.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.4","input":[],"client_metadata":{"x-codex-turn-state":"second-frame"}}`)))
			second := requirePassthroughUpstreamWrite(t, upstream, 3*time.Second)
			require.Equal(t, "second-frame", gjson.GetBytes(second, "client_metadata.x-codex-turn-state").String())
			upstream.Send(fmt.Sprintf(`{"type":"response.metadata","headers":{"x-codex-turn-state":%q}}`, secondToken))
			readCodexStatePassthroughFrame(t, ctx, client)
			upstream.Send(`{"type":"response.completed","response":{"id":"resp_passive_second","model":"gpt-5.4","usage":{"input_tokens":1,"output_tokens":1}}}`)
			readCodexStatePassthroughFrame(t, ctx, client)
			require.NoError(t, client.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.3","input":[]}`)))
			third := requirePassthroughUpstreamWrite(t, upstream, 3*time.Second)
			require.False(t, gjson.GetBytes(third, "client_metadata.x-codex-turn-state").Exists())
			thirdFinalModel := "gpt-5.3-codex"
			if passthrough {
				thirdFinalModel = "gpt-5.3"
			}
			require.Equal(t, thirdFinalModel, gjson.GetBytes(third, "model").String())
			upstream.Send(fmt.Sprintf(`{"type":"response.completed","response":{"id":"resp_passive_third","model":%q,"usage":{"input_tokens":1,"output_tokens":1}}}`, thirdFinalModel))
			readCodexStatePassthroughFrame(t, ctx, client)
			_ = client.CloseNow()
			select {
			case <-serverErr:
			case <-ctx.Done():
				t.Fatal("gateway did not finish the local observed connection")
			}
			requireNativeWSNoStateObservation(t, account.ID)
			require.Empty(t, request, "all observed models must share the original physical socket")
			assertNoWrites()
			requirePassiveWSNoMaintenance(t, svc.codexTurnStateService)
		})
	}
}
