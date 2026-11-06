package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUsageRecordSnapshotTypedAccountOwnsGetterValues(t *testing.T) {
	for _, plan := range []any{"plus", "", json.Number("7"), float64(8), int64(9), 10, false, nil} {
		source := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
			Credentials: map[string]any{"plan_type": plan, "access_token": "secret"},
			Extra: map[string]any{OpenAIAPIKeyModeExtraKey: "codex_engine", openAILongContextBillingEnabledKey: true,
				"quota_limit": "12.5", "quota_used": json.Number("2.5"),
				"quota_notify_daily_enabled": true, "quota_notify_daily_threshold": 4.0,
				"quota_notify_daily_threshold_type": thresholdTypePercentage}}
		wantPlan := source.GetCredential("plan_type")
		first := snapshotUsageAccount(source)
		second := snapshotUsageAccount(first)
		require.NotSame(t, first.usageBillingValues, second.usageBillingValues)
		source.Credentials["plan_type"] = "changed"
		source.Extra["quota_limit"], source.Extra[OpenAIAPIKeyModeExtraKey] = 99.0, ""
		for _, frozen := range []*Account{first, second} {
			require.NotNil(t, frozen.usageBillingValues)
			require.Nil(t, frozen.Credentials)
			require.Nil(t, frozen.Extra)
			require.Equal(t, wantPlan, frozen.GetCredential("plan_type"))
			require.Empty(t, frozen.GetCredential("access_token"))
			require.True(t, frozen.IsCodexEngine())
			require.True(t, frozen.IsOpenAILongContextBillingEnabled())
			require.Equal(t, 12.5, frozen.GetQuotaLimit())
			require.Equal(t, 2.5, frozen.GetQuotaUsed())
			enabled, threshold, kind := frozen.QuotaNotifyConfig(quotaDimDaily)
			require.True(t, enabled)
			require.Equal(t, 4.0, threshold)
			require.Equal(t, thresholdTypePercentage, kind)
		}
		// Ordinary repository/request objects retain their existing mutable map contract.
		require.Nil(t, source.usageBillingValues)
		require.Equal(t, "changed", source.GetCredential("plan_type"))
		require.Equal(t, 99.0, source.GetQuotaLimit())
		require.False(t, source.IsCodexEngine())
	}
}

func TestUsageRecordSnapshotTypedShadowKeepsLiveParentSettings(t *testing.T) {
	for _, os := range []string{"", "linux"} {
		for _, plan := range []string{"", "plus"} {
			t.Run(os+"/plan="+plan, func(t *testing.T) {
				child := &Account{ID: 30, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
					ParentAccountID: usageSnapshotTestValue(int64(31)), OpenAIOAuthCredentialOS: os,
					OpenAIOAuthCredentialOwnerID: 31, OpenAIOAuthCredentialStateGeneration: "physical-state",
					OpenAIOAuthCredentialEpoch: "physical-epoch", Credentials: map[string]any{"plan_type": plan},
					Extra: map[string]any{"quota_limit": 50.0}}
				parent := &Account{ID: 31, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
					Credentials: map[string]any{"plan_type": "parent-old", "access_token": "parent-secret", "base_url": "current-config"},
					Extra:       map[string]any{openAILongContextBillingEnabledKey: false, "quota_limit": 100.0}}
				repo := &openAIRecordUsageAccountRepoStub{account: parent}
				frozen := snapshotUsageAccount(child)
				require.Zero(t, repo.calls)
				// These settings change after enqueueing; the original worker-time read must survive.
				parent.Extra[openAILongContextBillingEnabledKey], parent.Extra["quota_limit"] = true, 200.0
				parent.Credentials["plan_type"] = "parent-current"
				child.Credentials["plan_type"], child.Extra["quota_limit"] = "wrong-attempt", 999.0
				billing, err := resolveOpenAIUsageBillingAccount(context.Background(), repo, frozen)
				require.NoError(t, err)
				require.Equal(t, 1, repo.calls)
				require.Nil(t, billing.usageBillingValues, "the live parent must not inherit the child's typed backing")
				require.True(t, billing.IsOpenAILongContextBillingEnabled())
				require.Equal(t, 200.0, billing.GetQuotaLimit())
				require.Equal(t, 50.0, frozen.GetQuotaLimit())
				require.Equal(t, "current-config", billing.GetCredential("base_url"))
				if os == "" {
					require.Same(t, parent, billing)
					require.Equal(t, "parent-current", billing.GetCredential("plan_type"))
				} else {
					require.NotSame(t, parent, billing)
					require.Equal(t, plan, billing.GetCredential("plan_type"))
					require.Empty(t, billing.GetCredential("access_token"))
					require.Equal(t, "physical-state", billing.OpenAIOAuthCredentialStateGeneration)
					require.Equal(t, "physical-epoch", billing.OpenAIOAuthCredentialEpoch)
				}
				require.Equal(t, "parent-current", parent.GetCredential("plan_type"))
				require.Equal(t, "parent-secret", parent.GetCredential("access_token"))
			})
		}
	}
}
