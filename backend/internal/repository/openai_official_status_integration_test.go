//go:build integration

package repository

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestOfficialStatusStoreRecoveryAndLease(t *testing.T) {
	ctx := context.Background()
	rdb := testRedis(t)
	t.Cleanup(func() { rdb.Del(ctx, officialStatusSnapshotKey, leaderLockKeyPrefix+"test:official-status:lease") })
	store := NewOfficialStatusStore(rdb)
	now := time.Now().UTC()
	snapshot := &service.OfficialStatusSnapshot{Version: 1, CheckedAt: now, Groups: []service.OfficialStatusGroup{
		{ID: "apis", Name: "APIs", Status: "operational", FetchedAt: &now},
		{ID: "chatgpt", Name: "ChatGPT", Status: "partial_outage", FetchedAt: &now, Stale: true},
		{ID: "codex", Name: "Codex", Status: "unknown", Stale: true},
	}, Incidents: []service.OfficialStatusIncident{{ID: "test", Title: "Test", URL: "https://status.openai.com/incidents/test", Status: "monitoring", Groups: []string{"apis"}, Components: []string{"Responses"}, UpdatedAt: now, Summary: "Checking"}}, IncidentsFetchedAt: &now, IncidentsStale: true}
	require.NoError(t, store.Save(ctx, snapshot))
	restored, err := NewOfficialStatusStore(rdb).Load(ctx)
	require.NoError(t, err)
	require.Equal(t, snapshot, restored)
	ttl, err := rdb.TTL(ctx, officialStatusSnapshotKey).Result()
	require.NoError(t, err)
	require.Equal(t, time.Duration(-1), ttl)
	for _, raw := range []string{`{"version":2}`, `{`, strings.Repeat("x", 1<<20+1)} {
		require.NoError(t, rdb.Set(ctx, officialStatusSnapshotKey, raw, 0).Err())
		restored, err = store.Load(ctx)
		require.NoError(t, err)
		require.Nil(t, restored)
	}
	require.NoError(t, store.Save(ctx, snapshot))
	restored, err = store.Load(ctx)
	require.NoError(t, err)
	require.Equal(t, snapshot, restored)
	locks := NewLeaderLockCache(rdb)
	var winners atomic.Int32
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			acquired, err := locks.TryAcquireLeaderLock(ctx, "test:official-status:lease", fmt.Sprint(i), 120*time.Second)
			if err == nil && acquired {
				winners.Add(1)
			}
		})
	}
	wg.Wait()
	require.EqualValues(t, 1, winners.Load())
	ttl, err = rdb.PTTL(ctx, leaderLockKeyPrefix+"test:official-status:lease").Result()
	require.NoError(t, err)
	require.Greater(t, ttl, 119*time.Second)
}
