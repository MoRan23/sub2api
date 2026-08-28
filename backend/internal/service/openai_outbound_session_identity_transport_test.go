package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
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

func TestApplyOpenAIOAuthIdentityPlanInstallationOnlyPreservesMemoryTurnMetadata(t *testing.T) {
	const installationID = "77777777-7777-4777-8777-777777777777"
	nested := `{"installation_id":"client-installation","request_kind":"memory","session_id":"client-session","thread_id":"client-thread","turn_id":"` + codexWireTestTurn + `","turn_started_at_unix_ms":1777777777123,"parent_thread_id":"client-parent","forked_from_thread_id":"client-fork","custom":"keep"}`
	body := []byte(`{"model":"gpt-5.6-sol","client_metadata":{"x-codex-installation-id":"client-installation","turn_id":"` + codexWireTestTurn + `","x-codex-turn-metadata":` + strconv.Quote(nested) + `},"x-codex-turn-metadata":` + strconv.Quote(nested) + `}`)
	headers := http.Header{
		codexInstallationIDKey:     {"client-installation"},
		openAIWSTurnMetadataHeader: {nested},
	}
	capture := CaptureOpenAIOAuthIdentity(nil, body, "")
	require.Equal(t, CodexWireRequestMemory, capture.WireProfile.RequestKind)
	plan := OpenAIOAuthIdentityPlan{
		Capture:               capture,
		WireProfile:           capture.WireProfile,
		ProjectionMode:        OpenAIOAuthIdentityProjectionRegular,
		InstallationPolicy:    OpenAIOAuthInstallationAccountPin,
		InstallationEnabled:   true,
		InstallationID:        installationID,
		TurnIdentityRequested: false,
		TurnIdentityEnabled:   false,
	}

	out, err := ApplyOpenAIOAuthIdentityPlan(headers, body, plan)
	require.NoError(t, err)
	require.Equal(t, installationID, headers.Get(codexInstallationIDKey))
	headerMetadata := gjson.Parse(headers.Get(openAIWSTurnMetadataHeader))
	require.Equal(t, installationID, headerMetadata.Get("installation_id").String())
	require.Equal(t, "memory", headerMetadata.Get("request_kind").String())
	require.Equal(t, codexWireTestTurn, headerMetadata.Get("turn_id").String())
	require.Equal(t, int64(1777777777123), headerMetadata.Get("turn_started_at_unix_ms").Int())
	require.Equal(t, "client-parent", headerMetadata.Get("parent_thread_id").String())
	require.Equal(t, "client-fork", headerMetadata.Get("forked_from_thread_id").String())
	require.Equal(t, "keep", headerMetadata.Get("custom").String())

	require.Equal(t, installationID, gjson.GetBytes(out, "client_metadata.x-codex-installation-id").String())
	require.Equal(t, codexWireTestTurn, gjson.GetBytes(out, "client_metadata.turn_id").String())
	for _, path := range []string{"client_metadata.x-codex-turn-metadata", "x-codex-turn-metadata"} {
		projected := gjson.GetBytes(out, path).String()
		require.Equal(t, installationID, gjson.Get(projected, "installation_id").String(), path)
		require.Equal(t, "memory", gjson.Get(projected, "request_kind").String(), path)
		require.Equal(t, codexWireTestTurn, gjson.Get(projected, "turn_id").String(), path)
		require.Equal(t, int64(1777777777123), gjson.Get(projected, "turn_started_at_unix_ms").Int(), path)
		require.Equal(t, "client-parent", gjson.Get(projected, "parent_thread_id").String(), path)
		require.Equal(t, "client-fork", gjson.Get(projected, "forked_from_thread_id").String(), path)
		require.Equal(t, "keep", gjson.Get(projected, "custom").String(), path)
	}
}

func TestBuildUpstreamRequestCompatBridgeMaterializesPlanAndDefersProjection(t *testing.T) {
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

	firstBody := []byte(`{"model":"gpt-5.4","prompt_cache_key":"compat-first"}`)
	SetOpenAIOAuthIdentityCapture(c, CaptureOpenAIOAuthIdentity(c, firstBody, "compat-first"))
	setOpenAICompatMessagesBridgeContext(c, true)
	firstReq, err := svc.buildUpstreamRequestWithOptions(
		context.Background(), c, account, firstBody, "oauth-token", true, "compat-first", false,
		openAIUpstreamRequestBuildOptions{deferOAuthIdentityProjection: true},
	)
	require.NoError(t, err)
	// The facade must materialize a plan before the shared builder returns, while
	// the compatibility bridge owns the one final projection after restoring its
	// required headers.
	require.Empty(t, firstReq.Header.Get("session-id"))
	require.Empty(t, firstReq.Header.Get("thread-id"))
	require.Empty(t, firstReq.Header.Get("session_id"))
	require.Empty(t, firstReq.Header.Get("conversation_id"))
	plan, planned := OpenAIOAuthIdentityPlanFromContext(c)
	require.True(t, planned)
	require.True(t, plan.TurnIdentityEnabled)
	ensureCodexIdentityHeadersFromPlan(firstReq.Header, plan.ClientIdentity)
	projectedBody, err := ApplyOpenAIOAuthIdentityPlan(firstReq.Header, firstBody, plan)
	require.NoError(t, err)
	require.NotEqual(t, isolateOpenAISessionID(0, "compat-first"), firstReq.Header.Get("session-id"))
	require.NoError(t, ValidateOpenAIOutboundSessionIdentity(OpenAIOutboundSessionIdentity{
		SessionID: firstReq.Header.Get("session-id"),
		ThreadID:  firstReq.Header.Get("thread-id"),
	}))
	require.Equal(t, firstReq.Header.Get("session-id"), firstReq.Header.Get("thread-id"))
	require.Empty(t, firstReq.Header.Get("session_id"))
	require.Empty(t, firstReq.Header.Get("conversation_id"))
	require.Equal(t, firstReq.Header.Get("session-id"), gjson.GetBytes(projectedBody, "client_metadata.session_id").String())
	require.Equal(t, firstReq.Header.Get("thread-id"), gjson.GetBytes(projectedBody, "client_metadata.thread_id").String())
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

func TestBuildOpenAIWSHeadersWithBodyDisabledPreservesCanonicalSessionHeaderOnly(t *testing.T) {
	svc := newTransportIdentityTestService(t, false)
	account := &Account{ID: 810010, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	c := newOutboundIdentityTestContext(t, map[string]string{
		"session-id": "canonical-handshake-session",
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
		"",
		[]byte(`{"type":"response.create","input":"hello"}`),
		false,
	)
	require.NoError(t, err)
	require.False(t, resolution.OutboundIdentityModeEnabled)
	require.Equal(t, "header_session-id", resolution.SessionSource)
	require.Equal(t, "canonical-handshake-session", headers.Get("session-id"))
	require.Empty(t, headers.Get("session_id"))
}

func TestBuildOpenAIWSHeadersWithBodyUUIDv7UsesFrozenCanonicalIdentity(t *testing.T) {
	svc := newTransportIdentityTestService(t, true)
	account := &Account{ID: 810013, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	c := newOutboundIdentityTestContext(t, map[string]string{
		"session-id": "stale-handshake-session",
	})
	body := []byte(`{"type":"response.create","client_metadata":{"x-codex-turn-metadata":"{\"session_id\":\"canonical-body-session\",\"thread_id\":\"canonical-body-thread\"}"},"input":"hello"}`)
	SetOpenAIOAuthIdentityCapture(c, CaptureOpenAIOAuthIdentity(c, body, ""))

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
		body,
		false,
	)
	require.NoError(t, err)
	require.True(t, resolution.OutboundIdentityModeEnabled)
	require.Equal(t, "canonical-body-session", resolution.OutboundIdentityLogicalKey)
	require.NotEqual(t, "stale-handshake-session", headers.Get("session-id"))
	require.Equal(t, resolution.OutboundIdentity.SessionID, headers.Get("session-id"))
	require.Empty(t, headers.Get("session_id"))
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

func TestApplyOpenAIOAuthIdentityPlanContextWindowIsNestedOnlyAndServerOwned(t *testing.T) {
	plan, err := FinalizeOpenAICodexWirePlan(
		codexWireProjectionTestPlan(t),
		string(CodexWireRequestTurn),
		CodexModelCapabilities{Known: true},
	)
	require.NoError(t, err)
	require.Equal(t, codexWireTestContextWindow, plan.Window.ContextWindowID)

	headers := http.Header{
		"Context-Window-Id":         {"attacker-header"},
		"X-Codex-Context-Window-Id": {"attacker-header"},
		"X-Codex-Context_window_id": {"attacker-header"},
		openAIWSTurnMetadataHeader:  {`{"context_window_id":"01989f44-7c00-7000-8000-000000000099","keep":"header"}`},
	}
	headers["context_window_id"] = []string{"attacker-header"}
	body := []byte(`{
		"context_window_id":"attacker-root",
		"x-codex-context-window-id":"attacker-root",
		"client_metadata":{
			"context_window_id":"attacker-flat",
			"context-window-id":"attacker-flat",
			"x-codex-context-window-id":"attacker-flat",
			"x-codex-context_window_id":"attacker-flat",
			"x-codex-turn-metadata":"{\"context_window_id\":\"01989f44-7c00-7000-8000-000000000099\",\"keep\":\"body\"}"
		}
	}`)

	out, err := ApplyOpenAIOAuthIdentityPlan(headers, body, plan)
	require.NoError(t, err)
	for _, name := range []string{
		"context_window_id", "context-window-id",
		"x-codex-context-window-id", "x-codex-context_window_id",
	} {
		require.Empty(t, headerValuesCaseInsensitive(headers, name), name)
		require.False(t, gjson.GetBytes(out, name).Exists(), name)
		require.False(t, gjson.GetBytes(out, "client_metadata."+name).Exists(), name)
	}

	headerNested := headers.Get(openAIWSTurnMetadataHeader)
	require.Equal(t, codexWireTestContextWindow, gjson.Get(headerNested, "context_window_id").String())
	require.Equal(t, "header", gjson.Get(headerNested, "keep").String())
	bodyNested := gjson.GetBytes(out, "client_metadata.x-codex-turn-metadata").String()
	require.Equal(t, codexWireTestContextWindow, gjson.Get(bodyNested, "context_window_id").String())
	require.Equal(t, "body", gjson.Get(bodyNested, "keep").String())
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

func TestOpenAIWSOutboundIdentityPlanDigestIgnoresRequestTurnFields(t *testing.T) {
	first := http.Header{openAIWSTurnMetadataHeader: {
		`{"installation_id":"stable-install","session_id":"stable-session","thread_id":"stable-thread","turn_id":"01989f44-7c00-7000-8000-000000000021","turn_started_at_unix_ms":1777777777001,"sandbox":"seatbelt","unknown":"one"}`,
	}}
	second := http.Header{openAIWSTurnMetadataHeader: {
		`{"installation_id":"other-raw-install","session_id":"other-raw-session","thread_id":"other-raw-thread","turn_id":"01989f44-7c00-7000-8000-000000000022","turn_started_at_unix_ms":1777777777002,"sandbox":"different","unknown":"two"}`,
	}}
	first.Set("x-codex-window-id", "window-a")
	second.Set("x-codex-window-id", "window-b")
	plan := OpenAIOAuthIdentityPlan{
		TurnIdentityRequested: true,
		InstallationPolicy:    OpenAIOAuthInstallationAccountPin,
		TurnIdentityEnabled:   true,
		TurnIdentity: OpenAICodexTurnIdentity{
			SessionID: "stable-session",
			ThreadID:  "stable-thread",
			Relation:  OpenAICodexTurnRelationRoot,
		},
		Window: OpenAICodexWindowSnapshot{
			ThreadID:        "stable-thread",
			ContextWindowID: codexWireTestContextWindow,
		},
	}
	require.Equal(t,
		openAIWSOutboundIdentityPlanDigest(first, plan),
		openAIWSOutboundIdentityPlanDigest(second, plan),
	)
	rotatedWindow := plan
	rotatedWindow.Window.ContextWindowID = "01989f44-7c00-7000-8000-000000000008"
	require.Equal(t,
		openAIWSOutboundIdentityPlanDigest(first, plan),
		openAIWSOutboundIdentityPlanDigest(first, rotatedWindow),
		"context window rotation must not split an otherwise reusable websocket",
	)

	stableChanged := plan
	stableChanged.TurnIdentity.ThreadID = "other-stable-thread"
	require.NotEqual(t,
		openAIWSOutboundIdentityPlanDigest(first, plan),
		openAIWSOutboundIdentityPlanDigest(second, stableChanged),
	)
}

func TestOpenAIWSOutboundIdentityPlanDigestIncludesMemoryHandshakeHeaders(t *testing.T) {
	plan := OpenAIOAuthIdentityPlan{
		TurnIdentityRequested: true,
		TurnIdentityEnabled:   true,
		TurnIdentity: OpenAICodexTurnIdentity{
			SessionID: "stable-session", ThreadID: "stable-thread", Relation: OpenAICodexTurnRelationRoot,
		},
	}
	base := http.Header{}
	memgen := base.Clone()
	memgen.Set("x-openai-memgen-request", "true")
	require.NotEqual(t,
		openAIWSOutboundIdentityPlanDigest(base, plan),
		openAIWSOutboundIdentityPlanDigest(memgen, plan),
	)

	guardian := memgen.Clone()
	guardian.Set("x-openai-subagent", "guardian")
	memory := memgen.Clone()
	memory.Set("x-openai-subagent", "memory_consolidation")
	require.NotEqual(t,
		openAIWSOutboundIdentityPlanDigest(guardian, plan),
		openAIWSOutboundIdentityPlanDigest(memory, plan),
	)
}

func TestOpenAIWSOutboundIdentityPlanDigestCoversCompleteHandshakeFingerprint(t *testing.T) {
	materialize := func(t *testing.T, input http.Header, plan OpenAIOAuthIdentityPlan) (http.Header, string) {
		t.Helper()
		headers := input.Clone()
		_, err := ApplyOpenAIOAuthIdentityPlan(headers, nil, plan)
		require.NoError(t, err)
		return headers, openAIWSOutboundIdentityPlanDigest(headers, plan)
	}

	base := OpenAIOAuthIdentityPlan{
		TurnIdentity: OpenAICodexTurnIdentity{
			SessionID: testOutboundSessionUUID,
			ThreadID:  testOutboundThreadUUID,
			Relation:  OpenAICodexTurnRelationDescendant,
		},
		TurnIdentityEnabled:   true,
		InstallationPolicy:    OpenAIOAuthInstallationAccountPin,
		InstallationEnabled:   true,
		InstallationID:        "11111111-2222-4333-8444-555555555555",
		ClientIdentityEnabled: true,
		ClientIdentity: CodexClientIdentityPlan{
			Mode:       CodexClientIdentityNormalize,
			UserAgent:  "codex_cli_rs/0.200.1 (Ubuntu 24.04; x86_64) xterm-256color",
			Originator: "codex_cli_rs",
			Version:    "0.200.1",
		},
	}
	baseInput := http.Header{
		"User-Agent": []string{"unrecognized-client/1.0"},
		"Originator": []string{"client"},
		"Version":    []string{"1.0"},
	}
	baseHeaders, baseDigest := materialize(t, baseInput, base)
	require.Len(t, baseDigest, 64)
	require.Equal(t, baseDigest, openAIWSOutboundIdentityPlanDigest(baseHeaders, base))

	installationChanged := base
	installationChanged.InstallationID = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	_, installationDigest := materialize(t, baseInput, installationChanged)
	require.NotEqual(t, baseDigest, installationDigest)

	clientChanged := base
	clientChanged.ClientIdentity.UserAgent = "codex_vscode/0.200.1 (Mac OS X 15.0; arm64) vscode"
	clientChanged.ClientIdentity.Originator = "codex_vscode"
	_, clientDigest := materialize(t, baseInput, clientChanged)
	require.NotEqual(t, baseDigest, clientDigest)

	safePair := base
	safePair.ClientIdentity.Mode = CodexClientIdentitySafePair
	cliInput := http.Header{
		"User-Agent": []string{"codex_cli_rs/0.200.1 (Windows 11.0.26100; x86_64) WindowsTerminal"},
		"Originator": []string{"codex_cli_rs"},
		"Version":    []string{"0.200.1"},
	}
	vscodeInput := http.Header{
		"User-Agent": []string{"codex_vscode/0.200.1 (Mac OS X 15.0; arm64) vscode"},
		"Originator": []string{"codex_vscode"},
		"Version":    []string{"0.200.1"},
	}
	cliHeaders, cliDigest := materialize(t, cliInput, safePair)
	_, vscodeDigest := materialize(t, vscodeInput, safePair)
	require.NotEqual(t, cliDigest, vscodeDigest, "recognized SafePair client identities must not share a socket")

	fallbackChanged := safePair
	fallbackChanged.ClientIdentity.UserAgent = "codex_vscode/0.200.1 (Linux; x86_64) vscode"
	fallbackChanged.ClientIdentity.Originator = "codex_vscode"
	_, sameWireDigest := materialize(t, cliInput, fallbackChanged)
	require.Equal(t, cliDigest, sameWireDigest, "unused SafePair fallback changes must not split identical wire identities")
	require.Equal(t, cliDigest, openAIWSOutboundIdentityPlanDigest(cliHeaders, safePair))

	turnDisabled := safePair
	turnDisabled.TurnIdentityEnabled = false
	turnDisabled.TurnIdentityRequested = false
	turnDisabled.TurnIdentity = OpenAICodexTurnIdentity{}
	_, flagOffCLI := materialize(t, cliInput, turnDisabled)
	_, flagOffVSCode := materialize(t, vscodeInput, turnDisabled)
	require.NotEqual(t, flagOffCLI, flagOffVSCode, "client identity still scopes a UUIDv7-disabled OAuth socket")

	flagOffInstallationChanged := turnDisabled
	flagOffInstallationChanged.InstallationID = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	_, flagOffInstallationDigest := materialize(t, cliInput, flagOffInstallationChanged)
	require.NotEqual(t, flagOffCLI, flagOffInstallationDigest, "installation identity still scopes a UUIDv7-disabled OAuth socket")
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
