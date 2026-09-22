package service

import (
	"context"
	"errors"
	"time"
)

var errCodexTurnStatePublicationConflict = errors.New("turn_state_publication_conflict")

// All carriers from one response share target-first admission, including a WS
// response that finishes before the physical-write callback.
func selectCodexTurnStateCandidates(tokens []string, accountType string, now time.Time) (string, CodexTurnStateShape, CodexTurnStateShape) {
	var best string
	var target, extended CodexTurnStateShape
	for _, token := range tokens {
		shape, err := ParseCodexTurnState(token, accountType, now)
		if err != nil {
			continue
		}
		if shape.Shape == CodexTurnStateShapeExtended {
			if extended.IssuedAt.IsZero() || shape.IssuedAt.After(extended.IssuedAt) {
				extended = shape
			}
		} else if best == "" || shape.IssuedAt.After(target.IssuedAt) {
			best, target = token, shape
		}
	}
	return best, target, extended
}

// Only trusted response parsing supplies shape. No token survives a deferred
// publication, and plan/time admission is rechecked when its send is confirmed.
func (s *CodexTurnStateService) publishCodexTurnStateAnomaly(ctx context.Context, key CodexTurnStateKey, shape CodexTurnStateShape, expected int64, strict bool, policyRevision string, demandAt time.Time, identity codexTurnStateCacheIdentity) (bool, error) {
	now := s.now()
	if demandAt.IsZero() || shape.Shape != CodexTurnStateShapeExtended || shape.IssuedAt.IsZero() ||
		shape.IssuedAt.After(now.Add(30*time.Second)) || !shape.ExpiresAt.Equal(shape.IssuedAt.Add(CodexTurnStateLifetime)) || !shape.ExpiresAt.After(now) {
		return false, nil
	}
	owner, err := s.currentOwner(ctx, key.OwnerAccountID, key.OSFamily)
	if err != nil {
		return false, err
	}
	if !codexTurnStateEligible(owner) || !CodexTurnStateConfigForAccount(owner).Enabled || CodexTurnStateGenerationForAccount(owner) != key.Generation {
		return false, nil
	}
	accountType := CodexTurnStateAccountTypeForAccount(owner)
	if !(accountType == "personal" && shape.TokenLength == 312 && shape.CipherBlocks == 11) &&
		!(accountType == "team_business" && shape.TokenLength == 356 && shape.CipherBlocks == 13) {
		return false, nil
	}
	for range 3 {
		allowed, revision, policyErr := s.checkModelPolicy(ctx, key.Model, true)
		if policyErr != nil {
			return false, policyErr
		}
		if !allowed || revision != policyRevision {
			return false, nil
		}
		record, err := s.repo.Get(ctx, key)
		if err != nil || record == nil {
			return false, err
		}
		if (strict && record.Version != expected) || (record.Version != expected && !identity.matches(record)) {
			return false, nil
		}
		// A newly invalidated package no longer qualifies for the successful
		// refresh interval. Preserve error backoff and real account cooldowns.
		if record.DemandReason == "refresh" && record.CollectionStatus == "scheduled" && record.LastError == "" && !codexTurnStateRetainsAccountCooldown(record) {
			record.NextCollectAt = time.Time{}
		}
		record.EncryptedToken, record.ExpiresAt = "", time.Time{}
		record.EncryptedCookieBundle, record.CookieBundleExpiresAt = "", nil
		record.BundleBinding = CodexTurnStateBundleBinding{}
		record.Shape, record.TokenLength, record.CipherBlocks = shape.Shape, shape.TokenLength, shape.CipherBlocks
		if shape.IssuedAt.After(record.IssuedAt) {
			record.IssuedAt = shape.IssuedAt
		}
		record.RefreshReason = "extended_shape"
		record.DemandReason, record.DemandAt = "extended_shape", demandAt
		if record.CollectionStatus != "collecting" || !record.LastCollectedAt.Add(CodexTurnStateCollectTimeout).After(s.now()) {
			record.CollectionStatus, record.CollectionReason = "pending", "queued"
		}
		if record.LastEligibleCollectionAt.IsZero() || record.LastEligibleCollectionAt.Before(now.Add(-CodexTurnStateActiveWindow)) {
			clearIdleCodexTurnStateDemand(record)
		}
		allowed, revision, policyErr = s.checkModelPolicy(ctx, key.Model, true)
		if policyErr != nil {
			return false, policyErr
		}
		if !allowed || revision != policyRevision {
			return false, nil
		}
		record.ModelPolicyRevision = policyRevision
		ok, saveErr := s.repo.SaveCAS(ctx, *record, record.Version)
		if saveErr != nil || ok {
			return ok, saveErr
		}
		// Scheduling writes may advance the version without changing the cache.
		// Recheck its identity after CAS conflicts so a new target remains intact.
	}
	return false, errCodexTurnStatePublicationConflict
}
