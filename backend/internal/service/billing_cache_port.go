package service

import (
	"time"
)

// SubscriptionCacheData represents cached subscription data
type SubscriptionCacheData struct {
	Status               string
	ExpiresAt            time.Time
	DailyUsage           float64
	WeeklyUsage          float64
	MonthlyUsage         float64
	TotalLimit           float64
	TotalUsed            float64
	TotalRemaining       float64
	NextQuotaExpireAt    *time.Time
	NextExpiringQuotaUSD float64
	Version              int64
}
