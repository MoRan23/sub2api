//go:build integration

package repository

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	dbmigrations "github.com/Wei-Shaw/sub2api/migrations"
)

func (s *UserRepoSuite) TestSubtractWallets_ExactIndependentDebits() {
	tests := []struct {
		name                             string
		ordinary, gift, debit, giftDebit float64
		wantOrdinary, wantGift           float64
		wantErr                          error
	}{
		{name: "ordinary only", ordinary: 10, gift: 4, debit: 2, wantOrdinary: 8, wantGift: 4},
		{name: "gift only", ordinary: 10, gift: 4, giftDebit: 3, wantOrdinary: 10, wantGift: 1},
		{name: "both", ordinary: 10, gift: 4, debit: 2, giftDebit: 3, wantOrdinary: 8, wantGift: 1},
		{name: "exact available amounts", ordinary: 10, gift: 4, debit: 10, giftDebit: 4},
		{name: "gift debit preserves ordinary debt", ordinary: -5, gift: 4, giftDebit: 3, wantOrdinary: -5, wantGift: 1},
		{name: "ordinary shortage preserves both", ordinary: 1, gift: 4, debit: 2, giftDebit: 3, wantOrdinary: 1, wantGift: 4, wantErr: service.ErrOrdinaryBalanceInsufficient},
		{name: "gift shortage preserves both", ordinary: 10, gift: 1, debit: 2, giftDebit: 3, wantOrdinary: 10, wantGift: 1, wantErr: service.ErrGiftBalanceInsufficient},
		{name: "ordinary shortage reported first", ordinary: 1, gift: 1, debit: 2, giftDebit: 3, wantOrdinary: 1, wantGift: 1, wantErr: service.ErrOrdinaryBalanceInsufficient},
		{name: "eight decimal places", ordinary: 0.12345678, gift: 0.98765432, debit: 0.00000001, giftDebit: 0.00000001, wantOrdinary: 0.12345677, wantGift: 0.98765431},
		{name: "round to wallet precision", ordinary: 1, gift: 1, debit: 0.123456789, giftDebit: 0.987654321, wantOrdinary: 0.87654321, wantGift: 0.01234568},
	}
	for i, tt := range tests {
		s.Run(tt.name, func() {
			user := s.mustCreateUser(&service.User{Email: fmt.Sprintf("admin-subtract-%d-%d@example.com", i, time.Now().UnixNano()), Balance: tt.ordinary, GiftBalance: tt.gift})
			_, err := s.client.ExecContext(s.ctx, `UPDATE users SET total_recharged=42, frozen_balance=7, frozen_gift_balance=8 WHERE id=$1`, user.ID)
			s.Require().NoError(err)
			change, err := s.repo.SubtractWallets(s.ctx, user.ID, tt.debit, tt.giftDebit)
			if tt.wantErr != nil {
				s.Require().ErrorIs(err, tt.wantErr)
			} else {
				s.Require().NoError(err)
			}
			s.InDelta(tt.ordinary, change.OldOrdinary, 1e-10)
			s.InDelta(tt.gift, change.OldGift, 1e-10)
			s.InDelta(tt.wantOrdinary, change.NewOrdinary, 1e-10)
			s.InDelta(tt.wantGift, change.NewGift, 1e-10)
			got, err := s.repo.GetByID(s.ctx, user.ID)
			s.Require().NoError(err)
			s.InDelta(tt.wantOrdinary, got.Balance, 1e-10)
			s.InDelta(tt.wantGift, got.GiftBalance, 1e-10)
			s.Equal(42.0, got.TotalRecharged)
			s.Equal(7.0, got.FrozenBalance)
			s.Equal(8.0, got.FrozenGiftBalance)
		})
	}
}

func (s *UserRepoSuite) TestSubtractWallets_RejectsInvalidAndMissingUsers() {
	user := s.mustCreateUser(&service.User{Balance: 5, GiftBalance: 4})
	for _, amounts := range [][2]float64{{0, 0}, {-1, 1}, {1, -1}, {math.NaN(), 1}, {1, math.NaN()}, {math.Inf(1), 1}, {1, math.Inf(-1)}, {1e-10, 1e-10}} {
		_, err := s.repo.SubtractWallets(s.ctx, user.ID, amounts[0], amounts[1])
		s.Require().Error(err)
	}
	got, err := s.repo.GetByID(s.ctx, user.ID)
	s.Require().NoError(err)
	s.Equal(5.0, got.Balance)
	s.Equal(4.0, got.GiftBalance)
	_, err = s.repo.SubtractWallets(s.ctx, -1, 1, 1)
	s.Require().ErrorIs(err, service.ErrUserNotFound)
	_, err = s.client.ExecContext(s.ctx, `UPDATE users SET deleted_at=NOW() WHERE id=$1`, user.ID)
	s.Require().NoError(err)
	_, err = s.repo.SubtractWallets(s.ctx, user.ID, 1, 1)
	s.Require().ErrorIs(err, service.ErrUserNotFound)
}

func (s *UserRepoSuite) TestSubtractWallets_ConcurrentCallsCannotPartiallyOverdraw() {
	user := s.mustCreateUser(&service.User{Balance: 10, GiftBalance: 5})
	const attempts = 20
	start := make(chan struct{})
	errCh := make(chan error, attempts)
	var wg sync.WaitGroup
	for range attempts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := s.repo.SubtractWallets(context.Background(), user.ID, 1, 1)
			errCh <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errCh)
	var success, insufficient int
	for err := range errCh {
		switch {
		case err == nil:
			success++
		case errors.Is(err, service.ErrGiftBalanceInsufficient):
			insufficient++
		default:
			s.Require().NoError(err)
		}
	}
	s.Equal(5, success)
	s.Equal(15, insufficient)
	got, err := s.repo.GetByID(s.ctx, user.ID)
	s.Require().NoError(err)
	s.Equal(5.0, got.Balance)
	s.Zero(got.GiftBalance)
}

func (s *UserRepoSuite) TestSubtractWallets_ConcurrentBillingIsNotOverwritten() {
	user := s.mustCreateUser(&service.User{Balance: 100, GiftBalance: 50})
	const pairs = 20
	errCh := make(chan error, pairs*2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range pairs {
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_, err := s.repo.SubtractWallets(context.Background(), user.ID, 1, 1)
			errCh <- err
		}()
		go func() {
			defer wg.Done()
			<-start
			errCh <- s.repo.DeductBalance(context.Background(), user.ID, 2)
		}()
	}
	close(start)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		s.Require().NoError(err)
	}
	got, err := s.repo.GetByID(s.ctx, user.ID)
	s.Require().NoError(err)
	s.Equal(40.0, got.Balance)
	s.Equal(30.0, got.GiftBalance)
}

func (s *UserRepoSuite) TestSubtractWalletsAndHistoryShareTransaction() {
	user := s.mustCreateUser(&service.User{Balance: 10, GiftBalance: 4})
	redeemRepo := NewRedeemCodeRepository(s.client)
	code := fmt.Sprintf("SUB-%d", time.Now().UnixNano())
	usedAt := time.Now()
	makeHistory := func() *service.RedeemCode {
		return &service.RedeemCode{Code: code, Type: service.AdjustmentTypeAdminBalance, Value: -2, GiftValue: -3, Status: service.StatusUsed, UsedBy: &user.ID, UsedAt: &usedAt, Notes: "administrator debit"}
	}
	tx, err := s.client.Tx(s.ctx)
	s.Require().NoError(err)
	txCtx := dbent.NewTxContext(s.ctx, tx)
	_, err = s.repo.SubtractWallets(txCtx, user.ID, 2, 3)
	s.Require().NoError(err)
	s.Require().NoError(redeemRepo.Create(txCtx, makeHistory()))
	s.Require().NoError(tx.Commit())
	s.T().Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM redeem_codes WHERE code=$1`, code)
	})

	// A duplicate history write fails after a second debit; rolling back the
	// transaction must restore both wallets and keep the first history intact.
	tx, err = s.client.Tx(s.ctx)
	s.Require().NoError(err)
	txCtx = dbent.NewTxContext(s.ctx, tx)
	_, err = s.repo.SubtractWallets(txCtx, user.ID, 1, 1)
	s.Require().NoError(err)
	s.Require().Error(redeemRepo.Create(txCtx, makeHistory()))
	s.Require().NoError(tx.Rollback())
	got, err := s.repo.GetByID(s.ctx, user.ID)
	s.Require().NoError(err)
	s.Equal(8.0, got.Balance)
	s.Equal(1.0, got.GiftBalance)
	var count int
	s.Require().NoError(integrationDB.QueryRowContext(s.ctx, `SELECT count(*) FROM redeem_codes WHERE code=$1 AND value=-2 AND gift_value=-3 AND notes='administrator debit'`, code).Scan(&count))
	s.Equal(1, count)
}

func (s *UserRepoSuite) TestAdminGiftDebitMigrationRetainsRedeemableCodeConstraint() {
	for _, kind := range []string{"balance", "subscription", "concurrency", "affiliate_balance"} {
		_, err := s.client.ExecContext(s.ctx, `INSERT INTO redeem_codes(code,type,value,gift_value) VALUES($1,$2,0,-1)`, fmt.Sprintf("NEG-%d", time.Now().UnixNano()), kind)
		s.Require().Error(err, kind)
		s.ErrorContains(err, "redeem_codes_gift_value_nonnegative")
	}
	code := fmt.Sprintf("ADM-%d", time.Now().UnixNano())
	_, err := s.client.ExecContext(s.ctx, `INSERT INTO redeem_codes(code,type,value,gift_value) VALUES($1,'admin_balance',0,-1)`, code)
	s.Require().NoError(err)
	s.T().Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM redeem_codes WHERE code=$1`, code)
	})
	migration, err := dbmigrations.FS.ReadFile("274_admin_gift_balance_debits.sql")
	s.Require().NoError(err)
	for range 2 {
		tx, err := s.client.Tx(s.ctx)
		s.Require().NoError(err)
		_, err = tx.Client().ExecContext(s.ctx, string(migration))
		s.Require().NoError(err)
		s.Require().NoError(tx.Commit())
	}
	var existingGift float64
	s.Require().NoError(integrationDB.QueryRowContext(s.ctx, `SELECT gift_value FROM redeem_codes WHERE code=$1`, code).Scan(&existingGift))
	s.Equal(-1.0, existingGift, "repeated migration must retain signed administrator history")
	_, err = s.client.ExecContext(s.ctx, `UPDATE redeem_codes SET type='balance' WHERE code=$1`, code)
	s.Require().Error(err)
	s.ErrorContains(err, "redeem_codes_gift_value_nonnegative")
	_, err = s.client.ExecContext(s.ctx, `UPDATE redeem_codes SET type='balance',gift_value=0 WHERE code=$1`, code)
	s.Require().NoError(err)
}
