package service

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCodexTurnStateCollectorRunsWithBusinessBeforeAndAfterStart(t *testing.T) {
	for _, businessFirst := range []bool{true, false} {
		t.Run(map[bool]string{true: "business_first", false: "collector_first"}[businessFirst], func(t *testing.T) {
			s, repo, account := newCodexStateTestService(t)
			ctx := context.Background()
			seed := seedCodexStateTestDemand(t, s, account, "gpt-5")
			var natural *CodexTurnStateAttempt
			startBusiness := func() {
				var err error
				natural, err = s.Prepare(ctx, account, "gpt-5")
				require.NoError(t, err)
				markCodexStateTestBusinessSent(t, s, natural)
			}
			if businessFirst {
				startBusiness()
			}
			started := make(chan context.Context, 1)
			release, done := make(chan struct{}), make(chan struct{})
			token := codexStateTestToken(10, s.now())
			s.collector = codexStateTestCollector(func(probeCtx context.Context, _ CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error) {
				started <- probeCtx
				select {
				case <-release:
				case <-probeCtx.Done():
				}
				return CodexTurnStateCollectResult{StatusCode: 200, Tokens: []string{token}}, nil
			})
			go func() { defer close(done); s.collect(ctx, seed.key) }()
			var probeCtx context.Context
			select {
			case probeCtx = <-started:
			case <-done:
				t.Fatal("collector must start even while business is in flight")
			case <-time.After(3 * time.Second):
				t.Fatal("collector did not start")
			}
			if !businessFirst {
				startBusiness()
			}
			// Exercise the one-second owner watcher as well as Prepare's immediate
			// path: neither may cancel solely because a business lease exists.
			select {
			case <-probeCtx.Done():
				t.Fatalf("business request cancelled collection: %v", probeCtx.Err())
			case <-time.After(1200 * time.Millisecond):
			}
			active, err := repo.HasBusiness(ctx, seed.key, s.now())
			require.NoError(t, err)
			require.True(t, active)
			close(release)
			<-done
			record, err := repo.Get(ctx, seed.key)
			require.NoError(t, err)
			plain, err := s.encryptor.Decrypt(record.EncryptedToken)
			require.NoError(t, err)
			require.Equal(t, token, plain)
			require.Equal(t, "collector", record.Source)
			require.Empty(t, record.DemandReason)
			require.NoError(t, s.Finish(ctx, natural, true))
		})
	}
}

func TestCodexTurnStateCollectorSkipsInactiveOwner(t *testing.T) {
	for _, name := range []string{"disabled", "unschedulable", "expired"} {
		t.Run(name, func(t *testing.T) {
			s, _, account := newCodexStateTestService(t)
			account.Status, account.Schedulable = StatusActive, true
			ctx := context.Background()
			attempt := seedCodexStateTestDemand(t, s, account, "gpt-5")
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
