package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenAIAttributionResultModeDefaultsAndValidation(t *testing.T) {
	require.True(t, (*Account)(nil).OpenAIAttributionReplacesWhitelist())
	for _, tc := range []struct {
		name  string
		extra map[string]any
		want  bool
	}{
		{"missing", nil, true},
		{"enabled", map[string]any{OpenAIAttributionReplaceWhitelistEnabledExtraKey: true}, true},
		{"disabled", map[string]any{OpenAIAttributionReplaceWhitelistEnabledExtraKey: false}, false},
		{"legacy null", map[string]any{OpenAIAttributionReplaceWhitelistEnabledExtraKey: nil}, true},
		{"legacy string", map[string]any{OpenAIAttributionReplaceWhitelistEnabledExtraKey: "false"}, true},
		{"legacy number", map[string]any{OpenAIAttributionReplaceWhitelistEnabledExtraKey: 0}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Extra: tc.extra}
			require.Equal(t, tc.want, a.OpenAIAttributionReplacesWhitelist())
			require.Equal(t, tc.extra != nil, HasOpenAIAttributionResultMode(tc.extra))
		})
	}
	for _, kind := range []string{AccountTypeOAuth, AccountTypeAPIKey} {
		for _, enabled := range []bool{true, false} {
			extra := map[string]any{OpenAIAttributionReplaceWhitelistEnabledExtraKey: enabled}
			require.NoError(t, ValidateOpenAIAttributionResultMode(PlatformOpenAI, kind, extra))
			created, err := buildAccountForCreate(&CreateAccountInput{Platform: PlatformOpenAI, Type: kind}, extra)
			require.NoError(t, err)
			require.Equal(t, enabled, created.OpenAIAttributionReplacesWhitelist())
		}
	}
	for _, value := range []any{nil, "false", 0, []any{}, map[string]any{}} {
		extra := map[string]any{OpenAIAttributionReplaceWhitelistEnabledExtraKey: value}
		require.Error(t, ValidateOpenAIAttributionResultMode(PlatformOpenAI, AccountTypeOAuth, extra))
		_, err := buildAccountForCreate(&CreateAccountInput{Platform: PlatformOpenAI, Type: AccountTypeOAuth}, extra)
		require.Error(t, err, "new writes must reject malformed values even though historical reads preserve legacy mode")
	}
	for _, pair := range [][2]string{{PlatformOpenAI, AccountTypeSetupToken}, {PlatformAnthropic, AccountTypeOAuth}, {PlatformGemini, AccountTypeAPIKey}} {
		require.NoError(t, ValidateOpenAIAttributionResultMode(pair[0], pair[1], nil))
		require.Error(t, ValidateOpenAIAttributionResultMode(pair[0], pair[1], map[string]any{OpenAIAttributionReplaceWhitelistEnabledExtraKey: false}))
	}
}

func TestOpenAIAttributionResultModePreservesManagedIntent(t *testing.T) {
	for _, kind := range []string{AccountTypeOAuth, AccountTypeAPIKey} {
		current := &Account{ID: 71, Platform: PlatformOpenAI, Type: kind, Extra: map[string]any{OpenAIAttributionReplaceWhitelistEnabledExtraKey: false}}
		stale := *current
		stale.Extra = map[string]any{OpenAIAttributionReplaceWhitelistEnabledExtraKey: true}
		require.NoError(t, PreserveAccountConfiguration(current, &stale, AccountConfigurationIntent{}))
		require.False(t, stale.OpenAIAttributionReplacesWhitelist())
		ctx := withAccountConfigurationIntent(context.Background(), []int64{current.ID}, map[string]any{OpenAIAttributionReplaceWhitelistEnabledExtraKey: true}, nil)
		require.NoError(t, PreserveAccountConfiguration(current, &stale, AccountConfigurationIntentFromContext(ctx, current.ID+1)))
		require.False(t, stale.OpenAIAttributionReplacesWhitelist(), "intent is scoped to its target account")
		require.NoError(t, PreserveAccountConfiguration(current, &stale, AccountConfigurationIntentFromContext(ctx, current.ID)))
		require.True(t, stale.OpenAIAttributionReplacesWhitelist())
		require.False(t, current.OpenAIAttributionReplacesWhitelist(), "preserving a snapshot must not mutate its source")
		stale.Platform = PlatformAnthropic
		require.NoError(t, PreserveAccountConfiguration(current, &stale, AccountConfigurationIntent{}))
		require.NotContains(t, stale.Extra, OpenAIAttributionReplaceWhitelistEnabledExtraKey)
	}
}

func TestOpenAIAttributionResultModeAdminUpdateValidation(t *testing.T) {
	a := engineAccount()
	a.ID = 721
	a.Extra = map[string]any{OpenAIAPIKeyModeExtraKey: "codex_engine", OpenAIAttributionReplaceWhitelistEnabledExtraKey: false}
	// Administrative edits omit unchanged managed preferences; the repository
	// restores their latest values under lock rather than replaying the snapshot.
	repo := &autoAttributionConfigurationRepo{upstreamBillingProbeAccountRepo: &upstreamBillingProbeAccountRepo{accounts: map[int64]*Account{a.ID: a}}}
	svc := &adminServiceImpl{accountRepo: repo}
	updated, err := svc.UpdateAccount(context.Background(), a.ID, &UpdateAccountInput{Extra: map[string]any{"unrelated": true}})
	require.NoError(t, err)
	require.False(t, updated.OpenAIAttributionReplacesWhitelist())
	require.NoError(t, svc.UpdateAccountExtra(context.Background(), a.ID, map[string]any{OpenAIAttributionReplaceWhitelistEnabledExtraKey: true}))
	require.True(t, repo.accounts[a.ID].OpenAIAttributionReplacesWhitelist())
	for _, update := range []func() error{
		func() error {
			_, err := svc.UpdateAccount(context.Background(), a.ID, &UpdateAccountInput{Extra: map[string]any{OpenAIAttributionReplaceWhitelistEnabledExtraKey: "false"}})
			return err
		},
		func() error {
			return svc.UpdateAccountExtra(context.Background(), a.ID, map[string]any{OpenAIAttributionReplaceWhitelistEnabledExtraKey: "false"})
		},
		func() error {
			_, err := svc.BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{AccountIDs: []int64{a.ID}, Extra: map[string]any{OpenAIAttributionReplaceWhitelistEnabledExtraKey: "false"}})
			return err
		},
	} {
		require.Error(t, update())
	}
}

func TestOpenAIAttributionResultModeAccountServiceIntent(t *testing.T) {
	a := &Account{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Extra: map[string]any{OpenAIAttributionReplaceWhitelistEnabledExtraKey: true, "enable_tls_fingerprint": false}}
	repo := &autoAttributionConfigurationRepo{upstreamBillingProbeAccountRepo: &upstreamBillingProbeAccountRepo{accounts: map[int64]*Account{a.ID: a}}}
	svc := NewAccountService(repo, nil)
	extra := map[string]any{OpenAIAttributionReplaceWhitelistEnabledExtraKey: false, "enable_tls_fingerprint": true}
	updated, err := svc.Update(context.Background(), a.ID, UpdateAccountRequest{Extra: &extra})
	require.NoError(t, err)
	require.False(t, updated.OpenAIAttributionReplacesWhitelist())
	require.Equal(t, map[string]any{OpenAIAttributionReplaceWhitelistEnabledExtraKey: false}, repo.intent.Extra)
	require.Equal(t, false, updated.Extra["enable_tls_fingerprint"])
	extra = map[string]any{"unrelated": true}
	updated, err = svc.Update(context.Background(), a.ID, UpdateAccountRequest{Extra: &extra})
	require.NoError(t, err)
	require.False(t, updated.OpenAIAttributionReplacesWhitelist())
	require.Empty(t, repo.intent.Extra)
}

func TestAccountSchedulingIntentScope(t *testing.T) {
	ctx := WithAccountSchedulingIntent(context.Background(), 7, false)
	value, explicit := AccountSchedulingIntentFromContext(ctx, 7)
	require.True(t, explicit)
	require.False(t, value, "same-value false is still explicit intent")
	_, explicit = AccountSchedulingIntentFromContext(ctx, 8)
	require.False(t, explicit)
	_, explicit = AccountSchedulingIntentFromContext(context.Background(), 7)
	require.False(t, explicit)
}

type attributionResultModeBulkRepo struct {
	*upstreamBillingProbeAccountRepo
	loads   int
	intents map[int64]AccountConfigurationIntent
}

func (r *attributionResultModeBulkRepo) GetByIDs(ctx context.Context, ids []int64) ([]*Account, error) {
	r.loads++
	return r.upstreamBillingProbeAccountRepo.GetByIDs(ctx, ids)
}

func (r *attributionResultModeBulkRepo) BulkUpdate(ctx context.Context, ids []int64, updates AccountBulkUpdate) (int64, error) {
	r.intents = make(map[int64]AccountConfigurationIntent, len(ids))
	for _, id := range ids {
		r.intents[id] = AccountConfigurationIntentFromContext(ctx, id)
	}
	return r.upstreamBillingProbeAccountRepo.BulkUpdate(ctx, ids, updates)
}

func TestOpenAIAttributionResultModeOnlyBulkPrefetchAndIntent(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		repo := &attributionResultModeBulkRepo{upstreamBillingProbeAccountRepo: &upstreamBillingProbeAccountRepo{accounts: map[int64]*Account{
			1: {ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth},
			2: {ID: 2, Platform: PlatformOpenAI, Type: AccountTypeAPIKey},
			3: {ID: 3, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Extra: map[string]any{OpenAIAPIKeyModeExtraKey: "codex_engine"}},
		}}}
		svc := &adminServiceImpl{accountRepo: repo}
		extra := map[string]any{OpenAIAttributionReplaceWhitelistEnabledExtraKey: enabled}
		result, err := svc.BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{AccountIDs: []int64{1, 2, 3}, Extra: extra})
		require.NoError(t, err)
		require.Equal(t, 3, result.Success)
		require.Equal(t, 1, repo.loads, "mode-only updates must prefetch all target accounts for validation")
		require.Len(t, repo.bulkUpdates, 1)
		require.Equal(t, extra, repo.bulkUpdates[0].Extra)
		for _, id := range []int64{1, 2, 3} {
			require.Equal(t, extra, repo.intents[id].Extra)
		}
		repo.accounts[3].Platform = PlatformAnthropic
		_, err = svc.BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{AccountIDs: []int64{1, 2, 3}, Extra: extra})
		require.Error(t, err)
		require.Len(t, repo.bulkUpdates, 1, "one unsupported target must reject the complete batch before writes")
	}
}
