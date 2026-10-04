//go:build integration

package repository

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAttributionRateLimitedManualPeriodicAndRecovery(t *testing.T) {
	ctx := context.Background()
	r, ar, a := attributionFixture(t)
	_, err := integrationDB.Exec(`UPDATE accounts SET rate_limit_reset_at=NOW()+interval '1 hour' WHERE id=$1`, a.ID)
	require.NoError(t, err)
	jobs, err := r.Enqueue(ctx, nil, false)
	require.NoError(t, err)
	require.Empty(t, jobs)
	page, err := r.List(ctx, a.ID, 1, 20)
	require.NoError(t, err)
	require.Zero(t, page.Total, "periodic skips do not generate repeated history")
	jobs, err = r.Enqueue(ctx, []int64{a.ID}, true)
	require.NoError(t, err)
	require.Equal(t, "skipped", jobs[0].Status)
	require.Equal(t, "account_rate_limited", jobs[0].Reason)
	claimed, err := r.Claim(ctx)
	require.NoError(t, err)
	require.Nil(t, claimed)
	_, err = integrationDB.Exec(`UPDATE accounts SET rate_limit_reset_at=NOW()-interval '1 second' WHERE id=$1;`, a.ID)
	require.NoError(t, err)
	_, err = integrationDB.Exec(`UPDATE model_attribution_state SET next_due_at=NOW() WHERE account_id=$1`, a.ID)
	require.NoError(t, err)
	jobs, err = r.Enqueue(ctx, nil, false)
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	j, err := r.Claim(ctx)
	require.NoError(t, err)
	require.NotNil(t, j)
	// A limit applied after dispatch blocks retries and also fences late results.
	before := attributionMapping(t, ar, a.ID)
	_, err = integrationDB.Exec(`UPDATE accounts SET rate_limit_reset_at=NOW()+interval '1 hour' WHERE id=$1`, a.ID)
	require.NoError(t, err)
	valid, err := r.Validate(ctx, j)
	require.False(t, valid)
	require.ErrorIs(t, err, service.ErrDiagnosticRateLimited)
	j.Status = "passed"
	require.NoError(t, r.Finish(ctx, j))
	ended, err := r.Get(ctx, j.ID)
	require.NoError(t, err)
	require.Equal(t, "skipped", ended.Status)
	require.Equal(t, "account_rate_limited", ended.Reason)
	require.Equal(t, before, attributionMapping(t, ar, a.ID))
}

func TestAttributionDirectProbeModelLimitAndInitialTests(t *testing.T) {
	ctx := context.Background()
	r, ar, a := attributionFixture(t)
	c, err := r.Config(ctx)
	require.NoError(t, err)
	// The actual probe is limited, even though business mapping sends it elsewhere.
	_, err = integrationDB.Exec(`UPDATE accounts SET credentials=jsonb_set(credentials,'{model_mapping}',jsonb_build_object($2::text,'other')),extra=jsonb_build_object('model_rate_limits',jsonb_build_object($2::text,jsonb_build_object('rate_limit_reset_at','2099-01-01T00:00:00Z'))) WHERE id=$1`, a.ID, c.Default.Model)
	require.NoError(t, err)
	jobs, err := r.Enqueue(ctx, nil, false)
	require.NoError(t, err)
	require.Empty(t, jobs)
	jobs, err = r.Enqueue(ctx, []int64{a.ID}, true)
	require.NoError(t, err)
	require.Equal(t, "account_rate_limited", jobs[0].Reason)
	for _, limit := range []string{"global", "attribution", "pelican"} {
		fresh := initialTestAccount(t, ar, nil)
		if limit == "global" {
			_, err = integrationDB.Exec(`UPDATE accounts SET rate_limit_reset_at=NOW()+interval '1 hour' WHERE id=$1`, fresh.ID)
		} else {
			model := c.NewAccountTests.AttributionModel
			if limit == "pelican" {
				model = c.NewAccountTests.PelicanModel
			}
			_, err = integrationDB.Exec(`UPDATE accounts SET extra=jsonb_build_object('model_rate_limits',jsonb_build_object($2::text,jsonb_build_object('rate_limit_reset_at','2099-01-01T00:00:00Z'))) WHERE id=$1`, fresh.ID, model)
		}
		require.NoError(t, err)
		require.NoError(t, r.EnqueueNewAccounts(ctx))
		attr, pelican := initialTestCounts(t, fresh.ID)
		if limit == "pelican" {
			require.Equal(t, 1, attr)
		} else {
			require.Zero(t, attr)
		}
		if limit == "attribution" {
			require.Equal(t, 1, pelican)
		} else {
			require.Zero(t, pelican)
		}
	}
}

func TestCandyRateLimitedExecutionPersistsSkipped(t *testing.T) {
	ctx := context.Background()
	r := NewAccountCandyTestRepository(integrationDB)
	b := candyIntegrationBatch(t, r, candyIntegrationAccount(t))
	j, err := r.Claim(ctx)
	require.NoError(t, err)
	require.NotNil(t, j)
	j.Status, j.FailureCode = "skipped", "account_rate_limited"
	ok, err := r.Complete(ctx, j)
	require.NoError(t, err)
	require.True(t, ok)
	ended, err := r.GetBatch(ctx, b.ID, 1, 20)
	require.NoError(t, err)
	require.Equal(t, 1, ended.Counts["skipped"])
	require.NotNil(t, ended.FinishedAt)
}
