package service

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Fixtures keep one account authorization and select only the transport identity
// by OS, matching the repository's shared credential metadata projection.
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

func codexStateTestOwnerAccount(account *Account) *Account {
	if account == nil || !IsOpenAIOAuthOSProfileOwner(account) {
		return account
	}
	copy := *codexStateTestScopeAccount(account)
	copy.OpenAIOAuthCredentialOS, copy.OpenAIOAuthAuthorizationGeneration = "", ""
	copy.OpenAIOAuthCredentialOwnerID = 0
	return &copy
}

func codexStateTestSlot(account *Account, os string) (*OpenAIOAuthOSCredential, error) {
	account = codexStateTestScopeAccount(account)
	if account == nil || account.OpenAIOAuthOSProfiles == nil {
		return nil, ErrOpenAIOAuthOSUnauthorized
	}
	if _, exists := account.OpenAIOAuthOSProfiles.Profiles[os]; !exists {
		return nil, ErrOpenAIOAuthOSProfileUnavailable
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
	mu            sync.Mutex
	owner         *Account
	authorization *OpenAIOAuthOSCredential
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
	if r.authorization == nil {
		return nil, nil
	}
	copy := *r.authorization
	copy.OSFamily, copy.Credentials = os, r.owner.Credentials
	return &copy, nil
}
func (r *codexStateOSAccounts) ListOpenAIOAuthOSCredentials(context.Context, int64) ([]*OpenAIOAuthOSCredential, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.authorization == nil {
		return nil, nil
	}
	copy := *r.authorization
	copy.OSFamily, copy.Credentials = r.owner.OpenAIOAuthOSProfiles.DefaultOS, r.owner.Credentials
	return []*OpenAIOAuthOSCredential{&copy}, nil
}

func newCodexStateOSService(t *testing.T) (*CodexTurnStateService, *codexStateMemoryRepo, *codexStateOSAccounts) {
	s, repo, owner := newCodexStateTestService(t)
	owner = codexStateTestScopeAccount(owner)
	owner.OpenAIOAuthCredentialOS, owner.OpenAIOAuthAuthorizationGeneration = "", ""
	owner.OpenAIOAuthCredentialOwnerID = 0
	accounts := &codexStateOSAccounts{owner: owner, authorization: &OpenAIOAuthOSCredential{
		OwnerAccountID: owner.ID, AuthorizationGeneration: "shared-auth", Revision: 1,
		StateGeneration: "shared-state", CredentialEpoch: "shared-epoch", Status: OpenAIOAuthAuthorizationAuthorized,
	}}
	s.accounts = accounts
	return s, repo, accounts
}

func TestCodexTurnStateOSCacheSharedAndAuthorizationFencesEveryIdentity(t *testing.T) {
	s, repo, accounts := newCodexStateOSService(t)
	ctx := context.Background()
	windows, err := ResolveOpenAIOAuthCredentialAccount(ctx, accounts, accounts.owner, "windows")
	require.NoError(t, err)
	linux, err := ResolveOpenAIOAuthCredentialAccount(ctx, accounts, accounts.owner, "linux")
	require.NoError(t, err)
	require.Equal(t, windows.GetOpenAIAccessToken(), linux.GetOpenAIAccessToken())
	require.Equal(t, windows.OpenAIOAuthAuthorizationGeneration, linux.OpenAIOAuthAuthorizationGeneration)
	require.NotEqual(t, windows.GetCredential("user_agent"), linux.GetCredential("user_agent"))
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
	require.Equal(t, token, b.Snapshot.Token, "the Linux identity reuses the Windows response cache")
	require.Equal(t, a.key, b.key)
	require.Empty(t, b.key.OSFamily)
	require.Equal(t, "windows", a.OSFamily)
	require.Equal(t, "linux", b.OSFamily)
	previousNow := s.now()
	s.now = func() time.Time { return previousNow.Add(time.Second) }
	markCodexStateTestBusinessSent(t, s, b)
	token = codexStateTestToken(10, s.now())
	s.Observe(b, token)
	require.NoError(t, s.Finish(ctx, b, true))
	c, err := s.Prepare(ctx, windows, "gpt-5")
	require.NoError(t, err)
	require.Equal(t, token, c.Snapshot.Token)
	b, err = s.Prepare(ctx, linux, "gpt-5")
	require.NoError(t, err)
	require.Equal(t, token, b.Snapshot.Token)
	require.True(t, s.ValidateAttempt(ctx, c))
	require.True(t, s.ValidateAttempt(ctx, b))
	records, err := repo.ListByAccount(ctx, accounts.owner.ID)
	require.NoError(t, err)
	require.Len(t, records, 1, "identity changes must not duplicate the model cache")
	require.Equal(t, "linux", records[0].OSFamily, "the shared cache preserves the physical response's source identity")
	windowsStatus, err := s.GetStatusForOS(ctx, accounts.owner.ID, "windows")
	require.NoError(t, err)
	linuxStatus, err := s.GetStatusForOS(ctx, accounts.owner.ID, "linux")
	require.NoError(t, err)
	require.Equal(t, windowsStatus.Models, linuxStatus.Models, "the observation selector must not partition cached models")
	require.Len(t, linuxStatus.Models, 1)
	require.True(t, linuxStatus.Models[0].CacheAvailable)
	require.Equal(t, "shared", linuxStatus.Models[0].CacheScope)
	require.Equal(t, "linux", linuxStatus.Models[0].OSFamily)
	accounts.authorization.AuthorizationGeneration = "shared-reauthorized"
	accounts.authorization.StateGeneration = "shared-rotated"
	require.False(t, s.ValidateAttempt(ctx, c))
	require.False(t, s.ValidateAttempt(ctx, b))
	for _, os := range []string{"windows", "linux"} {
		fresh, resolveErr := ResolveOpenAIOAuthCredentialAccount(ctx, accounts, accounts.owner, os)
		require.NoError(t, resolveErr)
		next, prepareErr := s.Prepare(ctx, fresh, "gpt-5")
		require.NoError(t, prepareErr)
		require.Empty(t, next.Snapshot.Token, "a new shared authorization never reuses the old generation")
	}
	accounts.authorization = nil
	accounts.owner.Credentials = map[string]any{}
	accounts.owner.Status = StatusError
	accounts.owner.Schedulable = false
	for _, account := range []*Account{windows, linux} {
		attempt, prepareErr := s.Prepare(ctx, account, "gpt-5")
		require.NoError(t, prepareErr, "cache preparation does not replace account scheduling or token validation")
		require.NotNil(t, attempt)
		require.False(t, attempt.Enabled)
		require.Empty(t, attempt.Snapshot.Token, "a revoked account cannot reuse a ticket on any identity")
		require.False(t, s.ValidateAttempt(ctx, attempt))
	}
}

func TestCodexTurnStateOSCollectorUsesDefaultIdentityAndSharedAuthPause(t *testing.T) {
	s, repo, accounts := newCodexStateOSService(t)
	ctx := context.Background()
	var collected []string
	s.collector = codexStateTestCollector(func(_ context.Context, in CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error) {
		collected = append(collected, codexTurnStateOS(in.Account))
		require.Equal(t, accounts.owner.GetOpenAIAccessToken(), in.Account.GetOpenAIAccessToken())
		return CodexTurnStateCollectResult{StatusCode: 401}, nil
	})
	keys := map[string]CodexTurnStateKey{}
	for _, os := range []string{"windows", "linux"} {
		account, err := ResolveOpenAIOAuthCredentialAccount(ctx, accounts, accounts.owner, os)
		require.NoError(t, err)
		a := seedCodexStateTestDemand(t, s, account, "gpt-5")
		keys[os] = a.key
	}
	require.Equal(t, keys["windows"], keys["linux"])
	s.collect(ctx, keys["windows"])
	s.collect(ctx, keys["linux"])
	require.Equal(t, []string{"windows"}, collected, "one account authorization pause covers every identity")
	for _, key := range keys {
		record, err := repo.Get(ctx, key)
		require.NoError(t, err)
		require.True(t, record.CollectorPaused)
	}
}

func TestCodexTurnStateOSSharedHistoryRetainsActualObservationIdentity(t *testing.T) {
	isolateCodexHistory(t)
	isolateCodexTurnStateSummaryStore(t)
	s, memory, accounts := newCodexStateOSService(t)
	repo := &codexHistoryTestRepository{codexStateMemoryRepo: memory}
	s.repo = repo
	ctx := context.Background()
	accounts.owner.Extra[CodexTurnStateExtraKey].(map[string]any)["enabled"] = false
	// Historical evidence retains its physical OS while authorizing shared demand.
	for _, os := range []string{"windows", "linux"} {
		owner, err := ResolveOpenAIOAuthCredentialAccount(ctx, accounts, accounts.owner, os)
		require.NoError(t, err)
		a, err := s.Prepare(ctx, owner, "gpt-5")
		require.NoError(t, err)
		s.bindHistoryCredentials(ctx, a, http.Header{"Authorization": {"Bearer " + owner.GetOpenAIAccessToken()}})
		markCodexStateTestBusinessSent(t, s, a)
		s.Observe(a, codexStateTestToken(11, s.now()))
		require.NoError(t, s.Finish(ctx, a, true))
		length := 292
		if os == "linux" {
			length = 312
		}
		s.recordCollectorObservation(owner, "gpt-5", CodexTurnStateCollectResult{Observation: &CodexTurnStateSafeObservation{
			ObservedAt: s.now(), TokenLength: length, Shape: "target", EnvelopeValid: true,
			IssuedAt: s.now(), ExpiresAt: s.now().Add(CodexTurnStateLifetime),
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
		require.Equal(t, "shared", status.CacheScope)
		require.Len(t, status.Observations, 1)
		require.Equal(t, os, status.Observations[0].OSFamily)
		require.Equal(t, map[string]int{"windows": 292, "linux": 312}[os], status.Observations[0].ResponseLength)
		batch, err := s.GetStatusesForOS(ctx, []int64{owner.ID}, os)
		require.NoError(t, err)
		require.Equal(t, status.Observations, batch.Items["1"].Observations)
	}
	require.Len(t, repo.proofs, 2)
	require.NotEqual(t, repo.proofs[0].OSFamily, repo.proofs[1].OSFamily)
	accounts.authorization = nil
	for _, os := range []string{"windows", "linux"} {
		status, err := s.GetStatusForOS(ctx, accounts.owner.ID, os)
		require.NoError(t, err)
		require.False(t, status.Enabled)
		require.Equal(t, "authorization_unavailable", status.Reason)
		require.Empty(t, status.Models)
		require.Empty(t, status.Observations)
	}
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
		accounts.authorization.AuthorizationGeneration = "reauthorized-auth"
		accounts.authorization.StateGeneration = "reauthorized-state"
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
