package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type usageBillingRepository struct {
	db *sql.DB
}

const (
	batchImageHoldTerminalCaptured = "captured"
	batchImageHoldTerminalReleased = "released"
)

type batchImageHoldReceipt struct {
	ordinaryHold float64
	giftHold     float64
	terminalKind string
	archived     bool
}

func NewUsageBillingRepository(_ *dbent.Client, sqlDB *sql.DB) service.UsageBillingRepository {
	return &usageBillingRepository{db: sqlDB}
}

func (r *usageBillingRepository) Apply(ctx context.Context, cmd *service.UsageBillingCommand) (_ *service.UsageBillingApplyResult, err error) {
	if cmd == nil {
		return &service.UsageBillingApplyResult{}, nil
	}
	if r == nil || r.db == nil {
		return nil, errors.New("usage billing repository db is nil")
	}

	cmd.Normalize()
	if cmd.RequestID == "" {
		return nil, service.ErrUsageBillingRequestIDRequired
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	applied, err := r.claimUsageBillingKey(ctx, tx, cmd)
	if err != nil {
		return nil, err
	}
	if !applied {
		return &service.UsageBillingApplyResult{Applied: false}, nil
	}

	result := &service.UsageBillingApplyResult{Applied: true}
	if err := r.applyUsageBillingEffects(ctx, tx, cmd, result); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	tx = nil
	return result, nil
}

func (r *usageBillingRepository) claimUsageBillingKey(ctx context.Context, tx *sql.Tx, cmd *service.UsageBillingCommand) (bool, error) {
	return r.claimUsageBillingRequest(ctx, tx, cmd.RequestID, cmd.APIKeyID, cmd.RequestFingerprint)
}

func (r *usageBillingRepository) claimUsageBillingRequest(ctx context.Context, tx *sql.Tx, requestID string, apiKeyID int64, requestFingerprint string) (bool, error) {
	var id int64
	err := tx.QueryRowContext(ctx, `
		INSERT INTO usage_billing_dedup (request_id, api_key_id, request_fingerprint)
		VALUES ($1, $2, $3)
		ON CONFLICT (request_id, api_key_id) DO NOTHING
		RETURNING id
	`, requestID, apiKeyID, requestFingerprint).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		var existingFingerprint string
		if err := tx.QueryRowContext(ctx, `
			SELECT request_fingerprint
			FROM usage_billing_dedup
			WHERE request_id = $1 AND api_key_id = $2
		`, requestID, apiKeyID).Scan(&existingFingerprint); err != nil {
			return false, err
		}
		if strings.TrimSpace(existingFingerprint) != strings.TrimSpace(requestFingerprint) {
			return false, service.ErrUsageBillingRequestConflict
		}
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var archivedFingerprint string
	err = tx.QueryRowContext(ctx, `
		SELECT request_fingerprint
		FROM usage_billing_dedup_archive
		WHERE request_id = $1 AND api_key_id = $2
	`, requestID, apiKeyID).Scan(&archivedFingerprint)
	if err == nil {
		if strings.TrimSpace(archivedFingerprint) != strings.TrimSpace(requestFingerprint) {
			return false, service.ErrUsageBillingRequestConflict
		}
		return false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	return true, nil
}

func (r *usageBillingRepository) ReserveBatchImageBalance(ctx context.Context, cmd *service.BatchImageBalanceHoldCommand) (*service.BatchImageBalanceHoldResult, error) {
	return r.applyBatchImageBalanceHold(ctx, cmd, reserveUsageBillingBatchImageBalance)
}

func (r *usageBillingRepository) CaptureBatchImageBalance(ctx context.Context, cmd *service.BatchImageBalanceHoldCommand) (*service.BatchImageBalanceHoldResult, error) {
	return r.applyBatchImageBalanceHold(ctx, cmd, captureUsageBillingBatchImageBalance)
}

func (r *usageBillingRepository) ReleaseBatchImageBalance(ctx context.Context, cmd *service.BatchImageBalanceHoldCommand) (*service.BatchImageBalanceHoldResult, error) {
	return r.applyBatchImageBalanceHold(ctx, cmd, releaseUsageBillingBatchImageBalance)
}

func (r *usageBillingRepository) applyBatchImageBalanceHold(
	ctx context.Context,
	cmd *service.BatchImageBalanceHoldCommand,
	apply func(context.Context, *sql.Tx, *service.BatchImageBalanceHoldCommand) (*service.BatchImageBalanceHoldResult, error),
) (_ *service.BatchImageBalanceHoldResult, err error) {
	if cmd == nil {
		return &service.BatchImageBalanceHoldResult{}, nil
	}
	if r == nil || r.db == nil {
		return nil, errors.New("usage billing repository db is nil")
	}
	cmd.Normalize()
	if cmd.RequestID == "" {
		return nil, service.ErrUsageBillingRequestIDRequired
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	applied, err := r.claimUsageBillingRequest(ctx, tx, cmd.RequestID, cmd.APIKeyID, cmd.RequestFingerprint)
	if err != nil {
		return nil, err
	}
	if !applied {
		return &service.BatchImageBalanceHoldResult{Applied: false}, nil
	}

	result, err := apply(ctx, tx, cmd)
	if err != nil {
		return nil, err
	}
	if result == nil {
		result = &service.BatchImageBalanceHoldResult{}
	}
	result.Applied = true

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	tx = nil
	return result, nil
}

func (r *usageBillingRepository) applyUsageBillingEffects(ctx context.Context, tx *sql.Tx, cmd *service.UsageBillingCommand, result *service.UsageBillingApplyResult) error {
	if cmd.SubscriptionCost > 0 && cmd.SubscriptionID != nil {
		if err := incrementUsageBillingSubscription(ctx, tx, *cmd.SubscriptionID, cmd.SubscriptionCost); err != nil {
			return err
		}
	}

	if cmd.BalanceCost > 0 {
		newBalance, sufficient, err := deductUsageBillingBalance(ctx, tx, cmd.UserID, cmd.BalanceCost)
		if err != nil {
			return err
		}
		result.NewBalance = &newBalance
		result.BalanceOverdrafted = !sufficient
	}

	if cmd.APIKeyQuotaCost > 0 {
		exhausted, err := incrementUsageBillingAPIKeyQuota(ctx, tx, cmd.APIKeyID, cmd.APIKeyQuotaCost)
		if err != nil {
			return err
		}
		result.APIKeyQuotaExhausted = exhausted
	}

	if cmd.APIKeyRateLimitCost > 0 {
		if err := incrementUsageBillingAPIKeyRateLimit(ctx, tx, cmd.APIKeyID, cmd.APIKeyRateLimitCost); err != nil {
			return err
		}
	}

	if cmd.AccountQuotaCost > 0 && (strings.EqualFold(cmd.AccountType, service.AccountTypeAPIKey) || strings.EqualFold(cmd.AccountType, service.AccountTypeBedrock)) {
		quotaState, err := incrementUsageBillingAccountQuota(ctx, tx, cmd.AccountID, cmd.AccountQuotaCost)
		if err != nil {
			return err
		}
		result.QuotaState = quotaState
	}

	return nil
}

func incrementUsageBillingSubscription(ctx context.Context, tx *sql.Tx, subscriptionID int64, costUSD float64) error {
	return incrementSubscriptionUsage(ctx, tx, subscriptionID, costUSD)
}

func deductUsageBillingBalance(ctx context.Context, tx *sql.Tx, userID int64, amount float64) (float64, bool, error) {
	var newBalance float64
	var sufficient bool
	err := tx.QueryRowContext(ctx, `
		WITH target AS (
			SELECT id, balance, gift_balance
			FROM users
			WHERE id = $2 AND deleted_at IS NULL
			FOR UPDATE
		), allocation AS (
			SELECT id, balance, gift_balance,
				LEAST(GREATEST(balance, 0), $1) AS ordinary_debit
			FROM target
		), allocated AS (
			SELECT id, balance, gift_balance, ordinary_debit,
				LEAST(gift_balance, GREATEST($1 - ordinary_debit, 0)) AS gift_debit
			FROM allocation
		), updated AS (
			UPDATE users AS u
			SET balance = a.balance - a.ordinary_debit
					- GREATEST($1 - a.ordinary_debit - a.gift_debit, 0),
				gift_balance = a.gift_balance - a.gift_debit,
				updated_at = NOW()
			FROM allocated AS a
			WHERE u.id = a.id AND u.deleted_at IS NULL
			RETURNING u.balance + u.gift_balance AS total_balance,
				a.balance + a.gift_balance >= $1 AS sufficient
		)
		SELECT total_balance, sufficient FROM updated
	`, amount, userID).Scan(&newBalance, &sufficient)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, service.ErrUserNotFound
	}
	if err != nil {
		return 0, false, err
	}
	return newBalance, sufficient, nil
}

func reserveUsageBillingBatchImageBalance(ctx context.Context, tx *sql.Tx, cmd *service.BatchImageBalanceHoldCommand) (*service.BatchImageBalanceHoldResult, error) {
	if cmd.HoldAmount <= 0 {
		return &service.BatchImageBalanceHoldResult{}, nil
	}
	var balance, frozen, ordinaryHold, giftHold float64
	err := tx.QueryRowContext(ctx, `
		WITH target AS (
			SELECT id, balance, gift_balance, frozen_balance, frozen_gift_balance
			FROM users
			WHERE id = $2 AND deleted_at IS NULL
			FOR UPDATE
		), allocation AS (
			SELECT *, LEAST(GREATEST(balance, 0), $1) AS ordinary_hold
			FROM target
		), updated AS (
			UPDATE users AS u
			SET balance = a.balance - a.ordinary_hold,
				gift_balance = a.gift_balance - ($1 - a.ordinary_hold),
				frozen_balance = a.frozen_balance + a.ordinary_hold,
				frozen_gift_balance = a.frozen_gift_balance + ($1 - a.ordinary_hold),
				updated_at = NOW()
			FROM allocation AS a
			WHERE u.id = a.id AND u.deleted_at IS NULL
				AND a.balance + a.gift_balance >= $1
			RETURNING u.balance + u.gift_balance AS total_balance,
				u.frozen_balance + u.frozen_gift_balance AS total_frozen,
				a.ordinary_hold,
				$1 - a.ordinary_hold AS gift_hold
		)
		SELECT total_balance, total_frozen, ordinary_hold, gift_hold FROM updated
	`, cmd.HoldAmount, cmd.UserID).Scan(&balance, &frozen, &ordinaryHold, &giftHold)
	if err == nil {
		if _, updateErr := tx.ExecContext(ctx, `
			UPDATE usage_billing_dedup
			SET ordinary_hold_amount = $1, gift_hold_amount = $2
			WHERE request_id = $3 AND api_key_id = $4
		`, ordinaryHold, giftHold, cmd.RequestID, cmd.APIKeyID); updateErr != nil {
			return nil, updateErr
		}
		return &service.BatchImageBalanceHoldResult{NewBalance: &balance, FrozenBalance: &frozen}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if exists, existsErr := userExistsForBilling(ctx, tx, cmd.UserID); existsErr != nil {
		return nil, existsErr
	} else if !exists {
		return nil, service.ErrUserNotFound
	}
	return nil, service.ErrBatchImageInsufficientBalance
}

func captureUsageBillingBatchImageBalance(ctx context.Context, tx *sql.Tx, cmd *service.BatchImageBalanceHoldCommand) (*service.BatchImageBalanceHoldResult, error) {
	if cmd.HoldAmount <= 0 && cmd.ActualAmount <= 0 {
		return &service.BatchImageBalanceHoldResult{}, nil
	}
	if cmd.ActualAmount-cmd.HoldAmount > 0.00000001 {
		return nil, service.ErrBatchImageSettlementCostExceedsHold
	}
	receipt, held, err := lockBatchImageHoldReceipt(ctx, tx, service.BatchImageHoldRequestID(cmd.BatchID), cmd.APIKeyID)
	if err != nil {
		return nil, err
	}
	if !held {
		return nil, errors.New("batch image balance hold was not found")
	}
	if done, terminalErr := validateBatchImageHoldTerminal(receipt.terminalKind, batchImageHoldTerminalCaptured); done || terminalErr != nil {
		return &service.BatchImageBalanceHoldResult{}, terminalErr
	}
	ordinaryHold, giftHold := receipt.ordinaryHold, receipt.giftHold
	if ordinaryHold == 0 && giftHold == 0 {
		ordinaryHold = cmd.HoldAmount
	}
	ordinaryUsed := cmd.ActualAmount
	if ordinaryUsed > ordinaryHold {
		ordinaryUsed = ordinaryHold
	}
	giftUsed := cmd.ActualAmount - ordinaryUsed
	ordinaryRelease := ordinaryHold - ordinaryUsed
	giftRelease := giftHold - giftUsed
	if giftRelease < -0.00000001 {
		return nil, service.ErrBatchImageSettlementCostExceedsHold
	}
	if giftRelease < 0 {
		giftRelease = 0
	}
	var balance, frozen float64
	err = tx.QueryRowContext(ctx, `
			UPDATE users
			SET balance = balance + $1,
				gift_balance = gift_balance + $2,
				frozen_balance = frozen_balance - $3,
				frozen_gift_balance = frozen_gift_balance - $4,
				updated_at = NOW()
			WHERE id = $5 AND deleted_at IS NULL
				AND frozen_balance >= $3
				AND frozen_gift_balance >= $4
			RETURNING balance + gift_balance, frozen_balance + frozen_gift_balance
	`, ordinaryRelease, giftRelease, ordinaryHold, giftHold, cmd.UserID).Scan(&balance, &frozen)
	if err == nil {
		if err := markBatchImageHoldTerminal(ctx, tx, service.BatchImageHoldRequestID(cmd.BatchID), cmd.APIKeyID, receipt.archived, batchImageHoldTerminalCaptured); err != nil {
			return nil, err
		}
		return &service.BatchImageBalanceHoldResult{NewBalance: &balance, FrozenBalance: &frozen}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if exists, existsErr := userExistsForBilling(ctx, tx, cmd.UserID); existsErr != nil {
		return nil, existsErr
	} else if !exists {
		return nil, service.ErrUserNotFound
	}
	return nil, errors.New("batch image frozen balance is insufficient")
}

func releaseUsageBillingBatchImageBalance(ctx context.Context, tx *sql.Tx, cmd *service.BatchImageBalanceHoldCommand) (*service.BatchImageBalanceHoldResult, error) {
	if cmd.HoldAmount <= 0 {
		return &service.BatchImageBalanceHoldResult{}, nil
	}
	// 释放前校验该 job 确实预留过 hold（hold request id 已被 claim），
	// 防止从未成功冻结的 job 触发"幻影释放"，从其他用户的冻结资金池中凭空生成余额。
	receipt, held, heldErr := lockBatchImageHoldReceipt(ctx, tx, service.BatchImageHoldRequestID(cmd.BatchID), cmd.APIKeyID)
	if heldErr != nil {
		return nil, heldErr
	}
	if !held {
		logger.LegacyPrintf("repository.usage_billing", "[BatchImage] release skipped, hold was never reserved: batch=%s", cmd.BatchID)
		return &service.BatchImageBalanceHoldResult{}, nil
	}
	if done, terminalErr := validateBatchImageHoldTerminal(receipt.terminalKind, batchImageHoldTerminalReleased); done || terminalErr != nil {
		return &service.BatchImageBalanceHoldResult{}, terminalErr
	}
	ordinaryHold, giftHold := receipt.ordinaryHold, receipt.giftHold
	if ordinaryHold == 0 && giftHold == 0 {
		ordinaryHold = cmd.HoldAmount
	}
	var balance, frozen float64
	err := tx.QueryRowContext(ctx, `
			UPDATE users
			SET balance = balance + $1,
				gift_balance = gift_balance + $2,
				frozen_balance = frozen_balance - $1,
				frozen_gift_balance = frozen_gift_balance - $2,
				updated_at = NOW()
			WHERE id = $3 AND deleted_at IS NULL
				AND frozen_balance >= $1
				AND frozen_gift_balance >= $2
			RETURNING balance + gift_balance, frozen_balance + frozen_gift_balance
	`, ordinaryHold, giftHold, cmd.UserID).Scan(&balance, &frozen)
	if err == nil {
		if err := markBatchImageHoldTerminal(ctx, tx, service.BatchImageHoldRequestID(cmd.BatchID), cmd.APIKeyID, receipt.archived, batchImageHoldTerminalReleased); err != nil {
			return nil, err
		}
		return &service.BatchImageBalanceHoldResult{NewBalance: &balance, FrozenBalance: &frozen}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if exists, existsErr := userExistsForBilling(ctx, tx, cmd.UserID); existsErr != nil {
		return nil, existsErr
	} else if !exists {
		return nil, service.ErrUserNotFound
	}
	return nil, errors.New("batch image frozen balance is insufficient")
}

// lockBatchImageHoldReceipt serializes capture and release against the same hold.
func lockBatchImageHoldReceipt(ctx context.Context, tx *sql.Tx, holdRequestID string, apiKeyID int64) (receipt batchImageHoldReceipt, exists bool, err error) {
	err = tx.QueryRowContext(ctx, `
				SELECT ordinary_hold_amount, gift_hold_amount, hold_terminal_kind
				FROM usage_billing_dedup
				WHERE request_id = $1 AND api_key_id = $2
				FOR UPDATE
	`, holdRequestID, apiKeyID).Scan(&receipt.ordinaryHold, &receipt.giftHold, &receipt.terminalKind)
	if err == nil {
		return receipt, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return batchImageHoldReceipt{}, false, err
	}
	err = tx.QueryRowContext(ctx, `
				SELECT ordinary_hold_amount, gift_hold_amount, hold_terminal_kind
				FROM usage_billing_dedup_archive
				WHERE request_id = $1 AND api_key_id = $2
				FOR UPDATE
	`, holdRequestID, apiKeyID).Scan(&receipt.ordinaryHold, &receipt.giftHold, &receipt.terminalKind)
	if err == nil {
		receipt.archived = true
		return receipt, true, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return batchImageHoldReceipt{}, false, nil
	}
	return batchImageHoldReceipt{}, false, err
}

func validateBatchImageHoldTerminal(stored, proposed string) (bool, error) {
	stored = strings.TrimSpace(stored)
	if stored == "" {
		return false, nil
	}
	if stored == proposed {
		return true, nil
	}
	return false, fmt.Errorf("%w: batch image hold already %s", service.ErrUsageBillingRequestConflict, stored)
}

func markBatchImageHoldTerminal(ctx context.Context, tx *sql.Tx, holdRequestID string, apiKeyID int64, archived bool, terminalKind string) error {
	query := `
		UPDATE usage_billing_dedup
		SET hold_terminal_kind = $1
		WHERE request_id = $2 AND api_key_id = $3 AND hold_terminal_kind = ''
	`
	if archived {
		query = `
			UPDATE usage_billing_dedup_archive
			SET hold_terminal_kind = $1
			WHERE request_id = $2 AND api_key_id = $3 AND hold_terminal_kind = ''
		`
	}
	result, err := tx.ExecContext(ctx, query, terminalKind, holdRequestID, apiKeyID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("%w: batch image hold terminal CAS lost", service.ErrUsageBillingRequestConflict)
	}
	return nil
}

func userExistsForBilling(ctx context.Context, tx *sql.Tx, userID int64) (bool, error) {
	var exists int
	err := tx.QueryRowContext(ctx, `
		SELECT 1
		FROM users
		WHERE id = $1 AND deleted_at IS NULL
	`, userID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func incrementUsageBillingAPIKeyQuota(ctx context.Context, tx *sql.Tx, apiKeyID int64, amount float64) (bool, error) {
	var exhausted bool
	err := tx.QueryRowContext(ctx, `
		UPDATE api_keys
		SET quota_used = quota_used + $1,
			status = CASE
				WHEN quota > 0
					AND status = $3
					AND quota_used < quota
					AND quota_used + $1 >= quota
				THEN $4
				ELSE status
			END,
			updated_at = NOW()
		WHERE id = $2 AND deleted_at IS NULL
		RETURNING quota > 0 AND quota_used >= quota AND quota_used - $1 < quota
	`, amount, apiKeyID, service.StatusAPIKeyActive, service.StatusAPIKeyQuotaExhausted).Scan(&exhausted)
	if errors.Is(err, sql.ErrNoRows) {
		return false, service.ErrAPIKeyNotFound
	}
	if err != nil {
		return false, err
	}
	return exhausted, nil
}

func incrementUsageBillingAPIKeyRateLimit(ctx context.Context, tx *sql.Tx, apiKeyID int64, cost float64) error {
	res, err := tx.ExecContext(ctx, `
		UPDATE api_keys SET
			usage_5h = CASE WHEN window_5h_start IS NOT NULL AND window_5h_start + INTERVAL '5 hours' <= NOW() THEN $1 ELSE usage_5h + $1 END,
			usage_1d = CASE WHEN window_1d_start IS NOT NULL AND window_1d_start + INTERVAL '24 hours' <= NOW() THEN $1 ELSE usage_1d + $1 END,
			usage_7d = CASE WHEN window_7d_start IS NOT NULL AND window_7d_start + INTERVAL '7 days' <= NOW() THEN $1 ELSE usage_7d + $1 END,
			window_5h_start = CASE WHEN window_5h_start IS NULL OR window_5h_start + INTERVAL '5 hours' <= NOW() THEN NOW() ELSE window_5h_start END,
			window_1d_start = CASE WHEN window_1d_start IS NULL OR window_1d_start + INTERVAL '24 hours' <= NOW() THEN date_trunc('day', NOW()) ELSE window_1d_start END,
			window_7d_start = CASE WHEN window_7d_start IS NULL OR window_7d_start + INTERVAL '7 days' <= NOW() THEN date_trunc('day', NOW()) ELSE window_7d_start END,
			updated_at = NOW()
		WHERE id = $2 AND deleted_at IS NULL
	`, cost, apiKeyID)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return service.ErrAPIKeyNotFound
	}
	return nil
}

func incrementUsageBillingAccountQuota(ctx context.Context, tx *sql.Tx, accountID int64, amount float64) (*service.AccountQuotaState, error) {
	rows, err := tx.QueryContext(ctx,
		`UPDATE accounts SET extra = (
			COALESCE(extra, '{}'::jsonb)
			|| jsonb_build_object('quota_used', COALESCE((extra->>'quota_used')::numeric, 0) + $1)
			|| CASE WHEN COALESCE((extra->>'quota_daily_limit')::numeric, 0) > 0 THEN
				jsonb_build_object(
					'quota_daily_used',
					CASE WHEN `+dailyExpiredExpr+`
					THEN $1
					ELSE COALESCE((extra->>'quota_daily_used')::numeric, 0) + $1 END,
					'quota_daily_start',
					CASE WHEN `+dailyExpiredExpr+`
					THEN `+nowUTC+`
					ELSE COALESCE(extra->>'quota_daily_start', `+nowUTC+`) END
				)
				|| CASE WHEN `+dailyExpiredExpr+` AND `+nextDailyResetAtExpr+` IS NOT NULL
				   THEN jsonb_build_object('quota_daily_reset_at', `+nextDailyResetAtExpr+`)
				   ELSE '{}'::jsonb END
			ELSE '{}'::jsonb END
			|| CASE WHEN COALESCE((extra->>'quota_weekly_limit')::numeric, 0) > 0 THEN
				jsonb_build_object(
					'quota_weekly_used',
					CASE WHEN `+weeklyExpiredExpr+`
					THEN $1
					ELSE COALESCE((extra->>'quota_weekly_used')::numeric, 0) + $1 END,
					'quota_weekly_start',
					CASE WHEN `+weeklyExpiredExpr+`
					THEN `+nowUTC+`
					ELSE COALESCE(extra->>'quota_weekly_start', `+nowUTC+`) END
				)
				|| CASE WHEN `+weeklyExpiredExpr+` AND `+nextWeeklyResetAtExpr+` IS NOT NULL
				   THEN jsonb_build_object('quota_weekly_reset_at', `+nextWeeklyResetAtExpr+`)
				   ELSE '{}'::jsonb END
			ELSE '{}'::jsonb END
		), updated_at = NOW()
		WHERE id = $2 AND deleted_at IS NULL
		RETURNING
			COALESCE((extra->>'quota_used')::numeric, 0),
			COALESCE((extra->>'quota_limit')::numeric, 0),
			COALESCE((extra->>'quota_daily_used')::numeric, 0),
			COALESCE((extra->>'quota_daily_limit')::numeric, 0),
			COALESCE((extra->>'quota_weekly_used')::numeric, 0),
			COALESCE((extra->>'quota_weekly_limit')::numeric, 0)`,
		amount, accountID)
	if err != nil {
		return nil, err
	}

	var state service.AccountQuotaState
	if rows.Next() {
		if err := rows.Scan(
			&state.TotalUsed, &state.TotalLimit,
			&state.DailyUsed, &state.DailyLimit,
			&state.WeeklyUsed, &state.WeeklyLimit,
		); err != nil {
			_ = rows.Close()
			return nil, err
		}
	} else {
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, err
		}
		_ = rows.Close()
		return nil, service.ErrAccountNotFound
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	// 必须在执行下一条 SQL 前显式关闭 rows：pq 驱动在同一连接上
	// 不允许前一条查询的结果集未耗尽时启动新查询，否则会返回
	// "unexpected Parse response" 错误。
	if err := rows.Close(); err != nil {
		return nil, err
	}
	// 任意维度额度在本次递增中从"未超"跨越到"已超"时，必须刷新调度快照，
	// 否则 Redis 中缓存的 Account 仍显示旧的 used 值，后续请求会继续选中本账号，
	// 最终观察到 daily_used / weekly_used 大幅超过配置的 limit。
	// 对于日/周额度，即使本次触发了周期重置（pre=0、post=amount），
	// 判定式 (post-amount) < limit 同样成立，逻辑与总额度保持一致。
	crossedTotal := state.TotalLimit > 0 && state.TotalUsed >= state.TotalLimit && (state.TotalUsed-amount) < state.TotalLimit
	crossedDaily := state.DailyLimit > 0 && state.DailyUsed >= state.DailyLimit && (state.DailyUsed-amount) < state.DailyLimit
	crossedWeekly := state.WeeklyLimit > 0 && state.WeeklyUsed >= state.WeeklyLimit && (state.WeeklyUsed-amount) < state.WeeklyLimit
	if crossedTotal || crossedDaily || crossedWeekly {
		if err := enqueueSchedulerOutbox(ctx, tx, service.SchedulerOutboxEventAccountChanged, &accountID, nil, nil); err != nil {
			logger.LegacyPrintf("repository.usage_billing", "[SchedulerOutbox] enqueue quota exceeded failed: account=%d err=%v", accountID, err)
			return nil, err
		}
	}
	return &state, nil
}
