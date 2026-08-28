package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// These tests deliberately exercise the transport seams rather than the
// identity store primitives.  They are kept in a separate file so the wire
// contract remains visible while the HTTP/WS implementation is being changed
// in parallel.

func newOpenAIIdentityPathContext(t *testing.T, path string, body []byte, apiKeyID int64) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	if apiKeyID != 0 {
		c.Set("api_key", &APIKey{ID: apiKeyID})
	}
	return c, recorder
}

func newOpenAIIdentityPathOAuthAccount(id int64) *Account {
	return &Account{
		ID:          id,
		Name:        "openai-identity-path-oauth",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token":       "oauth-token",
			"chatgpt_account_id": "chatgpt-identity-path",
		},
	}
}

func newOpenAIIdentityPathAPIKeyAccount(id int64) *Account {
	return &Account{
		ID:          id,
		Name:        "openai-identity-path-apikey",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{"api_key": "sk-identity-path"},
		Extra:       map[string]any{"openai_responses_supported": true},
	}
}

func newOpenAIIdentityPathService(t *testing.T, enabled bool, upstream *httpUpstreamRecorder) (*OpenAIGatewayService, *outboundIdentityGatewayCacheStub) {
	t.Helper()
	svc := newTransportIdentityTestService(t, enabled)
	svc.httpUpstream = upstream
	// The URL allowlist is intentionally disabled for these in-process request
	// builders; no network request is made by the tests.
	svc.cfg.Security.URLAllowlist.Enabled = false
	cache := &outboundIdentityGatewayCacheStub{}
	svc.cache = cache
	return svc, cache
}

func readOpenAIIdentityPathRequestBody(t *testing.T, req *http.Request) []byte {
	t.Helper()
	require.NotNil(t, req)
	require.NotNil(t, req.Body)
	body, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	_ = req.Body.Close()
	return body
}

func requireOpenAIIdentityPathPair(t *testing.T, headers http.Header, body []byte) OpenAIOutboundSessionIdentity {
	t.Helper()
	identity := OpenAIOutboundSessionIdentity{
		SessionID: headers.Get("session-id"),
		ThreadID:  headers.Get("thread-id"),
	}
	require.NoError(t, ValidateOpenAIOutboundSessionIdentity(identity))
	require.Empty(t, headers.Get("session_id"))
	require.Empty(t, headers.Get("conversation_id"))
	require.Equal(t, identity.ThreadID, headers.Get("x-client-request-id"))
	// These are accepted inbound aliases, but are intentionally not part of
	// the outbound wire allowlist.
	require.Empty(t, headers.Get("thread_id"))
	require.Empty(t, headers.Get("conversation-id"))
	require.Equal(t, identity.SessionID, gjson.GetBytes(body, "client_metadata.session_id").String())
	require.Equal(t, identity.ThreadID, gjson.GetBytes(body, "client_metadata.thread_id").String())
	return identity
}

func requireOpenAIIdentityPathNoBodyPair(t *testing.T, body []byte) {
	t.Helper()
	require.Empty(t, gjson.GetBytes(body, "client_metadata.session_id").String())
	require.Empty(t, gjson.GetBytes(body, "client_metadata.thread_id").String())
}

func requireOpenAIIdentityPathNoIdentityHeaders(t *testing.T, headers http.Header) {
	t.Helper()
	for _, name := range []string{
		"session-id", "session_id", "thread-id", "thread_id",
		"conversation_id", "conversation-id", "x-client-request-id",
	} {
		require.Empty(t, headers.Get(name), "unexpected identity header %s", name)
	}
}

func enableOpenAIIdentityPathFingerprintObservation(t *testing.T) {
	t.Helper()
	SetFingerprintObservationEnabled(false)
	SetFingerprintObservationEnabled(true)
	t.Cleanup(func() { SetFingerprintObservationEnabled(false) })
}

func requireOpenAIIdentityPathSingleFingerprintObservation(
	t *testing.T,
	identity OpenAIOutboundSessionIdentity,
	endpoint string,
) FingerprintObservationEntry {
	t.Helper()
	entries := SnapshotFingerprintObservations(0)
	require.Len(t, entries, 1)
	require.Equal(t, identity.SessionID, entries[0].SessionID)
	require.Equal(t, identity.ThreadID, entries[0].ThreadID)
	require.Equal(t, endpoint, entries[0].InboundEndpoint)
	return entries[0]
}

func identityPathCacheCalls(cache *outboundIdentityGatewayCacheStub) int {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	return cache.callCounter
}

func requireOpenAIIdentityPathRequestPair(t *testing.T, req *http.Request, body []byte) OpenAIOutboundSessionIdentity {
	t.Helper()
	return requireOpenAIIdentityPathPair(t, req.Header, body)
}

func requireOpenAIIdentityPathContextWindowID(t *testing.T, headers http.Header, body []byte) string {
	t.Helper()
	values := make([]string, 0, 2)
	if nested := strings.TrimSpace(headers.Get(openAIWSTurnMetadataHeader)); nested != "" {
		if value := gjson.Get(nested, "context_window_id").String(); value != "" {
			values = append(values, value)
		}
	}
	if nested := gjson.GetBytes(body, "client_metadata.x-codex-turn-metadata").String(); nested != "" {
		if value := gjson.Get(nested, "context_window_id").String(); value != "" {
			values = append(values, value)
		}
	}
	require.NotEmpty(t, values, "missing nested context_window_id")
	for _, value := range values {
		_, err := canonicalUUIDv7(value)
		require.NoError(t, err)
		require.Equal(t, values[0], value)
	}
	require.Empty(t, headers.Get("x-codex-context-window-id"), "context_window_id has no independent header")
	require.False(t, gjson.GetBytes(body, "client_metadata.context_window_id").Exists(), "context_window_id is nested-only")
	return values[0]
}

func requireOpenAIIdentityPathNoContextWindowID(t *testing.T, headers http.Header, body []byte) {
	t.Helper()
	require.False(t, gjson.Get(headers.Get(openAIWSTurnMetadataHeader), "context_window_id").Exists())
	nested := gjson.GetBytes(body, "client_metadata.x-codex-turn-metadata").String()
	require.False(t, gjson.Get(nested, "context_window_id").Exists())
	require.False(t, gjson.GetBytes(body, "client_metadata.context_window_id").Exists())
	require.Empty(t, headers.Get("x-codex-context-window-id"))
}

func (s *outboundIdentityGatewayCacheStub) SetSessionAccountID(context.Context, int64, string, int64, time.Duration) error {
	return nil
}

func TestOpenAIOutboundIdentityPathsResponsesBuilder(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled", true: "enabled"}[enabled], func(t *testing.T) {
			body := []byte(`{"model":"gpt-5.4","prompt_cache_key":"responses-path-key","client_metadata":{"other":"keep"}}`)
			c, _ := newOpenAIIdentityPathContext(t, "/v1/responses", body, 31)
			svc, cache := newOpenAIIdentityPathService(t, enabled, nil)
			account := newOpenAIIdentityPathOAuthAccount(910001)

			req, err := svc.buildUpstreamRequest(c.Request.Context(), c, account, body, "oauth-token", true, "responses-path-key", false)
			require.NoError(t, err)
			outboundBody := readOpenAIIdentityPathRequestBody(t, req)
			if enabled {
				requireOpenAIIdentityPathPair(t, req.Header, outboundBody)
			} else {
				require.Equal(t, isolateOpenAISessionID(31, "responses-path-key"), req.Header.Get("session_id"))
				require.Equal(t, isolateOpenAISessionID(31, "responses-path-key"), req.Header.Get("conversation_id"))
				require.Empty(t, req.Header.Get("thread-id"))
				require.Empty(t, req.Header.Get("x-client-request-id"))
				requireOpenAIIdentityPathNoBodyPair(t, outboundBody)
			}
			require.Equal(t, map[bool]int{false: 0, true: 1}[enabled], identityPathCacheCalls(cache))
		})
	}
}

func TestOpenAIOutboundIdentityPathsResponsesBuilderUsesExplicitTupleWithoutPromptKey(t *testing.T) {
	tests := []struct {
		name    string
		body    []byte
		headers map[string]string
	}{
		{
			name: "client metadata",
			body: []byte(`{"model":"gpt-5.4","input":"hello","client_metadata":{"session_id":"responses-metadata-session","thread_id":"responses-metadata-thread"}}`),
		},
		{
			name: "canonical headers",
			body: []byte(`{"model":"gpt-5.4","input":"hello"}`),
			headers: map[string]string{
				"session-id": "responses-header-session",
				"thread-id":  "responses-header-thread",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := newOpenAIIdentityPathContext(t, "/v1/responses", tt.body, 311)
			for key, value := range tt.headers {
				c.Request.Header.Set(key, value)
			}
			svc, cache := newOpenAIIdentityPathService(t, true, nil)
			account := newOpenAIIdentityPathOAuthAccount(910032)

			req, err := svc.buildUpstreamRequest(c.Request.Context(), c, account, tt.body, "oauth-token", true, "", false)
			require.NoError(t, err)
			requireOpenAIIdentityPathPair(t, req.Header, readOpenAIIdentityPathRequestBody(t, req))
			require.Equal(t, 2, identityPathCacheCalls(cache))
		})
	}
}

func TestOpenAIOutboundIdentityEnabledOAuthRejectsUnsafeSeedWithoutLegacyFallback(t *testing.T) {
	unsafeSeed := strings.Repeat("x", maxPersistedSessionIDLength+1)
	body := []byte(`{"model":"gpt-5.4","prompt_cache_key":"` + unsafeSeed + `","input":"hello"}`)

	for _, passthrough := range []bool{false, true} {
		t.Run(map[bool]string{false: "responses", true: "passthrough"}[passthrough], func(t *testing.T) {
			c, _ := newOpenAIIdentityPathContext(t, "/v1/responses", body, 310)
			svc, cache := newOpenAIIdentityPathService(t, true, nil)
			account := newOpenAIIdentityPathOAuthAccount(910031)

			var req *http.Request
			var err error
			if passthrough {
				account.Extra = map[string]any{"openai_passthrough": true}
				req, err = svc.buildUpstreamRequestOpenAIPassthrough(c.Request.Context(), c, account, body, "oauth-token")
			} else {
				req, err = svc.buildUpstreamRequest(c.Request.Context(), c, account, body, "oauth-token", true, unsafeSeed, false)
			}
			require.NoError(t, err)
			requireOpenAIIdentityPathNoIdentityHeaders(t, req.Header)
			require.Equal(t, 0, identityPathCacheCalls(cache))
		})
	}
}

func TestOpenAIOutboundIdentityPathsCompactUsesCanonicalPair(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled", true: "enabled"}[enabled], func(t *testing.T) {
			body := []byte(`{"model":"gpt-5.4","prompt_cache_key":"compact-path-key"}`)
			c, _ := newOpenAIIdentityPathContext(t, "/v1/responses/compact", body, 32)
			svc, cache := newOpenAIIdentityPathService(t, enabled, nil)
			account := newOpenAIIdentityPathOAuthAccount(910002)

			req, err := svc.buildUpstreamRequest(c.Request.Context(), c, account, body, "oauth-token", false, "compact-path-key", false)
			require.NoError(t, err)
			outboundBody := readOpenAIIdentityPathRequestBody(t, req)
			require.Empty(t, req.Header.Get("thread_id"))
			if enabled {
				sessionID := req.Header.Get("session-id")
				threadID := req.Header.Get("thread-id")
				require.Equal(t, sessionID, threadID)
				require.True(t, ValidateFingerprintObservationUUIDv7(sessionID))
				require.Empty(t, req.Header.Get("session_id"))
				require.Empty(t, req.Header.Get("conversation_id"))
				requireOpenAIIdentityPathContextWindowID(t, req.Header, outboundBody)
			} else {
				require.Empty(t, req.Header.Get("session-id"))
				require.Empty(t, req.Header.Get("thread-id"))
				require.Equal(t, isolateOpenAISessionID(32, "compact-path-key"), req.Header.Get("conversation_id"))
			}
			require.Empty(t, req.Header.Get("conversation-id"))
			require.Empty(t, req.Header.Get("x-client-request-id"))
			requireOpenAIIdentityPathNoBodyPair(t, outboundBody)
			if enabled {
				require.Equal(t, 1, identityPathCacheCalls(cache))
			} else {
				require.Equal(t, isolateOpenAISessionID(32, "compact-path-key"), req.Header.Get("session_id"))
				require.Equal(t, 0, identityPathCacheCalls(cache))
			}
		})
	}
}

func TestOpenAIOutboundIdentityPathsResponsesThenCompactReuseCanonicalIdentity(t *testing.T) {
	tests := []struct {
		name          string
		responsesBody []byte
		compactBody   []byte
		setupHeaders  func(http.Header)
		compactAlias  string
	}{
		{
			name:          "canonical tuple",
			responsesBody: []byte(`{"model":"gpt-5.4","stream":false,"input":"hello","client_metadata":{"session_id":"continuity-canonical","thread_id":"continuity-canonical"}}`),
			compactBody:   []byte(`{"model":"gpt-5.4","stream":false,"input":"hello","client_metadata":{"session_id":"continuity-canonical","thread_id":"continuity-canonical"}}`),
		},
		{
			name:          "prompt cache fallback",
			responsesBody: []byte(`{"model":"gpt-5.4","stream":false,"input":"hello","prompt_cache_key":"continuity-prompt"}`),
			compactBody:   []byte(`{"model":"gpt-5.4","stream":false,"input":"hello","prompt_cache_key":"continuity-prompt"}`),
			compactAlias:  "continuity-prompt",
		},
		{
			name:          "compact legacy alias",
			responsesBody: []byte(`{"model":"gpt-5.4","stream":false,"input":"hello"}`),
			compactBody:   []byte(`{"model":"gpt-5.4","stream":false,"input":"hello"}`),
			setupHeaders: func(headers http.Header) {
				headers.Set("session_id", "continuity-legacy")
			},
			compactAlias: "continuity-legacy",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upstream := &httpUpstreamRecorder{responses: []*http.Response{
				successfulInstallationTestResponse(),
				successfulInstallationTestResponse(),
			}}
			svc, _ := newOpenAIIdentityPathService(t, true, upstream)
			account := newOpenAIIdentityPathOAuthAccount(910073)
			account.Extra = map[string]any{openAIPinnedInstallationIDKey: transportTestPinnedInstallationID}

			responsesContext, _ := newOpenAIIdentityPathContext(t, "/v1/responses", tt.responsesBody, 73)
			if tt.setupHeaders != nil {
				tt.setupHeaders(responsesContext.Request.Header)
			}
			SetOpenAIOAuthIdentityCapture(responsesContext, CaptureOpenAIOAuthIdentity(responsesContext, tt.responsesBody, ""))
			result, err := svc.Forward(context.Background(), responsesContext, account, tt.responsesBody)
			require.NoError(t, err)
			require.NotNil(t, result)

			compactContext, _ := newOpenAIIdentityPathContext(t, "/v1/responses/compact", tt.compactBody, 73)
			if tt.setupHeaders != nil {
				tt.setupHeaders(compactContext.Request.Header)
			}
			SetOpenAIOAuthIdentityCapture(compactContext, CaptureOpenAIOAuthIdentityWithEndpointAlias(
				compactContext, tt.compactBody, tt.compactAlias,
			))
			result, err = svc.Forward(context.Background(), compactContext, account, tt.compactBody)
			require.NoError(t, err)
			require.NotNil(t, result)

			require.Len(t, upstream.requests, 2)
			require.Len(t, upstream.bodies, 2)
			responsesIdentity := requireOpenAIIdentityPathPair(t, upstream.requests[0].Header, upstream.bodies[0])
			responsesContextWindowID := requireOpenAIIdentityPathContextWindowID(t, upstream.requests[0].Header, upstream.bodies[0])
			compactIdentity := OpenAIOutboundSessionIdentity{
				SessionID: upstream.requests[1].Header.Get("session-id"),
				ThreadID:  upstream.requests[1].Header.Get("thread-id"),
			}
			require.NoError(t, ValidateOpenAIOutboundSessionIdentity(compactIdentity))
			require.Equal(t, responsesIdentity.SessionID, compactIdentity.SessionID)
			require.Equal(t, responsesIdentity.ThreadID, compactIdentity.ThreadID)
			compactContextWindowID := requireOpenAIIdentityPathContextWindowID(t, upstream.requests[1].Header, upstream.bodies[1])
			require.Equal(t, responsesContextWindowID, compactContextWindowID, "compact must use the current context window")
			requireOpenAIIdentityPathNoBodyPair(t, upstream.bodies[1])
			require.False(t, gjson.GetBytes(upstream.bodies[1], "client_metadata").Exists())

			for _, req := range upstream.requests {
				require.Equal(t, transportTestPinnedInstallationID, req.Header.Get(codexInstallationIDKey))
				require.Equal(t, openai.CodexDefaultOriginator, req.Header.Get("originator"))
				require.Equal(t, codexCLIVersion, req.Header.Get("version"))
				require.Equal(t, buildCodexCLIUserAgent(codexCLIVersion), req.Header.Get("user-agent"))
			}
		})
	}
}

func TestOpenAIOutboundIdentityPathsResponsesAndCompactObserveFinalTransport(t *testing.T) {
	tests := []struct {
		name        string
		path        string
		body        []byte
		apiKeyID    int64
		accountID   int64
		compactOnly bool
	}{
		{
			name:      "responses",
			path:      "/v1/responses",
			body:      []byte(`{"model":"gpt-5.4","stream":false,"input":"hello","prompt_cache_key":"responses-observation-key"}`),
			apiKeyID:  42,
			accountID: 910040,
		},
		{
			name:        "compact",
			path:        "/v1/responses/compact",
			body:        []byte(`{"model":"gpt-5.4","stream":false,"input":"hello","prompt_cache_key":"compact-observation-key"}`),
			apiKeyID:    43,
			accountID:   910041,
			compactOnly: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			enableOpenAIIdentityPathFingerprintObservation(t)
			c, _ := newOpenAIIdentityPathContext(t, tt.path, tt.body, tt.apiKeyID)
			upstream := &httpUpstreamRecorder{resp: successfulInstallationTestResponse()}
			svc, cache := newOpenAIIdentityPathService(t, true, upstream)
			account := newOpenAIIdentityPathOAuthAccount(tt.accountID)

			result, err := svc.Forward(context.Background(), c, account, tt.body)
			require.NoError(t, err)
			require.NotNil(t, result)
			require.NotNil(t, upstream.lastReq)

			identity := OpenAIOutboundSessionIdentity{}
			if tt.compactOnly {
				identity.SessionID = upstream.lastReq.Header.Get("session-id")
				identity.ThreadID = upstream.lastReq.Header.Get("thread-id")
				identity.Relation = OpenAICodexTurnRelationRoot
				require.True(t, ValidateFingerprintObservationUUIDv7(identity.SessionID))
				require.Equal(t, identity.SessionID, identity.ThreadID)
				requireOpenAIIdentityPathNoBodyPair(t, upstream.lastBody)
			} else {
				identity = requireOpenAIIdentityPathPair(t, upstream.lastReq.Header, upstream.lastBody)
			}
			requireOpenAIIdentityPathSingleFingerprintObservation(t, identity, http.MethodPost+" "+tt.path)
			require.Equal(t, 1, identityPathCacheCalls(cache))
		})
	}
}

func TestOpenAIOutboundIdentityPathsOAuthWSObservesFinalHandshakePair(t *testing.T) {
	enableOpenAIIdentityPathFingerprintObservation(t)
	c, _ := newOpenAIIdentityPathContext(t, "/v1/responses", nil, 44)
	c.Request.Method = http.MethodGet
	c.Request.Header.Set("session_id", "ws-observation-key")
	svc, cache := newOpenAIIdentityPathService(t, true, nil)
	account := newOpenAIIdentityPathOAuthAccount(910042)

	headers, resolution, err := svc.buildOpenAIWSHeadersWithBody(
		context.Background(),
		c,
		account,
		"oauth-token",
		OpenAIWSProtocolDecision{Transport: OpenAIUpstreamTransportResponsesWebsocketV2},
		true,
		"",
		"",
		"",
		[]byte(`{"model":"gpt-5.4"}`),
		true,
	)
	require.NoError(t, err)
	require.True(t, resolution.OutboundIdentityEnabled)
	require.NoError(t, ValidateOpenAIOutboundSessionIdentity(resolution.OutboundIdentity))
	require.Equal(t, resolution.OutboundIdentity.SessionID, headers.Get("session-id"))
	require.Equal(t, resolution.OutboundIdentity.ThreadID, headers.Get("thread-id"))
	requireOpenAIIdentityPathContextWindowID(t, headers, nil)
	require.Empty(t, SnapshotFingerprintObservations(0), "building candidate headers is not a successful upstream handshake")
	// Header construction alone is not a physical handshake. Simulate the
	// successful non-reused lease boundary used by the production WS forwarders.
	lease := &openAIWSConnLease{
		conn:   newOpenAIWSConn("test-handshake", account.ID, nil, nil),
		reused: false,
	}
	svc.recordFingerprintObservationAfterOpenAIWSHandshake(c, account, lease, headers)
	requireOpenAIIdentityPathSingleFingerprintObservation(t, resolution.OutboundIdentity, http.MethodGet+" /v1/responses")
	reusedLease := &openAIWSConnLease{
		conn:   newOpenAIWSConn("test-reused-handshake", account.ID, nil, nil),
		reused: true,
	}
	svc.recordFingerprintObservationAfterOpenAIWSHandshake(c, account, reusedLease, headers)
	require.Len(t, SnapshotFingerprintObservations(0), 1, "pool reuse must not duplicate a physical handshake observation")
	require.Equal(t, 1, identityPathCacheCalls(cache))
}

func TestOpenAIOutboundIdentityPathsAPIKeyTransportIsNotObserved(t *testing.T) {
	enableOpenAIIdentityPathFingerprintObservation(t)
	body := []byte(`{"model":"gpt-5.4","stream":false,"input":"hello","prompt_cache_key":"apikey-observation-key"}`)
	c, _ := newOpenAIIdentityPathContext(t, "/v1/responses", body, 45)
	upstream := &httpUpstreamRecorder{resp: successfulInstallationTestResponse()}
	svc, cache := newOpenAIIdentityPathService(t, true, upstream)
	account := newOpenAIIdentityPathAPIKeyAccount(910043)

	result, err := svc.Forward(context.Background(), c, account, body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, upstream.lastReq)
	require.Empty(t, SnapshotFingerprintObservations(0))
	require.Equal(t, 0, identityPathCacheCalls(cache))
}

func TestOpenAIOutboundIdentityPlanReusedAcrossSameAccountTransportRetry(t *testing.T) {
	tests := []struct {
		name                string
		path                string
		body                []byte
		setup               func(*gin.Context)
		configure           func(*Account)
		response            func() *http.Response
		invoke              func(*OpenAIGatewayService, *gin.Context, *Account, []byte) error
		assertPair          func(*testing.T, *http.Request, []byte) OpenAIOutboundSessionIdentity
		expectContextWindow bool
	}{
		{
			name: "responses", path: "/v1/responses",
			body:     []byte(`{"model":"gpt-5.4","stream":false,"input":"hello","prompt_cache_key":"same-account-retry"}`),
			response: successfulInstallationTestResponse,
			invoke: func(svc *OpenAIGatewayService, c *gin.Context, account *Account, body []byte) error {
				_, err := svc.Forward(context.Background(), c, account, body)
				return err
			},
			assertPair: requireOpenAIIdentityPathRequestPair, expectContextWindow: true,
		},
		{
			name: "passthrough", path: "/v1/responses",
			body: []byte(`{"model":"gpt-5.4","stream":true,"input":"hello","prompt_cache_key":"same-account-retry"}`),
			configure: func(account *Account) {
				account.Extra = map[string]any{"openai_passthrough": true}
			},
			response: func() *http.Response {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
					Body:       io.NopCloser(strings.NewReader("data: [DONE]\n\n")),
				}
			},
			invoke: func(svc *OpenAIGatewayService, c *gin.Context, account *Account, body []byte) error {
				_, err := svc.Forward(context.Background(), c, account, body)
				return err
			},
			assertPair: requireOpenAIIdentityPathRequestPair, expectContextWindow: true,
		},
		{
			name: "chat", path: "/v1/chat/completions",
			body: []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}],"stream":false}`),
			response: func() *http.Response {
				return openAICompatSSECompletedResponse("resp_same_account_chat", "gpt-4o")
			},
			invoke: func(svc *OpenAIGatewayService, c *gin.Context, account *Account, body []byte) error {
				_, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body, "same-account-retry", "gpt-4o")
				return err
			},
			assertPair: requireOpenAIIdentityPathRequestPair, expectContextWindow: true,
		},
		{
			name: "messages", path: "/v1/messages",
			body: []byte(`{"model":"gpt-4o","max_tokens":16,"messages":[{"role":"user","content":"hello"}],"stream":false}`),
			response: func() *http.Response {
				return openAICompatSSECompletedResponse("resp_same_account_messages", "gpt-4o")
			},
			invoke: func(svc *OpenAIGatewayService, c *gin.Context, account *Account, body []byte) error {
				_, err := svc.ForwardAsAnthropic(context.Background(), c, account, body, "same-account-retry", "gpt-4o")
				return err
			},
			assertPair: requireOpenAIIdentityPathRequestPair, expectContextWindow: true,
		},
		{
			name: "alpha", path: "/v1/alpha/search",
			body: []byte(`{"id":"same-account-retry","model":"gpt-5.4","commands":{"search_query":[{"q":"hello"}]}}`),
			setup: func(c *gin.Context) {
				c.Request.Header.Set(openAIWSTurnMetadataHeader, `{"session_id":"same-account-retry","thread_id":"same-account-retry"}`)
			},
			response: func() *http.Response {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(`{"output":"ok"}`)),
				}
			},
			invoke: func(svc *OpenAIGatewayService, c *gin.Context, account *Account, body []byte) error {
				_, err := svc.ForwardAlphaSearch(context.Background(), c, account, body)
				return err
			},
			assertPair: func(t *testing.T, req *http.Request, _ []byte) OpenAIOutboundSessionIdentity {
				t.Helper()
				metadata := req.Header.Get(openAIWSTurnMetadataHeader)
				identity := OpenAIOutboundSessionIdentity{
					SessionID: gjson.Get(metadata, "session_id").String(),
					ThreadID:  gjson.Get(metadata, "thread_id").String(),
				}
				require.NoError(t, ValidateOpenAIOutboundSessionIdentity(identity))
				return identity
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := newOpenAIIdentityPathContext(t, tt.path, tt.body, 450)
			if tt.setup != nil {
				tt.setup(c)
			}
			upstream := &httpUpstreamRecorder{responses: []*http.Response{tt.response(), tt.response()}}
			svc, cache := newOpenAIIdentityPathService(t, true, upstream)
			account := newOpenAIIdentityPathOAuthAccount(910050)
			if tt.configure != nil {
				tt.configure(account)
			}

			require.NoError(t, tt.invoke(svc, c, account, tt.body))
			require.Len(t, upstream.requests, 1)
			firstIdentity := tt.assertPair(t, upstream.requests[0], upstream.bodies[0])
			firstContextWindowID := ""
			if tt.expectContextWindow {
				firstContextWindowID = requireOpenAIIdentityPathContextWindowID(t, upstream.requests[0].Header, upstream.bodies[0])
			}
			firstStoreCalls := identityPathCacheCalls(cache)
			require.Positive(t, firstStoreCalls)

			require.NoError(t, tt.invoke(svc, c, account, tt.body))
			require.Len(t, upstream.requests, 2)
			secondIdentity := tt.assertPair(t, upstream.requests[1], upstream.bodies[1])
			require.Equal(t, firstIdentity, secondIdentity)
			if tt.expectContextWindow {
				require.Equal(t, firstContextWindowID,
					requireOpenAIIdentityPathContextWindowID(t, upstream.requests[1].Header, upstream.bodies[1]),
					"same physical request retry must keep context_window_id",
				)
			}
			require.Equal(t, firstStoreCalls, identityPathCacheCalls(cache), "same-account retry must not rematerialize the V2 plan")
		})
	}
}

func TestOpenAIOutboundIdentityPathsOAuthPassthrough(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled", true: "enabled"}[enabled], func(t *testing.T) {
			body := []byte(`{"model":"gpt-5.4","prompt_cache_key":"passthrough-path-key","input":"hello"}`)
			c, _ := newOpenAIIdentityPathContext(t, "/v1/responses", body, 33)
			svc, cache := newOpenAIIdentityPathService(t, enabled, nil)
			account := newOpenAIIdentityPathOAuthAccount(910003)
			account.Extra = map[string]any{"openai_passthrough": true}

			req, err := svc.buildUpstreamRequestOpenAIPassthrough(c.Request.Context(), c, account, body, "oauth-token")
			require.NoError(t, err)
			outboundBody := readOpenAIIdentityPathRequestBody(t, req)
			if enabled {
				requireOpenAIIdentityPathPair(t, req.Header, outboundBody)
			} else {
				legacy := isolateOpenAISessionID(33, "passthrough-path-key")
				require.Equal(t, legacy, req.Header.Get("session_id"))
				require.Equal(t, legacy, req.Header.Get("conversation_id"))
				requireOpenAIIdentityPathNoBodyPair(t, outboundBody)
			}
			require.Equal(t, map[bool]int{false: 0, true: 1}[enabled], identityPathCacheCalls(cache))
		})
	}
}

func TestOpenAIOutboundIdentityPathsOAuthPassthroughObservesEachPhysicalSend(t *testing.T) {
	enableOpenAIIdentityPathFingerprintObservation(t)
	body := []byte(`{"model":"gpt-5.4","stream":true,"prompt_cache_key":"passthrough-observation-key","input":"hello","client_metadata":{"x-codex-installation-id":"client-body-installation"}}`)
	c, _ := newOpenAIIdentityPathContext(t, "/v1/responses", body, 334)
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid-passthrough-observation"}},
		Body:       io.NopCloser(strings.NewReader("data: [DONE]\n\n")),
	}}
	svc, _ := newOpenAIIdentityPathService(t, true, upstream)
	account := newOpenAIIdentityPathOAuthAccount(910034)
	account.Extra = map[string]any{"openai_passthrough": true}

	result, err := svc.Forward(context.Background(), c, account, body)
	require.NoError(t, err)
	require.NotNil(t, result)
	identity := requireOpenAIIdentityPathPair(t, upstream.lastReq.Header, upstream.lastBody)
	entry := requireOpenAIIdentityPathSingleFingerprintObservation(t, identity, http.MethodPost+" /v1/responses")
	require.True(t, entry.Pinned)
	require.Equal(t, "client-body-installation", entry.ClientReportedInstallationID)
	require.NotEqual(t, "client-body-installation", entry.OutboundInstallationID)
	require.Equal(t, upstream.lastReq.Header.Get(codexInstallationIDKey), entry.OutboundInstallationID)
}

func TestOpenAIOutboundIdentityPathsOAuthPassthroughUsesMetadataWithoutLegacySeed(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","input":"hello","client_metadata":{"session_id":"passthrough-metadata-session","thread_id":"passthrough-metadata-thread"}}`)
	c, _ := newOpenAIIdentityPathContext(t, "/v1/responses", body, 331)
	svc, cache := newOpenAIIdentityPathService(t, true, nil)
	account := newOpenAIIdentityPathOAuthAccount(910033)
	account.Extra = map[string]any{"openai_passthrough": true}

	req, err := svc.buildUpstreamRequestOpenAIPassthrough(c.Request.Context(), c, account, body, "oauth-token")
	require.NoError(t, err)
	requireOpenAIIdentityPathPair(t, req.Header, readOpenAIIdentityPathRequestBody(t, req))
	require.Equal(t, 2, identityPathCacheCalls(cache))
}

func TestOpenAIHTTPIdentityBuildersGuardCompositeTurnStateAfterProjection(t *testing.T) {
	for _, tc := range []struct {
		name        string
		passthrough bool
	}{
		{name: "regular"},
		{name: "passthrough", passthrough: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetTurnStateLocalStore(t, 32)
			svc, _ := newOpenAIIdentityPathService(t, true, nil)
			account := newOpenAIIdentityPathOAuthAccount(920100)
			if tc.passthrough {
				account.Extra = map[string]any{"openai_passthrough": true}
			}
			build := func(c *gin.Context, body []byte) (*http.Request, error) {
				if tc.passthrough {
					return svc.buildUpstreamRequestOpenAIPassthrough(c.Request.Context(), c, account, body, "oauth-token")
				}
				return svc.buildUpstreamRequest(c.Request.Context(), c, account, body, "oauth-token", true, "guard-session", true)
			}

			firstBody := []byte(`{"model":"gpt-5.4","input":"hello","client_metadata":{"session_id":"guard-session","thread_id":"guard-session","keep":{"nested":true},"x-codex-turn-state":"known-state","x-codex-turn-metadata":"{\"turn_id\":\"` + turnStateTurnA + `\"}"}}`)
			c, _ := newOpenAIIdentityPathContext(t, "/v1/responses", firstBody, 707)
			c.Request.Header.Set(openAICodexTurnStateHeader, "known-state")
			firstReq, err := build(c, firstBody)
			require.NoError(t, err)
			firstOutboundBody := readOpenAIIdentityPathRequestBody(t, firstReq)
			require.Equal(t, "known-state", gjson.GetBytes(firstOutboundBody, "client_metadata.x-codex-turn-state").String())
			firstPlan, ok := OpenAIOAuthIdentityPlanFromContext(c)
			require.True(t, ok)
			svc.noteOpenAICodexTurnStateProvenanceForPlan(c, account, "known-state", firstPlan)

			mismatchBody := []byte(`{"sequence":9007199254740993,"model":"gpt-5.4","input":"hello","client_metadata":{"session_id":"guard-session","thread_id":"guard-session","keep":{"nested":true},"x-codex-turn-state":"known-state","x-codex-turn-metadata":"{\"turn_id\":\"` + turnStateTurnB + `\"}"},"tail":"preserved"}`)
			SetOpenAIOAuthIdentityCapture(c, CaptureOpenAIOAuthIdentity(c, mismatchBody, ""))
			c.Request.Header.Set(openAICodexTurnStateHeader, "known-state")
			mismatchReq, err := build(c, mismatchBody)
			require.NoError(t, err)
			mismatchOutboundBody := readOpenAIIdentityPathRequestBody(t, mismatchReq)
			require.Empty(t, mismatchReq.Header.Get(openAICodexTurnStateHeader))
			require.False(t, gjson.GetBytes(mismatchOutboundBody, "client_metadata.x-codex-turn-state").Exists())
			require.True(t, gjson.GetBytes(mismatchOutboundBody, "client_metadata.keep.nested").Bool())
			require.Equal(t, "preserved", gjson.GetBytes(mismatchOutboundBody, "tail").String())
			require.Contains(t, string(mismatchOutboundBody), "9007199254740993")
			require.Equal(t, int64(len(mismatchOutboundBody)), mismatchReq.ContentLength)
			replay, replayErr := mismatchReq.GetBody()
			require.NoError(t, replayErr)
			replayedBody, replayErr := io.ReadAll(replay)
			require.NoError(t, replayErr)
			require.NoError(t, replay.Close())
			require.Equal(t, mismatchOutboundBody, replayedBody)

			unknownBody := []byte(`{"sequence":9007199254740993,"model":"gpt-5.4","client_metadata":{"session_id":"guard-session","thread_id":"guard-session","keep":{"nested":true},"x-codex-turn-state":"external-state","x-codex-turn-metadata":"{\"turn_id\":\"` + turnStateTurnB + `\"}"},"tail":"preserved"}`)
			SetOpenAIOAuthIdentityCapture(c, CaptureOpenAIOAuthIdentity(c, unknownBody, ""))
			c.Request.Header.Set(openAICodexTurnStateHeader, "external-state")
			unknownReq, err := build(c, unknownBody)
			require.NoError(t, err)
			unknownOutboundBody := readOpenAIIdentityPathRequestBody(t, unknownReq)
			require.Equal(t, "external-state", unknownReq.Header.Get(openAICodexTurnStateHeader))
			require.Equal(t, "external-state", gjson.GetBytes(unknownOutboundBody, "client_metadata.x-codex-turn-state").String())
			require.True(t, gjson.GetBytes(unknownOutboundBody, "client_metadata.keep.nested").Bool())
			require.Equal(t, "preserved", gjson.GetBytes(unknownOutboundBody, "tail").String())
			require.Contains(t, string(unknownOutboundBody), "9007199254740993")
		})
	}
}

func TestOpenAIOutboundIdentityPassthroughPinsInstallationAcrossCarriers(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","input":"hello","prompt_cache_key":"passthrough-install-session","client_metadata":{"x-codex-installation-id":"client-body-install","x-codex-turn-metadata":"{\"installation_id\":\"client-nested-install\",\"label\":\"keep\"}"}}`)
	c, _ := newOpenAIIdentityPathContext(t, "/v1/responses", body, 332)
	c.Request.Header.Set(codexInstallationIDKey, "client-header-install")
	c.Request.Header.Set(openAIWSTurnMetadataHeader, `{"installation_id":"client-header-nested","label":"keep"}`)
	svc, _ := newOpenAIIdentityPathService(t, true, nil)
	account := newOpenAIIdentityPathOAuthAccount(910035)
	account.Extra = map[string]any{
		"openai_passthrough":          true,
		openAIPinnedInstallationIDKey: "11111111-2222-4333-8444-555555555555",
	}

	req, err := svc.buildUpstreamRequestOpenAIPassthrough(c.Request.Context(), c, account, body, "oauth-token")
	require.NoError(t, err)
	outboundBody := readOpenAIIdentityPathRequestBody(t, req)
	const pinnedInstallationID = "11111111-2222-4333-8444-555555555555"
	require.Equal(t, pinnedInstallationID, req.Header.Get(codexInstallationIDKey))
	headerMetadata := req.Header.Get(openAIWSTurnMetadataHeader)
	require.Equal(t, pinnedInstallationID, gjson.Get(headerMetadata, "installation_id").String())
	require.Equal(t, "keep", gjson.Get(headerMetadata, "label").String())
	require.Equal(t, pinnedInstallationID, gjson.GetBytes(outboundBody, "client_metadata.x-codex-installation-id").String())
	bodyMetadata := gjson.GetBytes(outboundBody, "client_metadata.x-codex-turn-metadata").String()
	require.Equal(t, pinnedInstallationID, gjson.Get(bodyMetadata, "installation_id").String())
	require.Equal(t, "keep", gjson.Get(bodyMetadata, "label").String())
	plan, ok := OpenAIOAuthIdentityPlanFromContext(c)
	require.True(t, ok)
	require.Equal(t, OpenAIOAuthIdentityProjectionPassthrough, plan.ProjectionMode)
	require.Equal(t, OpenAIOAuthInstallationAccountPin, plan.InstallationPolicy)
}

func TestOpenAIOutboundIdentityPassthroughCompactPinsHeaderAndStripsClientMetadata(t *testing.T) {
	const pinnedInstallationID = "11111111-2222-4333-8444-555555555555"
	body := []byte(`{"model":"gpt-5.4","prompt_cache_key":"passthrough-compact-session","client_metadata":{"x-codex-installation-id":"client-body-install","keep":"remove"}}`)
	c, _ := newOpenAIIdentityPathContext(t, "/v1/responses/compact", body, 333)
	c.Request.Header.Set(codexInstallationIDKey, "client-header-install")
	svc, _ := newOpenAIIdentityPathService(t, true, nil)
	account := newOpenAIIdentityPathOAuthAccount(910036)
	account.Extra = map[string]any{
		"openai_passthrough":          true,
		openAIPinnedInstallationIDKey: pinnedInstallationID,
	}

	req, err := svc.buildUpstreamRequestOpenAIPassthrough(c.Request.Context(), c, account, body, "oauth-token")
	require.NoError(t, err)
	outboundBody := readOpenAIIdentityPathRequestBody(t, req)
	require.Equal(t, pinnedInstallationID, req.Header.Get(codexInstallationIDKey))
	require.True(t, ValidateFingerprintObservationUUIDv7(req.Header.Get("session-id")))
	require.Equal(t, req.Header.Get("session-id"), req.Header.Get("thread-id"))
	require.Empty(t, req.Header.Get("x-client-request-id"))
	require.False(t, gjson.GetBytes(outboundBody, "client_metadata").Exists())
	plan, ok := OpenAIOAuthIdentityPlanFromContext(c)
	require.True(t, ok)
	require.Equal(t, OpenAIOAuthIdentityProjectionCompact, plan.ProjectionMode)
	require.Equal(t, OpenAIOAuthInstallationAccountPin, plan.InstallationPolicy)
}

func TestOpenAIOutboundIdentityPassthroughConversationHeaderKeepsPairSeed(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","prompt_cache_key":"passthrough-prompt","input":"hello"}`)
	c, _ := newOpenAIIdentityPathContext(t, "/v1/responses", body, 40)
	c.Request.Header.Set("conversation_id", "passthrough-conversation")
	svc, cache := newOpenAIIdentityPathService(t, true, nil)
	account := newOpenAIIdentityPathOAuthAccount(910006)

	req, err := svc.buildUpstreamRequestOpenAIPassthrough(c.Request.Context(), c, account, body, "oauth-token")
	require.NoError(t, err)
	requireOpenAIIdentityPathPair(t, req.Header, readOpenAIIdentityPathRequestBody(t, req))

	cache.mu.Lock()
	require.Len(t, cache.mappingKeys, 1)
	mappingKey := cache.mappingKeys[0]
	cache.mu.Unlock()
	expected, err := OpenAIOutboundSessionIdentityKey(
		"transport-identity-test-secret",
		"account:910006",
		40,
		"passthrough-conversation",
	)
	require.NoError(t, err)
	require.Equal(t, expected, mappingKey)
}

func TestOpenAIOutboundIdentityPassthroughInvalidPrimarySeedUsesValidConversationTuple(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","prompt_cache_key":"passthrough-prompt","input":"hello"}`)
	c, _ := newOpenAIIdentityPathContext(t, "/v1/responses", body, 41)
	c.Request.Header.Set("session_id", "\x01invalid-session")
	c.Request.Header.Set("conversation_id", "valid-conversation")
	svc, cache := newOpenAIIdentityPathService(t, true, nil)
	account := newOpenAIIdentityPathOAuthAccount(910007)

	req, err := svc.buildUpstreamRequestOpenAIPassthrough(c.Request.Context(), c, account, body, "oauth-token")
	require.NoError(t, err)
	outboundBody := readOpenAIIdentityPathRequestBody(t, req)
	requireOpenAIIdentityPathPair(t, req.Header, outboundBody)
	require.Equal(t, 1, identityPathCacheCalls(cache))
}

func TestOpenAIOutboundIdentityPathsAPIKeyPassthroughIsUntouched(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","prompt_cache_key":"apikey-passthrough-key","input":"hello"}`)
	c, _ := newOpenAIIdentityPathContext(t, "/v1/responses", body, 34)
	svc, cache := newOpenAIIdentityPathService(t, true, nil)
	account := newOpenAIIdentityPathAPIKeyAccount(910004)

	req, err := svc.buildUpstreamRequestOpenAIPassthrough(c.Request.Context(), c, account, body, "api-key-token")
	require.NoError(t, err)
	outboundBody := readOpenAIIdentityPathRequestBody(t, req)
	requireOpenAIIdentityPathNoIdentityHeaders(t, req.Header)
	require.Equal(t, "apikey-passthrough-key", gjson.GetBytes(outboundBody, "prompt_cache_key").String())
	requireOpenAIIdentityPathNoBodyPair(t, outboundBody)
	require.Equal(t, 0, identityPathCacheCalls(cache))
}

func TestOpenAIOutboundIdentityPathsNormalAPIKeyResponsesIsUntouched(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","prompt_cache_key":"apikey-responses-key","input":"hello"}`)
	c, _ := newOpenAIIdentityPathContext(t, "/v1/responses", body, 35)
	svc, cache := newOpenAIIdentityPathService(t, true, nil)
	account := newOpenAIIdentityPathAPIKeyAccount(910005)

	req, err := svc.buildUpstreamRequest(c.Request.Context(), c, account, body, "api-key-token", true, "apikey-responses-key", false)
	require.NoError(t, err)
	outboundBody := readOpenAIIdentityPathRequestBody(t, req)
	requireOpenAIIdentityPathNoIdentityHeaders(t, req.Header)
	requireOpenAIIdentityPathNoBodyPair(t, outboundBody)
	require.Equal(t, 0, identityPathCacheCalls(cache))
}

func TestOpenAIOutboundIdentityPathsChatAndMessagesUseOneFinalPair(t *testing.T) {
	for _, route := range []string{"chat", "messages"} {
		for _, enabled := range []bool{false, true} {
			t.Run(route+map[bool]string{false: "_disabled", true: "_enabled"}[enabled], func(t *testing.T) {
				enableOpenAIIdentityPathFingerprintObservation(t)
				var body []byte
				var c *gin.Context
				var apiKeyID int64
				var endpoint string
				if route == "chat" {
					body = []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hello"}],"stream":false}`)
					apiKeyID = 36
					endpoint = "/v1/chat/completions"
					c, _ = newOpenAIIdentityPathContext(t, endpoint, body, apiKeyID)
				} else {
					body = []byte(`{"model":"claude-sonnet-4-5","max_tokens":16,"messages":[{"role":"user","content":"hello"}],"stream":false}`)
					apiKeyID = 37
					endpoint = "/v1/messages"
					c, _ = newOpenAIIdentityPathContext(t, endpoint, body, apiKeyID)
				}
				upstream := &httpUpstreamRecorder{resp: openAICompatSSECompletedResponse("resp_identity_path_"+route, "gpt-5.4")}
				svc, cache := newOpenAIIdentityPathService(t, enabled, upstream)
				account := newOpenAIIdentityPathOAuthAccount(910010 + int64(len(route)))
				var err error
				if route == "chat" {
					_, err = svc.ForwardAsChatCompletions(context.Background(), c, account, body, "compat-path-key", "gpt-5.4")
				} else {
					_, err = svc.ForwardAsAnthropic(context.Background(), c, account, body, "compat-path-key", "gpt-5.4")
				}
				require.NoError(t, err)
				require.NotNil(t, upstream.lastReq)
				if enabled {
					identity := requireOpenAIIdentityPathPair(t, upstream.lastReq.Header, upstream.lastBody)
					requireOpenAIIdentityPathSingleFingerprintObservation(t, identity, http.MethodPost+" "+endpoint)
				} else {
					require.Equal(t, generateSessionUUID(isolateOpenAISessionID(apiKeyID, "compat-path-key")), upstream.lastReq.Header.Get("session_id"))
					requireOpenAIIdentityPathNoBodyPair(t, upstream.lastBody)
					entries := SnapshotFingerprintObservations(0)
					require.Len(t, entries, 1)
					require.Empty(t, entries[0].SessionID)
					require.Empty(t, entries[0].ThreadID)
					require.Equal(t, http.MethodPost+" "+endpoint, entries[0].InboundEndpoint)
				}
				require.Equal(t, map[bool]int{false: 0, true: 1}[enabled], identityPathCacheCalls(cache))
			})
		}
	}
}

func TestOpenAIOutboundIdentityCompatUsesPreConversionTuple(t *testing.T) {
	const logicalSession = "compat-pre-conversion-root"
	turnMetadata := `{"session_id":"` + logicalSession + `","thread_id":"` + logicalSession + `"}`

	for _, route := range []string{"chat", "messages"} {
		t.Run(route, func(t *testing.T) {
			var body []byte
			var endpoint string
			var apiKeyID int64
			encodedTurnMetadata := string(mustMarshalJSONString(turnMetadata))
			if route == "chat" {
				body = []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hello"}],"stream":false,"client_metadata":{"x-codex-turn-metadata":` + encodedTurnMetadata + `}}`)
				endpoint = "/v1/chat/completions"
				apiKeyID = 360
			} else {
				body = []byte(`{"model":"claude-sonnet-4-5","max_tokens":16,"messages":[{"role":"user","content":"hello"}],"stream":false,"client_metadata":{"x-codex-turn-metadata":` + encodedTurnMetadata + `}}`)
				endpoint = "/v1/messages"
				apiKeyID = 370
			}
			c, _ := newOpenAIIdentityPathContext(t, endpoint, body, apiKeyID)
			upstream := &httpUpstreamRecorder{resp: openAICompatSSECompletedResponse("resp_pre_conversion_"+route, "gpt-5.4")}
			svc, cache := newOpenAIIdentityPathService(t, true, upstream)
			account := newOpenAIIdentityPathOAuthAccount(910050 + int64(len(route)))

			var err error
			if route == "chat" {
				_, err = svc.ForwardAsChatCompletions(context.Background(), c, account, body, "compat-fallback", "gpt-5.4")
			} else {
				_, err = svc.ForwardAsAnthropic(context.Background(), c, account, body, "compat-fallback", "gpt-5.4")
			}
			require.NoError(t, err)
			require.NotNil(t, upstream.lastReq)
			requireOpenAIIdentityPathPair(t, upstream.lastReq.Header, upstream.lastBody)

			expected, keyErr := OpenAICodexSessionMappingKey(
				"transport-identity-test-secret",
				openAIOutboundSessionIdentityNamespace(account),
				apiKeyID,
				logicalSession,
			)
			require.NoError(t, keyErr)
			cache.mu.Lock()
			require.Contains(t, cache.mappingKeys, expected)
			cache.mu.Unlock()
		})
	}
}

func TestOpenAIOutboundIdentityCompatUsesExplicitTupleWithoutPromptKey(t *testing.T) {
	const turnMetadata = `{"session_id":"compat-no-prompt-session","thread_id":"compat-no-prompt-thread"}`
	encodedTurnMetadata := string(mustMarshalJSONString(turnMetadata))

	for _, route := range []string{"chat", "messages"} {
		t.Run(route, func(t *testing.T) {
			var body []byte
			var endpoint string
			if route == "chat" {
				body = []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}],"stream":false,"client_metadata":{"x-codex-turn-metadata":` + encodedTurnMetadata + `}}`)
				endpoint = "/v1/chat/completions"
			} else {
				body = []byte(`{"model":"gpt-4o","max_tokens":16,"messages":[{"role":"user","content":"hello"}],"stream":false,"client_metadata":{"x-codex-turn-metadata":` + encodedTurnMetadata + `}}`)
				endpoint = "/v1/messages"
			}
			c, _ := newOpenAIIdentityPathContext(t, endpoint, body, 371)
			upstream := &httpUpstreamRecorder{resp: openAICompatSSECompletedResponse("resp_no_prompt_"+route, "gpt-4o")}
			svc, cache := newOpenAIIdentityPathService(t, true, upstream)
			account := newOpenAIIdentityPathOAuthAccount(910051 + int64(len(route)))

			var err error
			if route == "chat" {
				_, err = svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "gpt-4o")
			} else {
				_, err = svc.ForwardAsAnthropic(context.Background(), c, account, body, "", "gpt-4o")
			}
			require.NoError(t, err)
			require.NotNil(t, upstream.lastReq)
			requireOpenAIIdentityPathPair(t, upstream.lastReq.Header, upstream.lastBody)
			require.Equal(t, 2, identityPathCacheCalls(cache))
		})
	}
}

func TestOpenAIOutboundIdentityPathsAPIKeyCompatibilityKeepsHistoricalCoverage(t *testing.T) {
	for _, route := range []string{"chat", "messages"} {
		t.Run(route, func(t *testing.T) {
			enableOpenAIIdentityPathFingerprintObservation(t)
			var body []byte
			var c *gin.Context
			var endpoint string
			var apiKeyID int64
			if route == "chat" {
				body = []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hello"}],"stream":false}`)
				endpoint = "/v1/chat/completions"
				apiKeyID = 47
				c, _ = newOpenAIIdentityPathContext(t, endpoint, body, apiKeyID)
			} else {
				body = []byte(`{"model":"gpt-5.4","max_tokens":16,"messages":[{"role":"user","content":"hello"}],"stream":false}`)
				endpoint = "/v1/messages"
				apiKeyID = 48
				c, _ = newOpenAIIdentityPathContext(t, endpoint, body, apiKeyID)
			}

			upstream := &httpUpstreamRecorder{resp: openAICompatSSECompletedResponse("resp_identity_apikey_"+route, "gpt-5.4")}
			svc, cache := newOpenAIIdentityPathService(t, true, upstream)
			account := newOpenAIIdentityPathAPIKeyAccount(910050 + int64(len(route)))
			var err error
			if route == "chat" {
				_, err = svc.ForwardAsChatCompletions(context.Background(), c, account, body, "apikey-compat-key", "gpt-5.4")
			} else {
				_, err = svc.ForwardAsAnthropic(context.Background(), c, account, body, "apikey-compat-key", "gpt-5.4")
			}

			require.NoError(t, err)
			require.NotNil(t, upstream.lastReq)
			require.Equal(t, generateSessionUUID(isolateOpenAISessionID(apiKeyID, "apikey-compat-key")), upstream.lastReq.Header.Get("session_id"))
			require.Empty(t, upstream.lastReq.Header.Get("session-id"))
			require.Empty(t, upstream.lastReq.Header.Get("thread-id"))
			requireOpenAIIdentityPathNoBodyPair(t, upstream.lastBody)
			require.Equal(t, 0, identityPathCacheCalls(cache))
			require.Empty(t, SnapshotFingerprintObservations(0))
		})
	}
}

func TestOpenAIOutboundIdentityPathsDisabledCompatDoesNotObserveClientUUIDv7(t *testing.T) {
	enableOpenAIIdentityPathFingerprintObservation(t)
	body := []byte(`{"model":"gpt-4o","stream":false,"input":[{"role":"user","content":[{"type":"input_text","text":"hello"}]}],"prompt_cache_key":"client-observation-key","client_metadata":{"session_id":"` + fingerprintObserverSessionV7 + `","thread_id":"` + fingerprintObserverThreadV7 + `"}}`)
	c, _ := newOpenAIIdentityPathContext(t, "/v1/chat/completions", body, 46)
	// Simulate a retry on a context that previously carried a server-owned pair.
	// The disabled build must clear this marker before observing the final wire.
	setFingerprintObservationOutboundIdentity(c, OpenAIOutboundSessionIdentity{
		SessionID: fingerprintObserverSessionV7,
		ThreadID:  fingerprintObserverThreadV7,
	})
	upstream := &httpUpstreamRecorder{resp: openAICompatSSECompletedResponse("resp_identity_client_uuid", "gpt-4o")}
	svc, cache := newOpenAIIdentityPathService(t, false, upstream)
	account := newOpenAIIdentityPathOAuthAccount(910044)

	_, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body, "client-observation-key", "gpt-4o")
	require.NoError(t, err)
	require.NotNil(t, upstream.lastReq)
	require.Equal(t, fingerprintObserverThreadV7, gjson.GetBytes(upstream.lastBody, "client_metadata.thread_id").String())

	entries := SnapshotFingerprintObservations(0)
	require.Len(t, entries, 1)
	require.Empty(t, entries[0].SessionID)
	require.Empty(t, entries[0].ThreadID)
	require.Equal(t, http.MethodPost+" /v1/chat/completions", entries[0].InboundEndpoint)
	require.Equal(t, 0, identityPathCacheCalls(cache))
}

func TestOpenAIOutboundIdentityPathsCompatWithoutPromptKeyIsUntouched(t *testing.T) {
	for _, route := range []string{"chat", "messages"} {
		t.Run(route, func(t *testing.T) {
			var body []byte
			var c *gin.Context
			if route == "chat" {
				body = []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}],"stream":false}`)
				c, _ = newOpenAIIdentityPathContext(t, "/v1/chat/completions", body, 38)
			} else {
				body = []byte(`{"model":"gpt-4o","max_tokens":16,"messages":[{"role":"user","content":"hello"}],"stream":false}`)
				c, _ = newOpenAIIdentityPathContext(t, "/v1/messages", body, 39)
			}
			upstream := &httpUpstreamRecorder{resp: openAICompatSSECompletedResponse("resp_identity_no_key_"+route, "gpt-4o")}
			svc, cache := newOpenAIIdentityPathService(t, true, upstream)
			account := newOpenAIIdentityPathOAuthAccount(910020 + int64(len(route)))
			var err error
			if route == "chat" {
				_, err = svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "gpt-4o")
			} else {
				_, err = svc.ForwardAsAnthropic(context.Background(), c, account, body, "", "gpt-4o")
			}
			require.NoError(t, err)
			require.NotNil(t, upstream.lastReq)
			requireOpenAIIdentityPathNoIdentityHeaders(t, upstream.lastReq.Header)
			requireOpenAIIdentityPathNoBodyPair(t, upstream.lastBody)
			require.Equal(t, 0, identityPathCacheCalls(cache))
		})
	}
}

func TestOpenAIOutboundIdentityPathsAlphaResponsesFallback(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled", true: "enabled"}[enabled], func(t *testing.T) {
			alphaBody := []byte(`{"id":"alpha-path-key","model":"gpt-5.4","query":"search"}`)
			c, _ := newOpenAIIdentityPathContext(t, "/v1/alpha/search", alphaBody, 40)
			svc, cache := newOpenAIIdentityPathService(t, enabled, nil)
			account := newOpenAIIdentityPathOAuthAccount(910030)
			responsesBody, err := buildOpenAIAlphaSearchResponsesWebSearchBody(alphaBody, "gpt-5.4")
			require.NoError(t, err)

			req, err := svc.buildOpenAIAlphaSearchResponsesWebSearchRequest(c.Request.Context(), c, account, alphaBody, responsesBody, "oauth-token")
			require.NoError(t, err)
			outboundBody := readOpenAIIdentityPathRequestBody(t, req)
			if enabled {
				requireOpenAIIdentityPathPair(t, req.Header, outboundBody)
			} else {
				legacy := isolateOpenAISessionID(40, "alpha-path-key")
				require.Equal(t, legacy, req.Header.Get("Session_ID"))
				require.Equal(t, legacy, req.Header.Get("Conversation_ID"))
				requireOpenAIIdentityPathNoBodyPair(t, outboundBody)
			}
			require.Equal(t, map[bool]int{false: 0, true: 1}[enabled], identityPathCacheCalls(cache))
		})
	}
}

func TestOpenAIOutboundIdentityPathsAlphaWithoutIDIsUntouched(t *testing.T) {
	alphaBody := []byte(`{"model":"gpt-5.4","query":"search"}`)
	c, _ := newOpenAIIdentityPathContext(t, "/v1/alpha/search", alphaBody, 41)
	svc, cache := newOpenAIIdentityPathService(t, true, nil)
	account := newOpenAIIdentityPathOAuthAccount(910031)
	responsesBody, err := buildOpenAIAlphaSearchResponsesWebSearchBody(alphaBody, "gpt-5.4")
	require.NoError(t, err)

	req, err := svc.buildOpenAIAlphaSearchResponsesWebSearchRequest(c.Request.Context(), c, account, alphaBody, responsesBody, "oauth-token")
	require.NoError(t, err)
	outboundBody := readOpenAIIdentityPathRequestBody(t, req)
	requireOpenAIIdentityPathNoIdentityHeaders(t, req.Header)
	requireOpenAIIdentityPathNoBodyPair(t, outboundBody)
	require.Equal(t, 0, identityPathCacheCalls(cache))
}

func TestOpenAIOutboundIdentityPathsAlphaWithoutIDUsesExplicitTurnMetadata(t *testing.T) {
	alphaBody := []byte(`{"model":"gpt-5.4","query":"search"}`)
	c, _ := newOpenAIIdentityPathContext(t, "/v1/alpha/search", alphaBody, 411)
	c.Request.Header.Set(openAIWSTurnMetadataHeader, `{"session_id":"alpha-metadata-session","thread_id":"alpha-metadata-thread"}`)
	svc, cache := newOpenAIIdentityPathService(t, true, nil)
	account := newOpenAIIdentityPathOAuthAccount(910034)
	responsesBody, err := buildOpenAIAlphaSearchResponsesWebSearchBody(alphaBody, "gpt-5.4")
	require.NoError(t, err)

	req, err := svc.buildOpenAIAlphaSearchResponsesWebSearchRequest(c.Request.Context(), c, account, alphaBody, responsesBody, "oauth-token")
	require.NoError(t, err)
	requireOpenAIIdentityPathPair(t, req.Header, readOpenAIIdentityPathRequestBody(t, req))
	require.Equal(t, 2, identityPathCacheCalls(cache))
}

func TestOpenAIOutboundIdentityDirectAlphaProjectsCanonicalMetadata(t *testing.T) {
	body := []byte(`{"id":"direct-alpha-id","model":"gpt-5.4","query":"search","future_field":{"keep":true}}`)
	c, _ := newOpenAIIdentityPathContext(t, "/v1/alpha/search", body, 412)
	const opaqueMetadata = "  opaque-turn-metadata\t"
	c.Request.Header[http.CanonicalHeaderKey(openAIWSTurnMetadataHeader)] = []string{
		opaqueMetadata,
		`{"session_id":"direct-alpha-session","thread_id":"direct-alpha-thread","label":"keep"}`,
	}
	svc, _ := newOpenAIIdentityPathService(t, true, nil)
	account := newOpenAIIdentityPathOAuthAccount(910036)
	account.Extra = map[string]any{openAIPinnedInstallationIDKey: "11111111-2222-4333-8444-555555555555"}

	req, err := svc.buildOpenAIAlphaSearchRequest(c.Request.Context(), c, account, body, "oauth-token")
	require.NoError(t, err)
	outboundBody := readOpenAIIdentityPathRequestBody(t, req)
	require.Equal(t, body, outboundBody)
	require.Empty(t, req.Header.Get("session-id"))
	require.Empty(t, req.Header.Get("thread-id"))
	require.Empty(t, req.Header.Get("x-client-request-id"))
	require.Empty(t, req.Header.Get(codexInstallationIDKey))
	require.False(t, gjson.GetBytes(outboundBody, "client_metadata").Exists())
	metadataValues := req.Header.Values(openAIWSTurnMetadataHeader)
	require.Len(t, metadataValues, 1)
	rewritten := metadataValues[0]
	require.Equal(t, "keep", gjson.Get(rewritten, "label").String())
	require.True(t, ValidateFingerprintObservationUUIDv7(gjson.Get(rewritten, "session_id").String()))
	require.True(t, ValidateFingerprintObservationUUIDv7(gjson.Get(rewritten, "thread_id").String()))
	require.Equal(t, "11111111-2222-4333-8444-555555555555", gjson.Get(rewritten, "installation_id").String())
}
