package repository

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/codexnative"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type upstreamTestHostValidator struct {
	validate func(context.Context, string) error
	closed   int
}

func (v *upstreamTestHostValidator) Validate(ctx context.Context, host string) error {
	return v.validate(ctx, host)
}

func (v *upstreamTestHostValidator) CloseIdleConnections() { v.closed++ }

// Prepare the real cached-client path, replacing only DNS and the final network
// transport. Proxy selection, redirects and in-flight accounting stay real.
func proxyDNSRequestPath(t *testing.T, mode, proxy string, validator *upstreamTestHostValidator) (*httpUpstreamService, *upstreamClientEntry, *http.Request, func(*http.Request) (*http.Response, error)) {
	t.Helper()
	s, ok := NewHTTPUpstream(nil).(*httpUpstreamService)
	require.True(t, ok)
	s.newHostValidator = func(selected string) (upstreamHostValidator, error) {
		require.Equal(t, proxy, selected)
		return validator, nil
	}
	ctx := service.WithHTTPUpstreamPublicHostsOnly(t.Context())
	scope := codexnative.Scope{AccountID: 91, Purpose: "oauth", AccountUserAgent: "Windows"}
	if mode == "native" {
		ctx = codexnative.WithScope(ctx, scope)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://target.example/start", nil)
	require.NoError(t, err)
	req.Header.Set("User-Agent", "codex-tui/0.163.0 (Windows 10.0; x86_64)")
	profile := &tlsfingerprint.Profile{Name: "proxy-dns-test"}
	var entry *upstreamClientEntry
	switch mode {
	case "native":
		entry, err = s.acquireNativeClient(proxy, 91, 1, service.HTTPUpstreamProfileDefault, scope, codexnative.Resolve(req.UserAgent(), scope))
	case "tls":
		entry, err = s.acquireClientWithTLS(proxy, 91, 1, profile, service.HTTPUpstreamProfileDefault)
	default:
		entry, err = s.acquireClient(proxy, 91, 1)
	}
	require.NoError(t, err)
	atomic.AddInt64(&entry.inFlight, -1)
	entry.client.CloseIdleConnections()
	t.Cleanup(func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		for key, entry := range s.clients {
			s.removeClientLocked(key, entry)
		}
	})
	return s, entry, req, func(req *http.Request) (*http.Response, error) {
		if mode == "tls" {
			return s.DoWithTLS(req, proxy, 91, 1, profile)
		}
		return s.Do(req, proxy, 91, 1)
	}
}

func TestHTTPUpstreamProxyDNSFailureStopsRequestAndReleasesClient(t *testing.T) {
	for _, mode := range []string{"ordinary", "tls", "native"} {
		for _, proxy := range []string{"http://127.0.0.1:12345", "socks5://127.0.0.1:12345", "ss://aes-128-gcm:test@127.0.0.1:12345"} {
			t.Run(mode+"/"+proxy, func(t *testing.T) {
				failure := errors.New("proxied DNS unavailable")
				var checks, sends int
				validator := &upstreamTestHostValidator{validate: func(ctx context.Context, host string) error {
					checks++
					require.Equal(t, "target.example", host)
					require.NoError(t, ctx.Err())
					return failure
				}}
				_, entry, req, send := proxyDNSRequestPath(t, mode, proxy, validator)
				entry.client.Transport = upstreamBoundaryRoundTripFunc(func(req *http.Request) (*http.Response, error) {
					sends++
					return &http.Response{StatusCode: 200, Body: http.NoBody, Request: req}, nil
				})
				for range 2 {
					response, err := send(req)
					require.ErrorIs(t, err, failure)
					require.Nil(t, response)
					require.Zero(t, atomic.LoadInt64(&entry.inFlight))
				}
				require.Equal(t, 2, checks, "a failed lookup must not suppress the next check")
				require.Zero(t, sends, "must not contact the business target or fall back to direct")
			})
		}
	}
}

func TestHTTPUpstreamProxyDNSValidatesRedirectWithSameProxy(t *testing.T) {
	for _, mode := range []string{"ordinary", "tls", "native"} {
		t.Run(mode, func(t *testing.T) {
			failure := errors.New("redirect resolved to private IP")
			var checked, sent []string
			validator := &upstreamTestHostValidator{validate: func(ctx context.Context, host string) error {
				checked = append(checked, host)
				if host == "private.example" {
					return failure
				}
				return nil
			}}
			_, entry, req, send := proxyDNSRequestPath(t, mode, "ss://aes-128-gcm:test@127.0.0.1:12345", validator)
			entry.client.Transport = upstreamBoundaryRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				sent = append(sent, req.URL.Hostname())
				return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": {"https://private.example/next"}}, Body: http.NoBody, Request: req}, nil
			})
			response, err := send(req)
			require.ErrorIs(t, err, failure)
			if response != nil {
				require.NoError(t, response.Body.Close())
			}
			require.Equal(t, []string{"target.example", "private.example"}, checked)
			require.Equal(t, []string{"target.example"}, sent)
			require.Zero(t, atomic.LoadInt64(&entry.inFlight))
		})
	}
}

func TestHTTPUpstreamProxyDNSCancellationAndPoolLifecycle(t *testing.T) {
	for _, mode := range []string{"ordinary", "tls", "native"} {
		t.Run(mode, func(t *testing.T) {
			var cancel context.CancelFunc
			validator := &upstreamTestHostValidator{validate: func(ctx context.Context, host string) error {
				cancel()
				<-ctx.Done()
				return ctx.Err()
			}}
			s, entry, req, send := proxyDNSRequestPath(t, mode, "socks5://127.0.0.1:12345", validator)
			ctx, cancelContext := context.WithCancel(req.Context())
			cancel = cancelContext
			defer cancel()
			req = req.WithContext(ctx)
			entry.client.Transport = upstreamBoundaryRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				t.Fatal("cancelled DNS must not send the business request")
				return nil, nil
			})
			_, err := send(req)
			require.ErrorIs(t, err, context.Canceled)
			require.Zero(t, atomic.LoadInt64(&entry.inFlight))
			s.mu.Lock()
			for key, cached := range s.clients {
				s.removeClientLocked(key, cached)
			}
			s.mu.Unlock()
			require.Equal(t, 1, validator.closed)
		})
	}
}

func TestHTTPUpstreamProxyDNSValidatorsFollowClientIsolation(t *testing.T) {
	s, ok := NewHTTPUpstream(nil).(*httpUpstreamService)
	require.True(t, ok)
	var routes []string
	s.newHostValidator = func(proxy string) (upstreamHostValidator, error) {
		routes = append(routes, proxy)
		return &upstreamTestHostValidator{validate: func(context.Context, string) error { return nil }}, nil
	}
	first := mustGetOrCreateClient(t, s, "http://127.0.0.1:12345", 1, 1)
	again := mustGetOrCreateClient(t, s, "http://127.0.0.1:12345", 1, 1)
	second := mustGetOrCreateClient(t, s, "http://127.0.0.1:12346", 1, 1)
	require.Same(t, first.hostValidator, again.hostValidator)
	require.NotSame(t, first.hostValidator, second.hostValidator)
	require.Equal(t, []string{"http://127.0.0.1:12345", "http://127.0.0.1:12346"}, routes)
}
