package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOpenAIFreeServiceTierPolicyIndependentFlags(t *testing.T) {
	for _, flags := range []struct{ fast, ultrafast bool }{{}, {true, false}, {false, true}, {true, true}} {
		for _, tier := range []string{"priority", "fast", "ultrafast", "default", "flex", "", "unknown"} {
			expected := (flags.fast && (tier == "priority" || tier == "fast")) || (flags.ultrafast && tier == "ultrafast")
			require.Equal(t, expected, openAIServiceTierBillsAtStandard(flags.fast, flags.ultrafast, tier), "flags=%+v tier=%s", flags, tier)
		}
	}
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Extra: map[string]any{FreeOpenAIFastExtraKey: true}}
	fast, ultrafast := effectiveOpenAIFreeServiceTiers(nil, account)
	require.True(t, fast, "account setting works without a group")
	require.False(t, ultrafast)
	fast, ultrafast = effectiveOpenAIFreeServiceTiers(&APIKey{Group: &Group{Platform: PlatformComposite, FreeOpenAIUltrafast: true}}, account)
	require.True(t, fast)
	require.True(t, ultrafast)
	fast, ultrafast = effectiveOpenAIFreeServiceTiers(&APIKey{Group: &Group{Platform: PlatformAnthropic, FreeOpenAIUltrafast: true}}, account)
	require.True(t, fast)
	require.False(t, ultrafast, "unsupported group does not grant free acceleration")
}

func TestOpenAIRecordUsageFreeAccelerationUsesBusinessAccount(t *testing.T) {
	for _, tc := range []struct {
		name, tier                                                                 string
		accountFast, accountUltra, groupFast, groupUltra, shadow, parentFree, free bool
	}{
		{name: "account fast", tier: "priority", accountFast: true, free: true},
		{name: "account ultrafast", tier: "ultrafast", accountUltra: true, free: true},
		{name: "group ultrafast", tier: "ultrafast", groupUltra: true, free: true},
		{name: "free fast excludes ultrafast", tier: "ultrafast", accountFast: true, groupFast: true},
		{name: "free ultrafast excludes fast", tier: "priority", accountUltra: true, groupUltra: true},
		{name: "shadow keeps preference", tier: "ultrafast", accountUltra: true, shadow: true, free: true},
		{name: "shadow excludes parent preference", tier: "ultrafast", shadow: true, parentFree: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
			svc := newOpenAIRecordUsageServiceForTest(usageRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
			svc.resolver = NewModelPricingResolver(nil, svc.billingService)
			groupID, parentID := int64(77), int64(909)
			inputPrice, outputPrice, fastMultiplier := .001, .002, 3.0
			account := &Account{ID: 3010, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{
				FreeOpenAIFastExtraKey: tc.accountFast, FreeOpenAIUltrafastExtraKey: tc.accountUltra,
			}}
			if tc.shadow {
				account.ParentAccountID = &parentID
				svc.accountRepo = &openAIRecordUsageAccountRepoStub{account: &Account{ID: parentID, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
					Extra: map[string]any{FreeOpenAIFastExtraKey: tc.parentFree, FreeOpenAIUltrafastExtraKey: tc.parentFree}}}
			}
			input := &OpenAIRecordUsageInput{
				Result: &OpenAIForwardResult{RequestID: "free-tier", Model: "gpt-6-astra", ServiceTier: &tc.tier,
					UpstreamResponseServiceTier: "default", Usage: OpenAIUsage{InputTokens: 100, OutputTokens: 50}, Duration: time.Second},
				Account: account, User: &User{ID: 2020}, APIKey: &APIKey{ID: 1020, GroupID: &groupID, Group: &Group{
					ID: groupID, Platform: PlatformComposite, Status: StatusActive, Hydrated: true, RateMultiplier: .5,
					FreeOpenAIFast: tc.groupFast, FreeOpenAIUltrafast: tc.groupUltra,
					ModelPricing: []ChannelModelPricing{{Models: []string{"gpt-6-astra"}, BillingMode: BillingModeToken,
						InputPrice: &inputPrice, OutputPrice: &outputPrice, FastMultiplier: &fastMultiplier}},
				}},
			}
			frozen := SnapshotOpenAIRecordUsageInput(input)
			account.Extra[FreeOpenAIFastExtraKey], account.Extra[FreeOpenAIUltrafastExtraKey] = !tc.accountFast, !tc.accountUltra
			input.APIKey.Group.FreeOpenAIFast, input.APIKey.Group.FreeOpenAIUltrafast = !tc.groupFast, !tc.groupUltra
			require.NoError(t, svc.RecordUsage(context.Background(), frozen))
			require.NotNil(t, usageRepo.lastLog)
			multiplier := 3.0
			if tc.tier == "ultrafast" {
				multiplier = 6
			}
			standardTotal := 100*inputPrice + 50*outputPrice
			require.InDelta(t, standardTotal*multiplier, usageRepo.lastLog.TotalCost, 1e-10)
			customer := standardTotal * multiplier * .5
			if tc.free {
				customer = standardTotal * .5
			}
			require.InDelta(t, customer, usageRepo.lastLog.ActualCost, 1e-10)
			require.Equal(t, tc.tier, *usageRepo.lastLog.ServiceTier)
			require.Nil(t, frozen.Account.Extra, "queued billing must not retain the extra map")
		})
	}
}

func TestCodexEngineFreeUltrafastSnapshotAndUpstreamCost(t *testing.T) {
	for _, fromGroup := range []bool{false, true} {
		price := &ModelPricing{InputPricePerToken: .1, OutputPricePerToken: .2, CacheReadPricePerToken: .01}
		svc := &OpenAIGatewayService{billingService: &BillingService{fallbackPrices: map[string]*ModelPricing{"gpt-6-astra": price}}}
		account := engineReceiptTestAccount()
		account.Extra[FreeOpenAIFastExtraKey] = !fromGroup
		account.Extra[FreeOpenAIUltrafastExtraKey] = !fromGroup
		input := &OpenAIRecordUsageInput{Account: account, User: &User{ID: 1}, APIKey: &APIKey{ID: 2,
			Group: &Group{Platform: PlatformComposite, FreeOpenAIFast: fromGroup, FreeOpenAIUltrafast: fromGroup}},
			Result: &OpenAIForwardResult{Model: "gpt-6-astra"}}
		raw, err := svc.CaptureCodexEngineBillingSnapshot(context.Background(), input)
		require.NoError(t, err)
		f, err := decodeCodexEnginePricingSnapshot(raw)
		require.NoError(t, err)
		require.True(t, f.FreeFast)
		require.True(t, f.FreeUltrafast)
		input.Account.Extra[FreeOpenAIUltrafastExtraKey] = false
		input.APIKey.Group.FreeOpenAIUltrafast = false
		require.True(t, f.FreeUltrafast, "request policy is frozen before settlement")
		price.InputPricePerToken = .2
		job := &CodexEngineBillingJob{CodexEngineBillingPending: CodexEngineBillingPending{UserID: 1, APIKeyID: 2, AccountID: account.ID, Snapshot: raw}}
		event := engineFrozenTestModelEvent()
		event.Model, event.ServiceTier = "gpt-6-astra", "ultrafast"
		_, log, err := svc.BuildCodexEngineBillingEvent(context.Background(), job, event)
		require.NoError(t, err)
		require.InDelta(t, 18.2*6, log.TotalCost, 1e-9)
		require.InDelta(t, 18.2, log.ActualCost, 1e-9, "new snapshots freeze free flags but still resolve settlement-time prices")
	}
	job, f := engineFrozenTestSnapshot(t)
	p := f.Prices["gpt-engine-fixture"]
	p.Resolved.BasePricing.UltrafastMultiplier = 6
	f.Prices["gpt-engine-fixture"] = p
	event := engineFrozenTestModelEvent()
	event.ServiceTier = "ultrafast"
	f.FreeUltrafast = true
	var err error
	job.Snapshot, err = json.Marshal(f)
	require.NoError(t, err)
	cmd, log, err := (&OpenAIGatewayService{}).BuildCodexEngineBillingEvent(context.Background(), job, event)
	require.NoError(t, err)
	require.InDelta(t, 10.2*6, log.TotalCost, 1e-9)
	require.InDelta(t, 10.2*2, cmd.BalanceCost, 1e-9)
	require.InDelta(t, 10.2*6*3, cmd.AccountQuotaCost, 1e-9)
	require.Equal(t, "ultrafast", *log.ServiceTier)
	f.FreeUltrafast, f.FreeFast = false, true
	job.Snapshot, err = json.Marshal(f)
	require.NoError(t, err)
	_, log, err = (&OpenAIGatewayService{}).BuildCodexEngineBillingEvent(context.Background(), job, event)
	require.NoError(t, err)
	require.InDelta(t, 10.2*6*2, log.ActualCost, 1e-9, "free Fast must not subsidize Ultrafast")
	var old codexEnginePricingSnapshot
	require.NoError(t, json.Unmarshal([]byte(`{"free_fast":true}`), &old))
	require.False(t, old.FreeUltrafast, "old persisted snapshots do not enable the new policy")
}
