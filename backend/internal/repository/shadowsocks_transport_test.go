package repository

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/codexnative"
	"github.com/Wei-Shaw/sub2api/internal/pkg/shadowsockstest"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/service"
	M "github.com/sagernet/sing/common/metadata"
	"github.com/stretchr/testify/require"
)

func TestShadowsocksTransportEntrypoints(t *testing.T) {
	password := base64.StdEncoding.EncodeToString([]byte("01234567890123456789012345678901"))
	for _, entrypoint := range []string{"ordinary", "fingerprint", "native", "shared_req", "native_req", "claude_oauth", "grok_oauth"} {
		t.Run(entrypoint, func(t *testing.T) {
			observed := make(chan string, 1)
			proxy := shadowsockstest.Start(t, "2022-blake3-aes-256-gcm", password, func(conn net.Conn, destination M.Socksaddr) error {
				observed <- destination.String()
				request, err := http.ReadRequest(bufio.NewReader(conn))
				if err != nil {
					return err
				}
				_ = request.Body.Close()
				_, err = io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok")
				return err
			})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			ctx = codexnative.WithScope(ctx, codexnative.Scope{AccountID: 41, AccountUserAgent: "Linux", Purpose: "test"})
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://ss-target.invalid/probe", nil)
			require.NoError(t, err)
			var client *http.Client
			switch entrypoint {
			case "ordinary", "fingerprint":
				var transport *http.Transport
				if entrypoint == "ordinary" {
					transport, err = buildUpstreamTransport(defaultPoolSettings(nil), proxy, upstreamProtocolModeDefault)
				} else {
					transport, err = buildUpstreamTransportWithTLSFingerprint(defaultPoolSettings(nil), proxy, nil)
				}
				require.NoError(t, err)
				require.Nil(t, transport.Proxy)
				client = &http.Client{Transport: transport}
			case "native":
				upstream, ok := NewHTTPUpstream(nil).(*httpUpstreamService)
				require.True(t, ok)
				scope, _ := codexnative.ScopeFromContext(ctx)
				entry, createErr := upstream.acquireNativeClient(proxy.String(), 41, 1, service.HTTPUpstreamProfileOpenAI, scope, codexnative.Resolve("Linux", scope))
				require.NoError(t, createErr)
				client = entry.client
			case "shared_req", "native_req":
				r, createErr := getSharedReqClient(reqClientOptions{ProxyURL: proxy.String(), Timeout: 5 * time.Second, Impersonate: true, OpenAINativeHTTP: entrypoint == "native_req"})
				require.NoError(t, createErr)
				require.Nil(t, r.GetTransport().Proxy)
				require.NotNil(t, r.GetTransport().DialContext)
				client = r.GetClient()
			case "claude_oauth":
				r, createErr := createReqClient(proxy.String())
				require.NoError(t, createErr)
				require.Nil(t, r.GetTransport().Proxy)
				client = r.GetClient()
			case "grok_oauth":
				client, err = createGrokHTTPClient(proxy.String(), true)
				require.NoError(t, err)
			}
			t.Cleanup(client.CloseIdleConnections)
			response, err := client.Do(request)
			require.NoError(t, err)
			defer func() { _ = response.Body.Close() }()
			body, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			require.Equal(t, "ok", string(body))
			require.Equal(t, "ss-target.invalid:80", <-observed, "destination name must be sent inside the SS tunnel")
		})
	}
}

func TestShadowsocksFingerprintPreservesTLSValidation(t *testing.T) {
	// The TLS server certificate is deliberately not trusted. Reaching its
	// ClientHello proves the SS route is used; certificate rejection proves
	// enabling the proxy has not disabled TLS verification.
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	hello := make(chan struct{}, 1)
	server.TLS = &tls.Config{GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) {
		select {
		case hello <- struct{}{}:
		default:
		}
		return nil, nil
	}}
	server.StartTLS()
	defer server.Close()
	proxy := shadowsockstest.Start(t, "aes-128-gcm", "test-password", func(conn net.Conn, _ M.Socksaddr) error {
		upstream, err := net.DialTimeout("tcp", server.Listener.Addr().String(), time.Second)
		if err != nil {
			return err
		}
		defer func() { _ = upstream.Close() }()
		done := make(chan struct{})
		go func() {
			_, _ = io.Copy(upstream, conn)
			_ = upstream.Close()
			close(done)
		}()
		_, err = io.Copy(conn, upstream)
		_ = conn.Close()
		<-done
		return err
	})
	transport, err := buildUpstreamTransportWithTLSFingerprint(defaultPoolSettings(nil), proxy, &tlsfingerprint.Profile{Name: "test"})
	require.NoError(t, err)
	t.Cleanup(transport.CloseIdleConnections)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	target, err := url.Parse(server.URL)
	require.NoError(t, err)
	// An otherwise unresolvable hostname detects accidental direct routing.
	target.Host = net.JoinHostPort("ss-target.invalid", target.Port())
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	require.NoError(t, err)
	_, err = (&http.Client{Transport: transport}).Do(request)
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "certificate") || strings.Contains(err.Error(), "x509"), err.Error())
	select {
	case <-hello:
	default:
		t.Fatal("fingerprint TLS handshake did not reach the proxied server")
	}
}
