package service

import (
	"context"
	"math"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dgraph-io/ristretto"
	"github.com/stretchr/testify/require"
)

func TestAssignTotalQuotaPermanentAddsIndependentGrants(t *testing.T) {
	legacyGroupAmount := 999.0
	group := &Group{ID: 1, SubscriptionType: SubscriptionTypeTotalQuota, TotalQuotaNoExpiry: true, TotalLimitUSD: &legacyGroupAmount}
	repo := newSubscriptionUserSubRepoStub()
	svc := NewSubscriptionService(&subscriptionGroupRepoStub{group: group}, repo, nil, nil, nil)
	firstAmount := 12.12345678
	first, err := svc.AssignSubscription(context.Background(), &AssignSubscriptionInput{UserID: 20, GroupID: 1, QuotaAmount: &firstAmount})
	require.NoError(t, err)
	require.Nil(t, first.ExpiresAt)
	require.Nil(t, first.QuotaEvents[0].ExpiresAt)
	require.InDelta(t, firstAmount, first.TotalRemainingUSD, 1e-9)
	repo.quotaEvents[first.ID][0].QuotaUsedUSD = 2
	secondAmount := 5.0
	second, err := svc.AssignSubscription(context.Background(), &AssignSubscriptionInput{UserID: 20, GroupID: 1, QuotaAmount: &secondAmount})
	require.NoError(t, err)
	require.Equal(t, first.ID, second.ID)
	require.Len(t, second.QuotaEvents, 2)
	require.Equal(t, 2.0, second.QuotaEvents[0].QuotaUsedUSD)
	require.InDelta(t, firstAmount+secondAmount-2, second.TotalRemainingUSD, 1e-9)
	require.Nil(t, second.ExpiresAt)
	require.Equal(t, -1, second.DaysRemaining())
	require.True(t, second.IsActive())
	require.False(t, second.IsExpired())
}

func TestAssignTotalQuotaRequiresExplicitAmountAndFiniteDays(t *testing.T) {
	legacyGroupAmount := 100.0
	for _, test := range []struct {
		name   string
		amount *float64
		days   int
	}{
		{name: "missing", days: 30},
		{name: "zero", amount: new(float64), days: 30},
		{name: "negative", amount: quotaGrantAmount(-1), days: 30},
		{name: "nonfinite", amount: quotaGrantAmount(math.Inf(1)), days: 30},
		{name: "missing days", amount: quotaGrantAmount(3)},
		{name: "too many days", amount: quotaGrantAmount(3), days: MaxValidityDays + 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := newSubscriptionUserSubRepoStub()
			svc := NewSubscriptionService(&subscriptionGroupRepoStub{group: &Group{ID: 1, SubscriptionType: SubscriptionTypeTotalQuota, TotalLimitUSD: &legacyGroupAmount}}, repo, nil, nil, nil)
			_, err := svc.AssignSubscription(context.Background(), &AssignSubscriptionInput{UserID: 1, GroupID: 1, QuotaAmount: test.amount, ValidityDays: test.days})
			require.Error(t, err)
			require.Zero(t, repo.createCalls)
			require.Empty(t, repo.quotaEvents)
		})
	}
}

func TestAssignTotalQuotaFiniteAmountAndExpiryComeFromGrant(t *testing.T) {
	now := time.Now()
	repo := newSubscriptionUserSubRepoStub()
	svc := NewSubscriptionService(&subscriptionGroupRepoStub{group: &Group{ID: 1, SubscriptionType: SubscriptionTypeTotalQuota}}, repo, nil, nil, nil)
	svc.now = func() time.Time { return now }
	sub, err := svc.AssignSubscription(context.Background(), &AssignSubscriptionInput{UserID: 1, GroupID: 1, QuotaAmount: quotaGrantAmount(25), ValidityDays: 3})
	require.NoError(t, err)
	require.Equal(t, now.AddDate(0, 0, 3), *sub.ExpiresAt)
	require.Equal(t, 25.0, sub.TotalRemainingUSD)
	require.Len(t, sub.QuotaEvents, 1)
}

func TestPermanentQuotaEligibilityUsesRemainingAmount(t *testing.T) {
	group := &Group{ID: 1, SubscriptionType: SubscriptionTypeTotalQuota, TotalQuotaNoExpiry: true}
	svc := NewSubscriptionService(nil, nil, nil, nil, nil)
	sub := &UserSubscription{Status: SubscriptionStatusActive, TotalRemainingUSD: 1}
	maintenance, err := svc.ValidateAndCheckLimits(sub, group)
	require.NoError(t, err)
	require.False(t, maintenance)
	require.True(t, sub.CheckTotalLimit(group, 1))
	require.False(t, sub.CheckTotalLimit(group, 2))
	sub.TotalRemainingUSD = 0
	_, err = svc.ValidateAndCheckLimits(sub, group)
	require.ErrorIs(t, err, ErrTotalLimitExceeded)
	require.False(t, sub.CheckTotalLimit(group, 0))
}

type quotaSubscriptionCacheStub struct {
	billingCacheWorkerStub
	data SubscriptionCacheData
}

func (c *quotaSubscriptionCacheStub) GetSubscriptionCache(context.Context, int64, int64) (*SubscriptionCacheData, error) {
	copy := c.data
	return &copy, nil
}

func TestBillingCachePermanentQuotaAndInvalidMissingExpiry(t *testing.T) {
	cache := &quotaSubscriptionCacheStub{data: SubscriptionCacheData{Status: SubscriptionStatusActive, TotalRemaining: 10}}
	svc := &BillingCacheService{cache: cache}
	group := &Group{ID: 1, SubscriptionType: SubscriptionTypeTotalQuota, TotalQuotaNoExpiry: true}
	require.NoError(t, svc.checkSubscriptionEligibility(context.Background(), 1, group, nil))
	cache.data.TotalRemaining = 0
	require.ErrorIs(t, svc.checkSubscriptionEligibility(context.Background(), 1, group, nil), ErrTotalLimitExceeded)
	group.SubscriptionType = SubscriptionTypeSubscription
	require.ErrorIs(t, svc.checkSubscriptionEligibility(context.Background(), 1, group, nil), ErrSubscriptionInvalid)
}

func quotaGrantAmount(v float64) *float64 { return &v }

type delayedSubscriptionLookup struct {
	userSubRepoNoop
	started chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (r *delayedSubscriptionLookup) GetActiveByUserIDAndGroupID(context.Context, int64, int64) (*UserSubscription, error) {
	if r.calls.Add(1) == 1 {
		close(r.started)
		<-r.release
		return &UserSubscription{Status: SubscriptionStatusActive, ExpiresAt: ptrTime(time.Now().Add(time.Hour)), TotalRemainingUSD: 1}, nil
	}
	return &UserSubscription{Status: SubscriptionStatusActive, ExpiresAt: ptrTime(time.Now().Add(time.Hour)), TotalRemainingUSD: 11}, nil
}

func TestSubscriptionCacheInvalidationDiscardsLateQuotaSnapshot(t *testing.T) {
	cache, err := ristretto.NewCache(&ristretto.Config{NumCounters: 100, MaxCost: 1 << 20, BufferItems: 64})
	require.NoError(t, err)
	t.Cleanup(cache.Close)
	repo := &delayedSubscriptionLookup{started: make(chan struct{}), release: make(chan struct{})}
	svc := &SubscriptionService{userSubRepo: repo, subCacheL1: cache, subCacheTTL: time.Minute}
	oldDone := make(chan error, 1)
	go func() { _, err := svc.GetActiveSubscription(context.Background(), 1, 2); oldDone <- err }()
	<-repo.started
	svc.InvalidateSubCacheSync(1, 2)
	fresh, err := svc.GetActiveSubscription(context.Background(), 1, 2)
	require.NoError(t, err)
	require.Equal(t, 11.0, fresh.TotalRemainingUSD)
	cache.Wait()
	close(repo.release)
	require.NoError(t, <-oldDone)
	cache.Wait()
	final, err := svc.GetActiveSubscription(context.Background(), 1, 2)
	require.NoError(t, err)
	require.Equal(t, 11.0, final.TotalRemainingUSD)
	require.Equal(t, int32(2), repo.calls.Load())
	*final.ExpiresAt = time.Now().Add(-time.Hour)
	unchanged, err := svc.GetActiveSubscription(context.Background(), 1, 2)
	require.NoError(t, err)
	require.True(t, unchanged.ExpiresAt.After(time.Now()))
}
