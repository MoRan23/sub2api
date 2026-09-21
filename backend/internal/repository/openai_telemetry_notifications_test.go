package repository

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestCodexTelemetryNotificationsAreLossyWakeups(t *testing.T) {
	server := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: server.Addr(), MaxRetries: -1})
	t.Cleanup(func() { _ = rdb.Close() })
	notifier := NewCodexTelemetryNotifier(rdb)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	received := make(chan struct{}, 8)
	finished := make(chan error, 1)
	go func() {
		finished <- notifier.Subscribe(ctx, func() { received <- struct{}{} })
	}()
	require.Eventually(t, func() bool {
		return server.PubSubNumSub(codexTelemetryWakeChannel)[codexTelemetryWakeChannel] == 1
	}, time.Second, time.Millisecond)

	// A crafted payload cannot become policy, routing data, or an observation.
	require.NoError(t, rdb.Publish(ctx, codexTelemetryWakeChannel, `{"enabled":false}`).Err())
	require.NoError(t, notifier.Notify(ctx))
	select {
	case <-received:
	case <-time.After(time.Second):
		t.Fatal("wake notification was not received")
	}
	cancel()
	select {
	case err := <-finished:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("idle subscription did not stop")
	}
	require.Empty(t, received)
	require.Empty(t, server.Keys(), "wakeups must not create authoritative Redis state")
	require.Eventually(t, func() bool {
		return server.PubSubNumSub(codexTelemetryWakeChannel)[codexTelemetryWakeChannel] == 0
	}, time.Second, time.Millisecond)
}

func TestCodexTelemetryNotificationsUnavailableAndCancelled(t *testing.T) {
	notifier := NewCodexTelemetryNotifier(nil)
	require.Error(t, notifier.Notify(context.Background()))
	require.Error(t, notifier.Subscribe(context.Background(), func() {}))
	server := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: server.Addr(), MaxRetries: -1})
	t.Cleanup(func() { _ = rdb.Close() })
	notifier = NewCodexTelemetryNotifier(rdb)
	require.Error(t, notifier.Subscribe(context.Background(), nil))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, notifier.Subscribe(ctx, func() {}), context.Canceled)
}
