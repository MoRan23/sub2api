//go:build unit

package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type tokenRefreshOSCredentialsRepo struct {
	*tokenRefreshAccountRepo
	OpenAIOAuthOSCredentialsRepository
	slots         map[string]*OpenAIOAuthOSCredential
	listedIDs     []int64
	readOS        []string
	slotErrors    int
	slotCooldowns int
	slotWriteErr  error
}

func (r *tokenRefreshOSCredentialsRepo) ListOpenAIOAuthOSCredentials(_ context.Context, id int64) ([]*OpenAIOAuthOSCredential, error) {
	r.listedIDs = append(r.listedIDs, id)
	var slots []*OpenAIOAuthOSCredential
	for _, os := range OpenAIOAuthOSFamilies() {
		if slot := r.slots[os]; slot != nil {
			slots = append(slots, slot)
		}
	}
	return slots, nil
}

func (r *tokenRefreshOSCredentialsRepo) GetOpenAIOAuthOSCredential(_ context.Context, _ int64, os string) (*OpenAIOAuthOSCredential, error) {
	r.readOS = append(r.readOS, os)
	return r.slots[os], nil
}

func (r *tokenRefreshOSCredentialsRepo) SetOpenAIOAuthOSCredentialErrorIfUnchanged(_ context.Context, id int64, os, generation string, revision int64, _ string) (bool, error) {
	if r.slotWriteErr != nil {
		return false, r.slotWriteErr
	}
	slot := r.slots[os]
	if slot == nil || slot.OwnerAccountID != id || slot.AuthorizationGeneration != generation || slot.Revision != revision {
		return false, nil
	}
	r.slotErrors++
	slot.Status = OpenAIOAuthAuthorizationReauthRequired
	return true, nil
}

func (r *tokenRefreshOSCredentialsRepo) SetOpenAIOAuthOSCredentialCooldownIfUnchanged(_ context.Context, id int64, os, generation string, revision int64, until time.Time, _ string) (bool, error) {
	if r.slotWriteErr != nil {
		return false, r.slotWriteErr
	}
	slot := r.slots[os]
	if slot == nil || slot.OwnerAccountID != id || slot.AuthorizationGeneration != generation || slot.Revision != revision {
		return false, nil
	}
	r.slotCooldowns++
	slot.RefreshRetryAfter = &until
	return true, nil
}

func newTokenRefreshOSFixture(t *testing.T) (*Account, *tokenRefreshOSCredentialsRepo) {
	t.Helper()
	account := &Account{
		ID: 71, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true,
		Credentials: map[string]any{"access_token": "default-mirror", "refresh_token": "default-refresh", "expires_at": time.Now().Add(24 * time.Hour).Unix()},
	}
	profiles, err := BuildOpenAIOAuthOSProfiles(account, nil)
	require.NoError(t, err)
	ApplyOpenAIOAuthOSProfiles(account, profiles)
	repo := &tokenRefreshOSCredentialsRepo{
		tokenRefreshAccountRepo: &tokenRefreshAccountRepo{mockAccountRepoForGemini: mockAccountRepoForGemini{accountsByID: map[int64]*Account{account.ID: account}}},
		slots:                   map[string]*OpenAIOAuthOSCredential{},
	}
	for _, os := range []string{OpenAIOSWindows, OpenAIOSLinux} {
		repo.slots[os] = &OpenAIOAuthOSCredential{
			OwnerAccountID: account.ID, OSFamily: os, AuthorizationGeneration: "generation-" + os, Revision: 3,
			Status:      OpenAIOAuthAuthorizationAuthorized,
			Credentials: map[string]any{"access_token": "access-" + os, "refresh_token": "refresh-" + os, "expires_at": time.Now().Add(-time.Minute).Unix()},
		}
	}
	return account, repo
}

type tokenRefreshOSRecorder struct {
	OpenAITokenRefresher
	mu        sync.Mutex
	refreshed []string
}

func (r *tokenRefreshOSRecorder) Refresh(_ context.Context, account *Account) (map[string]any, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.refreshed = append(r.refreshed, account.OpenAIOAuthCredentialOS)
	return nil, nil
}

func TestTokenRefreshService_OpenAISlotsExpandAfterAccountPagination(t *testing.T) {
	account, repo := newTokenRefreshOSFixture(t)
	refresher := &tokenRefreshOSRecorder{}
	svc := &TokenRefreshService{accountRepo: repo, cfg: &config.TokenRefreshConfig{MaxRetries: 1}}
	state := &tokenRefreshProviderState{service: svc, registration: tokenRefreshRegistration{platform: PlatformOpenAI, refresher: refresher}}
	stats := svc.processCandidatePage(context.Background(), []Account{*account}, map[string]*tokenRefreshProviderState{PlatformOpenAI: state}, time.Hour)

	require.Equal(t, 1, stats.total, "the account cursor sees each owner only once")
	require.Equal(t, 1, stats.oauth)
	require.Equal(t, 2, stats.needsRefresh, "the fresh default mirror must not hide expired private slots")
	require.Equal(t, 2, stats.refreshed)
	require.Equal(t, []int64{account.ID}, repo.listedIDs)
	require.ElementsMatch(t, []string{OpenAIOSWindows, OpenAIOSLinux}, refresher.refreshed)
	require.ElementsMatch(t, []string{OpenAIOSWindows, OpenAIOSLinux}, repo.readOS)
	require.Equal(t, "default-mirror", account.GetOpenAIAccessToken())
}

func TestTokenRefreshService_OpenAISlotsSkipIneligibleAuthorizations(t *testing.T) {
	for _, reason := range []string{"missing", "reauth", "cooldown", "no refresh token"} {
		t.Run(reason, func(t *testing.T) {
			account, repo := newTokenRefreshOSFixture(t)
			slot := repo.slots[OpenAIOSLinux]
			switch reason {
			case "missing":
				delete(repo.slots, OpenAIOSLinux)
			case "reauth":
				slot.Status = OpenAIOAuthAuthorizationReauthRequired
			case "cooldown":
				until := time.Now().Add(time.Minute)
				slot.RefreshRetryAfter = &until
			case "no refresh token":
				delete(slot.Credentials, "refresh_token")
			}
			svc := &TokenRefreshService{accountRepo: repo}
			candidates, err := svc.backgroundRefreshAccounts(context.Background(), account)
			require.NoError(t, err)
			require.Len(t, candidates, 1)
			require.Equal(t, OpenAIOSWindows, candidates[0].OpenAIOAuthCredentialOS)
			require.Equal(t, []string{OpenAIOSWindows}, repo.readOS, "ineligible slots must not start refresh work")
		})
	}
}

func TestTokenRefreshService_OpenAISlotFailureIsolation(t *testing.T) {
	for _, permanent := range []bool{false, true} {
		for _, stale := range []bool{false, true} {
			name := "transient"
			if permanent {
				name = "permanent"
			}
			if stale {
				name += " stale"
			}
			t.Run(name, func(t *testing.T) {
				account, repo := newTokenRefreshOSFixture(t)
				scoped, err := ResolveOpenAIOAuthCredentialAccount(context.Background(), repo, account, OpenAIOSLinux)
				require.NoError(t, err)
				if stale {
					repo.slots[OpenAIOSLinux].Revision++
				}
				failure := errors.New("upstream timeout")
				if permanent {
					failure = errors.New("invalid_grant")
				}
				blocker := &tokenRefreshRuntimeBlocker{}
				svc := &TokenRefreshService{accountRepo: repo, runtimeBlocker: blocker, cacheInvalidator: &tokenCacheInvalidatorStub{}, cfg: &config.TokenRefreshConfig{MaxRetries: 1}}
				err = svc.refreshWithRetry(context.Background(), scoped, &tokenRefresherStub{err: failure}, nil, time.Hour)
				require.Error(t, err)
				if stale {
					require.ErrorIs(t, err, errRefreshSkipped)
					require.Zero(t, repo.slotErrors+repo.slotCooldowns)
				} else if permanent {
					require.Equal(t, 1, repo.slotErrors)
					require.Equal(t, OpenAIOAuthAuthorizationReauthRequired, repo.slots[OpenAIOSLinux].Status)
				} else {
					require.Equal(t, 1, repo.slotCooldowns)
					require.NotNil(t, repo.slots[OpenAIOSLinux].RefreshRetryAfter)
				}
				require.Equal(t, OpenAIOAuthAuthorizationAuthorized, repo.slots[OpenAIOSWindows].Status)
				require.Nil(t, repo.slots[OpenAIOSWindows].RefreshRetryAfter)
				require.Zero(t, repo.setErrorCalls)
				require.Zero(t, repo.setTempUnschedCalls)
				require.Zero(t, blocker.blockCalls)
			})
		}
	}
}

func TestTokenRefreshService_OpenAISlotSuccessPreservesSharedCooldownAndPublishesCanonicalAccount(t *testing.T) {
	account, repo := newTokenRefreshOSFixture(t)
	repo.slots[OpenAIOSWindows].Status = OpenAIOAuthAuthorizationReauthRequired
	repo.slots[OpenAIOSWindows].LastError = "OAuth authorization requires sign-in"
	until := time.Now().Add(time.Hour)
	account.TempUnschedulableUntil = &until
	account.TempUnschedulableReason = "shared upstream rate limit"
	scoped, err := ResolveOpenAIOAuthCredentialAccount(context.Background(), repo, account, OpenAIOSLinux)
	require.NoError(t, err)
	cache, blocker := &tempUnschedCacheStub{}, &tokenRefreshRuntimeBlocker{}
	scheduler, invalidator := &tokenRefreshSchedulerCache{}, &tokenCacheInvalidatorStub{}
	svc := &TokenRefreshService{accountRepo: repo, tempUnschedCache: cache, runtimeBlocker: blocker, schedulerCache: scheduler, cacheInvalidator: invalidator}
	svc.postRefreshActions(context.Background(), scoped)

	require.Zero(t, repo.clearTempCalls)
	require.Zero(t, cache.deleteCalls)
	require.Zero(t, blocker.clearCalls)
	require.Equal(t, OpenAIOSLinux, invalidator.lastAccount.OpenAIOAuthCredentialOS)
	require.Equal(t, "access-linux", invalidator.lastAccount.GetOpenAIAccessToken())
	require.Equal(t, "default-mirror", scheduler.lastAccount.GetOpenAIAccessToken())
	require.Empty(t, scheduler.lastAccount.OpenAIOAuthCredentialOS)
	require.Equal(t, &until, scheduler.lastAccount.TempUnschedulableUntil)
	require.Equal(t, OpenAIOAuthAuthorizationReauthRequired, repo.slots[OpenAIOSWindows].Status)
	require.Equal(t, "OAuth authorization requires sign-in", repo.slots[OpenAIOSWindows].LastError)
}

func TestTokenRefreshService_OpenAISlotFailurePersistenceErrorNeverFallsBackToGlobalState(t *testing.T) {
	account, repo := newTokenRefreshOSFixture(t)
	scoped, err := ResolveOpenAIOAuthCredentialAccount(context.Background(), repo, account, OpenAIOSLinux)
	require.NoError(t, err)
	repo.slotWriteErr = errors.New("database unavailable")
	blocker := &tokenRefreshRuntimeBlocker{}
	svc := &TokenRefreshService{accountRepo: repo, runtimeBlocker: blocker, cfg: &config.TokenRefreshConfig{MaxRetries: 1}}
	for _, failure := range []error{errors.New("invalid_grant"), errors.New("upstream timeout")} {
		err = svc.refreshWithRetry(context.Background(), scoped, &tokenRefresherStub{err: failure}, nil, time.Hour)
		var containment *providerCycleContainmentRefreshError
		require.ErrorAs(t, err, &containment)
	}
	require.Zero(t, repo.setErrorCalls)
	require.Zero(t, repo.setTempUnschedCalls)
	require.Zero(t, blocker.blockCalls)
}

func TestOpenAITokenRefresher_LockScopeStableAcrossGeneration(t *testing.T) {
	account, repo := newTokenRefreshOSFixture(t)
	windows, err := ResolveOpenAIOAuthCredentialAccount(context.Background(), repo, account, OpenAIOSWindows)
	require.NoError(t, err)
	linux, err := ResolveOpenAIOAuthCredentialAccount(context.Background(), repo, account, OpenAIOSLinux)
	require.NoError(t, err)
	refresher := &OpenAITokenRefresher{}
	key := refresher.CacheKey(windows)
	windows.OpenAIOAuthAuthorizationGeneration = "replacement-authorization"
	require.Equal(t, key, refresher.CacheKey(windows))
	require.NotEqual(t, key, refresher.CacheKey(linux))
}
