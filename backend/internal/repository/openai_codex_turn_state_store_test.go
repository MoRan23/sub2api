package repository

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestGatewayCacheOpenAICodexTurnStateOriginCrossInstanceAndTTL(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	first, ok := NewGatewayCache(rdb).(service.OpenAICodexTurnStateOriginStore)
	require.True(t, ok)
	second, ok := NewGatewayCache(rdb).(service.OpenAICodexTurnStateOriginStore)
	require.True(t, ok)

	mappingKey := strings.Repeat("a", 64)
	origin := service.OpenAICodexTurnStateOrigin{
		CredentialOwnerNamespace: "account:42",
		TurnIdentityDigest:       strings.Repeat("b", 64),
		ExpiresAt:                time.Now().Add(90 * time.Second).UTC().Truncate(time.Nanosecond),
	}
	require.NoError(t, first.SetOpenAICodexTurnStateOrigin(context.Background(), mappingKey, origin, 90*time.Second))
	redisKey, err := openAICodexTurnStateOriginRedisKey(mappingKey)
	require.NoError(t, err)
	require.Equal(t, 90*time.Second, mr.TTL(redisKey))

	loaded, err := second.GetOpenAICodexTurnStateOrigin(context.Background(), mappingKey)
	require.NoError(t, err)
	require.Equal(t, origin.CredentialOwnerNamespace, loaded.CredentialOwnerNamespace)
	require.Equal(t, origin.TurnIdentityDigest, loaded.TurnIdentityDigest)
	require.WithinDuration(t, origin.ExpiresAt, loaded.ExpiresAt, time.Nanosecond)

	mr.FastForward(91 * time.Second)
	_, err = second.GetOpenAICodexTurnStateOrigin(context.Background(), mappingKey)
	require.ErrorIs(t, err, service.ErrOpenAICodexTurnStateOriginNotFound)
}

func TestGatewayCacheOpenAICodexTurnStateOriginDeleteAndValidation(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	store := NewGatewayCache(rdb).(service.OpenAICodexTurnStateOriginStore)
	mappingKey := strings.Repeat("c", 64)
	origin := service.OpenAICodexTurnStateOrigin{CredentialOwnerNamespace: "owner", TurnIdentityDigest: strings.Repeat("d", 64)}

	require.Error(t, store.SetOpenAICodexTurnStateOrigin(context.Background(), "raw-state", origin, time.Minute))
	require.Error(t, store.SetOpenAICodexTurnStateOrigin(context.Background(), mappingKey, service.OpenAICodexTurnStateOrigin{}, time.Minute))
	require.Error(t, store.SetOpenAICodexTurnStateOrigin(context.Background(), mappingKey, origin, 0))
	require.NoError(t, store.SetOpenAICodexTurnStateOrigin(context.Background(), mappingKey, origin, time.Minute))
	require.NoError(t, store.DeleteOpenAICodexTurnStateOrigin(context.Background(), mappingKey))
	_, err := store.GetOpenAICodexTurnStateOrigin(context.Background(), mappingKey)
	require.True(t, errors.Is(err, service.ErrOpenAICodexTurnStateOriginNotFound))
}
