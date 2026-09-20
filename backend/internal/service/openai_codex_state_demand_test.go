package service

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCodexTurnStateDemandOnlyFromDeliveredMatchingExtended(t *testing.T) {
	for _, name := range []string{"missing", "invalid", "future", "expired", "wrong_type", "failed", "unsent", "unknown_type", "extended"} {
		t.Run(name, func(t *testing.T) {
			s, repo, account := newCodexStateTestService(t)
			if name == "unknown_type" {
				account.Extra[CodexTurnStateExtraKey].(map[string]any)["account_type"] = "auto"
				account.Credentials["plan_type"] = "unknown"
			}
			attempt, err := s.Prepare(context.Background(), account, "gpt-5")
			require.NoError(t, err)
			if name != "unsent" {
				markCodexStateTestBusinessSent(t, s, attempt)
			}
			token := codexStateTestToken(11, s.now())
			switch name {
			case "missing":
				token = ""
			case "invalid":
				token = "invalid"
			case "future":
				token = codexStateTestToken(11, s.now().Add(time.Hour))
			case "expired":
				token = codexStateTestToken(11, s.now().Add(-time.Hour))
			case "wrong_type":
				token = codexStateTestToken(13, s.now())
			}
			s.Observe(attempt, token)
			require.NoError(t, s.Finish(context.Background(), attempt, name != "failed"))
			record, err := repo.Get(context.Background(), attempt.key)
			require.NoError(t, err)
			if name == "extended" {
				require.Equal(t, "extended_shape", record.DemandReason)
				require.False(t, record.DemandAt.IsZero())
			} else {
				require.Empty(t, record.DemandReason)
			}
			var calls atomic.Int64
			s.collector = codexStateTestCollector(func(context.Context, CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error) {
				calls.Add(1)
				return CodexTurnStateCollectResult{}, errors.New("synthetic failure")
			})
			s.collect(context.Background(), attempt.key)
			require.Equal(t, name == "extended", calls.Load() == 1)
		})
	}
}

func TestCodexTurnStateDemandCollectorOutcomeAtomicallyKeepsShapeAndRetry(t *testing.T) {
	for _, name := range []string{"extended", "missing", "network", "proxy", "timeout", "auth", "rate_limited", "target"} {
		t.Run(name, func(t *testing.T) {
			s, repo, account := newCodexStateTestService(t)
			attempt := seedCodexStateTestDemand(t, s, account, "gpt-5")
			before, _ := repo.Get(context.Background(), attempt.key)
			result := CodexTurnStateCollectResult{StatusCode: http.StatusOK}
			var collectErr error
			wantError := "no_target_state"
			switch name {
			case "extended":
				result.Tokens = []string{codexStateTestToken(11, s.now())}
			case "network":
				collectErr, wantError = errors.New("private endpoint secret"), "collection_failed"
			case "proxy":
				collectErr, wantError = ErrCodexTurnStateCollectorProxyUnavailable, "collector_proxy_unavailable"
			case "timeout":
				collectErr, wantError = context.DeadlineExceeded, "collection_timeout"
			case "auth":
				result.StatusCode, wantError = http.StatusUnauthorized, "collector_auth_rejected"
			case "rate_limited":
				result.StatusCode, result.RetryAfter, wantError = http.StatusTooManyRequests, time.Minute, "collector_rate_limited"
			case "target":
				result.Tokens = []string{codexStateTestToken(10, s.now())}
				wantError = ""
			}
			var calls atomic.Int64
			s.collector = codexStateTestCollector(func(context.Context, CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error) {
				calls.Add(1)
				return result, collectErr
			})
			s.collect(context.Background(), attempt.key)
			record, _ := repo.Get(context.Background(), attempt.key)
			require.Equal(t, before.Version+2, record.Version, "one send reservation and one atomic outcome")
			require.Equal(t, s.now(), record.LastCollectedAt)
			require.Equal(t, wantError, record.LastError)
			if name == "target" {
				require.NotEmpty(t, record.EncryptedToken)
				require.Empty(t, record.DemandReason)
				require.True(t, record.NextCollectAt.IsZero())
				require.Equal(t, "idle", record.CollectionStatus)
			} else {
				require.Equal(t, "extended_shape", record.DemandReason)
				retry := time.Duration(0)
				if name == "extended" || name == "missing" {
					retry = 30 * time.Second
				} else if name == "rate_limited" {
					retry = time.Minute
				}
				require.Equal(t, s.now().Add(retry), record.NextCollectAt)
				if name == "auth" {
					require.True(t, record.CollectorPaused)
					require.Equal(t, "paused", record.CollectionStatus)
				} else {
					require.Equal(t, "backoff", record.CollectionStatus)
				}
				if name == "extended" {
					require.Equal(t, CodexTurnStateShapeExtended, record.Shape)
					require.Equal(t, 312, record.TokenLength)
				}
			}
			s.collect(context.Background(), attempt.key)
			if name == "network" || name == "proxy" || name == "timeout" {
				require.EqualValues(t, 2, calls.Load(), "ordinary errors do not impose fixed shape-retry backoff")
			} else {
				require.EqualValues(t, 1, calls.Load(), "fresh target, explicit cooldown, or missing usable state still blocks recollection")
			}
		})
	}
}

func TestCodexTurnStateDemandRenewalAndLeaseHeartbeatUseActualBusinessTime(t *testing.T) {
	s, repo, account := newCodexStateTestService(t)
	ctx := context.Background()
	clock := s.now()
	s.now = func() time.Time { return clock }
	attempt, err := s.Prepare(ctx, account, "gpt-5")
	require.NoError(t, err)
	markCodexStateTestBusinessSent(t, s, attempt)
	token := codexStateTestToken(10, clock.Add(-54*time.Minute))
	s.Observe(attempt, token)
	require.NoError(t, s.Finish(ctx, attempt, true))
	record, _ := repo.Get(ctx, attempt.key)
	require.False(t, s.ensureCodexTurnStateDemand(ctx, record))
	clock = clock.Add(time.Minute)
	record, _ = repo.Get(ctx, attempt.key)
	require.True(t, s.ensureCodexTurnStateDemand(ctx, record))
	require.Equal(t, "expiring", record.DemandReason)
	previousExpiry := record.ExpiresAt
	second, err := s.Prepare(ctx, account, "gpt-5")
	require.NoError(t, err)
	markCodexStateTestBusinessSent(t, s, second)
	s.Observe(second, token)
	require.NoError(t, s.Finish(ctx, second, true))
	record, _ = repo.Get(ctx, second.key)
	require.Equal(t, previousExpiry, record.ExpiresAt)
	require.Equal(t, "expiring", record.DemandReason, "returning the same token cannot satisfy renewal demand")
	long, err := s.Prepare(ctx, account, "gpt-5")
	require.NoError(t, err)
	markCodexStateTestBusinessSent(t, s, long)
	actualSentAt := clock
	clock = clock.Add(CodexTurnStateActiveWindow + time.Second)
	s.scan(ctx)
	record, _ = repo.Get(ctx, long.key)
	require.Equal(t, actualSentAt, record.LastBusinessAt, "a lease heartbeat must not extend real activity")
	require.False(t, s.ensureCodexTurnStateDemand(ctx, record), "idle models pause even while an old request is still open")
	require.NoError(t, s.Finish(ctx, long, false))
}

func TestCodexTurnStateDemandRenewalRetainsValidCacheAndRetriesNearExpiry(t *testing.T) {
	for _, name := range []string{"extended", "near_target", "same_target", "fresh_target"} {
		t.Run(name, func(t *testing.T) {
			s, repo, account := newCodexStateTestService(t)
			ctx := context.Background()
			seed, err := s.Prepare(ctx, account, "gpt-5")
			require.NoError(t, err)
			markCodexStateTestBusinessSent(t, s, seed)
			oldToken := codexStateTestToken(10, s.now().Add(-58*time.Minute))
			s.Observe(seed, oldToken)
			require.NoError(t, s.Finish(ctx, seed, true))
			before, _ := repo.Get(ctx, seed.key)
			responseToken := codexStateTestToken(11, s.now())
			switch name {
			case "near_target":
				responseToken = codexStateTestToken(10, s.now().Add(-56*time.Minute))
			case "same_target":
				responseToken = oldToken
			case "fresh_target":
				responseToken = codexStateTestToken(10, s.now())
			}
			s.collector = codexStateTestCollector(func(context.Context, CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error) {
				return CodexTurnStateCollectResult{StatusCode: 200, Tokens: []string{responseToken}}, nil
			})
			s.collect(ctx, seed.key)
			after, _ := repo.Get(ctx, seed.key)
			require.NotEmpty(t, after.EncryptedToken)
			if name == "extended" || name == "same_target" {
				require.Equal(t, before.EncryptedToken, after.EncryptedToken)
				require.Equal(t, before.ExpiresAt, after.ExpiresAt)
				require.Equal(t, before.Shape, after.Shape)
				require.Equal(t, before.Source, after.Source)
			} else {
				plain, decryptErr := s.encryptor.Decrypt(after.EncryptedToken)
				require.NoError(t, decryptErr)
				require.Equal(t, responseToken, plain)
			}
			if name == "fresh_target" {
				require.Empty(t, after.DemandReason)
				require.Equal(t, "idle", after.CollectionStatus)
			} else {
				require.Equal(t, "expiring", after.DemandReason)
				require.Equal(t, "backoff", after.CollectionStatus)
				require.Equal(t, s.now().Add(30*time.Second), after.NextCollectAt)
			}
		})
	}
}

func TestCodexTurnStateDemandIdleResumeWithoutStateCannotRecreateOldDemand(t *testing.T) {
	s, repo, account := newCodexStateTestService(t)
	seed := seedCodexStateTestDemand(t, s, account, "gpt-5")
	clock := s.now().Add(CodexTurnStateActiveWindow + time.Second)
	s.now = func() time.Time { return clock }
	resumed, err := s.Prepare(context.Background(), account, "gpt-5")
	require.NoError(t, err)
	markCodexStateTestBusinessSent(t, s, resumed)
	require.NoError(t, s.Finish(context.Background(), resumed, true))
	record, err := repo.Get(context.Background(), seed.key)
	require.NoError(t, err)
	require.Empty(t, record.DemandReason)
	var calls atomic.Int64
	s.collector = codexStateTestCollector(func(context.Context, CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error) {
		calls.Add(1)
		return CodexTurnStateCollectResult{}, nil
	})
	s.collect(context.Background(), seed.key)
	require.Zero(t, calls.Load())
}

func TestCodexTurnStateDemandNaturalNearExpiryPreservesRetry(t *testing.T) {
	s, repo, account := newCodexStateTestService(t)
	ctx := context.Background()
	seed, err := s.Prepare(ctx, account, "gpt-5")
	require.NoError(t, err)
	markCodexStateTestBusinessSent(t, s, seed)
	s.Observe(seed, codexStateTestToken(10, s.now().Add(-58*time.Minute)))
	require.NoError(t, s.Finish(ctx, seed, true))
	var calls atomic.Int64
	s.collector = codexStateTestCollector(func(context.Context, CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error) {
		calls.Add(1)
		return CodexTurnStateCollectResult{StatusCode: http.StatusOK}, nil
	})
	s.collect(ctx, seed.key)
	before, err := repo.Get(ctx, seed.key)
	require.NoError(t, err)
	require.Equal(t, s.now().Add(30*time.Second), before.NextCollectAt)
	natural, err := s.Prepare(ctx, account, "gpt-5")
	require.NoError(t, err)
	markCodexStateTestBusinessSent(t, s, natural)
	token := codexStateTestToken(10, s.now().Add(-56*time.Minute))
	s.Observe(natural, token)
	require.NoError(t, s.Finish(ctx, natural, true))
	after, err := repo.Get(ctx, natural.key)
	require.NoError(t, err)
	plain, err := s.encryptor.Decrypt(after.EncryptedToken)
	require.NoError(t, err)
	require.Equal(t, token, plain)
	require.Equal(t, "expiring", after.DemandReason)
	require.Equal(t, before.DemandAt, after.DemandAt)
	require.Equal(t, before.NextCollectAt, after.NextCollectAt)
	require.Equal(t, "backoff", after.CollectionStatus)
	s.collect(ctx, natural.key)
	require.EqualValues(t, 1, calls.Load(), "a natural near-expiry target must not bypass retry pacing")
}

func TestCodexTurnStateDemandRespectsLongestAccountCooldown(t *testing.T) {
	s, repo, account := newCodexStateTestService(t)
	attempt := seedCodexStateTestDemand(t, s, account, "gpt-5")
	short, long := s.now().Add(time.Minute), s.now().Add(5*time.Minute)
	account.RateLimitResetAt, account.OverloadUntil = &short, &long
	var calls atomic.Int64
	s.collector = codexStateTestCollector(func(context.Context, CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error) {
		calls.Add(1)
		return CodexTurnStateCollectResult{}, nil
	})
	s.collect(context.Background(), attempt.key)
	record, err := repo.Get(context.Background(), attempt.key)
	require.NoError(t, err)
	require.Zero(t, calls.Load())
	require.Equal(t, long, record.NextCollectAt)
	require.Equal(t, "account_cooldown", record.CollectionReason)
	require.Equal(t, "backoff", record.CollectionStatus)
}

func TestCodexTurnStateDemandNaturalTargetPreservesOwnerRetryAfter(t *testing.T) {
	s, repo, account := newCodexStateTestService(t)
	first := seedCodexStateTestDemand(t, s, account, "gpt-5")
	second := seedCodexStateTestDemand(t, s, account, "gpt-5-mini")
	var calls atomic.Int64
	s.collector = codexStateTestCollector(func(context.Context, CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error) {
		calls.Add(1)
		return CodexTurnStateCollectResult{StatusCode: http.StatusTooManyRequests, RetryAfter: 5 * time.Minute}, nil
	})
	s.collect(context.Background(), first.key)
	natural, err := s.Prepare(context.Background(), account, "gpt-5")
	require.NoError(t, err)
	markCodexStateTestBusinessSent(t, s, natural)
	s.Observe(natural, codexStateTestToken(10, s.now()))
	require.NoError(t, s.Finish(context.Background(), natural, true))
	row, err := repo.Get(context.Background(), first.key)
	require.NoError(t, err)
	require.NotEmpty(t, row.EncryptedToken)
	require.Empty(t, row.DemandReason)
	require.Equal(t, "collector_rate_limited", row.LastError)
	require.Equal(t, s.now().Add(5*time.Minute), row.NextCollectAt)
	s.collect(context.Background(), second.key)
	require.EqualValues(t, 1, calls.Load(), "a natural target must not erase the credential owner's retry-after fence")
}

func TestCodexTurnStateDemandCollectorPublishesAlongsideRemoteBusinessLease(t *testing.T) {
	s, repo, account := newCodexStateTestService(t)
	seed := seedCodexStateTestDemand(t, s, account, "gpt-5")
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	s.collector = codexStateTestCollector(func(context.Context, CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error) {
		close(started)
		<-release
		return CodexTurnStateCollectResult{StatusCode: 200, Tokens: []string{codexStateTestToken(10, s.now())}}, nil
	})
	go func() { defer close(done); s.collect(context.Background(), seed.key) }()
	<-started
	reserved, err := repo.Get(context.Background(), seed.key)
	require.NoError(t, err)
	other := NewCodexTurnStateService(repo, s.accounts, s.encryptor, nil)
	other.now, other.modelPolicy = s.now, s.modelPolicy
	business, err := other.Prepare(context.Background(), account, "gpt-5")
	require.NoError(t, err)
	markCodexStateTestBusinessSent(t, other, business)
	// A remote instance's in-flight business does not block a valid collector
	// publication, even when no cancellation notification can be exchanged.
	close(release)
	<-done
	current, err := repo.Get(context.Background(), seed.key)
	require.NoError(t, err)
	require.Equal(t, reserved.Version+1, current.Version)
	require.NotEmpty(t, current.EncryptedToken)
	require.Equal(t, "collector", current.Source)
	other.Observe(business, codexStateTestToken(11, s.now()))
	require.NoError(t, other.Finish(context.Background(), business, true))
	after, err := repo.Get(context.Background(), seed.key)
	require.NoError(t, err)
	require.Equal(t, current.EncryptedToken, after.EncryptedToken, "a late anomaly from the older business snapshot cannot revoke the collector's new target")
	require.Equal(t, current.Version, after.Version)
}

type codexStateReservationFenceRepo struct {
	CodexTurnStateRepository
	entered chan struct{}
	release chan struct{}
}

func (r *codexStateReservationFenceRepo) SaveCAS(ctx context.Context, record CodexTurnStateRecord, expected int64) (bool, error) {
	if record.CollectionStatus == "collecting" {
		close(r.entered)
		<-r.release
	}
	return r.CodexTurnStateRepository.SaveCAS(ctx, record, expected)
}

func TestCodexTurnStateDemandReservationAndOutcomeAdvanceAlongsideBusinessLease(t *testing.T) {
	s, repo, account := newCodexStateTestService(t)
	ctx := context.Background()
	seed := seedCodexStateTestDemand(t, s, account, "gpt-5")
	before, err := repo.Get(ctx, seed.key)
	require.NoError(t, err)
	fence := &codexStateReservationFenceRepo{CodexTurnStateRepository: repo, entered: make(chan struct{}), release: make(chan struct{})}
	s.repo = fence
	var calls atomic.Int64
	s.collector = codexStateTestCollector(func(context.Context, CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error) {
		calls.Add(1)
		return CodexTurnStateCollectResult{}, nil
	})
	done := make(chan struct{})
	go func() { defer close(done); s.collect(ctx, seed.key) }()
	<-fence.entered
	other := NewCodexTurnStateService(repo, s.accounts, s.encryptor, nil)
	other.now, other.modelPolicy = s.now, s.modelPolicy
	business, err := other.Prepare(ctx, account, "gpt-5")
	require.NoError(t, err)
	markCodexStateTestBusinessSent(t, other, business)
	close(fence.release)
	<-done
	reserved, err := repo.Get(ctx, seed.key)
	require.NoError(t, err)
	require.Equal(t, before.Version+2, reserved.Version)
	require.EqualValues(t, 1, calls.Load())
	other.Observe(business, codexStateTestToken(10, s.now()))
	require.NoError(t, other.Finish(ctx, business, true))
	accepted, err := repo.Get(ctx, seed.key)
	require.NoError(t, err)
	require.NotEmpty(t, accepted.EncryptedToken, "reservation and failed collection must not prevent a same-issued natural target from replacing the unchanged anomaly")
	require.Equal(t, "business", accepted.Source)
	require.Empty(t, accepted.DemandReason)
}
