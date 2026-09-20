package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func newCodexProxyChangedTestService(t *testing.T) (*CodexTurnStateService, *codexStateMemoryRepo, *Account, CodexTurnStateKey, string) {
	t.Helper()
	s, repo, account := newCodexStateTestService(t)
	account.Extra[CodexTurnStateExtraKey].(map[string]any)["collector_proxy_id"] = float64(99)
	account.Extra[CodexTurnStateGenerationExtraKey] = "after-proxy-change"
	key := CodexTurnStateKey{OwnerAccountID: account.ID, Model: "gpt-5", Generation: "after-proxy-change"}
	token := codexStateTestToken(10, s.now().Add(-58*time.Minute))
	shape, err := ParseCodexTurnState(token, "personal", s.now())
	require.NoError(t, err)
	encrypted, err := s.encryptor.Encrypt(token)
	require.NoError(t, err)
	repo.records[key] = CodexTurnStateRecord{
		OwnerAccountID: key.OwnerAccountID, Model: key.Model, Generation: key.Generation, Version: 7,
		EncryptedToken: encrypted, IssuedAt: shape.IssuedAt, ExpiresAt: shape.ExpiresAt,
		Shape: shape.Shape, TokenLength: shape.TokenLength, CipherBlocks: shape.CipherBlocks, Source: "business",
		LastBusinessAt: s.now(), LastCollectedAt: s.now().Add(-time.Minute),
		CollectionStatus: "idle", CollectionReason: "collector_proxy_changed",
	}
	return s, repo, account, key, token
}

func TestCodexTurnStateProxyChangeDefersValidCacheUntilExpiration(t *testing.T) {
	for _, legacyDemand := range []string{"", "extended_shape"} {
		t.Run("previous_demand_"+legacyDemand, func(t *testing.T) {
			s, repo, account, key, _ := newCodexProxyChangedTestService(t)
			ctx := context.Background()
			s.ctx = ctx
			clock := s.now()
			s.now = func() time.Time { return clock }
			before := repo.records[key]
			before.DemandReason = legacyDemand
			repo.records[key] = before
			calls := 0
			s.collector = codexStateTestCollector(func(_ context.Context, input CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error) {
				calls++
				require.EqualValues(t, 99, input.ProxyID)
				return CodexTurnStateCollectResult{StatusCode: http.StatusOK, Tokens: []string{codexStateTestToken(10, clock)}}, nil
			})
			record, err := repo.Get(ctx, key)
			require.NoError(t, err)
			require.False(t, s.ensureCodexTurnStateDemand(ctx, record), "proxy change defers even a prior demand while the retained cache is valid")
			s.pumpDue(ctx)
			require.Empty(t, s.queue)
			s.collect(ctx, key)
			require.Zero(t, calls)
			after, err := repo.Get(ctx, key)
			require.NoError(t, err)
			require.Equal(t, before.EncryptedToken, after.EncryptedToken)
			require.Equal(t, before.ExpiresAt, after.ExpiresAt)
			require.True(t, after.NextCollectAt.IsZero(), "cache expiry must not become an owner-wide collection cooldown")
			status := projectCodexTurnStateStatus(account.ID, account, []CodexTurnStateRecord{*after}, []string{key.Model}, nil, clock)
			require.Equal(t, "idle", status.Models[0].CollectionStatus)
			require.Equal(t, "collector_proxy_changed", status.Models[0].CollectionReason)
			require.True(t, status.Models[0].CacheAvailable)

			clock = before.ExpiresAt
			s.pumpDue(ctx)
			require.Len(t, s.queue, 1)
			require.Equal(t, key, <-s.queue)
			s.collect(ctx, key)
			require.Equal(t, 1, calls)
			after, err = repo.Get(ctx, key)
			require.NoError(t, err)
			require.Equal(t, "collector", after.Source)
			require.Equal(t, clock.Add(CodexTurnStateLifetime), after.ExpiresAt)
			require.Empty(t, after.DemandReason)
			require.NotEqual(t, "collector_proxy_changed", after.CollectionReason)
		})
	}
}

func TestCodexTurnStateProxyChangeDoesNotDelayAnotherModel(t *testing.T) {
	s, repo, account, key, _ := newCodexProxyChangedTestService(t)
	ctx := context.Background()
	other := seedCodexStateTestDemand(t, s, account, "gpt-5-mini")
	calls := 0
	s.collector = codexStateTestCollector(func(_ context.Context, input CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error) {
		calls++
		require.Equal(t, other.Model, input.Model)
		require.EqualValues(t, 99, input.ProxyID)
		return CodexTurnStateCollectResult{StatusCode: http.StatusOK, Tokens: []string{codexStateTestToken(10, s.now())}}, nil
	})
	s.collect(ctx, key)
	s.collect(ctx, other.key)
	require.Equal(t, 1, calls)
	retained, err := repo.Get(ctx, key)
	require.NoError(t, err)
	require.Equal(t, "collector_proxy_changed", retained.CollectionReason)
	require.True(t, retained.NextCollectAt.IsZero())
	collected, err := repo.Get(ctx, other.key)
	require.NoError(t, err)
	require.NotEmpty(t, collected.EncryptedToken)
	require.Empty(t, collected.DemandReason)
}

func TestCodexTurnStateProxyChangeBusinessDuplicateAndNewTarget(t *testing.T) {
	s, repo, account, key, previousToken := newCodexProxyChangedTestService(t)
	ctx := context.Background()
	before := repo.records[key]
	duplicate, err := s.Prepare(ctx, account, key.Model)
	require.NoError(t, err)
	require.Equal(t, previousToken, duplicate.Snapshot.Token)
	markCodexStateTestBusinessSent(t, s, duplicate)
	s.Observe(duplicate, previousToken)
	require.NoError(t, s.Finish(ctx, duplicate, true))
	repeated, err := repo.Get(ctx, key)
	require.NoError(t, err)
	require.Equal(t, before.EncryptedToken, repeated.EncryptedToken)
	require.Equal(t, before.ExpiresAt, repeated.ExpiresAt)
	require.Equal(t, "collector_proxy_changed", repeated.CollectionReason)
	require.False(t, s.ensureCodexTurnStateDemand(ctx, repeated))

	newTarget := codexStateTestToken(10, s.now().Add(-56*time.Minute))
	natural, err := s.Prepare(ctx, account, key.Model)
	require.NoError(t, err)
	markCodexStateTestBusinessSent(t, s, natural)
	s.Observe(natural, newTarget)
	require.NoError(t, s.Finish(ctx, natural, true))
	updated, err := repo.Get(ctx, key)
	require.NoError(t, err)
	plain, err := s.encryptor.Decrypt(updated.EncryptedToken)
	require.NoError(t, err)
	require.Equal(t, newTarget, plain)
	require.Equal(t, "expiring", updated.DemandReason, "a new target resumes the normal five-minute renewal policy")
	require.NotEqual(t, "collector_proxy_changed", updated.CollectionReason)
	require.True(t, s.ensureCodexTurnStateDemand(ctx, updated))
}

func TestCodexTurnStateProxyChangeBusinessAnomalyInvalidatesImmediately(t *testing.T) {
	s, repo, account, key, _ := newCodexProxyChangedTestService(t)
	ctx := context.Background()
	anomaly := seedCodexStateTestDemand(t, s, account, key.Model)
	require.Equal(t, key, anomaly.key)
	record, err := repo.Get(ctx, key)
	require.NoError(t, err)
	require.Empty(t, record.EncryptedToken)
	require.Equal(t, "extended_shape", record.DemandReason)
	require.NotEqual(t, "collector_proxy_changed", record.CollectionReason)
	calls := 0
	s.collector = codexStateTestCollector(func(_ context.Context, input CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error) {
		calls++
		require.EqualValues(t, 99, input.ProxyID)
		return CodexTurnStateCollectResult{StatusCode: http.StatusOK, Tokens: []string{codexStateTestToken(10, s.now())}}, nil
	})
	s.collect(ctx, key)
	require.Equal(t, 1, calls)
}

func TestCodexTurnStateProxyChangeOnlyDefersMatchingValidCache(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*CodexTurnStateRecord, time.Time)
	}{
		{"extended", func(r *CodexTurnStateRecord, _ time.Time) {
			r.Shape, r.TokenLength, r.CipherBlocks = CodexTurnStateShapeExtended, 312, 11
		}},
		{"expired", func(r *CodexTurnStateRecord, now time.Time) { r.ExpiresAt = now }},
		{"missing_token", func(r *CodexTurnStateRecord, _ time.Time) { r.EncryptedToken = "" }},
		{"invalid_length", func(r *CodexTurnStateRecord, _ time.Time) { r.TokenLength = 313 }},
		{"wrong_blocks", func(r *CodexTurnStateRecord, _ time.Time) { r.CipherBlocks = 11 }},
		{"missing_issued_at", func(r *CodexTurnStateRecord, _ time.Time) { r.IssuedAt = time.Time{} }},
		{"future_issued_at", func(r *CodexTurnStateRecord, now time.Time) { r.IssuedAt = now.Add(time.Minute) }},
		{"wrong_lifetime", func(r *CodexTurnStateRecord, _ time.Time) { r.ExpiresAt = r.ExpiresAt.Add(time.Minute) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, repo, _, key, _ := newCodexProxyChangedTestService(t)
			record := repo.records[key]
			record.DemandReason = "extended_shape"
			tc.mutate(&record, s.now())
			repo.records[key] = record
			require.True(t, s.ensureCodexTurnStateDemand(context.Background(), &record), "an invalid retained cache must not suppress existing demand")
			calls := 0
			s.collector = codexStateTestCollector(func(context.Context, CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error) {
				calls++
				return CodexTurnStateCollectResult{StatusCode: http.StatusOK}, nil
			})
			s.collect(context.Background(), key)
			require.Equal(t, 1, calls)
		})
	}
}

func TestCodexTurnStateProxyChangeDefersValidTeamCache(t *testing.T) {
	s, repo, account, key, _ := newCodexProxyChangedTestService(t)
	account.Extra[CodexTurnStateExtraKey].(map[string]any)["account_type"] = "team_business"
	record := repo.records[key]
	token := codexStateTestToken(12, record.IssuedAt)
	encrypted, err := s.encryptor.Encrypt(token)
	require.NoError(t, err)
	record.EncryptedToken, record.TokenLength, record.CipherBlocks = encrypted, 332, 12
	repo.records[key] = record
	calls := 0
	s.collector = codexStateTestCollector(func(context.Context, CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error) {
		calls++
		return CodexTurnStateCollectResult{StatusCode: http.StatusOK}, nil
	})
	require.False(t, s.ensureCodexTurnStateDemand(context.Background(), &record))
	s.collect(context.Background(), key)
	require.Zero(t, calls)
	status := projectCodexTurnStateStatus(account.ID, account, []CodexTurnStateRecord{record}, []string{key.Model}, nil, s.now())
	require.True(t, status.Models[0].CacheAvailable)
	require.Equal(t, "idle", status.Models[0].CollectionStatus)
	require.Equal(t, "collector_proxy_changed", status.Models[0].CollectionReason)
}

func TestCodexTurnStateProxyChangeKeepsActivityAndProxyRequirements(t *testing.T) {
	for _, mode := range []string{"never_active", "idle", "no_proxy"} {
		t.Run(mode, func(t *testing.T) {
			s, repo, account, key, _ := newCodexProxyChangedTestService(t)
			ctx := context.Background()
			record := repo.records[key]
			record.ExpiresAt = s.now()
			record.DemandReason = "extended_shape"
			switch mode {
			case "never_active":
				record.LastBusinessAt = time.Unix(0, 0)
			case "idle":
				record.LastBusinessAt = s.now().Add(-CodexTurnStateActiveWindow - time.Second)
			case "no_proxy":
				account.Extra[CodexTurnStateExtraKey].(map[string]any)["collector_proxy_id"] = nil
			}
			repo.records[key] = record
			calls := 0
			s.collector = codexStateTestCollector(func(context.Context, CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error) {
				calls++
				return CodexTurnStateCollectResult{StatusCode: http.StatusOK}, nil
			})
			s.collect(ctx, key)
			require.Zero(t, calls)
			if mode != "no_proxy" {
				require.False(t, s.ensureCodexTurnStateDemand(ctx, &record))
				return
			}
			status := projectCodexTurnStateStatus(account.ID, account, []CodexTurnStateRecord{record}, []string{key.Model}, nil, s.now())
			require.Equal(t, "collector_proxy_not_configured", status.Models[0].CollectionReason)
			natural, err := s.Prepare(ctx, account, key.Model)
			require.NoError(t, err)
			markCodexStateTestBusinessSent(t, s, natural)
			fresh := codexStateTestToken(10, s.now())
			s.Observe(natural, fresh)
			require.NoError(t, s.Finish(ctx, natural, true))
			after, err := repo.Get(ctx, key)
			require.NoError(t, err)
			plain, err := s.encryptor.Decrypt(after.EncryptedToken)
			require.NoError(t, err)
			require.Equal(t, fresh, plain, "removing the collector proxy does not disable natural learning")
			require.Zero(t, calls)
		})
	}
}

func TestCodexTurnStateProxyChangeStatusRespectsAccountRestrictions(t *testing.T) {
	for _, mode := range []string{"disabled", "account_cooldown"} {
		t.Run(mode, func(t *testing.T) {
			s, repo, account, key, _ := newCodexProxyChangedTestService(t)
			record := repo.records[key]
			if mode == "disabled" {
				account.Extra[CodexTurnStateExtraKey].(map[string]any)["enabled"] = false
			} else {
				record.LastError = "account_cooldown"
				record.NextCollectAt = s.now().Add(time.Minute)
			}
			status := projectCodexTurnStateStatus(account.ID, account, []CodexTurnStateRecord{record}, []string{key.Model}, nil, s.now())
			require.Equal(t, mode, status.Models[0].CollectionReason)
			if mode == "disabled" {
				require.Equal(t, "blocked", status.Models[0].CollectionStatus)
			} else {
				require.Equal(t, "backoff", status.Models[0].CollectionStatus)
			}
		})
	}
}
