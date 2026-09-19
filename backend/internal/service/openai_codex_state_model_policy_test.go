package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Every test fixture declares its real wire models. A nil policy deliberately
// keeps the production fail-closed behavior instead of authorizing all models.
type codexStateTestModelPolicy struct {
	mu                    sync.Mutex
	models                []string
	revision              int
	err                   error
	authoritativeOverride bool
	authoritativeModels   []string
	authoritativeRevision string
	authoritativeErr      error
}

func newCodexStateTestModelPolicy(models ...string) *codexStateTestModelPolicy {
	return &codexStateTestModelPolicy{models: slices.Clone(models), revision: 1}
}

func (p *codexStateTestModelPolicy) CodexTurnStateModelPolicy(context.Context) ([]string, string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.models), fmt.Sprintf("test-policy-%d", p.revision), p.err
}

func (p *codexStateTestModelPolicy) CodexTurnStateModelPolicyAuthoritative(context.Context) ([]string, string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.authoritativeOverride {
		return slices.Clone(p.authoritativeModels), p.authoritativeRevision, p.authoritativeErr
	}
	return slices.Clone(p.models), fmt.Sprintf("test-policy-%d", p.revision), p.err
}

func (p *codexStateTestModelPolicy) setAuthoritative(models []string, revision string, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.authoritativeOverride = true
	p.authoritativeModels, p.authoritativeRevision, p.authoritativeErr = slices.Clone(models), revision, err
}

func (p *codexStateTestModelPolicy) set(models ...string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.models = slices.Clone(models)
	p.revision++
}

func (p *codexStateTestModelPolicy) fail(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.err = err
}

func TestCodexTurnStateModelPolicyExactFinalModelAdmission(t *testing.T) {
	for _, model := range []string{"gpt-5", "gpt-5-mini", "GPT-5", "gpt-5-codex", "vendor/gpt-5"} {
		t.Run(model, func(t *testing.T) {
			SetFingerprintObservationEnabled(false)
			s, repo, account := newCodexStateTestService(t)
			s.modelPolicy = newCodexStateTestModelPolicy("gpt-5")
			attempt, err := s.Prepare(context.Background(), account, model)
			require.NoError(t, err)
			if model == "gpt-5" {
				require.NotNil(t, attempt)
				require.True(t, attempt.Enabled)
				require.True(t, s.ValidateAttempt(context.Background(), attempt))
				require.NoError(t, s.Finish(context.Background(), attempt, false))
			} else {
				require.NotNil(t, attempt)
				require.False(t, attempt.Enabled, "only the exact final model is eligible for maintenance")
				require.Empty(t, attempt.Snapshot.Token)
				require.NoError(t, s.Finish(context.Background(), attempt, true))
				require.Empty(t, repo.records)
				require.Empty(t, s.queue)
			}
		})
	}
}

func TestCodexTurnStateModelPolicyUnavailableAndEmptyFailClosed(t *testing.T) {
	for _, mode := range []string{"missing", "empty", "error"} {
		t.Run(mode, func(t *testing.T) {
			SetFingerprintObservationEnabled(false)
			s, repo, account := newCodexStateTestService(t)
			policy := newCodexStateTestModelPolicy()
			s.modelPolicy = policy
			if mode == "missing" {
				s.modelPolicy = nil
			} else if mode == "error" {
				policy.fail(errors.New("settings unavailable"))
			}
			attempt, _ := s.Prepare(context.Background(), account, "gpt-5")
			require.NotNil(t, attempt)
			require.False(t, attempt.Enabled)
			require.Empty(t, attempt.Snapshot.Token)
			require.NoError(t, s.Finish(context.Background(), attempt, true))
			require.Empty(t, repo.records)
			require.Empty(t, repo.leases)
			require.Empty(t, s.business)
			require.Empty(t, s.queue)
		})
	}
}

func TestCodexTurnStateExcludedModelStillObservesWithoutMaintenance(t *testing.T) {
	enableCodexStatePassiveObservation(t)
	s, repo, account := newCodexStateTestService(t)
	s.modelPolicy = newCodexStateTestModelPolicy("gpt-5-mini")
	var calls atomic.Int64
	s.collector = codexStateTestCollector(func(context.Context, CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error) {
		calls.Add(1)
		return CodexTurnStateCollectResult{}, nil
	})
	attempt, err := s.Prepare(context.Background(), account, "gpt-5")
	require.NoError(t, err)
	require.NotNil(t, attempt)
	require.False(t, attempt.Enabled)
	require.True(t, attempt.AccountEnabled)
	require.Empty(t, attempt.Snapshot.Token)
	require.Empty(t, attempt.id)
	s.ObserveHeaders(attempt, http.Header{"X-Codex-Turn-State": {codexStateTestToken(10, s.now())}})
	require.Equal(t, 292, attempt.SafeObservation().TokenLength)
	require.Equal(t, "target", attempt.SafeObservation().Shape)
	require.Empty(t, attempt.candidates)
	require.NoError(t, s.Finish(context.Background(), attempt, true))
	s.collect(context.Background(), CodexTurnStateKey{OwnerAccountID: account.ID, Model: "gpt-5", Generation: "gen1"})
	require.Zero(t, calls.Load())
	require.Empty(t, repo.records)
	require.Empty(t, repo.leases)
	require.Empty(t, s.business)
	require.Empty(t, s.queue)
}

func TestCodexTurnStateModelRemovalPreservesCacheWithoutExtendingLifetime(t *testing.T) {
	s, repo, account := newCodexStateTestService(t)
	policy := newCodexStateTestModelPolicy("gpt-5")
	s.modelPolicy = policy
	ctx := context.Background()
	seed, err := s.Prepare(ctx, account, "gpt-5")
	require.NoError(t, err)
	token := codexStateTestToken(10, s.now())
	s.Observe(seed, token)
	require.NoError(t, s.Finish(ctx, seed, true))
	before, err := repo.Get(ctx, seed.key)
	require.NoError(t, err)
	require.NotEmpty(t, before.EncryptedToken)
	oldAttempt, err := s.Prepare(ctx, account, "gpt-5")
	require.NoError(t, err)
	policy.set()
	s.CancelExcludedModels(ctx)
	require.False(t, s.ValidateAttempt(ctx, oldAttempt))
	status, err := s.GetStatus(ctx, account.ID)
	require.NoError(t, err)
	require.Len(t, status.Models, 1)
	require.Equal(t, "model_excluded", status.Models[0].State)
	require.False(t, status.Models[0].ModelAllowed)
	after, err := repo.Get(ctx, seed.key)
	require.NoError(t, err)
	require.Equal(t, before.EncryptedToken, after.EncryptedToken)
	require.Equal(t, before.ExpiresAt, after.ExpiresAt)
	require.Equal(t, before.Version, after.Version)
	policy.set("gpt-5")
	require.False(t, s.ValidateAttempt(ctx, oldAttempt), "removal and re-addition cannot revive a frozen old attempt")
	require.NoError(t, s.Finish(ctx, oldAttempt, false))
	initialTime := s.now()
	s.now = func() time.Time { return initialTime.Add(10 * time.Minute) }
	resumed, err := s.Prepare(ctx, account, "gpt-5")
	require.NoError(t, err)
	require.Equal(t, token, resumed.Snapshot.Token)
	require.Equal(t, before.ExpiresAt, resumed.Snapshot.ExpiresAt)
	require.NoError(t, s.Finish(ctx, resumed, false))
	s.now = func() time.Time { return initialTime.Add(CodexTurnStateLifetime) }
	expired, err := s.Prepare(ctx, account, "gpt-5")
	require.NoError(t, err)
	require.Empty(t, expired.Snapshot.Token, "re-adding a model never renews the original signed timestamp")
	require.NoError(t, s.Finish(ctx, expired, false))
}

func TestCodexTurnStateModelPolicyErrorDisablesExistingSnapshotAndStatus(t *testing.T) {
	s, _, account := newCodexStateTestService(t)
	policy := newCodexStateTestModelPolicy("gpt-5")
	s.modelPolicy = policy
	ctx := context.Background()
	attempt, err := s.Prepare(ctx, account, "gpt-5")
	require.NoError(t, err)
	policy.fail(errors.New("settings secret detail"))
	require.False(t, s.ValidateAttempt(ctx, attempt))
	status, err := s.GetStatus(ctx, account.ID)
	require.NoError(t, err)
	require.Len(t, status.Models, 1)
	require.Equal(t, "model_policy_unavailable", status.Models[0].State)
	require.False(t, status.Models[0].ModelAllowed)
	require.NotContains(t, status.Models[0].LastError, "settings secret detail")
	require.NoError(t, s.Finish(ctx, attempt, false))
}

func TestCodexTurnStateModelRemovalRejectsBusinessPublication(t *testing.T) {
	for _, readd := range []bool{false, true} {
		t.Run(fmt.Sprintf("readd=%t", readd), func(t *testing.T) {
			s, repo, account := newCodexStateTestService(t)
			policy := newCodexStateTestModelPolicy("gpt-5")
			s.modelPolicy = policy
			ctx := context.Background()
			attempt, err := s.Prepare(ctx, account, "gpt-5")
			require.NoError(t, err)
			s.Observe(attempt, codexStateTestToken(10, s.now()))
			policy.set()
			if readd {
				policy.set("gpt-5")
			}
			require.NoError(t, s.Finish(ctx, attempt, true))
			record, err := repo.Get(ctx, attempt.key)
			require.NoError(t, err)
			require.Empty(t, record.EncryptedToken, "delivered response cannot publish under a superseded policy revision")
			active, err := repo.HasBusiness(ctx, attempt.key, s.now())
			require.NoError(t, err)
			require.False(t, active, "publication rejection still releases the business lease")
		})
	}
}

type codexStatePolicyChangingEncryptor struct {
	codexStateTestEncryptor
	policy *codexStateTestModelPolicy
}

func (e codexStatePolicyChangingEncryptor) Encrypt(value string) (string, error) {
	e.policy.set()
	return e.codexStateTestEncryptor.Encrypt(value)
}

func TestCodexTurnStateModelPolicyRecheckedBeforeFinalSave(t *testing.T) {
	s, repo, account := newCodexStateTestService(t)
	policy := newCodexStateTestModelPolicy("gpt-5")
	s.modelPolicy = policy
	s.encryptor = codexStatePolicyChangingEncryptor{policy: policy}
	ctx := context.Background()
	attempt, err := s.Prepare(ctx, account, "gpt-5")
	require.NoError(t, err)
	s.Observe(attempt, codexStateTestToken(10, s.now()))
	require.NoError(t, s.Finish(ctx, attempt, true))
	record, err := repo.Get(ctx, attempt.key)
	require.NoError(t, err)
	require.Empty(t, record.EncryptedToken, "policy removal after initial publication admission must prevent final CAS")
	require.EqualValues(t, 1, record.Version)
}

func TestCodexTurnStateAuthoritativePolicyOverridesStaleLocalAdmission(t *testing.T) {
	for _, mode := range []string{"excluded", "revised", "unavailable"} {
		t.Run(mode, func(t *testing.T) {
			s, repo, account := newCodexStateTestService(t)
			policy := newCodexStateTestModelPolicy("gpt-5")
			s.modelPolicy = policy
			ctx := context.Background()
			attempt, err := s.Prepare(ctx, account, "gpt-5")
			require.NoError(t, err)
			require.NotNil(t, attempt)
			if mode == "excluded" {
				policy.setAuthoritative(nil, "server-policy-2", nil)
			} else if mode == "revised" {
				policy.setAuthoritative([]string{"gpt-5"}, "server-policy-3", nil)
			} else {
				policy.setAuthoritative(nil, "", errors.New("database offline"))
			}
			require.False(t, s.ValidateAttempt(ctx, attempt), "final send must consult live policy, not stale local admission")
			s.Observe(attempt, codexStateTestToken(10, s.now()))
			require.NoError(t, s.Finish(ctx, attempt, true))
			record, err := repo.Get(ctx, attempt.key)
			require.NoError(t, err)
			require.Empty(t, record.EncryptedToken)
			var calls atomic.Int64
			s.collector = codexStateTestCollector(func(context.Context, CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error) {
				calls.Add(1)
				return CodexTurnStateCollectResult{}, nil
			})
			s.collect(ctx, attempt.key)
			require.Zero(t, calls.Load(), "collector must verify live policy immediately before sending")
		})
	}
}

func TestCodexTurnStateExcludedModelCancelsQueuedAndRejectsCollection(t *testing.T) {
	s, repo, account := newCodexStateTestService(t)
	policy := newCodexStateTestModelPolicy("gpt-5")
	s.modelPolicy = policy
	ctx := context.Background()
	attempt, err := s.Prepare(ctx, account, "gpt-5")
	require.NoError(t, err)
	require.NoError(t, s.Finish(ctx, attempt, false))
	var calls atomic.Int64
	s.collector = codexStateTestCollector(func(context.Context, CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error) {
		calls.Add(1)
		return CodexTurnStateCollectResult{}, nil
	})
	// Freeze worker startup so a queued job can be excluded deterministically.
	s.ctx = ctx
	s.enqueue(ctx, attempt.key)
	require.True(t, s.queued[attempt.key])
	require.Len(t, s.queue, 1)
	policy.set()
	s.CancelExcludedModels(ctx)
	require.NotContains(t, s.queued, attempt.key)
	s.enqueue(ctx, attempt.key)
	require.NotContains(t, s.queued, attempt.key)
	key := <-s.queue
	s.collect(ctx, key)
	require.Zero(t, calls.Load(), "a stale queue item must recheck model admission before acquiring the collector")
	require.Empty(t, repo.locks)
}

func TestCodexTurnStateExcludedSlowCollectorCannotPublishAfterReaddition(t *testing.T) {
	s, repo, account := newCodexStateTestService(t)
	policy := newCodexStateTestModelPolicy("gpt-5")
	s.modelPolicy = policy
	ctx := context.Background()
	attempt := seedCodexStateTestDemand(t, s, account, "gpt-5")
	started, cancelled, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() {
		s.cancelCollection(attempt.key)
		releaseOnce.Do(func() { close(release) })
	})
	s.collector = codexStateTestCollector(func(ctx context.Context, _ CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error) {
		close(started)
		<-ctx.Done()
		close(cancelled)
		<-release
		return CodexTurnStateCollectResult{StatusCode: http.StatusOK, Tokens: []string{codexStateTestToken(10, s.now())}}, nil
	})
	go func() { defer close(done); s.collect(ctx, attempt.key) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("collector did not start")
	}
	policy.set()
	s.CancelExcludedModels(ctx)
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("excluded in-flight collector was not cancelled")
	}
	policy.set("gpt-5")
	releaseOnce.Do(func() { close(release) })
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("collector did not stop")
	}
	record, err := repo.Get(ctx, attempt.key)
	require.NoError(t, err)
	require.Empty(t, record.EncryptedToken, "late results from an older policy revision must not publish")
}
