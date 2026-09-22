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
	if r.slots[os] == nil {
		return nil, nil
	}
	grant := *r.slots[os]
	grant.OSFamily = os
	return &grant, nil
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
	account := r.accountsByID[id]
	account.Status, account.Schedulable, account.ErrorMessage = StatusError, false, "OAuth authorization must be renewed"
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

func (r *tokenRefreshOSCredentialsRepo) MutateOpenAIOAuthAccountStateIfUnchanged(_ context.Context, id int64, snapshot OpenAIOAuthAccountStateSnapshot, change OpenAIOAuthAccountStateChange) (*OpenAIOAuthAccountStateResult, error) {
	if r.slotWriteErr != nil {
		return nil, r.slotWriteErr
	}
	slot := r.slots[OpenAIOSWindows]
	if slot == nil || id != snapshot.OwnerAccountID || slot.OwnerAccountID != id || slot.AuthorizationGeneration != snapshot.AuthorizationGeneration || slot.Revision != snapshot.CredentialRevision {
		return &OpenAIOAuthAccountStateResult{}, nil
	}
	switch change.Kind {
	case OpenAIOAuthAccountStateClearTemp:
		r.clearTempCalls++
		r.accountsByID[id].TempUnschedulableUntil = nil
		r.accountsByID[id].TempUnschedulableReason = ""
	case OpenAIOAuthAccountStateError:
		r.slotErrors++
		r.accountsByID[id].Status = StatusError
		r.accountsByID[id].Schedulable = false
		r.accountsByID[id].ErrorMessage = change.ErrorMessage
	case OpenAIOAuthAccountStateCooldown:
		r.slotCooldowns++
		r.accountsByID[id].TempUnschedulableUntil = &change.Until
		r.accountsByID[id].TempUnschedulableReason = change.Reason
	}
	return &OpenAIOAuthAccountStateResult{Applied: true}, nil
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
	grant := &OpenAIOAuthOSCredential{
		OwnerAccountID: account.ID, AuthorizationGeneration: "shared-generation", Revision: 3,
		Status:      OpenAIOAuthAuthorizationAuthorized,
		Credentials: map[string]any{"access_token": "shared-access", "refresh_token": "shared-refresh", "expires_at": time.Now().Add(-time.Minute).Unix()},
	}
	account.Credentials = grant.Credentials
	for _, os := range OpenAIOAuthOSFamilies() {
		repo.slots[os] = grant
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

func TestTokenRefreshService_OpenAISharedGrantRefreshesOnceAfterAccountPagination(t *testing.T) {
	account, repo := newTokenRefreshOSFixture(t)
	refresher := &tokenRefreshOSRecorder{}
	svc := &TokenRefreshService{accountRepo: repo, cfg: &config.TokenRefreshConfig{MaxRetries: 1}}
	state := &tokenRefreshProviderState{service: svc, registration: tokenRefreshRegistration{platform: PlatformOpenAI, refresher: refresher}}
	stats := svc.processCandidatePage(context.Background(), []Account{*account}, map[string]*tokenRefreshProviderState{PlatformOpenAI: state}, time.Hour)

	require.Equal(t, 1, stats.total, "the account cursor sees each owner only once")
	require.Equal(t, 1, stats.oauth)
	require.Equal(t, 1, stats.needsRefresh, "the shared grant is resolved once instead of using a stale account mirror")
	require.Equal(t, 1, stats.refreshed)
	require.Empty(t, repo.listedIDs)
	require.Equal(t, []string{OpenAIOSWindows}, refresher.refreshed)
	require.Equal(t, []string{OpenAIOSWindows}, repo.readOS)
	require.Equal(t, "shared-access", account.GetOpenAIAccessToken())
}

func TestTokenRefreshService_OpenAIRefreshEligibilityUsesAccountCredentials(t *testing.T) {
	for _, reason := range []string{"missing", "reauth", "cooldown", "no refresh token"} {
		t.Run(reason, func(t *testing.T) {
			account, repo := newTokenRefreshOSFixture(t)
			slot := repo.slots[OpenAIOSLinux]
			switch reason {
			case "missing":
				repo.slots = nil
			case "reauth":
				slot.Status = OpenAIOAuthAuthorizationReauthRequired
			case "cooldown":
				until := time.Now().Add(time.Minute)
				slot.RefreshRetryAfter = &until
			case "no refresh token":
				delete(slot.Credentials, "refresh_token")
			}
			svc := &TokenRefreshService{accountRepo: repo}
			candidate, err := svc.backgroundRefreshAccount(context.Background(), account)
			require.NoError(t, err)
			require.NotNil(t, candidate, "private authorization metadata does not filter account candidates")
			require.Equal(t, reason != "no refresh token", (&OpenAITokenRefresher{}).NeedsRefresh(candidate, time.Hour))
			require.Equal(t, []string{OpenAIOSWindows}, repo.readOS, "the revision is captured once using the default identity")
		})
	}
}

func TestTokenRefreshService_OpenAISharedGrantFailurePreservesRevisionGuard(t *testing.T) {
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
					require.Equal(t, StatusActive, account.Status)
					require.True(t, account.Schedulable)
				} else if permanent {
					require.Equal(t, 1, repo.slotErrors)
					require.Equal(t, OpenAIOAuthAuthorizationAuthorized, repo.slots[OpenAIOSLinux].Status)
					require.Equal(t, StatusError, account.Status)
					require.False(t, account.Schedulable)
				} else {
					require.Equal(t, 1, repo.slotCooldowns)
					require.NotNil(t, account.TempUnschedulableUntil)
					require.Contains(t, account.TempUnschedulableReason, "token refresh retry exhausted")
				}
				require.Equal(t, repo.slots[OpenAIOSLinux].Status, repo.slots[OpenAIOSWindows].Status)
				require.Equal(t, repo.slots[OpenAIOSLinux].RefreshRetryAfter, repo.slots[OpenAIOSWindows].RefreshRetryAfter)
				require.Zero(t, repo.setErrorCalls)
				require.Zero(t, repo.setTempUnschedCalls)
				if stale {
					require.Zero(t, blocker.blockCalls)
				} else {
					require.Equal(t, 1, blocker.blockCalls)
				}
			})
		}
	}
}

func TestTokenRefreshService_OpenAIRefreshSuccessClearsAccountCooldownAndPublishesCanonicalAccount(t *testing.T) {
	account, repo := newTokenRefreshOSFixture(t)
	until := time.Now().Add(time.Hour)
	account.TempUnschedulableUntil = &until
	account.TempUnschedulableReason = "shared upstream rate limit"
	scoped, err := ResolveOpenAIOAuthCredentialAccount(context.Background(), repo, account, OpenAIOSLinux)
	require.NoError(t, err)
	cache, blocker := &tempUnschedCacheStub{}, &tokenRefreshRuntimeBlocker{}
	scheduler, invalidator := &tokenRefreshSchedulerCache{}, &tokenCacheInvalidatorStub{}
	svc := &TokenRefreshService{accountRepo: repo, tempUnschedCache: cache, runtimeBlocker: blocker, schedulerCache: scheduler, cacheInvalidator: invalidator}
	svc.postRefreshActions(context.Background(), scoped)

	require.Equal(t, 1, repo.clearTempCalls)
	require.Equal(t, 1, cache.deleteCalls)
	require.Equal(t, 1, blocker.clearCalls)
	require.Equal(t, OpenAIOSLinux, invalidator.lastAccount.OpenAIOAuthCredentialOS)
	require.Equal(t, "shared-access", invalidator.lastAccount.GetOpenAIAccessToken())
	require.Equal(t, "shared-access", scheduler.lastAccount.GetOpenAIAccessToken())
	require.Empty(t, scheduler.lastAccount.OpenAIOAuthCredentialOS)
	require.Nil(t, scheduler.lastAccount.TempUnschedulableUntil)
	require.Equal(t, OpenAIOAuthAuthorizationAuthorized, repo.slots[OpenAIOSWindows].Status)
}

func TestTokenRefreshService_OpenAILateRefreshSuccessCannotClearNewGrantCooldown(t *testing.T) {
	account, repo := newTokenRefreshOSFixture(t)
	until := time.Now().Add(time.Hour)
	account.TempUnschedulableUntil = &until
	account.TempUnschedulableReason = "new authorization cooldown"
	scoped, err := ResolveOpenAIOAuthCredentialAccount(context.Background(), repo, account, OpenAIOSLinux)
	require.NoError(t, err)
	repo.slots[OpenAIOSWindows].AuthorizationGeneration = "replacement-grant"
	cache, blocker := &tempUnschedCacheStub{}, &tokenRefreshRuntimeBlocker{}
	svc := &TokenRefreshService{accountRepo: repo, tempUnschedCache: cache, runtimeBlocker: blocker}
	svc.postRefreshActions(context.Background(), scoped)
	require.Zero(t, repo.clearTempCalls)
	require.Zero(t, cache.deleteCalls)
	require.Zero(t, blocker.clearCalls)
	require.Equal(t, &until, account.TempUnschedulableUntil)
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

func TestOpenAITokenRefresher_LockScopeSharedAcrossOSAndFencedByGeneration(t *testing.T) {
	account, repo := newTokenRefreshOSFixture(t)
	windows, err := ResolveOpenAIOAuthCredentialAccount(context.Background(), repo, account, OpenAIOSWindows)
	require.NoError(t, err)
	linux, err := ResolveOpenAIOAuthCredentialAccount(context.Background(), repo, account, OpenAIOSLinux)
	require.NoError(t, err)
	refresher := &OpenAITokenRefresher{}
	key := refresher.CacheKey(windows)
	require.Equal(t, key, refresher.CacheKey(linux))
	windows.OpenAIOAuthAuthorizationGeneration = "replacement-authorization"
	require.NotEqual(t, key, refresher.CacheKey(windows))
}
