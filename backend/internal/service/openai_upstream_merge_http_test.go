package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func mergeHTTPAccount() *Account {
	proxy := &Proxy{ID: 7, Protocol: "http", Host: "account.invalid", Port: 8080, Status: StatusActive}
	return &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1,
		Credentials: map[string]any{"access_token": "local-test-token", "plan_type": "plus"}, Extra: map[string]any{}, ProxyID: &proxy.ID, Proxy: proxy}
}

func mergeHTTPBody(path string, stream bool) []byte {
	if path == "chat" || path == "messages" {
		return []byte(`{"model":"gpt-5.4","max_tokens":32,"messages":[{"role":"user","content":"hello"}],"stream":` + strconv.FormatBool(stream) + `}`)
	}
	return []byte(`{"model":"gpt-5.4","instructions":"Reply briefly.","input":[{"role":"user","content":"hello"}],"stream":` + strconv.FormatBool(stream) + `}`)
}

func mergeHTTPForward(t *testing.T, svc *OpenAIGatewayService, account *Account, path string, body []byte, headers http.Header) (*OpenAIForwardResult, *httptest.ResponseRecorder, error) {
	t.Helper()
	url := "/v1/responses"
	if path == "chat" {
		url = "/v1/chat/completions"
	}
	if path == "messages" {
		url = "/v1/messages"
	}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	for key, values := range headers {
		c.Request.Header[key] = append([]string(nil), values...)
	}
	c.Request.Header.Set("Content-Type", "application/json")
	var result *OpenAIForwardResult
	var err error
	switch path {
	case "chat":
		result, err = svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "")
	case "messages":
		result, err = svc.ForwardAsAnthropic(context.Background(), c, account, body, "", "")
	default:
		result, err = svc.Forward(context.Background(), c, account, body)
	}
	return result, recorder, err
}

func TestMappedGPT55FreezesNonLiteAllHTTPPaths(t *testing.T) {
	for _, path := range []string{"responses", "passthrough", "chat", "messages", "bridge"} {
		t.Run(path, func(t *testing.T) {
			account := mergeHTTPAccount()
			if path == "passthrough" {
				account.Extra["openai_passthrough"] = true
			}
			account.Credentials["model_mapping"] = map[string]any{"client-alias": "gpt-5.5"}
			upstream := &httpUpstreamRecorder{resp: openAICompatSSECompletedResponse("resp_gpt55", "gpt-5.5")}
			gateway := &OpenAIGatewayService{cfg: &config.Config{}, accountRepo: newAuthorizedOpenAIOAuthTestRepo(account), httpUpstream: upstream}
			gateway.codexModelCapabilities.observeManifest(openAICodexModelCapabilitiesNamespace(account), []byte(`{"models":[{"slug":"gpt-5.5","use_responses_lite":true}]}`), time.Now())
			body := []byte(strings.ReplaceAll(string(mergeHTTPBody(path, true)), "gpt-5.4", "client-alias"))
			if path == "passthrough" {
				body = []byte(strings.ReplaceAll(string(body), "client-alias", "gpt-5.5"))
			}
			if path == "bridge" {
				body = []byte(`{"type":"response.create","model":"gpt-5.5","input":"hi","client_metadata":{"ws_request_header_x_openai_internal_codex_responses_lite":"true"}}`)
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
				SetOpenAIClientTransport(c, OpenAIClientTransportWS)
				plan := codexWireProjectionTestPlan(t)
				plan.PolicySnapshot = defaultOpenAICodexFingerprintPolicy(0)
				plan.ProjectionMode = OpenAIOAuthIdentityProjectionPassthrough
				plan.CredentialOwnerNamespace = openAIOutboundSessionIdentityNamespace(account)
				_, err := gateway.proxyOpenAIWSHTTPBridgeTurn(context.Background(), c, account, "test-token", body, len(body), "gpt-5.5", "", "", "", "", 1, func([]byte) error { return nil }, &plan)
				require.NoError(t, err)
				require.False(t, plan.WireProfile.ToolNamespacesAllowed)
			} else {
				_, recorder, err := mergeHTTPForward(t, gateway, account, path, body, http.Header{responsesLiteHeader: {"true"}})
				require.NoError(t, err, recorder.Body.String())
			}
			require.Len(t, upstream.requests, 1)
			require.Equal(t, account.Proxy.URL(), upstream.lastProxyURL)
			require.Empty(t, upstream.lastReq.Header.Get(openAICodexTurnStateHeader))
			require.Empty(t, upstream.lastReq.Header.Get("Cookie"))
			require.Empty(t, upstream.lastReq.Header.Get(responsesLiteHeader))
			require.False(t, isOpenAIResponsesLiteWebSocketPayload(upstream.lastBody))
			require.Equal(t, "gpt-5.5", gjson.GetBytes(upstream.lastBody, "model").String())
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

func TestOpenAITerminalEarlyClosePreservesOpaqueStateAfterDelivery(t *testing.T) {
	account := mergeHTTPAccount()
	response := openAICompatSSECompletedResponse("resp_terminal_close", "gpt-5.4")
	terminal, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	reader := &hangingOpenAISSEAfterTerminal{payload: append(terminal, '\n'), release: make(chan struct{})}
	t.Cleanup(func() { _ = reader.Close() })
	recorder := httptest.NewRecorder()
	body := &mergeHTTPDeliveryCloseBody{ReadCloser: reader, recorder: recorder}
	response.Body = body
	response.Header.Set(openAICodexTurnStateHeader, "opaque-upstream-state")
	upstream := &httpUpstreamRecorder{resp: response}
	gateway := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{StreamKeepaliveInterval: 1, StreamDataIntervalTimeout: 30}}, accountRepo: newAuthorizedOpenAIOAuthTestRepo(account), httpUpstream: upstream}
	c, _ := gin.CreateTestContext(recorder)
	requestBody := mergeHTTPBody("responses", true)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(requestBody))
	done := make(chan error, 1)
	go func() { _, err := gateway.Forward(context.Background(), c, account, requestBody); done <- err }()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		_ = reader.Close()
		<-done
		t.Fatal("terminal delivery waited for upstream EOF")
	}
	require.True(t, body.closedAfterDelivery)
	require.Equal(t, "opaque-upstream-state", recorder.Header().Get(openAICodexTurnStateHeader))
	require.Len(t, upstream.requests, 1)
}

func TestOpenAIPassthroughFreezesTheActualWireModel(t *testing.T) {
	account := mergeHTTPAccount()
	account.Extra["openai_passthrough"] = true
	account.Credentials["model_mapping"] = map[string]any{"gpt-6-sol": "gpt-5.5"}
	upstream := &httpUpstreamRecorder{resp: openAICompatSSECompletedResponse("resp_passthrough_wire", "gpt-6-sol")}
	gateway := &OpenAIGatewayService{cfg: &config.Config{}, accountRepo: newAuthorizedOpenAIOAuthTestRepo(account), httpUpstream: upstream}
	gateway.codexModelCapabilities.observeManifest(openAICodexModelCapabilitiesNamespace(account), []byte(`{"models":[{"slug":"gpt-6-sol","use_responses_lite":true}]}`), time.Now())
	body := []byte(`{"model":"gpt-6-sol","instructions":"Reply briefly.","input":"hi","reasoning":{"mode":"pro","effort":"max"},"temperature":0.4,"stream":true}`)
	_, recorder, err := mergeHTTPForward(t, gateway, account, "passthrough", body, http.Header{openAICodexTurnStateHeader: {"opaque-client-state"}, responsesLiteHeader: {"true"}})
	require.NoError(t, err, recorder.Body.String())
	require.Len(t, upstream.requests, 1)
	require.Equal(t, "gpt-6-sol", gjson.GetBytes(upstream.lastBody, "model").String())
	require.Equal(t, "pro", gjson.GetBytes(upstream.lastBody, "reasoning.mode").String())
	require.False(t, gjson.GetBytes(upstream.lastBody, "temperature").Exists())
	require.Equal(t, "opaque-client-state", upstream.lastReq.Header.Get(openAICodexTurnStateHeader))
	require.Equal(t, "true", upstream.lastReq.Header.Get(responsesLiteHeader))
	require.Equal(t, account.Proxy.URL(), upstream.lastProxyURL)
}

func TestOpenAIModelEvidencePrecedesPublicModelRewrite(t *testing.T) {
	account := mergeHTTPAccount()
	account.Credentials["model_mapping"] = map[string]any{"public": "gpt-5.4"}
	response := openAICompatSSECompletedResponse("resp_model_evidence", "gpt-5-mini")
	response.Header.Set(openAICodexTurnStateHeader, "opaque-upstream-state")
	upstream := &httpUpstreamRecorder{resp: response}
	gateway := &OpenAIGatewayService{cfg: &config.Config{}, accountRepo: newAuthorizedOpenAIOAuthTestRepo(account), httpUpstream: upstream}
	body := []byte(strings.ReplaceAll(string(mergeHTTPBody("responses", true)), "gpt-5.4", "public"))
	result, recorder, err := mergeHTTPForward(t, gateway, account, "responses", body, nil)
	require.NoError(t, err, recorder.Body.String())
	require.Equal(t, "gpt-5-mini", result.UpstreamResponseModel)
	require.Contains(t, recorder.Body.String(), `"model":"public"`)
	require.NotContains(t, recorder.Body.String(), `"model":"gpt-5-mini"`)
	require.Len(t, upstream.requests, 1, "model disagreement never replays a delivered response")
	require.Equal(t, "opaque-upstream-state", recorder.Header().Get(openAICodexTurnStateHeader))
}
