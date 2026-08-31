package service

import (
	"log/slog"
	"math"
	"strings"
)

// ServiceTierBillingResolution describes how the billable service tier of one
// request was settled between the tier carried by the final outbound request
// and the tier the upstream reports having used.
type ServiceTierBillingResolution struct {
	Requested  string // tier carried by the request sent upstream ("" when none)
	Observed   string // tier declared by the upstream response ("" when none)
	Billing    string // tier used for billing and the usage log
	Downgraded bool   // Billing is cheaper than Requested
}

// ResolveBillingServiceTier picks the tier to bill. Different tier names cannot
// be ordered without the request's resolved pricing: channel FastMultiplier and
// FlexMultiplier values may reverse the usual priority/default/flex price order.
// Callers at the billing boundary therefore pass the two costs they calculated
// from the same model, token mix, channel pricing and rate multiplier. Without
// both costs, a different or unknown response tier is ignored conservatively.
func ResolveBillingServiceTier(requested, observed string) ServiceTierBillingResolution {
	return ResolveBillingServiceTierWithCosts(requested, observed, nil, nil)
}

// ResolveBillingServiceTierWithCosts is the pricing-aware resolver used by the
// usage paths. It adopts an upstream tier only when its actual resolved cost is
// no greater than the final outbound request tier's cost.
func ResolveBillingServiceTierWithCosts(requested, observed string, requestedCost, observedCost *CostBreakdown) ServiceTierBillingResolution {
	requested = normalizeBillingServiceTier(requested)
	observed = normalizeBillingServiceTier(observed)
	resolution := ServiceTierBillingResolution{Requested: requested, Observed: observed, Billing: requested}
	if observed == "" || observed == requested {
		return resolution
	}
	if requested == "" || !isKnownBillingServiceTier(requested) || !isKnownBillingServiceTier(observed) {
		return resolution
	}
	if !isValidServiceTierComparisonCost(requestedCost) || !isValidServiceTierComparisonCost(observedCost) {
		return resolution
	}
	if observedCost.TotalCost > requestedCost.TotalCost {
		return resolution
	}
	resolution.Billing = observed
	resolution.Downgraded = observedCost.TotalCost < requestedCost.TotalCost
	return resolution
}

func isKnownBillingServiceTier(tier string) bool {
	switch normalizeBillingServiceTier(tier) {
	case "default", "standard", "auto", "scale", "priority", "fast", "flex":
		return true
	default:
		return false
	}
}

func isValidServiceTierComparisonCost(cost *CostBreakdown) bool {
	return cost != nil && cost.TotalCost >= 0 && !math.IsInf(cost.TotalCost, 0)
}

// ResolveOpenAIServiceTierBilling applies the response-tier contract for the
// selected credential. Public OpenAI API responses declare the actual tier and
// may lower billing when resolved pricing proves they are no more expensive.
// The private ChatGPT Codex endpoint commonly reports default for effective Fast
// turns, so OAuth-like credentials retain the final outbound tier while still
// exposing the observed value.
func ResolveOpenAIServiceTierBilling(account *Account, requested, observed string) ServiceTierBillingResolution {
	return ResolveOpenAIServiceTierBillingWithCosts(account, requested, observed, nil, nil)
}

// ResolveOpenAIServiceTierBillingWithCosts combines the selected credential's
// response contract with the exact channel pricing used for this request.
func ResolveOpenAIServiceTierBillingWithCosts(account *Account, requested, observed string, requestedCost, observedCost *CostBreakdown) ServiceTierBillingResolution {
	if account != nil && account.IsOpenAIOAuthLike() && codexOAuthResponseTierIsNonAuthoritative(observed) {
		return ServiceTierBillingResolution{
			Requested: normalizeBillingServiceTier(requested),
			Observed:  normalizeBillingServiceTier(observed),
			Billing:   normalizeBillingServiceTier(requested),
		}
	}
	return ResolveBillingServiceTierWithCosts(requested, observed, requestedCost, observedCost)
}

func codexOAuthResponseTierIsNonAuthoritative(observed string) bool {
	switch normalizeBillingServiceTier(observed) {
	case "default":
		return true
	default:
		return false
	}
}

// ApplyOpenAIServiceTierBillingResolution adopts an authoritative observed tier
// only when the supplied costs prove it is no more expensive than the outbound
// request tier. The returned resolution is suitable for the audit log.
func ApplyOpenAIServiceTierBillingResolution(account *Account, result *OpenAIForwardResult, costs ...*CostBreakdown) ServiceTierBillingResolution {
	if result == nil {
		return ServiceTierBillingResolution{}
	}
	var requestedCost, observedCost *CostBreakdown
	if len(costs) == 2 {
		requestedCost, observedCost = costs[0], costs[1]
	}
	resolution := ResolveOpenAIServiceTierBillingWithCosts(account, optionalStringValue(result.ServiceTier), result.UpstreamResponseServiceTier, requestedCost, observedCost)
	if resolution.Billing != resolution.Requested {
		billing := resolution.Billing
		result.ServiceTier = &billing
	}
	return resolution
}

// ApplyForwardServiceTierBillingResolution is the ForwardResult counterpart of
// ApplyOpenAIServiceTierBillingResolution.
func ApplyForwardServiceTierBillingResolution(result *ForwardResult, costs ...*CostBreakdown) ServiceTierBillingResolution {
	if result == nil {
		return ServiceTierBillingResolution{}
	}
	var requestedCost, observedCost *CostBreakdown
	if len(costs) == 2 {
		requestedCost, observedCost = costs[0], costs[1]
	}
	resolution := ResolveBillingServiceTierWithCosts(optionalStringValue(result.ServiceTier), result.UpstreamResponseServiceTier, requestedCost, observedCost)
	if resolution.Billing != resolution.Requested {
		billing := resolution.Billing
		result.ServiceTier = &billing
	}
	return resolution
}

// logServiceTierBillingDowngrade leaves an audit trail for every request billed
// below the tier it asked for; unchanged tiers are not logged.
func logServiceTierBillingDowngrade(component string, account *Account, requestID string, resolution ServiceTierBillingResolution) {
	if !resolution.Downgraded {
		return
	}
	attrs := []any{
		"component", component,
		"request_id", strings.TrimSpace(requestID),
		"requested_tier", resolution.Requested,
		"response_tier", resolution.Observed,
		"billed_tier", resolution.Billing,
	}
	if account != nil {
		attrs = append(attrs, "platform", account.Platform, "account_id", account.ID)
	}
	slog.Info("billing.service_tier_downgraded", attrs...)
}
