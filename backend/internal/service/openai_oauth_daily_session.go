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

// OAuthDailyOSRoots pairs the independent stream and synchronous roots for one
// operating system. Both roots remain stable for the pool's UTC+8 business day.
type OAuthDailyOSRoots struct {
	StreamSessionID string `json:"stream_session_id"`
	SyncSessionID   string `json:"sync_session_id"`
}

// OAuthDailySessionPool is one account's generation for a UTC+8 calendar day.
// OSRoots contains the Windows, macOS, and Linux pairs after OS provisioning.
// The legacy fields remain compatibility mirrors: streaming slots are ordered
// Windows, macOS, Linux, and SyncSessionID belongs to DefaultOS.
type OAuthDailySessionPool struct {
	AccountID        int64                                `json:"account_id"`
	BusinessDate     string                               `json:"business_date"`
	Generation       string                               `json:"generation"`
	StreamSessionIDs [OAuthDailyStreamSessionCount]string `json:"stream_session_ids"`
	SyncSessionID    string                               `json:"sync_session_id"`
	OSRoots          map[string]OAuthDailyOSRoots         `json:"os_roots,omitempty"`
	DefaultOS        string                               `json:"default_os,omitempty"`
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

// OAuthDailySessionOSRepository is the opt-in extension for ordinary OAuth
// accounts. defaultOS is the credential owner's persisted default, not the
// current request's selected OS; it decides which OS inherits a legacy sync
// root on first provisioning. Callers supply their frozen server receive time.
type OAuthDailySessionOSRepository interface {
	GetOrCreateOAuthDailySessionPoolForOS(ctx context.Context, accountID int64, defaultOS string, now time.Time) (OAuthDailySessionPool, error)
}

// OAuthDailySessionPoolReader exposes a strictly read-only lookup used by
// administrative views. Implementations must never provision a missing pool.
type OAuthDailySessionPoolReader interface {
	ListOAuthDailySessionPools(ctx context.Context, accountIDs []int64, now time.Time) (map[int64]OAuthDailySessionPool, error)
}
