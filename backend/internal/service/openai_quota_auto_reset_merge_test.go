package service

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOpenAIQuotaAutoResetService_QueryFailurePersistsBoundedBackoff(t *testing.T) {
	for _, test := range []struct {
		name  string
		usage *OpenAIQuotaUsage
		err   error
		code  string
	}{
		{name: "network failure", err: errors.New("synthetic upstream failure"), code: "RESET_CREDIT_QUERY_FAILED"},
		{name: "empty response", code: "RESET_CREDIT_QUERY_FAILED"},
		{name: "missing credits", usage: &OpenAIQuotaUsage{}, code: "RESET_CREDIT_DETAILS_UNAVAILABLE"},
		{name: "missing expiration details", usage: &OpenAIQuotaUsage{RateLimitResetCredits: &OpenAIRateLimitResetCredits{AvailableCount: 1}}, code: "RESET_CREDIT_DETAILS_UNAVAILABLE"},
	} {
		t.Run(test.name, func(t *testing.T) {
			now := time.Now().UTC()
			account := newAutoResetTestAccount(600, now)
			account.Extra[OpenAIAutoResetCreditStateExtraKey] = &OpenAIAutoResetCreditState{
				Status: OpenAIAutoResetStatusFailed, AttemptCycleHash: "existing-cycle", AttemptCreditHash: "existing-credit",
			}
			repo := &autoResetTestAccountRepo{account: account}
			quota := &autoResetTestQuota{usage: test.usage, queryErr: test.err}
			service := newAutoResetTestService(repo, quota)
			require.Error(t, service.evaluateAccount(context.Background(), account.ID))
			state := repo.stateForTest()
			require.Equal(t, OpenAIAutoResetStatusFailed, state.Status)
			require.Equal(t, test.code, state.ErrorCode)
			require.Equal(t, "existing-cycle", state.AttemptCycleHash)
			require.Equal(t, "existing-credit", state.AttemptCreditHash)
			require.True(t, openAIAutoResetQueryFailureBackoffActive(state, time.Now()))
			require.NoError(t, service.evaluateAccount(context.Background(), account.ID))
			require.Equal(t, int32(1), quota.queryCalls.Load())
			require.Zero(t, quota.resetCalls.Load())
		})
	}
}

func TestOpenAIQuotaAutoResetService_TimedOutQueryStillPersistsBackoff(t *testing.T) {
	account := newAutoResetTestAccount(601, time.Now())
	repo := &autoResetTestAccountRepo{account: account, respectContext: true}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	quota := &autoResetTestQuota{queryErr: context.DeadlineExceeded, onQuery: func(int32) { cancel() }}
	service := newAutoResetTestService(repo, quota)
	require.ErrorIs(t, service.evaluateAccount(ctx, account.ID), context.DeadlineExceeded)
	require.True(t, openAIAutoResetQueryFailureBackoffActive(repo.stateForTest(), time.Now()))
	require.NoError(t, service.evaluateAccount(context.Background(), account.ID))
	require.Equal(t, int32(1), quota.queryCalls.Load())
}

func TestOpenAIQuotaAutoResetService_QueryFailureCannotOverwriteNewStateOrGrant(t *testing.T) {
	for _, test := range []struct {
		name       string
		change     func(*Account)
		wantFailed bool
	}{
		{name: "redeem in progress", change: func(a *Account) {
			a.Extra[OpenAIAutoResetCreditStateExtraKey] = &OpenAIAutoResetCreditState{
				Status: OpenAIAutoResetStatusResetting, AttemptCycleHash: "new-cycle", AttemptCreditHash: "new-credit",
			}
		}},
		{name: "completed redemption", change: func(a *Account) {
			a.Extra[OpenAIAutoResetCreditStateExtraKey] = &OpenAIAutoResetCreditState{
				Status: OpenAIAutoResetStatusSuccess, AttemptCycleHash: "new-cycle", AttemptCreditHash: "new-credit",
			}
		}},
		{name: "replacement authorization", change: func(a *Account) { a.OpenAIOAuthAuthorizationGeneration = "new-grant" }},
		{name: "ordinary refresh", change: func(a *Account) { a.OpenAIOAuthCredentialRevision++ }, wantFailed: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			account := newAutoResetTestAccount(602, time.Now())
			account.OpenAIOAuthCredentialOwnerID = account.ID
			account.OpenAIOAuthAuthorizationGeneration = "old-grant"
			account.OpenAIOAuthCredentialRevision = 1
			repo := &autoResetTestAccountRepo{account: account}
			var expected *OpenAIAutoResetCreditState
			quota := &autoResetTestQuota{queryErr: context.DeadlineExceeded, onQuery: func(int32) {
				repo.mu.Lock()
				test.change(repo.account)
				expected = openAIAutoResetStateFromExtra(repo.account.Extra)
				repo.mu.Unlock()
			}}
			require.ErrorIs(t, newAutoResetTestService(repo, quota).evaluateAccount(context.Background(), account.ID), context.DeadlineExceeded)
			if test.wantFailed {
				require.True(t, openAIAutoResetQueryFailureBackoffActive(repo.stateForTest(), time.Now()))
			} else {
				require.Equal(t, expected, repo.stateForTest())
			}
		})
	}
}

func TestOpenAIQuotaAutoResetService_FreshNoCreditSuppressesStaleUsageQuery(t *testing.T) {
	now := time.Now().UTC()
	service, repo, quota := newResetThresholdTestFixture(t, &OpenAIAutoResetCreditState{
		Status: OpenAIAutoResetStatusNoCredit, TriggerWindow: "7d", CheckedAt: now.Format(time.RFC3339), ErrorCode: "NO_RESET_CREDIT",
	}, 0)
	repo.setExtraForTest("codex_usage_updated_at", now.Add(-time.Hour).Format(time.RFC3339))
	for range 3 {
		require.NoError(t, service.evaluateAccount(context.Background(), 501))
	}
	require.Zero(t, quota.queryCalls.Load())
}

type autoResetFailUsageCASRepo struct {
	*autoResetTestAccountRepo
	failures atomic.Int32
}

func (r *autoResetFailUsageCASRepo) CompareAndUpdateOpenAIAutoResetPreflight(ctx context.Context, id int64, account *Account, expected *OpenAIAutoResetCreditState, updates map[string]any) (bool, error) {
	if _, usageWrite := updates["codex_usage_updated_at"]; usageWrite && r.failures.Add(1) == 1 {
		return false, errors.New("synthetic snapshot write failure")
	}
	return r.autoResetTestAccountRepo.CompareAndUpdateOpenAIAutoResetPreflight(ctx, id, account, expected, updates)
}

func TestOpenAIQuotaAutoResetService_SnapshotWriteFailureBacksOff(t *testing.T) {
	now := time.Now().UTC()
	account := newAutoResetTestAccount(603, now)
	repo := &autoResetFailUsageCASRepo{autoResetTestAccountRepo: &autoResetTestAccountRepo{account: account}}
	quota := &autoResetTestQuota{usage: newAutoResetTestUsage(now, 100, 0)}
	service := newAutoResetTestService(repo, quota)
	require.Error(t, service.evaluateAccount(context.Background(), account.ID))
	require.Equal(t, "USAGE_SNAPSHOT_WRITE_FAILED", repo.stateForTest().ErrorCode)
	require.NoError(t, service.evaluateAccount(context.Background(), account.ID))
	require.Equal(t, int32(1), quota.queryCalls.Load())
}

func TestNotifyOpenAIAutoResetFromScheduler_ConcurrentCooldownWinner(t *testing.T) {
	const accountID int64 = 9_900_003
	t.Cleanup(func() { openAIAutoResetSchedulerNotifiedAt.Delete(accountID) })
	var winners atomic.Int32
	var workers sync.WaitGroup
	now := time.Now()
	for range 64 {
		workers.Go(func() {
			if notifyOpenAIAutoResetFromSchedulerAt(accountID, now) {
				winners.Add(1)
			}
		})
	}
	workers.Wait()
	require.Equal(t, int32(1), winners.Load())
}
