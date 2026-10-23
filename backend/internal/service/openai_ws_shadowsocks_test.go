package service

import (
	"context"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/shadowsockstest"
	"github.com/coder/websocket"
	M "github.com/sagernet/sing/common/metadata"
	"github.com/stretchr/testify/require"
)

func TestOpenAIWebSocketShadowsocksProxy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		messageType, message, err := conn.Read(r.Context())
		if err == nil {
			_ = conn.Write(r.Context(), messageType, message)
		}
	}))
	defer server.Close()
	observed := make(chan string, 1)
	password := base64.StdEncoding.EncodeToString([]byte("01234567890123456789012345678901"))
	proxy := shadowsockstest.Start(t, "2022-blake3-aes-256-gcm", password, func(conn net.Conn, destination M.Socksaddr) error {
		observed <- destination.String()
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
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	dialer := newDefaultOpenAIWSClientDialer()
	conn, _, _, err := dialer.Dial(ctx, "ws://ss-websocket.invalid/responses", http.Header{}, proxy.String())
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	require.NoError(t, conn.WriteJSON(ctx, map[string]string{"type": "response.create"}))
	message, err := conn.ReadMessage(ctx)
	require.NoError(t, err)
	require.JSONEq(t, `{"type":"response.create"}`, string(message))
	require.Equal(t, "ss-websocket.invalid:80", <-observed)
}
