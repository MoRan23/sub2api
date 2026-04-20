package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type expiryWorkerUserSubRepoStub struct {
	userSubRepoNoop

	updateResult int64
	updateErr    error
	deleteErr    error

	batchUpdateCalls int
	deleteCalls      int
	deleteResults    []int64
	deleteCutoffs    []time.Time
	deleteLimits     []int
}

func (s *expiryWorkerUserSubRepoStub) BatchUpdateExpiredStatus(context.Context) (int64, error) {
	s.batchUpdateCalls++
	return s.updateResult, s.updateErr
}

func (s *expiryWorkerUserSubRepoStub) DeleteExpiredQuotaEventsBatch(_ context.Context, now time.Time, limit int) (int64, error) {
	s.deleteCalls++
	s.deleteCutoffs = append(s.deleteCutoffs, now)
	s.deleteLimits = append(s.deleteLimits, limit)
	if s.deleteErr != nil {
		return 0, s.deleteErr
	}
	if len(s.deleteResults) == 0 {
		return 0, nil
	}
	result := s.deleteResults[0]
	s.deleteResults = s.deleteResults[1:]
	return result, nil
}

func TestSubscriptionExpiryServiceRunOnce_CleansExpiredQuotaEventsInBatches(t *testing.T) {
	repo := &expiryWorkerUserSubRepoStub{
		updateResult: 1,
		deleteResults: []int64{
			2,
			1,
			0,
		},
	}
	svc := NewSubscriptionExpiryService(repo, time.Minute)

	svc.runOnce()

	require.Equal(t, 1, repo.batchUpdateCalls)
	require.Equal(t, 3, repo.deleteCalls)
	require.Len(t, repo.deleteCutoffs, 3)
	require.Equal(t, []int{
		subscriptionQuotaEventCleanupBatchSize,
		subscriptionQuotaEventCleanupBatchSize,
		subscriptionQuotaEventCleanupBatchSize,
	}, repo.deleteLimits)
	for i := 1; i < len(repo.deleteCutoffs); i++ {
		require.True(t, repo.deleteCutoffs[i].Equal(repo.deleteCutoffs[0]), "cleanup cutoff should stay stable within one run")
	}
}
