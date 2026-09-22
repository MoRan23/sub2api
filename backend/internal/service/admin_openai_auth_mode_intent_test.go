//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type adminAuthModeIntentRepo struct {
	accountRepoStubForBulkUpdate
	allowed map[int64]bool
}

func (r *adminAuthModeIntentRepo) Update(ctx context.Context, account *Account) error {
	r.allowed = map[int64]bool{account.ID: OpenAIOAuthCredentialModeChangeAllowed(ctx, account.ID)}
	return r.accountRepoStubForBulkUpdate.Update(ctx, account)
}
func (r *adminAuthModeIntentRepo) BulkUpdate(ctx context.Context, ids []int64, update AccountBulkUpdate) (int64, error) {
	r.allowed = make(map[int64]bool)
	for _, id := range ids {
		r.allowed[id] = OpenAIOAuthCredentialModeChangeAllowed(ctx, id)
	}
	return r.accountRepoStubForBulkUpdate.BulkUpdate(ctx, ids, update)
}

func TestAdminOpenAIAuthModeChangeRequiresExplicitFlag(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(map[bool]string{false: "stale_snapshot", true: "explicit_conversion"}[explicit], func(t *testing.T) {
			account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Credentials: map[string]any{"access_token": "stored"}}
			repo := &adminAuthModeIntentRepo{accountRepoStubForBulkUpdate: accountRepoStubForBulkUpdate{getByIDAccounts: map[int64]*Account{1: account}}}
			svc := &adminServiceImpl{accountRepo: repo}
			_, err := svc.UpdateAccount(context.Background(), 1, &UpdateAccountInput{OpenAIAuthModeChange: explicit, Credentials: map[string]any{"auth_mode": OpenAIAuthModePersonalAccessToken, "access_token": "incoming"}})
			require.NoError(t, err)
			require.Equal(t, explicit, repo.allowed[1])
			require.Equal(t, explicit, account.IsOpenAIPersonalAccessToken())
			if !explicit {
				require.Equal(t, "stored", account.Credentials["access_token"])
			}
		})
	}
}

func TestAdminOpenAIAuthModeChangeBulkScopesOnlyActualTransitions(t *testing.T) {
	regular := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "stored"}}
	pat := &Account{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"auth_mode": OpenAIAuthModePersonalAccessToken}}
	repo := &adminAuthModeIntentRepo{accountRepoStubForBulkUpdate: accountRepoStubForBulkUpdate{getByIDsAccounts: []*Account{regular, pat}, getByIDAccounts: map[int64]*Account{1: regular, 2: pat}}}
	svc := &adminServiceImpl{accountRepo: repo}
	_, err := svc.BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{AccountIDs: []int64{1, 2}, OpenAIAuthModeChange: true, Credentials: map[string]any{"auth_mode": OpenAIAuthModePersonalAccessToken, "access_token": "incoming"}})
	require.NoError(t, err)
	require.Equal(t, map[int64]bool{1: true, 2: false}, repo.allowed)
}

func TestAdminOpenAIAuthModeChangeRejectsMissingOrInvalidIntent(t *testing.T) {
	account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	for _, credentials := range []map[string]any{nil, {"access_token": "new"}, {"auth_mode": "unexpected"}, {"auth_mode": false}, {"auth_mode": "agentIdentity", "openai_auth_mode": "personalAccessToken"}} {
		_, _, err := explicitOpenAIAuthModeChange(account, true, credentials)
		require.Error(t, err)
	}
	changed, _, err := explicitOpenAIAuthModeChange(account, true, map[string]any{"auth_mode": "oauth"})
	require.NoError(t, err)
	require.False(t, changed)
}
