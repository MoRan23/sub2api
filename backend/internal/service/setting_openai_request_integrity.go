package service

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"
)

type cachedOpenAIRequestIntegrityObserve struct {
	enabled   bool
	expiresAt time.Time
}

func parseOpenAIRequestIntegrityObserveEnabled(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return true
	}
	enabled, err := strconv.ParseBool(raw)
	return err == nil && enabled
}

// IsOpenAIRequestIntegrityObserveEnabled returns a request-independent setting
// snapshot. Callers freeze it once per request or WS turn. The short-lived cache
// keeps external instance updates visible without a database read per send.
func (s *SettingService) IsOpenAIRequestIntegrityObserveEnabled(ctx context.Context) bool {
	if s == nil || s.settingRepo == nil {
		return true
	}
	if cached := s.openAIRequestIntegrityObserveCache.Load(); cached != nil && time.Now().Before(cached.expiresAt) {
		return cached.enabled
	}

	// Settings writes use this same lock. An older read can therefore never
	// publish after a newer committed admin update.
	s.settingsUpdateMu.Lock()
	defer s.settingsUpdateMu.Unlock()
	cached := s.openAIRequestIntegrityObserveCache.Load()
	if cached != nil && time.Now().Before(cached.expiresAt) {
		return cached.enabled
	}
	if ctx == nil {
		ctx = context.Background()
	}
	dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), openAIRequestPolicyDBTimeout)
	defer cancel()
	raw, err := s.settingRepo.GetValue(dbCtx, SettingKeyOpenAIRequestIntegrityObserveEnabled)
	enabled, ttl := true, openAIRequestPolicyCacheTTL
	if err != nil && !errors.Is(err, ErrSettingNotFound) {
		if cached != nil {
			enabled = cached.enabled
		}
		ttl = openAIRequestPolicyErrorTTL
	} else {
		enabled = parseOpenAIRequestIntegrityObserveEnabled(raw)
	}
	s.openAIRequestIntegrityObserveCache.Store(&cachedOpenAIRequestIntegrityObserve{
		enabled: enabled, expiresAt: time.Now().Add(ttl),
	})
	return enabled
}

// publishOpenAIRequestIntegrityObserveEnabled is called under settingsUpdateMu,
// and only for the setting included in a successfully committed write.
func (s *SettingService) publishOpenAIRequestIntegrityObserveEnabled(raw string) {
	s.openAIRequestIntegrityObserveCache.Store(&cachedOpenAIRequestIntegrityObserve{
		enabled:   parseOpenAIRequestIntegrityObserveEnabled(raw),
		expiresAt: time.Now().Add(openAIRequestPolicyCacheTTL),
	})
}
