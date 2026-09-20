package service

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCodexTurnStateRetryPolicyOnlyNoTargetWaitsThirtySeconds(t *testing.T) {
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
		{name: "extended", status: 200, blocks: 11, wait: 30 * time.Second},
		{name: "missing", status: 200, wait: 30 * time.Second},
		{name: "invalid", status: 200, invalid: true, wait: 30 * time.Second},
		{name: "transport", collectErr: errCodexTurnStateCollectorTransportFailed},
		{name: "timeout", collectErr: context.DeadlineExceeded},
		{name: "proxy_unavailable", collectErr: ErrCodexTurnStateCollectorProxyUnavailable},
		{name: "upstream_5xx", status: 503},
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
				require.True(t, reserved.NextCollectAt.After(clock.Add(30*time.Second)), "in-flight crash reservation remains separate from the completed-result policy")
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
		updated.OverloadUntil = &until
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
