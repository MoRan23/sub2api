package repository

import (
	"context"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/codexnative"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openaicookies"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type upstreamCookieRoundTripFunc func(*http.Request) (*http.Response, error)

func (f upstreamCookieRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestHTTPUpstreamFrozenCookieBundlesAtActualSendBoundary(t *testing.T) {
	for _, native := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "native"}[native], func(t *testing.T) {
			manager := openaicookies.NewManager()
			s := NewHTTPUpstreamWithCookies(nil, manager).(*httpUpstreamService)
			scope := openaicookies.Scope{OwnerAccountID: 31, OSFamily: "windows", AuthorizationGeneration: "grant-a"}
			send := func(proxy, path string, cookieScope openaicookies.Scope, base *openaicookies.Bundle, expected string, learn bool) openaicookies.Bundle {
				ctx := openaicookies.WithScope(context.Background(), cookieScope)
				var attempt *openaicookies.Attempt
				if base != nil {
					ctx, attempt = openaicookies.WithAttempt(openaicookies.WithBundle(ctx, *base))
					defer attempt.Discard()
				}
				nativeScope := codexnative.Scope{AccountID: 31, Purpose: "oauth", AccountUserAgent: "Windows"}
				if native {
					ctx = codexnative.WithScope(ctx, nativeScope)
				}
				request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://chatgpt.com"+path, nil)
				require.NoError(t, err)
				request.Header.Set("User-Agent", "codex-tui/0.155.1 (Windows 10.0; x86_64)")
				request.Header.Set("Cookie", "original=preserved-in-bypass")
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
						header.Add("Set-Cookie", "__oailb=synthetic-route; Path=/; Secure; Max-Age=600")
					}
					return &http.Response{StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(strings.NewReader("{}")), Request: r}, nil
				})
				response, err := s.Do(request, proxy, 31, 2)
				require.NoError(t, err)
				require.NoError(t, response.Body.Close())
				require.Zero(t, atomic.LoadInt64(&entry.inFlight))
				require.Equal(t, "original=preserved-in-bypass", request.Header.Get("Cookie"), "physical transport must not mutate the original request")
				if attempt == nil {
					return openaicookies.Bundle{}
				}
				bundle, err := attempt.Snapshot(time.Now().Add(openaicookies.BundleLifetime))
				require.NoError(t, err)
				return bundle
			}
			// Auxiliary responses cannot feed a later model's cookie snapshot.
			send("", "/backend-api/plugins/list", scope, nil, "original=preserved-in-bypass", true)
			fresh := openaicookies.Bundle{}
			bundle := send("", "/backend-api/codex/responses", scope, &fresh, "", true)
			send("http://proxy.invalid:8080", "/backend-api/codex/responses", scope, &bundle, "__oailb=synthetic-route", false)
			other := scope
			other.OSFamily = "linux"
			send("", "/backend-api/codex/responses", other, &bundle, "__oailb=synthetic-route", false)
			send("", "/backend-api/codex/responses", other, &fresh, "", false)
		})
	}
}

func TestHTTPUpstreamCookieBypassPreservesOriginalClientPolicy(t *testing.T) {
	service := NewHTTPUpstreamWithCookies(nil, openaicookies.NewManager()).(*httpUpstreamService)
	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	client := &http.Client{Jar: jar}
	ctx := openaicookies.Bypass(openaicookies.WithBundle(context.Background(), openaicookies.Bundle{}))
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://chatgpt.com/", nil)
	require.NoError(t, err)
	require.Same(t, client, service.OpenAICookieClient(client, request))
	require.Same(t, jar, client.Jar)
}
