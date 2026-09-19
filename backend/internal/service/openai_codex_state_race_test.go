package service

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type codexStateBusinessCheckRaceRepository struct {
	CodexTurnStateRepository
	beforeFirstReturn func()
	checks            int
}

func (r *codexStateBusinessCheckRaceRepository) HasBusiness(ctx context.Context, key CodexTurnStateKey, now time.Time) (bool, error) {
	active, err := r.CodexTurnStateRepository.HasBusiness(ctx, key, now)
	r.checks++
	if r.checks == 1 && err == nil && !active {
		// A real business request starts after the collector took its empty-lease
		// snapshot, but before it registers its cancellation function.
		r.beforeFirstReturn()
	}
	return active, err
}

func TestCodexTurnStateCollectorColdStartBusinessRegistrationRace(t *testing.T) {
	s, repo, account := newCodexStateTestService(t)
	account.Status, account.Schedulable = StatusActive, true
	ctx := context.Background()
	seed, err := s.Prepare(ctx, account, "gpt-5")
	require.NoError(t, err)
	require.NotNil(t, seed)
	require.NoError(t, s.Finish(ctx, seed, false))
	var calls atomic.Int64
	s.collector = codexStateTestCollector(func(context.Context, CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error) {
		calls.Add(1)
		return CodexTurnStateCollectResult{}, nil
	})
	var natural *CodexTurnStateAttempt
	race := &codexStateBusinessCheckRaceRepository{CodexTurnStateRepository: repo}
	race.beforeFirstReturn = func() {
		natural, err = s.Prepare(ctx, account, "gpt-5")
		require.NoError(t, err)
		require.NotNil(t, natural)
	}
	s.repo = race
	s.collect(ctx, seed.key)
	require.NotNil(t, natural, "test must insert the competing request in the race window")
	require.Zero(t, calls.Load(), "a stale empty-lease check must not start collection after business begins")
	active, err := repo.HasBusiness(ctx, seed.key, s.now())
	require.NoError(t, err)
	require.True(t, active)
	token := codexStateTestToken(10, s.now())
	s.Observe(natural, token)
	require.NoError(t, s.Finish(ctx, natural, true))
	s.collect(ctx, seed.key)
	require.Zero(t, calls.Load(), "the naturally learned target removes the need to collect")
	record, err := repo.Get(ctx, seed.key)
	require.NoError(t, err)
	require.Equal(t, "business", record.Source)
}

func TestCodexTurnStateCollectorSkipsInactiveOwner(t *testing.T) {
	for _, name := range []string{"disabled", "unschedulable", "expired"} {
		t.Run(name, func(t *testing.T) {
			s, _, account := newCodexStateTestService(t)
			account.Status, account.Schedulable = StatusActive, true
			ctx := context.Background()
			attempt, err := s.Prepare(ctx, account, "gpt-5")
			require.NoError(t, err)
			require.NotNil(t, attempt)
			require.NoError(t, s.Finish(ctx, attempt, false))
			switch name {
			case "disabled":
				account.Status = StatusDisabled
			case "unschedulable":
				account.Schedulable = false
			case "expired":
				expired := s.now().Add(-time.Minute)
				account.ExpiresAt = &expired
				account.AutoPauseOnExpired = true
			}
			var calls atomic.Int64
			s.collector = codexStateTestCollector(func(context.Context, CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error) {
				calls.Add(1)
				return CodexTurnStateCollectResult{}, nil
			})
			s.collect(ctx, attempt.key)
			require.Zero(t, calls.Load(), "maintenance must honor the owner's account eligibility")
		})
	}
}
