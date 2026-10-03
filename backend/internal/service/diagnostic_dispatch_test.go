package service

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type parallelCandyRepo struct {
	CandyTestRepository
	claimed atomic.Int64
}

func (r *parallelCandyRepo) Claim(context.Context) (*CandyTestItem, error) {
	id := r.claimed.Add(1)
	if id > 5 {
		return nil, nil
	}
	return &CandyTestItem{ID: id, AccountID: id}, nil
}
func (*parallelCandyRepo) Complete(context.Context, *CandyTestItem) (bool, error) { return true, nil }

type parallelCandyExecutor struct {
	CandyTestExecutor
	started chan int64
}

func (e *parallelCandyExecutor) Execute(ctx context.Context, item *CandyTestItem) (*CandyTestExecution, error) {
	e.started <- item.AccountID
	<-ctx.Done()
	return nil, ctx.Err()
}
func TestCandyDispatchStartsAllAccountsBeforeAnyComplete(t *testing.T) {
	e := &parallelCandyExecutor{started: make(chan int64, 5)}
	s := NewAccountCandyTestService(&parallelCandyRepo{}, e)
	t.Cleanup(s.Stop)
	s.Start()
	seen := map[int64]bool{}
	for range 5 {
		select {
		case id := <-e.started:
			seen[id] = true
		case <-time.After(3 * time.Second):
			t.Fatal("accounts waited for an earlier test to complete")
		}
	}
	require.Len(t, seen, 5)
}

type parallelAttributionRepo struct {
	AttributionRepository
	claimed atomic.Int64
}

func (r *parallelAttributionRepo) Claim(context.Context) (*AttributionJob, error) {
	id := r.claimed.Add(1)
	if id > 5 {
		return nil, nil
	}
	return &AttributionJob{ID: id, AccountID: id}, nil
}
func (*parallelAttributionRepo) Validate(context.Context, *AttributionJob) (bool, error) {
	return true, nil
}
func (*parallelAttributionRepo) Config(context.Context) (AttributionConfig, error) {
	return DefaultAttributionConfig(), nil
}
func (*parallelAttributionRepo) Finish(context.Context, *AttributionJob) error { return nil }

type blockingAttributionAnalyzer struct {
	AttributionAnalyzer
	started chan struct{}
}

func (a *blockingAttributionAnalyzer) Models(ctx context.Context, _ string) ([]string, error) {
	a.started <- struct{}{}
	<-ctx.Done()
	return nil, ctx.Err()
}
func TestAttributionDispatchStartsAllAccountsBeforeAnyComplete(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	a := &blockingAttributionAnalyzer{started: make(chan struct{}, 5)}
	s := &ModelAttributionService{repo: &parallelAttributionRepo{}, analyzer: a}
	s.wg.Add(1)
	go s.worker(ctx)
	t.Cleanup(func() { cancel(); s.wg.Wait() })
	for range 5 {
		select {
		case <-a.started:
		case <-time.After(3 * time.Second):
			t.Fatal("accounts waited for an earlier test to complete")
		}
	}
}
