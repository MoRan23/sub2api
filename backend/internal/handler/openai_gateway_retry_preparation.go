package handler

import (
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// openAIResponsesRetryPreparation belongs to one serial HTTP forwarding loop.
// Both bodies are immutable: Engine always receives the original channel-mapped
// bytes, while other accounts share the account-independent normalization. The
// account-specific identity, model, timezone and failover projections still run
// separately on every attempt.
type openAIResponsesRetryPreparation struct {
	body           []byte
	path           string
	prepared       bool
	valid          bool
	normalizedBody []byte
	normalizedPath string
	route          openAICompactionRoute
}

func (p *openAIResponsesRetryPreparation) forAccount(h *OpenAIGatewayHandler, c *gin.Context, reqLog *zap.Logger, account *service.Account) ([]byte, bool) {
	c.Request.URL.Path = p.path
	if account.IsCodexEngine() {
		return p.body, true
	}
	if !p.prepared {
		p.prepared = true
		body, valid := h.normalizeOpenAIResponsesCompactRequest(c, reqLog, p.body)
		if !valid {
			return nil, false
		}
		body, _ = normalizeCodexPageContext(body)
		body, _ = normalizeCodexAutomationBootstrap(body)
		body, _ = normalizeCodexDelegationBootstrap(body)
		if !h.validateFunctionCallOutputRequest(c, body, reqLog) {
			return nil, false
		}
		p.normalizedBody, p.normalizedPath = body, c.Request.URL.Path
		p.route = openAICompactionRouteFromContext(c)
		p.valid = true
	}
	if !p.valid {
		return nil, false
	}
	// Forward may change the URL path for its chosen upstream. Restore the
	// canonical compact path for the next attempt, without parsing the body again.
	c.Request.URL.Path = p.normalizedPath
	setOpenAICompactionRoute(c, p.route)
	return p.normalizedBody, true
}

// openAIUsagePayloadHash hashes only the original ingress payload, on demand.
// It is local to the serial request loop; async billing receives only its string.
type openAIUsagePayloadHash struct {
	body       []byte
	calculated bool
	hash       string
}

func (h *openAIUsagePayloadHash) value() string {
	if !h.calculated {
		h.hash = service.HashUsageRequestPayload(h.body)
		h.calculated = true
	}
	return h.hash
}
