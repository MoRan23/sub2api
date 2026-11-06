//go:build unit

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestUserPlatformQuotaEngineMergePreservesOrdinaryAndDeduplicates(t *testing.T) {
	c, _ := newMiniRedisCache(t)
	ctx := context.Background()
	start := time.Date(2026, 11, 6, 12, 30, 0, 123456000, time.UTC)
	entry := &service.UserPlatformQuotaCacheEntry{SchemaVersion: service.UserPlatformQuotaCacheSchemaV1,
		DailyUsageUSD: 10, WeeklyUsageUSD: 10, MonthlyUsageUSD: 10,
		EngineDailyUsageUSD: 2, EngineWeeklyUsageUSD: 2, EngineMonthlyUsageUSD: 2,
		DailyWindowStart: &start, WeeklyWindowStart: &start, MonthlyWindowStart: &start}
	require.NoError(t, c.SetUserPlatformQuotaCache(ctx, 1, "openai", entry, time.Hour))
	// Ordinary cost is dirty and has not reached PostgreSQL yet.
	require.NoError(t, c.IncrUserPlatformQuotaUsageCache(ctx, 1, "openai", 3, time.Hour, true))
	committed := *entry
	committed.EngineDailyUsageUSD, committed.EngineWeeklyUsageUSD, committed.EngineMonthlyUsageUSD = 5, 5, 5
	for i := 0; i < 2; i++ {
		require.NoError(t, c.MergeUserPlatformQuotaEngineUsage(ctx, 1, "openai", &committed, time.Hour))
	}
	// An older refresh and an earlier DB refill must not roll either part back.
	require.NoError(t, c.MergeUserPlatformQuotaEngineUsage(ctx, 1, "openai", entry, time.Hour))
	require.NoError(t, c.SetUserPlatformQuotaCache(ctx, 1, "openai", entry, time.Hour))
	got, ok, err := c.GetUserPlatformQuotaCache(ctx, 1, "openai")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, float64(16), got.DailyUsageUSD)
	require.Equal(t, float64(16), got.WeeklyUsageUSD)
	require.Equal(t, float64(16), got.MonthlyUsageUSD)
	require.Equal(t, float64(5), got.EngineDailyUsageUSD)
	require.True(t, got.MonthlyWindowStart.Equal(start), "preserve PostgreSQL microsecond window identity")
	keys, err := c.PopDirtyUserPlatformQuotaKeys(ctx, 10)
	require.NoError(t, err)
	require.Equal(t, []service.UserPlatformQuotaKey{{UserID: 1, Platform: "openai"}}, keys)
}

func TestUserPlatformQuotaEngineMergeWindowResetRejectsLateSnapshot(t *testing.T) {
	c, _ := newMiniRedisCache(t)
	ctx := context.Background()
	start := time.Now().UTC().Truncate(time.Microsecond)
	old := &service.UserPlatformQuotaCacheEntry{SchemaVersion: service.UserPlatformQuotaCacheSchemaV1,
		DailyUsageUSD: 9, EngineDailyUsageUSD: 4, DailyWindowStart: &start}
	require.NoError(t, c.SetUserPlatformQuotaCache(ctx, 1, "openai", old, time.Hour))
	reset := start.Add(time.Microsecond)
	newWindow := &service.UserPlatformQuotaCacheEntry{SchemaVersion: service.UserPlatformQuotaCacheSchemaV1, DailyWindowStart: &reset}
	require.NoError(t, c.MergeUserPlatformQuotaEngineUsage(ctx, 1, "openai", newWindow, time.Hour))
	require.NoError(t, c.IncrUserPlatformQuotaUsageCache(ctx, 1, "openai", 2, time.Hour, true))
	require.NoError(t, c.MergeUserPlatformQuotaEngineUsage(ctx, 1, "openai", old, time.Hour))
	require.NoError(t, c.SetUserPlatformQuotaCache(ctx, 1, "openai", old, time.Hour))
	got, _, err := c.GetUserPlatformQuotaCache(ctx, 1, "openai")
	require.NoError(t, err)
	require.Equal(t, float64(2), got.DailyUsageUSD)
	require.Zero(t, got.EngineDailyUsageUSD)
	require.True(t, got.DailyWindowStart.Equal(reset))
}

func TestUserPlatformQuotaEngineMergeMissingCacheRemainsMissing(t *testing.T) {
	c, _ := newMiniRedisCache(t)
	ctx := context.Background()
	start := time.Now().UTC().Truncate(time.Microsecond)
	require.NoError(t, c.MergeUserPlatformQuotaEngineUsage(ctx, 1, "openai", &service.UserPlatformQuotaCacheEntry{SchemaVersion: service.UserPlatformQuotaCacheSchemaV1, EngineDailyUsageUSD: 3, DailyWindowStart: &start}, time.Hour))
	_, found, err := c.GetUserPlatformQuotaCache(ctx, 1, "openai")
	require.NoError(t, err)
	require.False(t, found, "a partial Engine snapshot must not create a cache without limits/ordinary usage")
	// A DB cache-fill started before the Engine transaction must retain the
	// committed component even though the full cache was absent during refresh.
	limit := 10.0
	require.NoError(t, c.SetUserPlatformQuotaCache(ctx, 1, "openai", &service.UserPlatformQuotaCacheEntry{SchemaVersion: service.UserPlatformQuotaCacheSchemaV1, DailyUsageUSD: 2, DailyLimitUSD: &limit, DailyWindowStart: &start}, time.Hour))
	got, found, err := c.GetUserPlatformQuotaCache(ctx, 1, "openai")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, float64(5), got.DailyUsageUSD)
	require.Equal(t, float64(3), got.EngineDailyUsageUSD)
}

func TestUserPlatformQuotaCacheNamespaceDoesNotMixLegacySnapshots(t *testing.T) {
	c, _ := newMiniRedisCache(t)
	ctx := context.Background()
	legacyHash, legacyDirty := "billing:user_platform_quota:1:openai", "billing:upq:dirty"
	require.NoError(t, c.rdb.HSet(ctx, legacyHash, "schema_version", 1, "daily_usage", 99,
		"daily_window_start", 1700000000).Err())
	require.NoError(t, c.rdb.SAdd(ctx, legacyDirty, "1:openai").Err())
	_, found, err := c.GetUserPlatformQuotaCache(ctx, 1, "openai")
	require.NoError(t, err)
	require.False(t, found)
	keys, err := c.PopDirtyUserPlatformQuotaKeys(ctx, 10)
	require.NoError(t, err)
	require.Empty(t, keys, "the new flusher must not consume the legacy dirty set")
	start := time.Now().UTC().Truncate(time.Microsecond)
	entry := &service.UserPlatformQuotaCacheEntry{SchemaVersion: service.UserPlatformQuotaCacheSchemaV1,
		DailyUsageUSD: 7, DailyWindowStart: &start}
	require.NoError(t, c.SetUserPlatformQuotaCache(ctx, 1, "openai", entry, time.Hour))
	require.NoError(t, c.IncrUserPlatformQuotaUsageCache(ctx, 1, "openai", 2, time.Hour, true))
	entry.EngineDailyUsageUSD = 3
	require.NoError(t, c.MergeUserPlatformQuotaEngineUsage(ctx, 1, "openai", entry, time.Hour))
	got, found, err := c.GetUserPlatformQuotaCache(ctx, 1, "openai")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, float64(12), got.DailyUsageUSD)
	keys, err = c.PopDirtyUserPlatformQuotaKeys(ctx, 10)
	require.NoError(t, err)
	require.Equal(t, []service.UserPlatformQuotaKey{{UserID: 1, Platform: "openai"}}, keys)
	require.NoError(t, c.ReaddDirtyUserPlatformQuotaKeys(ctx, keys))
	entries, err := c.BatchGetUserPlatformQuotaCache(ctx, keys)
	require.NoError(t, err)
	require.Equal(t, float64(12), entries[0].DailyUsageUSD)
	require.NoError(t, c.DeleteUserPlatformQuotaCache(ctx, 1, "openai"))
	require.Equal(t, "99", c.rdb.HGet(ctx, legacyHash, "daily_usage").Val())
	require.Equal(t, int64(1), c.rdb.SCard(ctx, legacyDirty).Val())
	require.Equal(t, int64(1), c.rdb.SCard(ctx, userPlatformQuotaDirtySetKey()).Val())
}
