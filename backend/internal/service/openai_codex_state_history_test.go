package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type codexHistoryTestRepository struct {
	*codexStateMemoryRepo
	proofs []CodexTurnStateHistoryProof
}

func (r *codexHistoryTestRepository) CreateHistoryDemand(_ context.Context, p CodexTurnStateHistoryProof, _ time.Time) (bool, error) {
	for _, existing := range r.proofs {
		if existing.OwnerAccountID == p.OwnerAccountID && existing.OSFamily == p.OSFamily && existing.Model == p.Model && existing.Generation == p.Generation && !p.ObservedAt.After(existing.ObservedAt) {
			return false, nil
		}
	}
	r.proofs = append(r.proofs, p)
	return true, nil
}

type codexHistoryEndCallbackRepository struct {
	*codexHistoryTestRepository
	beforeEnd func()
}

func (r *codexHistoryEndCallbackRepository) EndBusiness(ctx context.Context, key CodexTurnStateKey, id string) error {
	if r.beforeEnd != nil {
		r.beforeEnd()
	}
	return r.codexHistoryTestRepository.EndBusiness(ctx, key, id)
}

func isolateCodexHistory(t *testing.T) {
	t.Helper()
	codexStateHistory.Lock()
	previous := codexStateHistory.proofs
	codexStateHistory.proofs = make(map[codexStateHistoryKey]CodexTurnStateHistoryProof)
	codexStateHistory.Unlock()
	t.Cleanup(func() { codexStateHistory.Lock(); codexStateHistory.proofs = previous; codexStateHistory.Unlock() })
}

func TestCodexTurnStateHistoryNeedsActualDeliveryAndCurrentCredentials(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		sent, delivered, matching bool
	}{
		{"delivered", true, true, true}, {"unsent", false, true, true},
		{"failed_delivery", true, false, true}, {"old_credentials", true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateCodexHistory(t)
			s, _, account := newCodexStateTestService(t)
			account.Extra[CodexTurnStateExtraKey].(map[string]any)["enabled"] = false
			account.Extra[CodexTurnStateCredentialEpochExtraKey] = "private-epoch"
			a, err := s.Prepare(context.Background(), account, "gpt-5")
			require.NoError(t, err)
			h := http.Header{"Authorization": {"Bearer test-token"}}
			if !tc.matching {
				h.Set("Authorization", "Bearer old-token")
			}
			s.bindHistoryCredentials(context.Background(), a, h)
			if tc.sent {
				markCodexStateTestBusinessSent(t, s, a)
			}
			s.Observe(a, codexStateTestToken(11, s.now()))
			require.NoError(t, s.Finish(context.Background(), a, tc.delivered))
			proofs := codexStateHistorySnapshot(account.ID, s.now().Add(-CodexTurnStateActiveWindow))
			if tc.sent && tc.delivered && tc.matching {
				require.Len(t, proofs, 1)
				require.True(t, proofs[0].EnvelopeValid)
				require.Equal(t, 312, proofs[0].TokenLength)
				require.Equal(t, s.now(), proofs[0].BusinessAt)
			} else {
				require.Empty(t, proofs)
			}
		})
	}
}

func TestCodexTurnStateHistoryActivationOnlyValidRecentMatchingAbnormal(t *testing.T) {
	for _, name := range []string{"extended", "normal", "expired", "future", "stale_business", "old_epoch", "undelivered", "malformed", "wrong_plan", "excluded", "stale_generation"} {
		t.Run(name, func(t *testing.T) {
			isolateCodexHistory(t)
			s, memory, account := newCodexStateTestService(t)
			account.Extra[CodexTurnStateCredentialEpochExtraKey] = "epoch"
			repo := &codexHistoryTestRepository{codexStateMemoryRepo: memory}
			s.repo = repo
			now := s.now()
			p := CodexTurnStateHistoryProof{OSFamily: "windows", OwnerAccountID: account.ID, Model: "gpt-5", CredentialEpoch: "epoch", BusinessAt: now,
				ObservedAt: now, IssuedAt: now, ExpiresAt: now.Add(CodexTurnStateLifetime), TokenLength: 312, CipherBlocks: 11, EnvelopeValid: true, Delivered: true}
			gen := CodexTurnStateGenerationForAccount(account)
			switch name {
			case "normal":
				p.TokenLength, p.CipherBlocks = 292, 10
			case "expired":
				p.ExpiresAt = now
			case "future":
				p.IssuedAt = now.Add(time.Minute)
			case "stale_business":
				p.BusinessAt = now.Add(-31 * time.Minute)
			case "old_epoch":
				p.CredentialEpoch = "old"
			case "undelivered":
				p.Delivered = false
			case "malformed":
				p.EnvelopeValid = false
			case "wrong_plan":
				p.TokenLength, p.CipherBlocks = 356, 13
			case "excluded":
				p.Model = "excluded"
			case "stale_generation":
				gen = "old"
			}
			codexStateHistory.Lock()
			codexStateHistory.proofs[codexStateHistoryKey{ownerAccountID: account.ID, model: p.Model, credentialEpoch: p.CredentialEpoch}] = p
			codexStateHistory.Unlock()
			s.activateHistoryForOwner(context.Background(), account, gen)
			if name == "extended" {
				require.Len(t, repo.proofs, 1)
				require.Equal(t, "personal", repo.proofs[0].AccountType)
				require.Equal(t, gen, repo.proofs[0].Generation)
				require.Equal(t, now, repo.proofs[0].BusinessAt)
			} else {
				require.Empty(t, repo.proofs)
			}
		})
	}
}

func TestCodexTurnStateDefaultModelsExcludeTerraButKeepExplicitSelection(t *testing.T) {
	require.Equal(t, []string{"gpt-6-astra", "gpt-5.6-sol"}, DefaultCodexTurnStateModels())
	models, _, err := ParseCodexTurnStateModelPolicyValues(map[string]string{SettingKeyCodexTurnStateModels: `["gpt-5.6-terra"]`})
	require.NoError(t, err)
	require.Equal(t, []string{"gpt-5.6-terra"}, models)
}

func TestCodexTurnStateHistoryOldEpochCannotEraseCurrentProof(t *testing.T) {
	isolateCodexHistory(t)
	s, memory, account := newCodexStateTestService(t)
	repo := &codexHistoryTestRepository{codexStateMemoryRepo: memory}
	s.repo = repo
	account.Extra[CodexTurnStateExtraKey].(map[string]any)["enabled"] = false
	account.Extra[CodexTurnStateCredentialEpochExtraKey] = "old"
	old, err := s.Prepare(context.Background(), account, "gpt-5")
	require.NoError(t, err)
	markCodexStateTestBusinessSent(t, s, old)
	account.Extra[CodexTurnStateCredentialEpochExtraKey] = "current"
	current, err := s.Prepare(context.Background(), account, "gpt-5")
	require.NoError(t, err)
	markCodexStateTestBusinessSent(t, s, current)
	s.Observe(current, codexStateTestToken(11, s.now()))
	require.NoError(t, s.Finish(context.Background(), current, true))
	s.Observe(old, codexStateTestToken(11, s.now()))
	require.NoError(t, s.Finish(context.Background(), old, true))
	account.Extra[CodexTurnStateExtraKey].(map[string]any)["enabled"] = true
	s.activateHistoryForOwner(context.Background(), account, CodexTurnStateGenerationForAccount(account))
	require.Len(t, repo.proofs, 1)
	require.Equal(t, "current", repo.proofs[0].CredentialEpoch)
}

func TestCodexTurnStateHistoryMixedCarriersPreferAnyTarget(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(map[bool]string{false: "target_header", true: "target_metadata"}[reverse], func(t *testing.T) {
			isolateCodexHistory(t)
			s, memory, account := newCodexStateTestService(t)
			repo := &codexHistoryTestRepository{codexStateMemoryRepo: memory}
			s.repo = repo
			account.Extra[CodexTurnStateExtraKey].(map[string]any)["enabled"] = false
			account.Extra[CodexTurnStateCredentialEpochExtraKey] = "epoch"
			a, err := s.Prepare(context.Background(), account, "gpt-5")
			require.NoError(t, err)
			markCodexStateTestBusinessSent(t, s, a)
			first, second := 10, 11
			if reverse {
				first, second = second, first
			}
			s.observe(a, codexStateTestToken(first, s.now()), "header")
			s.observe(a, codexStateTestToken(second, s.now()), "metadata")
			require.NoError(t, s.Finish(context.Background(), a, true))
			account.Extra[CodexTurnStateExtraKey].(map[string]any)["enabled"] = true
			s.activateHistoryForOwner(context.Background(), account, CodexTurnStateGenerationForAccount(account))
			require.Empty(t, repo.proofs, "one valid target in the delivered response avoids historical collection")
		})
	}
}

func TestCodexTurnStateLateWSWriteBindingCompletesBusinessActivity(t *testing.T) {
	isolateCodexHistory(t)
	s, memory, account := newCodexStateTestService(t)
	now := time.Now()
	s.now = func() time.Time { return now }
	repo := &codexHistoryTestRepository{codexStateMemoryRepo: memory}
	s.repo = repo
	account.Extra[CodexTurnStateCredentialEpochExtraKey] = "epoch"
	a, err := s.Prepare(context.Background(), account, "gpt-5")
	require.NoError(t, err)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	noteOpenAICodexStatePatch(c, a, nil, nil)
	wire := populateCodexTurnStateObservation(c, nil, nil, []byte(`{"type":"response.create","model":"gpt-5"}`), true)
	s.Observe(a, codexStateTestToken(11, now))
	require.NoError(t, s.Finish(context.Background(), a, true))
	before, err := repo.Get(context.Background(), a.key)
	require.NoError(t, err)
	require.Equal(t, time.Unix(0, 0), before.LastBusinessAt)
	bindCodexTurnStateSummarySequence(wire)
	after, err := repo.Get(context.Background(), a.key)
	require.NoError(t, err)
	require.False(t, after.LastBusinessAt.After(a.SafeObservation().ObservedAt), "the activity time comes from before Write, not its late callback")
	require.Equal(t, wire.sendStartedAt, after.LastBusinessAt)
	require.Len(t, repo.proofs, 1, "late binding activates the delivered anomaly immediately")
}

func TestCodexTurnStateWSWriteBindingDuringFinishActivatesWithBusinessLease(t *testing.T) {
	isolateCodexHistory(t)
	s, memory, account := newCodexStateTestService(t)
	now := time.Now()
	s.now = func() time.Time { return now }
	repo := &codexHistoryEndCallbackRepository{codexHistoryTestRepository: &codexHistoryTestRepository{codexStateMemoryRepo: memory}}
	s.repo = repo
	account.Extra[CodexTurnStateCredentialEpochExtraKey] = "epoch"
	a, err := s.Prepare(context.Background(), account, "gpt-5")
	require.NoError(t, err)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	noteOpenAICodexStatePatch(c, a, nil, nil)
	wire := populateCodexTurnStateObservation(c, nil, nil, []byte(`{"type":"response.create","model":"gpt-5"}`), true)
	repo.beforeEnd = func() {
		bindCodexTurnStateSummarySequence(wire)
		require.Len(t, repo.proofs, 1, "delivered history may create demand while the business lease is still present")
	}
	s.Observe(a, codexStateTestToken(11, now))
	require.NoError(t, s.Finish(context.Background(), a, true))
	require.Len(t, repo.proofs, 1, "releasing the lease must not consume delivered history twice")
}
