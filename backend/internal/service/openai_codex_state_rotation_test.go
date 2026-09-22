package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func newCodexRotationTestService(t *testing.T, accountType string, proxyIDs ...int64) (*CodexTurnStateService, *codexStateMemoryRepo, *Account, *time.Time) {
	t.Helper()
	s, repo, account := newCodexStateTestService(t)
	config := account.Extra[CodexTurnStateExtraKey].(map[string]any)
	config["account_type"] = accountType
	ids := make([]any, len(proxyIDs))
	for i, id := range proxyIDs {
		ids[i] = float64(id)
	}
	config["collector_proxy_ids"] = ids
	delete(config, "collector_proxy_id")
	now := s.now()
	s.now = func() time.Time { return now }
	return s, repo, account, &now
}

func seedCodexRotationTestDemand(t *testing.T, s *CodexTurnStateService, account *Account, model string, blocks int) CodexTurnStateKey {
	t.Helper()
	attempt, err := s.Prepare(context.Background(), account, model)
	require.NoError(t, err)
	require.NotNil(t, attempt)
	markCodexStateTestBusinessSent(t, s, attempt)
	s.Observe(attempt, codexStateTestToken(blocks, s.now()))
	require.NoError(t, s.Finish(context.Background(), attempt, true))
	return attempt.key
}

func codexRotationTestRecord(t *testing.T, repo *codexStateMemoryRepo, key CodexTurnStateKey) *CodexTurnStateRecord {
	t.Helper()
	record, err := repo.Get(context.Background(), key)
	require.NoError(t, err)
	require.NotNil(t, record)
	return record
}

func TestCodexTurnStateRotationCountsOnceAndCyclesAfterThree(t *testing.T) {
	for _, accountType := range []string{"personal", "team_business"} {
		for _, single := range []bool{false, true} {
			name := accountType + "/multiple_proxies"
			if single {
				name = accountType + "/single_proxy"
			}
			t.Run(name, func(t *testing.T) {
				ids := []int64{11, 22}
				if single {
					ids = ids[:1]
				}
				s, repo, account, now := newCodexRotationTestService(t, accountType, ids...)
				blocks := 11
				if accountType == "team_business" {
					blocks = 13
				}
				key := seedCodexRotationTestDemand(t, s, account, "gpt-5", blocks)
				var used []int64
				s.collector = codexStateTestCollector(func(_ context.Context, input CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error) {
					used = append(used, input.ProxyID)
					reserved := codexRotationTestRecord(t, repo, key)
					require.NotEmpty(t, reserved.CollectorAttemptID)
					require.Equal(t, input.ProxyID, reserved.CollectorProxyID)
					require.Equal(t, input.ProxyID, reserved.LastCollectorProxyID)
					token := codexStateTestToken(blocks, *now)
					return CodexTurnStateCollectResult{StatusCode: http.StatusOK, Tokens: []string{token, token}}, nil
				})
				for attempt := 0; attempt < 7; attempt++ {
					s.collect(context.Background(), key)
					require.Len(t, used, attempt+1)
					require.Equal(t, ids[(attempt/3)%len(ids)], used[attempt])
					record := codexRotationTestRecord(t, repo, key)
					require.Equal(t, (attempt+1)%3, record.CollectorExtendedCount, "duplicate candidates count only once per request")
					require.Equal(t, ids[((attempt+1)/3)%len(ids)], record.CollectorProxyID)
					require.Equal(t, used[attempt], record.LastCollectorProxyID)
					require.Empty(t, record.CollectorAttemptID)
					require.Equal(t, now.Add(CodexTurnStateRetryInterval), record.NextCollectAt)
					require.Equal(t, "extended_shape", record.DemandReason)
					require.Empty(t, record.EncryptedToken)
					*now = record.NextCollectAt.Add(-time.Nanosecond)
					s.collect(context.Background(), key)
					require.Len(t, used, attempt+1, "rotation cannot bypass the model's retry fence")
					*now = record.NextCollectAt
				}
			})
		}
	}

}

func TestCodexTurnStateRotationIsPerModelAndSurvivesServiceRestart(t *testing.T) {
	s, repo, account, now := newCodexRotationTestService(t, "personal", 11, 22)
	first := seedCodexRotationTestDemand(t, s, account, "gpt-5", 11)
	second := seedCodexRotationTestDemand(t, s, account, "gpt-5-mini", 11)
	var models []string
	var proxies []int64
	collector := codexStateTestCollector(func(_ context.Context, input CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error) {
		models, proxies = append(models, input.Model), append(proxies, input.ProxyID)
		return CodexTurnStateCollectResult{StatusCode: http.StatusOK, Tokens: []string{codexStateTestToken(11, *now)}}, nil
	})
	s.collector = collector
	for range 2 {
		s.collect(context.Background(), first)
		*now = codexRotationTestRecord(t, repo, first).NextCollectAt
	}
	restarted := NewCodexTurnStateService(repo, s.accounts, s.encryptor, collector)
	restarted.now, restarted.modelPolicy = s.now, s.modelPolicy
	restarted.collect(context.Background(), first)
	require.Equal(t, int64(22), codexRotationTestRecord(t, repo, first).CollectorProxyID)
	restarted.collect(context.Background(), second)
	require.Len(t, models, 4, "another model has its own shape-retry fence")
	restarted.collect(context.Background(), second)
	require.Equal(t, []string{"gpt-5", "gpt-5", "gpt-5", "gpt-5-mini"}, models)
	require.Equal(t, []int64{11, 11, 11, 11}, proxies)
	require.Equal(t, 1, codexRotationTestRecord(t, repo, second).CollectorExtendedCount)
	require.Zero(t, codexRotationTestRecord(t, repo, first).CollectorExtendedCount)
}

func TestCodexTurnStateRotationOtherOutcomesRetainPositionAndCount(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		err     error
		blocks  int
		offset  time.Duration
		invalid bool
	}{
		{name: "timeout", status: 200, err: context.DeadlineExceeded, blocks: 11},
		{name: "transport", err: errCodexTurnStateCollectorTransportFailed, blocks: 11},
		{name: "stream_failure", status: 200, err: errCodexTurnStateCollectorStreamFailed, blocks: 11},
		{name: "no_state", status: 200},
		{name: "invalid_envelope", status: 200, invalid: true},
		{name: "wrong_account_type", status: 200, blocks: 13},
		{name: "expired", status: 200, blocks: 11, offset: -CodexTurnStateLifetime},
		{name: "future", status: 200, blocks: 11, offset: time.Minute},
		{name: "http_rate_limit", status: 429, blocks: 11},
		{name: "sse_rate_limit", status: 200, err: errCodexTurnStateCollectorRateLimited, blocks: 11},
		{name: "unauthorized", status: 401, blocks: 11},
		{name: "forbidden", status: 403, blocks: 11},
		{name: "proxy_auth", status: 407, blocks: 11},
		{name: "upstream_unavailable", status: 503, blocks: 11},
		{name: "missing_http_status", blocks: 11},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, repo, account, now := newCodexRotationTestService(t, "personal", 11, 22)
			key := seedCodexRotationTestDemand(t, s, account, "gpt-5", 11)
			before := codexRotationTestRecord(t, repo, key)
			before.CollectorProxyID, before.CollectorExtendedCount = 22, 2
			repo.records[key] = *before
			s.collector = codexStateTestCollector(func(_ context.Context, input CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error) {
				require.EqualValues(t, 22, input.ProxyID)
				result := CodexTurnStateCollectResult{StatusCode: tc.status}
				if tc.blocks != 0 {
					result.Tokens = []string{codexStateTestToken(tc.blocks, now.Add(tc.offset))}
				}
				if tc.invalid {
					result.Tokens = []string{"not-a-fernet-envelope"}
				}
				if tc.status == 429 || tc.err == errCodexTurnStateCollectorRateLimited {
					result.RetryAfter = 2 * time.Minute
				}
				return result, tc.err
			})
			s.collect(context.Background(), key)
			after := codexRotationTestRecord(t, repo, key)
			require.EqualValues(t, 22, after.CollectorProxyID)
			require.EqualValues(t, 22, after.LastCollectorProxyID)
			require.Equal(t, 2, after.CollectorExtendedCount)
			require.Empty(t, after.CollectorAttemptID)
			require.NotEmpty(t, after.DemandReason)
			if tc.status == 429 || tc.err == errCodexTurnStateCollectorRateLimited {
				require.Equal(t, now.Add(2*time.Minute), after.NextCollectAt)
			}
			if tc.status == 401 || tc.status == 403 {
				require.True(t, after.CollectorPaused)
			}
		})
	}
}

func TestCodexTurnStateRotationTargetWinsAndResetsWithoutChangingProxy(t *testing.T) {
	s, repo, account, now := newCodexRotationTestService(t, "personal", 11, 22)
	key := seedCodexRotationTestDemand(t, s, account, "gpt-5", 11)
	before := codexRotationTestRecord(t, repo, key)
	before.CollectorProxyID, before.CollectorExtendedCount = 22, 2
	repo.records[key] = *before
	target := codexStateTestToken(10, *now)
	s.collector = codexStateTestCollector(func(context.Context, CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error) {
		return CodexTurnStateCollectResult{StatusCode: http.StatusOK, Tokens: []string{codexStateTestToken(11, *now), target}}, nil
	})
	s.collect(context.Background(), key)
	after := codexRotationTestRecord(t, repo, key)
	require.EqualValues(t, 22, after.CollectorProxyID)
	require.Zero(t, after.CollectorExtendedCount)
	require.Empty(t, after.CollectorAttemptID)
	require.Equal(t, "refresh", after.DemandReason)
	require.Equal(t, now.Add(CodexTurnStateCollectInterval), after.NextCollectAt)
	plain, err := s.encryptor.Decrypt(after.EncryptedToken)
	require.NoError(t, err)
	require.Equal(t, target, plain)
}

func TestCodexTurnStateRotationBusinessTargetClearsPendingAttemptEvenForSameToken(t *testing.T) {
	for _, duplicate := range []bool{false, true} {
		name := "new_target"
		if duplicate {
			name = "same_target"
		}
		t.Run(name, func(t *testing.T) {
			s, repo, account, now := newCodexRotationTestService(t, "personal", 11, 22)
			key := seedCodexRotationTestDemand(t, s, account, "gpt-5", 11)
			token := codexStateTestToken(10, *now)
			before := codexRotationTestRecord(t, repo, key)
			before.CollectorProxyID, before.CollectorExtendedCount = 22, 2
			before.CollectorAttemptID = "11111111-1111-4111-8111-111111111111"
			if duplicate {
				shape, err := ParseCodexTurnState(token, "personal", *now)
				require.NoError(t, err)
				before.EncryptedToken, err = s.encryptor.Encrypt(token)
				require.NoError(t, err)
				before.IssuedAt, before.ExpiresAt = shape.IssuedAt, shape.ExpiresAt
				before.Shape, before.TokenLength, before.CipherBlocks = shape.Shape, shape.TokenLength, shape.CipherBlocks
			}
			repo.records[key] = *before
			business, err := s.Prepare(context.Background(), account, key.Model)
			require.NoError(t, err)
			markCodexStateTestBusinessSent(t, s, business)
			s.Observe(business, token)
			require.NoError(t, s.Finish(context.Background(), business, true))
			after := codexRotationTestRecord(t, repo, key)
			require.EqualValues(t, 22, after.CollectorProxyID)
			require.Zero(t, after.CollectorExtendedCount)
			require.Empty(t, after.CollectorAttemptID)
			require.Equal(t, "refresh", after.DemandReason)
			require.Equal(t, now.Add(CodexTurnStateCollectInterval), after.NextCollectAt)
			require.Greater(t, after.Version, before.Version, "even a duplicate token must persist clearing the old attempt and anomaly count")
		})
	}
}

func TestCodexTurnStateRotationSameExpiringTokenDoesNotResetCount(t *testing.T) {
	for _, source := range []string{"business", "collector"} {
		t.Run(source, func(t *testing.T) {
			s, repo, account, now := newCodexRotationTestService(t, "personal", 11, 22)
			key := seedCodexRotationTestDemand(t, s, account, "gpt-5", 11)
			token := codexStateTestToken(10, now.Add(-CodexTurnStateLifetime+20*time.Second))
			shape, err := ParseCodexTurnState(token, "personal", *now)
			require.NoError(t, err)
			before := codexRotationTestRecord(t, repo, key)
			before.EncryptedToken, err = s.encryptor.Encrypt(token)
			require.NoError(t, err)
			before.IssuedAt, before.ExpiresAt = shape.IssuedAt, shape.ExpiresAt
			before.Shape, before.TokenLength, before.CipherBlocks = shape.Shape, shape.TokenLength, shape.CipherBlocks
			before.DemandReason, before.RefreshReason = "expiring", "expiring"
			before.CollectorProxyID, before.CollectorExtendedCount = 22, 2
			repo.records[key] = *before
			if source == "collector" {
				s.collector = codexStateTestCollector(func(context.Context, CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error) {
					return CodexTurnStateCollectResult{StatusCode: http.StatusOK, Tokens: []string{token, codexStateTestToken(11, *now)}}, nil
				})
				s.collect(context.Background(), key)
			} else {
				business, prepareErr := s.Prepare(context.Background(), account, key.Model)
				require.NoError(t, prepareErr)
				markCodexStateTestBusinessSent(t, s, business)
				s.Observe(business, token)
				require.NoError(t, s.Finish(context.Background(), business, true))
			}
			after := codexRotationTestRecord(t, repo, key)
			require.EqualValues(t, 22, after.CollectorProxyID)
			require.Equal(t, 2, after.CollectorExtendedCount)
			require.Equal(t, before.EncryptedToken, after.EncryptedToken)
			require.Equal(t, before.ExpiresAt, after.ExpiresAt)
			require.Equal(t, "expiring", after.DemandReason)
		})
	}
}

func TestCodexTurnStateRotationBusinessAnomalyAndIdlePreserveCount(t *testing.T) {
	s, repo, account, now := newCodexRotationTestService(t, "personal", 11, 22)
	key := seedCodexRotationTestDemand(t, s, account, "gpt-5", 11)
	before := codexRotationTestRecord(t, repo, key)
	before.CollectorProxyID, before.CollectorExtendedCount = 22, 2
	repo.records[key] = *before
	seedCodexRotationTestDemand(t, s, account, "gpt-5", 11)
	afterBusiness := codexRotationTestRecord(t, repo, key)
	require.Equal(t, 2, afterBusiness.CollectorExtendedCount, "business anomalies never count toward collector-proxy rotation")
	afterBusiness.CollectorAttemptID = "11111111-1111-4111-8111-111111111111"
	repo.records[key] = *afterBusiness
	*now = now.Add(CodexTurnStateActiveWindow + time.Second)
	s.collector = codexStateTestCollector(func(context.Context, CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error) {
		t.Fatal("an idle model must not send a probe")
		return CodexTurnStateCollectResult{}, nil
	})
	s.collect(context.Background(), key)
	afterIdle := codexRotationTestRecord(t, repo, key)
	require.Empty(t, afterIdle.DemandReason)
	require.Empty(t, afterIdle.CollectorAttemptID)
	require.EqualValues(t, 22, afterIdle.CollectorProxyID)
	require.Equal(t, 2, afterIdle.CollectorExtendedCount)
}

func TestCodexTurnStateRotationIgnoresDuplicateCompletion(t *testing.T) {
	s, repo, account, now := newCodexRotationTestService(t, "personal", 11, 22)
	key := seedCodexRotationTestDemand(t, s, account, "gpt-5", 11)
	var reservation CodexTurnStateRecord
	result := CodexTurnStateCollectResult{StatusCode: http.StatusOK, Tokens: []string{codexStateTestToken(11, *now)}}
	s.collector = codexStateTestCollector(func(context.Context, CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error) {
		reservation = *codexRotationTestRecord(t, repo, key)
		return result, nil
	})
	s.collect(context.Background(), key)
	after := codexRotationTestRecord(t, repo, key)
	require.Equal(t, 1, after.CollectorExtendedCount)
	require.NotEmpty(t, reservation.CollectorAttemptID)
	s.finishCollectorOutcome(context.Background(), account, key, reservation, reservation.ModelPolicyRevision, result, nil)
	repeated := codexRotationTestRecord(t, repo, key)
	require.Equal(t, after.Version, repeated.Version)
	require.Equal(t, after.CollectorExtendedCount, repeated.CollectorExtendedCount)
	require.Equal(t, after.CollectorProxyID, repeated.CollectorProxyID)
}

func TestCodexTurnStateRotationRejectsOldCompletionAfterNewBusinessDemand(t *testing.T) {
	for _, blocks := range []int{10, 11} {
		name := "old_target"
		if blocks == 11 {
			name = "old_anomaly"
		}
		t.Run(name, func(t *testing.T) {
			s, repo, account, now := newCodexRotationTestService(t, "personal", 11, 22)
			key := seedCodexRotationTestDemand(t, s, account, "gpt-5", 11)
			before := codexRotationTestRecord(t, repo, key)
			before.CollectorProxyID, before.CollectorExtendedCount = 22, 2
			repo.records[key] = *before
			var newDemand CodexTurnStateRecord
			s.collector = codexStateTestCollector(func(context.Context, CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error) {
				other := NewCodexTurnStateService(repo, s.accounts, s.encryptor, nil)
				other.now, other.modelPolicy = s.now, s.modelPolicy
				business, err := other.Prepare(context.Background(), account, key.Model)
				require.NoError(t, err)
				markCodexStateTestBusinessSent(t, other, business)
				other.Observe(business, codexStateTestToken(10, *now))
				require.NoError(t, other.Finish(context.Background(), business, true))
				seedCodexRotationTestDemand(t, other, account, key.Model, 11)
				newDemand = *codexRotationTestRecord(t, repo, key)
				require.Empty(t, newDemand.CollectorAttemptID)
				require.Zero(t, newDemand.CollectorExtendedCount)
				require.NotEmpty(t, newDemand.DemandReason)
				require.Empty(t, newDemand.EncryptedToken)
				return CodexTurnStateCollectResult{StatusCode: http.StatusOK, Tokens: []string{codexStateTestToken(blocks, now.Add(time.Second))}}, nil
			})
			s.collect(context.Background(), key)
			after := codexRotationTestRecord(t, repo, key)
			require.Equal(t, newDemand.Version, after.Version, "the old physical attempt cannot publish into a newly created demand")
			require.Equal(t, newDemand, *after)
		})
	}
}

func TestCodexTurnStateRotationEmptyListOnlyLearnsBusinessTargets(t *testing.T) {
	s, repo, account, now := newCodexRotationTestService(t, "personal")
	// Explicit [] must override a legacy single-value field still present in an
	// older stored account snapshot.
	account.Extra[CodexTurnStateExtraKey].(map[string]any)["collector_proxy_id"] = float64(99)
	key := seedCodexRotationTestDemand(t, s, account, "gpt-5", 11)
	s.collector = codexStateTestCollector(func(context.Context, CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error) {
		t.Fatal("an explicitly empty collector list must not fall back to the old proxy")
		return CodexTurnStateCollectResult{}, nil
	})
	s.collect(context.Background(), key)
	before := codexRotationTestRecord(t, repo, key)
	require.Zero(t, before.CollectorProxyID)
	require.Zero(t, before.CollectorExtendedCount)
	require.Empty(t, before.CollectorAttemptID)
	business, err := s.Prepare(context.Background(), account, key.Model)
	require.NoError(t, err)
	markCodexStateTestBusinessSent(t, s, business)
	token := codexStateTestToken(10, *now)
	s.Observe(business, token)
	require.NoError(t, s.Finish(context.Background(), business, true))
	after := codexRotationTestRecord(t, repo, key)
	plain, err := s.encryptor.Decrypt(after.EncryptedToken)
	require.NoError(t, err)
	require.Equal(t, token, plain)
	require.Equal(t, "refresh", after.DemandReason)
	require.Equal(t, now.Add(CodexTurnStateCollectInterval), after.NextCollectAt)
}

func TestCodexTurnStateRotationCompletionRequiresAttemptProxyAndGeneration(t *testing.T) {
	for _, changed := range []string{"attempt", "proxy", "generation", "proxy_removed"} {
		t.Run(changed, func(t *testing.T) {
			s, repo, account, now := newCodexRotationTestService(t, "personal", 11, 22)
			key := seedCodexRotationTestDemand(t, s, account, "gpt-5", 11)
			var reservation CodexTurnStateRecord
			result := CodexTurnStateCollectResult{StatusCode: http.StatusOK, Tokens: []string{codexStateTestToken(11, *now)}}
			s.collector = codexStateTestCollector(func(context.Context, CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error) {
				reservation = *codexRotationTestRecord(t, repo, key)
				return result, nil
			})
			s.collect(context.Background(), key)
			current := codexRotationTestRecord(t, repo, key)
			current.CollectorAttemptID = reservation.CollectorAttemptID
			switch changed {
			case "attempt":
				current.CollectorAttemptID = "22222222-2222-4222-8222-222222222222"
			case "proxy":
				current.CollectorProxyID = 22
			case "generation":
				account.Extra[CodexTurnStateGenerationExtraKey] = "changed-generation"
			case "proxy_removed":
				account.Extra[CodexTurnStateExtraKey].(map[string]any)["collector_proxy_ids"] = []any{float64(22)}
			}
			repo.records[key] = *current
			s.finishCollectorOutcome(context.Background(), account, key, reservation, reservation.ModelPolicyRevision, result, nil)
			after := codexRotationTestRecord(t, repo, key)
			require.Equal(t, *current, *after, "a stale reservation must not increment or overwrite a different attempt")
		})
	}
}

func TestCodexTurnStateRotationSameValidCollectorTargetCompletesDemand(t *testing.T) {
	s, repo, account, now := newCodexRotationTestService(t, "personal", 11, 22)
	key := seedCodexRotationTestDemand(t, s, account, "gpt-5", 11)
	token := codexStateTestToken(10, *now)
	shape, err := ParseCodexTurnState(token, "personal", *now)
	require.NoError(t, err)
	before := codexRotationTestRecord(t, repo, key)
	before.EncryptedToken, err = s.encryptor.Encrypt(token)
	require.NoError(t, err)
	before.IssuedAt, before.ExpiresAt = shape.IssuedAt, shape.ExpiresAt
	before.Shape, before.TokenLength, before.CipherBlocks = shape.Shape, shape.TokenLength, shape.CipherBlocks
	before.CollectorProxyID, before.CollectorExtendedCount = 22, 2
	repo.records[key] = *before
	s.collector = codexStateTestCollector(func(context.Context, CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error) {
		return CodexTurnStateCollectResult{StatusCode: http.StatusOK, Tokens: []string{token}}, nil
	})
	s.collect(context.Background(), key)
	after := codexRotationTestRecord(t, repo, key)
	require.Equal(t, before.EncryptedToken, after.EncryptedToken)
	require.Equal(t, before.ExpiresAt, after.ExpiresAt)
	require.EqualValues(t, 22, after.CollectorProxyID)
	require.Zero(t, after.CollectorExtendedCount)
	require.Empty(t, after.CollectorAttemptID)
	require.Equal(t, "refresh", after.DemandReason)
	require.Equal(t, now.Add(CodexTurnStateCollectInterval), after.NextCollectAt)
}

func TestCodexTurnStateRotationDelayedCancellationPreservesSuccessor(t *testing.T) {
	for _, phase := range []string{"queued", "running"} {
		t.Run(phase, func(t *testing.T) {
			s, repo, account, now := newCodexRotationTestService(t, "personal", 11, 22)
			key := seedCodexRotationTestDemand(t, s, account, "gpt-5", 11)
			var oldAttemptID string
			s.collector = codexStateTestCollector(func(context.Context, CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error) {
				oldAttemptID = codexRotationTestRecord(t, repo, key).CollectorAttemptID
				return CodexTurnStateCollectResult{StatusCode: http.StatusOK, Tokens: []string{codexStateTestToken(11, *now)}}, nil
			})
			s.collect(context.Background(), key)
			require.NotEmpty(t, oldAttemptID)
			*now = codexRotationTestRecord(t, repo, key).NextCollectAt
			oldNotification := key
			oldNotification.CollectorAttemptID = oldAttemptID
			if phase == "queued" {
				s.ctx = context.Background()
				s.enqueue(context.Background(), key)
				require.True(t, s.queued[key])
				s.cancelCollection(oldNotification)
				require.True(t, s.queued[key], "an old attempt notification cannot remove a queued successor")
				require.Equal(t, key, <-s.queue)
				return
			}
			target := codexStateTestToken(10, *now)
			s.collector = codexStateTestCollector(func(ctx context.Context, _ CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error) {
				current := codexRotationTestRecord(t, repo, key)
				require.NotEqual(t, oldAttemptID, current.CollectorAttemptID)
				s.cancelCollection(oldNotification)
				require.NoError(t, ctx.Err(), "a delayed cancellation may only cancel the physical attempt that produced it")
				return CodexTurnStateCollectResult{StatusCode: http.StatusOK, Tokens: []string{target}}, nil
			})
			s.collect(context.Background(), key)
			after := codexRotationTestRecord(t, repo, key)
			plain, err := s.encryptor.Decrypt(after.EncryptedToken)
			require.NoError(t, err)
			require.Equal(t, target, plain)
			require.Equal(t, "refresh", after.DemandReason)
			require.Equal(t, now.Add(CodexTurnStateCollectInterval), after.NextCollectAt)
			require.Empty(t, after.CollectorAttemptID)
		})
	}
}
