package repository

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestTLSFingerprintClientsIsolateEveryIdentityDimension(t *testing.T) {
	for _, isolation := range []string{config.ConnectionPoolIsolationAccount, config.ConnectionPoolIsolationAccountProxy, config.ConnectionPoolIsolationProxy} {
		t.Run(isolation, func(t *testing.T) {
			svc := NewHTTPUpstream(&config.Config{Gateway: config.GatewayConfig{ConnectionPoolIsolation: isolation}}).(*httpUpstreamService)
			profile := &tlsfingerprint.Profile{Name: "same-name", CipherSuites: []uint16{0x1301, 0x1302}}
			proxy := "http://proxy-user:proxy-password@proxy.local:8080"
			get := func(proxy string, account int64, fingerprint *tlsfingerprint.Profile, upstream service.HTTPUpstreamProfile) *upstreamClientEntry {
				entry, err := svc.getClientEntryWithTLS(proxy, account, 3, fingerprint, upstream, false, false)
				require.NoError(t, err)
				t.Cleanup(entry.client.CloseIdleConnections)
				return entry
			}
			base := get(proxy, 1, profile, service.HTTPUpstreamProfileOpenAI)
			require.Same(t, base, get(proxy+"/", 1, profile.Clone(), service.HTTPUpstreamProfileOpenAI))
			require.NotSame(t, base, get(proxy, 2, profile, service.HTTPUpstreamProfileOpenAI), "proxy-only config must not share fingerprinted accounts")
			require.NotSame(t, base, get(strings.Replace(proxy, "proxy-password", "new-password", 1), 1, profile, service.HTTPUpstreamProfileOpenAI))
			require.NotSame(t, base, get("http://proxy.local:8081", 1, profile, service.HTTPUpstreamProfileOpenAI))
			require.NotSame(t, base, get(proxy, 1, profile, service.HTTPUpstreamProfileCodexAuxiliary))
			require.NotSame(t, base, get(proxy, 1, profile, service.HTTPUpstreamProfileDefault))
			changed := profile.Clone()
			changed.CipherSuites[0], changed.CipherSuites[1] = changed.CipherSuites[1], changed.CipherSuites[0]
			require.NotSame(t, base, get(proxy, 1, changed, service.HTTPUpstreamProfileOpenAI), "same-name profile edits must create a new transport")
			for key := range svc.clients {
				require.NotContains(t, key, "proxy-user")
				require.NotContains(t, key, "proxy-password")
				require.NotContains(t, key, "proxy.local")
			}
		})
	}
}

func TestTLSFingerprintChangedProfileRespectsInflightCacheLimit(t *testing.T) {
	svc := NewHTTPUpstream(&config.Config{Gateway: config.GatewayConfig{MaxUpstreamClients: 1}}).(*httpUpstreamService)
	profile := &tlsfingerprint.Profile{Name: "original"}
	first, err := svc.acquireClientWithTLS("", 7, 2, profile, service.HTTPUpstreamProfileOpenAI)
	require.NoError(t, err)
	t.Cleanup(first.client.CloseIdleConnections)
	profile.Name = "updated"
	_, err = svc.acquireClientWithTLS("", 7, 2, profile, service.HTTPUpstreamProfileOpenAI)
	require.ErrorIs(t, err, errUpstreamClientLimitReached)
	require.Equal(t, int64(1), atomic.LoadInt64(&first.inFlight))
	atomic.AddInt64(&first.inFlight, -1)
	second, err := svc.acquireClientWithTLS("", 7, 2, profile, service.HTTPUpstreamProfileOpenAI)
	require.NoError(t, err)
	t.Cleanup(second.client.CloseIdleConnections)
	require.NotSame(t, first, second)
	require.Len(t, svc.clients, 1)
	atomic.AddInt64(&second.inFlight, -1)
}

func TestTLSFingerprintPoolSettingsStillInvalidateClient(t *testing.T) {
	svc := NewHTTPUpstream(nil).(*httpUpstreamService)
	profile := &tlsfingerprint.Profile{Name: "test"}
	first, err := svc.getClientEntryWithTLS("", 8, 2, profile, service.HTTPUpstreamProfileOpenAI, false, false)
	require.NoError(t, err)
	t.Cleanup(first.client.CloseIdleConnections)
	second, err := svc.getClientEntryWithTLS("", 8, 4, profile, service.HTTPUpstreamProfileOpenAI, false, false)
	require.NoError(t, err)
	t.Cleanup(second.client.CloseIdleConnections)
	require.NotSame(t, first, second)
	require.Equal(t, 4, second.client.Transport.(*http.Transport).MaxConnsPerHost)
}

func TestTLSFingerprintProfileChangeKeepsActiveResponseAlive(t *testing.T) {
	finishResponse := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "before-")
		w.(http.Flusher).Flush()
		select {
		case <-finishResponse:
			_, _ = io.WriteString(w, "after")
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(server.Close)
	svc := NewHTTPUpstream(nil).(*httpUpstreamService)
	profile := &tlsfingerprint.Profile{Name: "before"}
	first, err := svc.acquireClientWithTLS("", 11, 1, profile, service.HTTPUpstreamProfileOpenAI)
	require.NoError(t, err)
	t.Cleanup(first.client.CloseIdleConnections)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	require.NoError(t, err)
	// Exercise the cached transport's response lifetime against a local endpoint;
	// ClientHello immutability is checked separately above against local TLS.
	response, err := first.client.Do(req)
	require.NoError(t, err)
	defer response.Body.Close()
	profile.Name = "after"
	second, err := svc.acquireClientWithTLS("", 11, 1, profile, service.HTTPUpstreamProfileOpenAI)
	require.NoError(t, err)
	t.Cleanup(second.client.CloseIdleConnections)
	require.NotSame(t, first, second)
	require.Len(t, svc.clients, 2)
	require.Equal(t, int64(1), atomic.LoadInt64(&first.inFlight))
	close(finishResponse)
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Equal(t, "before-after", string(body))
	atomic.AddInt64(&first.inFlight, -1)
	atomic.AddInt64(&second.inFlight, -1)
}

func TestTLSFingerprintTransportUsesFrozenClientHello(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	gotCipherSuites := make(chan []uint16, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		server := tls.Server(conn, &tls.Config{GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
			gotCipherSuites <- append([]uint16(nil), hello.CipherSuites...)
			return nil, errors.New("test stops after observing ClientHello")
		}})
		_ = server.Handshake()
	}()
	svc := NewHTTPUpstream(nil).(*httpUpstreamService)
	profile := &tlsfingerprint.Profile{Name: "frozen", CipherSuites: []uint16{0x1301, 0x1302}}
	entry, err := svc.getClientEntryWithTLS("", 9, 1, profile, service.HTTPUpstreamProfileOpenAI, false, false)
	require.NoError(t, err)
	t.Cleanup(entry.client.CloseIdleConnections)
	profile.CipherSuites[0], profile.CipherSuites[1] = profile.CipherSuites[1], profile.CipherSuites[0]
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_, err = entry.client.Transport.(*http.Transport).DialTLSContext(ctx, "tcp", listener.Addr().String())
	require.Error(t, err)
	select {
	case suites := <-gotCipherSuites:
		require.Equal(t, []uint16{0x1301, 0x1302}, suites)
	case <-ctx.Done():
		t.Fatal("local server did not receive ClientHello")
	}
	<-finished
}

func TestTLSFingerprintProxyLogsOmitUserinfo(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	svc := NewHTTPUpstream(nil).(*httpUpstreamService)
	profile := &tlsfingerprint.Profile{Name: "test"}
	for _, proxy := range []string{
		"http://secret-proxy-user:secret-proxy-password@proxy.local:8080",
		"socks5://secret-proxy-user:secret-proxy-password@proxy.local:1080",
	} {
		entry, err := svc.getClientEntryWithTLS(proxy, 10, 1, profile, service.HTTPUpstreamProfileOpenAI, false, false)
		require.NoError(t, err)
		t.Cleanup(entry.client.CloseIdleConnections)
		_, err = svc.getClientEntryWithTLS(proxy, 10, 1, profile, service.HTTPUpstreamProfileOpenAI, false, false)
		require.NoError(t, err)
	}
	req, err := http.NewRequest(http.MethodGet, "https://upstream.invalid", nil)
	require.NoError(t, err)
	_, err = svc.DoWithTLS(req, "http://secret-proxy-user:secret-proxy-password@[bad", 10, 1, profile)
	require.Error(t, err, "invalid proxy must fail before dialing")
	logs := output.String()
	require.Contains(t, logs, "tls_fingerprint_creating_new_client")
	require.Contains(t, logs, "tls_fingerprint_reusing_client")
	require.Contains(t, logs, "tls_fingerprint_acquire_client_failed")
	require.NotContains(t, logs, "secret-proxy-user")
	require.NotContains(t, logs, "secret-proxy-password")
	require.Contains(t, logs, "proxy.local:8080")
	require.Equal(t, "direct", proxyLogIdentity(""))
	require.Equal(t, "invalid", proxyLogIdentity("http://secret-proxy-user:secret-proxy-password@[bad"))
}
