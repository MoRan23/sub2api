package service

import "strings"

// Interior pointers keep this single billing-only owner alive. No field points
// back to a request object; maps, slices and the text arena are owned separately.
type usageSnapshotOwner[I, R any] struct {
	input        I
	result       R
	key          APIKey
	user         User
	account      Account
	subscription UserSubscription
	storage      usageSnapshotStorage
}

type usageSnapshotStorage struct {
	floats  [24]float64
	ints    [4]int
	int64s  [2]int64
	strings [4]string
	audio   AudioUsage
	times   [1]ChannelTimePricing
	account usageAccountBillingValues
}

type usageSnapshotBuilder struct {
	text        strings.Builder
	storage     *usageSnapshotStorage
	floats      []float64
	ints        []int
	times       []ChannelTimePricing
	floatIndex  int
	intIndex    int
	int64Index  int
	stringIndex int
	timeIndex   int
}

func (b *usageSnapshotBuilder) init(storage *usageSnapshotStorage, stringBytes int, group *Group) {
	b.storage = storage
	b.floats, b.ints, b.times = storage.floats[:], storage.ints[:], storage.times[:]
	// The fixed portion covers Account, up to two Users, Group and Result.
	floatCount, intCount, timeCount := 15, 1, 0
	if group != nil && frozenUsageBillingGroup(group) == nil {
		for _, p := range group.ModelPricing {
			floatCount += usageSnapshotFloatPointerCount(p.InputPrice, p.OutputPrice, p.CacheWritePrice, p.CacheWrite1hPrice, p.CacheWrite12hPrice,
				p.CacheReadPrice, p.FastMultiplier, p.FlexMultiplier, p.ImageInputPrice, p.ImageOutputPrice, p.PerRequestPrice)
			for _, v := range p.Intervals {
				floatCount += usageSnapshotFloatPointerCount(v.InputPrice, v.OutputPrice, v.CacheWritePrice, v.CacheWrite1hPrice, v.CacheWrite12hPrice,
					v.CacheReadPrice, v.InputMultiplier, v.OutputMultiplier, v.CacheWriteMultiplier, v.CacheReadMultiplier, v.PerRequestPrice)
				if v.MaxTokens != nil {
					intCount++
				}
			}
			if p.TimePricing != nil {
				timeCount++
			}
		}
	}
	if floatCount > len(b.floats) {
		b.floats = make([]float64, floatCount)
	}
	if intCount > len(b.ints) {
		b.ints = make([]int, intCount)
	}
	if timeCount > len(b.times) {
		b.times = make([]ChannelTimePricing, timeCount)
	}
	if stringBytes > 0 {
		b.text.Grow(stringBytes)
	}
}

func (b *usageSnapshotBuilder) string(value string) string {
	if value == "" {
		return ""
	}
	start := b.text.Len()
	b.text.WriteString(value)
	return b.text.String()[start:b.text.Len()]
}

func (b *usageSnapshotBuilder) float(p *float64) *float64 {
	if p == nil {
		return nil
	}
	i := b.floatIndex
	b.floatIndex++
	if i >= len(b.floats) {
		return usageSnapshotScalar(p)
	}
	b.floats[i] = *p
	return &b.floats[i]
}

func (b *usageSnapshotBuilder) int(p *int) *int {
	if p == nil {
		return nil
	}
	i := b.intIndex
	b.intIndex++
	if i >= len(b.ints) {
		return usageSnapshotScalar(p)
	}
	b.ints[i] = *p
	return &b.ints[i]
}

func (b *usageSnapshotBuilder) int64(p *int64) *int64 {
	if p == nil {
		return nil
	}
	i := b.int64Index
	b.int64Index++
	if i >= len(b.storage.int64s) {
		return usageSnapshotScalar(p)
	}
	b.storage.int64s[i] = *p
	return &b.storage.int64s[i]
}

func (b *usageSnapshotBuilder) stringPointer(p *string) *string {
	if p == nil {
		return nil
	}
	i := b.stringIndex
	b.stringIndex++
	if i >= len(b.storage.strings) {
		value := b.string(*p)
		return &value
	}
	b.storage.strings[i] = b.string(*p)
	return &b.storage.strings[i]
}

func usageSnapshotScalar[T any](p *T) *T {
	value := *p
	return &value
}

func usageSnapshotFloatPointerCount(values ...*float64) int {
	count := 0
	for _, value := range values {
		if value != nil {
			count++
		}
	}
	return count
}

func usageSnapshotStringBytes(values ...string) int {
	n := 0
	for _, value := range values {
		n += len(value)
	}
	return n
}

func usageSnapshotStringPointerBytes(p *string) int {
	if p == nil {
		return 0
	}
	return len(*p)
}

func usageSnapshotGroup(key *APIKey) *Group {
	if key == nil {
		return nil
	}
	return key.Group
}

func usageSnapshotUserStringBytes(user *User) int {
	if user == nil {
		return 0
	}
	n := usageSnapshotStringBytes(user.Email, user.Username, user.BalanceNotifyThresholdType)
	for _, email := range user.BalanceNotifyExtraEmails {
		n += len(email.Email)
	}
	return n
}

func usageSnapshotSubjectsStringBytes(key *APIKey, user *User, account *Account) int {
	n := usageSnapshotUserStringBytes(user)
	if account != nil {
		n += usageSnapshotStringBytes(account.Name, account.Platform, account.Type, account.QuotaDimension, account.OpenAIOAuthCredentialOS,
			account.OpenAIOAuthAuthorizationGeneration, account.OpenAIOAuthCredentialStateGeneration, account.OpenAIOAuthCredentialEpoch,
			account.GetCredential("plan_type"), account.GetExtraString(OpenAIAPIKeyModeExtraKey),
			account.getExtraString("quota_notify_daily_threshold_type"), account.getExtraString("quota_notify_weekly_threshold_type"), account.getExtraString("quota_notify_total_threshold_type"))
	}
	if key == nil {
		return n
	}
	n += len(key.Key)
	if key.User != user {
		n += usageSnapshotUserStringBytes(key.User)
	}
	g := key.Group
	if g == nil || frozenUsageBillingGroup(g) != nil {
		return n
	}
	n += usageSnapshotStringBytes(g.Platform, g.SubscriptionType, g.PeakStart, g.PeakEnd)
	for model, prices := range g.VideoModelPrices {
		n += len(model)
		for resolution := range prices {
			n += len(resolution)
		}
	}
	for _, price := range g.ModelPricing {
		n += usageSnapshotStringBytes(price.Platform, string(price.BillingMode))
		for _, model := range price.Models {
			n += len(model)
		}
		for effort := range price.ReasoningEffortMultipliers {
			n += len(effort)
		}
		for _, interval := range price.Intervals {
			n += len(interval.TierLabel)
		}
		if price.TimePricing != nil {
			n += len(price.TimePricing.Timezone)
			for _, period := range price.TimePricing.Periods {
				n += len(period.StartTime) + len(period.EndTime)
			}
		}
	}
	return n
}

func usageSnapshotMediaStringBytes(outputSizes []string, breakdown map[string]int, audio *AudioUsage) int {
	n := 0
	for _, size := range outputSizes {
		n += len(size)
	}
	for size := range breakdown {
		n += len(size)
	}
	if audio != nil {
		n += len(audio.Mode)
	}
	return n
}

func usageSnapshotForwardStringBytes(r *ForwardResult) int {
	if r == nil {
		return 0
	}
	return usageSnapshotStringBytes(r.RequestID, r.Model, r.UpstreamModel, r.UpstreamResponseModel, r.UpstreamResponseServiceTier,
		r.ImageSize, r.ImageInputSize, r.ImageOutputSize, r.ImageSizeSource) +
		usageSnapshotStringPointerBytes(r.ReasoningEffort) + usageSnapshotStringPointerBytes(r.RequestedReasoningEffort) + usageSnapshotStringPointerBytes(r.ServiceTier) +
		usageSnapshotMediaStringBytes(r.ImageOutputSizes, r.ImageSizeBreakdown, r.AudioUsage)
}

func usageSnapshotOpenAIStringBytes(r *OpenAIForwardResult) int {
	if r == nil {
		return 0
	}
	n := 0
	if rejection := r.GrokSafetyRejection; rejection != nil {
		n = usageSnapshotStringBytes(rejection.CallID, rejection.Code, rejection.UpstreamRequestID, rejection.UpstreamClientRequestID)
	}
	return n + usageSnapshotStringBytes(r.RequestID, r.ResponseID, r.Model, r.BillingModel, r.UpstreamModel, r.UpstreamResponseModel,
		r.UpstreamResponseServiceTier, r.UpstreamEndpoint, r.UpstreamTerminalEvent, r.ImageSize, r.ImageInputSize, r.ImageOutputSize, r.ImageSizeSource, r.VideoResolution) +
		usageSnapshotStringPointerBytes(r.ReasoningEffort) + usageSnapshotStringPointerBytes(r.RequestedReasoningEffort) + usageSnapshotStringPointerBytes(r.ServiceTier) +
		usageSnapshotMediaStringBytes(r.ImageOutputSizes, r.ImageSizeBreakdown, r.AudioUsage)
}

func usageSnapshotChannelStringBytes(f ChannelUsageFields) int {
	return usageSnapshotStringBytes(f.OriginalModel, f.ChannelMappedModel, f.BillingModelSource, f.ModelMappingChain)
}
