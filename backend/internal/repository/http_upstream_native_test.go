package repository

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/codexnative"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestNativeUpstreamPoolIdentityIsolation(t *testing.T) {
	for _, mode := range []string{config.ConnectionPoolIsolationAccount, config.ConnectionPoolIsolationProxy, config.ConnectionPoolIsolationAccountProxy} {
		t.Run(mode, func(t *testing.T) {
			s := NewHTTPUpstream(&config.Config{Gateway: config.GatewayConfig{ConnectionPoolIsolation: mode}}).(*httpUpstreamService)
			get := func(account int64, proxy, ua, purpose string, profile service.HTTPUpstreamProfile) *upstreamClientEntry {
				scope := codexnative.Scope{AccountID: account, Purpose: purpose}
				e, err := s.acquireNativeClient(proxy, account, 2, profile, scope, codexnative.Resolve(ua, scope))
				require.NoError(t, err)
				atomic.AddInt64(&e.inFlight, -1)
				t.Cleanup(e.client.CloseIdleConnections)
				return e
			}
			base := get(1, "http://user:secret@proxy.test:8080", "Windows", "oauth", service.HTTPUpstreamProfileOpenAI)
			require.Same(t, base, get(1, "http://user:secret@proxy.test:8080/", "Windows 11", "oauth", service.HTTPUpstreamProfileOpenAI))
			require.NotSame(t, base, get(2, "http://user:secret@proxy.test:8080", "Windows", "oauth", service.HTTPUpstreamProfileOpenAI))
			require.NotSame(t, base, get(1, "http://user:other@proxy.test:8080", "Windows", "oauth", service.HTTPUpstreamProfileOpenAI))
			require.NotSame(t, base, get(1, "http://user:secret@proxy.test:8080", "Mac OS", "oauth", service.HTTPUpstreamProfileOpenAI))
			require.NotSame(t, base, get(1, "http://user:secret@proxy.test:8080", "Windows", "auth", service.HTTPUpstreamProfileOpenAI))
			require.NotSame(t, base, get(1, "http://user:secret@proxy.test:8080", "Windows", "oauth", service.HTTPUpstreamProfileCodexAuxiliary))
			for key := range s.clients {
				require.NotContains(t, key, "secret")
				require.NotContains(t, key, "proxy.test")
			}
		})
	}
}

func TestNativeUpstreamDoesNotEvictActivePools(t *testing.T) {
	s := NewHTTPUpstream(&config.Config{Gateway: config.GatewayConfig{MaxUpstreamClients: 1}}).(*httpUpstreamService)
	scope := codexnative.Scope{AccountID: 1}
	first, err := s.acquireNativeClient("", 1, 1, service.HTTPUpstreamProfileOpenAI, scope, codexnative.Resolve("Windows", scope))
	require.NoError(t, err)
	t.Cleanup(first.client.CloseIdleConnections)
	_, err = s.acquireNativeClient("", 1, 1, service.HTTPUpstreamProfileOpenAI, scope, codexnative.Resolve("Linux", scope))
	require.ErrorIs(t, err, errUpstreamClientLimitReached)
	atomic.AddInt64(&first.inFlight, -1)
	second, err := s.acquireNativeClient("", 1, 1, service.HTTPUpstreamProfileOpenAI, scope, codexnative.Resolve("Linux", scope))
	require.NoError(t, err)
	t.Cleanup(second.client.CloseIdleConnections)
	atomic.AddInt64(&second.inFlight, -1)
	require.NotSame(t, first, second)
	require.Len(t, s.clients, 1)
}

func TestNativeUpstreamPreservesRequestAndSelectsEachRedirect(t *testing.T) {
	var receivedUA, receivedBody, receivedIdentity string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "/final", http.StatusTemporaryRedirect)
			return
		}
		body, _ := io.ReadAll(r.Body)
		receivedUA = r.UserAgent()
		receivedBody = string(body)
		receivedIdentity = r.Header.Get("Session-Id")
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()
	s := NewHTTPUpstream(nil).(*httpUpstreamService)
	ctx := codexnative.WithScope(context.Background(), codexnative.Scope{AccountID: 9, AccountUserAgent: "Mac OS 26"})
	ctx = service.WithHTTPUpstreamProfile(ctx, service.HTTPUpstreamProfileOpenAI)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/redirect", strings.NewReader(`{"model":"test","input":"hello"}`))
	require.NoError(t, err)
	req.Header.Set("User-Agent", "OTel-OTLP-Exporter-Rust/0.31.0")
	req.Header.Set("Session-Id", "actual-root")
	resp, err := s.Do(req, "", 9, 2)
	require.NoError(t, err)
	_, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, req.UserAgent(), receivedUA)
	require.Equal(t, `{"model":"test","input":"hello"}`, receivedBody)
	require.Equal(t, "actual-root", receivedIdentity)
	require.Len(t, s.clients, 1)
	for _, entry := range s.clients {
		require.Equal(t, upstreamProtocolModeNativeHTTP, entry.protocolMode)
		require.Zero(t, atomic.LoadInt64(&entry.inFlight))
		entry.client.CloseIdleConnections()
	}
}

func TestNativeCollectorUsesFreshConnectionsWithoutCachingClients(t *testing.T) {
	for _, ua := range []string{"codex-tui/0.155.1 (Windows NT 10.0; x86_64)", "codex-tui/0.155.1 (Mac OS 26.0; arm64)", "codex-tui/0.155.1 (Linux; x86_64)"} {
		t.Run(ua, func(t *testing.T) {
			peers := make(chan string, 2)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				peers <- r.RemoteAddr
				_, _ = io.WriteString(w, "ok")
			}))
			defer server.Close()
			s := NewHTTPUpstream(nil).(*httpUpstreamService)
			ctx := codexnative.WithScope(context.Background(), codexnative.Scope{AccountID: 9, Purpose: "turn_state_collector"})
			ctx = service.WithHTTPUpstreamProfile(ctx, service.HTTPUpstreamProfileCodexAuxiliary)
			for range 2 {
				request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
				require.NoError(t, err)
				request.Header.Set("User-Agent", ua)
				response, err := s.Do(request, "", 9, 2)
				require.NoError(t, err)
				_, err = io.Copy(io.Discard, response.Body)
				require.NoError(t, err)
				require.NoError(t, response.Body.Close())
			}
			require.NotEqual(t, <-peers, <-peers, "collection attempts must not share a connection")
			require.Empty(t, s.clients, "one-shot collection transports must not grow the shared cache")
		})
	}
}
