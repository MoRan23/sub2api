//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func attributionFixture(t *testing.T) (*attributionRepository, *accountRepository, *service.Account) {
	t.Helper()
	ctx := context.Background()
	_, err := integrationDB.ExecContext(ctx, `TRUNCATE model_attribution_jobs,model_attribution_state; UPDATE model_attribution_config SET version=1,config='{"enabled":false,"base_url":"","default":{"model":"gpt-6-astra","high_models":[],"low_models":[]},"groups":[]}'`)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, `TRUNCATE model_attribution_jobs,model_attribution_state; UPDATE model_attribution_config SET version=version+1,config=jsonb_set(config,'{enabled}','false')`)
	})
	r := &attributionRepository{db: integrationDB}
	ar := newAccountRepositoryWithSQL(integrationEntClient, integrationDB, nil)
	a := &service.Account{Name: "attribution-synthetic", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, Concurrency: 1, Credentials: oauthOSTestGrant("synthetic-token"), Extra: map[string]any{}}
	a.Credentials["model_mapping"] = map[string]any{"old": "old", "alias": "custom", "wild*": "target", "high": "custom-high"}
	require.NoError(t, ar.Create(ctx, a))
	t.Cleanup(func() { _, _ = integrationDB.ExecContext(ctx, `DELETE FROM accounts WHERE id=$1`, a.ID) })
	c := service.DefaultAttributionConfig()
	c.Enabled = true
	c.BaseURL = "http://modeltrace.invalid"
	c.Default.HighModels = []string{"high", "high2"}
	c.Default.LowModels = []string{"low"}
	_, err = r.SaveConfig(ctx, c)
	require.NoError(t, err)
	return r, ar, a
}
func attributionRun(t *testing.T, r *attributionRepository, id int64, status string) *service.AttributionJob {
	t.Helper()
	ctx := context.Background()
	jobs, err := r.Enqueue(ctx, []int64{id}, true)
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	j, err := r.Claim(ctx)
	require.NoError(t, err)
	require.NotNil(t, j)
	require.Equal(t, jobs[0].ID, j.ID)
	j.Status = status
	require.NoError(t, r.Finish(ctx, j))
	got, err := r.Get(ctx, j.ID)
	require.NoError(t, err)
	return got
}
func attributionMapping(t *testing.T, ar *accountRepository, id int64) map[string]any {
	t.Helper()
	a, err := ar.GetByID(context.Background(), id)
	require.NoError(t, err)
	out, _ := a.Credentials["model_mapping"].(map[string]any)
	return out
}

func TestAttributionTransitionsAtomicityAndCache(t *testing.T) {
	ctx := context.Background()
	r, ar, a := attributionFixture(t)
	// Seed a cached pre-detection account; the transactional outbox must replace it.
	cache := NewSchedulerCache(testRedis(t))
	require.NoError(t, cache.SetAccount(ctx, a))
	scheduler := service.NewSchedulerSnapshotService(cache, NewSchedulerOutboxRepository(integrationDB), ar, nil, &config.Config{Gateway: config.GatewayConfig{Scheduling: config.GatewaySchedulingConfig{OutboxPollIntervalSeconds: 1, DbFallbackEnabled: true}}})
	scheduler.Start()
	defer scheduler.Stop()
	j := attributionRun(t, r, a.ID, "passed")
	require.Equal(t, "high", j.Result.Action)
	high := map[string]any{"high": "custom-high", "high2": "high2", "alias": "custom", "wild*": "target"}
	require.Equal(t, high, attributionMapping(t, ar, a.ID))
	require.Eventually(t, func() bool {
		cached, e := cache.GetAccount(ctx, a.ID)
		return e == nil && cached != nil && cached.GetModelMapping()["high2"] == "high2"
	}, 5*time.Second, 50*time.Millisecond)
	var outbox int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM scheduler_outbox WHERE account_id=$1`, a.ID).Scan(&outbox))
	require.Positive(t, outbox)
	j = attributionRun(t, r, a.ID, "passed")
	require.Equal(t, "unchanged", j.Result.Action)
	var after int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM scheduler_outbox WHERE account_id=$1`, a.ID).Scan(&after))
	require.Equal(t, outbox, after)
	j = attributionRun(t, r, a.ID, "mismatch")
	require.Equal(t, "low", j.Result.Action)
	low := map[string]any{"low": "low", "high": "custom-high", "alias": "custom", "wild*": "target"}
	require.Equal(t, low, attributionMapping(t, ar, a.ID))
	require.Equal(t, "unchanged", attributionRun(t, r, a.ID, "mismatch").Result.Action)
	require.Equal(t, "none", attributionRun(t, r, a.ID, "abnormal").Result.Action)
	require.Equal(t, low, attributionMapping(t, ar, a.ID))
	require.Equal(t, "none", attributionRun(t, r, a.ID, "failed").Result.Action)
	require.Equal(t, "high", attributionRun(t, r, a.ID, "passed").Result.Action)
	c, err := r.Config(ctx)
	require.NoError(t, err)
	c.Default.HighModels = []string{"new-high"}
	_, err = r.SaveConfig(ctx, c)
	require.NoError(t, err)
	require.Equal(t, "high", attributionRun(t, r, a.ID, "passed").Result.Action)
	require.Equal(t, "new-high", attributionMapping(t, ar, a.ID)["new-high"])
	_, err = r.SaveConfig(ctx, c)
	require.ErrorIs(t, err, service.ErrAttributionConflict)
	// A result write failure must roll back the mapping AND valid baseline/outbox.
	_, err = r.Enqueue(ctx, []int64{a.ID}, true)
	require.NoError(t, err)
	pending, err := r.Claim(ctx)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `CREATE FUNCTION attribution_test_reject() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic failure'; END $$; CREATE TRIGGER attribution_test_reject BEFORE UPDATE ON model_attribution_jobs FOR EACH ROW EXECUTE FUNCTION attribution_test_reject()`)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, `DROP TRIGGER IF EXISTS attribution_test_reject ON model_attribution_jobs; DROP FUNCTION IF EXISTS attribution_test_reject()`)
	})
	before := attributionMapping(t, ar, a.ID)
	pending.Status = "mismatch"
	require.Error(t, r.Finish(ctx, pending))
	require.Equal(t, before, attributionMapping(t, ar, a.ID))
	var verdict string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT verdict FROM model_attribution_state WHERE account_id=$1`, a.ID).Scan(&verdict))
	require.Equal(t, "passed", verdict)
}

func TestAttributionFencesRefreshAndGroupBaseline(t *testing.T) {
	ctx := context.Background()
	r, ar, a := attributionFixture(t)
	claim := func() *service.AttributionJob {
		_, e := r.Enqueue(ctx, []int64{a.ID}, true)
		require.NoError(t, e)
		j, e := r.Claim(ctx)
		require.NoError(t, e)
		require.NotNil(t, j)
		return j
	}
	j := claim()
	// Token rotation shares its authorization generation and must not invalidate detection.
	_, err := integrationDB.ExecContext(ctx, `UPDATE accounts SET credentials=jsonb_set(credentials,'{access_token}','"rotated-synthetic"') WHERE id=$1`, a.ID)
	require.NoError(t, err)
	valid, err := r.Validate(ctx, j)
	require.NoError(t, err)
	require.True(t, valid)
	j.Status = "passed"
	require.NoError(t, r.Finish(ctx, j))
	fresh, err := ar.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.Equal(t, "rotated-synthetic", fresh.GetCredential("access_token"))
	for _, mutation := range []string{"mapping", "authorization", "configuration", "group"} {
		t.Run(mutation, func(t *testing.T) {
			j = claim()
			switch mutation {
			case "mapping":
				_, err = integrationDB.ExecContext(ctx, `UPDATE accounts SET credentials=jsonb_set(credentials,'{model_mapping}','{"manual":"manual"}') WHERE id=$1`, a.ID)
			case "authorization":
				_, err = integrationDB.ExecContext(ctx, `UPDATE account_openai_oauth_credentials SET authorization_generation=$2 WHERE account_id=$1`, a.ID, uuid.NewString())
			case "configuration":
				var c service.AttributionConfig
				c, err = r.Config(ctx)
				require.NoError(t, err)
				c.Default.Model = "new-probe"
				_, err = r.SaveConfig(ctx, c)
			case "group":
				g := mustCreateGroup(t, integrationEntClient, &service.Group{Name: "attribution-fence", Platform: service.PlatformOpenAI})
				t.Cleanup(func() { _, _ = integrationDB.ExecContext(ctx, `DELETE FROM groups WHERE id=$1`, g.ID) })
				_, err = integrationDB.ExecContext(ctx, `INSERT INTO account_groups(account_id,group_id,priority) VALUES($1,$2,2)`, a.ID, g.ID)
			}
			require.NoError(t, err)
			before := attributionMapping(t, ar, a.ID)
			valid, err = r.Validate(ctx, j)
			require.NoError(t, err)
			require.False(t, valid)
			j.Status = "mismatch"
			require.NoError(t, r.Finish(ctx, j))
			require.Equal(t, "stale", j.Result.Action)
			require.Equal(t, before, attributionMapping(t, ar, a.ID))
		})
	}
	// Changing group priority selects a different policy even at the same config version.
	g1 := mustCreateGroup(t, integrationEntClient, &service.Group{Name: "attribution-group-1", Platform: service.PlatformOpenAI})
	g2 := mustCreateGroup(t, integrationEntClient, &service.Group{Name: "attribution-group-2", Platform: service.PlatformOpenAI})
	t.Cleanup(func() { _, _ = integrationDB.ExecContext(ctx, `DELETE FROM groups WHERE id IN ($1,$2)`, g1.ID, g2.ID) })
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO account_groups(account_id,group_id,priority) VALUES($1,$2,0),($1,$3,1)`, a.ID, g1.ID, g2.ID)
	require.NoError(t, err)
	c, err := r.Config(ctx)
	require.NoError(t, err)
	c.Groups = []service.AttributionGroupPolicy{{GroupID: g1.ID, Enabled: true, AttributionPolicy: service.AttributionPolicy{Model: "group-probe", HighModels: []string{"group-high-1"}, LowModels: []string{"group-low"}}}, {GroupID: g2.ID, Enabled: true, AttributionPolicy: service.AttributionPolicy{Model: "group-probe", HighModels: []string{"group-high-2"}, LowModels: []string{"group-low"}}}}
	_, err = r.SaveConfig(ctx, c)
	require.NoError(t, err)
	require.Equal(t, g1.ID, attributionRun(t, r, a.ID, "passed").Snapshot.GroupID)
	_, err = integrationDB.ExecContext(ctx, `UPDATE account_groups SET priority=-1 WHERE account_id=$1 AND group_id=$2`, a.ID, g2.ID)
	require.NoError(t, err)
	j = attributionRun(t, r, a.ID, "passed")
	require.Equal(t, g2.ID, j.Snapshot.GroupID)
	require.Equal(t, "high", j.Result.Action)
	require.Contains(t, attributionMapping(t, ar, a.ID), "group-high-2")
}

func TestAttributionQueueLeasesRetentionAndRestart(t *testing.T) {
	ctx := context.Background()
	r, ar, a := attributionFixture(t)
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := (&attributionRepository{db: integrationDB}).Enqueue(ctx, []int64{a.ID}, true)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	page, err := r.List(ctx, a.ID, 1, 20)
	require.NoError(t, err)
	require.Equal(t, 1, page.Total)
	ids := []int64{a.ID}
	for range 4 {
		b := &service.Account{Name: "attribution-parallel", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, Credentials: oauthOSTestGrant("synthetic")}
		require.NoError(t, ar.Create(ctx, b))
		ids = append(ids, b.ID)
		t.Cleanup(func() { _, _ = integrationDB.ExecContext(ctx, `DELETE FROM accounts WHERE id=$1`, b.ID) })
	}
	_, err = r.Enqueue(ctx, ids, true)
	require.NoError(t, err)
	claims := make(chan *service.AttributionJob, 12)
	errs = make(chan error, 12)
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			j, e := (&attributionRepository{db: integrationDB}).Claim(ctx)
			errs <- e
			if j != nil {
				claims <- j
			}
		}()
	}
	wg.Wait()
	close(claims)
	close(errs)
	for e := range errs {
		require.NoError(t, e)
	}
	claimed := []*service.AttributionJob{}
	for j := range claims {
		claimed = append(claimed, j)
	}
	require.Len(t, claimed, 3)
	old := claimed[0]
	ok, err := r.Heartbeat(ctx, old)
	require.NoError(t, err)
	require.True(t, ok)
	_, err = integrationDB.ExecContext(ctx, `UPDATE model_attribution_jobs SET lease_until=NOW()-interval '1 second' WHERE id=$1`, old.ID)
	require.NoError(t, err)
	next, err := r.Claim(ctx)
	require.NoError(t, err)
	require.NotNil(t, next)
	require.NotEqual(t, old.ID, next.ID)
	expired, err := r.Get(ctx, old.ID)
	require.NoError(t, err)
	require.Equal(t, "interrupted", expired.Reason)
	old.Status = "passed"
	require.NoError(t, r.Finish(ctx, old))
	expired, err = r.Get(ctx, old.ID)
	require.NoError(t, err)
	require.Equal(t, "failed", expired.Status)
	_, err = integrationDB.ExecContext(ctx, `UPDATE model_attribution_jobs SET started_at=NOW()-interval '11 minutes' WHERE id=$1`, next.ID)
	require.NoError(t, err)
	_, err = r.Claim(ctx)
	require.NoError(t, err)
	expired, err = r.Get(ctx, next.ID)
	require.NoError(t, err)
	require.Equal(t, "timeout", expired.Reason)
	// Disable cancels queued work, and enabling requeues eligible accounts only once.
	c, err := r.Config(ctx)
	require.NoError(t, err)
	c.Enabled = false
	_, err = r.SaveConfig(ctx, c)
	require.NoError(t, err)
	_, err = r.Enqueue(ctx, ids, true)
	require.ErrorIs(t, err, service.ErrAttributionDisabled)
	_, err = r.Claim(ctx)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE model_attribution_jobs SET status='failed',finished_at=NOW() WHERE status='running'`)
	require.NoError(t, err)
	c, err = r.Config(ctx)
	require.NoError(t, err)
	c.Enabled = true
	_, err = r.SaveConfig(ctx, c)
	require.NoError(t, err)
	jobs, err := r.Enqueue(ctx, nil, false)
	require.NoError(t, err)
	require.Len(t, jobs, 5)
	again, err := r.Enqueue(ctx, nil, false)
	require.NoError(t, err)
	require.Empty(t, again)
	_, err = integrationDB.ExecContext(ctx, `UPDATE model_attribution_jobs SET status='failed',finished_at=NOW() WHERE status='queued'`)
	require.NoError(t, err)
	require.Equal(t, "high", attributionRun(t, r, a.ID, "passed").Result.Action)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO model_attribution_jobs(account_id,account_name,source,status,snapshot,authorization_digest,fence_digest,finished_at) SELECT $1,'synthetic','manual','abnormal','{}','','',NOW() FROM generate_series(1,105)`, a.ID)
	require.NoError(t, err)
	attributionRun(t, r, a.ID, "abnormal")
	page, err = r.List(ctx, a.ID, 1, 100)
	require.NoError(t, err)
	require.Equal(t, 100, page.Total)
	var verdict string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT verdict FROM model_attribution_state WHERE account_id=$1`, a.ID).Scan(&verdict))
	require.Equal(t, "passed", verdict)
	require.Equal(t, "unchanged", attributionRun(t, r, a.ID, "passed").Result.Action)
	b, err := json.Marshal(page)
	require.NoError(t, err)
	require.NotContains(t, string(b), "synthetic-token")
}

func TestAttributionSkipAndMigrationRepeat(t *testing.T) {
	ctx := context.Background()
	r, _, a := attributionFixture(t)
	for _, tc := range []struct{ sql, reason string }{
		{`UPDATE accounts SET status='disabled' WHERE id=$1`, "account_inactive"},
		{`UPDATE accounts SET status='active',schedulable=false WHERE id=$1`, "scheduling_disabled"},
		{`UPDATE accounts SET schedulable=true,expires_at=NOW()-interval '1 minute' WHERE id=$1`, "account_expired"},
		{`UPDATE accounts SET expires_at=NULL,extra='{"openai_passthrough":true}' WHERE id=$1`, "passthrough_account"},
		{`UPDATE accounts SET extra='{}',type='apikey' WHERE id=$1`, "unsupported_account"},
		{`UPDATE accounts SET type='oauth',deleted_at=NOW() WHERE id=$1`, "account_missing"},
	} {
		_, err := integrationDB.ExecContext(ctx, tc.sql, a.ID)
		require.NoError(t, err)
		jobs, err := r.Enqueue(ctx, []int64{a.ID}, true)
		require.NoError(t, err)
		require.Len(t, jobs, 1)
		require.Equal(t, "skipped", jobs[0].Status)
		require.Equal(t, tc.reason, jobs[0].Reason)
		automatic, err := r.Enqueue(ctx, nil, false)
		require.NoError(t, err)
		require.Empty(t, automatic)
	}
	require.NoError(t, ApplyMigrations(ctx, integrationDB))
}
