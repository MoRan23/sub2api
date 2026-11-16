//go:build integration

package repository

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/lib/pq"
)

// The same fixture runs on the pre-rollup baseline and the optimized checkout.
// It exercises a cold repository read, without the service's 10-second cache.
func newDashboardHistoryFixture(b testing.TB) *usageLogRepository {
	ctx := context.Background()
	schema := fmt.Sprintf("stats_bench_%d", time.Now().UnixNano())
	if _, err := integrationDB.ExecContext(ctx, "CREATE SCHEMA "+pq.QuoteIdentifier(schema)); err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DROP SCHEMA "+pq.QuoteIdentifier(schema)+" CASCADE")
	})
	parsed, err := url.Parse(integrationDSN)
	if err != nil {
		b.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	db, err := sql.Open("postgres", parsed.String())
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = db.Close() })
	ddl := `CREATE TABLE users(id BIGINT PRIMARY KEY);
 CREATE TABLE groups(id BIGINT PRIMARY KEY,platform TEXT);
 CREATE TABLE accounts(id BIGINT PRIMARY KEY,platform TEXT);
 CREATE TABLE api_keys(id BIGINT PRIMARY KEY,user_id BIGINT,status TEXT,deleted_at TIMESTAMPTZ);
 CREATE TABLE usage_logs(id BIGINT PRIMARY KEY,user_id BIGINT,api_key_id BIGINT,group_id BIGINT,account_id BIGINT,
 input_tokens BIGINT NOT NULL,output_tokens BIGINT NOT NULL,cache_creation_tokens BIGINT NOT NULL,cache_read_tokens BIGINT NOT NULL,
 total_cost NUMERIC(20,10) NOT NULL,actual_cost NUMERIC(20,10) NOT NULL,duration_ms BIGINT,grok_safety_rejection JSONB,created_at TIMESTAMPTZ NOT NULL);
 CREATE INDEX usage_created ON usage_logs(created_at);
 CREATE INDEX usage_user_created ON usage_logs(user_id,created_at);
 CREATE INDEX usage_key_created ON usage_logs(api_key_id,created_at);
 INSERT INTO users SELECT generate_series(1,10);
 INSERT INTO groups VALUES(1,'openai'); INSERT INTO accounts VALUES(1,'openai');
 INSERT INTO api_keys SELECT id,id,'active',NULL FROM users;`
	if _, err = db.ExecContext(ctx, ddl); err != nil {
		b.Fatal(err)
	}
	for i, name := range []string{"222_group_usage_daily_rollups.sql", "223_group_usage_rollup_timezone.sql", "290_usage_rollup_fenced_builds.sql", "291_usage_subject_daily_rollups.sql"} {
		data, e := migrations.FS.ReadFile(name)
		if e != nil {
			if i >= 2 {
				continue
			}
			b.Fatal(e)
		}
		if _, e = db.ExecContext(ctx, string(data)); e != nil {
			b.Fatal(e)
		}
	}
	today := service.GroupUsageTodayStart(time.Now())
	_, err = db.ExecContext(ctx, `INSERT INTO usage_logs SELECT n,(n%10)+1,(n%10)+1,1,1,1000,200,50,750,0.05,0.025,1500,NULL,
 $1::timestamptz-(((n/10)%120)+1)*INTERVAL '1 day'+(n%3600)*INTERVAL '1 second' FROM generate_series(1,1000000) n`, today)
	if err != nil {
		b.Fatal(err)
	}
	if err = newDashboardAggregationRepositoryWithSQL(db).SyncGroupUsageRollups(ctx, today); err != nil {
		b.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, "ANALYZE"); err != nil {
		b.Fatal(err)
	}
	return newUsageLogRepositoryWithSQL(nil, db)
}

func BenchmarkUserDashboardHistory(b *testing.B) {
	repo := newDashboardHistoryFixture(b)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		got, e := repo.GetUserDashboardStats(ctx, 1)
		if e != nil {
			b.Fatal(e)
		}
		if got.TotalRequests != 100000 {
			b.Fatalf("requests=%d", got.TotalRequests)
		}
	}
	b.StopTimer()
}
