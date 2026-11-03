package service

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptrace"
	"sync/atomic"

	"github.com/gin-gonic/gin"
)

// codexEngineTransportAttempt belongs to one physical HTTP attempt. Its flags
// are monotonic even if the client follows redirects or retries a connection.
// A connection acquisition is already an uncertain send boundary: a partial
// write can fail before WroteHeaders/WroteRequest reports successful delivery.
type codexEngineTransportAttempt struct {
	acquiring atomic.Bool
	maySend   atomic.Bool
}

func (a *codexEngineTransportAttempt) traceRequest(req *http.Request) *http.Request {
	markPossibleSend := func() { a.maySend.Store(true) }
	trace := &httptrace.ClientTrace{
		GetConn:          func(string) { a.acquiring.Store(true) },
		GotConn:          func(httptrace.GotConnInfo) { markPossibleSend() },
		WroteHeaderField: func(string, []string) { markPossibleSend() },
		WroteHeaders:     markPossibleSend,
		WroteRequest:     func(httptrace.WroteRequestInfo) { markPossibleSend() },
	}
	return req.WithContext(httptrace.WithClientTrace(req.Context(), trace))
}

func (a *codexEngineTransportAttempt) definitelyUnsent(err error) bool {
	if !a.acquiring.Load() || a.maySend.Load() {
		return false
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}
	var opErr *net.OpError
	return errors.As(err, &opErr) && opErr.Op == "dial"
}

func (s *OpenAIGatewayService) handleCodexEngineTransportError(ctx context.Context, c *gin.Context, account *Account, err error, attempt *codexEngineTransportAttempt) error {
	if attempt.definitelyUnsent(err) {
		return s.handleOpenAIUpstreamTransportError(ctx, c, account, err, true)
	}
	if isOpenAICandyTest(ctx) || isOpenAICandyTestContext(c) {
		return safeCandyTestError(ctx, err)
	}
	if isClientCanceledTransportError(ctx, err) {
		return err
	}

	// Engine may already have accepted this request. Record the original
	// network error without turning it into a failover or changing account
	// health; only Engine can reconcile the execution's eventual result.
	safeErr := sanitizeUpstreamErrorMessage(err.Error())
	setOpsUpstreamError(c, 0, safeErr, "")
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		ProxyID:     opsOpenAIOutboundProxyID(c, account),
		ProxyName:   opsOpenAIOutboundProxyName(c, account),
		Platform:    account.Platform,
		AccountID:   account.ID,
		AccountName: account.Name,
		Passthrough: true,
		Kind:        "request_error",
		Message:     safeErr,
	})
	return err
}
