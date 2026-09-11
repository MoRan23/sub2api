package service

import (
	"context"
	"time"
)

const OAuthDailyStreamSessionCount = 3

var OAuthDailyLocation = func() *time.Location {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return time.FixedZone("UTC+8", 8*60*60)
	}
	return loc
}()

func OAuthDailyBusinessDate(now time.Time) string {
	return now.In(OAuthDailyLocation).Format("2006-01-02")
}

// OAuthDailySessionPool is one account's generation for a UTC+8 calendar day.
// StreamSessionIDs are stable roots; SyncSessionID is an independent root for
// non-streaming requests and account connection tests.
type OAuthDailySessionPool struct {
	AccountID        int64
	BusinessDate     string
	Generation       string
	StreamSessionIDs [OAuthDailyStreamSessionCount]string
	SyncSessionID    string
}

type OAuthDailySessionAffinity struct {
	AccountID         int64
	APIKeyID          int64
	LogicalSessionKey string
	BusinessDate      string
	Generation        string
	SlotIndex         int
	StreamSessionID   string
}

// OAuthDailySessionRepository atomically provisions daily roots and persists
// the random-first, sticky-later slot assignment for logical client sessions.
type OAuthDailySessionRepository interface {
	GetOrCreateOAuthDailySessionPool(ctx context.Context, accountID int64, now time.Time) (OAuthDailySessionPool, error)
	GetOrCreateOAuthDailySessionAffinity(ctx context.Context, accountID, apiKeyID int64, logicalSessionKey string, now time.Time) (OAuthDailySessionAffinity, error)
	ReleaseOAuthDailySessionGeneration(ctx context.Context, accountID int64, generation string) error
	CleanupOAuthDailySessionGenerations(ctx context.Context, before time.Time) (int, error)
}
