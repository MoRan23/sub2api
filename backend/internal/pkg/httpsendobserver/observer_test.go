package httpsendobserver

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestObserverSeesUnmodifiedPhysicalHTTPBoundary(t *testing.T) {
	calls := 0
	var observed *http.Request
	ctx := WithObserver(context.Background(), func(request *http.Request) { calls++; observed = request })
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://example.invalid/", nil)
	require.NoError(t, err)
	request.Header.Set("Cookie", "original=unchanged")
	request.Header.Set("X-Codex-Turn-State", "opaque")
	expected := request.Header.Clone()
	transport := Wrap(roundTripFunc(func(outbound *http.Request) (*http.Response, error) {
		require.Same(t, outbound, observed)
		require.Equal(t, expected, outbound.Header)
		return &http.Response{StatusCode: http.StatusOK}, nil
	}))
	for want := 1; want <= 2; want++ {
		_, err := transport.RoundTrip(request)
		require.NoError(t, err)
		require.Equal(t, want, calls, "each physical send is observed")
	}
	require.Equal(t, expected, request.Header)
}

func TestObserverDoesNotCountWebSocketOrUnmarkedRequests(t *testing.T) {
	calls := 0
	ctx := WithObserver(context.Background(), func(*http.Request) { calls++ })
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://example.invalid/", nil)
	require.NoError(t, err)
	request.Header["uPgRaDe"] = []string{"h2c, WebSocket"}
	require.False(t, Enabled(request))
	Notify(request)
	Notify(request.WithContext(context.Background()))
	Notify(nil)
	require.Zero(t, calls)
}
