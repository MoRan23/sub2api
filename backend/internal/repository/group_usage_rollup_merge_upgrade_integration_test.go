//go:build integration

package repository

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

func TestGroupUsageRollupMergeMigrationRepairsUpgradeWindow(t *testing.T) {
	useGroupUsageRepositoryTestTimezone(t, "Asia/Shanghai")
	for _, published := range []bool{false, true} {
		name := "old_build_in_progress"
		if published {
			name = "old_build_already_published"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			schema := createGroupUsageRollupSchemaWithMigrations(t, ctx, false, []string{
				"222_group_usage_daily_rollups.sql", "223_group_usage_rollup_timezone.sql",
				"290_usage_rollup_fenced_builds.sql", "291_usage_subject_daily_rollups.sql",
			})
			db := openGroupUsageRollupSchemaDB(t, schema, "Asia/Shanghai")
			execGroupUsageRollupSQL(t, ctx, db, `
				INSERT INTO groups(id) VALUES(10); INSERT INTO users(id) VALUES(1);
				INSERT INTO usage_logs(id,user_id,group_id,api_key_id,account_id,actual_cost,input_tokens,created_at) VALUES
					(1,1,10,20,30,3,30,TIMESTAMPTZ '2026-08-11 16:00:00+08'),
					(2,1,10,20,30,5,50,TIMESTAMPTZ '2026-08-12 12:00:00+08'),
					(3,1,10,20,30,7,70,TIMESTAMPTZ '2026-08-13 12:00:00+08'),
					(4,1,10,20,30,11,110,TIMESTAMPTZ '2026-08-14 12:00:00+08');
				INSERT INTO usage_group_daily_rollups(bucket_date,group_id,actual_cost) VALUES
					('2026-08-11',10,3),('2026-08-12',10,5),('2026-08-13',10,7);
				INSERT INTO usage_subject_daily_rollups(bucket_date,user_id,api_key_id,group_id,account_id,request_count,input_tokens,actual_cost) VALUES
					('2026-08-11',1,20,10,30,1,30,3),('2026-08-12',1,20,10,30,1,50,5),('2026-08-13',1,20,10,30,1,70,7);
				UPDATE usage_group_rollup_state SET closed_before='2026-08-12', retained_from=TIMESTAMPTZ '2026-08-10 10:00:00+08',
					timezone_name='Asia/Shanghai', building_before='2026-08-13', build_token='00000000-0000-4000-8000-000000000001';
				UPDATE usage_subject_rollup_state SET closed_before='2026-08-12', retained_from=TIMESTAMPTZ '2026-08-10 10:00:00+08',
					timezone_name='Asia/Shanghai', building_before='2026-08-13', build_token='00000000-0000-4000-8000-000000000002';
				INSERT INTO usage_group_rollup_stage(build_token,bucket_date,group_id,actual_cost)
					VALUES('00000000-0000-4000-8000-000000000001','2026-08-12',10,5);
				INSERT INTO usage_subject_rollup_stage(build_token,bucket_date,user_id,api_key_id,group_id,account_id,request_count,input_tokens,actual_cost)
					VALUES('00000000-0000-4000-8000-000000000002','2026-08-12',1,20,10,30,1,50,5);
			`)
			applyGroupUsageRollupTestMigration(t, ctx, db, "251_group_usage_rollup_nonblocking_build.sql")
			execGroupUsageRollupSQL(t, ctx, db, `UPDATE usage_group_rollup_state SET expired_before=TIMESTAMPTZ '2026-08-11 12:00:00+08'`)
			var oldRevision int64
			require.NoError(t, db.QueryRowContext(ctx, `SELECT revision FROM usage_group_rollup_state WHERE id=1`).Scan(&oldRevision))
			// Migration 251 does not recognize the old local building_before marker:
			// a write after the old scan does not fence this group publication.
			execGroupUsageRollupSQL(t, ctx, db, `INSERT INTO usage_logs(id,user_id,group_id,api_key_id,account_id,actual_cost,input_tokens,created_at)
				VALUES(5,1,10,20,30,13,130,TIMESTAMPTZ '2026-08-12 18:00:00+08')`)
			var afterWriteRevision int64
			require.NoError(t, db.QueryRowContext(ctx, `SELECT revision FROM usage_group_rollup_state WHERE id=1`).Scan(&afterWriteRevision))
			require.Equal(t, oldRevision, afterWriteRevision, "fixture must exercise the upgrade invalidation gap")
			if published {
				// Model an old instance publishing its already computed stale rows
				// before the final compatibility migration runs.
				execGroupUsageRollupSQL(t, ctx, db, `
					UPDATE usage_group_rollup_state SET closed_before='2026-08-14',build_token=NULL,building_before=NULL;
					UPDATE usage_subject_rollup_state SET closed_before='2026-08-14',build_token=NULL,building_before=NULL;`)
			}
			sourceBefore := groupUsageRollupSourceSnapshot(t, ctx, db)
			applyGroupUsageRollupTestMigration(t, ctx, db, "305_merge_v022_usage_rollup_compat.sql")
			require.Equal(t, sourceBefore, groupUsageRollupSourceSnapshot(t, ctx, db))
			var groupClosed, subjectClosed string
			var groupToken, subjectToken, groupBuilding, subjectBuilding, upstreamBuilding, upstreamDirty sql.NullString
			var groupRevision, subjectRevision int64
			require.NoError(t, db.QueryRowContext(ctx, `SELECT closed_before::text,build_token::text,building_before::text,
				building_until::text,build_dirty_from::text,revision FROM usage_group_rollup_state WHERE id=1`).Scan(
				&groupClosed, &groupToken, &groupBuilding, &upstreamBuilding, &upstreamDirty, &groupRevision))
			require.NoError(t, db.QueryRowContext(ctx, `SELECT closed_before::text,build_token::text,building_before::text,revision
				FROM usage_subject_rollup_state WHERE id=1`).Scan(&subjectClosed, &subjectToken, &subjectBuilding, &subjectRevision))
			require.Equal(t, "2026-08-11", groupClosed, "group rebuild starts at the exact retention floor's civil date")
			require.Equal(t, "2026-08-10", subjectClosed, "subject rebuild also repairs published retained history")
			for _, marker := range []sql.NullString{groupToken, subjectToken, groupBuilding, subjectBuilding, upstreamBuilding, upstreamDirty} {
				require.False(t, marker.Valid)
			}
			// Even with a matching revision supplied, the old token cannot publish.
			for _, table := range []string{"usage_group_rollup_state", "usage_subject_rollup_state"} {
				result, err := db.ExecContext(ctx, "UPDATE "+table+" SET closed_before='2026-08-14' WHERE build_token IN ('00000000-0000-4000-8000-000000000001'::uuid,'00000000-0000-4000-8000-000000000002'::uuid)")
				require.NoError(t, err)
				rows, err := result.RowsAffected()
				require.NoError(t, err)
				require.Zero(t, rows)
			}
			applyGroupUsageRollupTestMigration(t, ctx, db, "305_merge_v022_usage_rollup_compat.sql")
			var afterGroup, afterSubject int64
			require.NoError(t, db.QueryRowContext(ctx, `SELECT g.revision,s.revision FROM usage_group_rollup_state g CROSS JOIN usage_subject_rollup_state s`).Scan(&afterGroup, &afterSubject))
			require.Equal(t, groupRevision, afterGroup, "repeating the migration must not refence an unchanged state")
			require.Equal(t, subjectRevision, afterSubject)
			today := time.Date(2026, 8, 13, 16, 0, 0, 0, time.UTC)
			require.NoError(t, syncUsageDailyRollups(ctx, db, "group", today))
			require.NoError(t, syncUsageDailyRollups(ctx, db, "subject", today))
			var groupCost, subjectCost float64
			var requests, inputTokens int64
			require.NoError(t, db.QueryRowContext(ctx, `SELECT SUM(actual_cost)::float8 FROM usage_group_daily_rollups`).Scan(&groupCost))
			require.NoError(t, db.QueryRowContext(ctx, `SELECT SUM(actual_cost)::float8,SUM(request_count),SUM(input_tokens) FROM usage_subject_daily_rollups`).Scan(&subjectCost, &requests, &inputTokens))
			require.InDelta(t, 28, groupCost, 0.0000001)
			require.InDelta(t, groupCost, subjectCost, 0.0000001)
			require.Equal(t, int64(4), requests)
			require.Equal(t, int64(280), inputTokens)
			require.Equal(t, [3]float64{39, 11, 7}, groupUsageSummaryByGroup(t, ctx, db, today)[10])
			require.Equal(t, sourceBefore, groupUsageRollupSourceSnapshot(t, ctx, db))
		})
	}
}

func applyGroupUsageRollupTestMigration(t *testing.T, ctx context.Context, db *sql.DB, name string) {
	t.Helper()
	body, err := migrations.FS.ReadFile(name)
	require.NoError(t, err)
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, string(body))
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
}

func groupUsageRollupSourceSnapshot(t *testing.T, ctx context.Context, db *sql.DB) string {
	t.Helper()
	var snapshot string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT jsonb_agg(to_jsonb(l) ORDER BY id)::text FROM usage_logs l`).Scan(&snapshot))
	return snapshot
}
