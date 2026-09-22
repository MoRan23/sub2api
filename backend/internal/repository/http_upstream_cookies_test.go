package repository

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/codexnative"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openaicookies"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type upstreamCookieMemoryStore struct {
	mu       sync.Mutex
	entries  map[openaicookies.Scope]map[string]openaicookies.Entry
	versions map[openaicookies.Scope]map[string]int64
	sequence int64
}

func (s *upstreamCookieMemoryStore) Load(_ context.Context, scope openaicookies.Scope) (openaicookies.Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var entries []openaicookies.Entry
	for _, entry := range s.entries[scope] {
		entries = append(entries, entry)
	}
	versions := make(map[string]int64)
	for key, version := range s.versions[scope] {
		versions[key] = version
	}
	return openaicookies.Snapshot{Entries: entries, Versions: versions}, nil
}

func (s *upstreamCookieMemoryStore) Merge(_ context.Context, scope openaicookies.Scope, mutations []openaicookies.Mutation) (map[string]int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.entries == nil {
		s.entries = make(map[openaicookies.Scope]map[string]openaicookies.Entry)
	}
	if s.entries[scope] == nil {
		s.entries[scope] = make(map[string]openaicookies.Entry)
	}
	if s.versions == nil {
		s.versions = make(map[openaicookies.Scope]map[string]int64)
	}
	if s.versions[scope] == nil {
		s.versions[scope] = make(map[string]int64)
	}
	result := make(map[string]int64)
	for _, mutation := range mutations {
		s.sequence++
		s.versions[scope][mutation.Key] = s.sequence
		result[mutation.Key] = s.sequence
		if mutation.Entry == nil {
			delete(s.entries[scope], mutation.Key)
		} else {
			s.entries[scope][mutation.Key] = *mutation.Entry
		}
	}
	return result, nil
}

type upstreamCookieRoundTripFunc func(*http.Request) (*http.Response, error)

func (f upstreamCookieRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Exercise the actual outer Client.Do paths. Native dispatch uses inner
// Transport.RoundTrip, so a jar installed on that inner client would fail here.
func TestHTTPUpstreamCookiesAtActualSendBoundary(t *testing.T) {
	for _, native := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "native"}[native], func(t *testing.T) {
			manager := openaicookies.NewManager(&upstreamCookieMemoryStore{})
			s := NewHTTPUpstreamWithCookies(nil, manager).(*httpUpstreamService)
			scope := openaicookies.Scope{OwnerAccountID: 31, OSFamily: "windows", AuthorizationGeneration: "grant-a"}
			send := func(proxy, path string, cookieScope openaicookies.Scope, expected string, learn bool) {
				ctx := openaicookies.WithScope(context.Background(), cookieScope)
				nativeScope := codexnative.Scope{AccountID: 31, Purpose: "oauth", AccountUserAgent: "Windows"}
				if native {
					ctx = codexnative.WithScope(ctx, nativeScope)
				}
				request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://chatgpt.com"+path, nil)
				require.NoError(t, err)
				request.Header.Set("User-Agent", "codex-tui/0.155.1 (Windows 10.0; x86_64)")
				request.Header.Set("Cookie", "__oailb=untrusted-client-value")
				var entry *upstreamClientEntry
				if native {
					entry, err = s.acquireNativeClient(proxy, 31, 2, service.HTTPUpstreamProfileDefault, nativeScope, codexnative.Resolve(request.UserAgent(), nativeScope))
				} else {
					entry, err = s.acquireClient(proxy, 31, 2)
				}
				require.NoError(t, err)
				atomic.AddInt64(&entry.inFlight, -1)
				entry.client.CloseIdleConnections()
				entry.client.Transport = upstreamCookieRoundTripFunc(func(r *http.Request) (*http.Response, error) {
					require.Equal(t, expected, r.Header.Get("Cookie"))
					header := make(http.Header)
					if learn {
						header.Add("Set-Cookie", "__oailb=synthetic-routing-value; Path=/; Secure; Max-Age=600")
					}
					return &http.Response{StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(strings.NewReader("{}")), Request: r}, nil
				})
				response, err := s.Do(request, proxy, 31, 2)
				require.NoError(t, err)
				require.NoError(t, response.Body.Close())
				require.Zero(t, atomic.LoadInt64(&entry.inFlight))
				require.Equal(t, "__oailb=untrusted-client-value", request.Header.Get("Cookie"), "transport must clone rather than mutate caller headers")
			}
			send("", "/backend-api/plugins/list", scope, "", true)
			send("http://proxy.invalid:8080", "/backend-api/codex/responses", scope, "__oailb=synthetic-routing-value", false)
			other := scope
			other.OSFamily = "linux"
			send("", "/backend-api/codex/responses", other, "", false)
			other = scope
			other.AuthorizationGeneration = "grant-b"
			send("", "/backend-api/codex/responses", other, "", false)
		})
	}
}
