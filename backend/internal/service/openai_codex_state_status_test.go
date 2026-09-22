package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

type codexStateBatchAccounts struct {
	AccountRepository
	accounts   map[int64]*Account
	batchCalls [][]int64
	failCall   int
}

func (r *codexStateBatchAccounts) GetByIDs(_ context.Context, ids []int64) ([]*Account, error) {
	r.batchCalls = append(r.batchCalls, slices.Clone(ids))
	if r.failCall == len(r.batchCalls) {
		return nil, errors.New("account database unavailable")
	}
	var accounts []*Account
	for _, id := range ids {
		if account := r.accounts[id]; account != nil {
			accounts = append(accounts, codexStateTestOwnerAccount(account))
		}
	}
	return accounts, nil
}

func (r *codexStateBatchAccounts) GetByID(_ context.Context, id int64) (*Account, error) {
	return codexStateTestOwnerAccount(r.accounts[id]), nil
}

// All unimplemented runtime/collection methods panic through the embedded nil
// interface if a read endpoint ever starts to perform maintenance work.
type codexStateBatchRecords struct {
	CodexTurnStateRepository
	records    []CodexTurnStateRecord
	batchCalls [][]int64
	err        error
}

func (r *codexStateBatchRecords) ListByAccounts(_ context.Context, ids []int64) ([]CodexTurnStateRecord, error) {
	r.batchCalls = append(r.batchCalls, slices.Clone(ids))
	return r.forOwners(ids), r.err
}

func (r *codexStateBatchRecords) ListByAccount(_ context.Context, id int64) ([]CodexTurnStateRecord, error) {
	return r.forOwners([]int64{id}), r.err
}

func (r *codexStateBatchRecords) forOwners(ids []int64) []CodexTurnStateRecord {
	var records []CodexTurnStateRecord
	for _, record := range r.records {
		if slices.Contains(ids, record.OwnerAccountID) {
			records = append(records, record)
		}
	}
	return records
}

type codexStateBatchPolicy struct {
	models []string
	calls  int
	err    error
}

func (p *codexStateBatchPolicy) CodexTurnStateModelPolicy(context.Context) ([]string, string, error) {
	p.calls++
	return slices.Clone(p.models), "test-model-policy", p.err
}

func (p *codexStateBatchPolicy) CodexTurnStateModelPolicyAuthoritative(context.Context) ([]string, string, error) {
	panic("read-only status must use its single shared policy snapshot")
}

func codexStateBatchOwner(id int64, enabled bool) *Account {
	return &Account{ID: id, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Credentials: map[string]any{"plan_type": "plus", "access_token": "private-account-credential"},
		Extra:       map[string]any{CodexTurnStateExtraKey: map[string]any{"enabled": enabled, "account_type": "auto"}, CodexTurnStateGenerationExtraKey: fmt.Sprintf("private-generation-%d", id)}}
}

func codexStateBatchShadow(id, parent int64) *Account {
	return &Account{ID: id, Platform: PlatformOpenAI, Type: AccountTypeOAuth, ParentAccountID: &parent}
}

func TestCodexTurnStateBatchStatusDeduplicatesOwnersAndSharesSingleProjection(t *testing.T) {
	ownerOne, ownerTwo := codexStateBatchOwner(1, true), codexStateBatchOwner(2, true)
	ownerTwo.Credentials["plan_type"] = "self_serve_business_prolite"
	unsupported := &Account{ID: 3, Platform: PlatformAnthropic, Type: AccountTypeAPIKey}
	accounts := &codexStateBatchAccounts{accounts: map[int64]*Account{
		1: ownerOne, 2: ownerTwo, 3: unsupported, 4: codexStateBatchOwner(4, false),
		11: codexStateBatchShadow(11, 1), 12: codexStateBatchShadow(12, 1), 21: codexStateBatchShadow(21, 2),
		31: codexStateBatchShadow(31, 9), 32: codexStateBatchShadow(32, 33), 33: codexStateBatchShadow(33, 1), 34: codexStateBatchShadow(34, 3),
	}}
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	records := &codexStateBatchRecords{records: []CodexTurnStateRecord{
		{OSFamily: "windows", OwnerAccountID: 1, Generation: CodexTurnStateGenerationForAccount(ownerOne), Model: "gpt-5.6-sol", EncryptedToken: "private-encrypted-token", EncryptedCookieBundle: "private-encrypted-cookie-bundle", AuthorizationGeneration: "test-authorization", IssuedAt: now, ExpiresAt: now.Add(CodexTurnStateLifetime), TokenLength: 292, CipherBlocks: 10, Shape: "target", Source: "business"},
		{OSFamily: "linux", OwnerAccountID: 1, Generation: CodexTurnStateGenerationForAccount(ownerOne), Model: "gpt-6-astra", EncryptedToken: "private-encrypted-expired", ExpiresAt: now.Add(-time.Minute)},
		{OSFamily: "windows", OwnerAccountID: 1, Generation: "obsolete-generation", Model: "obsolete-model", EncryptedToken: "old-private-token"},
		{OSFamily: "windows", OwnerAccountID: 2, Generation: CodexTurnStateGenerationForAccount(ownerTwo), Model: "gpt-5.6-terra", CollectorPaused: true, LastError: "collector_auth_rejected"},
		{OSFamily: "windows", OwnerAccountID: 2, Generation: CodexTurnStateGenerationForAccount(ownerTwo), Model: "gpt-unlisted", EncryptedToken: "private-unlisted-token", IssuedAt: now, ExpiresAt: now.Add(CodexTurnStateLifetime)},
	}}
	policy := &codexStateBatchPolicy{models: []string{"gpt-5.6-terra", "gpt-5.6-sol", "gpt-6-astra"}}
	service := NewCodexTurnStateService(records, accounts, nil, codexStateTestCollector(func(context.Context, CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error) {
		t.Fatal("status reads must not start standalone collection")
		return CodexTurnStateCollectResult{}, nil
	}))
	service.modelPolicy, service.now = policy, func() time.Time { return now }
	result, err := service.GetStatuses(context.Background(), []int64{11, 12, 11, 21, 3, 4, 31, 32, 34, 999})
	require.NoError(t, err)
	require.Equal(t, [][]int64{{11, 12, 21, 3, 4, 31, 32, 34, 999}, {1, 2, 9, 33}}, accounts.batchCalls)
	require.Equal(t, [][]int64{{1, 2, 3, 4}}, records.batchCalls)
	require.Equal(t, 1, policy.calls, "the complete page uses one model-policy snapshot")
	require.Equal(t, policy.models, result.Models, "preserve administrator model ordering")
	require.Len(t, result.Items, 5)
	for _, missing := range []string{"31", "32", "34", "999", "1", "2"} {
		require.NotContains(t, result.Items, missing, "missing/broken owners must not look disabled; parent-only loads are not requested rows")
	}
	require.True(t, result.Items["11"].Inherited)
	require.Equal(t, "shared", result.Items["11"].CacheScope)
	require.EqualValues(t, 1, result.Items["11"].OwnerAccountID)
	require.Equal(t, "ready", result.Items["11"].Models[0].State)
	require.EqualValues(t, CodexTurnStateLifetime/time.Second, result.Items["11"].Models[0].RemainingSeconds)
	require.Equal(t, "expired", result.Items["11"].Models[1].State)
	require.Equal(t, "team_business", result.Items["21"].ResolvedAccountType)
	require.Equal(t, "paused", result.Items["21"].Models[0].State)
	require.Equal(t, "model_excluded", result.Items["21"].Models[1].State)
	require.False(t, result.Items["3"].Enabled)
	require.False(t, result.Items["4"].Enabled)
	for _, id := range []int64{11, 12, 21, 3, 4} {
		single, readErr := service.GetStatus(context.Background(), id)
		require.NoError(t, readErr)
		require.Equal(t, single, result.Items[fmt.Sprint(id)], "single-account details and the table must use the same projection")
	}
	require.NotSame(t, result.Items["11"], result.Items["12"])
	for _, os := range []string{"windows", "linux", "macos"} {
		selected, readErr := service.GetStatusForOS(context.Background(), 11, os)
		require.NoError(t, readErr)
		require.Equal(t, result.Items["11"].Models, selected.Models, "model cache projection is shared across OS selectors")
	}
	encoded, err := json.Marshal(result)
	require.NoError(t, err)
	for _, secret := range []string{"private-", "obsolete-model", "encrypted_token", "generation", "access_token"} {
		require.NotContains(t, string(encoded), secret)
	}
	require.Empty(t, service.business)
	require.Empty(t, service.queue)
}

func TestCodexTurnStateBatchStatusReusesRequestedParentAndAllowsEmptyPolicy(t *testing.T) {
	accounts := &codexStateBatchAccounts{accounts: map[int64]*Account{1: codexStateBatchOwner(1, true), 11: codexStateBatchShadow(11, 1)}}
	records := &codexStateBatchRecords{}
	policy := &codexStateBatchPolicy{models: []string{}}
	service := NewCodexTurnStateService(records, accounts, nil, nil)
	service.modelPolicy = policy
	result, err := service.GetStatuses(context.Background(), []int64{1, 11})
	require.NoError(t, err)
	require.Equal(t, [][]int64{{1, 11}}, accounts.batchCalls)
	require.Equal(t, [][]int64{{1}}, records.batchCalls)
	require.Len(t, result.Items, 2)
	require.Equal(t, []string{}, result.Models)
	require.Equal(t, []CodexTurnStateModelStatus{}, result.Items["1"].Models)
}

func TestCodexTurnStateBatchStatusRejectsInvalidInputAndStorageFailures(t *testing.T) {
	for _, failure := range []string{"invalid_id", "too_many_ids", "policy", "accounts", "parents", "records", "missing_repository"} {
		t.Run(failure, func(t *testing.T) {
			accounts := &codexStateBatchAccounts{accounts: map[int64]*Account{1: codexStateBatchOwner(1, true), 11: codexStateBatchShadow(11, 1)}}
			records := &codexStateBatchRecords{}
			policy := &codexStateBatchPolicy{models: []string{"gpt-6-astra"}}
			service := NewCodexTurnStateService(records, accounts, nil, nil)
			service.modelPolicy = policy
			ids := []int64{11}
			status := http.StatusServiceUnavailable
			switch failure {
			case "invalid_id":
				ids, status = []int64{0}, http.StatusBadRequest
			case "too_many_ids":
				ids, status = make([]int64, 201), http.StatusBadRequest
				for i := range ids {
					ids[i] = int64(i + 1)
				}
			case "policy":
				policy.err = errors.New("private-policy-database-error")
			case "accounts":
				accounts.failCall = 1
			case "parents":
				accounts.failCall = 2
			case "records":
				records.err = errors.New("private-state-database-error")
			case "missing_repository":
				service.repo = nil
			}
			result, err := service.GetStatuses(context.Background(), ids)
			require.Nil(t, result)
			require.Equal(t, status, infraerrors.Code(err))
			require.Empty(t, service.queue)
			require.Empty(t, service.business)
			if status == http.StatusBadRequest {
				require.Empty(t, accounts.batchCalls)
				require.Empty(t, records.batchCalls)
				require.Zero(t, policy.calls)
			}
		})
	}
}

func TestCodexTurnStateBatchStatusEmptyAndMissingAccountsDoNotQueryRuntime(t *testing.T) {
	for _, ids := range [][]int64{nil, {99, 99}} {
		accounts := &codexStateBatchAccounts{accounts: map[int64]*Account{}}
		records := &codexStateBatchRecords{}
		policy := &codexStateBatchPolicy{models: []string{"gpt-6-astra"}}
		service := NewCodexTurnStateService(records, accounts, nil, nil)
		service.modelPolicy = policy
		result, err := service.GetStatuses(context.Background(), ids)
		require.NoError(t, err)
		require.Empty(t, result.Items)
		require.Equal(t, []string{"gpt-6-astra"}, result.Models)
		require.Empty(t, records.batchCalls)
		require.Equal(t, 1, policy.calls)
	}
}
