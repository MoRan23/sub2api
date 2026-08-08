package service

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"time"
)

// cachedOpenAIUUIDv7SessionIdentity is the in-process representation of the
// global UUIDv7 session identity switch. expiresAt is a Unix nanosecond value
// so reads can stay lock-free on the OpenAI request hot path.
type cachedOpenAIUUIDv7SessionIdentity struct {
	generation uint64
	enabled    bool
	expiresAt  int64
}

const (
	openAIUUIDv7SessionIdentityCacheTTL  = 60 * time.Second
	openAIUUIDv7SessionIdentityErrorTTL  = 5 * time.Second
	openAIUUIDv7SessionIdentityDBTimeout = 5 * time.Second
	openAIUUIDv7SessionIdentitySFKey     = "openai_uuidv7_session_identity"
)

// IsOpenAIUUIDv7SessionIdentityEnabled returns the effective runtime switch.
// The setting is opt-in: a missing row, malformed value, or database error is
// fail-closed (false). Successful reads are cached for 60 seconds and joined
// through singleflight when the cache expires.
func (s *SettingService) IsOpenAIUUIDv7SessionIdentityEnabled(ctx context.Context) bool {
	if s == nil || s.settingRepo == nil {
		return false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// Generation-specific cache entries and singleflight keys prevent an old
	// database read from publishing stale state after an admin write invalidates
	// the switch. A small retry bound keeps an update storm fail-closed.
	for attempt := 0; attempt < 3; attempt++ {
		generation := s.openAIUUIDv7SessionIdentityGeneration.Load()
		now := time.Now().UnixNano()
		if cached, ok := s.openAIUUIDv7SessionIdentityCache.Load().(*cachedOpenAIUUIDv7SessionIdentity); ok &&
			cached != nil && cached.generation == generation && now < cached.expiresAt {
			if generation == s.openAIUUIDv7SessionIdentityGeneration.Load() {
				return cached.enabled
			}
			continue
		}

		singleflightKey := openAIUUIDv7SessionIdentitySFKey + ":" + strconv.FormatUint(generation, 10)
		value, _, _ := s.openAIUUIDv7SessionIdentitySF.Do(singleflightKey, func() (any, error) {
			if cached, ok := s.openAIUUIDv7SessionIdentityCache.Load().(*cachedOpenAIUUIDv7SessionIdentity); ok &&
				cached != nil && cached.generation == generation && time.Now().UnixNano() < cached.expiresAt {
				return cached, nil
			}

			dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), openAIUUIDv7SessionIdentityDBTimeout)
			defer cancel()
			raw, err := s.settingRepo.GetValue(dbCtx, SettingKeyEnableOpenAIUUIDv7SessionIdentity)
			ttl := openAIUUIDv7SessionIdentityCacheTTL
			enabled := raw == "true"
			if err != nil {
				// A missing row is the normal state before the default initializer
				// has run. Transient failures use a short TTL for prompt recovery.
				enabled = false
				if !errors.Is(err, ErrSettingNotFound) {
					ttl = openAIUUIDv7SessionIdentityErrorTTL
					slog.Warn("failed to get OpenAI UUIDv7 session identity setting", "error", err)
				}
			}
			resolved := &cachedOpenAIUUIDv7SessionIdentity{
				generation: generation,
				enabled:    enabled,
				expiresAt:  time.Now().Add(ttl).UnixNano(),
			}
			// The generation on the value makes a late store harmless. Avoid it
			// when possible so the current generation remains the common fast path.
			if generation == s.openAIUUIDv7SessionIdentityGeneration.Load() {
				s.openAIUUIDv7SessionIdentityCache.Store(resolved)
			}
			return resolved, nil
		})
		resolved, ok := value.(*cachedOpenAIUUIDv7SessionIdentity)
		if !ok || resolved == nil {
			return false
		}
		if resolved.generation != s.openAIUUIDv7SessionIdentityGeneration.Load() {
			continue
		}
		return resolved.enabled
	}
	return false
}

// GetOpenAIUUIDv7SessionIdentityEnabled is a getter-named alias for callers
// that use the Get* convention for runtime settings.
func (s *SettingService) GetOpenAIUUIDv7SessionIdentityEnabled(ctx context.Context) bool {
	return s.IsOpenAIUUIDv7SessionIdentityEnabled(ctx)
}

// InvalidateOpenAIUUIDv7SessionIdentityCache drops the runtime switch cache.
// Settings writes call this synchronously; the next getter therefore observes
// the newly persisted value instead of waiting for the 60-second TTL.
func (s *SettingService) InvalidateOpenAIUUIDv7SessionIdentityCache() {
	if s == nil {
		return
	}
	generation := s.openAIUUIDv7SessionIdentityGeneration.Add(1)
	// Store an expired typed entry rather than an untyped nil so concurrent
	// readers can safely load while the next request refreshes the value.
	s.openAIUUIDv7SessionIdentityCache.Store(&cachedOpenAIUUIDv7SessionIdentity{generation: generation, expiresAt: 0})
}
