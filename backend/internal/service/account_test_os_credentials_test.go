package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type accountTestOSRepository struct {
	AccountRepository
	OpenAIOAuthOSCredentialsRepository
	accounts           map[int64]*Account
	slots              map[string]*OpenAIOAuthOSCredential
	readOS             string
	failedOS           string
	recoveredOS        string
	failedAccountID    int64
	recoveredAccountID int64
}

func (r *accountTestOSRepository) MutateOpenAIOAuthAccountStateIfUnchanged(_ context.Context, accountID int64, snapshot OpenAIOAuthAccountStateSnapshot, change OpenAIOAuthAccountStateChange) (*OpenAIOAuthAccountStateResult, error) {
	result := &OpenAIOAuthAccountStateResult{}
	var grant *OpenAIOAuthOSCredential
	for _, candidate := range r.slots {
		if candidate.OwnerAccountID == snapshot.OwnerAccountID && candidate.AuthorizationGeneration == snapshot.AuthorizationGeneration && candidate.Revision == snapshot.CredentialRevision {
			grant = candidate
			break
		}
	}
	account := r.accounts[accountID]
	if grant == nil || account == nil {
		return result, nil
	}
	result.Applied = true
	switch change.Kind {
	case OpenAIOAuthAccountStateError:
		r.failedAccountID = accountID
		account.Status, account.ErrorMessage, account.Schedulable = StatusError, change.ErrorMessage, false
	case OpenAIOAuthAccountStateClearError:
		result.ClearedError = account.Status == StatusError
		account.Status, account.ErrorMessage = StatusActive, ""
	case OpenAIOAuthAccountStateRecover:
		r.recoveredAccountID = accountID
		result.ClearedError, result.ClearedRateLimit = account.Status == StatusError, hasRecoverableRuntimeState(account)
		if result.ClearedError {
			account.Status, account.ErrorMessage = StatusActive, ""
		}
		if result.ClearedRateLimit {
			account.RateLimitedAt, account.RateLimitResetAt, account.OverloadUntil, account.TempUnschedulableUntil = nil, nil, nil, nil
			account.TempUnschedulableReason = ""
			delete(account.Extra, "model_rate_limits")
			delete(account.Extra, "antigravity_quota_scopes")
		}
	}
	return result, nil
}

func (r *accountTestOSRepository) SetOpenAIOAuthOSCredentialErrorIfUnchanged(_ context.Context, _ int64, os, generation string, revision int64, _ string) (bool, error) {
	slot := r.slots[os]
	if slot == nil || slot.AuthorizationGeneration != generation || slot.Revision != revision {
		return false, nil
	}
	r.failedOS = os
	slot.Status = OpenAIOAuthAuthorizationReauthRequired
	return true, nil
}
func (r *accountTestOSRepository) PatchOpenAIOAuthOSCredentialsIfUnchanged(_ context.Context, _ int64, os, generation string, revision int64, _ *int64, _ map[string]any, _ []string) (bool, error) {
	slot := r.slots[os]
	if slot == nil || slot.AuthorizationGeneration != generation || slot.Revision != revision {
		return false, nil
	}
	r.recoveredOS = os
	return true, nil
}

func (r *accountTestOSRepository) GetByID(_ context.Context, id int64) (*Account, error) {
	return r.accounts[id], nil
}
func (r *accountTestOSRepository) GetOpenAIOAuthOSCredential(_ context.Context, _ int64, os string) (*OpenAIOAuthOSCredential, error) {
	r.readOS = os
	return r.slots[os], nil
}
func (r *accountTestOSRepository) ListOpenAIOAuthOSCredentials(context.Context, int64) ([]*OpenAIOAuthOSCredential, error) {
	var slots []*OpenAIOAuthOSCredential
	for _, slot := range r.slots {
		slots = append(slots, slot)
	}
	return slots, nil
}

func (r *accountTestOSRepository) UpdateExtra(ctx context.Context, id int64, values map[string]any) error {
	if r.AccountRepository == nil {
		return nil
	}
	return r.AccountRepository.UpdateExtra(ctx, id, values)
}
func (r *accountTestOSRepository) SetRateLimited(ctx context.Context, id int64, at time.Time) error {
	if r.AccountRepository == nil {
		return nil
	}
	return r.AccountRepository.SetRateLimited(ctx, id, at)
}
func (r *accountTestOSRepository) SetError(ctx context.Context, id int64, message string) error {
	if r.AccountRepository == nil {
		return nil
	}
	return r.AccountRepository.SetError(ctx, id, message)
}
func (r *accountTestOSRepository) ClearError(ctx context.Context, id int64) error {
	if r.AccountRepository == nil {
		return nil
	}
	return r.AccountRepository.ClearError(ctx, id)
}

func prepareAccountTestCredential(t *testing.T, svc *AccountTestService, account *Account) *Account {
	t.Helper()
	if IsOpenAIOAuthOSProfileOwner(account) {
		svc.accountRepo = accountTestDefaultOSRepository(t, svc.accountRepo, account)
	}
	return account
}

func accountTestDefaultOSRepository(t *testing.T, repository AccountRepository, account *Account) *accountTestOSRepository {
	t.Helper()
	if account.Credentials == nil {
		account.Credentials = make(map[string]any)
	}
	account.Credentials["user_agent"] = codexCLIUserAgent
	profiles, err := BuildOpenAIOAuthOSProfiles(account, nil)
	require.NoError(t, err)
	ApplyOpenAIOAuthOSProfiles(account, profiles)
	grant := &OpenAIOAuthOSCredential{OwnerAccountID: account.ID, Credentials: OpenAIOAuthProviderCredentials(account.Credentials), Status: OpenAIOAuthAuthorizationAuthorized, AuthorizationGeneration: "test-generation", Revision: 1}
	slots := make(map[string]*OpenAIOAuthOSCredential)
	for _, os := range OpenAIOAuthOSFamilies() {
		slots[os] = grant
	}
	return &accountTestOSRepository{AccountRepository: repository, accounts: map[int64]*Account{account.ID: account}, slots: slots}
}

func TestAccountTestRejectsInvalidOSBeforeUpstream(t *testing.T) {
	account := &Account{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "windows-secret"}}
	repo := accountTestDefaultOSRepository(t, nil, account)
	upstream := &httpUpstreamRecorder{}
	svc := &AccountTestService{accountRepo: repo, httpUpstream: upstream}
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/test", nil)
	err := svc.TestAccountConnection(c, account.ID, "gpt-5.4", "", "", AccountTestOptions{OSFamily: "invalid-os"})
	require.Error(t, err)
	require.Nil(t, upstream.lastReq)
	require.Nil(t, OpenAIOAuthAccountTestCredential(c))
	require.NotContains(t, rec.Body.String(), "windows-secret")
}

func TestAccountTestUsesSharedCredentialAndSelectedOSRoot(t *testing.T) {
	account := &Account{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "default-secret"}, Concurrency: 1}
	repo := accountTestDefaultOSRepository(t, nil, account)
	upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"output\":[]}}\n\n"))}}
	svc := &AccountTestService{accountRepo: repo, httpUpstream: upstream}
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/test", nil)
	err := svc.TestAccountConnection(c, account.ID, "gpt-5.4", "", "", AccountTestOptions{OSFamily: OpenAIOSMacOS})
	require.NoError(t, err)
	require.Equal(t, "Bearer default-secret", upstream.lastReq.Header.Get("Authorization"))
	require.Equal(t, account.OpenAIOAuthOSProfiles.Profiles[OpenAIOSMacOS].SyncSessionID, upstream.lastReq.Header.Get("session-id"))
	snapshot := OpenAIOAuthAccountTestCredential(c)
	require.NotNil(t, snapshot)
	require.Equal(t, OpenAIOSMacOS, snapshot.OpenAIOAuthCredentialOS)
	require.Equal(t, "test-generation", snapshot.OpenAIOAuthAuthorizationGeneration)
	require.Equal(t, int64(1), snapshot.OpenAIOAuthCredentialRevision)
	require.Equal(t, "default-secret", account.GetCredential("access_token"))
	limits := &RateLimitService{accountRepo: repo}
	_, err = limits.RecoverOpenAIOAuthOSAfterSuccessfulTest(context.Background(), snapshot)
	require.NoError(t, err)
	require.Equal(t, account.ID, repo.recoveredAccountID)
}

func TestAccountTest401MarksAccountWithFrozenCredentialSnapshot(t *testing.T) {
	account := &Account{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Credentials: map[string]any{"access_token": "default-token"}}
	repo := accountTestDefaultOSRepository(t, nil, account)
	upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusUnauthorized, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":"expired"}`))}}
	svc := &AccountTestService{accountRepo: repo, httpUpstream: upstream}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/test", nil)
	err := svc.TestAccountConnection(c, 7, "gpt-5.4", "", "", AccountTestOptions{OSFamily: OpenAIOSMacOS})
	require.Error(t, err)
	require.Equal(t, account.ID, repo.failedAccountID)
	require.Equal(t, OpenAIOAuthAuthorizationAuthorized, repo.slots[account.OpenAIOAuthOSProfiles.DefaultOS].Status)
	require.Equal(t, OpenAIOAuthAuthorizationAuthorized, repo.slots[OpenAIOSMacOS].Status)
	require.Equal(t, StatusError, account.Status)
	require.Contains(t, account.ErrorMessage, "Authentication failed (401)")
}

func TestAccountTestLate401CannotMarkReplacementCredentials(t *testing.T) {
	account := &Account{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Credentials: map[string]any{"access_token": "test-token"}}
	repo := accountTestDefaultOSRepository(t, nil, account)
	snapshot, err := ResolveOpenAIOAuthCredentialAccount(context.Background(), repo, account, account.OpenAIOAuthOSProfiles.DefaultOS)
	require.NoError(t, err)
	repo.slots[snapshot.OpenAIOAuthCredentialOS].Revision++
	svc := &AccountTestService{accountRepo: repo}
	svc.recordOpenAIAccountTestUnauthorized(context.Background(), snapshot, "old test failure")
	require.Zero(t, repo.failedAccountID)
	require.Equal(t, StatusActive, account.Status)
	require.True(t, account.Schedulable)
}
