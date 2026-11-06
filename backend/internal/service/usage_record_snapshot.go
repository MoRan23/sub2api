package service

// SnapshotRecordUsageInput owns only the values needed by deferred billing.
// It intentionally performs no pricing lookup: live prices, user overrides and
// shadow-parent billing settings are still resolved by RecordUsage in the worker.
func SnapshotRecordUsageInput(in *RecordUsageInput) *RecordUsageInput {
	if in == nil {
		return nil
	}
	upstreamID := in.upstreamRequestID
	if upstreamID == nil && in.Result != nil {
		upstreamID = usageUpstreamRequestIDPtr(in.Account, in.Result.UpstreamHeaders, false)
	}
	owner := new(usageSnapshotOwner[RecordUsageInput, ForwardResult])
	var b usageSnapshotBuilder
	b.init(&owner.storage, usageSnapshotSubjectsStringBytes(in.APIKey, in.User, in.Account)+
		usageSnapshotForwardStringBytes(in.Result)+usageSnapshotStringBytes(in.InboundEndpoint, in.UpstreamEndpoint, in.UserAgent, in.IPAddress, in.SessionID, in.RequestPayloadHash, in.QuotaPlatform)+
		usageSnapshotChannelStringBytes(in.ChannelUsageFields)+usageSnapshotStringPointerBytes(upstreamID), usageSnapshotGroup(in.APIKey))
	out := &owner.input
	*out = RecordUsageInput{
		Result: b.forward(&owner.result, in.Result),
		User:   b.user(&owner.user, in.User), Account: b.account(&owner.account, in.Account),
		Subscription: b.subscription(&owner.subscription, in.Subscription), PricingAt: in.PricingAt,
		InboundEndpoint: b.string(in.InboundEndpoint), UpstreamEndpoint: b.string(in.UpstreamEndpoint),
		UserAgent: b.string(in.UserAgent), IPAddress: b.string(in.IPAddress), SessionID: b.string(in.SessionID),
		RequestPayloadHash: b.string(in.RequestPayloadHash), ForceCacheBilling: in.ForceCacheBilling,
		APIKeyService: in.APIKeyService, QuotaPlatform: b.string(in.QuotaPlatform),
		ChannelUsageFields: b.channel(in.ChannelUsageFields),
		upstreamRequestID:  b.stringPointer(upstreamID),
	}
	out.APIKey = b.apiKey(&owner.key, in.APIKey, in.User, out.User)
	return out
}

// SnapshotOpenAIRecordUsageInput also excludes all WebSocket replay buffers and
// response headers. The selected upstream request ID is extracted synchronously.
func SnapshotOpenAIRecordUsageInput(in *OpenAIRecordUsageInput) *OpenAIRecordUsageInput {
	if in == nil {
		return nil
	}
	upstreamID := in.upstreamRequestID
	if upstreamID == nil && in.Result != nil {
		upstreamID = usageUpstreamRequestIDPtr(in.Account, in.Result.UpstreamHeaders, in.Result.OpenAIWSMode)
	}
	owner := new(usageSnapshotOwner[OpenAIRecordUsageInput, OpenAIForwardResult])
	var b usageSnapshotBuilder
	b.init(&owner.storage, usageSnapshotSubjectsStringBytes(in.APIKey, in.User, in.Account)+
		usageSnapshotOpenAIStringBytes(in.Result)+usageSnapshotStringBytes(in.InboundEndpoint, in.UpstreamEndpoint, in.UserAgent, in.IPAddress, in.SessionID, in.RequestPayloadHash, in.QuotaPlatform)+
		usageSnapshotChannelStringBytes(in.ChannelUsageFields)+usageSnapshotStringPointerBytes(upstreamID), usageSnapshotGroup(in.APIKey))
	out := &owner.input
	*out = OpenAIRecordUsageInput{
		Result: b.openAIResult(&owner.result, in.Result),
		User:   b.user(&owner.user, in.User), Account: b.account(&owner.account, in.Account),
		Subscription: b.subscription(&owner.subscription, in.Subscription), PricingAt: in.PricingAt,
		InboundEndpoint: b.string(in.InboundEndpoint), UpstreamEndpoint: b.string(in.UpstreamEndpoint),
		UserAgent: b.string(in.UserAgent), IPAddress: b.string(in.IPAddress), SessionID: b.string(in.SessionID),
		RequestPayloadHash: b.string(in.RequestPayloadHash), APIKeyService: in.APIKeyService,
		QuotaPlatform: b.string(in.QuotaPlatform), CyberBlocked: in.CyberBlocked, NativeCompactionV2: in.NativeCompactionV2,
		ChannelUsageFields: b.channel(in.ChannelUsageFields),
		upstreamRequestID:  b.stringPointer(upstreamID),
	}
	out.APIKey = b.apiKey(&owner.key, in.APIKey, in.User, out.User)
	return out
}

func (b *usageSnapshotBuilder) forward(out *ForwardResult, r *ForwardResult) *ForwardResult {
	if r == nil {
		return nil
	}
	*out = ForwardResult{
		RequestID: b.string(r.RequestID), Usage: r.Usage, Model: b.string(r.Model),
		UpstreamModel: b.string(r.UpstreamModel), UpstreamResponseModel: b.string(r.UpstreamResponseModel),
		UpstreamResponseModelConflict: r.UpstreamResponseModelConflict, UpstreamResponseServiceTier: b.string(r.UpstreamResponseServiceTier),
		Stream: r.Stream, Duration: r.Duration, FirstTokenMs: b.int(r.FirstTokenMs), ClientDisconnect: r.ClientDisconnect,
		ReasoningEffort: b.stringPointer(r.ReasoningEffort), RequestedReasoningEffort: b.stringPointer(r.RequestedReasoningEffort), ServiceTier: b.stringPointer(r.ServiceTier),
		ImageCount: r.ImageCount, ImageEndpoint: r.ImageEndpoint, ImageSize: b.string(r.ImageSize),
		ImageInputSize: b.string(r.ImageInputSize), ImageOutputSize: b.string(r.ImageOutputSize),
		ImageOutputSizes: b.strings(r.ImageOutputSizes), ImageSizeSource: b.string(r.ImageSizeSource),
		ImageSizeBreakdown: b.intMap(r.ImageSizeBreakdown), SearchCount: r.SearchCount,
		AudioUsage: b.audio(r.AudioUsage), CacheStorageTokenHours: r.CacheStorageTokenHours,
	}
	return out
}

func (b *usageSnapshotBuilder) openAIResult(out *OpenAIForwardResult, r *OpenAIForwardResult) *OpenAIForwardResult {
	if r == nil {
		return nil
	}
	*out = OpenAIForwardResult{
		RequestID: b.string(r.RequestID), ResponseID: b.string(r.ResponseID), Usage: r.Usage,
		Model: b.string(r.Model), BillingModel: b.string(r.BillingModel), UpstreamModel: b.string(r.UpstreamModel),
		UpstreamResponseModel: b.string(r.UpstreamResponseModel), UpstreamResponseModelConflict: r.UpstreamResponseModelConflict,
		UpstreamResponseServiceTier: b.string(r.UpstreamResponseServiceTier), UpstreamEndpoint: b.string(r.UpstreamEndpoint),
		ServiceTier: b.stringPointer(r.ServiceTier), ReasoningEffort: b.stringPointer(r.ReasoningEffort), RequestedReasoningEffort: b.stringPointer(r.RequestedReasoningEffort),
		Stream: r.Stream, OpenAIWSMode: r.OpenAIWSMode, UpstreamTerminalEvent: b.string(r.UpstreamTerminalEvent),
		Duration: r.Duration, FirstTokenMs: b.int(r.FirstTokenMs), ClientDisconnect: r.ClientDisconnect,
		ImageCount: r.ImageCount, ImageEndpoint: r.ImageEndpoint, ImageSize: b.string(r.ImageSize),
		ImageInputSize: b.string(r.ImageInputSize), ImageOutputSize: b.string(r.ImageOutputSize),
		ImageOutputSizes: b.strings(r.ImageOutputSizes), ImageSizeSource: b.string(r.ImageSizeSource),
		ImageSizeBreakdown: b.intMap(r.ImageSizeBreakdown), VideoCount: r.VideoCount,
		VideoResolution: b.string(r.VideoResolution), VideoDurationSeconds: r.VideoDurationSeconds,
		WebSearchCalls: r.WebSearchCalls, SearchCount: r.SearchCount, AudioUsage: b.audio(r.AudioUsage),
	}
	return out
}

func (b *usageSnapshotBuilder) apiKey(out *APIKey, k *APIKey, sourceUser, ownedUser *User) *APIKey {
	if k == nil {
		return nil
	}
	user := ownedUser
	if k.User != sourceUser && k.User != nil {
		user = b.user(new(User), k.User)
	} else if k.User == nil {
		user = nil
	}
	*out = APIKey{ID: k.ID, UserID: k.UserID, Key: b.string(k.Key), GroupID: b.int64(k.GroupID),
		Quota: k.Quota, RateLimit5h: k.RateLimit5h, RateLimit1d: k.RateLimit1d, RateLimit7d: k.RateLimit7d,
		User: user, Group: b.group(k.Group)}
	return out
}

func (b *usageSnapshotBuilder) user(out *User, u *User) *User {
	if u == nil {
		return nil
	}
	*out = User{ID: u.ID, Email: b.string(u.Email), Username: b.string(u.Username), Balance: u.Balance, GiftBalance: u.GiftBalance,
		BalanceNotifyEnabled: u.BalanceNotifyEnabled, BalanceNotifyThresholdType: b.string(u.BalanceNotifyThresholdType),
		BalanceNotifyThreshold: b.float(u.BalanceNotifyThreshold), TotalRecharged: u.TotalRecharged}
	if u.BalanceNotifyExtraEmails != nil {
		out.BalanceNotifyExtraEmails = make([]NotifyEmailEntry, len(u.BalanceNotifyExtraEmails))
		for i, e := range u.BalanceNotifyExtraEmails {
			e.Email = b.string(e.Email)
			out.BalanceNotifyExtraEmails[i] = e
		}
	}
	return out
}

func (b *usageSnapshotBuilder) subscription(out *UserSubscription, s *UserSubscription) *UserSubscription {
	if s == nil {
		return nil
	}
	*out = UserSubscription{ID: s.ID, UserID: s.UserID, GroupID: s.GroupID}
	return out
}

// Kept as a focused projection for package-level equivalence tests.
func snapshotUsageAccount(a *Account) *Account {
	return SnapshotRecordUsageInput(&RecordUsageInput{Account: a}).Account
}

func (b *usageSnapshotBuilder) account(out *Account, a *Account) *Account {
	if a == nil {
		return nil
	}
	*out = Account{ID: a.ID, Name: b.string(a.Name), Platform: b.string(a.Platform), Type: b.string(a.Type),
		RateMultiplier: b.float(a.RateMultiplier), ParentAccountID: b.int64(a.ParentAccountID), QuotaDimension: b.string(a.QuotaDimension),
		OpenAIOAuthCredentialOS: b.string(a.OpenAIOAuthCredentialOS), OpenAIOAuthCredentialOwnerID: a.OpenAIOAuthCredentialOwnerID,
		OpenAIOAuthAuthorizationGeneration: b.string(a.OpenAIOAuthAuthorizationGeneration), OpenAIOAuthCredentialRevision: a.OpenAIOAuthCredentialRevision,
		OpenAIOAuthCredentialStateGeneration: b.string(a.OpenAIOAuthCredentialStateGeneration), OpenAIOAuthCredentialEpoch: b.string(a.OpenAIOAuthCredentialEpoch)}
	// Billing never needs tokens, proxy settings, scheduling state or OS profiles.
	// Keep the physical request's provider plan, while shadow billing still reads
	// current parent settings through resolveOpenAIUsageBillingAccount in the worker.
	out.usageBillingValues = b.accountValues(a)
	return out
}

func (b *usageSnapshotBuilder) group(g *Group) *Group {
	if g == nil {
		return nil
	}
	if frozen := frozenUsageBillingGroup(g); frozen != nil {
		return frozen
	}
	// Auth-published groups reuse their independent billing projection. Keep the
	// larger fallback Group outside the common owner so those requests do not
	// allocate or clear an unused Group on each submission.
	out := new(Group)
	*out = Group{ID: g.ID, Platform: b.string(g.Platform), SubscriptionType: b.string(g.SubscriptionType), Hydrated: g.Hydrated,
		RateMultiplier: g.RateMultiplier, PeakRateEnabled: g.PeakRateEnabled, PeakStart: b.string(g.PeakStart), PeakEnd: b.string(g.PeakEnd), PeakRateMultiplier: g.PeakRateMultiplier,
		ImageCountBilling: g.ImageCountBilling, ImageRateIndependent: g.ImageRateIndependent, ImageRateMultiplier: g.ImageRateMultiplier,
		ImagePrice1K: b.float(g.ImagePrice1K), ImagePrice2K: b.float(g.ImagePrice2K), ImagePrice4K: b.float(g.ImagePrice4K),
		VideoRateIndependent: g.VideoRateIndependent, VideoRateMultiplier: g.VideoRateMultiplier,
		VideoPrice480P: b.float(g.VideoPrice480P), VideoPrice720P: b.float(g.VideoPrice720P), VideoPrice1080P: b.float(g.VideoPrice1080P),
		WebSearchPricePerCall: b.float(g.WebSearchPricePerCall), SearchPricePer1k: b.float(g.SearchPricePer1k),
		AudioRealtimePricePerMin: b.float(g.AudioRealtimePricePerMin), AudioTTSPricePerMillionChars: b.float(g.AudioTTSPricePerMillionChars), AudioSTTPricePerHour: b.float(g.AudioSTTPricePerHour),
		LongContextPricingEnabled: g.LongContextPricingEnabled, FreeOpenAIFast: g.FreeOpenAIFast}
	if g.VideoModelPrices != nil {
		out.VideoModelPrices = make(map[string]map[string]float64, len(g.VideoModelPrices))
		for model, prices := range g.VideoModelPrices {
			out.VideoModelPrices[b.string(model)] = b.floatMap(prices)
		}
	}
	if g.ModelPricing != nil {
		out.ModelPricing = make([]ChannelModelPricing, len(g.ModelPricing))
		for i, price := range g.ModelPricing {
			out.ModelPricing[i] = b.modelPricing(price)
		}
	}
	return out
}

func (b *usageSnapshotBuilder) modelPricing(p ChannelModelPricing) ChannelModelPricing {
	p.Platform = b.string(p.Platform)
	p.BillingMode = BillingMode(b.string(string(p.BillingMode)))
	p.Models = b.strings(p.Models)
	p.InputPrice, p.OutputPrice = b.float(p.InputPrice), b.float(p.OutputPrice)
	p.CacheWritePrice, p.CacheWrite1hPrice, p.CacheReadPrice = b.float(p.CacheWritePrice), b.float(p.CacheWrite1hPrice), b.float(p.CacheReadPrice)
	p.FastMultiplier, p.FlexMultiplier = b.float(p.FastMultiplier), b.float(p.FlexMultiplier)
	p.ImageInputPrice, p.ImageOutputPrice, p.PerRequestPrice = b.float(p.ImageInputPrice), b.float(p.ImageOutputPrice), b.float(p.PerRequestPrice)
	p.ReasoningEffortMultipliers = b.floatMap(p.ReasoningEffortMultipliers)
	if p.Intervals != nil {
		intervals := make([]PricingInterval, len(p.Intervals))
		for i, v := range p.Intervals {
			v.MaxTokens, v.TierLabel = b.int(v.MaxTokens), b.string(v.TierLabel)
			v.InputPrice, v.OutputPrice = b.float(v.InputPrice), b.float(v.OutputPrice)
			v.CacheWritePrice, v.CacheWrite1hPrice, v.CacheReadPrice = b.float(v.CacheWritePrice), b.float(v.CacheWrite1hPrice), b.float(v.CacheReadPrice)
			v.InputMultiplier, v.OutputMultiplier = b.float(v.InputMultiplier), b.float(v.OutputMultiplier)
			v.CacheWriteMultiplier, v.CacheReadMultiplier, v.PerRequestPrice = b.float(v.CacheWriteMultiplier), b.float(v.CacheReadMultiplier), b.float(v.PerRequestPrice)
			intervals[i] = v
		}
		p.Intervals = intervals
	}
	if p.TimePricing != nil {
		var t *ChannelTimePricing
		if b.timeIndex < len(b.times) {
			t = &b.times[b.timeIndex]
		} else {
			t = new(ChannelTimePricing)
		}
		b.timeIndex++
		*t = *p.TimePricing
		t.Timezone = b.string(t.Timezone)
		t.Periods = append([]ChannelTimePricingPeriod(nil), t.Periods...)
		for i := range t.Periods {
			t.Periods[i].StartTime = b.string(t.Periods[i].StartTime)
			t.Periods[i].EndTime = b.string(t.Periods[i].EndTime)
		}
		p.TimePricing = t
	}
	return p
}

func (b *usageSnapshotBuilder) channel(f ChannelUsageFields) ChannelUsageFields {
	return ChannelUsageFields{ChannelID: f.ChannelID, OriginalModel: b.string(f.OriginalModel), ChannelMappedModel: b.string(f.ChannelMappedModel), BillingModelSource: b.string(f.BillingModelSource), ModelMappingChain: b.string(f.ModelMappingChain)}
}

func (b *usageSnapshotBuilder) strings(in []string) []string {
	if in == nil {
		return nil
	}
	out := make([]string, len(in))
	for i, v := range in {
		out[i] = b.string(v)
	}
	return out
}
func (b *usageSnapshotBuilder) intMap(in map[string]int) map[string]int {
	if in == nil {
		return nil
	}
	out := make(map[string]int, len(in))
	for k, v := range in {
		out[b.string(k)] = v
	}
	return out
}
func (b *usageSnapshotBuilder) floatMap(in map[string]float64) map[string]float64 {
	if in == nil {
		return nil
	}
	out := make(map[string]float64, len(in))
	for k, v := range in {
		out[b.string(k)] = v
	}
	return out
}
func (b *usageSnapshotBuilder) audio(in *AudioUsage) *AudioUsage {
	if in == nil {
		return nil
	}
	b.storage.audio = AudioUsage{Mode: b.string(in.Mode), DurationOrUnits: in.DurationOrUnits}
	return &b.storage.audio
}

func usageRecordUpstreamRequestID(frozen *string, fallback func() *string) *string {
	if frozen != nil {
		return frozen
	}
	return fallback()
}
