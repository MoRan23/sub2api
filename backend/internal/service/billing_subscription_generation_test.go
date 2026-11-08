package service

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type subscriptionGenerationCacheStub struct {
	billingCacheWorkerStub
	mu              sync.Mutex
	generation      int
	data            *SubscriptionCacheData
	invalidationErr error
	invalidations   int
	staleWrites     int
}

func (c *subscriptionGenerationCacheStub) GetSubscriptionCacheGeneration(context.Context, int64, int64) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return strconv.Itoa(c.generation), nil
}
func (c *subscriptionGenerationCacheStub) GetSubscriptionCache(context.Context, int64, int64) (*SubscriptionCacheData, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.data == nil {
		return nil, errors.New("cache miss")
	}
	copy := *c.data
	return &copy, nil
}
func (c *subscriptionGenerationCacheStub) SetSubscriptionCache(_ context.Context, _, _ int64, data *SubscriptionCacheData) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if data.Generation != strconv.Itoa(c.generation) {
		c.staleWrites++
		return ErrSubscriptionCacheStale
	}
	copy := *data
	c.data = &copy
	return nil
}
func (c *subscriptionGenerationCacheStub) InvalidateSubscriptionCache(context.Context, int64, int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.invalidations++
	if c.invalidationErr != nil {
		return c.invalidationErr
	}
	c.generation++
	c.data = nil
	return nil
}

type subscriptionGenerationRepoStub struct {
	userSubRepoNoop
	load func(context.Context) (*UserSubscription, error)
}

func (r *subscriptionGenerationRepoStub) GetActiveByUserIDAndGroupID(ctx context.Context, _, _ int64) (*UserSubscription, error) {
	return r.load(ctx)
}

func subscriptionGenerationSnapshot(remaining float64) *UserSubscription {
	return &UserSubscription{ID: 3, UserID: 1, GroupID: 2, Status: SubscriptionStatusActive, TotalRemainingUSD: remaining,
		Group: &Group{ID: 2, SubscriptionType: SubscriptionTypeTotalQuota, TotalQuotaNoExpiry: true}}
}

func publishSubscriptionGenerationTask(t *testing.T, svc *BillingCacheService) {
	t.Helper()
	select {
	case task := <-svc.cacheWriteChan:
		require.Equal(t, cacheWriteSetSubscription, task.kind)
		svc.setSubscriptionCache(context.Background(), task.userID, task.groupID, task.subscriptionData)
	default:
		t.Fatal("no queued subscription snapshot")
	}
}

func TestSubscriptionGenerationQueuedSnapshotCannotUndoGrant(t *testing.T) {
	ctx := context.Background()
	remaining := 0.0
	cache := &subscriptionGenerationCacheStub{}
	svc := &BillingCacheService{cache: cache, cacheWriteChan: make(chan cacheWriteTask, 4), subRepo: &subscriptionGenerationRepoStub{
		load: func(context.Context) (*UserSubscription, error) {
			return subscriptionGenerationSnapshot(remaining), nil
		},
	}}
	old, err := svc.GetSubscriptionStatus(ctx, 1, 2)
	require.NoError(t, err)
	require.Zero(t, old.TotalRemaining)
	remaining = 5
	require.NoError(t, svc.InvalidateSubscription(ctx, 1, 2))
	publishSubscriptionGenerationTask(t, svc)
	require.Nil(t, cache.data)
	require.Equal(t, 1, cache.staleWrites)
	fresh, err := svc.GetSubscriptionStatus(ctx, 1, 2)
	require.NoError(t, err)
	require.Equal(t, 5.0, fresh.TotalRemaining)
	publishSubscriptionGenerationTask(t, svc)
	require.NoError(t, svc.checkSubscriptionEligibility(ctx, 1, subscriptionGenerationSnapshot(5).Group, nil))
}

func TestSubscriptionGenerationRetriesDBReadAcrossInvalidation(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var reads atomic.Int32
	cache := &subscriptionGenerationCacheStub{}
	svc := &BillingCacheService{cache: cache, cacheWriteChan: make(chan cacheWriteTask, 4), subRepo: &subscriptionGenerationRepoStub{
		load: func(context.Context) (*UserSubscription, error) {
			if reads.Add(1) == 1 {
				close(started)
				<-release
				return subscriptionGenerationSnapshot(0), nil
			}
			return subscriptionGenerationSnapshot(5), nil
		},
	}}
	done := make(chan *subscriptionCacheData, 1)
	errResult := make(chan error, 1)
	go func() {
		data, err := svc.GetSubscriptionStatus(context.Background(), 1, 2)
		done <- data
		errResult <- err
	}()
	<-started
	require.NoError(t, svc.InvalidateSubscription(context.Background(), 1, 2))
	close(release)
	require.Equal(t, 5.0, (<-done).TotalRemaining)
	require.NoError(t, <-errResult)
	require.Equal(t, int32(2), reads.Load())
	require.Len(t, svc.cacheWriteChan, 1, "only the new generation can be queued")
}

func TestSubscriptionGenerationFailedInvalidationBypassesStaleCache(t *testing.T) {
	ctx := context.Background()
	redisErr := errors.New("redis unavailable")
	cache := &subscriptionGenerationCacheStub{data: &SubscriptionCacheData{Generation: "0", Status: SubscriptionStatusActive, TotalRemaining: 1}, invalidationErr: redisErr}
	svc := &BillingCacheService{cache: cache, cacheWriteChan: make(chan cacheWriteTask, 4), subRepo: &subscriptionGenerationRepoStub{
		load: func(context.Context) (*UserSubscription, error) { return subscriptionGenerationSnapshot(0), nil },
	}}
	require.ErrorIs(t, svc.InvalidateSubscription(ctx, 1, 2), redisErr)
	require.ErrorIs(t, svc.checkSubscriptionEligibility(ctx, 1, subscriptionGenerationSnapshot(0).Group, nil), ErrTotalLimitExceeded)
	require.Empty(t, svc.cacheWriteChan, "failed invalidation cannot publish into the old generation")
	svc.setSubscriptionCache(ctx, 1, 2, &subscriptionCacheData{Generation: "0", TotalRemaining: 100})
	require.Equal(t, 1.0, cache.data.TotalRemaining, "pending invalidation also fences previously queued writes locally")
	cache.invalidationErr = nil
	fresh, err := svc.GetSubscriptionStatus(ctx, 1, 2)
	require.NoError(t, err)
	require.Zero(t, fresh.TotalRemaining)
	_, dirty := svc.subscriptionDirty.Load(subscriptionCacheIdentity{1, 2})
	require.False(t, dirty)
	publishSubscriptionGenerationTask(t, svc)
	require.Zero(t, cache.data.TotalRemaining)
}

func TestSubscriptionGenerationBackgroundRetriesFailedInvalidation(t *testing.T) {
	cache := &subscriptionGenerationCacheStub{invalidationErr: errors.New("redis temporarily unavailable")}
	svc := NewBillingCacheService(cache, nil, nil, nil, nil, nil, &config.Config{}, nil)
	t.Cleanup(svc.Stop)
	require.Error(t, svc.InvalidateSubscription(context.Background(), 1, 2))
	cache.mu.Lock()
	cache.invalidationErr = nil
	cache.mu.Unlock()
	require.Eventually(t, func() bool { _, dirty := svc.subscriptionDirty.Load(subscriptionCacheIdentity{1, 2}); return !dirty }, 3*time.Second, 10*time.Millisecond)
}

type overlappingSubscriptionGenerationCache struct {
	*subscriptionGenerationCacheStub
	afterInvalidation func()
}

func (c *overlappingSubscriptionGenerationCache) InvalidateSubscriptionCache(context.Context, int64, int64) error {
	c.afterInvalidation()
	return nil
}

func TestSubscriptionGenerationNewDirtyMarkerStillBypassesCache(t *testing.T) {
	cache := &overlappingSubscriptionGenerationCache{subscriptionGenerationCacheStub: &subscriptionGenerationCacheStub{
		data: &SubscriptionCacheData{Generation: "0", Status: SubscriptionStatusActive, TotalRemaining: 1},
	}}
	svc := &BillingCacheService{cache: cache, cacheWriteChan: make(chan cacheWriteTask, 4), subRepo: &subscriptionGenerationRepoStub{
		load: func(context.Context) (*UserSubscription, error) { return subscriptionGenerationSnapshot(0), nil },
	}}
	id := subscriptionCacheIdentity{1, 2}
	svc.subscriptionDirty.Store(id, &subscriptionInvalidation{pending: true})
	newer := &subscriptionInvalidation{pending: true}
	cache.afterInvalidation = func() {
		// A succeeds, another reader repopulates remaining=1, then mutation B
		// commits remaining=0 and cannot invalidate Redis before A returns.
		svc.subscriptionDirty.Store(id, newer)
	}
	data, err := svc.GetSubscriptionStatus(context.Background(), 1, 2)
	require.NoError(t, err)
	require.Zero(t, data.TotalRemaining)
	marker, dirty := svc.subscriptionDirty.Load(id)
	require.True(t, dirty)
	require.Same(t, newer, marker)
	require.Empty(t, svc.cacheWriteChan)
}

func TestSubscriptionGenerationFinalizeInvalidatesTotalAndPreservesPeriodicUsage(t *testing.T) {
	for _, kind := range []string{SubscriptionTypeTotalQuota, SubscriptionTypeSubscription} {
		t.Run(kind, func(t *testing.T) {
			cache := &subscriptionGenerationCacheStub{}
			writes := make(chan cacheWriteTask, 4)
			group := &Group{ID: 2, SubscriptionType: kind, TotalQuotaNoExpiry: kind == SubscriptionTypeTotalQuota}
			svc := &BillingCacheService{cache: cache, cacheWriteChan: writes}
			finalizePostUsageBilling(context.Background(), &postUsageBillingParams{
				Cost: &CostBreakdown{ActualCost: 1}, User: &User{ID: 1}, Account: &Account{ID: 4},
				APIKey: &APIKey{ID: 5, GroupID: &group.ID, Group: group}, IsSubscriptionBill: true,
			}, &billingDeps{billingCacheService: svc, deferredService: &DeferredService{}}, &UsageBillingApplyResult{Applied: true})
			if kind == SubscriptionTypeTotalQuota {
				require.Equal(t, 1, cache.invalidations)
				require.Empty(t, writes)
			} else {
				require.Zero(t, cache.invalidations)
				require.Equal(t, cacheWriteUpdateSubscriptionUsage, (<-writes).kind)
			}
		})
	}
}
