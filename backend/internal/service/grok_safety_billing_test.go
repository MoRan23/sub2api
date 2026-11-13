package service

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"unsafe"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
)

type grokSafetyAtomicRepoStub struct {
	UsageBillingRepository
	commands []*UsageBillingCommand
	logs     []*UsageLog
	err      error
	seen     map[string]bool
}

func (r *grokSafetyAtomicRepoStub) ApplyWithUsageLog(ctx context.Context, cmd *UsageBillingCommand, row *UsageLog) (*UsageBillingApplyResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.commands = append(r.commands, cmd)
	if r.err != nil {
		return nil, r.err
	}
	if r.seen == nil {
		r.seen = map[string]bool{}
	}
	if r.seen[cmd.RequestID] {
		return &UsageBillingApplyResult{}, nil
	}
	r.seen[cmd.RequestID] = true
	r.logs = append(r.logs, row)
	return &UsageBillingApplyResult{Applied: true}, nil
}

func grokSafetyBillingFixture(t *testing.T) (*OpenAIGatewayService, *OpenAIRecordUsageInput, *grokSafetyAtomicRepoStub, *openAIRecordUsageLogRepoStub) {
	t.Helper()
	rows := &openAIRecordUsageLogRepoStub{}
	atomic := &grokSafetyAtomicRepoStub{}
	svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(rows, atomic, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
	svc.billingCacheService = nil
	svc.resolver = NewModelPricingResolver(nil, svc.billingService)
	id, inputPrice, outputPrice, accountRate := int64(9), 0.0002, 0.0005, 3.0
	input := &OpenAIRecordUsageInput{
		Result: &OpenAIForwardResult{Model: "grok-test", RequestID: "upstream-original", Usage: OpenAIUsage{InputTokens: 1000, OutputTokens: 200},
			GrokSafetyRejection: &GrokSafetyRejection{CallID: "server-attempt-one", Code: GrokSafetyRejectionCode, Fee: 0.4, HTTPStatus: 403},
			ImageCount:          2, ImageEndpoint: true, VideoCount: 1, WebSearchCalls: 3, SearchCount: 4, AudioUsage: &AudioUsage{Mode: "tts", DurationOrUnits: 5}},
		User: &User{ID: 1},
		APIKey: &APIKey{ID: 2, GroupID: &id, Quota: 100, RateLimit5h: 50, Group: &Group{ID: id, Platform: PlatformGrok, RateMultiplier: 2,
			ModelPricing: []ChannelModelPricing{{Models: []string{"grok-test"}, BillingMode: BillingModeToken, InputPrice: &inputPrice, OutputPrice: &outputPrice}}}},
		Account:       &Account{ID: 3, Platform: PlatformGrok, Type: AccountTypeAPIKey, RateMultiplier: &accountRate, Extra: map[string]any{"quota_limit": 100.0}},
		QuotaPlatform: PlatformGrok,
	}
	return svc, input, atomic, rows
}

func TestGrokSafetyBillingAtomicTokenAndFixedFee(t *testing.T) {
	svc, in, repo, ordinary := grokSafetyBillingFixture(t)
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), ctxkey.ClientRequestID, "reused-client-id"))
	cancel()
	in.Result.ClientDisconnect = true
	require.NoError(t, svc.RecordUsage(ctx, in))
	require.Len(t, repo.logs, 1)
	require.Zero(t, ordinary.calls)
	log, cmd := repo.logs[0], repo.commands[0]
	require.Equal(t, "grok-safety:server-attempt-one", log.RequestID)
	require.InDelta(t, 0.7, log.TotalCost, 1e-12)
	require.InDelta(t, 1, log.ActualCost, 1e-12)
	require.InDelta(t, 1, cmd.BalanceCost, 1e-12)
	require.InDelta(t, 1, cmd.APIKeyQuotaCost, 1e-12)
	require.InDelta(t, 1, cmd.APIKeyRateLimitCost, 1e-12)
	require.InDelta(t, 1, cmd.PlatformQuotaCost, 1e-12)
	require.InDelta(t, 1.3, cmd.AccountQuotaCost, 1e-12)
	require.Zero(t, log.ImageCount)
	require.Zero(t, log.VideoCount)
	require.Equal(t, 2, in.Result.ImageCount, "writer must not mutate forwarding evidence")
	require.NoError(t, svc.RecordUsage(ctx, in))
	require.Len(t, repo.logs, 1, "one durable row for duplicate callback")
	in.Result.GrokSafetyRejection.CallID = "server-attempt-two"
	require.NoError(t, svc.RecordUsage(ctx, in))
	require.Len(t, repo.logs, 2, "new actual attempt does not collapse under a client id")
}

func TestGrokSafetyBillingZeroUsageAndMissingTokenPrice(t *testing.T) {
	for _, scenario := range []string{"zero_fee", "zero_usage", "missing_price"} {
		t.Run(scenario, func(t *testing.T) {
			svc, in, repo, ordinary := grokSafetyBillingFixture(t)
			in.Result.Usage = OpenAIUsage{}
			if scenario == "zero_fee" {
				in.Result.GrokSafetyRejection.Fee = 0
			}
			if scenario == "missing_price" {
				in.APIKey.Group.ModelPricing = nil
				in.Result.Model = "unpriced-refusal-xyz"
				in.Result.Usage.InputTokens = 10
			}
			require.NoError(t, svc.RecordUsage(context.Background(), in))
			require.Len(t, repo.logs, 1)
			require.Equal(t, in.Result.GrokSafetyRejection.Fee, repo.logs[0].ActualCost)
			require.Equal(t, scenario == "missing_price", repo.logs[0].GrokSafetyRejection.TokenPricingUnavailable)
			require.False(t, in.Result.GrokSafetyRejection.TokenPricingUnavailable, "pricing diagnostics must not mutate the forwarding evidence")
			require.Zero(t, ordinary.calls)
		})
	}
}

func TestGrokSafetyBillingVideoNameDoesNotRestoreSuccessfulUnits(t *testing.T) {
	svc, in, repo, _ := grokSafetyBillingFixture(t)
	in.Result.Model = "grok-imagine-video"
	in.Result.VideoDurationSeconds = 12
	in.Result.VideoResolution = "720p"
	in.APIKey.Group.ModelPricing[0].Models = []string{in.Result.Model}
	require.NoError(t, svc.RecordUsage(context.Background(), in))
	require.Len(t, repo.logs, 1)
	require.Zero(t, repo.logs[0].VideoCount)
	require.Nil(t, repo.logs[0].VideoDurationSeconds)
	require.Nil(t, repo.logs[0].VideoResolution)
	require.Equal(t, string(BillingModeToken), *repo.logs[0].BillingMode)
	require.InDelta(t, 1, repo.logs[0].ActualCost, 1e-12)
}

func TestGrokSafetyBillingRefusesNonAtomicOrFailedStorage(t *testing.T) {
	for _, scenario := range []string{"missing", "non_atomic", "transaction_failed"} {
		t.Run(scenario, func(t *testing.T) {
			svc, in, repo, ordinary := grokSafetyBillingFixture(t)
			switch scenario {
			case "missing":
				svc.usageBillingRepo = nil
			case "non_atomic":
				svc.usageBillingRepo = &openAIRecordUsageBillingRepoStub{}
			case "transaction_failed":
				repo.err = errors.New("usage insert failed")
			}
			require.Error(t, svc.RecordUsage(context.Background(), in))
			require.Zero(t, ordinary.calls, "never write a zero-price fallback after an atomic failure")
			require.Empty(t, repo.logs)
		})
	}
}

func TestGrokSafetyBillingSimpleModeKeepsNoDebit(t *testing.T) {
	svc, in, repo, ordinary := grokSafetyBillingFixture(t)
	svc.cfg.RunMode = config.RunModeSimple
	require.NoError(t, svc.RecordUsage(context.Background(), in))
	require.Len(t, repo.logs, 1)
	cmd := repo.commands[0]
	require.Zero(t, cmd.BalanceCost)
	require.Zero(t, cmd.SubscriptionCost)
	require.Zero(t, cmd.AccountQuotaCost)
	require.Zero(t, cmd.APIKeyQuotaCost)
	require.Zero(t, cmd.APIKeyRateLimitCost)
	require.Zero(t, cmd.PlatformQuotaCost)
	require.Zero(t, ordinary.calls)
}

func TestGrokSafetyBillingUsesSubscriptionAndFixedFeeWithZeroMultiplier(t *testing.T) {
	svc, in, repo, _ := grokSafetyBillingFixture(t)
	in.APIKey.Group.SubscriptionType = SubscriptionTypeSubscription
	in.APIKey.Group.RateMultiplier = 0
	in.Subscription = &UserSubscription{ID: 5}
	zero := 0.0
	in.Account.RateMultiplier = &zero
	require.NoError(t, svc.RecordUsage(context.Background(), in))
	cmd := repo.commands[0]
	require.Equal(t, 0.4, cmd.SubscriptionCost)
	require.Equal(t, 0.4, cmd.AccountQuotaCost)
	require.Zero(t, cmd.BalanceCost)
	require.Zero(t, cmd.PlatformQuotaCost)
}

func TestGrokSafetyBillingSkipsPerRequestPrice(t *testing.T) {
	svc, in, repo, _ := grokSafetyBillingFixture(t)
	unit := 500.0
	in.APIKey.Group.ModelPricing = []ChannelModelPricing{{Models: []string{"gpt-5.1"}, BillingMode: BillingModePerRequest, PerRequestPrice: &unit}}
	in.Result.Model = "gpt-5.1"
	expected := expectedOpenAICost(t, svc, in.Result.Model, in.Result.Usage, 2)
	require.NoError(t, svc.RecordUsage(context.Background(), in))
	require.InDelta(t, expected.ActualCost+0.4, repo.logs[0].ActualCost, 1e-12)
	require.Equal(t, string(BillingModeToken), *repo.logs[0].BillingMode)
}

func TestGrokSafetyFeeNeverMultiplied(t *testing.T) {
	for _, m := range []float64{0, 1, 2, 7} {
		cost := &CostBreakdown{InputCost: 2, TotalCost: 2.5, ActualCost: 6.5, FixedFee: 0.5}
		require.Equal(t, 2*m+0.5, accountUsageCost(cost, m))
		applyCostBreakdownMultiplier(cost, m)
		require.Equal(t, 2*m+0.5, cost.TotalCost)
		require.Equal(t, 6*m+0.5, cost.ActualCost)
		require.Equal(t, 0.5, cost.FixedFee)
	}
}

func TestGrokSafetyBillingEvidenceValidation(t *testing.T) {
	for _, bad := range []string{"platform", "code", "call_id", "negative", "infinite", "nan"} {
		t.Run(bad, func(t *testing.T) {
			svc, in, repo, _ := grokSafetyBillingFixture(t)
			switch bad {
			case "platform":
				in.Account.Platform = PlatformOpenAI
			case "code":
				in.Result.GrokSafetyRejection.Code = "different"
			case "call_id":
				in.Result.GrokSafetyRejection.CallID = " "
			case "negative":
				in.Result.GrokSafetyRejection.Fee = -1
			case "infinite":
				in.Result.GrokSafetyRejection.Fee = math.Inf(1)
			case "nan":
				in.Result.GrokSafetyRejection.Fee = math.NaN()
			}
			require.Error(t, svc.RecordUsage(context.Background(), in))
			require.Empty(t, repo.logs)
		})
	}
}

func TestGrokSafetyUsageSnapshotOwnsBoundedEvidence(t *testing.T) {
	_, in, _, _ := grokSafetyBillingFixture(t)
	large := strings.Repeat("not-retained", 1<<20) + "request-id"
	in.Result.GrokSafetyRejection.UpstreamRequestID = large[len(large)-10:]
	got := SnapshotOpenAIRecordUsageInput(in)
	require.NotSame(t, in.Result.GrokSafetyRejection, got.Result.GrokSafetyRejection)
	require.False(t, unsafe.StringData(in.Result.GrokSafetyRejection.UpstreamRequestID) == unsafe.StringData(got.Result.GrokSafetyRejection.UpstreamRequestID))
	in.Result.GrokSafetyRejection.CallID = "changed"
	in.Result.GrokSafetyRejection.Fee = 9
	require.Equal(t, "server-attempt-one", got.Result.GrokSafetyRejection.CallID)
	require.Equal(t, 0.4, got.Result.GrokSafetyRejection.Fee)
	require.Equal(t, "request-id", got.Result.GrokSafetyRejection.UpstreamRequestID)
}
