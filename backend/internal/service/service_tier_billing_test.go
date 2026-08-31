package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolveBillingServiceTier(t *testing.T) {
	tests := []struct {
		name          string
		requested     string
		observed      string
		requestedCost *CostBreakdown
		observedCost  *CostBreakdown
		billing       string
		downgraded    bool
	}{
		{name: "openai priority served as cheaper default", requested: "priority", observed: "default", requestedCost: serviceTierTestCost(2), observedCost: serviceTierTestCost(1), billing: "default", downgraded: true},
		{name: "anthropic fast served as cheaper standard", requested: "fast", observed: "standard", requestedCost: serviceTierTestCost(2), observedCost: serviceTierTestCost(1), billing: "standard", downgraded: true},
		{name: "priority honoured", requested: "priority", observed: "priority", billing: "priority"},
		{name: "no declaration keeps request", requested: "priority", observed: "", billing: "priority"},
		{name: "no request no declaration", requested: "", observed: "", billing: ""},
		{name: "priority response never raises untiered request", requested: "", observed: "priority", billing: ""},
		{name: "higher-cost priority response never raises default request", requested: "default", observed: "priority", requestedCost: serviceTierTestCost(1), observedCost: serviceTierTestCost(2), billing: "default"},
		{name: "higher-cost priority response never raises flex request", requested: "flex", observed: "priority", requestedCost: serviceTierTestCost(0.5), observedCost: serviceTierTestCost(2), billing: "flex"},
		{name: "higher-cost default never raises flex", requested: "flex", observed: "default", requestedCost: serviceTierTestCost(0.5), observedCost: serviceTierTestCost(1), billing: "flex"},
		{name: "equal-cost observed alias is safe to adopt", requested: "default", observed: "standard", requestedCost: serviceTierTestCost(1), observedCost: serviceTierTestCost(1), billing: "standard"},
		{name: "default echoed for untiered request", requested: "", observed: "default", billing: ""},
		{name: "unknown response tier ignored", requested: "priority", observed: "turbo", requestedCost: serviceTierTestCost(2), observedCost: serviceTierTestCost(1), billing: "priority"},
		{name: "missing pricing evidence keeps request", requested: "priority", observed: "default", billing: "priority"},
		{name: "case and whitespace normalised", requested: " Priority ", observed: "DEFAULT", requestedCost: serviceTierTestCost(2), observedCost: serviceTierTestCost(1), billing: "default", downgraded: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveBillingServiceTierWithCosts(tt.requested, tt.observed, tt.requestedCost, tt.observedCost)
			require.Equal(t, tt.billing, got.Billing)
			require.Equal(t, tt.downgraded, got.Downgraded)
		})
	}
}

func serviceTierTestCost(total float64) *CostBreakdown {
	return &CostBreakdown{TotalCost: total, ActualCost: total}
}

func TestResolveBillingServiceTierUsesResolvedCustomMultipliers(t *testing.T) {
	tokens := UsageTokens{InputTokens: 100, OutputTokens: 50, CacheReadTokens: 25}
	fastMultiplier := 0.4
	flexMultiplier := 3.0
	pricing := &ModelPricing{
		InputPricePerToken:     1,
		OutputPricePerToken:    2,
		CacheReadPricePerToken: 0.5,
		FastMultiplier:         &fastMultiplier,
		FlexMultiplier:         &flexMultiplier,
	}
	billing := &BillingService{}
	priorityCost := billing.computeTokenBreakdown(pricing, tokens, 1, "priority", false)
	flexCost := billing.computeTokenBreakdown(pricing, tokens, 1, "flex", false)

	t.Run("observed priority is adopted when custom fast is cheaper than requested flex", func(t *testing.T) {
		got := ResolveBillingServiceTierWithCosts("flex", "priority", flexCost, priorityCost)
		require.Equal(t, "priority", got.Billing)
		require.True(t, got.Downgraded)
	})

	t.Run("observed flex is rejected when custom flex is more expensive than requested priority", func(t *testing.T) {
		got := ResolveBillingServiceTierWithCosts("priority", "flex", priorityCost, flexCost)
		require.Equal(t, "priority", got.Billing)
		require.False(t, got.Downgraded)
	})
}

func TestApplyServiceTierBillingResolutionOnlyRewritesDowngrades(t *testing.T) {
	t.Run("codex exception only covers OpenAI default", func(t *testing.T) {
		require.True(t, codexOAuthResponseTierIsNonAuthoritative("default"))
		require.False(t, codexOAuthResponseTierIsNonAuthoritative("standard"))
		require.False(t, codexOAuthResponseTierIsNonAuthoritative("flex"))
	})

	t.Run("openai downgrade rewrites tier", func(t *testing.T) {
		requested := "priority"
		result := &OpenAIForwardResult{ServiceTier: &requested, UpstreamResponseServiceTier: "default"}
		resolution := ApplyOpenAIServiceTierBillingResolution(
			&Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey},
			result,
			serviceTierTestCost(2),
			serviceTierTestCost(1),
		)
		require.True(t, resolution.Downgraded)
		require.NotNil(t, result.ServiceTier)
		require.Equal(t, "default", *result.ServiceTier)
	})

	t.Run("openai honoured tier keeps pointer", func(t *testing.T) {
		requested := "priority"
		result := &OpenAIForwardResult{ServiceTier: &requested, UpstreamResponseServiceTier: "priority"}
		require.False(t, ApplyOpenAIServiceTierBillingResolution(&Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, result).Downgraded)
		require.Same(t, &requested, result.ServiceTier)
	})

	t.Run("openai untiered request stays nil", func(t *testing.T) {
		result := &OpenAIForwardResult{UpstreamResponseServiceTier: "priority"}
		require.False(t, ApplyOpenAIServiceTierBillingResolution(&Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, result).Downgraded)
		require.Nil(t, result.ServiceTier)
	})

	t.Run("openai default request is never raised", func(t *testing.T) {
		requested := "default"
		result := &OpenAIForwardResult{ServiceTier: &requested, UpstreamResponseServiceTier: "priority"}
		require.False(t, ApplyOpenAIServiceTierBillingResolution(&Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, result, serviceTierTestCost(1), serviceTierTestCost(2)).Downgraded)
		require.Same(t, &requested, result.ServiceTier)
	})

	t.Run("openai flex request is never raised", func(t *testing.T) {
		requested := "flex"
		result := &OpenAIForwardResult{ServiceTier: &requested, UpstreamResponseServiceTier: "priority"}
		require.False(t, ApplyOpenAIServiceTierBillingResolution(&Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, result, serviceTierTestCost(1), serviceTierTestCost(2)).Downgraded)
		require.Same(t, &requested, result.ServiceTier)
	})

	for _, accountType := range []string{AccountTypeOAuth, AccountTypeSetupToken} {
		t.Run("codex "+accountType+" keeps outbound priority despite default echo", func(t *testing.T) {
			requested := "priority"
			result := &OpenAIForwardResult{ServiceTier: &requested, UpstreamResponseServiceTier: "default"}
			resolution := ApplyOpenAIServiceTierBillingResolution(
				&Account{Platform: PlatformOpenAI, Type: accountType},
				result,
				serviceTierTestCost(2),
				serviceTierTestCost(1),
			)
			require.False(t, resolution.Downgraded)
			require.Equal(t, "priority", resolution.Requested)
			require.Equal(t, "default", resolution.Observed)
			require.Equal(t, "priority", resolution.Billing)
			require.Same(t, &requested, result.ServiceTier)
		})

		t.Run("codex "+accountType+" still accepts an explicit flex downgrade", func(t *testing.T) {
			requested := "priority"
			result := &OpenAIForwardResult{ServiceTier: &requested, UpstreamResponseServiceTier: "flex"}
			resolution := ApplyOpenAIServiceTierBillingResolution(
				&Account{Platform: PlatformOpenAI, Type: accountType},
				result,
				serviceTierTestCost(2),
				serviceTierTestCost(1),
			)
			require.True(t, resolution.Downgraded)
			require.Equal(t, "flex", resolution.Billing)
			require.Equal(t, "flex", *result.ServiceTier)
		})

		t.Run("codex "+accountType+" response never promotes an untiered request", func(t *testing.T) {
			result := &OpenAIForwardResult{UpstreamResponseServiceTier: "priority"}
			resolution := ApplyOpenAIServiceTierBillingResolution(
				&Account{Platform: PlatformOpenAI, Type: accountType},
				result,
			)
			require.False(t, resolution.Downgraded)
			require.Empty(t, resolution.Billing)
			require.Nil(t, result.ServiceTier)
		})
	}

	t.Run("non-openai oauth still uses the generic response contract", func(t *testing.T) {
		requested := "priority"
		result := &OpenAIForwardResult{ServiceTier: &requested, UpstreamResponseServiceTier: "default"}
		resolution := ApplyOpenAIServiceTierBillingResolution(
			&Account{Platform: PlatformGrok, Type: AccountTypeOAuth},
			result,
			serviceTierTestCost(2),
			serviceTierTestCost(1),
		)
		require.True(t, resolution.Downgraded)
		require.Equal(t, "default", resolution.Billing)
		require.Equal(t, "default", *result.ServiceTier)
	})

	t.Run("anthropic standard speed rewrites fast", func(t *testing.T) {
		requested := "fast"
		result := &ForwardResult{ServiceTier: &requested, UpstreamResponseServiceTier: "standard"}
		require.True(t, ApplyForwardServiceTierBillingResolution(result, serviceTierTestCost(2), serviceTierTestCost(1)).Downgraded)
		require.Equal(t, "standard", *result.ServiceTier)
	})

	t.Run("nil results are ignored", func(t *testing.T) {
		require.False(t, ApplyOpenAIServiceTierBillingResolution(nil, nil).Downgraded)
		require.False(t, ApplyForwardServiceTierBillingResolution(nil).Downgraded)
	})
}
