package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCodexTurnStateShapeRetryStartsWhenCollectionFinishes(t *testing.T) {
	s, repo, account := newCodexStateTestService(t)
	clock := s.now()
	s.now = func() time.Time { return clock }
	seed := seedCodexStateTestDemand(t, s, account, "gpt-5")
	s.collector = codexStateTestCollector(func(context.Context, CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error) {
		clock = clock.Add(12 * time.Second)
		return CodexTurnStateCollectResult{StatusCode: 200}, nil
	})
	s.collect(context.Background(), seed.key)
	row, err := repo.Get(context.Background(), seed.key)
	require.NoError(t, err)
	require.Equal(t, clock.Add(5*time.Second), row.NextCollectAt)
}

func TestCodexTurnStateSnapshotNeverExtendsEarlierStoredExpiry(t *testing.T) {
	s, repo, account := newCodexStateTestService(t)
	ctx := context.Background()
	clock := s.now()
	s.now = func() time.Time { return clock }
	seed, err := prepareCodexStateTest(s, ctx, account, "gpt-5")
	require.NoError(t, err)
	markCodexStateTestBusinessSent(t, s, seed)
	s.Observe(seed, codexStateTestToken(10, clock))
	require.NoError(t, s.Finish(ctx, seed, true))
	row, err := repo.Get(ctx, seed.key)
	require.NoError(t, err)
	row.ExpiresAt = clock.Add(10 * time.Second)
	bindCodexStateTestBundle(t, s, account, row, row.BundleBinding)
	ok, err := repo.SaveCAS(ctx, *row, row.Version)
	require.NoError(t, err)
	require.True(t, ok)
	attempt, err := prepareCodexStateTest(s, ctx, account, "gpt-5")
	require.NoError(t, err)
	require.Equal(t, row.ExpiresAt, attempt.Snapshot.ExpiresAt)
	clock = row.ExpiresAt
	require.False(t, s.ValidateAttempt(ctx, attempt))
	require.NoError(t, s.Finish(ctx, attempt, false))
}

func TestCodexTurnStateCollectionIgnoresBusinessTransportCooldowns(t *testing.T) {
	for _, status := range []int{200, 503} {
		s, repo, account := newCodexStateTestService(t)
		seed := seedCodexStateTestDemand(t, s, account, "gpt-5")
		until := s.now().Add(10 * time.Minute)
		account.OverloadUntil, account.TempUnschedulableUntil = &until, &until
		calls := 0
		s.collector = codexStateTestCollector(func(context.Context, CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error) {
			calls++
			return CodexTurnStateCollectResult{StatusCode: status, RetryAfter: time.Minute}, nil
		})
		s.collect(context.Background(), seed.key)
		require.Equal(t, 1, calls, "business overload must not block independent collection")
		row, err := repo.Get(context.Background(), seed.key)
		require.NoError(t, err)
		wait := time.Duration(0)
		if status == 200 {
			wait = 5 * time.Second
			require.Equal(t, "backoff", row.CollectionStatus)
		} else {
			require.Equal(t, "pending", row.CollectionStatus)
			require.Equal(t, "collector_upstream_unavailable", row.CollectionReason)
		}
		require.Equal(t, s.now().Add(wait), row.NextCollectAt, "ordinary outcomes must not inherit transport cooldowns or unclassified Retry-After")
	}
}

func TestCodexTurnStateModelMismatchDoesNotAdmitTargetOrChangeProxyCounterByItself(t *testing.T) {
	for _, tc := range []struct {
		name   string
		blocks []int
		count  int
	}{
		{name: "target", blocks: []int{10}, count: 1},
		{name: "target_first_even_with_extended", blocks: []int{11, 10}, count: 1},
		{name: "missing", count: 1},
		{name: "valid_extended_still_counts", blocks: []int{11}, count: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, repo, account := newCodexStateTestService(t)
			ctx := context.Background()
			seed := seedCodexStateTestDemand(t, s, account, "gpt-5")
			row, err := repo.Get(ctx, seed.key)
			require.NoError(t, err)
			row.CollectorExtendedCount = 1
			ok, err := repo.SaveCAS(ctx, *row, row.Version)
			require.NoError(t, err)
			require.True(t, ok)
			s.collector = codexStateTestCollector(func(context.Context, CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error) {
				result := CodexTurnStateCollectResult{StatusCode: 200, completed: true,
					ModelEvidence: CodexModelEvidence{ModelRelation: "different"}}
				for _, blocks := range tc.blocks {
					result.Tokens = append(result.Tokens, codexStateTestToken(blocks, s.now()))
				}
				return result, nil
			})
			s.collect(ctx, seed.key)
			row, err = repo.Get(ctx, seed.key)
			require.NoError(t, err)
			require.Empty(t, row.EncryptedToken)
			require.Equal(t, "model_mismatch", row.LastError)
			require.Equal(t, tc.count, row.CollectorExtendedCount)
			require.Equal(t, s.now().Add(5*time.Second), row.NextCollectAt)
			require.NotEmpty(t, row.DemandReason)
		})
	}
}

func TestCodexTurnStateCollectorFailurePrecedesCompletedModelEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		status       int
		err          error
		paused       bool
	}{
		{name: "deadline_after_response", status: 200, err: context.DeadlineExceeded, reason: "collection_timeout"},
		{name: "rate_limit", status: 429, reason: "collector_rate_limited"},
		{name: "auth", status: 401, reason: "collector_auth_rejected", paused: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, repo, account := newCodexStateTestService(t)
			seed := seedCodexStateTestDemand(t, s, account, "gpt-5")
			s.collector = codexStateTestCollector(func(context.Context, CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error) {
				return CodexTurnStateCollectResult{StatusCode: tc.status, completed: true,
					ModelEvidence: CodexModelEvidence{ModelRelation: "different"}}, tc.err
			})
			s.collect(context.Background(), seed.key)
			row, err := repo.Get(context.Background(), seed.key)
			require.NoError(t, err)
			require.Equal(t, tc.reason, row.LastError)
			require.Equal(t, tc.paused, row.CollectorPaused)
			require.Equal(t, s.now(), row.NextCollectAt)
		})
	}
}
