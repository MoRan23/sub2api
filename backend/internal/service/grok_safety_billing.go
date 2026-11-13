package service

import (
	"context"
	"errors"
	"math"
	"strings"
	"time"
)

func validateGrokSafetyBilling(account *Account, rejection *GrokSafetyRejection) error {
	if account == nil || account.Platform != PlatformGrok || rejection.Code != GrokSafetyRejectionCode ||
		strings.TrimSpace(rejection.CallID) == "" || len(rejection.CallID) > 128 ||
		math.IsNaN(rejection.Fee) || math.IsInf(rejection.Fee, 0) || rejection.Fee < 0 {
		return errors.New("invalid Grok safety billing evidence")
	}
	return nil
}

func clearGrokSafetySuccessUnits(result *OpenAIForwardResult) {
	result.ImageCount, result.VideoCount, result.SearchCount, result.WebSearchCalls = 0, 0, 0, 0
	result.ImageEndpoint = false
	result.ImageSize, result.ImageInputSize, result.ImageOutputSize, result.ImageSizeSource = "", "", "", ""
	result.ImageOutputSizes, result.ImageSizeBreakdown = nil, nil
	result.VideoResolution, result.VideoDurationSeconds = "", 0
	result.AudioUsage = nil
}

// Exact refusals use trusted final tokens only. A per-image/request/video price
// cannot charge a completed unit which the refusal did not deliver.
func (s *OpenAIGatewayService) calculateGrokSafetyTokenCost(ctx context.Context, result *OpenAIForwardResult,
	apiKey *APIKey, models []string, multiplier float64, tokens UsageTokens, tier string,
	longContext *bool, modelExempt bool, at time.Time,
) (*CostBreakdown, error) {
	if tokens == (UsageTokens{}) {
		return &CostBreakdown{BillingMode: string(BillingModeToken)}, nil
	}
	var lastErr error = ErrModelPricingUnavailable
	for _, model := range models {
		if strings.TrimSpace(model) == "" {
			continue
		}
		resolver := s.resolver
		if resolver == nil {
			resolver = NewModelPricingResolver(nil, s.billingService)
		}
		pricingInput := PricingInput{Model: model}
		if apiKey != nil && apiKey.Group != nil {
			pricingInput.Group, pricingInput.GroupID = apiKey.Group, &apiKey.Group.ID
		}
		resolved := resolver.Resolve(ctx, pricingInput)
		if resolved.Mode != "" && resolved.Mode != BillingModeToken {
			resolver = NewModelPricingResolver(nil, s.billingService)
			resolved = resolver.Resolve(ctx, PricingInput{Model: model})
			if apiKey != nil && apiKey.Group != nil {
				resolved.longContextPricingEnabled = apiKey.Group.LongContextPricingEnabled
			}
		}
		cost, err := s.billingService.CalculateCostUnified(CostInput{
			Ctx: ctx, Model: model, Tokens: tokens, RateMultiplier: multiplier, PricingAt: at,
			ServiceTier: tier, ReasoningEffort: optionalStringValue(result.ReasoningEffort),
			Resolver: resolver, Resolved: resolved, LongContextBillingEnabled: longContext,
			DisableModelLongContextPricing: modelExempt,
		})
		if err == nil {
			return cost, nil
		}
		lastErr = err
	}
	return nil, lastErr
}
