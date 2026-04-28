package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

type profitabilityRepository struct {
	sql sqlExecutor
}

func NewProfitabilityRepository(sqlDB *sql.DB) service.ProfitabilityUsageRepository {
	return &profitabilityRepository{sql: sqlDB}
}

func (r *profitabilityRepository) GetUsageCostTrend(ctx context.Context, filter service.ProfitabilityUsageFilter, granularity string) ([]service.ProfitabilityUsageTrendRow, error) {
	bucketExpr := "DATE_TRUNC('day', created_at)"
	if strings.EqualFold(strings.TrimSpace(granularity), "hour") {
		bucketExpr = "DATE_TRUNC('hour', created_at)"
	}

	query := fmt.Sprintf(`
		SELECT
			%s AS bucket,
			COALESCE(SUM(total_cost), 0) AS standard_cost_usd,
			COALESCE(SUM(actual_cost), 0) AS usage_proxy_usd,
			COALESCE(SUM(COALESCE(account_stats_cost, total_cost) * COALESCE(account_rate_multiplier, 1)), 0) AS account_cost_usd
		FROM usage_logs
		WHERE created_at >= $1 AND created_at < $2
	`, bucketExpr)

	args := []any{filter.StartTime, filter.EndTime}
	query, args = appendProfitabilityUsageFilters(query, args, filter)
	query += " GROUP BY bucket ORDER BY bucket ASC"

	rows, err := r.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	result := make([]service.ProfitabilityUsageTrendRow, 0)
	for rows.Next() {
		var row service.ProfitabilityUsageTrendRow
		if err := rows.Scan(&row.Bucket, &row.StandardCostUSD, &row.UsageProxyUSD, &row.AccountCostUSD); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func (r *profitabilityRepository) GetAccountAggregates(ctx context.Context, filter service.ProfitabilityUsageFilter) ([]service.ProfitabilityAccountAggregateRow, error) {
	query := `
		SELECT
			account_id,
			COALESCE(SUM(total_cost), 0) AS standard_cost_usd,
			COALESCE(SUM(actual_cost), 0) AS usage_proxy_usd,
			COALESCE(SUM(COALESCE(account_stats_cost, total_cost) * COALESCE(account_rate_multiplier, 1)), 0) AS account_cost_usd
		FROM usage_logs
		WHERE created_at >= $1 AND created_at < $2
	`
	args := []any{filter.StartTime, filter.EndTime}
	query, args = appendProfitabilityUsageFilters(query, args, filter)
	query += " GROUP BY account_id ORDER BY account_cost_usd DESC, account_id ASC"

	rows, err := r.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	result := make([]service.ProfitabilityAccountAggregateRow, 0)
	for rows.Next() {
		var row service.ProfitabilityAccountAggregateRow
		if err := rows.Scan(&row.AccountID, &row.StandardCostUSD, &row.UsageProxyUSD, &row.AccountCostUSD); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func (r *profitabilityRepository) GetAccountWindowAggregates(ctx context.Context, filter service.ProfitabilityUsageFilter, startTime time.Time) (map[int64]float64, error) {
	query := `
		SELECT
			account_id,
			COALESCE(SUM(COALESCE(account_stats_cost, total_cost) * COALESCE(account_rate_multiplier, 1)), 0) AS account_cost_usd
		FROM usage_logs
		WHERE created_at >= $1 AND created_at < $2
	`
	args := []any{startTime, filter.EndTime}
	windowFilter := filter
	windowFilter.StartTime = startTime
	query, args = appendProfitabilityUsageFilters(query, args, windowFilter)
	query += " GROUP BY account_id"

	rows, err := r.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	result := make(map[int64]float64)
	for rows.Next() {
		var accountID int64
		var cost float64
		if err := rows.Scan(&accountID, &cost); err != nil {
			return nil, err
		}
		result[accountID] = cost
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func (r *profitabilityRepository) GetAccountCostSeries(ctx context.Context, filter service.ProfitabilityUsageFilter, startTime, endTime time.Time, granularity string) ([]service.ProfitabilityAccountCostPoint, error) {
	bucketExpr := "DATE_TRUNC('day', created_at)"
	if strings.EqualFold(strings.TrimSpace(granularity), "hour") {
		bucketExpr = "DATE_TRUNC('hour', created_at)"
	}

	query := fmt.Sprintf(`
		SELECT
			account_id,
			%s AS bucket,
			COALESCE(SUM(COALESCE(account_stats_cost, total_cost) * COALESCE(account_rate_multiplier, 1)), 0) AS account_cost_usd
		FROM usage_logs
		WHERE created_at >= $1 AND created_at < $2
	`, bucketExpr)
	args := []any{startTime, endTime}
	seriesFilter := filter
	seriesFilter.StartTime = startTime
	seriesFilter.EndTime = endTime
	query, args = appendProfitabilityUsageFilters(query, args, seriesFilter)
	query += " GROUP BY account_id, bucket ORDER BY account_id ASC, bucket ASC"

	rows, err := r.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	result := make([]service.ProfitabilityAccountCostPoint, 0)
	for rows.Next() {
		var row service.ProfitabilityAccountCostPoint
		if err := rows.Scan(&row.AccountID, &row.Bucket, &row.AccountCostUSD); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func (r *profitabilityRepository) GetUserAggregates(ctx context.Context, filter service.ProfitabilityUsageFilter) ([]service.ProfitabilityUserAggregateRow, error) {
	query := `
		SELECT
			ul.user_id,
			COALESCE(u.email, '') AS email,
			ul.group_id,
			ul.subscription_id,
			COALESCE(SUM(ul.total_cost), 0) AS standard_cost_usd,
			COALESCE(SUM(ul.actual_cost), 0) AS usage_proxy_usd,
			COALESCE(SUM(COALESCE(ul.account_stats_cost, ul.total_cost) * COALESCE(ul.account_rate_multiplier, 1)), 0) AS account_cost_usd
		FROM usage_logs ul
		LEFT JOIN users u ON u.id = ul.user_id
		WHERE ul.created_at >= $1 AND ul.created_at < $2
		  AND ul.subscription_id IS NOT NULL
	`
	args := []any{filter.StartTime, filter.EndTime}
	query, args = appendProfitabilityUsageFiltersWithAlias(query, args, filter, "ul")
	query += `
		GROUP BY ul.user_id, u.email, ul.group_id, ul.subscription_id
		ORDER BY account_cost_usd DESC, ul.subscription_id ASC
	`

	rows, err := r.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	result := make([]service.ProfitabilityUserAggregateRow, 0)
	for rows.Next() {
		var row service.ProfitabilityUserAggregateRow
		if err := rows.Scan(&row.UserID, &row.Email, &row.GroupID, &row.SubscriptionID, &row.StandardCostUSD, &row.UsageProxyUSD, &row.AccountCostUSD); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func (r *profitabilityRepository) GetUserWindowAggregates(ctx context.Context, filter service.ProfitabilityUsageFilter, startTime time.Time) (map[int64]service.ProfitabilitySubscriptionWindowUsageRow, error) {
	query := `
		SELECT
			ul.subscription_id,
			COALESCE(SUM(ul.actual_cost), 0) AS usage_proxy_usd,
			COALESCE(SUM(COALESCE(ul.account_stats_cost, ul.total_cost) * COALESCE(ul.account_rate_multiplier, 1)), 0) AS account_cost_usd
		FROM usage_logs ul
		WHERE ul.created_at >= $1 AND ul.created_at < $2
		  AND ul.subscription_id IS NOT NULL
	`
	args := []any{startTime, filter.EndTime}
	windowFilter := filter
	windowFilter.StartTime = startTime
	query, args = appendProfitabilityUsageFiltersWithAlias(query, args, windowFilter, "ul")
	query += " GROUP BY ul.subscription_id"

	rows, err := r.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	result := make(map[int64]service.ProfitabilitySubscriptionWindowUsageRow)
	for rows.Next() {
		var (
			subscriptionID int64
			row            service.ProfitabilitySubscriptionWindowUsageRow
		)
		if err := rows.Scan(&subscriptionID, &row.UsageProxyUSD, &row.AccountCostUSD); err != nil {
			return nil, err
		}
		row.SubscriptionID = subscriptionID
		result[subscriptionID] = row
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func (r *profitabilityRepository) GetUserCostSeries(ctx context.Context, filter service.ProfitabilityUsageFilter, startTime, endTime time.Time, granularity string) ([]service.ProfitabilityUserCostPoint, error) {
	bucketExpr := "DATE_TRUNC('day', created_at)"
	if strings.EqualFold(strings.TrimSpace(granularity), "hour") {
		bucketExpr = "DATE_TRUNC('hour', created_at)"
	}

	query := fmt.Sprintf(`
		SELECT
			ul.subscription_id,
			%s AS bucket,
			COALESCE(SUM(ul.actual_cost), 0) AS usage_proxy_usd
		FROM usage_logs ul
		WHERE ul.created_at >= $1 AND ul.created_at < $2
		  AND ul.subscription_id IS NOT NULL
	`, bucketExpr)
	args := []any{startTime, endTime}
	seriesFilter := filter
	seriesFilter.StartTime = startTime
	seriesFilter.EndTime = endTime
	query, args = appendProfitabilityUsageFiltersWithAlias(query, args, seriesFilter, "ul")
	query += " GROUP BY ul.subscription_id, bucket ORDER BY ul.subscription_id ASC, bucket ASC"

	rows, err := r.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	result := make([]service.ProfitabilityUserCostPoint, 0)
	for rows.Next() {
		var row service.ProfitabilityUserCostPoint
		if err := rows.Scan(&row.SubscriptionID, &row.Bucket, &row.UsageProxyUSD); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func appendProfitabilityUsageFilters(query string, args []any, filter service.ProfitabilityUsageFilter) (string, []any) {
	return appendProfitabilityUsageFiltersWithAlias(query, args, filter, "")
}

func appendProfitabilityUsageFiltersWithAlias(query string, args []any, filter service.ProfitabilityUsageFilter, alias string) (string, []any) {
	prefix := ""
	if alias != "" {
		prefix = alias + "."
	}
	if filter.UserID > 0 {
		query += fmt.Sprintf(" AND %suser_id = $%d", prefix, len(args)+1)
		args = append(args, filter.UserID)
	}
	if filter.GroupID > 0 {
		query += fmt.Sprintf(" AND %sgroup_id = $%d", prefix, len(args)+1)
		args = append(args, filter.GroupID)
	}
	if filter.AccountID > 0 {
		query += fmt.Sprintf(" AND %saccount_id = $%d", prefix, len(args)+1)
		args = append(args, filter.AccountID)
	}
	return query, args
}
