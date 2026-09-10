package service

import (
	"container/list"
	"context"
	"errors"
	"strings"
	"time"
)

// OpenAICodexWindowMappingStore binds a downstream thread to one durable window
// record. An inherited record stays in place so its client-window HMAC tokens
// and any compaction already in flight continue to use the same storage key.
type OpenAICodexWindowMappingStore interface {
	ResolveOpenAICodexWindowMapping(ctx context.Context, canonicalKey, legacyKey, threadID string, ttl time.Duration) (string, error)
}

type openAICodexWindowMappingLocalEntry struct {
	mappingKey       string
	expiresAt        time.Time
	recency          *list.Element
	primaryConfirmed bool
}

func (s *openAICodexWindowLocalStore) rememberPrimaryWindowMapping(canonicalKey, selected string, ttl time.Duration) error {
	if !validOpenAICodexWindowMappingKey(canonicalKey) || !validOpenAICodexWindowMappingKey(selected) {
		return ErrOpenAICodexWindowStoredInvalid
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.mappingAliases == nil {
		s.mappingAliases = make(map[string]*openAICodexWindowMappingLocalEntry)
		s.mappingRecency = list.New()
	}
	alias := s.mappingAliases[canonicalKey]
	if alias != nil && alias.expiresAt.After(now) && alias.primaryConfirmed && alias.mappingKey != selected {
		return ErrOpenAICodexWindowStoredInvalid
	}
	if alias == nil {
		for len(s.mappingAliases) >= s.maxEntries {
			oldest := s.mappingRecency.Back()
			if oldest == nil {
				break
			}
			delete(s.mappingAliases, oldest.Value.(string))
			s.mappingRecency.Remove(oldest)
		}
		alias = &openAICodexWindowMappingLocalEntry{recency: s.mappingRecency.PushFront(canonicalKey)}
		s.mappingAliases[canonicalKey] = alias
	}
	alias.mappingKey = selected
	alias.primaryConfirmed = true
	alias.expiresAt = now.Add(normalizeOpenAICodexWindowTTL(ttl))
	s.mappingRecency.MoveToFront(alias.recency)
	return nil
}

func (s *openAICodexWindowLocalStore) confirmedWindowMapping(canonicalKey, threadID string, ttl time.Duration) (string, bool, error) {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	alias := s.mappingAliases[canonicalKey]
	if alias == nil || !alias.expiresAt.After(now) || !alias.primaryConfirmed {
		return "", false, nil
	}
	if !validOpenAICodexWindowMappingKey(alias.mappingKey) {
		return "", false, ErrOpenAICodexWindowStoredInvalid
	}
	window := s.liveEntryLocked(alias.mappingKey, now)
	if window == nil {
		return "", false, nil
	}
	if window.snapshot.ThreadID != threadID || ValidateOpenAICodexWindowMappingPair(window.snapshot, window.clientWindow) != nil {
		return "", false, ErrOpenAICodexWindowStoredInvalid
	}
	alias.expiresAt = now.Add(normalizeOpenAICodexWindowTTL(ttl))
	window.expiresAt = alias.expiresAt
	s.mappingRecency.MoveToFront(alias.recency)
	s.recency.MoveToFront(window.recency)
	return alias.mappingKey, true, nil
}

func resolveOpenAICodexPrimaryWindowMapping(ctx context.Context, primary OpenAICodexWindowMappingStore, local *openAICodexWindowLocalStore, canonicalKey, legacyKey, threadID string, ttl time.Duration) (string, error) {
	if err := validateOpenAICodexWindowMappingInput(canonicalKey, legacyKey, threadID); err != nil {
		return "", err
	}
	primaryCtx, cancel := context.WithTimeout(ctx, openAICodexWindowStoreTimeout)
	selected, err := primary.ResolveOpenAICodexWindowMapping(primaryCtx, canonicalKey, legacyKey, threadID, ttl)
	cancel()
	if err == nil {
		if err := local.rememberPrimaryWindowMapping(canonicalKey, selected, ttl); err != nil {
			return "", err
		}
		return selected, nil
	}
	if errors.Is(err, ErrOpenAICodexWindowStoreUnavailable) {
		cached, ok, cacheErr := local.confirmedWindowMapping(canonicalKey, threadID, ttl)
		if cacheErr != nil {
			return "", cacheErr
		}
		if ok {
			return cached, nil
		}
	}
	return "", err
}

func validateOpenAICodexWindowMappingInput(canonicalKey, legacyKey, threadID string) error {
	if !validOpenAICodexWindowMappingKey(canonicalKey) ||
		(legacyKey != "" && !validOpenAICodexWindowMappingKey(legacyKey)) {
		return errors.New("openai Codex window mapping alias must use lowercase SHA-256 keys")
	}
	if canonical, err := canonicalUUIDv7(threadID); err != nil || canonical != threadID {
		return errors.New("openai Codex window mapping thread must be a canonical UUIDv7")
	}
	return nil
}

// ValidateOpenAICodexWindowMappingPair permits an older client binding after
// independent compactions, but rejects records from incompatible generations.
func ValidateOpenAICodexWindowMappingPair(current OpenAICodexWindowSnapshot, binding *OpenAICodexClientWindowBinding) error {
	if ValidateOpenAICodexWindowSnapshot(current) != nil {
		return ErrOpenAICodexWindowStoredInvalid
	}
	if binding == nil {
		return nil
	}
	previous := binding.Server
	if ValidateOpenAICodexClientWindowBinding(*binding) != nil ||
		previous.ThreadID != current.ThreadID || previous.Number > current.Number ||
		(previous.Number == current.Number && previous.ContextWindowID != current.ContextWindowID) ||
		(previous.FirstContextWindowID != "" && current.FirstContextWindowID != "" && previous.FirstContextWindowID != current.FirstContextWindowID) {
		return ErrOpenAICodexWindowStoredInvalid
	}
	return nil
}

// Mapping selection and window/client validation share the window store mutex.
// In particular, an old account's concurrent compact cannot expose a mixed
// generation while the downstream alias is claimed.
func (s *openAICodexWindowLocalStore) ResolveOpenAICodexWindowMapping(ctx context.Context, canonicalKey, legacyKey, threadID string, ttl time.Duration) (string, error) {
	if s == nil {
		return "", ErrOpenAICodexWindowStoreUnavailable
	}
	if err := validateOpenAICodexWindowMappingInput(canonicalKey, legacyKey, threadID); err != nil {
		return "", err
	}
	if ctx != nil && ctx.Err() != nil {
		return "", ctx.Err()
	}
	ttl = normalizeOpenAICodexWindowTTL(ttl)
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.mappingAliases == nil {
		s.mappingAliases = make(map[string]*openAICodexWindowMappingLocalEntry)
		s.mappingRecency = list.New()
	}
	alias := s.mappingAliases[canonicalKey]
	if alias != nil && !alias.expiresAt.After(now) {
		delete(s.mappingAliases, canonicalKey)
		s.mappingRecency.Remove(alias.recency)
		alias = nil
	}
	validateEntry := func(key string) (*openAICodexWindowLocalEntry, error) {
		entry := s.liveEntryLocked(key, now)
		if entry == nil {
			return nil, nil
		}
		if entry.snapshot.ThreadID != threadID || ValidateOpenAICodexWindowMappingPair(entry.snapshot, entry.clientWindow) != nil {
			return nil, ErrOpenAICodexWindowStoredInvalid
		}
		return entry, nil
	}
	selected := canonicalKey
	var window *openAICodexWindowLocalEntry
	var err error
	if alias != nil {
		if !validOpenAICodexWindowMappingKey(alias.mappingKey) {
			return "", ErrOpenAICodexWindowStoredInvalid
		}
		selected = alias.mappingKey
		window, err = validateEntry(selected)
	} else {
		window, err = validateEntry(canonicalKey)
		if err == nil && window == nil && legacyKey != "" && legacyKey != canonicalKey {
			window, err = validateEntry(legacyKey)
			if window != nil {
				selected = legacyKey
			}
		}
	}
	if err != nil {
		return "", err
	}
	if alias == nil {
		for len(s.mappingAliases) >= s.maxEntries {
			oldest := s.mappingRecency.Back()
			if oldest == nil {
				break
			}
			delete(s.mappingAliases, oldest.Value.(string))
			s.mappingRecency.Remove(oldest)
		}
		alias = &openAICodexWindowMappingLocalEntry{mappingKey: selected, recency: s.mappingRecency.PushFront(canonicalKey)}
		s.mappingAliases[canonicalKey] = alias
	}
	alias.expiresAt = now.Add(ttl)
	s.mappingRecency.MoveToFront(alias.recency)
	if window != nil {
		window.expiresAt = now.Add(ttl)
		s.recency.MoveToFront(window.recency)
	}
	return selected, nil
}

func (s *OpenAICodexWindowRuntimeStore) ResolveOpenAICodexWindowMapping(ctx context.Context, canonicalKey, legacyKey, threadID string, ttl time.Duration) (string, error) {
	if s == nil || s.local == nil {
		return "", ErrOpenAICodexWindowStoreUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if s.primary == nil {
		return s.local.ResolveOpenAICodexWindowMapping(ctx, canonicalKey, legacyKey, threadID, ttl)
	}
	primary, ok := s.primary.(OpenAICodexWindowMappingStore)
	if !ok {
		return "", ErrOpenAICodexWindowStoreUnavailable
	}
	return resolveOpenAICodexPrimaryWindowMapping(ctx, primary, s.local, canonicalKey, legacyKey, threadID, ttl)
}

func (s *OpenAIGatewayService) resolveOpenAICodexDownstreamWindowMappingKey(ctx context.Context, plan OpenAIOAuthIdentityPlan) (string, error) {
	secret := ""
	if s != nil && s.cfg != nil {
		secret = s.cfg.JWT.Secret
	}
	if strings.TrimSpace(plan.TurnIdentityNamespace) == "" {
		return "", errOpenAIOutboundSessionIdentityNamespace
	}
	canonicalKey, err := OpenAICodexWindowMappingKey(secret, plan.TurnIdentityNamespace, plan.APIKeyID, plan.TurnIdentity.ThreadID)
	if err != nil {
		return "", err
	}
	legacyKey := ""
	if strings.TrimSpace(plan.LegacyIdentityNamespace) != "" {
		legacyKey, err = OpenAICodexWindowMappingKey(secret, plan.LegacyIdentityNamespace, plan.APIKeyID, plan.TurnIdentity.ThreadID)
		if err != nil {
			return "", err
		}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if s != nil && s.cache != nil {
		primary, ok := s.cache.(OpenAICodexWindowMappingStore)
		if !ok {
			return "", ErrOpenAICodexWindowStoreUnavailable
		}
		return resolveOpenAICodexPrimaryWindowMapping(ctx, primary, processOpenAICodexWindowLocalStore, canonicalKey, legacyKey, plan.TurnIdentity.ThreadID, OpenAICodexWindowTTL)
	}
	return processOpenAICodexWindowLocalStore.ResolveOpenAICodexWindowMapping(ctx, canonicalKey, legacyKey, plan.TurnIdentity.ThreadID, OpenAICodexWindowTTL)
}

var _ OpenAICodexWindowMappingStore = (*openAICodexWindowLocalStore)(nil)
var _ OpenAICodexWindowMappingStore = (*OpenAICodexWindowRuntimeStore)(nil)
