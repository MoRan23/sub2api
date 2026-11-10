package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func engineFrozenTestSnapshot(t *testing.T) (*CodexEngineBillingJob, codexEnginePricingSnapshot) {
	t.Helper()
	f := codexEnginePricingSnapshot{Version: 1, PricingAt: time.Date(2026, 11, 6, 2, 3, 4, 0, time.UTC), RequestedModel: "alias", UpstreamModel: "gpt-engine-fixture", BillingModel: "gpt-engine-fixture", AccountType: AccountTypeAPIKey, AccountRate: 3, TokenRate: 2, ImageRate: 4, BaseRate: 5, SearchPrice: .01, QuotaPlatform: PlatformOpenAI, KeyQuota: true, KeyRateLimit: true, AccountQuota: true, AccountDailyQuota: true, Prices: map[string]codexEngineFrozenPrice{
		"gpt-engine-fixture": {Resolved: ResolvedPricing{Mode: BillingModeToken, BasePricing: &ModelPricing{InputPricePerToken: .1, OutputPricePerToken: .2, CacheReadPricePerToken: .01, CacheCreationPricePerToken: .3}}},
		"gpt-engine-other":   {Resolved: ResolvedPricing{Mode: BillingModeToken, BasePricing: &ModelPricing{InputPricePerToken: .2, OutputPricePerToken: .3, CacheReadPricePerToken: .02}}},
		"gpt-image-fixture":  {Resolved: ResolvedPricing{Mode: BillingModeImage}, ImageCosts: map[string]float64{"1K": .1, "2K": .2, "4K": .3}},
	}}
	raw, err := json.Marshal(f)
	require.NoError(t, err)
	return &CodexEngineBillingJob{CodexEngineBillingPending: CodexEngineBillingPending{UserID: 1, APIKeyID: 2, AccountID: 3, Snapshot: raw}, ID: 4, ClaimToken: "claim"}, f
}

func engineFrozenTestModelEvent() *CodexEngineBillingUsageEvent {
	return &CodexEngineBillingUsageEvent{EventID: "event-model", Sequence: 1, Kind: "model", Purpose: "response", Model: "gpt-engine-fixture", ServiceTier: "default", UsageComplete: true, Usage: OpenAIUsage{InputTokens: 100, OutputTokens: 10, CacheReadInputTokens: 20}, ApplicableUsage: map[string]bool{"input_tokens": true, "output_tokens": true, "cached_input_tokens": true}, UsagePresent: map[string]bool{"input_tokens": true, "output_tokens": true, "cached_input_tokens": true}, UsageValues: map[string]int64{"input_tokens": 100, "output_tokens": 10, "cached_input_tokens": 20}}
}

func TestCodexEngineCurrentPricingCaptureExcludesPricesAndSecrets(t *testing.T) {
	ctx := context.Background()
	cfg := &config.Config{}
	cfg.Default.RateMultiplier = 2
	price := &ModelPricing{InputPricePerToken: .1, OutputPricePerToken: .2, CacheReadPricePerToken: .01}
	svc := &OpenAIGatewayService{cfg: cfg, billingService: &BillingService{fallbackPrices: map[string]*ModelPricing{"gpt-5.4": price}}}
	input := &OpenAIRecordUsageInput{Account: engineReceiptTestAccount(), User: &User{ID: 1}, APIKey: &APIKey{ID: 2, Key: "downstream-secret"}, Result: &OpenAIForwardResult{Model: "gpt-5.4", UpstreamModel: "gpt-5.4"}}
	input.Account.Credentials = map[string]any{"api_key": "upstream-secret", "base_url": "https://upstream.invalid"}
	input.Result.OpenAIWSMode, input.Result.Stream = true, true
	input.UserAgent, input.IPAddress, input.SessionID = "codex/1.0\r\n", "203.0.113.8", " client-session "
	input.RequestPayloadHash = strings.Repeat("a", 64)
	raw, err := svc.CaptureCodexEngineBillingSnapshot(ctx, input)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "secret")
	require.NotContains(t, string(raw), "upstream.invalid")
	price.InputPricePerToken, price.OutputPricePerToken = .2, .3
	cfg.Default.RateMultiplier = 999
	input.Account.Credentials["api_key"] = "changed"
	job := &CodexEngineBillingJob{CodexEngineBillingPending: CodexEngineBillingPending{UserID: 1, APIKeyID: 2, AccountID: input.Account.ID, Snapshot: raw}}
	// A restarted worker resolves today's card, retaining request multipliers.
	event := engineFrozenTestModelEvent()
	event.Model = "gpt-5.4"
	restarted := &OpenAIGatewayService{billingService: svc.billingService}
	cmd, log, err := restarted.BuildCodexEngineBillingEvent(ctx, job, event)
	require.NoError(t, err)
	require.InDelta(t, 19.2, log.TotalCost, 1e-9)
	require.InDelta(t, 38.4, cmd.BalanceCost, 1e-9)
	require.True(t, log.OpenAIWSMode)
	require.Equal(t, RequestTypeWSV2, log.RequestType)
	require.Equal(t, "codex/1.0", *log.UserAgent)
	require.Equal(t, "203.0.113.8", *log.IPAddress)
	require.Equal(t, "client-session", *log.SessionID)
	f, err := decodeCodexEnginePricingSnapshot(raw)
	require.NoError(t, err)
	require.Equal(t, input.RequestPayloadHash, f.RequestPayloadHash)
	require.Empty(t, f.Prices)
	require.Equal(t, 4, f.Version)
}

func TestCodexEngineFrozenPricingActualModelsTiersAndSearchAreSeparate(t *testing.T) {
	job, _ := engineFrozenTestSnapshot(t)
	svc := &OpenAIGatewayService{}
	event := engineFrozenTestModelEvent()
	cmd, log, err := svc.BuildCodexEngineBillingEvent(context.Background(), job, event)
	require.NoError(t, err)
	require.InDelta(t, 20.4, cmd.BalanceCost, 1e-9)
	require.InDelta(t, 30.6, cmd.AccountQuotaCost, 1e-9)
	require.Equal(t, 80, log.InputTokens)
	event.Model, event.ServiceTier, event.Purpose = "gpt-engine-other", "priority", "compact"
	_, other, err := svc.BuildCodexEngineBillingEvent(context.Background(), job, event)
	require.NoError(t, err)
	require.InDelta(t, 38.8, other.TotalCost, 1e-9)
	require.True(t, other.NativeCompactionV2)
	for _, operation := range []bool{false, true} {
		search := &CodexEngineBillingUsageEvent{Kind: "search", Purpose: "search", Model: event.Model}
		if operation {
			search.SearchOperations = 2
		} else {
			search.SearchCalls = 2
		}
		_, searchLog, err := svc.BuildCodexEngineBillingEvent(context.Background(), job, search)
		require.NoError(t, err)
		require.InDelta(t, .02, searchLog.TotalCost, 1e-9)
		require.InDelta(t, .1, searchLog.ActualCost, 1e-9)
		require.InDelta(t, 38.82, other.TotalCost+searchLog.TotalCost, 1e-9, "search cannot replace the model component")
	}
}

func TestCodexEngineFrozenPricingMissingFactsStayPending(t *testing.T) {
	job, _ := engineFrozenTestSnapshot(t)
	for _, tc := range []struct {
		name   string
		mutate func(*CodexEngineBillingUsageEvent)
	}{
		{"unknown model", func(e *CodexEngineBillingUsageEvent) { e.Model = "not-in-frozen-catalog" }},
		{"unknown tier", func(e *CodexEngineBillingUsageEvent) { e.ServiceTier = "" }},
		{"missing cached", func(e *CodexEngineBillingUsageEvent) { delete(e.UsagePresent, "cached_input_tokens") }},
		{"missing applicable write", func(e *CodexEngineBillingUsageEvent) { e.ApplicableUsage["cache_write_input_tokens"] = true }},
		{"invalid cached", func(e *CodexEngineBillingUsageEvent) { e.Usage.CacheReadInputTokens = 101 }},
		{"invalid image subset", func(e *CodexEngineBillingUsageEvent) { e.Usage.ImageOutputTokens = 11 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := engineFrozenTestModelEvent()
			tc.mutate(e)
			cmd, log, err := (&OpenAIGatewayService{}).BuildCodexEngineBillingEvent(context.Background(), job, e)
			require.Error(t, err)
			require.Nil(t, cmd)
			require.Nil(t, log)
		})
	}
}

func TestCodexEngineFrozenPricingPerRequestDoesNotRequireTokenDimensions(t *testing.T) {
	job, snapshot := engineFrozenTestSnapshot(t)
	price := .25
	snapshot.Prices["gpt-engine-fixture"] = codexEngineFrozenPrice{Resolved: ResolvedPricing{Mode: BillingModePerRequest, DefaultPerRequestPrice: price}}
	var err error
	job.Snapshot, err = json.Marshal(snapshot)
	require.NoError(t, err)
	event := &CodexEngineBillingUsageEvent{Kind: "model", Purpose: "response", Model: "gpt-engine-fixture"}
	cmd, log, err := (&OpenAIGatewayService{}).BuildCodexEngineBillingEvent(context.Background(), job, event)
	require.NoError(t, err)
	require.InDelta(t, .25, log.TotalCost, 1e-12)
	require.InDelta(t, .5, cmd.BalanceCost, 1e-12)
	require.Nil(t, log.ServiceTier)

	card := snapshot.Prices["gpt-engine-fixture"]
	higher := .75
	card.Resolved.RequestTiers = []PricingInterval{{MinTokens: 100, PerRequestPrice: &higher}}
	snapshot.Prices["gpt-engine-fixture"] = card
	job.Snapshot, err = json.Marshal(snapshot)
	require.NoError(t, err)
	_, _, err = (&OpenAIGatewayService{}).BuildCodexEngineBillingEvent(context.Background(), job, event)
	require.ErrorContains(t, err, "input context required")
	event.ApplicableUsage = map[string]bool{"input_tokens": true}
	event.UsagePresent = map[string]bool{"input_tokens": true}
	event.Usage.InputTokens = 101
	_, log, err = (&OpenAIGatewayService{}).BuildCodexEngineBillingEvent(context.Background(), job, event)
	require.NoError(t, err)
	require.InDelta(t, .75, log.TotalCost, 1e-12)
}

func TestCodexEngineFrozenPricingLongContextFreeFastAndIntervalBoundary(t *testing.T) {
	job, f := engineFrozenTestSnapshot(t)
	p := f.Prices["gpt-engine-fixture"]
	p.LongContext = true
	p.Resolved.BasePricing.LongContextInputThreshold = 50
	p.Resolved.BasePricing.LongContextInputMultiplier = 2
	p.Resolved.BasePricing.LongContextOutputMultiplier = 3
	f.Prices["gpt-engine-fixture"] = p
	f.FreeFast = true
	var err error
	job.Snapshot, err = json.Marshal(f)
	require.NoError(t, err)
	e := engineFrozenTestModelEvent()
	e.ServiceTier = "priority"
	_, log, err := (&OpenAIGatewayService{}).BuildCodexEngineBillingEvent(context.Background(), job, e)
	require.NoError(t, err)
	require.InDelta(t, 44.8, log.TotalCost, 1e-9)
	require.InDelta(t, 44.8, log.ActualCost, 1e-9, "free Fast bills the standard card but records actual priority cost")
	require.True(t, log.LongContextBillingApplied)
	maxTokens := 100
	p.TokenIntervals = []codexEngineFrozenTokenInterval{{MinTokens: 0, MaxTokens: &maxTokens, Price: &ModelPricing{InputPricePerToken: .01, OutputPricePerToken: .02, CacheReadPricePerToken: .001}}, {MinTokens: 100, Price: &ModelPricing{InputPricePerToken: 1, OutputPricePerToken: 2, CacheReadPricePerToken: .1}}}
	f.Prices["gpt-engine-fixture"] = p
	f.FreeFast = false
	job.Snapshot, err = json.Marshal(f)
	require.NoError(t, err)
	e.ServiceTier = "default"
	_, log, err = (&OpenAIGatewayService{}).BuildCodexEngineBillingEvent(context.Background(), job, e)
	require.NoError(t, err)
	require.InDelta(t, 1.02, log.TotalCost, 1e-9, "interval upper boundary is inclusive")
	require.False(t, log.LongContextBillingApplied)
	e.Usage.InputTokens = 101
	_, log, err = (&OpenAIGatewayService{}).BuildCodexEngineBillingEvent(context.Background(), job, e)
	require.NoError(t, err)
	require.InDelta(t, 103, log.TotalCost, 1e-9)
}

func TestCodexEngineFrozenPricingUngroupedLongContextMatchesOrdinaryPolicy(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Default.RateMultiplier = 1
			price := &ModelPricing{InputPricePerToken: .1, OutputPricePerToken: .2, CacheReadPricePerToken: .01, LongContextInputThreshold: 50, LongContextInputMultiplier: 2, LongContextOutputMultiplier: 3}
			svc := &OpenAIGatewayService{cfg: cfg, billingService: &BillingService{fallbackPrices: map[string]*ModelPricing{"gpt-5.4": price}}}
			account := engineReceiptTestAccount()
			account.Extra[openAILongContextBillingEnabledKey] = enabled
			in := &OpenAIRecordUsageInput{Account: account, User: &User{ID: 1}, APIKey: &APIKey{ID: 2}, Result: &OpenAIForwardResult{Model: "gpt-5.4"}}
			raw, err := svc.CaptureCodexEngineBillingSnapshot(context.Background(), in)
			require.NoError(t, err)
			event := engineFrozenTestModelEvent()
			event.Model, event.Usage.InputTokens = "gpt-5.4", 300000
			_, got, err := svc.BuildCodexEngineBillingEvent(context.Background(), &CodexEngineBillingJob{CodexEngineBillingPending: CodexEngineBillingPending{Snapshot: raw}}, event)
			require.NoError(t, err)
			want, err := svc.billingService.calculateCostWithServiceTierPolicy("gpt-5.4", UsageTokens{InputTokens: 299980, OutputTokens: 10, CacheReadTokens: 20}, 1, "default", enabled)
			require.NoError(t, err)
			require.Equal(t, enabled, got.LongContextBillingApplied)
			require.InDelta(t, want.TotalCost, got.TotalCost, 1e-12)
			require.InDelta(t, want.ActualCost, got.ActualCost, 1e-12)
		})
	}
}

func TestCodexEngineFrozenPricingImageUsesActualMetadataAndTokenCard(t *testing.T) {
	job, f := engineFrozenTestSnapshot(t)
	event := &CodexEngineBillingUsageEvent{Kind: "image", Purpose: "image", Model: "gpt-image-fixture", ImageCount: 2, ImageRequested: &CodexEngineBillingImageMetadata{Size: "4K"}, ImageActual: &CodexEngineBillingImageMetadata{Size: "1024x1024"}}
	_, log, err := (&OpenAIGatewayService{}).BuildCodexEngineBillingEvent(context.Background(), job, event)
	require.NoError(t, err)
	require.InDelta(t, .2, log.TotalCost, 1e-9)
	require.InDelta(t, .8, log.ActualCost, 1e-9)
	event.ImageActual = nil
	_, _, err = (&OpenAIGatewayService{}).BuildCodexEngineBillingEvent(context.Background(), job, event)
	require.Error(t, err, "requested dimensions cannot stand in for actual image dimensions")
	f.Prices["gpt-image-fixture"] = codexEngineFrozenPrice{ImageTokenPricing: true, Resolved: ResolvedPricing{Mode: BillingModeToken, BasePricing: &ModelPricing{InputPricePerToken: .1, OutputPricePerToken: .2, CacheReadPricePerToken: .01, ImageOutputPricePerToken: .3}}}
	job.Snapshot, err = json.Marshal(f)
	require.NoError(t, err)
	event = engineFrozenTestModelEvent()
	event.Kind = "image"
	event.Purpose = "image"
	event.Model = "gpt-image-fixture"
	event.ImageCount = 2
	event.Usage.ImageOutputTokens = 5
	event.Usage.ImageInputTokens, event.Usage.ImageCacheReadTokens = 10, 5
	event.ApplicableUsage["image_output_tokens"] = true
	event.UsagePresent["image_output_tokens"] = true
	for _, key := range []string{"image_input_tokens", "cached_image_input_tokens"} {
		event.ApplicableUsage[key] = true
		event.UsagePresent[key] = true
	}
	_, log, err = (&OpenAIGatewayService{}).BuildCodexEngineBillingEvent(context.Background(), job, event)
	require.NoError(t, err)
	require.InDelta(t, 10.7, log.TotalCost, 1e-9)
	require.Equal(t, "token", *log.BillingMode)
	require.Equal(t, 5, log.ImageSizeBreakdown["image_cache_read_tokens"])
}

func TestCodexEngineCurrentPricingUsesOriginalGroupCurrentImagePrice(t *testing.T) {
	price := .5
	group := &Group{ID: 4, RateMultiplier: 1, ModelPricing: []ChannelModelPricing{{Platform: PlatformOpenAI, Models: []string{"gpt-image-fixture"}, BillingMode: BillingModeImage, PerRequestPrice: &price}}}
	svc := engineCurrentPricingTestService(group)
	input := &OpenAIRecordUsageInput{User: &User{ID: 1}, APIKey: &APIKey{ID: 2, GroupID: &group.ID, Group: group}, Account: engineReceiptTestAccount(), Result: &OpenAIForwardResult{Model: "gpt-image-fixture", ImageEndpoint: true}}
	raw, err := svc.CaptureCodexEngineBillingSnapshot(context.Background(), input)
	require.NoError(t, err)
	price = 100
	job := &CodexEngineBillingJob{CodexEngineBillingPending: CodexEngineBillingPending{UserID: 1, APIKeyID: 2, AccountID: input.Account.ID, Snapshot: raw}}
	event := &CodexEngineBillingUsageEvent{Kind: "image", Model: "gpt-image-fixture", ImageCount: 2, ImageActual: &CodexEngineBillingImageMetadata{Size: "1024x1024"}}
	_, log, err := svc.BuildCodexEngineBillingEvent(context.Background(), job, event)
	require.NoError(t, err)
	require.InDelta(t, 200, log.TotalCost, 1e-9)
}

type engineFrozenStoreStub struct {
	CodexEngineBillingStore
	received *CodexEngineBillingEvent
	seen     map[string]string
	applied  int
}

func (s *engineFrozenStoreStub) ApplyEvent(_ context.Context, e *CodexEngineBillingEvent) (*UsageBillingApplyResult, error) {
	s.received = e
	if s.seen == nil {
		s.seen = make(map[string]string)
	}
	id := fmt.Sprintf("%d/%s", e.JobID, e.EventID)
	if fingerprint, ok := s.seen[id]; ok {
		if fingerprint != e.Fingerprint {
			return nil, ErrCodexEngineBillingConflict
		}
		return &UsageBillingApplyResult{Applied: false}, nil
	}
	s.seen[id] = e.Fingerprint
	s.applied++
	return &UsageBillingApplyResult{Applied: true}, nil
}

func TestCodexEngineFrozenPricingSettlementCarriesAtomicQuotaAndEvidence(t *testing.T) {
	job, _ := engineFrozenTestSnapshot(t)
	event := engineFrozenTestModelEvent()
	event.Fingerprint = strings.Repeat("a", 64)
	store := &engineFrozenStoreStub{}
	err := (&OpenAIGatewayService{}).SettleCodexEngineBillingEvent(context.Background(), store, job, event)
	require.NoError(t, err)
	require.Equal(t, int64(1), store.received.Sequence)
	require.Equal(t, event.UsageValues, store.received.Summary.Usage)
	require.Equal(t, PlatformOpenAI, store.received.Platform)
	require.InDelta(t, 20.4, store.received.PlatformQuotaCost, 1e-9)
}

func TestCodexEngineCurrentPricingCaptureDoesNotEnumerateCatalog(t *testing.T) {
	cfg := &config.Config{}
	cfg.Default.RateMultiplier = 1
	prices := map[string]*ModelPricing{}
	for i := 0; i <= codexEnginePricingModelLimit; i++ {
		prices["gpt-"+strings.Repeat("x", i+1)] = &ModelPricing{}
	}
	svc := &OpenAIGatewayService{cfg: cfg, billingService: &BillingService{fallbackPrices: prices}}
	in := engineReceiptTestInput("")
	raw, err := svc.CaptureCodexEngineBillingSnapshot(context.Background(), in)
	require.NoError(t, err)
	require.Less(t, len(raw), 4096, "catalog size must not affect metadata-only capture")
}
