package urlvalidator

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/proxyurl"
	"github.com/Wei-Shaw/sub2api/internal/pkg/proxyutil"
	"golang.org/x/net/dns/dnsmessage"
	"golang.org/x/net/idna"
)

const (
	resolvedIPTimeout = 5 * time.Second
	// A literal address avoids a local DNS bootstrap for the resolver itself.
	// TLS still authenticates this endpoint using the system certificate roots.
	proxyDNSEndpoint  = "https://1.1.1.1/dns-query"
	maxDNSMessageSize = 65535
)

// ResolvedIPValidator checks target addresses using the same proxy as its owner.
// Reuse it alongside a transport; each instance owns a separate connection pool
// and never shares credentials, DNS results or route state with another proxy.
type ResolvedIPValidator struct {
	client   *http.Client
	endpoint string
	lookupIP func(context.Context, string, string) ([]net.IP, error)
}

// NewResolvedIPValidator performs no network I/O. An empty proxy uses the local
// resolver; a configured proxy uses authenticated DNS-over-HTTPS through that
// proxy only. A proxy failure never falls back to the local resolver or direct.
func NewResolvedIPValidator(rawProxyURL string) (*ResolvedIPValidator, error) {
	_, proxy, err := proxyurl.Parse(rawProxyURL)
	if err != nil {
		return nil, err
	}
	if proxy == nil {
		return &ResolvedIPValidator{lookupIP: net.DefaultResolver.LookupIP}, nil
	}
	transport := &http.Transport{
		DialContext:           (&net.Dialer{Timeout: resolvedIPTimeout, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   resolvedIPTimeout,
		ResponseHeaderTimeout: resolvedIPTimeout,
		IdleConnTimeout:       30 * time.Second,
		MaxIdleConns:          2,
		MaxIdleConnsPerHost:   2,
		ForceAttemptHTTP2:     true,
	}
	if err := proxyutil.ConfigureTransportProxy(transport, proxy); err != nil {
		return nil, err
	}
	return &ResolvedIPValidator{
		client: &http.Client{
			Transport:     transport,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		endpoint: proxyDNSEndpoint,
	}, nil
}

func (v *ResolvedIPValidator) CloseIdleConnections() {
	if v != nil && v.client != nil {
		v.client.CloseIdleConnections()
	}
}

// ValidateResolvedIPContext retains direct-mode validation while propagating the
// caller's cancellation instead of starting an unrelated background lookup.
func ValidateResolvedIPContext(ctx context.Context, host string) error {
	v, err := NewResolvedIPValidator("")
	if err != nil {
		return err
	}
	return v.Validate(ctx, host)
}

// ValidateResolvedIPWithProxy is a one-shot convenience API. Client pools should
// retain NewResolvedIPValidator's result to reuse its DoH connections.
func ValidateResolvedIPWithProxy(ctx context.Context, host, proxyURL string) error {
	v, err := NewResolvedIPValidator(proxyURL)
	if err != nil {
		return err
	}
	defer v.CloseIdleConnections()
	return v.Validate(ctx, host)
}

func (v *ResolvedIPValidator) Validate(ctx context.Context, host string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if host == "" || IsBlockedHost(host) {
		return errors.New("host is not allowed")
	}
	if ip := net.ParseIP(host); ip != nil {
		return validateResolvedAddresses([]net.IP{ip})
	}
	if v == nil || (v.client == nil && v.lookupIP == nil) {
		return errors.New("dns validator is not configured")
	}
	host, err := idna.Lookup.ToASCII(host)
	if err != nil || strings.ContainsAny(host, " :/\\\t\r\n%[]") {
		return errors.New("invalid dns hostname")
	}
	ctx, cancel := context.WithTimeout(ctx, resolvedIPTimeout)
	defer cancel()
	var ips []net.IP
	if v.client == nil {
		ips, err = v.lookupIP(ctx, "ip", host)
	} else {
		ips, err = v.lookupProxy(ctx, host)
	}
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("dns resolution failed: %w", err)
	}
	return validateResolvedAddresses(ips)
}

func validateResolvedAddresses(ips []net.IP) error {
	if len(ips) == 0 {
		return errors.New("dns response contains no addresses")
	}
	for _, ip := range ips {
		if ip == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
			return errors.New("resolved ip is not allowed")
		}
	}
	return nil
}

func (v *ResolvedIPValidator) lookupProxy(ctx context.Context, host string) ([]net.IP, error) {
	name, err := dnsmessage.NewName(host + ".")
	if err != nil {
		return nil, errors.New("invalid dns hostname")
	}
	var addresses []net.IP
	for _, typ := range []dnsmessage.Type{dnsmessage.TypeA, dnsmessage.TypeAAAA} {
		var randomID [2]byte
		if _, err := rand.Read(randomID[:]); err != nil {
			return nil, errors.New("dns request could not be initialized")
		}
		query := dnsmessage.Message{
			Header:    dnsmessage.Header{ID: binary.BigEndian.Uint16(randomID[:]), RecursionDesired: true},
			Questions: []dnsmessage.Question{{Name: name, Type: typ, Class: dnsmessage.ClassINET}},
		}
		body, err := query.Pack()
		if err != nil {
			return nil, errors.New("invalid dns hostname")
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, v.endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, errors.New("invalid dns resolver endpoint")
		}
		req.Header.Set("Accept", "application/dns-message")
		req.Header.Set("Content-Type", "application/dns-message")
		resp, err := v.client.Do(req)
		if err != nil {
			// net/http errors may include a proxy URL. Keep only cancellation as
			// typed caller-visible state and do not expose credentials.
			return nil, errors.New("proxy dns request failed")
		}
		ips, err := readDNSResponse(resp, query)
		_ = resp.Body.Close()
		if err != nil {
			return nil, err
		}
		addresses = append(addresses, ips...)
	}
	return addresses, nil
}

func readDNSResponse(resp *http.Response, query dnsmessage.Message) ([]net.IP, error) {
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New("proxy dns resolver returned an unsuccessful status")
	}
	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/dns-message" {
		return nil, errors.New("proxy dns resolver returned an invalid content type")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxDNSMessageSize+1))
	if err != nil || len(body) > maxDNSMessageSize {
		return nil, errors.New("invalid dns response body")
	}
	var response dnsmessage.Message
	if err := response.Unpack(body); err != nil {
		return nil, errors.New("malformed dns response")
	}
	if response.ID != query.ID || !response.Response || response.OpCode != 0 || response.Truncated || response.RCode != dnsmessage.RCodeSuccess || len(response.Questions) != 1 {
		return nil, errors.New("invalid dns response header")
	}
	question := response.Questions[0]
	expected := query.Questions[0]
	if question.Type != expected.Type || question.Class != expected.Class || !strings.EqualFold(question.Name.String(), expected.Name.String()) {
		return nil, errors.New("dns response question does not match")
	}
	aliases := make(map[string]string)
	for _, answer := range response.Answers {
		if answer.Header.Class != dnsmessage.ClassINET {
			return nil, errors.New("invalid dns answer class")
		}
		if cname, ok := answer.Body.(*dnsmessage.CNAMEResource); ok {
			owner, target := strings.ToLower(answer.Header.Name.String()), strings.ToLower(cname.CNAME.String())
			if previous, exists := aliases[owner]; exists && previous != target {
				return nil, errors.New("conflicting dns aliases")
			}
			aliases[owner] = target
		}
	}
	validNames := make(map[string]bool)
	for name := strings.ToLower(expected.Name.String()); ; {
		if validNames[name] || len(validNames) >= 64 {
			return nil, errors.New("invalid dns alias chain")
		}
		validNames[name] = true
		next, ok := aliases[name]
		if !ok {
			break
		}
		name = next
	}
	var addresses []net.IP
	for _, answer := range response.Answers {
		if !validNames[strings.ToLower(answer.Header.Name.String())] {
			return nil, errors.New("dns answer name does not match")
		}
		switch record := answer.Body.(type) {
		case *dnsmessage.CNAMEResource:
			continue
		case *dnsmessage.AResource:
			if expected.Type != dnsmessage.TypeA {
				return nil, errors.New("dns answer type does not match")
			}
			addresses = append(addresses, net.IP(record.A[:]))
		case *dnsmessage.AAAAResource:
			if expected.Type != dnsmessage.TypeAAAA {
				return nil, errors.New("dns answer type does not match")
			}
			addresses = append(addresses, net.IP(record.AAAA[:]))
		default:
			return nil, errors.New("unsupported dns answer type")
		}
	}
	return addresses, nil
}
