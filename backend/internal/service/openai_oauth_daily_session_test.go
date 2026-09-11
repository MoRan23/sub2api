package service

import (
	"context"
	"testing"
	"time"
)

func TestOAuthDailyBusinessDateUsesUTC8Midnight(t *testing.T) {
	cases := []struct {
		name string
		now  time.Time
		want string
	}{
		{"before midnight", time.Date(2026, 9, 11, 15, 59, 59, 0, time.UTC), "2026-09-11"},
		{"exact UTC8 midnight", time.Date(2026, 9, 11, 16, 0, 0, 0, time.UTC), "2026-09-12"},
		{"after midnight", time.Date(2026, 9, 12, 0, 1, 0, 0, time.UTC), "2026-09-12"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := OAuthDailyBusinessDate(tc.now); got != tc.want {
				t.Fatalf("OAuthDailyBusinessDate(%s) = %q, want %q", tc.now, got, tc.want)
			}
		})
	}
}

func TestOAuthDailySessionPoolCarriesThreeStreamRootsAndIndependentSyncRoot(t *testing.T) {
	pool := OAuthDailySessionPool{
		AccountID:        1374,
		BusinessDate:     "2026-09-11",
		Generation:       "generation",
		StreamSessionIDs: [OAuthDailyStreamSessionCount]string{"stream-0", "stream-1", "stream-2"},
		SyncSessionID:    "sync-root",
	}
	if len(pool.StreamSessionIDs) != 3 {
		t.Fatalf("stream root count = %d, want 3", len(pool.StreamSessionIDs))
	}
	if pool.SyncSessionID == "" {
		t.Fatal("daily pool must carry an independent sync root")
	}
	for _, id := range pool.StreamSessionIDs {
		if id == pool.SyncSessionID {
			t.Fatal("sync root must not overlap a stream root")
		}
	}
}

// fakeOAuthDailySessionRepository keeps resolver tests independent of Ent and
// verifies that the synchronous path consumes the pool's dedicated root.
type fakeOAuthDailySessionRepository struct {
	pool  OAuthDailySessionPool
	calls int
}

func (r *fakeOAuthDailySessionRepository) GetOrCreateOAuthDailySessionPool(_ context.Context, _ int64, _ time.Time) (OAuthDailySessionPool, error) {
	r.calls++
	return r.pool, nil
}
func (r *fakeOAuthDailySessionRepository) GetOrCreateOAuthDailySessionAffinity(context.Context, int64, int64, string, time.Time) (OAuthDailySessionAffinity, error) {
	panic("unexpected affinity lookup in synchronous resolver")
}
func (r *fakeOAuthDailySessionRepository) ReleaseOAuthDailySessionGeneration(context.Context, int64, string) error {
	return nil
}
func (r *fakeOAuthDailySessionRepository) CleanupOAuthDailySessionGenerations(context.Context, time.Time) (int, error) {
	return 0, nil
}

type fakeOAuthSyncSessionRepository struct{ root string }

func (r *fakeOAuthSyncSessionRepository) GetOrCreateOAuthSyncSession(context.Context, int64) (string, error) {
	return r.root, nil
}
func (r *fakeOAuthSyncSessionRepository) GetOAuthSyncSession(context.Context, int64) (string, error) {
	return r.root, nil
}
func (r *fakeOAuthSyncSessionRepository) DeleteOAuthSyncSession(context.Context, int64) error {
	return nil
}

func TestResolveOAuthSynchronousTurnIdentityUsesDailyDedicatedRoot(t *testing.T) {
	settingsRepo := &openAIUUIDv7RuntimeRepo{values: map[string]string{
		SettingKeyEnableOpenAIUUIDv7SessionIdentity:     "true",
		SettingKeyEnableOpenAIOAuthDailySessionRotation: "true",
	}}
	dailyRepo := &fakeOAuthDailySessionRepository{pool: OAuthDailySessionPool{
		AccountID: 44, BusinessDate: "2026-09-11", Generation: "018f5c3c-6e3a-7abe-8def-1234567890ad",
		StreamSessionIDs: [OAuthDailyStreamSessionCount]string{
			"018f5c3c-6e3a-7abc-8def-1234567890ab", "018f5c3c-6e3a-7abd-8def-1234567890ac", "018f5c3c-6e3a-7abe-8def-1234567890ad",
		},
		SyncSessionID: "018f5c3c-6e3a-7abf-8def-1234567890ae",
	}}
	svc := &OpenAIGatewayService{
		settingService: NewSettingService(settingsRepo, nil), oauthDailySessionRepo: dailyRepo,
	}
	account := &Account{ID: 44, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	identity, enabled, err := svc.resolveOAuthSynchronousTurnIdentity(context.Background(), account, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if !enabled {
		t.Fatal("daily synchronous identity should be enabled")
	}
	if identity.SessionID != dailyRepo.pool.SyncSessionID {
		t.Fatalf("session root = %q, want %q", identity.SessionID, dailyRepo.pool.SyncSessionID)
	}
	if identity.ThreadID == identity.SessionID || identity.ParentThreadID != identity.SessionID {
		t.Fatalf("expected a context-free child thread under sync root: %#v", identity)
	}
	if dailyRepo.calls != 1 {
		t.Fatalf("pool calls = %d, want 1", dailyRepo.calls)
	}
}

func TestResolveOAuthSynchronousTurnIdentityKeepsLegacyRootWhenRotationDisabled(t *testing.T) {
	settingsRepo := &openAIUUIDv7RuntimeRepo{values: map[string]string{
		SettingKeyEnableOpenAIOAuthDailySessionRotation: "false",
	}}
	legacy := &fakeOAuthSyncSessionRepository{root: "018f5c3c-6e3a-7abf-8def-1234567890ae"}
	dailyRepo := &fakeOAuthDailySessionRepository{}
	svc := &OpenAIGatewayService{settingService: NewSettingService(settingsRepo, nil), oauthSyncSessionRepo: legacy, oauthDailySessionRepo: dailyRepo}
	identity, enabled, err := svc.resolveOAuthSynchronousTurnIdentity(context.Background(), &Account{ID: 44, Platform: PlatformOpenAI, Type: AccountTypeOAuth}, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if !enabled || identity.SessionID != legacy.root {
		t.Fatalf("legacy sync identity = %#v, enabled=%v", identity, enabled)
	}
	if dailyRepo.calls != 0 {
		t.Fatal("daily repository must not be used when the switch is disabled")
	}
}
