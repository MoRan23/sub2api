//go:build unit

package service

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type adminSubtractBillingCache struct {
	BillingCache
	invalidations chan int64
}

func (s *adminSubtractBillingCache) InvalidateUserBalance(_ context.Context, id int64) error {
	s.invalidations <- id
	return nil
}

func TestAdminService_UpdateUserBalance_SubtractWallets(t *testing.T) {
	tests := []struct {
		name              string
		balance, gift     float64
		ordinary, deduct  float64
		wantOrd, wantGift float64
		wantErr           error
	}{
		{name: "ordinary only", balance: 10, gift: 4, ordinary: 3, wantOrd: 7, wantGift: 4},
		{name: "gift only", balance: 10, gift: 4, deduct: 2, wantOrd: 10, wantGift: 2},
		{name: "both wallets", balance: 10, gift: 4, ordinary: 3, deduct: 2, wantOrd: 7, wantGift: 2},
		{name: "entire wallet balances", balance: 10, gift: 4, ordinary: 10, deduct: 4},
		{name: "gift only preserves ordinary debt", balance: -2, gift: 4, deduct: 2, wantOrd: -2, wantGift: 2},
		{name: "eight decimal places", balance: 1, gift: 1, ordinary: 0.12345678, deduct: 0.00000001, wantOrd: 0.87654322, wantGift: 0.99999999},
		{name: "ordinary cannot borrow gift", balance: 2, gift: 10, ordinary: 3, deduct: 1, wantErr: ErrOrdinaryBalanceInsufficient},
		{name: "gift cannot borrow ordinary", balance: 10, gift: 2, ordinary: 1, deduct: 3, wantErr: ErrGiftBalanceInsufficient},
		{name: "both insufficient reports ordinary first", balance: 2, gift: 2, ordinary: 3, deduct: 3, wantErr: ErrOrdinaryBalanceInsufficient},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &giftBalanceUserRepoStub{balanceUserRepoStub: &balanceUserRepoStub{userRepoStub: &userRepoStub{user: &User{ID: 7, Balance: tt.balance, GiftBalance: tt.gift, TotalRecharged: 99}}}}
			history := &balanceRedeemRepoStub{redeemRepoStub: &redeemRepoStub{}}
			auth := &authCacheInvalidatorStub{}
			cache := &adminSubtractBillingCache{invalidations: make(chan int64, 1)}
			affiliate := &adminRechargeAffiliateAccruerStub{}
			svc := &adminServiceImpl{userRepo: repo, redeemCodeRepo: history, authCacheInvalidator: auth, billingCacheService: &BillingCacheService{cache: cache}, affiliateService: affiliate, settingService: adminRechargeSettingService(true)}
			user, err := svc.UpdateUserBalance(context.Background(), 7, tt.ordinary, tt.deduct, "subtract", "refund gift")
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				require.Empty(t, repo.changes)
				require.Equal(t, tt.balance, repo.user.Balance)
				require.Equal(t, tt.gift, repo.user.GiftBalance)
				require.Empty(t, history.created)
				require.Empty(t, auth.userIDs)
				require.Empty(t, cache.invalidations)
				return
			}
			require.NoError(t, err)
			require.InDelta(t, tt.wantOrd, user.Balance, 1e-10)
			require.InDelta(t, tt.wantGift, user.GiftBalance, 1e-10)
			require.Equal(t, 99.0, user.TotalRecharged)
			require.Len(t, history.created, 1)
			require.InDelta(t, -tt.ordinary, history.created[0].Value, 1e-10)
			require.InDelta(t, -tt.deduct, history.created[0].GiftValue, 1e-10)
			require.Equal(t, AdjustmentTypeAdminBalance, history.created[0].Type)
			require.Equal(t, "refund gift", history.created[0].Notes)
			require.Equal(t, []int64{7}, auth.userIDs)
			require.Empty(t, affiliate.calls)
			select {
			case id := <-cache.invalidations:
				require.Equal(t, int64(7), id)
			case <-time.After(time.Second):
				t.Fatal("billing balance cache was not invalidated")
			}
		})
	}
}

func TestAdminService_UpdateUserBalance_SubtractValidationAndPrecision(t *testing.T) {
	for _, amounts := range [][2]float64{{0, 0}, {-1, 1}, {1, -1}, {math.NaN(), 1}, {1, math.NaN()}, {math.Inf(1), 1}, {1, math.Inf(-1)}, {0.000000001, 0.000000001}} {
		repo := &giftBalanceUserRepoStub{balanceUserRepoStub: &balanceUserRepoStub{userRepoStub: &userRepoStub{user: &User{ID: 7, Balance: 10, GiftBalance: 10}}}}
		svc := &adminServiceImpl{userRepo: repo}
		_, err := svc.UpdateUserBalance(context.Background(), 7, amounts[0], amounts[1], "subtract", "")
		require.Error(t, err)
		require.Empty(t, repo.changes)
	}
	repo := &giftBalanceUserRepoStub{balanceUserRepoStub: &balanceUserRepoStub{userRepoStub: &userRepoStub{user: &User{ID: 7, Balance: 1, GiftBalance: 1}}}}
	history := &balanceRedeemRepoStub{redeemRepoStub: &redeemRepoStub{}}
	svc := &adminServiceImpl{userRepo: repo, redeemCodeRepo: history}
	_, err := svc.UpdateUserBalance(context.Background(), 7, 0.123456785, 0.000000005, "subtract", "round to wallet precision")
	require.NoError(t, err)
	require.Equal(t, -0.12345679, history.created[0].Value)
	require.Equal(t, -0.00000001, history.created[0].GiftValue)
}

func TestAdminService_UpdateUserBalance_GiftHistoryFailureSkipsSideEffects(t *testing.T) {
	repo := &giftBalanceUserRepoStub{balanceUserRepoStub: &balanceUserRepoStub{userRepoStub: &userRepoStub{user: &User{ID: 7, Balance: 10, GiftBalance: 10}}}}
	auth := &authCacheInvalidatorStub{}
	cache := &adminSubtractBillingCache{invalidations: make(chan int64, 1)}
	svc := &adminServiceImpl{userRepo: repo, redeemCodeRepo: &balanceRedeemRepoStub{createErr: errors.New("history unavailable")}, authCacheInvalidator: auth, billingCacheService: &BillingCacheService{cache: cache}}
	_, err := svc.UpdateUserBalance(context.Background(), 7, 0, 1, "subtract", "")
	require.ErrorContains(t, err, "create balance adjustment history")
	require.Empty(t, auth.userIDs)
	require.Empty(t, cache.invalidations)
}
