package service

import (
	"context"
	"errors"
	"net/http"
	"time"
)

func codexTurnStateRetainsAccountCooldown(record *CodexTurnStateRecord) bool {
	return record.CollectorPaused || record.LastError == "account_cooldown" || record.LastError == "collector_rate_limited"
}

func clearIdleCodexTurnStateDemand(record *CodexTurnStateRecord) {
	record.DemandReason, record.DemandAt = "", time.Time{}
	record.CollectorAttemptID = ""
	if !codexTurnStateRetainsAccountCooldown(record) {
		record.NextCollectAt = time.Time{}
		record.CollectionStatus, record.CollectionReason = "idle", "waiting_business_response"
	}
}

func completeCodexTurnStateDemand(record *CodexTurnStateRecord, now time.Time) {
	record.DemandReason, record.DemandAt = "", time.Time{}
	record.CollectorExtendedCount, record.CollectorAttemptID = 0, ""
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

// Only trusted categories cross the diagnostic boundary. Error text may contain
// proxy credentials, request URLs, or upstream payloads, even when it resembles
// one of these codes, so it must never be used as a persisted reason.
func codexTurnStateCollectorFailureReason(result CodexTurnStateCollectResult, err error) string {
	switch {
	case result.StatusCode == http.StatusUnauthorized || result.StatusCode == http.StatusForbidden:
		return "collector_auth_rejected"
	case result.StatusCode == http.StatusTooManyRequests || errors.Is(err, errCodexTurnStateCollectorRateLimited):
		return "collector_rate_limited"
	case result.StatusCode == http.StatusProxyAuthRequired:
		return "collector_proxy_auth_required"
	case result.StatusCode >= 500 && result.StatusCode <= 599:
		return "collector_upstream_unavailable"
	case result.StatusCode != 0 && (result.StatusCode < 200 || result.StatusCode >= 300):
		return "collector_http_rejected"
	}
	var transport *codexTurnStateCollectorTransportError
	if errors.As(err, &transport) {
		return transport.code
	}
	if codexTurnStateCollectorTimedOut(err) {
		return "collection_timeout"
	}
	for _, category := range []struct {
		err  error
		code string
	}{
		{ErrCodexTurnStateCollectorProxyUnavailable, "collector_proxy_unavailable"},
		{errCodexTurnStateCollectorTransportFailed, "collector_transport_failed"},
		{errCodexTurnStateCollectorEmptyResponse, "collector_empty_response"},
		{errCodexTurnStateCollectorStreamFailed, "collector_stream_failed"},
		{errCodexTurnStateCollectorResponseFailed, "collector_response_failed"},
		{errCodexTurnStateCollectorResponseIncomplete, "collector_response_incomplete"},
		{errCodexTurnStateCollectorEventTooLarge, "collector_event_too_large"},
	} {
		if errors.Is(err, category.err) {
			return category.code
		}
	}
	if err != nil {
		return "collection_failed"
	}
	return "no_target_state"
}

// The entire collector outcome is one versioned write. In particular, an
// extended response cannot consume the CAS before its retry/error is saved.
func (s *CodexTurnStateService) finishCollectorOutcome(ctx context.Context, owner *Account, key CodexTurnStateKey, base CodexTurnStateRecord, policyRevision string, result CodexTurnStateCollectResult, collectErr error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	if base.CollectorAttemptID == "" || base.CollectorProxyID <= 0 {
		return
	}
	identity := base.cacheIdentity()
	for range 3 {
		if !s.authoritativeModelPolicyMatches(ctx, key.Model, policyRevision) {
			return
		}
		record, err := s.repo.Get(ctx, key)
		if err != nil || record == nil || record.DemandReason == "" || record.CollectorPaused {
			return
		}
		if record.CollectorAttemptID != base.CollectorAttemptID || record.CollectorProxyID != base.CollectorProxyID {
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
		proxyIDs := CodexTurnStateCollectorProxyIDs(CodexTurnStateConfigForAccount(current))
		if !codexTurnStateProxyAllowed(proxyIDs, base.CollectorProxyID) {
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
		} else if best != "" && target.IssuedAt.Equal(record.IssuedAt) && record.EncryptedToken != "" && record.ExpiresAt.After(now) {
			if record.ExpiresAt.After(now.Add(CodexTurnStateRefreshAhead)) {
				record.RefreshReason = ""
				completeCodexTurnStateDemand(record, now)
				accepted = true
			} else {
				targetStillExpiring = true
			}
		}
		if !accepted {
			concurrentCooldownReason := ""
			if !record.NextCollectAt.Equal(base.NextCollectAt) && record.NextCollectAt.After(now) && codexTurnStateRetainsAccountCooldown(record) {
				concurrentCooldownReason = record.LastError
			}
			if best == "" && !extended.IssuedAt.IsZero() {
				rotateCodexTurnStateProxy(record, proxyIDs)
			}
			if best == "" && !extended.IssuedAt.IsZero() && !extended.IssuedAt.Before(record.IssuedAt) && !(record.EncryptedToken != "" && record.ExpiresAt.After(now)) {
				record.EncryptedToken, record.ExpiresAt = "", time.Time{}
				record.Shape, record.TokenLength, record.CipherBlocks = extended.Shape, extended.TokenLength, extended.CipherBlocks
				record.IssuedAt = extended.IssuedAt
				record.RefreshReason = "extended_shape"
			}
			record.LastError = codexTurnStateCollectorFailureReason(result, outcomeErr)
			if record.LastError == "no_target_state" && targetStillExpiring {
				record.LastError = "target_still_expiring"
			}
			if record.LastError == "collector_auth_rejected" {
				record.CollectorPaused = true
			}
			// Only a completed response without a usable target gets the fixed
			// shape-retry delay. Transport/upstream errors return to the normal
			// one-second scheduler, while explicit cooldowns below still apply.
			retry := time.Duration(0)
			if record.LastError == "no_target_state" || record.LastError == "target_still_expiring" {
				retry = CodexTurnStateRetryInterval
			}
			// Keep the reason together with a concurrently established account
			// cooldown, so a later natural success cannot clear its retry fence.
			// Authentication rejection still takes precedence and pauses collection.
			if concurrentCooldownReason != "" && !record.CollectorPaused {
				record.LastError = concurrentCooldownReason
			}
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
		record.CollectorAttemptID = ""
		ok, saveErr := s.repo.SaveCAS(ctx, *record, record.Version)
		if saveErr != nil {
			return
		}
		if ok {
			if accepted {
				s.cancelCollectorAttempt(ctx, key, base.CollectorAttemptID)
			}
			return
		}
	}
}
