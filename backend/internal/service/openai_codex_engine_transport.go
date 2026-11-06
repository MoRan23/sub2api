package service

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptrace"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
)

// codexEngineTransportAttempt belongs to one physical HTTP attempt. Its flags
// are monotonic even if the client follows redirects or retries a connection.
// A connection acquisition is already an uncertain send boundary: a partial
// write can fail before WroteHeaders/WroteRequest reports successful delivery.
type codexEngineTransportAttempt struct {
	acquiring atomic.Bool
	maySend   atomic.Bool
	mu        sync.Mutex
	started   time.Time
	phase     string
	marks     map[string]int64
	reused    bool
}

// Only fixed stage names and elapsed times are stored. Trace callbacks can
// contain credentials, destinations and certificates; none are retained here.
func (a *codexEngineTransportAttempt) mark(phase string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.started.IsZero() {
		a.started = time.Now()
	}
	if a.marks == nil {
		a.marks = make(map[string]int64)
	}
	a.phase = phase
	a.marks[phase] = time.Since(a.started).Milliseconds()
}

func (a *codexEngineTransportAttempt) diagnostics(err error) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	phase := a.phase
	if phase == "" {
		phase = "unobserved"
	}
	kind := "transport_error"
	var dnsErr *net.DNSError
	var netErr net.Error
	switch {
	case errors.Is(err, context.Canceled):
		kind = "canceled"
	case errors.As(err, &dnsErr):
		kind = "dns_error"
	case errors.As(err, &netErr) && netErr.Timeout():
		kind = "timeout"
	}
	encoded, _ := json.Marshal(map[string]any{"transport": map[string]any{
		"phase": phase, "error_class": kind, "stage_elapsed_ms": a.marks,
		"connection_reused": a.reused, "may_have_sent": a.maySend.Load(),
	}})
	return string(encoded)
}

func (a *codexEngineTransportAttempt) traceRequest(req *http.Request) *http.Request {
	a.mark("start")
	markPossibleSend := func() { a.maySend.Store(true) }
	trace := &httptrace.ClientTrace{
		GetConn: func(string) { a.acquiring.Store(true); a.mark("connection_wait") },
		GotConn: func(info httptrace.GotConnInfo) {
			markPossibleSend()
			a.mu.Lock()
			a.reused = info.Reused
			a.mu.Unlock()
			a.mark("connection_acquired")
		},
		DNSStart: func(httptrace.DNSStartInfo) { a.mark("dns_start") },
		DNSDone: func(info httptrace.DNSDoneInfo) {
			if info.Err != nil {
				a.mark("dns_failed")
			} else {
				a.mark("dns_done")
			}
		},
		ConnectStart: func(string, string) { a.mark("tcp_start") },
		ConnectDone: func(_, _ string, err error) {
			if err != nil {
				a.mark("tcp_failed")
			} else {
				a.mark("tcp_done")
			}
		},
		TLSHandshakeStart: func() { a.mark("tls_start") },
		TLSHandshakeDone: func(_ tls.ConnectionState, err error) {
			if err != nil {
				a.mark("tls_failed")
			} else {
				a.mark("tls_done")
			}
		},
		WroteHeaderField: func(string, []string) { markPossibleSend() },
		WroteHeaders:     func() { markPossibleSend(); a.mark("headers_written") },
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			markPossibleSend()
			if info.Err != nil {
				a.mark("write_failed")
			} else {
				a.mark("request_written")
			}
		},
		GotFirstResponseByte: func() { a.mark("first_response_byte") },
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
		before := 0
		if value, ok := c.Get(OpsUpstreamErrorsKey); ok {
			if events, ok := value.([]*OpsUpstreamErrorEvent); ok {
				before = len(events)
			}
		}
		result := s.handleOpenAIUpstreamTransportError(ctx, c, account, err, true)
		if value, ok := c.Get(OpsUpstreamErrorsKey); ok {
			if events, ok := value.([]*OpsUpstreamErrorEvent); ok && len(events) > before {
				events[len(events)-1].Detail = attempt.diagnostics(err)
			}
		}
		return result
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
		Detail:      attempt.diagnostics(err),
	})
	return err
}
