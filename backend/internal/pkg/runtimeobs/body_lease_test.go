package runtimeobs

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBodyLeaseCurrentPendingReplayAndClose(t *testing.T) {
	var counters Counters
	tracker := NewBodyTracker(&counters)
	tracker.EnsureCurrent(LargeBodyThreshold)
	tracker.EnsureCurrent(LargeBodyThreshold * 2) // Replay updates size, never count.
	pending := tracker.Begin(LargeBodyThreshold * 3)
	stats := counters.Snapshot()
	require.EqualValues(t, 2, stats.ActiveLargeBodies)
	require.EqualValues(t, 5*LargeBodyThreshold, stats.ActiveLargeBodyBytes)
	tracker.CompleteCurrent()
	tracker.Activate(pending)
	stats = counters.Snapshot()
	require.EqualValues(t, 1, stats.ActiveBodies)
	require.EqualValues(t, 3*LargeBodyThreshold, stats.ActiveBodyBytes)
	tracker.EnsureCurrent(LargeBodyThreshold) // A new attempt does not shrink it.
	tracker.CompleteCurrent()
	pending.Release()
	tracker.Close()
	require.Zero(t, counters.Snapshot().ActiveBodies)
	var total uint64
	for _, n := range counters.Snapshot().BodySize.Counts {
		total += n
	}
	require.EqualValues(t, 2, total)
}

func TestBodyLeaseCloseConcurrentAndLateUpdates(t *testing.T) {
	var counters Counters
	tracker := NewBodyTracker(&counters)
	tracker.EnsureCurrent(LargeBodyThreshold)
	pending := tracker.Begin(2 * LargeBodyThreshold)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			pending.ObserveSize(3 * LargeBodyThreshold)
			tracker.CompleteCurrent()
			pending.Release()
			tracker.Close()
		})
	}
	wg.Wait()
	tracker.EnsureCurrent(LargeBodyThreshold)
	tracker.Begin(LargeBodyThreshold).Release()
	s := counters.Snapshot()
	require.Zero(t, s.ActiveBodies)
	require.Zero(t, s.ActiveBodyBytes)
	require.Zero(t, s.ActiveLargeBodies)
	require.Zero(t, s.ActiveLargeBodyBytes)
}
