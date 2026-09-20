package service

import (
	"context"
	"sort"
	"time"
)

const (
	codexTurnStatePendingPublicationLimit = 4096
	codexTurnStatePendingPublicationBatch = 32
)

// This short-lived retry item is neither history nor an injection snapshot.
// The original encrypted cache identity is used only to compare a fresh database
// record before invalidation. No response token, body, or attempt is retained.
type codexTurnStatePendingPublication struct {
	key            CodexTurnStateKey
	shape          CodexTurnStateShape
	identity       codexTurnStateCacheIdentity
	version        int64
	policyRevision string
	sentAt         time.Time
	observedAt     time.Time
	// Scheduling fields are protected by the service mutex.
	nextAttemptAt time.Time
	inFlight      bool
}

func (p *codexTurnStatePendingPublication) expired(now time.Time) bool {
	return !p.shape.ExpiresAt.After(now) || !p.sentAt.Add(CodexTurnStateActiveWindow).After(now)
}

// Transfer only the evidence needed by publication after successful delivery and
// physical-send confirmation. A later callback may retry the existing entry.
func (s *CodexTurnStateService) retainCodexTurnStateAnomaly(a *CodexTurnStateAttempt) *codexTurnStatePendingPublication {
	a.mu.Lock()
	if !a.Enabled || !a.finished || !a.historyDelivered || !a.historyPhysicalBound || a.businessSentAt.IsZero() || !a.anomalyPublication {
		a.mu.Unlock()
		return nil
	}
	var candidate *codexTurnStatePendingPublication
	if a.pendingAnomaly != nil {
		candidate = &codexTurnStatePendingPublication{key: a.key, shape: *a.pendingAnomaly, identity: a.baseCacheIdentity,
			version: a.baseVersion, policyRevision: a.policyRevision, sentAt: a.businessSentAt, observedAt: a.safeObservation.ObservedAt}
		a.pendingAnomaly = nil
	}
	key := a.key
	a.mu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped || (s.ctx != nil && s.ctx.Err() != nil) {
		return nil
	}
	now := s.now()
	for key, entry := range s.pendingPublications {
		if entry.expired(now) {
			delete(s.pendingPublications, key)
		}
	}
	previous := s.pendingPublications[key]
	if candidate == nil || candidate.expired(now) {
		return previous
	}
	if previous != nil && (previous.version > candidate.version || (previous.version == candidate.version && !candidate.observedAt.After(previous.observedAt))) {
		return previous
	}
	if s.pendingPublications == nil {
		s.pendingPublications = make(map[CodexTurnStateKey]*codexTurnStatePendingPublication)
	}
	if previous == nil && len(s.pendingPublications) >= codexTurnStatePendingPublicationLimit {
		// Keep the most recent bounded evidence, just as the passive history ring
		// does; first discard entries that have already exhausted their lifetime.
		var oldest *codexTurnStatePendingPublication
		for _, entry := range s.pendingPublications {
			if oldest == nil || entry.observedAt.Before(oldest.observedAt) {
				oldest = entry
			}
		}
		delete(s.pendingPublications, oldest.key)
	}
	s.pendingPublications[key] = candidate
	return candidate
}

func (s *CodexTurnStateService) processCodexTurnStateAnomaly(ctx context.Context, pending *codexTurnStatePendingPublication, immediate bool) error {
	s.mu.Lock()
	if s.pendingPublications[pending.key] != pending || pending.inFlight || (!immediate && pending.nextAttemptAt.After(s.now())) {
		s.mu.Unlock()
		return nil
	}
	if pending.expired(s.now()) {
		delete(s.pendingPublications, pending.key)
		s.mu.Unlock()
		return nil
	}
	pending.inFlight = true
	s.mu.Unlock()
	err := s.repo.MarkBusinessSent(ctx, pending.key, pending.sentAt)
	published := false
	if err == nil {
		published, err = s.publishCodexTurnStateAnomaly(ctx, pending.key, pending.shape, pending.version, false, pending.policyRevision, pending.observedAt, pending.identity)
	}
	s.mu.Lock()
	if s.pendingPublications[pending.key] == pending {
		pending.inFlight = false
		if err == nil || pending.expired(s.now()) {
			delete(s.pendingPublications, pending.key)
		} else {
			pending.nextAttemptAt = s.now().Add(time.Second)
		}
	}
	s.mu.Unlock()
	if published {
		s.enqueue(ctx, pending.key)
	}
	return err
}

func (s *CodexTurnStateService) retryCodexTurnStateAnomalies(ctx context.Context) {
	now := s.now()
	s.mu.Lock()
	var due []*codexTurnStatePendingPublication
	for key, pending := range s.pendingPublications {
		if pending.expired(now) {
			delete(s.pendingPublications, key)
		} else if !pending.inFlight && !pending.nextAttemptAt.After(now) {
			due = append(due, pending)
		}
	}
	// Fair scheduling ensures a repeatedly failing account cannot starve entries
	// that have not been retried during this outage.
	sort.Slice(due, func(i, j int) bool { return due[i].nextAttemptAt.Before(due[j].nextAttemptAt) })
	if len(due) > codexTurnStatePendingPublicationBatch {
		due = due[:codexTurnStatePendingPublicationBatch]
	}
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	for _, pending := range due {
		if ctx.Err() != nil {
			return
		}
		_ = s.processCodexTurnStateAnomaly(ctx, pending, false)
	}
}
