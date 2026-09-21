//go:build integration

package repository

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

var dailyRootTestOSFamilies = []string{"windows", "macos", "linux"}

func newDailyOSRootTestAccount(t *testing.T, parentID *int64) int64 {
	t.Helper()
	dimension := service.QuotaDimensionGlobal
	if parentID != nil {
		dimension = service.QuotaDimensionSpark
	}
	a := mustCreateAccount(t, testEntClient(t), &service.Account{
		Name: "daily-os-roots-" + t.Name(), Platform: service.PlatformOpenAI,
		Type: service.AccountTypeOAuth, ParentAccountID: parentID, QuotaDimension: dimension,
	})
	t.Cleanup(func() {
		_, err := integrationDB.ExecContext(context.Background(), `DELETE FROM accounts WHERE id = $1`, a.ID)
		require.NoError(t, err, "delete daily-root test account and its pools")
	})
	return a.ID
}

func dailyOSRootTestRepository(t *testing.T) (service.OAuthDailySessionRepository, service.OAuthDailySessionOSRepository, service.OAuthDailySessionPoolReader) {
	t.Helper()
	repo := NewOpenAIOAuthDailySessionRepository(testEntClient(t))
	osRepo, ok := repo.(service.OAuthDailySessionOSRepository)
	require.True(t, ok, "repository must expose OS-specific daily roots")
	reader, ok := repo.(service.OAuthDailySessionPoolReader)
	require.True(t, ok, "repository must retain its read-only pool lookup")
	return repo, osRepo, reader
}

func requireDailyOSRoots(t *testing.T, pool service.OAuthDailySessionPool) {
	t.Helper()
	require.Len(t, pool.OSRoots, 3)
	require.Contains(t, dailyRootTestOSFamilies, pool.DefaultOS)
	seen := make(map[string]bool, 6)
	for slot, osFamily := range dailyRootTestOSFamilies {
		roots, ok := pool.OSRoots[osFamily]
		require.True(t, ok, "missing %s roots", osFamily)
		require.Equal(t, pool.StreamSessionIDs[slot], roots.StreamSessionID, "legacy stream slot must map to %s", osFamily)
		for _, root := range []string{roots.StreamSessionID, roots.SyncSessionID} {
			id, err := uuid.Parse(root)
			require.NoError(t, err, "%s root must be a UUID", osFamily)
			require.Equal(t, uuid.Version(7), id.Version())
			require.False(t, seen[root], "all six roots must be distinct")
			seen[root] = true
		}
	}
	require.Equal(t, pool.SyncSessionID, pool.OSRoots[pool.DefaultOS].SyncSessionID, "the legacy sync root belongs to the first default OS")
}

func dailyOSRootTestCounts(t *testing.T, accountID int64) (pools, roots int) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`SELECT count(*) FROM openai_oauth_daily_session_pools WHERE account_id = $1`, accountID).Scan(&pools))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*)
		FROM openai_oauth_daily_os_roots r
		JOIN openai_oauth_daily_session_pools p ON p.id = r.pool_id
		WHERE p.account_id = $1`, accountID).Scan(&roots))
	return pools, roots
}

func TestOAuthDailyOSRootsUpgradePreservesLegacyRoots(t *testing.T) {
	for _, defaultOS := range dailyRootTestOSFamilies {
		t.Run(defaultOS, func(t *testing.T) {
			ctx := context.Background()
			repo, osRepo, reader := dailyOSRootTestRepository(t)
			accountID := newDailyOSRootTestAccount(t, nil)
			now := time.Date(2026, 9, 21, 4, 0, 0, 0, time.UTC)
			legacy, err := repo.GetOrCreateOAuthDailySessionPool(ctx, accountID, now)
			require.NoError(t, err)
			require.Empty(t, legacy.OSRoots)
			_, rootRows := dailyOSRootTestCounts(t, accountID)
			require.Zero(t, rootRows, "the legacy API must not initialize OS roots")

			upgraded, err := osRepo.GetOrCreateOAuthDailySessionPoolForOS(ctx, accountID, defaultOS, now)
			require.NoError(t, err)
			requireDailyOSRoots(t, upgraded)
			require.Equal(t, defaultOS, upgraded.DefaultOS)
			require.Equal(t, legacy.AccountID, upgraded.AccountID)
			require.Equal(t, legacy.BusinessDate, upgraded.BusinessDate)
			require.Equal(t, legacy.Generation, upgraded.Generation)
			require.Equal(t, legacy.StreamSessionIDs, upgraded.StreamSessionIDs)
			require.Equal(t, legacy.SyncSessionID, upgraded.SyncSessionID)
			for _, laterDefault := range dailyRootTestOSFamilies {
				again, getErr := osRepo.GetOrCreateOAuthDailySessionPoolForOS(ctx, accountID, laterDefault, now)
				require.NoError(t, getErr)
				require.Equal(t, upgraded, again, "later requests must not reassign the legacy sync root")
			}
			listed, err := reader.ListOAuthDailySessionPools(ctx, []int64{accountID}, now)
			require.NoError(t, err)
			require.Equal(t, upgraded, listed[accountID])
			poolRows, rootRows := dailyOSRootTestCounts(t, accountID)
			require.Equal(t, 1, poolRows)
			require.Equal(t, 3, rootRows)
		})
	}
}

func TestOAuthDailyOSRootsConcurrentInitializationHasOneWinner(t *testing.T) {
	for _, legacyFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("legacy=%t", legacyFirst), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			repo, osRepo, _ := dailyOSRootTestRepository(t)
			accountID := newDailyOSRootTestAccount(t, nil)
			now := time.Date(2026, 9, 21, 4, 0, 0, 0, time.UTC)
			var legacy service.OAuthDailySessionPool
			if legacyFirst {
				var err error
				legacy, err = repo.GetOrCreateOAuthDailySessionPool(ctx, accountID, now)
				require.NoError(t, err)
			}

			const workers = 12
			results := make([]service.OAuthDailySessionPool, workers)
			errors := make([]error, workers)
			start := make(chan struct{})
			var wg sync.WaitGroup
			for i := range workers {
				wg.Add(1)
				go func(index int) {
					defer wg.Done()
					<-start
					results[index], errors[index] = osRepo.GetOrCreateOAuthDailySessionPoolForOS(ctx, accountID, dailyRootTestOSFamilies[index%3], now)
				}(i)
			}
			close(start)
			wg.Wait()
			for i := range workers {
				require.NoError(t, errors[i], "worker %d", i)
				requireDailyOSRoots(t, results[i])
				require.Equal(t, results[0], results[i], "concurrent requests must return the same complete winning pool")
			}
			if legacyFirst {
				require.Equal(t, legacy.Generation, results[0].Generation)
				require.Equal(t, legacy.StreamSessionIDs, results[0].StreamSessionIDs)
				require.Equal(t, legacy.SyncSessionID, results[0].SyncSessionID)
			}
			poolRows, rootRows := dailyOSRootTestCounts(t, accountID)
			require.Equal(t, 1, poolRows)
			require.Equal(t, 3, rootRows)
		})
	}
}

func TestOAuthDailyOSRootsRotateAtUTC8Midnight(t *testing.T) {
	ctx := context.Background()
	_, osRepo, reader := dailyOSRootTestRepository(t)
	accountID := newDailyOSRootTestAccount(t, nil)
	beforeMidnight := time.Date(2026, 9, 21, 15, 59, 59, 0, time.UTC)
	afterMidnight := beforeMidnight.Add(time.Second)
	before, err := osRepo.GetOrCreateOAuthDailySessionPoolForOS(ctx, accountID, "windows", beforeMidnight)
	require.NoError(t, err)
	after, err := osRepo.GetOrCreateOAuthDailySessionPoolForOS(ctx, accountID, "linux", afterMidnight)
	require.NoError(t, err)
	requireDailyOSRoots(t, before)
	requireDailyOSRoots(t, after)
	require.Equal(t, "2026-09-21", before.BusinessDate)
	require.Equal(t, "2026-09-22", after.BusinessDate)
	require.Equal(t, "windows", before.DefaultOS)
	require.Equal(t, "linux", after.DefaultOS)
	require.NotEqual(t, before.Generation, after.Generation)
	previousRoots := make(map[string]bool, 6)
	for _, roots := range before.OSRoots {
		previousRoots[roots.StreamSessionID], previousRoots[roots.SyncSessionID] = true, true
	}
	for _, roots := range after.OSRoots {
		require.False(t, previousRoots[roots.StreamSessionID], "stream roots must rotate with the business date")
		require.False(t, previousRoots[roots.SyncSessionID], "sync roots must rotate with the business date")
	}
	listed, err := reader.ListOAuthDailySessionPools(ctx, []int64{accountID}, afterMidnight)
	require.NoError(t, err)
	require.Equal(t, map[int64]service.OAuthDailySessionPool{accountID: after}, listed)
	poolRows, rootRows := dailyOSRootTestCounts(t, accountID)
	require.Equal(t, 2, poolRows)
	require.Equal(t, 6, rootRows)
}

func TestOAuthDailyOSRootsShadowSharesOwnerPool(t *testing.T) {
	ctx := context.Background()
	_, osRepo, _ := dailyOSRootTestRepository(t)
	ownerID := newDailyOSRootTestAccount(t, nil)
	shadowID := newDailyOSRootTestAccount(t, &ownerID)
	now := time.Date(2026, 9, 21, 4, 0, 0, 0, time.UTC)
	shadow, err := osRepo.GetOrCreateOAuthDailySessionPoolForOS(ctx, shadowID, "macos", now)
	require.NoError(t, err)
	owner, err := osRepo.GetOrCreateOAuthDailySessionPoolForOS(ctx, ownerID, "linux", now)
	require.NoError(t, err)
	requireDailyOSRoots(t, shadow)
	require.Equal(t, ownerID, shadow.AccountID)
	require.Equal(t, "macos", shadow.DefaultOS)
	require.Equal(t, shadow, owner)
	poolRows, rootRows := dailyOSRootTestCounts(t, shadowID)
	require.Zero(t, poolRows)
	require.Zero(t, rootRows)
	poolRows, rootRows = dailyOSRootTestCounts(t, ownerID)
	require.Equal(t, 1, poolRows)
	require.Equal(t, 3, rootRows)
}

func TestOAuthDailyOSRootsListDoesNotCreateOrUpgradePools(t *testing.T) {
	ctx := context.Background()
	repo, _, reader := dailyOSRootTestRepository(t)
	legacyID := newDailyOSRootTestAccount(t, nil)
	missingID := newDailyOSRootTestAccount(t, nil)
	now := time.Date(2026, 9, 21, 4, 0, 0, 0, time.UTC)
	legacy, err := repo.GetOrCreateOAuthDailySessionPool(ctx, legacyID, now)
	require.NoError(t, err)
	var updatedBefore, updatedAfter time.Time
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`SELECT updated_at FROM openai_oauth_daily_session_pools WHERE account_id = $1`, legacyID).Scan(&updatedBefore))
	listed, err := reader.ListOAuthDailySessionPools(ctx, []int64{legacyID, missingID}, now)
	require.NoError(t, err)
	require.Equal(t, map[int64]service.OAuthDailySessionPool{legacyID: legacy}, listed)
	require.Empty(t, listed[legacyID].OSRoots)
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`SELECT updated_at FROM openai_oauth_daily_session_pools WHERE account_id = $1`, legacyID).Scan(&updatedAfter))
	require.Equal(t, updatedBefore, updatedAfter, "admin lookup must not touch pool timestamps")
	legacyPools, legacyRoots := dailyOSRootTestCounts(t, legacyID)
	require.Equal(t, 1, legacyPools)
	require.Zero(t, legacyRoots)
	missingPools, missingRoots := dailyOSRootTestCounts(t, missingID)
	require.Zero(t, missingPools)
	require.Zero(t, missingRoots)
}

func TestOAuthDailyOSRootsCascadeOnPoolAndAccountDeletion(t *testing.T) {
	for _, deleteAccount := range []bool{false, true} {
		t.Run(fmt.Sprintf("account=%t", deleteAccount), func(t *testing.T) {
			ctx := context.Background()
			_, osRepo, _ := dailyOSRootTestRepository(t)
			accountID := newDailyOSRootTestAccount(t, nil)
			_, err := osRepo.GetOrCreateOAuthDailySessionPoolForOS(ctx, accountID, "windows", time.Date(2026, 9, 21, 4, 0, 0, 0, time.UTC))
			require.NoError(t, err)
			var poolID int64
			require.NoError(t, integrationDB.QueryRowContext(ctx,
				`SELECT id FROM openai_oauth_daily_session_pools WHERE account_id = $1`, accountID).Scan(&poolID))
			if deleteAccount {
				_, err = integrationDB.ExecContext(ctx, `DELETE FROM accounts WHERE id = $1`, accountID)
			} else {
				_, err = integrationDB.ExecContext(ctx, `DELETE FROM openai_oauth_daily_session_pools WHERE id = $1`, poolID)
			}
			require.NoError(t, err)
			var rootRows int
			require.NoError(t, integrationDB.QueryRowContext(ctx,
				`SELECT count(*) FROM openai_oauth_daily_os_roots WHERE pool_id = $1`, poolID).Scan(&rootRows))
			require.Zero(t, rootRows, "removing a pool or its account must not orphan OS roots")
		})
	}
}
