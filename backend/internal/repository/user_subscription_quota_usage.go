package repository

import (
	"context"
	"database/sql"
	"math"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

const totalQuotaConsumeEpsilon = 1e-9

func incrementSubscriptionUsage(ctx context.Context, exec sqlExecutor, subscriptionID int64, costUSD float64) error {
	if costUSD <= 0 {
		return nil
	}

	subscriptionType, err := getSubscriptionTypeForUsage(ctx, exec, subscriptionID)
	if err != nil {
		return err
	}
	if subscriptionType == service.SubscriptionTypeTotalQuota {
		return consumeTotalQuotaSubscriptionUsage(ctx, exec, subscriptionID, costUSD)
	}
	return incrementWindowedSubscriptionUsage(ctx, exec, subscriptionID, costUSD)
}

func getSubscriptionTypeForUsage(ctx context.Context, exec sqlExecutor, subscriptionID int64) (string, error) {
	var subscriptionType string
	err := scanSingleRow(ctx, exec, `
		SELECT g.subscription_type
		FROM user_subscriptions us
		JOIN groups g ON g.id = us.group_id
		WHERE us.id = $1
		  AND us.deleted_at IS NULL
		  AND g.deleted_at IS NULL
	`, []any{subscriptionID}, &subscriptionType)
	if err == sql.ErrNoRows {
		return "", service.ErrSubscriptionNotFound
	}
	if err != nil {
		return "", err
	}
	return subscriptionType, nil
}

func incrementWindowedSubscriptionUsage(ctx context.Context, exec sqlExecutor, subscriptionID int64, costUSD float64) error {
	const updateSQL = `
		UPDATE user_subscriptions us
		SET
			daily_usage_usd = us.daily_usage_usd + $1,
			weekly_usage_usd = us.weekly_usage_usd + $1,
			monthly_usage_usd = us.monthly_usage_usd + $1,
			updated_at = NOW()
		FROM groups g
		WHERE us.id = $2
			AND us.deleted_at IS NULL
			AND us.group_id = g.id
			AND g.deleted_at IS NULL
	`
	res, err := exec.ExecContext(ctx, updateSQL, costUSD, subscriptionID)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected > 0 {
		return nil
	}
	return service.ErrSubscriptionNotFound
}

func consumeTotalQuotaSubscriptionUsage(ctx context.Context, exec sqlExecutor, subscriptionID int64, costUSD float64) error {
	now := time.Now().UTC()
	rows, err := exec.QueryContext(ctx, `
		SELECT id, quota_total_usd, quota_used_usd
		FROM user_subscription_quota_events
		WHERE user_subscription_id = $1
		  AND expires_at > $2
		ORDER BY expires_at ASC, id ASC
		FOR UPDATE
	`, subscriptionID, now)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()

	type quotaRow struct {
		id         int64
		totalUSD   float64
		usedUSD    float64
	}

	events := make([]quotaRow, 0)
	for rows.Next() {
		var row quotaRow
		if err := rows.Scan(&row.id, &row.totalUSD, &row.usedUSD); err != nil {
			return err
		}
		events = append(events, row)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(events) == 0 {
		return service.ErrTotalLimitExceeded
	}

	remainingCost := costUSD
	for _, event := range events {
		remaining := event.totalUSD - event.usedUSD
		if remaining <= totalQuotaConsumeEpsilon {
			continue
		}
		delta := math.Min(remainingCost, remaining)
		if delta <= totalQuotaConsumeEpsilon {
			break
		}
		if _, err := exec.ExecContext(ctx, `
			UPDATE user_subscription_quota_events
			SET quota_used_usd = quota_used_usd + $1,
			    updated_at = NOW()
			WHERE id = $2
		`, delta, event.id); err != nil {
			return err
		}
		remainingCost -= delta
		if remainingCost <= totalQuotaConsumeEpsilon {
			return nil
		}
	}

	if remainingCost > totalQuotaConsumeEpsilon {
		return service.ErrTotalLimitExceeded
	}
	return nil
}
