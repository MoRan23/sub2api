//go:build integration

package repository

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Wei-Shaw/sub2api/ent/paymentorder"
	"github.com/Wei-Shaw/sub2api/ent/setting"
	"github.com/Wei-Shaw/sub2api/ent/topupcard"
	"github.com/Wei-Shaw/sub2api/ent/topupmail"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/Wei-Shaw/sub2api/internal/payment/provider"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestSubscriptionTopupPaymentServiceGatewayLifecycle(t *testing.T) {
	ctx := context.Background()
	f := newTopupIntegrationFixture(t)
	skuID := f.sku(t, "card-a-"+uuid.NewString(), "card-b-"+uuid.NewString())
	_, err := f.client.User.UpdateOneID(f.user.ID).SetBalance(123).SetGiftBalance(45).Save(ctx)
	require.NoError(t, err)

	settings := NewSettingRepository(f.client)
	values := map[string]string{
		service.SettingPaymentEnabled:           "true",
		service.SettingEnabledPaymentTypes:      "easypay_usdt",
		service.SettingRechargeFeeRate:          "2",
		service.SettingBalanceRechargeMult:      "3",
		service.SettingBalanceGiftRatio:         "0.5",
		service.SettingSubscriptionUSDToCNYRate: "7.2",
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	previous, err := settings.GetMultiple(ctx, keys)
	require.NoError(t, err)
	require.NoError(t, settings.SetMultiple(ctx, values))
	t.Cleanup(func() {
		_, _ = f.client.Setting.Delete().Where(setting.KeyIn(keys...)).Exec(ctx)
		_ = settings.SetMultiple(ctx, previous)
	})

	var rejectCreate atomic.Bool
	rejectCreate.Store(true)
	requests := make(chan url.Values, 2)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/mapi.php" {
			requests <- r.PostForm
			if rejectCreate.Load() {
				_, _ = w.Write([]byte(`{"code":0,"msg":"simulated gateway failure"}`))
				return
			}
			_, _ = w.Write([]byte(`{"code":1,"trade_no":"topup-paid","payurl":"/checkout"}`))
			return
		}
		if r.URL.Path == "/api.php" {
			_, _ = fmt.Fprintf(w, `{"code":1,"status":1,"money":"25.18","trade_no":"topup-paid","out_trade_no":%q}`, r.Form.Get("out_trade_no"))
			return
		}
		http.NotFound(w, r)
	}))
	defer upstream.Close()
	key := []byte(strings.Repeat("x", 32))
	config := service.NewPaymentConfigService(f.client, settings, key)
	providerConfig := map[string]string{
		"pid": "1000", "pkey": "topup-test-merchant-secret", "apiBase": upstream.URL,
		"notifyUrl":     "https://app.example.test/api/v1/payment/webhook/easypay",
		"returnUrl":     "https://app.example.test/payment/result",
		"customMethods": `[{"type":"easypay_usdt","upstreamType":"usdt","displayName":"USDT"}]`,
	}
	instance, err := config.CreateProviderInstance(ctx, service.CreateProviderInstanceRequest{
		ProviderKey: payment.TypeEasyPay, Name: "topup-test", Config: providerConfig,
		SupportedTypes: []string{"easypay_usdt"}, Enabled: true, PaymentMode: "qrcode",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = f.client.PaymentProviderInstance.DeleteOneID(instance.ID).Exec(ctx) })
	svc := service.NewPaymentService(f.client, payment.NewRegistry(), payment.NewDefaultLoadBalancer(f.client, key), nil, nil, config, NewUserRepository(f.client, integrationDB), nil, nil)
	svc.SetSubscriptionTopupService(f.service)
	req := service.CreateOrderRequest{
		UserID: f.user.ID, OrderType: payment.OrderTypeSubscriptionTopup,
		SKUID: skuID, Quantity: 2, Amount: 0.01, PaymentType: "easypay_usdt",
		ClientIP: "127.0.0.1", SrcHost: "app.example.test", SrcURL: "https://app.example.test/purchase", Locale: "zh-CN,zh;q=0.9,en-US;q=0.8,en;q=0.7",
	}

	created, err := svc.CreateOrder(ctx, req)
	require.Nil(t, created)
	require.Error(t, err)
	failed, err := f.client.PaymentOrder.Query().Where(paymentorder.UserID(f.user.ID)).Only(ctx)
	require.NoError(t, err)
	require.Equal(t, service.OrderStatusFailed, failed.Status)
	available, err := f.client.TopupCard.Query().Where(topupcard.SkuID(skuID), topupcard.Status("available"), topupcard.OrderIDIsNil()).Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, available, "provider failure releases the entire reservation")
	require.Equal(t, "25.18", (<-requests).Get("money"))

	rejectCreate.Store(false)
	created, err = svc.CreateOrder(ctx, req)
	require.NoError(t, err)
	require.Equal(t, 24.68, created.Amount, "server SKU price overrides client amount and recharge multiplier")
	require.Equal(t, 25.18, created.PayAmount, "CNY fee applies without subscription currency conversion")
	require.Zero(t, created.GiftAmount)
	gatewayRequest := <-requests
	require.Equal(t, "25.18", gatewayRequest.Get("money"))
	require.Equal(t, "usdt", gatewayRequest.Get("type"))
	reserved, err := f.client.TopupCard.Query().Where(topupcard.OrderID(created.OrderID), topupcard.Status("reserved")).Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, reserved)
	before, err := f.client.TopupMail.Query().Where(topupmail.OrderID(created.OrderID)).Count(ctx)
	require.NoError(t, err)
	require.Zero(t, before)

	gateway, err := provider.NewEasyPay(strconv.FormatInt(instance.ID, 10), providerConfig)
	require.NoError(t, err)
	callback := url.Values{
		"pid": {"1000"}, "trade_no": {"topup-paid"}, "out_trade_no": {created.OutTradeNo},
		"type": {"usdt"}, "name": {"subscription topup"}, "money": {"25.18"},
		"trade_status": {"TRADE_SUCCESS"}, "sign_type": {"MD5"},
	}
	signature := md5.Sum([]byte("money=25.18&name=subscription topup&out_trade_no=" + created.OutTradeNo + "&pid=1000&trade_no=topup-paid&trade_status=TRADE_SUCCESS&type=usdt" + providerConfig["pkey"]))
	callback.Set("sign", hex.EncodeToString(signature[:]))
	notification, err := gateway.VerifyNotification(ctx, callback.Encode(), nil)
	require.NoError(t, err)
	require.NoError(t, svc.ConfirmNotificationUpstream(ctx, gateway, notification))
	require.NoError(t, svc.HandlePaymentNotification(ctx, notification, gateway.ProviderKey()))
	require.NoError(t, svc.HandlePaymentNotification(ctx, notification, gateway.ProviderKey()))
	completed, err := f.client.PaymentOrder.Get(ctx, created.OrderID)
	require.NoError(t, err)
	require.Equal(t, service.OrderStatusCompleted, completed.Status)
	require.Equal(t, "delivered", completed.TopupDeliveryStatus)
	require.Equal(t, "zh", service.TopupOrderSnapshot(completed).Locale)
	issued, err := f.client.TopupCard.Query().Where(topupcard.OrderID(created.OrderID), topupcard.Status("issued")).Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, issued)
	mailCount, err := f.client.TopupMail.Query().Where(topupmail.OrderID(created.OrderID)).Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, mailCount, "repeated callbacks must not duplicate delivery mail")
	mail, err := f.client.TopupMail.Query().Where(topupmail.OrderID(created.OrderID)).Only(ctx)
	require.NoError(t, err)
	require.Equal(t, "zh", mail.Locale)
	user, err := f.client.User.Get(ctx, f.user.ID)
	require.NoError(t, err)
	require.Equal(t, 123.0, user.Balance)
	require.Equal(t, 45.0, user.GiftBalance)
	require.Zero(t, user.TotalRecharged)
}
