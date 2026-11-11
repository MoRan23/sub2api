package service

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func attributionParallelIndex(prompt string) int {
	return int(prompt[len(prompt)-1] - 'A')
}

func attributionParallelStarted(t *testing.T, started <-chan int) {
	t.Helper()
	seen := map[int]bool{}
	for range 3 {
		select {
		case index := <-started:
			seen[index] = true
		case <-time.After(5 * time.Second):
			t.Fatal("all three probes must start before any response is released")
		}
	}
	require.Len(t, seen, 3)
}

func attributionParallelDone(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("attribution workers did not drain")
	}
}

func TestAttributionParallelOverlapAndOrderedEvidence(t *testing.T) {
	s, repo, analyzer, _, job := newAttributionRetryHarness("manual")
	job.Snapshot.Version, job.Snapshot.Daybreak = 3, &DiagnosticDaybreakPreferences{Blue: true}
	started := make(chan int, 3)
	releases := []chan struct{}{make(chan struct{}), make(chan struct{}), make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.probe = attributionProbeFunc(func(ctx context.Context, _ int64, model, prompt string) (*CandyTestExecution, error) {
		index := attributionParallelIndex(prompt)
		started <- index
		select {
		case <-releases[index]:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return &CandyTestExecution{Completed: true, ActualModel: model, ResponseText: prompt,
			AttributionAuthorization: "one-round-auth", Usage: &OpenAIUsage{InputTokens: index + 1},
			Daybreak: &DiagnosticDaybreakEvidence{Cyber: "daybreak_blue", Reason: prompt}}, nil
	})
	done := make(chan struct{})
	go func() { defer close(done); s.execute(ctx, job) }()
	attributionParallelStarted(t, started)
	close(releases[1])
	require.Eventually(t, func() bool { return repo.storedJob().Result.CompletedProbes == 1 }, 5*time.Second, time.Millisecond)
	checkpoint := repo.storedJob().Checkpoint
	require.Nil(t, checkpoint.Slots[0].Output)
	require.Equal(t, "challenge B", checkpoint.Slots[1].Output.Text)
	close(releases[2])
	require.Eventually(t, func() bool { return repo.storedJob().Result.CompletedProbes == 2 }, 5*time.Second, time.Millisecond)
	close(releases[0])
	attributionParallelDone(t, done)
	require.Equal(t, "passed", repo.stored.Status)
	require.Equal(t, 1, repo.finishes)
	for i, output := range analyzer.outputs {
		require.Equal(t, attributionRetryChallenges()[i].Prompt, output.Text)
		require.Equal(t, attributionRetryChallenges()[i].ExpectedCount, output.ExpectedCount)
		require.EqualValues(t, i+1, repo.stored.Result.Usage[i].InputTokens)
		require.Equal(t, output.Text, repo.stored.Result.Daybreak[i].Reason)
	}
}

func TestAttributionParallelInitialAuthorizationMismatchDrainsPeers(t *testing.T) {
	s, repo, analyzer, _, job := newAttributionRetryHarness("manual")
	job.Snapshot.Version, job.Snapshot.Daybreak = 3, &DiagnosticDaybreakPreferences{Blue: true}
	started, initialFences := make(chan int, 3), make(chan string, 3)
	releaseFirst, releaseMismatch := make(chan struct{}), make(chan struct{})
	peerCancelled, drainPeer := make(chan struct{}), make(chan struct{})
	var drainOnce sync.Once
	releasePeer := func() { drainOnce.Do(func() { close(drainPeer) }) }
	defer releasePeer()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.probe = attributionProbeFunc(func(ctx context.Context, _ int64, model, prompt string) (*CandyTestExecution, error) {
		index := attributionParallelIndex(prompt)
		fence, _ := ctx.Value(attributionProbeAuthorizationKey{}).(string)
		initialFences <- fence
		started <- index
		switch index {
		case 0:
			select {
			case <-releaseFirst:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		case 1:
			select {
			case <-releaseMismatch:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		default:
			<-ctx.Done()
			close(peerCancelled)
			<-drainPeer
		}
		return &CandyTestExecution{Completed: true, ActualModel: model, ResponseText: prompt,
			AttributionAuthorization: []string{"first-authorization", "changed-authorization", "late-authorization"}[index],
			Usage:                    &OpenAIUsage{InputTokens: index + 1},
			Daybreak:                 &DiagnosticDaybreakEvidence{Cyber: "daybreak_blue", Reason: prompt}}, nil
	})
	done := make(chan struct{})
	go func() { defer close(done); s.execute(ctx, job) }()
	attributionParallelStarted(t, started)
	for range 3 {
		require.Empty(t, <-initialFences, "every first-wave request starts before a probe digest has been established")
	}
	close(releaseFirst)
	require.Eventually(t, func() bool { return repo.storedJob().Result.CompletedProbes == 1 }, 5*time.Second, time.Millisecond)
	require.Equal(t, "first-authorization", repo.storedJob().Checkpoint.ProbeAuthorization)
	close(releaseMismatch)
	attributionParallelDone(t, peerCancelled)
	require.Equal(t, "running", repo.storedJob().Status, "authorization failure must drain its cancelled sibling before finishing")
	select {
	case <-done:
		t.Fatal("the round finished before its last worker drained")
	default:
	}
	releasePeer()
	attributionParallelDone(t, done)
	require.Equal(t, "failed", repo.stored.Status)
	require.Equal(t, "authorization_changed", repo.stored.Reason)
	require.Equal(t, 1, repo.finishes)
	require.Zero(t, repo.defers)
	require.Empty(t, analyzer.outputs)
	require.Nil(t, repo.stored.Result.Analysis)
	require.Empty(t, repo.stored.Result.Action)
	require.Empty(t, repo.stored.Result.AppliedTier)
	require.Equal(t, 1, repo.stored.Result.CompletedProbes)
	require.Equal(t, []*DiagnosticDaybreakEvidence{{Cyber: "daybreak_blue", Reason: "challenge A"}}, repo.stored.Result.Daybreak)
	require.NotNil(t, repo.beforeFinish.Slots[0].Output)
	require.Nil(t, repo.beforeFinish.Slots[1].Output)
	require.Nil(t, repo.beforeFinish.Slots[2].Output)
	for i := range 3 {
		require.EqualValues(t, i+1, repo.stored.Result.Usage[i].InputTokens, "each returned execution contributes usage exactly once")
	}
}

func TestAttributionParallelEarly503PreservesLaterSuccesses(t *testing.T) {
	s, repo, analyzer, clock, job := newAttributionRetryHarness("manual")
	job.Snapshot.Version, job.Snapshot.Daybreak = 3, &DiagnosticDaybreakPreferences{Blue: true}
	started := make(chan int, 3)
	releases := []chan struct{}{make(chan struct{}), make(chan struct{}), make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.probe = attributionProbeFunc(func(ctx context.Context, _ int64, model, prompt string) (*CandyTestExecution, error) {
		index := attributionParallelIndex(prompt)
		started <- index
		select {
		case <-releases[index]:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		execution := &CandyTestExecution{ActualModel: model, ResponseText: prompt, AttributionAuthorization: "frozen-auth",
			Usage: &OpenAIUsage{InputTokens: 7}, Daybreak: &DiagnosticDaybreakEvidence{Cyber: "daybreak_blue"}}
		if index == 0 {
			execution.UpstreamStatus, execution.RetryAfter = 503, time.Minute
			execution.Daybreak = &DiagnosticDaybreakEvidence{Cyber: "daybreak_red", Reason: "failed"}
			return execution, candyTestError("upstream_http_503")
		}
		execution.Completed = true
		return execution, nil
	})
	done := make(chan struct{})
	go func() { defer close(done); s.execute(ctx, job) }()
	attributionParallelStarted(t, started)
	close(releases[0])
	require.Eventually(t, func() bool { return repo.storedJob().Checkpoint.Slots[0].UnavailableRetries == 1 }, 5*time.Second, time.Millisecond)
	require.Equal(t, "running", repo.storedJob().Status, "503 cannot release the lease while siblings run")
	close(releases[2])
	close(releases[1])
	attributionParallelDone(t, done)
	require.Equal(t, "retry_wait", repo.stored.Status)
	require.Equal(t, 2, repo.stored.Result.CompletedProbes)
	require.Nil(t, repo.stored.Result.Daybreak[0])
	require.Empty(t, analyzer.outputs)
	resumed := resumeAttributionRetry(t, repo, clock)
	s.probe = attributionProbeFunc(func(ctx context.Context, _ int64, model, prompt string) (*CandyTestExecution, error) {
		require.Equal(t, "challenge A", prompt)
		require.Equal(t, "frozen-auth", ctx.Value(attributionProbeAuthorizationKey{}))
		return &CandyTestExecution{ActualModel: model, Completed: true, ResponseText: prompt, AttributionAuthorization: "frozen-auth",
			Usage: &OpenAIUsage{InputTokens: 11}, Daybreak: &DiagnosticDaybreakEvidence{Cyber: "daybreak_blue"}}, nil
	})
	s.execute(context.Background(), resumed)
	require.Equal(t, "passed", repo.stored.Status)
	require.Equal(t, 1, repo.stored.Result.Retries)
	require.EqualValues(t, 18, repo.stored.Result.Usage[0].InputTokens)
	require.EqualValues(t, 7, repo.stored.Result.Usage[1].InputTokens)
	require.EqualValues(t, 7, repo.stored.Result.Usage[2].InputTokens)
	require.Empty(t, AttributionDaybreakIssue(job.Snapshot, repo.stored.Result))
	require.Len(t, analyzer.outputs, 3)
}

func TestAttributionParallel429DrainsPeersBeforeFinish(t *testing.T) {
	s, repo, analyzer, _, job := newAttributionRetryHarness("scheduled")
	started := make(chan int, 3)
	release503, release429, peerCancelled, drainPeer := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.probe = attributionProbeFunc(func(ctx context.Context, _ int64, _, prompt string) (*CandyTestExecution, error) {
		index := attributionParallelIndex(prompt)
		started <- index
		execution := &CandyTestExecution{Usage: &OpenAIUsage{InputTokens: index + 3}}
		switch index {
		case 0:
			select {
			case <-release503:
			case <-ctx.Done():
			}
			execution.UpstreamStatus = 503
			return execution, candyTestError("upstream_http_503")
		case 1:
			select {
			case <-release429:
			case <-ctx.Done():
			}
			execution.UpstreamStatus = 429
			return execution, candyTestError("upstream_http_429")
		default:
			<-ctx.Done()
			close(peerCancelled)
			<-drainPeer
			return execution, ctx.Err()
		}
	})
	done := make(chan struct{})
	go func() { defer close(done); s.execute(ctx, job) }()
	attributionParallelStarted(t, started)
	close(release503)
	require.Eventually(t, func() bool { return repo.storedJob().Checkpoint.Slots[0].UnavailableRetries == 1 }, 5*time.Second, time.Millisecond)
	close(release429)
	attributionParallelDone(t, peerCancelled)
	require.Equal(t, "running", repo.storedJob().Status)
	close(drainPeer)
	attributionParallelDone(t, done)
	require.Equal(t, "skipped", repo.stored.Status)
	require.Equal(t, "account_rate_limited", repo.stored.Reason)
	require.Zero(t, repo.defers)
	require.Equal(t, 1, repo.finishes)
	require.Empty(t, analyzer.outputs)
	for i := range 3 {
		require.EqualValues(t, i+3, repo.stored.Result.Usage[i].InputTokens)
	}
}

func TestAttributionParallelDistinct503DueTimes(t *testing.T) {
	s, repo, _, clock, job := newAttributionRetryHarness("manual")
	var mu sync.Mutex
	counts := [3]int{}
	s.probe = attributionProbeFunc(func(_ context.Context, _ int64, model, prompt string) (*CandyTestExecution, error) {
		index := attributionParallelIndex(prompt)
		mu.Lock()
		counts[index]++
		count := counts[index]
		mu.Unlock()
		execution := &CandyTestExecution{ActualModel: model, ResponseText: prompt, Usage: &OpenAIUsage{InputTokens: 1}}
		if count == 1 && index < 2 {
			execution.UpstreamStatus, execution.RetryAfter = 503, []time.Duration{5 * time.Second, 90 * time.Second}[index]
			return execution, candyTestError("upstream_http_503")
		}
		execution.Completed = true
		return execution, nil
	})
	s.execute(context.Background(), job)
	require.Equal(t, [3]int{1, 1, 1}, counts)
	firstDue, secondDue := *repo.stored.Checkpoint.Slots[0].NextAttemptAt, *repo.stored.Checkpoint.Slots[1].NextAttemptAt
	require.Equal(t, 85*time.Second, secondDue.Sub(firstDue))
	s.execute(context.Background(), resumeAttributionRetry(t, repo, clock))
	require.Equal(t, [3]int{2, 1, 1}, counts, "later Retry-After must not be retried early")
	require.Equal(t, "retry_wait", repo.stored.Status)
	require.Equal(t, secondDue, *repo.stored.NextAttemptAt)
	s.execute(context.Background(), resumeAttributionRetry(t, repo, clock))
	require.Equal(t, "passed", repo.stored.Status)
	require.Equal(t, [3]int{2, 2, 1}, counts)
	require.Equal(t, 2, repo.stored.Result.Retries)
}

func TestAttributionParallelOrdinaryRetriesHaveIndependentLimitsAndSharedBudget(t *testing.T) {
	s, repo, _, _, job := newAttributionRetryHarness("manual")
	var mu sync.Mutex
	counts := [3]int{}
	s.probe = attributionProbeFunc(func(_ context.Context, _ int64, model, prompt string) (*CandyTestExecution, error) {
		index := attributionParallelIndex(prompt)
		mu.Lock()
		counts[index]++
		count := counts[index]
		mu.Unlock()
		execution := &CandyTestExecution{ActualModel: model, ResponseText: prompt, DurationMs: 1000, Usage: &OpenAIUsage{InputTokens: 1}}
		if count <= 2-index {
			execution.UpstreamStatus = 502
			return execution, candyTestError("upstream_http_502")
		}
		execution.Completed = true
		return execution, nil
	})
	s.execute(context.Background(), job)
	require.Equal(t, "passed", repo.stored.Status)
	require.Equal(t, [3]int{3, 2, 1}, counts)
	require.Equal(t, 3, repo.stored.Result.Retries)
	require.EqualValues(t, (10*time.Minute - 10*time.Second).Milliseconds(), repo.beforeFinish.RemainingBudgetMS)
	for i, count := range counts {
		require.EqualValues(t, count, repo.stored.Result.Usage[i].InputTokens)
	}
}

type attributionParallelPanicRepo struct {
	*attributionRetryRepo
	checkpointCalls int
}

func (r *attributionParallelPanicRepo) SaveCheckpoint(ctx context.Context, job *AttributionJob) (bool, error) {
	r.checkpointCalls++
	if r.checkpointCalls == 2 {
		panic("private repository panic")
	}
	return r.attributionRetryRepo.SaveCheckpoint(ctx, job)
}

func TestAttributionParallelCancellationAndPanicDrainWorkers(t *testing.T) {
	for _, scenario := range []string{"cancel", "panic", "checkpoint panic"} {
		t.Run(scenario, func(t *testing.T) {
			s, repo, analyzer, _, job := newAttributionRetryHarness("manual")
			job.Snapshot.Version, job.Snapshot.Daybreak = 3, &DiagnosticDaybreakPreferences{Blue: true}
			if scenario == "checkpoint panic" {
				s.repo = &attributionParallelPanicRepo{attributionRetryRepo: repo}
			}
			started, trigger := make(chan int, 3), make(chan struct{})
			var exited atomic.Int32
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s.probe = attributionProbeFunc(func(ctx context.Context, _ int64, model, prompt string) (*CandyTestExecution, error) {
				defer exited.Add(1)
				index := attributionParallelIndex(prompt)
				started <- index
				if scenario != "cancel" && index == 0 {
					select {
					case <-trigger:
					case <-ctx.Done():
						return nil, ctx.Err()
					}
					if scenario == "panic" {
						panic("private upstream panic")
					}
					return &CandyTestExecution{Completed: true, ActualModel: model, ResponseText: prompt,
						Daybreak: &DiagnosticDaybreakEvidence{Cyber: "daybreak_blue", Reason: prompt}}, nil
				}
				<-ctx.Done()
				return &CandyTestExecution{Usage: &OpenAIUsage{InputTokens: 2},
					Daybreak: &DiagnosticDaybreakEvidence{Cyber: "daybreak_red", Reason: prompt}}, ctx.Err()
			})
			done := make(chan struct{})
			go func() { defer close(done); s.execute(ctx, job) }()
			attributionParallelStarted(t, started)
			if scenario == "cancel" {
				cancel()
			} else {
				close(trigger)
			}
			attributionParallelDone(t, done)
			require.EqualValues(t, 3, exited.Load())
			require.Equal(t, "failed", repo.stored.Status)
			reason := "interrupted"
			if scenario != "cancel" {
				reason = "internal_error"
			}
			require.Equal(t, reason, repo.stored.Reason)
			require.Equal(t, 1, repo.finishes)
			require.Zero(t, repo.defers)
			require.Empty(t, analyzer.outputs)
			if scenario == "checkpoint panic" {
				require.Equal(t, []*DiagnosticDaybreakEvidence{{Cyber: "daybreak_blue", Reason: "challenge A"}}, repo.stored.Result.Daybreak)
			} else {
				require.Empty(t, repo.stored.Result.Daybreak, "cancelled executions cannot contribute successful probe evidence")
			}
			require.EqualValues(t, 2, repo.stored.Result.Usage[1].InputTokens)
			require.EqualValues(t, 2, repo.stored.Result.Usage[2].InputTokens)
		})
	}
}

func TestAttributionParallelLegacyCheckpointConversion(t *testing.T) {
	for completed := 0; completed <= 3; completed++ {
		cp := &AttributionCheckpoint{Version: 1, Challenges: attributionRetryChallenges(), RemainingBudgetMS: 1234, ProbeAuthorization: "legacy-auth"}
		for i := range completed {
			cp.Outputs = append(cp.Outputs, AttributionOutput{Text: cp.Challenges[i].Prompt, ExpectedCount: cp.Challenges[i].ExpectedCount})
		}
		if completed < 3 {
			cp.OrdinaryRetries, cp.UnavailableRetries = 2, 7
		}
		require.True(t, validAttributionCheckpoint(cp))
		upgradeAttributionCheckpoint(cp)
		require.True(t, validAttributionCheckpoint(cp))
		require.Equal(t, completed, cp.CompletedCount())
		require.EqualValues(t, 1234, cp.RemainingBudgetMS)
		require.Equal(t, "legacy-auth", cp.ProbeAuthorization)
		if completed < 3 {
			require.Equal(t, 2, cp.Slots[completed].OrdinaryRetries)
			require.Equal(t, 7, cp.Slots[completed].UnavailableRetries)
		}
	}
}
