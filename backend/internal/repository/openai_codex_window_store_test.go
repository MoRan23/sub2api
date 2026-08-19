package repository

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

const repositoryOpenAICodexWindowThread = "01989f44-7c00-7000-8000-000000000041"

func newOpenAICodexWindowRedisTest(t *testing.T) (*miniredis.Miniredis, *redis.Client, service.OpenAICodexWindowStore) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	store, ok := NewGatewayCache(rdb).(service.OpenAICodexWindowStore)
	require.True(t, ok)
	return mr, rdb, store
}

func TestGatewayCacheOpenAICodexWindowResolveCommitAndTTL(t *testing.T) {
	mr, rdb, first := newOpenAICodexWindowRedisTest(t)
	second := NewOpenAICodexWindowStore(rdb)
	ctx := context.Background()
	key := strings.Repeat("a", 64)
	initial := service.OpenAICodexWindowSnapshot{ThreadID: repositoryOpenAICodexWindowThread}

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

	digest := strings.Repeat("b", 64)
	advanced, err := first.CommitOpenAICodexWindow(ctx, key, repositoryOpenAICodexWindowThread, 0, digest, 2*time.Minute)
	require.NoError(t, err)
	require.Equal(t, service.OpenAICodexWindowCommitAdvanced, advanced.Status)
	require.Equal(t, uint64(1), advanced.Snapshot.Number)
	require.Equal(t, digest, advanced.Snapshot.LastCompactDigest)
	require.Equal(t, 2*time.Minute, mr.TTL(redisKey))

	repeated, err := second.CommitOpenAICodexWindow(ctx, key, repositoryOpenAICodexWindowThread, 0, digest, 3*time.Minute)
	require.NoError(t, err)
	require.Equal(t, service.OpenAICodexWindowCommitAlreadyCommitted, repeated.Status)
	require.Equal(t, advanced.Snapshot, repeated.Snapshot)
	require.Equal(t, 3*time.Minute, mr.TTL(redisKey))

	stale, err := second.CommitOpenAICodexWindow(ctx, key, repositoryOpenAICodexWindowThread, 0, strings.Repeat("c", 64), 4*time.Minute)
	require.NoError(t, err)
	require.Equal(t, service.OpenAICodexWindowCommitStale, stale.Status)
	require.Equal(t, advanced.Snapshot, stale.Snapshot)
	require.Equal(t, 4*time.Minute, mr.TTL(redisKey))

	mr.FastForward(4*time.Minute + time.Second)
	resumed, err := second.CommitOpenAICodexWindow(ctx, key, repositoryOpenAICodexWindowThread, 1, strings.Repeat("d", 64), time.Minute)
	require.NoError(t, err)
	require.Equal(t, service.OpenAICodexWindowCommitAdvanced, resumed.Status)
	require.Equal(t, uint64(2), resumed.Snapshot.Number)
}

func TestGatewayCacheOpenAICodexWindowResolvePromotesWithoutRegression(t *testing.T) {
	_, _, store := newOpenAICodexWindowRedisTest(t)
	ctx := context.Background()
	key := strings.Repeat("e", 64)
	initial := service.OpenAICodexWindowSnapshot{ThreadID: repositoryOpenAICodexWindowThread}
	_, err := store.ResolveOpenAICodexWindow(ctx, key, initial, time.Hour)
	require.NoError(t, err)

	promoted := service.OpenAICodexWindowSnapshot{
		ThreadID: repositoryOpenAICodexWindowThread, Number: 2, LastCompactDigest: strings.Repeat("f", 64),
	}
	winner, err := store.ResolveOpenAICodexWindow(ctx, key, promoted, time.Hour)
	require.NoError(t, err)
	require.Equal(t, promoted, winner)

	lower := service.OpenAICodexWindowSnapshot{
		ThreadID: repositoryOpenAICodexWindowThread, Number: 1, LastCompactDigest: strings.Repeat("1", 64),
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

func TestGatewayCacheOpenAICodexWindowConcurrentCAS(t *testing.T) {
	_, rdb, _ := newOpenAICodexWindowRedisTest(t)
	ctx := context.Background()

	t.Run("same compact is idempotent", func(t *testing.T) {
		key := strings.Repeat("3", 64)
		digest := strings.Repeat("4", 64)
		const workers = 32
		statuses := make(chan service.OpenAICodexWindowCommitStatus, workers)
		errs := make(chan error, workers)
		var wg sync.WaitGroup
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				store := NewOpenAICodexWindowStore(rdb)
				result, err := store.CommitOpenAICodexWindow(ctx, key, repositoryOpenAICodexWindowThread, 0, digest, time.Hour)
				errs <- err
				statuses <- result.Status
			}()
		}
		wg.Wait()
		close(errs)
		close(statuses)
		for err := range errs {
			require.NoError(t, err)
		}
		advanced, repeated := 0, 0
		for status := range statuses {
			switch status {
			case service.OpenAICodexWindowCommitAdvanced:
				advanced++
			case service.OpenAICodexWindowCommitAlreadyCommitted:
				repeated++
			}
		}
		require.Equal(t, 1, advanced)
		require.Equal(t, workers-1, repeated)
	})

	t.Run("different compacts have one winner", func(t *testing.T) {
		key := strings.Repeat("5", 64)
		const workers = 24
		statuses := make(chan service.OpenAICodexWindowCommitStatus, workers)
		errs := make(chan error, workers)
		var wg sync.WaitGroup
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func(index int) {
				defer wg.Done()
				digest := fmt.Sprintf("%064x", index+1)
				result, err := NewOpenAICodexWindowStore(rdb).CommitOpenAICodexWindow(ctx, key, repositoryOpenAICodexWindowThread, 0, digest, time.Hour)
				errs <- err
				statuses <- result.Status
			}(i)
		}
		wg.Wait()
		close(errs)
		close(statuses)
		for err := range errs {
			require.NoError(t, err)
		}
		advanced, stale := 0, 0
		for status := range statuses {
			if status == service.OpenAICodexWindowCommitAdvanced {
				advanced++
			} else if status == service.OpenAICodexWindowCommitStale {
				stale++
			}
		}
		require.Equal(t, 1, advanced)
		require.Equal(t, workers-1, stale)
	})
}

func TestGatewayCacheOpenAICodexWindowIsolationAndMalformedState(t *testing.T) {
	mr, _, store := newOpenAICodexWindowRedisTest(t)
	ctx := context.Background()
	firstKey := strings.Repeat("6", 64)
	secondKey := strings.Repeat("7", 64)
	digest := strings.Repeat("8", 64)

	first, err := store.CommitOpenAICodexWindow(ctx, firstKey, repositoryOpenAICodexWindowThread, 0, digest, time.Hour)
	require.NoError(t, err)
	require.Equal(t, uint64(1), first.Snapshot.Number)
	second, err := store.ResolveOpenAICodexWindow(ctx, secondKey, service.OpenAICodexWindowSnapshot{ThreadID: repositoryOpenAICodexWindowThread}, time.Hour)
	require.NoError(t, err)
	require.Zero(t, second.Number)

	malformedKey := strings.Repeat("9", 64)
	redisKey, err := OpenAICodexWindowRedisKey(malformedKey)
	require.NoError(t, err)
	malformed := `{"thread_id":"` + repositoryOpenAICodexWindowThread + `","window_number":0,"last_compact_digest":"","extra":true}`
	mr.Set(redisKey, malformed)
	_, err = store.ResolveOpenAICodexWindow(ctx, malformedKey, service.OpenAICodexWindowSnapshot{ThreadID: repositoryOpenAICodexWindowThread}, time.Hour)
	require.Error(t, err)
	stored, err := mr.Get(redisKey)
	require.NoError(t, err)
	require.Equal(t, malformed, stored)
}

func TestOpenAICodexWindowRuntimePromotesAfterRedisRecovery(t *testing.T) {
	mr, rdb, primary := newOpenAICodexWindowRedisTest(t)
	runtime := service.NewOpenAICodexWindowRuntimeStore(primary)
	ctx := context.Background()
	key := "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	initial := service.OpenAICodexWindowSnapshot{ThreadID: repositoryOpenAICodexWindowThread}

	resolved, err := runtime.ResolveOpenAICodexWindow(ctx, key, initial, time.Hour)
	require.NoError(t, err)
	require.Zero(t, resolved.Number)

	mr.Close()
	digest := strings.Repeat("a", 64)
	committed, err := runtime.CommitOpenAICodexWindow(ctx, key, repositoryOpenAICodexWindowThread, 0, digest, time.Hour)
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
