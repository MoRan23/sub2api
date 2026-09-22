package repository

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func codexHistoryProofFixture(now time.Time) service.CodexTurnStateHistoryProof {
	return service.CodexTurnStateHistoryProof{
		OwnerAccountID: 17, OSFamily: "windows", Model: "gpt-5.4", Generation: "generation-1",
		CredentialEpoch: "00000000-0000-4000-8000-000000000011", ModelPolicyRevision: "policy-1", AccountType: "personal",
		BusinessAt: now.Add(-2 * time.Minute), ObservedAt: now.Add(-time.Minute),
		IssuedAt: now.Add(-2 * time.Minute), ExpiresAt: now.Add(service.CodexTurnStateLifetime - 2*time.Minute),
		TokenLength: 312, CipherBlocks: 11, EnvelopeValid: true, Delivered: true,
	}
}

func TestCodexHistoryDemandRejectsUnsafeOrInactiveProofBeforeDatabaseWrite(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	require.True(t, validCodexStateHistoryProof(codexHistoryProofFixture(now), now))
	for name, mutate := range map[string]func(*service.CodexTurnStateHistoryProof){
		"not delivered":              func(p *service.CodexTurnStateHistoryProof) { p.Delivered = false },
		"invalid envelope":           func(p *service.CodexTurnStateHistoryProof) { p.EnvelopeValid = false },
		"missing epoch":              func(p *service.CodexTurnStateHistoryProof) { p.CredentialEpoch = "" },
		"missing policy":             func(p *service.CodexTurnStateHistoryProof) { p.ModelPolicyRevision = "" },
		"idle":                       func(p *service.CodexTurnStateHistoryProof) { p.BusinessAt = now.Add(-31 * time.Minute) },
		"future observation":         func(p *service.CodexTurnStateHistoryProof) { p.ObservedAt = now.Add(time.Second) },
		"business after observation": func(p *service.CodexTurnStateHistoryProof) { p.BusinessAt = now },
		"expired": func(p *service.CodexTurnStateHistoryProof) {
			p.IssuedAt = now.Add(-service.CodexTurnStateLifetime)
			p.ExpiresAt = now
		},
		"fabricated lifetime": func(p *service.CodexTurnStateHistoryProof) {
			p.ExpiresAt = p.IssuedAt.Add(service.CodexTurnStateLifetime + time.Second)
		},
		"target shape":        func(p *service.CodexTurnStateHistoryProof) { p.TokenLength = 292; p.CipherBlocks = 10 },
		"wrong account shape": func(p *service.CodexTurnStateHistoryProof) { p.TokenLength = 356; p.CipherBlocks = 13 },
		"wrong blocks":        func(p *service.CodexTurnStateHistoryProof) { p.CipherBlocks = 12 },
	} {
		t.Run(name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			proof := codexHistoryProofFixture(now)
			mutate(&proof)
			created, err := (&openAICodexStateRepository{db: db}).CreateHistoryDemand(context.Background(), proof, now)
			require.NoError(t, err)
			require.False(t, created)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
