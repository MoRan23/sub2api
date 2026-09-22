package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCodexTurnStateOnlySentLiteActivatesCollection(t *testing.T) {
	s, repo, account := newCodexStateTestService(t)
	ctx := context.Background()
	ordinary, err := s.PrepareForHTTP(ctx, account, "gpt-5", CodexTurnStateBundleBinding{WireMode: "responses", EgressKind: "direct"})
	require.NoError(t, err)
	markCodexStateTestBusinessSent(t, s, ordinary)
	s.Observe(ordinary, codexStateTestToken(10, s.now()))
	require.NoError(t, s.Finish(ctx, ordinary, true))
	row, err := repo.Get(ctx, ordinary.key)
	require.NoError(t, err)
	require.Equal(t, s.now(), row.LastBusinessAt)
	require.True(t, row.LastEligibleCollectionAt.IsZero())
	require.Empty(t, row.DemandReason)
	require.NotEmpty(t, row.EncryptedToken, "ordinary Responses natural learning remains available")

	lite, err := s.PrepareForHTTP(ctx, account, "gpt-5", codexStateTestBinding())
	require.NoError(t, err)
	require.True(t, lite.CollectionEligible)
	row, _ = repo.Get(ctx, lite.key)
	require.True(t, row.LastEligibleCollectionAt.IsZero(), "preparation is not business activity")
	markCodexStateTestBusinessSent(t, s, lite)
	row, _ = repo.Get(ctx, lite.key)
	require.Equal(t, s.now(), row.LastEligibleCollectionAt)
	require.NotEmpty(t, row.DemandReason)
	require.NoError(t, s.Finish(ctx, lite, false))
}

func TestCodexTurnStateProtocolMismatchDoesNotRevokeOtherBundle(t *testing.T) {
	s, repo, account := newCodexStateTestService(t)
	ctx := context.Background()
	lite, err := s.PrepareForHTTP(ctx, account, "gpt-5", codexStateTestBinding())
	require.NoError(t, err)
	markCodexStateTestBusinessSent(t, s, lite)
	s.Observe(lite, codexStateTestToken(10, s.now()))
	require.NoError(t, s.Finish(ctx, lite, true))
	before, _ := repo.Get(ctx, lite.key)
	other, err := s.PrepareForHTTP(ctx, account, "gpt-5", CodexTurnStateBundleBinding{WireMode: "responses", EgressKind: "direct"})
	require.NoError(t, err)
	require.Empty(t, other.Snapshot.Token)
	require.Equal(t, "bundle_protocol_mismatch", other.MaintenanceReason)
	markCodexStateTestBusinessSent(t, s, other)
	s.Observe(other, codexStateTestToken(11, s.now()))
	require.NoError(t, s.Finish(ctx, other, true))
	after, _ := repo.Get(ctx, lite.key)
	require.Equal(t, before.EncryptedToken, after.EncryptedToken)
	require.Equal(t, before.EncryptedCookieBundle, after.EncryptedCookieBundle)
	require.Equal(t, before.BundleBinding, after.BundleBinding)
}

func TestCodexTurnStateCookieEnvelopeRequiresMatchingV2Binding(t *testing.T) {
	s, repo, account := newCodexStateTestService(t)
	ctx := context.Background()
	a, err := s.PrepareForHTTP(ctx, account, "gpt-5", codexStateTestBinding())
	require.NoError(t, err)
	s.Observe(a, codexStateTestToken(10, s.now()))
	require.NoError(t, s.Finish(ctx, a, true))
	row, _ := repo.Get(ctx, a.key)
	plain, err := s.encryptor.Decrypt(row.EncryptedCookieBundle)
	require.NoError(t, err)
	var envelope codexTurnStateCookieEnvelope
	require.NoError(t, json.Unmarshal([]byte(plain), &envelope))
	require.Equal(t, 2, envelope.Version)
	for _, tc := range []struct {
		name   string
		change func(*codexTurnStateCookieEnvelope)
	}{
		{"legacy", func(v *codexTurnStateCookieEnvelope) { v.Version = 1 }},
		{"mode", func(v *codexTurnStateCookieEnvelope) { v.Binding.WireMode = "responses" }},
		{"route", func(v *codexTurnStateCookieEnvelope) {
			v.Binding = CodexTurnStateBundleBinding{WireMode: "lite", EgressKind: "proxy", ProxyID: 2, ProxyRouteGeneration: 1}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			value := envelope
			tc.change(&value)
			encoded, err := json.Marshal(value)
			require.NoError(t, err)
			corrupt := *row
			corrupt.EncryptedCookieBundle, err = s.encryptor.Encrypt(string(encoded))
			require.NoError(t, err)
			repo.records[a.key] = corrupt
			next, err := s.PrepareForHTTP(ctx, account, "gpt-5", codexStateTestBinding())
			require.NoError(t, err)
			require.Empty(t, next.Snapshot.Token)
			require.Equal(t, "bundle_binding_invalid", next.MaintenanceReason)
			require.NoError(t, s.Finish(ctx, next, false))
		})
	}
}

func TestCodexTurnStateRetryKeepsFrozenBundleAndExpiredEligibility(t *testing.T) {
	s, repo, account := newCodexStateTestService(t)
	ctx := context.Background()
	a, err := s.PrepareForHTTP(ctx, account, "gpt-5", codexStateTestBinding())
	require.NoError(t, err)
	markCodexStateTestBusinessSent(t, s, a)
	s.Observe(a, codexStateTestToken(10, s.now()))
	require.NoError(t, s.Finish(ctx, a, true))
	first, err := s.PrepareForHTTP(ctx, account, "gpt-5", codexStateTestBinding())
	require.NoError(t, err)
	require.NotEmpty(t, first.Snapshot.Token)
	require.NoError(t, s.Finish(ctx, first, false))
	retry, err := s.RetryForHTTP(ctx, first)
	require.NoError(t, err)
	require.Equal(t, first.Snapshot, retry.Snapshot)
	require.NotEqual(t, first.id, retry.id)
	require.False(t, retry.finished)
	require.True(t, s.ValidateAttempt(ctx, retry))
	require.NoError(t, s.Finish(ctx, retry, false))
	row, _ := repo.Get(ctx, a.key)
	row.LastEligibleCollectionAt = s.now().Add(-CodexTurnStateActiveWindow - time.Second)
	row.LastBusinessAt = s.now()
	require.False(t, s.ensureCodexTurnStateDemand(ctx, row), "recent non-Lite traffic does not renew eligible activity")
}

func TestCodexTurnStateRetrySurvivesReplacementButNotInvalidationABA(t *testing.T) {
	s, repo, account := newCodexStateTestService(t)
	ctx := context.Background()
	now := s.now()
	s.now = func() time.Time { return now }
	seed, err := prepareCodexStateTest(s, ctx, account, "gpt-5")
	require.NoError(t, err)
	markCodexStateTestBusinessSent(t, s, seed)
	s.Observe(seed, codexStateTestToken(10, now))
	require.NoError(t, s.Finish(ctx, seed, true))
	old, err := prepareCodexStateTest(s, ctx, account, "gpt-5")
	require.NoError(t, err)
	oldSnapshot := old.Snapshot
	newer, err := prepareCodexStateTest(s, ctx, account, "gpt-5")
	require.NoError(t, err)
	now = now.Add(time.Second)
	s.Observe(newer, codexStateTestToken(10, now))
	require.NoError(t, s.Finish(ctx, newer, true))
	require.True(t, s.ValidateAttempt(ctx, old), "normal replacement must retain a still-valid frozen retry bundle")
	retry, err := s.RetryForHTTP(ctx, old)
	require.NoError(t, err)
	require.Equal(t, oldSnapshot, retry.Snapshot)
	anomaly, err := prepareCodexStateTest(s, ctx, account, "gpt-5")
	require.NoError(t, err)
	markCodexStateTestBusinessSent(t, s, anomaly)
	s.Observe(anomaly, codexStateTestToken(11, now))
	require.NoError(t, s.Finish(ctx, anomaly, true))
	row, err := repo.Get(ctx, old.key)
	require.NoError(t, err)
	require.Greater(t, row.BundleInvalidationVersion, oldSnapshot.BundleInvalidationVersion)
	now = now.Add(time.Second)
	restored, err := prepareCodexStateTest(s, ctx, account, "gpt-5")
	require.NoError(t, err)
	s.Observe(restored, codexStateTestToken(10, now))
	require.NoError(t, s.Finish(ctx, restored, true))
	require.False(t, s.ValidateAttempt(ctx, old), "a new target cannot erase the old bundle's revocation watermark")
	require.False(t, s.ValidateAttempt(ctx, retry))
	require.NoError(t, s.Finish(ctx, old, false))
	require.NoError(t, s.Finish(ctx, retry, false))
}

func TestCodexTurnStateStatusChecksLiveBundleProxyReadOnly(t *testing.T) {
	for _, name := range []string{"active", "generation", "disabled", "expired", "removed"} {
		t.Run(name, func(t *testing.T) {
			s, repo, account := newCodexStateTestService(t)
			ctx := context.Background()
			binding := CodexTurnStateBundleBinding{WireMode: "lite", EgressKind: "proxy", ProxyID: 2, ProxyRouteGeneration: 1}
			a, err := s.PrepareForHTTP(ctx, account, "gpt-5", binding)
			require.NoError(t, err)
			s.Observe(a, codexStateTestToken(10, s.now()))
			require.NoError(t, s.Finish(ctx, a, true))
			proxy := &Proxy{ID: 2, Host: "localhost", Port: 7897, Status: StatusActive, RouteGeneration: 1}
			wantReason := "bundle_proxy_unavailable"
			switch name {
			case "active":
				wantReason = ""
			case "generation":
				proxy.RouteGeneration = 2
				wantReason = "bundle_proxy_changed"
			case "disabled":
				proxy.Status = "disabled"
			case "expired":
				expiry := s.now()
				proxy.ExpiresAt = &expiry
			case "removed":
				account.Extra[CodexTurnStateExtraKey].(map[string]any)["collector_proxy_id"] = float64(3)
			}
			s.SetProxyRepository(codexCollectorTransportProxies{proxy: proxy})
			before := repo.records[a.key]
			status, err := s.GetStatus(ctx, account.ID)
			require.NoError(t, err)
			require.Len(t, status.Models, 1)
			require.Equal(t, wantReason == "", status.Models[0].CacheAvailable)
			require.Equal(t, wantReason, status.Models[0].BundleUnavailableReason)
			require.Equal(t, before, repo.records[a.key], "reading status must never revoke or rewrite the bundle")
		})
	}
}
