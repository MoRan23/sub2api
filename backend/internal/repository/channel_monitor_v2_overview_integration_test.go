//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	dbmigrations "github.com/Wei-Shaw/sub2api/migrations"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

// These fixtures exercise the production aggregation SQL, including joins to
// final ops outcomes. No model traffic is sent by these tests.
type monitorOverviewFixture struct {
	group, user, account, key, otherKey int64
	at                                  time.Time
}

func newMonitorOverviewFixture(t *testing.T, platform string) monitorOverviewFixture {
	t.Helper()
	client := testEntClient(t)
	u := mustCreateUser(t, client, &service.User{Email: uuid.NewString() + "@monitor.test"})
	g := mustCreateGroup(t, client, &service.Group{Name: "overview-" + uuid.NewString(), Platform: platform})
	a := mustCreateAccount(t, client, &service.Account{Name: "overview-" + uuid.NewString(), Platform: "openai"})
	k := mustCreateApiKey(t, client, &service.APIKey{UserID: u.ID, GroupID: &g.ID})
	k2 := mustCreateApiKey(t, client, &service.APIKey{UserID: u.ID, GroupID: &g.ID})
	return monitorOverviewFixture{group: g.ID, user: u.ID, account: a.ID, key: k.ID, otherKey: k2.ID, at: time.Now().UTC().Truncate(5 * time.Minute).Add(-20 * time.Minute)}
}

func (f monitorOverviewFixture) usage(t *testing.T, key int64, model, requestID string, firstToken int) {
	t.Helper()
	_, err := integrationDB.ExecContext(context.Background(), `INSERT INTO usage_logs
	 (user_id,api_key_id,account_id,group_id,request_id,model,input_tokens,output_tokens,
	 cache_creation_tokens,cache_read_tokens,first_token_ms,duration_ms,actual_cost,created_at)
	 VALUES($1,$2,$3,$4,$5,$6,100,20,50,150,$7,1000,0,$8)`, f.user, key, f.account, f.group, requestID, model, firstToken, f.at.Add(10*time.Second))
	require.NoError(t, err)
}

func (f monitorOverviewFixture) failure(t *testing.T, key int64, model, requestID, clientID, owner, phase, kind, message string, status int, offset time.Duration) {
	t.Helper()
	_, err := integrationDB.ExecContext(context.Background(), `INSERT INTO ops_error_logs
	 (user_id,api_key_id,account_id,group_id,request_id,client_request_id,platform,model,
	 error_owner,error_phase,error_type,error_message,status_code,created_at)
	 VALUES($1,$2,$3,$4,$5,NULLIF($6,''),'openai',$7,$8,$9,$10,$11,$12,$13)`,
		f.user, key, f.account, f.group, requestID, clientID, model, owner, phase, kind, message, status, f.at.Add(offset))
	require.NoError(t, err)
}

func TestChannelMonitorV2OverviewAggregationOutcomesIntegration(t *testing.T) {
	ctx := context.Background()
	f := newMonitorOverviewFixture(t, "openai")
	f.usage(t, f.key, "free-success", "local:free-success", 100)
	f.usage(t, f.key, "partial-failure", "client:partial-client", 50000)
	f.failure(t, f.key, "partial-failure", "server-partial", "partial-client", "provider", "upstream", "upstream_error", "upstream timeout", 504, 20*time.Second)
	f.usage(t, f.key, "recovered", "local:recovered", 250)
	f.failure(t, f.key, "recovered", "recovered", "", "provider", "upstream", "upstream_error", "rate limit", 429, 15*time.Second)
	f.failure(t, f.key, "recovered", "recovered", "", "provider", "upstream", "upstream_error", "recovered after retry", 200, 20*time.Second)
	// A client ID is not globally unique, even within the same group and user.
	f.usage(t, f.key, "key-isolation", "client:shared", 100)
	f.usage(t, f.otherKey, "key-isolation", "client:shared", 500)
	f.failure(t, f.key, "key-isolation", "server-shared", "shared", "provider", "upstream", "upstream_error", "upstream failure", 502, 20*time.Second)
	f.usage(t, f.key, "both-success", "client:both-shared", 100)
	f.usage(t, f.otherKey, "both-success", "client:both-shared", 500)
	f.usage(t, f.key, "namespace-isolation", "client:local:scope", 100)
	f.usage(t, f.key, "namespace-isolation", "local:scope", 500)
	f.failure(t, f.key, "namespace-isolation", "scope", "", "provider", "upstream", "upstream_error", "upstream failure", 502, 20*time.Second)
	f.usage(t, f.key, "raw-client-prefix", "client:local:raw-client", 100)
	f.failure(t, f.key, "raw-client-prefix", "server-prefix", "local:raw-client", "provider", "upstream", "upstream_error", "upstream failure", 502, 20*time.Second)

	r := &channelMonitorV2Repository{db: integrationDB}
	for range 2 {
		require.NoError(t, r.RecomputeRange(ctx, f.at, f.at.Add(time.Minute)))
	}
	cases := []struct {
		model                        string
		success, errors, input, ttft int64
	}{
		{"free-success", 1, 0, 100, 1}, {"partial-failure", 0, 1, 0, 0},
		{"recovered", 1, 0, 100, 1}, {"key-isolation", 1, 1, 100, 1},
		{"both-success", 2, 0, 200, 2},
		{"namespace-isolation", 1, 1, 100, 1},
		{"raw-client-prefix", 0, 1, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			var success, errors, input, ttft, version int64
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT SUM(success_requests),SUM(availability_error_requests),SUM(input_tokens),SUM(ttft_count),MIN(aggregation_version) FROM channel_monitor_v2_metrics_1m WHERE group_id=$1 AND model=$2`, f.group, tc.model).Scan(&success, &errors, &input, &ttft, &version))
			require.Equal(t, tc.success, success)
			require.Equal(t, tc.errors, errors)
			require.Equal(t, tc.input, input)
			require.Equal(t, tc.ttft, ttft)
			require.EqualValues(t, 2, version)
			var rollSuccess, rollErrors int64
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT SUM(success_requests),SUM(availability_error_requests) FROM channel_monitor_v2_metrics_rollup WHERE group_id=$1 AND model=$2 AND bucket_seconds=300`, f.group, tc.model).Scan(&rollSuccess, &rollErrors))
			require.Equal(t, success, rollSuccess)
			require.Equal(t, errors, rollErrors)
		})
	}
	var failedHist int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COALESCE(SUM(sample_count),0) FROM channel_monitor_v2_latency_histograms_1m WHERE group_id=$1 AND model='partial-failure'`, f.group).Scan(&failedHist))
	require.Zero(t, failedHist, "failed partial usage must not contaminate latency histograms")
}

func TestChannelMonitorV2OverviewAvailabilityErrorClassificationIntegration(t *testing.T) {
	f := newMonitorOverviewFixture(t, "openai")
	cases := []struct {
		model, owner, phase, kind, message string
		status                             int
		count                              int64
	}{
		{"user-auth", "client", "auth", "auth_error", "invalid api key", 401, 0},
		{"user-balance", "client", "auth", "billing_error", "insufficient balance", 402, 0},
		{"user-params", "client", "request", "invalid_request", "invalid request", 400, 0},
		{"user-permission", "client", "auth", "permission_denied", "group not allowed", 403, 0},
		{"user-safety", "provider", "upstream", "upstream_error", "safety_review_rejected", 403, 0},
		{"user-cancel", "client", "stream", "client_cancelled", "client disconnected", 499, 0},
		{"user-rpm", "client", "request", "rate_limit", "user RPM exceeded", 429, 0},
		{"routing-capacity", "gateway", "scheduling", "scheduling_error", "rate capacity exceeded", 429, 1},
		{"provider-auth", "provider", "upstream", "upstream_error", "invalid api key", 401, 1},
		{"provider-quota", "provider", "upstream", "upstream_error", "insufficient quota", 429, 1},
		{"pool-empty", "gateway", "scheduling", "scheduling_error", "No available accounts in this group", 503, 1},
		{"provider-rate", "provider", "upstream", "upstream_error", "rate limit exceeded", 429, 1},
		{"provider-timeout", "provider", "upstream", "upstream_error", "context deadline exceeded", 504, 1},
		{"transport", "gateway", "upstream", "network_error", "connection reset by peer", 502, 1},
	}
	for _, tc := range cases {
		f.failure(t, f.key, tc.model, uuid.NewString(), "", tc.owner, tc.phase, tc.kind, tc.message, tc.status, 20*time.Second)
	}
	require.NoError(t, (&channelMonitorV2Repository{db: integrationDB}).RecomputeRange(context.Background(), f.at, f.at.Add(time.Minute)))
	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			var all, availability int64
			require.NoError(t, integrationDB.QueryRow(`SELECT SUM(error_requests),SUM(availability_error_requests) FROM channel_monitor_v2_metrics_1m WHERE group_id=$1 AND model=$2`, f.group, tc.model).Scan(&all, &availability))
			require.EqualValues(t, 1, all, "legacy error counts remain available")
			require.Equal(t, tc.count, availability)
		})
	}
}

func TestChannelMonitorV2OverviewCompositeHistogramAndScopeIntegration(t *testing.T) {
	ctx := context.Background()
	f := newMonitorOverviewFixture(t, "composite")
	f.usage(t, f.key, "openai-model", "local:composite-fast", 100)
	second := mustCreateAccount(t, testEntClient(t), &service.Account{Name: "overview-anthropic", Platform: "anthropic"})
	other := f
	other.account = second.ID
	other.usage(t, f.key, "anthropic-model", "local:composite-slow-1", 9000)
	other.usage(t, f.key, "anthropic-model", "local:composite-slow-2", 9000)
	_, err := integrationDB.ExecContext(ctx, `INSERT INTO ops_error_logs(user_id,api_key_id,group_id,request_id,model,platform,error_owner,error_phase,error_type,error_message,status_code,created_at) VALUES($1,$2,$3,'composite-pool-empty','openai-model','composite','gateway','scheduling','scheduling_error','No available accounts in this group',503,$4)`, f.user, f.key, f.group, f.at.Add(30*time.Second))
	require.NoError(t, err)
	hidden := newMonitorOverviewFixture(t, "openai")
	hidden.usage(t, hidden.key, "hidden-model", "local:hidden", 100)
	r := &channelMonitorV2Repository{db: integrationDB}
	require.NoError(t, r.RecomputeRange(ctx, f.at, f.at.Add(5*time.Minute)))
	cfg, err := r.GetConfig(ctx)
	require.NoError(t, err)
	filter := service.ChannelMonitorV2Filter{Start: f.at, End: f.at.Add(5 * time.Minute), Bucket: 5 * time.Minute, Range: "90m", AllowedGroupIDs: []int64{f.group}, RestrictGroups: true}
	got, err := r.GetMatrix(ctx, filter, *cfg, service.ChannelMonitorV2GroupByGroup, false)
	require.NoError(t, err)
	require.Len(t, got.Items, 1)
	row := got.Items[0]
	require.NotNil(t, row.GroupID)
	require.Equal(t, f.group, *row.GroupID)
	require.EqualValues(t, 3, row.Metrics.SuccessRequests)
	require.EqualValues(t, 3, row.Metrics.TTFT.SampleCount)
	require.NotNil(t, row.Metrics.TTFT.P50Ms)
	require.EqualValues(t, 10000, *row.Metrics.TTFT.P50Ms, "merge histogram counts, never average platform percentiles")
	require.InDelta(t, .5, row.Metrics.CacheRate, 1e-9)
	require.NotNil(t, row.Metrics.AvailabilityRate)
	require.EqualValues(t, 1, row.Metrics.AvailabilityErrorRequests)
	require.Equal(t, .75, *row.Metrics.AvailabilityRate)
	filter.AllowedGroupIDs = nil
	empty, err := r.GetMatrix(ctx, filter, *cfg, service.ChannelMonitorV2GroupByGroup, false)
	require.NoError(t, err)
	require.Empty(t, empty.Items)
}

func TestChannelMonitorV2OverviewRecoveredAcrossChunksIntegration(t *testing.T) {
	f := newMonitorOverviewFixture(t, "openai")
	f.failure(t, f.key, "cross-chunk", "cross-chunk", "", "provider", "upstream", "upstream_error", "upstream overload", 503, 59*time.Second)
	f.failure(t, f.key, "cross-chunk", "cross-chunk", "", "provider", "upstream", "upstream_error", "recovered", 200, 61*time.Second)
	later := f
	later.at = f.at.Add(time.Minute)
	later.usage(t, f.key, "cross-chunk", "local:cross-chunk", 100)
	repo := &channelMonitorV2Repository{db: integrationDB}
	require.NoError(t, repo.RecomputeRange(context.Background(), f.at, f.at.Add(time.Minute)))
	require.NoError(t, repo.RecomputeRange(context.Background(), f.at.Add(time.Minute), f.at.Add(2*time.Minute)))
	var success, failures int64
	require.NoError(t, integrationDB.QueryRow(`SELECT COALESCE(SUM(success_requests),0),COALESCE(SUM(availability_error_requests),0) FROM channel_monitor_v2_metrics_1m WHERE group_id=$1 AND model='cross-chunk'`, f.group).Scan(&success, &failures))
	require.EqualValues(t, 1, success)
	require.Zero(t, failures)
}

func TestChannelMonitorV2OverviewAttributionHistoryIntegration(t *testing.T) {
	ctx := context.Background()
	f := newMonitorOverviewFixture(t, "composite")
	client := testEntClient(t)
	second := mustCreateAccount(t, client, &service.Account{Name: "overview-second"})
	deleted := mustCreateAccount(t, client, &service.Account{Name: "overview-deleted"})
	otherGroup := mustCreateGroup(t, client, &service.Group{Name: "overview-hidden"})
	for _, id := range []int64{f.account, second.ID, deleted.ID} {
		_, err := integrationDB.ExecContext(ctx, `INSERT INTO account_groups(account_id,group_id,priority) VALUES($1,$2,50)`, id, f.group)
		require.NoError(t, err)
	}
	_, err := integrationDB.ExecContext(ctx, `INSERT INTO account_groups(account_id,group_id,priority) VALUES($1,$2,50)`, second.ID, otherGroup.ID)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE accounts SET deleted_at=NOW() WHERE id=$1`, deleted.ID)
	require.NoError(t, err)
	// A large private snapshot proves the query projects only public scalar fields.
	insert := func(account int64, index int, status string, finished *time.Time) {
		snapshot := fmt.Sprintf(`{"policy":{"model":"probe-%02d"},"private":"secret-credentials-and-answer"}`, index)
		_, err := integrationDB.ExecContext(ctx, `INSERT INTO model_attribution_jobs(account_id,account_name,source,status,snapshot,authorization_digest,fence_digest,reason,finished_at) VALUES($1,'private-account','manual',$2,$3,'secret-auth','secret-fence','private failure text',$4)`, account, status, snapshot, finished)
		require.NoError(t, err)
	}
	for i := 0; i < 28; i++ {
		at := f.at.Add(time.Duration(i) * time.Second)
		account := f.account
		if i%2 == 1 {
			account = second.ID
		}
		status := "passed"
		if i == 27 {
			status = "abnormal"
		}
		insert(account, i, status, &at)
	}
	at := f.at.Add(time.Hour)
	insert(deleted.ID, 90, "passed", &at)
	insert(f.account, 91, "running", nil)
	repo := &channelMonitorV2Repository{db: integrationDB}
	history, err := repo.GetGroupAttributionHistory(ctx, []int64{f.group})
	require.NoError(t, err)
	require.Len(t, history, 1)
	require.Len(t, history[f.group], 20)
	require.Equal(t, "probe-27", history[f.group][0].ProbeModel)
	require.Equal(t, "abnormal", history[f.group][0].Status)
	require.Equal(t, "probe-08", history[f.group][19].ProbeModel)
	encoded, err := json.Marshal(history)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "private")
	require.NotContains(t, string(encoded), "secret")
	for i := 1; i < len(history[f.group]); i++ {
		require.False(t, history[f.group][i].FinishedAt.After(history[f.group][i-1].FinishedAt))
	}
	empty, err := repo.GetGroupAttributionHistory(ctx, nil)
	require.NoError(t, err)
	require.Empty(t, empty)
	// Membership changes apply to the next read; snapshots never grant visibility.
	_, err = integrationDB.ExecContext(ctx, `DELETE FROM account_groups WHERE account_id=$1 AND group_id=$2`, second.ID, f.group)
	require.NoError(t, err)
	history, err = repo.GetGroupAttributionHistory(ctx, []int64{f.group})
	require.NoError(t, err)
	require.Len(t, history[f.group], 14)
	require.Equal(t, "probe-26", history[f.group][0].ProbeModel)
	// Old history grows independently of the response bound. Sample the real
	// query plan rather than running a second implementation of the query.
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO model_attribution_jobs(account_id,account_name,source,status,snapshot,authorization_digest,fence_digest,finished_at)
	 SELECT $1,'private-account','manual','passed','{"policy":{"model":"older"},"private":"never-expose"}','secret','secret',$2::timestamptz - n * INTERVAL '1 minute' FROM generate_series(1,1000) n`, f.account, f.at)
	require.NoError(t, err)
	groupIDs := []int64{f.group, otherGroup.ID}
	for range 48 {
		group := mustCreateGroup(t, client, &service.Group{Name: "overview-shared-" + uuid.NewString()})
		_, err := integrationDB.ExecContext(ctx, `INSERT INTO account_groups(account_id,group_id,priority) VALUES($1,$3,50),($2,$3,50)`, f.account, second.ID, group.ID)
		require.NoError(t, err)
		groupIDs = append(groupIDs, group.ID)
	}
	history, err = repo.GetGroupAttributionHistory(ctx, groupIDs)
	require.NoError(t, err)
	require.Len(t, history[f.group], 20)
	require.LessOrEqual(t, len(history[otherGroup.ID]), 20)
	encoded, err = json.Marshal(history)
	require.NoError(t, err)
	require.Len(t, history, 50)
	for _, entries := range history {
		require.LessOrEqual(t, len(entries), 20)
	}
	require.Less(t, len(encoded), 200000)
	var planJSON []byte
	require.NoError(t, integrationDB.QueryRowContext(ctx, "EXPLAIN (ANALYZE,BUFFERS,FORMAT JSON) "+channelMonitorV2GroupAttributionSQL, pq.Array(groupIDs)).Scan(&planJSON))
	var plan []struct {
		Plan struct {
			ActualRows float64 `json:"Actual Rows"`
		}
		ExecutionTime float64 `json:"Execution Time"`
	}
	require.NoError(t, json.Unmarshal(planJSON, &plan))
	require.Len(t, plan, 1)
	require.LessOrEqual(t, plan[0].Plan.ActualRows, 1000.0)
	t.Logf("attribution query: 1 batch for 50 groups, 1028 stored completed rows, %.0f returned rows, %d public JSON bytes, %.3fms", plan[0].Plan.ActualRows, len(encoded), plan[0].ExecutionTime)
}

func TestChannelMonitorV2OverviewMigrationUpgradeIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	container, err := tcpostgres.Run(ctx, selectDockerImage(ctx, postgresImageTag), tcpostgres.WithDatabase("overview_upgrade"), tcpostgres.WithUsername("postgres"), tcpostgres.WithPassword("postgres"), tcpostgres.BasicWaitStrategies())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, container.Terminate(context.Background())) })
	dsn, err := container.ConnectionString(ctx, "sslmode=disable", "TimeZone=UTC")
	require.NoError(t, err)
	db, err := openSQLWithRetry(ctx, dsn, 30*time.Second)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	baseline := fstest.MapFS{}
	files, err := fs.Glob(dbmigrations.FS, "*.sql")
	require.NoError(t, err)
	for _, name := range files {
		if name == "285_channel_monitor_v2_availability.sql" || name == "287_channel_monitor_v2_request_identity_notx.sql" || name == "286_channel_monitor_v2_attribution_history_notx.sql" {
			continue
		}
		body, err := dbmigrations.FS.ReadFile(name)
		require.NoError(t, err)
		baseline[name] = &fstest.MapFile{Data: body}
	}
	require.NoError(t, applyMigrationsFS(ctx, db, baseline))
	before := time.Now().UTC().Truncate(time.Minute).Add(-48 * time.Hour)
	_, err = db.ExecContext(ctx, `INSERT INTO channel_monitor_v2_metrics_1m(bucket_start,platform,group_id,model,success_requests) VALUES($1,'openai',0,'legacy',7)`, before)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO channel_monitor_v2_metrics_rollup(bucket_start,bucket_seconds,platform,group_id,model,success_requests) VALUES(date_trunc('day',$1::timestamptz),86400,'openai',0,'legacy',7)`, before)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `UPDATE channel_monitor_v2_watermarks SET usage_coverage_start=$1::timestamptz,error_coverage_start=$1::timestamptz,data_through=$1::timestamptz + INTERVAL '1 hour',backfill_cursor=$1::timestamptz,last_successful_at=NOW() WHERE id=1`, before)
	require.NoError(t, err)
	require.NoError(t, ApplyMigrations(ctx, db))
	var version, successes int
	var unknown bool
	require.NoError(t, db.QueryRowContext(ctx, `SELECT aggregation_version,success_requests,availability_error_requests IS NULL FROM channel_monitor_v2_metrics_1m WHERE model='legacy'`).Scan(&version, &successes, &unknown))
	require.Equal(t, 1, version)
	require.Equal(t, 7, successes)
	require.True(t, unknown)
	var watermarkVersion int
	var coverageMissing bool
	require.NoError(t, db.QueryRowContext(ctx, `SELECT aggregation_version,availability_coverage_start IS NULL AND availability_source_start IS NULL FROM channel_monitor_v2_watermarks WHERE id=1`).Scan(&watermarkVersion, &coverageMissing))
	require.Equal(t, 1, watermarkVersion)
	require.True(t, coverageMissing)
	ledger := mergeV028MigrationLedger(t, db)
	require.NoError(t, ApplyMigrations(ctx, db))
	require.Equal(t, ledger, mergeV028MigrationLedger(t, db))
	var userID, keyID, accountID, groupID int64
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO users(email,password_hash) VALUES('single-source@monitor.test','fixture') RETURNING id`).Scan(&userID))
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO groups(name,platform) VALUES('single-source','openai') RETURNING id`).Scan(&groupID))
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO accounts(name,platform,type) VALUES('single-source','openai','oauth') RETURNING id`).Scan(&accountID))
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO api_keys(user_id,key,name,group_id) VALUES($1,'single-source-key','fixture',$2) RETURNING id`, userID, groupID).Scan(&keyID))
	for _, source := range []string{"empty", "usage-only", "error-only"} {
		t.Run(source, func(t *testing.T) {
			_, err := db.ExecContext(ctx, `DELETE FROM usage_logs; DELETE FROM ops_error_logs;`)
			require.NoError(t, err)
			start := before.Add(time.Hour)
			_, err = db.ExecContext(ctx, `UPDATE channel_monitor_v2_watermarks SET aggregation_version=1,availability_coverage_start=NULL,availability_source_start=NULL,availability_epoch_at=$1::timestamptz WHERE id=1`, start)
			require.NoError(t, err)
			if source == "usage-only" {
				_, err = db.ExecContext(ctx, `INSERT INTO usage_logs(user_id,api_key_id,account_id,group_id,request_id,model,created_at) VALUES($1,$2,$3,$4,'local:single-source','single-source',$5)`, userID, keyID, accountID, groupID, start.Add(10*time.Second))
				require.NoError(t, err)
			}
			if source == "error-only" {
				_, err = db.ExecContext(ctx, `INSERT INTO ops_error_logs(user_id,api_key_id,account_id,group_id,request_id,model,platform,error_owner,error_phase,error_type,status_code,created_at) VALUES($1,$2,$3,$4,'single-source','single-source','openai','provider','upstream','upstream_error',502,$5)`, userID, keyID, accountID, groupID, start.Add(10*time.Second))
				require.NoError(t, err)
			}
			repo := &channelMonitorV2Repository{db: db}
			require.NoError(t, repo.RecomputeRange(ctx, start, start.Add(time.Minute)))
			wm, err := repo.GetAggregationWatermark(ctx)
			require.NoError(t, err)
			require.Equal(t, 2, wm.AggregationVersion, "an empty source must not permanently prevent upgrading availability")
			require.False(t, wm.AvailabilityCoverageStart.After(start))
			cfg, err := repo.GetConfig(ctx)
			require.NoError(t, err)
			filter := service.ChannelMonitorV2Filter{Start: start, End: start.Add(time.Minute), Bucket: time.Minute, AllowedGroupIDs: []int64{groupID}, RestrictGroups: true}
			matrix, err := repo.GetMatrix(ctx, filter, *cfg, service.ChannelMonitorV2GroupByGroup, false)
			require.NoError(t, err)
			require.Len(t, matrix.Items, 1)
			require.True(t, matrix.Coverage.AvailabilityReady)
			var oldCount, oldVersion int
			require.NoError(t, db.QueryRowContext(ctx, `SELECT success_requests,aggregation_version FROM channel_monitor_v2_metrics_rollup WHERE model='legacy' AND bucket_seconds=86400`).Scan(&oldCount, &oldVersion))
			require.Equal(t, 7, oldCount, "a partial known minute must not replace an unknown historical daily rollup")
			require.Equal(t, 1, oldVersion)
			switch source {
			case "empty":
				require.Nil(t, matrix.Items[0].Metrics.AvailabilityRate)
			case "usage-only":
				require.NotNil(t, matrix.Items[0].Metrics.AvailabilityRate)
				require.Equal(t, 1.0, *matrix.Items[0].Metrics.AvailabilityRate)
			case "error-only":
				require.NotNil(t, matrix.Items[0].Metrics.AvailabilityRate)
				require.Equal(t, 0.0, *matrix.Items[0].Metrics.AvailabilityRate)
			}
		})
	}
}

func TestChannelMonitorV2OverviewMigrationsRestartIntegration(t *testing.T) {
	ctx := context.Background()
	before := mergeV028MigrationLedger(t, integrationDB)
	for _, name := range []string{"285_channel_monitor_v2_availability.sql", "287_channel_monitor_v2_request_identity_notx.sql"} {
		require.Contains(t, before, name)
	}
	for range 2 {
		require.NoError(t, ApplyMigrations(ctx, integrationDB))
		require.Equal(t, before, mergeV028MigrationLedger(t, integrationDB))
	}
	rdb := testRedis(t)
	require.NoError(t, rdb.Set(ctx, "overview-isolation", "ready", time.Minute).Err())
	require.Equal(t, "ready", rdb.Get(ctx, "overview-isolation").Val())
}

func TestChannelMonitorV2OverviewPermanentExhaustedVisibilityIntegration(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	u := mustCreateUser(t, client, &service.User{Email: uuid.NewString() + "@monitor.test", Balance: 0, GiftBalance: 0})
	permanent := mustCreateGroup(t, client, &service.Group{Name: "permanent-empty", Platform: "openai", SubscriptionType: "total_quota", TotalQuotaNoExpiry: true})
	finite := mustCreateGroup(t, client, &service.Group{Name: "finite-empty", Platform: "openai", SubscriptionType: "total_quota"})
	now := time.Now().UTC()
	later := now.Add(time.Hour)
	for _, item := range []struct {
		group  int64
		expiry *time.Time
	}{{permanent.ID, nil}, {finite.ID, &later}} {
		_, err := integrationDB.ExecContext(ctx, `INSERT INTO user_subscriptions(user_id,group_id,starts_at,expires_at,status) VALUES($1,$2,$3,$4,'active')`, u.ID, item.group, now.Add(-time.Hour), item.expiry)
		require.NoError(t, err)
	}
	repo := NewUserSubscriptionRepository(client)
	subs, err := repo.ListActiveByUserID(ctx, u.ID)
	require.NoError(t, err)
	require.Len(t, subs, 2)
	for _, sub := range subs {
		require.Zero(t, sub.TotalRemainingUSD)
		if sub.GroupID == permanent.ID {
			require.Nil(t, sub.ExpiresAt)
		}
	}
	_, err = integrationDB.ExecContext(ctx, `UPDATE user_subscriptions SET status='revoked' WHERE user_id=$1 AND group_id=$2`, u.ID, permanent.ID)
	require.NoError(t, err)
	subs, err = repo.ListActiveByUserID(ctx, u.ID)
	require.NoError(t, err)
	require.Len(t, subs, 1)
	require.Equal(t, finite.ID, subs[0].GroupID)
	_, err = integrationDB.ExecContext(ctx, `UPDATE user_subscriptions SET expires_at=NOW()-INTERVAL '1 second' WHERE user_id=$1`, u.ID)
	require.NoError(t, err)
	subs, err = repo.ListActiveByUserID(ctx, u.ID)
	require.NoError(t, err)
	require.Empty(t, subs)
}

func TestChannelMonitorV2OverviewShadowDoesNotInheritHistoryIntegration(t *testing.T) {
	ctx := context.Background()
	f := newMonitorOverviewFixture(t, "openai")
	shadow := mustCreateAccount(t, testEntClient(t), &service.Account{Name: "overview-shadow", Platform: "openai"})
	_, err := integrationDB.ExecContext(ctx, `UPDATE accounts SET parent_account_id=$1,quota_dimension='spark' WHERE id=$2`, f.account, shadow.ID)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO account_groups(account_id,group_id,priority) VALUES($1,$2,50)`, shadow.ID, f.group)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO model_attribution_jobs(account_id,account_name,source,status,snapshot,authorization_digest,fence_digest,finished_at) VALUES($1,'parent','manual','passed','{"policy":{"model":"parent-only"}}','','',NOW())`, f.account)
	require.NoError(t, err)
	repo := &channelMonitorV2Repository{db: integrationDB}
	history, err := repo.GetGroupAttributionHistory(ctx, []int64{f.group})
	require.NoError(t, err)
	require.Empty(t, history)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO model_attribution_jobs(account_id,account_name,source,status,snapshot,authorization_digest,fence_digest,finished_at) VALUES($1,'shadow','manual','mismatch','{"policy":{"model":"shadow-own"}}','','',NOW())`, shadow.ID)
	require.NoError(t, err)
	history, err = repo.GetGroupAttributionHistory(ctx, []int64{f.group})
	require.NoError(t, err)
	require.Len(t, history[f.group], 1)
	require.Equal(t, "shadow-own", history[f.group][0].ProbeModel)
}
