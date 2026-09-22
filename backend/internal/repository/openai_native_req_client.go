package repository

import (
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/pkg/codexnative"
	"github.com/Wei-Shaw/sub2api/internal/pkg/servertiming"
	"github.com/imroc/req/v3"
)

// Attach to the standard http.Client, after req has merged its common headers.
// net/http invokes this dispatcher for every redirect, so the actual UA always
// wins over frozen account hints. Request-policy clones share this dispatcher;
// their cookies belong to the outer authorization-scoped transport.
func newOpenAINativeReqDispatcher(client *req.Client) *codexnative.Dispatcher {
	base := openAINativeReqTransportOptions(client.GetTransport())
	options := codexnative.Options{AutoDecompression: client.GetTransport().AutoDecompression}
	return codexnative.NewDispatcher(client.GetClient().Transport, func(platform codexnative.Platform) (http.RoundTripper, error) {
		transport, err := codexnative.NewTransportWithOptions(platform, base, options)
		if err != nil {
			return nil, err
		}
		return &timedOpenAINativeReqTransport{
			RoundTripper: servertiming.WrapRoundTripper(transport),
			transport:    transport,
		}, nil
	})
}

// Preserve operational transport settings, without retaining the mutable req
// client or its browser impersonation handshake in a native profile pool.
func openAINativeReqTransportOptions(source *req.Transport) *http.Transport {
	base := &http.Transport{
		Proxy:                  source.Proxy,
		OnProxyConnectResponse: source.OnProxyConnectResponse,
		DialContext:            source.DialContext,
		TLSHandshakeTimeout:    source.TLSHandshakeTimeout,
		DisableKeepAlives:      source.DisableKeepAlives,
		DisableCompression:     source.DisableCompression,
		MaxIdleConns:           source.MaxIdleConns,
		MaxIdleConnsPerHost:    source.MaxIdleConnsPerHost,
		MaxConnsPerHost:        source.MaxConnsPerHost,
		IdleConnTimeout:        source.IdleConnTimeout,
		ResponseHeaderTimeout:  source.ResponseHeaderTimeout,
		ExpectContinueTimeout:  source.ExpectContinueTimeout,
		ProxyConnectHeader:     source.ProxyConnectHeader.Clone(),
		GetProxyConnectHeader:  source.GetProxyConnectHeader,
		MaxResponseHeaderBytes: source.MaxResponseHeaderBytes,
		WriteBufferSize:        source.WriteBufferSize,
		ReadBufferSize:         source.ReadBufferSize,
	}
	if source.TLSClientConfig != nil {
		base.TLSClientConfig = source.TLSClientConfig.Clone()
	}
	return base
}

type timedOpenAINativeReqTransport struct {
	http.RoundTripper
	transport http.RoundTripper
}

func (t *timedOpenAINativeReqTransport) CloseIdleConnections() {
	if closer, ok := t.transport.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}
