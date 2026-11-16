//go:build integration

package repository

import (
	"context"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAttributionLocalPruneAndMaintenanceProtectLeases(t *testing.T) {
	ctx := context.Background()
	r, ar, a := attributionFixture(t)
	b := attributionAPIKeyAccount(t, ar)
	for _, id := range []int64{a.ID, b.ID} {
		_, err := integrationDB.ExecContext(ctx, `INSERT INTO model_attribution_jobs(account_id,account_name,source,status,snapshot,authorization_digest,fence_digest,finished_at)
SELECT $1,'synthetic','manual','cancelled','{}','','',NOW()-INTERVAL '4 days' FROM generate_series(1,120)`, id)
		require.NoError(t, err)
	}
	var leaseID, anchorID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT MIN(id) FROM model_attribution_jobs WHERE account_id=$1`, a.ID).Scan(&leaseID))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT MIN(id) FROM model_attribution_jobs WHERE account_id=$1`, b.ID).Scan(&anchorID))
	claim := "00000000-0000-0000-0000-000000000001"
	_, err := integrationDB.ExecContext(ctx, `UPDATE model_attribution_jobs SET claim_id=$1,lease_until=NOW()+INTERVAL '45 seconds' WHERE id=$2`, claim, leaseID)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE model_attribution_jobs SET status='passed' WHERE id=$1`, anchorID)
	require.NoError(t, err)
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	require.NoError(t, pruneAttribution(ctx, tx, a.ID))
	require.NoError(t, tx.Commit())
	count := func(id int64) int {
		var n int
		require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM model_attribution_jobs WHERE account_id=$1`, id).Scan(&n))
		return n
	}
	require.Equal(t, 101, count(a.ID), "an executing terminal lease is protected")
	require.Equal(t, 120, count(b.ID), "local completion never prunes unrelated accounts")
	require.NoError(t, r.ReleaseExecution(ctx, &service.AttributionJob{ID: leaseID, Claim: claim}))
	_, err = r.MaintainHistory(ctx, 0)
	require.NoError(t, err)
	require.Equal(t, 100, count(a.ID))
	require.Equal(t, 101, count(b.ID), "the old API-key verdict anchor survives maintenance")
}

func TestAttributionCapacityLeaseSurvivesCancellation(t *testing.T) {
	ctx := context.Background()
	r, ar, a := attributionFixture(t)
	b := attributionAPIKeyAccount(t, ar)
	c, err := r.Config(ctx)
	require.NoError(t, err)
	c.MaxConcurrentJobs = 1
	_, err = r.SaveConfig(ctx, c)
	require.NoError(t, err)
	_, err = r.Enqueue(ctx, []int64{a.ID, b.ID}, true)
	require.NoError(t, err)
	j, err := r.Claim(ctx)
	require.NoError(t, err)
	require.NotNil(t, j)
	blocked, err := r.Claim(ctx)
	require.NoError(t, err)
	require.Nil(t, blocked)
	cancelled, err := r.Cancel(ctx, j.ID)
	require.NoError(t, err)
	require.Equal(t, j.Claim, cancelled.Claim)
	active, err := r.Heartbeat(ctx, j)
	require.NoError(t, err)
	require.False(t, active)
	blocked, err = r.Claim(ctx)
	require.NoError(t, err)
	require.Nil(t, blocked, "cancelled probes still consume execution capacity")
	wrong := *j
	wrong.Claim = "00000000-0000-0000-0000-000000000000"
	require.NoError(t, r.ReleaseExecution(ctx, &wrong))
	blocked, err = r.Claim(ctx)
	require.NoError(t, err)
	require.Nil(t, blocked)
	require.NoError(t, r.ReleaseExecution(ctx, j))
	next, err := r.Claim(ctx)
	require.NoError(t, err)
	require.NotNil(t, next)
	require.NotEqual(t, j.ID, next.ID)
	// A lowered limit stops new admission without cancelling the current owner.
	c, err = r.Config(ctx)
	require.NoError(t, err)
	c.MaxConcurrentJobs = 1
	_, err = r.SaveConfig(ctx, c)
	require.NoError(t, err)
	active, err = r.Heartbeat(ctx, next)
	require.NoError(t, err)
	require.True(t, active)
	next.Status = "failed"
	require.NoError(t, r.Finish(ctx, next))
}

func TestAttributionCancelledLeasePreservesAccountExclusion(t *testing.T) {
	ctx := context.Background()
	r, _, a := attributionFixture(t)
	_, err := r.Enqueue(ctx, []int64{a.ID}, true)
	require.NoError(t, err)
	j, err := r.Claim(ctx)
	require.NoError(t, err)
	require.NotNil(t, j)
	_, err = r.Cancel(ctx, j.ID)
	require.NoError(t, err)
	queued, err := r.Enqueue(ctx, []int64{a.ID}, true)
	require.NoError(t, err)
	require.Len(t, queued, 1)
	blocked, err := r.Claim(ctx)
	require.NoError(t, err)
	require.Nil(t, blocked, "same-account probes cannot overlap while cancellation drains")
	require.NoError(t, r.ReleaseExecution(ctx, j))
	next, err := r.Claim(ctx)
	require.NoError(t, err)
	require.NotNil(t, next)
	require.Equal(t, queued[0].ID, next.ID)
	next.Status = "failed"
	require.NoError(t, r.Finish(ctx, next))
}
