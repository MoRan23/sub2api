package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
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

type fakeOAuthDailyAffinityRepository struct {
	pool     OAuthDailySessionPool
	affinity OAuthDailySessionAffinity
}

func (r *fakeOAuthDailyAffinityRepository) GetOrCreateOAuthDailySessionPool(context.Context, int64, time.Time) (OAuthDailySessionPool, error) {
	return r.pool, nil
}
func (r *fakeOAuthDailyAffinityRepository) GetOrCreateOAuthDailySessionAffinity(context.Context, int64, int64, string, time.Time) (OAuthDailySessionAffinity, error) {
	return r.affinity, nil
}
func (r *fakeOAuthDailyAffinityRepository) ReleaseOAuthDailySessionGeneration(context.Context, int64, string) error {
	return nil
}
func (r *fakeOAuthDailyAffinityRepository) CleanupOAuthDailySessionGenerations(context.Context, time.Time) (int, error) {
	return 0, nil
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

type dailyRotationSettingRepo struct{ values map[string]string }

func (r *dailyRotationSettingRepo) Get(_ context.Context, key string) (*Setting, error) {
	value, ok := r.values[key]
	if !ok {
		return nil, nil
	}
	return &Setting{Key: key, Value: value}, nil
}
func (r *dailyRotationSettingRepo) GetValue(_ context.Context, key string) (string, error) {
	if value, ok := r.values[key]; ok {
		return value, nil
	}
	return "", nil
}
func (r *dailyRotationSettingRepo) Set(_ context.Context, key, value string) error {
	r.values[key] = value
	return nil
}
func (r *dailyRotationSettingRepo) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	result := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, ok := r.values[key]; ok {
			result[key] = value
		}
	}
	return result, nil
}
func (r *dailyRotationSettingRepo) SetMultiple(_ context.Context, settings map[string]string) error {
	for key, value := range settings {
		r.values[key] = value
	}
	return nil
}
func (r *dailyRotationSettingRepo) GetAll(_ context.Context) (map[string]string, error) {
	result := make(map[string]string, len(r.values))
	for key, value := range r.values {
		result[key] = value
	}
	return result, nil
}
func (r *dailyRotationSettingRepo) Delete(_ context.Context, key string) error {
	delete(r.values, key)
	return nil
}

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
	settingsRepo := &dailyRotationSettingRepo{values: map[string]string{
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

func TestResolveOAuthIdentityPlanAppliesDailyAffinityOnHTTPStream(t *testing.T) {
	settingsRepo := &dailyRotationSettingRepo{values: map[string]string{
		SettingKeyEnableOpenAICodexFingerprintNormalization: "true",
		SettingKeyEnableOpenAIUUIDv7SessionIdentity:         "true",
		SettingKeyEnableOpenAIOAuthDailySessionRotation:     "true",
	}}
	root := "018f5c3c-6e3a-7abf-8def-1234567890ae"
	dailyRepo := &fakeOAuthDailyAffinityRepository{
		pool:     OAuthDailySessionPool{AccountID: 44, BusinessDate: "2026-09-11", Generation: root},
		affinity: OAuthDailySessionAffinity{AccountID: 44, APIKeyID: 12, LogicalSessionKey: "fallback", BusinessDate: "2026-09-11", Generation: root, SlotIndex: 1, StreamSessionID: root},
	}
	svc := &OpenAIGatewayService{settingService: NewSettingService(settingsRepo, nil), oauthDailySessionRepo: dailyRepo}
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/responses", nil)
	c.Set("api_key", &APIKey{ID: 12})
	setOpenAIClientRequestedStream(c, true)
	account := &Account{ID: 44, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	planned, err := svc.ResolveOpenAIOAuthIdentityPlan(context.Background(), c, account, OpenAIOAuthIdentityCapture{}, OpenAIOAuthIdentityPlanOptions{TurnIdentityEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if !planned.TurnIdentityEnabled || planned.TurnIdentity.SessionID != root || planned.TurnIdentity.ParentThreadID != root {
		t.Fatalf("daily HTTP affinity was not applied: %+v", planned.TurnIdentity)
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

func TestOAuthDailyLogicalSessionFallbackSeedIsStableForMetadataFreeStream(t *testing.T) {
	settingsRepo := &dailyRotationSettingRepo{values: map[string]string{
		SettingKeyEnableOpenAICodexFingerprintNormalization: "true",
		SettingKeyEnableOpenAIUUIDv7SessionIdentity:         "true",
		SettingKeyEnableOpenAIOAuthDailySessionRotation:     "true",
	}}
	svc := &OpenAIGatewayService{settingService: NewSettingService(settingsRepo, nil)}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/responses", nil)
	c.Request.Header.Set(codexInstallationIDKey, "client-installation")
	c.Set("api_key", &APIKey{ID: 123})
	account := &Account{ID: 44, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	body := []byte(`{"model":"gpt-5.6","stream":true,"input":"hello"}`)
	first := svc.oauthDailyLogicalSessionFallbackSeed(context.Background(), c, account, body)
	second := svc.oauthDailyLogicalSessionFallbackSeed(context.Background(), c, account, body)
	if first == "" || first != second {
		t.Fatalf("fallback seed is not stable: first=%q second=%q", first, second)
	}
	if svc.oauthDailyLogicalSessionFallbackSeed(context.Background(), c, account, []byte(`{"stream":false}`)) != "" {
		t.Fatal("fallback seed must be limited to streaming requests")
	}
	noMetadataContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	noMetadataContext.Request = httptest.NewRequest(http.MethodPost, "/responses", nil)
	noMetadataContext.Set("api_key", &APIKey{ID: 123})
	if svc.oauthDailyLogicalSessionFallbackSeed(context.Background(), noMetadataContext, account, body) == "" {
		t.Fatal("fallback seed must remain available without installation metadata")
	}
}
