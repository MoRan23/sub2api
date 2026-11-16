package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

// fakeLeaderLockCache is an in-memory LeaderLockCache for unit tests. It models the
// compare-and-delete release semantics of the real Redis-backed implementation.
type fakeLeaderLockCache struct {
	mu         sync.Mutex
	owners     map[string]string
	acquireErr error
}

func (f *fakeLeaderLockCache) TryAcquireLeaderLock(_ context.Context, key, owner string, _ time.Duration) (bool, error) {
	if f.acquireErr != nil {
		return false, f.acquireErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.owners == nil {
		f.owners = map[string]string{}
	}
	if _, held := f.owners[key]; held {
		return false, nil
	}
	f.owners[key] = owner
	return true, nil
}

func (f *fakeLeaderLockCache) ReleaseLeaderLock(_ context.Context, key, owner string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.owners[key] == owner {
		delete(f.owners, key)
	}
	return nil
}

func (f *fakeLeaderLockCache) heldBy(key string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.owners[key]
}

func TestTryAcquireSingletonLeaderLock_NoBackendRunsUngated(t *testing.T) {
	release, ok := tryAcquireSingletonLeaderLock(context.Background(), nil, nil, "k", "inst", time.Minute)
	require.True(t, ok)
	require.NotNil(t, release)
	require.NotPanics(t, release)
}

func TestTryAcquireSingletonLeaderLock_ContendedThenReleased(t *testing.T) {
	cache := &fakeLeaderLockCache{}
	ctx := context.Background()
	const key = "leader:test:contended"

	releaseA, ok := tryAcquireSingletonLeaderLock(ctx, cache, nil, key, "A", time.Minute)
	require.True(t, ok, "first instance should acquire")
	require.Equal(t, "A", cache.heldBy(key))

	_, okB := tryAcquireSingletonLeaderLock(ctx, cache, nil, key, "B", time.Minute)
	require.False(t, okB, "peer must be locked out while the lock is held")

	releaseA()
	require.Empty(t, cache.heldBy(key), "release must free the lock")

	releaseB, okB := tryAcquireSingletonLeaderLock(ctx, cache, nil, key, "B", time.Minute)
	require.True(t, okB, "peer should acquire after the holder releases")
	releaseB()
}

// When the cache errors, the helper must fall through rather than acquire via the
// cache. With no DB configured it runs ungated so the job is never starved by a
// flaky Redis.
func TestTryAcquireSingletonLeaderLock_CacheErrorFallsThrough(t *testing.T) {
	cache := &fakeLeaderLockCache{acquireErr: context.DeadlineExceeded}
	release, ok := tryAcquireSingletonLeaderLock(context.Background(), cache, nil, "k", "inst", time.Minute)
	require.True(t, ok, "cache error with no DB must run ungated, not skip")
	require.NotNil(t, release)
	require.NotPanics(t, release)
}

func TestSubscriptionExpiryService_ReminderSkipsScanWhenNotLeader(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	cache := &fakeLeaderLockCache{}
	// A peer already holds the reminder leader lock.
	_, _ = cache.TryAcquireLeaderLock(context.Background(), subscriptionExpiryReminderLeaderLockKey, "peer", time.Minute)

	repo := &subscriptionExpiryRepoStub{}
	settingRepo := &subscriptionExpirySettingRepoStub{values: map[string]string{SettingKeySMTPHost: "smtp.example.com"}}
	svc := NewSubscriptionExpiryService(repo, time.Minute)
	svc.SetSettingRepository(settingRepo)
	svc.SetNotificationEmailService(NewNotificationEmailService(settingRepo, NewEmailService(settingRepo, nil)))
	svc.SetLeaderLock(cache, db)

	svc.sendExpiryReminders(context.Background())

	require.Zero(t, repo.listCalls, "non-leader must not scan active subscriptions")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSubscriptionExpiryService_ReminderScansWhenLeader(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	mock.ExpectQuery("SELECT s.id,s.user_id,u.email,s.expires_at").WillReturnRows(sqlmock.NewRows([]string{"id", "user_id", "email", "expires_at", "days"}))
	repo := &subscriptionExpiryRepoStub{}
	settingRepo := &subscriptionExpirySettingRepoStub{values: map[string]string{SettingKeySMTPHost: "smtp.example.com"}}
	svc := NewSubscriptionExpiryService(repo, time.Minute)
	svc.SetSettingRepository(settingRepo)
	svc.SetNotificationEmailService(NewNotificationEmailService(settingRepo, NewEmailService(settingRepo, nil)))
	svc.SetLeaderLock(&fakeLeaderLockCache{}, db)

	svc.sendExpiryReminders(context.Background())

	require.Zero(t, repo.listCalls, "reminders must use the narrow projection, never full subscription hydration")
	require.NoError(t, mock.ExpectationsWereMet())
}

// Single-instance correctness: the lock is released at the end of each cycle, so
// the same instance must re-acquire it and run on every subsequent cycle (no
// self-lockout). Covers both Redis and PostgreSQL coordination.
func TestSubscriptionExpiryService_ReminderRunsEveryCycleSingleInstance(t *testing.T) {
	cases := map[string]LeaderLockCache{
		"with_cache":  &fakeLeaderLockCache{},
		"db_fallback": nil,
	}
	for name, cache := range cases {
		t.Run(name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer db.Close()
			for range 3 {
				if cache == nil {
					mock.ExpectQuery("SELECT pg_try_advisory_lock").WillReturnRows(sqlmock.NewRows([]string{"acquired"}).AddRow(true))
				}
				mock.ExpectQuery("SELECT s.id,s.user_id,u.email,s.expires_at").WillReturnRows(sqlmock.NewRows([]string{"id", "user_id", "email", "expires_at", "days"}))
				if cache == nil {
					mock.ExpectExec("SELECT pg_advisory_unlock").WillReturnResult(sqlmock.NewResult(0, 1))
				}
			}
			repo := &subscriptionExpiryRepoStub{}
			settingRepo := &subscriptionExpirySettingRepoStub{values: map[string]string{SettingKeySMTPHost: "smtp.example.com"}}
			svc := NewSubscriptionExpiryService(repo, time.Minute)
			svc.SetSettingRepository(settingRepo)
			svc.SetNotificationEmailService(NewNotificationEmailService(settingRepo, NewEmailService(settingRepo, nil)))
			svc.SetLeaderLock(cache, db)

			// Three consecutive cycles, mimicking the ticker loop.
			svc.sendExpiryReminders(context.Background())
			svc.sendExpiryReminders(context.Background())
			svc.sendExpiryReminders(context.Background())

			require.Zero(t, repo.listCalls)
			require.NoError(t, mock.ExpectationsWereMet(), "single instance must run each narrow scan")
		})
	}
}
