package service

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCodexTurnStateRetryPolicyOnlyNoTargetWaitsFiveSeconds(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		collectErr error
		blocks     int
		invalid    bool
		retryAfter time.Duration
		wait       time.Duration
		paused     bool
	}{
		{name: "extended", status: 200, blocks: 11, wait: 5 * time.Second},
		{name: "missing", status: 200, wait: 5 * time.Second},
		{name: "invalid", status: 200, invalid: true, wait: 5 * time.Second},
		{name: "transport", collectErr: errCodexTurnStateCollectorTransportFailed},
		{name: "timeout", collectErr: context.DeadlineExceeded},
		{name: "proxy_unavailable", collectErr: ErrCodexTurnStateCollectorProxyUnavailable},
		{name: "upstream_5xx", status: 503},
		{name: "upstream_5xx_retry_after", status: 503, retryAfter: time.Minute},
		{name: "stream", status: 200, collectErr: errCodexTurnStateCollectorStreamFailed},
		{name: "response_failed", status: 200, collectErr: errCodexTurnStateCollectorResponseFailed},
		{name: "response_incomplete", status: 200, collectErr: errCodexTurnStateCollectorResponseIncomplete},
		{name: "http_429_no_header", status: 429},
		{name: "sse_429_no_header", status: 200, collectErr: errCodexTurnStateCollectorRateLimited},
		{name: "http_429_short_header", status: 429, retryAfter: 7 * time.Second, wait: 7 * time.Second},
		{name: "sse_429_long_header", status: 200, collectErr: errCodexTurnStateCollectorRateLimited, retryAfter: time.Minute, wait: time.Minute},
		{name: "unauthorized", status: 401, paused: true},
		{name: "forbidden", status: 403, paused: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, repo, account := newCodexStateTestService(t)
			clock := s.now()
			s.now = func() time.Time { return clock }
			ctx := context.Background()
			attempt := seedCodexStateTestDemand(t, s, account, "gpt-5")
			calls := 0
			s.collector = codexStateTestCollector(func(context.Context, CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error) {
				calls++
				reserved, err := repo.Get(ctx, attempt.key)
				require.NoError(t, err)
				require.Equal(t, clock.Add(CodexTurnStateCollectTimeout+CodexTurnStateRetryInterval), reserved.NextCollectAt, "in-flight crash reservation remains separate from the completed-result policy")
				result := CodexTurnStateCollectResult{StatusCode: tc.status, RetryAfter: tc.retryAfter}
				if tc.blocks != 0 {
					result.Tokens = []string{codexStateTestToken(tc.blocks, clock)}
				}
				if tc.invalid {
					result.Tokens = []string{"invalid"}
				}
				return result, tc.collectErr
			})
			s.collect(ctx, attempt.key)
			require.Equal(t, 1, calls, "completion never retries recursively")
			after, err := repo.Get(ctx, attempt.key)
			require.NoError(t, err)
			require.Equal(t, clock.Add(tc.wait), after.NextCollectAt)
			require.Equal(t, tc.paused, after.CollectorPaused)
			require.NotEmpty(t, after.DemandReason)
			clock = clock.Add(CodexTurnStateDueInterval)
			s.collect(ctx, attempt.key)
			if tc.wait == 0 && !tc.paused {
				require.Equal(t, 2, calls, "a non-target-independent error is eligible on the next scheduler tick")
			} else {
				require.Equal(t, 1, calls, "target-shape retry or explicit cooldown/pause still blocks collection")
			}
		})
	}
}

func TestCodexTurnStateRetryPolicyPreservesLongerConcurrentCooldown(t *testing.T) {
	for _, result := range []CodexTurnStateCollectResult{{StatusCode: 503}, {StatusCode: 429, RetryAfter: 7 * time.Second}, {StatusCode: 200}} {
		s, repo, account := newCodexStateTestService(t)
		ctx := context.Background()
		attempt := seedCodexStateTestDemand(t, s, account, "gpt-5")
		until := s.now().Add(2 * time.Minute)
		s.collector = codexStateTestCollector(func(context.Context, CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error) {
			live, err := repo.Get(ctx, attempt.key)
			require.NoError(t, err)
			live.NextCollectAt, live.LastError = until, "account_cooldown"
			ok, err := repo.SaveCAS(ctx, *live, live.Version)
			require.NoError(t, err)
			require.True(t, ok)
			return result, errors.New("synthetic ordinary failure")
		})
		s.collect(ctx, attempt.key)
		after, err := repo.Get(ctx, attempt.key)
		require.NoError(t, err)
		require.Equal(t, until, after.NextCollectAt)
		require.Equal(t, "account_cooldown", after.LastError)
	}
}

func TestCodexTurnStateRetryPolicyOwnerCooldownStillAppliesToHTTP429(t *testing.T) {
	s, repo, account := newCodexStateTestService(t)
	ctx := context.Background()
	attempt := seedCodexStateTestDemand(t, s, account, "gpt-5")
	until := s.now().Add(3 * time.Minute)
	s.collector = codexStateTestCollector(func(context.Context, CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error) {
		updated := *account
		updated.RateLimitResetAt = &until
		accounts := s.accounts.(*codexStateTestAccounts)
		accounts.mu.Lock()
		accounts.account = &updated
		accounts.mu.Unlock()
		return CodexTurnStateCollectResult{StatusCode: http.StatusTooManyRequests}, nil
	})
	s.collect(ctx, attempt.key)
	after, err := repo.Get(ctx, attempt.key)
	require.NoError(t, err)
	require.Equal(t, until, after.NextCollectAt)
	require.Equal(t, "collector_rate_limited", after.LastError)
}

func TestCodexTurnStateRetryPolicyOtherModelShapeDelayDoesNotDelayTransportRetry(t *testing.T) {
	s, repo, account := newCodexStateTestService(t)
	clock := s.now()
	s.now = func() time.Time { return clock }
	ctx := context.Background()
	shape := seedCodexStateTestDemand(t, s, account, "gpt-5")
	transport := seedCodexStateTestDemand(t, s, account, "gpt-5-mini")
	calls := map[string]int{}
	s.collector = codexStateTestCollector(func(_ context.Context, input CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error) {
		calls[input.Model]++
		if input.Model == "gpt-5" {
			return CodexTurnStateCollectResult{StatusCode: http.StatusOK, completed: true}, nil
		}
		return CodexTurnStateCollectResult{}, context.DeadlineExceeded
	})
	s.collect(ctx, shape.key)
	s.collect(ctx, transport.key)
	require.Equal(t, 1, calls[transport.key.Model], "another model's no-target result is not an account cooldown")
	after, err := repo.Get(ctx, transport.key)
	require.NoError(t, err)
	require.Equal(t, clock, after.NextCollectAt)
	require.Empty(t, after.CollectorAttemptID)
	clock = clock.Add(CodexTurnStateDueInterval)
	s.collect(ctx, transport.key)
	require.Equal(t, 2, calls[transport.key.Model], "the timeout may retry at the next one-second scheduler tick")
	s.collect(ctx, shape.key)
	require.Equal(t, 1, calls[shape.key.Model], "the five-second shape delay still applies to its own model")
}

func TestCodexTurnStateRetryPolicyNewReservationClearsExpiredRateLimitReason(t *testing.T) {
	for _, reason := range []string{"collector_rate_limited", "account_cooldown"} {
		t.Run(reason, func(t *testing.T) {
			s, repo, account := newCodexStateTestService(t)
			ctx := context.Background()
			attempt := seedCodexStateTestDemand(t, s, account, "gpt-5")
			before, err := repo.Get(ctx, attempt.key)
			require.NoError(t, err)
			before.LastError, before.CollectionStatus = reason, "backoff"
			before.NextCollectAt, before.LastCollectedAt = s.now().Add(-time.Second), s.now().Add(-time.Minute)
			ok, err := repo.SaveCAS(ctx, *before, before.Version)
			require.NoError(t, err)
			require.True(t, ok)
			s.collector = codexStateTestCollector(func(context.Context, CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error) {
				reserved, err := repo.Get(ctx, attempt.key)
				require.NoError(t, err)
				require.Empty(t, reserved.LastError, "a crash reservation cannot inherit an old account-cooldown marker")
				require.Equal(t, "collecting", reserved.CollectionStatus)
				return CodexTurnStateCollectResult{}, context.DeadlineExceeded
			})
			s.collect(ctx, attempt.key)
			after, err := repo.Get(ctx, attempt.key)
			require.NoError(t, err)
			require.Equal(t, s.now(), after.NextCollectAt)
			require.Equal(t, "collection_timeout", after.LastError)
			require.Equal(t, "pending", after.CollectionStatus)
			require.Empty(t, after.CollectorAttemptID)
		})
	}
}

type codexTurnStateReservationDeadlineRepo struct{ *codexStateMemoryRepo }

func (r *codexTurnStateReservationDeadlineRepo) SaveCAS(ctx context.Context, record CodexTurnStateRecord, version int64) (bool, error) {
	accepted, err := r.codexStateMemoryRepo.SaveCAS(ctx, record, version)
	if accepted && record.CollectionStatus == "collecting" {
		<-ctx.Done()
	}
	return accepted, err
}

func TestCodexTurnStateRetryPolicyDeadlineAfterReservationClearsItWithoutSending(t *testing.T) {
	s, repo, account := newCodexStateTestService(t)
	attempt := seedCodexStateTestDemand(t, s, account, "gpt-5")
	s.repo = &codexTurnStateReservationDeadlineRepo{repo}
	calls := 0
	s.collector = codexStateTestCollector(func(context.Context, CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error) {
		calls++
		return CodexTurnStateCollectResult{}, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	s.collect(ctx, attempt.key)
	after, err := repo.Get(context.Background(), attempt.key)
	require.NoError(t, err)
	require.Zero(t, calls)
	require.Empty(t, after.CollectorAttemptID)
	require.Equal(t, s.now(), after.NextCollectAt)
	require.Equal(t, "collection_timeout", after.LastError)
	require.Equal(t, "pending", after.CollectionStatus)
}
