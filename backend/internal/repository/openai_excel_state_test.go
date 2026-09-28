package repository

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestOpenAIExcelRedisEncryptedCrossInstanceState(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	repo := &gatewayCache{rdb: client}
	cipher, err := NewAESEncryptor(&config.Config{Totp: config.TotpConfig{EncryptionKey: strings.Repeat("42", 32)}})
	require.NoError(t, err)
	first := service.NewOpenAIExcelStateStore(repo, cipher)
	second := service.NewOpenAIExcelStateStore(repo, cipher)
	ctx := context.Background()
	require.NoError(t, first.StoreExcelNativeCall(ctx, "account:1:tenant:2:grant:3:route:4", "call-1", json.RawMessage(`{"type":"function_call","id":"fc_1","call_id":"call-1","name":"run_officejs","arguments":"secret command"}`)))
	value, err := second.LoadExcelNativeCall(ctx, "account:1:tenant:2:grant:3:route:4", "call-1")
	require.NoError(t, err)
	require.Contains(t, string(value), "secret command")
	for _, key := range server.Keys() {
		if strings.HasSuffix(key, ":index") {
			continue
		}
		sealed, err := server.Get(key)
		require.NoError(t, err)
		require.NotContains(t, sealed, "secret command")
		require.NotContains(t, sealed, "run_officejs")
	}
	server.FastForward(7*24*time.Hour + time.Second)
	_, err = second.LoadExcelNativeCall(ctx, "account:1:tenant:2:grant:3:route:4", "call-1")
	require.ErrorIs(t, err, service.ErrOpenAIExcelHistoryNotFound)
}

func TestOpenAIExcelRedisAtomicBoundAndExpiry(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	repo := &gatewayCache{rdb: client}
	scope := strings.Repeat("a", 64)
	ctx := context.Background()
	var workers sync.WaitGroup
	errors := make(chan error, 30)
	for i := 0; i < 30; i++ {
		workers.Add(1)
		go func(i int) {
			defer workers.Done()
			key := strings.Repeat(string(rune('b'+i)), 64)
			errors <- repo.PutOpenAIExcelState(ctx, scope, key, "sealed", time.Minute, 5)
		}(i)
	}
	workers.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	require.Len(t, server.Keys(), 6)
	server.FastForward(2 * time.Minute)
	require.Empty(t, server.Keys())
}

func TestOpenAIExcelRedisNewEntrySurvivesEqualExpiryEviction(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	repo := &gatewayCache{rdb: client}
	ctx := context.Background()
	scope := strings.Repeat("f", 64)
	// The newest key sorts first when expiry scores tie. It must survive a
	// successful write so the immediately following tool result can restore it.
	for _, key := range []string{"c", "b", "a"} {
		require.NoError(t, repo.PutOpenAIExcelState(ctx, scope, strings.Repeat(key, 64), key, time.Minute, 2))
		value, err := repo.GetOpenAIExcelState(ctx, scope, strings.Repeat(key, 64))
		require.NoError(t, err)
		require.Equal(t, key, value)
	}
	require.Len(t, server.Keys(), 3)
}
