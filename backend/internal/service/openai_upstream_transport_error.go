package service

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"syscall"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/gin-gonic/gin"
)

// openAITransportFailoverBody is the OpenAI-format error body attached to the
// failover error for a transport-level failure. Kept identical to the legacy
// inline 502 body so the client-visible payload is unchanged if failover is
// ultimately exhausted.
var openAITransportFailoverBody = []byte(`{"error":{"type":"upstream_error","message":"Upstream request failed"}}`)

type upstreamTransportFailure struct {
	err error
}

func (e *upstreamTransportFailure) Error() string { return e.err.Error() }
func (e *upstreamTransportFailure) Unwrap() error { return e.err }

// MarkUpstreamTransportFailure preserves a confirmed transport failure on paths
// that cannot safely fail over, such as an already-sent plugin request.
func MarkUpstreamTransportFailure(err error) error {
	if err == nil || IsUpstreamTransportFailure(err) {
		return err
	}
	return &upstreamTransportFailure{err: err}
}

// IsUpstreamTransportFailure reads the source marker, not the client-facing 502.
func IsUpstreamTransportFailure(err error) bool {
	var marked *upstreamTransportFailure
	if errors.As(err, &marked) {
		return true
	}
	var failover *UpstreamFailoverError
	return errors.As(err, &failover) && failover.TransportFailure
}

// IsUpstreamNetworkError recognizes network causes before they are translated
// to protocol errors. HTTP responses and incomplete protocol messages are not
// network evidence on their own.
func IsUpstreamNetworkError(err error) bool {
	if err == nil {
		return false
	}
	if IsUpstreamTransportFailure(err) {
		return true
	}
	var failover *UpstreamFailoverError
	if errors.As(err, &failover) {
		return false
	}
	var applicationErr *infraerrors.ApplicationError
	if errors.As(err, &applicationErr) {
		return IsUpstreamNetworkError(applicationErr.Unwrap())
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed) ||
		errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.ECONNABORTED) || errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, syscall.ETIMEDOUT) ||
		errors.Is(err, syscall.EHOSTUNREACH) ||
		errors.Is(err, syscall.ENETUNREACH) {
		return true
	}
	var opErr *net.OpError
	var dnsErr *net.DNSError
	var timeoutErr net.Error
	var tlsRecordErr tls.RecordHeaderError
	var tlsCertErr *tls.CertificateVerificationError
	var certInvalidErr x509.CertificateInvalidError
	var certHostErr x509.HostnameError
	var certAuthorityErr x509.UnknownAuthorityError
	if errors.As(err, &opErr) || errors.As(err, &dnsErr) ||
		(errors.As(err, &timeoutErr) && timeoutErr.Timeout()) ||
		errors.As(err, &tlsRecordErr) || errors.As(err, &tlsCertErr) ||
		errors.As(err, &certInvalidErr) || errors.As(err, &certHostErr) || errors.As(err, &certAuthorityErr) {
		return true
	}

	// Proxy libraries and HTTP/2 may return untyped errors; require their
	// transport operation or an exact transport reason rather than HTTP prose.
	for cause := errors.Unwrap(err); cause != nil; cause = errors.Unwrap(err) {
		err = cause
	}
	message := strings.ToLower(err.Error())
	for _, method := range []string{"get \"", "post \"", "put \"", "patch \"", "delete \"", "head \"", "options \"", "connect \""} {
		if strings.HasPrefix(message, method) {
			_, reason, found := strings.Cut(message, "\": ")
			if found {
				message = reason
			}
			break
		}
	}
	if message == "eof" || message == "unexpected eof" || message == "i/o timeout" ||
		message == "connection reset by peer" || message == "connection reset" ||
		message == "broken pipe" || message == "use of closed network connection" ||
		message == "proxy authentication required" ||
		message == "context deadline exceeded (client.timeout exceeded while awaiting headers)" ||
		strings.HasPrefix(message, "stream error: stream id ") {
		return true
	}
	for _, operation := range []string{"dial tcp", "read tcp", "write tcp", "proxyconnect tcp:", "socks connect tcp", "tls: "} {
		if strings.HasPrefix(message, operation) {
			return true
		}
	}
	return false
}

// isClientCanceledTransportError reports whether a transport-level failure was
// caused by the client disconnecting: the request context itself is canceled
// and the round-trip aborted with context.Canceled. Such a failure says nothing
// about the upstream, so it is not recorded as an Ops upstream error event.
func isClientCanceledTransportError(ctx context.Context, err error) bool {
	return errors.Is(err, context.Canceled) && ctx != nil && errors.Is(ctx.Err(), context.Canceled)
}

// handleOpenAIUpstreamTransportError handles a transport-level upstream failure
// (Do/DoWithTLS returned a non-HTTP error: proxy/DNS/TCP/TLS). It:
//  1. records the failure in Ops error logs (status 0, kind=request_error),
//     except when the client disconnected (see isClientCanceledTransportError);
//  2. returns an error that is *UpstreamFailoverError (so the handler fails over
//     to a healthy account) for all non-canceled errors, or a plain error for
//     context.Canceled (client gone — no failover, no eviction).
//
// It deliberately does NOT write to the response: the handler owns the response
// (failover, or a protocol-correct error once failover is exhausted).
//
// passthrough tags the Ops error event for the OpenAI passthrough forward path.
func (s *OpenAIGatewayService) handleOpenAIUpstreamTransportError(ctx context.Context, c *gin.Context, account *Account, err error, passthrough bool) error {
	if isOpenAICandyTest(ctx) || isOpenAICandyTestContext(c) {
		return safeCandyTestError(ctx, err)
	}
	if isClientCanceledTransportError(ctx, err) {
		return err
	}
	safeErr := sanitizeUpstreamErrorMessage(err.Error())
	setOpsUpstreamError(c, 0, safeErr, "")
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		ProxyID:            opsOpenAIOutboundProxyID(c, account),
		ProxyName:          opsOpenAIOutboundProxyName(c, account),
		Platform:           account.Platform,
		AccountID:          account.ID,
		AccountName:        account.Name,
		UpstreamStatusCode: 0,
		Passthrough:        passthrough,
		Kind:               "request_error",
		Message:            safeErr,
	})

	// Client disconnected: do NOT fail over to another account and do NOT evict
	// this one — the upstream never had a chance to exhibit a fault.
	if errors.Is(err, context.Canceled) || (errors.Is(err, context.DeadlineExceeded) && errors.Is(ctx.Err(), context.DeadlineExceeded)) {
		return err
	}

	// Transport attempt reached the network path; count as Ollama Cloud / OpenCode Go activity.
	if s != nil {
		scheduleOllamaCloudUsageActivity(s.deferredService, account)
		scheduleOpenCodeGoUsageActivity(s.deferredService, account)
	}

	// 插件已把请求交给上游时，自动切换账号可能造成重复扣费或重复执行。
	var pluginErr *PluginTransportError
	if errors.As(err, &pluginErr) && pluginErr.RequestSent {
		if IsUpstreamNetworkError(err) {
			return MarkUpstreamTransportFailure(err)
		}
		return err
	}

	return &UpstreamFailoverError{
		StatusCode:       http.StatusBadGateway,
		ResponseBody:     openAITransportFailoverBody,
		TransportFailure: IsUpstreamNetworkError(err),
	}
}
