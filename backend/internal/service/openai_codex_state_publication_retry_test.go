package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Faults affect one physical attempt only. Historical recovery mirrors the
// repository's intentional rule that an unbound proof cannot revoke a target.
type codexStatePublicationRetryRepository struct {
	*codexStateMemoryRepo
	getFailure      error
	saveFailure     error
	markFailure     error
	endFailure      error
	casConflicts    int
	historyConsumed int
}

func (r *codexStatePublicationRetryRepository) Get(ctx context.Context, key CodexTurnStateKey) (*CodexTurnStateRecord, error) {
	if err := r.getFailure; err != nil {
		r.getFailure = nil
		return nil, err
	}
	return r.codexStateMemoryRepo.Get(ctx, key)
}

func (r *codexStatePublicationRetryRepository) SaveCAS(ctx context.Context, record CodexTurnStateRecord, expected int64) (bool, error) {
	if err := r.saveFailure; err != nil {
		r.saveFailure = nil
		return false, err
	}
	if r.casConflicts > 0 {
		r.casConflicts--
		// A concurrent scheduling-only write advances CAS without changing A.
		current, err := r.codexStateMemoryRepo.Get(ctx, record.Key())
		if err != nil {
			return false, err
		}
		_, err = r.codexStateMemoryRepo.SaveCAS(ctx, *current, current.Version)
		return false, err
	}
	return r.codexStateMemoryRepo.SaveCAS(ctx, record, expected)
}

func (r *codexStatePublicationRetryRepository) MarkBusinessSent(ctx context.Context, key CodexTurnStateKey, sentAt time.Time) error {
	if err := r.markFailure; err != nil {
		r.markFailure = nil
		return err
	}
	return r.codexStateMemoryRepo.MarkBusinessSent(ctx, key, sentAt)
}

func (r *codexStatePublicationRetryRepository) EndBusiness(ctx context.Context, key CodexTurnStateKey, id string) error {
	if err := r.endFailure; err != nil {
		r.endFailure = nil
		return err
	}
	return r.codexStateMemoryRepo.EndBusiness(ctx, key, id)
}

func (r *codexStatePublicationRetryRepository) CreateHistoryDemand(ctx context.Context, proof CodexTurnStateHistoryProof, now time.Time) (bool, error) {
	key := CodexTurnStateKey{OSFamily: "windows", OwnerAccountID: proof.OwnerAccountID, Model: proof.Model, Generation: proof.Generation}
	record, err := r.codexStateMemoryRepo.Get(ctx, key)
	if err != nil || record == nil || !proof.ObservedAt.After(record.HistoryProofObservedAt) {
		return false, err
	}
	r.historyConsumed++
	createDemand := record.EncryptedToken == "" || !record.ExpiresAt.After(now)
	if createDemand {
		record.DemandReason, record.DemandAt = "extended_shape", proof.ObservedAt
	}
	record.HistoryProofObservedAt = proof.ObservedAt
	_, err = r.codexStateMemoryRepo.SaveCAS(ctx, *record, record.Version)
	return createDemand, err
}

func TestCodexTurnStatePublicationRetryRecoversDeliveredAnomaly(t *testing.T) {
	for _, timing := range []string{"late_send", "normal_send"} {
		for _, fault := range []string{"get", "save", "cas_exhausted", "mark_business"} {
			if timing == "late_send" && fault == "mark_business" {
				// Already covered by LateWSSendRequiresPersistedSend: pending is
				// retained when the activity write fails before publication starts.
				continue
			}
			t.Run(timing+"/"+fault, func(t *testing.T) {
				s, memory, account, attempt, wire := prepareCodexStateLateSendTest(t, "personal")
				s.now = time.Now
				ctx := context.Background()
				account.Extra[CodexTurnStateCredentialEpochExtraKey] = "retry-test-epoch"
				attempt.credentialEpoch = "retry-test-epoch"
				repo := &codexStatePublicationRetryRepository{codexStateMemoryRepo: memory}
				s.repo = repo
				s.Observe(attempt, codexStateTestToken(11, s.now()))
				if timing == "late_send" {
					require.NoError(t, s.Finish(ctx, attempt, true))
				} else {
					bindCodexTurnStateSummarySequence(wire)
				}
				failure := errors.New("synthetic transient publication failure")
				switch fault {
				case "get":
					repo.getFailure = failure
				case "save":
					repo.saveFailure = failure
				case "cas_exhausted":
					repo.casConflicts = 3
				case "mark_business":
					repo.markFailure = failure
				}
				if timing == "late_send" {
					bindCodexTurnStateSummarySequence(wire)
				} else {
					_ = s.Finish(ctx, attempt, true)
				}
				require.Len(t, s.pendingPublications, 1, "failure must retain exact publication evidence")
				// History may be consumed during the outage, but must not consume
				// the separate exact evidence. No second send callback is required.
				s.activateHistoryForOwner(ctx, account, attempt.Generation)
				require.Len(t, s.pendingPublications, 1)
				retryAt := time.Now().Add(2 * time.Second)
				s.now = func() time.Time { return retryAt }
				s.pumpDue(ctx)
				after, err := memory.Get(ctx, attempt.key)
				require.NoError(t, err)
				require.Positive(t, repo.historyConsumed, "history fallback must not hide the dropped publication")
				require.True(t, after.EncryptedToken == "", "the delivered anomaly was lost after a transient publication failure; history alone cannot revoke the existing target")
				require.Equal(t, "extended_shape", after.DemandReason)
				require.Empty(t, s.pendingPublications, "successful background publication consumes its entry")
			})
		}
	}
}

func TestCodexTurnStatePendingPublicationRejectsStaleEvidence(t *testing.T) {
	for _, changed := range []string{"new_target", "same_time_new_target", "disabled", "generation", "policy", "expired", "idle"} {
		t.Run(changed, func(t *testing.T) {
			s, memory, account, attempt, wire := prepareCodexStateLateSendTest(t, "personal")
			ctx := context.Background()
			bindCodexTurnStateSummarySequence(wire)
			repo := &codexStatePublicationRetryRepository{codexStateMemoryRepo: memory, saveFailure: errors.New("synthetic temporary failure")}
			s.repo = repo
			issuedAt := s.now()
			if changed == "expired" {
				issuedAt = issuedAt.Add(-CodexTurnStateLifetime + 10*time.Second)
			}
			s.Observe(attempt, codexStateTestToken(11, issuedAt))
			require.Error(t, s.Finish(ctx, attempt, true))
			require.Len(t, s.pendingPublications, 1)
			retryAt := s.now().Add(2 * time.Second)
			switch changed {
			case "new_target":
				business, err := s.Prepare(ctx, account, attempt.Model)
				require.NoError(t, err)
				markCodexStateTestBusinessSent(t, s, business)
				s.Observe(business, codexStateTestToken(10, s.now()))
				require.NoError(t, s.Finish(ctx, business, true))
				fresh, err := memory.Get(ctx, attempt.key)
				require.NoError(t, err)
				require.Equal(t, s.now().Truncate(time.Second), fresh.IssuedAt, "pending anomaly must not suppress a newer natural target")
			case "same_time_new_target":
				fresh, err := memory.Get(ctx, attempt.key)
				require.NoError(t, err)
				bytes, err := base64.URLEncoding.DecodeString(attempt.Snapshot.Token)
				require.NoError(t, err)
				bytes[9] ^= 1
				fresh.EncryptedToken, err = s.encryptor.Encrypt(base64.URLEncoding.EncodeToString(bytes))
				require.NoError(t, err)
				ok, err := memory.SaveCAS(ctx, *fresh, fresh.Version)
				require.NoError(t, err)
				require.True(t, ok)
			case "disabled":
				account.Extra[CodexTurnStateExtraKey].(map[string]any)["enabled"] = false
			case "generation":
				account.Extra[CodexTurnStateGenerationExtraKey] = "new-generation"
			case "policy":
				s.modelPolicy.(*codexStateTestModelPolicy).set("other-model")
			case "expired":
				retryAt = s.now().Add(20 * time.Second)
			case "idle":
				retryAt = s.now().Add(31 * time.Minute)
			}
			before, err := memory.Get(ctx, attempt.key)
			require.NoError(t, err)
			s.now = func() time.Time { return retryAt }
			s.pumpDue(ctx)
			after, err := memory.Get(ctx, attempt.key)
			require.NoError(t, err)
			require.Equal(t, before.EncryptedToken, after.EncryptedToken)
			require.Empty(t, after.DemandReason)
			require.Empty(t, s.pendingPublications)
		})
	}
}

func TestCodexTurnStatePendingPublicationStopRejectsLateCompletion(t *testing.T) {
	for _, timing := range []string{"finish", "binding"} {
		t.Run(timing, func(t *testing.T) {
			s, memory, _, attempt, wire := prepareCodexStateLateSendTest(t, "personal")
			s.Observe(attempt, codexStateTestToken(11, s.now()))
			if timing == "binding" {
				require.NoError(t, s.Finish(context.Background(), attempt, true))
			} else {
				bindCodexTurnStateSummarySequence(wire)
			}
			s.Stop()
			s.repo = &codexStatePublicationRetryRepository{codexStateMemoryRepo: memory, markFailure: errors.New("synthetic shutdown error")}
			if timing == "binding" {
				bindCodexTurnStateSummarySequence(wire)
			} else {
				_ = s.Finish(context.Background(), attempt, true)
			}
			require.Empty(t, s.pendingPublications, "a completion after Stop must not retain new evidence")
		})
	}
}

func TestCodexTurnStatePendingPublicationIsBoundedAndPrivate(t *testing.T) {
	s, _, account := newCodexStateTestService(t)
	for index := range codexTurnStatePendingPublicationLimit + 1 {
		attempt := &CodexTurnStateAttempt{OSFamily: "windows", Enabled: true, finished: true, historyDelivered: true, historyPhysicalBound: true, anomalyPublication: true,
			key:            CodexTurnStateKey{OSFamily: "windows", OwnerAccountID: account.ID, Model: fmt.Sprintf("model-%04d", index), Generation: "gen1"},
			businessSentAt: s.now(), pendingAnomaly: &CodexTurnStateShape{Shape: "extended", ExpiresAt: s.now().Add(CodexTurnStateLifetime)},
			safeObservation: CodexTurnStateSafeObservation{ObservedAt: s.now().Add(time.Duration(index) * time.Nanosecond)}}
		s.retainCodexTurnStateAnomaly(attempt)
	}
	require.Len(t, s.pendingPublications, codexTurnStatePendingPublicationLimit)
	require.NotContains(t, s.pendingPublications, CodexTurnStateKey{OSFamily: "windows", OwnerAccountID: account.ID, Model: "model-0000", Generation: "gen1"})
	for _, pending := range s.pendingPublications {
		encoded, err := json.Marshal(pending)
		require.NoError(t, err)
		require.JSONEq(t, `{}`, string(encoded), "private publication evidence cannot be exported as JSON")
		break
	}
	s.Stop()
	require.Empty(t, s.pendingPublications)
}

func TestCodexTurnStatePendingPublicationRetriesOneBoundedBatch(t *testing.T) {
	s, _, _ := newCodexStateTestService(t)
	s.pendingPublications = make(map[CodexTurnStateKey]*codexTurnStatePendingPublication)
	for index := range codexTurnStatePendingPublicationBatch + 3 {
		key := CodexTurnStateKey{OSFamily: "windows", OwnerAccountID: 1, Model: fmt.Sprintf("model-%04d", index), Generation: "gen1"}
		s.pendingPublications[key] = &codexTurnStatePendingPublication{key: key, sentAt: s.now(),
			shape: CodexTurnStateShape{Shape: "extended", TokenLength: 312, CipherBlocks: 11,
				IssuedAt: s.now(), ExpiresAt: s.now().Add(CodexTurnStateLifetime)}, observedAt: s.now()}
	}
	// Every item is terminal because its model is excluded; a single tick still
	// handles at most the bounded batch, allowing other maintenance to progress.
	s.retryCodexTurnStateAnomalies(context.Background())
	require.Len(t, s.pendingPublications, 3)
}

func TestCodexTurnStatePublicationEndFailureKeepsRecoverableDemand(t *testing.T) {
	s, memory, _, attempt, wire := prepareCodexStateLateSendTest(t, "personal")
	ctx := context.Background()
	bindCodexTurnStateSummarySequence(wire)
	repo := &codexStatePublicationRetryRepository{codexStateMemoryRepo: memory, endFailure: errors.New("synthetic lease cleanup failure")}
	s.repo = repo
	s.Observe(attempt, codexStateTestToken(11, s.now()))
	require.Error(t, s.Finish(ctx, attempt, true))
	after, err := memory.Get(ctx, attempt.key)
	require.NoError(t, err)
	require.True(t, after.EncryptedToken == "")
	require.Equal(t, "extended_shape", after.DemandReason)
	require.True(t, s.queued[attempt.key], "successful publication queues the demand even when lease cleanup fails")
	s.pumpDue(ctx)
	require.True(t, s.queued[attempt.key], "persisted demand survives lease cleanup failure and is queued by the ordinary due scan")
}
