package service

import (
	"context"
	"crypto/tls"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/codexnative"
	"github.com/stretchr/testify/require"
)

// Unlike the unit trace tests, these requests exercise the production native
// transport and req callbacks against loopback TCP/TLS/proxy peers. No callback
// is invoked manually, and the collector's real destination is never dialed.
func TestCodexTurnStateCollectorNativeTransportDiagnostics(t *testing.T) {
	t.Run("tls_certificate", func(t *testing.T) {
		server := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			t.Error("untrusted TLS certificate must prevent sending an HTTP request")
		}))
		server.Config.ErrorLog = log.New(io.Discard, "", 0)
		server.StartTLS()
		t.Cleanup(server.Close)
		assertCodexCollectorNativeDiagnostic(t, &http.Transport{}, server.URL, "collector_tls_failed", "tls")
	})
	t.Run("tls_timeout", func(t *testing.T) {
		address := codexCollectorDiagnosticTCPPeer(t, func(conn net.Conn) {
			_, _ = io.Copy(io.Discard, conn) // Receive ClientHello without answering.
		})
		assertCodexCollectorNativeDiagnostic(t, &http.Transport{TLSHandshakeTimeout: 100 * time.Millisecond}, "https://"+address, "collector_tls_timeout", "tls")
	})
	t.Run("response_header_timeout", func(t *testing.T) {
		release := make(chan struct{})
		server := httptest.NewTLSServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
			_, _ = io.Copy(io.Discard, request.Body)
			select {
			case <-request.Context().Done():
			case <-release:
			}
		}))
		t.Cleanup(server.Close)
		t.Cleanup(func() { close(release) })
		assertCodexCollectorNativeDiagnostic(t, &http.Transport{
			TLSClientConfig:       &tls.Config{InsecureSkipVerify: true}, // Local fixture only.
			ResponseHeaderTimeout: 100 * time.Millisecond,
		}, server.URL, "collector_response_header_timeout", "response_headers")
	})
	t.Run("connect_proxy_authentication", func(t *testing.T) {
		proxy := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if request.Method != http.MethodConnect {
				t.Error("expected CONNECT proxy negotiation")
			}
			writer.WriteHeader(http.StatusProxyAuthRequired)
		}))
		t.Cleanup(proxy.Close)
		proxyURL, err := url.Parse(proxy.URL)
		require.NoError(t, err)
		// Target is also loopback, even though the rejected tunnel never dials it.
		assertCodexCollectorNativeDiagnostic(t, &http.Transport{Proxy: http.ProxyURL(proxyURL)}, "https://127.0.0.1:1", "collector_proxy_auth_required", "proxy_tunnel")
	})
	t.Run("socks_proxy_authentication", func(t *testing.T) {
		address := codexCollectorDiagnosticTCPPeer(t, func(conn net.Conn) {
			var greeting [2]byte
			if _, err := io.ReadFull(conn, greeting[:]); err != nil {
				return
			}
			if _, err := io.ReadFull(conn, make([]byte, int(greeting[1]))); err != nil {
				return
			}
			_, _ = conn.Write([]byte{5, 255}) // No acceptable authentication method.
		})
		proxyURL, err := url.Parse("socks5://" + address)
		require.NoError(t, err)
		assertCodexCollectorNativeDiagnostic(t, &http.Transport{Proxy: http.ProxyURL(proxyURL)}, "https://127.0.0.1:1", "collector_proxy_auth_required", "proxy_tunnel")
	})
}

func assertCodexCollectorNativeDiagnostic(t *testing.T, base *http.Transport, destination, reason, stage string) {
	t.Helper()
	transport, err := codexnative.NewTransport(codexnative.Linux, base)
	require.NoError(t, err)
	t.Cleanup(func() { transport.(interface{ CloseIdleConnections() }).CloseIdleConnections() })
	target, err := url.Parse(destination)
	require.NoError(t, err)
	account, _ := codexCollectorTransportFixture()
	collector := NewCodexTurnStateHTTPCollector(func(ctx context.Context, input CodexTurnStateCollectRequest, request *http.Request) (*http.Response, error) {
		input.onSend(time.Now())
		request = request.Clone(ctx)
		request.URL, request.Host = target, target.Host
		return transport.RoundTrip(request)
	})
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	result, err := collector.Collect(ctx, CodexTurnStateCollectRequest{Account: account, Model: "gpt-5.6-sol", ProxyID: 2})
	require.Error(t, err)
	var diagnostic *codexTurnStateCollectorTransportError
	require.ErrorAs(t, err, &diagnostic)
	require.Equal(t, reason, diagnostic.code)
	require.Equal(t, stage, diagnostic.stage)
	require.Equal(t, reason, codexTurnStateCollectorFailureReason(result, err))
	require.Empty(t, result.Tokens)
}

func codexCollectorDiagnosticTCPPeer(t *testing.T, serve func(net.Conn)) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(4 * time.Second))
		serve(conn)
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("local diagnostic peer did not stop")
		}
	})
	return listener.Addr().String()
}
