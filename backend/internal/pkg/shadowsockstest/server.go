// Package shadowsockstest provides a loopback-only Shadowsocks server for
// transport regression tests. It never dials a requested destination itself.
package shadowsockstest

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/sagernet/sing-shadowsocks"
	"github.com/sagernet/sing-shadowsocks/shadowaead"
	"github.com/sagernet/sing-shadowsocks/shadowaead_2022"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// Start serves encrypted TCP streams and gives the caller their decoded
// destination. The caller decides how to handle each stream without DNS access.
func Start(t testing.TB, method, password string, handle func(net.Conn, M.Socksaddr) error) *url.URL {
	t.Helper()
	handler := &streamHandler{handle: handle}
	var service shadowsocks.Service
	var err error
	if strings.HasPrefix(method, "2022-") {
		service, err = shadowaead_2022.NewServiceWithPassword(method, password, 60, handler, nil)
	} else {
		service, err = shadowaead.NewService(method, nil, password, 60, handler)
	}
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var mu sync.Mutex
	connections := make(map[net.Conn]struct{})
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			if ctx.Err() != nil {
				mu.Unlock()
				_ = conn.Close()
				return
			}
			connections[conn] = struct{}{}
			mu.Unlock()
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer func() {
					_ = conn.Close()
					mu.Lock()
					delete(connections, conn)
					mu.Unlock()
				}()
				_ = service.NewConnection(ctx, conn, M.Metadata{Source: M.SocksaddrFromNet(conn.RemoteAddr())}) //nolint:staticcheck // sing-shadowsocks v0.2.9 requires the legacy Metadata API.
			}()
		}
	}()
	t.Cleanup(func() {
		cancel()
		_ = listener.Close()
		mu.Lock()
		for conn := range connections {
			_ = conn.Close()
		}
		mu.Unlock()
		workers.Wait()
	})
	return &url.URL{Scheme: "ss", Host: listener.Addr().String(), User: url.UserPassword(method, password)}
}

type streamHandler struct {
	handle func(net.Conn, M.Socksaddr) error
}

func (h *streamHandler) NewConnection(_ context.Context, conn net.Conn, metadata M.Metadata) error { //nolint:staticcheck // Required by the pinned sing-shadowsocks handler interface.
	return h.handle(conn, metadata.Destination)
}

func (*streamHandler) NewPacketConnection(_ context.Context, _ N.PacketConn, _ M.Metadata) error { //nolint:staticcheck // Required by the pinned sing-shadowsocks handler interface.
	return errors.New("test server supports TCP only")
}

func (*streamHandler) NewError(context.Context, error) {}
