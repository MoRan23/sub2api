package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type codexV01470WireFixture struct {
	Source  string            `json:"source"`
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers"`
	Body    map[string]any    `json:"body"`
}

func loadCodexV01470WireFixture(t *testing.T, name string) codexV01470WireFixture {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "codex_v0_147_0", name+".json"))
	require.NoError(t, err)
	var fixture codexV01470WireFixture
	require.NoError(t, json.Unmarshal(raw, &fixture))
	require.NotEmpty(t, fixture.Source)
	require.NotEmpty(t, fixture.Path)
	return fixture
}

func replaceCodexFixtureIdentityWithLogical(t *testing.T, value any, identity OpenAICodexTurnIdentity) any {
	t.Helper()
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	replaced := strings.ReplaceAll(string(raw), identity.SessionID, "logical-session")
	if identity.ThreadID != identity.SessionID {
		replaced = strings.ReplaceAll(replaced, identity.ThreadID, "logical-thread")
	}
	if identity.ParentThreadID != "" && identity.ParentThreadID != identity.SessionID {
		replaced = strings.ReplaceAll(replaced, identity.ParentThreadID, "logical-parent")
	}
	if identity.ForkedFromThreadID != "" && identity.ForkedFromThreadID != identity.SessionID && identity.ForkedFromThreadID != identity.ParentThreadID {
		replaced = strings.ReplaceAll(replaced, identity.ForkedFromThreadID, "logical-fork")
	}
	var logical any
	require.NoError(t, json.Unmarshal([]byte(replaced), &logical))
	return logical
}

func normalizedCodexFixtureHeaders(headers http.Header) map[string]string {
	normalized := make(map[string]string, len(headers))
	for name, values := range headers {
		if len(values) == 0 {
			continue
		}
		normalized[strings.ToLower(name)] = values[0]
	}
	return normalized
}

func newTransportIdentityTestService(t *testing.T, enabled bool) *OpenAIGatewayService {
	t.Helper()
	value := "false"
	if enabled {
		value = "true"
	}
	repo := &openAIUUIDv7RuntimeRepo{values: map[string]string{
		SettingKeyEnableOpenAIUUIDv7SessionIdentity: value,
	}}
	return &OpenAIGatewayService{
		cfg:            &config.Config{JWT: config.JWTConfig{Secret: "transport-identity-test-secret"}},
		settingService: NewSettingService(repo, nil),
	}
}

func TestOpenAIOutboundSessionIdentityTransportEnabledRequestSnapshot(t *testing.T) {
	repo := &openAIUUIDv7RuntimeRepo{values: map[string]string{
		SettingKeyEnableOpenAIUUIDv7SessionIdentity: "true",
	}}
	settings := NewSettingService(repo, nil)
	svc := &OpenAIGatewayService{settingService: settings}
	requestCtx := newOutboundIdentityTestContext(t, nil)

	require.True(t, svc.openAIOutboundSessionIdentityTransportEnabledForRequest(context.Background(), requestCtx))
	repo.mu.Lock()
	repo.values[SettingKeyEnableOpenAIUUIDv7SessionIdentity] = "false"
	repo.mu.Unlock()
	settings.InvalidateOpenAIUUIDv7SessionIdentityCache()

	// One HTTP/WS request keeps the first mode even after invalidation.
	require.True(t, svc.openAIOutboundSessionIdentityTransportEnabledForRequest(context.Background(), requestCtx))
	// A new request sees the updated value.
	require.False(t, svc.openAIOutboundSessionIdentityTransportEnabledForRequest(context.Background(), newOutboundIdentityTestContext(t, nil)))

	// Callers without a Gin request retain live setting reads.
	repo.mu.Lock()
	repo.values[SettingKeyEnableOpenAIUUIDv7SessionIdentity] = "true"
	repo.mu.Unlock()
	settings.InvalidateOpenAIUUIDv7SessionIdentityCache()
	require.True(t, svc.openAIOutboundSessionIdentityTransportEnabledForRequest(context.Background(), nil))
}

func TestBuildUpstreamRequestConsumesPostBuildIdentityMarker(t *testing.T) {
	svc := newTransportIdentityTestService(t, true)
	account := &Account{
		ID:       810011,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token":       "oauth-token",
			"chatgpt_account_id": "transport-marker-account",
		},
	}
	c := newOutboundIdentityTestContext(t, nil)

	// Compatibility bridges set this marker before entering the shared builder;
	// their post-build phase owns the final UUID pair. The marker must not leak
	// into a later build reusing the same Gin context.
	setOpenAIOutboundSessionIdentityPostBuildContext(c)
	firstBody := []byte(`{"model":"gpt-5.4","prompt_cache_key":"compat-first"}`)
	firstReq, err := svc.buildUpstreamRequest(
		context.Background(), c, account, firstBody, "oauth-token", true, "compat-first", false,
	)
	require.NoError(t, err)
	// UUIDv7 mode leaves identity projection to the compatibility bridge's
	// post-build writer. It must not fall back to the legacy hex headers here.
	require.Empty(t, firstReq.Header.Get("session-id"))
	require.Empty(t, firstReq.Header.Get("thread-id"))
	require.Empty(t, firstReq.Header.Get("session_id"))
	require.Empty(t, firstReq.Header.Get("conversation_id"))
	require.False(t, isOpenAIOutboundSessionIdentityPostBuildContext(c))

	secondBody := []byte(`{"model":"gpt-5.4","prompt_cache_key":"ordinary-second"}`)
	secondReq, err := svc.buildUpstreamRequest(
		context.Background(), c, account, secondBody, "oauth-token", true, "ordinary-second", false,
	)
	require.NoError(t, err)
	secondWireBody, err := io.ReadAll(secondReq.Body)
	require.NoError(t, err)
	require.NotEqual(t, isolateOpenAISessionID(0, "ordinary-second"), secondReq.Header.Get("session-id"))
	require.NoError(t, ValidateOpenAIOutboundSessionIdentity(OpenAIOutboundSessionIdentity{
		SessionID: secondReq.Header.Get("session-id"),
		ThreadID:  secondReq.Header.Get("thread-id"),
	}))
	require.Equal(t, secondReq.Header.Get("session-id"), secondReq.Header.Get("thread-id"))
	require.Empty(t, secondReq.Header.Get("session_id"))
	require.Empty(t, secondReq.Header.Get("conversation_id"))
	require.Equal(t, secondReq.Header.Get("session-id"), gjson.GetBytes(secondWireBody, "client_metadata.session_id").String())
	require.Equal(t, secondReq.Header.Get("thread-id"), gjson.GetBytes(secondWireBody, "client_metadata.thread_id").String())
}

func TestResolveOpenAIOutboundSessionIdentityForTransportUsesFinalBody(t *testing.T) {
	svc := newTransportIdentityTestService(t, true)
	account := &Account{ID: 810001, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	c := newOutboundIdentityTestContext(t, nil)
	body := []byte(`{"client_metadata":{"thread_id":"body-logical-key"},"prompt_cache_key":"body-prompt"}`)

	first, key, enabled, err := svc.resolveOpenAIOutboundSessionIdentityForTransport(
		context.Background(), c, account, body, "caller-seed", false,
	)
	require.NoError(t, err)
	require.True(t, enabled)
	require.Equal(t, "body-logical-key", key)

	second, secondKey, secondEnabled, err := svc.resolveOpenAIOutboundSessionIdentityForTransport(
		context.Background(), c, account, body, "caller-seed", false,
	)
	require.NoError(t, err)
	require.True(t, secondEnabled)
	require.Equal(t, key, secondKey)
	require.Equal(t, first, second)
	require.NotEmpty(t, first.SessionID)
	require.NotEmpty(t, first.ThreadID)
}

func TestResolveOpenAIOutboundSessionIdentityForTransportExplicitTupleWinsPrompt(t *testing.T) {
	svc := newTransportIdentityTestService(t, true)
	account := &Account{ID: 810002, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	c := newOutboundIdentityTestContext(t, map[string]string{"session_id": "copied-header"})
	body := []byte(`{"client_metadata":{"session_id":"body-key"},"prompt_cache_key":"prompt-key"}`)

	_, key, enabled, err := svc.resolveOpenAIOutboundSessionIdentityForTransport(
		context.Background(), c, account, body, "prompt-key", true,
	)
	require.NoError(t, err)
	require.True(t, enabled)
	require.Equal(t, "body-key", key)
}

func TestResolveOpenAIOutboundSessionIdentityForTransportPinsWSFrameKey(t *testing.T) {
	svc := newTransportIdentityTestService(t, true)
	account := &Account{ID: 810007, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	c := newOutboundIdentityTestContext(t, map[string]string{"session_id": "stale-handshake-key"})
	body := []byte(`{"client_metadata":{"session_id":"next-frame-key"}}`)
	selected := resolveOpenAIWSFrameLogicalKey(body, "")

	_, key, enabled, err := svc.resolveOpenAIOutboundSessionIdentityForTransport(
		context.Background(), c, account, body, selected, true,
	)
	require.NoError(t, err)
	require.True(t, enabled)
	require.NotEmpty(t, selected)
	require.Equal(t, "next-frame-key", key)
}

func TestResolveOpenAIWSFrameLogicalIdentityForPinnedStateUsesFallbackOnlyBeforePin(t *testing.T) {
	body := []byte(`{"prompt_cache_key":"late-root"}`)
	unpinned := resolveOpenAIWSFrameLogicalIdentityForPinnedState(body, false)
	require.Equal(t, "late-root", unpinned.SessionKey)
	require.Equal(t, "late-root", unpinned.ThreadKey)
	require.False(t, unpinned.Explicit)

	pinned := resolveOpenAIWSFrameLogicalIdentityForPinnedState(body, true)
	require.Empty(t, pinned.SessionKey)
	require.Empty(t, pinned.ThreadKey)
}

func TestBuildOpenAIWSHeadersWithBodyUUIDv7UsesCodexTupleSources(t *testing.T) {
	svc := newTransportIdentityTestService(t, true)
	account := &Account{ID: 810008, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	c := newOutboundIdentityTestContext(t, nil)

	tests := []struct {
		name           string
		body           []byte
		promptCacheKey string
		wantKey        string
		wantIdentity   bool
	}{
		{
			name:         "client metadata",
			body:         []byte(`{"client_metadata":{"session_id":"frame-metadata-key"}}`),
			wantKey:      "frame-metadata-key",
			wantIdentity: true,
		},
		{
			name:         "turn metadata",
			body:         []byte(`{"x-codex-turn-metadata":{"thread_id":"frame-turn-key"}}`),
			wantKey:      "frame-turn-key",
			wantIdentity: true,
		},
		{
			name:           "prompt cache key",
			body:           []byte(`{"prompt_cache_key":"frame-prompt-key"}`),
			promptCacheKey: "frame-prompt-key",
			wantKey:        "frame-prompt-key",
			wantIdentity:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			headers, resolution, err := svc.buildOpenAIWSHeadersWithBody(
				context.Background(),
				c,
				account,
				"oauth-token",
				OpenAIWSProtocolDecision{Transport: OpenAIUpstreamTransportResponsesWebsocketV2},
				true,
				"",
				"",
				tt.promptCacheKey,
				tt.body,
				false,
			)
			require.NoError(t, err)
			require.True(t, resolution.OutboundIdentityModeEnabled)
			require.Equal(t, tt.wantIdentity, resolution.OutboundIdentityEnabled)
			require.Equal(t, tt.wantKey, resolution.OutboundIdentityLogicalKey)
			if resolution.OutboundLogicalIdentity.Explicit {
				require.NotEmpty(t, resolution.OutboundIdentityFrameKey)
			} else {
				require.Empty(t, resolution.OutboundIdentityFrameKey)
			}
			if tt.wantIdentity {
				require.NoError(t, ValidateOpenAIOutboundSessionIdentity(resolution.OutboundIdentity))
				require.Equal(t, resolution.OutboundIdentity.SessionID, headers.Get("session-id"))
				require.Equal(t, resolution.OutboundIdentity.ThreadID, headers.Get("thread-id"))
				require.Empty(t, headers.Get("session_id"))
				require.Empty(t, headers.Get("conversation_id"))
				require.NotEmpty(t, resolution.OutboundIdentityDigest)
			} else {
				require.Empty(t, headers.Get("session_id"))
				require.Empty(t, headers.Get("conversation_id"))
				require.Empty(t, resolution.OutboundIdentityDigest)
			}
		})
	}
}

func TestBuildOpenAIWSHeadersWithBodyUUIDv7UsesExplicitTurnMetadata(t *testing.T) {
	svc := newTransportIdentityTestService(t, true)
	account := &Account{ID: 810011, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	c := newOutboundIdentityTestContext(t, nil)
	_, resolution, err := svc.buildOpenAIWSHeadersWithBody(
		context.Background(),
		c,
		account,
		"oauth-token",
		OpenAIWSProtocolDecision{Transport: OpenAIUpstreamTransportResponsesWebsocketV2},
		true,
		"",
		`{"session_id":"explicit-turn-key"}`,
		"",
		[]byte(`{"model":"gpt-5"}`),
		false,
	)
	require.NoError(t, err)
	require.True(t, resolution.OutboundIdentityModeEnabled)
	require.True(t, resolution.OutboundIdentityEnabled)
	require.Equal(t, "explicit-turn-key", resolution.OutboundIdentityLogicalKey)
	require.NotEmpty(t, resolution.OutboundIdentityFrameKey)
}

func TestBuildOpenAIWSHeadersWithBodyUUIDv7RejectsUnsafeSeedWithoutLegacyFallback(t *testing.T) {
	svc := newTransportIdentityTestService(t, true)
	account := &Account{ID: 810012, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	unsafeSeed := strings.Repeat("x", maxPersistedSessionIDLength+1)
	c := newOutboundIdentityTestContext(t, map[string]string{"session_id": unsafeSeed})

	headers, resolution, err := svc.buildOpenAIWSHeadersWithBody(
		context.Background(),
		c,
		account,
		"oauth-token",
		OpenAIWSProtocolDecision{Transport: OpenAIUpstreamTransportResponsesWebsocketV2},
		true,
		"",
		"",
		unsafeSeed,
		[]byte(`{"model":"gpt-5"}`),
		false,
	)
	require.NoError(t, err)
	require.True(t, resolution.OutboundIdentityModeEnabled)
	require.False(t, resolution.OutboundIdentityEnabled)
	require.Empty(t, resolution.OutboundIdentity)
	require.Empty(t, headers.Get("session-id"))
	require.Empty(t, headers.Get("thread-id"))
	require.Empty(t, headers.Get("session_id"))
	require.Empty(t, headers.Get("conversation_id"))
}

func TestBuildOpenAIWSHeadersWithBodyUUIDv7KeepsHeaderPriorityAndTracksFrameKey(t *testing.T) {
	svc := newTransportIdentityTestService(t, true)
	account := &Account{ID: 810010, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	c := newOutboundIdentityTestContext(t, map[string]string{"session_id": "handshake-key"})
	body := []byte(`{"prompt_cache_key":"frame-key"}`)

	_, resolution, err := svc.buildOpenAIWSHeadersWithBody(
		context.Background(),
		c,
		account,
		"oauth-token",
		OpenAIWSProtocolDecision{Transport: OpenAIUpstreamTransportResponsesWebsocketV2},
		true,
		"",
		"",
		"",
		body,
		false,
	)
	require.NoError(t, err)
	require.True(t, resolution.OutboundIdentityEnabled)
	require.Equal(t, "handshake-key", resolution.OutboundIdentityLogicalKey)
	require.NotEmpty(t, resolution.OutboundIdentityFrameKey)
}

func TestAdvanceOpenAIWSFrameLogicalKey(t *testing.T) {
	tests := []struct {
		name         string
		frameKey     string
		previousKey  string
		pinnedKey    string
		wantPrevious string
		wantChanged  bool
	}{
		{name: "empty inherits", previousKey: "P", pinnedKey: "H", wantPrevious: "P"},
		{name: "repeated body key", frameKey: "P", previousKey: "P", pinnedKey: "H", wantPrevious: "P"},
		{name: "body key changes", frameKey: "Q", previousKey: "P", pinnedKey: "H", wantPrevious: "Q", wantChanged: true},
		{name: "first body matches pinned header", frameKey: "H", pinnedKey: "H", wantPrevious: "H"},
		{name: "first body differs from pinned header", frameKey: "P", pinnedKey: "H", wantPrevious: "P", wantChanged: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			next, changed := advanceOpenAIWSFrameLogicalKey(tt.frameKey, tt.previousKey, tt.pinnedKey)
			require.Equal(t, tt.wantPrevious, next)
			require.Equal(t, tt.wantChanged, changed)
		})
	}
}

func TestBuildOpenAIWSHeadersWithBodyDisabledPreservesLegacyHeaderSelection(t *testing.T) {
	svc := newTransportIdentityTestService(t, false)
	account := &Account{ID: 810009, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	c := newOutboundIdentityTestContext(t, map[string]string{
		"session_id":      "legacy-handshake-session",
		"conversation_id": "legacy-handshake-conversation",
	})

	headers, resolution, err := svc.buildOpenAIWSHeadersWithBody(
		context.Background(),
		c,
		account,
		"oauth-token",
		OpenAIWSProtocolDecision{Transport: OpenAIUpstreamTransportResponsesWebsocketV2},
		true,
		"",
		"",
		"body-prompt-key",
		[]byte(`{"client_metadata":{"session_id":"body-key"},"prompt_cache_key":"body-prompt-key"}`),
		false,
	)
	require.NoError(t, err)
	require.False(t, resolution.OutboundIdentityModeEnabled)
	require.False(t, resolution.OutboundIdentityEnabled)
	require.Empty(t, resolution.OutboundIdentityLogicalKey)
	require.Equal(t, isolateOpenAISessionID(0, "legacy-handshake-session"), headers.Get("session_id"))
	require.Equal(t, isolateOpenAISessionID(0, "legacy-handshake-conversation"), headers.Get("conversation_id"))
}

func TestResolveOpenAIOutboundSessionIdentityForTransportDisabledDoesNotResolve(t *testing.T) {
	svc := newTransportIdentityTestService(t, false)
	before := SnapshotOpenAIOutboundSessionIdentityRuntimeMetrics()
	identity, key, enabled, err := svc.resolveOpenAIOutboundSessionIdentityForTransport(
		context.Background(), newOutboundIdentityTestContext(t, nil),
		&Account{ID: 810003, Platform: PlatformOpenAI, Type: AccountTypeOAuth},
		[]byte(`{"client_metadata":{"session_id":"disabled-key"}}`), "seed", false,
	)
	require.NoError(t, err)
	require.False(t, enabled)
	require.Empty(t, key)
	require.Empty(t, identity)
	after := SnapshotOpenAIOutboundSessionIdentityRuntimeMetrics()
	require.Equal(t, before.ResolveTotal, after.ResolveTotal)
}

func TestResolveOpenAIOutboundSessionIdentityForTransportPropagatesNamespaceFailure(t *testing.T) {
	svc := newTransportIdentityTestService(t, true)
	svc.accountRepo = &outboundIdentityAccountRepoStub{accounts: map[int64]*Account{}}
	parentID := int64(810005)
	account := &Account{ID: 810006, ParentAccountID: &parentID, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	_, _, enabled, err := svc.resolveOpenAIOutboundSessionIdentityForTransport(
		context.Background(), newOutboundIdentityTestContext(t, nil), account,
		[]byte(`{"prompt_cache_key":"namespace-failure"}`), "namespace-failure", false,
	)
	require.False(t, enabled)
	require.Error(t, err)
	require.True(t, errors.Is(err, errOpenAIOutboundSessionIdentityNamespace))
}

func TestApplyOpenAIOutboundSessionIdentityCompactHeadersKeepsCanonicalPair(t *testing.T) {
	headers := http.Header{
		"Session-Id":          []string{"client-session"},
		"Thread-Id":           []string{"client-thread"},
		"Thread_Id":           []string{"client-thread-underscore"},
		"Conversation-Id":     []string{"client-conversation"},
		"X-Client-Request-Id": []string{"client-request"},
	}
	identity := OpenAIOutboundSessionIdentity{SessionID: testOutboundSessionUUID, ThreadID: testOutboundThreadUUID}
	applyOpenAIOutboundSessionIdentityCompactHeaders(headers, identity)
	require.Empty(t, headers.Get("session_id"))
	require.Equal(t, identity.SessionID, headers.Get("session-id"))
	require.Equal(t, identity.ThreadID, headers.Get("thread-id"))
	require.Empty(t, headers.Get("conversation_id"))
	require.Empty(t, headers.Get("x-client-request-id"))
}

func TestOpenAICodexV01470WireFixtures(t *testing.T) {
	const fixtureSessionID = "01989f44-7c00-7000-8000-000000000001"
	const fixtureThreadID = "01989f44-7c00-7000-8000-000000000002"

	tests := []struct {
		name     string
		fixture  string
		compact  bool
		identity OpenAICodexTurnIdentity
	}{
		{
			name:    "root responses",
			fixture: "root_responses",
			identity: OpenAICodexTurnIdentity{
				SessionID: fixtureSessionID,
				ThreadID:  fixtureSessionID,
				Relation:  OpenAICodexTurnRelationRoot,
			},
		},
		{
			name:    "child responses",
			fixture: "child_responses",
			identity: OpenAICodexTurnIdentity{
				SessionID:      fixtureSessionID,
				ThreadID:       fixtureThreadID,
				ParentThreadID: fixtureSessionID,
				Relation:       OpenAICodexTurnRelationDescendant,
			},
		},
		{
			name:    "remote compact",
			fixture: "remote_compact",
			compact: true,
			identity: OpenAICodexTurnIdentity{
				SessionID: fixtureSessionID,
				ThreadID:  fixtureSessionID,
				Relation:  OpenAICodexTurnRelationRoot,
			},
		},
		{
			name:    "websocket response create",
			fixture: "websocket_response_create",
			identity: OpenAICodexTurnIdentity{
				SessionID: fixtureSessionID,
				ThreadID:  fixtureSessionID,
				Relation:  OpenAICodexTurnRelationRoot,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := loadCodexV01470WireFixture(t, tt.fixture)
			require.NoError(t, ValidateOpenAICodexTurnIdentity(tt.identity))

			headers := make(http.Header)
			if turnMetadata := fixture.Headers[openAIWSTurnMetadataHeader]; turnMetadata != "" {
				logicalMetadata, ok := replaceCodexFixtureIdentityWithLogical(t, turnMetadata, tt.identity).(string)
				require.True(t, ok)
				headers.Set(openAIWSTurnMetadataHeader, logicalMetadata)
			}
			// Stale compatibility aliases must not survive either native writer.
			headers.Set("session_id", "client-session")
			headers.Set("thread_id", "client-thread")
			headers.Set("conversation_id", "client-conversation")
			headers.Set("x-client-request-id", "client-request")
			if tt.compact {
				applyOpenAIOutboundSessionIdentityCompactHeaders(headers, tt.identity)
			} else {
				ApplyOpenAIOutboundSessionIdentityHeaders(headers, tt.identity)
			}
			require.Equal(t, fixture.Headers, normalizedCodexFixtureHeaders(headers))

			logicalBody := replaceCodexFixtureIdentityWithLogical(t, fixture.Body, tt.identity)
			inputBody, err := json.Marshal(logicalBody)
			require.NoError(t, err)
			outboundBody := inputBody
			if !tt.compact {
				outboundBody, err = MergeOpenAIOutboundSessionIdentityBody(inputBody, tt.identity)
				require.NoError(t, err)
			}
			var actualBody map[string]any
			require.NoError(t, json.Unmarshal(outboundBody, &actualBody))
			require.Equal(t, fixture.Body, actualBody)
			if tt.compact {
				_, hasClientMetadata := actualBody["client_metadata"]
				require.False(t, hasClientMetadata)
			}
		})
	}
}

func TestOpenAICodexProjectorPreservesMetadataAndEscapesNonASCII(t *testing.T) {
	const forkThreadID = "018f5c3c-6e3a-7abe-8def-1234567890ad"
	identity := OpenAICodexTurnIdentity{
		SessionID:          testOutboundSessionUUID,
		ThreadID:           testOutboundThreadUUID,
		ParentThreadID:     testOutboundSessionUUID,
		ForkedFromThreadID: forkThreadID,
		Relation:           OpenAICodexTurnRelationDescendant,
	}
	require.NoError(t, ValidateOpenAICodexTurnIdentity(identity))
	logicalMetadata := `{"request_kind":"turn","label":"你好😀","session_id":"logical-session","thread_id":"logical-thread","parent_thread_id":"logical-parent","forked_from_thread_id":"logical-fork"}`
	headers := http.Header{openAIWSTurnMetadataHeader: []string{logicalMetadata}}
	ApplyOpenAIOutboundSessionIdentityHeaders(headers, identity)

	require.Equal(t, identity.SessionID, headers.Get("session-id"))
	require.Equal(t, identity.ThreadID, headers.Get("thread-id"))
	require.Equal(t, identity.ParentThreadID, headers.Get("x-codex-parent-thread-id"))
	headerMetadata := headers.Get(openAIWSTurnMetadataHeader)
	require.NotContains(t, headerMetadata, "你好")
	require.Contains(t, headerMetadata, `\u4f60\u597d\ud83d\ude00`)
	require.Equal(t, "turn", gjson.Get(headerMetadata, "request_kind").String())
	require.Equal(t, "你好😀", gjson.Get(headerMetadata, "label").String())
	require.Equal(t, identity.SessionID, gjson.Get(headerMetadata, "session_id").String())
	require.Equal(t, identity.ThreadID, gjson.Get(headerMetadata, "thread_id").String())
	require.Equal(t, identity.ParentThreadID, gjson.Get(headerMetadata, "parent_thread_id").String())
	require.Equal(t, identity.ForkedFromThreadID, gjson.Get(headerMetadata, "forked_from_thread_id").String())

	body := []byte(`{"model":"gpt-5.4","client_metadata":{"keep":"value","session_id":"logical-session","thread_id":"logical-thread","x-codex-turn-metadata":"{\"request_kind\":\"turn\",\"label\":\"你好😀\"}"}}`)
	outboundBody, err := MergeOpenAIOutboundSessionIdentityBody(body, identity)
	require.NoError(t, err)
	require.Equal(t, "value", gjson.GetBytes(outboundBody, "client_metadata.keep").String())
	require.Equal(t, identity.SessionID, gjson.GetBytes(outboundBody, "client_metadata.session_id").String())
	require.Equal(t, identity.ThreadID, gjson.GetBytes(outboundBody, "client_metadata.thread_id").String())
	require.Equal(t, identity.ParentThreadID, gjson.GetBytes(outboundBody, "client_metadata.x-codex-parent-thread-id").String())
	bodyMetadata := gjson.GetBytes(outboundBody, "client_metadata.x-codex-turn-metadata").String()
	require.NotContains(t, bodyMetadata, "你好")
	require.Contains(t, bodyMetadata, `\u4f60\u597d\ud83d\ude00`)
	require.Equal(t, identity.ParentThreadID, gjson.Get(bodyMetadata, "parent_thread_id").String())
	require.Equal(t, identity.ForkedFromThreadID, gjson.Get(bodyMetadata, "forked_from_thread_id").String())
}

func TestOpenAIWSOutboundIdentityHeaderValueForLogUsesDigest(t *testing.T) {
	identity := OpenAIOutboundSessionIdentity{SessionID: testOutboundSessionUUID, ThreadID: testOutboundThreadUUID}
	headers := make(http.Header)
	ApplyOpenAIOutboundSessionIdentityHeaders(headers, identity)
	digest := openAIWSOutboundIdentityDigest(identity)

	logged := openAIWSOutboundIdentityHeaderValueForLog(headers, "session-id", digest)
	require.Equal(t, "uuidv7:"+digest[:12], logged)
	require.NotContains(t, logged, identity.SessionID)
	require.NotContains(t, logged, identity.ThreadID)
	require.Equal(t, identity.SessionID, openAIWSOutboundIdentityHeaderValueForLog(headers, "session-id", ""))
}

func TestOpenAIWSConnPoolIdentityDigestScopesReuseAndPreferred(t *testing.T) {
	p := newOpenAIWSConnPool(&config.Config{})
	account := &Account{ID: 810004, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 2}
	ap := p.getOrCreateAccountPool(account.ID)
	first := newOpenAIWSConn("identity-a", account.ID, &openAIWSFakeConn{}, nil)
	first.betaFeatures = "responses=experimental"
	first.identityDigest = "digest-a"
	second := newOpenAIWSConn("identity-b", account.ID, &openAIWSFakeConn{}, nil)
	second.betaFeatures = "responses=experimental"
	second.identityDigest = "digest-b"
	ap.conns[first.id] = first
	ap.conns[second.id] = second

	require.Same(t, first, p.pickLeastBusyConnLocked(ap, "", "responses=experimental", "digest-a"))
	require.Same(t, second, p.pickLeastBusyConnLocked(ap, "", "responses=experimental", "digest-b"))
	require.Nil(t, p.pickLeastBusyConnLocked(ap, "", "responses=experimental", "digest-c"))

	_, err := p.Acquire(context.Background(), openAIWSAcquireRequest{
		Account:            account,
		WSURL:              "wss://example.com/v1/responses",
		Headers:            http.Header{"OpenAI-Beta": []string{"responses=experimental"}},
		IdentityDigest:     "digest-b",
		PreferredConnID:    first.id,
		ForcePreferredConn: true,
	})
	require.ErrorIs(t, err, errOpenAIWSPreferredConnUnavailable)
}
