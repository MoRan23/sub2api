//go:build integration

package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

type adminSubtractAuthCacheProbe struct {
	service.APIKeyAuthCacheInvalidator
	userIDs []int64
}

func (p *adminSubtractAuthCacheProbe) InvalidateAuthCacheByUserID(_ context.Context, id int64) {
	p.userIDs = append(p.userIDs, id)
}

type failedAdminSubtractHistory struct {
	service.RedeemCodeRepository
}

func (failedAdminSubtractHistory) Create(context.Context, *service.RedeemCode) error {
	return errors.New("injected admin history failure")
}

func (s *UserRepoSuite) adminSubtractService(history service.RedeemCodeRepository, invalidator service.APIKeyAuthCacheInvalidator) service.AdminService {
	return service.NewAdminService(
		nil, s.repo, nil, nil, nil, nil,
		history, nil, nil, nil, nil, nil,
		invalidator, s.client, nil, nil, nil, nil,
		nil, nil, nil, nil,
	)
}

func (s *UserRepoSuite) TestAdminSubtractServiceRollsBackBothWalletsWhenHistoryFails() {
	user := s.mustCreateUser(&service.User{Balance: 10, GiftBalance: 4})
	history := NewRedeemCodeRepository(s.client)
	invalidator := &adminSubtractAuthCacheProbe{}
	svc := s.adminSubtractService(failedAdminSubtractHistory{RedeemCodeRepository: history}, invalidator)

	_, err := svc.UpdateUserBalance(s.ctx, user.ID, 3, 2, "subtract", "must roll back")
	s.Require().ErrorContains(err, "injected admin history failure")
	stored, err := s.repo.GetByID(s.ctx, user.ID)
	s.Require().NoError(err)
	s.Equal(10.0, stored.Balance)
	s.Equal(4.0, stored.GiftBalance)
	entries, err := history.ListByUser(s.ctx, user.ID, 10)
	s.Require().NoError(err)
	s.Empty(entries)
	s.Empty(invalidator.userIDs, "failed transaction cannot invalidate post-commit caches")
}

func (s *UserRepoSuite) TestAdminSubtractServicePersistsGiftOnlyHistoryOnceAcrossIdempotencyRestart() {
	user := s.mustCreateUser(&service.User{Balance: -2, GiftBalance: 4})
	_, err := s.client.ExecContext(s.ctx, "UPDATE users SET total_recharged = 17 WHERE id = $1", user.ID)
	s.Require().NoError(err)
	history := NewRedeemCodeRepository(s.client)
	invalidator := &adminSubtractAuthCacheProbe{}
	svc := s.adminSubtractService(history, invalidator)
	opts := service.IdempotencyExecuteOptions{
		Scope: fmt.Sprintf("admin-gift-refund-%d", user.ID), ActorScope: "admin:test",
		Method: "POST", Route: "/api/v1/admin/users/:id/balance",
		IdempotencyKey: "same-gift-refund", RequireKey: true, TTL: time.Hour,
		Payload: map[string]any{"user_id": user.ID, "operation": "subtract", "balance": 0, "gift_balance": 1.23456789},
	}
	s.T().Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM idempotency_records WHERE scope = $1", opts.Scope)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM redeem_codes WHERE used_by = $1", user.ID)
	})
	cfg := service.DefaultIdempotencyConfig()
	cfg.ObserveOnly = false
	var executions int
	apply := func(ctx context.Context) (any, error) {
		executions++
		updated, err := svc.UpdateUserBalance(ctx, user.ID, 0, 1.23456789, "subtract", "gift refund integration")
		if err != nil {
			return nil, err
		}
		return map[string]any{"id": updated.ID}, nil
	}
	first := service.NewIdempotencyCoordinator(NewIdempotencyRepository(s.client, integrationDB), cfg)
	result, err := first.Execute(s.ctx, opts, apply)
	s.Require().NoError(err)
	s.False(result.Replayed)
	// Recreate the coordinator and its repository to exercise persisted replay,
	// rather than a single in-memory coordinator instance.
	second := service.NewIdempotencyCoordinator(NewIdempotencyRepository(s.client, integrationDB), cfg)
	result, err = second.Execute(s.ctx, opts, apply)
	s.Require().NoError(err)
	s.True(result.Replayed)
	s.Equal(1, executions)

	stored, err := s.repo.GetByID(s.ctx, user.ID)
	s.Require().NoError(err)
	s.Equal(-2.0, stored.Balance, "gift-only deduction leaves existing ordinary debt unchanged")
	s.Equal(2.76543211, stored.GiftBalance)
	s.Equal(17.0, stored.TotalRecharged)
	entries, err := history.ListByUser(s.ctx, user.ID, 10)
	s.Require().NoError(err)
	s.Require().Len(entries, 1)
	s.Equal(service.AdjustmentTypeAdminBalance, entries[0].Type)
	s.Zero(entries[0].Value)
	s.Equal(-1.23456789, entries[0].GiftValue)
	s.Equal("gift refund integration", entries[0].Notes)
	s.Equal([]int64{user.ID}, invalidator.userIDs)

	opts.Payload = map[string]any{"user_id": user.ID, "operation": "subtract", "balance": 0, "gift_balance": 2}
	_, err = second.Execute(s.ctx, opts, apply)
	s.Require().ErrorIs(err, service.ErrIdempotencyKeyConflict)
	s.Equal(1, executions, "a reused key with changed gift amount cannot deduct again")
}
