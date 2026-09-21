package service

import (
	"context"
	"time"
)

// CodexTelemetryNotifier carries lossy wakeups only. Every subscriber must read
// the PostgreSQL policy again; a notification never contains authoritative state.
type CodexTelemetryNotifier interface {
	Notify(context.Context) error
	Subscribe(context.Context, func()) error
}

// SetNotifier may replace a subscription at runtime. The subscription is part of
// the service lifecycle so an idle Redis connection cannot keep Stop blocked.
func (s *CodexTelemetryService) SetNotifier(notifier CodexTelemetryNotifier) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.stopNotifierLocked()
	if s.stopped || notifier == nil {
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.notifier = notifier
	s.notifierCancel = cancel
	s.wg.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.wg.Done()
		for ctx.Err() == nil {
			_ = notifier.Subscribe(ctx, func() {
				if ctx.Err() != nil {
					return
				}
				refreshCtx, refreshCancel := context.WithTimeout(ctx, time.Second)
				defer refreshCancel()
				s.refreshSharedPolicy(refreshCtx)
			})
			// Redis may be unavailable during startup or restarted independently.
			// PostgreSQL polling still makes progress while this subscription retries.
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

// stopNotifierLocked must run while holding s.mu, before waiting for s.wg.
func (s *CodexTelemetryService) stopNotifierLocked() {
	if s.notifierCancel != nil {
		s.notifierCancel()
		s.notifierCancel = nil
	}
	s.notifier = nil
}

// publishPolicyChange is called outside s.mu after the shared policy commits.
// Redis failures do not roll back the policy or fail business requests.
func (s *CodexTelemetryService) publishPolicyChange() {
	if s == nil {
		return
	}
	s.mu.Lock()
	notifier := s.notifier
	stopped := s.stopped
	s.mu.Unlock()
	if notifier == nil || stopped {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = notifier.Notify(ctx)
}
