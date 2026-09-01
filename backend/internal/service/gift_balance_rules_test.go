package service

import (
	"context"
	"math"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

type giftRedeemCaptureRepo struct {
	redeemRejectRepo
	created []RedeemCode
}

func (r *giftRedeemCaptureRepo) Create(_ context.Context, code *RedeemCode) error {
	if code != nil {
		r.created = append(r.created, *code)
	}
	return nil
}

func (r *giftRedeemCaptureRepo) CreateBatch(_ context.Context, codes []RedeemCode) error {
	r.created = append(r.created, codes...)
	return nil
}

func TestRedeemServiceGenerateCodesSnapshotsGiftRatioAndValue(t *testing.T) {
	repo := &giftRedeemCaptureRepo{}
	svc := NewRedeemService(repo, nil, nil, nil, nil, nil, nil, nil)

	codes, err := svc.GenerateCodes(context.Background(), GenerateCodesRequest{
		Count:     2,
		Type:      RedeemTypeBalance,
		Value:     80,
		GiftRatio: 12.3456,
	})
	require.NoError(t, err)
	require.Len(t, codes, 2)
	require.Equal(t, codes, repo.created)
	for _, code := range codes {
		require.Equal(t, RedeemTypeBalance, code.Type)
		require.InDelta(t, 80, code.Value, 1e-12)
		require.InDelta(t, 12.3456, code.GiftRatio, 1e-12)
		require.InDelta(t, 9.87648, code.GiftValue, 1e-12)
	}
}

func TestValidateRedeemGift(t *testing.T) {
	tests := []struct {
		name       string
		codeType   string
		value      float64
		ratio      float64
		wantReason string
	}{
		{name: "zero ratio for non-balance", codeType: RedeemTypeConcurrency, value: 1},
		{name: "maximum valid", codeType: RedeemTypeBalance, value: 1, ratio: 100},
		{name: "four decimal places valid", codeType: RedeemTypeBalance, value: 1, ratio: 12.3456},
		{name: "too many decimal places", codeType: RedeemTypeBalance, value: 1, ratio: 12.34567, wantReason: "INVALID_GIFT_RATIO"},
		{name: "sub float epsilon past integer", codeType: RedeemTypeBalance, value: 1, ratio: 1.0000000000009, wantReason: "INVALID_GIFT_RATIO"},
		{name: "sub float epsilon past fourth decimal", codeType: RedeemTypeBalance, value: 1, ratio: 0.0001000000005, wantReason: "INVALID_GIFT_RATIO"},
		{name: "negative", codeType: RedeemTypeBalance, value: 1, ratio: -0.1, wantReason: "INVALID_GIFT_RATIO"},
		{name: "above maximum", codeType: RedeemTypeBalance, value: 1, ratio: 100.0001, wantReason: "INVALID_GIFT_RATIO"},
		{name: "nan", codeType: RedeemTypeBalance, value: 1, ratio: math.NaN(), wantReason: "INVALID_GIFT_RATIO"},
		{name: "infinite", codeType: RedeemTypeBalance, value: 1, ratio: math.Inf(1), wantReason: "INVALID_GIFT_RATIO"},
		{name: "negative balance code", codeType: RedeemTypeBalance, value: -1, ratio: 10, wantReason: "GIFT_RATIO_NOT_ALLOWED"},
		{name: "concurrency code", codeType: RedeemTypeConcurrency, value: 1, ratio: 10, wantReason: "GIFT_RATIO_NOT_ALLOWED"},
		{name: "subscription code", codeType: RedeemTypeSubscription, value: 1, ratio: 10, wantReason: "GIFT_RATIO_NOT_ALLOWED"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateRedeemGift(tt.codeType, tt.value, tt.ratio)
			if tt.wantReason == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			require.True(t, infraerrors.IsBadRequest(err))
			require.Equal(t, tt.wantReason, infraerrors.Reason(err))
		})
	}
}

func TestPaymentConfigGiftRatioDefaultsPersistsAndValidates(t *testing.T) {
	t.Run("parse default and stored value", func(t *testing.T) {
		svc := &PaymentConfigService{}
		require.Zero(t, svc.parsePaymentConfig(map[string]string{}).BalanceGiftRatio)
		require.InDelta(t, 12.3456, svc.parsePaymentConfig(map[string]string{SettingBalanceGiftRatio: "12.3456"}).BalanceGiftRatio, 1e-12)
		require.Zero(t, svc.parsePaymentConfig(map[string]string{SettingBalanceGiftRatio: "12.34567"}).BalanceGiftRatio)
		require.Zero(t, svc.parsePaymentConfig(map[string]string{SettingBalanceGiftRatio: "101"}).BalanceGiftRatio)
	})

	t.Run("valid update preserves four decimals", func(t *testing.T) {
		repo := &paymentConfigSettingRepoStub{values: map[string]string{}}
		svc := &PaymentConfigService{settingRepo: repo}
		ratio := 12.3456
		require.NoError(t, svc.UpdatePaymentConfig(context.Background(), UpdatePaymentConfigRequest{BalanceGiftRatio: &ratio}))
		require.Equal(t, "12.3456", repo.updates[SettingBalanceGiftRatio])
	})

	for _, ratio := range []float64{-0.1, 100.0001, 1.23456, 1.0000000000009, 0.0001000000005, math.NaN(), math.Inf(1)} {
		t.Run("invalid update", func(t *testing.T) {
			repo := &paymentConfigSettingRepoStub{values: map[string]string{}}
			svc := &PaymentConfigService{settingRepo: repo}
			err := svc.UpdatePaymentConfig(context.Background(), UpdatePaymentConfigRequest{BalanceGiftRatio: &ratio})
			require.Error(t, err)
			require.Equal(t, "INVALID_BALANCE_GIFT_RATIO", infraerrors.Reason(err))
			require.Empty(t, repo.updates)
		})
	}
}

func TestPaymentOrderSnapshotsGiftRatioAndAmount(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	entity, err := client.User.Create().
		SetEmail("gift-order-snapshot@example.com").
		SetPasswordHash("hash").
		SetUsername("gift-order-snapshot").
		Save(ctx)
	require.NoError(t, err)
	user := &User{ID: entity.ID, Email: entity.Email, Username: entity.Username}
	cfg := &PaymentConfig{MaxPendingOrders: 3, OrderTimeoutMin: 30, BalanceGiftRatio: 12.3456}

	order, err := (&PaymentService{entClient: client}).createOrderInTx(
		ctx,
		CreateOrderRequest{
			UserID: entity.ID, PaymentType: payment.TypeAlipay, OrderType: payment.OrderTypeBalance,
			ClientIP: "127.0.0.1", SrcHost: "app.example.com",
		},
		user,
		nil,
		cfg,
		80,
		80,
		0,
		80,
		nil,
	)
	require.NoError(t, err)
	require.InDelta(t, 12.3456, order.GiftRatio, 1e-12)
	require.InDelta(t, 9.87648, order.GiftAmount, 1e-12)

	// Later configuration changes must not rewrite the already-created order.
	cfg.BalanceGiftRatio = 50
	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.InDelta(t, 12.3456, reloaded.GiftRatio, 1e-12)
	require.InDelta(t, 9.87648, reloaded.GiftAmount, 1e-12)
}

func TestUserTotalBalancesCombineOrdinaryAndGiftWallets(t *testing.T) {
	user := &User{Balance: -2.5, GiftBalance: 8, FrozenBalance: 1.25, FrozenGiftBalance: 0.75}
	require.InDelta(t, 5.5, user.TotalBalance(), 1e-12)
	require.InDelta(t, 2, user.TotalFrozenBalance(), 1e-12)

	var nilUser *User
	require.Zero(t, nilUser.TotalBalance())
	require.Zero(t, nilUser.TotalFrozenBalance())
}
