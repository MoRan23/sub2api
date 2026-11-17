//go:build unit

package repository

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// Hold the first timestamp response after Redis has already returned its old
// value, so a second generation can publish before the delayed reader resumes.
type lastUsedResponseBarrier struct {
	blocked atomic.Bool
	entered chan struct{}
	release chan struct{}
}

func (h *lastUsedResponseBarrier) DialHook(next redis.DialHook) redis.DialHook          { return next }
func (h *lastUsedResponseBarrier) ProcessHook(next redis.ProcessHook) redis.ProcessHook { return next }
func (h *lastUsedResponseBarrier) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, commands []redis.Cmder) error {
		err := next(ctx, commands)
		for _, command := range commands {
			args := command.Args()
			if err == nil && command.Name() == "mget" && len(args) > 1 {
				key, _ := args[1].(string)
				if strings.HasPrefix(key, schedulerAccountLastUsedPrefix) && h.blocked.CompareAndSwap(false, true) {
					close(h.entered)
					select {
					case <-h.release:
					case <-ctx.Done():
						return ctx.Err()
					}
				}
			}
		}
		return err
	}
}

func TestSchedulerLastUsedLateReadCannotReplaceNewGeneration(t *testing.T) {
	ctx := context.Background()
	cache, _, _ := newSchedulerCacheMemoUnit(t, defaultSchedulerSnapshotWriteChunkSize)
	bucket := memoTestBucket(181)
	memoPublish(t, cache, bucket, memoTestAccount(181, "a"))
	firstTime := time.Now().UTC().Truncate(time.Millisecond)
	require.NoError(t, cache.UpdateLastUsed(ctx, map[int64]time.Time{181: firstTime}))
	entry := &schedulerSnapshotEntry{lastUsedKeys: []string{schedulerLastUsedKey("181")}}
	hook := &lastUsedResponseBarrier{entered: make(chan struct{}), release: make(chan struct{})}
	cache.rdb.AddHook(hook)
	done := make(chan error, 1)
	go func() {
		_, err := cache.entryLastUsed(ctx, entry)
		done <- err
	}()
	select {
	case <-hook.entered:
	case <-time.After(time.Second):
		t.Fatal("first timestamp read did not reach the barrier")
	}
	later := firstTime.Add(time.Minute)
	require.NoError(t, cache.UpdateLastUsed(ctx, map[int64]time.Time{181: later}))
	newer, err := cache.entryLastUsed(ctx, entry)
	require.NoError(t, err)
	require.Equal(t, later, *newer[0])
	generation := cache.lastUsedGeneration.Load()
	close(hook.release)
	require.NoError(t, <-done)
	stored := entry.lastUsed.Load()
	require.Equal(t, generation, stored.generation)
	require.Equal(t, later, *stored.values[0])
}
