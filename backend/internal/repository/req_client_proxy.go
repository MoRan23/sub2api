package repository

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/proxyurl"
	"github.com/Wei-Shaw/sub2api/internal/pkg/proxyutil"
	"github.com/imroc/req/v3"
)

// configureReqClientProxy keeps req's existing HTTP/SOCKS tunnel handling and
// TLS impersonation. Shadowsocks supplies only the encrypted TCP stream below
// that handshake; req must not interpret it as an HTTP proxy.
func configureReqClientProxy(client *req.Client, raw string) error {
	trimmed, parsed, err := proxyurl.Parse(raw)
	if err != nil {
		return err
	}
	if parsed == nil {
		return nil
	}
	if parsed.Scheme != "ss" {
		client.SetProxyURL(trimmed)
		return nil
	}
	dial, err := proxyutil.NewShadowsocksDialer(parsed)
	if err != nil {
		return err
	}
	transport := client.GetTransport()
	transport.Proxy = nil
	transport.DialContext = dial
	return nil
}
