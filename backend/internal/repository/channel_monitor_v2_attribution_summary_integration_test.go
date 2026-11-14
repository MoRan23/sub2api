//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

// Every fixture writes only to the disposable Postgres started by TestMain.
// Timestamps are explicit: no model requests or attribution workers are run.
type attributionOverviewFixture struct {
	t    *testing.T
	now  time.Time
	repo *channelMonitorV2Repository
}

func newAttributionOverviewFixture(t *testing.T) attributionOverviewFixture {
	t.Helper()
	var original []byte
	require.NoError(t, integrationDB.QueryRow(`SELECT config FROM model_attribution_config WHERE id=1`).Scan(&original))
	t.Cleanup(func() {
		_, err := integrationDB.Exec(`UPDATE model_attribution_config SET config=$1 WHERE id=1`, original)
		require.NoError(t, err)
	})
	f := attributionOverviewFixture{t: t, now: time.Now().UTC().Truncate(time.Microsecond), repo: &channelMonitorV2Repository{db: integrationDB}}
	f.enabled(true)
	return f
}

func (f attributionOverviewFixture) enabled(enabled bool) {
	f.t.Helper()
	_, err := integrationDB.Exec(`UPDATE model_attribution_config SET config=jsonb_set(config,'{enabled}',to_jsonb($1::boolean)) WHERE id=1`, enabled)
	require.NoError(f.t, err)
}

func (f attributionOverviewFixture) group() int64 {
	f.t.Helper()
	return mustCreateGroup(f.t, testEntClient(f.t), &service.Group{Name: "attribution-overview-" + uuid.NewString(), Platform: "openai"}).ID
}

func (f attributionOverviewFixture) account(group int64, kind, extra string) int64 {
	f.t.Helper()
	var id int64
	require.NoError(f.t, integrationDB.QueryRow(`INSERT INTO accounts(name,platform,type,extra,schedulable) VALUES($1,'openai',$2,$3,true) RETURNING id`, "attribution-overview-"+uuid.NewString(), kind, extra).Scan(&id))
	f.bind(id, group)
	return id
}

func (f attributionOverviewFixture) bind(account, group int64) {
	f.t.Helper()
	_, err := integrationDB.Exec(`INSERT INTO account_groups(account_id,group_id,priority) VALUES($1,$2,50)`, account, group)
	require.NoError(f.t, err)
}

func (f attributionOverviewFixture) job(account int64, status string, at time.Time) int64 {
	f.t.Helper()
	var id int64
	require.NoError(f.t, integrationDB.QueryRow(`INSERT INTO model_attribution_jobs(account_id,account_name,source,status,snapshot,authorization_digest,fence_digest,reason,finished_at)
	 VALUES($1,'private-account','manual',$2,'{"policy":{"model":"probe-model"},"private":"secret-credentials-and-answer"}','secret-auth','secret-fence','private-reason',$3) RETURNING id`, account, status, at).Scan(&id))
	return id
}

func (f attributionOverviewFixture) read(groups ...int64) map[int64]service.ChannelMonitorV2OverviewAttribution {
	f.t.Helper()
	got, err := f.repo.GetGroupAttributionOverview(context.Background(), groups, f.now)
	require.NoError(f.t, err)
	for _, group := range groups {
		row, ok := got[group]
		require.True(f.t, ok, "requested group %d must have an explicit no-data result", group)
		require.True(f.t, f.now.Equal(row.AsOf))
		require.Len(f.t, row.History, 20)
		for i, bucket := range row.History {
			require.True(f.t, f.now.Add(time.Duration(i-20)*10*time.Minute).Equal(bucket.BucketStart))
			require.True(f.t, f.now.Add(time.Duration(i-19)*10*time.Minute).Equal(bucket.BucketEnd))
		}
		require.Equal(f.t, row.SummaryState, row.History[19].State)
	}
	return got
}

func TestChannelMonitorV2OverviewAttributionSummaryRulesIntegration(t *testing.T) {
	f := newAttributionOverviewFixture(t)
	cases := []struct {
		name  string
		kind  string
		extra string
		jobs  []struct {
			status string
			offset time.Duration
		}
		state   string
		enabled bool
	}{
		{name: "oauth exact fifteen minute boundary", kind: "oauth", extra: `{}`, jobs: []struct {
			status string
			offset time.Duration
		}{{"passed", -15 * time.Minute}, {"failed", -time.Minute}}, state: "passed", enabled: true},
		{name: "oauth older than fifteen minutes", kind: "oauth", extra: `{}`, jobs: []struct {
			status string
			offset time.Duration
		}{{"passed", -15*time.Minute - time.Microsecond}, {"failed", -time.Minute}}, state: "failed", enabled: true},
		{name: "oauth mismatch outranks failure", kind: "oauth", extra: `{}`, jobs: []struct {
			status string
			offset time.Duration
		}{{"mismatch", -14 * time.Minute}, {"failed", -time.Minute}}, state: "mismatch", enabled: true},
		{name: "oauth abnormal is failed", kind: "oauth", extra: `{}`, jobs: []struct {
			status string
			offset time.Duration
		}{{"abnormal", -time.Minute}}, state: "failed", enabled: true},
		{name: "oauth invalid rounds have no conclusion", kind: "oauth", extra: `{}`, jobs: []struct {
			status string
			offset time.Duration
		}{{"cancelled", -2 * time.Minute}, {"skipped", -time.Minute}}, state: "no_data", enabled: true},
		{name: "oauth future excluded", kind: "oauth", extra: `{}`, jobs: []struct {
			status string
			offset time.Duration
		}{{"passed", time.Microsecond}}, state: "no_data", enabled: true},
		{name: "apikey opt out retains old history", kind: "apikey", extra: `{"openai_api_key_auto_attribution_enabled":false}`, jobs: []struct {
			status string
			offset time.Duration
		}{{"passed", -365 * 24 * time.Hour}}, state: "passed"},
		{name: "engine opt out retains old history", kind: "apikey", extra: `{"openai_api_key_mode":"codex_engine","openai_api_key_auto_attribution_enabled":false}`, jobs: []struct {
			status string
			offset time.Duration
		}{{"mismatch", -365 * 24 * time.Hour}}, state: "mismatch"},
		{name: "engine enabled without history", kind: "apikey", extra: `{"openai_api_key_mode":"codex_engine","openai_api_key_auto_attribution_enabled":true}`, state: "no_data", enabled: true},
		{name: "apikey disabled without history", kind: "apikey", extra: `{}`, state: "no_data"},
		{name: "apikey latest effective tie uses id", kind: "apikey", extra: `{"openai_api_key_auto_attribution_enabled":true}`, jobs: []struct {
			status string
			offset time.Duration
		}{{"passed", -time.Hour}, {"mismatch", -time.Hour}, {"cancelled", -time.Minute}}, state: "mismatch", enabled: true},
	}
	groups := make([]int64, len(cases))
	for i, tc := range cases {
		groups[i] = f.group()
		account := f.account(groups[i], tc.kind, tc.extra)
		for _, job := range tc.jobs {
			f.job(account, job.status, f.now.Add(job.offset))
		}
	}
	got := f.read(groups...)
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.state, got[groups[i]].SummaryState)
			require.Equal(t, tc.enabled, got[groups[i]].Enabled)
		})
	}
	f.enabled(false)
	disabled := f.read(groups...)
	for _, group := range groups {
		require.False(t, disabled[group].Enabled)
		require.Equal(t, got[group].History, disabled[group].History)
		require.Equal(t, got[group].SummaryState, disabled[group].SummaryState)
	}
	empty, err := f.repo.GetGroupAttributionOverview(context.Background(), nil, f.now)
	require.NoError(t, err)
	require.Empty(t, empty)
}

func TestChannelMonitorV2OverviewAttributionBeyondDisplayAndRetentionLimitIntegration(t *testing.T) {
	f := newAttributionOverviewFixture(t)
	for _, failures := range []int{20, 100} {
		t.Run(fmt.Sprintf("pass-after-%d-failures", failures), func(t *testing.T) {
			group := f.group()
			account := f.account(group, "oauth", `{}`)
			f.job(account, "passed", f.now.Add(-10*time.Minute))
			for i := range failures {
				f.job(account, "failed", f.now.Add(-9*time.Minute+time.Duration(i)*time.Second))
			}
			got := f.read(group)[group]
			require.Equal(t, "passed", got.SummaryState, "pass behind %d newer failures must still contribute", failures)
			require.Len(t, got.Recent, 20)
			require.Equal(t, "failed", got.Latest.Status)
			for _, recent := range got.Recent {
				require.Equal(t, "failed", recent.Status)
			}
		})
	}
}

func TestChannelMonitorV2OverviewAttributionTemporalBucketsIntegration(t *testing.T) {
	f := newAttributionOverviewFixture(t)
	apiGroup, oauthGroup := f.group(), f.group()
	api := f.account(apiGroup, "apikey", `{"openai_api_key_auto_attribution_enabled":false}`)
	f.job(api, "passed", f.now.Add(-24*time.Hour))
	f.job(api, "failed", f.now.Add(-90*time.Minute))
	f.job(api, "cancelled", f.now.Add(-60*time.Minute))
	f.job(api, "skipped", f.now.Add(-30*time.Minute))
	oauth := f.account(oauthGroup, "oauth", `{}`)
	f.job(oauth, "passed", f.now.Add(-205*time.Minute))
	f.job(oauth, "mismatch", f.now.Add(-190*time.Minute))
	f.job(oauth, "failed", f.now.Add(-180*time.Minute))
	got := f.read(apiGroup, oauthGroup)
	for i, bucket := range got[apiGroup].History {
		want := "passed"
		if i >= 10 {
			want = "failed"
		}
		require.Equal(t, want, bucket.State, "API-key bucket %d", i)
	}
	for i, bucket := range got[oauthGroup].History {
		want := "no_data"
		switch i {
		case 0:
			want = "passed"
		case 1:
			want = "mismatch"
		case 2:
			want = "failed"
		}
		require.Equal(t, want, bucket.State, "OAuth bucket %d", i)
	}
}

func TestChannelMonitorV2OverviewAttributionMembershipAndEnabledIntegration(t *testing.T) {
	f := newAttributionOverviewFixture(t)
	main, shared, shadowGroup, deletedGroup, mixedGroup := f.group(), f.group(), f.group(), f.group(), f.group()
	parent := f.account(main, "oauth", `{}`)
	f.bind(parent, shared)
	f.job(parent, "passed", f.now.Add(-time.Minute))
	shadow := f.account(shadowGroup, "oauth", `{}`)
	_, err := integrationDB.Exec(`UPDATE accounts SET parent_account_id=$1,quota_dimension='spark' WHERE id=$2`, parent, shadow)
	require.NoError(t, err)
	deleted := f.account(deletedGroup, "oauth", `{}`)
	f.job(deleted, "passed", f.now.Add(-time.Minute))
	_, err = integrationDB.Exec(`UPDATE accounts SET deleted_at=NOW() WHERE id=$1`, deleted)
	require.NoError(t, err)
	api := f.account(mixedGroup, "apikey", `{"openai_api_key_mode":"codex_engine","openai_api_key_auto_attribution_enabled":false}`)
	f.job(api, "mismatch", f.now.Add(-24*time.Hour))
	oauth := f.account(mixedGroup, "oauth", `{}`)
	f.job(oauth, "abnormal", f.now.Add(-time.Minute))
	_, err = integrationDB.Exec(`UPDATE accounts SET status='error',rate_limit_reset_at=NOW()+INTERVAL '1 day' WHERE id=$1`, oauth)
	require.NoError(t, err)
	got := f.read(main, shared, shadowGroup, deletedGroup, mixedGroup)
	require.Equal(t, "passed", got[main].SummaryState)
	require.Equal(t, got[main].History, got[shared].History)
	require.Equal(t, "no_data", got[shadowGroup].SummaryState)
	require.False(t, got[shadowGroup].Enabled)
	require.Equal(t, "no_data", got[deletedGroup].SummaryState)
	require.False(t, got[deletedGroup].Enabled)
	require.Equal(t, "mismatch", got[mixedGroup].SummaryState)
	require.True(t, got[mixedGroup].Enabled, "error and rate-limit state do not change attribution configuration")
	_, err = integrationDB.Exec(`UPDATE accounts SET schedulable=false WHERE id=$1`, parent)
	require.NoError(t, err)
	paused := f.read(main, shared)
	for _, group := range []int64{main, shared} {
		require.False(t, paused[group].Enabled)
		require.Equal(t, "no_data", paused[group].SummaryState)
		require.Nil(t, paused[group].Latest)
		require.Empty(t, paused[group].Recent)
		for _, bucket := range paused[group].History {
			require.Equal(t, "no_data", bucket.State)
		}
	}
	legacy, err := f.repo.GetGroupAttributionHistory(context.Background(), []int64{main, shared})
	require.NoError(t, err)
	require.Empty(t, legacy)
	_, err = integrationDB.Exec(`UPDATE accounts SET schedulable=true WHERE id=$1`, parent)
	require.NoError(t, err)
	restored := f.read(main, shared)
	for _, group := range []int64{main, shared} {
		require.Equal(t, got[group], restored[group])
	}
	f.job(shadow, "failed", f.now.Add(-time.Minute))
	require.Equal(t, "failed", f.read(shadowGroup)[shadowGroup].SummaryState, "shadow keeps its own result")
	_, err = integrationDB.Exec(`DELETE FROM account_groups WHERE account_id=$1 AND group_id=$2`, parent, main)
	require.NoError(t, err)
	got = f.read(main, shared)
	require.Equal(t, "no_data", got[main].SummaryState)
	require.False(t, got[main].Enabled)
	require.Equal(t, "passed", got[shared].SummaryState)

	for _, tc := range []struct {
		extra   string
		enabled bool
	}{
		{`{"openai_passthrough":true}`, false},
		{`{"openai_oauth_passthrough":true}`, false},
		{`{"openai_passthrough":false,"openai_oauth_passthrough":true}`, true},
		{`{"openai_passthrough":"invalid","openai_oauth_passthrough":true}`, false},
	} {
		group := f.group()
		f.account(group, "oauth", tc.extra)
		require.Equal(t, tc.enabled, f.read(group)[group].Enabled, tc.extra)
	}
}

func TestChannelMonitorV2OverviewAttributionPruningPreservesHistoryIntegration(t *testing.T) {
	f := newAttributionOverviewFixture(t)
	oauthGroup, apiGroup := f.group(), f.group()
	oauth := f.account(oauthGroup, "oauth", `{}`)
	api := f.account(apiGroup, "apikey", `{"openai_api_key_mode":"codex_engine","openai_api_key_auto_attribution_enabled":false}`)
	obsolete := f.job(api, "failed", f.now.Add(-48*time.Hour))
	anchor := f.job(api, "passed", f.now.Add(-24*time.Hour))
	f.job(api, "failed", f.now.Add(-90*time.Minute))
	f.job(oauth, "passed", f.now.Add(-205*time.Minute))
	f.job(oauth, "passed", f.now.Add(-10*time.Minute))
	for i := range 150 {
		f.job(api, "cancelled", f.now.Add(-5*time.Minute+time.Duration(i)*time.Second))
		f.job(oauth, "failed", f.now.Add(-5*time.Minute+time.Duration(i)*time.Second))
	}
	before := f.read(oauthGroup, apiGroup)
	tx, err := integrationDB.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })
	require.NoError(t, pruneAttribution(context.Background(), tx))
	require.NoError(t, tx.Commit())
	after := f.read(oauthGroup, apiGroup)
	for _, group := range []int64{oauthGroup, apiGroup} {
		require.Equal(t, before[group].History, after[group].History)
		require.Equal(t, before[group].SummaryState, after[group].SummaryState)
	}
	var kept, removed int
	require.NoError(t, integrationDB.QueryRow(`SELECT count(*) FILTER (WHERE id=$1),count(*) FILTER (WHERE id=$2) FROM model_attribution_jobs WHERE account_id=$3`, anchor, obsolete, api).Scan(&kept, &removed))
	require.Equal(t, 1, kept, "API-key latest pre-window effective anchor survives")
	require.Zero(t, removed, "obsolete pre-window history still gets pruned")
}

func TestChannelMonitorV2OverviewAttributionBatchProjectionIntegration(t *testing.T) {
	f := newAttributionOverviewFixture(t)
	groups := []int64{f.group()}
	api := f.account(groups[0], "apikey", `{"openai_api_key_auto_attribution_enabled":true}`)
	oauth := f.account(groups[0], "oauth", `{}`)
	for range 49 {
		group := f.group()
		groups = append(groups, group)
		f.bind(api, group)
		f.bind(oauth, group)
	}
	_, err := integrationDB.Exec(`INSERT INTO model_attribution_jobs(account_id,account_name,source,status,snapshot,authorization_digest,fence_digest,finished_at)
	 SELECT account_id,'private-account','manual','failed',jsonb_build_object('policy',jsonb_build_object('model','old'),'private',repeat('secret-answer',1000)),'secret','secret',$3::timestamptz - n*INTERVAL '1 minute'
	 FROM unnest(ARRAY[$1::bigint,$2::bigint]) account_id CROSS JOIN generate_series(1,1000) n`, api, oauth, f.now.Add(-216*time.Minute))
	require.NoError(t, err)
	f.job(api, "passed", f.now.Add(-24*time.Hour))
	f.job(oauth, "passed", f.now.Add(-time.Minute))
	got := f.read(groups...)
	require.Len(t, got, 50)
	encoded, err := json.Marshal(got)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "private")
	require.NotContains(t, string(encoded), "secret")
	require.Less(t, len(encoded), 500000)
	var planJSON []byte
	require.NoError(t, integrationDB.QueryRow("EXPLAIN (ANALYZE,BUFFERS,FORMAT JSON) "+channelMonitorV2GroupAttributionOverviewSQL, pq.Array(groups), f.now).Scan(&planJSON))
	var plan []struct {
		Plan struct {
			ActualRows float64 `json:"Actual Rows"`
		}
		ExecutionTime float64 `json:"Execution Time"`
	}
	require.NoError(t, json.Unmarshal(planJSON, &plan))
	require.Len(t, plan, 1)
	require.LessOrEqual(t, plan[0].Plan.ActualRows, 1050.0)
	t.Logf("attribution summary query: 1 batch, 50 shared groups, 2002 stored jobs, %.0f SQL rows, %d public JSON bytes, %.3fms", plan[0].Plan.ActualRows, len(encoded), plan[0].ExecutionTime)
}
