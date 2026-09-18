package service

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/gin-gonic/gin"
)

func (s *OpenAIGatewayService) applyOpenAIWSResidencyHeaders(ctx context.Context, account *Account, headers http.Header) {
	if account == nil || (account.Platform != "" && account.Platform != PlatformOpenAI) {
		return
	}
	policy, _ := openai.RequestPolicyFromContext(ctx)
	openai.ApplyCodexResidencyHeader(headers, policy.CodexResidencyUS)
}

func (s *OpenAIGatewayService) refreshOpenAIWSHeadersForDial(frozenCtx, dialCtx context.Context, account *Account, headers http.Header) (http.Header, error) {
	policy, _ := openai.RequestPolicyFromContext(frozenCtx)
	dialCtx = openai.WithRequestPolicy(dialCtx, policy)
	refreshed, err := s.refreshOpenAIAgentIdentityHeaders(dialCtx, account, headers)
	if err != nil {
		return nil, err
	}
	s.applyOpenAIWSResidencyHeaders(dialCtx, account, refreshed)
	return refreshed, nil
}

// Each factory belongs to one accepted frame, including any background prewarm
// spawned from it. It must not close over a session variable advanced by later
// frames while a previous dial or account retry is still running.
func (s *OpenAIGatewayService) openAIWSHeadersFactory(ctx context.Context, account *Account) func(context.Context, http.Header) (http.Header, error) {
	return func(dialCtx context.Context, headers http.Header) (http.Header, error) {
		return s.refreshOpenAIWSHeadersForDial(ctx, dialCtx, account, headers)
	}
}

func openAIWSContextForTimezoneState(ctx context.Context, c *gin.Context, state *RequestTimezoneState) context.Context {
	if state == nil {
		return ctx
	}
	ctx = openai.WithRequestPolicy(ctx, state.Policy)
	if c != nil && c.Request != nil {
		c.Request = c.Request.WithContext(openai.WithRequestPolicy(c.Request.Context(), state.Policy))
	}
	return ctx
}

func cloneOpenAIWSResidencyHeaders(headers http.Header) http.Header {
	result := make(http.Header)
	for name, values := range headers {
		if strings.EqualFold(name, openai.CodexResidencyHeaderName) {
			result[name] = append([]string(nil), values...)
		}
	}
	return result
}

// Restore only the residency carrier from its pre-policy view before applying
// a later frame's policy. Turning forcing off restores existing account values;
// it cannot leave the old forced value behind or delete an original value.
func (s *OpenAIGatewayService) resetOpenAIWSResidencyHeaders(ctx context.Context, account *Account, headers, original http.Header) {
	for name := range headers {
		if strings.EqualFold(name, openai.CodexResidencyHeaderName) {
			delete(headers, name)
		}
	}
	for name, values := range original {
		headers[name] = append([]string(nil), values...)
	}
	s.applyOpenAIWSResidencyHeaders(ctx, account, headers)
}

func openAIWSPhysicalObservationHeaders(conn openAIWSClientConn, fallback http.Header) http.Header {
	if source, ok := conn.(interface{ FingerprintObservationHeaders() http.Header }); ok {
		return source.FingerprintObservationHeaders()
	}
	return cloneFingerprintObservationHeaders(fallback)
}

// prepareOpenAIWSFrameTimezone is called before account/protocol adaptation.
// A new accepted response.create always owns a new clock snapshot, including
// tool continuations. Only an entry frame retried by the handler can reuse one.
func (s *OpenAIGatewayService) prepareOpenAIWSFrameTimezone(ctx context.Context, c *gin.Context, account *Account, body []byte, passthrough, reuse bool, acceptedAt time.Time) ([]byte, *RequestTimezoneState) {
	if account == nil || (account.Platform != "" && account.Platform != PlatformOpenAI) {
		return body, nil
	}
	preparationBody := body
	if reuse {
		if state, ok := RequestTimezoneStateFromContext(c); ok && state != nil {
			// A failover payload can already contain converted replay history and
			// relocated current input. Never replace it with the original body or
			// rescan it as a newly accepted turn.
			converted, _ := state.ApplyToBody(body)
			return converted, state
		}
		if c != nil {
			if value, ok := c.Get(openAIRequestTimezoneCaptureKey); ok {
				if capture, valid := value.(*openAIRequestTimezoneCapture); valid && capture != nil {
					acceptedAt = capture.acceptedAt
					if len(capture.body) > 0 {
						preparationBody = capture.body
					}
				}
			}
		}
	}
	policy, _ := openai.RequestPolicyFromContext(ctx)
	if !reuse && s != nil && s.settingService != nil {
		// A connection is long lived, but each accepted frame is a new logical
		// request. Only a retry reuses the earlier immutable policy snapshot.
		policy = s.settingService.GetOpenAIRequestPolicy(ctx)
	}
	_, state := PrepareOpenAIRequestTimezone(preparationBody, policy, acceptedAt, passthrough, IsFingerprintObservationEnabled())
	converted, _ := state.ApplyToBody(body)
	SetRequestTimezoneState(c, state)
	return converted, state
}

func openAIWSObservationFramePlan(account *Account, plan *OpenAIOAuthIdentityPlan) *OpenAIOAuthIdentityPlan {
	if account == nil || !account.UsesOpenAICodexProtocol() {
		return nil
	}
	return plan
}
