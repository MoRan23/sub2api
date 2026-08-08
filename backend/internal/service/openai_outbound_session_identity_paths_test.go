package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
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
		SessionID: headers.Get("session_id"),
		ThreadID:  headers.Get("thread-id"),
	}
	require.NoError(t, ValidateOpenAIOutboundSessionIdentity(identity))
	require.Equal(t, identity.SessionID, headers.Get("session-id"))
	require.Equal(t, identity.ThreadID, headers.Get("conversation_id"))
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

func TestOpenAIOutboundIdentityPathsCompactIsSessionOnly(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled", true: "enabled"}[enabled], func(t *testing.T) {
			body := []byte(`{"model":"gpt-5.4","prompt_cache_key":"compact-path-key"}`)
			c, _ := newOpenAIIdentityPathContext(t, "/v1/responses/compact", body, 32)
			svc, cache := newOpenAIIdentityPathService(t, enabled, nil)
			account := newOpenAIIdentityPathOAuthAccount(910002)

			req, err := svc.buildUpstreamRequest(c.Request.Context(), c, account, body, "oauth-token", false, "compact-path-key", false)
			require.NoError(t, err)
			outboundBody := readOpenAIIdentityPathRequestBody(t, req)
			require.Empty(t, req.Header.Get("session-id"))
			require.Empty(t, req.Header.Get("thread-id"))
			require.Empty(t, req.Header.Get("thread_id"))
			if enabled {
				require.Empty(t, req.Header.Get("conversation_id"))
			} else {
				require.Equal(t, isolateOpenAISessionID(32, "compact-path-key"), req.Header.Get("conversation_id"))
			}
			require.Empty(t, req.Header.Get("conversation-id"))
			require.Empty(t, req.Header.Get("x-client-request-id"))
			requireOpenAIIdentityPathNoBodyPair(t, outboundBody)
			if enabled {
				sessionID := req.Header.Get("session_id")
				parsed, parseErr := uuid.Parse(sessionID)
				require.NoError(t, parseErr)
				require.Equal(t, uuid.Version(7), parsed.Version())
				require.Equal(t, uuid.RFC4122, parsed.Variant())
				require.Equal(t, 1, identityPathCacheCalls(cache))
			} else {
				require.Equal(t, isolateOpenAISessionID(32, "compact-path-key"), req.Header.Get("session_id"))
				require.Equal(t, 0, identityPathCacheCalls(cache))
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
				identity.SessionID = upstream.lastReq.Header.Get("session_id")
				require.True(t, ValidateFingerprintObservationUUIDv7(identity.SessionID))
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
	require.Equal(t, resolution.OutboundIdentity.SessionID, headers.Get("session_id"))
	require.Equal(t, resolution.OutboundIdentity.ThreadID, headers.Get("thread-id"))
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

func TestOpenAIOutboundIdentityPassthroughInvalidPrimarySeedFallsBackToLegacy(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","prompt_cache_key":"passthrough-prompt","input":"hello"}`)
	c, _ := newOpenAIIdentityPathContext(t, "/v1/responses", body, 41)
	c.Request.Header.Set("session_id", "\x01invalid-session")
	c.Request.Header.Set("conversation_id", "valid-conversation")
	svc, cache := newOpenAIIdentityPathService(t, true, nil)
	account := newOpenAIIdentityPathOAuthAccount(910007)

	req, err := svc.buildUpstreamRequestOpenAIPassthrough(c.Request.Context(), c, account, body, "oauth-token")
	require.NoError(t, err)
	outboundBody := readOpenAIIdentityPathRequestBody(t, req)
	require.Equal(t, isolateOpenAISessionID(41, "\x01invalid-session"), req.Header.Get("session_id"))
	require.Equal(t, isolateOpenAISessionID(41, "valid-conversation"), req.Header.Get("conversation_id"))
	requireOpenAIIdentityPathNoBodyPair(t, outboundBody)
	require.Equal(t, 0, identityPathCacheCalls(cache))
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

func TestOpenAIOutboundIdentityPathsAPIKeyCompatibilityKeepsHistoricalCoverage(t *testing.T) {
	for _, route := range []string{"chat", "messages"} {
		t.Run(route, func(t *testing.T) {
			enableOpenAIIdentityPathFingerprintObservation(t)
			var body []byte
			var c *gin.Context
			var endpoint string
			if route == "chat" {
				body = []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hello"}],"stream":false}`)
				endpoint = "/v1/chat/completions"
				c, _ = newOpenAIIdentityPathContext(t, endpoint, body, 47)
			} else {
				body = []byte(`{"model":"gpt-5.4","max_tokens":16,"messages":[{"role":"user","content":"hello"}],"stream":false}`)
				endpoint = "/v1/messages"
				c, _ = newOpenAIIdentityPathContext(t, endpoint, body, 48)
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
			requireOpenAIIdentityPathPair(t, upstream.lastReq.Header, upstream.lastBody)
			require.Equal(t, 1, identityPathCacheCalls(cache))
			// UUIDv7 replaces this compatibility call site's historical isolated
			// session header, but fingerprint observation remains OAuth-only.
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
