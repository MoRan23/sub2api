package service

import (
	"context"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// Retained in the connection contract for compatibility. Shared ticket/Cookie
// bundles apply only to physical HTTP requests, never native WS connections.
type openAICodexWSStateMode struct {
	Enabled    bool
	Generation string
}

func (m openAICodexWSStateMode) poolKey() string {
	if !m.Enabled {
		return ""
	}
	return "frame:" + m.Generation
}

func (s *OpenAIGatewayService) openAICodexWSStateMode(ctx context.Context, account *Account) openAICodexWSStateMode {
	return openAICodexWSStateMode{}
}

func (s *OpenAIGatewayService) openAICodexWSStateModeChanged(ctx context.Context, account *Account, initial openAICodexWSStateMode) bool {
	return false
}

// Native WS keeps the ordinary client provenance/forwarding rules. It must not
// read or inject an HTTP bundle, learn a ticket, or activate background collection.
// WS-to-HTTP bridges use the separate HTTP request path and remain supported.
func (s *OpenAIGatewayService) prepareOpenAICodexWSStateFrame(ctx context.Context, c *gin.Context, account *Account, payload []byte, firstGuardedHeaderToken string, credentialHeaders http.Header) ([]byte, *CodexTurnStateAttempt, error) {
	noteOpenAICodexStatePatch(c, nil, nil, nil)
	return payload, nil, nil
}

// Only these private fields are retained for checking a physical socket's
// credential generation. They never enter the fingerprint observation stream.
func cloneOpenAICodexWSCredentialHeaders(headers http.Header) http.Header {
	result := make(http.Header)
	for key, values := range headers {
		if strings.EqualFold(key, "Authorization") || strings.EqualFold(key, "ChatGPT-Account-Id") {
			for _, value := range values {
				result.Add(key, value)
			}
		}
	}
	return result
}

func openAIWSCodexStateCredentialHeaders(conn openAIWSClientConn, fallback http.Header) http.Header {
	if source, ok := conn.(interface{ CodexStateCredentialHeaders() http.Header }); ok {
		return source.CodexStateCredentialHeaders()
	}
	return cloneOpenAICodexWSCredentialHeaders(fallback)
}

func openAIWSCodexStateOutboundHeaderLength(conn openAIWSClientConn, fallback http.Header) int {
	if source, ok := conn.(interface{ CodexStateOutboundHeaderLength() int }); ok {
		return source.CodexStateOutboundHeaderLength()
	}
	return codexTurnStateHeaderLength(fallback)
}

func (s *OpenAIGatewayService) observeOpenAICodexWSStateHeaders(attempt *CodexTurnStateAttempt, headers http.Header) {
	if s != nil && s.codexTurnStateService != nil && attempt != nil {
		s.codexTurnStateService.ObserveHeaders(attempt, headers)
		// The same headers may be reused with the physical connection; they are
		// not a fresh per-turn response declaration.
		observeCodexModelHeaders(attempt, headers, "connection")
	}
}

func (s *OpenAIGatewayService) observeOpenAICodexWSStateEvent(attempt *CodexTurnStateAttempt, payload []byte) {
	if s != nil && s.codexTurnStateService != nil && attempt != nil {
		s.codexTurnStateService.ObserveEvent(attempt, payload)
	}
}

func (s *OpenAIGatewayService) finishOpenAICodexWSState(ctx context.Context, attempt *CodexTurnStateAttempt, delivered bool) {
	if s != nil && s.codexTurnStateService != nil && attempt != nil {
		// A client disconnect must still release the natural-request reservation.
		_ = s.codexTurnStateService.Finish(context.WithoutCancel(ctx), attempt, delivered)
		finishOpenAICodexStateObservation(attempt)
	}
}
