package service

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
)

// gatewayTransportFailoverBody is the Anthropic-format error body attached to
// the failover error for a transport-level failure. Kept identical to the
// legacy inline 502 body so the client-visible payload is unchanged if
// failover is ultimately exhausted.
var gatewayTransportFailoverBody = []byte(`{"type":"error","error":{"type":"upstream_error","message":"Upstream request failed"}}`)

// handleUpstreamTransportError handles a transport-level upstream failure on
// the Anthropic/Bedrock forward paths (Do/DoWithTLS returned a non-HTTP error:
// proxy / DNS / TCP / TLS). It:
//  1. records the failure in Ops error logs (status 0, kind=request_error) —
//     the caller passes path-specific fields (UpstreamURL, Passthrough) via
//     event; identity, proxy attribution and classification fields are
//     filled here from the same account snapshot that built the transport.
//     A client disconnect is not recorded (see isClientCanceledTransportError);
//  2. returns an error that is *UpstreamFailoverError (so the handler fails
//     over to a healthy account) for all non-canceled errors, or the original
//     error for context.Canceled (client gone — no failover, no eviction).
//
// It deliberately does NOT write to the response: the handler owns the
// response (failover, or a protocol-correct error once failover is exhausted).
func (s *GatewayService) handleUpstreamTransportError(ctx context.Context, c *gin.Context, account *Account, err error, event OpsUpstreamErrorEvent) error {
	if isClientCanceledTransportError(ctx, err) {
		return err
	}
	safeErr := sanitizeUpstreamErrorMessage(err.Error())
	setOpsUpstreamError(c, 0, safeErr, "")
	event.ProxyID, event.ProxyName = opsUpstreamProxyAttribution(account)
	event.Platform = account.Platform
	event.AccountID = account.ID
	event.AccountName = account.Name
	event.UpstreamStatusCode = 0
	event.Kind = "request_error"
	event.Message = safeErr
	appendOpsUpstreamError(c, event)

	// Client disconnected: do NOT fail over to another account and do NOT
	// evict this one — the upstream never had a chance to exhibit a fault.
	if errors.Is(err, context.Canceled) || (errors.Is(err, context.DeadlineExceeded) && errors.Is(ctx.Err(), context.DeadlineExceeded)) {
		return err
	}

	// Transport attempt left local validation; count Ollama Cloud activity.
	scheduleOllamaCloudUsageActivity(s.deferredService, account)

	return &UpstreamFailoverError{
		StatusCode:       http.StatusBadGateway,
		ResponseBody:     gatewayTransportFailoverBody,
		TransportFailure: IsUpstreamNetworkError(err),
	}
}
