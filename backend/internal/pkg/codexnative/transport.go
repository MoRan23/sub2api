package codexnative

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"sort"
	"strings"

	"github.com/imroc/req/v3"
	utls "github.com/refraction-networking/utls"
)

// Options retains response decoding of auxiliary req clients without changing
// their outbound Accept-Encoding or User-Agent headers.
type Options struct {
	AutoDecompression bool
}

type nativeTransport struct {
	transport *req.Transport
	profile   profile
	options   Options
}

// NewTransport creates one immutable platform transport. Account/proxy/purpose
// isolation is supplied by the owner of this transport, not by per-turn IDs.
// The base's existing dialer/proxy/pool configuration is preserved, but the
// target TLS handshake and HTTP wire format are replaced by the native profile.
func NewTransport(platform Platform, base *http.Transport) (http.RoundTripper, error) {
	return NewTransportWithOptions(platform, base, Options{})
}

func NewTransportWithOptions(platform Platform, base *http.Transport, options Options) (http.RoundTripper, error) {
	p, err := profileFor(platform)
	if err != nil {
		return nil, err
	}
	if base == nil {
		base = http.DefaultTransport.(*http.Transport)
	}
	base = base.Clone()
	config := &tls.Config{}
	if base.TLSClientConfig != nil {
		config = base.TLSClientConfig.Clone()
	}
	if config.RootCAs != nil {
		config.RootCAs = config.RootCAs.Clone()
	}
	if len(config.Certificates) != 0 || config.GetClientCertificate != nil {
		return nil, errors.New("codexnative: client certificate authentication is not supported by the native profile")
	}
	if len(config.EncryptedClientHelloConfigList) != 0 {
		return nil, errors.New("codexnative: ECH is incompatible with the sampled native profile")
	}
	if config.MinVersion > p.MinVersion || (config.MaxVersion != 0 && config.MaxVersion < p.MaxVersion) {
		// Changing the sampled offer would silently select a different profile;
		// ignoring these bounds could bypass a caller's explicit TLS policy.
		return nil, errors.New("codexnative: configured TLS versions restrict the sampled native profile")
	}

	t := req.NewTransport().EnableForceHTTP1().DisableAutoDecode().SetLowercaseHTTP1HeaderNames(true)
	t.CancelDialOnRequestCancel = true
	t.Proxy = base.Proxy
	t.OnProxyConnectResponse = base.OnProxyConnectResponse
	t.DialContext = base.DialContext
	if t.DialContext == nil && base.Dial != nil {
		// Preserve legacy dialers; standard callers already supply DialContext.
		t.DialContext = func(_ context.Context, network, address string) (net.Conn, error) { return base.Dial(network, address) }
	}
	t.TLSClientConfig = config // HTTPS proxy outer TLS retains standard validation.
	t.TLSHandshakeTimeout = base.TLSHandshakeTimeout
	t.DisableKeepAlives = base.DisableKeepAlives
	t.DisableCompression = true
	t.MaxIdleConns = base.MaxIdleConns
	t.MaxIdleConnsPerHost = base.MaxIdleConnsPerHost
	t.MaxConnsPerHost = base.MaxConnsPerHost
	t.IdleConnTimeout = base.IdleConnTimeout
	t.ResponseHeaderTimeout = base.ResponseHeaderTimeout
	t.ExpectContinueTimeout = base.ExpectContinueTimeout
	t.ProxyConnectHeader = base.ProxyConnectHeader.Clone()
	t.GetProxyConnectHeader = base.GetProxyConnectHeader
	t.MaxResponseHeaderBytes = base.MaxResponseHeaderBytes
	t.WriteBufferSize = base.WriteBufferSize
	t.ReadBufferSize = base.ReadBufferSize
	t.SetTLSHandshake(func(ctx context.Context, address string, plain net.Conn) (net.Conn, *tls.ConnectionState, error) {
		// req invokes this after CONNECT/SOCKS. Its separate outer HTTPS-proxy
		// connection keeps the normal crypto/tls handshake and certificate check.
		if base.TLSHandshakeTimeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, base.TLSHandshakeTimeout)
			defer cancel()
		}
		return handshake(ctx, plain, address, platform, config)
	})
	return &nativeTransport{transport: t, profile: p, options: options}, nil
}

func (t *nativeTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	// Request.Body may populate trailer values while it is read. Like the
	// standard transport, the writer must observe that same trailer map at EOF.
	clone.Trailer = request.Trailer
	if clone.Header == nil {
		clone.Header = make(http.Header)
	}
	// A missing UA must remain missing; req's default UA is inappropriate for
	// token/OTel requests. Normalize only this special header used by its writer.
	userAgent := UserAgent(clone.Header)
	for name := range clone.Header {
		if strings.EqualFold(name, "User-Agent") {
			delete(clone.Header, name)
		}
	}
	clone.Header["User-Agent"] = []string{userAgent}
	clone.Header[req.HeaderOderKey] = t.headerOrder(clone)
	response, err := t.transport.RoundTrip(clone)
	if err == nil && response != nil {
		// Do not expose req's private ordering pseudo-header to observers.
		response.Request = request
		if t.options.AutoDecompression {
			decodeResponse(response)
		}
	}
	return response, err
}

func (t *nativeTransport) CloseIdleConnections() { t.transport.CloseIdleConnections() }

// UserAgent matches the value that the native HTTP writer sends. Canonical
// Header semantics take precedence, including an explicitly blank value;
// direct lower-case map keys are accepted without changing the caller's map.
func UserAgent(headers http.Header) string {
	if values, exists := headers["User-Agent"]; exists {
		if len(values) > 0 {
			return values[0]
		}
		return ""
	}
	var names []string
	for name := range headers {
		if strings.EqualFold(name, "User-Agent") {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		if values := headers[name]; len(values) > 0 {
			return values[0]
		}
	}
	return ""
}

func (t *nativeTransport) headerOrder(request *http.Request) []string {
	order := append([]string(nil), t.profile.HeaderOrder...)
	known := make(map[string]bool, len(order)+4)
	for _, name := range order {
		known[name] = true
	}
	for _, name := range []string{"host", "content-length", "transfer-encoding", "trailer", req.HeaderOderKey, req.PseudoHeaderOderKey} {
		known[name] = true
	}
	var extra []string
	for name := range request.Header {
		name = strings.ToLower(name)
		if !known[name] {
			known[name] = true
			extra = append(extra, name)
		}
	}
	// The transport can emit Connection: close from the request/base policy.
	if !known["connection"] && (request.Close || t.transport.DisableKeepAlives) {
		extra = append(extra, "connection")
	}
	sort.Strings(extra)
	order = append(order, extra...)
	return append(order, "host", "content-length", "transfer-encoding", "trailer")
}

func handshake(ctx context.Context, plain net.Conn, address string, platform Platform, base *tls.Config) (net.Conn, *tls.ConnectionState, error) {
	host := address
	if parsed, _, err := net.SplitHostPort(address); err == nil {
		host = parsed
	}
	if base.ServerName != "" {
		host = base.ServerName
	}
	config := &utls.Config{
		ServerName: host, Rand: base.Rand, Time: base.Time, RootCAs: base.RootCAs,
		InsecureSkipVerify: base.InsecureSkipVerify, VerifyPeerCertificate: base.VerifyPeerCertificate,
		DynamicRecordSizingDisabled: base.DynamicRecordSizingDisabled, KeyLogWriter: base.KeyLogWriter,
		// No session cache: these templates describe first-handshake traffic.
		// Empty ticket extensions are still emitted when the sample has them.
		ClientSessionCache: nil,
	}
	if base.VerifyConnection != nil {
		config.VerifyConnection = func(state utls.ConnectionState) error { return base.VerifyConnection(standardConnectionState(state)) }
	}
	spec, err := profileSpec(platform)
	if err != nil {
		return nil, nil, err
	}
	if platform == Windows {
		plain = &firstRecordVersionConn{Conn: plain}
	}
	conn := utls.UClient(plain, config, utls.HelloCustom)
	if err := conn.ApplyPreset(spec); err != nil {
		return nil, nil, err
	}
	if platform != Linux {
		conn.HandshakeState.Hello.SessionId = nil
	}
	if err := conn.HandshakeContext(ctx); err != nil {
		return nil, nil, err
	}
	state := standardConnectionState(conn.ConnectionState())
	return conn, &state, nil
}

func standardConnectionState(cs utls.ConnectionState) tls.ConnectionState {
	return tls.ConnectionState{
		Version: cs.Version, HandshakeComplete: cs.HandshakeComplete, DidResume: cs.DidResume,
		CipherSuite: cs.CipherSuite, NegotiatedProtocol: cs.NegotiatedProtocol,
		NegotiatedProtocolIsMutual: cs.NegotiatedProtocolIsMutual, ServerName: cs.ServerName,
		PeerCertificates: cs.PeerCertificates, VerifiedChains: cs.VerifiedChains,
		SignedCertificateTimestamps: cs.SignedCertificateTimestamps, OCSPResponse: cs.OCSPResponse,
		TLSUnique: cs.TLSUnique, ECHAccepted: cs.ECHAccepted,
	}
}

// firstRecordVersionConn changes only the first ClientHello record's legacy
// record version. It sits inside the proxy tunnel and leaves subsequent TLS
// records untouched. Offset tracking handles partial writes without buffering.
type firstRecordVersionConn struct {
	net.Conn
	offset int
	hello  bool
}

func (c *firstRecordVersionConn) Write(data []byte) (int, error) {
	if c.offset >= 5 || len(data) == 0 {
		return c.Conn.Write(data)
	}
	if c.offset == 0 {
		c.hello = data[0] == 22
	}
	wire := data
	if c.hello {
		wire = append([]byte(nil), data...)
		for i := range wire {
			if position := c.offset + i; position == 1 || position == 2 {
				wire[i] = 3
			}
		}
	}
	n, err := c.Conn.Write(wire)
	c.offset += n
	return n, err
}
