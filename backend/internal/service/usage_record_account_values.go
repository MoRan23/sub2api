package service

// usageAccountBillingValues is installed only on an owned billing projection.
// It keeps the existing Account getter contract without allocating generic
// credentials/Extra maps or boxing their scalar values for each queued task.
// Repository, forwarding and scheduling accounts leave the pointer nil.
type usageAccountBillingValues struct {
	planType      string
	accessMode    string
	longContext   bool
	freeFast      bool
	freeUltrafast bool
	cacheTTL      bool
	cacheTarget   string
	quota         [6]float64 // total limit/used, daily limit/used, weekly limit/used
	notify        [3]usageAccountNotifyValues
}

type usageAccountNotifyValues struct {
	enabled   bool
	threshold float64
	kind      string
}

func (v *usageAccountBillingValues) extraString(key string) string {
	switch key {
	case OpenAIAPIKeyModeExtraKey:
		return v.accessMode
	case "cache_ttl_override_target":
		if v.cacheTarget == "1h" {
			return "1h"
		}
	case "quota_notify_daily_threshold_type":
		return v.notify[0].extraKind()
	case "quota_notify_weekly_threshold_type":
		return v.notify[1].extraKind()
	case "quota_notify_total_threshold_type":
		return v.notify[2].extraKind()
	}
	return ""
}

func (v usageAccountNotifyValues) extraKind() string {
	if v.kind == thresholdTypeFixed {
		return ""
	}
	return v.kind
}

func (v *usageAccountBillingValues) extraFloat64(key string) float64 {
	switch key {
	case "quota_limit":
		return v.quota[0]
	case "quota_used":
		return v.quota[1]
	case "quota_daily_limit":
		return v.quota[2]
	case "quota_daily_used":
		return v.quota[3]
	case "quota_weekly_limit":
		return v.quota[4]
	case "quota_weekly_used":
		return v.quota[5]
	case "quota_notify_daily_threshold":
		return v.notify[0].threshold
	case "quota_notify_weekly_threshold":
		return v.notify[1].threshold
	case "quota_notify_total_threshold":
		return v.notify[2].threshold
	}
	return 0
}

func (v *usageAccountBillingValues) extraBool(key string) bool {
	switch key {
	case openAILongContextBillingEnabledKey:
		return v.longContext
	case FreeOpenAIFastExtraKey:
		return v.freeFast
	case FreeOpenAIUltrafastExtraKey:
		return v.freeUltrafast
	case "cache_ttl_override_enabled":
		return v.cacheTTL
	case "quota_notify_daily_enabled":
		return v.notify[0].enabled
	case "quota_notify_weekly_enabled":
		return v.notify[1].enabled
	case "quota_notify_total_enabled":
		return v.notify[2].enabled
	}
	return false
}

func (b *usageSnapshotBuilder) accountValues(a *Account) *usageAccountBillingValues {
	values := &b.storage.account
	*values = usageAccountBillingValues{
		planType: b.string(a.GetCredential("plan_type")), accessMode: b.string(a.GetExtraString(OpenAIAPIKeyModeExtraKey)),
		longContext: a.IsOpenAILongContextBillingEnabled(), cacheTTL: a.IsCacheTTLOverrideEnabled(),
		freeFast: a.IsFreeOpenAIFastEnabled(), freeUltrafast: a.IsFreeOpenAIUltrafastEnabled(),
		cacheTarget: "5m",
		quota:       [6]float64{a.GetQuotaLimit(), a.GetQuotaUsed(), a.GetQuotaDailyLimit(), a.GetQuotaDailyUsed(), a.GetQuotaWeeklyLimit(), a.GetQuotaWeeklyUsed()},
		notify: [3]usageAccountNotifyValues{
			b.accountNotify(a, "quota_notify_daily_enabled", "quota_notify_daily_threshold", "quota_notify_daily_threshold_type"),
			b.accountNotify(a, "quota_notify_weekly_enabled", "quota_notify_weekly_threshold", "quota_notify_weekly_threshold_type"),
			b.accountNotify(a, "quota_notify_total_enabled", "quota_notify_total_threshold", "quota_notify_total_threshold_type"),
		},
	}
	if a.GetCacheTTLOverrideTarget() == "1h" {
		values.cacheTarget = "1h"
	}
	return values
}

func (b *usageSnapshotBuilder) accountNotify(a *Account, enabled, threshold, kind string) usageAccountNotifyValues {
	thresholdType := a.getExtraStringDefault(kind, thresholdTypeFixed)
	if thresholdType != thresholdTypeFixed {
		thresholdType = b.string(thresholdType)
	}
	return usageAccountNotifyValues{enabled: a.getExtraBool(enabled), threshold: a.getExtraFloat64(threshold), kind: thresholdType}
}

// A shadow still loads current parent settings in RecordUsage. Only this small
// physical-provider plan is materialized for its existing credential merge.
func usageAccountProviderCredentials(account *Account) map[string]any {
	if account.usageBillingValues == nil {
		return account.Credentials
	}
	if plan := account.usageBillingValues.planType; plan != "" {
		return map[string]any{"plan_type": plan}
	}
	return nil
}
