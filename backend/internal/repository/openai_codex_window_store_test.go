package repository

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

const (
	repositoryOpenAICodexWindowThread         = "01989f44-7c00-7000-8000-000000000041"
	repositoryOpenAICodexContextWindowInitial = "01989f44-7c00-7000-8000-000000000141"
	repositoryOpenAICodexContextWindowNext    = "01989f44-7c00-7000-8000-000000000142"
	repositoryOpenAICodexContextWindowLater   = "01989f44-7c00-7000-8000-000000000143"
)

func newOpenAICodexWindowRedisTest(t *testing.T) (*miniredis.Miniredis, *redis.Client, service.OpenAICodexWindowStore) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	store, ok := NewGatewayCache(rdb).(service.OpenAICodexWindowStore)
	require.True(t, ok)
	return mr, rdb, store
}

type testOpenAICodexRedisServerError string

func (e testOpenAICodexRedisServerError) Error() string { return string(e) }
func (testOpenAICodexRedisServerError) RedisError()     {}

func TestClassifyOpenAICodexWindowRedisErrorFailClosed(t *testing.T) {
	semanticCases := []struct {
		name     string
		err      error
		stored   bool
		legacy   bool
		canceled bool
	}{
		{name: "legacy", err: testOpenAICodexRedisServerError("CODEX_WINDOW_LEGACY_REQUIRES_RESOLVE"), legacy: true},
		{name: "invalid CAS", err: testOpenAICodexRedisServerError("CODEX_WINDOW_INVALID_STORED_VALUE"), stored: true},
		{name: "wrong type", err: testOpenAICodexRedisServerError("WRONGTYPE Operation against a key holding the wrong kind of value"), stored: true},
		{name: "authentication", err: testOpenAICodexRedisServerError("NOAUTH Authentication required")},
		{name: "permission", err: testOpenAICodexRedisServerError("NOPERM this user has no permissions")},
		{name: "read only", err: testOpenAICodexRedisServerError("READONLY You can't write against a read only replica")},
		{name: "configuration", err: testOpenAICodexRedisServerError("MISCONF Redis is configured to save RDB snapshots")},
		{name: "out of memory", err: testOpenAICodexRedisServerError("OOM command not allowed when used memory > maxmemory")},
		{name: "loading", err: testOpenAICodexRedisServerError("LOADING Redis is loading the dataset in memory")},
		{name: "try again", err: testOpenAICodexRedisServerError("TRYAGAIN Multiple keys request during rehashing")},
		{name: "redis nil", err: redis.Nil},
		{name: "unknown server reply", err: testOpenAICodexRedisServerError("SOMETHING unexpected")},
		{name: "unknown local error", err: errors.New("unexpected local error")},
		{name: "canceled", err: context.Canceled, canceled: true},
	}
	for _, test := range semanticCases {
		t.Run(test.name, func(t *testing.T) {
			err := classifyOpenAICodexWindowRedisError("test operation", test.err)
			require.Error(t, err)
			require.NotErrorIs(t, err, service.ErrOpenAICodexWindowStoreUnavailable)
			require.Equal(t, test.stored, errors.Is(err, service.ErrOpenAICodexWindowStoredInvalid))
			require.Equal(t, test.legacy, errors.Is(err, service.ErrOpenAICodexWindowLegacyRequiresResolve))
			require.Equal(t, test.canceled, errors.Is(err, context.Canceled))
		})
	}

	availabilityCases := []struct {
		name string
		err  error
	}{
		{name: "deadline", err: context.DeadlineExceeded},
		{name: "EOF", err: io.EOF},
		{name: "unexpected EOF", err: io.ErrUnexpectedEOF},
		{name: "closed", err: redis.ErrClosed},
		{name: "pool timeout", err: redis.ErrPoolTimeout},
		{name: "pool exhausted", err: redis.ErrPoolExhausted},
		{name: "network", err: &net.OpError{Op: "read", Net: "tcp", Err: errors.New("connection reset")}},
	}
	for _, test := range availabilityCases {
		t.Run(test.name, func(t *testing.T) {
			err := classifyOpenAICodexWindowRedisError("test operation", test.err)
			require.ErrorIs(t, err, service.ErrOpenAICodexWindowStoreUnavailable)
		})
	}
}

func TestGatewayCacheOpenAICodexWindowResolveCommitAndTTL(t *testing.T) {
	mr, rdb, first := newOpenAICodexWindowRedisTest(t)
	second := NewOpenAICodexWindowStore(rdb)
	ctx := context.Background()
	key := strings.Repeat("a", 64)
	initial := service.OpenAICodexWindowSnapshot{ThreadID: repositoryOpenAICodexWindowThread, ContextWindowID: repositoryOpenAICodexContextWindowInitial}

	resolved, err := first.ResolveOpenAICodexWindow(ctx, key, initial, 90*time.Second)
	require.NoError(t, err)
	require.Equal(t, initial, resolved)
	redisKey, err := OpenAICodexWindowRedisKey(key)
	require.NoError(t, err)
	require.Equal(t, 90*time.Second, mr.TTL(redisKey))
	defaultTTLKey := strings.Repeat("0", 64)
	_, err = first.ResolveOpenAICodexWindow(ctx, defaultTTLKey, initial, 0)
	require.NoError(t, err)
	defaultRedisKey, err := OpenAICodexWindowRedisKey(defaultTTLKey)
	require.NoError(t, err)
	require.Equal(t, service.OpenAICodexWindowTTL, mr.TTL(defaultRedisKey))
	wrongContext := initial
	wrongContext.ContextWindowID = repositoryOpenAICodexContextWindowLater
	contextStale, err := first.CommitOpenAICodexWindow(ctx, key, wrongContext, strings.Repeat("c", 64), repositoryOpenAICodexContextWindowNext, time.Minute)
	require.NoError(t, err)
	require.Equal(t, service.OpenAICodexWindowCommitStale, contextStale.Status)
	require.Equal(t, initial, contextStale.Snapshot)

	digest := strings.Repeat("b", 64)
	advanced, err := first.CommitOpenAICodexWindow(ctx, key, initial, digest, repositoryOpenAICodexContextWindowNext, 2*time.Minute)
	require.NoError(t, err)
	require.Equal(t, service.OpenAICodexWindowCommitAdvanced, advanced.Status)
	require.Equal(t, uint64(1), advanced.Snapshot.Number)
	require.Equal(t, repositoryOpenAICodexContextWindowNext, advanced.Snapshot.ContextWindowID)
	require.Equal(t, digest, advanced.Snapshot.LastCompactDigest)
	require.Equal(t, 2*time.Minute, mr.TTL(redisKey))

	repeated, err := second.CommitOpenAICodexWindow(ctx, key, initial, digest, repositoryOpenAICodexContextWindowLater, 3*time.Minute)
	require.NoError(t, err)
	require.Equal(t, service.OpenAICodexWindowCommitAlreadyCommitted, repeated.Status)
	require.Equal(t, advanced.Snapshot, repeated.Snapshot)
	require.Equal(t, 3*time.Minute, mr.TTL(redisKey))

	stale, err := second.CommitOpenAICodexWindow(ctx, key, initial, strings.Repeat("c", 64), repositoryOpenAICodexContextWindowLater, 4*time.Minute)
	require.NoError(t, err)
	require.Equal(t, service.OpenAICodexWindowCommitStale, stale.Status)
	require.Equal(t, advanced.Snapshot, stale.Snapshot)
	require.Equal(t, 4*time.Minute, mr.TTL(redisKey))

	mr.FastForward(4*time.Minute + time.Second)
	resumed, err := second.CommitOpenAICodexWindow(ctx, key, advanced.Snapshot, strings.Repeat("d", 64), repositoryOpenAICodexContextWindowLater, time.Minute)
	require.NoError(t, err)
	require.Equal(t, service.OpenAICodexWindowCommitAdvanced, resumed.Status)
	require.Equal(t, uint64(2), resumed.Snapshot.Number)
	require.Equal(t, repositoryOpenAICodexContextWindowLater, resumed.Snapshot.ContextWindowID)
}

func TestGatewayCacheOpenAICodexWindowResolvePromotesWithoutRegression(t *testing.T) {
	_, _, store := newOpenAICodexWindowRedisTest(t)
	ctx := context.Background()
	key := strings.Repeat("e", 64)
	initial := service.OpenAICodexWindowSnapshot{ThreadID: repositoryOpenAICodexWindowThread, ContextWindowID: repositoryOpenAICodexContextWindowInitial}
	_, err := store.ResolveOpenAICodexWindow(ctx, key, initial, time.Hour)
	require.NoError(t, err)

	promoted := service.OpenAICodexWindowSnapshot{
		ThreadID: repositoryOpenAICodexWindowThread, Number: 2, ContextWindowID: repositoryOpenAICodexContextWindowLater, LastCompactDigest: strings.Repeat("f", 64),
	}
	winner, err := store.ResolveOpenAICodexWindow(ctx, key, promoted, time.Hour)
	require.NoError(t, err)
	require.Equal(t, promoted, winner)

	lower := service.OpenAICodexWindowSnapshot{
		ThreadID: repositoryOpenAICodexWindowThread, Number: 1, ContextWindowID: repositoryOpenAICodexContextWindowNext, LastCompactDigest: strings.Repeat("1", 64),
	}
	winner, err = store.ResolveOpenAICodexWindow(ctx, key, lower, time.Hour)
	require.NoError(t, err)
	require.Equal(t, promoted, winner)

	// At the same generation the existing Redis value is the convergence winner.
	conflict := promoted
	conflict.LastCompactDigest = strings.Repeat("2", 64)
	winner, err = store.ResolveOpenAICodexWindow(ctx, key, conflict, time.Hour)
	require.NoError(t, err)
	require.Equal(t, promoted, winner)
}

func TestGatewayCacheOpenAICodexWindowResolveMigratesLegacyStateInPlace(t *testing.T) {
	mr, _, store := newOpenAICodexWindowRedisTest(t)
	ctx := context.Background()
	key := strings.Repeat("2", 64)
	redisKey, err := OpenAICodexWindowRedisKey(key)
	require.NoError(t, err)
	legacyDigest := strings.Repeat("e", 64)
	legacy := fmt.Sprintf(
		`{"thread_id":%q,"window_number":2,"last_compact_digest":%q}`,
		repositoryOpenAICodexWindowThread,
		legacyDigest,
	)
	mr.Set(redisKey, legacy)

	candidate := service.OpenAICodexWindowSnapshot{
		ThreadID:        repositoryOpenAICodexWindowThread,
		ContextWindowID: repositoryOpenAICodexContextWindowInitial,
	}
	winner, err := store.ResolveOpenAICodexWindow(ctx, key, candidate, 75*time.Second)
	require.NoError(t, err)
	require.Equal(t, uint64(2), winner.Number)
	require.Equal(t, legacyDigest, winner.LastCompactDigest)
	require.Equal(t, repositoryOpenAICodexContextWindowInitial, winner.ContextWindowID)
	require.Equal(t, 75*time.Second, mr.TTL(redisKey))

	storedRaw, err := mr.Get(redisKey)
	require.NoError(t, err)
	stored, err := decodeStrictOpenAICodexWindowSnapshot([]byte(storedRaw))
	require.NoError(t, err)
	require.Equal(t, winner, stored)
	require.Contains(t, storedRaw, `"context_window_id"`)

	// Once upgraded, a later candidate cannot replace the persisted UUID at the
	// same or an older generation.
	conflict := candidate
	conflict.ContextWindowID = repositoryOpenAICodexContextWindowLater
	again, err := store.ResolveOpenAICodexWindow(ctx, key, conflict, time.Hour)
	require.NoError(t, err)
	require.Equal(t, winner, again)
}

func TestGatewayCacheOpenAICodexWindowCommitRequiresLegacyResolveWithoutMutation(t *testing.T) {
	tests := []struct {
		name          string
		expected      service.OpenAICodexWindowSnapshot
		compactDigest string
	}{
		{
			name: "duplicate",
			expected: service.OpenAICodexWindowSnapshot{
				ThreadID: repositoryOpenAICodexWindowThread, Number: 1, ContextWindowID: repositoryOpenAICodexContextWindowInitial, LastCompactDigest: strings.Repeat("a", 64),
			},
			compactDigest: strings.Repeat("e", 64),
		},
		{
			name:          "stale",
			expected:      service.OpenAICodexWindowSnapshot{ThreadID: repositoryOpenAICodexWindowThread, ContextWindowID: repositoryOpenAICodexContextWindowInitial},
			compactDigest: strings.Repeat("c", 64),
		},
		{
			name: "matching generation",
			expected: service.OpenAICodexWindowSnapshot{
				ThreadID: repositoryOpenAICodexWindowThread, Number: 2, ContextWindowID: repositoryOpenAICodexContextWindowInitial, LastCompactDigest: strings.Repeat("e", 64),
			},
			compactDigest: strings.Repeat("d", 64),
		},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mr, _, store := newOpenAICodexWindowRedisTest(t)
			key := fmt.Sprintf("%064x", index+0x70)
			redisKey, err := OpenAICodexWindowRedisKey(key)
			require.NoError(t, err)
			legacy := fmt.Sprintf(
				`{"thread_id":%q,"window_number":2,"last_compact_digest":%q}`,
				repositoryOpenAICodexWindowThread,
				strings.Repeat("e", 64),
			)
			mr.Set(redisKey, legacy)
			mr.SetTTL(redisKey, 37*time.Second)

			_, err = store.CommitOpenAICodexWindow(context.Background(), key, test.expected, test.compactDigest, repositoryOpenAICodexContextWindowNext, time.Hour)
			require.ErrorIs(t, err, service.ErrOpenAICodexWindowLegacyRequiresResolve)
			stored, getErr := mr.Get(redisKey)
			require.NoError(t, getErr)
			require.Equal(t, legacy, stored)
			require.Equal(t, 37*time.Second, mr.TTL(redisKey))
		})
	}
}

func TestGatewayCacheOpenAICodexWindowCommitRejectsInvalidTransitionWithoutMutation(t *testing.T) {
	mr, _, store := newOpenAICodexWindowRedisTest(t)
	ctx := context.Background()
	expected := service.OpenAICodexWindowSnapshot{ThreadID: repositoryOpenAICodexWindowThread, ContextWindowID: repositoryOpenAICodexContextWindowInitial}

	t.Run("proposal must rotate", func(t *testing.T) {
		key := strings.Repeat("c", 64)
		_, err := store.CommitOpenAICodexWindow(ctx, key, expected, strings.Repeat("a", 64), expected.ContextWindowID, time.Hour)
		require.Error(t, err)
		redisKey, keyErr := OpenAICodexWindowRedisKey(key)
		require.NoError(t, keyErr)
		require.False(t, mr.Exists(redisKey))
	})

	t.Run("same digest at impossible generation", func(t *testing.T) {
		key := strings.Repeat("d", 64)
		redisKey, err := OpenAICodexWindowRedisKey(key)
		require.NoError(t, err)
		storedSnapshot := service.OpenAICodexWindowSnapshot{
			ThreadID: repositoryOpenAICodexWindowThread, Number: 2, ContextWindowID: repositoryOpenAICodexContextWindowLater, LastCompactDigest: strings.Repeat("b", 64),
		}
		raw := fmt.Sprintf(`{"thread_id":%q,"window_number":2,"context_window_id":%q,"last_compact_digest":%q}`,
			storedSnapshot.ThreadID, storedSnapshot.ContextWindowID, storedSnapshot.LastCompactDigest)
		mr.Set(redisKey, raw)
		mr.SetTTL(redisKey, 41*time.Second)
		_, err = store.CommitOpenAICodexWindow(ctx, key, expected, storedSnapshot.LastCompactDigest, repositoryOpenAICodexContextWindowNext, time.Hour)
		require.ErrorIs(t, err, service.ErrOpenAICodexWindowStoredInvalid)
		stored, getErr := mr.Get(redisKey)
		require.NoError(t, getErr)
		require.Equal(t, raw, stored)
		require.Equal(t, 41*time.Second, mr.TTL(redisKey))
	})
}

func TestGatewayCacheOpenAICodexWindowConcurrentCAS(t *testing.T) {
	_, rdb, _ := newOpenAICodexWindowRedisTest(t)
	ctx := context.Background()

	t.Run("same compact is idempotent", func(t *testing.T) {
		key := strings.Repeat("3", 64)
		digest := strings.Repeat("4", 64)
		const workers = 32
		results := make(chan service.OpenAICodexWindowCommitResult, workers)
		errs := make(chan error, workers)
		var wg sync.WaitGroup
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func(index int) {
				defer wg.Done()
				store := NewOpenAICodexWindowStore(rdb)
				proposed := fmt.Sprintf("01989f44-7c00-7000-8000-%012x", index+0x500)
				result, err := store.CommitOpenAICodexWindow(ctx, key, service.OpenAICodexWindowSnapshot{
					ThreadID: repositoryOpenAICodexWindowThread, ContextWindowID: repositoryOpenAICodexContextWindowInitial,
				}, digest, proposed, time.Hour)
				errs <- err
				results <- result
			}(i)
		}
		wg.Wait()
		close(errs)
		close(results)
		for err := range errs {
			require.NoError(t, err)
		}
		advanced, repeated := 0, 0
		winnerContextWindowIDs := make(map[string]struct{})
		for result := range results {
			winnerContextWindowIDs[result.Snapshot.ContextWindowID] = struct{}{}
			switch result.Status {
			case service.OpenAICodexWindowCommitAdvanced:
				advanced++
			case service.OpenAICodexWindowCommitAlreadyCommitted:
				repeated++
			}
		}
		require.Equal(t, 1, advanced)
		require.Equal(t, workers-1, repeated)
		require.Len(t, winnerContextWindowIDs, 1)
	})

	t.Run("different compacts have one winner", func(t *testing.T) {
		key := strings.Repeat("5", 64)
		const workers = 24
		results := make(chan service.OpenAICodexWindowCommitResult, workers)
		errs := make(chan error, workers)
		var wg sync.WaitGroup
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func(index int) {
				defer wg.Done()
				digest := fmt.Sprintf("%064x", index+1)
				proposed := fmt.Sprintf("01989f44-7c00-7000-8000-%012x", index+0x600)
				result, err := NewOpenAICodexWindowStore(rdb).CommitOpenAICodexWindow(ctx, key, service.OpenAICodexWindowSnapshot{
					ThreadID: repositoryOpenAICodexWindowThread, ContextWindowID: repositoryOpenAICodexContextWindowInitial,
				}, digest, proposed, time.Hour)
				errs <- err
				results <- result
			}(i)
		}
		wg.Wait()
		close(errs)
		close(results)
		for err := range errs {
			require.NoError(t, err)
		}
		advanced, stale := 0, 0
		winnerContextWindowIDs := make(map[string]struct{})
		for result := range results {
			winnerContextWindowIDs[result.Snapshot.ContextWindowID] = struct{}{}
			if result.Status == service.OpenAICodexWindowCommitAdvanced {
				advanced++
			} else if result.Status == service.OpenAICodexWindowCommitStale {
				stale++
			}
		}
		require.Equal(t, 1, advanced)
		require.Equal(t, workers-1, stale)
		require.Len(t, winnerContextWindowIDs, 1)
	})
}

func TestGatewayCacheOpenAICodexWindowIsolationAndMalformedState(t *testing.T) {
	mr, _, store := newOpenAICodexWindowRedisTest(t)
	ctx := context.Background()
	firstKey := strings.Repeat("6", 64)
	secondKey := strings.Repeat("7", 64)
	digest := strings.Repeat("8", 64)

	first, err := store.CommitOpenAICodexWindow(ctx, firstKey, service.OpenAICodexWindowSnapshot{
		ThreadID: repositoryOpenAICodexWindowThread, ContextWindowID: repositoryOpenAICodexContextWindowInitial,
	}, digest, repositoryOpenAICodexContextWindowNext, time.Hour)
	require.NoError(t, err)
	require.Equal(t, uint64(1), first.Snapshot.Number)
	second, err := store.ResolveOpenAICodexWindow(ctx, secondKey, service.OpenAICodexWindowSnapshot{ThreadID: repositoryOpenAICodexWindowThread, ContextWindowID: repositoryOpenAICodexContextWindowInitial}, time.Hour)
	require.NoError(t, err)
	require.Zero(t, second.Number)

	malformedKey := strings.Repeat("9", 64)
	redisKey, err := OpenAICodexWindowRedisKey(malformedKey)
	require.NoError(t, err)
	malformed := `{"thread_id":"` + repositoryOpenAICodexWindowThread + `","window_number":0,"last_compact_digest":"","extra":true}`
	mr.Set(redisKey, malformed)
	_, err = store.ResolveOpenAICodexWindow(ctx, malformedKey, service.OpenAICodexWindowSnapshot{ThreadID: repositoryOpenAICodexWindowThread, ContextWindowID: repositoryOpenAICodexContextWindowInitial}, time.Hour)
	require.ErrorIs(t, err, service.ErrOpenAICodexWindowStoredInvalid)
	stored, err := mr.Get(redisKey)
	require.NoError(t, err)
	require.Equal(t, malformed, stored)

	invalidContextKey := strings.Repeat("b", 64)
	invalidContextRedisKey, err := OpenAICodexWindowRedisKey(invalidContextKey)
	require.NoError(t, err)
	invalidContext := `{"thread_id":"` + repositoryOpenAICodexWindowThread + `","window_number":0,"context_window_id":"550e8400-e29b-41d4-a716-446655440000","last_compact_digest":""}`
	mr.Set(invalidContextRedisKey, invalidContext)
	_, err = store.ResolveOpenAICodexWindow(ctx, invalidContextKey, service.OpenAICodexWindowSnapshot{
		ThreadID: repositoryOpenAICodexWindowThread, ContextWindowID: repositoryOpenAICodexContextWindowInitial,
	}, time.Hour)
	require.ErrorIs(t, err, service.ErrOpenAICodexWindowStoredInvalid)
	stored, err = mr.Get(invalidContextRedisKey)
	require.NoError(t, err)
	require.Equal(t, invalidContext, stored)
}

func TestGatewayCacheOpenAICodexWindowWrongTypeFailsClosedWithoutMutation(t *testing.T) {
	mr, rdb, primary := newOpenAICodexWindowRedisTest(t)
	ctx := context.Background()
	key := strings.Repeat("f", 64)
	redisKey, err := OpenAICodexWindowRedisKey(key)
	require.NoError(t, err)
	require.NoError(t, rdb.RPush(ctx, redisKey, "corrupt-window-state").Err())
	require.NoError(t, rdb.Expire(ctx, redisKey, 37*time.Second).Err())

	expected := service.OpenAICodexWindowSnapshot{
		ThreadID:        repositoryOpenAICodexWindowThread,
		ContextWindowID: repositoryOpenAICodexContextWindowInitial,
	}
	assertWrongType := func(err error) {
		t.Helper()
		require.ErrorIs(t, err, service.ErrOpenAICodexWindowStoredInvalid)
		require.NotErrorIs(t, err, service.ErrOpenAICodexWindowStoreUnavailable)
		values, listErr := rdb.LRange(ctx, redisKey, 0, -1).Result()
		require.NoError(t, listErr)
		require.Equal(t, []string{"corrupt-window-state"}, values)
		require.Equal(t, 37*time.Second, mr.TTL(redisKey))
	}

	_, err = primary.ResolveOpenAICodexWindow(ctx, key, expected, time.Hour)
	assertWrongType(err)
	_, err = primary.CommitOpenAICodexWindow(
		ctx, key, expected, strings.Repeat("a", 64), repositoryOpenAICodexContextWindowNext, time.Hour,
	)
	assertWrongType(err)

	// The runtime store may seed its local candidate before asking Redis, but a
	// schema/server reply must still fail closed instead of advancing it.
	runtime := service.NewOpenAICodexWindowRuntimeStore(primary)
	_, err = runtime.ResolveOpenAICodexWindow(ctx, key, expected, time.Hour)
	assertWrongType(err)
	_, err = runtime.CommitOpenAICodexWindow(
		ctx, key, expected, strings.Repeat("b", 64), repositoryOpenAICodexContextWindowNext, time.Hour,
	)
	assertWrongType(err)
}

func TestOpenAICodexWindowRuntimePromotesAfterRedisRecovery(t *testing.T) {
	mr, rdb, primary := newOpenAICodexWindowRedisTest(t)
	runtime := service.NewOpenAICodexWindowRuntimeStore(primary)
	ctx := context.Background()
	key := "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	initial := service.OpenAICodexWindowSnapshot{ThreadID: repositoryOpenAICodexWindowThread, ContextWindowID: repositoryOpenAICodexContextWindowInitial}

	resolved, err := runtime.ResolveOpenAICodexWindow(ctx, key, initial, time.Hour)
	require.NoError(t, err)
	require.Zero(t, resolved.Number)

	mr.Close()
	_, err = primary.ResolveOpenAICodexWindow(ctx, key, initial, time.Hour)
	require.ErrorIs(t, err, service.ErrOpenAICodexWindowStoreUnavailable)
	digest := strings.Repeat("a", 64)
	committed, err := runtime.CommitOpenAICodexWindow(ctx, key, resolved, digest, repositoryOpenAICodexContextWindowNext, time.Hour)
	require.NoError(t, err)
	require.Equal(t, service.OpenAICodexWindowCommitAdvanced, committed.Status)
	require.Equal(t, uint64(1), committed.Snapshot.Number)
	require.NoError(t, mr.Restart())
	require.NoError(t, rdb.Ping(ctx).Err())

	promoted, err := runtime.ResolveOpenAICodexWindow(ctx, key, initial, time.Hour)
	require.NoError(t, err)
	require.Equal(t, uint64(1), promoted.Number)
	require.Equal(t, digest, promoted.LastCompactDigest)

	redisWinner, err := primary.ResolveOpenAICodexWindow(ctx, key, initial, time.Hour)
	require.NoError(t, err)
	require.Equal(t, promoted, redisWinner)
}
