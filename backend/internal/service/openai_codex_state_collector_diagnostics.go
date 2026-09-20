package service

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http/httptrace"
	"sync"
	"syscall"
	"time"

	utls "github.com/refraction-networking/utls"
)

// The cause remains available for internal errors.Is/As inspection, but neither
// Error nor the persisted failure category can disclose its URL or credentials.
type codexTurnStateCollectorTransportError struct {
	cause error
	code  string
	stage string
}

func (e *codexTurnStateCollectorTransportError) Error() string { return e.code }
func (e *codexTurnStateCollectorTransportError) Unwrap() error { return e.cause }
func (e *codexTurnStateCollectorTransportError) Is(target error) bool {
	return target == errCodexTurnStateCollectorTransportFailed
}

// httptrace callbacks may run on transport goroutines after Do returns. Only
// fixed phases and the send timestamp are retained, never addresses or headers.
type codexTurnStateCollectorTrace struct {
	mu     sync.Mutex
	stage  string
	sentAt time.Time
}

func (t *codexTurnStateCollectorTrace) setStage(stage string) {
	t.mu.Lock()
	t.stage = stage
	t.mu.Unlock()
}

func (t *codexTurnStateCollectorTrace) markSent(at time.Time) {
	t.mu.Lock()
	t.sentAt = at
	t.mu.Unlock()
}

func (t *codexTurnStateCollectorTrace) snapshot() (string, time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.stage, t.sentAt
}

func (t *codexTurnStateCollectorTrace) hooks() *httptrace.ClientTrace {
	return &httptrace.ClientTrace{
		DNSStart: func(httptrace.DNSStartInfo) { t.setStage("dns") },
		DNSDone: func(info httptrace.DNSDoneInfo) {
			if info.Err == nil {
				t.setStage("connect")
			}
		},
		ConnectStart: func(string, string) { t.setStage("connect") },
		ConnectDone: func(_, _ string, err error) {
			if err == nil {
				t.setStage("proxy_tunnel")
			}
		},
		TLSHandshakeStart: func() { t.setStage("tls") },
		TLSHandshakeDone: func(_ tls.ConnectionState, err error) {
			if err == nil {
				t.setStage("request_write")
			}
		},
		GotConn: func(httptrace.GotConnInfo) { t.setStage("request_write") },
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			if info.Err == nil {
				t.setStage("response_headers")
			}
		},
	}
}

func newCodexTurnStateCollectorTransportError(err error, stage string) error {
	return &codexTurnStateCollectorTransportError{cause: err, stage: stage, code: classifyCodexTurnStateCollectorTransport(err, stage)}
}

func classifyCodexTurnStateCollectorTransport(err error, stage string) string {
	if errors.Is(err, ErrCodexTurnStateCollectorProxyUnavailable) {
		return "collector_proxy_unavailable"
	}
	var operation *net.OpError
	errors.As(err, &operation)
	if codexTurnStateCollectorTimedOut(err) {
		switch {
		case stage == "tls" || codexTurnStateErrorLeafText(err) == "net/http: TLS handshake timeout":
			return "collector_tls_timeout"
		case stage == "response_headers":
			return "collector_response_header_timeout"
		case stage == "dns" || stage == "connect" || operation != nil && (operation.Op == "dial" || operation.Op == "proxyconnect"):
			return "collector_connect_timeout"
		default:
			return "collection_timeout"
		}
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return "collector_dns_failed"
	}
	var unknownAuthority x509.UnknownAuthorityError
	var certificateInvalid x509.CertificateInvalidError
	var hostname x509.HostnameError
	var systemRoots x509.SystemRootsError
	var record tls.RecordHeaderError
	var nativeRecord utls.RecordHeaderError
	var alert tls.AlertError
	var nativeAlert utls.AlertError
	var verification *tls.CertificateVerificationError
	var nativeVerification *utls.CertificateVerificationError
	if stage == "tls" || errors.As(err, &unknownAuthority) || errors.As(err, &certificateInvalid) || errors.As(err, &hostname) ||
		errors.As(err, &systemRoots) || errors.As(err, &record) || errors.As(err, &nativeRecord) || errors.As(err, &alert) ||
		errors.As(err, &nativeAlert) || errors.As(err, &verification) || errors.As(err, &nativeVerification) {
		return "collector_tls_failed"
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return "collector_connection_refused"
	}
	if errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed) {
		return "collector_connection_closed"
	}
	leaf := codexTurnStateErrorLeafText(err)
	if operation != nil && (operation.Op == "remote error" || operation.Op == "local error") {
		switch leaf {
		case "tls: handshake failure", "tls: bad certificate", "tls: protocol version not supported", "tls: no application protocol":
			return "collector_tls_failed"
		}
	}
	// req discards a rejected CONNECT response and retains only its reason
	// phrase. Recognize the exact standard authentication phrase at this
	// transport boundary; arbitrary HTTP/body messages never enter this path.
	if leaf == "Proxy Authentication Required" {
		return "collector_proxy_auth_required"
	}
	if operation != nil && operation.Op == "socks connect" {
		switch leaf {
		case "username/password authentication failed", "no acceptable authentication methods", "invalid username/password", "invalid username/password version":
			return "collector_proxy_auth_required"
		default:
			return "collector_proxy_tunnel_failed"
		}
	}
	if stage == "proxy_tunnel" || operation != nil && operation.Op == "proxyconnect" {
		return "collector_proxy_tunnel_failed"
	}
	return "collector_transport_failed"
}

// Only inspect the final error in a bounded chain for known fixed transport
// templates. Never persist or log this text, nor search arbitrary wrappers.
func codexTurnStateErrorLeafText(err error) string {
	for range 16 {
		if err == nil {
			return ""
		}
		next := errors.Unwrap(err)
		if next == nil {
			return err.Error()
		}
		err = next
	}
	return ""
}

func codexTurnStateCollectorCanceled(err error) bool {
	return errors.Is(err, context.Canceled)
}

// A deadline observed after Collect returns must not erase the transport phase
// captured by that attempt or a precise stream rate-limit result.
func codexTurnStateCollectorDeadlineError(err error) error {
	var transport *codexTurnStateCollectorTransportError
	if errors.As(err, &transport) && transport.code != "collector_transport_failed" {
		return err
	}
	if errors.Is(err, errCodexTurnStateCollectorRateLimited) {
		return err
	}
	return context.DeadlineExceeded
}
