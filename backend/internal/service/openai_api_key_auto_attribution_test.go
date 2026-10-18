package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOpenAIAPIKeyAutoAttributionEligibility(t *testing.T) {
	now := time.Now()
	future := now.Add(time.Hour)
	for _, tc := range []struct {
		name   string
		change func(*Account)
		reason string
	}{
		{"enabled", func(*Account) {}, ""},
		{"explicit generic", func(a *Account) { a.Extra[OpenAIAPIKeyModeExtraKey] = "generic" }, ""},
		{"missing", func(a *Account) { delete(a.Extra, OpenAIAPIKeyAutoAttributionEnabledExtraKey) }, "unsupported_account"},
		{"disabled", func(a *Account) { a.Extra[OpenAIAPIKeyAutoAttributionEnabledExtraKey] = false }, "unsupported_account"},
		{"invalid boolean", func(a *Account) { a.Extra[OpenAIAPIKeyAutoAttributionEnabledExtraKey] = "true" }, "unsupported_account"},
		{"unknown mode", func(a *Account) { a.Extra[OpenAIAPIKeyModeExtraKey] = "future" }, "unsupported_account"},
		{"null mode", func(a *Account) { a.Extra[OpenAIAPIKeyModeExtraKey] = nil }, "unsupported_account"},
		{"engine on", func(a *Account) { a.Extra[OpenAIAPIKeyModeExtraKey] = "codex_engine" }, ""},
		{"engine missing", func(a *Account) {
			a.Extra[OpenAIAPIKeyModeExtraKey] = "codex_engine"
			delete(a.Extra, OpenAIAPIKeyAutoAttributionEnabledExtraKey)
		}, "unsupported_account"},
		{"engine off", func(a *Account) {
			a.Extra[OpenAIAPIKeyModeExtraKey] = "codex_engine"
			a.Extra[OpenAIAPIKeyAutoAttributionEnabledExtraKey] = false
		}, "unsupported_account"},
		{"engine invalid boolean", func(a *Account) {
			a.Extra[OpenAIAPIKeyModeExtraKey] = "codex_engine"
			a.Extra[OpenAIAPIKeyAutoAttributionEnabledExtraKey] = "true"
		}, "unsupported_account"},
		{"oauth off", func(a *Account) {
			a.Type = AccountTypeOAuth
			a.Extra[OpenAIAPIKeyAutoAttributionEnabledExtraKey] = false
		}, ""},
		{"inactive", func(a *Account) { a.Status = StatusDisabled }, "account_inactive"},
		{"scheduling disabled", func(a *Account) { a.Schedulable = false }, "scheduling_disabled"},
		{"rate limited", func(a *Account) { a.RateLimitResetAt = &future }, "account_rate_limited"},
		{"other platform", func(a *Account) { a.Platform = PlatformAnthropic }, "unsupported_account"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true,
				Extra: map[string]any{OpenAIAPIKeyAutoAttributionEnabledExtraKey: true}}
			tc.change(a)
			require.Equal(t, tc.reason, AttributionSkipReason(a, now, false))
			if a.Platform == PlatformOpenAI {
				require.Empty(t, AttributionSkipReason(a, now, true), "manual probes ignore automatic eligibility")
			}
		})
	}
	require.False(t, (*Account)(nil).IsOpenAIAPIKeyAutoAttributionEnabled())
}

func TestOpenAIAPIKeyAutoAttributionValidation(t *testing.T) {
	for _, value := range []any{nil, "true", 1, []any{}, map[string]any{}} {
		extra := map[string]any{OpenAIAPIKeyAutoAttributionEnabledExtraKey: value}
		require.Error(t, ValidateOpenAIAPIKeyAutoAttribution(PlatformOpenAI, AccountTypeAPIKey, extra))
		_, err := buildAccountForCreate(&CreateAccountInput{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, extra)
		require.Error(t, err)
	}
	for _, enabled := range []bool{false, true} {
		extra := map[string]any{OpenAIAPIKeyAutoAttributionEnabledExtraKey: enabled}
		require.NoError(t, ValidateOpenAIAPIKeyAutoAttribution(PlatformOpenAI, AccountTypeAPIKey, extra))
		require.Error(t, ValidateOpenAIAPIKeyAutoAttribution(PlatformOpenAI, AccountTypeOAuth, extra))
		require.Error(t, ValidateOpenAIAPIKeyAutoAttribution(PlatformAnthropic, AccountTypeAPIKey, extra))
		created, err := buildAccountForCreate(&CreateAccountInput{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, extra)
		require.NoError(t, err)
		require.Equal(t, enabled, created.Extra[OpenAIAPIKeyAutoAttributionEnabledExtraKey])
	}
}

func TestOpenAIAPIKeyAutoAttributionAdminUpdates(t *testing.T) {
	account := engineAccount()
	account.ID = 721
	account.Extra = map[string]any{OpenAIAPIKeyAutoAttributionEnabledExtraKey: true}
	repo := &upstreamBillingProbeAccountRepo{accounts: map[int64]*Account{account.ID: account}}
	svc := &adminServiceImpl{accountRepo: repo}
	updated, err := svc.UpdateAccount(context.Background(), account.ID, &UpdateAccountInput{Extra: map[string]any{"unrelated": "preserved"}})
	require.NoError(t, err)
	require.True(t, updated.IsOpenAIAPIKeyAutoAttributionEnabled())
	require.NoError(t, svc.UpdateAccountExtra(context.Background(), account.ID, map[string]any{OpenAIAPIKeyAutoAttributionEnabledExtraKey: false}))
	require.False(t, repo.accounts[account.ID].IsOpenAIAPIKeyAutoAttributionEnabled())
	for _, call := range []func() error{
		func() error {
			_, err := svc.UpdateAccount(context.Background(), account.ID, &UpdateAccountInput{Extra: map[string]any{OpenAIAPIKeyAutoAttributionEnabledExtraKey: "true"}})
			return err
		},
		func() error {
			return svc.UpdateAccountExtra(context.Background(), account.ID, map[string]any{OpenAIAPIKeyAutoAttributionEnabledExtraKey: "true"})
		},
		func() error {
			_, err := svc.BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{AccountIDs: []int64{account.ID}, Extra: map[string]any{OpenAIAPIKeyAutoAttributionEnabledExtraKey: "true"}})
			return err
		},
	} {
		require.Error(t, call())
	}
}

func TestOpenAIAPIKeyAutoAttributionPreferenceSurvivesModeChanges(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		account := engineAccount()
		account.ID = 721
		account.Extra = map[string]any{OpenAIAPIKeyModeExtraKey: "generic", OpenAIAPIKeyAutoAttributionEnabledExtraKey: enabled}
		repo := &upstreamBillingProbeAccountRepo{accounts: map[int64]*Account{account.ID: account}}
		svc := &adminServiceImpl{accountRepo: repo}
		for _, mode := range []string{"codex_engine", "generic"} {
			updated, err := svc.UpdateAccount(context.Background(), account.ID, &UpdateAccountInput{Extra: map[string]any{OpenAIAPIKeyModeExtraKey: mode}})
			require.NoError(t, err)
			require.Equal(t, mode, updated.Extra[OpenAIAPIKeyModeExtraKey])
			require.Equal(t, enabled, updated.Extra[OpenAIAPIKeyAutoAttributionEnabledExtraKey])
			require.Equal(t, enabled, updated.IsOpenAIAPIKeyAutoAttributionEnabled())
		}
	}
}

func TestOpenAIAPIKeyAutoAttributionPreservesAdminIntent(t *testing.T) {
	current := &Account{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Extra: map[string]any{OpenAIAPIKeyAutoAttributionEnabledExtraKey: true}}
	stale := *current
	stale.Extra = map[string]any{OpenAIAPIKeyAutoAttributionEnabledExtraKey: false}
	require.NoError(t, PreserveAccountConfiguration(current, &stale, AccountConfigurationIntent{}))
	require.True(t, stale.IsOpenAIAPIKeyAutoAttributionEnabled())
	ctx := withAccountConfigurationIntent(context.Background(), []int64{7}, map[string]any{OpenAIAPIKeyAutoAttributionEnabledExtraKey: false}, nil)
	require.NoError(t, PreserveAccountConfiguration(current, &stale, AccountConfigurationIntentFromContext(ctx, 7)))
	require.False(t, stale.IsOpenAIAPIKeyAutoAttributionEnabled())
	require.True(t, current.IsOpenAIAPIKeyAutoAttributionEnabled())
	stale.Type = AccountTypeOAuth
	require.NoError(t, PreserveAccountConfiguration(current, &stale, AccountConfigurationIntent{}))
	require.NotContains(t, stale.Extra, OpenAIAPIKeyAutoAttributionEnabledExtraKey)
}

type autoAttributionConfigurationRepo struct {
	*upstreamBillingProbeAccountRepo
	intent AccountConfigurationIntent
}

func (r *autoAttributionConfigurationRepo) Update(ctx context.Context, account *Account) error {
	current, err := r.GetByID(ctx, account.ID)
	if err != nil {
		return err
	}
	r.intent = AccountConfigurationIntentFromContext(ctx, account.ID)
	if err := PreserveAccountConfiguration(current, account, r.intent); err != nil {
		return err
	}
	return r.upstreamBillingProbeAccountRepo.Update(ctx, account)
}

func TestOpenAIAPIKeyAutoAttributionAccountServiceUpdateIntent(t *testing.T) {
	a := &Account{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Extra: map[string]any{OpenAIAPIKeyAutoAttributionEnabledExtraKey: false, "enable_tls_fingerprint": false}}
	repo := &autoAttributionConfigurationRepo{upstreamBillingProbeAccountRepo: &upstreamBillingProbeAccountRepo{accounts: map[int64]*Account{a.ID: a}}}
	svc := NewAccountService(repo, nil)
	extra := map[string]any{OpenAIAPIKeyAutoAttributionEnabledExtraKey: true, "enable_tls_fingerprint": true}
	updated, err := svc.Update(context.Background(), a.ID, UpdateAccountRequest{Extra: &extra})
	require.NoError(t, err)
	require.True(t, updated.IsOpenAIAPIKeyAutoAttributionEnabled())
	require.Equal(t, map[string]any{OpenAIAPIKeyAutoAttributionEnabledExtraKey: true}, repo.intent.Extra)
	require.Equal(t, false, updated.Extra["enable_tls_fingerprint"], "new update intent must not grant ownership of other managed settings")
	extra = map[string]any{"unrelated": true}
	updated, err = svc.Update(context.Background(), a.ID, UpdateAccountRequest{Extra: &extra})
	require.NoError(t, err)
	require.True(t, updated.IsOpenAIAPIKeyAutoAttributionEnabled())
	require.Empty(t, repo.intent.Extra)
}
