package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

const (
	testOutboundSessionUUID = "018f5c3c-6e3a-7abc-8def-1234567890ab"
	testOutboundThreadUUID  = "018f5c3c-6e3a-7abd-8def-1234567890ac"
)

func resetProcessCodexIdentityStore(t *testing.T) {
	t.Helper()
	previousV2 := processOpenAICodexTurnIdentityStore
	store := newOpenAICodexIdentityLocalStore()
	processOpenAICodexTurnIdentityStore = store
	t.Cleanup(func() {
		processOpenAICodexTurnIdentityStore = previousV2
	})
}

func TestValidateOpenAICodexTurnIdentityLifecycle(t *testing.T) {
	root := OpenAICodexTurnIdentity{
		SessionID: testOutboundSessionUUID,
		ThreadID:  testOutboundSessionUUID,
		Relation:  OpenAICodexTurnRelationRoot,
	}
	require.NoError(t, ValidateOpenAICodexTurnIdentity(root))
	descendant := OpenAICodexTurnIdentity{
		SessionID:          testOutboundSessionUUID,
		ThreadID:           testOutboundThreadUUID,
		ParentThreadID:     testOutboundSessionUUID,
		ForkedFromThreadID: testOutboundSessionUUID,
		Relation:           OpenAICodexTurnRelationDescendant,
	}
	require.NoError(t, ValidateOpenAICodexTurnIdentity(descendant))

	root.ThreadID = testOutboundThreadUUID
	require.Error(t, ValidateOpenAICodexTurnIdentity(root))
	descendant.ThreadID = descendant.SessionID
	require.Error(t, ValidateOpenAICodexTurnIdentity(descendant))
	descendant.ThreadID = "11111111-1111-4111-8111-111111111111"
	require.Error(t, ValidateOpenAICodexTurnIdentity(descendant))
}

func TestOpenAICodexMappingKeysAreDomainSeparated(t *testing.T) {
	sessionA, err := OpenAICodexSessionMappingKey("secret", "account:4", 9, "session")
	require.NoError(t, err)
	sessionAgain, err := OpenAICodexSessionMappingKey("secret", "account:4", 9, "session")
	require.NoError(t, err)
	require.Equal(t, sessionA, sessionAgain)
	require.Len(t, sessionA, 64)

	threadA, err := OpenAICodexThreadMappingKey("secret", "account:4", 9, "session", "thread-a", testOutboundSessionUUID)
	require.NoError(t, err)
	threadB, err := OpenAICodexThreadMappingKey("secret", "account:4", 9, "session", "thread-b", testOutboundSessionUUID)
	require.NoError(t, err)
	require.NotEqual(t, sessionA, threadA)
	require.NotEqual(t, threadA, threadB)
	changedAPIKey, err := OpenAICodexSessionMappingKey("secret", "account:4", 10, "session")
	require.NoError(t, err)
	require.NotEqual(t, sessionA, changedAPIKey)
}

func newCodexLogicalResolverContext(t *testing.T, headers http.Header) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request.Header = headers
	return c
}

func newOutboundIdentityTestContext(t *testing.T, headers map[string]string) *gin.Context {
	t.Helper()
	httpHeaders := make(http.Header)
	for name, value := range headers {
		httpHeaders.Set(name, value)
	}
	return newCodexLogicalResolverContext(t, httpHeaders)
}

func TestResolveOpenAICodexLogicalTurnIdentityPriorityAndPromptNeutrality(t *testing.T) {
	headers := make(http.Header)
	headers.Set("session-id", "header-session")
	headers.Set("thread-id", "header-thread")
	headers.Set(openAIWSTurnMetadataHeader, `{"session_id":"header-meta-session","thread_id":"header-meta-thread"}`)
	body := []byte(`{
      "prompt_cache_key":"cache-a",
      "client_metadata":{
        "session_id":"flat-session",
        "thread_id":"flat-thread",
        "x-codex-turn-metadata":"{\"session_id\":\"canonical-session\",\"thread_id\":\"canonical-thread\",\"parent_thread_id\":\"parent\",\"forked_from_thread_id\":\"fork\"}"
      }
    }`)
	resolved := ResolveOpenAICodexLogicalTurnIdentity(newCodexLogicalResolverContext(t, headers), body, "caller")
	require.Equal(t, "canonical-session", resolved.SessionKey)
	require.Equal(t, "canonical-thread", resolved.ThreadKey)
	require.Equal(t, "parent", resolved.ParentThreadKey)
	require.Equal(t, "fork", resolved.ForkedFromThreadKey)
	require.Equal(t, OpenAICodexTurnRelationDescendant, resolved.Relation)
	require.True(t, resolved.Explicit)

	body = []byte(strings.ReplaceAll(string(body), "cache-a", "cache-b"))
	changedPrompt := ResolveOpenAICodexLogicalTurnIdentity(newCodexLogicalResolverContext(t, headers), body, "different-caller")
	require.Equal(t, resolved.SessionKey, changedPrompt.SessionKey)
	require.Equal(t, resolved.ThreadKey, changedPrompt.ThreadKey)
}

func TestResolveOpenAICodexLogicalTurnIdentitySingleFieldBecomesRoot(t *testing.T) {
	for name, headers := range map[string]http.Header{
		"session": {"session-id": []string{"only-session"}},
		"thread":  {"thread-id": []string{"only-thread"}},
	} {
		t.Run(name, func(t *testing.T) {
			resolved := ResolveOpenAICodexLogicalTurnIdentity(newCodexLogicalResolverContext(t, headers), []byte(`{"prompt_cache_key":"ignored"}`), "")
			require.NotEmpty(t, resolved.SessionKey)
			require.Equal(t, resolved.SessionKey, resolved.ThreadKey)
			require.Equal(t, OpenAICodexTurnRelationRoot, resolved.Relation)
			require.True(t, resolved.Explicit)
		})
	}
	fallback := ResolveOpenAICodexLogicalTurnIdentity(nil, []byte(`{"prompt_cache_key":"cache-only"}`), "")
	require.Equal(t, "cache-only", fallback.SessionKey)
	require.Equal(t, fallback.SessionKey, fallback.ThreadKey)
	require.False(t, fallback.Explicit)
}

func TestResolveOpenAICodexLogicalTurnIdentityRejectsUnsafeAndExcludedIDs(t *testing.T) {
	headers := make(http.Header)
	headers.Set("session-id", "\x01unsafe")
	headers.Set("thread-id", strings.Repeat("x", 256))
	headers.Set("x-client-request-id", "must-not-be-identity")
	body := []byte(`{
      "installation_id":"installation",
      "request_id":"request",
      "message_id":"message",
      "response_id":"response",
      "turn_id":"turn",
      "client_metadata":{"installation_id":"nested-installation"},
      "prompt_cache_key":"safe-fallback"
    }`)
	resolved := ResolveOpenAICodexLogicalTurnIdentity(newCodexLogicalResolverContext(t, headers), body, "")
	require.Equal(t, "safe-fallback", resolved.SessionKey)
	require.Equal(t, resolved.SessionKey, resolved.ThreadKey)
	require.False(t, resolved.Explicit)

	withoutFallback := ResolveOpenAICodexLogicalTurnIdentity(newCodexLogicalResolverContext(t, headers), []byte(`{"installation_id":"only-id"}`), "")
	require.Empty(t, withoutFallback.SessionKey)
	require.Empty(t, withoutFallback.ThreadKey)
}

func TestLocalOpenAICodexStorePreservesHierarchy(t *testing.T) {
	store := newOpenAICodexIdentityLocalStore()
	ctx := context.Background()
	sessionID, err := store.GetOrCreateCodexSession(ctx, "session-key", testOutboundSessionUUID, time.Minute)
	require.NoError(t, err)
	require.Equal(t, testOutboundSessionUUID, sessionID)

	childOne, err := store.GetOrCreateCodexThread(ctx, "session-key", "thread-one", sessionID, testOutboundThreadUUID, time.Minute)
	require.NoError(t, err)
	otherThread := "018f5c3c-6e3a-7abe-8def-1234567890ad"
	childTwo, err := store.GetOrCreateCodexThread(ctx, "session-key", "thread-two", sessionID, otherThread, time.Minute)
	require.NoError(t, err)
	require.Equal(t, childOne.SessionID, childTwo.SessionID)
	require.NotEqual(t, childOne.ThreadID, childTwo.ThreadID)

	stable, err := store.GetOrCreateCodexThread(ctx, "session-key", "thread-one", sessionID, otherThread, time.Minute)
	require.NoError(t, err)
	require.Equal(t, childOne, stable)
	_, err = store.GetOrCreateCodexThread(ctx, "session-key", "thread-three", "018f5c3c-6e3a-7abf-8def-1234567890ae", otherThread, time.Minute)
	require.ErrorIs(t, err, ErrOpenAICodexSessionWinnerChanged)
}

func TestOpenAICodexResolutionRetriesLocalThreadAfterSessionPromotion(t *testing.T) {
	local := newOpenAICodexIdentityLocalStore()
	ctx := context.Background()
	const sessionDigest = "session-promotion-race"
	initialSessionID, err := local.GetOrCreateCodexSession(ctx, sessionDigest, testOutboundSessionUUID, time.Minute)
	require.NoError(t, err)

	state := &openAICodexIdentityResolutionState{
		ctx:            ctx,
		local:          local,
		namespace:      "account:42",
		apiKeyID:       7,
		usePrimary:     false,
		sessionDigest:  sessionDigest,
		logicalSession: "logical-session",
		sessionID:      initialSessionID,
	}
	sharedWinner := "018f5c3c-6e3a-7abe-8def-1234567890ad"
	local.promoteSession(sessionDigest, sharedWinner, time.Minute)

	threadID, err := state.resolveThread("logical-child")
	require.NoError(t, err)
	require.Equal(t, sharedWinner, state.sessionID)
	require.NotEqual(t, sharedWinner, threadID)
	require.NoError(t, ValidateOpenAICodexTurnIdentity(OpenAICodexTurnIdentity{
		SessionID: sharedWinner,
		ThreadID:  threadID,
		Relation:  OpenAICodexTurnRelationDescendant,
	}))
}

func TestResolveOpenAICodexTurnIdentityRootChildrenAndRelations(t *testing.T) {
	resetProcessCodexIdentityStore(t)
	svc := &OpenAIGatewayService{cfg: &config.Config{JWT: config.JWTConfig{Secret: "hierarchy-secret"}}}
	account := &Account{ID: 501, Type: AccountTypeOAuth}
	rootLogical := normalizeLogicalTuple(openAICodexLogicalTuple{session: "logical-session", thread: "logical-session"}, "test", true)
	root, ok, err := svc.resolveOpenAICodexTurnIdentity(context.Background(), nil, account, rootLogical)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, root.SessionID, root.ThreadID)
	require.Equal(t, OpenAICodexTurnRelationRoot, root.Relation)

	childOneLogical := normalizeLogicalTuple(openAICodexLogicalTuple{session: "logical-session", thread: "child-one", parent: "logical-session"}, "test", true)
	childOne, ok, err := svc.resolveOpenAICodexTurnIdentity(context.Background(), nil, account, childOneLogical)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, root.SessionID, childOne.SessionID)
	require.NotEqual(t, childOne.SessionID, childOne.ThreadID)
	require.Equal(t, root.ThreadID, childOne.ParentThreadID)

	childTwoLogical := normalizeLogicalTuple(openAICodexLogicalTuple{session: "logical-session", thread: "child-two", parent: "child-one", fork: "child-one"}, "test", true)
	childTwo, ok, err := svc.resolveOpenAICodexTurnIdentity(context.Background(), nil, account, childTwoLogical)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, root.SessionID, childTwo.SessionID)
	require.NotEqual(t, childOne.ThreadID, childTwo.ThreadID)
	require.Equal(t, childOne.ThreadID, childTwo.ParentThreadID)
	require.Equal(t, childOne.ThreadID, childTwo.ForkedFromThreadID)

	stable, _, err := svc.resolveOpenAICodexTurnIdentity(context.Background(), nil, account, childTwoLogical)
	require.NoError(t, err)
	require.Equal(t, childTwo, stable)
}

func TestResolveOpenAICodexTurnIdentityIsolationDimensions(t *testing.T) {
	resetProcessCodexIdentityStore(t)
	svc := &OpenAIGatewayService{cfg: &config.Config{JWT: config.JWTConfig{Secret: "isolation-secret"}}}
	logical := normalizeLogicalTuple(openAICodexLogicalTuple{session: "shared-client-session", thread: "shared-client-session"}, "test", true)

	contextForAPIKey := func(id int64) *gin.Context {
		c := newOutboundIdentityTestContext(t, nil)
		c.Set("api_key", &APIKey{ID: id})
		return c
	}
	first, _, err := svc.resolveOpenAICodexTurnIdentity(context.Background(), contextForAPIKey(11), &Account{ID: 91, Type: AccountTypeOAuth}, logical)
	require.NoError(t, err)
	changedAPIKey, _, err := svc.resolveOpenAICodexTurnIdentity(context.Background(), contextForAPIKey(12), &Account{ID: 91, Type: AccountTypeOAuth}, logical)
	require.NoError(t, err)
	changedAccount, _, err := svc.resolveOpenAICodexTurnIdentity(context.Background(), contextForAPIKey(11), &Account{ID: 92, Type: AccountTypeOAuth}, logical)
	require.NoError(t, err)
	require.NotEqual(t, first.SessionID, changedAPIKey.SessionID)
	require.NotEqual(t, first.SessionID, changedAccount.SessionID)

	parentID := int64(91)
	shadow, _, err := svc.resolveOpenAICodexTurnIdentity(context.Background(), contextForAPIKey(11), &Account{ID: 93, Type: AccountTypeOAuth, ParentAccountID: &parentID}, logical)
	require.NoError(t, err)
	require.Equal(t, first, shadow, "OAuth shadow and credential owner must share the namespace")
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
	store       *openAICodexIdentityLocalStore
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

func (s *outboundIdentityGatewayCacheStub) identityStore() *openAICodexIdentityLocalStore {
	if s.store == nil {
		s.store = newOpenAICodexIdentityLocalStore()
	}
	return s.store
}

func (s *outboundIdentityGatewayCacheStub) GetOrCreateCodexSession(ctx context.Context, mappingKey, candidate string, ttl time.Duration) (string, error) {
	s.mu.Lock()
	s.callCounter++
	s.mappingKeys = append(s.mappingKeys, mappingKey)
	fail, storeErr := s.fail, s.storeErr
	store := s.identityStore()
	s.mu.Unlock()
	if storeErr != nil {
		return "", storeErr
	}
	if fail {
		return "", errors.New("redis unavailable")
	}
	return store.GetOrCreateCodexSession(ctx, mappingKey, candidate, ttl)
}

func (s *outboundIdentityGatewayCacheStub) GetOrCreateCodexThread(ctx context.Context, sessionKey, threadKey, sessionID, candidate string, ttl time.Duration) (OpenAICodexTurnIdentity, error) {
	s.mu.Lock()
	s.callCounter++
	s.mappingKeys = append(s.mappingKeys, threadKey)
	fail, storeErr := s.fail, s.storeErr
	store := s.identityStore()
	s.mu.Unlock()
	if storeErr != nil {
		return OpenAICodexTurnIdentity{}, storeErr
	}
	if fail {
		return OpenAICodexTurnIdentity{}, errors.New("redis unavailable")
	}
	identity, err := store.GetOrCreateCodexThread(ctx, sessionKey, threadKey, sessionID, candidate, ttl)
	if err == nil {
		s.mu.Lock()
		s.candidates = append(s.candidates, identity)
		s.winner, s.hasWinner = identity, true
		s.mu.Unlock()
	}
	return identity, err
}

func (s *outboundIdentityGatewayCacheStub) GetOrCreateCodexSessionAliases(ctx context.Context, mappingKeys []string, candidate string, ttl time.Duration) (OpenAICodexAliasStoreResolution, error) {
	s.mu.Lock()
	s.callCounter++
	s.mappingKeys = append(s.mappingKeys, mappingKeys...)
	fail, storeErr := s.fail, s.storeErr
	store := s.identityStore()
	s.mu.Unlock()
	if storeErr != nil {
		return OpenAICodexAliasStoreResolution{}, storeErr
	}
	if fail {
		return OpenAICodexAliasStoreResolution{}, errors.New("redis unavailable")
	}
	return store.GetOrCreateCodexSessionAliases(ctx, mappingKeys, candidate, ttl)
}

func (s *outboundIdentityGatewayCacheStub) GetOrCreateCodexThreadAliases(ctx context.Context, mappings []OpenAICodexThreadAliasMapping, sessionID, candidate string, ttl time.Duration) (OpenAICodexAliasStoreResolution, error) {
	s.mu.Lock()
	s.callCounter++
	for _, mapping := range mappings {
		s.mappingKeys = append(s.mappingKeys, mapping.ThreadMappingKey)
	}
	fail, storeErr := s.fail, s.storeErr
	store := s.identityStore()
	s.mu.Unlock()
	if storeErr != nil {
		return OpenAICodexAliasStoreResolution{}, storeErr
	}
	if fail {
		return OpenAICodexAliasStoreResolution{}, errors.New("redis unavailable")
	}
	return store.GetOrCreateCodexThreadAliases(ctx, mappings, sessionID, candidate, ttl)
}

func TestResolveOpenAICodexAliasesDoNotBindConflictingLowerPriorityTuple(t *testing.T) {
	resetProcessCodexIdentityStore(t)
	const secret = "cross-alias-secret"
	const logicalSessionA = "logical-session-a"

	cache := &outboundIdentityGatewayCacheStub{}
	account := &Account{ID: 173, Type: AccountTypeOAuth}
	svc := &OpenAIGatewayService{cache: cache, cfg: &config.Config{JWT: config.JWTConfig{Secret: secret}}}

	firstContext := newOutboundIdentityTestContext(t, map[string]string{
		openAIWSTurnMetadataHeader: `{"session_id":"logical-session-b","thread_id":"logical-thread-b"}`,
	})
	firstCapture := CaptureOpenAIOAuthIdentity(firstContext, []byte(`{"client_metadata":{"x-codex-turn-metadata":"{\"session_id\":\"logical-session-a\",\"thread_id\":\"logical-thread-a\"}"}}`), "")
	require.Equal(t, logicalSessionA, firstCapture.Logical.SessionKey)
	require.Len(t, firstCapture.Aliases, 1)
	first, ok, err := svc.resolveOpenAICodexTurnIdentityWithAliases(context.Background(), firstContext, account, firstCapture.Logical, firstCapture.Aliases)
	require.NoError(t, err)
	require.True(t, ok)

	secondContext := newOutboundIdentityTestContext(t, map[string]string{
		openAIWSTurnMetadataHeader: `{"session_id":"logical-session-b","thread_id":"logical-thread-b"}`,
	})
	secondCapture := CaptureOpenAIOAuthIdentity(secondContext, nil, "")
	second, ok, err := svc.resolveOpenAICodexTurnIdentityWithAliases(context.Background(), secondContext, account, secondCapture.Logical, secondCapture.Aliases)
	require.NoError(t, err)
	require.True(t, ok)
	require.NotEqual(t, first, second)
}

func TestResolveOpenAICodexEndpointAliasReusesLegacyV2Mapping(t *testing.T) {
	resetProcessCodexIdentityStore(t)
	const (
		secret           = "compact-alias-secret"
		namespace        = "account:175"
		legacySeed       = "compact-legacy-seed"
		canonicalSession = "compact-canonical-session"
	)
	cache := &outboundIdentityGatewayCacheStub{}
	account := &Account{ID: 175, Type: AccountTypeOAuth}
	svc := &OpenAIGatewayService{cache: cache, cfg: &config.Config{JWT: config.JWTConfig{Secret: secret}}}

	legacyDigest, err := OpenAICodexSessionMappingKey(secret, namespace, 0, legacySeed)
	require.NoError(t, err)
	_, err = cache.identityStore().GetOrCreateCodexSession(context.Background(), legacyDigest, testOutboundSessionUUID, time.Hour)
	require.NoError(t, err)

	body := []byte(`{"client_metadata":{"session_id":"compact-canonical-session","thread_id":"compact-canonical-session"}}`)
	capture := CaptureOpenAIOAuthIdentityWithEndpointAlias(nil, body, legacySeed)
	require.Equal(t, canonicalSession, capture.Logical.SessionKey)
	require.Len(t, capture.Aliases, 2)
	require.False(t, capture.Aliases[1].Explicit)

	identity, ok, err := svc.resolveOpenAICodexTurnIdentityWithAliases(context.Background(), nil, account, capture.Logical, capture.Aliases)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, testOutboundSessionUUID, identity.SessionID)
	require.Equal(t, testOutboundSessionUUID, identity.ThreadID)

	canonicalDigest, err := OpenAICodexSessionMappingKey(secret, namespace, 0, canonicalSession)
	require.NoError(t, err)
	bound, err := cache.identityStore().GetOrCreateCodexSession(context.Background(), canonicalDigest, "018f5c3c-6e3a-7abe-8def-1234567890ad", time.Hour)
	require.NoError(t, err)
	require.Equal(t, testOutboundSessionUUID, bound)
}

func TestResolveOpenAICodexAliasesDoNotReuseConflictingTupleWithoutHMACSecret(t *testing.T) {
	resetProcessCodexIdentityStore(t)
	svc := &OpenAIGatewayService{}
	account := &Account{ID: 174, Type: AccountTypeOAuth}
	firstContext := newOutboundIdentityTestContext(t, map[string]string{
		openAIWSTurnMetadataHeader: `{"session_id":"fallback-session-b","thread_id":"fallback-thread-b"}`,
	})
	firstCapture := CaptureOpenAIOAuthIdentity(firstContext, []byte(`{"client_metadata":{"x-codex-turn-metadata":"{\"session_id\":\"fallback-session-a\",\"thread_id\":\"fallback-thread-a\"}"}}`), "")
	first, ok, err := svc.resolveOpenAICodexTurnIdentityWithAliases(context.Background(), firstContext, account, firstCapture.Logical, firstCapture.Aliases)
	require.NoError(t, err)
	require.True(t, ok)

	secondContext := newOutboundIdentityTestContext(t, map[string]string{
		openAIWSTurnMetadataHeader: `{"session_id":"fallback-session-b","thread_id":"fallback-thread-b"}`,
	})
	secondCapture := CaptureOpenAIOAuthIdentity(secondContext, nil, "")
	second, ok, err := svc.resolveOpenAICodexTurnIdentityWithAliases(context.Background(), secondContext, account, secondCapture.Logical, secondCapture.Aliases)
	require.NoError(t, err)
	require.True(t, ok)
	require.NotEqual(t, first, second)
}

func TestResolveOpenAICodexTurnIdentityPromotesFallbackAfterRecovery(t *testing.T) {
	resetProcessCodexIdentityStore(t)
	cache := &outboundIdentityGatewayCacheStub{fail: true}
	svc := &OpenAIGatewayService{cache: cache, cfg: &config.Config{JWT: config.JWTConfig{Secret: "recovery-secret"}}}
	logical := normalizeLogicalTuple(openAICodexLogicalTuple{session: "recovery-session", thread: "recovery-child"}, "test", true)
	local, ok, err := svc.resolveOpenAICodexTurnIdentity(context.Background(), nil, &Account{ID: 72}, logical)
	require.NoError(t, err)
	require.True(t, ok)

	cache.mu.Lock()
	cache.fail = false
	cache.mu.Unlock()
	recovered, ok, err := svc.resolveOpenAICodexTurnIdentity(context.Background(), nil, &Account{ID: 72}, logical)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, local, recovered)
	stable, _, err := svc.resolveOpenAICodexTurnIdentity(context.Background(), nil, &Account{ID: 72}, logical)
	require.NoError(t, err)
	require.Equal(t, recovered, stable)
}

func TestOpenAICodexIdentityNamespaceUsesOAuthCredentialOwner(t *testing.T) {
	parentID := int64(84)
	shadow := &Account{ID: 85, Type: AccountTypeOAuth, ParentAccountID: &parentID}
	namespace, err := (&OpenAIGatewayService{}).resolveOpenAIOutboundSessionIdentityNamespace(context.Background(), shadow)
	require.NoError(t, err)
	require.Equal(t, "account:84", namespace)

	badParent := int64(0)
	shadow.ParentAccountID = &badParent
	_, err = (&OpenAIGatewayService{}).resolveOpenAIOutboundSessionIdentityNamespace(context.Background(), shadow)
	require.ErrorIs(t, err, errOpenAIOutboundSessionIdentityNamespace)
}

func TestNewOpenAICodexIdentitiesUseUUIDv7(t *testing.T) {
	root, err := newOpenAICodexRootIdentity()
	require.NoError(t, err)
	require.Equal(t, root.SessionID, root.ThreadID)
	parsed, err := uuid.Parse(root.SessionID)
	require.NoError(t, err)
	require.Equal(t, uuid.Version(7), parsed.Version())

	child, err := newOpenAICodexDescendantIdentity(root.SessionID)
	require.NoError(t, err)
	require.Equal(t, root.SessionID, child.SessionID)
	require.NotEqual(t, child.SessionID, child.ThreadID)
}
