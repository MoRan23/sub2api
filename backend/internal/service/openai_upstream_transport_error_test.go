//go:build unit

package service

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"syscall"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestIsUpstreamNetworkError(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"DNS", &net.DNSError{Err: "no such host", Name: "proxy.example", IsNotFound: true}, true},
		{"DNS timeout", &net.DNSError{Err: "i/o timeout", Name: "proxy.example", IsTimeout: true}, true},
		{"TCP wrapped", &url.Error{Op: "Post", URL: "https://example.test", Err: &net.OpError{Op: "dial", Net: "tcp", Err: &os.SyscallError{Syscall: "connect", Err: syscall.ECONNREFUSED}}}, true},
		{"connection reset", syscall.ECONNRESET, true},
		{"connection aborted", syscall.ECONNABORTED, true},
		{"broken pipe", syscall.EPIPE, true},
		{"no route", syscall.EHOSTUNREACH, true},
		{"network unreachable", syscall.ENETUNREACH, true},
		{"timeout", syscall.ETIMEDOUT, true},
		{"deadline", context.DeadlineExceeded, true},
		{"cancel", context.Canceled, false},
		{"wrapped cancel", fmt.Errorf("request canceled: %w", context.Canceled), false},
		{"EOF", io.EOF, true},
		{"unexpected EOF", fmt.Errorf("read response: %w", io.ErrUnexpectedEOF), true},
		{"closed connection", net.ErrClosed, true},
		{"TLS record", tls.RecordHeaderError{Msg: "bad record"}, true},
		{"TLS certificate", &tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}}, true},
		{"certificate hostname", x509.HostnameError{Certificate: &x509.Certificate{}, Host: "example.test"}, true},
		{"socks proxy authentication", errors.New("socks connect tcp proxy.example:1080->example.test:443: username/password authentication failed"), true},
		{"HTTP proxy", errors.New("proxyconnect tcp: Proxy Authentication Required"), true},
		{"raw dial failure", errors.New("dial tcp 1.2.3.4:443: connect: connection refused"), true},
		{"raw response header timeout", errors.New(`Post "https://example.test": context deadline exceeded (Client.Timeout exceeded while awaiting headers)`), true},
		{"HTTP2 reset", errors.New("stream error: stream ID 3; INTERNAL_ERROR; received from peer"), true},
		{"raw EOF", errors.New(`Post "https://example.test": EOF`), true},
		{"invalid grant", errors.New("invalid_grant: authentication failed"), false},
		{"HTTP 502 text", errors.New("upstream returned HTTP 502: connection refused"), false},
		{"HTTP 503 body", errors.New("token 刷新失败 (HTTP 503): dial tcp: connection refused"), false},
		{"HTTP EOF body", errors.New("token refresh failed: status 503, body: EOF"), false},
		{"HTTP TLS body", errors.New("token refresh failed: status 503, body: tls: handshake failure"), false},
		{"HTTP error with network prose", infraerrors.New(http.StatusBadGateway, "OPENAI_OAUTH_TOKEN_REFRESH_FAILED", "token refresh failed: status 503, body: dial tcp: tls: failure"), false},
		{"HTTP wrapper with network cause", infraerrors.New(http.StatusBadGateway, "TOKEN_REFRESH_FAILED", "token refresh failed").WithCause(io.ErrUnexpectedEOF), true},
		{"actual HTTP 502", &UpstreamFailoverError{StatusCode: http.StatusBadGateway, ResponseBody: []byte(`{"error":{"message":"dial tcp: connection refused"}}`)}, false},
		{"synthetic 502", &UpstreamFailoverError{StatusCode: http.StatusBadGateway, TransportFailure: true}, true},
		{"protocol truncation", errors.New("upstream stream ended before terminal event"), false},
		{"invalid JSON", errors.New("invalid character 'x' looking for beginning of value"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, IsUpstreamNetworkError(tc.err))
		})
	}
}

func TestMarkUpstreamTransportFailure(t *testing.T) {
	require.Nil(t, MarkUpstreamTransportFailure(nil))
	cause := fmt.Errorf("upstream response read: %w", io.ErrUnexpectedEOF)
	marked := MarkUpstreamTransportFailure(cause)
	require.Equal(t, cause.Error(), marked.Error())
	require.ErrorIs(t, marked, io.ErrUnexpectedEOF)
	require.True(t, IsUpstreamTransportFailure(fmt.Errorf("forward: %w", marked)))
	require.Same(t, marked, MarkUpstreamTransportFailure(marked))
	require.False(t, IsUpstreamTransportFailure(cause))
	var failover *UpstreamFailoverError
	require.False(t, errors.As(marked, &failover), "marking must not enable failover")

	failover = &UpstreamFailoverError{StatusCode: http.StatusBadGateway, TransportFailure: true}
	require.True(t, IsUpstreamTransportFailure(failover))
	require.Same(t, failover, MarkUpstreamTransportFailure(failover))
	require.False(t, failover.ShouldReportAccountScheduleFailure())
	require.True(t, failover.ShouldRetryNextAccount())
	require.True(t, (&UpstreamFailoverError{StatusCode: http.StatusBadGateway}).ShouldReportAccountScheduleFailure())
}
