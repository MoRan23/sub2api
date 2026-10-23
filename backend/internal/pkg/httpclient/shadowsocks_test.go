package httpclient

import (
	"bufio"
	"context"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/codexnative"
	"github.com/Wei-Shaw/sub2api/internal/pkg/shadowsockstest"
	M "github.com/sagernet/sing/common/metadata"
	"github.com/stretchr/testify/require"
)

func TestShadowsocksNativeClientKeepsEncryptedDialer(t *testing.T) {
	for _, native := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "native"}[native], func(t *testing.T) {
			destination := make(chan string, 1)
			password := base64.StdEncoding.EncodeToString([]byte("01234567890123456789012345678901"))
			proxy := shadowsockstest.Start(t, "2022-blake3-aes-256-gcm", password, func(conn net.Conn, address M.Socksaddr) error {
				destination <- address.String()
				request, err := http.ReadRequest(bufio.NewReader(conn))
				if err != nil {
					return err
				}
				_ = request.Body.Close()
				_, err = io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok")
				return err
			})
			client, err := GetClient(Options{ProxyURL: proxy.String(), OpenAINative: native, Timeout: 5 * time.Second})
			require.NoError(t, err)
			t.Cleanup(client.CloseIdleConnections)
			ctx := codexnative.WithScope(context.Background(), codexnative.Scope{AccountID: 1, AccountUserAgent: "Linux"})
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://ss-native.invalid/test", nil)
			require.NoError(t, err)
			response, err := client.Do(request)
			require.NoError(t, err)
			defer func() { _ = response.Body.Close() }()
			body, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			require.Equal(t, "ok", string(body))
			require.Equal(t, "ss-native.invalid:80", <-destination)
		})
	}
}
