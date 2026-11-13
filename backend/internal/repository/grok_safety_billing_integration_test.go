//go:build integration

package repository

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestGrokSafetyBillingAtomicity(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB).(service.AtomicUsageBillingRepository)
	user := mustCreateUser(t, client, &service.User{Email: uuid.NewString() + "@example.com", PasswordHash: "hash", Balance: 10, GiftBalance: 5})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: "sk-" + uuid.NewString(), Name: "safety", Quota: 100})
	account := mustCreateAccount(t, client, &service.Account{Name: "safety-" + uuid.NewString(), Platform: "grok", Type: service.AccountTypeAPIKey})
	quotaRepo := NewUserPlatformQuotaRepository(client)
	mustSeedQuotaRow(t, ctx, quotaRepo, user.ID, "grok")
	makeBill := func(cost float64) (*service.UsageBillingCommand, *service.UsageLog) {
		callID := uuid.NewString()
		requestID := "grok-safety:" + callID
		return &service.UsageBillingCommand{RequestID: requestID, UserID: user.ID, APIKeyID: key.ID, AccountID: account.ID, AccountType: account.Type, BalanceCost: cost, APIKeyQuotaCost: cost, PlatformQuotaCost: cost, Platform: "grok"},
			&service.UsageLog{RequestID: requestID, UserID: user.ID, APIKeyID: key.ID, AccountID: account.ID, Model: "grok-test", TotalCost: cost, ActualCost: cost, RateMultiplier: 1,
				GrokSafetyRejection: &service.GrokSafetyRejection{CallID: callID, Code: service.GrokSafetyRejectionCode, Fee: cost, HTTPStatus: 403, UpstreamRequestID: "upstream-private"}}
	}
	t.Run("concurrent duplicate commits once", func(t *testing.T) {
		cmd, bill := makeBill(2)
		var wg sync.WaitGroup
		results := make(chan bool, 8)
		errs := make(chan error, 8)
		for range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				localCmd, localBill := *cmd, *bill
				result, err := repo.ApplyWithUsageLog(ctx, &localCmd, &localBill)
				if err != nil {
					errs <- err
					return
				}
				results <- result.Applied
			}()
		}
		wg.Wait()
		close(errs)
		close(results)
		for err := range errs {
			require.NoError(t, err)
		}
		applied := 0
		for value := range results {
			if value {
				applied++
			}
		}
		require.Equal(t, 1, applied)
		var count int
		var balance, gift, quota float64
		require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM usage_logs WHERE request_id=$1", cmd.RequestID).Scan(&count))
		require.Equal(t, 1, count)
		require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT balance,gift_balance FROM users WHERE id=$1", user.ID).Scan(&balance, &gift))
		require.Equal(t, float64(13), balance+gift)
		require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT quota_used FROM api_keys WHERE id=$1", key.ID).Scan(&quota))
		require.Equal(t, float64(2), quota)
		platform, err := quotaRepo.GetByUserPlatform(ctx, user.ID, "grok")
		require.NoError(t, err)
		require.Equal(t, float64(2), platform.DailyUsageUSD)
		var id int64
		require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT id FROM usage_logs WHERE request_id=$1", cmd.RequestID).Scan(&id))
		stored, err := NewUsageLogRepository(client, integrationDB).GetByID(ctx, id)
		require.NoError(t, err)
		require.Equal(t, bill.GrokSafetyRejection, stored.GrokSafetyRejection)
	})
	t.Run("bill failure rolls back all effects and receipt", func(t *testing.T) {
		cmd, bill := makeBill(3)
		// The foreign-key failure occurs at bill insertion, after quota effects.
		cmd.AccountID, bill.AccountID = -999999999, -999999999
		_, err := repo.ApplyWithUsageLog(ctx, cmd, bill)
		require.Error(t, err)
		var count int
		var total, quota float64
		require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM usage_billing_dedup WHERE request_id=$1", cmd.RequestID).Scan(&count))
		require.Zero(t, count)
		require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT balance+gift_balance FROM users WHERE id=$1", user.ID).Scan(&total))
		require.Equal(t, float64(13), total)
		require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT quota_used FROM api_keys WHERE id=$1", key.ID).Scan(&quota))
		require.Equal(t, float64(2), quota)
		platform, err := quotaRepo.GetByUserPlatform(ctx, user.ID, "grok")
		require.NoError(t, err)
		require.Equal(t, float64(2), platform.DailyUsageUSD)
	})
	t.Run("zero cost still persists a refusal", func(t *testing.T) {
		cmd, bill := makeBill(0)
		result, err := repo.ApplyWithUsageLog(ctx, cmd, bill)
		require.NoError(t, err)
		require.True(t, result.Applied)
		require.NotZero(t, bill.ID)
		result, err = repo.ApplyWithUsageLog(ctx, cmd, bill)
		require.NoError(t, err)
		require.False(t, result.Applied)
	})
}

func TestGrokSafetyAccountCostAndSuccessStatistics(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	user := mustCreateUser(t, client, &service.User{Email: uuid.NewString() + "@example.com", PasswordHash: "hash"})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: "sk-" + uuid.NewString(), Name: "stats"})
	account := mustCreateAccount(t, client, &service.Account{Name: "stats-" + uuid.NewString(), Platform: "grok", Type: service.AccountTypeAPIKey})
	logs := NewUsageLogRepository(client, integrationDB)
	for i, multiplier := range []float64{0, 2} {
		customCost := float64(12)
		log := &service.UsageLog{UserID: user.ID, APIKeyID: key.ID, AccountID: account.ID, RequestID: uuid.NewString(), Model: "grok-test", TotalCost: 7, ActualCost: 7, RateMultiplier: 1, AccountStatsCost: &customCost, AccountRateMultiplier: &multiplier,
			GrokSafetyRejection: &service.GrokSafetyRejection{CallID: fmt.Sprint(i), Code: service.GrokSafetyRejectionCode, Fee: 2}}
		_, err := logs.Create(ctx, log)
		require.NoError(t, err)
	}
	stats, err := logs.GetAccountWindowStats(ctx, account.ID, time.Now().Add(-time.Hour))
	require.NoError(t, err)
	require.Equal(t, float64(24), stats.Cost) // 2 + (12-2)*2+2
	require.Equal(t, float64(14), stats.UserCost)
	var successes int
	var fees float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FILTER (WHERE "+usageLogSuccessFilterUL+"),SUM(ul.actual_cost) FROM usage_logs ul WHERE ul.user_id=$1", user.ID).Scan(&successes, &fees))
	require.Zero(t, successes)
	require.Equal(t, float64(14), fees)
	users, err := logs.GetBatchUserUsageStats(ctx, []int64{user.ID}, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	require.NoError(t, err)
	require.Equal(t, float64(14), users[user.ID].TotalActualCost)
}

func TestGrokSafetySubscriptionBilling(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB).(service.AtomicUsageBillingRepository)
	for _, kind := range []string{"periodic", "finite-total", "permanent-total"} {
		t.Run(kind, func(t *testing.T) {
			user := mustCreateUser(t, client, &service.User{Email: uuid.NewString() + "@example.com", PasswordHash: "hash", Balance: 10})
			groupType := service.SubscriptionTypeSubscription
			if kind != "periodic" {
				groupType = service.SubscriptionTypeTotalQuota
			}
			group := mustCreateGroup(t, client, &service.Group{Name: "safety-sub-" + uuid.NewString(), Platform: service.PlatformGrok, SubscriptionType: groupType, TotalQuotaNoExpiry: kind == "permanent-total"})
			key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, GroupID: &group.ID, Key: "sk-" + uuid.NewString(), Name: "sub"})
			account := mustCreateAccount(t, client, &service.Account{Name: "safety-sub-" + uuid.NewString(), Platform: service.PlatformGrok, Type: service.AccountTypeAPIKey, Extra: map[string]any{"quota_limit": 100.0}})
			sub := mustCreateSubscription(t, client, &service.UserSubscription{UserID: user.ID, GroupID: group.ID})
			if kind == "permanent-total" {
				_, err := integrationDB.ExecContext(ctx, "UPDATE user_subscriptions SET expires_at=NULL WHERE id=$1", sub.ID)
				require.NoError(t, err)
			}
			if kind != "periodic" {
				create := client.UserSubscriptionQuotaEvent.Create().SetUserSubscriptionID(sub.ID).SetQuotaTotalUsd(10).SetQuotaUsedUsd(0).SetStartsAt(time.Now().Add(-time.Hour)).SetSourceKind(service.SubscriptionQuotaSourceAdminAssign)
				if kind == "finite-total" {
					create.SetExpiresAt(time.Now().Add(time.Hour))
				}
				_, err := create.Save(ctx)
				require.NoError(t, err)
			}
			callID := uuid.NewString()
			cmd := &service.UsageBillingCommand{RequestID: "grok-safety:" + callID, UserID: user.ID, APIKeyID: key.ID, AccountID: account.ID, AccountType: account.Type, SubscriptionID: &sub.ID, SubscriptionCost: 2, AccountQuotaCost: 2}
			bill := &service.UsageLog{RequestID: cmd.RequestID, UserID: user.ID, APIKeyID: key.ID, AccountID: account.ID, GroupID: &group.ID, SubscriptionID: &sub.ID, Model: "grok-test", TotalCost: 2, ActualCost: 2, RateMultiplier: 1, BillingType: 1, GrokSafetyRejection: &service.GrokSafetyRejection{CallID: callID, Code: service.GrokSafetyRejectionCode, Fee: 2, HTTPStatus: 403}}
			result, err := repo.ApplyWithUsageLog(ctx, cmd, bill)
			require.NoError(t, err)
			require.True(t, result.Applied)
			result, err = repo.ApplyWithUsageLog(ctx, cmd, bill)
			require.NoError(t, err)
			require.False(t, result.Applied)
			var balance, usage, accountUsage float64
			require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT balance FROM users WHERE id=$1", user.ID).Scan(&balance))
			require.Equal(t, float64(10), balance)
			if kind == "periodic" {
				require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT daily_usage_usd FROM user_subscriptions WHERE id=$1", sub.ID).Scan(&usage))
			} else {
				require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT SUM(quota_used_usd) FROM user_subscription_quota_events WHERE user_subscription_id=$1", sub.ID).Scan(&usage))
			}
			require.Equal(t, float64(2), usage)
			require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT (extra->>'quota_used')::numeric FROM accounts WHERE id=$1", account.ID).Scan(&accountUsage))
			require.Equal(t, float64(2), accountUsage)
		})
	}
}
