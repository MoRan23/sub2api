package handler

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"time"
)

// Keep the handler's already-resolved authorization and channel policy for the
// selected physical attempt. The service freezes only an explicit safe subset
// immediately before dispatch; this in-memory value is never persisted itself.
func (h *OpenAIGatewayHandler) setCodexEngineBillingInput(c *gin.Context, account *service.Account, key *service.APIKey, subscription *service.UserSubscription, mapping service.ChannelMappingResult, model string, body []byte) {
	if c == nil || c.Request == nil {
		return
	}
	h.setCodexEngineBillingInputAt(c, account, key, subscription, mapping, model, body, service.OpenAIPricingAtFromContext(c.Request.Context()))
}

func (h *OpenAIGatewayHandler) setCodexEngineBillingInputAt(c *gin.Context, account *service.Account, key *service.APIKey, subscription *service.UserSubscription, mapping service.ChannelMappingResult, model string, body []byte, pricingAt time.Time) {
	if account == nil || !account.IsCodexEngine() || key == nil || c == nil || c.Request == nil {
		return
	}
	service.SetCodexEngineBillingInput(c, &service.OpenAIRecordUsageInput{
		APIKey: key, User: key.User, Account: account, Subscription: subscription,
		InboundEndpoint: GetInboundEndpoint(c), UserAgent: c.GetHeader("User-Agent"),
		IPAddress: ip.GetClientIP(c), SessionID: service.ExtractClientSessionID(c),
		RequestPayloadHash: service.HashUsageRequestPayload(body), APIKeyService: h.apiKeyService,
		QuotaPlatform:      service.QuotaPlatform(c.Request.Context(), key),
		ChannelUsageFields: clientRequestedUsageFields(c, mapping, model, ""),
		PricingAt:          pricingAt,
		NativeCompactionV2: service.IsOpenAINativeCompactionV2(c),
	})
}
