//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

func TestAttributionExpectedModelsBatchAndOverrides(t *testing.T) {
	ctx := context.Background()
	r, ar, a := attributionFixture(t)
	g := mustCreateGroup(t, integrationEntClient, &service.Group{Name: "expected-group", Platform: service.PlatformOpenAI})
	t.Cleanup(func() { _, _ = integrationDB.Exec(`DELETE FROM groups WHERE id=$1`, g.ID) })
	b := attributionAPIKeyAccount(t, ar)
	require.NoError(t, ar.BindGroups(ctx, b.ID, []int64{g.ID}))
	c, err := r.Config(ctx)
	require.NoError(t, err)
	c.Default.ExpectedModels = []string{"global-a", "global-b"}
	c.Groups = []service.AttributionGroupPolicy{{GroupID: g.ID, Enabled: true, AttributionPolicy: service.AttributionPolicy{Model: "group-probe", ExpectedModels: []string{"group-expected"}, HighModels: []string{"high"}, LowModels: []string{"low"}}}}
	_, err = r.SaveConfig(ctx, c)
	require.NoError(t, err)
	jobs, err := r.Enqueue(ctx, []int64{a.ID, b.ID}, true, service.AttributionProbeOptions{Model: "manual-probe"})
	require.NoError(t, err)
	require.Len(t, jobs, 2)
	require.Equal(t, []string{"global-a", "global-b"}, jobs[0].Snapshot.Policy.ExpectedModels)
	require.Equal(t, []string{"group-expected"}, jobs[1].Snapshot.Policy.ExpectedModels)
	shared := []string{"override"}
	duplicate, err := r.Enqueue(ctx, []int64{a.ID}, true, service.AttributionProbeOptions{Model: "different", ExpectedModels: &shared})
	require.NoError(t, err)
	require.Equal(t, jobs[0].ID, duplicate[0].ID)
	require.Equal(t, jobs[0].Snapshot, duplicate[0].Snapshot)
	for range 2 {
		j, err := r.Claim(ctx)
		require.NoError(t, err)
		require.NotNil(t, j)
		j.Status = "abnormal"
		require.NoError(t, r.Finish(ctx, j))
	}
	jobs, err = r.Enqueue(ctx, []int64{a.ID, b.ID}, true, service.AttributionProbeOptions{ExpectedModels: &shared})
	require.NoError(t, err)
	for _, j := range jobs {
		require.Equal(t, shared, j.Snapshot.Policy.ExpectedModels)
	}
	for range 2 {
		j, err := r.Claim(ctx)
		require.NoError(t, err)
		require.NotNil(t, j)
		j.Status = "abnormal"
		require.NoError(t, r.Finish(ctx, j))
	}
	empty := []string{}
	jobs, err = r.Enqueue(ctx, []int64{a.ID, b.ID}, true, service.AttributionProbeOptions{ExpectedModels: &empty})
	require.NoError(t, err)
	require.Equal(t, []string{c.Default.Model}, jobs[0].Snapshot.Policy.ExpectedModels)
	require.Equal(t, []string{"group-probe"}, jobs[1].Snapshot.Policy.ExpectedModels)
}

func TestAttributionExpectedModelsConsecutivePassesAndFence(t *testing.T) {
	ctx := context.Background()
	r, ar, a := attributionFixture(t)
	c, err := r.Config(ctx)
	require.NoError(t, err)
	c.Default.ExpectedModels = []string{"first", "second"}
	c, err = r.SaveConfig(ctx, c)
	require.NoError(t, err)
	run := func(winner string, override *[]string) *service.AttributionJob {
		t.Helper()
		_, err := r.Enqueue(ctx, []int64{a.ID}, true, service.AttributionProbeOptions{ExpectedModels: override})
		require.NoError(t, err)
		j, err := r.Claim(ctx)
		require.NoError(t, err)
		require.NotNil(t, j)
		j.Status = "passed"
		j.Result.Analysis = &service.AttributionAnalysis{Prediction: winner}
		require.NoError(t, r.Finish(ctx, j))
		return j
	}
	first := run("first", nil)
	require.Equal(t, "awaiting_confirmation", first.Result.Action)
	require.Equal(t, a.Credentials["model_mapping"], attributionMapping(t, ar, a.ID))
	second := run("second", nil)
	require.Equal(t, "high", second.Result.Action)
	other := []string{"second"}
	require.Equal(t, "awaiting_confirmation", run("second", &other).Result.Action)
	require.Equal(t, "awaiting_confirmation", run("first", nil).Result.Action)
	_, err = r.Enqueue(ctx, []int64{a.ID}, true)
	require.NoError(t, err)
	old, err := r.Claim(ctx)
	require.NoError(t, err)
	c.Default.ExpectedModels = []string{"third"}
	_, err = r.SaveConfig(ctx, c)
	require.NoError(t, err)
	old.Status = "passed"
	require.NoError(t, r.Finish(ctx, old))
	require.Equal(t, "stale", old.Result.Action)
	require.Equal(t, []string{"first", "second"}, old.Snapshot.Policy.ExpectedModels)
	require.Equal(t, "awaiting_confirmation", run("third", nil).Result.Action)
	// Current configuration must never be used to interpret old history.
	_, err = integrationDB.ExecContext(ctx, `UPDATE model_attribution_jobs SET snapshot=jsonb_set(snapshot,'{policy}',(snapshot->'policy')-'expected_models') WHERE id=$1`, first.ID)
	require.NoError(t, err)
	history, err := r.Get(ctx, first.ID)
	require.NoError(t, err)
	require.Equal(t, []string{history.Snapshot.Policy.Model}, service.ExpectedAttributionModels(history.Snapshot.Policy))
}

func TestAttributionInitialExpectedModelsSnapshot(t *testing.T) {
	for _, scenario := range []string{"inherit", "custom", "follow probe"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			r, ar, _ := attributionFixture(t)
			c, err := r.Config(ctx)
			require.NoError(t, err)
			c.NewAccountTests.Pelican = false
			c.NewAccountTests.AttributionModel = "initial-probe"
			c.Default.ExpectedModels = []string{"global"}
			custom := []string{"initial-expected"}
			if scenario == "follow probe" {
				custom = []string{}
			}
			if scenario != "inherit" {
				c.NewAccountTests.AttributionExpectedModels = &custom
			}
			c, err = r.SaveConfig(ctx, c)
			require.NoError(t, err)
			a := initialTestAccount(t, ar, nil)
			replacement := []string{"new-initial"}
			c.NewAccountTests.AttributionExpectedModels = &replacement
			c.NewAccountTests.AttributionModel = "new-probe"
			_, err = r.SaveConfig(ctx, c)
			require.NoError(t, err)
			require.NoError(t, r.EnqueueNewAccounts(ctx))
			j, err := r.Claim(ctx)
			require.NoError(t, err)
			require.NotNil(t, j)
			require.Equal(t, a.ID, j.AccountID)
			require.Equal(t, "initial-probe", j.Snapshot.Policy.Model)
			want := []string{"global"}
			if scenario == "custom" {
				want = []string{"initial-expected"}
			}
			if scenario == "follow probe" {
				want = []string{"initial-probe"}
			}
			require.Equal(t, want, j.Snapshot.Policy.ExpectedModels)
			require.NoError(t, r.EnqueueNewAccounts(ctx))
			n, _ := initialTestCounts(t, a.ID)
			require.Equal(t, 1, n)
		})
	}
}

func TestAttributionExpectedModelsMigration(t *testing.T) {
	ctx := context.Background()
	r, _, _ := attributionFixture(t)
	before, err := r.Config(ctx)
	require.NoError(t, err)
	body, err := migrations.FS.ReadFile("268_attribution_expected_models.sql")
	require.NoError(t, err)
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `CREATE TEMP TABLE account_initial_tests(account_id BIGINT,model TEXT,pelican_model TEXT,processed_at TIMESTAMPTZ) ON COMMIT DROP; INSERT INTO account_initial_tests VALUES(1,'old-probe','pelican',NULL),(2,'old-probe','pelican',NOW())`)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, string(body))
	require.NoError(t, err)
	var count int
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT count(*) FROM account_initial_tests WHERE attribution_expected_models IS NULL AND model='old-probe'`).Scan(&count))
	require.Equal(t, 2, count)
	require.NoError(t, tx.Commit())
	require.NoError(t, ApplyMigrations(ctx, integrationDB))
	after, err := r.Config(ctx)
	require.NoError(t, err)
	a, _ := json.Marshal(before)
	b, _ := json.Marshal(after)
	require.JSONEq(t, string(a), string(b))
}
