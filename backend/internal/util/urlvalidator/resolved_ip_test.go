package urlvalidator

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/shadowsockstest"
	M "github.com/sagernet/sing/common/metadata"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/dns/dnsmessage"
)

type dnsRoundTripFunc func(*http.Request) (*http.Response, error)

func (f dnsRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func dnsQuestion(t *testing.T, req *http.Request) dnsmessage.Message {
	t.Helper()
	require.Equal(t, proxyDNSEndpoint, req.URL.String())
	require.Equal(t, http.MethodPost, req.Method)
	require.Equal(t, "application/dns-message", req.Header.Get("Content-Type"))
	require.Equal(t, "application/dns-message", req.Header.Get("Accept"))
	body, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	var query dnsmessage.Message
	require.NoError(t, query.Unpack(body))
	require.Len(t, query.Questions, 1)
	require.Equal(t, dnsmessage.ClassINET, query.Questions[0].Class)
	return query
}

func dnsReply(t *testing.T, query dnsmessage.Message, answers ...dnsmessage.Resource) *http.Response {
	t.Helper()
	message := dnsmessage.Message{
		Header:    dnsmessage.Header{ID: query.ID, Response: true, RecursionAvailable: true},
		Questions: query.Questions,
		Answers:   answers,
	}
	return packedDNSReply(t, message)
}

func packedDNSReply(t *testing.T, message dnsmessage.Message) *http.Response {
	t.Helper()
	body, err := message.Pack()
	require.NoError(t, err)
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/dns-message"}}, Body: io.NopCloser(bytes.NewReader(body))}
}

func dnsAddress(name dnsmessage.Name, raw string) dnsmessage.Resource {
	ip := net.ParseIP(raw)
	header := dnsmessage.ResourceHeader{Name: name, Class: dnsmessage.ClassINET, TTL: 30}
	if v4 := ip.To4(); v4 != nil {
		header.Type = dnsmessage.TypeA
		return dnsmessage.Resource{Header: header, Body: &dnsmessage.AResource{A: [4]byte(v4)}}
	}
	header.Type = dnsmessage.TypeAAAA
	return dnsmessage.Resource{Header: header, Body: &dnsmessage.AAAAResource{AAAA: [16]byte(ip.To16())}}
}

func mockedDNSValidator(t *testing.T, roundTrip dnsRoundTripFunc) *ResolvedIPValidator {
	t.Helper()
	return &ResolvedIPValidator{
		client: &http.Client{Transport: roundTrip}, endpoint: proxyDNSEndpoint,
		lookupIP: func(context.Context, string, string) ([]net.IP, error) {
			t.Fatal("proxied validation must never use the local resolver")
			return nil, nil
		},
	}
}

func TestResolvedIPValidatorProxyDNSAddresses(t *testing.T) {
	for _, tc := range []struct {
		name    string
		v4, v6  []string
		wantErr bool
	}{
		{name: "IPv4 only", v4: []string{"93.184.216.34"}},
		{name: "IPv6 only", v6: []string{"2606:4700:4700::1111"}},
		{name: "dual stack", v4: []string{"93.184.216.34"}, v6: []string{"2606:4700:4700::1111"}},
		{name: "no addresses", wantErr: true},
		{name: "mixed public and private", v4: []string{"93.184.216.34", "10.0.0.1"}, wantErr: true},
		{name: "IPv6 private", v4: []string{"93.184.216.34"}, v6: []string{"fd00::1"}, wantErr: true},
		{name: "IPv6 link local", v6: []string{"fe80::1"}, wantErr: true},
		{name: "loopback", v4: []string{"127.0.0.1"}, wantErr: true},
		{name: "unspecified", v4: []string{"0.0.0.0"}, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var questions []dnsmessage.Type
			v := mockedDNSValidator(t, func(req *http.Request) (*http.Response, error) {
				query := dnsQuestion(t, req)
				question := query.Questions[0]
				require.Equal(t, "example.test.", question.Name.String())
				questions = append(questions, question.Type)
				ips := tc.v4
				if question.Type == dnsmessage.TypeAAAA {
					ips = tc.v6
				}
				var answers []dnsmessage.Resource
				for _, ip := range ips {
					answers = append(answers, dnsAddress(question.Name, ip))
				}
				return dnsReply(t, query, answers...), nil
			})
			err := v.Validate(context.Background(), "EXAMPLE.test.")
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, []dnsmessage.Type{dnsmessage.TypeA, dnsmessage.TypeAAAA}, questions)
		})
	}
}

func TestResolvedIPValidatorLiteralAddressesDoNotResolve(t *testing.T) {
	v := mockedDNSValidator(t, func(*http.Request) (*http.Response, error) {
		t.Fatal("literal addresses must not trigger DNS")
		return nil, nil
	})
	for _, host := range []string{"1.1.1.1", "2606:4700:4700::1111"} {
		require.NoError(t, v.Validate(context.Background(), host))
	}
	for _, host := range []string{"", "localhost", "127.0.0.1", "::1", "::ffff:192.168.1.1", "0.0.0.0", "224.0.0.1"} {
		require.Error(t, v.Validate(context.Background(), host))
	}
}

func TestResolvedIPValidatorProxyDNSAliases(t *testing.T) {
	v := mockedDNSValidator(t, func(req *http.Request) (*http.Response, error) {
		query := dnsQuestion(t, req)
		alias := dnsmessage.MustNewName("resolved.example.test.")
		answers := []dnsmessage.Resource{{
			Header: dnsmessage.ResourceHeader{Name: query.Questions[0].Name, Type: dnsmessage.TypeCNAME, Class: dnsmessage.ClassINET},
			Body:   &dnsmessage.CNAMEResource{CNAME: alias},
		}}
		if query.Questions[0].Type == dnsmessage.TypeA {
			answers = append(answers, dnsAddress(alias, "93.184.216.34"))
		}
		return dnsReply(t, query, answers...), nil
	})
	require.NoError(t, v.Validate(context.Background(), "example.test"))
}

func TestResolvedIPValidatorRejectsInvalidProxyDNS(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*dnsmessage.Message, *http.Response)
	}{
		{"wrong ID", func(m *dnsmessage.Message, _ *http.Response) { m.ID++ }},
		{"not response", func(m *dnsmessage.Message, _ *http.Response) { m.Response = false }},
		{"opcode", func(m *dnsmessage.Message, _ *http.Response) { m.OpCode = 1 }},
		{"truncated", func(m *dnsmessage.Message, _ *http.Response) { m.Truncated = true }},
		{"NXDOMAIN", func(m *dnsmessage.Message, _ *http.Response) { m.RCode = dnsmessage.RCodeNameError }},
		{"SERVFAIL", func(m *dnsmessage.Message, _ *http.Response) { m.RCode = dnsmessage.RCodeServerFailure }},
		{"missing question", func(m *dnsmessage.Message, _ *http.Response) { m.Questions = nil }},
		{"wrong question name", func(m *dnsmessage.Message, _ *http.Response) {
			m.Questions[0].Name = dnsmessage.MustNewName("other.test.")
		}},
		{"wrong question type", func(m *dnsmessage.Message, _ *http.Response) { m.Questions[0].Type = dnsmessage.TypeAAAA }},
		{"wrong question class", func(m *dnsmessage.Message, _ *http.Response) { m.Questions[0].Class = dnsmessage.ClassCHAOS }},
		{"wrong answer name", func(m *dnsmessage.Message, _ *http.Response) {
			m.Answers[0].Header.Name = dnsmessage.MustNewName("other.test.")
		}},
		{"wrong answer class", func(m *dnsmessage.Message, _ *http.Response) { m.Answers[0].Header.Class = dnsmessage.ClassCHAOS }},
		{"wrong answer type", func(m *dnsmessage.Message, _ *http.Response) {
			m.Answers[0] = dnsAddress(m.Questions[0].Name, "2606:4700:4700::1111")
		}},
		{"alias loop", func(m *dnsmessage.Message, _ *http.Response) {
			m.Answers = []dnsmessage.Resource{{Header: dnsmessage.ResourceHeader{Name: m.Questions[0].Name, Type: dnsmessage.TypeCNAME, Class: dnsmessage.ClassINET}, Body: &dnsmessage.CNAMEResource{CNAME: m.Questions[0].Name}}}
		}},
		{"HTTP error", func(_ *dnsmessage.Message, r *http.Response) { r.StatusCode = http.StatusBadGateway }},
		{"redirect", func(_ *dnsmessage.Message, r *http.Response) { r.StatusCode = http.StatusFound }},
		{"invalid media type", func(_ *dnsmessage.Message, r *http.Response) { r.Header.Set("Content-Type", "text/html") }},
		{"missing media type", func(_ *dnsmessage.Message, r *http.Response) { r.Header.Del("Content-Type") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := mockedDNSValidator(t, func(req *http.Request) (*http.Response, error) {
				query := dnsQuestion(t, req)
				message := dnsmessage.Message{Header: dnsmessage.Header{ID: query.ID, Response: true}, Questions: append([]dnsmessage.Question(nil), query.Questions...), Answers: []dnsmessage.Resource{dnsAddress(query.Questions[0].Name, "93.184.216.34")}}
				response := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/dns-message"}}}
				tc.mutate(&message, response)
				response.Body = packedDNSReply(t, message).Body
				return response, nil
			})
			require.Error(t, v.Validate(context.Background(), "example.test"))
		})
	}
	for _, body := range [][]byte{{0, 1}, bytes.Repeat([]byte{0}, maxDNSMessageSize+1)} {
		v := mockedDNSValidator(t, func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/dns-message"}}, Body: io.NopCloser(bytes.NewReader(body))}, nil
		})
		require.Error(t, v.Validate(context.Background(), "example.test"))
	}
}

func TestResolvedIPValidatorFailureAndCancellation(t *testing.T) {
	v := mockedDNSValidator(t, func(*http.Request) (*http.Response, error) {
		return nil, errors.New("proxy failed: http://secret:password@proxy.test")
	})
	err := v.Validate(context.Background(), "example.test")
	require.ErrorContains(t, err, "proxy dns request failed")
	require.NotContains(t, err.Error(), "secret")
	require.NotContains(t, err.Error(), "password")
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, v.Validate(canceled, "example.test"), context.Canceled)
	v = mockedDNSValidator(t, func(req *http.Request) (*http.Response, error) {
		<-req.Context().Done()
		return nil, req.Context().Err()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, v.Validate(ctx, "example.test"), context.DeadlineExceeded)
}

func TestResolvedIPValidatorConstructorIsolation(t *testing.T) {
	for _, proxy := range []string{"nonsense", "ftp://proxy.test", "ss://missing-credentials@proxy.test:443"} {
		_, err := NewResolvedIPValidator(proxy)
		require.Error(t, err)
	}
	a, err := NewResolvedIPValidator("http://first:secret@127.0.0.1:8080")
	require.NoError(t, err)
	defer a.CloseIdleConnections()
	b, err := NewResolvedIPValidator("http://second:other@127.0.0.1:8080")
	require.NoError(t, err)
	defer b.CloseIdleConnections()
	require.NotSame(t, a.client.Transport, b.client.Transport)
	transport, ok := a.client.Transport.(*http.Transport)
	require.True(t, ok)
	require.True(t, transport.TLSClientConfig == nil || !transport.TLSClientConfig.InsecureSkipVerify)
	require.ErrorIs(t, a.client.CheckRedirect(nil, nil), http.ErrUseLastResponse)
}

func TestResolvedIPValidatorDirectUsesCallerContext(t *testing.T) {
	type key struct{}
	ctx := context.WithValue(context.Background(), key{}, "caller")
	v := &ResolvedIPValidator{lookupIP: func(ctx context.Context, network, host string) ([]net.IP, error) {
		require.Equal(t, "caller", ctx.Value(key{}))
		require.Equal(t, "ip", network)
		require.Equal(t, "example.test", host)
		_, hasDeadline := ctx.Deadline()
		require.True(t, hasDeadline)
		return []net.IP{net.ParseIP("93.184.216.34")}, nil
	}}
	require.NoError(t, v.Validate(ctx, "example.test"))
}

func TestResolvedIPValidatorShadowsocksDoH(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IPAddresses: []net.IP{net.ParseIP("1.1.1.1")}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	certificate, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	roots := x509.NewCertPool()
	roots.AddCert(certificate)
	var queries atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/dns-query" || req.Method != http.MethodPost {
			http.Error(w, "wrong request", http.StatusBadRequest)
			return
		}
		body, readErr := io.ReadAll(req.Body)
		if readErr != nil {
			http.Error(w, "read failed", http.StatusBadRequest)
			return
		}
		var query dnsmessage.Message
		if query.Unpack(body) != nil || len(query.Questions) != 1 {
			http.Error(w, "bad DNS", http.StatusBadRequest)
			return
		}
		queries.Add(1)
		message := dnsmessage.Message{Header: dnsmessage.Header{ID: query.ID, Response: true}, Questions: query.Questions}
		if query.Questions[0].Type == dnsmessage.TypeA {
			message.Answers = []dnsmessage.Resource{dnsAddress(query.Questions[0].Name, "93.184.216.34")}
		}
		body, packErr := message.Pack()
		if packErr != nil {
			http.Error(w, "pack failed", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/dns-message")
		_, _ = w.Write(body)
	}))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	defer server.Close()
	var tunnels atomic.Int32
	proxy := shadowsockstest.Start(t, "2022-blake3-aes-128-gcm", "MDEyMzQ1Njc4OWFiY2RlZg==", func(conn net.Conn, destination M.Socksaddr) error {
		if destination.String() != "1.1.1.1:443" {
			return fmt.Errorf("unexpected DNS endpoint: %s", destination.String())
		}
		tunnels.Add(1)
		upstream, dialErr := net.DialTimeout("tcp", server.Listener.Addr().String(), time.Second)
		if dialErr != nil {
			return dialErr
		}
		defer func() { _ = upstream.Close() }()
		copied := make(chan struct{})
		go func() { _, _ = io.Copy(upstream, conn); _ = upstream.Close(); close(copied) }()
		_, _ = io.Copy(conn, upstream)
		_ = conn.Close()
		<-copied
		return nil
	})
	v, err := NewResolvedIPValidator(proxy.String())
	require.NoError(t, err)
	defer v.CloseIdleConnections()
	transport, ok := v.client.Transport.(*http.Transport)
	require.True(t, ok)
	transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	v.lookupIP = func(context.Context, string, string) ([]net.IP, error) {
		return nil, errors.New("local resolver must not be used")
	}
	require.NoError(t, v.Validate(context.Background(), "must-not-resolve-locally.invalid"))
	require.Equal(t, int32(2), queries.Load())
	require.Positive(t, tunnels.Load())
	require.Equal(t, proxyDNSEndpoint, v.endpoint)
	require.False(t, strings.Contains(v.endpoint, "127.0.0.1"))
}
