//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAttributionActionsRefreshOldAndNewGroupCaches(t *testing.T) {
	ctx := context.Background()
	r, ar, a := attributionFixture(t)
	old := attributionActionGroup(t, "actions-cache-original")
	target := attributionActionGroup(t, "actions-cache-target")
	require.NoError(t, ar.BindGroups(ctx, a.ID, []int64{old}))
	// Empty scheduler snapshots intentionally report cache misses. Keep a
	// second account in both groups so a hit proves the old membership was
	// actually removed rather than merely invalidated for database fallback.
	sentinel := attributionAPIKeyAccount(t, ar)
	require.NoError(t, ar.BindGroups(ctx, sentinel.ID, []int64{old, target}))
	attributionSetActions(t, r, []int64{target}, []int64{old})
	cache := NewSchedulerCache(testRedis(t))
	scheduler := service.NewSchedulerSnapshotService(cache, NewSchedulerOutboxRepository(integrationDB), ar,
		NewGroupRepository(integrationEntClient, integrationDB), &config.Config{Gateway: config.GatewayConfig{
			Scheduling: config.GatewaySchedulingConfig{OutboxPollIntervalSeconds: 1, DbFallbackEnabled: true},
		}})
	scheduler.Start()
	defer scheduler.Stop()
	assertAccount := func(c *assert.CollectT, groupID int64, want bool) {
		accounts, hit, err := cache.GetSnapshot(ctx, service.SchedulerBucket{
			GroupID: groupID, Platform: service.PlatformOpenAI, Mode: service.SchedulerModeSingle,
		})
		if !assert.NoError(c, err) || !assert.True(c, hit, "group %d cache must be populated", groupID) {
			return
		}
		found := false
		for _, candidate := range accounts {
			found = found || candidate.ID == a.ID
		}
		assert.Equal(c, want, found, "group %d membership", groupID)
	}
	// Let all setup events settle before testing the attribution event itself.
	var setupEvent int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COALESCE(max(id),0) FROM scheduler_outbox`).Scan(&setupEvent))
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		watermark, err := cache.GetOutboxWatermark(ctx)
		assert.NoError(c, err)
		assert.GreaterOrEqual(c, watermark, setupEvent)
		assertAccount(c, old, true)
		assertAccount(c, target, false)
	}, 10*time.Second, 50*time.Millisecond)

	j := attributionRun(t, r, a.ID, "passed")
	require.Equal(t, "high", j.Result.Action)
	var actionEvent int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT max(id) FROM scheduler_outbox WHERE account_id=$1`, a.ID).Scan(&actionEvent))
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		watermark, err := cache.GetOutboxWatermark(ctx)
		cached, accountErr := cache.GetAccount(ctx, a.ID)
		assert.NoError(c, err)
		assert.GreaterOrEqual(c, watermark, actionEvent)
		if assert.NoError(c, accountErr) && assert.NotNil(c, cached) {
			assert.Equal(c, []int64{target}, cached.GroupIDs)
			assert.Equal(c, "high-target", cached.GetModelMapping()["alias"])
		}
		assertAccount(c, old, false)
		assertAccount(c, target, true)
	}, 10*time.Second, 50*time.Millisecond)
}
