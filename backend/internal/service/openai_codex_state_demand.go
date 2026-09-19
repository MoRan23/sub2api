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
func (s *CodexTurnStateService) finishCollectorOutcome(ctx context.Context, owner *Account, key CodexTurnStateKey, expected int64, policyRevision string, result CodexTurnStateCollectResult, collectErr error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	if !s.authoritativeModelPolicyMatches(ctx, key.Model, policyRevision) {
		return
	}
	if businessActive, checkErr := s.repo.HasBusiness(ctx, key, s.now()); checkErr != nil || businessActive {
		return
	}
	record, err := s.repo.Get(ctx, key)
	if err != nil || record == nil || record.Version != expected || record.DemandReason == "" {
		return
	}
	current, err := s.currentOwner(ctx, key.OwnerAccountID)
	if err != nil || !codexTurnStateEligible(current) || !CodexTurnStateConfigForAccount(current).Enabled || CodexTurnStateGenerationForAccount(current) != key.Generation {
		return
	}
	now := s.now()
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
			collectErr = encryptErr
		}
	}
	if !accepted {
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
		if collectErr != nil {
			record.LastError = "collection_failed"
		}
		if errors.Is(collectErr, ErrCodexTurnStateCollectorProxyUnavailable) {
			record.LastError = "collector_proxy_unavailable"
		}
		if errors.Is(collectErr, context.DeadlineExceeded) {
			record.LastError = "collection_timeout"
		}
		if errors.Is(collectErr, context.Canceled) {
			record.LastError = "business_preempted"
		}
		switch {
		case result.StatusCode == 401 || result.StatusCode == 403:
			record.CollectorPaused, record.LastError = true, "collector_auth_rejected"
		case result.StatusCode == 429 || errors.Is(collectErr, errCodexTurnStateCollectorRateLimited):
			record.LastError = "collector_rate_limited"
		}
		retry := CodexTurnStateRetryInterval
		if result.RetryAfter > retry {
			retry = result.RetryAfter
		}
		record.NextCollectAt = now.Add(retry)
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
	record.CollectorPublication = true
	if ok, saveErr := s.repo.SaveCAS(ctx, *record, expected); saveErr == nil && ok && accepted {
		s.cancelAndNotify(ctx, key)
	}
}
