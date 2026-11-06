package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func marshalEngineBillingSummary(summary service.CodexEngineBillingSummaryGroup) ([]byte, error) {
	if !validEngineBillingText(summary.Kind, 64) || !validEngineBillingText(summary.Purpose, 128) || !validEngineBillingText(summary.Model, 256) || len(summary.Usage) > 64 {
		return nil, errors.New("invalid engine billing summary dimensions")
	}
	if summary.ServiceTier != nil && !validEngineBillingText(*summary.ServiceTier, 128) {
		return nil, errors.New("invalid engine billing summary tier")
	}
	if summary.Usage == nil {
		summary.Usage = map[string]int64{}
	}
	for key, value := range summary.Usage {
		if !validEngineBillingText(key, 64) || value < 0 {
			return nil, errors.New("invalid engine billing summary usage")
		}
	}
	return json.Marshal(summary)
}

// ReadSummary aggregates durably received facts, including unpriced events. Missing
// counters stay missing; they are not coerced to zero or taken from public usage.
func (r *codexEngineBillingStore) ReadSummary(ctx context.Context, jobID, throughSequence int64) ([]service.CodexEngineBillingSummaryGroup, error) {
	if err := r.ready(); err != nil {
		return nil, err
	}
	if jobID <= 0 || throughSequence < 0 {
		return nil, errors.New("invalid engine billing summary range")
	}
	rows, err := r.db.QueryContext(ctx, `SELECT e.summary->>'kind', e.summary->>'purpose', e.summary->>'model',
		e.summary->>'service_tier', u.key, SUM(u.value::numeric)
		FROM codex_engine_billing_received_events e LEFT JOIN LATERAL jsonb_each_text(e.summary->'usage') u ON TRUE
		WHERE e.job_id=$1 AND e.sequence <= $2
		GROUP BY e.summary->>'kind', e.summary->>'purpose', e.summary->>'model', e.summary->>'service_tier', u.key
		ORDER BY 1,2,3,4,5`, jobID, throughSequence)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	groups := make([]service.CodexEngineBillingSummaryGroup, 0)
	indices := make(map[string]int)
	for rows.Next() {
		var kind, purpose, model string
		var tier, key sql.NullString
		var value sql.NullInt64
		if err := rows.Scan(&kind, &purpose, &model, &tier, &key, &value); err != nil {
			return nil, err
		}
		var tierPtr *string
		if tier.Valid {
			s := tier.String
			tierPtr = &s
		}
		encoded, _ := json.Marshal([]any{kind, purpose, model, tierPtr})
		index, ok := indices[string(encoded)]
		if !ok {
			index = len(groups)
			indices[string(encoded)] = index
			groups = append(groups, service.CodexEngineBillingSummaryGroup{Kind: kind, Purpose: purpose, Model: model, ServiceTier: tierPtr, Usage: map[string]int64{}})
		}
		if key.Valid && value.Valid {
			groups[index].Usage[key.String] = value.Int64
		}
	}
	return groups, rows.Err()
}

// Keep platform quota increments in the same transaction as the event ledger,
// ordinary billing and usage row. The existing window helpers define resets.
func incrementEngineBillingPlatformQuota(ctx context.Context, tx *sql.Tx, userID int64, platform string, cost float64, now time.Time) error {
	if !validEngineBillingText(platform, 64) {
		return errors.New("invalid engine billing quota platform")
	}
	var id int64
	var daily, weekly, monthly float64
	var engineDaily, engineWeekly, engineMonthly float64
	var dailyStart, weeklyStart, monthlyStart *time.Time
	err := tx.QueryRowContext(ctx, `SELECT id,daily_usage_usd,weekly_usage_usd,monthly_usage_usd,daily_window_start,weekly_window_start,monthly_window_start,
		engine_daily_usage_usd,engine_weekly_usage_usd,engine_monthly_usage_usd
		FROM user_platform_quotas WHERE user_id=$1 AND platform=$2 AND deleted_at IS NULL FOR UPDATE`, userID, platform).
		Scan(&id, &daily, &weekly, &monthly, &dailyStart, &weeklyStart, &monthlyStart, &engineDaily, &engineWeekly, &engineMonthly)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	now = now.Truncate(time.Microsecond)
	cost = service.QuantizeUsageBillingAmount(cost)
	day, week := quotaCalendarStart(dailyStart, timezone.StartOfDay(now)), quotaCalendarStart(weeklyStart, timezone.StartOfWeek(now))
	engineMonthly, _ = monthlyMaybeReset(engineMonthly, monthlyStart, cost, now)
	monthly, nextMonth := monthlyMaybeReset(monthly, monthlyStart, cost, now)
	_, err = tx.ExecContext(ctx, `UPDATE user_platform_quotas SET daily_usage_usd=$2,weekly_usage_usd=$3,monthly_usage_usd=$4,
		daily_window_start=$5,weekly_window_start=$6,monthly_window_start=$7,
		engine_daily_usage_usd=$8,engine_weekly_usage_usd=$9,engine_monthly_usage_usd=$10,updated_at=NOW() WHERE id=$1`,
		id, maybeReset(daily, dailyStart, day, cost), maybeReset(weekly, weeklyStart, week, cost), monthly, day, week, nextMonth,
		maybeReset(engineDaily, dailyStart, day, cost), maybeReset(engineWeekly, weeklyStart, week, cost), engineMonthly)
	return err
}
