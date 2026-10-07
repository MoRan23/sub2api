//go:build unit

package service

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"net/url"
	"strconv"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	paymentprovider "github.com/Wei-Shaw/sub2api/internal/payment/provider"
	"github.com/stretchr/testify/require"
)

type easyPayRecoveryGiftProvider struct {
	*paymentprovider.EasyPay
	query *paymentOrderLifecycleQueryProvider
}

func (p *easyPayRecoveryGiftProvider) QueryOrder(ctx context.Context, reference string) (*payment.QueryOrderResponse, error) {
	return p.query.QueryOrder(ctx, reference)
}

func TestEasyPayRejectedCallbackRecoversGiftAndRebateOnce(t *testing.T) {
	for _, tc := range []struct {
		name         string
		paymentType  string
		upstreamType string
		activeVerify bool
	}{
		{name: "alipay periodic query", paymentType: payment.TypeAlipay, upstreamType: payment.TypeAlipay},
		{name: "custom method active query", paymentType: "easypay_usdt", upstreamType: "usdt", activeVerify: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			client := newPaymentConfigServiceTestClient(t)
			ensurePaymentAuditOrderActionUniqueIndex(t, ctx, client)
			config := map[string]string{
				"pid": "1000", "pkey": "recovery-test-merchant-key",
				"apiBase": "https://pay.example.com", "notifyUrl": "https://site.example.com/api/v1/payment/webhook/easypay",
				"returnUrl":     "https://site.example.com/payment/result",
				"customMethods": `[{"type":"easypay_usdt","upstreamType":"usdt","displayName":"USDT"}]`,
			}
			instance, err := client.PaymentProviderInstance.Create().
				SetProviderKey(payment.TypeEasyPay).
				SetName("easypay-recovery").
				SetConfig(encryptWebhookProviderConfig(t, config)).
				SetSupportedTypes(tc.paymentType).
				SetEnabled(true).
				Save(ctx)
			require.NoError(t, err)
			instanceID := strconv.FormatInt(instance.ID, 10)
			order := createPaymentFulfillmentGiftOrder(t, ctx, client)
			order, err = client.PaymentOrder.UpdateOneID(order.ID).
				SetStatus(OrderStatusPending).
				ClearPaidAt().
				SetPaymentTradeNo("").
				SetOutTradeNo("EASYPAY-CALLBACK-QUERY").
				SetPaymentType(tc.paymentType).
				SetPayAmount(81.60).
				SetFeeRate(2).
				SetProviderInstanceID(instanceID).
				SetProviderKey(payment.TypeEasyPay).
				SetProviderSnapshot(buildPaymentOrderProviderSnapshot(&payment.InstanceSelection{
					InstanceID: instanceID, ProviderKey: payment.TypeEasyPay, Config: config,
				}, CreateOrderRequest{})).
				Save(ctx)
			require.NoError(t, err)

			verifier, err := paymentprovider.NewEasyPay(instanceID, config)
			require.NoError(t, err)
			query := &paymentOrderLifecycleQueryProvider{
				key: payment.TypeEasyPay,
				resp: &payment.QueryOrderResponse{
					TradeNo: "easy-paid", Status: payment.ProviderStatusPaid,
					Amount: order.PayAmount, Metadata: verifier.MerchantIdentityMetadata(),
				},
			}
			gateway := &easyPayRecoveryGiftProvider{EasyPay: verifier, query: query}
			t.Cleanup(replacePaymentProviderFactoryForTest(t, gateway))

			walletCalls := 0
			userRepo := &paymentFulfillmentGiftUserRepo{
				mockUserRepo: mockUserRepo{getByIDUser: &User{ID: order.UserID}},
				creditWalletFn: func(txCtx context.Context, userID int64, ordinary, gift float64) error {
					walletCalls++
					require.Equal(t, order.UserID, userID)
					require.InDelta(t, order.Amount, ordinary, 1e-8)
					require.InDelta(t, order.GiftAmount, gift, 1e-8)
					tx := dbent.TxFromContext(txCtx)
					require.NotNil(t, tx)
					return tx.Client().User.UpdateOneID(userID).
						AddBalance(ordinary).AddGiftBalance(gift).AddTotalRecharged(ordinary).Exec(txCtx)
				},
			}
			inviterID := int64(9001)
			affiliateRepo := &paymentFulfillmentAffiliateRepoStub{
				inviteeSummary: &AffiliateSummary{
					UserID: order.UserID, AffCode: "INVITEE", InviterID: &inviterID, CreatedAt: time.Now().Add(-time.Hour),
				},
				inviterSummary: &AffiliateSummary{UserID: inviterID, AffCode: "INVITER", CreatedAt: time.Now().Add(-2 * time.Hour)},
			}
			settings := NewSettingService(&paymentFulfillmentSettingRepoStub{values: map[string]string{
				SettingKeyAffiliateEnabled: "true", SettingKeyAffiliateRebateRate: "10", SettingKeyAffiliateRebateFreezeHours: "0",
			}}, nil)
			affiliateSvc := NewAffiliateService(affiliateRepo, settings, nil, nil)
			redeemRepo := &paymentFulfillmentRedeemRepo{}
			svc := &PaymentService{
				entClient: client, loadBalancer: newWebhookProviderTestLoadBalancer(client),
				userRepo: userRepo, affiliateService: affiliateSvc,
				redeemService: NewRedeemService(redeemRepo, userRepo, nil, nil, nil, client, nil, affiliateSvc),
			}

			// A valid notification plus an unsigned empty extension must be rejected
			// specifically by the whitelist, before any payment is fulfilled.
			callback := url.Values{
				"pid": {"1000"}, "trade_no": {"easy-paid"}, "out_trade_no": {order.OutTradeNo},
				"type": {tc.upstreamType}, "name": {"balance recharge"}, "money": {"81.60"},
				"trade_status": {"TRADE_SUCCESS"}, "sign_type": {"MD5"},
			}
			signature := md5.Sum([]byte("money=81.60&name=balance recharge&out_trade_no=" + order.OutTradeNo +
				"&pid=1000&trade_no=easy-paid&trade_status=TRADE_SUCCESS&type=" + tc.upstreamType + config["pkey"]))
			callback.Set("sign", hex.EncodeToString(signature[:]))
			callback.Set("device", "")
			notification, err := gateway.VerifyNotification(ctx, callback.Encode(), nil)
			require.Nil(t, notification)
			require.ErrorContains(t, err, "unexpected notify param: device")
			require.Zero(t, walletCalls)
			require.Empty(t, affiliateRepo.accrueCalls)
			pending, err := client.PaymentOrder.Get(ctx, order.ID)
			require.NoError(t, err)
			require.Equal(t, OrderStatusPending, pending.Status)
			require.Nil(t, pending.PaidAt)

			if tc.activeVerify {
				// Preserve the existing periodic-query scope: custom EasyPay methods
				// recover through an authenticated active order query instead.
				recovered, err := svc.ReconcilePendingPaymentOrders(ctx)
				require.NoError(t, err)
				require.Zero(t, recovered)
				require.Zero(t, query.queryCalls)
				verified, err := svc.VerifyOrderByOutTradeNo(ctx, order.OutTradeNo, order.UserID)
				require.NoError(t, err)
				require.Equal(t, OrderStatusCompleted, verified.Status)
			} else {
				recovered, err := svc.ReconcilePendingPaymentOrders(ctx)
				require.NoError(t, err)
				require.Equal(t, 1, recovered)
			}

			// Repeating recovery and accepting the subsequent genuine callback
			// must not issue either wallet credit or the inviter rebate again.
			verified, err := svc.VerifyOrderByOutTradeNo(ctx, order.OutTradeNo, order.UserID)
			require.NoError(t, err)
			require.Equal(t, OrderStatusCompleted, verified.Status)
			recovered, err := svc.ReconcilePendingPaymentOrders(ctx)
			require.NoError(t, err)
			require.Zero(t, recovered)
			callback.Del("device")
			notification, err = gateway.VerifyNotification(ctx, callback.Encode(), nil)
			require.NoError(t, err)
			require.NoError(t, svc.HandlePaymentNotification(ctx, notification, gateway.ProviderKey()))
			require.NoError(t, svc.HandlePaymentNotification(ctx, notification, gateway.ProviderKey()))
			require.Equal(t, 1, query.queryCalls)
			require.Equal(t, order.OutTradeNo, query.lastQueryTradeNo)
			require.Zero(t, query.cancelCalls)
			require.Equal(t, 1, walletCalls)
			require.Equal(t, 1, redeemRepo.createCalls)
			require.Len(t, redeemRepo.useCalls, 1)
			require.Len(t, affiliateRepo.accrueCalls, 1)
			require.InDelta(t, order.Amount*0.1, affiliateRepo.accrueCalls[0].amount, 1e-8)
			require.NotNil(t, affiliateRepo.accrueCalls[0].sourceOrderID)
			require.Equal(t, order.ID, *affiliateRepo.accrueCalls[0].sourceOrderID)
			user, err := client.User.Get(ctx, order.UserID)
			require.NoError(t, err)
			require.InDelta(t, order.Amount, user.Balance, 1e-8)
			require.InDelta(t, order.GiftAmount, user.GiftBalance, 1e-8)
			require.InDelta(t, order.Amount, user.TotalRecharged, 1e-8)
			completed, err := client.PaymentOrder.Get(ctx, order.ID)
			require.NoError(t, err)
			require.Equal(t, OrderStatusCompleted, completed.Status)
			require.Equal(t, "easy-paid", completed.PaymentTradeNo)
			require.NotNil(t, completed.PaidAt)
		})
	}
}
