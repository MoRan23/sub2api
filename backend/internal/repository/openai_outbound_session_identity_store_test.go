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
	repositoryOutboundSessionUUID = "018f5c3c-6e3a-7abc-8def-1234567890ab"
	repositoryOutboundThreadUUID  = "018f5c3c-6e3a-7abd-8def-1234567890ac"
	repositoryOutboundMappingKey  = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

func newOutboundIdentityRedisTestStore(t *testing.T) (*openAIOutboundSessionIdentityRedisStore, *miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return &openAIOutboundSessionIdentityRedisStore{rdb: rdb}, mr, rdb
}

func TestOpenAIOutboundSessionIdentityRedisStoreWinnerAndTTL(t *testing.T) {
	store, mr, _ := newOutboundIdentityRedisTestStore(t)
	ctx := context.Background()
	candidate := service.OpenAIOutboundSessionIdentity{SessionID: repositoryOutboundSessionUUID, ThreadID: repositoryOutboundThreadUUID}
	other := service.OpenAIOutboundSessionIdentity{SessionID: "018f5c3c-6e3a-7abe-8def-1234567890ad", ThreadID: "018f5c3c-6e3a-7abf-8def-1234567890ae"}

	got, err := store.GetOrCreate(ctx, repositoryOutboundMappingKey, candidate, 90*time.Second)
	require.NoError(t, err)
	require.Equal(t, candidate, got)
	require.Equal(t, 90*time.Second, mr.TTL(openAIOutboundSessionIdentityRedisKey(repositoryOutboundMappingKey)))

	got, err = store.GetOrCreate(ctx, repositoryOutboundMappingKey, other, 90*time.Second)
	require.NoError(t, err)
	require.Equal(t, candidate, got, "existing Redis value must win over a later candidate")
	require.Equal(t, 90*time.Second, mr.TTL(openAIOutboundSessionIdentityRedisKey(repositoryOutboundMappingKey)))
}

func TestOpenAIOutboundSessionIdentityRedisStoreConcurrentMiss(t *testing.T) {
	_, mr, firstClient := newOutboundIdentityRedisTestStore(t)
	secondClient := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = secondClient.Close() })
	stores := []*openAIOutboundSessionIdentityRedisStore{
		{rdb: firstClient},
		{rdb: secondClient},
	}
	ctx := context.Background()
	const workers = 20
	results := make([]service.OpenAIOutboundSessionIdentity, workers)
	candidates := make([]service.OpenAIOutboundSessionIdentity, workers)
	seenCandidates := make(map[service.OpenAIOutboundSessionIdentity]struct{}, workers)
	for i := range candidates {
		sessionID, err := uuid.NewV7()
		require.NoError(t, err)
		threadID, err := uuid.NewV7()
		require.NoError(t, err)
		candidates[i] = service.OpenAIOutboundSessionIdentity{SessionID: sessionID.String(), ThreadID: threadID.String()}
		if _, exists := seenCandidates[candidates[i]]; exists {
			t.Fatalf("generated duplicate UUIDv7 candidate at index %d", i)
		}
		seenCandidates[candidates[i]] = struct{}{}
	}
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var err error
			results[i], err = stores[i%len(stores)].GetOrCreate(ctx, strings.Repeat("b", 64), candidates[i], time.Minute)
			if err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.NotEmpty(t, results[0])
	for _, got := range results {
		require.Equal(t, results[0], got)
	}
	matchedCandidate := false
	for _, candidate := range candidates {
		if candidate == results[0] {
			matchedCandidate = true
			break
		}
	}
	require.True(t, matchedCandidate, "Redis winner must be one of the submitted candidates")
}

func TestOpenAIOutboundSessionIdentityRedisStoreRepairsCorruptionWithCAS(t *testing.T) {
	store, mr, rdb := newOutboundIdentityRedisTestStore(t)
	ctx := context.Background()
	mappingKey := strings.Repeat("c", 64)
	key := openAIOutboundSessionIdentityRedisKey(mappingKey)
	require.NoError(t, rdb.Set(ctx, key, `{"session_id":"not-a-uuid","thread_id":"not-a-uuid"}`, time.Minute).Err())
	candidate := service.OpenAIOutboundSessionIdentity{SessionID: repositoryOutboundSessionUUID, ThreadID: repositoryOutboundThreadUUID}
	got, err := store.GetOrCreate(ctx, mappingKey, candidate, 2*time.Minute)
	require.NoError(t, err)
	require.Equal(t, candidate, got)
	require.Equal(t, 2*time.Minute, mr.TTL(key))
	stored, err := rdb.Get(ctx, key).Result()
	require.NoError(t, err)
	var repaired service.OpenAIOutboundSessionIdentity
	require.NoError(t, json.Unmarshal([]byte(stored), &repaired))
	require.Equal(t, candidate, repaired)
}

func TestOpenAIOutboundSessionIdentityRedisStoreRepairsUnexpectedFields(t *testing.T) {
	store, _, rdb := newOutboundIdentityRedisTestStore(t)
	ctx := context.Background()
	mappingKey := strings.Repeat("f", 64)
	key := openAIOutboundSessionIdentityRedisKey(mappingKey)
	corrupt := `{"session_id":"` + repositoryOutboundSessionUUID + `","thread_id":"` + repositoryOutboundThreadUUID + `","logical_key":"raw-client-key"}`
	require.NoError(t, rdb.Set(ctx, key, corrupt, time.Minute).Err())
	candidate := service.OpenAIOutboundSessionIdentity{SessionID: "018f5c3c-6e3a-7abe-8def-1234567890ad", ThreadID: "018f5c3c-6e3a-7abf-8def-1234567890ae"}
	got, err := store.GetOrCreate(ctx, mappingKey, candidate, time.Minute)
	require.NoError(t, err)
	require.Equal(t, candidate, got)
	stored, err := rdb.Get(ctx, key).Result()
	require.NoError(t, err)
	require.NotContains(t, stored, "raw-client-key")
	var repaired service.OpenAIOutboundSessionIdentity
	require.NoError(t, json.Unmarshal([]byte(stored), &repaired))
	require.NoError(t, service.ValidateOpenAIOutboundSessionIdentity(repaired))
}

func TestOpenAIOutboundSessionIdentityRedisStoreRepairsDuplicateFields(t *testing.T) {
	store, _, rdb := newOutboundIdentityRedisTestStore(t)
	ctx := context.Background()
	mappingKey := strings.Repeat("a", 64)
	key := openAIOutboundSessionIdentityRedisKey(mappingKey)
	corrupt := `{"session_id":"bad","session_id":"` + repositoryOutboundSessionUUID + `","thread_id":"` + repositoryOutboundThreadUUID + `"}`
	require.NoError(t, rdb.Set(ctx, key, corrupt, time.Minute).Err())
	candidate := service.OpenAIOutboundSessionIdentity{
		SessionID: "018f5c3c-6e3a-7abe-8def-1234567890ad",
		ThreadID:  "018f5c3c-6e3a-7abf-8def-1234567890ae",
	}
	got, err := store.GetOrCreate(ctx, mappingKey, candidate, time.Minute)
	require.NoError(t, err)
	require.Equal(t, candidate, got)
	stored, err := rdb.Get(ctx, key).Result()
	require.NoError(t, err)
	require.JSONEq(t, `{"session_id":"`+candidate.SessionID+`","thread_id":"`+candidate.ThreadID+`"}`, stored)
}

func TestOpenAIOutboundSessionIdentityCorruptCASDoesNotOverwriteConcurrentWinner(t *testing.T) {
	_, _, rdb := newOutboundIdentityRedisTestStore(t)
	ctx := context.Background()
	mappingKey := strings.Repeat("d", 64)
	key := openAIOutboundSessionIdentityRedisKey(mappingKey)
	corrupt := `{"session_id":"bad","thread_id":"bad"}`
	winner := service.OpenAIOutboundSessionIdentity{SessionID: repositoryOutboundSessionUUID, ThreadID: repositoryOutboundThreadUUID}
	winnerJSON, err := json.Marshal(winner)
	require.NoError(t, err)
	require.NoError(t, rdb.Set(ctx, key, winnerJSON, time.Minute).Err())
	candidate := service.OpenAIOutboundSessionIdentity{SessionID: "018f5c3c-6e3a-7abe-8def-1234567890ad", ThreadID: "018f5c3c-6e3a-7abf-8def-1234567890ae"}
	candidateJSON, err := json.Marshal(candidate)
	require.NoError(t, err)
	got, err := openAIOutboundSessionIdentityReconcileScript.Run(ctx, rdb, []string{key}, corrupt, candidateJSON, 120, "1").Text()
	require.NoError(t, err)
	require.JSONEq(t, string(winnerJSON), got)
}

func TestOpenAIOutboundSessionIdentityRedisStoreRejectsInvalidCandidate(t *testing.T) {
	store, _, _ := newOutboundIdentityRedisTestStore(t)
	_, err := store.GetOrCreate(context.Background(), strings.Repeat("e", 64), service.OpenAIOutboundSessionIdentity{
		SessionID: "11111111-1111-4111-8111-111111111111",
		ThreadID:  repositoryOutboundThreadUUID,
	}, time.Minute)
	require.Error(t, err)
}

func TestOpenAIOutboundSessionIdentityRedisKeyIsVersioned(t *testing.T) {
	got := OpenAIOutboundSessionIdentityRedisKey(repositoryOutboundMappingKey)
	require.Equal(t, OpenAIOutboundSessionIdentityKeyPrefix+repositoryOutboundMappingKey, got)
	require.Empty(t, OpenAIOutboundSessionIdentityRedisKey("raw-logical-key"))
	payload, err := json.Marshal(service.OpenAIOutboundSessionIdentity{SessionID: repositoryOutboundSessionUUID, ThreadID: repositoryOutboundThreadUUID})
	require.NoError(t, err)
	require.Contains(t, string(payload), "session_id")
}

func TestOpenAIOutboundSessionIdentityRedisStoreRejectsRawLogicalKey(t *testing.T) {
	store, mr, _ := newOutboundIdentityRedisTestStore(t)
	candidate := service.OpenAIOutboundSessionIdentity{SessionID: repositoryOutboundSessionUUID, ThreadID: repositoryOutboundThreadUUID}
	_, err := store.GetOrCreate(context.Background(), "raw-user-session", candidate, time.Minute)
	require.Error(t, err)
	require.Empty(t, mr.Keys(), "raw logical key must never reach Redis")
}
