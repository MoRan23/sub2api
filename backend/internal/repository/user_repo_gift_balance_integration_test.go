//go:build integration

package repository

import (
	"context"
	"fmt"
	"sync"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func (s *UserRepoSuite) TestDeductBalance_UsesOrdinaryThenGiftThenOrdinaryDebt() {
	tests := []struct {
		name          string
		balance       float64
		giftBalance   float64
		amount        float64
		wantBalance   float64
		wantGift      float64
		wantTotalDiff float64
	}{
		{name: "ordinary wallet covers cost", balance: 10, giftBalance: 5, amount: 4, wantBalance: 6, wantGift: 5, wantTotalDiff: 4},
		{name: "cost crosses into gift wallet", balance: 3, giftBalance: 5, amount: 6, wantBalance: 0, wantGift: 2, wantTotalDiff: 6},
		{name: "both wallets exhausted", balance: 3, giftBalance: 2, amount: 8, wantBalance: -3, wantGift: 0, wantTotalDiff: 8},
		{name: "existing ordinary debt skips to gift", balance: -2, giftBalance: 4, amount: 6, wantBalance: -4, wantGift: 0, wantTotalDiff: 6},
	}

	for i, tt := range tests {
		s.Run(tt.name, func() {
			user := s.mustCreateUser(&service.User{
				Email:       fmt.Sprintf("gift-deduct-%d-%d@example.com", i, time.Now().UnixNano()),
				Balance:     tt.balance,
				GiftBalance: tt.giftBalance,
			})
			beforeTotal := tt.balance + tt.giftBalance

			s.Require().NoError(s.repo.DeductBalance(s.ctx, user.ID, tt.amount))
			got, err := s.repo.GetByID(s.ctx, user.ID)
			s.Require().NoError(err)
			s.InDelta(tt.wantBalance, got.Balance, 1e-8)
			s.InDelta(tt.wantGift, got.GiftBalance, 1e-8)
			s.GreaterOrEqual(got.GiftBalance, 0.0)
			s.InDelta(tt.wantTotalDiff, beforeTotal-got.TotalBalance(), 1e-8,
				"combined wallet delta must equal the billed amount")
		})
	}
}

func (s *UserRepoSuite) TestDeductBalance_ConcurrentCallsHaveOneSerializedWalletResult() {
	user := s.mustCreateUser(&service.User{
		Email:       fmt.Sprintf("gift-deduct-concurrent-%d@example.com", time.Now().UnixNano()),
		Balance:     10,
		GiftBalance: 5,
	})

	const deductions = 24
	errCh := make(chan error, deductions)
	var wg sync.WaitGroup
	for i := 0; i < deductions; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errCh <- s.repo.DeductBalance(context.Background(), user.ID, 1)
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		s.Require().NoError(err)
	}

	got, err := s.repo.GetByID(s.ctx, user.ID)
	s.Require().NoError(err)
	s.InDelta(-9, got.Balance, 1e-8)
	s.Zero(got.GiftBalance)
	s.InDelta(-9, got.TotalBalance(), 1e-8)
}

func (s *UserRepoSuite) TestCreditRedeemWallet_CountsOnlyOrdinaryAsRecharged() {
	user := s.mustCreateUser(&service.User{
		Email:       fmt.Sprintf("gift-credit-redeem-%d@example.com", time.Now().UnixNano()),
		Balance:     1,
		GiftBalance: 2,
	})
	_, err := s.client.ExecContext(s.ctx, "UPDATE users SET total_recharged = $1 WHERE id = $2", 7, user.ID)
	s.Require().NoError(err)

	s.Require().NoError(s.repo.CreditRedeemWallet(s.ctx, user.ID, 10, 3))
	got, err := s.repo.GetByID(s.ctx, user.ID)
	s.Require().NoError(err)
	s.InDelta(11, got.Balance, 1e-8)
	s.InDelta(5, got.GiftBalance, 1e-8)
	s.InDelta(17, got.TotalRecharged, 1e-8,
		"gift amount must not increase the historical ordinary recharge base")
}

func (s *UserRepoSuite) TestAdjustWallets_GiftOnlyAddPreservesOrdinaryDebt() {
	user := s.mustCreateUser(&service.User{
		Email:       fmt.Sprintf("gift-admin-adjust-%d@example.com", time.Now().UnixNano()),
		Balance:     -2,
		GiftBalance: 3,
	})

	change, err := s.repo.AdjustWallets(s.ctx, user.ID, 0, 4)
	s.Require().NoError(err)
	s.InDelta(-2, change.OldOrdinary, 1e-8)
	s.InDelta(-2, change.NewOrdinary, 1e-8)
	s.InDelta(3, change.OldGift, 1e-8)
	s.InDelta(7, change.NewGift, 1e-8)

	got, err := s.repo.GetByID(s.ctx, user.ID)
	s.Require().NoError(err)
	s.InDelta(-2, got.Balance, 1e-8)
	s.InDelta(7, got.GiftBalance, 1e-8)
	s.Zero(got.TotalRecharged)
}

func (s *UserRepoSuite) TestAdjustWallets_CountsOnlyOrdinaryAsRecharged() {
	user := s.mustCreateUser(&service.User{
		Email:       fmt.Sprintf("gift-admin-recharged-%d@example.com", time.Now().UnixNano()),
		Balance:     1,
		GiftBalance: 2,
	})
	_, err := s.client.ExecContext(s.ctx, "UPDATE users SET total_recharged = $1 WHERE id = $2", 7, user.ID)
	s.Require().NoError(err)

	_, err = s.repo.AdjustWallets(s.ctx, user.ID, 5, 4)
	s.Require().NoError(err)
	got, err := s.repo.GetByID(s.ctx, user.ID)
	s.Require().NoError(err)
	s.InDelta(6, got.Balance, 1e-8)
	s.InDelta(6, got.GiftBalance, 1e-8)
	s.InDelta(12, got.TotalRecharged, 1e-8)
}

func (s *UserRepoSuite) TestAdjustWalletsAndRedeemHistoryShareExternalTransaction() {
	user := s.mustCreateUser(&service.User{
		Email:       fmt.Sprintf("gift-admin-tx-%d@example.com", time.Now().UnixNano()),
		Balance:     2,
		GiftBalance: 3,
	})
	redeemRepo := NewRedeemCodeRepository(s.client)
	conflicting := &service.RedeemCode{
		Code:   fmt.Sprintf("ADMIN-TX-%d", time.Now().UnixNano()),
		Type:   service.AdjustmentTypeAdminBalance,
		Status: service.StatusUsed,
	}
	s.Require().NoError(redeemRepo.Create(s.ctx, conflicting))
	s.T().Cleanup(func() { _ = redeemRepo.Delete(context.Background(), conflicting.ID) })

	tx, err := s.client.Tx(s.ctx)
	s.Require().NoError(err)
	txCtx := dbent.NewTxContext(s.ctx, tx)
	_, err = s.repo.AdjustWallets(txCtx, user.ID, 5, 7)
	s.Require().NoError(err)
	err = redeemRepo.Create(txCtx, &service.RedeemCode{
		Code:   conflicting.Code,
		Type:   service.AdjustmentTypeAdminBalance,
		Status: service.StatusUsed,
	})
	s.Require().Error(err)
	s.Require().NoError(tx.Rollback())

	got, err := s.repo.GetByID(s.ctx, user.ID)
	s.Require().NoError(err)
	s.InDelta(2, got.Balance, 1e-8)
	s.InDelta(3, got.GiftBalance, 1e-8)
}

func (s *UserRepoSuite) TestDeductAvailableWallets_ClampsWalletsIndependently() {
	tests := []struct {
		name         string
		balance      float64
		giftBalance  float64
		ordinary     float64
		gift         float64
		wantDeducted service.WalletAmounts
		wantBalance  float64
		wantGift     float64
	}{
		{
			name: "ordinary debt is preserved while gift is reclaimed", balance: -2, giftBalance: 3,
			ordinary: 5, gift: 5, wantDeducted: service.WalletAmounts{Gift: 3}, wantBalance: -2, wantGift: 0,
		},
		{
			name: "ordinary and gift clamp separately", balance: 4, giftBalance: 3,
			ordinary: 10, gift: 2, wantDeducted: service.WalletAmounts{Ordinary: 4, Gift: 2}, wantBalance: 0, wantGift: 1,
		},
	}

	for i, tt := range tests {
		s.Run(tt.name, func() {
			user := s.mustCreateUser(&service.User{
				Email:       fmt.Sprintf("gift-refund-deduct-%d-%d@example.com", i, time.Now().UnixNano()),
				Balance:     tt.balance,
				GiftBalance: tt.giftBalance,
			})

			deducted, err := s.repo.DeductAvailableWallets(s.ctx, user.ID, tt.ordinary, tt.gift)
			s.Require().NoError(err)
			s.InDelta(tt.wantDeducted.Ordinary, deducted.Ordinary, 1e-8)
			s.InDelta(tt.wantDeducted.Gift, deducted.Gift, 1e-8)
			got, err := s.repo.GetByID(s.ctx, user.ID)
			s.Require().NoError(err)
			s.InDelta(tt.wantBalance, got.Balance, 1e-8)
			s.InDelta(tt.wantGift, got.GiftBalance, 1e-8)
			s.GreaterOrEqual(got.GiftBalance, 0.0)
		})
	}
}
