//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestUserPlatformQuotaEngineSnapshotMergeAndReset(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	userID := mustCreateUserForQuota(t, client)
	repo := NewUserPlatformQuotaRepository(client)
	mustSeedQuotaRow(t, ctx, repo, userID, "openai")
	now := time.Now().UTC().Truncate(time.Microsecond)
	require.NoError(t, repo.IncrementUsageWithReset(ctx, userID, "openai", 3, now))
	rec, err := repo.GetByUserPlatform(ctx, userID, "openai")
	require.NoError(t, err)
	snapshot := UserPlatformQuotaSnapshot{UserID: userID, Platform: "openai", DailyUsageUSD: 5, WeeklyUsageUSD: 5, MonthlyUsageUSD: 5,
		DailyWindowStart: *rec.DailyWindowStart, WeeklyWindowStart: *rec.WeeklyWindowStart, MonthlyWindowStart: *rec.MonthlyWindowStart}
	addEngine := func(cost float64, at time.Time) {
		tx, err := integrationDB.BeginTx(ctx, nil)
		require.NoError(t, err)
		defer tx.Rollback()
		require.NoError(t, incrementEngineBillingPlatformQuota(ctx, tx, userID, "openai", cost, at))
		require.NoError(t, tx.Commit())
	}
	addEngine(2, now)
	require.NoError(t, repo.BatchSnapshotUsage(ctx, []UserPlatformQuotaSnapshot{snapshot}, now))
	rec, err = repo.GetByUserPlatform(ctx, userID, "openai")
	require.NoError(t, err)
	require.Equal(t, float64(7), rec.DailyUsageUSD)
	require.Equal(t, float64(2), rec.EngineDailyUsageUSD)
	// A refreshed snapshot includes Engine 2 and ordinary 7. Then another
	// event commits before the snapshot; only ordinary 7 is merged.
	snapshot.DailyUsageUSD, snapshot.WeeklyUsageUSD, snapshot.MonthlyUsageUSD = 9, 9, 9
	snapshot.EngineDailyUsageUSD, snapshot.EngineWeeklyUsageUSD, snapshot.EngineMonthlyUsageUSD = 2, 2, 2
	addEngine(3, now)
	for i := 0; i < 2; i++ {
		require.NoError(t, repo.BatchSnapshotUsage(ctx, []UserPlatformQuotaSnapshot{snapshot}, now))
	}
	rec, err = repo.GetByUserPlatform(ctx, userID, "openai")
	require.NoError(t, err)
	require.Equal(t, float64(12), rec.DailyUsageUSD)
	require.Equal(t, float64(12), rec.WeeklyUsageUSD)
	require.Equal(t, float64(12), rec.MonthlyUsageUSD)
	// Admin reset advances the exact window and clears both components. An
	// already-read flusher snapshot cannot restore either pre-reset amount.
	reset := now.Add(time.Second)
	for _, window := range []string{"daily", "weekly", "monthly"} {
		require.NoError(t, repo.ResetExpiredWindow(ctx, userID, "openai", window, reset))
	}
	require.NoError(t, repo.BatchSnapshotUsage(ctx, []UserPlatformQuotaSnapshot{snapshot}, reset))
	addEngine(1, reset)
	require.NoError(t, repo.IncrementUsageWithReset(ctx, userID, "openai", 2, reset))
	rec, err = repo.GetByUserPlatform(ctx, userID, "openai")
	require.NoError(t, err)
	require.Equal(t, float64(3), rec.DailyUsageUSD)
	require.Equal(t, float64(3), rec.WeeklyUsageUSD)
	require.Equal(t, float64(3), rec.MonthlyUsageUSD)
	require.Equal(t, float64(1), rec.EngineDailyUsageUSD)
	require.True(t, rec.DailyWindowStart.Equal(reset))
	// A genuinely newer window drops both old components.
	next := reset.Add(31 * 24 * time.Hour)
	addEngine(4, next)
	rec, err = repo.GetByUserPlatform(ctx, userID, "openai")
	require.NoError(t, err)
	require.Equal(t, float64(4), rec.MonthlyUsageUSD)
	require.Equal(t, float64(4), rec.EngineMonthlyUsageUSD)
}

func TestUserPlatformQuotaCanonicalWindowAnchor(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	userID := mustCreateUserForQuota(t, client)
	repo := NewUserPlatformQuotaRepository(client)
	mustSeedQuotaRow(t, ctx, repo, userID, "openai")
	adapter := NewUserPlatformQuotaServiceAdapter(repo).(*userPlatformQuotaServiceAdapter)
	now := time.Now().UTC().Truncate(time.Microsecond)
	first, err := adapter.LoadCurrentUserPlatformQuota(ctx, userID, "openai", now)
	require.NoError(t, err)
	second, err := adapter.LoadCurrentUserPlatformQuota(ctx, userID, "openai", now.Add(time.Second))
	require.NoError(t, err)
	require.True(t, first.MonthlyWindowStart.Equal(*second.MonthlyWindowStart))
	require.True(t, first.MonthlyWindowStart.Equal(now))
}
