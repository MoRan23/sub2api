//go:build unit

package service

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type openAIOSRuntimeRepo struct {
	AccountRepository
	OpenAIOAuthOSCredentialsRepository
	mu              sync.Mutex
	account         *Account
	slots           map[string]*OpenAIOAuthOSCredential
	globalErrors    int
	globalCooldowns int
}

func newOpenAIOSRuntimeRepo() *openAIOSRuntimeRepo {
	r := &openAIOSRuntimeRepo{slots: make(map[string]*OpenAIOAuthOSCredential)}
	r.account = &Account{ID: 41, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true,
		Credentials: map[string]any{"access_token": "windows-access", "refresh_token": "windows-refresh"},
		OpenAIOAuthOSProfiles: &OpenAIOAuthOSProfiles{DefaultOS: OpenAIOSWindows, Profiles: map[string]OpenAIOAuthOSProfile{
			OpenAIOSWindows: {OSFamily: OpenAIOSWindows}, OpenAIOSLinux: {OSFamily: OpenAIOSLinux},
		}}}
	for _, os := range []string{OpenAIOSWindows, OpenAIOSLinux} {
		r.slots[os] = &OpenAIOAuthOSCredential{OwnerAccountID: 41, OSFamily: os, Status: OpenAIOAuthAuthorizationAuthorized,
			AuthorizationGeneration: os + "-generation", Revision: 1, Credentials: map[string]any{
				"access_token": os + "-access", "refresh_token": os + "-refresh", "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339),
			}}
	}
	return r
}

func (r *openAIOSRuntimeRepo) GetByID(_ context.Context, id int64) (*Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if id != r.account.ID {
		return nil, ErrAccountNotFound
	}
	return snapshotOAuthRefreshAccount(r.account), nil
}

func (r *openAIOSRuntimeRepo) GetOpenAIOAuthOSCredential(_ context.Context, id int64, os string) (*OpenAIOAuthOSCredential, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	slot := r.slots[os]
	if id != r.account.ID || slot == nil {
		return nil, ErrOpenAIOAuthOSUnauthorized
	}
	copy := *slot
	copy.Credentials = shallowCopyMap(slot.Credentials)
	return &copy, nil
}

func (r *openAIOSRuntimeRepo) PatchOpenAIOAuthOSCredentialsIfUnchanged(_ context.Context, id int64, os, generation string, revision int64, proxyID *int64, patch map[string]any, removed []string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	slot := r.slots[os]
	if id != r.account.ID || slot == nil || slot.Status != OpenAIOAuthAuthorizationAuthorized || slot.AuthorizationGeneration != generation || slot.Revision != revision || !reflect.DeepEqual(proxyID, r.account.ProxyID) {
		return false, nil
	}
	for _, key := range removed {
		delete(slot.Credentials, key)
	}
	for key, value := range patch {
		slot.Credentials[key] = value
	}
	slot.Revision++
	return true, nil
}

func (r *openAIOSRuntimeRepo) SetOpenAIOAuthOSCredentialErrorIfUnchanged(_ context.Context, id int64, os, generation string, revision int64, reason string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	slot := r.slots[os]
	if id != r.account.ID || slot == nil || slot.AuthorizationGeneration != generation || slot.Revision != revision {
		return false, nil
	}
	slot.Status, slot.LastError = OpenAIOAuthAuthorizationReauthRequired, reason
	return true, nil
}

func (r *openAIOSRuntimeRepo) SetError(context.Context, int64, string) error {
	r.globalErrors++
	return nil
}
func (r *openAIOSRuntimeRepo) SetTempUnschedulable(context.Context, int64, time.Time, string) error {
	r.globalCooldowns++
	return nil
}

func TestOpenAIOSProviderUsesOnlySelectedAuthorizedSlot(t *testing.T) {
	r := newOpenAIOSRuntimeRepo()
	cache := newOpenAITokenCacheStub()
	cache.tokens["openai:account:41"] = "legacy-windows-cache"
	p := NewOpenAITokenProvider(r, cache, nil)
	ctx := ContextWithOpenAIRequestOS(context.Background(), OpenAIRequestOS{Family: OpenAIOSLinux, Captured: true})
	token, err := p.GetAccessToken(ctx, r.account)
	require.NoError(t, err)
	require.Equal(t, "linux-access", token)
	token, err = p.GetAccessToken(context.Background(), r.account)
	require.NoError(t, err)
	require.Equal(t, "windows-access", token)
	ctx = ContextWithOpenAIRequestOS(context.Background(), OpenAIRequestOS{Family: OpenAIOSMacOS, Captured: true})
	_, err = p.GetAccessToken(ctx, r.account)
	require.ErrorIs(t, err, ErrOpenAIOAuthOSUnauthorized)
}

func TestOpenAIOSProviderRejectsReauthorizationEvenWithOldCache(t *testing.T) {
	r := newOpenAIOSRuntimeRepo()
	scoped, err := ResolveOpenAIOAuthCredentialAccount(context.Background(), r, r.account, OpenAIOSLinux)
	require.NoError(t, err)
	cache := newOpenAITokenCacheStub()
	cache.tokens[OpenAITokenCacheKey(scoped)] = "linux-access"
	r.slots[OpenAIOSLinux].AuthorizationGeneration = "replacement"
	r.slots[OpenAIOSLinux].Credentials["access_token"] = "replacement-token"
	_, err = NewOpenAITokenProvider(r, cache, nil).GetAccessToken(context.Background(), scoped)
	require.ErrorIs(t, err, ErrOpenAIOAuthOSAuthorizationChanged)
}

func TestOpenAIOSProviderDoesNotReturnExpiredSelectedToken(t *testing.T) {
	r := newOpenAIOSRuntimeRepo()
	r.slots[OpenAIOSLinux].Credentials["expires_at"] = time.Now().Add(-time.Minute).Format(time.RFC3339)
	ctx := ContextWithOpenAIRequestOS(context.Background(), OpenAIRequestOS{Family: OpenAIOSLinux, Captured: true})
	scoped, err := ResolveOpenAIOAuthCredentialAccount(ctx, r, r.account, OpenAIOSLinux)
	require.NoError(t, err)
	cache := newOpenAITokenCacheStub()
	cache.tokens[OpenAITokenCacheKey(scoped)] = "expired-cache-token"
	token, err := NewOpenAITokenProvider(r, cache, nil).GetAccessToken(ctx, r.account)
	require.ErrorContains(t, err, "expired for the selected operating system")
	require.Empty(t, token)
	require.Equal(t, "windows-access", r.slots[OpenAIOSWindows].Credentials["access_token"])
}

func TestOpenAIOSRefreshRereadAndPersistenceRemainInAttemptedSlot(t *testing.T) {
	r := newOpenAIOSRuntimeRepo()
	scoped, err := ResolveOpenAIOAuthCredentialAccount(context.Background(), r, r.account, OpenAIOSLinux)
	require.NoError(t, err)
	executor := &refreshAPIExecutorStub{needsRefresh: true, credentials: map[string]any{"access_token": "linux-rotated", "refresh_token": "linux-rotated-refresh"}}
	executor.canRefresh = func(a *Account) bool {
		require.Equal(t, OpenAIOSLinux, a.OpenAIOAuthCredentialOS)
		require.Equal(t, "linux-refresh", a.GetOpenAIRefreshToken())
		return true
	}
	result, err := NewOAuthRefreshAPI(r, nil).RefreshIfNeeded(context.Background(), scoped, executor, time.Minute)
	require.NoError(t, err)
	require.True(t, result.Refreshed)
	require.Equal(t, OpenAIOSLinux, result.Account.OpenAIOAuthCredentialOS)
	require.Equal(t, "linux-rotated", result.Account.GetOpenAIAccessToken())
	require.Equal(t, "windows-access", r.slots[OpenAIOSWindows].Credentials["access_token"])
	require.Equal(t, OpenAITokenRefreshLockKey(scoped), OpenAITokenRefreshLockKey(result.Account))
	require.NotEqual(t, OpenAITokenCacheKey(scoped), OpenAITokenCacheKey(result.Account))
}

func TestOpenAIOSRefreshRejectsLateResultAfterReauthorization(t *testing.T) {
	r := newOpenAIOSRuntimeRepo()
	scoped, err := ResolveOpenAIOAuthCredentialAccount(context.Background(), r, r.account, OpenAIOSLinux)
	require.NoError(t, err)
	executor := &refreshAPIExecutorStub{needsRefresh: true, credentials: map[string]any{"access_token": "late-access", "refresh_token": "late-refresh"}}
	executor.onRefresh = func() {
		r.slots[OpenAIOSLinux].AuthorizationGeneration = "new-generation"
		r.slots[OpenAIOSLinux].Credentials["access_token"] = "new-access"
	}
	_, err = NewOAuthRefreshAPI(r, nil).RefreshIfNeeded(context.Background(), scoped, executor, time.Minute)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrOpenAIOAuthOSAuthorizationChanged)
	require.Equal(t, "new-access", r.slots[OpenAIOSLinux].Credentials["access_token"])
}

func TestOpenAIOSInvalidGrantRecoveryCannotUseDefaultSlot(t *testing.T) {
	r := newOpenAIOSRuntimeRepo()
	scoped, err := ResolveOpenAIOAuthCredentialAccount(context.Background(), r, r.account, OpenAIOSLinux)
	require.NoError(t, err)
	executor := &refreshAPIExecutorStub{needsRefresh: true, err: errors.New("invalid_grant")}
	_, err = NewOAuthRefreshAPI(r, nil).RefreshIfNeeded(context.Background(), scoped, executor, time.Minute)
	require.EqualError(t, err, "invalid_grant")
}

func TestOpenAIOSUpstreamRevocationPausesOnlyAttemptedSlot(t *testing.T) {
	r := newOpenAIOSRuntimeRepo()
	scoped, err := ResolveOpenAIOAuthCredentialAccount(context.Background(), r, r.account, OpenAIOSLinux)
	require.NoError(t, err)
	svc := NewRateLimitService(r, nil, nil, nil, nil)
	require.True(t, svc.HandleUpstreamError(context.Background(), scoped, 401, nil, []byte(`{"error":{"code":"token_revoked"}}`)))
	require.Equal(t, OpenAIOAuthAuthorizationReauthRequired, r.slots[OpenAIOSLinux].Status)
	require.Equal(t, OpenAIOAuthAuthorizationAuthorized, r.slots[OpenAIOSWindows].Status)
	require.Zero(t, r.globalErrors)
	require.Zero(t, r.globalCooldowns)
}

type openAIOSBlockingExecutor struct {
	started chan string
	release chan struct{}
}

func (e *openAIOSBlockingExecutor) CanRefresh(*Account) bool { return true }
func (e *openAIOSBlockingExecutor) NeedsRefresh(a *Account, _ time.Duration) bool {
	return a.GetOpenAIAccessToken() == a.OpenAIOAuthCredentialOS+"-access"
}
func (e *openAIOSBlockingExecutor) CacheKey(a *Account) string { return OpenAITokenRefreshLockKey(a) }
func (e *openAIOSBlockingExecutor) Refresh(ctx context.Context, a *Account) (map[string]any, error) {
	e.started <- a.OpenAIOAuthCredentialOS
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-e.release:
	}
	return map[string]any{"access_token": a.OpenAIOAuthCredentialOS + "-rotated", "refresh_token": a.OpenAIOAuthCredentialOS + "-next-refresh"}, nil
}

func TestOpenAIOSRefreshLocksPermitParallelSlotsAndSerializeSameSlot(t *testing.T) {
	r := newOpenAIOSRuntimeRepo()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	linux, err := ResolveOpenAIOAuthCredentialAccount(ctx, r, r.account, OpenAIOSLinux)
	require.NoError(t, err)
	windows, err := ResolveOpenAIOAuthCredentialAccount(ctx, r, r.account, OpenAIOSWindows)
	require.NoError(t, err)
	executor := &openAIOSBlockingExecutor{started: make(chan string, 3), release: make(chan struct{})}
	api := NewOAuthRefreshAPI(r, nil)
	errors := make(chan error, 3)
	start := func(account *Account) {
		go func() { _, err := api.RefreshIfNeeded(ctx, account, executor, time.Minute); errors <- err }()
	}
	start(linux)
	select {
	case os := <-executor.started:
		require.Equal(t, OpenAIOSLinux, os)
	case <-ctx.Done():
		t.Fatal("Linux refresh did not start")
	}
	start(linux)
	start(windows)
	select {
	case os := <-executor.started:
		require.Equal(t, OpenAIOSWindows, os)
	case <-ctx.Done():
		t.Fatal("Windows refresh blocked behind Linux")
	}
	close(executor.release)
	for range 3 {
		require.NoError(t, <-errors)
	}
	require.Empty(t, executor.started, "same slot should re-read the first refresh instead of rotating again")
}

func TestOpenAIOSProviderReturnsRefreshedAttemptRevisionForAuthFailure(t *testing.T) {
	r := newOpenAIOSRuntimeRepo()
	r.slots[OpenAIOSLinux].Credentials["expires_at"] = time.Now().Add(time.Minute).Format(time.RFC3339)
	scoped, err := ResolveOpenAIOAuthCredentialAccount(context.Background(), r, r.account, OpenAIOSLinux)
	require.NoError(t, err)
	executor := &refreshAPIExecutorStub{needsRefresh: true, credentials: map[string]any{
		"access_token": "linux-refreshed-access", "refresh_token": "linux-refreshed-refresh", "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339),
	}}
	p := NewOpenAITokenProvider(r, newOpenAITokenCacheStub(), nil)
	p.SetRefreshAPI(NewOAuthRefreshAPI(r, nil), executor)
	token, used, err := p.GetAccessTokenWithAccount(context.Background(), scoped)
	require.NoError(t, err)
	require.Equal(t, "linux-refreshed-access", token)
	require.Equal(t, int64(2), used.OpenAIOAuthCredentialRevision)
	require.Equal(t, int64(1), scoped.OpenAIOAuthCredentialRevision, "provider must not mutate a caller's snapshot")
	attempt, err := OpenAIOAuthTokenAccountSnapshot(scoped, used)
	require.NoError(t, err)
	require.Equal(t, token, attempt.GetOpenAIAccessToken())
	require.True(t, NewRateLimitService(r, nil, nil, nil, nil).HandleUpstreamError(context.Background(), attempt, 401, nil, []byte(`{"error":{"code":"token_revoked"}}`)))
	require.Equal(t, OpenAIOAuthAuthorizationReauthRequired, r.slots[OpenAIOSLinux].Status)
	require.Equal(t, OpenAIOAuthAuthorizationAuthorized, r.slots[OpenAIOSWindows].Status)
}

type openAIOSCacheReadHook struct {
	OpenAITokenCache
	onRead func()
}

func (c *openAIOSCacheReadHook) GetAccessToken(ctx context.Context, key string) (string, error) {
	token, err := c.OpenAITokenCache.GetAccessToken(ctx, key)
	c.onRead()
	return token, err
}

func TestOpenAIOSCachedAttemptNeverBorrowsRevisionFromLaterRefresh(t *testing.T) {
	r := newOpenAIOSRuntimeRepo()
	scoped, err := ResolveOpenAIOAuthCredentialAccount(context.Background(), r, r.account, OpenAIOSLinux)
	require.NoError(t, err)
	cache := newOpenAITokenCacheStub()
	cache.tokens[OpenAITokenCacheKey(scoped)] = "linux-access"
	hookedCache := &openAIOSCacheReadHook{OpenAITokenCache: cache, onRead: func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.slots[OpenAIOSLinux].Revision++
		r.slots[OpenAIOSLinux].Credentials["access_token"] = "later-refresh-access"
	}}
	token, used, err := NewOpenAITokenProvider(r, hookedCache, nil).GetAccessTokenWithAccount(context.Background(), scoped)
	require.NoError(t, err)
	require.Equal(t, "linux-access", token)
	require.Equal(t, int64(1), used.OpenAIOAuthCredentialRevision)
	attempt, err := OpenAIOAuthTokenAccountSnapshot(scoped, used)
	require.NoError(t, err)
	require.True(t, NewRateLimitService(r, nil, nil, nil, nil).HandleUpstreamError(context.Background(), attempt, 401, nil, []byte(`{"error":{"code":"token_revoked"}}`)))
	require.Equal(t, OpenAIOAuthAuthorizationAuthorized, r.slots[OpenAIOSLinux].Status, "late failure must not quarantine a later token")
	require.Zero(t, r.globalErrors)
}

func TestOpenAIOSCredentialSnapshotPreservesSparkBusinessAccount(t *testing.T) {
	r := newOpenAIOSRuntimeRepo()
	scoped, err := ResolveOpenAIOAuthCredentialAccount(context.Background(), r, r.account, OpenAIOSLinux)
	require.NoError(t, err)
	parentID := r.account.ID
	shadow := &Account{ID: 99, Platform: PlatformOpenAI, Type: AccountTypeOAuth, ParentAccountID: &parentID, QuotaDimension: QuotaDimensionSpark}
	attempt, err := OpenAIOAuthTokenAccountSnapshot(shadow, scoped)
	require.NoError(t, err)
	require.Equal(t, int64(99), attempt.ID)
	require.Equal(t, parentID, attempt.OpenAIOAuthCredentialOwnerID)
	require.Equal(t, OpenAIOSLinux, attempt.OpenAIOAuthCredentialOS)
	require.Equal(t, "linux-access", attempt.GetOpenAIAccessToken())
}

type openAIOSSparkRuntimeRepo struct {
	*openAIOSRuntimeRepo
	shadow *Account
}

func (r *openAIOSSparkRuntimeRepo) GetByID(ctx context.Context, id int64) (*Account, error) {
	if id == r.shadow.ID {
		return snapshotOAuthRefreshAccount(r.shadow), nil
	}
	return r.openAIOSRuntimeRepo.GetByID(ctx, id)
}

func TestOpenAIOSScopedSparkRefreshPersistsOwnerSlot(t *testing.T) {
	base := newOpenAIOSRuntimeRepo()
	parentID := base.account.ID
	shadow := &Account{ID: 99, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		ParentAccountID: &parentID, QuotaDimension: QuotaDimensionSpark}
	repo := &openAIOSSparkRuntimeRepo{openAIOSRuntimeRepo: base, shadow: shadow}
	ctx := context.Background()
	attempt, err := ResolveOpenAIOAuthCredentialAccount(ctx, repo, shadow, OpenAIOSLinux)
	require.NoError(t, err)
	require.True(t, attempt.IsCredentialShadow())
	credentials := shallowCopyMap(attempt.Credentials)
	credentials["access_token"] = "linux-refreshed-access"
	credentials["refresh_token"] = "linux-refreshed-refresh"

	fresh, applied, err := persistOpenAIOAuthRefreshCredentials(ctx, repo, attempt, credentials)
	require.NoError(t, err)
	require.True(t, applied, "a scoped Spark refresh must CAS the owner's private slot")
	require.Equal(t, shadow.ID, fresh.ID)
	require.Equal(t, parentID, fresh.OpenAIOAuthCredentialOwnerID)
	require.Equal(t, OpenAIOSLinux, fresh.OpenAIOAuthCredentialOS)
	require.Equal(t, int64(2), fresh.OpenAIOAuthCredentialRevision)
	require.Equal(t, "linux-refreshed-access", fresh.GetOpenAIAccessToken())
	require.Equal(t, "linux-refreshed-refresh", base.slots[OpenAIOSLinux].Credentials["refresh_token"])
	require.Equal(t, "windows-access", base.slots[OpenAIOSWindows].Credentials["access_token"])
	require.Equal(t, "windows-access", base.account.GetOpenAIAccessToken())

	credentials["access_token"] = "late-linux-access"
	_, applied, err = persistOpenAIOAuthRefreshCredentials(ctx, repo, attempt, credentials)
	require.NoError(t, err)
	require.False(t, applied, "the old Spark attempt must retain the original revision guard")
	require.Equal(t, "linux-refreshed-access", base.slots[OpenAIOSLinux].Credentials["access_token"])
}

func TestOpenAIOSGatewayRefreshUpdatesSparkAttemptWithoutChangingBusinessID(t *testing.T) {
	r := newOpenAIOSRuntimeRepo()
	r.slots[OpenAIOSLinux].Credentials["expires_at"] = time.Now().Add(time.Minute).Format(time.RFC3339)
	scoped, err := ResolveOpenAIOAuthCredentialAccount(context.Background(), r, r.account, OpenAIOSLinux)
	require.NoError(t, err)
	parentID := r.account.ID
	shadow := &Account{ID: 99, Platform: PlatformOpenAI, Type: AccountTypeOAuth, ParentAccountID: &parentID, QuotaDimension: QuotaDimensionSpark}
	attempt, err := OpenAIOAuthTokenAccountSnapshot(shadow, scoped)
	require.NoError(t, err)
	executor := &refreshAPIExecutorStub{needsRefresh: true, credentials: map[string]any{
		"access_token": "linux-refreshed-access", "refresh_token": "linux-refreshed-refresh", "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339),
	}}
	p := NewOpenAITokenProvider(r, newOpenAITokenCacheStub(), nil)
	p.SetRefreshAPI(NewOAuthRefreshAPI(r, nil), executor)
	gateway := &OpenAIGatewayService{accountRepo: r, openAITokenProvider: p}
	token, authType, err := gateway.GetAccessToken(context.Background(), attempt)
	require.NoError(t, err)
	require.Equal(t, "oauth", authType)
	require.Equal(t, "linux-refreshed-access", token)
	require.Equal(t, int64(99), attempt.ID)
	require.Equal(t, parentID, attempt.OpenAIOAuthCredentialOwnerID)
	require.Equal(t, int64(2), attempt.OpenAIOAuthCredentialRevision)
	require.Equal(t, token, attempt.GetOpenAIAccessToken())
	require.True(t, NewRateLimitService(r, nil, nil, nil, nil).HandleUpstreamError(context.Background(), attempt, 401, nil, []byte(`{"error":{"code":"token_revoked"}}`)))
	require.Equal(t, OpenAIOAuthAuthorizationReauthRequired, r.slots[OpenAIOSLinux].Status)
	require.Equal(t, OpenAIOAuthAuthorizationAuthorized, r.slots[OpenAIOSWindows].Status)
	require.Zero(t, r.globalErrors)
}

func TestOpenAIOSProviderAndShadowFailClosedWithoutSlotReader(t *testing.T) {
	account := &Account{ID: 71, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "legacy-secret"}}
	_, err := NewOpenAITokenProvider(nil, nil, nil).GetAccessToken(context.Background(), account)
	require.ErrorIs(t, err, ErrOpenAIOAuthOSUnauthorized)
	_, err = resolveCredentialAccount(context.Background(), nil, account)
	require.ErrorIs(t, err, ErrOpenAIOAuthOSUnauthorized)
}

func TestOpenAIOS429PlanMetadataPatchRemainsInSelectedSlot(t *testing.T) {
	r := newOpenAIOSRuntimeRepo()
	r.slots[OpenAIOSWindows].Credentials["plan_type"] = "plus"
	r.slots[OpenAIOSLinux].Credentials["plan_type"] = "plus"
	scoped, err := ResolveOpenAIOAuthCredentialAccount(context.Background(), r, r.account, OpenAIOSLinux)
	require.NoError(t, err)
	persistOpenAI429PlanType(context.Background(), r, scoped, []byte(`{"error":{"type":"usage_limit_reached","plan_type":"free"}}`))
	require.Equal(t, "free", r.slots[OpenAIOSLinux].Credentials["plan_type"])
	require.Equal(t, "plus", r.slots[OpenAIOSWindows].Credentials["plan_type"])
	require.Zero(t, r.globalErrors)
}
