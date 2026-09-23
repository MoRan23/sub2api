package repository

import (
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/codexnative"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// A model terminal event does not imply transport EOF. Exercise the actual
// native HTTP transport, including the decompressor, rather than only testing
// the shared cancellation wrapper with the standard Go transport.
func TestNativeUpstreamCompletedEventCloseReleasesAttempt(t *testing.T) {
	for _, ua := range []string{"codex-tui/0.155.1 (Windows NT 10.0; x86_64)", "codex-tui/0.155.1 (Mac OS 26.0; arm64)", "codex-tui/0.155.1 (Linux; x86_64)"} {
		for _, encoding := range []string{"plain", "gzip"} {
			for _, concurrentRead := range []bool{false, true} {
				name := ua + "/" + encoding
				if concurrentRead {
					name += "/concurrent-read"
				}
				t.Run(name, func(t *testing.T) {
					const terminal = "data: {\"type\":\"response.completed\"}\n\n"
					cancelled := make(chan struct{})
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if r.URL.Path == "/next" {
							_, _ = io.WriteString(w, "next response")
							return
						}
						w.Header().Set("Content-Type", "text/event-stream")
						if encoding == "gzip" {
							w.Header().Set("Content-Encoding", "gzip")
							writer := gzip.NewWriter(w)
							_, _ = io.WriteString(writer, terminal)
							_ = writer.Flush()
							defer func() { _ = writer.Close() }()
						} else {
							_, _ = io.WriteString(w, terminal)
						}
						_ = http.NewResponseController(w).Flush()
						<-r.Context().Done()
						close(cancelled)
					}))
					defer server.Close()
					defer server.CloseClientConnections()
					s, ok := NewHTTPUpstream(nil).(*httpUpstreamService)
					require.True(t, ok)
					ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
					defer cancel()
					ctx = codexnative.WithScope(ctx, codexnative.Scope{AccountID: 9, Purpose: "oauth"})
					ctx = service.WithHTTPUpstreamProfile(ctx, service.HTTPUpstreamProfileOpenAI)
					request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
					require.NoError(t, err)
					request.Header.Set("User-Agent", ua)
					if encoding == "gzip" {
						request.Header.Set("Accept-Encoding", "gzip")
					}
					response, err := s.Do(request, "", 9, 1)
					require.NoError(t, err)
					prefix := make([]byte, len(terminal))
					_, err = io.ReadFull(response.Body, prefix)
					require.NoError(t, err)
					require.Equal(t, terminal, string(prefix))
					readDone := make(chan struct{})
					if concurrentRead {
						readStarted := make(chan struct{})
						response.Body = &notifyReadCloser{ReadCloser: response.Body, started: readStarted}
						go func() { _, _ = io.Copy(io.Discard, response.Body); close(readDone) }()
						<-readStarted
					} else {
						close(readDone)
					}
					closed := make(chan struct{})
					go func() {
						var group sync.WaitGroup
						for range 4 {
							group.Go(func() { _ = response.Body.Close() })
						}
						group.Wait()
						close(closed)
					}()
					for _, done := range []<-chan struct{}{closed, readDone, cancelled} {
						select {
						case <-done:
						case <-time.After(3 * time.Second):
							t.Fatal("native response Close must cancel the attempt and unblock its reader")
						}
					}
					require.NoError(t, ctx.Err(), "closing an attempt must not cancel its caller")
					requireNoUpstreamInFlight(t, s)
					next := request.Clone(ctx)
					next.URL.Path = "/next"
					response, err = s.Do(next, "", 9, 1)
					require.NoError(t, err)
					body, err := io.ReadAll(response.Body)
					require.NoError(t, err)
					require.Equal(t, "next response", string(body))
					require.NoError(t, response.Body.Close())
					requireNoUpstreamInFlight(t, s)
					for _, entry := range s.clients {
						entry.client.CloseIdleConnections()
					}
				})
			}
		}
	}
}

func TestNativeUpstreamCompletedBodyKeepsConnection(t *testing.T) {
	for _, encoding := range []string{"plain", "gzip"} {
		t.Run(encoding, func(t *testing.T) {
			peers := make(chan string, 3)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				peers <- r.RemoteAddr
				if encoding == "gzip" {
					w.Header().Set("Content-Encoding", "gzip")
					writer := gzip.NewWriter(w)
					_, _ = io.WriteString(writer, "completed body")
					_ = writer.Close()
				} else {
					_, _ = io.WriteString(w, "completed body")
				}
			}))
			defer server.Close()
			s, ok := NewHTTPUpstream(nil).(*httpUpstreamService)
			require.True(t, ok)
			ctx := codexnative.WithScope(t.Context(), codexnative.Scope{AccountID: 9, Purpose: "oauth"})
			var firstPeer string
			for range 3 {
				request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
				require.NoError(t, err)
				request.Header.Set("User-Agent", "codex-tui/0.155.1 (Windows NT 10.0; x86_64)")
				request.Header.Set("Accept-Encoding", encoding)
				response, err := s.Do(request, "", 9, 1)
				require.NoError(t, err)
				body, err := io.ReadAll(response.Body)
				require.NoError(t, err)
				require.Equal(t, "completed body", string(body))
				require.NoError(t, response.Body.Close())
				peer := <-peers
				if firstPeer == "" {
					firstPeer = peer
				} else {
					require.Equal(t, firstPeer, peer, "completed native responses must remain reusable")
				}
			}
			require.NoError(t, ctx.Err())
			requireNoUpstreamInFlight(t, s)
			for _, entry := range s.clients {
				entry.client.CloseIdleConnections()
			}
		})
	}
}
