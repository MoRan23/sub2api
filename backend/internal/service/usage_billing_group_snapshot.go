package service

import "strings"

// usageBillingGroupSnapshot owns only deferred-billing configuration. It never
// retains the authentication Group, its maps, or its pointer-valued prices.
// A new authentication-cache publication creates a new immutable projection;
// invalidating that entry does not alter projections held by queued work.
type usageBillingGroupSnapshot struct {
	group Group
}

// frozenUsageBillingGroup is only consumed by the billing snapshot builder.
// Ordinary repository/request Groups retain its deep-copy fallback. Authenticated
// Groups are read-only; routing and media hydration replace the Group itself.
func frozenUsageBillingGroup(group *Group) *Group {
	if group == nil || group.billingSnapshot == nil {
		return nil
	}
	return &group.billingSnapshot.group
}

// prepareAPIKeyAuthBillingSnapshot must run before the authentication snapshot
// is published to L1 or returned to singleflight waiters, never lazily by readers.
func prepareAPIKeyAuthBillingSnapshot(snapshot *APIKeyAuthSnapshot) {
	if snapshot == nil || snapshot.Version != apiKeyAuthSnapshotVersion || snapshot.Group == nil || snapshot.Group.billingSnapshot != nil {
		return
	}
	g := snapshot.Group
	frozen := &usageBillingGroupSnapshot{group: Group{
		ID: g.ID, Platform: strings.Clone(g.Platform), SubscriptionType: strings.Clone(g.SubscriptionType), Hydrated: true,
		RateMultiplier: g.RateMultiplier, PeakRateEnabled: g.PeakRateEnabled, PeakStart: strings.Clone(g.PeakStart), PeakEnd: strings.Clone(g.PeakEnd), PeakRateMultiplier: g.PeakRateMultiplier,
		ImageCountBilling: g.ImageCountBilling, ImageRateIndependent: g.ImageRateIndependent, ImageRateMultiplier: g.ImageRateMultiplier,
		ImagePrice1K: cloneUsageBillingScalar(g.ImagePrice1K), ImagePrice2K: cloneUsageBillingScalar(g.ImagePrice2K), ImagePrice4K: cloneUsageBillingScalar(g.ImagePrice4K),
		VideoRateIndependent: g.VideoRateIndependent, VideoRateMultiplier: g.VideoRateMultiplier,
		VideoPrice480P: cloneUsageBillingScalar(g.VideoPrice480P), VideoPrice720P: cloneUsageBillingScalar(g.VideoPrice720P), VideoPrice1080P: cloneUsageBillingScalar(g.VideoPrice1080P),
		WebSearchPricePerCall: cloneUsageBillingScalar(g.WebSearchPricePerCall), SearchPricePer1k: cloneUsageBillingScalar(g.SearchPricePer1k),
		GrokSafetyFeeEnabled: g.GrokSafetyFeeEnabled, GrokSafetyFee: cloneUsageBillingScalar(g.GrokSafetyFee),
		AudioRealtimePricePerMin: cloneUsageBillingScalar(g.AudioRealtimePricePerMin), AudioTTSPricePerMillionChars: cloneUsageBillingScalar(g.AudioTTSPricePerMillionChars), AudioSTTPricePerHour: cloneUsageBillingScalar(g.AudioSTTPricePerHour),
		LongContextPricingEnabled: g.LongContextPricingEnabled, FreeOpenAIFast: g.FreeOpenAIFast, FreeOpenAIUltrafast: g.FreeOpenAIUltrafast,
	}}
	// L2 data goes through the same normalization as the public request Group.
	// Clone its strings too, so the frozen projection owns its entire price graph.
	if videoPrices := NormalizeVideoModelPrices(g.VideoModelPrices); videoPrices != nil {
		frozen.group.VideoModelPrices = make(map[string]map[string]float64, len(videoPrices))
		for model, prices := range videoPrices {
			frozen.group.VideoModelPrices[strings.Clone(model)] = cloneUsageBillingFloatMap(prices)
		}
	}
	if g.ModelPricing != nil {
		frozen.group.ModelPricing = make([]ChannelModelPricing, len(g.ModelPricing))
		for i, price := range g.ModelPricing {
			frozen.group.ModelPricing[i] = cloneUsageBillingModelPricing(price)
		}
	}
	g.billingSnapshot = frozen
}

func cloneUsageBillingScalar[T any](value *T) *T {
	if value == nil {
		return nil
	}
	owned := *value
	return &owned
}

func cloneUsageBillingFloatMap(values map[string]float64) map[string]float64 {
	if values == nil {
		return nil
	}
	owned := make(map[string]float64, len(values))
	for key, value := range values {
		owned[strings.Clone(key)] = value
	}
	return owned
}

func cloneUsageBillingModelPricing(price ChannelModelPricing) ChannelModelPricing {
	price.Platform = strings.Clone(price.Platform)
	price.BillingMode = BillingMode(strings.Clone(string(price.BillingMode)))
	if price.Models != nil {
		models := make([]string, len(price.Models))
		for i, model := range price.Models {
			models[i] = strings.Clone(model)
		}
		price.Models = models
	}
	price.InputPrice, price.OutputPrice = cloneUsageBillingScalar(price.InputPrice), cloneUsageBillingScalar(price.OutputPrice)
	price.CacheWritePrice, price.CacheWrite1hPrice, price.CacheReadPrice = cloneUsageBillingScalar(price.CacheWritePrice), cloneUsageBillingScalar(price.CacheWrite1hPrice), cloneUsageBillingScalar(price.CacheReadPrice)
	price.CacheWrite12hPrice = cloneUsageBillingScalar(price.CacheWrite12hPrice)
	price.FastMultiplier, price.FlexMultiplier = cloneUsageBillingScalar(price.FastMultiplier), cloneUsageBillingScalar(price.FlexMultiplier)
	price.ImageInputPrice, price.ImageOutputPrice, price.PerRequestPrice = cloneUsageBillingScalar(price.ImageInputPrice), cloneUsageBillingScalar(price.ImageOutputPrice), cloneUsageBillingScalar(price.PerRequestPrice)
	price.ReasoningEffortMultipliers = cloneUsageBillingFloatMap(price.ReasoningEffortMultipliers)
	if price.Intervals != nil {
		intervals := make([]PricingInterval, len(price.Intervals))
		for i, interval := range price.Intervals {
			interval.MaxTokens, interval.TierLabel = cloneUsageBillingScalar(interval.MaxTokens), strings.Clone(interval.TierLabel)
			interval.InputPrice, interval.OutputPrice = cloneUsageBillingScalar(interval.InputPrice), cloneUsageBillingScalar(interval.OutputPrice)
			interval.CacheWritePrice, interval.CacheWrite1hPrice, interval.CacheReadPrice = cloneUsageBillingScalar(interval.CacheWritePrice), cloneUsageBillingScalar(interval.CacheWrite1hPrice), cloneUsageBillingScalar(interval.CacheReadPrice)
			interval.CacheWrite12hPrice = cloneUsageBillingScalar(interval.CacheWrite12hPrice)
			interval.InputMultiplier, interval.OutputMultiplier = cloneUsageBillingScalar(interval.InputMultiplier), cloneUsageBillingScalar(interval.OutputMultiplier)
			interval.CacheWriteMultiplier, interval.CacheReadMultiplier, interval.PerRequestPrice = cloneUsageBillingScalar(interval.CacheWriteMultiplier), cloneUsageBillingScalar(interval.CacheReadMultiplier), cloneUsageBillingScalar(interval.PerRequestPrice)
			intervals[i] = interval
		}
		price.Intervals = intervals
	}
	if price.TimePricing != nil {
		timing := *price.TimePricing
		timing.Timezone = strings.Clone(timing.Timezone)
		if timing.Periods != nil {
			timing.Periods = make([]ChannelTimePricingPeriod, len(price.TimePricing.Periods))
			for i, period := range price.TimePricing.Periods {
				period.StartTime, period.EndTime = strings.Clone(period.StartTime), strings.Clone(period.EndTime)
				timing.Periods[i] = period
			}
		}
		price.TimePricing = &timing
	}
	return price
}
