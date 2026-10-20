package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type attributionCallbackRepository struct {
	AttributionRepository
	callback func(context.Context, int64)
}

func (r *attributionCallbackRepository) SetAccountChangedCallback(callback func(context.Context, int64)) {
	r.callback = callback
}

type attributionRefreshingAccounts struct {
	AccountRepository
	refreshed []int64
}

func (a *attributionRefreshingAccounts) RefreshAccountSchedulingSnapshot(_ context.Context, id int64) {
	a.refreshed = append(a.refreshed, id)
}

func TestAttributionConstructorWiresCommittedAccountRefresh(t *testing.T) {
	repo := &attributionCallbackRepository{}
	accounts := &attributionRefreshingAccounts{}
	service := NewModelAttributionService(repo, accounts, nil)
	require.NotNil(t, service)
	require.NotNil(t, repo.callback)
	repo.callback(context.Background(), 42)
	require.Equal(t, []int64{42}, accounts.refreshed)
	// Existing alternate repositories and account adapters need no new method.
	require.NotNil(t, NewModelAttributionService(nil, nil, nil))
}

func TestAttributionResultModeBaselineAndLegacy(t *testing.T) {
	snapshot := AttributionSnapshot{Version: 4, ResultMode: AttributionResultModeReplace, SchedulingRevision: 1, Policy: AttributionPolicy{Model: "probe", HighModels: []string{"high"}, LowModels: []string{"low"}}}
	baseline := AttributionPolicyDigest(snapshot)
	snapshot.ResultMode = AttributionResultModeSchedule
	require.Equal(t, AttributionResultModeSchedule, snapshot.EffectiveResultMode())
	require.NotEqual(t, baseline, AttributionPolicyDigest(snapshot), "result modes must never share a consecutive-pass baseline")
	schedule := AttributionPolicyDigest(snapshot)
	snapshot.SchedulingRevision++
	require.Equal(t, schedule, AttributionPolicyDigest(snapshot), "revision is an apply fence, not the policy; an attribution pause must not prevent later recovery")
	for _, version := range []int{0, 1, 2, 3} {
		snapshot.Version = version
		require.Equal(t, AttributionResultModeReplace, snapshot.EffectiveResultMode(), "legacy tasks retain replacement behavior")
		legacy := AttributionPolicyDigest(snapshot)
		snapshot.ResultMode = AttributionResultModeReplace
		require.Equal(t, legacy, AttributionPolicyDigest(snapshot))
		snapshot.ResultMode = AttributionResultModeSchedule
	}
	snapshot.Version, snapshot.ResultMode = 4, ""
	require.Equal(t, AttributionResultModeReplace, snapshot.EffectiveResultMode())
}

func TestAttributionScheduleModeAllowsPausedAutomaticAccounts(t *testing.T) {
	now := time.Now()
	for _, kind := range []string{AccountTypeOAuth, AccountTypeAPIKey} {
		for _, mode := range []string{"generic", "codex_engine"} {
			for _, value := range []any{nil, true, false, "false", 0} {
				a := &Account{Platform: PlatformOpenAI, Type: kind, Status: StatusActive, Schedulable: false, Extra: map[string]any{
					"openai_attribution_replace_whitelist_enabled": value,
					OpenAIAPIKeyModeExtraKey:                       mode, OpenAIAPIKeyAutoAttributionEnabledExtraKey: true,
				}}
				want := "scheduling_disabled"
				if value == false {
					want = ""
				}
				require.Equal(t, want, AttributionSkipReason(a, now, false), "kind=%s mode=%s value=%v", kind, mode, value)
				require.Empty(t, AttributionSkipReason(a, now, true), "manual eligibility is unchanged")
			}
		}
	}
	a := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Extra: map[string]any{"openai_attribution_replace_whitelist_enabled": false}}
	require.Equal(t, "unsupported_account", AttributionSkipReason(a, now, false), "pause mode does not opt API keys into automatic detection")
	a.Type, a.Status = AccountTypeOAuth, StatusDisabled
	require.Equal(t, "account_inactive", AttributionSkipReason(a, now, false))
	a.Status = StatusActive
	expires := now.Add(-time.Minute)
	a.ExpiresAt = &expires
	require.Equal(t, "account_expired", AttributionSkipReason(a, now, false))
}
