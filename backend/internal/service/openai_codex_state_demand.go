package service

import (
	"context"
	"errors"
	"time"
)

func codexTurnStateRetainsAccountCooldown(record *CodexTurnStateRecord) bool {
	return record.CollectorPaused || record.LastError == "account_cooldown" || record.LastError == "collector_rate_limited"
}

func clearIdleCodexTurnStateDemand(record *CodexTurnStateRecord) {
	record.DemandReason, record.DemandAt = "", time.Time{}
	if !codexTurnStateRetainsAccountCooldown(record) {
		record.NextCollectAt = time.Time{}
		record.CollectionStatus, record.CollectionReason = "idle", "waiting_business_response"
	}
}

func completeCodexTurnStateDemand(record *CodexTurnStateRecord, now time.Time) {
	record.DemandReason, record.DemandAt = "", time.Time{}
	if record.CollectorPaused {
		record.CollectionStatus, record.CollectionReason = "paused", record.LastError
		return
	}
	if codexTurnStateRetainsAccountCooldown(record) && record.NextCollectAt.After(now) {
		record.CollectionStatus, record.CollectionReason = "backoff", record.LastError
		return
	}
	record.LastError, record.CollectionStatus, record.CollectionReason = "", "idle", ""
	record.NextCollectAt = time.Time{}
}

// The entire collector outcome is one versioned write. In particular, an
// extended response cannot consume the CAS before its retry/error is saved.
func (s *CodexTurnStateService) finishCollectorOutcome(ctx context.Context, owner *Account, key CodexTurnStateKey, base CodexTurnStateRecord, policyRevision string, result CodexTurnStateCollectResult, collectErr error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	identity := base.cacheIdentity()
	for range 3 {
		if !s.authoritativeModelPolicyMatches(ctx, key.Model, policyRevision) {
			return
		}
		record, err := s.repo.Get(ctx, key)
		if err != nil || record == nil || record.DemandReason == "" || record.CollectorPaused {
			return
		}
		// Scheduling writes and another abnormal business response may advance the
		// version while collection runs. Only a different nonempty cache preempts
		// this result; a remaining demand with no cache can still be satisfied.
		if record.EncryptedToken != "" && !identity.matches(record) {
			return
		}
		current, err := s.currentOwner(ctx, key.OwnerAccountID)
		now := s.now()
		if err != nil || !codexTurnStateEligible(current) || !CodexTurnStateConfigForAccount(current).Enabled || CodexTurnStateGenerationForAccount(current) != key.Generation ||
			current.Status != StatusActive || !current.Schedulable || (current.ExpiresAt != nil && !current.ExpiresAt.After(now)) {
			return
		}
		outcomeErr := collectErr
		var best string
		var target, extended CodexTurnStateShape
		if collectErr == nil && result.StatusCode >= 200 && result.StatusCode < 300 {
			for _, token := range result.Tokens {
				shape, parseErr := ParseCodexTurnState(token, CodexTurnStateAccountTypeForAccount(current), now)
				if parseErr != nil {
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
		}
		accepted := false
		targetStillExpiring := false
		if best != "" && (target.IssuedAt.After(record.IssuedAt) || (record.EncryptedToken == "" && target.IssuedAt.Equal(record.IssuedAt))) {
			encrypted, encryptErr := s.encryptor.Encrypt(best)
			if encryptErr == nil {
				record.EncryptedToken, record.Source, record.Shape = encrypted, "collector", target.Shape
				record.IssuedAt, record.ExpiresAt = target.IssuedAt, target.ExpiresAt
				record.TokenLength, record.CipherBlocks = target.TokenLength, target.CipherBlocks
				if target.ExpiresAt.After(now.Add(CodexTurnStateRefreshAhead)) {
					record.RefreshReason = ""
					completeCodexTurnStateDemand(record, now)
					accepted = true
				} else {
					targetStillExpiring = true
					record.DemandReason, record.RefreshReason = "expiring", "expiring"
				}
			} else {
				outcomeErr = encryptErr
			}
		}
		if !accepted {
			concurrentCooldownReason := ""
			if !record.NextCollectAt.Equal(base.NextCollectAt) && record.NextCollectAt.After(now) && codexTurnStateRetainsAccountCooldown(record) {
				concurrentCooldownReason = record.LastError
			}
			if !extended.IssuedAt.IsZero() && !extended.IssuedAt.Before(record.IssuedAt) && !(record.EncryptedToken != "" && record.ExpiresAt.After(now)) {
				record.EncryptedToken, record.ExpiresAt = "", time.Time{}
				record.Shape, record.TokenLength, record.CipherBlocks = extended.Shape, extended.TokenLength, extended.CipherBlocks
				record.IssuedAt = extended.IssuedAt
				record.RefreshReason = "extended_shape"
			}
			record.LastError = "no_target_state"
			if targetStillExpiring {
				record.LastError = "target_still_expiring"
			}
			if outcomeErr != nil {
				record.LastError = "collection_failed"
			}
			if errors.Is(outcomeErr, ErrCodexTurnStateCollectorProxyUnavailable) {
				record.LastError = "collector_proxy_unavailable"
			}
			if errors.Is(outcomeErr, context.DeadlineExceeded) {
				record.LastError = "collection_timeout"
			}
			switch {
			case result.StatusCode == 401 || result.StatusCode == 403:
				record.CollectorPaused, record.LastError = true, "collector_auth_rejected"
			case result.StatusCode == 429 || errors.Is(outcomeErr, errCodexTurnStateCollectorRateLimited):
				record.LastError = "collector_rate_limited"
			}
			// Keep the reason together with a concurrently established account
			// cooldown, so a later natural success cannot clear its retry fence.
			// Authentication rejection still takes precedence and pauses collection.
			if concurrentCooldownReason != "" && !record.CollectorPaused {
				record.LastError = concurrentCooldownReason
			}
			retry := CodexTurnStateRetryInterval
			if result.RetryAfter > retry {
				retry = result.RetryAfter
			}
			next := now.Add(retry)
			// Replace this attempt's crash-recovery reservation on completion, but
			// retain a later retry fence introduced by a concurrent writer.
			if !record.NextCollectAt.Equal(base.NextCollectAt) && record.NextCollectAt.After(next) {
				next = record.NextCollectAt
			}
			record.NextCollectAt = next
			for _, until := range []*time.Time{current.RateLimitResetAt, current.OverloadUntil, current.TempUnschedulableUntil, owner.RateLimitResetAt, owner.OverloadUntil, owner.TempUnschedulableUntil} {
				if until != nil && until.After(record.NextCollectAt) {
					record.NextCollectAt = *until
				}
			}
			record.CollectionStatus, record.CollectionReason = "backoff", record.LastError
			if record.CollectorPaused {
				record.CollectionStatus = "paused"
			}
		}
		if !s.authoritativeModelPolicyMatches(ctx, key.Model, policyRevision) {
			return
		}
		record.ModelPolicyRevision = policyRevision
		ok, saveErr := s.repo.SaveCAS(ctx, *record, record.Version)
		if saveErr != nil {
			return
		}
		if ok {
			if accepted {
				s.cancelAndNotify(ctx, key)
			}
			return
		}
	}
}
