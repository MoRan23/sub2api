//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// Use dedicated plugin IDs instead of testRedis's command-prefix hook: List
// deliberately returns Redis keys and removes its own namespace prefix.
func TestMergePluginKVRealRedisTTLIsolationAndReconstruction(t *testing.T) {
	ctx := context.Background()
	opts := *integrationRedis.Options()
	first := redis.NewClient(&opts)
	second := redis.NewClient(&opts)
	t.Cleanup(func() { _ = first.Close(); _ = second.Close() })
	store := NewPluginKVStore(first)
	reconnected := NewPluginKVStore(second)
	plugin := "merge-kv-" + uuid.NewString()
	otherPlugin := plugin + "-other"
	keys := []struct{ plugin, namespace, key string }{
		{plugin, "state", "shared"},
		{otherPlugin, "state", "shared"},
		{plugin, "state2", "shared"},
		{plugin, "state", "temporary"},
		{plugin, "state", "persistent"},
		{plugin, "state", "empty"},
	}
	t.Cleanup(func() {
		for _, key := range keys {
			require.NoError(t, reconnected.Delete(context.Background(), key.plugin, key.namespace, key.key))
		}
	})

	require.NoError(t, store.Set(ctx, plugin, "state", "shared", []byte("first-plugin"), 0))
	require.NoError(t, store.Set(ctx, otherPlugin, "state", "shared", []byte("second-plugin"), 0))
	require.NoError(t, store.Set(ctx, plugin, "state2", "shared", []byte("second-namespace"), 0))
	for _, tc := range []struct{ plugin, namespace, want string }{
		{plugin, "state", "first-plugin"},
		{otherPlugin, "state", "second-plugin"},
		{plugin, "state2", "second-namespace"},
	} {
		value, found, err := reconnected.Get(ctx, tc.plugin, tc.namespace, "shared")
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, tc.want, string(value))
		listed, err := reconnected.List(ctx, tc.plugin, tc.namespace, "shared", 100)
		require.NoError(t, err)
		require.Equal(t, []string{"shared"}, listed)
	}

	require.NoError(t, store.Set(ctx, plugin, "state", "temporary", []byte("expires"), time.Second))
	require.NoError(t, store.Set(ctx, plugin, "state", "persistent", []byte("old"), time.Second))
	require.NoError(t, store.Set(ctx, plugin, "state", "persistent", []byte("retained"), 0))
	require.NoError(t, store.Set(ctx, plugin, "state", "empty", []byte{}, 0))
	value, found, err := reconnected.Get(ctx, plugin, "state", "temporary")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, []byte("expires"), value)

	// Closing the publishing client must not remove server-owned plugin state.
	require.NoError(t, first.Close())
	require.Eventually(t, func() bool {
		_, found, err := reconnected.Get(ctx, plugin, "state", "temporary")
		return err == nil && !found
	}, 3*time.Second, 25*time.Millisecond)
	value, found, err = reconnected.Get(ctx, plugin, "state", "persistent")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, []byte("retained"), value)
	remaining, err := second.PTTL(ctx, pluginKVKeyPrefix+plugin+":state:persistent").Result()
	require.NoError(t, err)
	require.Equal(t, time.Duration(-1), remaining, "TTL=0 overwrite must remove the old deadline")
	value, found, err = reconnected.Get(ctx, plugin, "state", "empty")
	require.NoError(t, err)
	require.True(t, found, "empty values remain distinguishable from absent keys")
	require.Empty(t, value)

	listed, err := reconnected.List(ctx, plugin, "state", "", 100)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"shared", "persistent", "empty"}, listed)
	limited, err := reconnected.List(ctx, plugin, "state", "", 1)
	require.NoError(t, err)
	require.Len(t, limited, 1)
	require.Contains(t, listed, limited[0])
	_, found, err = reconnected.Get(ctx, plugin+"-absent", "state", "shared")
	require.NoError(t, err)
	require.False(t, found)
}
