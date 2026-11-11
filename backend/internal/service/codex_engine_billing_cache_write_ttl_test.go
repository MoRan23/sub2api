package service

import (
	"context"
	"encoding/json"
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

func engineBillingTTLTestEvent(id string, sequence int, short, long int) map[string]any {
	event := billingTestEvent(id, sequence)
	event["model"] = "gpt-engine-fixture"
	event["usage"] = map[string]any{
		"input_tokens": 100, "output_tokens": 10, "cached_input_tokens": 20,
		"cache_write_input_tokens":     short + long,
		"cache_write_30m_input_tokens": short, "cache_write_12h_input_tokens": long,
	}
	event["applicable_usage"] = []string{"input_tokens", "output_tokens", "cached_input_tokens", "cache_write_input_tokens", "cache_write_30m_input_tokens", "cache_write_12h_input_tokens"}
	return event
}

func engineBillingRemoveTTL(event map[string]any) {
	usage := event["usage"].(map[string]any)
	delete(usage, "cache_write_30m_input_tokens")
	delete(usage, "cache_write_12h_input_tokens")
	event["applicable_usage"] = []string{"input_tokens", "output_tokens", "cached_input_tokens", "cache_write_input_tokens"}
}

func BenchmarkCodexEngineBillingUsageEvent(b *testing.B) {
	for _, name := range []string{"aggregate", "ttl"} {
		b.Run(name, func(b *testing.B) {
			facts := engineBillingTTLTestEvent("benchmark", 1, 9, 21)
			if name == "aggregate" {
				engineBillingRemoveTTL(facts)
			}
			raw, err := json.Marshal(facts)
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if _, err := ParseCodexEngineBillingUsageEvent(raw); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func TestCodexEngineBillingCacheWriteTTLProtocol(t *testing.T) {
	for _, tc := range []struct {
		name        string
		short, long int
	}{
		{"30m", 30, 0}, {"12h", 0, 30}, {"mixed", 9, 21}, {"zero", 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			event, err := ParseCodexEngineBillingUsageEvent(billingJSON(t, engineBillingTTLTestEvent("ttl", 1, tc.short, tc.long)))
			require.NoError(t, err)
			require.Equal(t, tc.short+tc.long, event.Usage.CacheCreationInputTokens)
			require.Equal(t, tc.short, event.Usage.CacheCreation30mInputTokens)
			require.Equal(t, tc.long, event.Usage.CacheCreation12hInputTokens)
			for _, key := range []string{"cache_write_30m_input_tokens", "cache_write_12h_input_tokens"} {
				require.True(t, event.UsagePresent[key], "explicit zero remains observed")
				require.True(t, event.ApplicableUsage[key])
			}
			restored, err := ParseCodexEngineBillingUsageEvent(event.Raw)
			require.NoError(t, err)
			require.Equal(t, event.Fingerprint, restored.Fingerprint)
			require.Equal(t, event.UsageValues, restored.UsageValues)
		})
	}
	legacy := engineBillingTTLTestEvent("aggregate", 1, 9, 21)
	engineBillingRemoveTTL(legacy)
	event, err := ParseCodexEngineBillingUsageEvent(billingJSON(t, legacy))
	require.NoError(t, err)
	require.Equal(t, 30, event.Usage.CacheCreationInputTokens)
	require.NotContains(t, event.UsageValues, "cache_write_30m_input_tokens")
	require.NotContains(t, event.UsageValues, "cache_write_12h_input_tokens")
	require.Zero(t, event.Usage.CacheCreation30mInputTokens)
	require.Zero(t, event.Usage.CacheCreation12hInputTokens)

	all := engineBillingTTLTestEvent("all-dimensions", 1, 0, 0)
	for _, name := range []string{"image_input_tokens", "cached_image_input_tokens", "image_output_tokens", "search_calls", "search_operations", "images"} {
		all["applicable_usage"] = append(all["applicable_usage"].([]string), name)
		all["usage"].(map[string]any)[name] = 0
	}
	_, err = ParseCodexEngineBillingUsageEvent(billingJSON(t, all))
	require.NoError(t, err, "all 12 supported dimensions fit the applicability bound")
}

func TestCodexEngineBillingCacheWriteTTLRejectsInvalidFacts(t *testing.T) {
	for name, mutate := range map[string]func(map[string]any){
		"missing short":     func(e map[string]any) { delete(e["usage"].(map[string]any), "cache_write_30m_input_tokens") },
		"missing long":      func(e map[string]any) { delete(e["usage"].(map[string]any), "cache_write_12h_input_tokens") },
		"missing aggregate": func(e map[string]any) { delete(e["usage"].(map[string]any), "cache_write_input_tokens") },
		"negative":          func(e map[string]any) { e["usage"].(map[string]any)["cache_write_30m_input_tokens"] = -1 },
		"fraction":          func(e map[string]any) { e["usage"].(map[string]any)["cache_write_12h_input_tokens"] = 21.5 },
		"null":              func(e map[string]any) { e["usage"].(map[string]any)["cache_write_12h_input_tokens"] = nil },
		"string":            func(e map[string]any) { e["usage"].(map[string]any)["cache_write_12h_input_tokens"] = "21" },
		"counter overflow": func(e map[string]any) {
			e["usage"].(map[string]any)["cache_write_12h_input_tokens"] = json.Number("9223372036854775808")
		},
		"sum below aggregate": func(e map[string]any) { e["usage"].(map[string]any)["cache_write_12h_input_tokens"] = 20 },
		"sum above aggregate": func(e map[string]any) { e["usage"].(map[string]any)["cache_write_12h_input_tokens"] = 22 },
		"sum overflow": func(e map[string]any) {
			u := e["usage"].(map[string]any)
			u["input_tokens"], u["cache_write_input_tokens"] = int64(math.MaxInt64), int64(math.MaxInt64)
			u["cache_write_30m_input_tokens"], u["cache_write_12h_input_tokens"] = int64(math.MaxInt64), 1
		},
		"undeclared TTL": func(e map[string]any) {
			e["applicable_usage"] = []string{"input_tokens", "output_tokens", "cached_input_tokens", "cache_write_input_tokens"}
		},
		"partial applicability": func(e map[string]any) {
			engineBillingRemoveTTL(e)
			e["usage_complete"] = false
			e["applicable_usage"] = append(e["applicable_usage"].([]string), "cache_write_30m_input_tokens")
		},
		"TTL without aggregate applicability": func(e map[string]any) {
			e["usage_complete"] = false
			e["usage"] = map[string]any{}
			e["applicable_usage"] = []string{"cache_write_30m_input_tokens", "cache_write_12h_input_tokens"}
		},
		"partial incomplete TTL": func(e map[string]any) {
			e["usage_complete"] = false
			delete(e["usage"].(map[string]any), "cache_write_12h_input_tokens")
		},
		"Anthropic bucket": func(e map[string]any) { e["usage"].(map[string]any)["cache_write_5m_input_tokens"] = 1 },
	} {
		t.Run(name, func(t *testing.T) {
			e := engineBillingTTLTestEvent("ttl", 1, 9, 21)
			mutate(e)
			_, err := ParseCodexEngineBillingUsageEvent(billingJSON(t, e))
			require.ErrorIs(t, err, ErrCodexEngineBillingProtocol)
		})
	}
}

func TestCodexEngineBillingCacheWriteTTLMixedSummary(t *testing.T) {
	ttl := engineBillingTTLTestEvent("ttl", 1, 0, 30)
	aggregate := engineBillingTTLTestEvent("aggregate", 2, 9, 21)
	engineBillingRemoveTTL(aggregate)
	page := billingTestPage([]map[string]any{ttl, aggregate}, true)
	summary := map[string]any{"input_tokens": 200, "output_tokens": 20, "cached_input_tokens": 40, "cache_write_input_tokens": 60, "cache_write_30m_input_tokens": 0, "cache_write_12h_input_tokens": 30}
	page["summary"].(map[string]any)["groups"] = []any{map[string]any{"kind": "model", "purpose": "response", "model": "gpt-engine-fixture", "service_tier": "standard", "usage": summary}}
	parsed, err := ParseCodexEngineBillingPage(billingJSON(t, page))
	require.NoError(t, err)
	store := &engineBillingTestStore{}
	for _, e := range parsed.Events {
		require.NoError(t, store.StageEvents(t.Context(), &CodexEngineBillingReceivedPage{JobID: 1, Events: []CodexEngineBillingReceivedEvent{{
			EventID: e.EventID, Sequence: e.Sequence, Fingerprint: e.Fingerprint, Raw: e.Raw,
			Summary: CodexEngineBillingSummaryGroup{Kind: e.Kind, Purpose: e.Purpose, Model: e.Model, ServiceTier: optionalTrimmedStringPtr(e.ServiceTier), Usage: e.UsageValues},
		}}}))
	}
	actual, err := store.ReadSummary(t.Context(), 1, 2)
	require.NoError(t, err)
	require.True(t, codexEngineBillingSummariesEqual(actual, parsed.Summary))
	delete(parsed.Summary[0].Usage, "cache_write_30m_input_tokens")
	require.False(t, codexEngineBillingSummariesEqual(actual, parsed.Summary), "observed zero is distinct from omission")

	for _, long := range []any{61, int64(math.MaxInt64)} {
		summary["cache_write_12h_input_tokens"] = long
		_, err := ParseCodexEngineBillingPage(billingJSON(t, page))
		require.ErrorIs(t, err, ErrCodexEngineBillingProtocol, "known TTL subset must fit the summary aggregate")
	}
	summary["cache_write_12h_input_tokens"] = 30
	delete(summary, "cache_write_30m_input_tokens")
	_, err = ParseCodexEngineBillingPage(billingJSON(t, page))
	require.ErrorIs(t, err, ErrCodexEngineBillingProtocol)
}

func TestCodexEngineBillingCacheWriteTTLPricing(t *testing.T) {
	longPrice := .6
	zeroPrice := 0.0
	for _, tc := range []struct {
		name        string
		short, long int
		aggregate   bool
		longPrice   *float64
		cacheCost   float64
	}{
		{"30m", 30, 0, false, &longPrice, 9},
		{"12h", 0, 30, false, &longPrice, 18},
		{"mixed", 9, 21, false, &longPrice, 15.3},
		{"aggregate fallback", 9, 21, true, &longPrice, 9},
		{"flat card", 9, 21, false, nil, 9},
		{"free 12h", 9, 21, false, &zeroPrice, 2.7},
		{"zero writes", 0, 0, false, &longPrice, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			job, snapshot := engineFrozenTestSnapshot(t)
			price := snapshot.Prices["gpt-engine-fixture"]
			price.Resolved.BasePricing.CacheCreation12hPricePerToken = tc.longPrice
			price.StatsCatalog = price.Resolved.BasePricing
			snapshot.Prices["gpt-engine-fixture"] = price
			job.Snapshot = billingJSON(t, snapshot)
			facts := engineBillingTTLTestEvent("ttl", 1, tc.short, tc.long)
			if tc.aggregate {
				engineBillingRemoveTTL(facts)
			}
			event, err := ParseCodexEngineBillingUsageEvent(billingJSON(t, facts))
			require.NoError(t, err)
			cmd, log, err := (&OpenAIGatewayService{}).BuildCodexEngineBillingEvent(t.Context(), job, &event)
			require.NoError(t, err)
			total := float64(80-tc.short-tc.long)*.1 + 10*.2 + 20*.01 + tc.cacheCost
			require.InDelta(t, tc.cacheCost, log.CacheCreationCost, 1e-12)
			require.InDelta(t, total, log.TotalCost, 1e-12)
			require.InDelta(t, total*2, cmd.BalanceCost, 1e-12)
			require.InDelta(t, total*3, cmd.AccountQuotaCost, 1e-12)
			require.NotNil(t, log.AccountStatsCost)
			require.InDelta(t, total, *log.AccountStatsCost, 1e-12)
			require.Equal(t, tc.short+tc.long, log.CacheCreationTokens)
			require.Equal(t, 80-tc.short-tc.long, log.InputTokens)
			require.Equal(t, event.Usage.CacheCreation30mInputTokens, log.CacheCreation30mTokens)
			require.Equal(t, event.Usage.CacheCreation12hInputTokens, log.CacheCreation12hTokens)
			require.Zero(t, log.CacheCreation5mTokens)
			require.Zero(t, log.CacheCreation1hTokens)
		})
	}
}

func TestCodexEngineBillingCacheWriteTTLCurrentChannelPricing(t *testing.T) {
	input, output, cached, short, long := .1, .2, .01, .3, .6
	card := ChannelModelPricing{Platform: PlatformOpenAI, Models: []string{"gpt-engine-fixture"}, BillingMode: BillingModeToken, InputPrice: &input, OutputPrice: &output, CacheReadPrice: &cached, CacheWritePrice: &short, CacheWrite12hPrice: &long}
	group := &Group{ID: 17}
	svc := engineCurrentPricingTestService(group)
	svc.channelService.cache.Store(populateChannelCache([]Channel{{ID: 1, Status: StatusActive, GroupIDs: []int64{group.ID}, ModelPricing: []ChannelModelPricing{card}, ApplyPricingToAccountStats: true}}, map[int64]string{group.ID: PlatformOpenAI}))
	job, snapshot := engineFrozenTestSnapshot(t)
	snapshot.Version, snapshot.Prices, snapshot.GroupID = codexEnginePricingSnapshotVersion, nil, &group.ID
	snapshot.SearchPrice = 0
	job.Snapshot = billingJSON(t, snapshot)
	event, err := ParseCodexEngineBillingUsageEvent(billingJSON(t, engineBillingTTLTestEvent("ttl", 1, 9, 21)))
	require.NoError(t, err)
	cmd, log, err := svc.BuildCodexEngineBillingEvent(t.Context(), job, &event)
	require.NoError(t, err)
	require.InDelta(t, 15.3, log.CacheCreationCost, 1e-12)
	require.InDelta(t, 22.5, log.TotalCost, 1e-12)
	require.InDelta(t, 45, cmd.BalanceCost, 1e-12)
	require.NotNil(t, log.AccountStatsCost)
	require.InDelta(t, 22.5, *log.AccountStatsCost, 1e-12)
}

func TestCodexEngineBillingCacheWriteTTLUnobservedApplicableStaysPending(t *testing.T) {
	facts := engineBillingTTLTestEvent("unobserved", 1, 9, 21)
	facts["usage_complete"] = false
	delete(facts["usage"].(map[string]any), "cache_write_30m_input_tokens")
	delete(facts["usage"].(map[string]any), "cache_write_12h_input_tokens")
	event, err := ParseCodexEngineBillingUsageEvent(billingJSON(t, facts))
	require.NoError(t, err, "incomplete facts remain receivable")
	job, _ := engineFrozenTestSnapshot(t)
	cmd, log, err := (&OpenAIGatewayService{}).BuildCodexEngineBillingEvent(t.Context(), job, &event)
	require.ErrorContains(t, err, "engine token evidence missing cache_write_30m_input_tokens")
	require.Nil(t, cmd)
	require.Nil(t, log)
}

func TestCodexEngineBillingCacheWriteTTLSettlementReplay(t *testing.T) {
	job, _ := engineFrozenTestSnapshot(t)
	facts := engineBillingTTLTestEvent("ttl", 1, 9, 21)
	event, err := ParseCodexEngineBillingUsageEvent(billingJSON(t, facts))
	require.NoError(t, err)
	store := &engineFrozenStoreStub{}
	svc := &OpenAIGatewayService{}
	require.NoError(t, svc.SettleCodexEngineBillingEvent(t.Context(), store, job, &event))
	replayed, err := ParseCodexEngineBillingUsageEvent(event.Raw)
	require.NoError(t, err)
	require.NoError(t, svc.SettleCodexEngineBillingEvent(t.Context(), store, job, &replayed))
	require.Equal(t, 1, store.applied)
	require.Equal(t, 9, store.received.UsageLog.CacheCreation30mTokens)
	require.Equal(t, 21, store.received.UsageLog.CacheCreation12hTokens)
	require.Equal(t, int64(21), store.received.Summary.Usage["cache_write_12h_input_tokens"])
	facts["usage"].(map[string]any)["cache_write_30m_input_tokens"] = 10
	facts["usage"].(map[string]any)["cache_write_12h_input_tokens"] = 20
	changed, err := ParseCodexEngineBillingUsageEvent(billingJSON(t, facts))
	require.NoError(t, err)
	require.NotEqual(t, event.Fingerprint, changed.Fingerprint)
	require.ErrorIs(t, svc.SettleCodexEngineBillingEvent(t.Context(), store, job, &changed), ErrCodexEngineBillingConflict)
	engineBillingRemoveTTL(facts)
	aggregate, err := ParseCodexEngineBillingUsageEvent(billingJSON(t, facts))
	require.NoError(t, err)
	require.ErrorIs(t, svc.SettleCodexEngineBillingEvent(t.Context(), store, job, &aggregate), ErrCodexEngineBillingConflict)
	require.Equal(t, 1, store.applied)
}

func TestCodexEngineBillingCacheWriteTTLDeliveryGate(t *testing.T) {
	for _, outcome := range []string{CodexEngineBillingDelivered, CodexEngineBillingNoCustomerCharge} {
		t.Run(outcome, func(t *testing.T) {
			base := &engineBillingTestStore{}
			store := &engineBillingDeliveryTestStore{engineBillingTestStore: base}
			job := &CodexEngineBillingJob{ID: 3, ClaimToken: "claim", ReceiptID: billingTestReceipt, DeliveryOutcome: CodexEngineBillingDeliveryPending}
			event, err := ParseCodexEngineBillingUsageEvent(billingJSON(t, engineBillingTTLTestEvent("ttl", 1, 9, 21)))
			require.NoError(t, err)
			require.NoError(t, base.StageEvents(t.Context(), &CodexEngineBillingReceivedPage{JobID: job.ID, Events: []CodexEngineBillingReceivedEvent{{EventID: event.EventID, Sequence: event.Sequence, Fingerprint: event.Fingerprint, Raw: event.Raw}}}))
			settled := 0
			svc := NewCodexEngineBillingService(store, nil, nil, nil)
			svc.settle = func(_ context.Context, j *CodexEngineBillingJob, e *CodexEngineBillingUsageEvent) error {
				settled++
				require.Equal(t, 21, e.Usage.CacheCreation12hInputTokens)
				base.markSettled(j.ID, e)
				return nil
			}
			pending, err := svc.settleReceived(t.Context(), job)
			require.NoError(t, err)
			require.True(t, pending)
			require.Zero(t, settled)
			require.NoError(t, svc.FinalizeDelivery(t.Context(), job, outcome))
			pending, err = svc.settleReceived(t.Context(), job)
			require.NoError(t, err)
			require.False(t, pending)
			if outcome == CodexEngineBillingDelivered {
				require.Equal(t, 1, settled)
			} else {
				require.Zero(t, settled)
				require.Empty(t, base.received[job.ID], "incomplete delivery discards TTL facts")
			}
		})
	}
}
