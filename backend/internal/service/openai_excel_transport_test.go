package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/codexnative"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func excelTransportAccount() *Account {
	return &Account{ID: 71, Name: "synthetic-excel", Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 2,
		OpenAIOAuthAuthorizationGeneration: "synthetic-grant",
		Credentials:                        map[string]any{"access_token": "synthetic-token", "chatgpt_account_id": "synthetic-account"},
		Extra:                              map[string]any{OpenAIExcelUpstreamEnabledExtraKey: true, OpenAIUpstreamRouteGenerationExtraKey: "excel-route-one"}}
}

func authorizeExcelTransportFixture(gateway *OpenAIGatewayService, account *Account) {
	authorizeOpenAIForwardFixture(gateway, account)
	credential := openAIOAuthTestCredential(account, account.OpenAIOAuthOSProfiles.DefaultOS)
	account.OpenAIOAuthAuthorizationGeneration = credential.AuthorizationGeneration
}

func excelTransportResponse() *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(
		"data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_excel\",\"model\":\"gpt-6-astra\",\"status\":\"in_progress\"}}\n\n" +
			"data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n" +
			"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_excel\",\"model\":\"gpt-6-astra\",\"status\":\"completed\",\"output\":[{\"id\":\"msg_excel\",\"type\":\"message\",\"role\":\"assistant\",\"status\":\"completed\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello\"}]}],\"usage\":{\"input_tokens\":2,\"output_tokens\":1,\"total_tokens\":3}}}\n\n"))}
}

func TestOpenAIExcelGatewayTransportIdentityAndSingleSend(t *testing.T) {
	for _, tc := range []struct {
		name, ua string
		platform codexnative.Platform
	}{
		{"windows", "codex-tui/0.155.1 (Windows NT 10.0; Win64; x64)", codexnative.Windows},
		{"macos", "codex-tui/0.155.1 (Mac OS 26.6.2; arm64)", codexnative.MacOS},
		{"linux", "codex-tui/0.155.1 (Linux; x86_64)", codexnative.Linux},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := excelTransportAccount()
			upstream := &httpUpstreamRecorder{resp: excelTransportResponse()}
			state, _ := excelStateFixture()
			gateway := &OpenAIGatewayService{httpUpstream: upstream, excelState: state}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
			defer cancel()
			attempt := &openAICandyTestAttempt{}
			ctx = withOpenAICandyTest(ctx, attempt)
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, chatgptCodexURL, strings.NewReader(`{"model":"gpt-6-astra","input":"hello","stream":true,"reasoning":{"effort":"xhigh"}}`))
			require.NoError(t, err)
			req.Header.Set("Authorization", "Bearer synthetic-token")
			req.Header.Set("ChatGPT-Account-ID", "synthetic-account")
			req.Header.Set("User-Agent", tc.ua)
			req.Header.Set("x-codex-installation-id", "installation-"+tc.name)
			req.Header.Set("Cookie", "untrusted=must-not-send")
			resp, err := gateway.doOpenAIUpstream(req, "http://synthetic-proxy.invalid:8000", account)
			require.NoError(t, err)
			data, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.NoError(t, resp.Body.Close())
			require.Contains(t, string(data), `response.completed`)
			require.Len(t, upstream.requests, 1)
			require.Equal(t, openAIExcelResponsesURL, upstream.lastReq.URL.String())
			require.Equal(t, "bps.openai.com", upstream.lastReq.Host)
			require.Equal(t, "http://synthetic-proxy.invalid:8000", upstream.lastProxyURL)
			require.Equal(t, openAIExcelUserAgent, upstream.lastReq.UserAgent())
			require.Equal(t, "installation-"+tc.name, upstream.lastReq.Header.Get("x-codex-installation-id"))
			require.Empty(t, upstream.lastReq.Header.Get("Cookie"))
			scope, ok := codexnative.ScopeFromContext(upstream.lastReq.Context())
			require.True(t, ok)
			require.Equal(t, codexnative.Windows, codexnative.Resolve(upstream.lastReq.UserAgent(), scope).Platform)
			deadline, ok := upstream.lastReq.Context().Deadline()
			require.True(t, ok)
			require.WithinDuration(t, time.Now().Add(30*time.Minute), deadline, time.Second)
			require.Equal(t, "gpt-6-astra", attempt.actualModel)
			require.Equal(t, "xhigh", attempt.actualEffort)
			_, err = gateway.doOpenAIUpstream(req, "", account)
			require.ErrorContains(t, err, "inference_replay_disabled")
			require.Len(t, upstream.requests, 1)
		})
	}
}

func TestOpenAIExcelGatewayAllTextEntrypoints(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, mode := range []string{"responses", "passthrough", "chat", "messages"} {
		t.Run(mode, func(t *testing.T) {
			account := excelTransportAccount()
			if mode == "passthrough" {
				account.Extra["openai_passthrough"] = true
			}
			upstream := &httpUpstreamRecorder{resp: excelTransportResponse()}
			state, _ := excelStateFixture()
			gateway := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream, excelState: state}
			authorizeExcelTransportFixture(gateway, account)
			body := []byte(`{"model":"gpt-6-astra","input":"hello","stream":true}`)
			if mode == "chat" || mode == "messages" {
				body = []byte(`{"model":"gpt-6-astra","messages":[{"role":"user","content":"hello"}],"stream":true,"max_tokens":50}`)
			}
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/"+mode, bytes.NewReader(body))
			c.Request.Header.Set("session_id", "source-session")
			var err error
			switch mode {
			case "chat":
				_, err = gateway.ForwardAsChatCompletions(c.Request.Context(), c, account, body, "", "gpt-6-astra")
			case "messages":
				_, err = gateway.ForwardAsAnthropic(c.Request.Context(), c, account, body, "", "gpt-6-astra")
			default:
				_, err = gateway.Forward(c.Request.Context(), c, account, body)
			}
			require.NoError(t, err, recorder.Body.String())
			require.Len(t, upstream.requests, 1)
			require.Equal(t, openAIExcelResponsesURL, upstream.lastReq.URL.String())
			require.Equal(t, "gpt-6-astra", gjson.GetBytes(upstream.lastBody, "model").String())
			require.Empty(t, upstream.lastReq.Header.Get(responsesLiteHeader))
			require.Contains(t, recorder.Body.String(), "hello")
		})
	}
}

func TestOpenAIExcelCatalogAndCapabilityMask(t *testing.T) {
	account := excelTransportAccount()
	account.Extra["openai_compact_mode"] = OpenAICompactModeForceOn
	account.Extra["openai_remote_compaction_v2_supported"] = true
	gateway := &OpenAIGatewayService{}
	models, err := gateway.FetchCandyTestModels(context.Background(), account)
	require.NoError(t, err)
	require.Equal(t, "excel_builtin", gjson.GetBytes(models.Body, "source").String())
	options, err := candyTestUpstreamModelOptions(account, models.Body)
	require.NoError(t, err)
	require.Len(t, options, 6)
	for _, option := range options {
		require.Equal(t, []string{"low", "medium", "high", "xhigh"}, option.ReasoningEfforts)
	}
	require.False(t, account.AllowsOpenAICompact())
	require.False(t, account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityRemoteCompactionV2))
	require.False(t, account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityAlphaSearch))
	require.Equal(t, OpenAIUpstreamTransportHTTPSSE, NewOpenAIWSProtocolResolver(&config.Config{}).Resolve(account).Transport)
	require.False(t, effectiveCodexHTTPModelCapabilities(account, "gpt-6-astra", CodexModelCapabilities{Known: true, UseResponsesLite: true}, true).UseResponsesLite)
}

func TestOpenAIExcelForbiddenDoesNotRevokeCapabilityDeniedAccount(t *testing.T) {
	account := excelTransportAccount()
	service := &OpenAIGatewayService{}
	for _, code := range []string{"feature_not_enabled", "permission_denied", ""} {
		body := []byte(`{"error":{"code":"` + code + `","message":"Excel unavailable"}}`)
		require.True(t, isOpenAIExcelCapabilityForbidden(account, http.StatusForbidden, body))
		require.False(t, service.handleOpenAIAccountUpstreamError(context.Background(), account, http.StatusForbidden, nil, body))
		require.False(t, service.shouldFailoverOpenAIUpstreamResponse(account, http.StatusForbidden, "", body))
	}
	for _, code := range []string{"token_revoked", "account_deactivated", "token_expired"} {
		require.False(t, isOpenAIExcelCapabilityForbidden(account, http.StatusForbidden, []byte(`{"error":{"code":"`+code+`"}}`)))
	}
}

func TestOpenAIExcelWSRouteChangeRequiresReconnect(t *testing.T) {
	initial := excelTransportAccount()
	initial.Extra[OpenAIExcelUpstreamEnabledExtraKey] = false
	current := excelTransportAccount()
	current.Extra[OpenAIExcelUpstreamEnabledExtraKey] = true
	current.Extra[OpenAIUpstreamRouteGenerationExtraKey] = "route-two"
	gateway := &OpenAIGatewayService{accountRepo: &auxiliaryOSLegacyTestRepository{accounts: map[int64]*Account{current.ID: current}}}
	err := gateway.validateOpenAIBackendWSRequest(context.Background(), initial, "scope", []byte(`{"type":"response.create"}`), nil)
	var closeErr *OpenAIWSClientCloseError
	require.True(t, errors.As(err, &closeErr))
	require.Equal(t, coderws.StatusNormalClosure, closeErr.StatusCode())
}

func TestOpenAIExcelHTTPBridgeUsesAdapter(t *testing.T) {
	account := excelTransportAccount()
	upstream := &httpUpstreamRecorder{resp: excelTransportResponse()}
	state, _ := excelStateFixture()
	gateway := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream, excelState: state}
	authorizeExcelTransportFixture(gateway, account)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	c.Request.Header.Set("session_id", "logical-excel-bridge")
	payload := []byte(`{"type":"response.create","model":"gpt-6-astra","input":"hello","stream":true}`)
	var frames [][]byte
	result, err := gateway.proxyOpenAIWSHTTPBridgeTurn(context.Background(), c, account, "synthetic-token", payload, len(payload),
		"gpt-6-astra", "", "", "", "", 1, func(frame []byte) error { frames = append(frames, bytes.Clone(frame)); return nil }, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, upstream.requests, 1)
	require.Equal(t, openAIExcelResponsesURL, upstream.lastReq.URL.String())
	require.Contains(t, string(bytes.Join(frames, nil)), "response.completed")
	require.Contains(t, string(bytes.Join(frames, nil)), "hello")
}

func TestOpenAIExcelOrdinaryAccountTestUsesAdapterAndSource(t *testing.T) {
	account := excelTransportAccount()
	upstream := &httpUpstreamRecorder{resp: excelTransportResponse()}
	state, _ := excelStateFixture()
	gateway := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream, excelState: state}
	authorizeExcelTransportFixture(gateway, account)
	service := &AccountTestService{cfg: gateway.cfg, httpUpstream: upstream, openAIGatewayService: gateway}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/admin/accounts/71/test", nil)
	require.NoError(t, service.testOpenAIAccountConnection(c, account, "gpt-6-astra", "", ""))
	require.Len(t, upstream.requests, 1)
	require.Equal(t, openAIExcelResponsesURL, upstream.lastReq.URL.String())
	require.Contains(t, recorder.Body.String(), `"upstream_kind":"excel"`)
	require.Contains(t, recorder.Body.String(), `"success":true`)
}

func TestOpenAIExcelInvalidResponseCannotReplayOrPause(t *testing.T) {
	account := excelTransportAccount()
	upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"status":"incomplete"}`))}}
	state, _ := excelStateFixture()
	gateway := &OpenAIGatewayService{httpUpstream: upstream, excelState: state}
	req, err := http.NewRequest(http.MethodPost, chatgptCodexURL, strings.NewReader(`{"model":"gpt-6-astra","input":"hello"}`))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer synthetic-token")
	req.Header.Set("ChatGPT-Account-ID", "synthetic-account")
	_, err = gateway.doOpenAIUpstream(req, "", account)
	var responseErr *openAIExcelResponseError
	require.ErrorAs(t, err, &responseErr)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	result := gateway.handleOpenAIUpstreamTransportError(context.Background(), c, account, err, false)
	var failover *UpstreamFailoverError
	require.False(t, errors.As(result, &failover))
	require.Equal(t, http.StatusBadGateway, recorder.Code)
	require.Len(t, upstream.requests, 1)
}

func TestOpenAIExcelUnsupportedEffortAndCompactNeverSend(t *testing.T) {
	for _, compact := range []bool{false, true} {
		account := excelTransportAccount()
		upstream := &httpUpstreamRecorder{resp: excelTransportResponse()}
		gateway := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
		authorizeExcelTransportFixture(gateway, account)
		path := "/v1/responses"
		if compact {
			path += "/compact"
		}
		body := []byte(`{"model":"gpt-6-astra","input":"hello","reasoning":{"effort":"ultra"}}`)
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
		_, err := gateway.Forward(context.Background(), c, account, body)
		require.Error(t, err)
		require.Empty(t, upstream.requests)
		require.Equal(t, http.StatusBadRequest, recorder.Code)
	}
}

func TestOpenAIExcelIntegrityDisclosesProtocolBoundary(t *testing.T) {
	observation := NewOpenAIRequestIntegrityState(true, "responses", []byte(`{"model":"gpt-6-astra","input":"hello"}`)).Check(excelTransportAccount(), []byte(`{"model":"gpt-6-astra","input":[]}`), RequestIntegrityCheckOptions{Transport: "http"})
	require.Equal(t, "skipped", observation.Status)
	require.Equal(t, "excel_protocol_not_applicable", observation.Reason)
	require.Equal(t, []string{"excel_protocol_conversion"}, observation.RuleCodes)
}

func TestOpenAIExcelContinuationRejectedBeforeProjection(t *testing.T) {
	account := excelTransportAccount()
	upstream := &httpUpstreamRecorder{resp: excelTransportResponse()}
	gateway := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	authorizeExcelTransportFixture(gateway, account)
	body := []byte(`{"model":"gpt-6-astra","previous_response_id":"resp_excel_before","input":"continue"}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	_, err := gateway.Forward(context.Background(), c, account, body)
	require.Error(t, err)
	require.Empty(t, upstream.requests)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Contains(t, recorder.Body.String(), "excel_continuation_unsupported")
	wsErr := gateway.validateOpenAIBackendWSRequest(context.Background(), account, "scope", body, nil)
	var closeErr *OpenAIWSClientCloseError
	require.ErrorAs(t, wsErr, &closeErr)
	require.Equal(t, coderws.StatusPolicyViolation, closeErr.StatusCode())
}

func TestOpenAIExcelIngressOpaqueSourceUsesFinalGrantAfterProjection(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "codex_after_switch", true: "excel"}[enabled], func(t *testing.T) {
			account := excelTransportAccount()
			account.Extra[OpenAIExcelUpstreamEnabledExtraKey] = enabled
			account.OpenAIOAuthAuthorizationGeneration = "" // It is not known at ingress.
			state, _ := excelStateFixture()
			gateway := &OpenAIGatewayService{excelState: state}
			raw := []byte(`{"model":"gpt-6-astra","prompt_cache_key":"client-session","input":[{"type":"reasoning","encrypted_content":"opaque-from-current-grant"}]}`)
			ctx := withOpenAIExcelRequestScope(context.Background(), nil, account, raw)
			ctx = WithOpenAIBackendIngressSource(ctx, raw, nil)
			// Normal projection can remove an opaque input before transport. Its
			// captured lookup key must still be validated under the token snapshot.
			projected := `{"model":"gpt-6-astra","input":"hello"}`
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, chatgptCodexURL, strings.NewReader(projected))
			require.NoError(t, err)
			req.Header.Set("Authorization", "Bearer synthetic-token")
			req.Header.Set("ChatGPT-Account-ID", "synthetic-account")
			account.OpenAIOAuthAuthorizationGeneration = "bound-grant"
			scope := gateway.openAIExcelHistoryScope(ctx, account, req.Header, []byte(projected))
			require.Contains(t, scope, "bound-grant")
			require.NoError(t, gateway.rememberOpenAIBackendPayload(ctx, account, scope, raw, nil))
			_, _, actualScope, err := gateway.prepareOpenAIExcelUpstream(req, "", account)
			require.NoError(t, err)
			require.Equal(t, scope, actualScope)

			account.OpenAIOAuthAuthorizationGeneration = "replacement-grant"
			_, _, _, err = gateway.prepareOpenAIExcelUpstream(req, "", account)
			require.ErrorIs(t, err, ErrOpenAIBackendHistoryUnavailable)
		})
	}
}
