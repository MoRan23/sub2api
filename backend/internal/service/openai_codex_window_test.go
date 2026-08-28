package service

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const (
	testOpenAICodexWindowThread         = "01989f44-7c00-7000-8000-000000000031"
	testOpenAICodexWindowOtherThread    = "01989f44-7c00-7000-8000-000000000032"
	testOpenAICodexContextWindowInitial = "01989f44-7c00-7000-8000-000000000131"
	testOpenAICodexContextWindowNext    = "01989f44-7c00-7000-8000-000000000132"
	testOpenAICodexContextWindowLater   = "01989f44-7c00-7000-8000-000000000133"
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

	initial := OpenAICodexWindowSnapshot{ThreadID: testOpenAICodexWindowThread, ContextWindowID: testOpenAICodexContextWindowInitial}
	digest, err := OpenAICodexCompactTurnDigest(secret, "account:41", 7, initial, "compact-turn")
	require.NoError(t, err)
	require.Len(t, digest, 64)
	require.NotContains(t, digest, "compact-turn")
	require.NotEqual(t, mustOpenAICodexHMACForTest(t, secret, "sub2api/openai-codex-window/v1/compact",
		"account:41", "7", testOpenAICodexWindowThread, "0", "compact-turn"), digest)

	nextWindow := OpenAICodexWindowSnapshot{
		ThreadID: testOpenAICodexWindowThread, Number: 1, ContextWindowID: testOpenAICodexContextWindowNext, LastCompactDigest: strings.Repeat("f", 64),
	}
	nextWindowDigest, err := OpenAICodexCompactTurnDigest(secret, "account:41", 7, nextWindow, "compact-turn")
	require.NoError(t, err)
	require.NotEqual(t, digest, nextWindowDigest)
	contextChanged := initial
	contextChanged.ContextWindowID = testOpenAICodexContextWindowLater
	contextDigest, err := OpenAICodexCompactTurnDigest(secret, "account:41", 7, contextChanged, "compact-turn")
	require.NoError(t, err)
	require.NotEqual(t, digest, contextDigest)
	contextChanged.ContextWindowID = "550e8400-e29b-41d4-a716-446655440000"
	_, err = OpenAICodexCompactTurnDigest(secret, "account:41", 7, contextChanged, "compact-turn")
	require.Error(t, err)
}

func mustOpenAICodexHMACForTest(t *testing.T, secret, domain string, fields ...string) string {
	t.Helper()
	value, err := openAICodexHMAC(secret, domain, fields...)
	require.NoError(t, err)
	return value
}

func TestOpenAICodexWindowSnapshotValidationAndID(t *testing.T) {
	initial := OpenAICodexWindowSnapshot{ThreadID: testOpenAICodexWindowThread, ContextWindowID: testOpenAICodexContextWindowInitial}
	require.NoError(t, ValidateOpenAICodexWindowSnapshot(initial))
	require.Equal(t, testOpenAICodexWindowThread+":0", initial.WindowID())

	advanced := OpenAICodexWindowSnapshot{ThreadID: testOpenAICodexWindowThread, Number: 1, ContextWindowID: testOpenAICodexContextWindowNext, LastCompactDigest: strings.Repeat("a", 64)}
	require.NoError(t, ValidateOpenAICodexWindowSnapshot(advanced))
	require.Equal(t, testOpenAICodexWindowThread+":1", advanced.WindowID())
	require.Error(t, ValidateOpenAICodexWindowSnapshot(OpenAICodexWindowSnapshot{ThreadID: testOpenAICodexWindowThread, Number: 1, ContextWindowID: testOpenAICodexContextWindowNext}))
	require.Error(t, ValidateOpenAICodexWindowSnapshot(OpenAICodexWindowSnapshot{ThreadID: testOpenAICodexWindowThread, ContextWindowID: testOpenAICodexContextWindowInitial, LastCompactDigest: strings.Repeat("a", 64)}))
	require.Error(t, ValidateOpenAICodexWindowSnapshot(OpenAICodexWindowSnapshot{ThreadID: "not-a-uuid", ContextWindowID: testOpenAICodexContextWindowInitial}))
	require.Error(t, ValidateOpenAICodexWindowSnapshot(OpenAICodexWindowSnapshot{ThreadID: testOpenAICodexWindowThread}))
	require.Error(t, ValidateOpenAICodexWindowSnapshot(OpenAICodexWindowSnapshot{ThreadID: testOpenAICodexWindowThread, ContextWindowID: "550e8400-e29b-41d4-a716-446655440000"}))
	require.Error(t, ValidateOpenAICodexWindowSnapshot(OpenAICodexWindowSnapshot{ThreadID: testOpenAICodexWindowThread, ContextWindowID: "not-a-uuid"}))
}

func TestOpenAICodexWindowLocalCASIsIdempotentAndConcurrent(t *testing.T) {
	store := newOpenAICodexWindowLocalStore(16)
	ctx := context.Background()
	key := strings.Repeat("a", 64)
	digest := strings.Repeat("b", 64)
	expected := OpenAICodexWindowSnapshot{ThreadID: testOpenAICodexWindowThread, ContextWindowID: testOpenAICodexContextWindowInitial}
	_, err := store.ResolveOpenAICodexWindow(ctx, key, expected, time.Hour)
	require.NoError(t, err)
	_, err = store.CommitOpenAICodexWindow(ctx, key, expected, digest, expected.ContextWindowID, time.Hour)
	require.Error(t, err)
	require.Equal(t, expected, store.entries[key].snapshot)
	wrongContext := expected
	wrongContext.ContextWindowID = testOpenAICodexContextWindowLater
	contextStale, err := store.CommitOpenAICodexWindow(ctx, key, wrongContext, strings.Repeat("c", 64), testOpenAICodexContextWindowNext, time.Hour)
	require.NoError(t, err)
	require.Equal(t, OpenAICodexWindowCommitStale, contextStale.Status)
	require.Equal(t, expected, contextStale.Snapshot)

	const workers = 64
	results := make(chan OpenAICodexWindowCommitResult, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			proposed := fmt.Sprintf("01989f44-7c00-7000-8000-%012x", index+0x200)
			result, commitErr := store.CommitOpenAICodexWindow(ctx, key, expected, digest, proposed, time.Hour)
			results <- result
			errs <- commitErr
		}(i)
	}
	wg.Wait()
	close(results)
	close(errs)
	advanced, repeated := 0, 0
	winnerContextWindowIDs := make(map[string]struct{})
	for commitErr := range errs {
		require.NoError(t, commitErr)
	}
	for result := range results {
		require.Equal(t, uint64(1), result.Snapshot.Number)
		require.NoError(t, ValidateOpenAICodexWindowSnapshot(result.Snapshot))
		winnerContextWindowIDs[result.Snapshot.ContextWindowID] = struct{}{}
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
	require.Len(t, winnerContextWindowIDs, 1)

	impossibleExpected := OpenAICodexWindowSnapshot{
		ThreadID: testOpenAICodexWindowThread, Number: 1, ContextWindowID: testOpenAICodexContextWindowInitial, LastCompactDigest: strings.Repeat("d", 64),
	}
	expiresBeforeInvalidCommit := store.entries[key].expiresAt
	_, err = store.CommitOpenAICodexWindow(ctx, key, impossibleExpected, digest, testOpenAICodexContextWindowLater, 2*time.Hour)
	require.ErrorIs(t, err, ErrOpenAICodexWindowStoredInvalid)
	require.Equal(t, expiresBeforeInvalidCommit, store.entries[key].expiresAt)

	stale, err := store.CommitOpenAICodexWindow(ctx, key, expected, strings.Repeat("c", 64), testOpenAICodexContextWindowLater, time.Hour)
	require.NoError(t, err)
	require.Equal(t, OpenAICodexWindowCommitStale, stale.Status)
	require.Equal(t, uint64(1), stale.Snapshot.Number)
	_, isWinner := winnerContextWindowIDs[stale.Snapshot.ContextWindowID]
	require.True(t, isWinner)
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
		contextWindowID := fmt.Sprintf("01989f44-7c00-7000-8000-%012x", index+0x300)
		_, err := store.ResolveOpenAICodexWindow(ctx, key, OpenAICodexWindowSnapshot{ThreadID: threadID, ContextWindowID: contextWindowID}, time.Hour)
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

type semanticErrorOpenAICodexWindowStore struct {
	store      OpenAICodexWindowStore
	resolveErr error
	commitErr  error
}

func (s *semanticErrorOpenAICodexWindowStore) ResolveOpenAICodexWindow(ctx context.Context, mappingKey string, candidate OpenAICodexWindowSnapshot, ttl time.Duration) (OpenAICodexWindowSnapshot, error) {
	if s.resolveErr != nil {
		return OpenAICodexWindowSnapshot{}, s.resolveErr
	}
	return s.store.ResolveOpenAICodexWindow(ctx, mappingKey, candidate, ttl)
}

func (s *semanticErrorOpenAICodexWindowStore) CommitOpenAICodexWindow(ctx context.Context, mappingKey string, expected OpenAICodexWindowSnapshot, compactDigest, proposedNextContextWindowID string, ttl time.Duration) (OpenAICodexWindowCommitResult, error) {
	if s.commitErr != nil {
		return OpenAICodexWindowCommitResult{}, s.commitErr
	}
	return s.store.CommitOpenAICodexWindow(ctx, mappingKey, expected, compactDigest, proposedNextContextWindowID, ttl)
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
		return OpenAICodexWindowSnapshot{}, fmt.Errorf("primary unavailable: %w", ErrOpenAICodexWindowStoreUnavailable)
	}
	return s.store.ResolveOpenAICodexWindow(ctx, mappingKey, candidate, ttl)
}

func (s *toggleOpenAICodexWindowStore) CommitOpenAICodexWindow(ctx context.Context, mappingKey string, expected OpenAICodexWindowSnapshot, compactDigest, proposedNextContextWindowID string, ttl time.Duration) (OpenAICodexWindowCommitResult, error) {
	if s.unavailable() {
		return OpenAICodexWindowCommitResult{}, fmt.Errorf("primary unavailable: %w", ErrOpenAICodexWindowStoreUnavailable)
	}
	return s.store.CommitOpenAICodexWindow(ctx, mappingKey, expected, compactDigest, proposedNextContextWindowID, ttl)
}

func TestOpenAICodexWindowRuntimeFallbackAndRecoveryPromotion(t *testing.T) {
	ctx := context.Background()
	key := strings.Repeat("d", 64)
	primaryLocal := newOpenAICodexWindowLocalStore(16)
	primary := &toggleOpenAICodexWindowStore{store: primaryLocal}
	processLocal := newOpenAICodexWindowLocalStore(16)
	runtime := newOpenAICodexWindowRuntimeStoreWithLocal(primary, processLocal)

	initialCandidate := OpenAICodexWindowSnapshot{ThreadID: testOpenAICodexWindowThread, ContextWindowID: testOpenAICodexContextWindowInitial}
	initial, err := runtime.ResolveOpenAICodexWindow(ctx, key, initialCandidate, time.Hour)
	require.NoError(t, err)
	require.Zero(t, initial.Number)

	primary.setFailing(true)
	firstDigest := strings.Repeat("e", 64)
	committed, err := runtime.CommitOpenAICodexWindow(ctx, key, initial, firstDigest, testOpenAICodexContextWindowNext, time.Hour)
	require.NoError(t, err)
	require.Equal(t, OpenAICodexWindowCommitAdvanced, committed.Status)
	require.Equal(t, uint64(1), committed.Snapshot.Number)
	require.Equal(t, testOpenAICodexContextWindowNext, committed.Snapshot.ContextWindowID)
	require.True(t, processLocal.entries[key].pendingPromotion)
	require.Zero(t, primaryLocal.entries[key].snapshot.Number)

	primary.setFailing(false)
	promoted, err := runtime.ResolveOpenAICodexWindow(ctx, key, initialCandidate, time.Hour)
	require.NoError(t, err)
	require.Equal(t, uint64(1), promoted.Number)
	require.Equal(t, firstDigest, promoted.LastCompactDigest)
	require.False(t, processLocal.entries[key].pendingPromotion)
	require.Equal(t, promoted, primaryLocal.entries[key].snapshot)

	secondDigest := strings.Repeat("f", 64)
	second, err := runtime.CommitOpenAICodexWindow(ctx, key, promoted, secondDigest, testOpenAICodexContextWindowLater, time.Hour)
	require.NoError(t, err)
	require.Equal(t, OpenAICodexWindowCommitAdvanced, second.Status)
	require.Equal(t, uint64(2), second.Snapshot.Number)
	require.Equal(t, testOpenAICodexContextWindowLater, second.Snapshot.ContextWindowID)
	require.Equal(t, second.Snapshot, primaryLocal.entries[key].snapshot)
}

func TestOpenAICodexWindowRuntimeDoesNotFallbackOnSemanticErrors(t *testing.T) {
	t.Run("resolve", func(t *testing.T) {
		primary := &semanticErrorOpenAICodexWindowStore{
			store:      newOpenAICodexWindowLocalStore(16),
			resolveErr: fmt.Errorf("bad stored value: %w", ErrOpenAICodexWindowStoredInvalid),
		}
		local := newOpenAICodexWindowLocalStore(16)
		runtime := newOpenAICodexWindowRuntimeStoreWithLocal(primary, local)
		key := strings.Repeat("7", 64)
		candidate := OpenAICodexWindowSnapshot{ThreadID: testOpenAICodexWindowThread, ContextWindowID: testOpenAICodexContextWindowInitial}
		_, err := runtime.ResolveOpenAICodexWindow(context.Background(), key, candidate, time.Hour)
		require.ErrorIs(t, err, ErrOpenAICodexWindowStoredInvalid)
		require.False(t, local.entries[key].pendingPromotion)
	})

	t.Run("commit", func(t *testing.T) {
		primaryLocal := newOpenAICodexWindowLocalStore(16)
		primary := &semanticErrorOpenAICodexWindowStore{store: primaryLocal}
		local := newOpenAICodexWindowLocalStore(16)
		runtime := newOpenAICodexWindowRuntimeStoreWithLocal(primary, local)
		key := strings.Repeat("8", 64)
		expected := OpenAICodexWindowSnapshot{ThreadID: testOpenAICodexWindowThread, ContextWindowID: testOpenAICodexContextWindowInitial}
		_, err := runtime.ResolveOpenAICodexWindow(context.Background(), key, expected, time.Hour)
		require.NoError(t, err)
		primary.commitErr = fmt.Errorf("legacy: %w", ErrOpenAICodexWindowLegacyRequiresResolve)
		_, err = runtime.CommitOpenAICodexWindow(context.Background(), key, expected, strings.Repeat("a", 64), testOpenAICodexContextWindowNext, time.Hour)
		require.ErrorIs(t, err, ErrOpenAICodexWindowLegacyRequiresResolve)
		require.Equal(t, expected, local.entries[key].snapshot)
		require.Equal(t, expected, primaryLocal.entries[key].snapshot)
		require.False(t, local.entries[key].pendingPromotion)
	})
}

func TestOpenAICodexWindowLocalDistinctConcurrentCommitsHaveOneWinner(t *testing.T) {
	store := newOpenAICodexWindowLocalStore(16)
	ctx := context.Background()
	key := strings.Repeat("9", 64)
	expected := OpenAICodexWindowSnapshot{ThreadID: testOpenAICodexWindowThread, ContextWindowID: testOpenAICodexContextWindowInitial}
	require.NoError(t, func() error {
		_, err := store.ResolveOpenAICodexWindow(ctx, key, expected, time.Hour)
		return err
	}())

	const workers = 24
	results := make(chan OpenAICodexWindowCommitResult, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			digest := fmt.Sprintf("%064x", index+1)
			proposed := fmt.Sprintf("01989f44-7c00-7000-8000-%012x", index+0x400)
			result, err := store.CommitOpenAICodexWindow(ctx, key, expected, digest, proposed, time.Hour)
			errs <- err
			results <- result
		}(i)
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	advanced, stale := 0, 0
	winnerContextWindowIDs := make(map[string]struct{})
	for result := range results {
		winnerContextWindowIDs[result.Snapshot.ContextWindowID] = struct{}{}
		if result.Status == OpenAICodexWindowCommitAdvanced {
			advanced++
		} else if result.Status == OpenAICodexWindowCommitStale {
			stale++
		}
	}
	require.Equal(t, 1, advanced)
	require.Equal(t, workers-1, stale)
	require.Len(t, winnerContextWindowIDs, 1)
}
