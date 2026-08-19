package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const (
	testOpenAICodexWindowThread      = "01989f44-7c00-7000-8000-000000000031"
	testOpenAICodexWindowOtherThread = "01989f44-7c00-7000-8000-000000000032"
)

func TestOpenAICodexWindowHMACIsolation(t *testing.T) {
	const secret = "window-test-secret"
	first, err := OpenAICodexWindowMappingKey(secret, "account:41", 7, testOpenAICodexWindowThread)
	require.NoError(t, err)
	require.Len(t, first, 64)
	require.NotContains(t, first, "account")
	require.NotContains(t, first, testOpenAICodexWindowThread)

	again, err := OpenAICodexWindowMappingKey(secret, "account:41", 7, testOpenAICodexWindowThread)
	require.NoError(t, err)
	require.Equal(t, first, again)

	ownerChanged, err := OpenAICodexWindowMappingKey(secret, "account:42", 7, testOpenAICodexWindowThread)
	require.NoError(t, err)
	apiKeyChanged, err := OpenAICodexWindowMappingKey(secret, "account:41", 8, testOpenAICodexWindowThread)
	require.NoError(t, err)
	threadChanged, err := OpenAICodexWindowMappingKey(secret, "account:41", 7, testOpenAICodexWindowOtherThread)
	require.NoError(t, err)
	require.NotEqual(t, first, ownerChanged)
	require.NotEqual(t, first, apiKeyChanged)
	require.NotEqual(t, first, threadChanged)

	digest, err := OpenAICodexCompactTurnDigest(secret, "account:41", 7, testOpenAICodexWindowThread, 0, "compact-turn")
	require.NoError(t, err)
	require.Len(t, digest, 64)
	require.NotContains(t, digest, "compact-turn")
	nextWindowDigest, err := OpenAICodexCompactTurnDigest(secret, "account:41", 7, testOpenAICodexWindowThread, 1, "compact-turn")
	require.NoError(t, err)
	require.NotEqual(t, digest, nextWindowDigest)
}

func TestOpenAICodexWindowSnapshotValidationAndID(t *testing.T) {
	initial := OpenAICodexWindowSnapshot{ThreadID: testOpenAICodexWindowThread}
	require.NoError(t, ValidateOpenAICodexWindowSnapshot(initial))
	require.Equal(t, testOpenAICodexWindowThread+":0", initial.WindowID())

	advanced := OpenAICodexWindowSnapshot{ThreadID: testOpenAICodexWindowThread, Number: 1, LastCompactDigest: strings.Repeat("a", 64)}
	require.NoError(t, ValidateOpenAICodexWindowSnapshot(advanced))
	require.Equal(t, testOpenAICodexWindowThread+":1", advanced.WindowID())
	require.Error(t, ValidateOpenAICodexWindowSnapshot(OpenAICodexWindowSnapshot{ThreadID: testOpenAICodexWindowThread, Number: 1}))
	require.Error(t, ValidateOpenAICodexWindowSnapshot(OpenAICodexWindowSnapshot{ThreadID: testOpenAICodexWindowThread, LastCompactDigest: strings.Repeat("a", 64)}))
	require.Error(t, ValidateOpenAICodexWindowSnapshot(OpenAICodexWindowSnapshot{ThreadID: "not-a-uuid"}))
}

func TestOpenAICodexWindowLocalCASIsIdempotentAndConcurrent(t *testing.T) {
	store := newOpenAICodexWindowLocalStore(16)
	ctx := context.Background()
	key := strings.Repeat("a", 64)
	digest := strings.Repeat("b", 64)
	_, err := store.ResolveOpenAICodexWindow(ctx, key, OpenAICodexWindowSnapshot{ThreadID: testOpenAICodexWindowThread}, time.Hour)
	require.NoError(t, err)

	const workers = 64
	results := make(chan OpenAICodexWindowCommitResult, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, commitErr := store.CommitOpenAICodexWindow(ctx, key, testOpenAICodexWindowThread, 0, digest, time.Hour)
			results <- result
			errs <- commitErr
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	advanced, repeated := 0, 0
	for commitErr := range errs {
		require.NoError(t, commitErr)
	}
	for result := range results {
		require.Equal(t, uint64(1), result.Snapshot.Number)
		switch result.Status {
		case OpenAICodexWindowCommitAdvanced:
			advanced++
		case OpenAICodexWindowCommitAlreadyCommitted:
			repeated++
		default:
			t.Fatalf("unexpected status %q", result.Status)
		}
	}
	require.Equal(t, 1, advanced)
	require.Equal(t, workers-1, repeated)

	stale, err := store.CommitOpenAICodexWindow(ctx, key, testOpenAICodexWindowThread, 0, strings.Repeat("c", 64), time.Hour)
	require.NoError(t, err)
	require.Equal(t, OpenAICodexWindowCommitStale, stale.Status)
	require.Equal(t, uint64(1), stale.Snapshot.Number)
}

func TestOpenAICodexWindowLocalStoreBoundedAndThreadIsolated(t *testing.T) {
	store := newOpenAICodexWindowLocalStore(2)
	ctx := context.Background()
	keys := []string{strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)}
	for index, key := range keys {
		threadID := testOpenAICodexWindowThread
		if index == 1 {
			threadID = testOpenAICodexWindowOtherThread
		}
		_, err := store.ResolveOpenAICodexWindow(ctx, key, OpenAICodexWindowSnapshot{ThreadID: threadID}, time.Hour)
		require.NoError(t, err)
	}
	require.Len(t, store.entries, 2)
	_, exists := store.entries[keys[0]]
	require.False(t, exists)
	require.Equal(t, testOpenAICodexWindowOtherThread, store.entries[keys[1]].snapshot.ThreadID)
}

type toggleOpenAICodexWindowStore struct {
	mu      sync.RWMutex
	failing bool
	store   OpenAICodexWindowStore
}

func (s *toggleOpenAICodexWindowStore) setFailing(failing bool) {
	s.mu.Lock()
	s.failing = failing
	s.mu.Unlock()
}

func (s *toggleOpenAICodexWindowStore) unavailable() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.failing
}

func (s *toggleOpenAICodexWindowStore) ResolveOpenAICodexWindow(ctx context.Context, mappingKey string, candidate OpenAICodexWindowSnapshot, ttl time.Duration) (OpenAICodexWindowSnapshot, error) {
	if s.unavailable() {
		return OpenAICodexWindowSnapshot{}, errors.New("primary unavailable")
	}
	return s.store.ResolveOpenAICodexWindow(ctx, mappingKey, candidate, ttl)
}

func (s *toggleOpenAICodexWindowStore) CommitOpenAICodexWindow(ctx context.Context, mappingKey, threadID string, expected uint64, compactDigest string, ttl time.Duration) (OpenAICodexWindowCommitResult, error) {
	if s.unavailable() {
		return OpenAICodexWindowCommitResult{}, errors.New("primary unavailable")
	}
	return s.store.CommitOpenAICodexWindow(ctx, mappingKey, threadID, expected, compactDigest, ttl)
}

func TestOpenAICodexWindowRuntimeFallbackAndRecoveryPromotion(t *testing.T) {
	ctx := context.Background()
	key := strings.Repeat("d", 64)
	primaryLocal := newOpenAICodexWindowLocalStore(16)
	primary := &toggleOpenAICodexWindowStore{store: primaryLocal}
	processLocal := newOpenAICodexWindowLocalStore(16)
	runtime := newOpenAICodexWindowRuntimeStoreWithLocal(primary, processLocal)

	initial, err := runtime.ResolveOpenAICodexWindow(ctx, key, OpenAICodexWindowSnapshot{ThreadID: testOpenAICodexWindowThread}, time.Hour)
	require.NoError(t, err)
	require.Zero(t, initial.Number)

	primary.setFailing(true)
	firstDigest := strings.Repeat("e", 64)
	committed, err := runtime.CommitOpenAICodexWindow(ctx, key, testOpenAICodexWindowThread, 0, firstDigest, time.Hour)
	require.NoError(t, err)
	require.Equal(t, OpenAICodexWindowCommitAdvanced, committed.Status)
	require.Equal(t, uint64(1), committed.Snapshot.Number)
	require.True(t, processLocal.entries[key].pendingPromotion)
	require.Zero(t, primaryLocal.entries[key].snapshot.Number)

	primary.setFailing(false)
	promoted, err := runtime.ResolveOpenAICodexWindow(ctx, key, OpenAICodexWindowSnapshot{ThreadID: testOpenAICodexWindowThread}, time.Hour)
	require.NoError(t, err)
	require.Equal(t, uint64(1), promoted.Number)
	require.Equal(t, firstDigest, promoted.LastCompactDigest)
	require.False(t, processLocal.entries[key].pendingPromotion)
	require.Equal(t, promoted, primaryLocal.entries[key].snapshot)

	secondDigest := strings.Repeat("f", 64)
	second, err := runtime.CommitOpenAICodexWindow(ctx, key, testOpenAICodexWindowThread, 1, secondDigest, time.Hour)
	require.NoError(t, err)
	require.Equal(t, OpenAICodexWindowCommitAdvanced, second.Status)
	require.Equal(t, uint64(2), second.Snapshot.Number)
	require.Equal(t, second.Snapshot, primaryLocal.entries[key].snapshot)
}

func TestOpenAICodexWindowLocalDistinctConcurrentCommitsHaveOneWinner(t *testing.T) {
	store := newOpenAICodexWindowLocalStore(16)
	ctx := context.Background()
	key := strings.Repeat("9", 64)
	require.NoError(t, func() error {
		_, err := store.ResolveOpenAICodexWindow(ctx, key, OpenAICodexWindowSnapshot{ThreadID: testOpenAICodexWindowThread}, time.Hour)
		return err
	}())

	const workers = 24
	statuses := make(chan OpenAICodexWindowCommitStatus, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			digest := fmt.Sprintf("%064x", index+1)
			result, err := store.CommitOpenAICodexWindow(ctx, key, testOpenAICodexWindowThread, 0, digest, time.Hour)
			errs <- err
			statuses <- result.Status
		}(i)
	}
	wg.Wait()
	close(statuses)
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	advanced, stale := 0, 0
	for status := range statuses {
		if status == OpenAICodexWindowCommitAdvanced {
			advanced++
		} else if status == OpenAICodexWindowCommitStale {
			stale++
		}
	}
	require.Equal(t, 1, advanced)
	require.Equal(t, workers-1, stale)
}
