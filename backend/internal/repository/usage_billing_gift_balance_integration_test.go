//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestUsageBillingRepositoryApply_UsesBothWalletsAndReportsCombinedBalance(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB)
	user := mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("usage-billing-gift-%d@example.com", time.Now().UnixNano()),
		PasswordHash: "hash",
		Balance:      2,
		GiftBalance:  4,
	})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID: user.ID,
		Key:    "sk-usage-billing-gift-" + uuid.NewString(),
		Name:   "billing-gift",
	})

	first, err := repo.Apply(ctx, &service.UsageBillingCommand{
		RequestID: uuid.NewString(), APIKeyID: apiKey.ID, UserID: user.ID, BalanceCost: 5,
	})
	require.NoError(t, err)
	require.True(t, first.Applied)
	require.False(t, first.BalanceOverdrafted)
	require.NotNil(t, first.NewBalance)
	require.InDelta(t, 1, *first.NewBalance, 1e-8)
	assertPersistedWallets(t, ctx, user.ID, 0, 1, 0, 0)

	second, err := repo.Apply(ctx, &service.UsageBillingCommand{
		RequestID: uuid.NewString(), APIKeyID: apiKey.ID, UserID: user.ID, BalanceCost: 3,
	})
	require.NoError(t, err)
	require.True(t, second.Applied)
	require.True(t, second.BalanceOverdrafted)
	require.NotNil(t, second.NewBalance)
	require.InDelta(t, -2, *second.NewBalance, 1e-8)
	assertPersistedWallets(t, ctx, user.ID, -2, 0, 0, 0)
}

func TestUsageBillingRepositoryBatchImage_CaptureReturnsRemainderToOriginalWallets(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB)
	user, apiKey := createBatchImageGiftWalletFixture(t, ctx, client, 0.6, 0.8)
	batchID := "gift-capture-" + uuid.NewString()

	reserved, err := repo.ReserveBatchImageBalance(ctx, &service.BatchImageBalanceHoldCommand{
		RequestID: service.BatchImageHoldRequestID(batchID), APIKeyID: apiKey.ID,
		UserID: user.ID, BatchID: batchID, HoldAmount: 1,
	})
	require.NoError(t, err)
	require.True(t, reserved.Applied)
	require.InDelta(t, 0.4, *reserved.NewBalance, 1e-8)
	require.InDelta(t, 1, *reserved.FrozenBalance, 1e-8)
	assertPersistedWallets(t, ctx, user.ID, 0, 0.4, 0.6, 0.4)

	captured, err := repo.CaptureBatchImageBalance(ctx, &service.BatchImageBalanceHoldCommand{
		RequestID: service.BatchImageCaptureRequestID(batchID), APIKeyID: apiKey.ID,
		UserID: user.ID, BatchID: batchID, HoldAmount: 1, ActualAmount: 0.25,
	})
	require.NoError(t, err)
	require.True(t, captured.Applied)
	require.InDelta(t, 1.15, *captured.NewBalance, 1e-8)
	require.InDelta(t, 0, *captured.FrozenBalance, 1e-8)
	assertPersistedWallets(t, ctx, user.ID, 0.35, 0.8, 0, 0)
}

func TestUsageBillingRepositoryBatchImage_ReleaseRestoresOriginalWallets(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB)
	user, apiKey := createBatchImageGiftWalletFixture(t, ctx, client, 0.6, 0.8)
	batchID := "gift-release-" + uuid.NewString()

	_, err := repo.ReserveBatchImageBalance(ctx, &service.BatchImageBalanceHoldCommand{
		RequestID: service.BatchImageHoldRequestID(batchID), APIKeyID: apiKey.ID,
		UserID: user.ID, BatchID: batchID, HoldAmount: 1,
	})
	require.NoError(t, err)
	released, err := repo.ReleaseBatchImageBalance(ctx, &service.BatchImageBalanceHoldCommand{
		RequestID: service.BatchImageReleaseRequestID(batchID), APIKeyID: apiKey.ID,
		UserID: user.ID, BatchID: batchID, HoldAmount: 1,
	})
	require.NoError(t, err)
	require.True(t, released.Applied)
	require.InDelta(t, 1.4, *released.NewBalance, 1e-8)
	require.InDelta(t, 0, *released.FrozenBalance, 1e-8)
	assertPersistedWallets(t, ctx, user.ID, 0.6, 0.8, 0, 0)
}

func createBatchImageGiftWalletFixture(t *testing.T, _ context.Context, client *dbent.Client, balance, gift float64) (*service.User, *service.APIKey) {
	t.Helper()
	user := mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("batch-image-gift-%d@example.com", time.Now().UnixNano()),
		PasswordHash: "hash",
		Balance:      balance,
		GiftBalance:  gift,
	})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID: user.ID,
		Key:    "sk-batch-image-gift-" + uuid.NewString(),
		Name:   "batch-image-gift",
	})
	return user, apiKey
}

func assertPersistedWallets(t *testing.T, ctx context.Context, userID int64, balance, gift, frozen, frozenGift float64) {
	t.Helper()
	var gotBalance, gotGift, gotFrozen, gotFrozenGift float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT balance, gift_balance, frozen_balance, frozen_gift_balance
		FROM users WHERE id = $1
	`, userID).Scan(&gotBalance, &gotGift, &gotFrozen, &gotFrozenGift))
	require.InDelta(t, balance, gotBalance, 1e-8)
	require.InDelta(t, gift, gotGift, 1e-8)
	require.InDelta(t, frozen, gotFrozen, 1e-8)
	require.InDelta(t, frozenGift, gotFrozenGift, 1e-8)
}
