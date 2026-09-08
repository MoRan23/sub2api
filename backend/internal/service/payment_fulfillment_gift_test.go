//go:build unit

package service

import (
	"context"
	"math"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/stretchr/testify/require"
)

type paymentFulfillmentGiftUserRepo struct {
	mockUserRepo
	creditWalletFn func(context.Context, int64, float64, float64) error
}

func (r *paymentFulfillmentGiftUserRepo) CreditRedeemWallet(ctx context.Context, userID int64, balance, gift float64) error {
	return r.creditWalletFn(ctx, userID, balance, gift)
}

func createPaymentFulfillmentGiftOrder(t *testing.T, ctx context.Context, client *dbent.Client) *dbent.PaymentOrder {
	t.Helper()
	order := createPaymentFulfillmentSubscriptionOrder(t, ctx, client, OrderStatusPaid, time.Now())
	order, err := client.PaymentOrder.UpdateOneID(order.ID).
		SetOrderType(payment.OrderTypeBalance).
		ClearPlanID().
		ClearSubscriptionGroupID().
		ClearSubscriptionDays().
		SetGiftRatio(12.3456).
		SetGiftAmount(9.87648).
		Save(ctx)
	require.NoError(t, err)
	return order
}

func TestPaymentFulfillmentGiftBypassesLimitAndRemainsIdempotent(t *testing.T) {
	for _, existingUnused := range []bool{false, true} {
		name := "new code"
		if existingUnused {
			name = "existing unused code"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			client := newPaymentConfigServiceTestClient(t)
			ensurePaymentAuditOrderActionUniqueIndex(t, ctx, client)
			order := createPaymentFulfillmentGiftOrder(t, ctx, client)
			redeemRepo := &paymentFulfillmentRedeemRepo{}
			if existingUnused {
				redeemRepo.codesByCode = map[string]*RedeemCode{
					order.RechargeCode: {
						ID: 101, Code: order.RechargeCode, Type: RedeemTypeBalance,
						Value: order.Amount, GiftRatio: order.GiftRatio, GiftValue: order.GiftAmount,
						Status: StatusUnused,
					},
				}
			}
			walletCalls := 0
			userRepo := &paymentFulfillmentGiftUserRepo{
				mockUserRepo: mockUserRepo{getByIDUser: &User{ID: order.UserID}},
				creditWalletFn: func(txCtx context.Context, userID int64, balance, gift float64) error {
					walletCalls++
					require.Equal(t, order.UserID, userID)
					require.InDelta(t, order.Amount, balance, 1e-8)
					require.InDelta(t, order.GiftAmount, gift, 1e-8)
					tx := dbent.TxFromContext(txCtx)
					require.NotNil(t, tx, "both wallets must be credited in the redemption transaction")
					return tx.Client().User.UpdateOneID(userID).
						AddBalance(balance).
						AddGiftBalance(gift).
						AddTotalRecharged(balance).
						Exec(txCtx)
				},
			}
			cache := &paymentFulfillmentRedeemCacheStub{count: redeemMaxFailedAttempts}
			inviterID := int64(9001)
			affiliateRepo := &paymentFulfillmentAffiliateRepoStub{
				inviteeSummary: &AffiliateSummary{
					UserID: order.UserID, AffCode: "INVITEE", InviterID: &inviterID, CreatedAt: time.Now().Add(-time.Hour),
				},
				inviterSummary: &AffiliateSummary{
					UserID: inviterID, AffCode: "INVITER", CreatedAt: time.Now().Add(-2 * time.Hour),
				},
			}
			settings := NewSettingService(&paymentFulfillmentSettingRepoStub{values: map[string]string{
				SettingKeyAffiliateEnabled: "true", SettingKeyAffiliateRebateRate: "10", SettingKeyAffiliateRebateFreezeHours: "0",
			}}, nil)
			affiliateSvc := NewAffiliateService(affiliateRepo, settings, nil, nil)
			redeemSvc := NewRedeemService(redeemRepo, userRepo, nil, cache, nil, client, nil, affiliateSvc)
			svc := &PaymentService{entClient: client, redeemService: redeemSvc, userRepo: userRepo, affiliateService: affiliateSvc}

			require.NoError(t, svc.ExecuteBalanceFulfillment(ctx, order.ID))
			require.NoError(t, svc.ExecuteBalanceFulfillment(ctx, order.ID))
			// Recover a committed redemption whose worker never recorded completion.
			_, err := client.PaymentOrder.UpdateOneID(order.ID).
				SetStatus(OrderStatusRecharging).
				SetUpdatedAt(time.Now().Add(-paymentFulfillmentLeaseDuration - time.Minute)).
				ClearCompletedAt().
				Save(ctx)
			require.NoError(t, err)
			require.NoError(t, svc.ExecuteBalanceFulfillment(ctx, order.ID))

			require.Equal(t, 1, walletCalls)
			require.Len(t, redeemRepo.useCalls, 1)
			if existingUnused {
				require.Zero(t, redeemRepo.createCalls)
			} else {
				require.Equal(t, 1, redeemRepo.createCalls)
			}
			require.Zero(t, cache.getCalls)
			require.Zero(t, cache.incrementCalls)
			require.Equal(t, 1, cache.acquireCalls)
			require.Equal(t, 1, cache.releaseCalls)
			require.Equal(t, redeemMaxFailedAttempts, cache.count)
			require.Len(t, affiliateRepo.accrueCalls, 1)
			require.InDelta(t, order.Amount*0.1, affiliateRepo.accrueCalls[0].amount, 1e-8)
			require.NotNil(t, affiliateRepo.accrueCalls[0].sourceOrderID)
			require.Equal(t, order.ID, *affiliateRepo.accrueCalls[0].sourceOrderID)
			require.True(t, redeemCodeMatchesBalanceOrder(redeemRepo.codesByCode[order.RechargeCode], order))

			user, err := client.User.Get(ctx, order.UserID)
			require.NoError(t, err)
			require.InDelta(t, order.Amount, user.Balance, 1e-8)
			require.InDelta(t, order.GiftAmount, user.GiftBalance, 1e-8)
			require.InDelta(t, order.Amount, user.TotalRecharged, 1e-8)
			reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
			require.NoError(t, err)
			require.Equal(t, OrderStatusCompleted, reloaded.Status)
		})
	}
}

func TestPaymentFulfillmentGiftRejectsMismatchedOrForeignCodes(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*RedeemCode, *dbent.PaymentOrder)
		wantErr string
	}{
		{name: "ordinary snapshot", mutate: func(code *RedeemCode, _ *dbent.PaymentOrder) { code.Value++ }, wantErr: "wallet amounts"},
		{name: "gift ratio snapshot", mutate: func(code *RedeemCode, _ *dbent.PaymentOrder) { code.GiftRatio++ }, wantErr: "wallet amounts"},
		{name: "gift value snapshot", mutate: func(code *RedeemCode, _ *dbent.PaymentOrder) { code.GiftValue++ }, wantErr: "wallet amounts"},
		{name: "gift ratio NaN", mutate: func(code *RedeemCode, _ *dbent.PaymentOrder) { code.GiftRatio = math.NaN() }, wantErr: "wallet amounts"},
		{name: "gift value infinite", mutate: func(code *RedeemCode, _ *dbent.PaymentOrder) { code.GiftValue = math.Inf(1) }, wantErr: "wallet amounts"},
		{name: "foreign used code", mutate: func(code *RedeemCode, order *dbent.PaymentOrder) {
			otherUserID := order.UserID + 1
			code.Status, code.UsedBy = StatusUsed, &otherUserID
		}, wantErr: "user mismatch"},
		{name: "used gift mismatch", mutate: func(code *RedeemCode, order *dbent.PaymentOrder) {
			code.Status, code.UsedBy, code.GiftValue = StatusUsed, &order.UserID, 0
		}, wantErr: "wallet amounts"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			client := newPaymentConfigServiceTestClient(t)
			order := createPaymentFulfillmentGiftOrder(t, ctx, client)
			code := &RedeemCode{
				ID: 101, Code: order.RechargeCode, Type: RedeemTypeBalance,
				Value: order.Amount, GiftRatio: order.GiftRatio, GiftValue: order.GiftAmount, Status: StatusUnused,
			}
			tt.mutate(code, order)
			redeemRepo := &paymentFulfillmentRedeemRepo{paymentOrderLifecycleRedeemRepo: paymentOrderLifecycleRedeemRepo{
				codesByCode: map[string]*RedeemCode{order.RechargeCode: code},
			}}
			cache := &paymentFulfillmentRedeemCacheStub{count: redeemMaxFailedAttempts}
			svc := &PaymentService{entClient: client, redeemService: &RedeemService{redeemRepo: redeemRepo, cache: cache}}

			err := svc.ExecuteBalanceFulfillment(ctx, order.ID)
			require.ErrorContains(t, err, tt.wantErr)
			require.Empty(t, redeemRepo.useCalls)
			require.Zero(t, redeemRepo.createCalls)
			require.Zero(t, cache.getCalls)
			require.Zero(t, cache.incrementCalls)
			require.Zero(t, cache.acquireCalls)
			user, err := client.User.Get(ctx, order.UserID)
			require.NoError(t, err)
			require.Zero(t, user.Balance)
			require.Zero(t, user.GiftBalance)
			require.Zero(t, user.TotalRecharged)
			reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
			require.NoError(t, err)
			require.Equal(t, OrderStatusFailed, reloaded.Status)
			require.Nil(t, reloaded.CompletedAt)
		})
	}
}
