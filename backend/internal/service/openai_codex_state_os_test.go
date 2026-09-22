package service

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Existing single-grant fixtures represent an explicitly authorized Windows
// slot. Production never falls back to the account's legacy credential mirror.
func codexStateTestScopeAccount(account *Account) *Account {
	if account == nil || !IsOpenAIOAuthOSProfileOwner(account) {
		return account
	}
	copy := *account
	if copy.OpenAIOAuthOSProfiles == nil {
		profiles, err := BuildOpenAIOAuthOSProfiles(&copy, nil)
		if err != nil {
			panic(err)
		}
		copy.OpenAIOAuthOSProfiles = profiles
	}
	copy.OpenAIOAuthOSProfiles = CloneOpenAIOAuthOSProfiles(copy.OpenAIOAuthOSProfiles)
	profile := copy.OpenAIOAuthOSProfiles.Profiles[copy.OpenAIOAuthOSProfiles.DefaultOS]
	profile.Authorization.Status = OpenAIOAuthAuthorizationAuthorized
	copy.OpenAIOAuthOSProfiles.Profiles[copy.OpenAIOAuthOSProfiles.DefaultOS] = profile
	copy.OpenAIOAuthCredentialOS = copy.OpenAIOAuthOSProfiles.DefaultOS
	copy.OpenAIOAuthCredentialOwnerID = copy.ID
	copy.OpenAIOAuthAuthorizationGeneration = "test-authorization"
	return &copy
}

func codexStateTestSlot(account *Account, os string) (*OpenAIOAuthOSCredential, error) {
	account = codexStateTestScopeAccount(account)
	if account == nil || os != account.OpenAIOAuthOSProfiles.DefaultOS {
		return nil, ErrOpenAIOAuthOSUnauthorized
	}
	return &OpenAIOAuthOSCredential{OwnerAccountID: account.ID, OSFamily: os, Credentials: account.Credentials,
		AuthorizationGeneration: "test-authorization", Revision: 1, StateGeneration: CodexTurnStateGenerationForAccount(account),
		CredentialEpoch: CodexTurnStateCredentialEpochForAccount(account), Status: OpenAIOAuthAuthorizationAuthorized}, nil
}

func (r *codexStateTestAccounts) GetOpenAIOAuthOSCredential(_ context.Context, id int64, os string) (*OpenAIOAuthOSCredential, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.account == nil || r.account.ID != id {
		return nil, ErrAccountNotFound
	}
	return codexStateTestSlot(r.account, os)
}
func (r *codexStateTestAccounts) ListOpenAIOAuthOSCredentials(ctx context.Context, id int64) ([]*OpenAIOAuthOSCredential, error) {
	slot, err := r.GetOpenAIOAuthOSCredential(ctx, id, "windows")
	return []*OpenAIOAuthOSCredential{slot}, err
}
func (r *codexWSStateTestAccounts) GetOpenAIOAuthOSCredential(ctx context.Context, id int64, os string) (*OpenAIOAuthOSCredential, error) {
	a, err := r.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	return codexStateTestSlot(a, os)
}
func (r *codexWSStateTestAccounts) ListOpenAIOAuthOSCredentials(ctx context.Context, id int64) ([]*OpenAIOAuthOSCredential, error) {
	slot, err := r.GetOpenAIOAuthOSCredential(ctx, id, "windows")
	return []*OpenAIOAuthOSCredential{slot}, err
}
func (r *codexStateBatchAccounts) GetOpenAIOAuthOSCredential(_ context.Context, id int64, os string) (*OpenAIOAuthOSCredential, error) {
	return codexStateTestSlot(r.accounts[id], os)
}
func (r *codexStateBatchAccounts) ListOpenAIOAuthOSCredentials(ctx context.Context, id int64) ([]*OpenAIOAuthOSCredential, error) {
	slot, err := r.GetOpenAIOAuthOSCredential(ctx, id, "windows")
	return []*OpenAIOAuthOSCredential{slot}, err
}
func (r codexCollectorTransportAccounts) GetOpenAIOAuthOSCredential(_ context.Context, id int64, os string) (*OpenAIOAuthOSCredential, error) {
	return codexStateTestSlot(r.account, os)
}
func (r codexCollectorTransportAccounts) ListOpenAIOAuthOSCredentials(ctx context.Context, id int64) ([]*OpenAIOAuthOSCredential, error) {
	slot, err := r.GetOpenAIOAuthOSCredential(ctx, id, "windows")
	return []*OpenAIOAuthOSCredential{slot}, err
}
func (r *codexStatePassthroughAccounts) GetOpenAIOAuthOSCredential(ctx context.Context, id int64, os string) (*OpenAIOAuthOSCredential, error) {
	a, err := r.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	return codexStateTestSlot(a, os)
}
func (r *codexStatePassthroughAccounts) ListOpenAIOAuthOSCredentials(ctx context.Context, id int64) ([]*OpenAIOAuthOSCredential, error) {
	slot, err := r.GetOpenAIOAuthOSCredential(ctx, id, "windows")
	return []*OpenAIOAuthOSCredential{slot}, err
}

type codexStateOSAccounts struct {
	AccountRepository
	mu    sync.Mutex
	owner *Account
	slots map[string]*OpenAIOAuthOSCredential
}

type codexStateOSCooldownRepo struct {
	*codexStateMemoryRepo
	cooldowns map[int64]time.Time
}

func (r *codexStateOSCooldownRepo) ExtendCollectorCooldown(_ context.Context, id int64, until time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if until.After(r.cooldowns[id]) {
		r.cooldowns[id] = until
	}
	return nil
}

func (r *codexStateOSCooldownRepo) GetCollectorCooldowns(_ context.Context, ids []int64) (map[int64]time.Time, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make(map[int64]time.Time)
	for _, id := range ids {
		result[id] = r.cooldowns[id]
	}
	return result, nil
}

func (r *codexStateOSAccounts) GetByID(context.Context, int64) (*Account, error) { return r.owner, nil }
func (r *codexStateOSAccounts) GetByIDs(context.Context, []int64) ([]*Account, error) {
	return []*Account{r.owner}, nil
}
func (r *codexStateOSAccounts) GetOpenAIOAuthOSCredential(_ context.Context, _ int64, os string) (*OpenAIOAuthOSCredential, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.slots[os] == nil {
		return nil, nil
	}
	copy := *r.slots[os]
	return &copy, nil
}
func (r *codexStateOSAccounts) ListOpenAIOAuthOSCredentials(context.Context, int64) ([]*OpenAIOAuthOSCredential, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var slots []*OpenAIOAuthOSCredential
	for _, s := range r.slots {
		copy := *s
		slots = append(slots, &copy)
	}
	return slots, nil
}

func newCodexStateOSService(t *testing.T) (*CodexTurnStateService, *codexStateMemoryRepo, *codexStateOSAccounts) {
	s, repo, owner := newCodexStateTestService(t)
	owner = codexStateTestScopeAccount(owner)
	owner.OpenAIOAuthCredentialOS, owner.OpenAIOAuthAuthorizationGeneration = "", ""
	owner.OpenAIOAuthCredentialOwnerID = 0
	accounts := &codexStateOSAccounts{owner: owner, slots: map[string]*OpenAIOAuthOSCredential{}}
	for _, os := range []string{"windows", "linux"} {
		accounts.slots[os] = &OpenAIOAuthOSCredential{OwnerAccountID: owner.ID, OSFamily: os, Credentials: map[string]any{"access_token": os + "-token", "plan_type": "plus"}, AuthorizationGeneration: os + "-auth", Revision: 1, StateGeneration: os + "-state", CredentialEpoch: os + "-epoch", Status: OpenAIOAuthAuthorizationAuthorized}
	}
	s.accounts = accounts
	return s, repo, accounts
}

func TestCodexTurnStateOSCacheAndRevocationIsolation(t *testing.T) {
	s, _, accounts := newCodexStateOSService(t)
	ctx := context.Background()
	windows, err := ResolveOpenAIOAuthCredentialAccount(ctx, accounts, accounts.owner, "windows")
	require.NoError(t, err)
	linux, err := ResolveOpenAIOAuthCredentialAccount(ctx, accounts, accounts.owner, "linux")
	require.NoError(t, err)
	a, err := s.Prepare(ctx, windows, "gpt-5")
	require.NoError(t, err)
	require.True(t, a.Enabled)
	markCodexStateTestBusinessSent(t, s, a)
	token := codexStateTestToken(10, s.now())
	s.Observe(a, token)
	require.NoError(t, s.Finish(ctx, a, true))
	b, err := s.Prepare(ctx, linux, "gpt-5")
	require.NoError(t, err)
	require.True(t, b.Enabled)
	require.Empty(t, b.Snapshot.Token)
	c, err := s.Prepare(ctx, windows, "gpt-5")
	require.NoError(t, err)
	require.Equal(t, token, c.Snapshot.Token)
	accounts.slots["linux"].StateGeneration = "linux-rotated"
	require.True(t, s.ValidateAttempt(ctx, c))
	require.False(t, s.ValidateAttempt(ctx, b))
	delete(accounts.slots, "linux")
	_, err = s.Prepare(ctx, linux, "gpt-5")
	require.Error(t, err)
}

func TestCodexTurnStateOSCollectorUsesDemandSlotAndLocalAuthPause(t *testing.T) {
	s, repo, accounts := newCodexStateOSService(t)
	ctx := context.Background()
	var collected []string
	s.collector = codexStateTestCollector(func(_ context.Context, in CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error) {
		collected = append(collected, codexTurnStateOS(in.Account))
		require.Equal(t, codexTurnStateOS(in.Account)+"-token", in.Account.GetOpenAIAccessToken())
		return CodexTurnStateCollectResult{StatusCode: 401}, nil
	})
	keys := map[string]CodexTurnStateKey{}
	for _, os := range []string{"windows", "linux"} {
		account, err := ResolveOpenAIOAuthCredentialAccount(ctx, accounts, accounts.owner, os)
		require.NoError(t, err)
		a := seedCodexStateTestDemand(t, s, account, "gpt-5")
		keys[os] = a.key
	}
	s.collect(ctx, keys["windows"])
	s.collect(ctx, keys["linux"])
	require.Equal(t, []string{"windows", "linux"}, collected)
	for _, key := range keys {
		record, err := repo.Get(ctx, key)
		require.NoError(t, err)
		require.True(t, record.CollectorPaused)
	}
}

func TestCodexTurnStateOSHistoryAndObservationIsolation(t *testing.T) {
	isolateCodexHistory(t)
	isolateCodexTurnStateSummaryStore(t)
	s, memory, accounts := newCodexStateOSService(t)
	repo := &codexHistoryTestRepository{codexStateMemoryRepo: memory}
	s.repo = repo
	ctx := context.Background()
	accounts.owner.Extra[CodexTurnStateExtraKey].(map[string]any)["enabled"] = false
	// Even an equal credential epoch cannot combine OS proof or observation keys.
	accounts.slots["linux"].CredentialEpoch = accounts.slots["windows"].CredentialEpoch
	for _, os := range []string{"windows", "linux"} {
		owner, err := ResolveOpenAIOAuthCredentialAccount(ctx, accounts, accounts.owner, os)
		require.NoError(t, err)
		a, err := s.Prepare(ctx, owner, "gpt-5")
		require.NoError(t, err)
		s.bindHistoryCredentials(ctx, a, http.Header{"Authorization": {"Bearer " + os + "-token"}})
		markCodexStateTestBusinessSent(t, s, a)
		s.Observe(a, codexStateTestToken(11, s.now()))
		require.NoError(t, s.Finish(ctx, a, true))
		length := 292
		if os == "linux" {
			length = 312
		}
		s.recordCollectorObservation(owner, "gpt-5", CodexTurnStateCollectResult{Observation: &CodexTurnStateSafeObservation{
			ObservedAt: s.now(), TokenLength: length, Shape: "target", EnvelopeValid: true,
			IssuedAt: s.now(), ExpiresAt: s.now().Add(time.Hour),
		}})
	}
	proofs := codexStateHistorySnapshot(accounts.owner.ID, s.now().Add(-time.Minute))
	require.Len(t, proofs, 2)
	accounts.owner.Extra[CodexTurnStateExtraKey].(map[string]any)["enabled"] = true
	for _, os := range []string{"windows", "linux"} {
		owner, err := ResolveOpenAIOAuthCredentialAccount(ctx, accounts, accounts.owner, os)
		require.NoError(t, err)
		s.activateHistoryForOwner(ctx, owner, CodexTurnStateGenerationForAccount(owner))
		status, err := s.GetStatusForOS(ctx, owner.ID, os)
		require.NoError(t, err)
		require.Equal(t, os, status.OSFamily)
		require.Len(t, status.Observations, 1)
		require.Equal(t, os, status.Observations[0].OSFamily)
		batch, err := s.GetStatusesForOS(ctx, []int64{owner.ID}, os)
		require.NoError(t, err)
		require.Equal(t, status.Observations, batch.Items["1"].Observations)
	}
	require.Len(t, repo.proofs, 2)
	require.NotEqual(t, repo.proofs[0].OSFamily, repo.proofs[1].OSFamily)
	delete(accounts.slots, "linux")
	status, err := s.GetStatusForOS(ctx, accounts.owner.ID, "linux")
	require.NoError(t, err)
	require.False(t, status.Enabled)
	require.Equal(t, "authorization_unavailable", status.Reason)
	require.Empty(t, status.Models)
	require.Empty(t, status.Observations)
}

func TestCodexTurnStateOSShared429SurvivesReauthorizationAndBusinessSuccess(t *testing.T) {
	s, memory, accounts := newCodexStateOSService(t)
	repo := &codexStateOSCooldownRepo{codexStateMemoryRepo: memory, cooldowns: make(map[int64]time.Time)}
	s.repo = repo
	ctx := context.Background()
	var collected []string
	s.collector = codexStateTestCollector(func(_ context.Context, in CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error) {
		collected = append(collected, codexTurnStateOS(in.Account))
		// Reauthorization races the physical 429, before state CAS can accept it.
		accounts.mu.Lock()
		accounts.slots["windows"].StateGeneration = "reauthorized"
		accounts.mu.Unlock()
		return CodexTurnStateCollectResult{StatusCode: 429, RetryAfter: time.Hour}, nil
	})
	keys := map[string]CodexTurnStateKey{}
	for _, os := range []string{"windows", "linux"} {
		account, err := ResolveOpenAIOAuthCredentialAccount(ctx, accounts, accounts.owner, os)
		require.NoError(t, err)
		keys[os] = seedCodexStateTestDemand(t, s, account, "gpt-5").key
	}
	s.collect(ctx, keys["windows"])
	require.Len(t, collected, 1)
	delete(accounts.slots, "windows")
	linux, err := ResolveOpenAIOAuthCredentialAccount(ctx, accounts, accounts.owner, "linux")
	require.NoError(t, err)
	natural, err := s.Prepare(ctx, linux, "gpt-5")
	require.NoError(t, err)
	markCodexStateTestBusinessSent(t, s, natural)
	s.Observe(natural, codexStateTestToken(10, s.now()))
	require.NoError(t, s.Finish(ctx, natural, true))
	keys["linux"] = seedCodexStateTestDemand(t, s, linux, "gpt-5.4").key
	s.collect(ctx, keys["linux"])
	require.Equal(t, []string{"windows"}, collected)
	until, err := repo.GetCollectorCooldowns(ctx, []int64{accounts.owner.ID})
	require.NoError(t, err)
	require.Equal(t, s.now().Add(time.Hour), until[accounts.owner.ID])
	status, err := s.GetStatusForOS(ctx, accounts.owner.ID, "linux")
	require.NoError(t, err)
	for _, model := range status.Models {
		if model.Model == "gpt-5.4" {
			require.Equal(t, "backoff", model.CollectionStatus)
			require.Equal(t, until[accounts.owner.ID], *model.NextCollectAt)
		}
	}
}
