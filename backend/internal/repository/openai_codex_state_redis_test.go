package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestCodexStateCollectorLeaseCrossInstanceOwnerFencing(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	first := NewOpenAICodexStateRepository(nil, rdb)
	second := NewOpenAICodexStateRepository(nil, rdb)
	ctx := context.Background()
	ok, err := first.AcquireCollector(ctx, 17, "first", 30*time.Second)
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = second.AcquireCollector(ctx, 17, "second", 30*time.Second)
	require.NoError(t, err)
	require.False(t, ok)
	// An expired lock owner cannot release its successor's lock.
	mr.FastForward(31 * time.Second)
	ok, err = second.AcquireCollector(ctx, 17, "second", 30*time.Second)
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, first.ReleaseCollector(ctx, 17, "first"))
	ok, err = first.AcquireCollector(ctx, 17, "third", 30*time.Second)
	require.NoError(t, err)
	require.False(t, ok)
	require.NoError(t, second.ReleaseCollector(ctx, 17, "second"))
	ok, err = first.AcquireCollector(ctx, 17, "third", 30*time.Second)
	require.NoError(t, err)
	require.True(t, ok)
	// Different credential owners do not block each other.
	ok, err = second.AcquireCollector(ctx, 18, "other", 30*time.Second)
	require.NoError(t, err)
	require.True(t, ok)
}

func TestCodexStateCancellationCrossInstanceAndContext(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	first := NewOpenAICodexStateRepository(nil, rdb)
	second := NewOpenAICodexStateRepository(nil, rdb)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	received := make(chan service.CodexTurnStateKey, 1)
	finished := make(chan error, 1)
	go func() {
		finished <- second.SubscribeCancels(ctx, func(key service.CodexTurnStateKey) { received <- key })
	}()
	require.Eventually(t, func() bool {
		result, err := rdb.PubSubNumSub(context.Background(), codexStateCancelChannel).Result()
		return err == nil && result[codexStateCancelChannel] == 1
	}, time.Second, time.Millisecond)
	// Invalid payloads are ignored and never invoke the handler.
	require.NoError(t, rdb.Publish(ctx, codexStateCancelChannel, `{"OwnerAccountID":17}`).Err())
	key := service.CodexTurnStateKey{OSFamily: "windows", OwnerAccountID: 17, Model: "gpt-5.4", Generation: "generation-2"}
	require.NoError(t, first.PublishCancel(ctx, key))
	select {
	case actual := <-received:
		require.Equal(t, key, actual)
	case <-time.After(time.Second):
		t.Fatal("cross-instance cancellation was not delivered")
	}
	key.CollectorAttemptID = "not-a-uuid"
	require.ErrorContains(t, first.PublishCancel(ctx, key), "collector attempt")
	require.NoError(t, rdb.Publish(ctx, codexStateCancelChannel,
		`{"OwnerAccountID":17,"Model":"gpt-5.4","Generation":"generation-2","CollectorAttemptID":"invalid"}`).Err())
	key.CollectorAttemptID = uuid.NewString()
	require.NoError(t, first.PublishCancel(ctx, key))
	select {
	case actual := <-received:
		require.Equal(t, key, actual, "late cancellation must identify only its exact attempt")
	case <-time.After(time.Second):
		t.Fatal("attempt-specific cancellation was not delivered")
	}
	cancel()
	select {
	case err := <-finished:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("idle subscription did not respect cancellation")
	}
}

func TestCodexStateRepositoryUnavailableAndInvalidInputs(t *testing.T) {
	r := &openAICodexStateRepository{}
	ctx := context.Background()
	_, err := r.Get(ctx, service.CodexTurnStateKey{})
	require.Error(t, err)
	_, err = r.AcquireCollector(ctx, 1, "owner", time.Second)
	require.Error(t, err)
	require.Error(t, validateCodexStateKey(service.CodexTurnStateKey{OSFamily: "windows", OwnerAccountID: 1, Model: "gpt-5.4"}))
	var nilRepository *openAICodexStateRepository
	require.Error(t, nilRepository.PublishCancel(ctx, service.CodexTurnStateKey{}))
}

func TestCodexStateActivationCrossInstanceScopeOnly(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	first := &openAICodexStateRepository{rdb: rdb}
	second := &openAICodexStateRepository{rdb: rdb}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	received := make(chan codexStateActivation, 1)
	finished := make(chan error, 1)
	go func() {
		finished <- second.SubscribeOSActivations(ctx, func(ownerID int64, osFamily, generation string) {
			received <- codexStateActivation{OwnerAccountID: ownerID, OSFamily: osFamily, Generation: generation}
		})
	}()
	require.Eventually(t, func() bool {
		result, err := rdb.PubSubNumSub(context.Background(), codexStateActivationChannel).Result()
		return err == nil && result[codexStateActivationChannel] == 1
	}, time.Second, time.Millisecond)
	require.NoError(t, rdb.Publish(ctx, codexStateActivationChannel, `{"owner_account_id":17}`).Err())
	require.Error(t, first.PublishOSActivation(ctx, 0, "windows", "generation"))
	require.NoError(t, first.PublishOSActivation(ctx, 17, "windows", "generation-2"))
	select {
	case actual := <-received:
		require.Equal(t, codexStateActivation{OwnerAccountID: 17, OSFamily: "windows", Generation: "generation-2"}, actual)
	case <-time.After(time.Second):
		t.Fatal("activation was not delivered")
	}
	cancel()
	select {
	case err := <-finished:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("activation subscription did not stop")
	}
}
