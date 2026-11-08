package repository

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func subscriptionGenerationTestCache(t *testing.T, server *miniredis.Miniredis) *billingCache {
	t.Helper()
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return NewBillingCache(client).(*billingCache)
}

func TestSubscriptionGenerationRejectsCrossInstanceStaleSnapshot(t *testing.T) {
	for _, previousRemaining := range []float64{0, 4} {
		t.Run(map[bool]string{true: "exhausted", false: "positive"}[previousRemaining == 0], func(t *testing.T) {
			server := miniredis.RunT(t)
			reader := subscriptionGenerationTestCache(t, server)
			writer := subscriptionGenerationTestCache(t, server)
			ctx := context.Background()
			const userID, groupID = int64(12), int64(34)

			// The reader captured this token before its DB read. The queued result
			// may arrive after another instance has committed an additional grant.
			oldGeneration, err := reader.GetSubscriptionCacheGeneration(ctx, userID, groupID)
			require.NoError(t, err)
			require.NotEmpty(t, oldGeneration)
			stale := &service.SubscriptionCacheData{Generation: oldGeneration, Status: service.SubscriptionStatusActive, TotalRemaining: previousRemaining}
			require.NoError(t, writer.InvalidateSubscriptionCache(ctx, userID, groupID))
			newGeneration, err := writer.GetSubscriptionCacheGeneration(ctx, userID, groupID)
			require.NoError(t, err)
			require.NotEqual(t, oldGeneration, newGeneration)

			require.ErrorIs(t, reader.SetSubscriptionCache(ctx, userID, groupID, stale), service.ErrSubscriptionCacheStale)
			_, err = writer.GetSubscriptionCache(ctx, userID, groupID)
			require.ErrorIs(t, err, redis.Nil, "a delayed pre-grant result must not recreate the deleted entry")

			fresh := &service.SubscriptionCacheData{Generation: newGeneration, Status: service.SubscriptionStatusActive, TotalLimit: 15, TotalRemaining: 15}
			require.NoError(t, writer.SetSubscriptionCache(ctx, userID, groupID, fresh))
			require.ErrorIs(t, reader.SetSubscriptionCache(ctx, userID, groupID, stale), service.ErrSubscriptionCacheStale)
			got, err := reader.GetSubscriptionCache(ctx, userID, groupID)
			require.NoError(t, err)
			require.Equal(t, newGeneration, got.Generation)
			require.Equal(t, 15.0, got.TotalRemaining, "a late write must not replace the new grant's snapshot")
			require.Nil(t, got.ExpiresAt)
		})
	}
}

func TestSubscriptionGenerationSurvivesDataExpiry(t *testing.T) {
	server := miniredis.RunT(t)
	cache := subscriptionGenerationTestCache(t, server)
	ctx := context.Background()
	const userID, groupID = int64(12), int64(34)
	oldGeneration, err := cache.GetSubscriptionCacheGeneration(ctx, userID, groupID)
	require.NoError(t, err)
	require.NoError(t, cache.InvalidateSubscriptionCache(ctx, userID, groupID))
	generation, err := cache.GetSubscriptionCacheGeneration(ctx, userID, groupID)
	require.NoError(t, err)
	require.NotEqual(t, oldGeneration, generation)
	fresh := &service.SubscriptionCacheData{Generation: generation, Status: service.SubscriptionStatusActive, TotalRemaining: 5}
	require.NoError(t, cache.SetSubscriptionCache(ctx, userID, groupID, fresh))

	server.FastForward(24 * time.Hour)
	require.False(t, server.Exists(billingSubKey(userID, groupID)))
	retained, err := cache.GetSubscriptionCacheGeneration(ctx, userID, groupID)
	require.NoError(t, err)
	require.Equal(t, generation, retained)
	ttl, err := cache.rdb.TTL(ctx, billingSubGenerationKey(userID, groupID)).Result()
	require.NoError(t, err)
	require.Equal(t, time.Duration(-1), ttl, "generation tokens must not expire along with cached data")
	old := &service.SubscriptionCacheData{Generation: oldGeneration, Status: service.SubscriptionStatusActive, TotalRemaining: 1}
	require.ErrorIs(t, cache.SetSubscriptionCache(ctx, userID, groupID, old), service.ErrSubscriptionCacheStale)
	require.NoError(t, cache.SetSubscriptionCache(ctx, userID, groupID, fresh))
}

func TestSubscriptionGenerationLossDoesNotReviveOldToken(t *testing.T) {
	server := miniredis.RunT(t)
	cache := subscriptionGenerationTestCache(t, server)
	ctx := context.Background()
	const userID, groupID = int64(12), int64(34)
	oldGeneration, err := cache.GetSubscriptionCacheGeneration(ctx, userID, groupID)
	require.NoError(t, err)
	stale := &service.SubscriptionCacheData{Generation: oldGeneration, Status: service.SubscriptionStatusActive, TotalRemaining: 1}
	require.NoError(t, cache.SetSubscriptionCache(ctx, userID, groupID, stale))
	server.Del(billingSubGenerationKey(userID, groupID))

	_, err = cache.GetSubscriptionCache(ctx, userID, groupID)
	require.ErrorIs(t, err, redis.Nil)
	require.ErrorIs(t, cache.SetSubscriptionCache(ctx, userID, groupID, stale), service.ErrSubscriptionCacheStale)
	replacement, err := cache.GetSubscriptionCacheGeneration(ctx, userID, groupID)
	require.NoError(t, err)
	require.NotEqual(t, oldGeneration, replacement)
	require.ErrorIs(t, cache.SetSubscriptionCache(ctx, userID, groupID, stale), service.ErrSubscriptionCacheStale)
}

func TestSubscriptionGenerationRejectsMalformedCachedGeneration(t *testing.T) {
	for _, corruption := range []string{"missing snapshot generation", "different snapshot generation", "missing generation key"} {
		t.Run(corruption, func(t *testing.T) {
			server := miniredis.RunT(t)
			cache := subscriptionGenerationTestCache(t, server)
			ctx := context.Background()
			const userID, groupID = int64(12), int64(34)
			key := billingSubKey(userID, groupID)
			// Direct initialization remains supported for callers without a DB load.
			require.NoError(t, cache.SetSubscriptionCache(ctx, userID, groupID, &service.SubscriptionCacheData{Status: service.SubscriptionStatusActive, TotalRemaining: 3}))
			switch corruption {
			case "missing snapshot generation":
				require.NoError(t, cache.rdb.HDel(ctx, key, subFieldGeneration).Err())
			case "different snapshot generation":
				require.NoError(t, cache.rdb.HSet(ctx, key, subFieldGeneration, "superseded-token").Err())
			case "missing generation key":
				require.NoError(t, cache.rdb.Del(ctx, billingSubGenerationKey(userID, groupID)).Err())
			}
			_, err := cache.GetSubscriptionCache(ctx, userID, groupID)
			require.ErrorIs(t, err, redis.Nil)
		})
	}
}

func TestSubscriptionGenerationPreservesWindowUsageUpdates(t *testing.T) {
	server := miniredis.RunT(t)
	cache := subscriptionGenerationTestCache(t, server)
	ctx := context.Background()
	const userID, groupID = int64(12), int64(34)
	expires := time.Now().Add(time.Hour).Truncate(time.Second)
	initial := &service.SubscriptionCacheData{Status: service.SubscriptionStatusActive, ExpiresAt: &expires, DailyUsage: 1, WeeklyUsage: 2, MonthlyUsage: 3, TotalLimit: 8, TotalUsed: 2, TotalRemaining: 6}
	require.NoError(t, cache.SetSubscriptionCache(ctx, userID, groupID, initial))
	before, err := cache.GetSubscriptionCache(ctx, userID, groupID)
	require.NoError(t, err)
	server.FastForward(time.Minute)
	previousTTL := server.TTL(billingSubKey(userID, groupID))
	require.NoError(t, cache.UpdateSubscriptionUsage(ctx, userID, groupID, .5))
	got, err := cache.GetSubscriptionCache(ctx, userID, groupID)
	require.NoError(t, err)
	require.Equal(t, 1.5, got.DailyUsage)
	require.Equal(t, 2.5, got.WeeklyUsage)
	require.Equal(t, 3.5, got.MonthlyUsage)
	require.Equal(t, initial.TotalRemaining, got.TotalRemaining, "total quota settlement uses invalidation, not a window counter delta")
	require.Equal(t, initial.TotalUsed, got.TotalUsed)
	require.Equal(t, initial.TotalLimit, got.TotalLimit)
	require.Equal(t, before.Generation, got.Generation)
	require.True(t, expires.Equal(*got.ExpiresAt))
	require.Greater(t, server.TTL(billingSubKey(userID, groupID)), previousTTL)

	require.NoError(t, cache.UpdateSubscriptionUsage(ctx, 99, 99, .5))
	require.False(t, server.Exists(billingSubKey(99, 99)), "window increments must not create a partial cache entry")
}

func TestSubscriptionGenerationInvalidationPublishesAfterClearingData(t *testing.T) {
	server := miniredis.RunT(t)
	cache := subscriptionGenerationTestCache(t, server)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	const userID, groupID = int64(12), int64(34)
	require.NoError(t, cache.SetSubscriptionCache(ctx, userID, groupID, &service.SubscriptionCacheData{Status: service.SubscriptionStatusActive, TotalRemaining: 3}))
	before, err := cache.GetSubscriptionCacheGeneration(ctx, userID, groupID)
	require.NoError(t, err)
	subscriber := cache.rdb.Subscribe(ctx, subCacheInvalidateChannel)
	t.Cleanup(func() { _ = subscriber.Close() })
	_, err = subscriber.Receive(ctx)
	require.NoError(t, err)

	require.NoError(t, cache.InvalidateSubscriptionCache(ctx, userID, groupID))
	message, err := subscriber.ReceiveMessage(ctx)
	require.NoError(t, err)
	require.Equal(t, "sub:12:34", message.Payload)
	_, err = cache.GetSubscriptionCache(ctx, userID, groupID)
	require.ErrorIs(t, err, redis.Nil)
	after, err := cache.GetSubscriptionCacheGeneration(ctx, userID, groupID)
	require.NoError(t, err)
	require.NotEqual(t, before, after)
}

type subscriptionGenerationL1Repo struct {
	service.UserSubscriptionRepository
	remaining atomic.Int64
	reads     atomic.Int64
}

func (r *subscriptionGenerationL1Repo) GetActiveByUserIDAndGroupID(_ context.Context, userID, groupID int64) (*service.UserSubscription, error) {
	r.reads.Add(1)
	return &service.UserSubscription{
		ID:                1,
		UserID:            userID,
		GroupID:           groupID,
		Status:            service.SubscriptionStatusActive,
		TotalRemainingUSD: float64(r.remaining.Load()),
		Group: &service.Group{
			ID:                 groupID,
			SubscriptionType:   service.SubscriptionTypeTotalQuota,
			TotalQuotaNoExpiry: true,
		},
	}, nil
}

func TestSubscriptionGenerationInvalidationRefreshesBothInstancesL1(t *testing.T) {
	server := miniredis.RunT(t)
	firstCache := subscriptionGenerationTestCache(t, server)
	secondCache := subscriptionGenerationTestCache(t, server)
	repo := &subscriptionGenerationL1Repo{}
	repo.remaining.Store(1)
	cfg := &config.Config{SubscriptionCache: config.SubscriptionCacheConfig{L1Size: 128, L1TTLSeconds: 3600}}
	instances := make([]*service.SubscriptionService, 0, 2)
	for _, cache := range []*billingCache{firstCache, secondCache} {
		billing := service.NewBillingCacheService(cache, nil, repo, nil, nil, nil, cfg, nil)
		t.Cleanup(billing.Stop)
		subscriptions := service.NewSubscriptionService(nil, repo, billing, nil, cfg)
		t.Cleanup(subscriptions.Stop)
		instances = append(instances, subscriptions)
	}
	ctx := context.Background()
	const userID, groupID = int64(12), int64(34)

	for _, subscriptions := range instances {
		initial, err := subscriptions.GetActiveSubscription(ctx, userID, groupID)
		require.NoError(t, err)
		require.Equal(t, 1.0, initial.TotalRemainingUSD)
		// Ristretto inserts asynchronously. Confirm an actual cache hit before
		// changing the repository, rather than relying on a scheduling delay.
		require.Eventually(t, func() bool {
			before := repo.reads.Load()
			cached, err := subscriptions.GetActiveSubscription(ctx, userID, groupID)
			return err == nil && cached.TotalRemainingUSD == 1 && repo.reads.Load() == before
		}, 3*time.Second, 10*time.Millisecond)
	}
	warmReads := repo.reads.Load()
	repo.remaining.Store(0)
	for _, subscriptions := range instances {
		cached, err := subscriptions.GetActiveSubscription(ctx, userID, groupID)
		require.NoError(t, err)
		require.Equal(t, 1.0, cached.TotalRemainingUSD)
	}
	require.Equal(t, warmReads, repo.reads.Load(), "both instances must retain a warm L1 before the broadcast")

	require.NoError(t, firstCache.InvalidateSubscriptionCache(ctx, userID, groupID))
	for _, subscriptions := range instances {
		require.Eventually(t, func() bool {
			fresh, err := subscriptions.GetActiveSubscription(ctx, userID, groupID)
			if err != nil || fresh.TotalRemainingUSD != 0 {
				return false
			}
			_, err = subscriptions.ValidateAndCheckLimits(fresh, fresh.Group)
			return errors.Is(err, service.ErrTotalLimitExceeded)
		}, 3*time.Second, 10*time.Millisecond, "a Redis invalidation must clear the local and remote L1 without waiting for its one-hour TTL")
	}
	require.GreaterOrEqual(t, repo.reads.Load(), warmReads+2)
}
