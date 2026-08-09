package service

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func newOpenAIV1CleanupTestWorker(t *testing.T) (*OpenAIOutboundSessionV1CleanupWorker, *miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	worker := NewOpenAIOutboundSessionV1CleanupWorker(rdb)
	worker.verifyWait = time.Millisecond
	worker.retryMin = time.Millisecond
	worker.retryMax = 2 * time.Millisecond
	return worker, mr, rdb
}

func TestOpenAIOutboundSessionV1CleanupDeletesOnlyV1AndMarksComplete(t *testing.T) {
	worker, mr, rdb := newOpenAIV1CleanupTestWorker(t)
	ctx := context.Background()
	for _, key := range []string{
		"openai-outbound-session:v1:aaaaaaaa",
		"openai-outbound-session:v1:bbbbbbbb",
	} {
		require.NoError(t, rdb.Set(ctx, key, `{"session_id":"legacy"}`, 0).Err())
	}
	v2Key := "openai-outbound-session:v2:cccccccc:session"
	require.NoError(t, rdb.Set(ctx, v2Key, `{"session_id":"v2"}`, 0).Err())

	complete, deleted, err := worker.cleanupOnce(ctx)
	require.NoError(t, err)
	require.True(t, complete)
	require.EqualValues(t, 2, deleted)
	require.True(t, mr.Exists(v2Key))
	require.True(t, mr.Exists(openAIOutboundSessionV1CleanupMarker))
	for _, key := range mr.Keys() {
		require.NotContains(t, key, "openai-outbound-session:v1:")
	}

	complete, deleted, err = worker.cleanupOnce(ctx)
	require.NoError(t, err)
	require.True(t, complete)
	require.Zero(t, deleted)
}

func TestOpenAIOutboundSessionV1CleanupRespectsDistributedLock(t *testing.T) {
	worker, _, rdb := newOpenAIV1CleanupTestWorker(t)
	require.NoError(t, rdb.Set(context.Background(), openAIOutboundSessionV1CleanupLock, "other-instance", time.Minute).Err())
	complete, deleted, err := worker.cleanupOnce(context.Background())
	require.ErrorIs(t, err, errOpenAIOutboundSessionV1CleanupLockHeld)
	require.False(t, complete)
	require.Zero(t, deleted)
}

func TestOpenAIOutboundSessionV1CleanupDoesNotMarkWhenKeyAppearsDuringVerify(t *testing.T) {
	worker, _, rdb := newOpenAIV1CleanupTestWorker(t)
	worker.verifyWait = 20 * time.Millisecond
	ctx := context.Background()
	go func() {
		time.Sleep(5 * time.Millisecond)
		_ = rdb.Set(ctx, "openai-outbound-session:v1:late-writer", "legacy", 0).Err()
	}()
	complete, _, err := worker.cleanupOnce(ctx)
	require.ErrorIs(t, err, errOpenAIOutboundSessionV1KeysRemain)
	require.False(t, complete)
	exists, existsErr := rdb.Exists(ctx, openAIOutboundSessionV1CleanupMarker).Result()
	require.NoError(t, existsErr)
	require.Zero(t, exists)
}

func TestOpenAIOutboundSessionV1CleanupReopensMarkerForKeyWrittenAfterFinalScan(t *testing.T) {
	worker, mr, rdb := newOpenAIV1CleanupTestWorker(t)
	worker.verifyWait = 0
	ctx := context.Background()

	complete, deleted, err := worker.cleanupOnce(ctx)
	require.NoError(t, err)
	require.True(t, complete)
	require.Zero(t, deleted)
	require.True(t, mr.Exists(openAIOutboundSessionV1CleanupMarker))

	lateKey := "openai-outbound-session:v1:after-final-scan"
	require.NoError(t, rdb.Set(ctx, lateKey, "legacy", 0).Err())

	complete, deleted, err = worker.cleanupOnce(ctx)
	require.NoError(t, err)
	require.True(t, complete)
	require.EqualValues(t, 1, deleted)
	require.False(t, mr.Exists(lateKey))
	require.True(t, mr.Exists(openAIOutboundSessionV1CleanupMarker))
}

func TestOpenAIOutboundSessionV1CleanupMarkCompleteRequiresCurrentLockToken(t *testing.T) {
	worker, mr, rdb := newOpenAIV1CleanupTestWorker(t)
	ctx := context.Background()
	require.NoError(t, rdb.Set(ctx, openAIOutboundSessionV1CleanupLock, "other-instance", time.Minute).Err())

	err := worker.markComplete(ctx)
	require.ErrorIs(t, err, errOpenAIOutboundSessionV1CleanupLockLost)
	require.False(t, mr.Exists(openAIOutboundSessionV1CleanupMarker))
}

func TestOpenAIOutboundSessionV1CleanupStopCancelsVerificationWait(t *testing.T) {
	worker, _, _ := newOpenAIV1CleanupTestWorker(t)
	worker.verifyWait = time.Hour
	worker.Start()
	done := make(chan struct{})
	go func() { worker.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cleanup worker did not stop after cancellation")
	}
}
