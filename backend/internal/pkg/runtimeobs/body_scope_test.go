package runtimeobs

import (
	"bytes"
	"io"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRequestBodyScopeCountsOnceAndReleases(t *testing.T) {
	var counters Counters
	req := httptest.NewRequest("POST", "/v1/responses", bytes.NewReader(make([]byte, LargeBodyThreshold)))
	req, release := counters.TrackRequestBody(req)
	req, nestedRelease := counters.TrackRequestBody(req)
	_, err := io.Copy(io.Discard, req.Body)
	require.NoError(t, err)
	ObserveBufferedBody(req.Context(), LargeBodyThreshold)
	ObserveBufferedBody(req.Context(), LargeBodyThreshold)
	nestedRelease()
	got := counters.Snapshot()
	require.EqualValues(t, 1, got.ActiveBodies)
	require.EqualValues(t, LargeBodyThreshold, got.ActiveBodyBytes)
	require.EqualValues(t, 1, got.ActiveLargeBodies)
	release()
	release()
	got = counters.Snapshot()
	require.Zero(t, got.ActiveBodies)
	require.Zero(t, got.ActiveBodyBytes)
	require.Zero(t, got.ActiveLargeBodies)
	require.Zero(t, got.ActiveLargeBodyBytes)
	var samples uint64
	for _, n := range got.BodySize.Counts {
		samples += n
	}
	require.EqualValues(t, 1, samples)
}

func TestRequestBodyScopeDecodedSizeAndConcurrentFinish(t *testing.T) {
	var counters Counters
	req := httptest.NewRequest("POST", "/v1/responses", bytes.NewReader([]byte("compressed")))
	req, release := counters.TrackRequestBody(req)
	_, err := io.Copy(io.Discard, req.Body)
	require.NoError(t, err)
	ObserveBufferedBody(req.Context(), 2*LargeBodyThreshold)
	got := counters.Snapshot()
	require.EqualValues(t, 2*LargeBodyThreshold, got.ActiveLargeBodyBytes)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			ObserveBufferedBody(req.Context(), 3*LargeBodyThreshold)
			release()
		})
	}
	wg.Wait()
	ObserveBufferedBody(req.Context(), 4*LargeBodyThreshold)
	got = counters.Snapshot()
	require.Zero(t, got.ActiveBodies)
	require.Zero(t, got.ActiveBodyBytes)
	require.Zero(t, got.ActiveLargeBodies)
	require.Zero(t, got.ActiveLargeBodyBytes)
}

func TestRequestBodyScopeUnreadAndNilRequest(t *testing.T) {
	var counters Counters
	nilReq, release := counters.TrackRequestBody(nil)
	require.Nil(t, nilReq)
	release()
	req := httptest.NewRequest("POST", "/v1/responses", bytes.NewReader([]byte("not consumed")))
	_, release = counters.TrackRequestBody(req)
	release()
	require.Zero(t, counters.Snapshot().ActiveBodies)
}
