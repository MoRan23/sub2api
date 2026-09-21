package service

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type testCodexTelemetryNotifier struct {
	started chan struct{}
	stopped chan struct{}
	called  atomic.Int64
}

func (n *testCodexTelemetryNotifier) Notify(context.Context) error {
	n.called.Add(1)
	return errors.New("Redis unavailable")
}

func (n *testCodexTelemetryNotifier) Subscribe(ctx context.Context, _ func()) error {
	close(n.started)
	<-ctx.Done()
	close(n.stopped)
	return ctx.Err()
}

func newTestCodexTelemetryNotifier() *testCodexTelemetryNotifier {
	return &testCodexTelemetryNotifier{started: make(chan struct{}), stopped: make(chan struct{})}
}

func TestCodexTelemetryNotifierReplacementAndStop(t *testing.T) {
	s := NewCodexTelemetryService(nil)
	t.Cleanup(s.Stop)
	first, second := newTestCodexTelemetryNotifier(), newTestCodexTelemetryNotifier()
	s.SetNotifier(first)
	select {
	case <-first.started:
	case <-time.After(time.Second):
		t.Fatal("first subscription not started")
	}
	s.publishPolicyChange() // Redis failure is deliberately non-fatal.
	require.EqualValues(t, 1, first.called.Load())
	s.SetNotifier(second)
	select {
	case <-first.stopped:
	case <-time.After(time.Second):
		t.Fatal("replaced subscription not cancelled")
	}
	select {
	case <-second.started:
	case <-time.After(time.Second):
		t.Fatal("replacement subscription not started")
	}
	done := make(chan struct{})
	go func() { s.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Stop blocked on subscription")
	}
	select {
	case <-second.stopped:
	default:
		t.Fatal("Stop did not cancel subscription")
	}
	s.publishPolicyChange()
	require.Zero(t, second.called.Load())
	s.SetNotifier(first) // Installing after Stop must not restart a subscription.
}

func TestCodexTelemetryNotifierNilSafe(t *testing.T) {
	var missing *CodexTelemetryService
	missing.SetNotifier(nil)
	missing.publishPolicyChange()
	s := NewCodexTelemetryService(nil)
	t.Cleanup(s.Stop)
	s.SetNotifier(nil)
	s.publishPolicyChange()
}
