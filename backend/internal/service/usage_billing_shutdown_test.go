//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestUsageBillingLane_ConcurrentStopAndAdmission(t *testing.T) {
	for round := 0; round < 10; round++ {
		lane := NewUsageBillingLaneCombiner(newLaneTestRepo())
		start := make(chan struct{})
		outcomes := make(chan error, 30)
		for i := 0; i < cap(outcomes); i++ {
			go func(i int) {
				<-start
				_, err := lane.Apply(context.Background(), laneTestCommand(7, fmt.Sprint(i), 1))
				outcomes <- err
			}(i)
		}
		stopped := make(chan struct{})
		go func() { <-start; lane.Stop(); close(stopped) }()
		close(start)
		for i := 0; i < cap(outcomes); i++ {
			err := <-outcomes
			require.True(t, err == nil || errors.Is(err, ErrUsageBillingStopped), "unexpected settlement error: %v", err)
		}
		<-stopped
		lane.Stop()
	}
}

type blockingDirectBillingRepo struct {
	*laneTestRepo
	started chan struct{}
	release chan struct{}
}

func (r *blockingDirectBillingRepo) blockDirect() {
	close(r.started)
	<-r.release
}

func (r *blockingDirectBillingRepo) ApplyWithUsageLog(context.Context, *UsageBillingCommand, *UsageLog) (*UsageBillingApplyResult, error) {
	r.blockDirect()
	return &UsageBillingApplyResult{Applied: true}, nil
}
func (r *blockingDirectBillingRepo) ReserveBatchImageBalance(context.Context, *BatchImageBalanceHoldCommand) (*BatchImageBalanceHoldResult, error) {
	r.blockDirect()
	return &BatchImageBalanceHoldResult{Applied: true}, nil
}
func (r *blockingDirectBillingRepo) CaptureBatchImageBalance(context.Context, *BatchImageBalanceHoldCommand) (*BatchImageBalanceHoldResult, error) {
	r.blockDirect()
	return &BatchImageBalanceHoldResult{Applied: true}, nil
}
func (r *blockingDirectBillingRepo) ReleaseBatchImageBalance(context.Context, *BatchImageBalanceHoldCommand) (*BatchImageBalanceHoldResult, error) {
	r.blockDirect()
	return &BatchImageBalanceHoldResult{Applied: true}, nil
}

func TestUsageBillingLane_StopWaitsForDirectSettlements(t *testing.T) {
	calls := map[string]func(*UsageBillingLaneCombiner) error{
		"atomic failure bill": func(c *UsageBillingLaneCombiner) error {
			_, err := c.ApplyWithUsageLog(context.Background(), &UsageBillingCommand{}, &UsageLog{})
			return err
		},
		"image reserve": func(c *UsageBillingLaneCombiner) error {
			_, err := c.ReserveBatchImageBalance(context.Background(), &BatchImageBalanceHoldCommand{})
			return err
		},
		"image capture": func(c *UsageBillingLaneCombiner) error {
			_, err := c.CaptureBatchImageBalance(context.Background(), &BatchImageBalanceHoldCommand{})
			return err
		},
		"image release": func(c *UsageBillingLaneCombiner) error {
			_, err := c.ReleaseBatchImageBalance(context.Background(), &BatchImageBalanceHoldCommand{})
			return err
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			repo := &blockingDirectBillingRepo{laneTestRepo: newLaneTestRepo(), started: make(chan struct{}), release: make(chan struct{})}
			lane := NewUsageBillingLaneCombiner(repo)
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(repo.release) }) }
			t.Cleanup(func() { release(); lane.Stop() })
			result := make(chan error, 1)
			go func() { result <- call(lane) }()
			<-repo.started
			stopped := make(chan struct{})
			go func() { lane.Stop(); close(stopped) }()
			require.Eventually(t, func() bool {
				lane.admissionMu.Lock()
				defer lane.admissionMu.Unlock()
				return lane.stopping
			}, time.Second, time.Millisecond)
			require.ErrorIs(t, call(lane), ErrUsageBillingStopped)
			select {
			case <-stopped:
				t.Fatal("Stop returned before the direct repository operation finished")
			case <-time.After(20 * time.Millisecond):
			}
			release()
			require.NoError(t, <-result)
			select {
			case <-stopped:
			case <-time.After(time.Second):
				t.Fatal("Stop did not return after the operation finished")
			}
		})
	}
}

func TestUsageRecordWorkerPool_StopWaitsForInlineFallbacks(t *testing.T) {
	for _, tc := range []struct {
		name      string
		policy    string
		mandatory bool
	}{
		{"ordinary sync", config.UsageRecordOverflowPolicySync, false},
		{"mandatory drop", config.UsageRecordOverflowPolicyDrop, true},
		{"mandatory sample", config.UsageRecordOverflowPolicySample, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := NewUsageRecordWorkerPoolWithOptions(UsageRecordWorkerPoolOptions{
				WorkerCount: 1, QueueSize: 1, TaskTimeout: time.Second,
				OverflowPolicy: tc.policy, OverflowSamplePercent: 1,
			})
			if tc.policy == config.UsageRecordOverflowPolicySample {
				pool.overflowCounter.Store(1) // Select the sample-drop branch.
			}
			workerRelease, inlineRelease := make(chan struct{}), make(chan struct{})
			var workerOnce, inlineOnce sync.Once
			releaseWorker := func() { workerOnce.Do(func() { close(workerRelease) }) }
			releaseInline := func() { inlineOnce.Do(func() { close(inlineRelease) }) }
			t.Cleanup(func() { releaseWorker(); releaseInline(); pool.Stop() })
			workerStarted := make(chan struct{})
			require.Equal(t, UsageRecordSubmitModeEnqueued, pool.Submit(func(context.Context) {
				close(workerStarted)
				<-workerRelease
			}))
			<-workerStarted
			require.Equal(t, UsageRecordSubmitModeEnqueued, pool.Submit(func(context.Context) {}))
			inlineStarted := make(chan time.Time, 1)
			mode := make(chan UsageRecordSubmitMode, 1)
			go func() {
				task := func(ctx context.Context) {
					deadline, _ := ctx.Deadline()
					inlineStarted <- deadline
					<-inlineRelease
				}
				if tc.mandatory {
					mode <- pool.SubmitMandatory(task)
				} else {
					mode <- pool.Submit(task)
				}
			}()
			deadline := <-inlineStarted
			if tc.mandatory {
				require.Greater(t, time.Until(deadline), 8*time.Second, "keep the existing 10-second mandatory fallback timeout")
			}
			releaseWorker()
			stopped := make(chan struct{})
			go func() { pool.Stop(); close(stopped) }()
			require.Eventually(t, func() bool {
				pool.admissionMu.Lock()
				defer pool.admissionMu.Unlock()
				return pool.stopping
			}, time.Second, time.Millisecond)
			require.Equal(t, UsageRecordSubmitModeDroppedStopped, pool.SubmitMandatory(func(context.Context) { t.Error("late work entered the stopped pool") }))
			select {
			case <-stopped:
				t.Fatal("Stop returned before inline billing finished")
			case <-time.After(20 * time.Millisecond):
			}
			releaseInline()
			require.Equal(t, UsageRecordSubmitModeSync, <-mode)
			select {
			case <-stopped:
			case <-time.After(time.Second):
				t.Fatal("Stop did not wait for and finish inline fallback")
			}
		})
	}
}
