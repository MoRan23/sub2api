package repository

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

var (
	userSubjectDashboardQuery = buildSubjectDashboardQuery("user_id")
	keySubjectDashboardQuery  = buildSubjectDashboardQuery("api_key_id")
)

// Keep SQL immutable across reads rather than allocating the same large string.
// Both streams retain IDs so current metadata determines platform attribution.
func buildSubjectDashboardQuery(field string) string {
	return `WITH parts AS (
 SELECT group_id,account_id,FALSE AS is_today,` + subjectRollupColumns + `
 FROM usage_subject_daily_rollups WHERE ` + field + `=$1 AND bucket_date >= $2::date AND bucket_date < $3::date
 UNION ALL
 SELECT COALESCE(group_id,0),COALESCE(account_id,0),created_at >= $5 AS is_today,` + subjectRollupAggregate + `
 FROM usage_logs WHERE ` + field + `=$1 AND created_at >= $4
 GROUP BY group_id,account_id,created_at >= $5
 ), attributed AS (
 SELECT p.*,` + usageLogEffectivePlatformExpr + ` AS platform
 FROM parts p LEFT JOIN groups g ON g.id=p.group_id LEFT JOIN accounts a ON a.id=p.account_id
 )
 SELECT GROUPING(platform),platform,
 COALESCE(SUM(request_count),0),COALESCE(SUM(input_tokens),0),COALESCE(SUM(output_tokens),0),
 COALESCE(SUM(cache_creation_tokens),0),COALESCE(SUM(cache_read_tokens),0),
 COALESCE(SUM(total_cost),0),COALESCE(SUM(actual_cost),0),
 COALESCE(SUM(duration_sum_ms)/NULLIF(SUM(duration_count),0),0),
 COALESCE(SUM(request_count) FILTER(WHERE is_today),0),COALESCE(SUM(input_tokens) FILTER(WHERE is_today),0),
 COALESCE(SUM(output_tokens) FILTER(WHERE is_today),0),COALESCE(SUM(cache_creation_tokens) FILTER(WHERE is_today),0),
 COALESCE(SUM(cache_read_tokens) FILTER(WHERE is_today),0),COALESCE(SUM(total_cost) FILTER(WHERE is_today),0),
 COALESCE(SUM(actual_cost) FILTER(WHERE is_today),0),
 COALESCE(SUM(success_count),0),COALESCE(SUM(charged_tokens),0),COALESCE(SUM(charged_cost),0),
 COALESCE(SUM(success_count) FILTER(WHERE is_today),0),COALESCE(SUM(charged_tokens) FILTER(WHERE is_today),0),
 COALESCE(SUM(charged_cost) FILTER(WHERE is_today),0)
 FROM attributed GROUP BY GROUPING SETS((),(platform))`
}

func subjectRollupTailStart(closed, stateTZ string, today time.Time) (time.Time, error) {
	tail, err := service.ParseGroupUsageDate(closed)
	if err != nil || stateTZ != service.GroupUsageTimezoneName() || tail.Before(service.GroupUsageYesterdayStart(today)) || tail.After(today) {
		return time.Time{}, service.ErrUsageStatsInitializing
	}
	return tail, nil
}

// subject summaries never expand their raw tail beyond yesterday's local midnight.
func (r *usageLogRepository) getSubjectDashboardStats(ctx context.Context, id int64, byKey bool) (result *UserDashboardStats, err error) {
	q := r.sql
	if db, ok := q.(*sql.DB); ok {
		tx, e := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
		if e != nil {
			return nil, e
		}
		defer func() {
			if err != nil {
				_ = tx.Rollback()
			} else if err = tx.Commit(); err != nil {
				result = nil
			}
		}()
		q = tx
	}
	today := service.GroupUsageTodayStart(time.Now())
	var closed, stateTZ string
	var retained time.Time
	err = scanSingleRow(ctx, q, "SELECT closed_before::text,retained_from,timezone_name FROM usage_subject_rollup_state WHERE id=1", nil, &closed, &retained, &stateTZ)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, service.ErrUsageStatsInitializing
		}
		return nil, err
	}
	tail, e := subjectRollupTailStart(closed, stateTZ, today)
	if e != nil {
		return nil, e
	}
	query := userSubjectDashboardQuery
	if byKey {
		query = keySubjectDashboardQuery
	}
	rows, e := q.QueryContext(ctx, query, id, service.GroupUsageDate(retained), closed, tail.UTC(), today.UTC())
	if e != nil {
		return nil, e
	}
	stats := &UserDashboardStats{}
	platforms := make([]PlatformDashboardStats, 0)
	for rows.Next() {
		var grouping int
		var platform sql.NullString
		var row UserDashboardStats
		var p PlatformDashboardStats
		if e = rows.Scan(&grouping, &platform, &row.TotalRequests, &row.TotalInputTokens, &row.TotalOutputTokens,
			&row.TotalCacheCreationTokens, &row.TotalCacheReadTokens, &row.TotalCost, &row.TotalActualCost, &row.AverageDurationMs,
			&row.TodayRequests, &row.TodayInputTokens, &row.TodayOutputTokens, &row.TodayCacheCreationTokens, &row.TodayCacheReadTokens,
			&row.TodayCost, &row.TodayActualCost, &p.TotalRequests, &p.TotalTokens, &p.TotalActualCost,
			&p.TodayRequests, &p.TodayTokens, &p.TodayActualCost); e != nil {
			rows.Close()
			return nil, e
		}
		if grouping == 1 {
			*stats = row
		} else if !byKey && platform.Valid && platform.String != "" && p.TotalActualCost > 0 {
			p.Platform = platform.String
			platforms = append(platforms, p)
		}
	}
	e = rows.Err()
	ce := rows.Close()
	if e != nil {
		return nil, e
	}
	if ce != nil {
		return nil, ce
	}
	stats.TotalTokens = stats.TotalInputTokens + stats.TotalOutputTokens + stats.TotalCacheCreationTokens + stats.TotalCacheReadTokens
	stats.TodayTokens = stats.TodayInputTokens + stats.TodayOutputTokens + stats.TodayCacheCreationTokens + stats.TodayCacheReadTokens
	sort.Slice(platforms, func(i, j int) bool { return platforms[i].TotalActualCost > platforms[j].TotalActualCost })
	stats.ByPlatform = platforms
	if byKey {
		stats.TotalAPIKeys = 1
		stats.ActiveAPIKeys = 1
	} else {
		err = scanSingleRow(ctx, q, "SELECT COUNT(*),COUNT(*) FILTER(WHERE status='active') FROM api_keys WHERE user_id=$1 AND deleted_at IS NULL", []any{id}, &stats.TotalAPIKeys, &stats.ActiveAPIKeys)
	}
	if err == nil {
		perfQuery := "SELECT COUNT(*),COALESCE(SUM(input_tokens+output_tokens),0) FROM usage_logs WHERE user_id=$1 AND created_at >= $2"
		if byKey {
			perfQuery = "SELECT COUNT(*),COALESCE(SUM(input_tokens+output_tokens+cache_creation_tokens+cache_read_tokens),0) FROM usage_logs WHERE api_key_id=$1 AND created_at >= $2"
		}
		var requests, tokens int64
		err = scanSingleRow(ctx, q, perfQuery, []any{id, time.Now().Add(-5 * time.Minute)}, &requests, &tokens)
		stats.Rpm = requests / 5
		stats.Tpm = tokens / 5
	}
	if err != nil {
		return nil, err
	}
	return stats, nil
}
