//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func (r *refreshAPIAccountRepo) PatchOpenAIOAuthCredentialsIfUnchanged(_ context.Context, id int64, expectedAuth map[string]any, expectedProxyID *int64, patch map[string]any, removed []string) (bool, error) {
	if r.updateErr != nil {
		return false, r.updateErr
	}
	applied := applyOpenAIRefreshTestPatch(r.account, id, expectedAuth, expectedProxyID, patch, removed)
	if applied {
		r.updateCalls++
		r.updateCredentialsCalls++
	}
	return applied, nil
}

func (r *tokenRefreshAccountRepo) PatchOpenAIOAuthCredentialsIfUnchanged(_ context.Context, id int64, expectedAuth map[string]any, expectedProxyID *int64, patch map[string]any, removed []string) (bool, error) {
	if r.updateErr != nil {
		return false, r.updateErr
	}
	account := r.accountsByID[id]
	applied := applyOpenAIRefreshTestPatch(account, id, expectedAuth, expectedProxyID, patch, removed)
	if applied {
		r.updateCalls++
		r.updateCredentialsCalls++
		r.lastAccount = account
		if r.cancelOnUpdate != nil {
			r.cancelOnUpdate()
		}
	}
	return applied, nil
}

func openAIRefreshMappingAccount() *Account {
	return &Account{
		ID: 7, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Status: StatusActive, Schedulable: true,
		Credentials: map[string]any{
			"access_token": "old-access", "refresh_token": "old-refresh", "_token_version": int64(1),
			"model_mapping":         map[string]any{"gpt-6-astra": "gpt-6-astra", "gpt-5.6-sol": "gpt-5.6-sol"},
			"compact_model_mapping": map[string]any{"gpt-6-astra": "gpt-6-astra"},
			"quota_limit":           float64(100), "plan_type": "pro",
		},
	}
}

func changeOpenAIRefreshMapping(account *Account) {
	account.Credentials["model_mapping"] = map[string]any{"gpt-5.6-sol": "gpt-5.6-sol"}
	account.Credentials["compact_model_mapping"] = map[string]any{"gpt-5.6-sol": "gpt-5.6-sol"}
	account.Credentials["quota_limit"] = float64(20)
	account.Credentials["plan_type"] = "plus"
	account.Schedulable = false
}

func TestOpenAIRefreshCredentialsPreservesConcurrentModelRestrictions(t *testing.T) {
	for _, unified := range []bool{false, true} {
		name := "fallback"
		if unified {
			name = "unified"
		}
		t.Run(name, func(t *testing.T) {
			stored := openAIRefreshMappingAccount()
			request := snapshotOAuthRefreshAccount(stored)
			repo := &tokenRefreshAccountRepo{mockAccountRepoForGemini: mockAccountRepoForGemini{accountsByID: map[int64]*Account{stored.ID: stored}}, snapshotReads: true}
			scheduler := &tokenRefreshSchedulerCache{}
			invalidator := &tokenCacheInvalidatorStub{}
			cfg := &config.Config{TokenRefresh: config.TokenRefreshConfig{MaxRetries: 1}}
			svc := NewTokenRefreshService(repo, nil, nil, nil, nil, invalidator, nil, cfg, nil)
			svc.schedulerCache = scheduler
			if unified {
				svc.refreshAPI = NewOAuthRefreshAPI(repo, nil)
			}
			next := shallowCopyMap(request.Credentials)
			next["access_token"] = "rotated-access"
			next["refresh_token"] = "rotated-refresh"
			executor := &refreshAPIExecutorStub{needsRefresh: true, credentials: next, onRefresh: func() { changeOpenAIRefreshMapping(stored) }}
			err := svc.refreshWithRetry(context.Background(), request, executor, executor, time.Hour)
			require.NoError(t, err)
			require.Equal(t, 1, repo.updateCredentialsCalls)
			require.Equal(t, 0, repo.fullUpdateCalls)
			require.Equal(t, "rotated-access", stored.GetCredential("access_token"))
			require.False(t, stored.IsModelSupported("gpt-6-astra"), "token refresh must not restore a disabled model")
			require.True(t, stored.IsModelSupported("gpt-5.6-sol"))
			require.Equal(t, map[string]any{"gpt-5.6-sol": "gpt-5.6-sol"}, stored.Credentials["compact_model_mapping"])
			require.Equal(t, float64(20), stored.Credentials["quota_limit"])
			require.Equal(t, "plus", stored.GetCredential("plan_type"), "unchanged provider field must retain concurrent admin edit")
			require.Equal(t, 1, scheduler.setAccountCalls)
			require.False(t, scheduler.lastAccount.Schedulable)
			require.False(t, scheduler.lastAccount.IsModelSupported("gpt-6-astra"))
			require.False(t, invalidator.lastAccount.IsModelSupported("gpt-6-astra"))
		})
	}
}

func TestOpenAIRefreshCredentialsSkipsConcurrentReauthorization(t *testing.T) {
	for _, key := range openAIRefreshAuthIdentityKeys {
		t.Run(key, func(t *testing.T) {
			stored := openAIRefreshMappingAccount()
			expected := snapshotOAuthRefreshAccount(stored)
			repo := &refreshAPIAccountRepo{account: stored}
			next := shallowCopyMap(expected.Credentials)
			next["access_token"] = "stale-provider-result"
			executor := &refreshAPIExecutorStub{needsRefresh: true, credentials: next, onRefresh: func() { stored.Credentials[key] = "reauthorized" }}
			result, err := NewOAuthRefreshAPI(repo, nil).RefreshIfNeeded(context.Background(), expected, executor, time.Hour)
			require.NoError(t, err)
			require.False(t, result.Refreshed)
			require.Nil(t, result.NewCredentials)
			require.Zero(t, repo.updateCredentialsCalls)
			require.Equal(t, "reauthorized", result.Account.Credentials[key])
		})
	}
}

func TestOpenAIRefreshCredentialPatchOnlyChangesProviderFields(t *testing.T) {
	previous := map[string]any{"access_token": "old", "plan_type": "pro", "model_mapping": "old-mapping", "refresh_token": "old-refresh"}
	next := map[string]any{"access_token": "new", "plan_type": "pro", "model_mapping": "bad-mapping", "quota_limit": 999}
	patch, removed := openAIRefreshCredentialPatch(previous, next)
	require.Equal(t, map[string]any{"access_token": "new"}, patch)
	require.Equal(t, []string{"refresh_token"}, removed)
}

func TestOpenAIRefreshCredentialPatchPreservesPATNormalization(t *testing.T) {
	account := openAIRefreshMappingAccount()
	account.Credentials[openAIAuthModeCredentialKey] = OpenAIAuthModePersonalAccessToken
	account.Credentials["expires_at"] = "old-expiry"
	account.Credentials["client_id"] = "old-client"
	next := NormalizeOpenAIPersonalAccessTokenCredentials(account, nil, shallowCopyMap(account.Credentials))
	patch, removed := openAIRefreshCredentialPatch(account.Credentials, next)
	require.Contains(t, removed, "refresh_token")
	require.Contains(t, removed, "expires_at")
	require.Contains(t, removed, "client_id")
	require.NotContains(t, removed, "model_mapping")
	require.Equal(t, "Bearer", patch["token_type"])
}

func TestOpenAIRefreshCredentialsFailsClosedWithoutAtomicRepository(t *testing.T) {
	account := openAIRefreshMappingAccount()
	repo := &mockAccountRepoForGemini{}
	_, applied, err := persistOpenAIOAuthRefreshCredentials(context.Background(), repo, account, account.Credentials)
	require.Error(t, err)
	var configErr *providerConfigurationRefreshError
	require.ErrorAs(t, err, &configErr)
	require.False(t, applied)
}

func TestOpenAIRefreshCredentialsShadowDoesNotPersist(t *testing.T) {
	account := openAIRefreshMappingAccount()
	account.ParentAccountID = new(int64(8))
	repo := &refreshAPIAccountRepo{account: account}
	_, applied, err := persistOpenAIOAuthRefreshCredentials(context.Background(), repo, account, map[string]any{"access_token": "leak"})
	require.NoError(t, err)
	require.False(t, applied)
	require.Zero(t, repo.updateCalls)
	require.Equal(t, "old-access", account.GetCredential("access_token"))
}

func TestOpenAIRefreshCredentialsDoesNotRetryAmbiguousPersistence(t *testing.T) {
	account := openAIRefreshMappingAccount()
	repo := &refreshAPIAccountRepo{account: account, updateErr: errors.New("database connection lost")}
	_, applied, err := persistOpenAIOAuthRefreshCredentials(context.Background(), repo, account, map[string]any{"access_token": "new"})
	require.Error(t, err)
	var containmentErr *providerCycleContainmentRefreshError
	require.ErrorAs(t, err, &containmentErr)
	require.False(t, applied)
}

type openAIRefreshDurableReadFailureRepo struct {
	*tokenRefreshAccountRepo
}

func (r *openAIRefreshDurableReadFailureRepo) GetByID(ctx context.Context, id int64) (*Account, error) {
	if r.updateCredentialsCalls > 0 {
		return nil, errors.New("durable account read failed")
	}
	return r.tokenRefreshAccountRepo.GetByID(ctx, id)
}

func TestOpenAIRefreshCredentialsInvalidatesTokenAfterDurableReadFailure(t *testing.T) {
	for _, unified := range []bool{false, true} {
		name := "fallback"
		if unified {
			name = "unified"
		}
		t.Run(name, func(t *testing.T) {
			stored := openAIRefreshMappingAccount()
			request := snapshotOAuthRefreshAccount(stored)
			repo := &openAIRefreshDurableReadFailureRepo{tokenRefreshAccountRepo: &tokenRefreshAccountRepo{
				mockAccountRepoForGemini: mockAccountRepoForGemini{accountsByID: map[int64]*Account{stored.ID: stored}}, snapshotReads: true,
			}}
			scheduler := &tokenRefreshSchedulerCache{}
			invalidator := &tokenCacheInvalidatorStub{}
			tokenCache := &refreshAPICacheStub{lockResult: true}
			cfg := &config.Config{TokenRefresh: config.TokenRefreshConfig{MaxRetries: 3}}
			svc := NewTokenRefreshService(repo, nil, nil, nil, nil, invalidator, nil, cfg, nil)
			svc.schedulerCache = scheduler
			if unified {
				svc.refreshAPI = NewOAuthRefreshAPI(repo, tokenCache)
			}
			next := shallowCopyMap(request.Credentials)
			next["access_token"] = "rotated-access"
			executor := &refreshAPIExecutorStub{needsRefresh: true, credentials: next, onRefresh: func() { changeOpenAIRefreshMapping(stored) }}
			err := svc.refreshWithRetry(context.Background(), request, executor, executor, time.Hour)
			var containmentErr *providerCycleContainmentRefreshError
			require.ErrorAs(t, err, &containmentErr)
			require.Equal(t, 1, executor.refreshCalls)
			require.Equal(t, "rotated-access", stored.GetCredential("access_token"))
			require.Zero(t, scheduler.setAccountCalls, "failed reread must not publish the stale pre-refresh account")
			if unified {
				require.Equal(t, 1, tokenCache.deleteCalls)
				require.NoError(t, tokenCache.deleteCtxErr)
			} else {
				require.Equal(t, 1, invalidator.calls)
				require.NoError(t, invalidator.ctxErr)
			}
		})
	}
}

func TestOpenAIRefreshCredentialsFallbackSkipsConcurrentReauthorization(t *testing.T) {
	stored := openAIRefreshMappingAccount()
	request := snapshotOAuthRefreshAccount(stored)
	repo := &tokenRefreshAccountRepo{mockAccountRepoForGemini: mockAccountRepoForGemini{accountsByID: map[int64]*Account{stored.ID: stored}}, snapshotReads: true}
	scheduler := &tokenRefreshSchedulerCache{}
	cfg := &config.Config{TokenRefresh: config.TokenRefreshConfig{MaxRetries: 3}}
	svc := NewTokenRefreshService(repo, nil, nil, nil, nil, nil, nil, cfg, nil)
	svc.schedulerCache = scheduler
	next := shallowCopyMap(request.Credentials)
	next["access_token"] = "discarded-access"
	executor := &refreshAPIExecutorStub{needsRefresh: true, credentials: next, onRefresh: func() { stored.Credentials["access_token"] = "reauthorized-access" }}
	err := svc.refreshWithRetry(context.Background(), request, executor, executor, time.Hour)
	require.ErrorIs(t, err, errRefreshSkipped)
	require.Equal(t, 1, executor.refreshCalls)
	require.Equal(t, "reauthorized-access", stored.GetCredential("access_token"))
	require.Zero(t, repo.updateCredentialsCalls)
	require.Zero(t, scheduler.setAccountCalls)
}

func TestAdminServicePersistOpenAIOAuthRefreshCredentialsPreservesModelRestrictions(t *testing.T) {
	stored := openAIRefreshMappingAccount()
	expected := snapshotOAuthRefreshAccount(stored)
	next := shallowCopyMap(expected.Credentials)
	next["access_token"] = "new-access"
	changeOpenAIRefreshMapping(stored)
	repo := &refreshAPIAccountRepo{account: stored}
	svc := &adminServiceImpl{accountRepo: repo}
	result, applied, err := svc.PersistOpenAIOAuthRefreshCredentials(context.Background(), expected, next)
	require.NoError(t, err)
	require.True(t, applied)
	require.Equal(t, "new-access", result.GetCredential("access_token"))
	require.False(t, result.IsModelSupported("gpt-6-astra"))
	require.NotEqual(t, int64(1), result.Credentials["_token_version"])
	require.Equal(t, int64(1), next["_token_version"], "do not mutate the caller's snapshot")
}
