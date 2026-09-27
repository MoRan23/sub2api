package repository

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/codexnative"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// A replayable POST would normally be resent for 307/308. Candy's existing
// request-scoped redirect policy must reach both real HTTP transport variants.
func TestCandyHTTPTransportDoesNotReplayRedirectedPOST(t *testing.T) {
	for _, native := range []bool{false, true} {
		for _, status := range []int{http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
			t.Run(fmt.Sprintf("native_%t/status_%d", native, status), func(t *testing.T) {
				var firstCalls, secondCalls atomic.Int64
				second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					secondCalls.Add(1)
					_, _ = io.Copy(io.Discard, r.Body)
					w.WriteHeader(http.StatusOK)
				}))
				t.Cleanup(second.Close)
				first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					firstCalls.Add(1)
					_, _ = io.Copy(io.Discard, r.Body)
					w.Header().Set("Location", second.URL)
					w.WriteHeader(status)
				}))
				t.Cleanup(first.Close)
				upstream, ok := NewHTTPUpstream(nil).(*httpUpstreamService)
				require.True(t, ok)
				t.Cleanup(func() {
					for _, entry := range upstream.clients {
						entry.client.CloseIdleConnections()
					}
				})
				ctx := context.Background()
				if native {
					ctx = codexnative.WithScope(ctx, codexnative.Scope{AccountID: 17, AccountUserAgent: "Mac OS 26"})
					ctx = service.WithHTTPUpstreamProfile(ctx, service.HTTPUpstreamProfileOpenAI)
				}
				send := func(requestContext context.Context) int {
					req, err := http.NewRequestWithContext(requestContext, http.MethodPost, first.URL, strings.NewReader(`{"model":"synthetic","input":"local fixture"}`))
					require.NoError(t, err)
					require.NotNil(t, req.GetBody, "fixture must be automatically replayable without the guard")
					resp, err := upstream.Do(req, "", 17, 1)
					require.NoError(t, err)
					_, err = io.Copy(io.Discard, resp.Body)
					require.NoError(t, err)
					require.NoError(t, resp.Body.Close())
					return resp.StatusCode
				}
				require.Equal(t, status, send(service.WithHTTPUpstreamRedirectsDisabled(ctx)))
				require.EqualValues(t, 1, firstCalls.Load())
				require.Zero(t, secondCalls.Load(), "benchmark body must never be resent to the redirect target")
				// The shared client remains unchanged for ordinary business requests.
				require.Equal(t, http.StatusOK, send(ctx))
				require.EqualValues(t, 2, firstCalls.Load())
				require.EqualValues(t, 1, secondCalls.Load())
			})
		}
	}
}
