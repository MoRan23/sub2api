package service

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

const transportTestPinnedInstallationID = "11111111-2222-4333-8444-555555555555"

func successfulInstallationTestResponse() *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}, "x-request-id": []string{"rid-installation"}},
		Body:       io.NopCloser(strings.NewReader(`{"id":"resp_installation","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`)),
	}
}

func installationTestOAuthAccount(extra map[string]any) *Account {
	if extra == nil {
		extra = make(map[string]any)
	}
	if _, exists := extra[openAIPinnedInstallationIDKey]; !exists {
		extra[openAIPinnedInstallationIDKey] = transportTestPinnedInstallationID
	}
	return &Account{
		ID:          8801,
		Name:        "installation-oauth",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{"access_token": "oauth-token", "chatgpt_account_id": "chatgpt-account"},
		Extra:       extra,
		Status:      StatusActive,
		Schedulable: true,
	}
}

func decodeInstallationTestMetadata(t *testing.T, raw string) map[string]any {
	t.Helper()
	var metadata map[string]any
	if err := json.Unmarshal([]byte(raw), &metadata); err != nil {
		t.Fatalf("decode turn metadata %q: %v", raw, err)
	}
	return metadata
}

func requireInstallationTestRootIdentity(t *testing.T, metadata map[string]any) string {
	t.Helper()
	sessionID, _ := metadata["session_id"].(string)
	threadID, _ := metadata["thread_id"].(string)
	require.NoError(t, ValidateOpenAIOutboundSessionIdentity(OpenAIOutboundSessionIdentity{
		SessionID: sessionID,
		ThreadID:  threadID,
	}))
	return sessionID
}

func TestOpenAIInstallationNormalHTTPRewritesBodyAndHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-5.4","stream":false,"instructions":"test","input":"hello","client_metadata":{"x-codex-installation-id":"client-body","x-codex-turn-metadata":"{\"installation_id\":\"client-nested-body\",\"session_id\":\"body-session\"}"}}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set(codexInstallationIDKey, "client-header")
	c.Request.Header.Set(openAIWSTurnMetadataHeader, `{"installation_id":"client-nested-header","session_id":"header-session"}`)

	upstream := &httpUpstreamRecorder{resp: successfulInstallationTestResponse()}
	svc := &OpenAIGatewayService{httpUpstream: upstream}
	result, err := svc.Forward(context.Background(), c, installationTestOAuthAccount(nil), body)
	if err != nil {
		t.Fatalf("forward request: %v", err)
	}
	if result == nil || upstream.lastReq == nil {
		t.Fatal("expected forward result and upstream request")
	}

	var outboundBody map[string]any
	if err := json.Unmarshal(upstream.lastBody, &outboundBody); err != nil {
		t.Fatalf("decode outbound body: %v", err)
	}
	clientMetadata := outboundBody["client_metadata"].(map[string]any)
	if clientMetadata[codexInstallationIDKey] != transportTestPinnedInstallationID {
		t.Fatalf("unexpected body installation ID: %#v", clientMetadata)
	}
	bodyTurn := decodeInstallationTestMetadata(t, clientMetadata[openAIWSTurnMetadataHeader].(string))
	if bodyTurn[codexTurnMetadataInstallationIDKey] != transportTestPinnedInstallationID {
		t.Fatalf("unexpected body turn metadata: %#v", bodyTurn)
	}
	turnSessionID := requireInstallationTestRootIdentity(t, bodyTurn)
	if upstream.lastReq.Header.Get(codexInstallationIDKey) != transportTestPinnedInstallationID {
		t.Fatalf("unexpected header installation ID: %#v", upstream.lastReq.Header)
	}
	headerTurn := decodeInstallationTestMetadata(t, upstream.lastReq.Header.Get(openAIWSTurnMetadataHeader))
	if headerTurn[codexTurnMetadataInstallationIDKey] != transportTestPinnedInstallationID {
		t.Fatalf("unexpected header turn metadata: %#v", headerTurn)
	}
	require.Equal(t, turnSessionID, requireInstallationTestRootIdentity(t, headerTurn))
	require.Equal(t, turnSessionID, upstream.lastReq.Header.Get("session-id"))
	require.Equal(t, turnSessionID, upstream.lastReq.Header.Get("thread-id"))
}

func TestOpenAIInstallationCompactUsesHeadersOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-5.4","stream":false,"instructions":"test","input":"hello","client_metadata":{"x-codex-installation-id":"client-body","x-codex-turn-metadata":"{\"installation_id\":\"client-nested-body\"}"}}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/compact", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set(codexInstallationIDKey, "client-header")
	c.Request.Header.Set(openAIWSTurnMetadataHeader, `{"installation_id":"client-nested-header","thread_id":"thread-1"}`)
	c.Request.Header.Set("x-codex-window-id", "window-1")

	upstream := &httpUpstreamRecorder{resp: successfulInstallationTestResponse()}
	svc := &OpenAIGatewayService{httpUpstream: upstream}
	if _, err := svc.Forward(context.Background(), c, installationTestOAuthAccount(nil), body); err != nil {
		t.Fatalf("forward compact request: %v", err)
	}
	var outboundBody map[string]any
	if err := json.Unmarshal(upstream.lastBody, &outboundBody); err != nil {
		t.Fatalf("decode compact body: %v", err)
	}
	if _, exists := outboundBody["client_metadata"]; exists {
		t.Fatalf("compact body contains client_metadata: %#v", outboundBody)
	}
	if upstream.lastReq.Header.Get(codexInstallationIDKey) != transportTestPinnedInstallationID {
		t.Fatalf("unexpected compact installation header: %#v", upstream.lastReq.Header)
	}
	headerTurn := decodeInstallationTestMetadata(t, upstream.lastReq.Header.Get(openAIWSTurnMetadataHeader))
	if headerTurn[codexTurnMetadataInstallationIDKey] != transportTestPinnedInstallationID {
		t.Fatalf("unexpected compact turn metadata: %#v", headerTurn)
	}
	turnSessionID := requireInstallationTestRootIdentity(t, headerTurn)
	if upstream.lastReq.Header.Get("x-codex-window-id") != "window-1" ||
		upstream.lastReq.Header.Get("session-id") != turnSessionID ||
		upstream.lastReq.Header.Get("thread-id") != turnSessionID ||
		upstream.lastReq.Header.Get("session_id") != "" ||
		upstream.lastReq.Header.Get("x-client-request-id") != "" {
		t.Fatalf("compact compatibility headers were not preserved: %#v", upstream.lastReq.Header)
	}
}

func TestOpenAIInstallationHTTPPassthroughPinsAllCarriers(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-5.4","stream":false,"instructions":"test","input":"hello","client_metadata":{"x-codex-installation-id":"client-body","x-codex-turn-metadata":"{\"installation_id\":\"client-nested-body\"}"}}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set(codexInstallationIDKey, "client-header")
	c.Request.Header.Set(openAIWSTurnMetadataHeader, `{"installation_id":"client-nested-header"}`)

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid-passthrough-installation"}},
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			`data: {"type":"response.completed","response":{"id":"resp_passthrough_installation","status":"completed","usage":{"input_tokens":1,"output_tokens":1}}}`,
			"",
			"data: [DONE]",
			"",
		}, "\n"))),
	}}
	svc := &OpenAIGatewayService{httpUpstream: upstream}
	account := installationTestOAuthAccount(map[string]any{
		openAIPinnedInstallationIDKey: transportTestPinnedInstallationID,
		"openai_passthrough":          true,
	})
	if _, err := svc.Forward(context.Background(), c, account, body); err != nil {
		t.Fatalf("forward passthrough request: %v", err)
	}
	if got := strings.TrimSpace(jsonStringPath(t, upstream.lastBody, "client_metadata", codexInstallationIDKey)); got != transportTestPinnedInstallationID {
		t.Fatalf("passthrough body installation ID was not pinned: %q", got)
	}
	if got := upstream.lastReq.Header.Get(codexInstallationIDKey); got != transportTestPinnedInstallationID {
		t.Fatalf("passthrough header installation ID was not pinned: %q", got)
	}
	headerTurn := decodeInstallationTestMetadata(t, upstream.lastReq.Header.Get(openAIWSTurnMetadataHeader))
	if headerTurn[codexTurnMetadataInstallationIDKey] != transportTestPinnedInstallationID {
		t.Fatalf("passthrough header turn metadata was not pinned: %#v", headerTurn)
	}
	bodyTurn := decodeInstallationTestMetadata(t, jsonStringPath(t, upstream.lastBody, "client_metadata", openAIWSTurnMetadataHeader))
	if bodyTurn[codexTurnMetadataInstallationIDKey] != transportTestPinnedInstallationID {
		t.Fatalf("passthrough body turn metadata was not pinned: %#v", bodyTurn)
	}
}

func TestOpenAIInstallationAPIKeyRemainsUnchanged(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-5.4","stream":false,"input":"hello","client_metadata":{"x-codex-installation-id":"api-key-body","x-codex-turn-metadata":"{\"installation_id\":\"api-key-nested\",\"session_id\":\"api-key-session\"}"}}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/compact", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set(codexInstallationIDKey, "api-key-header")
	c.Request.Header.Set(openAIWSTurnMetadataHeader, `{"installation_id":"api-key-header-nested","session_id":"api-key-header-session"}`)

	upstream := &httpUpstreamRecorder{resp: successfulInstallationTestResponse()}
	svc := &OpenAIGatewayService{
		cfg:          &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}}},
		httpUpstream: upstream,
	}
	account := &Account{
		ID:          8802,
		Name:        "installation-apikey",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{"api_key": "sk-test", "base_url": "https://api.openai.com"},
		Extra:       map[string]any{"use_responses_api": true},
		Status:      StatusActive,
		Schedulable: true,
	}
	if _, err := svc.Forward(context.Background(), c, account, body); err != nil {
		t.Fatalf("forward API-key compact request: %v", err)
	}
	if got := jsonStringPath(t, upstream.lastBody, "client_metadata", codexInstallationIDKey); got != "api-key-body" {
		t.Fatalf("API-key body installation ID changed to %q", got)
	}
	apiKeyBodyTurn := jsonStringPath(t, upstream.lastBody, "client_metadata", openAIWSTurnMetadataHeader)
	if g := gjsonGetString(apiKeyBodyTurn, codexTurnMetadataInstallationIDKey); g != "api-key-nested" {
		t.Fatalf("API-key nested body installation ID changed to %q", g)
	}
	if got := upstream.lastReq.Header.Get(codexInstallationIDKey); got != "api-key-header" {
		t.Fatalf("API-key header installation ID changed to %q", got)
	}
	if got := upstream.lastReq.Header.Get(openAIWSTurnMetadataHeader); got != `{"installation_id":"api-key-header-nested","session_id":"api-key-header-session"}` {
		t.Fatalf("API-key turn metadata changed to %q", got)
	}
}

func jsonStringPath(t *testing.T, body []byte, path ...string) string {
	t.Helper()
	var value any
	if err := json.Unmarshal(body, &value); err != nil {
		t.Fatalf("decode JSON body: %v", err)
	}
	current := value
	for _, part := range path {
		object, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		current = object[part]
	}
	result, _ := current.(string)
	return result
}

func gjsonGetString(raw, path string) string {
	var object map[string]any
	if json.Unmarshal([]byte(raw), &object) != nil {
		return ""
	}
	value, _ := object[path].(string)
	return value
}

func TestBuildOpenAIWSHeadersPinsInstallationForPassthrough(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	c.Request.Header.Set(codexInstallationIDKey, "client-header")
	c.Request.Header.Set(openAIWSTurnMetadataHeader, `{"installation_id":"client-nested","session_id":"session-1"}`)
	account := installationTestOAuthAccount(map[string]any{"openai_passthrough": true})
	svc := &OpenAIGatewayService{}
	decision := OpenAIWSProtocolDecision{Transport: OpenAIUpstreamTransportResponsesWebsocketV2}

	passthroughHeaders, _, err := svc.buildOpenAIWSHeaders(
		context.Background(), c, account, "oauth-token", decision, true, "",
		c.GetHeader(openAIWSTurnMetadataHeader), "", false,
	)
	if err != nil {
		t.Fatalf("build passthrough headers: %v", err)
	}
	if passthroughHeaders.Get(codexInstallationIDKey) != transportTestPinnedInstallationID {
		t.Fatalf("passthrough WS header was not pinned: %#v", passthroughHeaders)
	}
	passthroughNested := decodeInstallationTestMetadata(t, passthroughHeaders.Get(openAIWSTurnMetadataHeader))
	require.Equal(t, transportTestPinnedInstallationID, passthroughNested[codexTurnMetadataInstallationIDKey])
	passthroughSessionID := requireInstallationTestRootIdentity(t, passthroughNested)
	require.Equal(t, passthroughSessionID, passthroughHeaders.Get("session-id"))
	require.Equal(t, passthroughSessionID, passthroughHeaders.Get("thread-id"))

	pinnedHeaders, pinnedResolution, err := svc.buildOpenAIWSHeaders(
		context.Background(), c, account, "oauth-token", decision, true, "",
		c.GetHeader(openAIWSTurnMetadataHeader), "", true,
	)
	if err != nil {
		t.Fatalf("build pinned headers: %v", err)
	}
	require.Equal(t, OpenAIOAuthIdentityProjectionPassthrough, pinnedResolution.OutboundIdentityPlan.ProjectionMode)
	require.Equal(t, OpenAIOAuthInstallationAccountPin, pinnedResolution.OutboundIdentityPlan.InstallationPolicy)
	if pinnedHeaders.Get(codexInstallationIDKey) != transportTestPinnedInstallationID {
		t.Fatalf("non-passthrough WS header was not pinned: %#v", pinnedHeaders)
	}
	nested := decodeInstallationTestMetadata(t, pinnedHeaders.Get(openAIWSTurnMetadataHeader))
	if nested[codexTurnMetadataInstallationIDKey] != transportTestPinnedInstallationID {
		t.Fatalf("non-passthrough WS turn metadata was not pinned: %#v", nested)
	}
	pinnedSessionID := requireInstallationTestRootIdentity(t, nested)
	require.Equal(t, pinnedSessionID, pinnedHeaders.Get("session-id"))
	require.Equal(t, pinnedSessionID, pinnedHeaders.Get("thread-id"))
}

func TestBuildOpenAIWSHeadersIdentityPlanReuseRequiresCredentialOwnerMatch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	capture := CaptureOpenAIOAuthIdentity(c, []byte(`{"type":"response.create","model":"gpt-5.1"}`), "")
	SetOpenAIOAuthIdentityCapture(c, capture)
	decision := OpenAIWSProtocolDecision{Transport: OpenAIUpstreamTransportResponsesWebsocketV2}
	svc := &OpenAIGatewayService{}
	firstAccount := installationTestOAuthAccount(nil)
	secondAccount := installationTestOAuthAccount(nil)
	secondAccount.ID = firstAccount.ID + 1

	firstPlan := OpenAIOAuthIdentityPlan{
		Capture:                  capture,
		PolicySnapshot:           defaultOpenAICodexFingerprintPolicy(0),
		TurnIdentityRequested:    true,
		ClientIdentityEnabled:    true,
		ClientIdentity:           resolveCodexClientIdentityPlan(CodexClientIdentityNormalize, ""),
		InstallationID:           "first-plan-installation",
		InstallationEnabled:      true,
		ProjectionMode:           OpenAIOAuthIdentityProjectionRegular,
		InstallationPolicy:       OpenAIOAuthInstallationAccountPin,
		CredentialOwnerNamespace: openAIOutboundSessionIdentityNamespace(firstAccount),
	}
	SetOpenAIOAuthIdentityPlan(c, firstPlan)
	_, reused, err := svc.buildOpenAIWSHeadersWithBody(
		context.Background(), c, firstAccount, "oauth-token", decision, true, "", "", "", nil, true, "gpt-5.1", "",
	)
	require.NoError(t, err)
	require.Equal(t, "first-plan-installation", reused.OutboundIdentityPlan.InstallationID)

	SetOpenAIOAuthIdentityPlan(c, firstPlan)
	_, rematerialized, err := svc.buildOpenAIWSHeadersWithBody(
		context.Background(), c, secondAccount, "oauth-token", decision, true, "", "", "", nil, true, "gpt-5.1", "",
	)
	require.NoError(t, err)
	require.Equal(t, openAIOutboundSessionIdentityNamespace(secondAccount), rematerialized.OutboundIdentityPlan.CredentialOwnerNamespace)
	require.Equal(t, transportTestPinnedInstallationID, rematerialized.OutboundIdentityPlan.InstallationID)
}

func TestOpenAIInstallationIngressWSRewritesEveryResponseCreate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3

	upstreamConn := &openAIWSCaptureConn{events: [][]byte{
		[]byte(`{"type":"response.completed","response":{"id":"resp_installation_ingress_1","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		[]byte(`{"type":"response.completed","response":{"id":"resp_installation_ingress_2","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
	}}
	captureDialer := &openAIWSCaptureDialer{conn: upstreamConn}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(captureDialer)
	svc := &OpenAIGatewayService{
		cfg:              cfg,
		httpUpstream:     &httpUpstreamRecorder{},
		cache:            &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
		openaiWSPool:     pool,
	}
	account := installationTestOAuthAccount(map[string]any{
		openAIPinnedInstallationIDKey:     transportTestPinnedInstallationID,
		"responses_websockets_v2_enabled": true,
	})

	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{CompressionMode: coderws.CompressionContextTakeover})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() { _ = conn.CloseNow() }()

		recorder := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(recorder)
		ginCtx.Request = r.Clone(r.Context())
		readCtx, cancelRead := context.WithTimeout(r.Context(), 3*time.Second)
		messageType, firstMessage, readErr := conn.Read(readCtx)
		cancelRead()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if messageType != coderws.MessageText && messageType != coderws.MessageBinary {
			serverErrCh <- io.ErrUnexpectedEOF
			return
		}
		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "oauth-token", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialHeaders := make(http.Header)
	dialHeaders.Set(codexInstallationIDKey, "client-handshake-installation")
	dialHeaders.Set(openAIWSTurnMetadataHeader, `{"installation_id":"client-handshake-nested","session_id":"handshake-session"}`)
	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), &coderws.DialOptions{HTTPHeader: dialHeaders})
	cancelDial()
	if err != nil {
		t.Fatalf("dial ingress websocket: %v", err)
	}
	defer func() { _ = clientConn.CloseNow() }()

	writeTurn := func(payload string) {
		writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancelWrite()
		if err := clientConn.Write(writeCtx, coderws.MessageText, []byte(payload)); err != nil {
			t.Fatalf("write ingress turn: %v", err)
		}
		readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancelRead()
		if _, _, err := clientConn.Read(readCtx); err != nil {
			t.Fatalf("read ingress result: %v", err)
		}
	}
	writeTurn(`{"type":"response.create","model":"gpt-5.1","stream":false,"client_metadata":{"x-codex-installation-id":"client-frame-1","x-codex-turn-metadata":"{\"installation_id\":\"client-frame-nested-1\",\"turn\":1}"}}`)
	writeTurn(`{"type":"response.create","model":"gpt-5.1","stream":false,"previous_response_id":"resp_installation_ingress_1","client_metadata":{"x-codex-installation-id":"client-frame-2","x-codex-turn-metadata":"{\"installation_id\":\"client-frame-nested-2\",\"turn\":2}"}}`)
	_ = clientConn.Close(coderws.StatusNormalClosure, "done")

	select {
	case serverErr := <-serverErrCh:
		if serverErr != nil {
			t.Fatalf("ingress websocket failed: %v", serverErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for ingress websocket")
	}
	if len(upstreamConn.writes) != 2 {
		t.Fatalf("expected two upstream response.create frames, got %d", len(upstreamConn.writes))
	}
	var turnSessionID string
	for index, write := range upstreamConn.writes {
		metadata, _ := write["client_metadata"].(map[string]any)
		if metadata[codexInstallationIDKey] != transportTestPinnedInstallationID {
			t.Fatalf("turn %d top-level installation ID not pinned: %#v", index+1, metadata)
		}
		nested := decodeInstallationTestMetadata(t, metadata[openAIWSTurnMetadataHeader].(string))
		if nested[codexTurnMetadataInstallationIDKey] != transportTestPinnedInstallationID {
			t.Fatalf("turn %d nested metadata not pinned: %#v", index+1, nested)
		}
		currentSessionID := requireInstallationTestRootIdentity(t, nested)
		if turnSessionID == "" {
			turnSessionID = currentSessionID
		}
		require.Equal(t, turnSessionID, currentSessionID)
	}
	if captureDialer.lastHeaders.Get(codexInstallationIDKey) != transportTestPinnedInstallationID {
		t.Fatalf("ingress handshake installation ID not pinned: %#v", captureDialer.lastHeaders)
	}
	handshakeNested := decodeInstallationTestMetadata(t, captureDialer.lastHeaders.Get(openAIWSTurnMetadataHeader))
	if handshakeNested[codexTurnMetadataInstallationIDKey] != transportTestPinnedInstallationID {
		t.Fatalf("ingress handshake turn metadata not pinned: %#v", handshakeNested)
	}
	require.Equal(t, turnSessionID, requireInstallationTestRootIdentity(t, handshakeNested))
	require.Equal(t, turnSessionID, captureDialer.lastHeaders.Get("session-id"))
	require.Equal(t, turnSessionID, captureDialer.lastHeaders.Get("thread-id"))
}
