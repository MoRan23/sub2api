package service

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

type attributionExecutionLeaseRepo struct {
	*attributionRetryRepo
	releases atomic.Int64
}

func (r *attributionExecutionLeaseRepo) ReleaseExecution(context.Context, *AttributionJob) error {
	r.releases.Add(1)
	return nil
}

func TestAttributionCancelReleasesCapacityOnlyAfterDrain(t *testing.T) {
	s, base, _, _, job := newAttributionRetryHarness("manual")
	repo := &attributionExecutionLeaseRepo{attributionRetryRepo: base}
	s.repo = repo
	started := make(chan int, 3)
	cancelled := make(chan struct{}, 3)
	drain := make(chan struct{})
	s.probe = attributionProbeFunc(func(ctx context.Context, _ int64, _, prompt string) (*CandyTestExecution, error) {
		started <- attributionParallelIndex(prompt)
		<-ctx.Done()
		cancelled <- struct{}{}
		<-drain
		return nil, ctx.Err()
	})
	done := make(chan struct{})
	go func() { defer close(done); s.execute(context.Background(), job) }()
	attributionParallelStarted(t, started)
	_, err := s.Cancel(context.Background(), job.ID)
	require.NoError(t, err)
	for range 3 {
		<-cancelled
	}
	require.Zero(t, repo.releases.Load())
	close(drain)
	attributionParallelDone(t, done)
	require.EqualValues(t, 1, repo.releases.Load())
}
