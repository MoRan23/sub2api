package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func engineCompactFloat(v float64) *float64 { return &v }

type engineCurrentPricingGroupRepo struct {
	GroupRepository
	group *Group
	err   error
	ids   []int64
}

func (r *engineCurrentPricingGroupRepo) GetByIDLite(_ context.Context, id int64) (*Group, error) {
	r.ids = append(r.ids, id)
	return r.group, r.err
}

func engineCurrentPricingTestService(group *Group) *OpenAIGatewayService {
	channels := &ChannelService{groupRepo: &engineCurrentPricingGroupRepo{group: group}}
	channels.cache.Store(populateChannelCache(nil, map[int64]string{}))
	return &OpenAIGatewayService{channelService: channels, billingService: &BillingService{fallbackPrices: map[string]*ModelPricing{}}}
}

func engineCurrentPricingTestJob(t *testing.T, group *Group) *CodexEngineBillingJob {
	t.Helper()
	job, snapshot := engineFrozenTestSnapshot(t)
	snapshot.Version, snapshot.Prices, snapshot.SearchPrice = 4, nil, 0
	if group != nil {
		snapshot.GroupID = &group.ID
	}
	var err error
	job.Snapshot, err = marshalCodexEnginePricingSnapshot(&snapshot)
	require.NoError(t, err)
	return job
}

func TestCodexEngineCurrentPricingUsesLiveGroupAndOriginalIdentity(t *testing.T) {
	group := &Group{ID: 17, ModelPricing: []ChannelModelPricing{{Models: []string{"gpt-engine-fixture"}, BillingMode: BillingModeToken, InputPrice: engineCompactFloat(.1), OutputPrice: engineCompactFloat(.2), CacheReadPrice: engineCompactFloat(.01)}}}
	svc := engineCurrentPricingTestService(group)
	job := engineCurrentPricingTestJob(t, group)
	_, first, err := svc.BuildCodexEngineBillingEvent(context.Background(), job, engineFrozenTestModelEvent())
	require.NoError(t, err)
	require.InDelta(t, 10.2, first.TotalCost, 1e-12)
	group.ModelPricing[0].InputPrice = engineCompactFloat(.2)
	group.RateMultiplier = 100
	cmd, second, err := svc.BuildCodexEngineBillingEvent(context.Background(), job, engineFrozenTestModelEvent())
	require.NoError(t, err)
	require.InDelta(t, 18.2, second.TotalCost, 1e-12)
	require.InDelta(t, 36.4, cmd.BalanceCost, 1e-12, "request multiplier remains frozen")
	require.Equal(t, []int64{17, 17}, svc.channelService.groupRepo.(*engineCurrentPricingGroupRepo).ids)
	require.Equal(t, &group.ID, second.GroupID)
}

func TestCodexEngineCurrentPricingMissingDependenciesStayPending(t *testing.T) {
	group := &Group{ID: 17}
	job := engineCurrentPricingTestJob(t, group)
	for _, tc := range []struct {
		name    string
		service *OpenAIGatewayService
	}{
		{"service", &OpenAIGatewayService{}},
		{"deleted group", engineCurrentPricingTestService(nil)},
		{"missing price", engineCurrentPricingTestService(group)},
		{"wrong group", engineCurrentPricingTestService(&Group{ID: 99})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd, log, err := tc.service.BuildCodexEngineBillingEvent(context.Background(), job, engineFrozenTestModelEvent())
			require.Error(t, err)
			require.Nil(t, cmd)
			require.Nil(t, log)
		})
	}
	svc := engineCurrentPricingTestService(group)
	svc.channelService.groupRepo.(*engineCurrentPricingGroupRepo).err = errors.New("database unavailable")
	_, _, err := svc.BuildCodexEngineBillingEvent(context.Background(), job, engineFrozenTestModelEvent())
	require.ErrorContains(t, err, "database unavailable")
}

func TestCodexEngineCurrentPricingUnmatchedTierIsNotFree(t *testing.T) {
	group := &Group{ID: 17, ModelPricing: []ChannelModelPricing{{Models: []string{"gpt-engine-fixture"}, BillingMode: BillingModePerRequest, Intervals: []PricingInterval{{MinTokens: 500, PerRequestPrice: engineCompactFloat(.5)}}}}}
	svc := engineCurrentPricingTestService(group)
	job := engineCurrentPricingTestJob(t, group)
	event := engineFrozenTestModelEvent()
	_, _, err := svc.BuildCodexEngineBillingEvent(context.Background(), job, event)
	require.ErrorIs(t, err, ErrModelPricingUnavailable)
	group.ModelPricing[0].PerRequestPrice = engineCompactFloat(0)
	_, log, err := svc.BuildCodexEngineBillingEvent(context.Background(), job, event)
	require.NoError(t, err)
	require.Zero(t, log.TotalCost)
}

func TestCodexEngineCurrentPricingSearchAndImageAreResolvedAtSettlement(t *testing.T) {
	group := &Group{ID: 17, WebSearchPricePerCall: engineCompactFloat(.2), ImagePrice1K: engineCompactFloat(.3)}
	svc := engineCurrentPricingTestService(group)
	job := engineCurrentPricingTestJob(t, group)
	search := &CodexEngineBillingUsageEvent{Kind: "search", Model: "gpt-engine-fixture", SearchOperations: 2}
	group.WebSearchPricePerCall = engineCompactFloat(.4)
	_, log, err := svc.BuildCodexEngineBillingEvent(context.Background(), job, search)
	require.NoError(t, err)
	require.InDelta(t, .8, log.TotalCost, 1e-12)
	require.InDelta(t, 4, log.ActualCost, 1e-12)
	image := &CodexEngineBillingUsageEvent{Kind: "image", Model: "gpt-image-fixture", ImageCount: 2, ImageActual: &CodexEngineBillingImageMetadata{Size: "1024x1024"}}
	group.ImagePrice1K = engineCompactFloat(.7)
	_, log, err = svc.BuildCodexEngineBillingEvent(context.Background(), job, image)
	require.NoError(t, err)
	require.InDelta(t, 1.4, log.TotalCost, 1e-12)
	require.InDelta(t, 5.6, log.ActualCost, 1e-12)
	group.ImagePrice1K = nil
	_, _, err = svc.BuildCodexEngineBillingEvent(context.Background(), job, image)
	require.ErrorIs(t, err, ErrModelPricingUnavailable)
	group.ImagePrice1K = engineCompactFloat(0)
	_, log, err = svc.BuildCodexEngineBillingEvent(context.Background(), job, image)
	require.NoError(t, err)
	require.Zero(t, log.TotalCost, "explicit free image price is distinct from missing price")
}

func TestCodexEngineCurrentPricingChannelAndStatsFollowCurrentCards(t *testing.T) {
	group := &Group{ID: 17, Platform: PlatformOpenAI}
	svc := engineCurrentPricingTestService(group)
	job := engineCurrentPricingTestJob(t, group)
	card := ChannelModelPricing{Models: []string{"gpt-engine-fixture"}, Platform: PlatformOpenAI, BillingMode: BillingModeToken, InputPrice: engineCompactFloat(.2), OutputPrice: engineCompactFloat(.3), CacheReadPrice: engineCompactFloat(.02)}
	statsCard := card.Clone()
	statsCard.InputPrice = engineCompactFloat(.4)
	channel := Channel{ID: 7, Status: StatusActive, GroupIDs: []int64{17}, ModelPricing: []ChannelModelPricing{card}, AccountStatsPricingRules: []AccountStatsPricingRule{{GroupIDs: []int64{17}, Pricing: []ChannelModelPricing{statsCard}}}}
	svc.channelService.cache.Store(populateChannelCache([]Channel{channel}, map[int64]string{17: PlatformOpenAI}))
	_, log, err := svc.BuildCodexEngineBillingEvent(context.Background(), job, engineFrozenTestModelEvent())
	require.NoError(t, err)
	require.InDelta(t, 19.4, log.TotalCost, 1e-12)
	require.NotNil(t, log.AccountStatsCost)
	require.InDelta(t, 35.4, *log.AccountStatsCost, 1e-12)
	channel.AccountStatsPricingRules = nil
	channel.ApplyPricingToAccountStats = true
	channel.ModelPricing[0].InputPrice = engineCompactFloat(.5)
	svc.channelService.cache.Store(populateChannelCache([]Channel{channel}, map[int64]string{17: PlatformOpenAI}))
	_, log, err = svc.BuildCodexEngineBillingEvent(context.Background(), job, engineFrozenTestModelEvent())
	require.NoError(t, err)
	require.InDelta(t, 43.4, log.TotalCost, 1e-12)
	require.InDelta(t, log.TotalCost, *log.AccountStatsCost, 1e-12)
}

func TestCodexEngineCurrentPricingKeepsRequestTimeAndEntitlements(t *testing.T) {
	group := &Group{ID: 17, Platform: PlatformOpenAI, RateMultiplier: 1, LongContextPricingEnabled: true, ImageCountBilling: true}
	svc := engineCurrentPricingTestService(group)
	card := ChannelModelPricing{Models: []string{"gpt-engine-fixture"}, Platform: PlatformOpenAI, BillingMode: BillingModeToken, InputPrice: engineCompactFloat(.1), OutputPrice: engineCompactFloat(.2), CacheReadPrice: engineCompactFloat(.01), TimePricing: &ChannelTimePricing{Timezone: "UTC", Periods: []ChannelTimePricingPeriod{{StartTime: "02:00", EndTime: "03:00", Multiplier: 3}}}}
	svc.channelService.cache.Store(populateChannelCache([]Channel{{ID: 7, Status: StatusActive, GroupIDs: []int64{17}, ModelPricing: []ChannelModelPricing{card}}}, map[int64]string{17: PlatformOpenAI}))
	job := engineCurrentPricingTestJob(t, group)
	_, log, err := svc.BuildCodexEngineBillingEvent(context.Background(), job, engineFrozenTestModelEvent())
	require.NoError(t, err)
	require.InDelta(t, 30.6, log.TotalCost, 1e-12, "current time rule is evaluated at original PricingAt 02:03 UTC")
	in := engineReceiptTestInput("")
	in.APIKey.GroupID, in.APIKey.Group = &group.ID, group
	in.Result.ImageEndpoint = true
	raw, err := svc.CaptureCodexEngineBillingSnapshot(context.Background(), in)
	require.NoError(t, err)
	group.LongContextPricingEnabled, group.ImageCountBilling = false, false
	snapshot, err := decodeCodexEnginePricingSnapshot(raw)
	require.NoError(t, err)
	require.True(t, snapshot.GroupLongContextPricing)
	require.True(t, snapshot.ImageCountBilling)
	require.True(t, snapshot.ImageEndpoint)
}

func TestCodexEngineCurrentPricingCodecRejectsPriceCatalog(t *testing.T) {
	job := engineCurrentPricingTestJob(t, nil)
	for _, field := range []string{"prices", "price_refs", "price_cards", "token_prices", "search_price"} {
		t.Run(field, func(t *testing.T) {
			var value map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(job.Snapshot, &value))
			switch field {
			case "prices":
				value[field] = json.RawMessage(`{"model":{}}`)
			case "price_refs":
				value[field] = json.RawMessage(`{}`)
			case "search_price":
				value[field] = json.RawMessage(`0.1`)
			default:
				value[field] = json.RawMessage(`[]`)
			}
			raw, err := json.Marshal(value)
			require.NoError(t, err)
			_, err = decodeCodexEnginePricingSnapshot(raw)
			require.Error(t, err)
		})
	}
}

func TestCodexEngineCurrentPricingCaptureOmitsCatalog(t *testing.T) {
	const groupID int64 = 321
	models := make([]string, 64)
	for i := range models {
		models[i] = fmt.Sprintf("gpt-%03d-%s", i, strings.Repeat("long-model-alias-", 8))
	}
	channelCard := ChannelModelPricing{
		Platform: PlatformOpenAI, Models: models, BillingMode: BillingModeToken,
		InputPrice: engineCompactFloat(.01), OutputPrice: engineCompactFloat(.02), CacheReadPrice: engineCompactFloat(.001),
	}
	channel := Channel{ID: 77, Status: StatusActive, GroupIDs: []int64{groupID}, ModelPricing: []ChannelModelPricing{channelCard}}
	channels := &ChannelService{}
	channels.cache.Store(populateChannelCache([]Channel{channel}, map[int64]string{groupID: PlatformOpenAI}))
	cfg := &config.Config{}
	cfg.Default.RateMultiplier = 1
	svc := &OpenAIGatewayService{cfg: cfg, channelService: channels, billingService: &BillingService{fallbackPrices: map[string]*ModelPricing{}}}
	in := engineReceiptTestInput("")
	in.APIKey.GroupID = new(int64)
	*in.APIKey.GroupID = groupID
	in.Result.Model, in.Result.UpstreamModel, in.Result.BillingModel = models[0], models[0], models[0]
	in.OriginalModel, in.ChannelMappedModel = models[0], ""
	raw, err := svc.CaptureCodexEngineBillingSnapshot(context.Background(), in)
	require.NoError(t, err)
	require.LessOrEqual(t, len(raw), codexEnginePricingSnapshotLimit)
	frozen, err := decodeCodexEnginePricingSnapshot(raw)
	require.NoError(t, err)
	require.Empty(t, frozen.Prices)
	var wire map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &wire))
	require.Equal(t, 4, frozen.Version)
	for _, field := range []string{"prices", "price_refs", "price_cards", "token_prices", "search_price"} {
		require.NotContains(t, wire, field)
	}
	require.Less(t, len(raw), 4096)
}

func TestCodexEngineCompactPricingReadsLegacyVersions(t *testing.T) {
	for _, version := range []int{1, 2} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			job, frozen := engineFrozenTestSnapshot(t)
			frozen.Version = version
			var err error
			job.Snapshot, err = json.Marshal(frozen)
			require.NoError(t, err)
			decoded, err := decodeCodexEnginePricingSnapshot(job.Snapshot)
			require.NoError(t, err)
			require.Equal(t, version, decoded.Version)
			command, log, err := (&OpenAIGatewayService{}).BuildCodexEngineBillingEvent(context.Background(), job, engineFrozenTestModelEvent())
			require.NoError(t, err)
			require.InDelta(t, 10.2, log.TotalCost, 1e-12)
			require.InDelta(t, 20.4, command.BalanceCost, 1e-12)
		})
	}
}

func TestCodexEngineCompactPricingJSONBWhitespaceBoundary(t *testing.T) {
	_, frozen := engineFrozenTestSnapshot(t)
	frozen.Version, frozen.Prices, frozen.SearchPrice = 4, nil, 0
	frozen.UserAgent = "x"
	raw, err := marshalCodexEnginePricingSnapshot(&frozen)
	require.NoError(t, err)
	frozen.UserAgent = strings.Repeat("x", codexEnginePricingSnapshotLimit-len(raw))
	raw, err = marshalCodexEnginePricingSnapshot(&frozen)
	require.NoError(t, err)
	var formatted bytes.Buffer
	// JSONB adds spaces when rendering persisted objects. Extra formatting must
	// not make an otherwise bounded logical snapshot impossible to recover.
	require.NoError(t, json.Indent(&formatted, raw, "", " "))
	require.Greater(t, formatted.Len(), codexEnginePricingSnapshotLimit)
	decoded, err := decodeCodexEnginePricingSnapshot(formatted.Bytes())
	require.NoError(t, err)
	require.Equal(t, frozen.PricingAt, decoded.PricingAt)
	compact, err := marshalCodexEnginePricingSnapshot(decoded)
	require.NoError(t, err)
	require.LessOrEqual(t, len(compact), codexEnginePricingSnapshotLimit)
	// The decoder still rejects genuinely oversized data after whitespace is removed.
	frozen.UserAgent = strings.Repeat("x", codexEnginePricingSnapshotLimit)
	_, err = marshalCodexEnginePricingSnapshot(&frozen)
	require.Error(t, err)
}

func TestCodexEngineCompactPricingPreservesAdmittedQuotaMetadata(t *testing.T) {
	_, frozen := engineFrozenTestSnapshot(t)
	id := int64(99)
	frozen.Version, frozen.Prices, frozen.SearchPrice = 4, nil, 0
	frozen.SubscriptionID, frozen.SubscriptionBilling, frozen.TotalQuotaBilling = &id, true, true
	frozen.TotalQuotaSpendSnapshot = &codexEngineQuotaSpendSnapshot{SubscriptionID: id, EventIDs: []int64{10, 11}, OverflowEventID: 11, TakenAt: time.Date(2026, 11, 5, 1, 2, 3, 0, time.UTC)}
	raw, err := marshalCodexEnginePricingSnapshot(&frozen)
	require.NoError(t, err)
	decoded, err := decodeCodexEnginePricingSnapshot(raw)
	require.NoError(t, err)
	require.Equal(t, frozen.TotalQuotaSpendSnapshot, decoded.TotalQuotaSpendSnapshot)
	require.Equal(t, frozen.PricingAt, decoded.PricingAt)
	require.Equal(t, frozen.SubscriptionID, decoded.SubscriptionID)
}
