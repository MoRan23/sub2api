package sharedflight

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func waitForWaiters(t *testing.T, g *Group[string, int], key string, n int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		g.mu.Lock()
		c := g.calls[key]
		ready := c != nil && c.waiters == n
		g.mu.Unlock()
		if ready {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("waiters did not join")
}
func TestIndependentCancellationAndImmutableResult(t *testing.T) {
	var g Group[string, int]
	var calls atomic.Int32
	release := make(chan struct{})
	started := make(chan context.Context, 1)
	load := func(ctx context.Context) (int, error) {
		calls.Add(1)
		started <- ctx
		select {
		case <-release:
			return 7, nil
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	first, cancel := context.WithCancel(context.Background())
	defer cancel()
	firstDone := make(chan error, 1)
	go func() { _, err := g.Do(first, "key", load); firstDone <- err }()
	loadCtx := <-started
	secondDone := make(chan int, 1)
	go func() {
		v, err := g.Do(context.Background(), "key", load)
		if err != nil {
			secondDone <- -1
		} else {
			secondDone <- v
		}
	}()
	waitForWaiters(t, &g, "key", 2)
	cancel()
	if !errors.Is(<-firstDone, context.Canceled) {
		t.Fatal("first waiter was not canceled")
	}
	if loadCtx.Err() != nil {
		t.Fatal("first cancellation canceled other waiter")
	}
	close(release)
	if got := <-secondDone; got != 7 {
		t.Fatalf("got %d", got)
	}
	if calls.Load() != 1 {
		t.Fatalf("loaded %d times", calls.Load())
	}
}
func TestLastWaiterCancellationAllowsNewGeneration(t *testing.T) {
	var g Group[string, int]
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan context.Context, 1)
	done := make(chan error, 1)
	go func() {
		_, err := g.Do(ctx, "key", func(ctx context.Context) (int, error) { started <- ctx; <-ctx.Done(); return 0, ctx.Err() })
		done <- err
	}()
	loadCtx := <-started
	cancel()
	<-done
	select {
	case <-loadCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("orphan load retained")
	}
	v, err := g.Do(context.Background(), "key", func(context.Context) (int, error) { return 9, nil })
	if err != nil || v != 9 {
		t.Fatalf("new load: %d %v", v, err)
	}
}
func TestCompletedEmptyResultsAreNotCached(t *testing.T) {
	var g Group[string, int]
	calls := 0
	for i := 0; i < 2; i++ {
		_, err := g.Do(context.Background(), "key", func(context.Context) (int, error) { calls++; return 0, nil })
		if err != nil {
			t.Fatal(err)
		}
	}
	if calls != 2 {
		t.Fatalf("empty result cached: %d", calls)
	}
}
func TestCancelStopsOutstandingLoads(t *testing.T) {
	var g Group[string, int]
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := g.Do(context.Background(), "key", func(ctx context.Context) (int, error) { close(started); <-ctx.Done(); return 0, ctx.Err() })
		done <- err
	}()
	<-started
	g.Cancel()
	if !errors.Is(<-done, context.Canceled) {
		t.Fatal("load survived shutdown")
	}
}

func TestLoadPanicPropagatesToCallerRecovery(t *testing.T) {
	var g Group[string, int]
	func() {
		defer func() {
			if recover() != "loader panic" {
				t.Error("load panic was not propagated to its caller")
			}
		}()
		_, _ = g.Do(context.Background(), "key", func(context.Context) (int, error) { panic("loader panic") })
	}()
	value, err := g.Do(context.Background(), "key", func(context.Context) (int, error) { return 1, nil })
	if err != nil || value != 1 {
		t.Fatalf("load did not recover: %d %v", value, err)
	}
}
