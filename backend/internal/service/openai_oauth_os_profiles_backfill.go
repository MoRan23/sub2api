package service

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// OpenAIOAuthOSProfileBackfill repairs legacy accounts after startup. The
// repository pages accounts and uses the same row locks as request-time repair.
type OpenAIOAuthOSProfileBackfill struct {
	repo   OpenAIOAuthOSProfilesBackfiller
	cancel context.CancelFunc
	done   chan struct{}
	start  sync.Once
	stop   sync.Once
}

func NewOpenAIOAuthOSProfileBackfill(repo AccountRepository) *OpenAIOAuthOSProfileBackfill {
	backfiller, _ := repo.(OpenAIOAuthOSProfilesBackfiller)
	return &OpenAIOAuthOSProfileBackfill{repo: backfiller, done: make(chan struct{})}
}

func (s *OpenAIOAuthOSProfileBackfill) Start() {
	if s == nil {
		return
	}
	s.start.Do(func() {
		ctx, cancel := context.WithCancel(context.Background())
		s.cancel = cancel
		go func() {
			defer close(s.done)
			if s.repo == nil {
				return
			}
			for {
				err := s.repo.BackfillOpenAIOAuthOSProfiles(ctx)
				if err == nil || ctx.Err() != nil {
					return
				}
				// Do not log repository errors: they may include database arguments.
				slog.Warn("openai_oauth_os_profile_backfill_failed", "retry_seconds", 30)
				timer := time.NewTimer(30 * time.Second)
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
			}
		}()
	})
}

func (s *OpenAIOAuthOSProfileBackfill) Stop() {
	if s == nil {
		return
	}
	s.stop.Do(func() {
		// The provider always starts the worker before publishing it.
		if s.cancel != nil {
			s.cancel()
			<-s.done
		}
	})
}
