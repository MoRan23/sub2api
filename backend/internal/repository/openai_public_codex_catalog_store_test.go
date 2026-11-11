package repository

import (
	"context"
	"encoding/json"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestPublicCodexCatalogRedisPersistenceAndMonotonicWrite(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	store := NewPublicCodexCatalogStore(client)
	ctx := context.Background()
	empty, err := store.Load(ctx)
	require.NoError(t, err)
	require.Nil(t, empty)
	now := time.Now().UTC().Truncate(time.Millisecond)
	newer := &service.PublicCodexCatalogSnapshot{Body: []byte("{\n  \"models\": [{\"slug\":\"new\",\"unknown\":\"<keep>\"}]\n}"), ETag: `"new"`, CheckedAt: now}
	require.NoError(t, store.Save(ctx, newer))
	older := &service.PublicCodexCatalogSnapshot{Body: []byte(`{"models":[{"slug":"old"}]}`), CheckedAt: now.Add(-time.Second)}
	require.NoError(t, store.Save(ctx, older))
	restored, err := NewPublicCodexCatalogStore(client).Load(ctx)
	require.NoError(t, err)
	require.Equal(t, newer, restored)
	require.Zero(t, server.TTL(publicCodexCatalogRedisKey))
	require.Len(t, server.Keys(), 1)
	require.NoError(t, client.HSet(ctx, publicCodexCatalogRedisKey, "snapshot", "broken").Err())
	_, err = store.Load(ctx)
	require.Error(t, err)
}

func TestPublicCodexCatalogRedisRepairsBadIndexWithoutRollingBack(t *testing.T) {
	for _, index := range []string{"not-a-number", strconv.FormatInt(time.Now().Add(365*24*time.Hour).UnixMilli(), 10)} {
		t.Run(index, func(t *testing.T) {
			server := miniredis.RunT(t)
			client := redis.NewClient(&redis.Options{Addr: server.Addr()})
			defer client.Close()
			store := NewPublicCodexCatalogStore(client)
			ctx := context.Background()
			now := time.Now().UTC()
			latest := &service.PublicCodexCatalogSnapshot{Body: []byte(`{"models":[{"slug":"latest"}]}`), CheckedAt: now}
			require.NoError(t, store.Save(ctx, latest))
			require.NoError(t, client.HSet(ctx, publicCodexCatalogRedisKey, "checked_at_ms", index).Err())
			// Same-millisecond late results must not overwrite newer observations.
			late := &service.PublicCodexCatalogSnapshot{Body: []byte(`{"models":[{"slug":"late"}]}`), CheckedAt: now.Add(-time.Nanosecond)}
			require.NoError(t, store.Save(ctx, late))
			got, err := store.Load(ctx)
			require.NoError(t, err)
			require.Equal(t, latest, got)
			require.Equal(t, strconv.FormatInt(now.UnixMilli(), 10), client.HGet(ctx, publicCodexCatalogRedisKey, "checked_at_ms").Val())
		})
	}
}

func TestPublicCodexCatalogRedisHealsInvalidSnapshot(t *testing.T) {
	now := time.Now().UTC()
	future, _ := json.Marshal(&service.PublicCodexCatalogSnapshot{Body: []byte(`{"models":[{"slug":"future"}]}`), CheckedAt: now.Add(24 * time.Hour)})
	invalidBody, _ := json.Marshal(&service.PublicCodexCatalogSnapshot{Body: []byte(`{"models":[null]}`), CheckedAt: now.Add(time.Second)})
	for _, data := range []string{"broken", string(future), string(invalidBody)} {
		server := miniredis.RunT(t)
		client := redis.NewClient(&redis.Options{Addr: server.Addr()})
		store := NewPublicCodexCatalogStore(client)
		ctx := context.Background()
		require.NoError(t, client.HSet(ctx, publicCodexCatalogRedisKey, "checked_at_ms", now.Add(24*time.Hour).UnixMilli(), "snapshot", data).Err())
		valid := &service.PublicCodexCatalogSnapshot{Body: []byte(`{"models":[{"slug":"valid"}]}`), CheckedAt: now}
		require.NoError(t, store.Save(ctx, valid))
		got, err := store.Load(ctx)
		require.NoError(t, err)
		require.Equal(t, valid, got)
		require.NoError(t, client.Close())
	}
}

func TestPublicCodexCatalogRedisConcurrentSavesKeepNewest(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	defer client.Close()
	store := NewPublicCodexCatalogStore(client)
	ctx := context.Background()
	now := time.Now().UTC()
	var wg sync.WaitGroup
	for i := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			require.NoError(t, store.Save(ctx, &service.PublicCodexCatalogSnapshot{Body: []byte(`{"models":[{"slug":"valid"}]}`), CheckedAt: now.Add(time.Duration(i) * time.Nanosecond)}))
		}()
	}
	wg.Wait()
	got, err := store.Load(ctx)
	require.NoError(t, err)
	require.Equal(t, now.Add(3*time.Nanosecond), got.CheckedAt)
}
