//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
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
	snapshot := officialStatusHistoryStoreTestSnapshot(now)
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

func TestOfficialStatusStoreRejectsInvalidHistoryAndRecovers(t *testing.T) {
	ctx := context.Background()
	rdb := testRedis(t)
	t.Cleanup(func() { rdb.Del(ctx, officialStatusSnapshotKey) })
	store := NewOfficialStatusStore(rdb)
	now := time.Now().UTC()
	snapshot := officialStatusHistoryStoreTestSnapshot(now)
	for _, tc := range []struct {
		name   string
		mutate func(*service.OfficialStatusSnapshot)
	}{
		{"old_version", func(s *service.OfficialStatusSnapshot) { s.Version = 1 }},
		{"nil_days", func(s *service.OfficialStatusSnapshot) { s.Groups[2].History.Days = nil }},
		{"short_history", func(s *service.OfficialStatusSnapshot) { s.Groups[0].History.Days = s.Groups[0].History.Days[1:] }},
		{"oversized_history", func(s *service.OfficialStatusSnapshot) {
			s.Groups[0].History.Days = append(s.Groups[0].History.Days, s.Groups[0].History.Days[90])
		}},
		{"nonconsecutive_dates", func(s *service.OfficialStatusSnapshot) {
			s.Groups[0].History.Days[45].Date = s.Groups[0].History.Days[44].Date
		}},
		{"wrong_tail_date", func(s *service.OfficialStatusSnapshot) {
			s.Groups[0].History.Days[90].Date = now.AddDate(0, 0, 1).Format(time.DateOnly)
		}},
		{"invalid_status", func(s *service.OfficialStatusSnapshot) { s.Groups[0].History.Days[0].Status = "invalid" }},
		{"future_fetched_at", func(s *service.OfficialStatusSnapshot) {
			future := now.Add(time.Second)
			s.Groups[0].History.FetchedAt = &future
		}},
		{"missing_fetched_at", func(s *service.OfficialStatusSnapshot) { s.Groups[0].History.FetchedAt = nil }},
		{"empty_not_stale", func(s *service.OfficialStatusSnapshot) { s.Groups[2].History.Stale = false }},
		{"empty_with_uptime", func(s *service.OfficialStatusSnapshot) {
			s.Groups[2].History.UptimePercent = s.Groups[0].History.UptimePercent
		}},
		{"empty_with_fetched_at", func(s *service.OfficialStatusSnapshot) { s.Groups[2].History.FetchedAt = &now }},
		{"negative_uptime", func(s *service.OfficialStatusSnapshot) { *s.Groups[0].History.UptimePercent = -0.01 }},
		{"uptime_over_100", func(s *service.OfficialStatusSnapshot) { *s.Groups[0].History.UptimePercent = 100.01 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, store.Save(ctx, snapshot))
			invalid := officialStatusHistoryStoreTestSnapshot(now)
			tc.mutate(invalid)
			require.Error(t, store.Save(ctx, invalid))
			restored, err := store.Load(ctx)
			require.NoError(t, err)
			require.Equal(t, snapshot, restored)

			raw, err := json.Marshal(invalid)
			require.NoError(t, err)
			require.NoError(t, rdb.Set(ctx, officialStatusSnapshotKey, raw, 0).Err())
			restored, err = store.Load(ctx)
			require.NoError(t, err)
			require.Nil(t, restored)

			require.NoError(t, store.Save(ctx, snapshot))
			restored, err = NewOfficialStatusStore(rdb).Load(ctx)
			require.NoError(t, err)
			require.Equal(t, snapshot, restored)
		})
	}
	for _, uptime := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		invalid := officialStatusHistoryStoreTestSnapshot(now)
		invalid.Groups[0].History.UptimePercent = &uptime
		require.Error(t, store.Save(ctx, invalid))
	}
	restored, err := store.Load(ctx)
	require.NoError(t, err)
	require.Equal(t, snapshot, restored)
}

func officialStatusHistoryStoreTestSnapshot(now time.Time) *service.OfficialStatusSnapshot {
	snapshot := &service.OfficialStatusSnapshot{Version: 2, CheckedAt: now, Groups: []service.OfficialStatusGroup{
		{ID: "apis", Name: "APIs", Status: "operational", FetchedAt: &now},
		{ID: "chatgpt", Name: "ChatGPT", Status: "partial_outage", FetchedAt: &now, Stale: true},
		{ID: "codex", Name: "Codex", Status: "unknown", Stale: true, History: service.OfficialStatusHistory{Days: []service.OfficialStatusDay{}, Stale: true}},
	}, Incidents: []service.OfficialStatusIncident{{ID: "test", Title: "Test", URL: "https://status.openai.com/incidents/test", Status: "monitoring", Groups: []string{"apis"}, Components: []string{"Responses"}, UpdatedAt: now, Summary: "Checking"}}, IncidentsFetchedAt: &now, IncidentsStale: true}
	for i := range 2 {
		history := &snapshot.Groups[i].History
		history.Days = make([]service.OfficialStatusDay, 91)
		history.FetchedAt = &now
		history.Stale = i == 1
		for day := range history.Days {
			history.Days[day] = service.OfficialStatusDay{Date: now.UTC().AddDate(0, 0, day-90).Format(time.DateOnly), Status: "operational"}
		}
	}
	uptime := 99.5
	snapshot.Groups[0].History.UptimePercent = &uptime
	snapshot.Groups[1].History.Days[88].Status = "partial_outage"
	return snapshot
}
