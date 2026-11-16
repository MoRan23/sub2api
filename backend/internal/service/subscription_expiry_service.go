package service

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	subscriptionExpiryReminderSMTPWarningInterval = time.Minute
	// subscriptionExpiryReminderLeaderLockKey gates the per-cycle reminder scan so
	// that only one instance queues due subscriptions per scan cycle.
	subscriptionExpiryReminderLeaderLockKey = "subscription:expiry:reminder:leader"
	// subscriptionExpiryReminderLeaderLockTTL bounds crash recovery; the scan can
	// page through many subscriptions, so keep it comfortably above one cycle.
	subscriptionExpiryReminderLeaderLockTTL = 5 * time.Minute
)

const subscriptionQuotaEventCleanupBatchSize = 1000

// SubscriptionExpiryService periodically updates expired subscription status.
type SubscriptionExpiryService struct {
	userSubRepo              UserSubscriptionRepository
	settingRepo              SettingRepository
	notificationEmailService *NotificationEmailService
	interval                 time.Duration
	stopCh                   chan struct{}
	stopOnce                 sync.Once
	startOnce                sync.Once
	runCtx                   context.Context
	runCancel                context.CancelFunc
	wg                       sync.WaitGroup

	lockCache  LeaderLockCache
	db         *sql.DB
	instanceID string

	smtpWarningMu   sync.Mutex
	lastSMTPWarning time.Time
}

func NewSubscriptionExpiryService(userSubRepo UserSubscriptionRepository, interval time.Duration) *SubscriptionExpiryService {
	ctx, cancel := context.WithCancel(context.Background())
	return &SubscriptionExpiryService{
		userSubRepo: userSubRepo,
		interval:    interval,
		stopCh:      make(chan struct{}),
		instanceID:  uuid.NewString(),
		runCtx:      ctx,
		runCancel:   cancel,
	}
}

// SetLeaderLock injects the reminder outbox DB and the cache used to elect one
// scanner. Without Redis, scanning uses the database advisory-lock fallback.
func (s *SubscriptionExpiryService) SetLeaderLock(lockCache LeaderLockCache, db *sql.DB) {
	if s == nil {
		return
	}
	s.lockCache = lockCache
	s.db = db
}

func (s *SubscriptionExpiryService) SetSettingRepository(settingRepo SettingRepository) {
	s.settingRepo = settingRepo
}

func (s *SubscriptionExpiryService) SetNotificationEmailService(notificationEmailService *NotificationEmailService) {
	s.notificationEmailService = notificationEmailService
}

func (s *SubscriptionExpiryService) Start() {
	if s == nil || s.userSubRepo == nil || s.interval <= 0 {
		return
	}
	s.startOnce.Do(func() {
		if s.db != nil && s.notificationEmailService != nil {
			for range 2 {
				s.wg.Add(1)
				go s.reminderWorker()
			}
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			ticker := time.NewTicker(s.interval)
			defer ticker.Stop()

			s.runOnce()
			for {
				select {
				case <-ticker.C:
					s.runOnce()
				case <-s.stopCh:
					return
				}
			}
		}()
	})
}

func (s *SubscriptionExpiryService) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() {
		if s.runCancel != nil {
			s.runCancel()
		}
		close(s.stopCh)
	})
	s.wg.Wait()
}

func (s *SubscriptionExpiryService) runOnce() {
	parent := s.runCtx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)

	updated, err := s.userSubRepo.BatchUpdateExpiredStatus(ctx)
	cancel()
	if err != nil {
		log.Printf("[SubscriptionExpiry] Update expired subscriptions failed: %v", err)
	}
	if updated > 0 {
		log.Printf("[SubscriptionExpiry] Updated %d expired subscriptions", updated)
	}
	ctx, cancel = context.WithTimeout(parent, 10*time.Second)
	s.cleanupExpiredQuotaEvents(ctx)
	cancel()
	ctx, cancel = context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	s.sendExpiryReminders(ctx)
}

func (s *SubscriptionExpiryService) cleanupExpiredQuotaEvents(ctx context.Context) bool {
	if s == nil || s.userSubRepo == nil {
		return true
	}
	cleanupBefore := time.Now().UTC()
	var deletedTotal int64
	for {
		if err := ctx.Err(); err != nil {
			if deletedTotal > 0 {
				log.Printf("[SubscriptionExpiry] Deleted %d expired quota events before stop", deletedTotal)
			}
			return false
		}

		deleted, err := s.userSubRepo.DeleteExpiredQuotaEventsBatch(ctx, cleanupBefore, subscriptionQuotaEventCleanupBatchSize)
		if err != nil {
			log.Printf("[SubscriptionExpiry] Delete expired quota events failed: %v", err)
			return false
		}
		if deleted == 0 {
			break
		}
		deletedTotal += deleted
	}
	if deletedTotal > 0 {
		log.Printf("[SubscriptionExpiry] Deleted %d expired quota events", deletedTotal)
	}
	return true
}

func (s *SubscriptionExpiryService) sendExpiryReminders(ctx context.Context) {
	if s == nil || s.userSubRepo == nil || s.notificationEmailService == nil || ctx.Err() != nil {
		return
	}
	if !s.expiryReminderEnabled(ctx) {
		return
	}
	if !s.smtpConfigured(ctx) {
		return
	}
	if s.db == nil {
		return
	}

	// Only the leader pages due subscription projections into the durable outbox.
	// Delivery has independent cluster-wide admission and never holds this lock.
	release, ok := tryAcquireSingletonLeaderLock(ctx, s.lockCache, s.db, subscriptionExpiryReminderLeaderLockKey, s.instanceID, subscriptionExpiryReminderLeaderLockTTL)
	if !ok {
		return
	}
	defer release()
	if err := s.enqueueExpiryReminders(ctx, time.Now().UTC()); err != nil {
		log.Printf("[SubscriptionExpiry] Enqueue reminders failed: %v", err)
	}
}

func (s *SubscriptionExpiryService) expiryReminderEnabled(ctx context.Context) bool {
	if s == nil || s.settingRepo == nil {
		return true
	}
	value, err := s.settingRepo.GetValue(ctx, SettingKeySubscriptionExpiryNotifyEnabled)
	if err != nil {
		if errors.Is(err, ErrSettingNotFound) {
			return true
		}
		log.Printf("[SubscriptionExpiry] Read expiry reminder switch failed: %v", err)
		return false
	}
	return !isFalseSettingValue(value)
}

func (s *SubscriptionExpiryService) smtpConfigured(ctx context.Context) bool {
	if s == nil || s.notificationEmailService == nil || s.notificationEmailService.emailService == nil {
		return false
	}
	_, err := s.notificationEmailService.emailService.GetSMTPConfig(ctx)
	if err == nil {
		return true
	}
	if errors.Is(err, ErrEmailNotConfigured) {
		s.smtpWarningMu.Lock()
		defer s.smtpWarningMu.Unlock()
		now := time.Now()
		if s.lastSMTPWarning.IsZero() || now.Sub(s.lastSMTPWarning) >= subscriptionExpiryReminderSMTPWarningInterval {
			log.Printf("[SubscriptionExpiry] SMTP is not configured; skipping expiry reminders")
			s.lastSMTPWarning = now
		}
		return false
	}
	log.Printf("[SubscriptionExpiry] Read SMTP configuration failed; skipping expiry reminders: %v", err)
	return false
}
