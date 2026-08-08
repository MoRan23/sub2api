package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const (
	testOutboundSessionUUID = "018f5c3c-6e3a-7abc-8def-1234567890ab"
	testOutboundThreadUUID  = "018f5c3c-6e3a-7abd-8def-1234567890ac"
)

func TestOpenAIOutboundSessionIdentityKeyIsVersionedAndStable(t *testing.T) {
	key, err := OpenAIOutboundSessionIdentityKey("jwt-secret", "42", 9, "prompt-cache")
	require.NoError(t, err)
	require.Len(t, key, 64)
	require.Equal(t, key, mustOutboundIdentityKey(t, "jwt-secret", "42", 9, "prompt-cache"))
	otherNamespace, err := OpenAIOutboundSessionIdentityKey("jwt-secret", "43", 9, "prompt-cache")
	require.NoError(t, err)
	require.NotEqual(t, key, otherNamespace)
	_, err = OpenAIOutboundSessionIdentityKey("", "42", 9, "prompt-cache")
	require.Error(t, err)
}

func mustOutboundIdentityKey(t *testing.T, secret, namespace string, apiKeyID int64, logical string) string {
	t.Helper()
	key, err := OpenAIOutboundSessionIdentityKey(secret, namespace, apiKeyID, logical)
	require.NoError(t, err)
	return key
}

func TestNewOpenAIOutboundSessionIdentityIsUUIDv7Pair(t *testing.T) {
	first, err := newOpenAIOutboundSessionIdentity()
	require.NoError(t, err)
	second, err := newOpenAIOutboundSessionIdentity()
	require.NoError(t, err)
	require.NoError(t, ValidateOpenAIOutboundSessionIdentity(first))
	require.NoError(t, ValidateOpenAIOutboundSessionIdentity(second))
	require.NotEqual(t, first, second)
}

func TestValidateOpenAIOutboundSessionIdentityRejectsNonCanonicalUUIDForms(t *testing.T) {
	identity := OpenAIOutboundSessionIdentity{SessionID: testOutboundSessionUUID, ThreadID: testOutboundThreadUUID}
	withWhitespace := identity
	withWhitespace.SessionID = " " + withWhitespace.SessionID + " "
	require.Error(t, ValidateOpenAIOutboundSessionIdentity(withWhitespace))
	uppercase := identity
	uppercase.ThreadID = strings.ToUpper(uppercase.ThreadID)
	require.Error(t, ValidateOpenAIOutboundSessionIdentity(uppercase))
	braced := identity
	braced.SessionID = "{" + braced.SessionID + "}"
	require.Error(t, ValidateOpenAIOutboundSessionIdentity(braced))
}

func TestLocalOpenAIOutboundSessionIdentityStoreRefreshesAndWinsAtomically(t *testing.T) {
	store := NewLocalOpenAIOutboundSessionIdentityStore()
	candidate := OpenAIOutboundSessionIdentity{SessionID: testOutboundSessionUUID, ThreadID: testOutboundThreadUUID}
	winner, err := store.GetOrCreate(context.Background(), "same-key", candidate, 30*time.Millisecond)
	require.NoError(t, err)
	require.Equal(t, candidate, winner)
	other := OpenAIOutboundSessionIdentity{SessionID: "018f5c3c-6e3a-7abe-8def-1234567890ad", ThreadID: "018f5c3c-6e3a-7abf-8def-1234567890ae"}
	gotExisting, err := store.GetOrCreate(context.Background(), "same-key", other, 30*time.Millisecond)
	require.NoError(t, err)
	require.Equal(t, candidate, gotExisting)

	const workers = 24
	results := make([]OpenAIOutboundSessionIdentity, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			candidate := OpenAIOutboundSessionIdentity{SessionID: "018f5c3c-6e3a-7abc-8def-1234567890ab", ThreadID: "018f5c3c-6e3a-7abd-8def-1234567890ac"}
			results[i], _ = store.GetOrCreate(context.Background(), "concurrent-key", candidate, time.Minute)
		}(i)
	}
	wg.Wait()
	for _, got := range results {
		require.Equal(t, results[0], got)
	}

	// Expiry permits a new pair to take over.
	time.Sleep(40 * time.Millisecond)
	got, err := store.GetOrCreate(context.Background(), "same-key", other, time.Minute)
	require.NoError(t, err)
	require.Equal(t, other, got)
}

func TestLocalOpenAIOutboundSessionIdentityStoreEvictsLeastRecentlyUsedAtCapacity(t *testing.T) {
	store := newOpenAIOutboundSessionIdentityLocalStoreWithCapacity(2)
	first := OpenAIOutboundSessionIdentity{SessionID: testOutboundSessionUUID, ThreadID: testOutboundThreadUUID}
	second := OpenAIOutboundSessionIdentity{SessionID: "018f5c3c-6e3a-7abe-8def-1234567890ad", ThreadID: "018f5c3c-6e3a-7abf-8def-1234567890ae"}
	third := OpenAIOutboundSessionIdentity{SessionID: "018f5c3c-6e3a-7ac0-8def-1234567890af", ThreadID: "018f5c3c-6e3a-7ac1-8def-1234567890b0"}
	replacement := OpenAIOutboundSessionIdentity{SessionID: "018f5c3c-6e3a-7ac2-8def-1234567890b1", ThreadID: "018f5c3c-6e3a-7ac3-8def-1234567890b2"}

	_, err := store.GetOrCreate(context.Background(), "first", first, time.Minute)
	require.NoError(t, err)
	_, err = store.GetOrCreate(context.Background(), "second", second, time.Minute)
	require.NoError(t, err)
	// Refresh first so second is the least recently used live mapping.
	gotFirst, err := store.GetOrCreate(context.Background(), "first", replacement, time.Minute)
	require.NoError(t, err)
	require.Equal(t, first, gotFirst)
	_, err = store.GetOrCreate(context.Background(), "third", third, time.Minute)
	require.NoError(t, err)

	store.mu.Lock()
	_, firstPresent := store.entries["first"]
	_, secondPresent := store.entries["second"]
	_, thirdPresent := store.entries["third"]
	entryCount := len(store.entries)
	store.mu.Unlock()
	require.True(t, firstPresent)
	require.False(t, secondPresent)
	require.True(t, thirdPresent)
	require.Equal(t, 2, entryCount)
}

func TestLocalOpenAIOutboundSessionIdentityStorePrunesExpiredEntriesAcrossRecencyOrder(t *testing.T) {
	store := newOpenAIOutboundSessionIdentityLocalStoreWithCapacity(3)
	identities := []OpenAIOutboundSessionIdentity{
		{SessionID: testOutboundSessionUUID, ThreadID: testOutboundThreadUUID},
		{SessionID: "018f5c3c-6e3a-7abe-8def-1234567890ad", ThreadID: "018f5c3c-6e3a-7abf-8def-1234567890ae"},
		{SessionID: "018f5c3c-6e3a-7ac0-8def-1234567890af", ThreadID: "018f5c3c-6e3a-7ac1-8def-1234567890b0"},
		{SessionID: "018f5c3c-6e3a-7ac2-8def-1234567890b1", ThreadID: "018f5c3c-6e3a-7ac3-8def-1234567890b2"},
	}
	_, err := store.GetOrCreate(context.Background(), "long-lived-oldest", identities[0], time.Minute)
	require.NoError(t, err)
	_, err = store.GetOrCreate(context.Background(), "short-lived-middle", identities[1], 10*time.Millisecond)
	require.NoError(t, err)
	_, err = store.GetOrCreate(context.Background(), "long-lived-newest", identities[2], time.Minute)
	require.NoError(t, err)
	time.Sleep(40 * time.Millisecond)
	_, err = store.GetOrCreate(context.Background(), "new", identities[3], time.Minute)
	require.NoError(t, err)

	store.mu.Lock()
	_, expiredPresent := store.entries["short-lived-middle"]
	_, oldestPresent := store.entries["long-lived-oldest"]
	_, newestPresent := store.entries["long-lived-newest"]
	entryCount := len(store.entries)
	store.mu.Unlock()
	require.False(t, expiredPresent)
	require.True(t, oldestPresent, "expiry pruning must avoid evicting a live LRU entry")
	require.True(t, newestPresent)
	require.Equal(t, 3, entryCount)
}

func TestLocalOpenAIOutboundSessionIdentityStoreCapacityIsConcurrentSafe(t *testing.T) {
	const capacity = 16
	store := newOpenAIOutboundSessionIdentityLocalStoreWithCapacity(capacity)
	var wg sync.WaitGroup
	for i := 0; i < 128; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			candidate, err := newOpenAIOutboundSessionIdentity()
			if err != nil {
				t.Errorf("generate candidate: %v", err)
				return
			}
			if _, err = store.GetOrCreate(context.Background(), fmt.Sprintf("key-%03d", i), candidate, time.Minute); err != nil {
				t.Errorf("store candidate: %v", err)
			}
		}(i)
	}
	wg.Wait()
	store.mu.Lock()
	entryCount := len(store.entries)
	heapCount := len(store.expirations)
	recencyCount := store.recency.Len()
	storedIdentities := make([]OpenAIOutboundSessionIdentity, 0, entryCount)
	for _, entry := range store.entries {
		storedIdentities = append(storedIdentities, entry.identity)
	}
	store.mu.Unlock()
	require.LessOrEqual(t, entryCount, capacity)
	require.Equal(t, entryCount, heapCount)
	require.Equal(t, entryCount, recencyCount)
	for _, identity := range storedIdentities {
		require.NoError(t, ValidateOpenAIOutboundSessionIdentity(identity))
	}
}

func TestResolveOpenAIOutboundSessionIdentityUsesProcessFallback(t *testing.T) {
	// No cache is injected here; the resolver must remain usable and stable.
	svc := &OpenAIGatewayService{cfg: &config.Config{JWT: config.JWTConfig{Secret: "resolver-secret"}}}
	account := &Account{ID: 918273}
	first, ok, err := svc.resolveOpenAIOutboundSessionIdentity(context.Background(), nil, account, "logical-fallback-test")
	require.NoError(t, err)
	require.True(t, ok)
	second, ok, err := svc.resolveOpenAIOutboundSessionIdentity(context.Background(), nil, account, "logical-fallback-test")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, first, second)
	require.NoError(t, ValidateOpenAIOutboundSessionIdentity(first))
	_, ok, err = svc.resolveOpenAIOutboundSessionIdentity(context.Background(), nil, account, "   ")
	require.NoError(t, err)
	require.False(t, ok)
}

func TestApplyOpenAIOutboundSessionIdentityHeaders(t *testing.T) {
	headers := make(http.Header)
	identity := OpenAIOutboundSessionIdentity{SessionID: testOutboundSessionUUID, ThreadID: testOutboundThreadUUID}
	applyOpenAIOutboundSessionIdentityHeaders(headers, identity)
	require.Equal(t, identity.SessionID, headers.Get("session-id"))
	require.Equal(t, identity.SessionID, headers.Get("session_id"))
	require.Equal(t, identity.ThreadID, headers.Get("thread-id"))
	require.Equal(t, identity.ThreadID, headers.Get("conversation_id"))
	require.Equal(t, identity.ThreadID, headers.Get("x-client-request-id"))
	require.Empty(t, headers.Get("thread_id"))
	require.Empty(t, headers.Get("conversation-id"))
}

func TestApplyOpenAIOutboundSessionIdentityHeadersClearsStaleAliases(t *testing.T) {
	headers := http.Header{
		"Session-Id":          []string{"old-session"},
		"Session_Id":          []string{"old-session-underscore"},
		"Thread-Id":           []string{"old-thread"},
		"Thread_Id":           []string{"old-thread-underscore"},
		"Conversation-Id":     []string{"old-conversation"},
		"Conversation_Id":     []string{"old-conversation-underscore"},
		"X-Client-Request-Id": []string{"old-request"},
	}
	applyOpenAIOutboundSessionIdentityHeaders(headers, OpenAIOutboundSessionIdentity{SessionID: testOutboundSessionUUID})
	require.Equal(t, testOutboundSessionUUID, headers.Get("session-id"))
	require.Equal(t, testOutboundSessionUUID, headers.Get("session_id"))
	require.Empty(t, headers.Get("thread-id"))
	require.Empty(t, headers.Get("thread_id"))
	require.Empty(t, headers.Get("conversation_id"))
	require.Empty(t, headers.Get("conversation-id"))
	require.Empty(t, headers.Get("x-client-request-id"))
}

func TestApplyOpenAIOutboundSessionIdentityHeadersClearsNonCanonicalCaseAliases(t *testing.T) {
	headers := http.Header{
		"session-id":          []string{"old-session"},
		"SESSION_ID":          []string{"old-session-underscore"},
		"THREAD-ID":           []string{"old-thread"},
		"Conversation_Id":     []string{"old-conversation"},
		"X-CLIENT-REQUEST-ID": []string{"old-request"},
	}
	applyOpenAIOutboundSessionIdentityHeaders(headers, OpenAIOutboundSessionIdentity{SessionID: testOutboundSessionUUID, ThreadID: testOutboundThreadUUID})
	for key, values := range headers {
		for _, alias := range []string{"session-id", "session_id", "thread-id", "thread_id", "conversation_id", "conversation-id", "x-client-request-id"} {
			if strings.EqualFold(key, alias) {
				require.NotEqual(t, []string{"old-session"}, values)
				require.NotEqual(t, []string{"old-session-underscore"}, values)
				require.NotEqual(t, []string{"old-thread"}, values)
				require.NotEqual(t, []string{"old-conversation"}, values)
				require.NotEqual(t, []string{"old-request"}, values)
			}
		}
	}
}

func TestMergeOpenAIOutboundSessionIdentityBody(t *testing.T) {
	identity := OpenAIOutboundSessionIdentity{SessionID: testOutboundSessionUUID, ThreadID: testOutboundThreadUUID}
	body := []byte(`{"model":"gpt-5","client_metadata":{"existing":"keep"},"input":"hello"}`)
	merged, err := mergeOpenAIOutboundSessionIdentityBody(body, identity)
	require.NoError(t, err)
	require.Equal(t, identity.SessionID, gjson.GetBytes(merged, "client_metadata.session_id").String())
	require.Equal(t, identity.ThreadID, gjson.GetBytes(merged, "client_metadata.thread_id").String())
	require.Equal(t, "keep", gjson.GetBytes(merged, "client_metadata.existing").String())
	require.Contains(t, string(merged), `"model"`)

	unchanged, err := mergeOpenAIOutboundSessionIdentityBody([]byte("not-json"), identity)
	require.Error(t, err)
	require.Equal(t, "not-json", string(unchanged))
	invalidUTF8 := []byte{'{', '"', 'x', '"', ':', '"', 0xff, '"', '}'}
	unchanged, err = mergeOpenAIOutboundSessionIdentityBody(invalidUTF8, identity)
	require.Error(t, err)
	require.Equal(t, invalidUTF8, unchanged)
	require.True(t, strings.Contains(string(merged), "client_metadata"))
	aliases, err := mergeOpenAIOutboundSessionIdentityBody(
		[]byte(`{"client_metadata":{"session-id":"client-session","thread-id":"client-thread","conversation_id":"client-conversation","keep":"yes"}}`),
		identity,
	)
	require.NoError(t, err)
	require.Equal(t, identity.SessionID, gjson.GetBytes(aliases, "client_metadata.session_id").String())
	require.Equal(t, identity.ThreadID, gjson.GetBytes(aliases, "client_metadata.thread_id").String())
	require.False(t, gjson.GetBytes(aliases, "client_metadata.session-id").Exists())
	require.False(t, gjson.GetBytes(aliases, "client_metadata.thread-id").Exists())
	require.False(t, gjson.GetBytes(aliases, "client_metadata.conversation_id").Exists())
	require.Equal(t, "yes", gjson.GetBytes(aliases, "client_metadata.keep").String())

	duplicateObjects := []byte(`{"model":"gpt-5","client_metadata":{"session_id":"first-session","keep":"first"},"client_metadata":{"session_id":"last-session","thread-id":"client-thread","keep":"last"},"prompt_cache_key":"first-prompt","prompt_cache_key":"last-prompt"}`)
	resolved := ResolveOpenAIOutboundSessionLogicalKey(newOutboundIdentityTestContext(t, nil), duplicateObjects, "")
	require.Equal(t, "last-session", resolved, "logical-key resolution must use the same last-value-wins object semantics as body merging")
	canonical, err := mergeOpenAIOutboundSessionIdentityBody(duplicateObjects, identity)
	require.NoError(t, err)
	canonicalText := string(canonical)
	// A structured round-trip must collapse duplicate top-level names before
	// the server-owned pair is written.
	require.Equal(t, 1, strings.Count(canonicalText, `"client_metadata"`))
	require.Equal(t, 1, strings.Count(canonicalText, `"prompt_cache_key"`))
	require.Equal(t, "last-prompt", gjson.GetBytes(canonical, "prompt_cache_key").String())
	require.Equal(t, "last", gjson.GetBytes(canonical, "client_metadata.keep").String())
	require.Equal(t, identity.SessionID, gjson.GetBytes(canonical, "client_metadata.session_id").String())
	require.Equal(t, identity.ThreadID, gjson.GetBytes(canonical, "client_metadata.thread_id").String())
	require.Equal(t, 1, strings.Count(canonicalText, `"session_id"`))
	require.Equal(t, 1, strings.Count(canonicalText, `"thread_id"`))
	require.False(t, gjson.GetBytes(canonical, "client_metadata.thread-id").Exists())

	duplicateFields := []byte(`{"client_metadata":{"session_id":"first-session","session_id":"second-session","thread_id":"first-thread","thread_id":"second-thread","conversation_id":"client-conversation"}}`)
	canonicalFields, err := mergeOpenAIOutboundSessionIdentityBody(duplicateFields, identity)
	require.NoError(t, err)
	canonicalFieldsText := string(canonicalFields)
	require.Equal(t, 1, strings.Count(canonicalFieldsText, `"client_metadata"`))
	require.Equal(t, 1, strings.Count(canonicalFieldsText, `"session_id"`))
	require.Equal(t, 1, strings.Count(canonicalFieldsText, `"thread_id"`))
	require.Equal(t, identity.SessionID, gjson.GetBytes(canonicalFields, "client_metadata.session_id").String())
	require.Equal(t, identity.ThreadID, gjson.GetBytes(canonicalFields, "client_metadata.thread_id").String())
	require.False(t, gjson.GetBytes(canonicalFields, "client_metadata.conversation_id").Exists())
	unchanged, err = mergeOpenAIOutboundSessionIdentityBody([]byte("null"), identity)
	require.Error(t, err)
	require.Equal(t, "null", string(unchanged))
}

func newOutboundIdentityTestContext(t *testing.T, headers map[string]string) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	for name, value := range headers {
		c.Request.Header.Set(name, value)
	}
	return c
}

func TestResolveOpenAIOutboundSessionLogicalKeyHeaderAliases(t *testing.T) {
	tests := []struct {
		name   string
		header string
		source string
	}{
		{name: "session hyphen", header: "session-id", source: OpenAIOutboundSessionLogicalKeySourceHeaderSession},
		{name: "session underscore", header: "session_id", source: OpenAIOutboundSessionLogicalKeySourceHeaderSession},
		{name: "thread hyphen", header: "thread-id", source: OpenAIOutboundSessionLogicalKeySourceHeaderThread},
		{name: "thread underscore", header: "thread_id", source: OpenAIOutboundSessionLogicalKeySourceHeaderThread},
		{name: "conversation hyphen", header: "conversation-id", source: OpenAIOutboundSessionLogicalKeySourceHeaderConversation},
		{name: "conversation underscore", header: "conversation_id", source: OpenAIOutboundSessionLogicalKeySourceHeaderConversation},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveOpenAIOutboundSessionLogicalKeyWithSource(newOutboundIdentityTestContext(t, map[string]string{tt.header: "  value-1  "}), nil, "")
			require.Equal(t, "value-1", got.LogicalKey)
			require.Equal(t, tt.source, got.Source)
		})
	}
}

func TestResolveOpenAIOutboundSessionLogicalKeyAffinityHeaders(t *testing.T) {
	for _, header := range openAIOutboundSessionAffinityHeaders {
		t.Run(header, func(t *testing.T) {
			got := ResolveOpenAIOutboundSessionLogicalKeyWithSource(newOutboundIdentityTestContext(t, map[string]string{header: "affinity-key"}), nil, "")
			require.Equal(t, "affinity-key", got.LogicalKey)
			require.Equal(t, OpenAIOutboundSessionLogicalKeySourceHeaderAffinity, got.Source)
		})
	}
}

func TestResolveOpenAIOutboundSessionLogicalKeyPriority(t *testing.T) {
	c := newOutboundIdentityTestContext(t, map[string]string{
		"session-id":                  "header-session",
		"thread_id":                   "header-thread",
		"conversation-id":             "header-conversation",
		openCodeSessionAffinityHeader: "header-affinity",
		openAIWSTurnMetadataHeader:    `{"thread_id":"turn-header"}`,
	})
	body := []byte(`{"client_metadata":{"conversation_id":"metadata-conversation","x-codex-turn-metadata":{"session_id":"turn-metadata"}},"x-codex-turn-metadata":{"thread_id":"turn-body"},"prompt_cache_key":"prompt-key"}`)
	got := ResolveOpenAIOutboundSessionLogicalKeyWithSource(c, body, "caller-seed")
	require.Equal(t, "header-session", got.LogicalKey)
	require.Equal(t, OpenAIOutboundSessionLogicalKeySourceHeaderSession, got.Source)
}

func TestResolveOpenAIOutboundSessionLogicalKeyClientMetadataAndTurnMetadata(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		header string
		want   string
		source string
	}{
		{
			name:   "client metadata direct",
			body:   `{"client_metadata":{"thread_id":"metadata-thread"}}`,
			want:   "metadata-thread",
			source: OpenAIOutboundSessionLogicalKeySourceClientMetadata,
		},
		{
			name:   "turn metadata header json",
			header: `{"conversation_id":"turn-header"}`,
			want:   "turn-header",
			source: OpenAIOutboundSessionLogicalKeySourceTurnMetadata,
		},
		{
			name:   "turn metadata body json string",
			body:   `{"x-codex-turn-metadata":"{\"session_id\":\"turn-body-string\"}"}`,
			want:   "turn-body-string",
			source: OpenAIOutboundSessionLogicalKeySourceTurnMetadata,
		},
		{
			name:   "turn metadata body object",
			body:   `{"x-codex-turn-metadata":{"thread_id":"turn-body-object"}}`,
			want:   "turn-body-object",
			source: OpenAIOutboundSessionLogicalKeySourceTurnMetadata,
		},
		{
			name:   "turn metadata nested client metadata",
			body:   `{"client_metadata":{"x-codex-turn-metadata":"{\"conversation_id\":\"turn-nested\"}"}}`,
			want:   "turn-nested",
			source: OpenAIOutboundSessionLogicalKeySourceTurnMetadata,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			headers := map[string]string{}
			if tt.header != "" {
				headers[openAIWSTurnMetadataHeader] = tt.header
			}
			got := ResolveOpenAIOutboundSessionLogicalKeyWithSource(newOutboundIdentityTestContext(t, headers), []byte(tt.body), "")
			require.Equal(t, tt.want, got.LogicalKey)
			require.Equal(t, tt.source, got.Source)
		})
	}
}

func TestResolveOpenAIOutboundSessionLogicalKeyInvalidHigherPriorityFallsThrough(t *testing.T) {
	c := newOutboundIdentityTestContext(t, map[string]string{
		"session-id": "\x01invalid",
		"thread-id":  "thread-fallback",
	})
	got := ResolveOpenAIOutboundSessionLogicalKeyWithSource(c, nil, "caller-seed")
	require.Equal(t, "thread-fallback", got.LogicalKey)
	require.Equal(t, OpenAIOutboundSessionLogicalKeySourceHeaderThread, got.Source)

	// An invalid metadata value must not block a valid turn-metadata value.
	c = newOutboundIdentityTestContext(t, nil)
	body := []byte(`{"client_metadata":{"session_id":"\u0001bad"},"x-codex-turn-metadata":{"conversation_id":"turn-fallback"}}`)
	got = ResolveOpenAIOutboundSessionLogicalKeyWithSource(c, body, "")
	require.Equal(t, "turn-fallback", got.LogicalKey)
	require.Equal(t, OpenAIOutboundSessionLogicalKeySourceTurnMetadata, got.Source)
}

func TestResolveOpenAIOutboundSessionLogicalKeyCallerSeedAndPromptCache(t *testing.T) {
	c := newOutboundIdentityTestContext(t, nil)
	body := []byte(`{"prompt_cache_key":"body-prompt"}`)
	got := ResolveOpenAIOutboundSessionLogicalKeyWithSource(c, body, "caller-seed")
	require.Equal(t, "caller-seed", got.LogicalKey)
	require.Equal(t, OpenAIOutboundSessionLogicalKeySourceCallerSeed, got.Source)

	got = ResolveOpenAIOutboundSessionLogicalKeyWithSource(c, body, "")
	require.Equal(t, "body-prompt", got.LogicalKey)
	require.Equal(t, OpenAIOutboundSessionLogicalKeySourcePromptCacheKey, got.Source)

	got = ResolveOpenAIOutboundSessionLogicalKeyWithSource(c, []byte(`{"x-codex-turn-metadata":"not-json","prompt_cache_key":"body-prompt"}`), "")
	require.Equal(t, "body-prompt", got.LogicalKey, "malformed turn metadata should be ignored")
}

func TestResolveOpenAIOutboundSessionLogicalKeyExcludesNonSessionIDs(t *testing.T) {
	c := newOutboundIdentityTestContext(t, map[string]string{
		"x-request-id":         "request-header",
		"x-message-id":         "message-header",
		"x-response-id":        "response-header",
		codexInstallationIDKey: "installation-header",
	})
	body := []byte(`{"request_id":"request-body","message_id":"message-body","response_id":"response-body","installation_id":"installation-body","client_metadata":{"request_id":"nested-request","message_id":"nested-message","response_id":"nested-response","installation_id":"nested-installation"},"x-codex-turn-metadata":{"request_id":"turn-request","message_id":"turn-message","response_id":"turn-response","installation_id":"turn-installation"}}`)
	require.Empty(t, ResolveOpenAIOutboundSessionLogicalKey(c, body, ""))
}

func TestResolveOpenAIOutboundSessionLogicalKeyRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name string
		body []byte
		seed string
	}{
		{name: "invalid utf8 seed", seed: string([]byte{0xff, 0xfe})},
		{name: "control seed", seed: "bad\nseed"},
		{name: "too long seed", seed: strings.Repeat("x", 256)},
		{name: "control body prompt", body: []byte(`{"prompt_cache_key":"bad\u0000key"}`)},
		{name: "too long body prompt", body: []byte(`{"prompt_cache_key":"` + strings.Repeat("x", 256) + `"}`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Empty(t, ResolveOpenAIOutboundSessionLogicalKey(newOutboundIdentityTestContext(t, nil), tt.body, tt.seed))
		})
	}
	// Invalid UTF-8 cannot be valid JSON and is ignored without surfacing raw
	// bytes to logs or metrics.
	require.Empty(t, ResolveOpenAIOutboundSessionLogicalKey(newOutboundIdentityTestContext(t, nil), []byte{'{', '"', 'p', 'r', 'o', 'm', 'p', 't', '_', 'c', 'a', 'c', 'h', 'e', '_', 'k', 'e', 'y', '"', ':', '"', 0xff, '"', '}'}, ""))
}

type outboundIdentityAccountRepoStub struct {
	AccountRepository
	accounts map[int64]*Account
}

func (r *outboundIdentityAccountRepoStub) GetByID(_ context.Context, id int64) (*Account, error) {
	if account, ok := r.accounts[id]; ok {
		return account, nil
	}
	return nil, errors.New("account not found")
}

func TestOpenAIOutboundSessionIdentityNamespaceUsesCredentialOwner(t *testing.T) {
	parentID := int64(701)
	shadowID := int64(702)
	parent := &Account{ID: parentID, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	shadow := &Account{ID: shadowID, Platform: PlatformOpenAI, Type: AccountTypeOAuth, ParentAccountID: &parentID}
	repo := &outboundIdentityAccountRepoStub{accounts: map[int64]*Account{parentID: parent}}
	svc := &OpenAIGatewayService{accountRepo: repo}
	namespace, err := svc.resolveOpenAIOutboundSessionIdentityNamespace(context.Background(), shadow)
	require.NoError(t, err)
	require.Equal(t, "account:701", namespace)

	repo.accounts[parentID] = &Account{ID: parentID, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	_, err = svc.resolveOpenAIOutboundSessionIdentityNamespace(context.Background(), shadow)
	require.Error(t, err)
	require.ErrorIs(t, err, errOpenAIOutboundSessionIdentityNamespace)

	nestedID := int64(703)
	nested := &Account{ID: nestedID, Platform: PlatformOpenAI, Type: AccountTypeOAuth, ParentAccountID: &parentID}
	repo.accounts[parentID] = nested
	_, err = svc.resolveOpenAIOutboundSessionIdentityNamespace(context.Background(), shadow)
	require.Error(t, err)
	require.ErrorIs(t, err, errOpenAIOutboundSessionIdentityNamespace)

	nilRepoSvc := &OpenAIGatewayService{}
	namespace, err = nilRepoSvc.resolveOpenAIOutboundSessionIdentityNamespace(context.Background(), shadow)
	require.NoError(t, err)
	require.Equal(t, "account:701", namespace)
}

func TestOpenAIOutboundSessionIdentityNamespaceUsesCurrentAccountForAPIKeyShadow(t *testing.T) {
	parentID := int64(711)
	shadowID := int64(712)
	shadow := &Account{ID: shadowID, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, ParentAccountID: &parentID}
	namespace, err := (&OpenAIGatewayService{}).resolveOpenAIOutboundSessionIdentityNamespace(context.Background(), shadow)
	require.NoError(t, err)
	require.Equal(t, "account:712", namespace)
}

func TestResolveOpenAIOutboundSessionIdentityDoesNotReResolveSelectedKey(t *testing.T) {
	previous := processOpenAIOutboundSessionIdentityStore
	processOpenAIOutboundSessionIdentityStore = newOpenAIOutboundSessionIdentityLocalStore()
	t.Cleanup(func() { processOpenAIOutboundSessionIdentityStore = previous })
	svc := &OpenAIGatewayService{cfg: &config.Config{JWT: config.JWTConfig{Secret: "selected-key-secret"}}}
	c := newOutboundIdentityTestContext(t, map[string]string{"session-id": "header-key"})
	first, ok, err := svc.resolveOpenAIOutboundSessionIdentity(context.Background(), c, &Account{ID: 801}, "selected-seed")
	require.NoError(t, err)
	require.True(t, ok)
	second, ok, err := svc.resolveOpenAIOutboundSessionIdentity(context.Background(), nil, &Account{ID: 801}, "selected-seed")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, first, second)
}

type outboundIdentityGatewayCacheStub struct {
	GatewayCache
	mu          sync.Mutex
	fail        bool
	winner      OpenAIOutboundSessionIdentity
	hasWinner   bool
	candidates  []OpenAIOutboundSessionIdentity
	mappingKeys []string
	callCounter int
	storeErr    error
}

func (s *outboundIdentityGatewayCacheStub) GetOrCreate(_ context.Context, mappingKey string, candidate OpenAIOutboundSessionIdentity, _ time.Duration) (OpenAIOutboundSessionIdentity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.callCounter++
	s.mappingKeys = append(s.mappingKeys, mappingKey)
	s.candidates = append(s.candidates, candidate)
	if s.storeErr != nil {
		return OpenAIOutboundSessionIdentity{}, s.storeErr
	}
	if s.fail {
		return OpenAIOutboundSessionIdentity{}, errors.New("redis unavailable")
	}
	if !s.hasWinner {
		s.winner = candidate
		s.hasWinner = true
	}
	return s.winner, nil
}

func TestResolveOpenAIOutboundSessionIdentityPromotesProcessFallbackOnRecovery(t *testing.T) {
	previousStore := processOpenAIOutboundSessionIdentityStore
	processOpenAIOutboundSessionIdentityStore = newOpenAIOutboundSessionIdentityLocalStore()
	t.Cleanup(func() { processOpenAIOutboundSessionIdentityStore = previousStore })
	cache := &outboundIdentityGatewayCacheStub{fail: true}
	svc := &OpenAIGatewayService{cache: cache, cfg: &config.Config{JWT: config.JWTConfig{Secret: "promotion-secret"}}}
	account := &Account{ID: 802}
	before := SnapshotOpenAIOutboundSessionIdentityRuntimeMetrics()
	local, ok, err := svc.resolveOpenAIOutboundSessionIdentity(context.Background(), nil, account, "promotion-key")
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, ValidateOpenAIOutboundSessionIdentity(local))

	cache.mu.Lock()
	cache.fail = false
	cache.mu.Unlock()
	recovered, ok, err := svc.resolveOpenAIOutboundSessionIdentity(context.Background(), nil, account, "promotion-key")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, local, recovered)
	stable, ok, err := svc.resolveOpenAIOutboundSessionIdentity(context.Background(), nil, account, "promotion-key")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, local, stable)

	cache.mu.Lock()
	require.GreaterOrEqual(t, cache.callCounter, 3)
	require.Equal(t, local, cache.candidates[0])
	require.Equal(t, local, cache.candidates[1], "recovery must receive the process winner as candidate")
	require.Equal(t, local, cache.winner, "Redis winner must remain stable")
	cache.mu.Unlock()
	after := SnapshotOpenAIOutboundSessionIdentityRuntimeMetrics()
	require.Equal(t, int64(1), after.PromotionTotal-before.PromotionTotal)
}

func TestResolveOpenAIOutboundSessionIdentitySeparatesInvalidPrimaryOutputMetric(t *testing.T) {
	previousStore := processOpenAIOutboundSessionIdentityStore
	processOpenAIOutboundSessionIdentityStore = newOpenAIOutboundSessionIdentityLocalStore()
	t.Cleanup(func() { processOpenAIOutboundSessionIdentityStore = previousStore })
	cache := &outboundIdentityGatewayCacheStub{winner: OpenAIOutboundSessionIdentity{SessionID: "bad", ThreadID: "bad"}, hasWinner: true}
	svc := &OpenAIGatewayService{cache: cache, cfg: &config.Config{JWT: config.JWTConfig{Secret: "invalid-output-secret"}}}
	before := SnapshotOpenAIOutboundSessionIdentityRuntimeMetrics()
	got, ok, err := svc.resolveOpenAIOutboundSessionIdentity(context.Background(), nil, &Account{ID: 803}, "invalid-output-key")
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, ValidateOpenAIOutboundSessionIdentity(got))
	after := SnapshotOpenAIOutboundSessionIdentityRuntimeMetrics()
	require.Equal(t, int64(1), after.PrimaryStoreInvalidTotal-before.PrimaryStoreInvalidTotal)
	require.Equal(t, int64(0), after.PrimaryStoreFailureTotal-before.PrimaryStoreFailureTotal)

	cache.storeErr = fmt.Errorf("corrupt payload: %w", ErrOpenAIOutboundSessionIdentityStoredValueInvalid)
	beforeInvalidError := SnapshotOpenAIOutboundSessionIdentityRuntimeMetrics()
	_, ok, err = svc.resolveOpenAIOutboundSessionIdentity(context.Background(), nil, &Account{ID: 803}, "invalid-error-key")
	require.NoError(t, err)
	require.True(t, ok)
	afterInvalidError := SnapshotOpenAIOutboundSessionIdentityRuntimeMetrics()
	require.Equal(t, int64(1), afterInvalidError.PrimaryStoreInvalidTotal-beforeInvalidError.PrimaryStoreInvalidTotal)

	cache.storeErr = errors.New("ordinary redis outage")
	beforeOrdinary := SnapshotOpenAIOutboundSessionIdentityRuntimeMetrics()
	_, ok, err = svc.resolveOpenAIOutboundSessionIdentity(context.Background(), nil, &Account{ID: 803}, "ordinary-error-key")
	require.NoError(t, err)
	require.True(t, ok)
	afterOrdinary := SnapshotOpenAIOutboundSessionIdentityRuntimeMetrics()
	require.Equal(t, int64(1), afterOrdinary.PrimaryStoreFailureTotal-beforeOrdinary.PrimaryStoreFailureTotal)
	require.Equal(t, int64(0), afterOrdinary.PrimaryStoreInvalidTotal-beforeOrdinary.PrimaryStoreInvalidTotal)
}
