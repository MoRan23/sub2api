//go:build unit

package repository

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSchedulerCandidatePoolShares2000AccountsAcross100Groups(t *testing.T) {
	cache := newSchedulerCacheUnit(t)
	fixed := time.Now()
	cache.now = func() time.Time { return fixed }
	ctx := context.Background()
	accounts := make([]service.Account, 2000)
	for i := range accounts {
		accounts[i] = memoTestAccount(int64(i+1), fmt.Sprintf("account-%d", i))
	}
	firstBucket := memoTestBucket(1)
	token, err := cache.CaptureBucketWriteToken(ctx, firstBucket)
	require.NoError(t, err)
	ids, err := cache.SetSnapshotAndReturnAccountIDs(ctx, firstBucket, token, accounts)
	require.NoError(t, err)
	var first service.CandidateSet
	for group := int64(1); group <= 100; group++ {
		bucket := memoTestBucket(group)
		if group > 1 {
			token, err := cache.CaptureBucketWriteToken(ctx, bucket)
			require.NoError(t, err)
			require.NoError(t, cache.SetSnapshotByAccountIDs(ctx, bucket, token, ids))
		}
		views, hit, err := cache.GetCandidateSnapshot(ctx, bucket)
		require.NoError(t, err)
		require.True(t, hit)
		require.Len(t, views, 2000)
		if first == nil {
			first = views
		} else {
			for i := range views {
				require.Same(t, first[i].Ref(), views[i].Ref())
			}
		}
	}
	require.Len(t, cache.accountEntries, 2000)
	require.Len(t, cache.snapshotEntries, 100)
	require.NotContains(t, first[0].Credentials, "access_token")
	require.LessOrEqual(t, cache.accountBytes+cache.bucketBytes, cache.cacheMaxBytes)
}

func TestSchedulerCandidatePoolBudgetEvictionKeepsInFlightViews(t *testing.T) {
	cache := newSchedulerCacheUnit(t)
	cache.cacheMaxBytes = 4096
	now := time.Now()
	cache.now = func() time.Time { return now }
	var first service.CandidateSet
	for id := int64(1); id <= 10; id++ {
		now = now.Add(time.Millisecond)
		bucket := memoTestBucket(id)
		memoPublish(t, cache, bucket, memoTestAccount(id, fmt.Sprintf("account-%d", id)))
		views, hit, err := cache.GetCandidateSnapshot(context.Background(), bucket)
		require.NoError(t, err)
		require.True(t, hit)
		require.Equal(t, id, views[0].ID)
		require.LessOrEqual(t, cache.accountBytes+cache.bucketBytes, cache.cacheMaxBytes)
		if first == nil {
			first = views
		}
	}
	require.Positive(t, cache.poolEvictions.Load()+cache.bucketEvictions.Load())
	require.Equal(t, "account-1", first[0].Name)
	require.Less(t, len(cache.accountEntries), 10)
}

func TestSchedulerCandidatePoolMetadataVersionsAndHotSidecar(t *testing.T) {
	cache := newSchedulerCacheUnit(t)
	ctx := context.Background()
	account := memoTestAccount(1, "original")
	bucketA, bucketB := memoTestBucket(1), memoTestBucket(2)
	memoPublish(t, cache, bucketA, account)
	memoPublish(t, cache, bucketB, account)
	first, _, err := cache.GetCandidateSnapshot(ctx, bucketA)
	require.NoError(t, err)
	second, _, err := cache.GetCandidateSnapshot(ctx, bucketB)
	require.NoError(t, err)
	require.Same(t, first[0].Ref(), second[0].Ref())
	usedAt := time.Now().UTC().Truncate(time.Millisecond)
	require.NoError(t, cache.UpdateLastUsed(ctx, map[int64]time.Time{1: usedAt}))
	updated, _, err := cache.GetCandidateSnapshot(ctx, bucketA)
	require.NoError(t, err)
	require.Same(t, first[0].Ref(), updated[0].Ref())
	require.Nil(t, first[0].LastUsedAt)
	require.Nil(t, updated[0].Ref().LastUsedAt)
	require.Equal(t, usedAt, *updated[0].LastUsedAt)
	updated[0].MutableAccount().Name = "request mutation"
	require.Equal(t, "original", first[0].Name)
	account.Name = "new version"
	require.NoError(t, cache.SetAccount(ctx, &account))
	newA, _, err := cache.GetCandidateSnapshot(ctx, bucketA)
	require.NoError(t, err)
	newB, _, err := cache.GetCandidateSnapshot(ctx, bucketB)
	require.NoError(t, err)
	require.NotSame(t, first[0].Ref(), newA[0].Ref())
	require.Same(t, newA[0].Ref(), newB[0].Ref())
	require.Equal(t, "original", first[0].Name)
	require.Equal(t, "new version", newB[0].Name)
}

func TestSchedulerCandidatePoolBudgetBypassAndIdleEviction(t *testing.T) {
	cache := newSchedulerCacheUnit(t)
	fixed := time.Now()
	cache.now = func() time.Time { return fixed }
	ctx := context.Background()
	bucket := memoTestBucket(1)
	memoPublish(t, cache, bucket, memoTestAccount(1, "one"))
	cache.cacheMaxBytes = 128
	views, hit, err := cache.GetCandidateSnapshot(ctx, bucket)
	require.NoError(t, err)
	require.True(t, hit)
	require.Equal(t, "one", views[0].Name)
	require.Empty(t, cache.accountEntries)
	require.Empty(t, cache.snapshotEntries)
	cache.cacheMaxBytes = defaultSchedulerCacheMaxBytes
	_, hit, err = cache.GetCandidateSnapshot(ctx, bucket)
	require.NoError(t, err)
	require.True(t, hit)
	require.Len(t, cache.accountEntries, 1)
	cache.snapshotMu.Lock()
	cache.evictSchedulerCacheLocked(fixed.Add(schedulerSnapshotCacheEvictAge+time.Second), 0)
	cache.snapshotMu.Unlock()
	require.Empty(t, cache.accountEntries)
	require.Empty(t, cache.snapshotEntries)
	require.Zero(t, cache.accountBytes+cache.bucketBytes)
	require.Equal(t, "one", views[0].Name, "eviction must not mutate an in-flight view")
	cache.StopCacheCleanup()
}

func TestSchedulerCandidatePoolConcurrentReadersAndDeletion(t *testing.T) {
	cache := newSchedulerCacheUnit(t)
	ctx := context.Background()
	bucket := memoTestBucket(1)
	memoPublish(t, cache, bucket, memoTestAccount(1, "one"))
	var wg sync.WaitGroup
	results := make(chan *service.Account, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			views, hit, err := cache.GetCandidateSnapshot(ctx, bucket)
			if err != nil || !hit {
				t.Errorf("candidate read: hit=%v err=%v", hit, err)
				return
			}
			results <- views[0].Ref()
		}()
	}
	wg.Wait()
	close(results)
	var first *service.Account
	for account := range results {
		if first == nil {
			first = account
		} else {
			require.Same(t, first, account)
		}
	}
	require.Len(t, cache.accountEntries, 1)
	require.NoError(t, cache.DeleteAccount(ctx, 1))
	_, hit, err := cache.GetCandidateSnapshot(ctx, bucket)
	require.NoError(t, err)
	require.False(t, hit, "a deleted account must not be served from the shared pool")
}
