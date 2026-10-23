package proxyutil

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/shadowsockstest"
	"github.com/sagernet/sing-shadowsocks/shadowaead"
	M "github.com/sagernet/sing/common/metadata"
	"github.com/stretchr/testify/require"
)

func ssTestPassword(method string) string {
	if method == "2022-blake3-aes-128-gcm" {
		return base64.StdEncoding.EncodeToString([]byte("0123456789abcdef"))
	}
	if strings.HasPrefix(method, "2022-") {
		return base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	}
	return "test-password"
}

func TestShadowsocksEncryptedTCPMethods(t *testing.T) {
	for _, method := range []string{"aes-128-gcm", "aes-192-gcm", "aes-256-gcm", "chacha20-ietf-poly1305", "2022-blake3-aes-128-gcm", "2022-blake3-aes-256-gcm", "2022-blake3-chacha20-poly1305"} {
		t.Run(method, func(t *testing.T) {
			destinations := make(chan string, 8)
			proxyURL := shadowsockstest.Start(t, method, ssTestPassword(method), func(conn net.Conn, destination M.Socksaddr) error {
				destinations <- destination.String()
				_, err := io.Copy(conn, conn)
				return err
			})
			transport := &http.Transport{Proxy: http.ProxyFromEnvironment}
			require.NoError(t, ConfigureTransportProxy(transport, proxyURL))
			require.Nil(t, transport.Proxy)
			// A nonresolving domain proves the client forwards it without local DNS.
			for _, destination := range []string{"no-dns.invalid:443", "192.0.2.42:80", "[2001:db8::42]:443"} {
				ctx, cancel := context.WithCancel(context.Background())
				conn, err := transport.DialContext(ctx, "tcp", destination)
				require.NoError(t, err)
				cancel() // A pooled connection must survive its initiating context.
				require.NoError(t, conn.SetDeadline(time.Now().Add(3*time.Second)))
				payload := []byte("encrypted hello / " + method)
				_, err = conn.Write(payload)
				require.NoError(t, err)
				response := make([]byte, len(payload))
				_, err = io.ReadFull(conn, response)
				require.NoError(t, err)
				require.Equal(t, payload, response)
				_ = conn.Close()
				require.Equal(t, destination, <-destinations)
			}
		})
	}
}

func TestShadowsocksWrongCredentialsFailClosed(t *testing.T) {
	for _, method := range []string{"aes-256-gcm", "2022-blake3-aes-256-gcm"} {
		t.Run(method, func(t *testing.T) {
			accepted := make(chan struct{}, 1)
			proxyURL := shadowsockstest.Start(t, method, ssTestPassword(method), func(net.Conn, M.Socksaddr) error {
				accepted <- struct{}{}
				return nil
			})
			wrong := "wrong-password"
			if strings.HasPrefix(method, "2022-") {
				wrong = base64.StdEncoding.EncodeToString([]byte(strings.Repeat("z", 32)))
			}
			proxyURL.User = url.UserPassword(method, wrong)
			dial, err := NewShadowsocksDialer(proxyURL)
			require.NoError(t, err)
			conn, err := dial(context.Background(), "tcp", "no-dns.invalid:443")
			if err == nil {
				defer func() { _ = conn.Close() }()
				require.NoError(t, conn.SetReadDeadline(time.Now().Add(3*time.Second)))
				_, err = conn.Read(make([]byte, 1))
			}
			require.Error(t, err)
			require.NotContains(t, err.Error(), wrong)
			select {
			case <-accepted:
				t.Fatal("unauthenticated request reached destination handler")
			default:
			}
		})
	}
}

func TestShadowsocksConcurrentDialerReuse(t *testing.T) {
	method := "2022-blake3-aes-256-gcm"
	proxyURL := shadowsockstest.Start(t, method, ssTestPassword(method), func(conn net.Conn, _ M.Socksaddr) error {
		_, err := io.Copy(conn, conn)
		return err
	})
	dial, err := NewShadowsocksDialer(proxyURL)
	require.NoError(t, err)
	results := make(chan error, 8)
	for i := 0; i < cap(results); i++ {
		go func() {
			conn, err := dial(context.Background(), "tcp", "no-dns.invalid:443")
			if err != nil {
				results <- err
				return
			}
			defer func() { _ = conn.Close() }()
			if err = conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
				results <- err
				return
			}
			if _, err = conn.Write([]byte("hello")); err == nil {
				response := make([]byte, 5)
				_, err = io.ReadFull(conn, response)
				if err == nil && string(response) != "hello" {
					err = fmt.Errorf("unexpected echo")
				}
			}
			results <- err
		}()
	}
	for i := 0; i < cap(results); i++ {
		require.NoError(t, <-results)
	}
}

type ssBlockingHandshakeConn struct {
	net.Conn
	started chan struct{}
	once    sync.Once
}

func (c *ssBlockingHandshakeConn) Write(data []byte) (int, error) {
	c.once.Do(func() { close(c.started) })
	return c.Conn.Write(data)
}

func TestShadowsocksHandshakeCancellationAndDeadline(t *testing.T) {
	for _, explicitCancel := range []bool{false, true} {
		t.Run(map[bool]string{true: "cancel", false: "deadline"}[explicitCancel], func(t *testing.T) {
			client, server := net.Pipe()
			defer func() { _ = server.Close() }()
			wrapped := &ssBlockingHandshakeConn{Conn: client, started: make(chan struct{})}
			method, err := shadowaead.New("aes-128-gcm", nil, "password")
			require.NoError(t, err)
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			result := make(chan error, 1)
			go func() {
				_, err := finishShadowsocksHandshake(ctx, wrapped, method, M.ParseSocksaddr("no-dns.invalid:443"))
				result <- err
			}()
			<-wrapped.started
			if explicitCancel {
				cancel()
			}
			select {
			case err := <-result:
				require.Error(t, err)
			case <-time.After(3 * time.Second):
				t.Fatal("handshake did not unblock")
			}
			// Cancellation closes the underlying connection, not only the wrapper.
			_ = server.SetReadDeadline(time.Now().Add(time.Second))
			_, err = server.Read(make([]byte, 1))
			require.ErrorIs(t, err, io.EOF)
		})
	}
}

func TestShadowsocksValidationAndCancellationBeforeDial(t *testing.T) {
	for _, tc := range []struct{ method, password string }{
		{"aes-128-gcm", ""}, {"rc4-md5", "password"}, {"none", "password"},
		{"2022-blake3-aes-128-gcm", base64.StdEncoding.EncodeToString(make([]byte, 32))},
		{"2022-blake3-aes-256-gcm", base64.StdEncoding.EncodeToString(make([]byte, 16))},
		{"2022-blake3-aes-256-gcm", ssTestPassword("2022-blake3-aes-256-gcm") + ":" + ssTestPassword("2022-blake3-aes-256-gcm")},
	} {
		require.Error(t, ValidateShadowsocksCredentials(tc.method, tc.password))
	}
	proxyURL, err := url.Parse("ss://aes-128-gcm:password@127.0.0.1:1")
	require.NoError(t, err)
	dial, err := NewShadowsocksDialer(proxyURL)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	conn, err := dial(ctx, "tcp", "no-dns.invalid:443")
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, conn)
	_, err = dial(context.Background(), "udp", "no-dns.invalid:443")
	require.ErrorContains(t, err, "only supports TCP")
}
