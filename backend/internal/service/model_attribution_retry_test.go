package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// This repository persists independent copies so a resumed worker cannot obtain
// successful answers or retry counters from the previous worker's memory.
type attributionRetryRepo struct {
	AttributionRepository
	config       AttributionConfig
	stored       *AttributionJob
	claim        int
	owned        bool
	saves        int
	defers       int
	finishes     int
	loseSave     bool
	loseDefer    bool
	beforeFinish *AttributionCheckpoint
	clock        *attributionRetryClock
}

func copyAttributionRetryJob(job *AttributionJob) *AttributionJob {
	encoded, err := json.Marshal(job)
	if err != nil {
		panic(err)
	}
	var out AttributionJob
	if err := json.Unmarshal(encoded, &out); err != nil {
		panic(err)
	}
	out.Claim = job.Claim
	out.Snapshot.Authorization = job.Snapshot.Authorization
	out.Snapshot.Fence = job.Snapshot.Fence
	if job.Checkpoint != nil {
		encoded, err = json.Marshal(job.Checkpoint)
		if err != nil {
			panic(err)
		}
		if err := json.Unmarshal(encoded, &out.Checkpoint); err != nil {
			panic(err)
		}
	}
	return &out
}

func (r *attributionRetryRepo) Config(context.Context) (AttributionConfig, error) {
	return r.config, nil
}

func (r *attributionRetryRepo) Validate(_ context.Context, j *AttributionJob) (bool, error) {
	return r.owned && r.stored.Claim == j.Claim, nil
}

func (r *attributionRetryRepo) Heartbeat(ctx context.Context, j *AttributionJob) (bool, error) {
	return r.Validate(ctx, j)
}

func (r *attributionRetryRepo) SaveCheckpoint(ctx context.Context, j *AttributionJob) (bool, error) {
	valid, err := r.Validate(ctx, j)
	if !valid || err != nil || r.loseSave {
		r.owned = false
		return false, err
	}
	r.saves++
	status, reason := r.stored.Status, r.stored.Reason
	r.stored = copyAttributionRetryJob(j)
	r.stored.Status, r.stored.Reason = status, reason
	return true, nil
}

func (r *attributionRetryRepo) DeferRetry(ctx context.Context, j *AttributionJob, next time.Time) (bool, error) {
	valid, err := r.Validate(ctx, j)
	if !valid || err != nil || r.loseDefer {
		r.owned = false
		return false, err
	}
	r.defers++
	r.stored = copyAttributionRetryJob(j)
	r.stored.Status = "retry_wait"
	r.stored.NextAttemptAt = &next
	r.owned = false
	return true, nil
}

func (r *attributionRetryRepo) Claim(context.Context) (*AttributionJob, error) {
	if r.owned || r.stored.Status != "retry_wait" || r.stored.NextAttemptAt == nil || r.clock.Now().Before(*r.stored.NextAttemptAt) {
		return nil, nil
	}
	r.claim++
	r.owned = true
	r.stored.Status = "running"
	r.stored.Claim = fmt.Sprintf("claim-%d", r.claim)
	r.stored.NextAttemptAt = nil
	return copyAttributionRetryJob(r.stored), nil
}

func (r *attributionRetryRepo) Finish(_ context.Context, j *AttributionJob) error {
	if !r.owned || r.stored.Claim != j.Claim {
		return nil
	}
	r.finishes++
	r.stored = copyAttributionRetryJob(j)
	r.beforeFinish = r.stored.Checkpoint
	r.stored.Checkpoint = nil
	r.owned = false
	return nil
}

func (r *attributionRetryRepo) Cancel(_ context.Context, id int64) (*AttributionJob, error) {
	if id != r.stored.ID {
		return nil, ErrAttributionNotFound
	}
	if r.stored.Status == "running" || r.stored.Status == "retry_wait" || r.stored.Status == "queued" {
		r.stored.Status = "cancelled"
		r.stored.Reason = "cancelled"
		r.stored.Checkpoint = nil
		r.stored.NextAttemptAt = nil
		r.owned = false
	}
	return copyAttributionRetryJob(r.stored), nil
}

type attributionRetryClock struct {
	mu   sync.Mutex
	time time.Time
}

func (c *attributionRetryClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.time
}

func (c *attributionRetryClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.time = c.time.Add(d)
	c.mu.Unlock()
}

func (c *attributionRetryClock) Wait(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.Advance(d)
	return nil
}

type attributionRetryAnalyzer struct {
	challengeCalls int
	outputs        []AttributionOutput
}

func (*attributionRetryAnalyzer) Info(context.Context, string) (*AttributionDetector, error) {
	return testAttributionDetector(), nil
}

func (*attributionRetryAnalyzer) Models(context.Context, string, *AttributionDetector) ([]string, error) {
	return []string{AttributionDefaultModel, "gpt-6-luna"}, nil
}

func (a *attributionRetryAnalyzer) Challenges(context.Context, string, *AttributionDetector) ([]AttributionChallenge, error) {
	a.challengeCalls++
	return attributionRetryChallenges(), nil
}

func (a *attributionRetryAnalyzer) Analyze(_ context.Context, _ string, _ *AttributionDetector, outputs []AttributionOutput) (*AttributionAnalysis, error) {
	a.outputs = append([]AttributionOutput(nil), outputs...)
	return validAttributionAnalysis(), nil
}

func attributionRetryChallenges() []AttributionChallenge {
	return []AttributionChallenge{{ID: "a", Prompt: "challenge A", ExpectedCount: 300}, {ID: "b", Prompt: "challenge B", ExpectedCount: 301}, {ID: "c", Prompt: "challenge C", ExpectedCount: 302}}
}

func newAttributionRetryHarness(source string) (*ModelAttributionService, *attributionRetryRepo, *attributionRetryAnalyzer, *attributionRetryClock, *AttributionJob) {
	clock := &attributionRetryClock{time: time.Now()}
	started := clock.Now()
	j := &AttributionJob{ID: 123, AccountID: 11, Source: source, Status: "running", Claim: "claim-0", StartedAt: &started,
		Snapshot: AttributionSnapshot{Version: 2, ConfigVersion: 1, Policy: AttributionPolicy{Model: AttributionDefaultModel}, Detector: testAttributionDetector()}}
	repo := &attributionRetryRepo{stored: copyAttributionRetryJob(j), owned: true, clock: clock,
		config: AttributionConfig{Version: 1, BaseURL: "http://detector.invalid", Detector: testAttributionDetector()}}
	analyzer := &attributionRetryAnalyzer{}
	s := &ModelAttributionService{repo: repo, analyzer: analyzer, now: clock.Now, wait: clock.Wait}
	return s, repo, analyzer, clock, j
}

func resumeAttributionRetry(t *testing.T, repo *attributionRetryRepo, clock *attributionRetryClock) *AttributionJob {
	t.Helper()
	require.Equal(t, "retry_wait", repo.stored.Status)
	require.NotNil(t, repo.stored.NextAttemptAt)
	clock.Advance(repo.stored.NextAttemptAt.Sub(clock.Now()))
	j, err := repo.Claim(context.Background())
	require.NoError(t, err)
	require.NotNil(t, j)
	return j
}

func TestAttributionRetry503ResumesFrozenThirdProbePastTenMinutes(t *testing.T) {
	for _, source := range []string{"manual", "scheduled", "initial"} {
		t.Run(source, func(t *testing.T) {
			s, repo, analyzer, clock, j := newAttributionRetryHarness(source)
			var prompts []string
			thirdAttempts := 0
			s.probe = attributionProbeFunc(func(ctx context.Context, _ int64, model, prompt string) (*CandyTestExecution, error) {
				require.True(t, isAttributionExecution(ctx))
				require.Equal(t, source == "manual", isManualAttribution(ctx))
				prompts = append(prompts, prompt)
				execution := &CandyTestExecution{ActualModel: model, UpstreamModel: model, Usage: &OpenAIUsage{InputTokens: 10, OutputTokens: 20}}
				if prompt == "challenge C" {
					thirdAttempts++
					if thirdAttempts <= 4 {
						clock.Advance(3 * time.Minute)
						execution.UpstreamStatus = 503
						return execution, candyTestError("account_rate_limited")
					}
				}
				clock.Advance(time.Second)
				execution.Completed = true
				execution.ResponseText = "private full answer for " + prompt
				return execution, nil
			})
			for attempt := 0; attempt < 4; attempt++ {
				s.execute(context.Background(), j)
				require.Zero(t, repo.finishes, "a deferred round must not be finalized")
				require.Equal(t, 2, repo.stored.Result.CompletedProbes)
				require.Len(t, repo.stored.Checkpoint.Outputs, 2)
				require.EqualValues(t, (10*time.Minute - 2*time.Second).Milliseconds(), repo.stored.Checkpoint.RemainingBudgetMS)
				require.Equal(t, attempt+1, repo.stored.Checkpoint.UnavailableRetries)
				public, err := json.Marshal(repo.stored)
				require.NoError(t, err)
				require.NotContains(t, string(public), "private full answer")
				require.NotContains(t, string(public), "checkpoint")
				j = resumeAttributionRetry(t, repo, clock)
			}
			require.Greater(t, clock.Now().Sub(*j.StartedAt), 10*time.Minute)
			s.execute(context.Background(), j)
			require.Equal(t, "passed", repo.stored.Status)
			require.Empty(t, repo.stored.Reason, "a successful resumed round must clear the waiting reason")
			require.Equal(t, 1, repo.finishes)
			require.Equal(t, 4, repo.defers)
			require.Equal(t, 1, analyzer.challengeCalls)
			require.Equal(t, []string{"challenge A", "challenge B", "challenge C", "challenge C", "challenge C", "challenge C", "challenge C"}, prompts)
			require.Equal(t, 3, repo.stored.Result.CompletedProbes)
			require.Equal(t, 4, repo.stored.Result.Retries)
			require.Len(t, repo.stored.Result.Usage, 3)
			require.EqualValues(t, 10, repo.stored.Result.Usage[0].InputTokens)
			require.EqualValues(t, 10, repo.stored.Result.Usage[1].InputTokens)
			require.EqualValues(t, 50, repo.stored.Result.Usage[2].InputTokens)
			require.EqualValues(t, 100, repo.stored.Result.Usage[2].OutputTokens)
			require.Equal(t, []AttributionOutput{{Text: "private full answer for challenge A", ExpectedCount: 300}, {Text: "private full answer for challenge B", ExpectedCount: 301}, {Text: "private full answer for challenge C", ExpectedCount: 302}}, analyzer.outputs)
			require.Nil(t, repo.stored.Checkpoint)
		})
	}
}

func TestAttributionRetryOrdinaryFailuresKeepLimitAndBudgetAcross503(t *testing.T) {
	s, repo, analyzer, clock, j := newAttributionRetryHarness("manual")
	statuses := []int{502, 503, 502, 503, 502}
	probes := 0
	s.probe = attributionProbeFunc(func(_ context.Context, _ int64, _, prompt string) (*CandyTestExecution, error) {
		require.Equal(t, "challenge A", prompt)
		require.Less(t, probes, len(statuses))
		status := statuses[probes]
		probes++
		clock.Advance(2 * time.Minute)
		return &CandyTestExecution{UpstreamStatus: status}, candyTestError(fmt.Sprintf("upstream_http_%d", status))
	})
	for i := 0; i < 2; i++ {
		s.execute(context.Background(), j)
		require.Zero(t, repo.finishes)
		require.Equal(t, i+1, repo.stored.Checkpoint.OrdinaryRetries)
		wantConsumed := time.Duration(i+1)*2*time.Minute + time.Duration((1<<(i+1))-1)*time.Second
		require.EqualValues(t, (10*time.Minute - wantConsumed).Milliseconds(), repo.stored.Checkpoint.RemainingBudgetMS)
		j = resumeAttributionRetry(t, repo, clock)
	}
	s.execute(context.Background(), j)
	require.Equal(t, 5, probes)
	require.Equal(t, "failed", repo.stored.Status)
	require.Equal(t, "upstream_http_502", repo.stored.Reason)
	require.Equal(t, 4, repo.stored.Result.Retries)
	require.Empty(t, analyzer.outputs)
	require.Equal(t, 1, repo.finishes)
}

func TestAttributionRetry429AndUnclassifiedLimitsRemainTerminal(t *testing.T) {
	for _, tc := range []struct {
		name      string
		execution *CandyTestExecution
		err       error
	}{
		{name: "HTTP 429", execution: &CandyTestExecution{UpstreamStatus: 429}, err: candyTestError("upstream_http_429")},
		{name: "SSE explicit 429", execution: &CandyTestExecution{UpstreamStatus: 429}, err: candyTestError("account_rate_limited")},
		{name: "legacy rate code", err: ErrDiagnosticRateLimited},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, repo, analyzer, _, j := newAttributionRetryHarness("scheduled")
			probes := 0
			s.probe = attributionProbeFunc(func(context.Context, int64, string, string) (*CandyTestExecution, error) {
				probes++
				return tc.execution, tc.err
			})
			s.execute(context.Background(), j)
			require.Equal(t, 1, probes)
			require.Zero(t, repo.defers)
			require.Equal(t, "skipped", repo.stored.Status)
			require.Equal(t, "account_rate_limited", repo.stored.Reason)
			require.Empty(t, analyzer.outputs)
		})
	}
}

func TestAttributionRetryBudgetExhaustedDoesNotProbe(t *testing.T) {
	s, repo, analyzer, _, j := newAttributionRetryHarness("manual")
	j.Checkpoint = &AttributionCheckpoint{Version: 1, Challenges: attributionRetryChallenges(), RemainingBudgetMS: 0}
	s.probe = attributionProbeFunc(func(context.Context, int64, string, string) (*CandyTestExecution, error) {
		t.Fatal("an exhausted non-503 budget must not start a physical request")
		return nil, nil
	})
	s.execute(context.Background(), j)
	require.Equal(t, "failed", repo.stored.Status)
	require.Equal(t, "timeout", repo.stored.Reason)
	require.Zero(t, repo.defers)
	require.Empty(t, analyzer.outputs)
}

func TestAttributionRetryLostCheckpointClaimNeverFinishes(t *testing.T) {
	for _, loss := range []string{"initial checkpoint", "success checkpoint", "retry checkpoint"} {
		t.Run(loss, func(t *testing.T) {
			s, repo, analyzer, _, j := newAttributionRetryHarness("manual")
			repo.loseSave = loss == "initial checkpoint"
			s.probe = attributionProbeFunc(func(_ context.Context, _ int64, model, _ string) (*CandyTestExecution, error) {
				switch loss {
				case "initial checkpoint":
					t.Fatal("lost checkpoint ownership must stop before inference")
				case "success checkpoint":
					repo.loseSave = true
					return &CandyTestExecution{ActualModel: model, Completed: true, ResponseText: "private completed answer"}, nil
				case "retry checkpoint":
					repo.loseDefer = true
					return nil, candyTestError("upstream_http_503")
				}
				return nil, errors.New("unexpected probe")
			})
			s.execute(context.Background(), j)
			require.Zero(t, repo.finishes, "a stale worker cannot finalize another owner's task")
			require.Empty(t, analyzer.outputs)
		})
	}
}

func TestAttributionRetryCancelledWaitCannotResume(t *testing.T) {
	s, repo, _, clock, j := newAttributionRetryHarness("manual")
	s.probe = attributionProbeFunc(func(context.Context, int64, string, string) (*CandyTestExecution, error) {
		return nil, candyTestError("upstream_http_503")
	})
	s.execute(context.Background(), j)
	require.Equal(t, "retry_wait", repo.stored.Status)
	for i := 0; i < 2; i++ {
		cancelled, err := s.Cancel(context.Background(), j.ID)
		require.NoError(t, err)
		require.Equal(t, "cancelled", cancelled.Status)
		require.Nil(t, cancelled.Checkpoint)
		require.Nil(t, cancelled.NextAttemptAt)
	}
	clock.Advance(time.Hour)
	claimed, err := repo.Claim(context.Background())
	require.NoError(t, err)
	require.Nil(t, claimed)
	require.Zero(t, repo.finishes)
}

func TestAttributionRetryCancellationDuringProbeRejectsLateSuccess(t *testing.T) {
	s, repo, analyzer, _, j := newAttributionRetryHarness("manual")
	s.probe = attributionProbeFunc(func(ctx context.Context, _ int64, model, _ string) (*CandyTestExecution, error) {
		_, err := s.Cancel(ctx, j.ID)
		require.NoError(t, err)
		return &CandyTestExecution{ActualModel: model, Completed: true, ResponseText: "late private answer"}, nil
	})
	s.execute(context.Background(), j)
	require.Equal(t, "cancelled", repo.stored.Status)
	require.Zero(t, repo.finishes)
	require.Empty(t, analyzer.outputs)
	require.Nil(t, repo.stored.Checkpoint)
}

func TestAttributionRetryOrdinaryWaitConsumesRemainingBudget(t *testing.T) {
	s, repo, analyzer, clock, j := newAttributionRetryHarness("manual")
	j.Checkpoint = &AttributionCheckpoint{Version: 1, Challenges: attributionRetryChallenges(), RemainingBudgetMS: 1500}
	probes := 0
	s.probe = attributionProbeFunc(func(ctx context.Context, _ int64, _, _ string) (*CandyTestExecution, error) {
		probes++
		deadline, ok := ctx.Deadline()
		require.True(t, ok, "each physical request must retain a timeout")
		require.LessOrEqual(t, time.Until(deadline), 10*time.Minute)
		require.Greater(t, time.Until(deadline), 9*time.Minute, "physical timeout must not preempt a definite 503 before its status arrives")
		clock.Advance(time.Second)
		return nil, candyTestError("upstream_http_502")
	})
	s.execute(context.Background(), j)
	require.Equal(t, 1, probes)
	require.Equal(t, "failed", repo.stored.Status)
	require.Equal(t, "timeout", repo.stored.Reason)
	require.Zero(t, repo.stored.Result.Retries, "a waiting interval does not count as an actual retry request")
	require.Empty(t, analyzer.outputs)
}

func TestAttributionRetry503LongerThanRemainingOrdinaryBudgetStillWaits(t *testing.T) {
	s, repo, _, clock, j := newAttributionRetryHarness("manual")
	j.Checkpoint = &AttributionCheckpoint{Version: 1, Challenges: attributionRetryChallenges(), RemainingBudgetMS: 1000}
	s.probe = attributionProbeFunc(func(ctx context.Context, _ int64, _, _ string) (*CandyTestExecution, error) {
		deadline, ok := ctx.Deadline()
		require.True(t, ok)
		require.Greater(t, time.Until(deadline), 9*time.Minute)
		clock.Advance(2 * time.Second)
		return &CandyTestExecution{UpstreamStatus: 503}, candyTestError("account_rate_limited")
	})
	s.execute(context.Background(), j)
	require.Equal(t, "retry_wait", repo.stored.Status)
	require.EqualValues(t, 1000, repo.stored.Checkpoint.RemainingBudgetMS)
	require.Zero(t, repo.finishes)
	require.Equal(t, 1, repo.defers)
}

func TestAttributionRetry503BackoffAndRetryAfter(t *testing.T) {
	for _, tc := range []struct {
		name       string
		retries    int
		retryAfter time.Duration
		minimum    time.Duration
		maximum    time.Duration
	}{
		{name: "initial", retries: 1, minimum: 2 * time.Second, maximum: 2400 * time.Millisecond},
		{name: "second", retries: 2, minimum: 4 * time.Second, maximum: 4800 * time.Millisecond},
		{name: "exponential cap", retries: 100, minimum: 60 * time.Second, maximum: 60 * time.Second},
		{name: "retry after minimum", retries: 1, retryAfter: 90 * time.Second, minimum: 90 * time.Second, maximum: 90 * time.Second},
		{name: "retry after cap", retries: 1, retryAfter: time.Hour, minimum: 300 * time.Second, maximum: 300 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for i := 0; i < 30; i++ {
				delay := attributionRetryDelay(tc.retries, tc.retryAfter)
				require.GreaterOrEqual(t, delay, tc.minimum)
				require.LessOrEqual(t, delay, tc.maximum)
			}
		})
	}
}

func TestAttributionRetryDoesNotInfer503FromErrorText(t *testing.T) {
	s, repo, _, _, j := newAttributionRetryHarness("manual")
	probes := 0
	s.probe = attributionProbeFunc(func(context.Context, int64, string, string) (*CandyTestExecution, error) {
		probes++
		return nil, errors.New("HTTP 503 rate_limit_exceeded: private upstream body")
	})
	s.execute(context.Background(), j)
	require.Equal(t, 1, probes)
	require.Zero(t, repo.defers)
	require.Equal(t, "failed", repo.stored.Status)
	require.Equal(t, "probe_failed", repo.stored.Reason)
	public, err := json.Marshal(repo.stored)
	require.NoError(t, err)
	require.NotContains(t, string(public), "private upstream body")
}

func TestAttributionRetryRestoresAuthorizationFence(t *testing.T) {
	s, repo, _, clock, j := newAttributionRetryHarness("manual")
	probes := 0
	s.probe = attributionProbeFunc(func(ctx context.Context, _ int64, model, prompt string) (*CandyTestExecution, error) {
		probes++
		expected, _ := ctx.Value(attributionProbeAuthorizationKey{}).(string)
		if probes == 1 {
			require.Empty(t, expected)
			return &CandyTestExecution{AttributionAuthorization: "original-authorization", UpstreamStatus: 503}, candyTestError("upstream_http_503")
		}
		require.Equal(t, "original-authorization", expected)
		return &CandyTestExecution{AttributionAuthorization: "original-authorization", ActualModel: model, Completed: true, ResponseText: "answer " + prompt}, nil
	})
	s.execute(context.Background(), j)
	require.Equal(t, "original-authorization", repo.stored.Checkpoint.ProbeAuthorization)
	j = resumeAttributionRetry(t, repo, clock)
	s.execute(context.Background(), j)
	require.Equal(t, "passed", repo.stored.Status)
	require.Equal(t, 4, probes)
	public, err := json.Marshal(repo.stored)
	require.NoError(t, err)
	require.NotContains(t, string(public), "original-authorization")
}

func TestAttributionRetryRejectsChangedAuthorizationBeforeDeferring(t *testing.T) {
	s, repo, analyzer, _, j := newAttributionRetryHarness("manual")
	j.Checkpoint = &AttributionCheckpoint{Version: 1, Challenges: attributionRetryChallenges(), RemainingBudgetMS: (10 * time.Minute).Milliseconds(), ProbeAuthorization: "original-authorization"}
	s.probe = attributionProbeFunc(func(ctx context.Context, _ int64, _, _ string) (*CandyTestExecution, error) {
		require.Equal(t, "original-authorization", ctx.Value(attributionProbeAuthorizationKey{}))
		return &CandyTestExecution{AttributionAuthorization: "replacement-authorization", UpstreamStatus: 503}, candyTestError("upstream_http_503")
	})
	s.execute(context.Background(), j)
	require.Equal(t, "failed", repo.stored.Status)
	require.Equal(t, "authorization_changed", repo.stored.Reason)
	require.Zero(t, repo.defers)
	require.Empty(t, analyzer.outputs)
	require.Nil(t, repo.stored.Checkpoint)
}
