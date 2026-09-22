package service

import (
	"context"
	"errors"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type codexStateLateSendRepository struct {
	CodexTurnStateRepository
	beforeEnd func()
	markErr   error
}

func (r *codexStateLateSendRepository) MarkBusinessSent(ctx context.Context, key CodexTurnStateKey, sentAt time.Time) error {
	if r.markErr != nil {
		return r.markErr
	}
	return r.CodexTurnStateRepository.MarkBusinessSent(ctx, key, sentAt)
}

func (r *codexStateLateSendRepository) EndBusiness(ctx context.Context, key CodexTurnStateKey, id string) error {
	if callback := r.beforeEnd; callback != nil {
		r.beforeEnd = nil
		callback()
	}
	return r.CodexTurnStateRepository.EndBusiness(ctx, key, id)
}

func prepareCodexStateLateSendTest(t *testing.T, accountType string) (*CodexTurnStateService, *codexStateMemoryRepo, *Account, *CodexTurnStateAttempt, *codexTurnStateWireObservation) {
	t.Helper()
	isolateCodexHistory(t)
	isolateCodexTurnStateSummaryStore(t)
	s, repo, account := newCodexStateTestService(t)
	now := time.Now().UTC()
	s.now = func() time.Time { return now }
	account.Extra[CodexTurnStateExtraKey].(map[string]any)["account_type"] = accountType
	blocks := 10
	if accountType == "team_business" {
		blocks = 12
	}
	seed, err := s.Prepare(context.Background(), account, "gpt-5")
	require.NoError(t, err)
	markCodexStateTestBusinessSent(t, s, seed)
	s.Observe(seed, codexStateTestToken(blocks, now.Add(-2*time.Minute)))
	require.NoError(t, s.Finish(context.Background(), seed, true))
	a, err := s.Prepare(context.Background(), account, "gpt-5")
	require.NoError(t, err)
	require.NotEmpty(t, a.Snapshot.Token)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	noteOpenAICodexStatePatch(c, a, nil, nil)
	wire := populateCodexTurnStateObservation(c, nil, nil, []byte(`{"type":"response.create","model":"gpt-5"}`), true)
	s.ctx = context.Background()
	s.collector = codexStateTestCollector(func(context.Context, CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error) {
		t.Fatal("this test only queues recovery; no collector should run")
		return CodexTurnStateCollectResult{}, nil
	})
	return s, repo, account, a, wire
}

func TestCodexTurnStateLateWSSendInvalidatesPreparedCache(t *testing.T) {
	for _, accountType := range []string{"personal", "team_business"} {
		for _, binding := range []string{"after_finish", "during_finish"} {
			t.Run(accountType+"/"+binding, func(t *testing.T) {
				s, repo, _, a, wire := prepareCodexStateLateSendTest(t, accountType)
				ctx := context.Background()
				before, err := repo.Get(ctx, a.key)
				require.NoError(t, err)
				before.NextCollectAt = s.now().Add(CodexTurnStateRetryInterval)
				before.LastCollectedAt = s.now()
				before.LastError = "no_target_state"
				ok, err := repo.SaveCAS(ctx, *before, before.Version)
				require.NoError(t, err)
				require.True(t, ok)
				blocks := 11
				if accountType == "team_business" {
					blocks = 13
				}
				s.Observe(a, codexStateTestToken(blocks, s.now()))
				if binding == "during_finish" {
					s.repo = &codexStateLateSendRepository{CodexTurnStateRepository: repo, beforeEnd: func() { bindCodexTurnStateSummarySequence(wire) }}
				}
				require.NoError(t, s.Finish(ctx, a, true))
				if binding == "after_finish" {
					unsent, err := repo.Get(ctx, a.key)
					require.NoError(t, err)
					require.NotEmpty(t, unsent.EncryptedToken, "delivery alone is not proof of a successful physical send")
					require.Equal(t, "refresh", unsent.DemandReason)
					bindCodexTurnStateSummarySequence(wire)
				}
				after, err := repo.Get(ctx, a.key)
				require.NoError(t, err)
				require.Empty(t, after.EncryptedToken, "late successful-send binding must revoke the cache used by the delivered anomaly")
				require.True(t, after.ExpiresAt.IsZero())
				require.Equal(t, "extended_shape", after.DemandReason)
				require.Equal(t, before.NextCollectAt, after.NextCollectAt, "late publication must preserve the existing failed-attempt retry fence")
				require.True(t, s.queued[a.key])
				bindCodexTurnStateSummarySequence(wire)
				repeated, err := repo.Get(ctx, a.key)
				require.NoError(t, err)
				require.Equal(t, after.Version, repeated.Version, "repeated binding must not publish twice")
			})
		}
	}
}

func TestCodexTurnStateLateWSSendPreservesConcurrentTarget(t *testing.T) {
	s, repo, account, a, wire := prepareCodexStateLateSendTest(t, "personal")
	ctx := context.Background()
	s.Observe(a, codexStateTestToken(11, s.now()))
	require.NoError(t, s.Finish(ctx, a, true))
	newer, err := s.Prepare(ctx, account, a.Model)
	require.NoError(t, err)
	markCodexStateTestBusinessSent(t, s, newer)
	token := codexStateTestToken(10, s.now().Add(-time.Minute))
	s.Observe(newer, token)
	require.NoError(t, s.Finish(ctx, newer, true))
	beforeBinding, err := repo.Get(ctx, a.key)
	require.NoError(t, err)
	bindCodexTurnStateSummarySequence(wire)
	after, err := repo.Get(ctx, a.key)
	require.NoError(t, err)
	require.Equal(t, beforeBinding.Version, after.Version)
	require.Equal(t, beforeBinding.EncryptedToken, after.EncryptedToken)
	require.Equal(t, "refresh", after.DemandReason)
	require.False(t, s.queued[a.key])
}

func TestCodexTurnStateLateWSSendRechecksPublicationAdmission(t *testing.T) {
	for _, name := range []string{"unsent", "undelivered", "disabled", "generation_changed", "model_policy_changed", "unknown_type", "wrong_type", "expired", "future", "target_priority", "invalid_candidate"} {
		t.Run(name, func(t *testing.T) {
			s, repo, account, a, wire := prepareCodexStateLateSendTest(t, "personal")
			ctx := context.Background()
			before, err := repo.Get(ctx, a.key)
			require.NoError(t, err)
			token := codexStateTestToken(11, s.now())
			if name == "invalid_candidate" {
				token = codexStateTestToken(11, s.now().Add(time.Minute))
			}
			s.Observe(a, token)
			if name == "target_priority" {
				s.Observe(a, a.Snapshot.Token)
				// Target-first admission cannot depend on carrier order.
				s.Observe(a, token)
			}
			require.NoError(t, s.Finish(ctx, a, name != "undelivered"))
			require.Empty(t, a.candidates, "deferred publication must not retain response tokens")
			switch name {
			case "disabled":
				account.Extra[CodexTurnStateExtraKey].(map[string]any)["enabled"] = false
			case "generation_changed":
				account.Extra[CodexTurnStateGenerationExtraKey] = "new-generation"
			case "model_policy_changed":
				s.modelPolicy.(*codexStateTestModelPolicy).set("other-model")
			case "unknown_type":
				account.Extra[CodexTurnStateExtraKey].(map[string]any)["account_type"] = "auto"
				delete(account.Credentials, "plan_type")
			case "wrong_type":
				account.Extra[CodexTurnStateExtraKey].(map[string]any)["account_type"] = "team_business"
			case "expired":
				now := s.now().Add(CodexTurnStateLifetime)
				s.now = func() time.Time { return now }
			case "future":
				now := s.now().Add(-time.Minute)
				s.now = func() time.Time { return now }
			}
			if name == "unsent" {
				s.completeBusinessSent(a)
			} else {
				bindCodexTurnStateSummarySequence(wire)
			}
			after, err := repo.Get(ctx, a.key)
			require.NoError(t, err)
			if name == "expired" || name == "target_priority" {
				require.Equal(t, before.Version+1, after.Version, "expiry scheduling or same-ticket bundle publication is one CAS")
			} else {
				require.Equal(t, before.Version, after.Version)
			}
			require.Equal(t, before.EncryptedToken, after.EncryptedToken)
			if name == "expired" {
				require.Equal(t, "cookie_expired", after.DemandReason)
				require.True(t, s.queued[a.key], "a fresh physical send reactivates an expired bundle")
			} else {
				require.Equal(t, "refresh", after.DemandReason)
				require.False(t, s.queued[a.key])
			}
		})
	}
}

func TestCodexTurnStateLateWSSendRequiresPersistedSend(t *testing.T) {
	s, repo, _, a, wire := prepareCodexStateLateSendTest(t, "personal")
	ctx := context.Background()
	s.Observe(a, codexStateTestToken(11, s.now()))
	require.NoError(t, s.Finish(ctx, a, true))
	wrapped := &codexStateLateSendRepository{CodexTurnStateRepository: repo, markErr: errors.New("synthetic activity-store outage")}
	s.repo = wrapped
	bindCodexTurnStateSummarySequence(wire)
	afterFailure, err := repo.Get(ctx, a.key)
	require.NoError(t, err)
	require.NotEmpty(t, afterFailure.EncryptedToken)
	require.Equal(t, "refresh", afterFailure.DemandReason)
	require.False(t, s.queued[a.key])
	wrapped.markErr = nil
	bindCodexTurnStateSummarySequence(wire)
	afterRecovery, err := repo.Get(ctx, a.key)
	require.NoError(t, err)
	require.Empty(t, afterRecovery.EncryptedToken)
	require.Equal(t, "extended_shape", afterRecovery.DemandReason)
}

func TestCodexTurnStateLateWSSendConcurrentBindingsPublishOnce(t *testing.T) {
	s, repo, _, a, wire := prepareCodexStateLateSendTest(t, "personal")
	ctx := context.Background()
	s.Observe(a, codexStateTestToken(11, s.now()))
	require.NoError(t, s.Finish(ctx, a, true))
	before, err := repo.Get(ctx, a.key)
	require.NoError(t, err)
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() { bindCodexTurnStateSummarySequence(wire) })
	}
	workers.Wait()
	after, err := repo.Get(ctx, a.key)
	require.NoError(t, err)
	require.Equal(t, before.Version+1, after.Version)
	require.Empty(t, after.EncryptedToken)
	require.Equal(t, "extended_shape", after.DemandReason)
}
