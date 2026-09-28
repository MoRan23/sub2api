package service

import (
	"context"
	"errors"
	"net/http"

	coderws "github.com/coder/websocket"
)

// A physical websocket cannot switch upstream protocol or credential-route
// generation. Account administration takes effect at the next turn boundary.
func (s *OpenAIGatewayService) validateOpenAIBackendWSRequest(ctx context.Context, account *Account, scope string, payload []byte, headers http.Header) error {
	if account == nil {
		return nil
	}
	if err := rejectOpenAIExcelContinuation(nil, account, payload); err != nil {
		return NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, "Excel upstream requires full history instead of previous_response_id", err)
	}
	if SupportsOpenAIExcelUpstream(account) && s.accountRepo != nil {
		current, err := s.accountRepo.GetByID(ctx, account.ID)
		if err != nil || current == nil {
			return NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, "unable to verify account upstream; reconnect", errors.New("account upstream unavailable"))
		}
		if current.OpenAIUpstreamKind() != account.OpenAIUpstreamKind() || current.OpenAIUpstreamRouteGeneration() != account.OpenAIUpstreamRouteGeneration() {
			return NewOpenAIWSClientCloseError(coderws.StatusNormalClosure, "account upstream changed; reconnect", nil)
		}
	}
	if err := s.validateOpenAIBackendPayload(ctx, account, scope, payload, headers); err != nil {
		return NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, "upstream continuation is unavailable; start a new conversation", err)
	}
	if err := s.validateOpenAIBackendIngressSource(ctx, account, scope); err != nil {
		return NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, "upstream continuation is unavailable; start a new conversation", err)
	}
	return nil
}

func (s *OpenAIGatewayService) rememberOpenAIBackendWSResponse(ctx context.Context, account *Account, scope string, payload []byte, headers http.Header) error {
	if err := s.rememberOpenAIBackendPayload(ctx, account, scope, payload, headers); err != nil {
		return NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, "unable to preserve upstream continuation", err)
	}
	return nil
}
