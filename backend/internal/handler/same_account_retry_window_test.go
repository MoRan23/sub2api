package handler

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func retryWindowError(deadline time.Time) *service.UpstreamFailoverError {
	return &service.UpstreamFailoverError{StatusCode: http.StatusTooManyRequests, RetryableOnSameAccount: true, SameAccountRetryDeadline: deadline}
}

func TestSameAccountRetryWindowCannotBeRenewedByOtherRequests(t *testing.T) {
	var first, second sameAccountRetryWindows
	deadline := time.Now().Add(time.Minute)
	require.True(t, first.allow(7, retryWindowError(deadline), 0, 3))
	// Another request succeeds; the service creates a fresh account window.
	fresh := retryWindowError(deadline.Add(time.Minute))
	require.True(t, first.allow(7, fresh, 8, 3))
	require.Equal(t, deadline, fresh.SameAccountRetryDeadline)
	require.True(t, second.allow(7, retryWindowError(deadline.Add(time.Minute)), 0, 3))
	require.Equal(t, deadline.Add(time.Minute), second[7])
	// Reaching the original deadline stays final even after another reset.
	first[7] = time.Now().Add(-time.Second)
	require.False(t, first.allow(7, retryWindowError(deadline.Add(time.Hour)), 72, 3))
	require.True(t, first.allow(8, retryWindowError(deadline), 0, 3), "another account has its own window")
}

func TestSameAccountRetryWindowPreservesLimitsAndShorterDeadlines(t *testing.T) {
	var windows sameAccountRetryWindows
	require.True(t, windows.allow(7, &service.UpstreamFailoverError{RetryableOnSameAccount: true}, 0, 3))
	require.Nil(t, windows, "ordinary retries allocate no deadline map")
	deadline := time.Now().Add(time.Minute)
	require.True(t, windows.allow(7, retryWindowError(deadline), 0, 3))
	require.True(t, windows.allow(7, retryWindowError(deadline.Add(-time.Second)), 1, 3))
	require.Equal(t, deadline.Add(-time.Second), windows[7])
	raced := retryWindowError(time.Time{})
	require.True(t, windows.allow(7, raced, 2, 3))
	require.Equal(t, windows[7], raced.SameAccountRetryDeadline)
	ordinary := &service.UpstreamFailoverError{StatusCode: 503, RetryableOnSameAccount: true}
	require.False(t, windows.allow(7, ordinary, 3, 3), "a prior 429 must not remove a 503 count limit")
	limited := retryWindowError(deadline)
	limited.SameAccountRetryMax = 1
	require.False(t, windows.allow(7, limited, 1, 3))
}

func TestSameAccountRetryWindowDoesNotLeakFromCountCappedErrors(t *testing.T) {
	var windows sameAccountRetryWindows
	capacity := retryWindowError(time.Now().Add(time.Minute))
	capacity.SameAccountRetryMax = 1
	require.True(t, windows.allow(7, capacity, 0, 3))
	require.Nil(t, windows)
	// Grok capacity and idle errors carry per-error deadlines plus a hard count
	// cap. They must not turn a later ordinary 429 into a deadline-only policy.
	ordinary := retryWindowError(time.Time{})
	require.False(t, windows.allow(7, ordinary, 3, 3))
	require.True(t, ordinary.SameAccountRetryDeadline.IsZero())
	capacity.StatusCode = http.StatusBadGateway
	require.True(t, windows.allow(7, capacity, 0, 3))
	require.Nil(t, windows)
}

func TestWaitSameAccountRetryStopsAtDeadlineAndCancellation(t *testing.T) {
	require.False(t, waitSameAccountRetry(context.Background(), retryWindowError(time.Now().Add(-time.Second)), time.Hour))
	err := retryWindowError(time.Now().Add(10 * time.Millisecond))
	started := time.Now()
	require.False(t, waitSameAccountRetry(context.Background(), err, time.Hour))
	require.Less(t, time.Since(started), time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.False(t, waitSameAccountRetry(ctx, retryWindowError(time.Now().Add(time.Minute)), time.Hour))
}

func TestFailoverStateUsesOriginalWindowAfterServiceReset(t *testing.T) {
	state := NewFailoverState(1, false)
	state.sameAccountWindows = sameAccountRetryWindows{7: time.Now().Add(-time.Second)}
	upstream := retryWindowError(time.Now().Add(time.Minute))
	mock := &mockTempUnscheduler{}
	require.Equal(t, FailoverContinue, state.HandleFailoverError(context.Background(), mock, 7, service.PlatformOpenAI, 3, upstream))
	require.Equal(t, 1, state.SwitchCount)
	require.Contains(t, state.FailedAccountIDs, int64(7))
	require.Zero(t, state.SameAccountRetryCount[7])
}
