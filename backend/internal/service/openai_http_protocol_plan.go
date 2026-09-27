package service

import (
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const openAIHTTPProtocolPlanKey = "openai_http_protocol_plan"

// A physical HTTP attempt uses one final-model capability decision throughout
// identity projection, wire preparation and transmission. No cached state may
// select or change its account's configured egress.
type openAIHTTPProtocolPlan struct {
	accountID    int64
	model        string
	capabilities CodexModelCapabilities
}

func (s *OpenAIGatewayService) freezeOpenAIHTTPProtocol(c *gin.Context, account *Account, model string, body []byte) {
	if s == nil || c == nil || account == nil || !account.IsOpenAIOAuth() || account.IsOpenAIPersonalAccessToken() || account.IsOpenAIAgentIdentity() {
		return
	}
	if isOpenAIResponsesCompactPath(c) || gjson.GetBytes(body, "generate").Type == gjson.False {
		return
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return
	}
	if _, ok := frozenOpenAIHTTPModelCapabilities(c, account, model); ok {
		return
	}
	capabilities := effectiveCodexHTTPModelCapabilities(account, model, s.openAICodexModelCapabilities(openAICodexModelCapabilitiesNamespace(account), model), explicitOpenAIResponsesLiteHTTP(c, nil) || isOpenAIResponsesLiteWebSocketPayload(body))
	c.Set(openAIHTTPProtocolPlanKey, openAIHTTPProtocolPlan{account.ID, model, capabilities})
}

func (s *OpenAIGatewayService) prepareOpenAIHTTPProtocol(c *gin.Context, account *Account, body []byte, protocol, defaultModel string) {
	model := strings.TrimSpace(gjson.GetBytes(body, "model").String())
	switch protocol {
	case "messages":
		model = normalizeOpenAIModelForUpstream(account, resolveOpenAIForwardModel(account, NormalizeOpenAICompatRequestedModel(model), defaultModel))
	case "chat":
		model = normalizeOpenAIModelForUpstream(account, resolveOpenAIForwardModel(account, model, defaultModel))
	default:
		model = s.resolveOpenAIResponsesHTTPFinalModel(c, account, model)
	}
	s.freezeOpenAIHTTPProtocol(c, account, model, body)
}

// Match forwarding before model-sensitive normalization. Passthrough preserves
// the wire model except for compact fallback; normal Responses applies mapping.
func (s *OpenAIGatewayService) resolveOpenAIResponsesHTTPFinalModel(c *gin.Context, account *Account, model string) string {
	if isOpenAIResponsesCompactPath(c) {
		if compactModel := s.resolveOpenAICompactFallbackModel(account, model); compactModel != "" {
			return compactModel
		}
	}
	if account != nil && account.IsOpenAIPassthroughEnabled() {
		return model
	}
	_, model = resolveOpenAIForwardMappedModels(account, model, isOpenAIResponsesCompactPath(c))
	return model
}

func frozenOpenAIHTTPModelCapabilities(c *gin.Context, account *Account, model string) (CodexModelCapabilities, bool) {
	if c == nil || account == nil {
		return CodexModelCapabilities{}, false
	}
	raw, _ := c.Get(openAIHTTPProtocolPlanKey)
	plan, ok := raw.(openAIHTTPProtocolPlan)
	if !ok || plan.accountID != account.ID || plan.model != strings.TrimSpace(model) {
		return CodexModelCapabilities{}, false
	}
	return plan.capabilities, true
}

func (s *OpenAIGatewayService) prepareOpenAIHTTPBridgeProtocol(c *gin.Context, account *Account, body []byte, newFrame bool) ([]byte, *RequestTimezoneState) {
	if c == nil {
		return body, nil
	}
	if newFrame {
		c.Set(openAIHTTPProtocolPlanKey, nil)
	}
	s.freezeOpenAIHTTPProtocol(c, account, gjson.GetBytes(body, "model").String(), body)
	// Bridge turns retain the physical account route. A new connection/account
	// clears that route through the ordinary ingress path.
	FreezeOpenAIOutboundRoute(c, account)
	state, _ := RequestTimezoneStateFromContext(c)
	return body, state
}
