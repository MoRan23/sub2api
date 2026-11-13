//go:build unit

package service

import (
	"context"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestAdminAccountFastSettingsCreateAndUpdate(t *testing.T) {
	repo := &longContextBillingRepoStub{}
	svc := &adminServiceImpl{accountRepo: repo}
	account, err := svc.CreateAccount(context.Background(), &CreateAccountInput{
		Name: "fast", Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "test"}, SkipDefaultGroupBind: true,
		Extra: map[string]any{ForceOpenAIFastExtraKey: true, FreeOpenAIUltrafastExtraKey: true},
	})
	require.NoError(t, err)
	require.True(t, account.IsForceOpenAIFastEnabled())
	require.True(t, account.IsFreeOpenAIUltrafastEnabled())
	updated, err := svc.UpdateAccount(context.Background(), account.ID, &UpdateAccountInput{Extra: map[string]any{"unrelated": true}})
	require.NoError(t, err)
	require.True(t, updated.IsForceOpenAIFastEnabled())
	require.True(t, updated.IsFreeOpenAIUltrafastEnabled())
	_, err = svc.UpdateAccount(context.Background(), account.ID, &UpdateAccountInput{Extra: map[string]any{ForceOpenAIUltrafastExtraKey: true}})
	require.Equal(t, "OPENAI_ACCOUNT_FAST_CONFLICT", infraerrors.Reason(err))
	updated, err = svc.UpdateAccount(context.Background(), account.ID, &UpdateAccountInput{Extra: map[string]any{ForceOpenAIFastExtraKey: false, ForceOpenAIUltrafastExtraKey: true}})
	require.NoError(t, err)
	require.False(t, updated.IsForceOpenAIFastEnabled())
	require.True(t, updated.IsForceOpenAIUltrafastEnabled())
}

func TestAdminAccountFastSettingsPartialAndBulkValidateEffectivePair(t *testing.T) {
	repo := &longContextBillingRepoStub{account: &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Extra: map[string]any{ForceOpenAIFastExtraKey: true}}}
	svc := &adminServiceImpl{accountRepo: repo}
	err := svc.UpdateAccountExtra(context.Background(), 1, map[string]any{ForceOpenAIUltrafastExtraKey: true})
	require.Equal(t, "OPENAI_ACCOUNT_FAST_CONFLICT", infraerrors.Reason(err))
	require.Zero(t, repo.updateExtraCalls)
	require.NoError(t, svc.UpdateAccountExtra(context.Background(), 1, map[string]any{ForceOpenAIFastExtraKey: false, ForceOpenAIUltrafastExtraKey: true}))
	require.Equal(t, 1, repo.updateExtraCalls)
	_, err = svc.BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{AccountIDs: []int64{1}, Extra: map[string]any{ForceOpenAIUltrafastExtraKey: true}})
	require.Equal(t, "OPENAI_ACCOUNT_FAST_CONFLICT", infraerrors.Reason(err))
	require.Zero(t, repo.bulkUpdateCalls)
	_, err = svc.BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{AccountIDs: []int64{1}, Extra: map[string]any{ForceOpenAIFastExtraKey: false, ForceOpenAIUltrafastExtraKey: true}})
	require.NoError(t, err)
	require.Equal(t, 1, repo.bulkUpdateCalls)
	repo.accounts = []*Account{repo.account, {ID: 2, Platform: PlatformAnthropic}}
	_, err = svc.BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{AccountIDs: []int64{1, 2}, Extra: map[string]any{FreeOpenAIFastExtraKey: false}})
	require.Equal(t, "OPENAI_ACCOUNT_FAST_UNSUPPORTED", infraerrors.Reason(err))
	require.Equal(t, 1, repo.bulkUpdateCalls, "mixed unsupported targets must reject before writing")
}

func TestAdminAccountFastSettingsShadowOwnsPreferences(t *testing.T) {
	parentID := int64(9)
	repo := &longContextBillingRepoStub{account: &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, ParentAccountID: &parentID}}
	svc := &adminServiceImpl{accountRepo: repo}
	updated, err := svc.UpdateAccount(context.Background(), 1, &UpdateAccountInput{Extra: map[string]any{ForceOpenAIFastExtraKey: true, FreeOpenAIFastExtraKey: true}})
	require.NoError(t, err)
	require.True(t, updated.IsForceOpenAIFastEnabled())
	require.True(t, updated.IsFreeOpenAIFastEnabled())
	_, err = svc.BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{AccountIDs: []int64{1}, Extra: map[string]any{FreeOpenAIUltrafastExtraKey: true}})
	require.NoError(t, err, "shadow-only fast policy edits do not require selecting its parent")
	require.Equal(t, 1, repo.bulkUpdateCalls)
}
