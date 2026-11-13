package service

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

const codexEnginePricingSnapshotVersion = 4
const codexEnginePricingModelLimit = 512
const codexEnginePricingSnapshotLimit = 256 * 1024

// Only bounded numeric diagnostics leave the snapshot builder; prices and
// request metadata must never be included in an error event.
type codexEngineBillingSnapshotError struct {
	Reason     string
	SizeBytes  int
	ModelCount int
	LimitBytes int
}

func (e *codexEngineBillingSnapshotError) Error() string {
	if e.Reason == "billing_snapshot_model_limit" {
		return fmt.Sprintf("engine applicable pricing catalog exceeds snapshot limit (models=%d, limit=%d)", e.ModelCount, codexEnginePricingModelLimit)
	}
	return fmt.Sprintf("engine applicable pricing snapshot exceeds storage limit (bytes=%d, limit=%d, models=%d)", e.SizeBytes, e.LimitBytes, e.ModelCount)
}

// SetCodexEngineBillingAPIKeyService supplies cache invalidation only. Recovered
// settlement must never reload prices or billing ownership through this service.
func (s *OpenAIGatewayService) SetCodexEngineBillingAPIKeyService(keys *APIKeyService) {
	if s != nil {
		s.engineBillingAPIKeys = keys
	}
}

// The snapshot is deliberately an allowlist, not a serialization of an Account,
// APIKey, Group or User: none of their credentials belongs in a recovery job.
type codexEnginePricingSnapshot struct {
	Version                  int                               `json:"version"`
	PricingAt                time.Time                         `json:"pricing_at"`
	RequestedModel           string                            `json:"requested_model"`
	UpstreamModel            string                            `json:"upstream_model"`
	BillingModel             string                            `json:"billing_model"`
	RequestedTier            string                            `json:"requested_tier,omitempty"`
	ReasoningEffort          string                            `json:"reasoning_effort,omitempty"`
	RequestedReasoningEffort string                            `json:"requested_reasoning_effort,omitempty"`
	OpenAIWSMode             bool                              `json:"openai_ws_mode"`
	RequestType              RequestType                       `json:"request_type"`
	UserAgent                string                            `json:"user_agent,omitempty"`
	IPAddress                string                            `json:"ip_address,omitempty"`
	SessionID                string                            `json:"session_id,omitempty"`
	RequestPayloadHash       string                            `json:"request_payload_hash,omitempty"`
	InboundEndpoint          string                            `json:"inbound_endpoint,omitempty"`
	UpstreamEndpoint         string                            `json:"upstream_endpoint,omitempty"`
	GroupID                  *int64                            `json:"group_id,omitempty"`
	SubscriptionID           *int64                            `json:"subscription_id,omitempty"`
	TotalQuotaBilling        bool                              `json:"total_quota_billing,omitempty"`
	TotalQuotaSpendSnapshot  *codexEngineQuotaSpendSnapshot    `json:"total_quota_spend_snapshot,omitempty"`
	ChannelID                int64                             `json:"channel_id,omitempty"`
	ModelMappingChain        string                            `json:"model_mapping_chain,omitempty"`
	QuotaPlatform            string                            `json:"quota_platform,omitempty"`
	AccountType              string                            `json:"account_type"`
	Stream                   bool                              `json:"stream"`
	NativeCompactionV2       bool                              `json:"native_compaction_v2"`
	SubscriptionBilling      bool                              `json:"subscription_billing"`
	SimpleMode               bool                              `json:"simple_mode"`
	SimpleKeyRateOnly        bool                              `json:"simple_key_rate_only"`
	KeyQuota                 bool                              `json:"key_quota"`
	KeyRateLimit             bool                              `json:"key_rate_limit"`
	AccountQuota             bool                              `json:"account_quota"`
	AccountDailyQuota        bool                              `json:"account_daily_quota"`
	AccountWeeklyQuota       bool                              `json:"account_weekly_quota"`
	AccountRate              float64                           `json:"account_rate"`
	TokenRate                float64                           `json:"token_rate"`
	ImageRate                float64                           `json:"image_rate"`
	BaseRate                 float64                           `json:"base_rate"`
	SearchPrice              float64                           `json:"search_price,omitempty"`
	FreeFast                 bool                              `json:"free_fast"`
	FreeUltrafast            bool                              `json:"free_ultrafast"`
	LongContext              bool                              `json:"long_context"`
	GroupLongContextPricing  bool                              `json:"group_long_context_pricing,omitempty"`
	ImageCountBilling        bool                              `json:"image_count_billing,omitempty"`
	ImageEndpoint            bool                              `json:"image_endpoint,omitempty"`
	Prices                   map[string]codexEngineFrozenPrice `json:"prices,omitempty"`
}

// Persist the admitted batches, not a current balance. Recovery may occur after
// their expiry or a new grant, and must still settle against the original grant.
type codexEngineQuotaSpendSnapshot struct {
	SubscriptionID  int64     `json:"subscription_id"`
	EventIDs        []int64   `json:"event_ids"`
	OverflowEventID int64     `json:"overflow_event_id"`
	TakenAt         time.Time `json:"taken_at"`
}

func (f *codexEnginePricingSnapshot) validateTotalQuotaSpendSnapshot() error {
	q := f.TotalQuotaSpendSnapshot
	if !f.TotalQuotaBilling {
		if q != nil {
			return errors.New("unexpected engine quota spend snapshot")
		}
		return nil
	}
	if !f.SubscriptionBilling || f.SubscriptionID == nil || q == nil || q.SubscriptionID <= 0 ||
		q.SubscriptionID != *f.SubscriptionID || len(q.EventIDs) == 0 || q.TakenAt.IsZero() {
		return errors.New("engine admitted quota spend snapshot unavailable")
	}
	seen := make(map[int64]struct{}, len(q.EventIDs))
	for _, id := range q.EventIDs {
		if id <= 0 {
			return errors.New("invalid engine quota event ID")
		}
		if _, exists := seen[id]; exists {
			return errors.New("duplicate engine quota event ID")
		}
		seen[id] = struct{}{}
	}
	if _, exists := seen[q.OverflowEventID]; !exists {
		return errors.New("engine quota overflow event was not admitted")
	}
	return nil
}

// Exported fields mirror the nonsecret resolved card, including the two private
// flags which would otherwise silently disappear in JSON persistence.
type codexEngineFrozenPrice struct {
	Resolved          ResolvedPricing                  `json:"resolved"`
	Channel           *ChannelModelPricing             `json:"channel,omitempty"`
	LongContext       bool                             `json:"long_context"`
	ImageCosts        map[string]float64               `json:"image_costs,omitempty"`
	StatsCustom       *ChannelModelPricing             `json:"stats_custom,omitempty"`
	StatsUseCustomer  bool                             `json:"stats_use_customer,omitempty"`
	StatsCatalog      *ModelPricing                    `json:"stats_catalog,omitempty"`
	ImageTokenPricing bool                             `json:"image_token_pricing,omitempty"`
	TokenIntervals    []codexEngineFrozenTokenInterval `json:"token_intervals,omitempty"`
	TimeMultiplier    *float64                         `json:"time_multiplier,omitempty"`
}

type codexEngineFrozenTokenInterval struct {
	MinTokens int           `json:"min_tokens"`
	MaxTokens *int          `json:"max_tokens,omitempty"`
	Price     *ModelPricing `json:"price"`
}

// CaptureCodexEngineBillingSnapshot freezes request identity and billing policy,
// but leaves model, channel, image and search prices to settlement-time lookup.
func (s *OpenAIGatewayService) CaptureCodexEngineBillingSnapshot(ctx context.Context, in *OpenAIRecordUsageInput) (json.RawMessage, error) {
	if s == nil || s.billingService == nil || in == nil || in.Result == nil || in.Account == nil || !in.Account.IsCodexEngine() || in.APIKey == nil || in.User == nil {
		return nil, errors.New("engine billing snapshot inputs unavailable")
	}
	a, key, result := in.Account, in.APIKey, in.Result
	baseRate := 1.0
	if s.cfg != nil {
		baseRate = s.cfg.Default.RateMultiplier
	}
	if key.GroupID != nil && key.Group != nil {
		baseRate = s.ResolveUserGroupRateMultiplier(ctx, in.User.ID, *key.GroupID, key.Group.RateMultiplier)
	}
	at := openAIUsagePricingAt(in)
	tokenRate, imageRate := computePeakAwareMultipliers(key, baseRate, at)
	billingModel := firstNonEmpty(result.BillingModel, forwardResultBillingModel(result.Model, result.UpstreamModel))
	if in.BillingModelSource == BillingModelSourceChannelMapped && in.ChannelMappedModel != "" && in.ChannelMappedModel != in.OriginalModel {
		billingModel = in.ChannelMappedModel
	}
	if in.BillingModelSource == BillingModelSourceRequested && in.OriginalModel != "" {
		billingModel = in.OriginalModel
	}
	f := codexEnginePricingSnapshot{
		Version: codexEnginePricingSnapshotVersion, PricingAt: at,
		RequestedModel: firstNonEmpty(in.OriginalModel, result.Model), UpstreamModel: firstNonEmpty(result.UpstreamModel, result.Model), BillingModel: billingModel,
		RequestedTier: optionalStringValue(result.ServiceTier), ReasoningEffort: optionalStringValue(result.ReasoningEffort),
		RequestedReasoningEffort: optionalStringValue(coalesceRequestedReasoningEffort(result.RequestedReasoningEffort, result.ReasoningEffort)),
		OpenAIWSMode:             result.OpenAIWSMode, RequestType: RequestTypeFromLegacy(result.Stream, result.OpenAIWSMode),
		UserAgent: codexEngineBillingUserAgent(in.UserAgent), SessionID: sanitizeSessionID(in.SessionID),
		InboundEndpoint: in.InboundEndpoint, UpstreamEndpoint: in.UpstreamEndpoint, GroupID: key.GroupID, ChannelID: in.ChannelID, ModelMappingChain: in.ModelMappingChain,
		AccountType: a.Type, QuotaPlatform: firstNonEmpty(in.QuotaPlatform, PlatformFromAPIKey(key)), Stream: result.Stream, NativeCompactionV2: in.NativeCompactionV2,
		KeyQuota: key.Quota > 0 && in.APIKeyService != nil, KeyRateLimit: key.HasRateLimits() && in.APIKeyService != nil,
		AccountQuota: a.SupportsAccountQuota() && a.HasAnyQuotaLimit(), AccountDailyQuota: a.GetQuotaDailyLimit() > 0, AccountWeeklyQuota: a.GetQuotaWeeklyLimit() > 0,
		AccountRate: a.BillingRateMultiplier(), TokenRate: tokenRate, ImageRate: imageRate, BaseRate: baseRate,
		SearchPrice: 0, LongContext: a.IsOpenAILongContextBillingEnabled(), ImageEndpoint: result.ImageEndpoint,
	}
	if in.CyberBlocked {
		f.RequestType = RequestTypeCyberBlocked
	}
	if ip := net.ParseIP(strings.TrimSpace(in.IPAddress)); ip != nil {
		f.IPAddress = ip.String()
	}
	if digest := strings.TrimSpace(in.RequestPayloadHash); len(digest) == 64 {
		if _, err := hex.DecodeString(digest); err == nil {
			f.RequestPayloadHash = strings.ToLower(digest)
		}
	}
	f.FreeFast, f.FreeUltrafast = effectiveOpenAIFreeServiceTiers(key, a)
	if key.Group != nil {
		f.GroupLongContextPricing = key.Group.LongContextPricingEnabled
		f.ImageCountBilling = key.Group.ImageCountBilling
	}
	if in.Subscription != nil {
		id := in.Subscription.ID
		f.SubscriptionID = &id
		f.SubscriptionBilling = key.Group != nil && key.Group.IsSubscriptionType()
	}
	f.TotalQuotaBilling = f.SubscriptionBilling && key.Group.IsTotalQuotaSubscriptionType()
	if f.TotalQuotaBilling {
		if admitted, ok := TotalQuotaSpendSnapshotFromContext(ctx); ok && admitted != nil {
			f.TotalQuotaSpendSnapshot = &codexEngineQuotaSpendSnapshot{
				SubscriptionID: admitted.SubscriptionID, EventIDs: append([]int64(nil), admitted.EventIDs...),
				OverflowEventID: admitted.OverflowEventID, TakenAt: admitted.TakenAt,
			}
		}
		if err := f.validateTotalQuotaSpendSnapshot(); err != nil {
			return nil, err
		}
	}
	f.SimpleMode = s.cfg != nil && s.cfg.RunMode == config.RunModeSimple
	f.SimpleKeyRateOnly = simpleModeKeyRateLimitBillingEnabled(s.cfg, key)
	return marshalCodexEnginePricingSnapshot(&f)
}

func codexEngineBillingUserAgent(value string) string {
	value = strings.TrimSpace(strings.ToValidUTF8(value, ""))
	value = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, value)
	if len(value) > 512 {
		value = strings.ToValidUTF8(value[:512], "")
	}
	return value
}

func decodeCodexEnginePricingSnapshot(raw json.RawMessage) (*codexEnginePricingSnapshot, error) {
	f, err := unmarshalCodexEnginePricingSnapshot(raw)
	if err != nil {
		return nil, err
	}
	// Version 1 jobs keep their legacy settlement semantics; their missing
	// admission evidence cannot be reconstructed from today's grants.
	if (f.Version != 1 && f.Version != 2 && f.Version != codexEnginePricingSnapshotVersion) || len(f.Prices) > codexEnginePricingModelLimit || f.PricingAt.IsZero() {
		return nil, errors.New("unsupported engine pricing snapshot")
	}
	if f.Version == codexEnginePricingSnapshotVersion && len(f.Prices) != 0 {
		return nil, errors.New("current engine pricing snapshot must not freeze model prices")
	}
	if err := f.validateTotalQuotaSpendSnapshot(); err != nil {
		return nil, err
	}
	for _, v := range []float64{f.AccountRate, f.TokenRate, f.ImageRate, f.BaseRate, f.SearchPrice} {
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
			return nil, errors.New("invalid engine pricing multiplier")
		}
	}
	return f, nil
}

func codexEngineValidateTokenEvidence(event *CodexEngineBillingUsageEvent) error {
	// Inclusive input/output and cache split are needed even for plain text.
	// Optional dimensions declared inapplicable by the source stay absent; an
	// applicable but unobserved dimension cannot be silently treated as free.
	for _, name := range []string{"input_tokens", "output_tokens", "cached_input_tokens"} {
		if !event.ApplicableUsage[name] || !event.UsagePresent[name] {
			return fmt.Errorf("engine token evidence missing %s", name)
		}
	}
	for _, name := range []string{"cache_write_input_tokens", "cache_write_30m_input_tokens", "cache_write_12h_input_tokens", "image_input_tokens", "cached_image_input_tokens", "image_output_tokens"} {
		if event.ApplicableUsage[name] && !event.UsagePresent[name] {
			return fmt.Errorf("engine token evidence missing %s", name)
		}
	}
	return nil
}

// New jobs resolve only the event's model at settlement time. Legacy jobs retain
// their frozen price cards; missing live dependencies leave new events pending.
func (s *OpenAIGatewayService) BuildCodexEngineBillingEvent(ctx context.Context, job *CodexEngineBillingJob, event *CodexEngineBillingUsageEvent) (*UsageBillingCommand, *UsageLog, error) {
	if job == nil || event == nil {
		return nil, nil, errors.New("engine billing event is nil")
	}
	f, err := decodeCodexEnginePricingSnapshot(job.Snapshot)
	if err != nil {
		return nil, nil, err
	}
	model := strings.TrimSpace(event.Model)
	priceModel := model
	if model == f.UpstreamModel {
		priceModel = f.BillingModel
	}
	tier := strings.TrimSpace(event.ServiceTier)
	effort := firstNonEmpty(event.ReasoningEffort, f.ReasoningEffort)
	u := event.Usage
	if u.InputTokens < 0 || u.OutputTokens < 0 || u.CacheReadInputTokens < 0 || u.CacheCreationInputTokens < 0 || u.CacheReadInputTokens > u.InputTokens || u.CacheCreationInputTokens > u.InputTokens-u.CacheReadInputTokens || u.ImageInputTokens < 0 || u.ImageCacheReadTokens < 0 || u.ImageOutputTokens < 0 || u.ImageInputTokens > u.InputTokens || u.ImageCacheReadTokens > u.CacheReadInputTokens || u.ImageCacheReadTokens > u.ImageInputTokens || u.ImageOutputTokens > u.OutputTokens || u.ImageInputTokens-u.ImageCacheReadTokens > u.InputTokens-u.CacheReadInputTokens-u.CacheCreationInputTokens {
		return nil, nil, errors.New("invalid engine usage token buckets")
	}
	if u.CacheCreation30mInputTokens != 0 || u.CacheCreation12hInputTokens != 0 || event.UsagePresent["cache_write_30m_input_tokens"] || event.UsagePresent["cache_write_12h_input_tokens"] {
		if !event.UsagePresent["cache_write_input_tokens"] || !event.UsagePresent["cache_write_30m_input_tokens"] || !event.UsagePresent["cache_write_12h_input_tokens"] ||
			!event.ApplicableUsage["cache_write_input_tokens"] || !event.ApplicableUsage["cache_write_30m_input_tokens"] || !event.ApplicableUsage["cache_write_12h_input_tokens"] ||
			u.CacheCreation30mInputTokens < 0 || u.CacheCreation12hInputTokens < 0 || u.CacheCreation30mInputTokens > u.CacheCreationInputTokens || u.CacheCreation12hInputTokens != u.CacheCreationInputTokens-u.CacheCreation30mInputTokens {
			return nil, nil, errors.New("invalid engine cache write TTL evidence")
		}
	}
	tokens := UsageTokens{InputTokens: u.InputTokens - u.CacheReadInputTokens - u.CacheCreationInputTokens, OutputTokens: u.OutputTokens, CacheReadTokens: u.CacheReadInputTokens, CacheCreationTokens: u.CacheCreationInputTokens, CacheCreation30mTokens: u.CacheCreation30mInputTokens, CacheCreation12hTokens: u.CacheCreation12hInputTokens, ImageInputTokens: max(u.ImageInputTokens-u.ImageCacheReadTokens, 0), ImageCacheReadTokens: u.ImageCacheReadTokens, ImageOutputTokens: u.ImageOutputTokens}
	var cost *CostBreakdown
	rate := f.TokenRate
	p, known := f.Prices[priceModel]
	bs := &BillingService{}
	var group *Group
	var resolver *ModelPricingResolver
	if f.Version >= 4 {
		if s == nil || s.billingService == nil {
			return nil, nil, errors.New("engine settlement pricing service unavailable")
		}
		bs = s.billingService
		if f.GroupID != nil {
			if *f.GroupID <= 0 || s.channelService == nil || s.channelService.groupRepo == nil {
				return nil, nil, errors.New("engine original billing group unavailable")
			}
			group, err = s.channelService.groupRepo.GetByIDLite(ctx, *f.GroupID)
			if err != nil {
				return nil, nil, fmt.Errorf("load engine original billing group: %w", err)
			}
			if group == nil || group.ID != *f.GroupID {
				return nil, nil, errors.New("engine original billing group unavailable")
			}
			// Resolver lookups deliberately tolerate unavailable channels elsewhere.
			// Recovered settlement must wait instead of silently losing an override.
			if _, err = s.channelService.GetChannelForGroup(ctx, *f.GroupID); err != nil {
				return nil, nil, fmt.Errorf("load engine settlement channel: %w", err)
			}
		}
		resolver = s.resolver
		if resolver == nil {
			resolver = NewModelPricingResolver(s.channelService, bs)
		}
		if event.Kind != "search" {
			r := resolver.Resolve(ctx, PricingInput{Model: priceModel, GroupID: f.GroupID, Group: group})
			if r == nil {
				return nil, nil, ErrModelPricingUnavailable
			}
			if r.Mode == BillingModePerRequest || r.Mode == BillingModeImage || r.Mode == BillingModeVideo {
				configured := r.channelPricing != nil && r.channelPricing.PerRequestPrice != nil
				for _, interval := range r.RequestTiers {
					configured = configured || interval.PerRequestPrice != nil
				}
				if !configured {
					return nil, nil, fmt.Errorf("engine configured request price missing: %w", ErrModelPricingUnavailable)
				}
			}
			r.longContextPricingEnabled = f.GroupLongContextPricing
			p = codexEngineFrozenPrice{Resolved: *r, Channel: r.channelPricing, LongContext: r.longContextPricingEnabled}
			p.ImageTokenPricing = r.Mode == BillingModeToken && (r.Source == PricingSourceGroup || r.Source == PricingSourceChannel) && !(f.ImageEndpoint && f.ImageCountBilling)
			known = true
		}
	}
	imageSize := ""
	switch event.Kind {
	case "search":
		if event.SearchCalls < 0 || event.SearchOperations < 0 || event.SearchCalls > 0 && event.SearchOperations > 0 {
			return nil, nil, errors.New("invalid engine search units")
		}
		units := event.SearchCalls
		if event.SearchOperations > 0 {
			units = event.SearchOperations
		}
		if units == 0 {
			return nil, nil, errors.New("missing engine search units")
		}
		// Operations and native calls are distinct meter dimensions. Never
		// manufacture calls from operations. Legacy jobs use their saved price;
		// current jobs resolve the original group's price below.
		cost = &CostBreakdown{TotalCost: float64(units) * f.SearchPrice, ActualCost: float64(units) * f.SearchPrice * f.BaseRate, BillingMode: string(BillingModePerRequest)}
		if f.Version >= 4 {
			var searchPrice *float64
			if group != nil {
				searchPrice = group.WebSearchPricePerCall
			}
			if searchPrice != nil && (math.IsNaN(*searchPrice) || math.IsInf(*searchPrice, 0) || *searchPrice < 0) {
				return nil, nil, errors.New("invalid engine current search price")
			}
			cost = bs.CalculateWebSearchCost(units, searchPrice, f.BaseRate)
		}
		rate = f.BaseRate
	case "image":
		if !known {
			return nil, nil, fmt.Errorf("engine image price missing for %q: %w", priceModel, ErrModelPricingUnavailable)
		}
		if event.ImageActual != nil {
			imageSize = event.ImageActual.Size
		}
		if !p.ImageTokenPricing {
			if event.ImageCount <= 0 {
				return nil, nil, errors.New("missing engine actual image count")
			}
			size, validSize := ClassifyImageBillingTier(imageSize)
			if f.Version >= 4 {
				if !validSize {
					return nil, nil, errors.New("engine actual image size unavailable")
				}
				cost, err = s.calculateCodexEngineCurrentImageCost(ctx, f, group, resolver, &p.Resolved, priceModel, size, event.ImageCount, effort)
				if err != nil {
					return nil, nil, err
				}
				rate = f.ImageRate
				break
			}
			unit, ok := p.ImageCosts[size]
			if !validSize || !ok {
				return nil, nil, fmt.Errorf("engine actual image size price missing: %w", ErrModelPricingUnavailable)
			}
			cost = &CostBreakdown{TotalCost: unit * float64(event.ImageCount), ActualCost: unit * float64(event.ImageCount) * f.ImageRate, BillingMode: string(BillingModeImage)}
			rate = f.ImageRate
			break
		}
		fallthrough
	case "model":
		if !known {
			return nil, nil, fmt.Errorf("engine frozen price missing for %q: %w", priceModel, ErrModelPricingUnavailable)
		}
		if p.Resolved.Mode == BillingModeToken || p.Resolved.Mode == "" {
			if err := codexEngineValidateTokenEvidence(event); err != nil {
				return nil, nil, err
			}
			switch normalizeBillingServiceTier(tier) {
			case "default", "standard", "priority", "fast", "flex", OpenAIFastTierUltrafast:
			default:
				return nil, nil, errors.New("engine actual service tier is unknown")
			}
		} else if len(p.Resolved.RequestTiers) > 0 && (!event.ApplicableUsage["input_tokens"] || !event.UsagePresent["input_tokens"]) {
			return nil, nil, errors.New("engine input context required for per-request price tier")
		}
		r := p.Resolved
		r.channelPricing = p.Channel
		r.longContextPricingEnabled = p.LongContext
		input := CostInput{Ctx: ctx, Model: priceModel, Tokens: tokens, RequestCount: 1, RateMultiplier: f.TokenRate, PricingAt: f.PricingAt, ServiceTier: tier, ReasoningEffort: effort, Resolver: &ModelPricingResolver{}, Resolved: &r, LongContextBillingEnabled: &f.LongContext}
		calculate := func(input CostInput) (*CostBreakdown, error) {
			if f.Version >= 4 {
				input.Resolver, input.GroupID, input.Group = resolver, f.GroupID, group
				if err := validateCodexEngineCurrentRequestPrice(&r, input); err != nil {
					return nil, err
				}
				return bs.CalculateCostUnified(input)
			}
			if r.Mode != BillingModeToken && r.Mode != "" {
				return bs.CalculateCostUnified(input)
			}
			price := p.Resolved.BasePricing
			contextTokens := u.InputTokens
			if !p.LongContext && !f.LongContext {
				contextTokens = 1
			}
			for _, interval := range p.TokenIntervals {
				if contextTokens > interval.MinTokens && (interval.MaxTokens == nil || contextTokens <= *interval.MaxTokens) {
					price = interval.Price
					break
				}
			}
			if price == nil {
				return nil, ErrModelPricingUnavailable
			}
			out := bs.computeTokenBreakdown(price, tokens, f.TokenRate, input.ServiceTier, len(p.TokenIntervals) == 0 && (p.LongContext || f.LongContext))
			if p.TimeMultiplier != nil {
				applyCostBreakdownMultiplier(out, *p.TimeMultiplier)
			}
			applyCostBreakdownMultiplier(out, reasoningEffortBillingMultiplier(effort, price.ReasoningEffortMultipliers))
			out.BillingMode = string(BillingModeToken)
			return out, nil
		}
		cost, err = calculate(input)
		if err != nil {
			return nil, nil, err
		}
		if openAIServiceTierBillsAtStandard(f.FreeFast, f.FreeUltrafast, tier) {
			input.ServiceTier = ""
			standard, e := calculate(input)
			if e != nil {
				return nil, nil, e
			}
			cost.ActualCost = standard.ActualCost
		}
	default:
		return nil, nil, errors.New("unsupported engine billing event kind")
	}
	if cost == nil || math.IsNaN(cost.TotalCost) || math.IsInf(cost.TotalCost, 0) || cost.TotalCost < 0 || math.IsNaN(cost.ActualCost) || math.IsInf(cost.ActualCost, 0) || cost.ActualCost < 0 {
		return nil, nil, errors.New("invalid engine calculated cost")
	}
	billingType := BillingTypeBalance
	if f.SubscriptionBilling {
		billingType = BillingTypeSubscription
	}
	log := &UsageLog{UserID: job.UserID, APIKeyID: job.APIKeyID, AccountID: job.AccountID, Model: model, RequestedModel: f.RequestedModel, UpstreamModel: optionalTrimmedStringPtr(model), UpstreamResponseModel: optionalTrimmedStringPtr(model), ServiceTier: optionalTrimmedStringPtr(tier), ReasoningEffort: optionalTrimmedStringPtr(effort), GroupID: f.GroupID, SubscriptionID: f.SubscriptionID, ChannelID: optionalInt64Ptr(f.ChannelID), ModelMappingChain: optionalTrimmedStringPtr(f.ModelMappingChain), InboundEndpoint: optionalTrimmedStringPtr(f.InboundEndpoint), UpstreamEndpoint: optionalTrimmedStringPtr(f.UpstreamEndpoint), InputTokens: tokens.InputTokens, OutputTokens: tokens.OutputTokens, CacheReadTokens: tokens.CacheReadTokens, CacheCreationTokens: tokens.CacheCreationTokens, ImageInputTokens: u.ImageInputTokens, ImageOutputTokens: u.ImageOutputTokens, ImageCount: event.ImageCount, ImageSize: optionalTrimmedStringPtr(imageSize), InputCost: cost.InputCost, OutputCost: cost.OutputCost, CacheReadCost: cost.CacheReadCost, CacheCreationCost: cost.CacheCreationCost, ImageInputCost: cost.ImageInputCost, ImageOutputCost: cost.ImageOutputCost, TotalCost: cost.TotalCost, ActualCost: cost.ActualCost, RateMultiplier: rate, AccountRateMultiplier: &f.AccountRate, BillingType: billingType, BillingMode: optionalTrimmedStringPtr(cost.BillingMode), Stream: f.Stream, NativeCompactionV2: f.NativeCompactionV2 || event.Purpose == "compact", LongContextBillingApplied: cost.LongContextBillingApplied, CreatedAt: f.PricingAt}
	log.OpenAIWSMode, log.RequestType = f.OpenAIWSMode, f.RequestType
	log.CacheCreation30mTokens, log.CacheCreation12hTokens = tokens.CacheCreation30mTokens, tokens.CacheCreation12hTokens
	if !event.OccurredAt.IsZero() {
		log.CreatedAt = event.OccurredAt
	}
	log.UserAgent, log.IPAddress, log.SessionID = optionalTrimmedStringPtr(f.UserAgent), optionalTrimmedStringPtr(f.IPAddress), optionalTrimmedStringPtr(f.SessionID)
	log.RequestedReasoningEffort = optionalTrimmedStringPtr(f.RequestedReasoningEffort)
	if u.ImageCacheReadTokens > 0 {
		log.ImageSizeBreakdown = map[string]int{"image_cache_read_tokens": u.ImageCacheReadTokens}
	}
	if event.ImageActual != nil {
		log.ImageOutputSize = optionalTrimmedStringPtr(event.ImageActual.Size)
		if size, ok := ClassifyImageBillingTier(event.ImageActual.Size); ok {
			log.ImageSize, log.BillingTier, log.ImageSizeSource = &size, &size, optionalTrimmedStringPtr(ImageSizeSourceOutput)
		}
	}
	if event.ImageRequested != nil {
		log.ImageInputSize = optionalTrimmedStringPtr(event.ImageRequested.Size)
	}
	statsPrice := f.Prices[model]
	if f.Version >= 4 && event.Kind != "search" && f.GroupID != nil {
		log.AccountStatsCost = resolveAccountStatsCost(ctx, s.channelService, bs, job.AccountID, *f.GroupID, model, tokens, max(1, event.ImageCount), cost.TotalCost, tier, f.PricingAt, f.LongContext, effort)
	}
	if statsPrice.StatsCustom != nil && event.Kind != "search" {
		log.AccountStatsCost = calculateStatsCost(statsPrice.StatsCustom, tokens, max(1, event.ImageCount))
		if log.AccountStatsCost != nil {
			*log.AccountStatsCost *= reasoningEffortBillingMultiplier(effort, statsPrice.StatsCustom.ReasoningEffortMultipliers)
		}
	}
	if log.AccountStatsCost == nil && event.Kind != "search" {
		if statsPrice.StatsUseCustomer {
			if cost.TotalCost > 0 {
				v := cost.TotalCost
				log.AccountStatsCost = &v
			}
		} else if statsPrice.StatsCatalog != nil && event.Kind == "model" {
			c := bs.computeTokenBreakdown(statsPrice.StatsCatalog, tokens, 1, tier, f.LongContext)
			applyCostBreakdownMultiplier(c, reasoningEffortBillingMultiplier(effort, statsPrice.StatsCatalog.ReasoningEffortMultipliers))
			log.AccountStatsCost = &c.TotalCost
		}
	}
	cmd := &UsageBillingCommand{APIKeyID: job.APIKeyID, UserID: job.UserID, AccountID: job.AccountID, AccountType: f.AccountType, SubscriptionID: f.SubscriptionID, Model: model, ServiceTier: tier, ReasoningEffort: effort, BillingType: billingType, InputTokens: log.InputTokens, OutputTokens: log.OutputTokens, CacheReadTokens: log.CacheReadTokens, CacheCreationTokens: log.CacheCreationTokens, ImageCount: log.ImageCount}
	if f.SimpleMode {
		if f.SimpleKeyRateOnly && f.KeyRateLimit {
			cmd.APIKeyRateLimitCost = cost.ActualCost
		}
	} else {
		if f.SubscriptionBilling {
			cmd.SubscriptionCost = cost.ActualCost
		} else {
			cmd.BalanceCost = cost.ActualCost
		}
		if f.KeyQuota {
			cmd.APIKeyQuotaCost = cost.ActualCost
		}
		if f.KeyRateLimit {
			cmd.APIKeyRateLimitCost = cost.ActualCost
		}
		if f.AccountQuota {
			cmd.AccountQuotaCost = cost.TotalCost * f.AccountRate
			cmd.AccountQuotaHasDailyLimit = f.AccountDailyQuota
			cmd.AccountQuotaHasWeeklyLimit = f.AccountWeeklyQuota
		}
	}
	cmd.Normalize()
	return cmd, log, nil
}

// Match the normal image pricing precedence while using the original request's
// group and multiplier. A configured zero is valid; absent model prices are not.
func (s *OpenAIGatewayService) calculateCodexEngineCurrentImageCost(ctx context.Context, f *codexEnginePricingSnapshot, group *Group, resolver *ModelPricingResolver, resolved *ResolvedPricing, model, size string, count int, effort string) (*CostBreakdown, error) {
	configured := resolved.Mode == BillingModePerRequest || resolved.Mode == BillingModeImage
	calculateConfigured := func() (*CostBreakdown, error) {
		input := CostInput{Ctx: ctx, Model: model, GroupID: f.GroupID, Group: group, RequestCount: count, SizeTier: size, RateMultiplier: f.ImageRate, PricingAt: f.PricingAt, ReasoningEffort: effort, Resolver: resolver, Resolved: resolved}
		if err := validateCodexEngineCurrentRequestPrice(resolved, input); err != nil {
			return nil, err
		}
		return s.billingService.CalculateCostUnified(input)
	}
	if configured && resolved.Source == PricingSourceGroup {
		return calculateConfigured()
	}
	key := &APIKey{GroupID: f.GroupID, Group: group}
	if apiKeyHasConfiguredImagePrice(key, size) {
		return s.billingService.CalculateImageCost(model, size, count, imagePriceConfigFromAPIKey(key), f.ImageRate), nil
	}
	if configured && resolved.Source == PricingSourceChannel {
		return calculateConfigured()
	}
	known := resolved.BasePricing != nil
	if catalog := s.billingService.pricingService; catalog != nil {
		if price := catalog.GetModelPricing(model); price != nil && price.OutputCostPerImage > 0 {
			known = true
		}
	}
	if !known {
		return nil, fmt.Errorf("engine image price missing for %q: %w", model, ErrModelPricingUnavailable)
	}
	return s.billingService.CalculateImageCost(model, size, count, nil, f.ImageRate), nil
}

func validateCodexEngineCurrentRequestPrice(resolved *ResolvedPricing, input CostInput) error {
	if resolved.Mode == BillingModeToken || resolved.Mode == "" {
		return nil
	}
	if resolved.channelPricing != nil && resolved.channelPricing.PerRequestPrice != nil {
		return nil
	}
	for _, tier := range resolved.RequestTiers {
		if input.SizeTier != "" && tier.TierLabel == input.SizeTier && tier.PerRequestPrice != nil {
			return nil
		}
	}
	contextTokens := input.Tokens.InputTokens + input.Tokens.CacheCreationTokens + input.Tokens.CacheReadTokens
	if tier := FindMatchingInterval(resolved.RequestTiers, contextTokens); tier != nil && tier.PerRequestPrice != nil {
		return nil
	}
	return fmt.Errorf("engine request price does not cover confirmed usage: %w", ErrModelPricingUnavailable)
}

func (s *OpenAIGatewayService) SettleCodexEngineBillingEvent(ctx context.Context, store CodexEngineBillingStore, job *CodexEngineBillingJob, event *CodexEngineBillingUsageEvent) error {
	cmd, log, err := s.BuildCodexEngineBillingEvent(ctx, job, event)
	if err != nil {
		return err
	}
	f, err := decodeCodexEnginePricingSnapshot(job.Snapshot)
	if err != nil {
		return err
	}
	quotaCost := log.ActualCost
	if f.SimpleMode || f.SubscriptionBilling {
		quotaCost = 0
	}
	// Workers restart with an independent context. Restore only this job's
	// frozen admission evidence, never a snapshot supplied by another caller.
	ctx = WithTotalQuotaSpendSnapshot(ctx, nil)
	if q := f.TotalQuotaSpendSnapshot; q != nil {
		ctx = WithTotalQuotaSpendSnapshot(ctx, &TotalQuotaSpendSnapshot{
			SubscriptionID: q.SubscriptionID, EventIDs: append([]int64(nil), q.EventIDs...),
			OverflowEventID: q.OverflowEventID, TakenAt: q.TakenAt,
		})
	}
	result, err := store.ApplyEvent(ctx, &CodexEngineBillingEvent{JobID: job.ID, ClaimToken: job.ClaimToken, EventID: event.EventID, Fingerprint: event.Fingerprint, Sequence: event.Sequence, Summary: CodexEngineBillingSummaryGroup{Kind: event.Kind, Purpose: event.Purpose, Model: event.Model, ServiceTier: optionalTrimmedStringPtr(event.ServiceTier), Usage: event.UsageValues}, Platform: f.QuotaPlatform, PlatformQuotaCost: quotaCost, Command: *cmd, UsageLog: *log})
	if err != nil {
		return err
	}
	if result == nil {
		return errors.New("engine billing settlement result unavailable")
	}
	// Invalidation is idempotent. Recovered events must never apply a second
	// monetary delta to a potentially stale cache.
	if s.billingCacheService != nil {
		if err := s.billingCacheService.InvalidateUserBalance(ctx, job.UserID); err != nil {
			return fmt.Errorf("invalidate engine balance cache: %w", err)
		}
		if err := s.billingCacheService.InvalidateAPIKeyRateLimit(ctx, job.APIKeyID); err != nil {
			return fmt.Errorf("invalidate engine key rate cache: %w", err)
		}
		if log.GroupID != nil && log.BillingType == BillingTypeSubscription {
			if err := s.billingCacheService.InvalidateSubscription(ctx, job.UserID, *log.GroupID); err != nil {
				return fmt.Errorf("invalidate engine subscription cache: %w", err)
			}
		}
		if quotaCost > 0 {
			if err := s.billingCacheService.RefreshUserPlatformQuotaEngineUsage(ctx, job.UserID, f.QuotaPlatform); err != nil {
				return fmt.Errorf("refresh engine platform quota cache: %w", err)
			}
		}
	}
	if s.deferredService != nil {
		s.deferredService.ScheduleLastUsedUpdate(job.AccountID)
	}
	if s.engineBillingAPIKeys != nil {
		if key, e := s.engineBillingAPIKeys.GetByID(ctx, job.APIKeyID); e == nil && key != nil && key.Key != "" {
			s.engineBillingAPIKeys.InvalidateAuthCacheByKey(ctx, key.Key)
		}
	}
	return store.AckReceivedSettled(ctx, job.ID, job.ClaimToken, event.EventID, event.Fingerprint, event.Sequence)
}
