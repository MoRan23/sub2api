package service

import "time"

// A proxy-only configuration change keeps a previously admitted target without
// extending its lifetime. Its marker is cleared when a new business result
// changes the cache or the ordinary expiry demand starts.
func codexTurnStateWaitsForProxyCacheExpiry(record *CodexTurnStateRecord, now time.Time) bool {
	if record == nil || record.CollectionReason != "collector_proxy_changed" || record.EncryptedToken == "" ||
		record.Shape != CodexTurnStateShapeTarget || record.IssuedAt.IsZero() || record.IssuedAt.After(now.Add(30*time.Second)) ||
		!record.ExpiresAt.After(record.IssuedAt) || record.ExpiresAt.After(record.IssuedAt.Add(CodexTurnStateLifetime)) || !record.ExpiresAt.After(now) {
		return false
	}
	return (record.TokenLength == 292 && record.CipherBlocks == 10) || (record.TokenLength == 332 && record.CipherBlocks == 12)
}
