package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/lib/pq"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// groupUsageRollupSnapshot 是一次汇总水位的快照。
// closedBefore / retainedFrom 在水位无效时退化为 1970-01-01，
// 与此前 SQL 里 CASE WHEN valid 的语义一一对应。
type groupUsageRollupSnapshot struct {
	valid        bool
	closedBefore string    // date，历史日桶的右开边界
	retainedDate string    // retained_from 在服务端时区内的日期，历史日桶的左闭边界
	tailStart    time.Time // 尾段（还没进日桶的那部分 usage_logs）的起点
}

// readGroupUsageRollupSnapshot 读一次汇总水位并在 Go 侧判定有效性。
//
// 判定条件与原先 SQL 里的 state_values CTE 完全一致：
// 恰好一行、时区名与当前服务端配置相同、且水位不在未来。
func readGroupUsageRollupSnapshot(ctx context.Context, sqlq sqlExecutor, timezoneName, todayDate string) (groupUsageRollupSnapshot, error) {
	var rowCount int
	var closedBefore sql.NullString
	var retainedFrom sql.NullTime
	var stateTimezone sql.NullString
	var expiredBefore sql.NullTime
	if err := scanSingleRow(ctx, sqlq, `
		SELECT
			COUNT(*),
			MAX(closed_before)::text,
			MAX(retained_from),
			MAX(timezone_name),
			MAX(expired_before)
		FROM usage_group_rollup_state
		WHERE id = 1
	`, nil, &rowCount, &closedBefore, &retainedFrom, &stateTimezone, &expiredBefore); err != nil {
		return groupUsageRollupSnapshot{}, fmt.Errorf("读取分组用量汇总水位: %w", err)
	}
	floor := time.Unix(0, 0).UTC()
	if rowCount == 1 && expiredBefore.Valid && expiredBefore.Time.After(floor) {
		floor = expiredBefore.Time.UTC()
	}
	invalid := groupUsageRollupSnapshot{closedBefore: "1970-01-01", retainedDate: "1970-01-01", tailStart: floor}
	if rowCount != 1 || !closedBefore.Valid || !retainedFrom.Valid ||
		!stateTimezone.Valid || stateTimezone.String != timezoneName ||
		closedBefore.String > todayDate {
		return invalid, nil
	}

	tailStart, err := service.ParseGroupUsageDate(closedBefore.String)
	if err != nil {
		// 水位值解析不出来，按无效处理：宁可多扫一次，也不要算错钱。
		return invalid, nil //nolint:nilerr // 与上面的 valid 判定同语义，均降级为全量重算
	}
	if floor.After(tailStart) {
		tailStart = floor
	}
	retainedDate := service.GroupUsageDate(retainedFrom.Time)
	if expiredDate := service.GroupUsageDate(floor); expiredDate > retainedDate {
		retainedDate = expiredDate
	}
	return groupUsageRollupSnapshot{
		valid:        true,
		closedBefore: closedBefore.String,
		retainedDate: retainedDate,
		tailStart:    tailStart.UTC(),
	}, nil
}

func (r *usageLogRepository) getAllGroupUsageSummaryFromRollups(ctx context.Context, todayStart time.Time) (results []usagestats.GroupUsageSummary, err error) {
	return r.getGroupUsageSummaryFromRollups(ctx, todayStart, nil)
}

func (r *usageLogRepository) GetGroupUsageSummaryByIDs(ctx context.Context, todayStart time.Time, ids []int64) ([]usagestats.GroupUsageSummary, error) {
	if len(ids) == 0 {
		return []usagestats.GroupUsageSummary{}, nil
	}
	return r.getGroupUsageSummaryFromRollups(ctx, todayStart, ids)
}

func (r *usageLogRepository) getGroupUsageSummaryFromRollups(ctx context.Context, todayStart time.Time, ids []int64) (results []usagestats.GroupUsageSummary, err error) {
	sqlq := r.sql
	if db, ok := sqlq.(*sql.DB); ok {
		// Cleanup can delete logs and move the watermark backwards while rollup
		// rebuilds can replace historical buckets. Both reads must see one MVCC
		// snapshot, otherwise an old boundary can be combined with new data.
		tx, beginErr := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
		if beginErr != nil {
			return nil, beginErr
		}
		defer func() {
			if err != nil {
				_ = tx.Rollback()
				return
			}
			if err = tx.Commit(); err != nil {
				results = nil
			}
		}()
		sqlq = tx
	}

	todayStart = service.GroupUsageTodayStart(todayStart)
	yesterdayStart := service.GroupUsageYesterdayStart(todayStart)
	timezoneName := service.GroupUsageTimezoneName()
	todayDate := service.GroupUsageDate(todayStart)
	yesterdayDate := service.GroupUsageDate(yesterdayStart)

	state, err := readGroupUsageRollupSnapshot(ctx, sqlq, timezoneName, todayDate)
	if err != nil {
		return nil, err
	}

	// 尾段起点必须以**查询参数**的形式给进来。
	//
	// 此前它是在同一条 SQL 里由 state CTE 算出、再 CROSS JOIN 给 tail 用的，
	// 于是 created_at 的下界对 planner 来说是个运行期才知道的值：既进不了
	// index cond，也估不准选择性，只能退化成 usage_logs 全表扫。
	// 生产实测（1721 万行 / PostgreSQL 17）：
	//   Seq Scan on usage_logs … rows=17212870, Rows Removed by Join Filter: 17178082
	//   Execution Time: 48720 ms
	// 换成参数之后走 idx_usage_logs_created_at：
	//   Index Scan … Index Cond: (created_at >= $7)
	//   Execution Time: 34 ms
	query := `
		WITH historical AS (
			SELECT
				rollup.group_id,
				COALESCE(SUM(rollup.actual_cost), 0) AS actual_cost,
				COALESCE(SUM(rollup.actual_cost) FILTER (
					WHERE rollup.bucket_date = $3::date
				), 0) AS yesterday_cost
			FROM usage_group_daily_rollups rollup
			WHERE $4::boolean
				AND rollup.bucket_date >= $5::date
				AND rollup.bucket_date < $6::date
			GROUP BY rollup.group_id
		),
		tail AS (
			SELECT
				ul.group_id,
				COALESCE(SUM(ul.actual_cost), 0) AS actual_cost,
				COALESCE(SUM(ul.actual_cost) FILTER (WHERE ul.created_at >= $1), 0) AS today_cost,
				COALESCE(SUM(ul.actual_cost) FILTER (
					WHERE ul.created_at >= $2
						AND ul.created_at < $1
				), 0) AS yesterday_cost
			FROM usage_logs ul
			WHERE ul.created_at >= $7
			GROUP BY ul.group_id
		)
		SELECT
			g.id AS group_id,
			COALESCE(historical.actual_cost, 0) + COALESCE(tail.actual_cost, 0) AS total_cost,
			COALESCE(tail.today_cost, 0) AS today_cost,
			COALESCE(historical.yesterday_cost, 0) + COALESCE(tail.yesterday_cost, 0) AS yesterday_cost
		FROM groups g
		LEFT JOIN historical ON historical.group_id = g.id
		LEFT JOIN tail ON tail.group_id = g.id
		ORDER BY g.id
	`

	args := []any{
		todayStart,
		yesterdayStart,
		yesterdayDate,
		state.valid,
		state.retainedDate,
		state.closedBefore,
		state.tailStart,
	}
	if ids != nil {
		query = strings.Replace(query, "WHERE $4::boolean", "WHERE $4::boolean AND rollup.group_id = ANY($8::bigint[])", 1)
		query = strings.Replace(query, "WHERE ul.created_at >= $7", "WHERE ul.created_at >= $7 AND ul.group_id = ANY($8::bigint[])", 1)
		query = strings.Replace(query, "ORDER BY g.id", "WHERE g.id = ANY($8::bigint[]) ORDER BY g.id", 1)
		args = append(args, pq.Array(ids))
	}
	rows, err := sqlq.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
			results = nil
		}
	}()

	results = make([]usagestats.GroupUsageSummary, 0)
	for rows.Next() {
		var row usagestats.GroupUsageSummary
		if err := rows.Scan(&row.GroupID, &row.TotalCost, &row.TodayCost, &row.YesterdayCost); err != nil {
			return nil, err
		}
		results = append(results, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return results, nil
}

// SyncGroupUsageRollups publishes one day at a time without holding a state lock during scans.
func (r *dashboardAggregationRepository) SyncGroupUsageRollups(ctx context.Context, todayStart time.Time) error {
	if r == nil || r.sql == nil {
		return nil
	}
	db, ok := r.sql.(*sql.DB)
	if !ok {
		return fmt.Errorf("daily rollup builder requires a database, not a caller transaction")
	}
	if err := syncUsageDailyRollups(ctx, db, "group", todayStart); err != nil {
		return err
	}
	return syncUsageDailyRollups(ctx, db, "subject", todayStart)
}

func lockGroupUsageRollupState(ctx context.Context, tx *sql.Tx) error {
	var id int16
	if err := tx.QueryRowContext(ctx, `
		SELECT id
		FROM usage_group_rollup_state
		WHERE id = 1
		FOR UPDATE
	`).Scan(&id); err != nil {
		return fmt.Errorf("锁定分组用量汇总水位: %w", err)
	}
	return nil
}

// Both state rows are invalidated in group-before-subject order, including
// explicit partition maintenance which does not run the usage row triggers.
func invalidateGroupUsageRollupsAt(ctx context.Context, q sqlExecutor, affectedAt time.Time) error {
	_, err := q.ExecContext(ctx, "SELECT invalidate_group_usage_rollups_at($1, $2)", affectedAt.UTC(), service.GroupUsageTimezoneName())
	return err
}

// Retention is an exact instant, not a rounded day. The database fences both
// builders and invalidates the boundary day before old source rows are removed.
func expireGroupUsageRollupsBefore(ctx context.Context, q sqlExecutor, cutoff time.Time) error {
	_, err := q.ExecContext(ctx, "SELECT expire_usage_rollups_before($1, $2)", cutoff.UTC(), service.GroupUsageTimezoneName())
	return err
}
