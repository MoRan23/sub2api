package service

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"
)

type cachedOpenAIDaybreak struct {
	enabled   bool
	expiresAt time.Time
}

func parseOpenAIDaybreakEnabled(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return true
	}
	enabled, err := strconv.ParseBool(raw)
	return err == nil && enabled
}

// IsOpenAIDaybreakEnabled returns the current system preference. Callers read it
// once per physical request or WS frame, rather than freezing it for a connection.
// External instance updates become visible after the short cache TTL.
func (s *SettingService) IsOpenAIDaybreakEnabled(ctx context.Context) bool {
	if s == nil || s.settingRepo == nil {
		return true
	}
	if cached := s.openAIDaybreakCache.Load(); cached != nil && time.Now().Before(cached.expiresAt) {
		return cached.enabled
	}

	// Serialize refresh and committed settings writes so an older read can never
	// publish over a newer admin update.
	s.settingsUpdateMu.Lock()
	defer s.settingsUpdateMu.Unlock()
	cached := s.openAIDaybreakCache.Load()
	if cached != nil && time.Now().Before(cached.expiresAt) {
		return cached.enabled
	}
	if ctx == nil {
		ctx = context.Background()
	}
	dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), openAIRequestPolicyDBTimeout)
	defer cancel()
	raw, err := s.settingRepo.GetValue(dbCtx, SettingKeyOpenAIDaybreakEnabled)
	enabled, ttl := true, openAIRequestPolicyCacheTTL
	if err != nil && !errors.Is(err, ErrSettingNotFound) {
		if cached != nil {
			enabled = cached.enabled
		}
		ttl = openAIRequestPolicyErrorTTL
	} else {
		enabled = parseOpenAIDaybreakEnabled(raw)
	}
	s.openAIDaybreakCache.Store(&cachedOpenAIDaybreak{
		enabled: enabled, expiresAt: time.Now().Add(ttl),
	})
	return enabled
}

// publishOpenAIDaybreakEnabled runs under settingsUpdateMu and only publishes
// a field included in a successfully committed write.
func (s *SettingService) publishOpenAIDaybreakEnabled(raw string) {
	s.openAIDaybreakCache.Store(&cachedOpenAIDaybreak{
		enabled:   parseOpenAIDaybreakEnabled(raw),
		expiresAt: time.Now().Add(openAIRequestPolicyCacheTTL),
	})
}
