package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCodexTurnStateStatusSeparatesUsableCacheFromPausedCollector(t *testing.T) {
	s, _, account := newCodexStateTestService(t)
	now := s.now()
	record := CodexTurnStateRecord{OwnerAccountID: account.ID, Model: "gpt-5", Generation: "gen1", EncryptedToken: "ciphertext",
		Shape: "target", TokenLength: 292, CipherBlocks: 10, IssuedAt: now.Add(-time.Minute), ExpiresAt: now.Add(59 * time.Minute),
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
	busy := CodexTurnStateRecord{OwnerAccountID: account.ID, Model: "gpt-5", Generation: "gen1", LastBusinessAt: now,
		DemandReason: "extended_shape", CollectionStatus: "collecting", LastCollectedAt: now, BusinessInFlight: true}
	cooldown := CodexTurnStateRecord{OwnerAccountID: account.ID, Model: "gpt-5-mini", Generation: "gen1", LastBusinessAt: now,
		LastCollectedAt: now, NextCollectAt: now.Add(time.Minute), LastError: "collector_rate_limited"}
	status := projectCodexTurnStateStatus(account.ID, account, []CodexTurnStateRecord{busy, cooldown}, []string{"gpt-5", "gpt-5-mini"}, nil, now)
	require.Equal(t, "collecting", status.Models[0].CollectionStatus)
	require.Equal(t, "collecting", status.Models[0].CollectionReason)
	busy.CollectionStatus = "pending"
	status = projectCodexTurnStateStatus(account.ID, account, []CodexTurnStateRecord{busy, cooldown}, []string{"gpt-5", "gpt-5-mini"}, nil, now)
	require.Equal(t, "backoff", status.Models[0].CollectionStatus)
	require.Equal(t, "account_cooldown", status.Models[0].CollectionReason)
	require.Equal(t, cooldown.NextCollectAt, *status.Models[0].NextCollectAt)
}
