//go:build integration

package repository

import (
	"context"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestSubjectDashboardQueryPlan(t *testing.T) {
	repo := newDashboardHistoryFixture(t)
	ctx := context.Background()
	today := service.GroupUsageTodayStart(time.Now())
	var closed, tz string
	var retained time.Time
	require.NoError(t, scanSingleRow(ctx, repo.sql, `SELECT closed_before::text,retained_from,timezone_name FROM usage_subject_rollup_state WHERE id=1`, nil, &closed, &retained, &tz))
	tail, err := subjectRollupTailStart(closed, tz, today)
	require.NoError(t, err)
	var source, buckets, tailRows int64
	require.NoError(t, scanSingleRow(ctx, repo.sql, `SELECT COUNT(*) FROM usage_logs WHERE user_id=1`, nil, &source))
	require.NoError(t, scanSingleRow(ctx, repo.sql, `SELECT COUNT(*) FROM usage_subject_daily_rollups WHERE user_id=1`, nil, &buckets))
	require.NoError(t, scanSingleRow(ctx, repo.sql, `SELECT COUNT(*) FROM usage_logs WHERE user_id=1 AND created_at >= $1`, []any{tail}, &tailRows))
	require.EqualValues(t, 100000, source)
	require.EqualValues(t, 120, buckets)
	require.Zero(t, tailRows)
	t.Logf("source rows=%d; published user day buckets=%d; tail rows=%d; raw tail starts=%s", source, buckets, tailRows, tail.Format(time.RFC3339))
	for _, query := range []struct {
		label, sql string
		args       []any
	}{
		{"raw history aggregate", `SELECT COUNT(*),SUM(input_tokens),SUM(output_tokens),SUM(actual_cost),AVG(duration_ms) FROM usage_logs WHERE user_id=$1`, []any{int64(1)}},
		{"published day buckets and bounded raw tail", userSubjectDashboardQuery, []any{int64(1), service.GroupUsageDate(retained), closed, tail.UTC(), today.UTC()}},
	} {
		t.Log(query.label)
		rows, err := repo.sql.QueryContext(ctx, "EXPLAIN (ANALYZE,BUFFERS) "+query.sql, query.args...)
		require.NoError(t, err)
		for rows.Next() {
			var line string
			require.NoError(t, rows.Scan(&line))
			t.Log(line)
		}
		require.NoError(t, rows.Err())
		require.NoError(t, rows.Close())
	}
}
