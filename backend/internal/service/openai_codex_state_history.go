package service

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// CodexTurnStateHistoryProof is private maintenance evidence, never an API DTO.
// It contains no token, ciphertext or credential-derived digest.
type CodexTurnStateHistoryProof struct {
	OwnerAccountID                                                       int64
	OSFamily                                                             string
	Model, Generation, CredentialEpoch, ModelPolicyRevision, AccountType string
	ObservedAt, BusinessAt, IssuedAt, ExpiresAt                          time.Time
	TokenLength, CipherBlocks                                            int
	EnvelopeValid, Delivered                                             bool
	Shapes                                                               [4]CodexTurnStateShape
}

type CodexTurnStateHistoryRepository interface {
	CreateHistoryDemand(context.Context, CodexTurnStateHistoryProof, time.Time) (bool, error)
}

type CodexTurnStateOSActivationRepository interface {
	PublishOSActivation(context.Context, int64, string, string) error
	SubscribeOSActivations(context.Context, func(int64, string, string)) error
}

type codexStateHistoryKey struct {
	ownerAccountID         int64
	osFamily               string
	model, credentialEpoch string
}

var codexStateHistory = struct {
	sync.Mutex
	proofs map[codexStateHistoryKey]CodexTurnStateHistoryProof
}{proofs: make(map[codexStateHistoryKey]CodexTurnStateHistoryProof)}

var codexStateConfigurationListeners = struct {
	sync.Mutex
	listeners map[*CodexTurnStateService]chan int64
}{listeners: make(map[*CodexTurnStateService]chan int64)}

// NotifyCodexTurnStateAccountConfigurationChanged is called only after commit.
// Queue overflow is recovered by the periodic authoritative history check.
func NotifyCodexTurnStateAccountConfigurationChanged(accountID int64) {
	if accountID <= 0 {
		return
	}
	codexStateConfigurationListeners.Lock()
	defer codexStateConfigurationListeners.Unlock()
	for _, ch := range codexStateConfigurationListeners.listeners {
		select {
		case ch <- accountID:
		default:
		}
	}
}

func (s *CodexTurnStateService) startHistoryActivation(ctx context.Context) {
	ch := make(chan int64, 128)
	codexStateConfigurationListeners.Lock()
	codexStateConfigurationListeners.listeners[s] = ch
	codexStateConfigurationListeners.Unlock()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() {
			codexStateConfigurationListeners.Lock()
			delete(codexStateConfigurationListeners.listeners, s)
			codexStateConfigurationListeners.Unlock()
		}()
		for {
			select {
			case <-ctx.Done():
				return
			case ownerID := <-ch:
				workCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				s.activateHistoryForAccount(workCtx, ownerID)
				cancel()
			}
		}
	}()
	if bus, ok := s.repo.(CodexTurnStateOSActivationRepository); ok {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			for ctx.Err() == nil {
				_ = bus.SubscribeOSActivations(ctx, func(id int64, osFamily, generation string) {
					workCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
					defer cancel()
					owner, err := s.currentOwner(workCtx, id, osFamily)
					if err == nil && owner != nil {
						s.activateHistoryForOwner(workCtx, owner, generation)
					}
				})
				timer := time.NewTimer(time.Second)
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
			}
		}()
	}
}

// Called with a.mu held. Envelope evidence does not depend on an inferred plan.
func (s *CodexTurnStateService) observeHistoryEnvelopeLocked(a *CodexTurnStateAttempt, token string, now time.Time) {
	envelope, err := InspectCodexTurnStateEnvelope(token, now)
	a.safeObservation.IssuedAt, a.safeObservation.ExpiresAt = envelope.IssuedAt, envelope.ExpiresAt
	a.safeObservation.EnvelopeValid = err == nil
	var shapes [4]CodexTurnStateShape
	if a.historyProof != nil {
		shapes = a.historyProof.Shapes
	}
	if err == nil {
		index := -1
		switch envelope.ObservedShape {
		case CodexTurnStateObservedPersonalTarget:
			index = 0
		case CodexTurnStateObservedPersonalExtended:
			index = 1
		case CodexTurnStateObservedTeamBusinessTarget:
			index = 2
		case CodexTurnStateObservedTeamBusinessExtended:
			index = 3
		}
		if index >= 0 && envelope.ExpiresAt.After(shapes[index].ExpiresAt) {
			shapes[index] = CodexTurnStateShape{TokenLength: envelope.TokenLength, CipherBlocks: envelope.CipherBlocks, IssuedAt: envelope.IssuedAt, ExpiresAt: envelope.ExpiresAt}
		}
	}
	a.historyProof = &CodexTurnStateHistoryProof{OwnerAccountID: a.OwnerAccountID, OSFamily: a.OSFamily, Model: a.Model, Shapes: shapes,
		CredentialEpoch: a.credentialEpoch, ObservedAt: a.safeObservation.ObservedAt,
		IssuedAt: envelope.IssuedAt, ExpiresAt: envelope.ExpiresAt, TokenLength: envelope.TokenLength,
		CipherBlocks: envelope.CipherBlocks, EnvelopeValid: err == nil}
}

func (s *CodexTurnStateService) bindHistoryCredentials(ctx context.Context, a *CodexTurnStateAttempt, headers http.Header) {
	if a == nil {
		return
	}
	owner, err := s.currentOwner(ctx, a.OwnerAccountID, a.OSFamily)
	header := func(name string) string {
		for key, values := range headers {
			if strings.EqualFold(key, name) && len(values) > 0 {
				return strings.TrimSpace(values[0])
			}
		}
		return ""
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err != nil || !codexTurnStateEligible(owner) || a.credentialEpoch == "" ||
		CodexTurnStateCredentialEpochForAccount(owner) != a.credentialEpoch ||
		strings.TrimSpace(owner.GetCredential("access_token")) == "" ||
		header("Authorization") != "Bearer "+strings.TrimSpace(owner.GetCredential("access_token")) ||
		header("ChatGPT-Account-Id") != strings.TrimSpace(owner.GetCredential("chatgpt_account_id")) {
		a.credentialEpoch = ""
	}
}

func (s *CodexTurnStateService) recordDeliveredHistory(a *CodexTurnStateAttempt, delivered bool) {
	a.mu.Lock()
	a.historyDelivered = delivered
	a.mu.Unlock()
	recordCodexDeliveredHistory(a)
}

// Complete a WS activity marker if delivery raced the successful-write callback.
func (s *CodexTurnStateService) completeBusinessSent(a *CodexTurnStateAttempt) {
	a.mu.Lock()
	enabled, key, sentAt, delivered := a.Enabled, a.key, a.businessSentAt, a.historyDelivered
	a.mu.Unlock()
	if !enabled || s.repo == nil || sentAt.IsZero() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// Finish may have observed delivery before this successful-write callback.
	// Unlike historical recovery, that physical attempt still has the exact
	// cache identity needed to revoke its own cached token safely.
	if pending := s.retainCodexTurnStateAnomaly(a); pending != nil {
		if err := s.processCodexTurnStateAnomaly(ctx, pending, true); err != nil {
			return
		}
	} else if err := s.repo.MarkBusinessSent(ctx, key, sentAt); err != nil || !delivered {
		return
	}
	owner, err := s.currentOwner(ctx, a.OwnerAccountID, a.OSFamily)
	if err == nil && owner != nil {
		s.activateHistoryForOwner(ctx, owner, key.Generation)
	}
}

func recordCodexDeliveredHistory(a *CodexTurnStateAttempt) {
	a.mu.Lock()
	if !a.finished || !a.historyDelivered || !a.historyPhysicalBound || a.historyProof == nil || a.credentialEpoch == "" || a.businessSentAt.IsZero() {
		a.mu.Unlock()
		return
	}
	proof := *a.historyProof
	proof.CredentialEpoch, proof.BusinessAt, proof.Delivered = a.credentialEpoch, a.businessSentAt, true
	a.mu.Unlock()
	key := codexStateHistoryKey{ownerAccountID: proof.OwnerAccountID, osFamily: proof.OSFamily, model: proof.Model, credentialEpoch: proof.CredentialEpoch}
	codexStateHistory.Lock()
	defer codexStateHistory.Unlock()
	previous, exists := codexStateHistory.proofs[key]
	if exists && !proof.ObservedAt.After(previous.ObservedAt) {
		return
	}
	if !exists && len(codexStateHistory.proofs) >= codexTurnStateObservationCapacity {
		var oldestKey codexStateHistoryKey
		var oldest time.Time
		for k, p := range codexStateHistory.proofs {
			if oldest.IsZero() || p.ObservedAt.Before(oldest) {
				oldestKey, oldest = k, p.ObservedAt
			}
		}
		delete(codexStateHistory.proofs, oldestKey)
	}
	codexStateHistory.proofs[key] = proof
}

func codexStateHistorySnapshot(ownerID int64, since time.Time) []CodexTurnStateHistoryProof {
	codexStateHistory.Lock()
	defer codexStateHistory.Unlock()
	var result []CodexTurnStateHistoryProof
	for key, proof := range codexStateHistory.proofs {
		if proof.BusinessAt.Before(since) {
			delete(codexStateHistory.proofs, key)
			continue
		}
		if ownerID == 0 || key.ownerAccountID == ownerID {
			result = append(result, proof)
		}
	}
	return result
}

func (s *CodexTurnStateService) scanHistory(ctx context.Context) {
	proofs := codexStateHistorySnapshot(0, s.now().Add(-CodexTurnStateActiveWindow))
	seen := make(map[int64]bool)
	ids := make([]int64, 0, len(proofs))
	for _, p := range proofs {
		if !seen[p.OwnerAccountID] {
			seen[p.OwnerAccountID] = true
			ids = append(ids, p.OwnerAccountID)
		}
	}
	if len(ids) == 0 {
		return
	}
	owners, err := s.accounts.GetByIDs(ctx, ids)
	if err != nil {
		return
	}
	for _, owner := range owners {
		if owner != nil {
			for _, os := range []string{"windows", "macos", "linux"} {
				projected, err := ResolveOpenAIOAuthCredentialAccount(ctx, s.accounts, owner, os)
				if err == nil {
					s.activateHistoryForOwner(ctx, projected, CodexTurnStateGenerationForAccount(projected))
				}
			}
		}
	}
}

func (s *CodexTurnStateService) activateHistoryForOwner(ctx context.Context, owner *Account, generation string) {
	if generation != CodexTurnStateGenerationForAccount(owner) {
		return
	}
	s.mu.Lock()
	var canceled []CodexTurnStateKey
	for key := range s.running {
		if key.OwnerAccountID == owner.ID && key.OSFamily == codexTurnStateOS(owner) && (key.Generation != generation || !CodexTurnStateConfigForAccount(owner).Enabled) {
			canceled = append(canceled, key)
		}
	}
	s.mu.Unlock()
	for _, key := range canceled {
		s.cancelCollection(key)
	}
	repo, ok := s.repo.(CodexTurnStateHistoryRepository)
	accountType := CodexTurnStateAccountTypeForAccount(owner)
	if !ok || !codexTurnStateEligible(owner) || !CodexTurnStateConfigForAccount(owner).Enabled || generation == "" || accountType == "" {
		return
	}
	now := s.now()
	for _, proof := range codexStateHistorySnapshot(owner.ID, now.Add(-CodexTurnStateActiveWindow)) {
		if proof.OSFamily != codexTurnStateOS(owner) {
			continue
		}
		targetIndex := 0
		if accountType == "team_business" {
			targetIndex = 2
		}
		if proof.Shapes[targetIndex].ExpiresAt.After(now) {
			continue
		}
		if candidate := proof.Shapes[targetIndex+1]; !candidate.ExpiresAt.IsZero() {
			proof.EnvelopeValid = true
			proof.TokenLength, proof.CipherBlocks = candidate.TokenLength, candidate.CipherBlocks
			proof.IssuedAt, proof.ExpiresAt = candidate.IssuedAt, candidate.ExpiresAt
		}
		if !proof.Delivered || !proof.EnvelopeValid || proof.CredentialEpoch == "" || proof.CredentialEpoch != CodexTurnStateCredentialEpochForAccount(owner) ||
			proof.BusinessAt.After(now.Add(30*time.Second)) || !proof.ExpiresAt.After(now) || proof.IssuedAt.After(now.Add(30*time.Second)) {
			continue
		}
		if !(accountType == "personal" && proof.TokenLength == 312 && proof.CipherBlocks == 11) &&
			!(accountType == "team_business" && proof.TokenLength == 356 && proof.CipherBlocks == 13) {
			continue
		}
		allowed, revision, err := s.checkModelPolicy(ctx, proof.Model, true)
		if err != nil || !allowed {
			continue
		}
		proof.Generation, proof.ModelPolicyRevision, proof.AccountType = generation, revision, accountType
		if created, err := repo.CreateHistoryDemand(ctx, proof, now); err == nil && created {
			s.enqueue(ctx, CodexTurnStateKey{OwnerAccountID: owner.ID, OSFamily: codexTurnStateOS(owner), Model: proof.Model, Generation: generation})
		}
	}
}

func (s *CodexTurnStateService) recordCollectorObservation(owner *Account, model string, result CodexTurnStateCollectResult) {
	safe := result.Observation
	if safe == nil || safe.ObservedAt.IsZero() || owner == nil {
		return
	}
	value := CodexTurnStateObservation{CodexModelEvidence: result.ModelEvidence.clone(), OSFamily: codexTurnStateOS(owner), Model: model, RequestSource: "collector", ResponseLength: safe.TokenLength,
		ResponseShape: safe.Shape, ResponseSource: safe.ResponseSource, ResponseObservedShape: safe.ObservedShape,
		ResponseCipherBlocks: safe.CipherBlocks, ResponseValidationReason: safe.ValidationReason,
		Action: "collector_omitted", ObservationID: result.observationID, credentialEpoch: CodexTurnStateCredentialEpochForAccount(owner),
		envelopeEvidence: codexTurnStateObservationEnvelope{checked: true, valid: safe.EnvelopeValid, issuedAt: safe.IssuedAt, expiresAt: safe.ExpiresAt}}
	if value.ObservationID == "" {
		value.ObservationID = uuid.NewString()
	}
	if !result.requestSentAt.IsZero() {
		sentAt := result.requestSentAt
		value.RequestSentAt = &sentAt
	}
	if value.ResponseShape == CodexTurnStateShapeExtended {
		value.ResponseShape = "suspect"
	}
	if value.ResponseShape == CodexTurnStateShapeInvalid {
		value.ResponseShape = "unknown"
	}
	globalCodexTurnStateSummaryStore.update(globalCodexTurnStateSummaryStore.nextSequence(), owner.ID, value, true, safe.ObservedAt)
	logCodexTurnStateObservation(owner.ID, value, safe.ObservedAt)
}
