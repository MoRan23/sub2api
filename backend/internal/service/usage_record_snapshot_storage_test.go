package service

import (
	"strings"
	"testing"
	"unsafe"

	"github.com/stretchr/testify/require"
)

func TestUsageRecordSnapshotStringArenaGrowthPreservesEarlierStrings(t *testing.T) {
	var storage usageSnapshotStorage
	var builder usageSnapshotBuilder
	builder.init(&storage, 4, nil)
	source := strings.Repeat("request payload", 4096) + "first"
	firstSource := source[len(source)-5:]
	first := builder.string(firstSource)
	secondSource := strings.Repeat("second", 10000)
	second := builder.string(secondSource)
	third := builder.string("third")
	require.Equal(t, "first", first)
	require.Equal(t, secondSource, second)
	require.Equal(t, "third", third)
	require.False(t, unsafe.StringData(first) == unsafe.StringData(firstSource))
	require.False(t, unsafe.StringData(second) == unsafe.StringData(secondSource))
}

func TestUsageRecordSnapshotScalarBackingExhaustionKeepsValues(t *testing.T) {
	var storage usageSnapshotStorage
	var builder usageSnapshotBuilder
	builder.init(&storage, 0, nil)
	var floats []*float64
	var ints []*int
	var int64s []*int64
	var texts []*string
	for i := 0; i < 40; i++ {
		f, n, n64, s := float64(i), i, int64(i), strings.Repeat("x", i+1)
		floats = append(floats, builder.float(&f))
		ints = append(ints, builder.int(&n))
		int64s = append(int64s, builder.int64(&n64))
		texts = append(texts, builder.stringPointer(&s))
		f, n, n64, s = -1, -1, -1, "changed"
	}
	for i := range floats {
		require.Equal(t, float64(i), *floats[i])
		require.Equal(t, i, *ints[i])
		require.Equal(t, int64(i), *int64s[i])
		require.Equal(t, strings.Repeat("x", i+1), *texts[i])
	}
}

func TestUsageRecordSnapshotLargePricingBackingOwnsAllValues(t *testing.T) {
	key, user, account, subscription := usageSnapshotTestSubjects()
	value, maxTokens, groupID, firstToken := 1.25, 10000, int64(7), 9
	user.BalanceNotifyThreshold = &value
	key.User = &User{ID: 123, Email: "separate@example.com", BalanceNotifyThreshold: &value}
	account.RateMultiplier, account.ParentAccountID = &value, &groupID
	key.GroupID = &groupID
	g := key.Group
	g.ImagePrice1K, g.ImagePrice2K, g.ImagePrice4K = &value, &value, &value
	g.VideoPrice480P, g.VideoPrice720P, g.VideoPrice1080P = &value, &value, &value
	g.SearchPricePer1k, g.WebSearchPricePerCall = &value, &value
	g.AudioRealtimePricePerMin, g.AudioTTSPricePerMillionChars, g.AudioSTTPricePerHour = &value, &value, &value
	price := ChannelModelPricing{
		Models: []string{"gpt-4o"}, InputPrice: &value, OutputPrice: &value,
		CacheWritePrice: &value, CacheWrite1hPrice: &value, CacheWrite12hPrice: &value, CacheReadPrice: &value,
		FastMultiplier: &value, FlexMultiplier: &value, ImageInputPrice: &value, ImageOutputPrice: &value, PerRequestPrice: &value,
		Intervals: []PricingInterval{{MaxTokens: &maxTokens, TierLabel: "one",
			InputPrice: &value, OutputPrice: &value, CacheWritePrice: &value, CacheWrite1hPrice: &value, CacheWrite12hPrice: &value, CacheReadPrice: &value,
			InputMultiplier: &value, OutputMultiplier: &value, CacheWriteMultiplier: &value, CacheReadMultiplier: &value, PerRequestPrice: &value}},
		TimePricing: &ChannelTimePricing{Timezone: "UTC", Periods: []ChannelTimePricingPeriod{{StartTime: "00:00", EndTime: "23:59", Multiplier: 1.1}}},
	}
	g.ModelPricing = make([]ChannelModelPricing, 8)
	for i := range g.ModelPricing {
		g.ModelPricing[i] = price
	}
	tier, effort, requested, upstream := "priority", "high", "medium", "upstream-id"
	result := &OpenAIForwardResult{Model: "gpt-4o", ServiceTier: &tier, ReasoningEffort: &effort, RequestedReasoningEffort: &requested,
		FirstTokenMs: &firstToken, AudioUsage: &AudioUsage{Mode: "tts", DurationOrUnits: 2}}
	got := SnapshotOpenAIRecordUsageInput(&OpenAIRecordUsageInput{
		Result: result, APIKey: key, User: user, Account: account, Subscription: subscription, upstreamRequestID: &upstream,
	})
	require.NotSame(t, got.User, got.APIKey.User)
	for i := range g.ModelPricing {
		require.Equal(t, g.ModelPricing[i], got.APIKey.Group.ModelPricing[i])
	}
	value, maxTokens, groupID, firstToken = 999, 999, 999, 999
	tier, effort, requested, upstream = "changed", "changed", "changed", "changed"
	price.TimePricing.Periods[0].StartTime = "12:00"
	result.AudioUsage.Mode = "changed"
	require.Equal(t, 1.25, *got.User.BalanceNotifyThreshold)
	require.Equal(t, 1.25, *got.APIKey.User.BalanceNotifyThreshold)
	require.Equal(t, 1.25, *got.Account.RateMultiplier)
	require.EqualValues(t, 7, *got.Account.ParentAccountID)
	require.EqualValues(t, 7, *got.APIKey.GroupID)
	require.Equal(t, 9, *got.Result.FirstTokenMs)
	require.Equal(t, "priority", *got.Result.ServiceTier)
	require.Equal(t, "high", *got.Result.ReasoningEffort)
	require.Equal(t, "medium", *got.Result.RequestedReasoningEffort)
	require.Equal(t, "upstream-id", *got.upstreamRequestID)
	require.Equal(t, "tts", got.Result.AudioUsage.Mode)
	for _, p := range got.APIKey.Group.ModelPricing {
		for _, pointer := range []*float64{p.InputPrice, p.OutputPrice, p.CacheWritePrice, p.CacheWrite1hPrice, p.CacheWrite12hPrice, p.CacheReadPrice,
			p.FastMultiplier, p.FlexMultiplier, p.ImageInputPrice, p.ImageOutputPrice, p.PerRequestPrice} {
			require.Equal(t, 1.25, *pointer)
		}
		for _, interval := range p.Intervals {
			require.Equal(t, 10000, *interval.MaxTokens)
			for _, pointer := range []*float64{interval.InputPrice, interval.OutputPrice, interval.CacheWritePrice, interval.CacheWrite1hPrice, interval.CacheWrite12hPrice,
				interval.CacheReadPrice, interval.InputMultiplier, interval.OutputMultiplier, interval.CacheWriteMultiplier, interval.CacheReadMultiplier, interval.PerRequestPrice} {
				require.Equal(t, 1.25, *pointer)
			}
		}
		require.Equal(t, "00:00", p.TimePricing.Periods[0].StartTime)
	}
}
