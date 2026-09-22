package service

import (
	"context"
	"errors"
	"net/http"
	"time"
)

func codexTurnStateRetainsAccountCooldown(record *CodexTurnStateRecord) bool {
	return record.CollectorPaused || codexTurnStateHasAccountCooldown(record)
}

func codexTurnStateHasAccountCooldown(record *CodexTurnStateRecord) bool {
	return record.LastError == "account_cooldown" || record.LastError == "collector_rate_limited"
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
	record.DemandReason, record.DemandAt = "refresh", now
	record.CollectorExtendedCount, record.CollectorAttemptID = 0, ""
	if record.CollectorPaused {
		record.CollectionStatus, record.CollectionReason = "paused", record.LastError
		return
	}
	if codexTurnStateRetainsAccountCooldown(record) && record.NextCollectAt.After(now) {
		record.CollectionStatus, record.CollectionReason = "backoff", record.LastError
		return
	}
	record.LastError, record.CollectionStatus, record.CollectionReason = "", "scheduled", "refresh"
	record.NextCollectAt = now.Add(CodexTurnStateCollectInterval)
	if record.CookieBundleExpiresAt != nil && record.CookieBundleExpiresAt.Before(record.NextCollectAt) {
		record.NextCollectAt = *record.CookieBundleExpiresAt
	}
}

func codexTurnStateCookieExpired(record *CodexTurnStateRecord, now time.Time) bool {
	return record != nil && record.EncryptedToken != "" && record.CookieBundleExpiresAt != nil && !record.CookieBundleExpiresAt.After(now)
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
	defer result.discardCookies()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	if base.CollectorAttemptID == "" || base.CollectorProxyID <= 0 {
		return
	}
	// A real account-wide Retry-After remains authoritative even if the request's
	// credential slot was rebound, revoked, or satisfied by a business response.
	if cooldowns, ok := s.repo.(CodexTurnStateCooldownRepository); ok &&
		codexTurnStateCollectorFailureReason(result, collectErr) == "collector_rate_limited" {
		retry := result.RetryAfter
		if retry < time.Second {
			retry = time.Second
		}
		if err := cooldowns.ExtendCollectorCooldown(ctx, key.OwnerAccountID, s.now().Add(retry)); err != nil {
			return
		}
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
		current, err := s.currentOwner(ctx, key.OwnerAccountID, key.OSFamily)
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
		// Keep candidate shape selection separate from model admission: a rejected
		// target must not turn a response into an extended-only rotation signal.
		modelMismatch := collectErr == nil && result.StatusCode >= 200 && result.StatusCode < 300 && result.ModelMismatch()
		hasTargetCandidate := best != ""
		var bundle codexTurnStateCookiePublication
		if !modelMismatch && best != "" {
			bundle, outcomeErr = s.prepareCollectorCookiePublication(key, current.OpenAIOAuthAuthorizationGeneration, &result, target.ExpiresAt)
			if outcomeErr != nil {
				best = ""
			}
		}
		if !modelMismatch && best != "" && (target.IssuedAt.After(record.IssuedAt) || (record.EncryptedToken == "" && target.IssuedAt.Equal(record.IssuedAt))) {
			encrypted, encryptErr := s.encryptor.Encrypt(best)
			if encryptErr == nil {
				record.EncryptedToken, record.Source, record.Shape = encrypted, "collector", target.Shape
				applyCodexTurnStateCookiePublication(record, bundle)
				record.OSFamily = codexTurnStateOS(owner)
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
		} else if !modelMismatch && best != "" && target.IssuedAt.Equal(record.IssuedAt) && record.EncryptedToken != "" && record.ExpiresAt.After(now) {
			cachedToken, decryptErr := s.encryptor.Decrypt(record.EncryptedToken)
			if decryptErr != nil || cachedToken != best {
				// Equal issuance seconds do not prove that a ticket and Cookie set
				// belong to the same response. Preserve the existing pair intact.
				best = ""
				outcomeErr = decryptErr
			} else {
				applyCodexTurnStateCookiePublication(record, bundle)
				record.OSFamily = codexTurnStateOS(owner)
				if record.ExpiresAt.After(now.Add(CodexTurnStateRefreshAhead)) {
					record.RefreshReason = ""
					completeCodexTurnStateDemand(record, now)
					accepted = true
				} else {
					targetStillExpiring = true
				}
			}
		}
		if !accepted {
			concurrentCooldownReason := ""
			if !record.NextCollectAt.Equal(base.NextCollectAt) && record.NextCollectAt.After(now) && codexTurnStateRetainsAccountCooldown(record) {
				concurrentCooldownReason = record.LastError
			}
			if !hasTargetCandidate && !extended.IssuedAt.IsZero() {
				rotateCodexTurnStateProxy(record, proxyIDs)
			}
			if !hasTargetCandidate && !extended.IssuedAt.IsZero() && !extended.IssuedAt.Before(record.IssuedAt) && !(record.EncryptedToken != "" && record.ExpiresAt.After(now)) {
				record.EncryptedToken, record.ExpiresAt = "", time.Time{}
				record.EncryptedCookieBundle, record.CookieBundleExpiresAt = "", nil
				record.Shape, record.TokenLength, record.CipherBlocks = extended.Shape, extended.TokenLength, extended.CipherBlocks
				record.IssuedAt = extended.IssuedAt
				record.RefreshReason = "extended_shape"
			}
			record.LastError = codexTurnStateCollectorFailureReason(result, outcomeErr)
			if modelMismatch {
				record.LastError = "model_mismatch"
			} else if record.LastError == "no_target_state" && targetStillExpiring {
				record.LastError = "target_still_expiring"
			}
			if record.LastError == "collector_auth_rejected" {
				record.CollectorPaused = true
			}
			// Only a completed response without a usable target gets the fixed
			// shape-retry delay. Transport/upstream errors return to the normal
			// one-second scheduler, while explicit cooldowns below still apply.
			retry := time.Duration(0)
			if record.LastError == "no_target_state" || record.LastError == "target_still_expiring" || record.LastError == "model_mismatch" {
				retry = CodexTurnStateRetryInterval
			}
			// Keep the reason together with a concurrently established account
			// cooldown, so a later natural success cannot clear its retry fence.
			// Authentication rejection still takes precedence and pauses collection.
			if concurrentCooldownReason != "" && !record.CollectorPaused {
				record.LastError = concurrentCooldownReason
			}
			if codexTurnStateCollectorFailureReason(result, outcomeErr) == "collector_rate_limited" && result.RetryAfter > retry {
				retry = result.RetryAfter
			}
			next := now.Add(retry)
			// Replace this attempt's crash-recovery reservation on completion, but
			// retain a later retry fence introduced by a concurrent writer.
			if !record.NextCollectAt.Equal(base.NextCollectAt) && record.NextCollectAt.After(next) {
				next = record.NextCollectAt
			}
			record.NextCollectAt = next
			for _, until := range []*time.Time{current.RateLimitResetAt, owner.RateLimitResetAt} {
				if until != nil && until.After(record.NextCollectAt) {
					record.NextCollectAt = *until
				}
			}
			record.CollectionStatus, record.CollectionReason = "pending", record.LastError
			if record.NextCollectAt.After(now) {
				record.CollectionStatus = "backoff"
			}
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
			if accepted || targetStillExpiring {
				result.commitCookies(ctx)
			}
			if accepted {
				s.cancelCollectorAttempt(ctx, key, base.CollectorAttemptID)
			}
			return
		}
	}
}
