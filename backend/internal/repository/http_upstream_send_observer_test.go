package repository

import (
	"context"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/codexnative"
	"github.com/Wei-Shaw/sub2api/internal/pkg/httpsendobserver"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openaicookies"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type upstreamBoundaryRoundTripFunc func(*http.Request) (*http.Response, error)

func (f upstreamBoundaryRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestHTTPUpstreamObserverRunsOnceAtOrdinaryAndNativeSend(t *testing.T) {
	for _, native := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "native"}[native], func(t *testing.T) {
			s, ok := NewHTTPUpstreamWithCookies(nil, openaicookies.NewManager()).(*httpUpstreamService)
			require.True(t, ok)
			calls := 0
			var observed *http.Request
			ctx := httpsendobserver.WithObserver(context.Background(), func(request *http.Request) { calls++; observed = request })
			ctx = openaicookies.WithScope(ctx, openaicookies.Scope{OwnerAccountID: 31, OSFamily: "windows", AuthorizationGeneration: "grant"})
			nativeScope := codexnative.Scope{AccountID: 31, Purpose: "oauth", AccountUserAgent: "Windows"}
			if native {
				ctx = codexnative.WithScope(ctx, nativeScope)
			}
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://chatgpt.com/responses", nil)
			require.NoError(t, err)
			request.Header.Set("User-Agent", "codex-tui/0.155.1 (Windows 10.0; x86_64)")
			request.Header.Set("Cookie", "original=preserved")
			request.Header.Set("X-Codex-Turn-State", "opaque-original")
			var entry *upstreamClientEntry
			if native {
				entry, err = s.acquireNativeClient("", 31, 2, service.HTTPUpstreamProfileDefault, nativeScope, codexnative.Resolve(request.UserAgent(), nativeScope))
			} else {
				entry, err = s.acquireClient("", 31, 2)
			}
			require.NoError(t, err)
			atomic.AddInt64(&entry.inFlight, -1)
			entry.client.CloseIdleConnections()
			entry.client.Transport = upstreamBoundaryRoundTripFunc(func(outbound *http.Request) (*http.Response, error) {
				require.Equal(t, 1, calls)
				require.Same(t, outbound, observed)
				require.Equal(t, "original=preserved", outbound.Header.Get("Cookie"))
				require.Equal(t, "opaque-original", outbound.Header.Get("X-Codex-Turn-State"))
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("{}")), Request: outbound}, nil
			})
			response, err := s.Do(request, "", 31, 2)
			require.NoError(t, err)
			require.NoError(t, response.Body.Close())
			require.Equal(t, 1, calls)
			require.Zero(t, atomic.LoadInt64(&entry.inFlight))
		})
	}
}

func TestHTTPUpstreamObserverDoesNotRecordFailedLocalDispatch(t *testing.T) {
	for _, native := range []bool{false, true} {
		calls := 0
		ctx := httpsendobserver.WithObserver(context.Background(), func(*http.Request) { calls++ })
		if native {
			ctx = codexnative.WithScope(ctx, codexnative.Scope{AccountID: 31})
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://chatgpt.com/responses", nil)
		require.NoError(t, err)
		_, err = NewHTTPUpstream(nil).Do(request, "://invalid-proxy", 31, 2)
		require.Error(t, err)
		require.Zero(t, calls)
	}
}

func TestHTTPUpstreamUnmarkedRequestsPreserveOriginalClientPolicy(t *testing.T) {
	s, ok := NewHTTPUpstreamWithCookies(nil, openaicookies.NewManager()).(*httpUpstreamService)
	require.True(t, ok)
	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	client := &http.Client{Jar: jar}
	request, err := http.NewRequest(http.MethodGet, "https://chatgpt.com/", nil)
	require.NoError(t, err)
	require.Same(t, client, s.httpClientWithRequestBoundary(client, request))
	require.Same(t, jar, client.Jar)
}
