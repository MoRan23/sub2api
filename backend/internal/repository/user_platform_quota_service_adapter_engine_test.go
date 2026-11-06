//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/ent/userplatformquota"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestUserPlatformQuotaServiceAdapterPreservesEngineComponents(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	userID := mustCreateUserForQuota(t, client)
	repo := NewUserPlatformQuotaRepository(client)
	mustSeedQuotaRow(t, ctx, repo, userID, "openai")
	now := time.Now().UTC().Truncate(time.Microsecond)
	_, err := client.UserPlatformQuota.Update().Where(userplatformquota.UserIDEQ(userID)).
		SetDailyUsageUsd(5).SetWeeklyUsageUsd(7).SetMonthlyUsageUsd(9).
		SetEngineDailyUsageUsd(2).SetEngineWeeklyUsageUsd(3).SetEngineMonthlyUsageUsd(4).
		SetDailyWindowStart(now).SetWeeklyWindowStart(now).SetMonthlyWindowStart(now).Save(ctx)
	require.NoError(t, err)
	adapter := NewUserPlatformQuotaServiceAdapter(repo).(*userPlatformQuotaServiceAdapter)
	for name, load := range map[string]func() (*service.UserPlatformQuotaRecord, error){
		"GetByUserPlatform": func() (*service.UserPlatformQuotaRecord, error) {
			return adapter.GetByUserPlatform(ctx, userID, "openai")
		},
		"LoadCurrentUserPlatformQuotaWindowAnchor": func() (*service.UserPlatformQuotaRecord, error) {
			return adapter.LoadCurrentUserPlatformQuota(ctx, userID, "openai", now)
		},
	} {
		t.Run(name, func(t *testing.T) {
			rec, err := load()
			require.NoError(t, err)
			require.NotNil(t, rec)
			require.Equal(t, []float64{2, 3, 4}, []float64{rec.EngineDailyUsageUSD, rec.EngineWeeklyUsageUSD, rec.EngineMonthlyUsageUSD})
			require.Equal(t, []float64{5, 7, 9}, []float64{rec.DailyUsageUSD, rec.WeeklyUsageUSD, rec.MonthlyUsageUSD})
			require.True(t, rec.MonthlyWindowStart.Equal(now))
		})
	}
}

func newEngineQuotaAdapterCacheFixture(t *testing.T) (int64, UserPlatformQuotaRepository, service.UserPlatformQuotaRepository, *billingCache, *service.BillingCacheService) {
	t.Helper()
	ctx := context.Background()
	userID := mustCreateUserForQuota(t, testEntClient(t))
	repo := NewUserPlatformQuotaRepository(testEntClient(t))
	mustSeedQuotaRow(t, ctx, repo, userID, "openai")
	require.NoError(t, repo.IncrementUsageWithReset(ctx, userID, "openai", 3, time.Now().UTC().Truncate(time.Microsecond)))
	adapter := NewUserPlatformQuotaServiceAdapter(repo)
	cache := &billingCache{rdb: integrationRedis}
	cfg := &config.Config{}
	cfg.Billing.UserPlatformQuotaCacheTTLSeconds = 60
	cfg.Database.UserPlatformQuotaFlusherEnabled = true
	svc := service.NewBillingCacheService(cache, nil, nil, nil, nil, nil, cfg, adapter)
	require.NoError(t, cache.SetUserBalance(ctx, userID, 100))
	t.Cleanup(func() {
		svc.Stop()
		require.NoError(t, cache.DeleteUserPlatformQuotaCache(ctx, userID, "openai"))
		require.NoError(t, cache.InvalidateUserBalance(ctx, userID))
		require.NoError(t, integrationRedis.SRem(ctx, userPlatformQuotaDirtySetKey(), userPlatformQuotaDirtyMember(userID, "openai")).Err())
	})
	return userID, repo, adapter, cache, svc
}

func addEngineQuotaAdapterUsage(t *testing.T, userID int64, cost float64) {
	t.Helper()
	ctx := context.Background()
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer tx.Rollback()
	require.NoError(t, incrementEngineBillingPlatformQuota(ctx, tx, userID, "openai", cost, time.Now()))
	require.NoError(t, tx.Commit())
}

func requireEngineQuotaComponents(t *testing.T, entry *service.UserPlatformQuotaCacheEntry, total, engine float64) {
	t.Helper()
	require.NotNil(t, entry)
	require.Equal(t, []float64{total, total, total}, []float64{entry.DailyUsageUSD, entry.WeeklyUsageUSD, entry.MonthlyUsageUSD})
	require.Equal(t, []float64{engine, engine, engine}, []float64{entry.EngineDailyUsageUSD, entry.EngineWeeklyUsageUSD, entry.EngineMonthlyUsageUSD})
}

func TestUserPlatformQuotaServiceAdapterEngineHotRefreshPreservesOrdinary(t *testing.T) {
	ctx := context.Background()
	userID, _, _, cache, svc := newEngineQuotaAdapterCacheFixture(t)
	require.NoError(t, svc.CheckBillingEligibility(ctx, &service.User{ID: userID}, nil, nil, nil, "openai"))
	// This ordinary dollar is still only in Redis when the Engine transaction commits.
	svc.IncrementUserPlatformQuotaUsage(userID, "openai", 1)
	addEngineQuotaAdapterUsage(t, userID, 2)
	for range 2 {
		require.NoError(t, svc.RefreshUserPlatformQuotaEngineUsage(ctx, userID, "openai"))
		entry, found, err := cache.GetUserPlatformQuotaCache(ctx, userID, "openai")
		require.NoError(t, err)
		require.True(t, found)
		requireEngineQuotaComponents(t, entry, 6, 2)
	}
}

func TestUserPlatformQuotaServiceAdapterEngineColdFillOrdinaryFlush(t *testing.T) {
	ctx := context.Background()
	userID, repo, adapter, cache, svc := newEngineQuotaAdapterCacheFixture(t)
	addEngineQuotaAdapterUsage(t, userID, 2)
	// Exercise the production cache-miss loader, not a manually constructed entry.
	require.NoError(t, svc.CheckBillingEligibility(ctx, &service.User{ID: userID}, nil, nil, nil, "openai"))
	entry, found, err := cache.GetUserPlatformQuotaCache(ctx, userID, "openai")
	require.NoError(t, err)
	require.True(t, found)
	requireEngineQuotaComponents(t, entry, 5, 2)
	svc.IncrementUserPlatformQuotaUsage(userID, "openai", 1)
	entries, err := cache.BatchGetUserPlatformQuotaCache(ctx, []service.UserPlatformQuotaKey{{UserID: userID, Platform: "openai"}})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	entry = entries[0]
	requireEngineQuotaComponents(t, entry, 6, 2)
	// Match the flusher's snapshot handoff and execute the real SQL merge twice.
	snapshot := service.UserPlatformQuotaSnapshot{
		UserID: userID, Platform: "openai",
		DailyUsageUSD: entry.DailyUsageUSD, WeeklyUsageUSD: entry.WeeklyUsageUSD, MonthlyUsageUSD: entry.MonthlyUsageUSD,
		EngineDailyUsageUSD: entry.EngineDailyUsageUSD, EngineWeeklyUsageUSD: entry.EngineWeeklyUsageUSD, EngineMonthlyUsageUSD: entry.EngineMonthlyUsageUSD,
		DailyWindowStart: *entry.DailyWindowStart, WeeklyWindowStart: *entry.WeeklyWindowStart, MonthlyWindowStart: *entry.MonthlyWindowStart,
	}
	for range 2 {
		require.NoError(t, adapter.BatchSnapshotUsage(ctx, []service.UserPlatformQuotaSnapshot{snapshot}, time.Now()))
		rec, err := repo.GetByUserPlatform(ctx, userID, "openai")
		require.NoError(t, err)
		require.NotNil(t, rec)
		require.Equal(t, []float64{6, 6, 6}, []float64{rec.DailyUsageUSD, rec.WeeklyUsageUSD, rec.MonthlyUsageUSD})
		require.Equal(t, []float64{2, 2, 2}, []float64{rec.EngineDailyUsageUSD, rec.EngineWeeklyUsageUSD, rec.EngineMonthlyUsageUSD})
	}
}
