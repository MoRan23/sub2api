//go:build unit

package service

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

type candidateFlightRepo struct {
	AccountRepository
	calls   atomic.Int32
	entered chan int
	release chan struct{}
}

func (r *candidateFlightRepo) ListModelAvailabilityCandidates(ctx context.Context, _ *int64, _ []string, _ bool) ([]Account, error) {
	n := int(r.calls.Add(1))
	if r.entered != nil {
		r.entered <- n
		select {
		case <-r.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return []Account{{ID: int64(n), Status: StatusActive, Schedulable: true}}, nil
}

type candidateFlightCache struct {
	SchedulerCache
	epoch     atomic.Int64
	published atomic.Int32
}

func (c *candidateFlightCache) GetSnapshot(context.Context, SchedulerBucket) ([]*Account, bool, error) {
	return nil, false, nil
}
func (c *candidateFlightCache) CaptureBucketWriteToken(context.Context, SchedulerBucket) (SchedulerBucketWriteToken, error) {
	return SchedulerBucketWriteToken{Epoch: c.epoch.Load()}, nil
}
func (c *candidateFlightCache) SetSnapshot(_ context.Context, _ SchedulerBucket, token SchedulerBucketWriteToken, _ []Account) error {
	if token.Epoch != c.epoch.Load() {
		return ErrSchedulerBucketWriteFenced
	}
	c.published.Add(1)
	return nil
}

func TestSchedulerCandidateFlightsSeparateWriteEpochs(t *testing.T) {
	repo := &candidateFlightRepo{entered: make(chan int, 2), release: make(chan struct{})}
	cache := &candidateFlightCache{}
	cache.epoch.Store(1)
	svc := NewSchedulerSnapshotService(cache, nil, repo, nil, nil)
	done := make(chan error, 2)
	go func() {
		_, _, err := svc.ListSchedulableCandidates(context.Background(), nil, PlatformOpenAI, false)
		done <- err
	}()
	require.Equal(t, 1, <-repo.entered)
	cache.epoch.Store(2)
	go func() {
		_, _, err := svc.ListSchedulableCandidates(context.Background(), nil, PlatformOpenAI, false)
		done <- err
	}()
	require.Equal(t, 2, <-repo.entered)
	close(repo.release)
	require.NoError(t, <-done)
	require.NoError(t, <-done)
	require.EqualValues(t, 1, cache.published.Load())
}

type parentProjectionRepo struct {
	AccountRepository
	ids       []int64
	fullReads int
}

func (r *parentProjectionRepo) GetShadowParentHealthBatch(_ context.Context, ids []int64) (map[int64]ShadowParentHealth, error) {
	r.ids = append([]int64(nil), ids...)
	result := make(map[int64]ShadowParentHealth)
	for _, id := range ids {
		result[id] = ShadowParentHealth{ID: id, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive}
	}
	return result, nil
}
func (r *parentProjectionRepo) GetByID(context.Context, int64) (*Account, error) {
	r.fullReads++
	return &Account{ID: 9, Status: "disabled"}, nil
}
func TestShadowParentsBatchDeduplicatedAndFinalReadFresh(t *testing.T) {
	repo := &parentProjectionRepo{}
	scheduler := &defaultOpenAIAccountScheduler{service: &OpenAIGatewayService{accountRepo: repo}}
	parent := int64(9)
	accounts := CandidatesFromAccounts([]Account{{ID: 1, ParentAccountID: &parent}, {ID: 2, ParentAccountID: &parent}})
	original := context.Background()
	filtered := scheduler.withShadowParentHealth(original, accounts)
	require.Equal(t, []int64{9}, repo.ids)
	require.Equal(t, StatusActive, scheduler.lookupShadowParentAccount(filtered, 9).Status)
	require.Zero(t, repo.fullReads)
	require.Equal(t, "disabled", scheduler.lookupShadowParentAccount(original, 9).Status)
	require.Equal(t, 1, repo.fullReads)
}
