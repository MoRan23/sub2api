package service

import (
	"context"
	"errors"
	"time"
)

type userPlatformQuotaEngineCache interface {
	MergeUserPlatformQuotaEngineUsage(context.Context, int64, string, *UserPlatformQuotaCacheEntry, time.Duration) error
}

type userPlatformQuotaWindowLoader interface {
	LoadCurrentUserPlatformQuota(context.Context, int64, string, time.Time) (*UserPlatformQuotaRecord, error)
}

func (s *BillingCacheService) loadCurrentUserPlatformQuota(ctx context.Context, userID int64, platform string) (*UserPlatformQuotaRecord, error) {
	if loader, ok := s.userPlatformQuotaRepo.(userPlatformQuotaWindowLoader); ok {
		return loader.LoadCurrentUserPlatformQuota(ctx, userID, platform, time.Now())
	}
	return s.userPlatformQuotaRepo.GetByUserPlatform(ctx, userID, platform)
}

// RefreshUserPlatformQuotaEngineUsage uses an absolute committed component,
// making receipt retries and reordered refreshes idempotent. Ordinary Redis
// usage remains intact, including consumption not yet persisted by the flusher.
func (s *BillingCacheService) RefreshUserPlatformQuotaEngineUsage(ctx context.Context, userID int64, platform string) error {
	if s == nil || s.cache == nil || s.userPlatformQuotaRepo == nil || platform == "" {
		return nil
	}
	cache, ok := s.cache.(userPlatformQuotaEngineCache)
	if !ok {
		return errors.New("platform quota cache cannot merge Engine consumption")
	}
	rec, err := s.userPlatformQuotaRepo.GetByUserPlatform(ctx, userID, platform)
	if err != nil || rec == nil {
		return err
	}
	ttl := time.Minute
	if s.cfg != nil && s.cfg.Billing.UserPlatformQuotaCacheTTLSeconds > 0 {
		ttl = time.Duration(s.cfg.Billing.UserPlatformQuotaCacheTTLSeconds) * time.Second
	}
	return cache.MergeUserPlatformQuotaEngineUsage(ctx, userID, platform, &UserPlatformQuotaCacheEntry{
		SchemaVersion:         UserPlatformQuotaCacheSchemaV1,
		EngineDailyUsageUSD:   rec.EngineDailyUsageUSD,
		EngineWeeklyUsageUSD:  rec.EngineWeeklyUsageUSD,
		EngineMonthlyUsageUSD: rec.EngineMonthlyUsageUSD,
		DailyWindowStart:      rec.DailyWindowStart,
		WeeklyWindowStart:     rec.WeeklyWindowStart,
		MonthlyWindowStart:    rec.MonthlyWindowStart,
	}, ttl)
}
