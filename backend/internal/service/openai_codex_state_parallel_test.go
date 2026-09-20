package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type codexStateMicrosecondTimestampRepository struct {
	CodexTurnStateRepository
}

func (r *codexStateMicrosecondTimestampRepository) Get(ctx context.Context, key CodexTurnStateKey) (*CodexTurnStateRecord, error) {
	record, err := r.CodexTurnStateRepository.Get(ctx, key)
	if record != nil {
		record.NextCollectAt = record.NextCollectAt.UTC().Truncate(time.Microsecond)
	}
	return record, err
}

func (r *codexStateMicrosecondTimestampRepository) SaveCAS(ctx context.Context, record CodexTurnStateRecord, expected int64) (bool, error) {
	record.NextCollectAt = record.NextCollectAt.UTC().Truncate(time.Microsecond)
	return r.CodexTurnStateRepository.SaveCAS(ctx, record, expected)
}

func TestCodexTurnStateCollectorRetryReplacesReservationAtPostgresTimestampPrecision(t *testing.T) {
	s, memory, account := newCodexStateTestService(t)
	now := s.now().Add(123456789 * time.Nanosecond)
	s.now = func() time.Time { return now }
	repo := &codexStateMicrosecondTimestampRepository{CodexTurnStateRepository: memory}
	s.repo = repo
	seed := seedCodexStateTestDemand(t, s, account, "gpt-5")
	calls := 0
	s.collector = codexStateTestCollector(func(context.Context, CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error) {
		calls++
		return CodexTurnStateCollectResult{}, errors.New("synthetic immediate failure")
	})
	s.collect(context.Background(), seed.key)
	require.Equal(t, 1, calls)
	after, err := repo.Get(context.Background(), seed.key)
	require.NoError(t, err)
	require.Equal(t, now.UTC().Truncate(time.Microsecond), after.NextCollectAt,
		"PostgreSQL microsecond precision must not make this attempt's 50-second crash reservation look like a concurrent cooldown")
	require.Equal(t, "backoff", after.CollectionStatus)
	require.Equal(t, "collection_failed", after.LastError)
	require.Equal(t, "extended_shape", after.DemandReason)
}

func TestCodexTurnStateParallelCollectorFailureRetainsConcurrentCooldownThroughBusinessSuccess(t *testing.T) {
	for _, marker := range []string{"collector_rate_limited", "account_cooldown"} {
		t.Run(marker, func(t *testing.T) {
			s, repo, account := newCodexStateTestService(t)
			ctx := context.Background()
			seed := seedCodexStateTestDemand(t, s, account, "gpt-5")
			otherModel := seedCodexStateTestDemand(t, s, account, "gpt-5-mini")
			retryAt := s.now().Add(5 * time.Minute)
			calls := 0
			s.collector = codexStateTestCollector(func(ctx context.Context, _ CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error) {
				calls++
				// Commit a competing account-level cooldown after the collector
				// reservation, but before its ordinary network failure is published.
				current, err := repo.Get(ctx, seed.key)
				require.NoError(t, err)
				current.NextCollectAt, current.LastError = retryAt, marker
				current.CollectionStatus, current.CollectionReason = "backoff", marker
				ok, err := repo.SaveCAS(ctx, *current, current.Version)
				require.NoError(t, err)
				require.True(t, ok)
				return CodexTurnStateCollectResult{}, errors.New("synthetic ordinary network failure")
			})
			s.collect(ctx, seed.key)
			require.Equal(t, 1, calls)
			afterFailure, err := repo.Get(ctx, seed.key)
			require.NoError(t, err)
			require.Equal(t, retryAt, afterFailure.NextCollectAt)
			require.Equal(t, marker, afterFailure.LastError, "a rebased ordinary failure must preserve the reason that makes this retry fence account-wide")
			require.Equal(t, "backoff", afterFailure.CollectionStatus)
			require.Equal(t, marker, afterFailure.CollectionReason)
			require.Equal(t, "extended_shape", afterFailure.DemandReason)
			business, err := s.Prepare(ctx, account, "gpt-5")
			require.NoError(t, err)
			markCodexStateTestBusinessSent(t, s, business)
			token := codexStateTestToken(10, s.now().Add(time.Second))
			s.Observe(business, token)
			require.NoError(t, s.Finish(ctx, business, true))
			afterBusiness, err := repo.Get(ctx, seed.key)
			require.NoError(t, err)
			plain, err := s.encryptor.Decrypt(afterBusiness.EncryptedToken)
			require.NoError(t, err)
			require.Equal(t, token, plain)
			require.Equal(t, "business", afterBusiness.Source)
			require.Empty(t, afterBusiness.DemandReason)
			require.Equal(t, retryAt, afterBusiness.NextCollectAt)
			require.Equal(t, marker, afterBusiness.LastError, "natural target success must not erase the concurrent account cooldown")
			require.Equal(t, "backoff", afterBusiness.CollectionStatus)
			require.Equal(t, marker, afterBusiness.CollectionReason)
			s.collect(ctx, otherModel.key)
			require.Equal(t, 1, calls, "the preserved cooldown must continue to block collection for the same owner's other models")
		})
	}
}

type codexStateInvalidationConflictRepository struct {
	CodexTurnStateRepository
	beforeFirstInvalidation func(context.Context)
	invalidationWrites      int
}

type codexStateCollectorPublicationConflictRepository struct {
	CodexTurnStateRepository
	beforeFirstPublication func(context.Context)
	publicationWrites      int
}

func (r *codexStateCollectorPublicationConflictRepository) SaveCAS(ctx context.Context, record CodexTurnStateRecord, expected int64) (bool, error) {
	if record.Source == "collector" && record.Shape == CodexTurnStateShapeTarget && record.EncryptedToken != "" {
		r.publicationWrites++
		if r.beforeFirstPublication != nil {
			compete := r.beforeFirstPublication
			r.beforeFirstPublication = nil
			compete(ctx)
		}
	}
	return r.CodexTurnStateRepository.SaveCAS(ctx, record, expected)
}

func TestCodexTurnStateParallelCollectorPublicationRetriesSchedulingCASConflict(t *testing.T) {
	s, repo, account := newCodexStateTestService(t)
	ctx := context.Background()
	seed := seedCodexStateTestDemand(t, s, account, "gpt-5")
	newToken := codexStateTestToken(10, s.now().Add(time.Second))
	s.collector = codexStateTestCollector(func(context.Context, CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error) {
		return CodexTurnStateCollectResult{StatusCode: 200, Tokens: []string{newToken}}, nil
	})
	retryAt := s.now().Add(2 * time.Minute)
	conflict := &codexStateCollectorPublicationConflictRepository{CodexTurnStateRepository: repo}
	conflict.beforeFirstPublication = func(ctx context.Context) {
		current, err := repo.Get(ctx, seed.key)
		require.NoError(t, err)
		require.Empty(t, current.EncryptedToken)
		current.NextCollectAt = retryAt
		current.LastError = "collector_rate_limited"
		current.CollectionStatus, current.CollectionReason = "backoff", "collector_rate_limited"
		ok, err := repo.SaveCAS(ctx, *current, current.Version)
		require.NoError(t, err)
		require.True(t, ok, "the competing scheduling write must commit immediately before the collector publication CAS")
	}
	s.repo = conflict
	s.collect(ctx, seed.key)
	require.Equal(t, 2, conflict.publicationWrites, "a scheduling-only CAS conflict must retry the accepted collector target")
	after, err := repo.Get(ctx, seed.key)
	require.NoError(t, err)
	plain, err := s.encryptor.Decrypt(after.EncryptedToken)
	require.NoError(t, err)
	require.Equal(t, newToken, plain)
	require.Equal(t, "collector", after.Source)
	require.Empty(t, after.DemandReason)
	require.Equal(t, retryAt, after.NextCollectAt, "recomputing the outcome must retain the concurrent account cooldown")
	require.Equal(t, "collector_rate_limited", after.LastError)
	require.Equal(t, "backoff", after.CollectionStatus)
}

func TestCodexTurnStateParallelCollectorPublicationPreservesBusinessTargetAfterCASConflict(t *testing.T) {
	s, repo, account := newCodexStateTestService(t)
	ctx := context.Background()
	seed := seedCodexStateTestDemand(t, s, account, "gpt-5")
	collectedToken := codexStateTestToken(10, s.now().Add(time.Second))
	s.collector = codexStateTestCollector(func(context.Context, CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error) {
		return CodexTurnStateCollectResult{StatusCode: 200, Tokens: []string{collectedToken}}, nil
	})
	businessToken := codexStateTestToken(10, s.now().Add(2*time.Second))
	var accepted *CodexTurnStateRecord
	conflict := &codexStateCollectorPublicationConflictRepository{CodexTurnStateRepository: repo}
	conflict.beforeFirstPublication = func(ctx context.Context) {
		other := NewCodexTurnStateService(repo, s.accounts, s.encryptor, nil)
		other.now, other.modelPolicy = s.now, s.modelPolicy
		business, err := other.Prepare(ctx, account, "gpt-5")
		require.NoError(t, err)
		markCodexStateTestBusinessSent(t, other, business)
		other.Observe(business, businessToken)
		require.NoError(t, other.Finish(ctx, business, true))
		accepted, err = repo.Get(ctx, seed.key)
		require.NoError(t, err)
	}
	s.repo = conflict
	s.collect(ctx, seed.key)
	require.Equal(t, 1, conflict.publicationWrites, "the CAS retry must not submit a second write over the winning business target")
	require.NotNil(t, accepted)
	after, err := repo.Get(ctx, seed.key)
	require.NoError(t, err)
	require.Equal(t, accepted.Version, after.Version)
	require.Equal(t, accepted.EncryptedToken, after.EncryptedToken)
	plain, err := s.encryptor.Decrypt(after.EncryptedToken)
	require.NoError(t, err)
	require.Equal(t, businessToken, plain)
	require.Equal(t, "business", after.Source)
	require.Empty(t, after.DemandReason)
}

func (r *codexStateInvalidationConflictRepository) SaveCAS(ctx context.Context, record CodexTurnStateRecord, expected int64) (bool, error) {
	if record.Shape == CodexTurnStateShapeExtended && record.EncryptedToken == "" && record.DemandReason == "extended_shape" {
		r.invalidationWrites++
		if r.beforeFirstInvalidation != nil {
			compete := r.beforeFirstInvalidation
			r.beforeFirstInvalidation = nil
			compete(ctx)
		}
	}
	return r.CodexTurnStateRepository.SaveCAS(ctx, record, expected)
}

func TestCodexTurnStateParallelInvalidationRetriesSchedulingCASConflict(t *testing.T) {
	s, repo, account := newCodexStateTestService(t)
	ctx := context.Background()
	seed, err := s.Prepare(ctx, account, "gpt-5")
	require.NoError(t, err)
	markCodexStateTestBusinessSent(t, s, seed)
	oldToken := codexStateTestToken(10, s.now().Add(-time.Minute))
	s.Observe(seed, oldToken)
	require.NoError(t, s.Finish(ctx, seed, true))
	business, err := s.Prepare(ctx, account, "gpt-5")
	require.NoError(t, err)
	markCodexStateTestBusinessSent(t, s, business)
	require.Equal(t, oldToken, business.Snapshot.Token)
	retryAt := s.now().Add(2 * time.Minute)
	conflict := &codexStateInvalidationConflictRepository{CodexTurnStateRepository: repo}
	conflict.beforeFirstInvalidation = func(ctx context.Context) {
		current, readErr := repo.Get(ctx, business.key)
		require.NoError(t, readErr)
		current.NextCollectAt, current.LastCollectedAt = retryAt, s.now()
		current.LastError = "collector_rate_limited"
		current.CollectionStatus, current.CollectionReason = "backoff", "collector_rate_limited"
		ok, saveErr := repo.SaveCAS(ctx, *current, current.Version)
		require.NoError(t, saveErr)
		require.True(t, ok, "the competing collector must commit before the invalidation's first CAS")
	}
	s.repo = conflict
	s.Observe(business, codexStateTestToken(11, s.now()))
	require.NoError(t, s.Finish(ctx, business, true))
	require.Equal(t, 2, conflict.invalidationWrites, "a scheduling-only conflict must retry instead of losing the anomaly")
	after, err := repo.Get(ctx, business.key)
	require.NoError(t, err)
	require.Empty(t, after.EncryptedToken)
	require.Equal(t, CodexTurnStateShapeExtended, after.Shape)
	require.Equal(t, "extended_shape", after.DemandReason)
	require.Equal(t, business.SafeObservation().ObservedAt, after.DemandAt)
	require.Equal(t, retryAt, after.NextCollectAt, "the invalidation retry must retain the concurrent cooldown")
	require.Equal(t, "collector_rate_limited", after.LastError)
	require.Equal(t, s.now(), after.LastCollectedAt)
}

func TestCodexTurnStateParallelInvalidationPreservesNewTargetAfterCASConflict(t *testing.T) {
	s, repo, account := newCodexStateTestService(t)
	ctx := context.Background()
	seed, err := s.Prepare(ctx, account, "gpt-5")
	require.NoError(t, err)
	markCodexStateTestBusinessSent(t, s, seed)
	oldToken := codexStateTestToken(10, s.now().Add(-time.Minute))
	s.Observe(seed, oldToken)
	require.NoError(t, s.Finish(ctx, seed, true))
	business, err := s.Prepare(ctx, account, "gpt-5")
	require.NoError(t, err)
	markCodexStateTestBusinessSent(t, s, business)
	require.Equal(t, oldToken, business.Snapshot.Token)
	newToken := codexStateTestToken(10, s.now())
	var accepted *CodexTurnStateRecord
	conflict := &codexStateInvalidationConflictRepository{CodexTurnStateRepository: repo}
	conflict.beforeFirstInvalidation = func(ctx context.Context) {
		other := NewCodexTurnStateService(repo, s.accounts, s.encryptor, nil)
		other.now, other.modelPolicy = s.now, s.modelPolicy
		fresh, prepareErr := other.Prepare(ctx, account, "gpt-5")
		require.NoError(t, prepareErr)
		markCodexStateTestBusinessSent(t, other, fresh)
		other.Observe(fresh, newToken)
		require.NoError(t, other.Finish(ctx, fresh, true))
		var readErr error
		accepted, readErr = repo.Get(ctx, business.key)
		require.NoError(t, readErr)
		require.NotEqual(t, business.Snapshot.Version, accepted.Version)
	}
	s.repo = conflict
	s.Observe(business, codexStateTestToken(11, s.now()))
	require.NoError(t, s.Finish(ctx, business, true))
	require.Equal(t, 1, conflict.invalidationWrites, "the retry must recheck content and stop before invalidating the replacement target")
	require.NotNil(t, accepted)
	after, err := repo.Get(ctx, business.key)
	require.NoError(t, err)
	require.Equal(t, accepted.Version, after.Version)
	require.Equal(t, accepted.EncryptedToken, after.EncryptedToken)
	plain, err := s.encryptor.Decrypt(after.EncryptedToken)
	require.NoError(t, err)
	require.Equal(t, newToken, plain)
	require.Equal(t, CodexTurnStateShapeTarget, after.Shape)
	require.Equal(t, "business", after.Source)
	require.Empty(t, after.DemandReason, "a late anomaly must not create demand for the newly accepted target")
}

func TestCodexTurnStateParallelCollectorMetadataDoesNotInvalidateBusinessSnapshot(t *testing.T) {
	s, repo, account := newCodexStateTestService(t)
	ctx := context.Background()
	seed, err := s.Prepare(ctx, account, "gpt-5")
	require.NoError(t, err)
	markCodexStateTestBusinessSent(t, s, seed)
	oldToken := codexStateTestToken(10, s.now().Add(-58*time.Minute))
	s.Observe(seed, oldToken)
	require.NoError(t, s.Finish(ctx, seed, true))
	business, err := s.Prepare(ctx, account, "gpt-5")
	require.NoError(t, err)
	markCodexStateTestBusinessSent(t, s, business)
	require.Equal(t, oldToken, business.Snapshot.Token)
	before, err := repo.Get(ctx, business.key)
	require.NoError(t, err)
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	s.collector = codexStateTestCollector(func(context.Context, CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error) {
		close(started)
		<-release
		return CodexTurnStateCollectResult{StatusCode: 200, Tokens: []string{codexStateTestToken(11, s.now())}}, nil
	})
	go func() { defer close(done); s.collect(ctx, seed.key) }()
	select {
	case <-started:
	case <-done:
		t.Fatal("renewal must start while business is in flight")
	}
	reserved, err := repo.Get(ctx, business.key)
	require.NoError(t, err)
	require.Greater(t, reserved.Version, before.Version)
	require.True(t, s.ValidateAttempt(ctx, business), "a collection reservation changes scheduling, not the frozen cache contents")
	close(release)
	<-done
	afterCollection, err := repo.Get(ctx, business.key)
	require.NoError(t, err)
	require.Greater(t, afterCollection.Version, reserved.Version)
	require.Equal(t, before.EncryptedToken, afterCollection.EncryptedToken)
	require.Equal(t, before.ExpiresAt, afterCollection.ExpiresAt)
	require.Equal(t, "backoff", afterCollection.CollectionStatus)
	require.True(t, s.ValidateAttempt(ctx, business), "a non-target renewal result must not disable the still-valid frozen cache")
	s.Observe(business, codexStateTestToken(11, s.now()))
	require.NoError(t, s.Finish(ctx, business, true))
	afterBusiness, err := repo.Get(ctx, business.key)
	require.NoError(t, err)
	require.Empty(t, afterBusiness.EncryptedToken, "a delivered anomaly must revoke its unchanged old cache across scheduling-only version changes")
	require.Equal(t, "extended_shape", afterBusiness.DemandReason)
	require.Equal(t, afterCollection.NextCollectAt, afterBusiness.NextCollectAt, "an anomaly must preserve the collector's retry fence")
	require.False(t, s.ValidateAttempt(ctx, business), "a frozen token cannot be injected after its cache was revoked")
}

func TestCodexTurnStateParallelDuplicateNearExpiryBusinessTargetDoesNotCancelRenewal(t *testing.T) {
	s, repo, account := newCodexStateTestService(t)
	ctx := context.Background()
	seed, err := s.Prepare(ctx, account, "gpt-5")
	require.NoError(t, err)
	markCodexStateTestBusinessSent(t, s, seed)
	oldToken := codexStateTestToken(10, s.now().Add(-58*time.Minute))
	s.Observe(seed, oldToken)
	require.NoError(t, s.Finish(ctx, seed, true))
	before, err := repo.Get(ctx, seed.key)
	require.NoError(t, err)
	started := make(chan context.Context, 1)
	release, done := make(chan struct{}), make(chan struct{})
	newToken := codexStateTestToken(10, s.now())
	s.collector = codexStateTestCollector(func(probeCtx context.Context, _ CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error) {
		started <- probeCtx
		select {
		case <-release:
		case <-probeCtx.Done():
		}
		return CodexTurnStateCollectResult{StatusCode: 200, Tokens: []string{newToken}}, nil
	})
	go func() { defer close(done); s.collect(ctx, seed.key) }()
	probeCtx := <-started
	business, err := s.Prepare(ctx, account, "gpt-5")
	require.NoError(t, err)
	markCodexStateTestBusinessSent(t, s, business)
	s.Observe(business, oldToken)
	require.NoError(t, s.Finish(ctx, business, true))
	require.NoError(t, probeCtx.Err(), "a duplicate expiring token must not cancel the active renewal")
	afterBusiness, err := repo.Get(ctx, seed.key)
	require.NoError(t, err)
	require.Equal(t, before.ExpiresAt, afterBusiness.ExpiresAt)
	require.Equal(t, "expiring", afterBusiness.DemandReason)
	close(release)
	<-done
	after, err := repo.Get(ctx, seed.key)
	require.NoError(t, err)
	plain, err := s.encryptor.Decrypt(after.EncryptedToken)
	require.NoError(t, err)
	require.Equal(t, newToken, plain)
	require.Equal(t, "collector", after.Source)
	require.Empty(t, after.DemandReason)
}

func TestCodexTurnStateParallelLateCollectorCannotOverwriteRemoteBusinessSuccess(t *testing.T) {
	s, repo, account := newCodexStateTestService(t)
	ctx := context.Background()
	seed := seedCodexStateTestDemand(t, s, account, "gpt-5")
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	s.collector = codexStateTestCollector(func(context.Context, CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error) {
		close(started)
		<-release
		return CodexTurnStateCollectResult{StatusCode: 200, Tokens: []string{codexStateTestToken(10, s.now())}}, nil
	})
	go func() { defer close(done); s.collect(ctx, seed.key) }()
	<-started
	other := NewCodexTurnStateService(repo, s.accounts, s.encryptor, nil)
	other.now, other.modelPolicy = s.now, s.modelPolicy
	business, err := other.Prepare(ctx, account, "gpt-5")
	require.NoError(t, err)
	markCodexStateTestBusinessSent(t, other, business)
	newToken := codexStateTestToken(10, s.now().Add(time.Second))
	other.Observe(business, newToken)
	require.NoError(t, other.Finish(ctx, business, true))
	accepted, err := repo.Get(ctx, seed.key)
	require.NoError(t, err)
	close(release)
	<-done
	after, err := repo.Get(ctx, seed.key)
	require.NoError(t, err)
	require.Equal(t, accepted.Version, after.Version, "CAS still protects business success when remote cancellation notifications are lost")
	plain, err := s.encryptor.Decrypt(after.EncryptedToken)
	require.NoError(t, err)
	require.Equal(t, newToken, plain)
	require.Equal(t, "business", after.Source)
	require.Empty(t, after.DemandReason)
}

func TestCodexTurnStateParallelCollectorTargetSurvivesRepeatedBusinessAnomaly(t *testing.T) {
	for _, tc := range []struct {
		name        string
		issuedAfter time.Duration
	}{
		{name: "same_issued_anomaly"},
		{name: "newer_issued_anomaly", issuedAfter: time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, repo, account := newCodexStateTestService(t)
			ctx := context.Background()
			seed := seedCodexStateTestDemand(t, s, account, "gpt-5")
			started := make(chan context.Context, 1)
			release, done := make(chan struct{}), make(chan struct{})
			newToken := codexStateTestToken(10, s.now().Add(2*time.Second))
			s.collector = codexStateTestCollector(func(probeCtx context.Context, _ CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error) {
				started <- probeCtx
				<-release
				return CodexTurnStateCollectResult{StatusCode: 200, Tokens: []string{newToken}}, nil
			})
			go func() { defer close(done); s.collect(ctx, seed.key) }()
			probeCtx := <-started
			reserved, err := repo.Get(ctx, seed.key)
			require.NoError(t, err)
			business, err := s.Prepare(ctx, account, "gpt-5")
			require.NoError(t, err)
			markCodexStateTestBusinessSent(t, s, business)
			s.Observe(business, codexStateTestToken(11, s.now().Add(tc.issuedAfter)))
			require.NoError(t, s.Finish(ctx, business, true))
			require.NoError(t, probeCtx.Err(), "a repeated business anomaly must not cancel collection")
			afterBusiness, err := repo.Get(ctx, seed.key)
			require.NoError(t, err)
			require.Greater(t, afterBusiness.Version, reserved.Version)
			require.Empty(t, afterBusiness.EncryptedToken)
			require.Equal(t, "extended_shape", afterBusiness.DemandReason)
			require.Equal(t, "collecting", afterBusiness.CollectionStatus, "an anomaly must not hide the independent collection still in flight")
			close(release)
			<-done
			after, err := repo.Get(ctx, seed.key)
			require.NoError(t, err)
			plain, err := s.encryptor.Decrypt(after.EncryptedToken)
			require.NoError(t, err)
			require.Equal(t, newToken, plain, "a concurrent repeated anomaly cannot discard a newer target collected for the still-pending demand")
			require.Greater(t, after.Version, afterBusiness.Version)
			require.Equal(t, "collector", after.Source)
			require.Empty(t, after.DemandReason)
			require.Equal(t, "idle", after.CollectionStatus)
		})
	}
}
