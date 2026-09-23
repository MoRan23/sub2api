package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openaicookies"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestMappedGPT55SelectsNonLiteBeforeTicketRouteAllHTTPPaths(t *testing.T) {
	for _, path := range []string{"responses", "passthrough", "chat", "messages", "bridge"} {
		t.Run(path, func(t *testing.T) {
			state, repo, account := newCodexStateTestService(t)
			state.modelPolicy = newCodexStateTestModelPolicy("gpt-5.5")
			state.now = time.Now
			seedCodexHTTPRouteBundle(t, state, account, "gpt-5.5", "lite")
			key := CodexTurnStateKey{OwnerAccountID: account.ID, Model: "gpt-5.5", Generation: CodexTurnStateGenerationForAccount(account)}
			before, err := repo.Get(context.Background(), key)
			require.NoError(t, err)
			require.NotNil(t, before)
			if path == "passthrough" {
				account.Extra["openai_passthrough"] = true
			}
			account.Credentials["model_mapping"] = map[string]any{"client-alias": "gpt-5.5"}
			proxies := &codexHTTPRouteProxyRepo{proxy: Proxy{ID: 2, Protocol: "http", Host: "issuer.invalid", Port: 8080, Status: StatusActive, RouteGeneration: 1}}
			upstream := &codexStateCookieIntegrationUpstream{httpUpstreamRecorder: &httpUpstreamRecorder{resp: openAICompatSSECompletedResponse("resp_gpt55", "gpt-5.5")}, manager: openaicookies.NewManager(), account: account}
			gateway := &OpenAIGatewayService{cfg: &config.Config{}, accountRepo: state.accounts, httpUpstream: upstream, codexTurnStateService: state, codexTurnStateProxyRepo: proxies}
			gateway.codexModelCapabilities.observeManifest(openAICodexModelCapabilitiesNamespace(account), []byte(`{"models":[{"slug":"gpt-5.5","use_responses_lite":true}]}`), time.Now())
			body := []byte(strings.ReplaceAll(string(codexStateHTTPIntegrationBody(path, true)), "gpt-5.4", "client-alias"))
			if path == "passthrough" {
				body = []byte(strings.ReplaceAll(string(body), "client-alias", "gpt-5.5"))
			}
			if path == "bridge" {
				// The WS ingress pipeline already maps the model before HTTP bridging.
				body = []byte(`{"type":"response.create","model":"gpt-5.5","input":"hi","client_metadata":{"ws_request_header_x_openai_internal_codex_responses_lite":"true"}}`)
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
				SetOpenAIClientTransport(c, OpenAIClientTransportWS)
				plan := codexWireProjectionTestPlan(t)
				plan.PolicySnapshot = defaultOpenAICodexFingerprintPolicy(0)
				plan.ProjectionMode = OpenAIOAuthIdentityProjectionPassthrough
				plan.CredentialOwnerNamespace = openAIOutboundSessionIdentityNamespace(account)
				plan.CredentialOS = account.OpenAIOAuthCredentialOS
				plan.OSOwnerID = account.OpenAIOAuthCredentialOwnerID
				plan.AuthorizationGeneration = account.OpenAIOAuthAuthorizationGeneration
				_, err = gateway.proxyOpenAIWSHTTPBridgeTurn(context.Background(), c, account, "test-token", body, len(body), "gpt-5.5", "", "", "", "", 1, func([]byte) error { return nil }, &plan)
				require.NoError(t, err)
				require.False(t, plan.WireProfile.ToolNamespacesAllowed)
			} else {
				_, recorder, forwardErr := codexStateHTTPIntegrationForward(t, gateway, account, path, body)
				require.NoError(t, forwardErr, recorder.Body.String())
			}
			require.Len(t, upstream.requests, 1)
			require.Empty(t, upstream.lastProxyURL, "non-Lite uses the original account route")
			require.Empty(t, upstream.lastReq.Header.Get(openAICodexTurnStateHeader))
			require.Empty(t, upstream.lastReq.Header.Get("Cookie"))
			require.Empty(t, upstream.lastReq.Header.Get(responsesLiteHeader))
			require.False(t, isOpenAIResponsesLiteWebSocketPayload(upstream.lastBody))
			require.Equal(t, "gpt-5.5", gjson.GetBytes(upstream.lastBody, "model").String())
			after, err := repo.Get(context.Background(), key)
			require.NoError(t, err)
			require.Equal(t, before.LastEligibleCollectionAt, after.LastEligibleCollectionAt, "non-Lite activity cannot keep the Lite collector alive")
			require.Equal(t, before.EncryptedToken, after.EncryptedToken, "protocol bypass does not discard the other protocol's bundle")
		})
	}
}

type mergeHTTPDeliveryCloseBody struct {
	io.ReadCloser
	closedAfterDelivery bool
	recorder            *httptest.ResponseRecorder
}

func (b *mergeHTTPDeliveryCloseBody) Close() error {
	b.closedAfterDelivery = strings.Contains(b.recorder.Body.String(), `"type":"response.completed"`)
	return b.ReadCloser.Close()
}

func TestOpenAITerminalEarlyCloseCommitsTicketAfterDelivery(t *testing.T) {
	state, repo, account := newCodexStateTestService(t)
	state.now = time.Now
	token := codexStateTestToken(10, state.now())
	response := openAICompatSSECompletedResponse("resp_terminal_close", "gpt-5.4")
	terminal, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	reader := &hangingOpenAISSEAfterTerminal{payload: append(terminal, '\n'), release: make(chan struct{})}
	t.Cleanup(func() { _ = reader.Close() })
	recorder := httptest.NewRecorder()
	body := &mergeHTTPDeliveryCloseBody{ReadCloser: reader, recorder: recorder}
	response.Body = body
	response.Header.Set(openAICodexTurnStateHeader, token)
	upstream := &codexStateCookieIntegrationUpstream{httpUpstreamRecorder: &httpUpstreamRecorder{resp: response}, manager: openaicookies.NewManager(), account: account}
	gateway := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{StreamKeepaliveInterval: 1, StreamDataIntervalTimeout: 30}}, accountRepo: state.accounts, httpUpstream: upstream, codexTurnStateService: state}
	c, _ := gin.CreateTestContext(recorder)
	requestBody := codexStateHTTPIntegrationBody("responses", true)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(string(requestBody)))
	done := make(chan error, 1)
	go func() {
		_, forwardErr := gateway.Forward(context.Background(), c, account, requestBody)
		done <- forwardErr
	}()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		_ = reader.Close()
		<-done
		t.Fatal("terminal delivery waited for upstream EOF")
	}
	require.True(t, body.closedAfterDelivery, "observers must settle after the terminal has been flushed")
	row, err := repo.Get(context.Background(), CodexTurnStateKey{OwnerAccountID: account.ID, Model: "gpt-5.4", Generation: CodexTurnStateGenerationForAccount(account)})
	require.NoError(t, err)
	require.NotNil(t, row)
	require.NotEmpty(t, row.EncryptedToken, "the delivered terminal can publish its target ticket")
}

func TestOpenAIPassthroughFreezesTheActualWireModel(t *testing.T) {
	state, _, account := newCodexStateTestService(t)
	state.now = time.Now
	state.modelPolicy = newCodexStateTestModelPolicy("gpt-6-sol", "gpt-5.5")
	account.Extra["openai_passthrough"] = true
	account.Credentials["model_mapping"] = map[string]any{"gpt-6-sol": "gpt-5.5"}
	token := seedCodexHTTPRouteBundle(t, state, account, "gpt-6-sol", "lite")
	proxies := &codexHTTPRouteProxyRepo{proxy: Proxy{ID: 2, Protocol: "http", Host: "issuer.invalid", Port: 8080, Status: StatusActive, RouteGeneration: 1}}
	upstream := &codexStateCookieIntegrationUpstream{httpUpstreamRecorder: &httpUpstreamRecorder{resp: openAICompatSSECompletedResponse("resp_passthrough_wire", "gpt-6-sol")}, manager: openaicookies.NewManager(), account: account}
	gateway := &OpenAIGatewayService{cfg: &config.Config{}, accountRepo: state.accounts, httpUpstream: upstream, codexTurnStateService: state, codexTurnStateProxyRepo: proxies}
	gateway.codexModelCapabilities.observeManifest(openAICodexModelCapabilitiesNamespace(account), []byte(`{"models":[{"slug":"gpt-6-sol","use_responses_lite":true}]}`), time.Now())
	body := []byte(`{"model":"gpt-6-sol","instructions":"Reply briefly.","input":"hi","reasoning":{"mode":"pro","effort":"max"},"temperature":0.4,"stream":true}`)
	_, recorder, err := codexStateHTTPIntegrationForward(t, gateway, account, "passthrough", body)
	require.NoError(t, err, recorder.Body.String())
	require.Len(t, upstream.requests, 1)
	require.Equal(t, "gpt-6-sol", gjson.GetBytes(upstream.lastBody, "model").String())
	require.Equal(t, "pro", gjson.GetBytes(upstream.lastBody, "reasoning.mode").String())
	require.False(t, gjson.GetBytes(upstream.lastBody, "temperature").Exists())
	require.Equal(t, token, upstream.lastReq.Header.Get(openAICodexTurnStateHeader))
	require.Equal(t, "true", upstream.lastReq.Header.Get(responsesLiteHeader))
	require.Equal(t, proxies.proxy.URL(), upstream.lastProxyURL)
}

func TestOpenAITicketModelEvidencePrecedesPublicModelRewrite(t *testing.T) {
	state, repo, account := newCodexStateTestService(t)
	state.now = time.Now
	account.Credentials["model_mapping"] = map[string]any{"public": "gpt-5.4"}
	token := codexStateTestToken(10, state.now())
	response := openAICompatSSECompletedResponse("resp_model_evidence", "gpt-5-mini")
	response.Header.Set(openAICodexTurnStateHeader, token)
	upstream := &codexStateCookieIntegrationUpstream{httpUpstreamRecorder: &httpUpstreamRecorder{resp: response}, manager: openaicookies.NewManager(), account: account}
	gateway := &OpenAIGatewayService{cfg: &config.Config{}, accountRepo: state.accounts, httpUpstream: upstream, codexTurnStateService: state}
	body := []byte(strings.ReplaceAll(string(codexStateHTTPIntegrationBody("responses", true)), "gpt-5.4", "public"))
	result, recorder, err := codexStateHTTPIntegrationForward(t, gateway, account, "responses", body)
	require.NoError(t, err, recorder.Body.String())
	require.Equal(t, "gpt-5-mini", result.UpstreamResponseModel)
	require.Contains(t, recorder.Body.String(), `"model":"public"`)
	require.NotContains(t, recorder.Body.String(), `"model":"gpt-5-mini"`)
	require.Len(t, upstream.requests, 1, "model disagreement never replays a delivered business response")
	row, err := repo.Get(context.Background(), CodexTurnStateKey{OwnerAccountID: account.ID, Model: "gpt-5.4", Generation: CodexTurnStateGenerationForAccount(account)})
	require.NoError(t, err)
	require.NotNil(t, row)
	require.NotEmpty(t, row.EncryptedToken, "natural learning retains its existing admission policy")
	evidence := decryptCodexCookieAtomicTestEnvelope(t, state, *row).ResponseEvidence
	require.Equal(t, "gpt-5-mini", evidence.UpstreamResponseModel)
	require.Equal(t, "different", evidence.ModelRelation)
}
