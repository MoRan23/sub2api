//go:build unit

package service

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestTopupPaymentUsesCNYPriceAndExistingFee(t *testing.T) {
	t.Parallel()
	amountText, amount, err := calculateCreateOrderPayAmountForOrderType(29.97, 2.5, "CNY", payment.OrderTypeSubscriptionTopup, 7.15)
	require.NoError(t, err)
	require.Equal(t, "30.72", amountText)
	require.Equal(t, 30.72, amount)
	_, _, err = calculateCreateOrderPayAmountForOrderType(29.97, 0, "USD", payment.OrderTypeSubscriptionTopup, 7.15)
	require.Equal(t, "TOPUP_CURRENCY_UNSUPPORTED", infraerrors.Reason(err))
}

func TestTopupPaymentRequiresInitializedService(t *testing.T) {
	t.Parallel()
	svc := &PaymentService{}
	_, err := svc.validateOrderInput(context.Background(), CreateOrderRequest{OrderType: payment.OrderTypeSubscriptionTopup}, &PaymentConfig{})
	require.Equal(t, "TOPUP_UNAVAILABLE", infraerrors.Reason(err))
	require.True(t, isSupportedPaymentOrderType(payment.OrderTypeSubscriptionTopup))
}

func TestTopupPaymentRefundEntrypointsRejectBeforeProviderOrWallet(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPaymentFulfillmentSubscriptionOrder(t, ctx, client, OrderStatusCompleted, time.Now())
	_, err := client.PaymentOrder.UpdateOneID(order.ID).SetOrderType(payment.OrderTypeSubscriptionTopup).Save(ctx)
	require.NoError(t, err)
	// Intentionally no provider or wallet dependencies: rejection must precede them.
	svc := &PaymentService{entClient: client}
	_, err = svc.validateRefundRequest(ctx, order.ID, order.UserID)
	require.Equal(t, "TOPUP_REFUND_CONTACT_ADMIN", infraerrors.Reason(err))
	plan, result, err := svc.PrepareRefund(ctx, order.ID, 0, "", true, true)
	require.Nil(t, plan)
	require.Nil(t, result)
	require.Equal(t, "TOPUP_REFUND_CONTACT_ADMIN", infraerrors.Reason(err))
	result, err = svc.QueryAndFinalizeRefund(ctx, order.ID)
	require.Nil(t, result)
	require.Equal(t, "TOPUP_REFUND_CONTACT_ADMIN", infraerrors.Reason(err))
	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusCompleted, reloaded.Status)
}

func TestTopupPaymentWeChatOAuthPreservesSKUQuantity(t *testing.T) {
	t.Parallel()
	startURL, err := buildWeChatPaymentOAuthStartURL(CreateOrderRequest{
		PaymentType: payment.TypeWxpay,
		OrderType:   payment.OrderTypeSubscriptionTopup,
		SKUID:       23,
		Quantity:    3,
		Amount:      29.97,
		SrcURL:      "https://app.example.com/purchase",
	}, "snsapi_base")
	require.NoError(t, err)
	parsed, err := url.Parse(startURL)
	require.NoError(t, err)
	require.Equal(t, "23", parsed.Query().Get("sku_id"))
	require.Equal(t, "3", parsed.Query().Get("quantity"))
	require.Equal(t, payment.OrderTypeSubscriptionTopup, parsed.Query().Get("order_type"))
	require.Equal(t, payment.TypeWxpay, parsed.Query().Get("payment_type"))

	resume := NewPaymentResumeService([]byte("0123456789abcdef0123456789abcdef"))
	token, err := resume.CreateWeChatPaymentResumeToken(WeChatPaymentResumeClaims{
		OpenID:      "topup-payer",
		PaymentType: payment.TypeWxpay,
		OrderType:   payment.OrderTypeSubscriptionTopup,
		SKUID:       23,
		Quantity:    3,
	})
	require.NoError(t, err)
	claims, err := resume.ParseWeChatPaymentResumeToken(token)
	require.NoError(t, err)
	require.Equal(t, int64(23), claims.SKUID)
	require.Equal(t, 3, claims.Quantity)
	require.Equal(t, payment.OrderTypeSubscriptionTopup, claims.OrderType)
}
