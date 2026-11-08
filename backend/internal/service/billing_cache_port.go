package service

import (
	"context"
	"errors"
	"time"
)

// ErrSubscriptionCacheStale means a committed mutation superseded a DB load.
var ErrSubscriptionCacheStale = errors.New("subscription cache generation changed")

// SubscriptionCacheGeneration fences DB snapshots before asynchronous publication.
// The token must be obtained before the database read, not when the queued write runs.
type SubscriptionCacheGeneration interface {
	GetSubscriptionCacheGeneration(context.Context, int64, int64) (string, error)
}

// SubscriptionCacheData represents cached subscription data
type SubscriptionCacheData struct {
	Generation           string
	Status               string
	ExpiresAt            *time.Time
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
