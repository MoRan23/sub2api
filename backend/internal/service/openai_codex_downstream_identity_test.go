package service

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// General gateway fixtures previously used the process-local identity fallback.
// Model the new narrow interface with that same store; dedicated failure and
// migration fixtures below retain their own isolated stores and error controls.
func (s *stubGatewayCache) ResolveCodexDownstreamSession(ctx context.Context, request OpenAICodexDownstreamSessionRequest, ttl time.Duration) (OpenAICodexDownstreamSessionResolution, error) {
	return processOpenAICodexTurnIdentityStore.ResolveCodexDownstreamSession(ctx, request, ttl)
}

func (s *stubGatewayCache) ResolveCodexDownstreamThread(ctx context.Context, request OpenAICodexDownstreamThreadRequest, ttl time.Duration) (OpenAICodexTurnIdentity, error) {
	return processOpenAICodexTurnIdentityStore.ResolveCodexDownstreamThread(ctx, request, ttl)
}

func (s *outboundIdentityGatewayCacheStub) ResolveCodexDownstreamSession(ctx context.Context, request OpenAICodexDownstreamSessionRequest, ttl time.Duration) (OpenAICodexDownstreamSessionResolution, error) {
	s.mu.Lock()
	s.callCounter++
	s.mappingKeys = append(s.mappingKeys, request.SessionMappingKeys...)
	fail, storeErr, store := s.fail, s.storeErr, s.identityStore()
	s.mu.Unlock()
	if storeErr != nil {
		return OpenAICodexDownstreamSessionResolution{}, storeErr
	}
	if fail {
		return OpenAICodexDownstreamSessionResolution{}, errors.New("redis unavailable")
	}
	return store.ResolveCodexDownstreamSession(ctx, request, ttl)
}

func (s *outboundIdentityGatewayCacheStub) ResolveCodexDownstreamThread(ctx context.Context, request OpenAICodexDownstreamThreadRequest, ttl time.Duration) (OpenAICodexTurnIdentity, error) {
	s.mu.Lock()
	s.callCounter++
	for _, mapping := range request.ThreadMappings {
		s.mappingKeys = append(s.mappingKeys, mapping.ThreadMappingKey)
	}
	fail, storeErr, store := s.fail, s.storeErr, s.identityStore()
	s.mu.Unlock()
	if storeErr != nil {
		return OpenAICodexTurnIdentity{}, storeErr
	}
	if fail {
		return OpenAICodexTurnIdentity{}, errors.New("redis unavailable")
	}
	identity, err := store.ResolveCodexDownstreamThread(ctx, request, ttl)
	if err == nil {
		s.mu.Lock()
		s.candidates = append(s.candidates, identity)
		s.winner, s.hasWinner = identity, true
		s.mu.Unlock()
	}
	return identity, err
}

func downstreamIdentityContext(key int64) *gin.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	c.Set("api_key", &APIKey{ID: key})
	return c
}

func downstreamIdentityService(t *testing.T) (*OpenAIGatewayService, *outboundIdentityGatewayCacheStub) {
	t.Helper()
	resetProcessCodexIdentityStore(t)
	cache := &outboundIdentityGatewayCacheStub{}
	return &OpenAIGatewayService{cfg: &config.Config{JWT: config.JWTConfig{Secret: "downstream-identity-test-secret"}}, cache: cache}, cache
}

func TestCodexDownstreamIdentityInheritsLegacyTreeAcrossAccounts(t *testing.T) {
	svc, _ := downstreamIdentityService(t)
	ctx := context.Background()
	first := &Account{ID: 141, Type: AccountTypeOAuth}
	second := &Account{ID: 142, Type: AccountTypeOAuth}
	logical := OpenAICodexLogicalTurnIdentity{SessionKey: "legacy-root", ThreadKey: "legacy-child", ParentThreadKey: "legacy-parent", ForkedFromThreadKey: "legacy-fork", Explicit: true}
	legacy, ok, _, err := svc.resolveOpenAICodexLegacyTurnIdentityWithAliasesDetailed(ctx, downstreamIdentityContext(19), first, logical, nil)
	require.NoError(t, err)
	require.True(t, ok)
	otherLegacy, _, _, err := svc.resolveOpenAICodexLegacyTurnIdentityWithAliasesDetailed(ctx, downstreamIdentityContext(19), second, logical, nil)
	require.NoError(t, err)
	require.NotEqual(t, legacy.SessionID, otherLegacy.SessionID)

	root := OpenAICodexLogicalTurnIdentity{SessionKey: logical.SessionKey, ThreadKey: logical.SessionKey, Explicit: true}
	rootIdentity, _, _, source, err := svc.resolveOpenAICodexDownstreamTurnIdentityDetailed(ctx, downstreamIdentityContext(19), first, root, nil, OpenAICodexDownstreamIdentityNamespace)
	require.NoError(t, err)
	require.Equal(t, legacy.SessionID, rootIdentity.SessionID)
	require.Equal(t, "account:141", source)
	// The child and its lineage have never been addressed in the new scope.
	// Resolve them while another upstream is selected: only the fixed source wins.
	child, _, _, source, err := svc.resolveOpenAICodexDownstreamTurnIdentityDetailed(ctx, downstreamIdentityContext(19), second, logical, nil, OpenAICodexDownstreamIdentityNamespace)
	require.NoError(t, err)
	require.Equal(t, legacy, child)
	require.Equal(t, "account:141", source)
	back, _, _, _, err := svc.resolveOpenAICodexDownstreamTurnIdentityDetailed(ctx, downstreamIdentityContext(19), first, logical, nil, OpenAICodexDownstreamIdentityNamespace)
	require.NoError(t, err)
	require.Equal(t, child, back)
}

func TestCodexDownstreamIdentityAccountIndependentAndKeyIsolated(t *testing.T) {
	svc, _ := downstreamIdentityService(t)
	logical := OpenAICodexLogicalTurnIdentity{SessionKey: "same-client-session", ThreadKey: "child", Explicit: true}
	first, _, _, source, err := svc.resolveOpenAICodexDownstreamTurnIdentityDetailed(context.Background(), downstreamIdentityContext(21), &Account{ID: 1}, logical, nil, OpenAICodexDownstreamIdentityNamespace)
	require.NoError(t, err)
	require.Empty(t, source)
	second, _, _, source, err := svc.resolveOpenAICodexDownstreamTurnIdentityDetailed(context.Background(), downstreamIdentityContext(21), &Account{ID: 2}, logical, nil, OpenAICodexDownstreamIdentityNamespace)
	require.NoError(t, err)
	require.Equal(t, first, second)
	require.Empty(t, source)
	otherKey, _, _, _, err := svc.resolveOpenAICodexDownstreamTurnIdentityDetailed(context.Background(), downstreamIdentityContext(22), &Account{ID: 1}, logical, nil, OpenAICodexDownstreamIdentityNamespace)
	require.NoError(t, err)
	require.NotEqual(t, first.SessionID, otherKey.SessionID)
	require.NotEqual(t, first.ThreadID, otherKey.ThreadID)
}

func TestCodexDownstreamIdentityConcurrentFirstMigrationChoosesOneOwner(t *testing.T) {
	svc, _ := downstreamIdentityService(t)
	logical := OpenAICodexLogicalTurnIdentity{SessionKey: "race-session", ThreadKey: "race-child", Explicit: true}
	accounts := []*Account{{ID: 301}, {ID: 302}}
	legacy := make(map[string]OpenAICodexTurnIdentity)
	for _, account := range accounts {
		identity, _, _, err := svc.resolveOpenAICodexLegacyTurnIdentityWithAliasesDetailed(context.Background(), downstreamIdentityContext(45), account, logical, nil)
		require.NoError(t, err)
		legacy[fmt.Sprintf("account:%d", account.ID)] = identity
	}
	type result struct {
		identity OpenAICodexTurnIdentity
		source   string
		err      error
	}
	results := make(chan result, 24)
	var group sync.WaitGroup
	for index := 0; index < 24; index++ {
		group.Add(1)
		go func(account *Account) {
			defer group.Done()
			identity, _, _, source, err := svc.resolveOpenAICodexDownstreamTurnIdentityDetailed(context.Background(), downstreamIdentityContext(45), account, logical, nil, OpenAICodexDownstreamIdentityNamespace)
			results <- result{identity, source, err}
		}(accounts[index%2])
	}
	group.Wait()
	close(results)
	var winner result
	for resolved := range results {
		require.NoError(t, resolved.err)
		if winner.source == "" {
			winner = resolved
		}
		require.Equal(t, winner.source, resolved.source)
		require.Equal(t, winner.identity, resolved.identity)
		require.Equal(t, legacy[resolved.source], resolved.identity)
	}
}

func TestCodexDownstreamIdentityStoreFailureRequiresWarmCanonicalMapping(t *testing.T) {
	svc, cache := downstreamIdentityService(t)
	logical := OpenAICodexLogicalTurnIdentity{SessionKey: "warm-session", ThreadKey: "warm-child", Explicit: true}
	first, _, _, _, err := svc.resolveOpenAICodexDownstreamTurnIdentityDetailed(context.Background(), downstreamIdentityContext(81), &Account{ID: 801}, logical, nil, OpenAICodexDownstreamIdentityNamespace)
	require.NoError(t, err)
	cache.mu.Lock()
	cache.fail = true
	cache.mu.Unlock()
	warm, _, outcome, _, err := svc.resolveOpenAICodexDownstreamTurnIdentityDetailed(context.Background(), downstreamIdentityContext(81), &Account{ID: 802}, logical, nil, OpenAICodexDownstreamIdentityNamespace)
	require.NoError(t, err)
	require.Equal(t, first, warm)
	require.Equal(t, OpenAIOAuthIdentityResolveStoreError, outcome)
	cold := OpenAICodexLogicalTurnIdentity{SessionKey: "cold-session", ThreadKey: "cold-session", Explicit: true}
	_, _, _, _, err = svc.resolveOpenAICodexDownstreamTurnIdentityDetailed(context.Background(), downstreamIdentityContext(81), &Account{ID: 802}, cold, nil, OpenAICodexDownstreamIdentityNamespace)
	require.ErrorIs(t, err, ErrOpenAICodexDownstreamIdentityStoreUnavailable)
	coldThread := logical
	coldThread.ThreadKey = "never-resolved-child"
	_, _, _, _, err = svc.resolveOpenAICodexDownstreamTurnIdentityDetailed(context.Background(), downstreamIdentityContext(81), &Account{ID: 802}, coldThread, nil, OpenAICodexDownstreamIdentityNamespace)
	require.ErrorIs(t, err, ErrOpenAICodexDownstreamIdentityStoreUnavailable)
	cache.mu.Lock()
	cache.fail = false
	cache.mu.Unlock()
	recovered, _, _, _, err := svc.resolveOpenAICodexDownstreamTurnIdentityDetailed(context.Background(), downstreamIdentityContext(81), &Account{ID: 802}, logical, nil, OpenAICodexDownstreamIdentityNamespace)
	require.NoError(t, err)
	require.Equal(t, first, recovered)
}

func TestCodexDownstreamIdentityRejectsAnonymousSharedScope(t *testing.T) {
	svc, _ := downstreamIdentityService(t)
	logical := OpenAICodexLogicalTurnIdentity{SessionKey: "anonymous-session", ThreadKey: "anonymous-session"}
	_, _, _, err := svc.resolveOpenAICodexTurnIdentityWithAliasesDetailed(context.Background(), nil, &Account{ID: 7}, logical, nil)
	require.ErrorIs(t, err, ErrOpenAICodexDownstreamIdentityScopeMissing)
	_, _, _, _, err = svc.resolveOpenAICodexDownstreamTurnIdentityDetailed(context.Background(), nil, &Account{ID: 7}, logical, nil, "synthetic:client-controlled")
	require.ErrorIs(t, err, ErrOpenAICodexDownstreamIdentityScopeMissing)
}

func TestCodexDownstreamIdentityMigratesEndpointAliasAndKeepsItAfterFailover(t *testing.T) {
	svc, _ := downstreamIdentityService(t)
	account := &Account{ID: 971}
	legacyLogical := OpenAICodexLogicalTurnIdentity{SessionKey: "legacy-endpoint-session", ThreadKey: "legacy-endpoint-thread", Explicit: true}
	legacy, _, _, err := svc.resolveOpenAICodexLegacyTurnIdentityWithAliasesDetailed(context.Background(), downstreamIdentityContext(97), account, legacyLogical, nil)
	require.NoError(t, err)
	canonical := OpenAICodexLogicalTurnIdentity{SessionKey: "canonical-session", ThreadKey: "canonical-thread", Explicit: true}
	aliases := []OpenAICodexLogicalTurnAlias{{SessionKey: legacyLogical.SessionKey, ThreadKey: legacyLogical.ThreadKey, Source: OpenAIOutboundSessionLogicalKeySourceCallerSeed}}
	migrated, _, _, source, err := svc.resolveOpenAICodexDownstreamTurnIdentityDetailed(context.Background(), downstreamIdentityContext(97), account, canonical, aliases, OpenAICodexDownstreamIdentityNamespace)
	require.NoError(t, err)
	require.Equal(t, legacy, migrated)
	require.Equal(t, "account:971", source)
	// Later endpoints need not repeat the alias that established the mapping.
	reused, _, _, source, err := svc.resolveOpenAICodexDownstreamTurnIdentityDetailed(context.Background(), downstreamIdentityContext(97), &Account{ID: 972}, canonical, nil, OpenAICodexDownstreamIdentityNamespace)
	require.NoError(t, err)
	require.Equal(t, migrated, reused)
	require.Equal(t, "account:971", source)
}
