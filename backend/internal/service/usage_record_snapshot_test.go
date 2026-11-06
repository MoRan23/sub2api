package service

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func usageSnapshotTestValue[T any](v T) *T { return &v }

func usageSnapshotTestSubjects() (*APIKey, *User, *Account, *UserSubscription) {
	user := &User{ID: 20, Email: "u@example.com", Username: "billing user", Balance: 40, GiftBalance: 2,
		PasswordHash: "not-for-billing", BalanceNotifyEnabled: true, BalanceNotifyThreshold: usageSnapshotTestValue(4.0),
		BalanceNotifyThresholdType: "percentage", TotalRecharged: 100, BalanceNotifyExtraEmails: []NotifyEmailEntry{{Email: "extra@example.com", Verified: true}}}
	group := &Group{ID: 10, Platform: PlatformOpenAI, RateMultiplier: 1.5, FreeOpenAIFast: true,
		ImageRateMultiplier: 1, VideoRateMultiplier: 1, ImagePrice1K: usageSnapshotTestValue(0.1),
		VideoModelPrices: map[string]map[string]float64{"grok-imagine-video": {"480p": 0.05}}, SearchPricePer1k: usageSnapshotTestValue(5.0),
		AudioTTSPricePerMillionChars: usageSnapshotTestValue(7.0), WebSearchPricePerCall: usageSnapshotTestValue(0.02),
		ModelPricing: []ChannelModelPricing{{Models: []string{"gpt-4o"}, InputPrice: usageSnapshotTestValue(2.0), OutputPrice: usageSnapshotTestValue(8.0),
			FastMultiplier: usageSnapshotTestValue(2.0), ReasoningEffortMultipliers: map[string]float64{"high": 1.2},
			Intervals:   []PricingInterval{{MaxTokens: usageSnapshotTestValue(10000), InputPrice: usageSnapshotTestValue(3.0)}},
			TimePricing: &ChannelTimePricing{Timezone: "UTC", Periods: []ChannelTimePricingPeriod{{StartTime: "00:00", EndTime: "23:59", Multiplier: 1.1}}}}}}
	key := &APIKey{ID: 21, UserID: user.ID, Key: "auth-invalidation-key", GroupID: &group.ID, User: user, Group: group, Quota: 100, RateLimit5h: 10}
	account := &Account{ID: 30, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Name: "upstream", RateMultiplier: usageSnapshotTestValue(0.8),
		Credentials: map[string]any{"access_token": "not-for-billing", "plan_type": "plus"}, Extra: map[string]any{
			AccountExtraUpstreamRequestIDHeader: "X-Upstream-ID", "request_body": make([]byte, 4096),
			"quota_limit": 100.0, "quota_used": 80.0, "quota_daily_limit": 10.0, "quota_daily_used": 8.0,
			"quota_notify_daily_enabled": true, "quota_notify_daily_threshold": 20.0, "quota_notify_daily_threshold_type": "percentage"}}
	sub := &UserSubscription{ID: 40, UserID: user.ID, GroupID: group.ID, User: user, Group: group, QuotaEvents: []UserSubscriptionQuotaEvent{{ID: 999}}}
	return key, user, account, sub
}

func TestUsageRecordSnapshotOwnsMutableBillingValues(t *testing.T) {
	key, user, account, sub := usageSnapshotTestSubjects()
	tier, effort, firstToken := "priority", "high", 12
	result := &OpenAIForwardResult{RequestID: "upstream", Model: "gpt-4o", Usage: OpenAIUsage{InputTokens: 1000, OutputTokens: 100},
		ServiceTier: &tier, ReasoningEffort: &effort, FirstTokenMs: &firstToken,
		UpstreamHeaders: http.Header{"X-Upstream-Id": {"physical-request"}}, ResponseHeaders: http.Header{"X-Large": {"unused"}},
		ImageOutputSizes: []string{"1K"}, ImageSizeBreakdown: map[string]int{"1K": 1}, AudioUsage: &AudioUsage{Mode: "tts", DurationOrUnits: 0.5},
		wsReplayInput: []json.RawMessage{[]byte(`{"content":"request"}`)}, wsAccountFailoverReplayInput: []json.RawMessage{[]byte(`{"content":"response"}`)}}
	in := &OpenAIRecordUsageInput{Result: result, APIKey: key, User: user, Account: account, Subscription: sub, PricingAt: time.Unix(1700000000, 0)}
	got := SnapshotOpenAIRecordUsageInput(in)
	require.Same(t, got.User, got.APIKey.User, "shared source user should have one owned projection")
	require.NotSame(t, in.User, got.User)
	require.Equal(t, "physical-request", *got.upstreamRequestID)
	require.Nil(t, got.Result.UpstreamHeaders)
	require.Nil(t, got.Result.ResponseHeaders)
	require.Nil(t, got.Result.wsReplayInput)
	require.Nil(t, got.Result.wsAccountFailoverReplayInput)
	require.Empty(t, got.User.PasswordHash)
	require.Nil(t, got.Subscription.User)
	require.Nil(t, got.Subscription.Group)
	require.Nil(t, got.Subscription.QuotaEvents)
	require.NotContains(t, got.Account.Credentials, "access_token")
	require.NotContains(t, got.Account.Extra, "request_body")
	require.Equal(t, buildQuotaDims(account), buildQuotaDims(got.Account))
	require.Equal(t, account.BillingRateMultiplier(), got.Account.BillingRateMultiplier())
	require.Same(t, got.User, got.APIKey.User)
	require.Equal(t, in.PricingAt, got.PricingAt)

	tier, effort, firstToken = "flex", "low", 99
	result.Usage.InputTokens = 9999
	result.ImageOutputSizes[0], result.ImageSizeBreakdown["1K"] = "4K", 99
	result.AudioUsage.DurationOrUnits = 99
	result.UpstreamHeaders.Set("X-Upstream-ID", "reused-header")
	*key.GroupID = 999
	*key.Group.ModelPricing[0].InputPrice = 99
	*key.Group.ModelPricing[0].Intervals[0].MaxTokens = 999
	*key.Group.ModelPricing[0].Intervals[0].InputPrice = 99
	key.Group.ModelPricing[0].ReasoningEffortMultipliers["high"] = 99
	key.Group.ModelPricing[0].TimePricing.Periods[0].Multiplier = 99
	key.Group.VideoModelPrices["grok-imagine-video"]["480p"] = 99
	user.BalanceNotifyExtraEmails[0].Email = "changed@example.com"
	*user.BalanceNotifyThreshold = 99
	*account.RateMultiplier = 99
	sub.ID = 99
	require.Equal(t, "priority", *got.Result.ServiceTier)
	require.Equal(t, "high", *got.Result.ReasoningEffort)
	require.Equal(t, 12, *got.Result.FirstTokenMs)
	require.Equal(t, 1000, got.Result.Usage.InputTokens)
	require.Equal(t, []string{"1K"}, got.Result.ImageOutputSizes)
	require.Equal(t, 1, got.Result.ImageSizeBreakdown["1K"])
	require.Equal(t, 0.5, got.Result.AudioUsage.DurationOrUnits)
	require.EqualValues(t, 10, *got.APIKey.GroupID)
	require.Equal(t, 2.0, *got.APIKey.Group.ModelPricing[0].InputPrice)
	require.Equal(t, 10000, *got.APIKey.Group.ModelPricing[0].Intervals[0].MaxTokens)
	require.Equal(t, 3.0, *got.APIKey.Group.ModelPricing[0].Intervals[0].InputPrice)
	require.Equal(t, 1.2, got.APIKey.Group.ModelPricing[0].ReasoningEffortMultipliers["high"])
	require.Equal(t, 1.1, got.APIKey.Group.ModelPricing[0].TimePricing.Periods[0].Multiplier)
	require.Equal(t, 0.05, got.APIKey.Group.VideoModelPrices["grok-imagine-video"]["480p"])
	require.Equal(t, "extra@example.com", got.User.BalanceNotifyExtraEmails[0].Email)
	require.Equal(t, 4.0, *got.User.BalanceNotifyThreshold)
	require.Equal(t, 0.8, got.Account.BillingRateMultiplier())
	require.EqualValues(t, 40, got.Subscription.ID)
}

func TestUsageRecordSnapshotPreservesUserRelationships(t *testing.T) {
	for _, shared := range []bool{false, true} {
		user := &User{ID: 1, Email: "request@example.com"}
		keyUser := user
		if !shared {
			keyUser = &User{ID: 2, Email: "key@example.com"}
		}
		key := &APIKey{ID: 3, User: keyUser}
		gateway := SnapshotRecordUsageInput(&RecordUsageInput{User: user, APIKey: key})
		openAI := SnapshotOpenAIRecordUsageInput(&OpenAIRecordUsageInput{User: user, APIKey: key})
		for _, pair := range [][2]*User{{gateway.User, gateway.APIKey.User}, {openAI.User, openAI.APIKey.User}} {
			require.NotSame(t, user, pair[0])
			require.NotSame(t, keyUser, pair[1])
			if shared {
				require.Same(t, pair[0], pair[1])
			} else {
				require.NotSame(t, pair[0], pair[1])
			}
			require.Equal(t, keyUser.ID, pair[1].ID)
			require.Equal(t, keyUser.Email, pair[1].Email)
		}
		user.Email, keyUser.Email = "changed-request", "changed-key"
		require.Equal(t, "request@example.com", gateway.User.Email)
		require.Equal(t, "request@example.com", openAI.User.Email)
	}
}

func TestUsageRecordSnapshotAccountSparseExtraMatchesGetters(t *testing.T) {
	floatKeys := []string{
		"quota_limit", "quota_used", "quota_daily_limit", "quota_daily_used", "quota_weekly_limit", "quota_weekly_used",
		"quota_notify_daily_threshold", "quota_notify_weekly_threshold", "quota_notify_total_threshold",
	}
	for _, test := range []struct {
		name  string
		value any
	}{
		{"nil", nil}, {"zero", float64(0)}, {"false", false}, {"malformed", []byte("not numeric")},
		{"int", 12}, {"int64", int64(13)}, {"float32", float32(1.5)}, {"float64", 2.25},
		{"json_number", json.Number("3.5")}, {"numeric_string", " 4.75 "}, {"invalid_string", "bad"},
		{"negative", -2.0}, {"negative_zero", math.Copysign(0, -1)}, {"string_negative_zero", "-0"},
		{"nan", math.Float64frombits(0x7ff8000000000042)}, {"positive_inf", math.Inf(1)}, {"negative_inf", math.Inf(-1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Extra: make(map[string]any)}
			for _, key := range floatKeys {
				source.Extra[key] = test.value
			}
			got := snapshotUsageAccount(source)
			for _, key := range floatKeys {
				require.Equal(t, math.Float64bits(source.getExtraFloat64(key)), math.Float64bits(got.getExtraFloat64(key)), key)
			}
			if value := source.getExtraFloat64(floatKeys[0]); value == 0 && !math.Signbit(value) {
				require.Nil(t, got.Extra, "default numeric values should allocate no Extra map")
			}
			source.Extra[floatKeys[0]] = 1234.0
			require.NotEqual(t, 1234.0, got.getExtraFloat64(floatKeys[0]))
		})
	}
	for _, platform := range []string{PlatformOpenAI, PlatformAnthropic, PlatformGrok} {
		for _, accountType := range []string{AccountTypeAPIKey, AccountTypeOAuth, AccountTypeSetupToken} {
			source := &Account{Platform: platform, Type: accountType, Extra: map[string]any{
				OpenAIAPIKeyModeExtraKey: "codex_engine", openAILongContextBillingEnabledKey: true,
				"cache_ttl_override_enabled": true, "cache_ttl_override_target": "1h",
				"quota_notify_daily_enabled": true, "quota_notify_daily_threshold_type": thresholdTypePercentage,
				"quota_notify_weekly_enabled": false, "quota_notify_weekly_threshold_type": "custom-preserved",
				"quota_notify_total_enabled": "true", "quota_notify_total_threshold_type": "",
			}}
			got := snapshotUsageAccount(source)
			require.Equal(t, source.IsCodexEngine(), got.IsCodexEngine())
			require.Equal(t, source.IsOpenAILongContextBillingEnabled(), got.IsOpenAILongContextBillingEnabled())
			require.Equal(t, source.IsCacheTTLOverrideEnabled(), got.IsCacheTTLOverrideEnabled())
			require.Equal(t, source.GetCacheTTLOverrideTarget(), got.GetCacheTTLOverrideTarget())
			for _, dim := range []string{quotaDimDaily, quotaDimWeekly, quotaDimTotal} {
				wantEnabled, wantThreshold, wantType := source.QuotaNotifyConfig(dim)
				gotEnabled, gotThreshold, gotType := got.QuotaNotifyConfig(dim)
				require.Equal(t, wantEnabled, gotEnabled)
				require.Equal(t, wantThreshold, gotThreshold)
				require.Equal(t, wantType, gotType)
			}
		}
	}
	// Empty/malformed/default controls retain their getter defaults with no map.
	for _, extra := range []map[string]any{nil, {}, {
		OpenAIAPIKeyModeExtraKey: 7, openAILongContextBillingEnabledKey: "true",
		"cache_ttl_override_enabled": false, "cache_ttl_override_target": "unsupported",
		"quota_notify_daily_threshold_type": thresholdTypeFixed,
	}} {
		source := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Extra: extra}
		got := snapshotUsageAccount(source)
		require.Nil(t, got.Extra)
		require.Equal(t, source.GetCacheTTLOverrideTarget(), got.GetCacheTTLOverrideTarget())
	}
}

func TestUsageRecordSnapshotAccountSparseStringsOwnStorage(t *testing.T) {
	backing := strings.Repeat("large-request-", 10000) + "codex_enginepercentage1h"
	modeStart := len(backing) - len("codex_enginepercentage1h")
	source := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Extra: map[string]any{
		OpenAIAPIKeyModeExtraKey:            backing[modeStart : modeStart+len("codex_engine")],
		"quota_notify_daily_threshold_type": backing[modeStart+len("codex_engine") : len(backing)-2],
		"cache_ttl_override_target":         backing[len(backing)-2:],
	}}
	got := snapshotUsageAccount(source)
	require.True(t, got.IsCodexEngine())
	require.False(t, unsafe.StringData(source.GetExtraString(OpenAIAPIKeyModeExtraKey)) == unsafe.StringData(got.GetExtraString(OpenAIAPIKeyModeExtraKey)))
	_, _, thresholdType := got.QuotaNotifyConfig(quotaDimDaily)
	require.Equal(t, thresholdTypePercentage, thresholdType)
	require.False(t, unsafe.StringData(source.GetExtraString("quota_notify_daily_threshold_type")) == unsafe.StringData(thresholdType))
	require.Equal(t, "1h", got.GetCacheTTLOverrideTarget())
	require.False(t, unsafe.StringData(source.GetCacheTTLOverrideTarget()) == unsafe.StringData(got.GetCacheTTLOverrideTarget()))
	source.Extra[OpenAIAPIKeyModeExtraKey], source.Extra["quota_notify_daily_threshold_type"] = "", ""
	require.True(t, got.IsCodexEngine())
	_, _, thresholdType = got.QuotaNotifyConfig(quotaDimDaily)
	require.Equal(t, thresholdTypePercentage, thresholdType)
}

func TestUsageRecordSnapshotPreservesBillingResults(t *testing.T) {
	for _, kind := range []string{"openai", "image", "search", "audio", "video", "subscription", "simple", "anthropic", "gemini", "systemone"} {
		t.Run(kind, func(t *testing.T) {
			var logs []*UsageLog
			var commands []*UsageBillingCommand
			for _, frozen := range []bool{false, true} {
				key, user, account, sub := usageSnapshotTestSubjects()
				usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
				billingRepo := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: false}}
				if kind == "subscription" {
					key.Group.SubscriptionType = SubscriptionTypeSubscription
				} else {
					sub = nil
				}
				if kind == "anthropic" || kind == "gemini" || kind == "systemone" {
					account.Platform, account.Type, key.Group.Platform = PlatformAnthropic, AccountTypeOAuth, PlatformAnthropic
					account.Extra["cache_ttl_override_enabled"], account.Extra["cache_ttl_override_target"] = true, "1h"
					cfg := &config.Config{}
					cfg.Default.RateMultiplier = 1.1
					svc := &GatewayService{cfg: cfg, usageLogRepo: usageRepo, usageBillingRepo: billingRepo,
						billingService: NewBillingService(cfg, nil), deferredService: &DeferredService{}, billingCacheService: &BillingCacheService{}}
					in := &RecordUsageInput{APIKey: key, User: user, Account: account, Subscription: sub, APIKeyService: &openAIRecordUsageAPIKeyQuotaStub{},
						Result: &ForwardResult{RequestID: "stable", Model: "claude-sonnet-4-5", Usage: ClaudeUsage{InputTokens: 1000, OutputTokens: 100, CacheCreationInputTokens: 20},
							UpstreamHeaders: http.Header{"X-Upstream-Id": {"physical"}}, Duration: time.Second}, ForceCacheBilling: true}
					if kind == "gemini" {
						account.Platform, account.Type, key.Group.Platform, in.Result.Model = PlatformGemini, AccountTypeAPIKey, PlatformGemini, "gemini-2.5-pro"
						svc.billingService = NewBillingService(cfg, &PricingService{pricingData: map[string]*LiteLLMModelPricing{
							"gemini-2.5-pro": {Mode: "chat", InputCostPerToken: 1.25e-6, OutputCostPerToken: 10e-6, CacheReadInputTokenCost: 0.3125e-6},
						}})
					}
					if kind == "systemone" {
						account.Platform, account.Type, key.Group.Platform = PlatformTypeSafe, AccountTypeAPIKey, PlatformTypeSafe
					}
					if frozen {
						in = SnapshotRecordUsageInput(in)
					}
					require.NoError(t, svc.RecordUsage(context.Background(), in))
				} else {
					svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
					if kind == "simple" {
						svc.cfg.RunMode, svc.cfg.SimpleModeKeyRateLimitEnabled = config.RunModeSimple, true
					}
					svc.resolver = NewModelPricingResolver(nil, svc.billingService)
					result := &OpenAIForwardResult{RequestID: "stable", Model: "gpt-4o", Usage: OpenAIUsage{InputTokens: 1000, OutputTokens: 100}, Duration: time.Second,
						UpstreamHeaders: http.Header{"X-Upstream-Id": {"physical"}}}
					switch kind {
					case "image":
						result.ImageCount, result.ImageEndpoint, result.ImageSize, key.Group.ImageCountBilling = 2, true, "1K", true
					case "search":
						result.SearchCount = 2
					case "audio":
						result.AudioUsage = &AudioUsage{Mode: "tts", DurationOrUnits: 0.5}
					case "video":
						result.Model, result.VideoCount, result.VideoResolution, result.VideoDurationSeconds = "grok-imagine-video", 1, "480p", 8
					}
					in := &OpenAIRecordUsageInput{Result: result, APIKey: key, User: user, Account: account, Subscription: sub, APIKeyService: &openAIRecordUsageAPIKeyQuotaStub{}}
					if frozen {
						in = SnapshotOpenAIRecordUsageInput(in)
					}
					require.NoError(t, svc.RecordUsage(context.Background(), in))
				}
				require.NotNil(t, usageRepo.lastLog)
				require.NotNil(t, billingRepo.lastCmd)
				usageRepo.lastLog.CreatedAt = time.Time{}
				logs = append(logs, usageRepo.lastLog)
				commands = append(commands, billingRepo.lastCmd)
			}
			require.Equal(t, logs[0], logs[1])
			require.Equal(t, commands[0], commands[1])
			require.Greater(t, logs[0].TotalCost, 0.0)
		})
	}
}

func TestUsageRecordSnapshotKeepsEngineExclusiveSettlement(t *testing.T) {
	in := SnapshotOpenAIRecordUsageInput(&OpenAIRecordUsageInput{
		Result:  &OpenAIForwardResult{Usage: OpenAIUsage{InputTokens: 100}},
		Account: &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Extra: map[string]any{OpenAIAPIKeyModeExtraKey: "codex_engine"}},
	})
	require.True(t, in.Account.IsCodexEngine())
	require.NoError(t, (&OpenAIGatewayService{}).RecordUsage(context.Background(), in))
}

func TestUsageRecordSnapshotKeepsWorkerTimeLookups(t *testing.T) {
	key, user, account, _ := usageSnapshotTestSubjects()
	account.Type, account.ParentAccountID, account.OpenAIOAuthCredentialOS = AccountTypeOAuth, usageSnapshotTestValue(int64(31)), "linux"
	parent := &Account{ID: 31, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	accountRepo := &openAIRecordUsageAccountRepoStub{account: parent}
	rateRepo := &openAIUserGroupRateRepoStub{rate: usageSnapshotTestValue(1.0)}
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(usageRepo, &openAIRecordUsageBillingRepoStub{}, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, rateRepo)
	svc.accountRepo = accountRepo
	in := SnapshotOpenAIRecordUsageInput(&OpenAIRecordUsageInput{APIKey: key, User: user, Account: account,
		Result: &OpenAIForwardResult{RequestID: "late-pricing", Model: "gpt-4o", Usage: OpenAIUsage{InputTokens: 100, OutputTokens: 10}}})
	require.Zero(t, rateRepo.calls)
	require.Zero(t, accountRepo.calls)
	rateRepo.rate = usageSnapshotTestValue(3.0)
	parent.Extra = map[string]any{openAILongContextBillingEnabledKey: true}
	require.NoError(t, svc.RecordUsage(context.Background(), in))
	require.Equal(t, 1, accountRepo.calls)
	require.Equal(t, 1, rateRepo.calls)
	require.Equal(t, 3.0, usageRepo.lastLog.RateMultiplier)
}
