//go:build integration

package repository

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func TestUsageBillingRepositoryApplyBatch_DualWalletMatchesSequential(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB)
	for _, ordinary := range []float64{2, -2} {
		t.Run(fmt.Sprint(ordinary), func(t *testing.T) {
			sequential := newBatchBillingFixture(t, client, ordinary)
			batched := newBatchBillingFixture(t, client, ordinary)
			_, err := integrationDB.ExecContext(ctx, "UPDATE users SET gift_balance = 3 WHERE id IN ($1, $2)", sequential.userID, batched.userID)
			require.NoError(t, err)
			steps := []batchBillingStep{
				{key: 'A', balance: 1.00000001, keyQuota: 1.00000001},
				{key: 'A', balance: 2, keyQuota: 2},
				{key: 'B', balance: 4, keyRate: 4},
			}
			expected := make([]*service.UsageBillingApplyResult, len(steps))
			for i, cmd := range sequential.commands(uuid.NewString(), steps) {
				expected[i], err = repo.Apply(ctx, cmd)
				require.NoError(t, err)
			}
			cmds := batched.commands(uuid.NewString(), steps)
			results, err := repo.ApplyBatch(ctx, cmds)
			require.NoError(t, err)
			for i, result := range results {
				require.NoError(t, result.Err)
				require.Equal(t, expected[i], result.Result)
			}
			var seqBalance, seqGift, batchBalance, batchGift string
			require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT balance::text, gift_balance::text FROM users WHERE id=$1", sequential.userID).Scan(&seqBalance, &seqGift))
			require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT balance::text, gift_balance::text FROM users WHERE id=$1", batched.userID).Scan(&batchBalance, &batchGift))
			require.Equal(t, seqBalance, batchBalance)
			require.Equal(t, seqGift, batchGift)
			results, err = repo.ApplyBatch(ctx, cmds)
			require.NoError(t, err)
			for _, result := range results {
				require.False(t, result.Result.Applied)
			}
		})
	}
}

// The permanent grant predates the finite grant, so ID lock order and FEFO
// consumption order intentionally differ.
func batchQuotaFixture(t *testing.T, client *dbent.Client) (batchBillingFixture, int64, int64, int64) {
	t.Helper()
	ctx := context.Background()
	f := newBatchBillingFixture(t, client, 0)
	_, err := integrationDB.ExecContext(ctx, "UPDATE groups SET subscription_type = $1 WHERE id = (SELECT group_id FROM user_subscriptions WHERE id = $2)", service.SubscriptionTypeTotalQuota, f.subscriptionID)
	require.NoError(t, err)
	now := time.Now().UTC()
	event := func(total float64, expires *time.Time) int64 {
		builder := client.UserSubscriptionQuotaEvent.Create().
			SetUserSubscriptionID(f.subscriptionID).
			SetQuotaTotalUsd(total).SetQuotaUsedUsd(0).
			SetStartsAt(now.Add(-time.Hour)).
			SetSourceKind(service.SubscriptionQuotaSourceAdminAssign)
		if expires != nil {
			builder.SetExpiresAt(*expires)
		}
		result, err := builder.Save(ctx)
		require.NoError(t, err)
		return result.ID
	}
	expiredAt, finiteAt := now.Add(-time.Minute), now.Add(time.Hour)
	expired := event(4, &expiredAt)
	permanent := event(100, nil)
	finite := event(5, &finiteAt)
	return f, expired, permanent, finite
}

func batchQuotaCommand(f batchBillingFixture, requestID string, cost float64, ids ...int64) *service.UsageBillingCommand {
	return &service.UsageBillingCommand{
		RequestID: requestID, APIKeyID: f.keyA, UserID: f.userID, SubscriptionID: &f.subscriptionID, SubscriptionCost: cost,
		TotalQuotaSpendSnapshot: &service.TotalQuotaSpendSnapshot{
			SubscriptionID: f.subscriptionID, EventIDs: ids, OverflowEventID: ids[len(ids)-1], TakenAt: time.Now(),
		},
	}
}

func TestUsageBillingRepositoryApplyBatch_PerCommandQuotaSnapshot(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB)
	f, expired, permanent, finite := batchQuotaFixture(t, client)
	cmds := []*service.UsageBillingCommand{
		batchQuotaCommand(f, uuid.NewString(), 2, expired, permanent),
		batchQuotaCommand(f, uuid.NewString(), 8, finite, permanent),
		{RequestID: uuid.NewString(), APIKeyID: f.keyA, UserID: f.userID, SubscriptionID: &f.subscriptionID, SubscriptionCost: 1},
	}
	// A stale parent snapshot must never replace another command's snapshot.
	ctx = service.WithTotalQuotaSpendSnapshot(ctx, cmds[0].TotalQuotaSpendSnapshot)
	outcomes, err := repo.ApplyBatch(ctx, cmds)
	require.NoError(t, err)
	for _, outcome := range outcomes {
		require.NoError(t, outcome.Err)
		require.True(t, outcome.Result.Applied)
	}
	for id, expected := range map[int64]float64{expired: 2, permanent: 4, finite: 5} {
		var used float64
		require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT quota_used_usd FROM user_subscription_quota_events WHERE id=$1", id).Scan(&used))
		require.Equal(t, expected, used)
	}
}

func TestUsageBillingRepositoryApplyBatch_DifferentSnapshotsConcurrentWithSingle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB)
	f, expired, permanent, finite := batchQuotaFixture(t, client)
	const count = 20
	start := make(chan struct{})
	errs := make(chan error, count)
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			if i%2 == 0 {
				_, err := repo.ApplyBatch(ctx, []*service.UsageBillingCommand{
					batchQuotaCommand(f, uuid.NewString(), 1, expired, permanent),
					batchQuotaCommand(f, uuid.NewString(), 1, finite, permanent),
				})
				errs <- err
				return
			}
			// The single path is also used by atomic Grok and Engine quota
			// settlement through incrementSubscriptionUsage.
			_, err := repo.Apply(ctx, batchQuotaCommand(f, uuid.NewString(), 1, finite, permanent))
			errs <- err
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	var total float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT SUM(quota_used_usd) FROM user_subscription_quota_events WHERE user_subscription_id=$1", f.subscriptionID).Scan(&total))
	require.Equal(t, float64(count/2*3), total)
}
