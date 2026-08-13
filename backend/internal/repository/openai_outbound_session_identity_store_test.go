package repository

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

const (
	repositoryCodexSessionUUID = "018f5c3c-6e3a-7abc-8def-1234567890ab"
	repositoryCodexThreadUUID  = "018f5c3c-6e3a-7abd-8def-1234567890ac"
	repositorySessionDigest    = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	repositoryThreadDigest     = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func newCodexIdentityRedisTestStore(t *testing.T) (*openAICodexTurnIdentityRedisStore, *miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return &openAICodexTurnIdentityRedisStore{rdb: rdb}, mr, rdb
}

func TestOpenAICodexRedisStoreRootWinnerAndSlidingTTL(t *testing.T) {
	store, mr, _ := newCodexIdentityRedisTestStore(t)
	ctx := context.Background()
	winner, err := store.GetOrCreateCodexSession(ctx, repositorySessionDigest, repositoryCodexSessionUUID, 90*time.Second)
	require.NoError(t, err)
	require.Equal(t, repositoryCodexSessionUUID, winner)
	key := OpenAICodexSessionIdentityRedisKey(repositorySessionDigest)
	require.Equal(t, 90*time.Second, mr.TTL(key))

	mr.FastForward(40 * time.Second)
	other := "018f5c3c-6e3a-7abe-8def-1234567890ad"
	winner, err = store.GetOrCreateCodexSession(ctx, repositorySessionDigest, other, 90*time.Second)
	require.NoError(t, err)
	require.Equal(t, repositoryCodexSessionUUID, winner)
	require.Equal(t, 90*time.Second, mr.TTL(key))
	stored, err := mr.Get(key)
	require.NoError(t, err)
	require.JSONEq(t, `{"session_id":"`+repositoryCodexSessionUUID+`"}`, stored)
}

func TestOpenAICodexRedisStoreSessionAliasesClaimAndReuseOldWinner(t *testing.T) {
	store, _, _ := newCodexIdentityRedisTestStore(t)
	ctx := context.Background()
	aliasDigest := strings.Repeat("c", 64)
	oldWinner, err := store.GetOrCreateCodexSession(ctx, aliasDigest, repositoryCodexSessionUUID, time.Minute)
	require.NoError(t, err)

	resolution, err := store.GetOrCreateCodexSessionAliases(ctx, []string{repositorySessionDigest, aliasDigest}, "018f5c3c-6e3a-7abe-8def-1234567890ad", time.Minute)
	require.NoError(t, err)
	require.Equal(t, oldWinner, resolution.Identity.SessionID)
	require.True(t, resolution.Reused)
	require.Equal(t, 1, resolution.AliasesClaimed)

	canonical, err := store.GetOrCreateCodexSession(ctx, repositorySessionDigest, "018f5c3c-6e3a-7abf-8def-1234567890ae", time.Minute)
	require.NoError(t, err)
	require.Equal(t, oldWinner, canonical)
}

func TestOpenAICodexRedisStoreSessionAliasesConvergeConflictingWinnersAtomically(t *testing.T) {
	store, mr, _ := newCodexIdentityRedisTestStore(t)
	ctx := context.Background()
	otherDigest := strings.Repeat("d", 64)
	otherWinner := "018f5c3c-6e3a-7abe-8def-1234567890ad"
	_, err := store.GetOrCreateCodexSession(ctx, repositorySessionDigest, repositoryCodexSessionUUID, time.Minute)
	require.NoError(t, err)
	_, err = store.GetOrCreateCodexSession(ctx, otherDigest, otherWinner, time.Minute)
	require.NoError(t, err)

	resolution, err := store.GetOrCreateCodexSessionAliases(ctx, []string{repositorySessionDigest, otherDigest}, repositoryCodexSessionUUID, time.Minute)
	require.NoError(t, err)
	require.Equal(t, repositoryCodexSessionUUID, resolution.Identity.SessionID)
	require.Equal(t, 1, resolution.ConflictsResolved)
	first, _ := mr.Get(OpenAICodexSessionIdentityRedisKey(repositorySessionDigest))
	second, _ := mr.Get(OpenAICodexSessionIdentityRedisKey(otherDigest))
	require.Contains(t, first, repositoryCodexSessionUUID)
	require.Contains(t, second, repositoryCodexSessionUUID)
}

func TestOpenAICodexRedisStoreSessionAliasesRepairMalformedAndNonCanonicalUUIDsAtomically(t *testing.T) {
	store, mr, rdb := newCodexIdentityRedisTestStore(t)
	ctx := context.Background()
	validDigest := strings.Repeat("c", 64)
	malformedDigest := strings.Repeat("d", 64)
	require.NoError(t, rdb.Set(ctx, OpenAICodexSessionIdentityRedisKey(repositorySessionDigest), `{"session_id":"`+strings.ToUpper(repositoryCodexSessionUUID)+`"}`, time.Minute).Err())
	require.NoError(t, rdb.Set(ctx, OpenAICodexSessionIdentityRedisKey(validDigest), `{"session_id":"`+repositoryCodexSessionUUID+`"}`, time.Minute).Err())
	require.NoError(t, rdb.Set(ctx, OpenAICodexSessionIdentityRedisKey(malformedDigest), `{bad-json`, time.Minute).Err())

	resolution, err := store.GetOrCreateCodexSessionAliases(ctx, []string{repositorySessionDigest, validDigest, malformedDigest}, "018f5c3c-6e3a-7abe-8def-1234567890ad", time.Minute)
	require.NoError(t, err)
	require.Equal(t, repositoryCodexSessionUUID, resolution.Identity.SessionID)
	require.True(t, resolution.Reused)
	for _, digest := range []string{repositorySessionDigest, validDigest, malformedDigest} {
		stored, getErr := mr.Get(OpenAICodexSessionIdentityRedisKey(digest))
		require.NoError(t, getErr)
		require.JSONEq(t, `{"session_id":"`+repositoryCodexSessionUUID+`"}`, stored)
	}
}

func TestOpenAICodexRedisStoreThreadAliasesRepairMalformedAndNonCanonicalUUIDsAtomically(t *testing.T) {
	store, mr, rdb := newCodexIdentityRedisTestStore(t)
	ctx := context.Background()
	aliasSessionDigest := strings.Repeat("c", 64)
	malformedSessionDigest := strings.Repeat("d", 64)
	aliasThreadDigest := strings.Repeat("e", 64)
	malformedThreadDigest := strings.Repeat("f", 64)
	_, err := store.GetOrCreateCodexSessionAliases(ctx, []string{repositorySessionDigest, aliasSessionDigest, malformedSessionDigest}, repositoryCodexSessionUUID, time.Minute)
	require.NoError(t, err)
	require.NoError(t, rdb.Set(ctx, OpenAICodexThreadIdentityRedisKey(repositorySessionDigest, repositoryThreadDigest), `{"session_id":"`+repositoryCodexSessionUUID+`","thread_id":"`+strings.ToUpper(repositoryCodexThreadUUID)+`"}`, time.Minute).Err())
	require.NoError(t, rdb.Set(ctx, OpenAICodexThreadIdentityRedisKey(aliasSessionDigest, aliasThreadDigest), `{"session_id":"`+repositoryCodexSessionUUID+`","thread_id":"`+repositoryCodexThreadUUID+`"}`, time.Minute).Err())
	require.NoError(t, rdb.Set(ctx, OpenAICodexThreadIdentityRedisKey(malformedSessionDigest, malformedThreadDigest), `{bad-json`, time.Minute).Err())

	mappings := []service.OpenAICodexThreadAliasMapping{
		{SessionMappingKey: repositorySessionDigest, ThreadMappingKey: repositoryThreadDigest},
		{SessionMappingKey: aliasSessionDigest, ThreadMappingKey: aliasThreadDigest},
		{SessionMappingKey: malformedSessionDigest, ThreadMappingKey: malformedThreadDigest},
	}
	resolution, err := store.GetOrCreateCodexThreadAliases(ctx, mappings, repositoryCodexSessionUUID, "018f5c3c-6e3a-7abe-8def-1234567890ad", time.Minute)
	require.NoError(t, err)
	require.Equal(t, repositoryCodexThreadUUID, resolution.Identity.ThreadID)
	require.True(t, resolution.Reused)
	for _, mapping := range mappings {
		stored, getErr := mr.Get(OpenAICodexThreadIdentityRedisKey(mapping.SessionMappingKey, mapping.ThreadMappingKey))
		require.NoError(t, getErr)
		require.JSONEq(t, `{"session_id":"`+repositoryCodexSessionUUID+`","thread_id":"`+repositoryCodexThreadUUID+`"}`, stored)
	}
}

func TestOpenAICodexRedisStoreDescendantsShareSession(t *testing.T) {
	store, mr, _ := newCodexIdentityRedisTestStore(t)
	ctx := context.Background()
	sessionID, err := store.GetOrCreateCodexSession(ctx, repositorySessionDigest, repositoryCodexSessionUUID, time.Minute)
	require.NoError(t, err)
	childOne, err := store.GetOrCreateCodexThread(ctx, repositorySessionDigest, repositoryThreadDigest, sessionID, repositoryCodexThreadUUID, time.Minute)
	require.NoError(t, err)
	otherDigest := strings.Repeat("c", 64)
	otherThread := "018f5c3c-6e3a-7abe-8def-1234567890ad"
	childTwo, err := store.GetOrCreateCodexThread(ctx, repositorySessionDigest, otherDigest, sessionID, otherThread, time.Minute)
	require.NoError(t, err)
	require.Equal(t, sessionID, childOne.SessionID)
	require.Equal(t, sessionID, childTwo.SessionID)
	require.NotEqual(t, childOne.ThreadID, childTwo.ThreadID)
	require.Equal(t, time.Minute, mr.TTL(OpenAICodexThreadIdentityRedisKey(repositorySessionDigest, repositoryThreadDigest)))

	stable, err := store.GetOrCreateCodexThread(ctx, repositorySessionDigest, repositoryThreadDigest, sessionID, otherThread, time.Minute)
	require.NoError(t, err)
	require.Equal(t, childOne, stable)
}

func TestOpenAICodexRedisStoreConcurrentSessionAndThreadMiss(t *testing.T) {
	_, mr, firstClient := newCodexIdentityRedisTestStore(t)
	secondClient := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = secondClient.Close() })
	stores := []*openAICodexTurnIdentityRedisStore{{rdb: firstClient}, {rdb: secondClient}}
	ctx := context.Background()
	const workers = 24
	sessions := make([]string, workers)
	threads := make([]service.OpenAICodexTurnIdentity, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sid, err := uuid.NewV7()
			require.NoError(t, err)
			sessions[i], err = stores[i%2].GetOrCreateCodexSession(ctx, repositorySessionDigest, sid.String(), time.Minute)
			require.NoError(t, err)
			tid, err := uuid.NewV7()
			require.NoError(t, err)
			threads[i], err = stores[i%2].GetOrCreateCodexThread(ctx, repositorySessionDigest, repositoryThreadDigest, sessions[i], tid.String(), time.Minute)
			require.NoError(t, err)
		}(i)
	}
	wg.Wait()
	for i := 1; i < workers; i++ {
		require.Equal(t, sessions[0], sessions[i])
		require.Equal(t, threads[0], threads[i])
	}
}

func TestOpenAICodexRedisStoreRepairsStrictSchemaCorruption(t *testing.T) {
	store, _, rdb := newCodexIdentityRedisTestStore(t)
	ctx := context.Background()
	sessionKey := OpenAICodexSessionIdentityRedisKey(repositorySessionDigest)
	require.NoError(t, rdb.Set(ctx, sessionKey, `{"session_id":"bad","logical_key":"must-not-survive"}`, time.Minute).Err())
	sessionID, err := store.GetOrCreateCodexSession(ctx, repositorySessionDigest, repositoryCodexSessionUUID, time.Minute)
	require.NoError(t, err)
	require.Equal(t, repositoryCodexSessionUUID, sessionID)
	storedSession, err := rdb.Get(ctx, sessionKey).Result()
	require.NoError(t, err)
	require.NotContains(t, storedSession, "logical_key")

	threadKey := OpenAICodexThreadIdentityRedisKey(repositorySessionDigest, repositoryThreadDigest)
	require.NoError(t, rdb.Set(ctx, threadKey, `{"session_id":"`+sessionID+`","thread_id":"bad","raw":"must-not-survive"}`, time.Minute).Err())
	identity, err := store.GetOrCreateCodexThread(ctx, repositorySessionDigest, repositoryThreadDigest, sessionID, repositoryCodexThreadUUID, time.Minute)
	require.NoError(t, err)
	require.Equal(t, repositoryCodexThreadUUID, identity.ThreadID)
	storedThread, err := rdb.Get(ctx, threadKey).Result()
	require.NoError(t, err)
	require.NotContains(t, storedThread, "raw")
	var wire map[string]string
	require.NoError(t, json.Unmarshal([]byte(storedThread), &wire))
	require.Len(t, wire, 2)
}

func TestOpenAICodexRedisStoreRejectsChangedSessionWinner(t *testing.T) {
	store, _, _ := newCodexIdentityRedisTestStore(t)
	ctx := context.Background()
	sessionID, err := store.GetOrCreateCodexSession(ctx, repositorySessionDigest, repositoryCodexSessionUUID, time.Minute)
	require.NoError(t, err)
	wrongSession := "018f5c3c-6e3a-7abe-8def-1234567890ad"
	_, err = store.GetOrCreateCodexThread(ctx, repositorySessionDigest, repositoryThreadDigest, wrongSession, repositoryCodexThreadUUID, time.Minute)
	require.ErrorIs(t, err, service.ErrOpenAICodexSessionWinnerChanged)
	require.NotEqual(t, wrongSession, sessionID)
}

func TestOpenAICodexRedisKeysAreV2AndNeverContainLogicalValues(t *testing.T) {
	sessionKey := OpenAICodexSessionIdentityRedisKey(repositorySessionDigest)
	threadKey := OpenAICodexThreadIdentityRedisKey(repositorySessionDigest, repositoryThreadDigest)
	require.Equal(t, OpenAICodexTurnIdentityKeyPrefix+repositorySessionDigest+":session", sessionKey)
	require.Equal(t, OpenAICodexTurnIdentityKeyPrefix+repositorySessionDigest+":thread:"+repositoryThreadDigest, threadKey)
	require.NotContains(t, sessionKey, ":v1:")
	require.Empty(t, OpenAICodexSessionIdentityRedisKey("raw-logical-session"))
	require.Empty(t, OpenAICodexThreadIdentityRedisKey(repositorySessionDigest, "raw-logical-thread"))
}

func TestOpenAICodexRedisStoreRejectsInvalidCandidates(t *testing.T) {
	store, mr, _ := newCodexIdentityRedisTestStore(t)
	_, err := store.GetOrCreateCodexSession(context.Background(), repositorySessionDigest, "11111111-1111-4111-8111-111111111111", time.Minute)
	require.Error(t, err)
	require.Empty(t, mr.Keys())
}
