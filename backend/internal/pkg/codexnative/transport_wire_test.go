package codexnative

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// These servers inspect the decrypted HTTP bytes, rather than Header maps, which
// discard the capitalization and ordering that this transport must preserve.
type nativeWireRequest struct {
	line    string
	names   []string
	headers http.Header
	body    string
	state   tls.ConnectionState
	err     error
}

type nativeWireOrigin struct {
	url      string
	addr     string
	roots    *x509.CertPool
	requests chan nativeWireRequest
	accepted atomic.Int32
}

func nativeWireCertificate(t *testing.T) (tls.Certificate, *x509.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "native wire test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames: []string{"localhost", "native-target.invalid"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, cert
}

func newNativeWireOrigin(t *testing.T, version uint16) *nativeWireOrigin {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cert, parsed := nativeWireCertificate(t)
	origin := &nativeWireOrigin{addr: listener.Addr().String(), roots: x509.NewCertPool(), requests: make(chan nativeWireRequest, 16)}
	origin.roots.AddCert(parsed)
	scheme := "http"
	if version != 0 {
		scheme = "https"
	}
	_, port, _ := net.SplitHostPort(origin.addr)
	origin.url = scheme + "://localhost:" + port
	var mu sync.Mutex
	connections := make(map[net.Conn]struct{})
	var workers sync.WaitGroup
	acceptedDone := make(chan struct{})
	go func() {
		defer close(acceptedDone)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			origin.accepted.Add(1)
			mu.Lock()
			connections[conn] = struct{}{}
			mu.Unlock()
			workers.Add(1)
			go func(raw net.Conn) {
				defer workers.Done()
				defer raw.Close()
				defer func() { mu.Lock(); delete(connections, raw); mu.Unlock() }()
				_ = raw.SetDeadline(time.Now().Add(10 * time.Second))
				var state tls.ConnectionState
				conn := raw
				if version != 0 {
					config := &tls.Config{
						Certificates: []tls.Certificate{cert}, MinVersion: version, MaxVersion: version,
						NextProtos: []string{"h2", "http/1.1"},
					}
					if version == tls.VersionTLS13 {
						// Exercise real MLKEM agreement, not just its advertised ID.
						config.CurvePreferences = []tls.CurveID{tls.X25519MLKEM768}
					}
					secured := tls.Server(raw, config)
					if err := secured.Handshake(); err != nil {
						return // Certificate-rejection tests intentionally abort here.
					}
					state, conn = secured.ConnectionState(), secured
				}
				reader := bufio.NewReader(conn)
				for {
					line, err := reader.ReadString('\n')
					if err != nil {
						return
					}
					capture := nativeWireRequest{line: strings.TrimSuffix(line, "\r\n"), state: state}
					var block strings.Builder
					block.WriteString(line)
					for {
						line, err = reader.ReadString('\n')
						if err != nil {
							capture.err = err
							break
						}
						block.WriteString(line)
						if line == "\r\n" {
							break
						}
						name, value, ok := strings.Cut(line, ":")
						if !ok || !strings.HasPrefix(value, " ") || strings.HasPrefix(value, "  ") {
							capture.err = fmt.Errorf("unexpected header formatting %q", line)
						}
						capture.names = append(capture.names, name)
					}
					if capture.err == nil {
						request, err := http.ReadRequest(bufio.NewReader(io.MultiReader(strings.NewReader(block.String()), reader)))
						if err != nil {
							capture.err = err
						} else {
							body, err := io.ReadAll(request.Body)
							_ = request.Body.Close()
							capture.body, capture.headers, capture.err = string(body), request.Header, err
						}
					}
					origin.requests <- capture
					if capture.err != nil {
						return
					}
					if _, err := io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok"); err != nil {
						return
					}
				}
			}(conn)
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		<-acceptedDone
		mu.Lock()
		for conn := range connections {
			_ = conn.Close()
		}
		mu.Unlock()
		workers.Wait()
	})
	return origin
}

func nativeWireClient(t *testing.T, platform Platform, base *http.Transport) *http.Client {
	t.Helper()
	transport, err := NewTransport(platform, base)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	t.Cleanup(client.CloseIdleConnections)
	return client
}

func nativeWireDrain(t *testing.T, response *http.Response, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if _, err := io.Copy(io.Discard, response.Body); err != nil {
		t.Fatal(err)
	}
}

func (origin *nativeWireOrigin) receive(t *testing.T) nativeWireRequest {
	t.Helper()
	select {
	case captured := <-origin.requests:
		if captured.err != nil {
			t.Fatal(captured.err)
		}
		return captured
	case <-time.After(5 * time.Second):
		t.Fatal("local origin did not receive the HTTP request")
		return nativeWireRequest{}
	}
}

func TestNativeTransportHTTP1WireAcrossPlatforms(t *testing.T) {
	for _, tc := range []struct {
		name     string
		platform Platform
		version  uint16
	}{
		{"windows_tls12", Windows, tls.VersionTLS12},
		{"macos_tls12", MacOS, tls.VersionTLS12},
		{"linux_tls13", Linux, tls.VersionTLS13},
		{"linux_tls12", Linux, tls.VersionTLS12},
		{"plain_http", Linux, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			origin := newNativeWireOrigin(t, tc.version)
			base := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: origin.roots}, MaxIdleConnsPerHost: 2}
			client := nativeWireClient(t, tc.platform, base)
			payload := `{"model":"wire-test","input":"body stays unchanged"}`
			request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, origin.url+"/responses?wire=1", strings.NewReader(payload))
			if err != nil {
				t.Fatal(err)
			}
			ordered := []string{"version", "x-codex-beta-features", "x-codex-window-id", "x-codex-turn-metadata"}
			if tc.platform == Windows {
				ordered = append(ordered, "x-openai-internal-codex-responses-lite")
			}
			ordered = append(ordered, "x-client-request-id", "session-id", "thread-id", "accept", "content-type", "authorization", "originator", "user-agent")
			for _, name := range ordered {
				request.Header.Set(name, "test-"+name)
			}
			request.Header.Set("X-Zeta", "last-extra")
			request.Header.Set("X-Alpha", "first-extra")
			request.Header.Add("X-Alpha", "second-value")
			original := request.Header.Clone()
			response, err := client.Do(request)
			nativeWireDrain(t, response, err)
			captured := origin.receive(t)
			want := append(append([]string(nil), ordered...), "x-alpha", "x-alpha", "x-zeta", "host", "content-length")
			if !reflect.DeepEqual(want, captured.names) {
				t.Fatalf("header names/order:\n got %v\nwant %v", captured.names, want)
			}
			if captured.line != "POST /responses?wire=1 HTTP/1.1" || captured.body != payload {
				t.Fatalf("request changed: %q %q", captured.line, captured.body)
			}
			if got := captured.headers.Values("X-Alpha"); !reflect.DeepEqual(got, []string{"first-extra", "second-value"}) {
				t.Fatalf("duplicate values changed: %v", got)
			}
			if captured.headers.Get("Accept-Encoding") != "" {
				t.Fatal("transport injected Accept-Encoding")
			}
			if !reflect.DeepEqual(request.Header, original) {
				t.Fatal("transport mutated caller's header map")
			}
			if captured.state.Version != tc.version || captured.state.NegotiatedProtocol != "" {
				t.Fatalf("unexpected TLS version/ALPN: %#x/%q", captured.state.Version, captured.state.NegotiatedProtocol)
			}
			if tc.version != 0 && captured.state.ServerName != "localhost" {
				t.Fatalf("wrong target SNI %q", captured.state.ServerName)
			}
			if tc.version == tls.VersionTLS13 && captured.state.CurveID != tls.X25519MLKEM768 {
				t.Fatalf("Linux did not complete MLKEM key agreement: %d", captured.state.CurveID)
			}
			response, err = client.Get(origin.url + "/again")
			nativeWireDrain(t, response, err)
			origin.receive(t)
			if got := origin.accepted.Load(); got != 1 {
				t.Fatalf("sequential requests did not reuse connection: %d", got)
			}
			client.CloseIdleConnections()
			response, err = client.Get(origin.url + "/after-close-idle")
			nativeWireDrain(t, response, err)
			origin.receive(t)
			if got := origin.accepted.Load(); got != 2 {
				t.Fatalf("CloseIdleConnections did not close the real pool: %d connections", got)
			}
		})
	}
}

func TestNativeTransportUnknownLengthBodyAndExplicitEncoding(t *testing.T) {
	origin := newNativeWireOrigin(t, 0)
	client := nativeWireClient(t, Linux, &http.Transport{})
	payload := "streaming body without a known content length"
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, origin.url+"/upload", io.NopCloser(strings.NewReader(payload)))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/octet-stream")
	request.Header.Set("Accept-Encoding", "gzip, deflate")
	response, err := client.Do(request)
	nativeWireDrain(t, response, err)
	captured := origin.receive(t)
	if captured.body != payload {
		t.Fatalf("chunked body changed: %q", captured.body)
	}
	want := []string{"content-type", "accept-encoding", "host", "transfer-encoding"}
	if !reflect.DeepEqual(captured.names, want) {
		t.Fatalf("chunked header names/order: got %v, want %v", captured.names, want)
	}
	if captured.headers.Get("Accept-Encoding") != "gzip, deflate" {
		t.Fatal("explicit Accept-Encoding changed")
	}
	if captured.headers.Get("User-Agent") != "" || captured.headers.Get("Expect") != "" || captured.headers.Get("Content-Length") != "" {
		t.Fatalf("transport injected unwanted defaults: %v", captured.headers)
	}
}

type nativeDynamicTrailerBody struct {
	reader  *strings.Reader
	trailer http.Header
}

func (body *nativeDynamicTrailerBody) Read(p []byte) (int, error) {
	n, err := body.reader.Read(p)
	if err == io.EOF {
		body.trailer.Set("X-Final", "computed-at-body-eof")
	}
	return n, err
}

func (*nativeDynamicTrailerBody) Close() error { return nil }

func TestNativeTransportDynamicTrailerWire(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	type capture struct {
		wire, body string
		trailer    http.Header
		err        error
	}
	captured := make(chan capture, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			captured <- capture{err: err}
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		var wire bytes.Buffer
		request, err := http.ReadRequest(bufio.NewReader(io.TeeReader(conn, &wire)))
		if err != nil {
			captured <- capture{err: err}
			return
		}
		body, err := io.ReadAll(request.Body)
		request.Body.Close()
		captured <- capture{wire: wire.String(), body: string(body), trailer: request.Trailer, err: err}
		_, _ = io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok")
	}()
	client := nativeWireClient(t, Linux, &http.Transport{})
	trailer := http.Header{"X-Final": nil}
	payload := "stream with an EOF-computed trailer"
	body := &nativeDynamicTrailerBody{reader: strings.NewReader(payload), trailer: trailer}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://"+listener.Addr().String()+"/trailer", body)
	if err != nil {
		t.Fatal(err)
	}
	request.Trailer = trailer
	request.Header.Set("Content-Type", "application/octet-stream")
	originalHeaders := request.Header.Clone()
	response, err := client.Do(request)
	nativeWireDrain(t, response, err)
	select {
	case got := <-captured:
		if got.err != nil {
			t.Fatal(got.err)
		}
		if got.body != payload || got.trailer.Get("X-Final") != "computed-at-body-eof" {
			t.Fatalf("dynamic trailer lost: body=%q, trailer=%v", got.body, got.trailer)
		}
		if !strings.Contains(got.wire, "\r\ntrailer: x-final\r\n") || !strings.Contains(got.wire, "\r\n0\r\nx-final: computed-at-body-eof\r\n\r\n") {
			t.Fatalf("trailer declaration or final field is not lowercase on the wire:\n%s", got.wire)
		}
	case <-time.After(time.Second):
		t.Fatal("local origin did not capture the dynamic trailer")
	}
	if !reflect.DeepEqual(request.Header, originalHeaders) {
		t.Fatal("native transport mutated caller headers while adding trailer metadata")
	}
}

func TestNativeTransportFinishedRequestCancellationPreservesIdleConnection(t *testing.T) {
	origin := newNativeWireOrigin(t, tls.VersionTLS13)
	client := nativeWireClient(t, Linux, &http.Transport{TLSClientConfig: &tls.Config{RootCAs: origin.roots}, MaxIdleConnsPerHost: 1})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, origin.url+"/first", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(request)
	nativeWireDrain(t, response, err)
	origin.receive(t)
	cancel()
	response, err = client.Get(origin.url + "/second")
	nativeWireDrain(t, response, err)
	origin.receive(t)
	if got := origin.accepted.Load(); got != 1 {
		t.Fatalf("canceling a finished request closed a reusable connection: %d connections", got)
	}
}

func TestNativeTransportConcurrentHandshakes(t *testing.T) {
	for _, platform := range []Platform{Windows, Linux, MacOS} {
		t.Run(string(platform), func(t *testing.T) {
			version := uint16(tls.VersionTLS12)
			if platform == Linux {
				version = tls.VersionTLS13
			}
			origin := newNativeWireOrigin(t, version)
			client := nativeWireClient(t, platform, &http.Transport{TLSClientConfig: &tls.Config{RootCAs: origin.roots}, DisableKeepAlives: true})
			const count = 6
			start := make(chan struct{})
			results := make(chan error, count)
			for i := range count {
				go func() {
					<-start
					response, err := client.Get(fmt.Sprintf("%s/concurrent/%d", origin.url, i))
					if err == nil {
						_, err = io.Copy(io.Discard, response.Body)
						response.Body.Close()
					}
					results <- err
				}()
			}
			close(start)
			for range count {
				if err := <-results; err != nil {
					t.Fatal(err)
				}
				captured := origin.receive(t)
				if captured.state.Version != version || captured.state.NegotiatedProtocol != "" {
					t.Fatalf("concurrent handshake changed platform profile: %#x/%q", captured.state.Version, captured.state.NegotiatedProtocol)
				}
			}
			if got := origin.accepted.Load(); got != count {
				t.Fatalf("expected independent handshakes, got %d connections", got)
			}
		})
	}
}

// relayNativeWireTunnel runs only against the test origin. Closing either end
// unblocks the other copy goroutine, including when a client cancels its request.
func relayNativeWireTunnel(client, target net.Conn) {
	defer client.Close()
	defer target.Close()
	done := make(chan struct{})
	go func() { _, _ = io.Copy(target, client); _ = target.Close(); close(done) }()
	_, _ = io.Copy(client, target)
	_ = client.Close()
	<-done
}

func TestNativeTransportCONNECTProxyLayers(t *testing.T) {
	for _, secureProxy := range []bool{false, true} {
		t.Run(fmt.Sprintf("https_proxy_%t", secureProxy), func(t *testing.T) {
			origin := newNativeWireOrigin(t, tls.VersionTLS12)
			observed := make(chan nativeWireRequest, 1)
			proxy := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				capture := nativeWireRequest{line: r.Method + " " + r.Host, headers: r.Header.Clone()}
				if r.TLS != nil {
					capture.state = *r.TLS
				}
				observed <- capture
				if r.Method != http.MethodConnect {
					http.Error(w, "CONNECT required", http.StatusBadRequest)
					return
				}
				target, err := net.DialTimeout("tcp", origin.addr, time.Second)
				if err != nil {
					http.Error(w, err.Error(), http.StatusBadGateway)
					return
				}
				client, buffered, err := w.(http.Hijacker).Hijack()
				if err != nil {
					_ = target.Close()
					return
				}
				_, err = buffered.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
				if err == nil {
					err = buffered.Flush()
				}
				if err != nil {
					_ = client.Close()
					_ = target.Close()
					return
				}
				relayNativeWireTunnel(client, target)
			}))
			if secureProxy {
				certificate, _ := nativeWireCertificate(t)
				proxy.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
				proxy.EnableHTTP2 = true
				proxy.StartTLS()
				origin.roots.AddCert(proxy.Certificate())
			} else {
				proxy.Start()
			}
			t.Cleanup(proxy.Close)
			proxyURL, _ := url.Parse(proxy.URL)
			_, proxyPort, _ := net.SplitHostPort(proxyURL.Host)
			proxyURL.Host = net.JoinHostPort("localhost", proxyPort)
			proxyURL.User = url.UserPassword("wire-user", "wire-password")
			client := nativeWireClient(t, Windows, &http.Transport{
				Proxy: http.ProxyURL(proxyURL), DialContext: (&net.Dialer{Timeout: time.Second}).DialContext,
				TLSClientConfig: &tls.Config{RootCAs: origin.roots, NextProtos: []string{"h2", "http/1.1"}}, TLSHandshakeTimeout: time.Second,
			})
			_, originPort, _ := net.SplitHostPort(origin.addr)
			targetHost := net.JoinHostPort("native-target.invalid", originPort)
			response, err := client.Get("https://" + targetHost + "/proxy")
			nativeWireDrain(t, response, err)
			captured := origin.receive(t)
			if captured.state.ServerName != "native-target.invalid" || captured.state.Version != tls.VersionTLS12 || captured.state.NegotiatedProtocol != "" {
				t.Fatalf("inner TLS is not the native target handshake: %+v", captured.state)
			}
			if captured.headers.Get("Proxy-Authorization") != "" {
				t.Fatal("proxy credentials leaked to origin")
			}
			select {
			case connect := <-observed:
				if connect.line != "CONNECT "+targetHost {
					t.Fatalf("wrong CONNECT destination %q", connect.line)
				}
				if got := connect.headers.Get("Proxy-Authorization"); got != "Basic "+base64.StdEncoding.EncodeToString([]byte("wire-user:wire-password")) {
					t.Fatalf("missing proxy authentication %q", got)
				}
				if secureProxy && (connect.state.ServerName != "localhost" || connect.state.Version < tls.VersionTLS12 || connect.state.NegotiatedProtocol == "h2") {
					t.Fatalf("wrong outer proxy TLS state: %+v", connect.state)
				}
			case <-time.After(time.Second):
				t.Fatal("proxy did not observe CONNECT")
			}
		})
	}
}

func TestNativeTransportSOCKSProxyRemoteDNSAndAuthentication(t *testing.T) {
	for _, scheme := range []string{"socks5", "socks5h"} {
		t.Run(scheme, func(t *testing.T) {
			origin := newNativeWireOrigin(t, tls.VersionTLS12)
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = listener.Close() })
			observed := make(chan string, 1)
			proxyErrors := make(chan error, 1)
			done := make(chan struct{})
			go func() {
				defer close(done)
				conn, err := listener.Accept()
				if err != nil {
					proxyErrors <- err
					return
				}
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				if err := serveNativeWireSOCKS(conn, origin.addr, observed); err != nil {
					proxyErrors <- err
				}
			}()
			proxyURL, _ := url.Parse(scheme + "://wire-user:wire-password@" + listener.Addr().String())
			client := nativeWireClient(t, Windows, &http.Transport{Proxy: http.ProxyURL(proxyURL), TLSClientConfig: &tls.Config{RootCAs: origin.roots}})
			_, port, _ := net.SplitHostPort(origin.addr)
			target := net.JoinHostPort("native-target.invalid", port)
			response, err := client.Get("https://" + target + "/socks")
			if err != nil {
				select {
				case proxyErr := <-proxyErrors:
					t.Fatalf("request: %v; SOCKS server: %v", err, proxyErr)
				default:
					t.Fatal(err)
				}
			}
			nativeWireDrain(t, response, nil)
			origin.receive(t)
			select {
			case got := <-observed:
				if got != target {
					t.Fatalf("SOCKS target %q, want %q", got, target)
				}
			case <-time.After(time.Second):
				t.Fatal("SOCKS destination was not observed")
			}
			client.CloseIdleConnections()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("SOCKS tunnel was not closed with idle connection")
			}
			select {
			case err := <-proxyErrors:
				t.Fatal(err)
			default:
			}
		})
	}
}

func serveNativeWireSOCKS(conn net.Conn, destination string, observed chan<- string) error {
	read := func(n int) ([]byte, error) { b := make([]byte, n); _, err := io.ReadFull(conn, b); return b, err }
	header, err := read(2)
	if err != nil {
		return err
	}
	if header[0] != 5 {
		return fmt.Errorf("SOCKS version %d", header[0])
	}
	methods, err := read(int(header[1]))
	if err != nil {
		return err
	}
	if !strings.ContainsRune(string(methods), 2) {
		return errors.New("SOCKS client did not offer username/password")
	}
	if _, err = conn.Write([]byte{5, 2}); err != nil {
		return err
	}
	auth, err := read(2)
	if err != nil {
		return err
	}
	username, err := read(int(auth[1]))
	if err != nil {
		return err
	}
	length, err := read(1)
	if err != nil {
		return err
	}
	password, err := read(int(length[0]))
	if err != nil {
		return err
	}
	if auth[0] != 1 || string(username) != "wire-user" || string(password) != "wire-password" {
		return errors.New("incorrect SOCKS credentials")
	}
	if _, err = conn.Write([]byte{1, 0}); err != nil {
		return err
	}
	command, err := read(4)
	if err != nil {
		return err
	}
	if command[0] != 5 || command[1] != 1 || command[3] != 3 {
		return fmt.Errorf("SOCKS must send a domain CONNECT, got %v", command)
	}
	length, err = read(1)
	if err != nil {
		return err
	}
	hostname, err := read(int(length[0]))
	if err != nil {
		return err
	}
	port, err := read(2)
	if err != nil {
		return err
	}
	observed <- net.JoinHostPort(string(hostname), fmt.Sprint(binary.BigEndian.Uint16(port)))
	target, err := net.DialTimeout("tcp", destination, time.Second)
	if err != nil {
		return err
	}
	if _, err = conn.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0}); err != nil {
		_ = target.Close()
		return err
	}
	relayNativeWireTunnel(conn, target)
	return nil
}

func TestNativeTransportRejectsUntrustedCertificate(t *testing.T) {
	origin := newNativeWireOrigin(t, tls.VersionTLS13)
	client := nativeWireClient(t, Linux, &http.Transport{TLSClientConfig: &tls.Config{RootCAs: x509.NewCertPool()}})
	response, err := client.Get(origin.url)
	if response != nil {
		response.Body.Close()
	}
	var unknown x509.UnknownAuthorityError
	if !errors.As(err, &unknown) {
		t.Fatalf("expected certificate verification failure, got %v", err)
	}
	select {
	case <-origin.requests:
		t.Fatal("HTTP request sent after certificate rejection")
	default:
	}
}

func TestNativeTransportRejectsUntrustedHTTPSProxy(t *testing.T) {
	origin := newNativeWireOrigin(t, tls.VersionTLS12)
	var connectRequests atomic.Int32
	proxy := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connectRequests.Add(1)
		http.Error(w, "proxy should fail certificate verification first", http.StatusBadGateway)
	}))
	certificate, _ := nativeWireCertificate(t)
	proxy.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
	proxy.StartTLS()
	t.Cleanup(proxy.Close)
	proxyURL, _ := url.Parse(proxy.URL)
	client := nativeWireClient(t, Windows, &http.Transport{Proxy: http.ProxyURL(proxyURL), TLSClientConfig: &tls.Config{RootCAs: origin.roots}})
	response, err := client.Get(origin.url)
	if response != nil {
		response.Body.Close()
	}
	var unknown x509.UnknownAuthorityError
	if !errors.As(err, &unknown) {
		t.Fatalf("expected proxy certificate verification failure, got %v", err)
	}
	if connectRequests.Load() != 0 || origin.accepted.Load() != 0 {
		t.Fatal("untrusted HTTPS proxy was used for CONNECT or an origin connection")
	}
}

func TestNativeTransportProxyCancellation(t *testing.T) {
	for _, scheme := range []string{"http", "https", "socks5"} {
		t.Run(scheme, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = listener.Close() })
			certificate, parsed := nativeWireCertificate(t)
			roots := x509.NewCertPool()
			roots.AddCert(parsed)
			arrived, closed := make(chan struct{}), make(chan struct{})
			go func() {
				defer close(closed)
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				if scheme == "https" {
					conn = tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12})
				}
				if _, err := io.ReadFull(conn, make([]byte, 1)); err != nil {
					return
				}
				close(arrived)
				_, _ = io.Copy(io.Discard, conn) // Stall CONNECT/SOCKS negotiation.
			}()
			proxyURL, _ := url.Parse(scheme + "://" + listener.Addr().String())
			client := nativeWireClient(t, Windows, &http.Transport{Proxy: http.ProxyURL(proxyURL), TLSClientConfig: &tls.Config{RootCAs: roots}})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			request, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://native-target.invalid/cancel", nil)
			result := make(chan error, 1)
			go func() {
				response, err := client.Do(request)
				if response != nil {
					response.Body.Close()
				}
				result <- err
			}()
			select {
			case <-arrived:
			case <-time.After(2 * time.Second):
				t.Fatal("client did not reach proxy negotiation")
			}
			cancel()
			select {
			case err := <-result:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("proxy negotiation ignored cancellation: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("canceled proxy request did not return")
			}
			select {
			case <-closed:
			case <-time.After(time.Second):
				t.Fatal("canceled proxy negotiation leaked its socket")
			}
		})
	}
}

func TestNativeTransportTLSHandshakeTimeout(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		_, _ = io.Copy(io.Discard, conn) // Deliberately never answer ClientHello.
	}()
	client := nativeWireClient(t, Linux, &http.Transport{TLSHandshakeTimeout: 75 * time.Millisecond})
	started := time.Now()
	response, err := client.Get("https://" + listener.Addr().String())
	if response != nil {
		response.Body.Close()
	}
	var timeout net.Error
	if !errors.As(err, &timeout) || !timeout.Timeout() {
		t.Fatalf("expected handshake timeout, got %v", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("handshake timeout ignored: %v", elapsed)
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("timed-out handshake left the connection open")
	}
}

func TestNativeTransportResponseHeaderTimeout(t *testing.T) {
	arrived := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(arrived); <-r.Context().Done() }))
	t.Cleanup(server.Close)
	client := nativeWireClient(t, Linux, &http.Transport{ResponseHeaderTimeout: 75 * time.Millisecond})
	response, err := client.Get(server.URL)
	if response != nil {
		response.Body.Close()
	}
	var timeout net.Error
	if !errors.As(err, &timeout) || !timeout.Timeout() {
		t.Fatalf("expected response-header timeout, got %v", err)
	}
	select {
	case <-arrived:
	default:
		t.Fatal("request never reached local server")
	}
}

func TestNativeTransportSSETimeoutCancellationAndReuse(t *testing.T) {
	release := make(chan struct{})
	canceled := make(chan struct{})
	cancelHandlerRelease := make(chan struct{})
	var accepted atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/stream" || r.URL.Path == "/cancel" {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: first\n\n")
			w.(http.Flusher).Flush()
			if r.URL.Path == "/cancel" {
				<-r.Context().Done()
				close(canceled)
				// Keep the handler from racing cancellation with a clean final
				// chunk. The client must terminate its own blocked body read.
				<-cancelHandlerRelease
				return
			}
			select {
			case <-release:
				_, _ = io.WriteString(w, "data: second\n\n")
			case <-r.Context().Done():
			}
			return
		}
		_, _ = io.WriteString(w, "ok")
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			accepted.Add(1)
		}
	}
	server.StartTLS()
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(cancelHandlerRelease) })
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	client := nativeWireClient(t, Linux, &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots}, ResponseHeaderTimeout: 75 * time.Millisecond, MaxIdleConnsPerHost: 1})
	response, err := client.Get(server.URL + "/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	first := make([]byte, len("data: first\n\n"))
	if _, err := io.ReadFull(response.Body, first); err != nil {
		t.Fatal(err)
	}
	if string(first) != "data: first\n\n" {
		t.Fatalf("first SSE frame %q", first)
	}
	// ResponseHeaderTimeout must stop when headers arrive, not cut off SSE.
	<-time.After(150 * time.Millisecond)
	close(release)
	rest, err := io.ReadAll(response.Body)
	if err != nil || string(rest) != "data: second\n\n" {
		t.Fatalf("SSE body truncated: %q, %v", rest, err)
	}
	response.Body.Close()
	response, err = client.Get(server.URL + "/reuse")
	nativeWireDrain(t, response, err)
	if n := accepted.Load(); n != 1 {
		t.Fatalf("completed SSE connection not reused: %d", n)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/cancel", nil)
	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if _, err := io.ReadFull(response.Body, first); err != nil {
		t.Fatal(err)
	}
	cancel()
	if _, err := io.ReadAll(response.Body); !errors.Is(err, context.Canceled) {
		t.Fatalf("stream ignored cancellation: %v", err)
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("server did not observe SSE cancellation")
	}
}
