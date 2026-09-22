package service

import (
	"context"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// The mode is internal connection state, never a client-controlled wire header.
// Its generation makes otherwise identical pooled sockets incompatible after
// the account's configuration or effective OAuth credentials change.
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
	if s == nil || s.codexTurnStateService == nil {
		return openAICodexWSStateMode{}
	}
	enabled, generation, err := s.codexTurnStateService.Enabled(ctx, account)
	if err != nil {
		return openAICodexWSStateMode{}
	}
	return openAICodexWSStateMode{Enabled: enabled, Generation: generation}
}

func (s *OpenAIGatewayService) openAICodexWSStateModeChanged(ctx context.Context, account *Account, initial openAICodexWSStateMode) bool {
	if s == nil || s.codexTurnStateService == nil {
		return false
	}
	enabled, generation, err := s.codexTurnStateService.Enabled(ctx, account)
	if err != nil {
		// A storage outage prevents injection but is not evidence of a settings
		// change. Keep forwarding the ordinary client frames on this socket.
		return false
	}
	return (openAICodexWSStateMode{Enabled: enabled, Generation: generation}).poolKey() != initial.poolKey()
}

// prepareOpenAICodexWSStateFrame runs only after the ordinary client provenance
// guard. The attempt's private server snapshot is the sole source of a cache
// override, and never changes the provenance associated with a client token.
func (s *OpenAIGatewayService) prepareOpenAICodexWSStateFrame(ctx context.Context, c *gin.Context, account *Account, payload []byte, firstGuardedHeaderToken string, credentialHeaders http.Header) ([]byte, *CodexTurnStateAttempt, error) {
	noteOpenAICodexStatePatch(c, nil, nil, nil)
	if s == nil || s.codexTurnStateService == nil ||
		strings.TrimSpace(gjson.GetBytes(payload, "type").String()) != "response.create" ||
		gjson.GetBytes(payload, "generate").Type == gjson.False {
		return payload, nil, nil
	}
	model := strings.TrimSpace(gjson.GetBytes(payload, "model").String())
	attempt, err := s.codexTurnStateService.Prepare(ctx, account, model)
	if err != nil || attempt == nil {
		// The handshake may already have been suppressed. Preserve its guarded
		// first-frame value even when no classification/cache can be loaded.
		final, patchErr := applyOpenAICodexWSStateSnapshot(payload, "", firstGuardedHeaderToken)
		noteOpenAICodexStatePatch(c, nil, payload, final)
		return final, nil, patchErr
	}
	if attempt.Enabled && !s.codexTurnStateService.ValidateCredentialHeaders(ctx, attempt, credentialHeaders) {
		s.finishOpenAICodexWSState(ctx, attempt, false)
		attempt = passiveCodexStateAfterValidationFailure(attempt)
		if attempt == nil {
			final, patchErr := applyOpenAICodexWSStateSnapshot(payload, "", firstGuardedHeaderToken)
			noteOpenAICodexStatePatch(c, nil, payload, final)
			return final, nil, patchErr
		}
	}
	s.codexTurnStateService.bindHistoryCredentials(ctx, attempt, credentialHeaders)
	final, err := applyOpenAICodexWSStateSnapshot(payload, attempt.Snapshot.Token, firstGuardedHeaderToken)
	if err != nil {
		s.finishOpenAICodexWSState(ctx, attempt, false)
		return payload, nil, err
	}
	noteOpenAICodexStatePatch(c, attempt, payload, final)
	return final, attempt, nil
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

func applyOpenAICodexWSStateSnapshot(payload []byte, cachedToken, firstGuardedHeaderToken string) ([]byte, error) {
	token := strings.TrimSpace(cachedToken)
	if token == "" {
		// Existing per-frame input takes precedence over a first-frame migration.
		if gjson.GetBytes(payload, "client_metadata.x-codex-turn-state").Exists() {
			return payload, nil
		}
		token = strings.TrimSpace(firstGuardedHeaderToken)
	}
	if token == "" {
		return payload, nil
	}
	return sjson.SetBytes(payload, "client_metadata.x-codex-turn-state", token)
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
