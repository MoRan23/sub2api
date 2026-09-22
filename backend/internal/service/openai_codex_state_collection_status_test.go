package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCodexTurnStateStatusSeparatesUsableCacheFromPausedCollector(t *testing.T) {
	s, _, account := newCodexStateTestService(t)
	now := s.now()
	record := CodexTurnStateRecord{OSFamily: "windows", OwnerAccountID: account.ID, Model: "gpt-5", Generation: "gen1", EncryptedToken: "ciphertext",
		EncryptedCookieBundle: "encrypted:fixture-cookie-bundle", AuthorizationGeneration: account.OpenAIOAuthAuthorizationGeneration,
		Shape: "target", TokenLength: 292, CipherBlocks: 10, IssuedAt: now.Add(-time.Minute), ExpiresAt: now.Add(CodexTurnStateLifetime - time.Minute),
		LastBusinessAt: now, CollectorPaused: true, LastError: "collector_auth_rejected"}
	status := projectCodexTurnStateStatus(account.ID, account, []CodexTurnStateRecord{record}, []string{"gpt-5"}, nil, now)
	require.True(t, status.Models[0].CacheAvailable)
	require.Equal(t, "paused", status.Models[0].CollectionStatus)
	require.Equal(t, "collector_auth_rejected", status.Models[0].CollectionReason)
	record.ExpiresAt = now
	status = projectCodexTurnStateStatus(account.ID, account, []CodexTurnStateRecord{record}, []string{"gpt-5"}, nil, now)
	require.False(t, status.Models[0].CacheAvailable)
}

func TestCodexTurnStateStatusShowsParallelCollectionAndOwnerCooldown(t *testing.T) {
	s, _, account := newCodexStateTestService(t)
	now := s.now()
	busy := CodexTurnStateRecord{OSFamily: "windows", OwnerAccountID: account.ID, Model: "gpt-5", Generation: "gen1", LastBusinessAt: now,
		DemandReason: "extended_shape", CollectionStatus: "collecting", LastCollectedAt: now, BusinessInFlight: true}
	cooldown := CodexTurnStateRecord{OSFamily: "windows", OwnerAccountID: account.ID, Model: "gpt-5-mini", Generation: "gen1", LastBusinessAt: now,
		LastCollectedAt: now, NextCollectAt: now.Add(time.Minute), LastError: "collector_rate_limited"}
	status := projectCodexTurnStateStatus(account.ID, account, []CodexTurnStateRecord{busy, cooldown}, []string{"gpt-5", "gpt-5-mini"}, nil, now)
	require.Equal(t, "collecting", status.Models[0].CollectionStatus)
	require.Equal(t, "collecting", status.Models[0].CollectionReason)
	busy.CollectionStatus = "pending"
	status = projectCodexTurnStateStatus(account.ID, account, []CodexTurnStateRecord{busy, cooldown}, []string{"gpt-5", "gpt-5-mini"}, nil, now)
	require.Equal(t, "backoff", status.Models[0].CollectionStatus)
	require.Equal(t, "collector_rate_limited", status.Models[0].CollectionReason)
	require.Equal(t, cooldown.NextCollectAt, *status.Models[0].NextCollectAt)
}

func TestCodexTurnStateStatusDoesNotBorrowAnotherModelsShapeRetryOrReservation(t *testing.T) {
	for _, reason := range []string{"no_target_state", "model_mismatch", "collecting"} {
		t.Run(reason, func(t *testing.T) {
			s, _, account := newCodexStateTestService(t)
			now := s.now()
			failed := CodexTurnStateRecord{OSFamily: "windows", OwnerAccountID: account.ID, Model: "gpt-5", Generation: "gen1", LastBusinessAt: now,
				DemandReason: "extended_shape", LastCollectedAt: now, NextCollectAt: now, LastError: "collector_connect_timeout", CollectionStatus: "pending"}
			other := failed
			other.Model, other.LastError, other.CollectionStatus = "gpt-5-mini", reason, "backoff"
			other.NextCollectAt = now.Add(5 * time.Second)
			if reason == "collecting" {
				other.LastError, other.CollectionStatus = "", "collecting"
				other.CollectorAttemptID = "other-attempt"
				other.NextCollectAt = now.Add(CodexTurnStateCollectTimeout + CodexTurnStateRetryInterval)
			}
			status := projectCodexTurnStateStatus(account.ID, account, []CodexTurnStateRecord{failed, other}, []string{"gpt-5", "gpt-5-mini"}, nil, now)
			require.Equal(t, "pending", status.Models[0].CollectionStatus)
			require.Equal(t, "queued", status.Models[0].CollectionReason)
			require.Equal(t, now, *status.Models[0].NextCollectAt)
			require.Equal(t, "collector_connect_timeout", status.Models[0].LastError)
		})
	}
}

func TestCodexTurnStateStatusExpiresSharedCacheWithCookieBundle(t *testing.T) {
	s, _, account := newCodexStateTestService(t)
	now := s.now()
	cookieExpiresAt := now.Add(10 * time.Second)
	record := CodexTurnStateRecord{
		OwnerAccountID: account.ID, OSFamily: "linux", Model: "gpt-5", Generation: "gen1",
		EncryptedToken: "private-token", EncryptedCookieBundle: "private-cookie-bundle",
		AuthorizationGeneration: account.OpenAIOAuthAuthorizationGeneration, CookieBundleExpiresAt: &cookieExpiresAt,
		Shape: "target", TokenLength: 292, CipherBlocks: 10, IssuedAt: now, ExpiresAt: now.Add(CodexTurnStateLifetime),
		LastBusinessAt: now, DemandReason: "refresh", CollectionStatus: "scheduled", NextCollectAt: now.Add(CodexTurnStateCollectInterval),
	}
	require.Empty(t, record.Key().OSFamily, "source identity must not partition the shared record")
	status := projectCodexTurnStateStatus(account.ID, account, []CodexTurnStateRecord{record}, []string{"gpt-5"}, nil, now)
	require.Len(t, status.Models, 1)
	require.Equal(t, "shared", status.CacheScope)
	require.Equal(t, "shared", status.Models[0].CacheScope)
	require.Equal(t, "linux", status.Models[0].OSFamily)
	require.True(t, status.Models[0].CacheAvailable)
	require.Equal(t, cookieExpiresAt, *status.Models[0].CookieBundleExpiresAt)
	for _, missing := range []string{"cookie_bundle", "authorization_generation", "stale_authorization_generation"} {
		incomplete := record
		switch missing {
		case "cookie_bundle":
			incomplete.EncryptedCookieBundle = ""
		case "authorization_generation":
			incomplete.AuthorizationGeneration = ""
		case "stale_authorization_generation":
			incomplete.AuthorizationGeneration = "old-authorization"
		}
		unavailable := projectCodexTurnStateStatus(account.ID, account, []CodexTurnStateRecord{incomplete}, []string{"gpt-5"}, nil, now)
		require.False(t, unavailable.Models[0].CacheAvailable, "an incomplete or stale bundle must be unavailable: %s", missing)
	}
	status = projectCodexTurnStateStatus(account.ID, account, []CodexTurnStateRecord{record}, []string{"gpt-5"}, nil, cookieExpiresAt)
	require.False(t, status.Models[0].CacheAvailable, "the token cannot outlive a required cookie in its atomic bundle")
	require.Equal(t, "expired", status.Models[0].State)
}
