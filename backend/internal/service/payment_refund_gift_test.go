//go:build unit

package service

import (
	"context"
	"errors"
	"math"
	"strconv"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentauditlog"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestCalculateRefundGiftAmountUsesWalletPrecision(t *testing.T) {
	require.InDelta(t, 9.9995, calculateRefundGiftAmount(100, 10, 99.995, "USD"), 1e-12)
	require.InDelta(t, 10, calculateRefundGiftAmount(100, 10, 100.000000004, "USD"), 1e-12)
}

func TestRequestRefundUsesWalletPrecisionForBalanceAvailability(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	user, err := client.User.Create().
		SetEmail("refund-wallet-precision@example.com").
		SetPasswordHash("hash").
		SetUsername("refund-wallet-precision").
		Save(ctx)
	require.NoError(t, err)
	provider, err := client.PaymentProviderInstance.Create().
		SetProviderKey(payment.TypeStripe).
		SetName("refund-wallet-precision-provider").
		SetConfig("{}").
		SetSupportedTypes("stripe").
		SetEnabled(true).
		SetRefundEnabled(true).
		SetAllowUserRefund(true).
		Save(ctx)
	require.NoError(t, err)
	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(100).
		SetGiftAmount(20).
		SetPayAmount(100).
		SetFeeRate(0).
		SetRechargeCode("REFUND-WALLET-PRECISION").
		SetOutTradeNo("refund_wallet_precision").
		SetPaymentType(payment.TypeStripe).
		SetPaymentTradeNo("pi_refund_wallet_precision").
		SetOrderType(payment.OrderTypeBalance).
		SetStatus(OrderStatusCompleted).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetPaidAt(time.Now()).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		SetProviderInstanceID(strconv.FormatInt(provider.ID, 10)).
		Save(ctx)
	require.NoError(t, err)

	svc := &PaymentService{
		entClient: client,
		userRepo: &mockUserRepo{getByIDUser: &User{
			ID:          user.ID,
			Balance:     99.999999996,
			GiftBalance: 19.999999996,
		}},
	}
	require.NoError(t, svc.RequestRefund(ctx, order.ID, user.ID, "wallet precision"))

	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusRefundRequested, reloaded.Status)
}

func TestPrepDeductUsesWalletPrecisionForBalanceAvailability(t *testing.T) {
	plan := &RefundPlan{RefundAmount: 100, GiftBalanceToDeduct: 20}
	svc := &PaymentService{userRepo: &mockUserRepo{getByIDUser: &User{
		Balance:     99.999999996,
		GiftBalance: 19.999999996,
	}}}

	result := svc.prepDeduct(context.Background(), &dbent.PaymentOrder{
		UserID:    1,
		OrderType: payment.OrderTypeBalance,
	}, plan, false)

	require.Nil(t, result)
	require.Equal(t, payment.DeductionTypeBalance, plan.DeductionType)
	require.False(t, walletAmountLessThan(plan.BalanceToDeduct, 100))
	require.False(t, walletAmountLessThan(plan.GiftBalanceToDeduct, 20))
}

func TestPrepareRefundRejectsWalletOverageBelowCurrencyTolerance(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderForTest(t, ctx, client, "wallet-overage")
	_, err := client.PaymentOrder.UpdateOneID(order.ID).SetStatus(OrderStatusCompleted).Save(ctx)
	require.NoError(t, err)
	svc := &PaymentService{entClient: client}

	plan, result, err := svc.PrepareRefund(ctx, order.ID, 100.005, "", false, false)
	require.Nil(t, plan)
	require.Nil(t, result)
	require.Error(t, err)
	require.Equal(t, "REFUND_AMOUNT_EXCEEDED", infraerrors.Reason(err))
}

func TestMarkRefundOKUsesWalletPrecisionForFullRefund(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderForTest(t, ctx, client, "wallet-full-status")
	_, err := client.PaymentOrder.UpdateOneID(order.ID).SetStatus(OrderStatusRefunding).Save(ctx)
	require.NoError(t, err)

	result, err := (&PaymentService{entClient: client}).markRefundOk(ctx, &RefundPlan{
		OrderID:      order.ID,
		Order:        order,
		RefundAmount: 99.999999996,
		Reason:       "wallet precision full refund",
	})
	require.NoError(t, err)
	require.True(t, result.Success)
	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusRefunded, reloaded.Status)
}

func TestPendingRefundPreservesNoDeductBalance(t *testing.T) {
	for _, orderType := range []string{payment.OrderTypeBalance, payment.OrderTypeSubscription} {
		t.Run(orderType, func(t *testing.T) {
			ctx := context.Background()
			client := newPaymentConfigServiceTestClient(t)
			order := createPendingRefundOrderForTest(t, ctx, client, "no-deduct-"+orderType)
			order, err := client.PaymentOrder.UpdateOneID(order.ID).
				SetStatus(OrderStatusCompleted).
				SetOrderType(orderType).
				SetGiftAmount(20).
				Save(ctx)
			require.NoError(t, err)

			authCache := &mockAuthCacheInvalidator{}
			billingCache := &mockBillingCache{}
			svc := &PaymentService{
				entClient:    client,
				loadBalancer: &captureLoadBalancer{},
				userRepo: &mockUserRepo{deductAvailableBalanceFn: func(context.Context, int64, float64) (float64, error) {
					t.Fatal("deduct_balance=false must not deduct a wallet")
					return 0, nil
				}},
				redeemService: &RedeemService{
					authCacheInvalidator: authCache,
					billingCacheService:  &BillingCacheService{cache: billingCache},
				},
			}

			plan, early, err := svc.PrepareRefund(ctx, order.ID, order.Amount, "no deduction", false, false)
			require.NoError(t, err)
			require.Nil(t, early)
			require.False(t, plan.DeductBalance)
			require.Equal(t, payment.DeductionTypeNone, plan.DeductionType)
			require.Zero(t, plan.BalanceToDeduct)
			require.Zero(t, plan.GiftBalanceToDeduct)

			_, err = client.PaymentOrder.UpdateOneID(order.ID).SetStatus(OrderStatusRefunding).Save(ctx)
			require.NoError(t, err)
			pendingResult, err := svc.markRefundPending(ctx, plan, &payment.RefundResponse{RefundID: "rf_no_deduct", Status: payment.ProviderStatusPending})
			require.NoError(t, err)
			require.False(t, pendingResult.Success)
			pendingDetail, err := svc.latestRefundPendingDetail(ctx, order.ID)
			require.NoError(t, err)
			require.NotNil(t, pendingDetail.DeductBalance)
			require.False(t, *pendingDetail.DeductBalance)
			require.Equal(t, payment.DeductionTypeNone, pendingDetail.DeductionType)

			restoreProvider := replacePaymentProviderFactoryForTest(t, &refundQueryProviderTestDouble{
				refundResponse: &payment.RefundResponse{RefundID: "rf_no_deduct", Status: payment.ProviderStatusSuccess},
			})
			defer restoreProvider()

			result, err := svc.QueryAndFinalizeRefund(ctx, order.ID)
			require.NoError(t, err)
			require.True(t, result.Success)
			require.Zero(t, result.BalanceDeducted)
			require.Zero(t, result.GiftBalanceDeducted)
			require.Zero(t, result.SubDaysDeducted)
			require.Empty(t, authCache.invalidatedUserIDs)
			require.Zero(t, billingCache.invalidateCallCount.Load())
		})
	}
}

func TestPendingRefundFinalFailureRestoresFailedWalletRollback(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderForTest(t, ctx, client, "failed-rollback-restored")
	order, err := client.PaymentOrder.UpdateOneID(order.ID).
		SetGiftAmount(20).
		Save(ctx)
	require.NoError(t, err)

	repo := &refundWalletRepoTestDouble{mockUserRepo: &mockUserRepo{}, client: client}
	authCache := &mockAuthCacheInvalidator{}
	billingCache := &mockBillingCache{}
	svc := &PaymentService{
		entClient:    client,
		loadBalancer: &captureLoadBalancer{},
		userRepo:     repo,
		redeemService: &RedeemService{
			authCacheInvalidator: authCache,
			billingCacheService:  &BillingCacheService{cache: billingCache},
		},
	}
	// Legacy pending rows held the original wallet deduction when their
	// attempted rollback failed. They must still settle correctly after upgrade.
	_, err = client.PaymentAuditLog.Update().
		Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(order.ID, 10)), paymentauditlog.ActionEQ("REFUND_PENDING")).
		SetDetail(`{"refundID":"rf_restore","deductionRollbackOK":false,"deductBalance":true,"deductionType":"balance","balanceDeducted":80,"giftBalanceDeducted":20}`).
		Save(ctx)
	require.NoError(t, err)
	pendingDetail, err := svc.latestRefundPendingDetail(ctx, order.ID)
	require.NoError(t, err)
	require.False(t, pendingDetail.DeductionRollbackOK)
	require.Equal(t, 80.0, pendingDetail.BalanceDeducted)
	require.Equal(t, 20.0, pendingDetail.GiftBalanceDeducted)

	restoreProvider := replacePaymentProviderFactoryForTest(t, &refundQueryProviderTestDouble{
		refundResponse: &payment.RefundResponse{RefundID: "rf_restore", Status: payment.ProviderStatusFailed},
	})
	defer restoreProvider()

	result, err := svc.QueryAndFinalizeRefund(ctx, order.ID)
	require.NoError(t, err)
	require.False(t, result.Success)
	user, err := client.User.Get(ctx, order.UserID)
	require.NoError(t, err)
	require.Equal(t, 80.0, user.Balance)
	require.Equal(t, 20.0, user.GiftBalance)
	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusRefundFailed, reloaded.Status)

	authCache.mu.Lock()
	require.Equal(t, []int64{order.UserID}, authCache.invalidatedUserIDs)
	authCache.mu.Unlock()
	require.Eventually(t, func() bool {
		return billingCache.invalidateCallCount.Load() == 1
	}, 2*time.Second, 10*time.Millisecond)
}

func TestPendingRefundWalletRollbackAndStateAreAtomic(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderForTest(t, ctx, client, "pending-wallet-atomic")

	_, err := client.PaymentAuditLog.Delete().
		Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(order.ID, 10)), paymentauditlog.ActionEQ("REFUND_PENDING")).
		Exec(ctx)
	require.NoError(t, err)
	order, err = client.PaymentOrder.UpdateOneID(order.ID).
		SetStatus(OrderStatusRefunding).
		SetGiftAmount(20).
		Save(ctx)
	require.NoError(t, err)
	_, err = client.User.UpdateOneID(order.UserID).
		SetBalance(0).
		SetGiftBalance(0).
		Save(ctx)
	require.NoError(t, err)

	_, err = client.ExecContext(ctx, `
		CREATE TRIGGER fail_refund_pending_audit
		BEFORE INSERT ON payment_audit_logs
		WHEN NEW.action = 'REFUND_PENDING'
		BEGIN
			SELECT RAISE(ABORT, 'injected pending audit failure');
		END
	`)
	require.NoError(t, err)

	authCache := &mockAuthCacheInvalidator{}
	billingCache := &mockBillingCache{}
	svc := &PaymentService{
		entClient: client,
		userRepo:  &refundWalletRepoTestDouble{mockUserRepo: &mockUserRepo{}, client: client},
		redeemService: &RedeemService{
			authCacheInvalidator: authCache,
			billingCacheService:  &BillingCacheService{cache: billingCache},
		},
	}
	plan := &RefundPlan{
		OrderID:             order.ID,
		Order:               order,
		RefundAmount:        order.Amount,
		GatewayAmount:       order.PayAmount,
		Reason:              "pending audit failure",
		DeductBalance:       true,
		DeductionType:       payment.DeductionTypeBalance,
		BalanceToDeduct:     80,
		GiftBalanceToDeduct: 20,
	}

	result, err := svc.markRefundPending(ctx, plan, &payment.RefundResponse{RefundID: "rf_atomic", Status: payment.ProviderStatusPending})
	require.Nil(t, result)
	require.ErrorContains(t, err, "injected pending audit failure")

	user, err := client.User.Get(ctx, order.UserID)
	require.NoError(t, err)
	require.Zero(t, user.Balance)
	require.Zero(t, user.GiftBalance)
	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusRefunding, reloaded.Status)
	require.Equal(t, 80.0, plan.BalanceToDeduct)
	require.Equal(t, 20.0, plan.GiftBalanceToDeduct)
	require.Empty(t, authCache.invalidatedUserIDs)
	require.Zero(t, billingCache.invalidateCallCount.Load())
}

func TestPendingRefundSuccessInvalidatesWalletCaches(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderForTest(t, ctx, client, "success-invalidates-wallet")
	_, err := client.User.UpdateOneID(order.UserID).SetBalance(100).Save(ctx)
	require.NoError(t, err)

	repo := &refundWalletRepoTestDouble{mockUserRepo: &mockUserRepo{}, client: client}
	authCache := &mockAuthCacheInvalidator{}
	billingCache := &mockBillingCache{}
	svc := &PaymentService{
		entClient:    client,
		loadBalancer: &captureLoadBalancer{},
		userRepo:     repo,
		redeemService: &RedeemService{
			authCacheInvalidator: authCache,
			billingCacheService:  &BillingCacheService{cache: billingCache},
		},
	}
	restoreProvider := replacePaymentProviderFactoryForTest(t, &refundQueryProviderTestDouble{
		refundResponse: &payment.RefundResponse{RefundID: "rf_success_cache", Status: payment.ProviderStatusSuccess},
	})
	defer restoreProvider()

	result, err := svc.QueryAndFinalizeRefund(ctx, order.ID)
	require.NoError(t, err)
	require.True(t, result.Success)
	user, err := client.User.Get(ctx, order.UserID)
	require.NoError(t, err)
	require.Zero(t, user.Balance)
	authCache.mu.Lock()
	require.Equal(t, []int64{order.UserID}, authCache.invalidatedUserIDs)
	authCache.mu.Unlock()
	require.Eventually(t, func() bool {
		return billingCache.invalidateCallCount.Load() == 1
	}, 2*time.Second, 10*time.Millisecond)
}

func TestPendingRefundNonForceShortageRollsBackWalletDeduction(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderForTest(t, ctx, client, "non-force-wallet-shortage")
	order, err := client.PaymentOrder.UpdateOneID(order.ID).SetGiftAmount(20).Save(ctx)
	require.NoError(t, err)
	_, err = client.User.UpdateOneID(order.UserID).SetBalance(100).SetGiftBalance(10).Save(ctx)
	require.NoError(t, err)

	repo := &refundWalletRepoTestDouble{mockUserRepo: &mockUserRepo{}, client: client}
	svc := &PaymentService{entClient: client, userRepo: repo}
	result, err := svc.finalizePendingRefundSuccess(ctx, svc.refundFinalizePlan(order, true))
	require.Nil(t, result)
	require.Error(t, err)
	require.Equal(t, "BALANCE_NOT_ENOUGH", infraerrors.Reason(err))

	user, err := client.User.Get(ctx, order.UserID)
	require.NoError(t, err)
	require.Equal(t, 100.0, user.Balance)
	require.Equal(t, 10.0, user.GiftBalance)
	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusRefundPending, reloaded.Status)
}

func TestPendingRefundHoldsBothWalletsUntilGatewaySettles(t *testing.T) {
	for _, status := range []string{payment.ProviderStatusSuccess, payment.ProviderStatusFailed} {
		t.Run(status, func(t *testing.T) {
			ctx := context.Background()
			client := newPaymentConfigServiceTestClient(t)
			order := createCompletedRefundableOrderForTest(t, ctx, client, "hold-gift-"+status)
			order, err := client.PaymentOrder.UpdateOneID(order.ID).SetGiftAmount(20).Save(ctx)
			require.NoError(t, err)
			_, err = client.User.UpdateOneID(order.UserID).SetBalance(100).SetGiftBalance(20).Save(ctx)
			require.NoError(t, err)
			repo := &refundWalletRepoTestDouble{
				mockUserRepo: &mockUserRepo{getByIDUser: &User{ID: order.UserID, Balance: 100, GiftBalance: 20}},
				client:       client,
			}
			restore := replacePaymentProviderFactoryForTest(t, &pendingRefundProviderTestDouble{queryStatus: status})
			defer restore()
			svc := &PaymentService{entClient: client, loadBalancer: &captureLoadBalancer{}, userRepo: repo}
			plan, early, err := svc.PrepareRefund(ctx, order.ID, 100, "both wallets", false, true)
			require.NoError(t, err)
			require.Nil(t, early)
			result, err := svc.ExecuteRefund(ctx, plan)
			require.NoError(t, err)
			require.False(t, result.Success)
			detail, err := svc.latestRefundPendingDetail(ctx, order.ID)
			require.NoError(t, err)
			require.True(t, detail.DeductionHeld)
			require.Equal(t, 100.0, detail.BalanceDeducted)
			require.Equal(t, 20.0, detail.GiftBalanceDeducted)
			user, err := client.User.Get(ctx, order.UserID)
			require.NoError(t, err)
			require.Zero(t, user.Balance)
			require.Zero(t, user.GiftBalance)

			result, err = svc.QueryAndFinalizeRefund(ctx, order.ID)
			require.NoError(t, err)
			user, err = client.User.Get(ctx, order.UserID)
			require.NoError(t, err)
			if status == payment.ProviderStatusSuccess {
				require.True(t, result.Success)
				require.Equal(t, 100.0, result.BalanceDeducted)
				require.Equal(t, 20.0, result.GiftBalanceDeducted)
				require.Zero(t, user.Balance)
				require.Zero(t, user.GiftBalance)
			} else {
				require.False(t, result.Success)
				require.Equal(t, 100.0, user.Balance)
				require.Equal(t, 20.0, user.GiftBalance)
			}
			_, err = svc.QueryAndFinalizeRefund(ctx, order.ID)
			require.Equal(t, "INVALID_STATUS", infraerrors.Reason(err))
		})
	}
}

func TestPendingRefundFailedAuditRollsBackBothWalletRestores(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderWithDetailForTest(t, ctx, client, "failed-gift-audit",
		`{"refundID":"rf_test","deductionHeld":true,"deductionType":"balance","balanceDeducted":100,"giftBalanceDeducted":20}`)
	_, err := client.ExecContext(ctx, `
		CREATE TRIGGER fail_refund_failed_audit
		BEFORE INSERT ON payment_audit_logs
		WHEN NEW.action = 'REFUND_FAILED'
		BEGIN
			SELECT RAISE(ABORT, 'injected failed audit failure');
		END
	`)
	require.NoError(t, err)
	repo := &refundWalletRepoTestDouble{mockUserRepo: &mockUserRepo{}, client: client}
	restore := replacePaymentProviderFactoryForTest(t, &pendingRefundProviderTestDouble{queryStatus: payment.ProviderStatusFailed})
	defer restore()
	svc := &PaymentService{entClient: client, loadBalancer: &captureLoadBalancer{}, userRepo: repo}

	result, err := svc.QueryAndFinalizeRefund(ctx, order.ID)
	require.Nil(t, result)
	require.ErrorContains(t, err, "injected failed audit failure")
	user, err := client.User.Get(ctx, order.UserID)
	require.NoError(t, err)
	require.Zero(t, user.Balance)
	require.Zero(t, user.GiftBalance)
	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusRefundPending, reloaded.Status)

	_, err = client.ExecContext(ctx, `DROP TRIGGER fail_refund_failed_audit`)
	require.NoError(t, err)
	result, err = svc.QueryAndFinalizeRefund(ctx, order.ID)
	require.NoError(t, err)
	require.False(t, result.Success)
	user, err = client.User.Get(ctx, order.UserID)
	require.NoError(t, err)
	require.Equal(t, 100.0, user.Balance)
	require.Equal(t, 20.0, user.GiftBalance)
}

type refundWalletRepoTestDouble struct {
	*mockUserRepo
	client          *dbent.Client
	restoreFailures int
}

func (r *refundWalletRepoTestDouble) DeductAvailableWallets(ctx context.Context, id int64, ordinary, gift float64) (WalletAmounts, error) {
	client := r.clientForContext(ctx)
	user, err := client.User.Get(ctx, id)
	if err != nil {
		return WalletAmounts{}, err
	}
	deducted := WalletAmounts{
		Ordinary: math.Max(0, math.Min(ordinary, user.Balance)),
		Gift:     math.Max(0, math.Min(gift, user.GiftBalance)),
	}
	_, err = client.User.UpdateOneID(id).
		AddBalance(-deducted.Ordinary).
		AddGiftBalance(-deducted.Gift).
		Save(ctx)
	return deducted, err
}

func (r *refundWalletRepoTestDouble) RestoreWallets(ctx context.Context, id int64, ordinary, gift float64) error {
	if r.restoreFailures > 0 {
		r.restoreFailures--
		return errors.New("injected wallet restore failure")
	}
	_, err := r.clientForContext(ctx).User.UpdateOneID(id).
		AddBalance(ordinary).
		AddGiftBalance(gift).
		Save(ctx)
	return err
}

func (r *refundWalletRepoTestDouble) clientForContext(ctx context.Context) *dbent.Client {
	if tx := dbent.TxFromContext(ctx); tx != nil {
		return tx.Client()
	}
	return r.client
}
