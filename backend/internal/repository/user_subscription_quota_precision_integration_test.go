//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestUsageBillingRepositoryApply_TotalQuotaExhaustsHistoricalDust(t *testing.T) {
	for _, withSnapshot := range []bool{false, true} {
		t.Run(map[bool]string{false: "current batches", true: "admission snapshot"}[withSnapshot], func(t *testing.T) {
			ctx := context.Background()
			client := testEntClient(t)
			user := mustCreateUser(t, client, &service.User{})
			group := mustCreateGroup(t, client, &service.Group{Name: "quota-dust-" + uuid.NewString(), SubscriptionType: service.SubscriptionTypeTotalQuota, TotalQuotaNoExpiry: true})
			key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, GroupID: &group.ID, Key: "quota-dust-" + uuid.NewString(), Name: "quota-dust"})
			subs := NewUserSubscriptionRepository(client)
			sub := &service.UserSubscription{UserID: user.ID, GroupID: group.ID, StartsAt: time.Now(), Status: service.SubscriptionStatusActive}
			require.NoError(t, subs.Create(ctx, sub))
			var firstID, lastID int64
			// Ten-digit historical usage is still present even though Apply now
			// quantizes new request costs to eight fractional digits.
			require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO user_subscription_quota_events
				(user_subscription_id,quota_total_usd,quota_used_usd,starts_at,source_kind)
				VALUES ($1,1,0.9999999995,NOW(),'admin_assign') RETURNING id`, sub.ID).Scan(&firstID))
			require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO user_subscription_quota_events
				(user_subscription_id,quota_total_usd,starts_at,source_kind)
				VALUES ($1,1,NOW(),'admin_assign') RETURNING id`, sub.ID).Scan(&lastID))
			if withSnapshot {
				snapshot, err := subs.(*userSubscriptionRepository).GetQuotaSpendSnapshot(ctx, sub.ID, time.Now())
				require.NoError(t, err)
				ctx = service.WithTotalQuotaSpendSnapshot(ctx, snapshot)
			}
			billing := NewUsageBillingRepository(client, integrationDB)
			cmd := &service.UsageBillingCommand{RequestID: uuid.NewString(), APIKeyID: key.ID, UserID: user.ID, SubscriptionID: &sub.ID, SubscriptionCost: 1.00000001}
			result, err := billing.Apply(ctx, cmd)
			require.NoError(t, err)
			require.True(t, result.Applied)
			require.Equal(t, 1.00000001, cmd.SubscriptionCost)
			var firstUsed, lastUsed string
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT quota_used_usd::text FROM user_subscription_quota_events WHERE id=$1`, firstID).Scan(&firstUsed))
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT quota_used_usd::text FROM user_subscription_quota_events WHERE id=$1`, lastID).Scan(&lastUsed))
			require.Equal(t, "1.0000000000", firstUsed)
			require.Equal(t, "1.0000000095", lastUsed)
			result, err = billing.Apply(ctx, cmd)
			require.NoError(t, err)
			require.False(t, result.Applied, "retry must not charge the historical tail twice")
			// A request already in flight may still overdraw the final batch.
			_, err = billing.Apply(ctx, &service.UsageBillingCommand{RequestID: uuid.NewString(), APIKeyID: key.ID, UserID: user.ID, SubscriptionID: &sub.ID, SubscriptionCost: 2})
			require.NoError(t, err)
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT quota_used_usd::text FROM user_subscription_quota_events WHERE id=$1`, lastID).Scan(&lastUsed))
			require.Equal(t, "3.0000000095", lastUsed)
			summary, err := subs.GetQuotaSummary(ctx, sub.ID, time.Now())
			require.NoError(t, err)
			require.Zero(t, summary.TotalRemainingUSD)
			sub.TotalRemainingUSD = summary.TotalRemainingUSD
			_, err = service.NewSubscriptionService(nil, nil, nil, nil, nil).ValidateAndCheckLimits(sub, group)
			require.ErrorIs(t, err, service.ErrTotalLimitExceeded)
		})
	}
}

func TestTotalQuotaRetirementPreservesSpendableTailAndLateSnapshot(t *testing.T) {
	tx := testEntTx(t)
	ctx := dbent.NewTxContext(context.Background(), tx)
	client := tx.Client()
	user := mustCreateUser(t, client, &service.User{})
	group := mustCreateGroup(t, client, &service.Group{Name: "finite-quota-dust", SubscriptionType: service.SubscriptionTypeTotalQuota})
	repo := NewUserSubscriptionRepository(client).(*userSubscriptionRepository)
	now := time.Now().UTC()
	expires := now.Add(time.Hour)
	sub := &service.UserSubscription{UserID: user.ID, GroupID: group.ID, StartsAt: now, ExpiresAt: &expires, Status: service.SubscriptionStatusActive}
	require.NoError(t, repo.Create(ctx, sub))
	tail := &service.UserSubscriptionQuotaEvent{UserSubscriptionID: sub.ID, QuotaTotalUSD: 1, QuotaUsedUSD: 0.9999999995, StartsAt: now, ExpiresAt: &expires, SourceKind: "admin_assign"}
	depleted := &service.UserSubscriptionQuotaEvent{UserSubscriptionID: sub.ID, QuotaTotalUSD: 1, QuotaUsedUSD: 1, StartsAt: now, ExpiresAt: &expires, SourceKind: "admin_assign"}
	require.NoError(t, repo.CreateQuotaEvent(ctx, tail))
	require.NoError(t, repo.CreateQuotaEvent(ctx, depleted))
	snapshot, err := repo.GetQuotaSpendSnapshot(ctx, sub.ID, now)
	require.NoError(t, err)
	appended := &service.UserSubscriptionQuotaEvent{UserSubscriptionID: sub.ID, QuotaTotalUSD: 1, StartsAt: now, ExpiresAt: &expires, SourceKind: "admin_assign"}
	require.NoError(t, repo.CreateQuotaEvent(ctx, appended))
	require.NoError(t, repo.RetireDepletedQuotaEventsOnAppend(ctx, sub.ID, appended.ID, now))
	storedTail, err := client.UserSubscriptionQuotaEvent.Get(ctx, tail.ID)
	require.NoError(t, err)
	require.True(t, storedTail.ExpiresAt.After(now), "a positive tail is still spendable")
	storedDepleted, err := client.UserSubscriptionQuotaEvent.Get(ctx, depleted.ID)
	require.NoError(t, err)
	require.WithinDuration(t, now, *storedDepleted.ExpiresAt, time.Microsecond)
	// The admission snapshot may still charge its retired overflow batch and
	// must not divert that request into the newly appended credit.
	require.NoError(t, repo.IncrementUsage(service.WithTotalQuotaSpendSnapshot(ctx, snapshot), sub.ID, 1e-8))
	storedTail, err = client.UserSubscriptionQuotaEvent.Get(ctx, tail.ID)
	require.NoError(t, err)
	require.Equal(t, 1.0, storedTail.QuotaUsedUsd)
	storedDepleted, err = client.UserSubscriptionQuotaEvent.Get(ctx, depleted.ID)
	require.NoError(t, err)
	require.Equal(t, 1.0000000095, storedDepleted.QuotaUsedUsd)
	storedAppended, err := client.UserSubscriptionQuotaEvent.Get(ctx, appended.ID)
	require.NoError(t, err)
	require.Zero(t, storedAppended.QuotaUsedUsd)
}
