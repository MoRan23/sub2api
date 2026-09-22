package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Legacy auxiliary tests model one explicitly authorized default slot. The
// production resolver remains strict when credential storage is unavailable.
type auxiliaryOSLegacyTestRepository struct {
	AccountRepository
	OpenAIOAuthOSCredentialsRepository
	mu        sync.Mutex
	accounts  map[int64]*Account
	errors    map[int64]string
	cooldowns map[int64]time.Time
}

func (r *auxiliaryOSLegacyTestRepository) GetByID(ctx context.Context, id int64) (*Account, error) {
	r.mu.Lock()
	a := r.accounts[id]
	r.mu.Unlock()
	if a != nil {
		return a, nil
	}
	if r.AccountRepository != nil {
		return r.AccountRepository.GetByID(ctx, id)
	}
	return nil, ErrAccountNotFound
}

func (r *auxiliaryOSLegacyTestRepository) GetOpenAIOAuthOSCredential(ctx context.Context, id int64, os string) (*OpenAIOAuthOSCredential, error) {
	a, err := r.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if a.OpenAIOAuthOSProfiles == nil || os != a.OpenAIOAuthOSProfiles.DefaultOS {
		return nil, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	status := OpenAIOAuthAuthorizationAuthorized
	if r.errors[id] != "" {
		status = OpenAIOAuthAuthorizationReauthRequired
	}
	slot := &OpenAIOAuthOSCredential{OwnerAccountID: id, OSFamily: os, Credentials: a.Credentials, AuthorizationGeneration: "fixture-authorization", Revision: 1, Status: status}
	if until, ok := r.cooldowns[id]; ok {
		slot.RefreshRetryAfter = &until
	}
	return slot, nil
}

func (r *auxiliaryOSLegacyTestRepository) ListOpenAIOAuthOSCredentials(ctx context.Context, id int64) ([]*OpenAIOAuthOSCredential, error) {
	a, err := r.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	slot, err := r.GetOpenAIOAuthOSCredential(ctx, id, a.OpenAIOAuthOSProfiles.DefaultOS)
	return []*OpenAIOAuthOSCredential{slot}, err
}

func (r *auxiliaryOSLegacyTestRepository) SetOpenAIOAuthOSCredentialErrorIfUnchanged(_ context.Context, id int64, _ string, _ string, _ int64, message string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errors[id] = message
	return true, nil
}

func (r *auxiliaryOSLegacyTestRepository) PatchOpenAIOAuthOSCredentialsIfUnchanged(_ context.Context, id int64, _ string, _ string, _ int64, _ *int64, updates map[string]any, deletes []string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a := r.accounts[id]
	if a == nil {
		return false, nil
	}
	for key, value := range updates {
		a.Credentials[key] = value
	}
	for _, key := range deletes {
		delete(a.Credentials, key)
	}
	return true, nil
}

func (r *auxiliaryOSLegacyTestRepository) SetOpenAIOAuthOSCredentialCooldownIfUnchanged(_ context.Context, id int64, _ string, _ string, _ int64, until time.Time, _ string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cooldowns[id] = until
	return true, nil
}

var auxiliaryOSFixtureMu sync.Mutex

func registerAuxiliaryOSFixture(t *testing.T, gateway *OpenAIGatewayService, account *Account) {
	t.Helper()
	auxiliaryOSFixtureMu.Lock()
	defer auxiliaryOSFixtureMu.Unlock()
	if !IsOpenAIOAuthOSProfileOwner(account) {
		return
	}
	if account.OpenAIOAuthOSProfiles == nil {
		if account.Credentials == nil {
			account.Credentials = make(map[string]any)
		}
		if account.GetOpenAIUserAgent() == "" {
			account.Credentials["user_agent"] = CodexCanonicalUserAgent()
		}
		profiles, err := BuildOpenAIOAuthOSProfiles(account, nil)
		require.NoError(t, err)
		profile := profiles.Profiles[profiles.DefaultOS]
		profile.Authorization.Status = OpenAIOAuthAuthorizationAuthorized
		profiles.Profiles[profiles.DefaultOS] = profile
		ApplyOpenAIOAuthOSProfiles(account, profiles)
	}
	repo, ok := gateway.accountRepo.(*auxiliaryOSLegacyTestRepository)
	if !ok {
		repo = &auxiliaryOSLegacyTestRepository{AccountRepository: gateway.accountRepo, accounts: map[int64]*Account{}, errors: map[int64]string{}, cooldowns: map[int64]time.Time{}}
		gateway.accountRepo = repo
		if gateway.rateLimitService != nil {
			gateway.rateLimitService.accountRepo = repo
		}
	}
	repo.mu.Lock()
	repo.accounts[account.ID] = account
	repo.mu.Unlock()
}

func fetchAuthorizedCodexModelsFixture(t *testing.T, gateway *OpenAIGatewayService, ctx context.Context, account *Account, version, etag string) (*OpenAIModelsResponse, error) {
	registerAuxiliaryOSFixture(t, gateway, account)
	return gateway.FetchCodexModelsManifest(ctx, account, version, etag)
}

func auxiliaryOSFixtureRepository(t *testing.T, base AccountRepository, accounts ...*Account) AccountRepository {
	t.Helper()
	gateway := &OpenAIGatewayService{accountRepo: base}
	for _, account := range accounts {
		registerAuxiliaryOSFixture(t, gateway, account)
	}
	return gateway.accountRepo
}

func scopedAuxiliaryOSFixture(t *testing.T, gateway *OpenAIGatewayService, account *Account) *Account {
	t.Helper()
	registerAuxiliaryOSFixture(t, gateway, account)
	scoped, err := ResolveOpenAIOAuthCredentialAccount(context.Background(), gateway.accountRepo, account, "")
	require.NoError(t, err)
	return scoped
}
