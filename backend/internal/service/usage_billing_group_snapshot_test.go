//go:build unit

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func usageBillingSnapshotTestGroup() *Group {
	p := usageSnapshotTestValue[float64]
	return &Group{
		ID: 10, Platform: PlatformOpenAI, SubscriptionType: SubscriptionTypeSubscription, Hydrated: true,
		RateMultiplier: 1.5, PeakRateEnabled: true, PeakStart: "10:00", PeakEnd: "12:00", PeakRateMultiplier: 2.5,
		ImageCountBilling: true, ImageRateIndependent: true, ImageRateMultiplier: 1.2,
		ImagePrice1K: p(1), ImagePrice2K: p(2), ImagePrice4K: p(4),
		VideoRateIndependent: true, VideoRateMultiplier: 1.3,
		VideoPrice480P: p(0.1), VideoPrice720P: p(0.2), VideoPrice1080P: p(0.4),
		VideoModelPrices:      map[string]map[string]float64{"video": {"480p": 0.3, "720p": 0.5}},
		WebSearchPricePerCall: p(0.02), SearchPricePer1k: p(5),
		AudioRealtimePricePerMin: p(6), AudioTTSPricePerMillionChars: p(7), AudioSTTPricePerHour: p(8),
		LongContextPricingEnabled: true, FreeOpenAIFast: true,
		ModelPricing: []ChannelModelPricing{{
			ID: 31, ChannelID: 32, Platform: PlatformOpenAI, BillingMode: BillingModeToken,
			Models: []string{"gpt-4o", "gpt-4o-*"}, InputPrice: p(11), OutputPrice: p(12),
			CacheWritePrice: p(13), CacheWrite1hPrice: p(14), CacheReadPrice: p(15),
			FastMultiplier: p(16), FlexMultiplier: p(17), ImageInputPrice: p(18), ImageOutputPrice: p(19), PerRequestPrice: p(20),
			ReasoningEffortMultipliers: map[string]float64{"high": 1.7},
			Intervals: []PricingInterval{{
				ID: 41, PricingID: 31, MinTokens: 1000, MaxTokens: usageSnapshotTestValue(10000), TierLabel: "large", SortOrder: 2,
				InputPrice: p(21), OutputPrice: p(22), CacheWritePrice: p(23), CacheWrite1hPrice: p(24), CacheReadPrice: p(25),
				InputMultiplier: p(26), OutputMultiplier: p(27), CacheWriteMultiplier: p(28), CacheReadMultiplier: p(29), PerRequestPrice: p(30),
			}},
			TimePricing: &ChannelTimePricing{Timezone: "UTC", WeekdaysOnly: true,
				Periods: []ChannelTimePricingPeriod{{StartTime: "00:00", EndTime: "23:59", Multiplier: 1.1}}},
		}},
	}
}

func usageBillingSnapshotTestAPIKey() *APIKey {
	key, _, _, _ := usageSnapshotTestSubjects()
	key.Group = usageBillingSnapshotTestGroup()
	key.GroupID = &key.Group.ID
	return key
}

func usageBillingSnapshotTestEqualGroup(t *testing.T, want, got *Group) {
	t.Helper()
	require.NotNil(t, got)
	actual := *got
	actual.billingSnapshot = nil
	require.Equal(t, want, &actual)
}

func usageBillingSnapshotTestMutatePointers(prices ...*float64) {
	for _, price := range prices {
		if price != nil {
			*price = 999
		}
	}
}

func usageBillingSnapshotTestMutateNested(pricing []ChannelModelPricing, videos map[string]map[string]float64) {
	p := &pricing[0]
	p.Platform, p.BillingMode, p.Models[0] = "changed", BillingModeImage, "changed-model"
	p.ReasoningEffortMultipliers["high"] = 999
	p.ReasoningEffortMultipliers["low"] = 999
	usageBillingSnapshotTestMutatePointers(p.InputPrice, p.OutputPrice, p.CacheWritePrice, p.CacheWrite1hPrice,
		p.CacheReadPrice, p.FastMultiplier, p.FlexMultiplier, p.ImageInputPrice, p.ImageOutputPrice, p.PerRequestPrice)
	iv := &p.Intervals[0]
	*iv.MaxTokens = 999
	iv.MinTokens, iv.TierLabel = 999, "changed-tier"
	usageBillingSnapshotTestMutatePointers(iv.InputPrice, iv.OutputPrice, iv.CacheWritePrice, iv.CacheWrite1hPrice,
		iv.CacheReadPrice, iv.InputMultiplier, iv.OutputMultiplier, iv.CacheWriteMultiplier, iv.CacheReadMultiplier, iv.PerRequestPrice)
	p.TimePricing.Timezone, p.TimePricing.WeekdaysOnly = "Asia/Tokyo", false
	p.TimePricing.Periods[0] = ChannelTimePricingPeriod{StartTime: "01:00", EndTime: "02:00", Multiplier: 999}
	videos["video"]["480p"] = 999
	videos["video"]["1080p"] = 999
	videos["other"] = map[string]float64{"480p": 999}
}

func usageBillingSnapshotTestMutateGroup(g *Group) {
	g.ID, g.Platform, g.SubscriptionType = 999, "changed", SubscriptionTypeStandard
	g.RateMultiplier, g.PeakRateMultiplier = 999, 999
	g.PeakRateEnabled, g.PeakStart, g.PeakEnd = false, "01:00", "02:00"
	g.ImageCountBilling, g.ImageRateIndependent, g.VideoRateIndependent = false, false, false
	g.ImageRateMultiplier, g.VideoRateMultiplier = 999, 999
	g.LongContextPricingEnabled, g.FreeOpenAIFast = false, false
	usageBillingSnapshotTestMutatePointers(g.ImagePrice1K, g.ImagePrice2K, g.ImagePrice4K,
		g.VideoPrice480P, g.VideoPrice720P, g.VideoPrice1080P, g.WebSearchPricePerCall, g.SearchPricePer1k,
		g.AudioRealtimePricePerMin, g.AudioTTSPricePerMillionChars, g.AudioSTTPricePerHour)
	usageBillingSnapshotTestMutateNested(g.ModelPricing, g.VideoModelPrices)
}

func usageBillingSnapshotTestMutateAuthGroup(g *APIKeyAuthGroupSnapshot) {
	g.ID, g.Platform, g.SubscriptionType = 999, "changed", SubscriptionTypeStandard
	g.RateMultiplier, g.PeakRateMultiplier = 999, 999
	g.PeakRateEnabled, g.PeakStart, g.PeakEnd = false, "01:00", "02:00"
	g.ImageCountBilling, g.ImageRateIndependent, g.VideoRateIndependent = false, false, false
	g.ImageRateMultiplier, g.VideoRateMultiplier = 999, 999
	g.LongContextPricingEnabled, g.FreeOpenAIFast = false, false
	usageBillingSnapshotTestMutatePointers(g.ImagePrice1K, g.ImagePrice2K, g.ImagePrice4K,
		g.VideoPrice480P, g.VideoPrice720P, g.VideoPrice1080P, g.WebSearchPricePerCall, g.SearchPricePer1k,
		g.AudioRealtimePricePerMin, g.AudioTTSPricePerMillionChars, g.AudioSTTPricePerHour)
	usageBillingSnapshotTestMutateNested(g.ModelPricing, g.VideoModelPrices)
}

func TestUsageBillingGroupSnapshotOwnsAllMutablePrices(t *testing.T) {
	for _, source := range []string{"repository_group", "auth_snapshot_group", "restored_request_group"} {
		t.Run(source, func(t *testing.T) {
			key := usageBillingSnapshotTestAPIKey()
			svc := &APIKeyService{}
			snapshot := svc.snapshotFromAPIKey(context.Background(), key)
			restored := svc.snapshotToAPIKey(key.Key, snapshot)
			frozen := frozenUsageBillingGroup(restored.Group)
			want := usageBillingSnapshotTestGroup()
			usageBillingSnapshotTestEqualGroup(t, want, frozen)
			require.NotSame(t, key.Group, frozen)
			require.NotSame(t, restored.Group, frozen)
			require.Same(t, frozen, frozenUsageBillingGroup(svc.snapshotToAPIKey("another-request", snapshot).Group))
			switch source {
			case "repository_group":
				usageBillingSnapshotTestMutateGroup(key.Group)
			case "auth_snapshot_group":
				usageBillingSnapshotTestMutateAuthGroup(snapshot.Group)
			case "restored_request_group":
				usageBillingSnapshotTestMutateGroup(restored.Group)
			}
			usageBillingSnapshotTestEqualGroup(t, want, frozen)
		})
	}
}

func TestUsageBillingGroupSnapshotPreservesNilAndEmpty(t *testing.T) {
	for _, tc := range []struct {
		name  string
		group *Group
	}{
		{name: "nil_containers", group: &Group{Hydrated: true}},
		{name: "empty_containers", group: &Group{Hydrated: true, ModelPricing: []ChannelModelPricing{}, VideoModelPrices: map[string]map[string]float64{}}},
		{name: "nested_nil_and_empty", group: &Group{Hydrated: true,
			VideoModelPrices: map[string]map[string]float64{"nil": nil, "empty": {}},
			ModelPricing: []ChannelModelPricing{{}, {
				Models: []string{}, ReasoningEffortMultipliers: map[string]float64{}, Intervals: []PricingInterval{},
				TimePricing: &ChannelTimePricing{Periods: []ChannelTimePricingPeriod{}},
			}, {TimePricing: &ChannelTimePricing{Periods: nil}}},
		}},
		{name: "video_normalization", group: &Group{Hydrated: true,
			VideoModelPrices: map[string]map[string]float64{
				" GROK-IMAGINE-VIDEO ": {"480p": 0.2, "720p": -1, "unknown-resolution": 99},
			},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key, _, _, _ := usageSnapshotTestSubjects()
			key.Group = tc.group
			svc := &APIKeyService{}
			snapshot := svc.snapshotFromAPIKey(context.Background(), key)
			frozen := frozenUsageBillingGroup(svc.snapshotToAPIKey(key.Key, snapshot).Group)
			want := *tc.group
			want.VideoModelPrices = NormalizeVideoModelPrices(tc.group.VideoModelPrices)
			usageBillingSnapshotTestEqualGroup(t, &want, frozen)
		})
		t.Run("raw_auth_"+tc.name, func(t *testing.T) {
			snapshot := &APIKeyAuthSnapshot{
				Version: apiKeyAuthSnapshotVersion,
				Group: &APIKeyAuthGroupSnapshot{
					ModelPricing:     tc.group.ModelPricing,
					VideoModelPrices: tc.group.VideoModelPrices,
				},
			}
			prepareAPIKeyAuthBillingSnapshot(snapshot)
			svc := &APIKeyService{}
			materialized := svc.snapshotToAPIKey("raw-auth", snapshot).Group
			frozen := frozenUsageBillingGroup(materialized)
			want := *tc.group
			want.VideoModelPrices = NormalizeVideoModelPrices(tc.group.VideoModelPrices)
			usageBillingSnapshotTestEqualGroup(t, &want, frozen)
			require.Equal(t, materialized.VideoModelPrices, frozen.VideoModelPrices)
			if tc.name == "video_normalization" {
				require.Equal(t, map[string]map[string]float64{"grok-imagine-video": {"480p": 0.2}}, frozen.VideoModelPrices)
			}
		})
	}
}

func TestUsageBillingGroupSnapshotRequiresPublishedCarrier(t *testing.T) {
	require.Nil(t, frozenUsageBillingGroup(nil))
	require.Nil(t, frozenUsageBillingGroup(&Group{}))
	require.Nil(t, frozenUsageBillingGroup(usageBillingSnapshotTestGroup()))
	prepareAPIKeyAuthBillingSnapshot(nil)
	prepareAPIKeyAuthBillingSnapshot(&APIKeyAuthSnapshot{Version: apiKeyAuthSnapshotVersion})

	svc := &APIKeyService{}
	key := usageBillingSnapshotTestAPIKey()
	key.Group = nil
	snapshot := svc.snapshotFromAPIKey(context.Background(), key)
	require.Nil(t, frozenUsageBillingGroup(svc.snapshotToAPIKey(key.Key, snapshot).Group))

	for _, version := range []int{0, apiKeyAuthSnapshotVersion - 1, apiKeyAuthSnapshotVersion + 1} {
		t.Run(fmt.Sprintf("version_%d", version), func(t *testing.T) {
			invalid := &APIKeyAuthSnapshot{Version: version, Group: &APIKeyAuthGroupSnapshot{ID: 10}}
			prepareAPIKeyAuthBillingSnapshot(invalid)
			require.Nil(t, frozenUsageBillingGroup(svc.snapshotToAPIKey("untrusted", invalid).Group))
			_, used, err := svc.applyAuthCacheEntry("untrusted", &APIKeyAuthCacheEntry{Snapshot: invalid})
			require.NoError(t, err)
			require.False(t, used)
		})
	}
}

func TestUsageBillingGroupSnapshotJSONRoundtripAndReplacement(t *testing.T) {
	svc := &APIKeyService{}
	key := usageBillingSnapshotTestAPIKey()
	original := svc.snapshotFromAPIKey(context.Background(), key)
	originalFrozen := frozenUsageBillingGroup(svc.snapshotToAPIKey(key.Key, original).Group)
	payload, err := json.Marshal(original)
	require.NoError(t, err)
	require.NotContains(t, string(payload), "billingSnapshot")
	require.NotContains(t, string(payload), "billing_snapshot")

	var decoded APIKeyAuthSnapshot
	require.NoError(t, json.Unmarshal(payload, &decoded))
	require.Nil(t, frozenUsageBillingGroup(svc.snapshotToAPIKey(key.Key, &decoded).Group))
	prepareAPIKeyAuthBillingSnapshot(&decoded)
	decodedFrozen := frozenUsageBillingGroup(svc.snapshotToAPIKey(key.Key, &decoded).Group)
	usageBillingSnapshotTestEqualGroup(t, usageBillingSnapshotTestGroup(), decodedFrozen)
	require.NotSame(t, originalFrozen, decodedFrozen)
	afterPrepare, err := json.Marshal(&decoded)
	require.NoError(t, err)
	require.JSONEq(t, string(payload), string(afterPrepare), "local carrier must not change the Redis wire format")
	prepareAPIKeyAuthBillingSnapshot(&decoded)
	require.Same(t, decodedFrozen, frozenUsageBillingGroup(svc.snapshotToAPIKey(key.Key, &decoded).Group))
	usageBillingSnapshotTestMutateAuthGroup(decoded.Group)
	usageBillingSnapshotTestEqualGroup(t, usageBillingSnapshotTestGroup(), decodedFrozen)
	usageBillingSnapshotTestEqualGroup(t, usageBillingSnapshotTestGroup(), originalFrozen)

	// A freshly loaded auth snapshot publishes the new prices without rewriting
	// the frozen object already retained by an in-flight billing record.
	var replacement APIKeyAuthSnapshot
	require.NoError(t, json.Unmarshal(payload, &replacement))
	replacement.Group.RateMultiplier = 3
	*replacement.Group.ModelPricing[0].InputPrice = 42
	prepareAPIKeyAuthBillingSnapshot(&replacement)
	next := frozenUsageBillingGroup(svc.snapshotToAPIKey(key.Key, &replacement).Group)
	require.NotNil(t, next)
	require.NotSame(t, originalFrozen, next)
	require.Equal(t, 3.0, next.RateMultiplier)
	require.Equal(t, 42.0, *next.ModelPricing[0].InputPrice)
	usageBillingSnapshotTestEqualGroup(t, usageBillingSnapshotTestGroup(), originalFrozen)
}

func TestUsageBillingGroupSnapshotL2PreparesBeforeL1Publication(t *testing.T) {
	key := usageBillingSnapshotTestAPIKey()
	sourceService := &APIKeyService{}
	payload, err := json.Marshal(&APIKeyAuthCacheEntry{Snapshot: sourceService.snapshotFromAPIKey(context.Background(), key)})
	require.NoError(t, err)
	cache := &authCacheStub{getAuthCache: func(context.Context, string) (*APIKeyAuthCacheEntry, error) {
		var decoded APIKeyAuthCacheEntry
		err := json.Unmarshal(payload, &decoded)
		return &decoded, err
	}}
	svc := NewAPIKeyService(nil, nil, nil, nil, nil, cache, &config.Config{
		APIKeyAuth: config.APIKeyAuthCacheConfig{L1Size: 100, L1TTLSeconds: 60, L2TTLSeconds: 60},
	})
	require.NotNil(t, svc.authCacheL1)
	t.Cleanup(svc.authCacheL1.Close)
	entry, ok := svc.getAuthCacheEntry(context.Background(), "frozen-l2")
	require.True(t, ok)
	restored, used, err := svc.applyAuthCacheEntry(key.Key, entry)
	require.NoError(t, err)
	require.True(t, used)
	frozen := frozenUsageBillingGroup(restored.Group)
	usageBillingSnapshotTestEqualGroup(t, usageBillingSnapshotTestGroup(), frozen)
	svc.authCacheL1.Wait()
	cached, ok := svc.authCacheL1.Get("frozen-l2")
	require.True(t, ok)
	l1Entry := cached.(*APIKeyAuthCacheEntry)
	require.Same(t, frozen, frozenUsageBillingGroup(svc.snapshotToAPIKey(key.Key, l1Entry.Snapshot).Group))
	usageBillingSnapshotTestMutateAuthGroup(entry.Snapshot.Group)
	usageBillingSnapshotTestEqualGroup(t, usageBillingSnapshotTestGroup(), frozen)

	nextSource := usageBillingSnapshotTestAPIKey()
	nextSource.Group.RateMultiplier = 3
	*nextSource.Group.ModelPricing[0].InputPrice = 42
	payload, err = json.Marshal(&APIKeyAuthCacheEntry{Snapshot: sourceService.snapshotFromAPIKey(context.Background(), nextSource)})
	require.NoError(t, err)
	svc.authCacheL1.Del("frozen-l2")
	svc.authCacheL1.Wait()
	nextEntry, ok := svc.getAuthCacheEntry(context.Background(), "frozen-l2")
	require.True(t, ok)
	nextKey, used, err := svc.applyAuthCacheEntry(key.Key, nextEntry)
	require.NoError(t, err)
	require.True(t, used)
	next := frozenUsageBillingGroup(nextKey.Group)
	require.NotNil(t, next)
	require.NotSame(t, frozen, next)
	require.Equal(t, 3.0, next.RateMultiplier)
	require.Equal(t, 42.0, *next.ModelPricing[0].InputPrice)
	usageBillingSnapshotTestEqualGroup(t, usageBillingSnapshotTestGroup(), frozen)
}

func TestUsageBillingGroupSnapshotReusedByDeferredInputs(t *testing.T) {
	key := usageBillingSnapshotTestAPIKey()
	svc := &APIKeyService{}
	snapshot := svc.snapshotFromAPIKey(context.Background(), key)
	restored := svc.snapshotToAPIKey(key.Key, snapshot)
	frozen := frozenUsageBillingGroup(restored.Group)
	require.NotNil(t, frozen)
	for range 2 {
		gateway := SnapshotRecordUsageInput(&RecordUsageInput{APIKey: restored, User: restored.User})
		openAI := SnapshotOpenAIRecordUsageInput(&OpenAIRecordUsageInput{APIKey: restored, User: restored.User})
		require.Same(t, frozen, gateway.APIKey.Group)
		require.Same(t, frozen, openAI.APIKey.Group)
		require.NotSame(t, restored, gateway.APIKey)
		require.NotSame(t, restored, openAI.APIKey)
	}
	// Groups loaded outside auth-cache publication must still be copied per task.
	plain := usageBillingSnapshotTestAPIKey()
	first := SnapshotRecordUsageInput(&RecordUsageInput{APIKey: plain, User: plain.User})
	second := SnapshotRecordUsageInput(&RecordUsageInput{APIKey: plain, User: plain.User})
	require.NotSame(t, plain.Group, first.APIKey.Group)
	require.NotSame(t, first.APIKey.Group, second.APIKey.Group)
	usageBillingSnapshotTestMutateGroup(plain.Group)
	usageBillingSnapshotTestEqualGroup(t, usageBillingSnapshotTestGroup(), first.APIKey.Group)
	usageBillingSnapshotTestEqualGroup(t, usageBillingSnapshotTestGroup(), second.APIKey.Group)
}

func TestUsageBillingGroupSnapshotConcurrentReaders(t *testing.T) {
	key := usageBillingSnapshotTestAPIKey()
	svc := &APIKeyService{}
	snapshot := svc.snapshotFromAPIKey(context.Background(), key)
	requestGroup := svc.snapshotToAPIKey(key.Key, snapshot).Group
	frozen := frozenUsageBillingGroup(requestGroup)
	usageBillingSnapshotTestEqualGroup(t, usageBillingSnapshotTestGroup(), frozen)
	want, err := json.Marshal(frozen)
	require.NoError(t, err)

	const readers = 8
	start := make(chan struct{})
	errors := make(chan error, readers)
	var wg sync.WaitGroup
	for range readers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for range 100 {
				got := frozenUsageBillingGroup(requestGroup)
				if got != frozen {
					errors <- fmt.Errorf("reader received a different frozen group")
					return
				}
				data, err := json.Marshal(got)
				if err != nil || string(data) != string(want) {
					errors <- fmt.Errorf("frozen billing prices changed during source mutation: %v", err)
					return
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		for range 100 {
			usageBillingSnapshotTestMutateGroup(key.Group)
		}
	}()
	close(start)
	wg.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	usageBillingSnapshotTestEqualGroup(t, usageBillingSnapshotTestGroup(), frozen)
}
