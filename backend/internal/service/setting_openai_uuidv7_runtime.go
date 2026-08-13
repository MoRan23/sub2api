package service

import (
	"context"
	"log/slog"
	"strconv"
	"time"
)

// CodexFingerprintPolicySnapshot freezes the global OAuth Codex identity
// policy for one request or WebSocket connection.
type CodexFingerprintPolicySnapshot struct {
	Generation            uint64
	MasterEnabled         bool
	InstallationIDEnabled bool
	TurnIdentityEnabled   bool
	ClientIdentityEnabled bool
}

func (p CodexFingerprintPolicySnapshot) InstallationIDNormalizationEnabled() bool {
	return p.MasterEnabled && p.InstallationIDEnabled
}

func (p CodexFingerprintPolicySnapshot) TurnIdentityNormalizationEnabled() bool {
	return p.MasterEnabled && p.TurnIdentityEnabled
}

func (p CodexFingerprintPolicySnapshot) ClientIdentityNormalizationEnabled() bool {
	return p.MasterEnabled && p.ClientIdentityEnabled
}

type cachedOpenAICodexFingerprintPolicy struct {
	policy            CodexFingerprintPolicySnapshot
	expiresAt         int64
	hasLastSuccessful bool
	lastSuccessful    CodexFingerprintPolicySnapshot
}

const (
	openAIUUIDv7SessionIdentityCacheTTL  = 60 * time.Second
	openAIUUIDv7SessionIdentityErrorTTL  = 5 * time.Second
	openAIUUIDv7SessionIdentityDBTimeout = 5 * time.Second
	openAIUUIDv7SessionIdentitySFKey     = "openai_codex_fingerprint_policy"
	openAIUUIDv7SessionIdentityDefault   = true
)

var openAICodexFingerprintPolicySettingKeys = []string{
	SettingKeyEnableOpenAICodexFingerprintNormalization,
	SettingKeyEnableOpenAICodexInstallationIDNormalization,
	SettingKeyEnableOpenAIUUIDv7SessionIdentity,
	SettingKeyEnableOpenAICodexClientIdentityNormalization,
}

func defaultOpenAICodexFingerprintPolicy(generation uint64) CodexFingerprintPolicySnapshot {
	return CodexFingerprintPolicySnapshot{
		Generation:            generation,
		MasterEnabled:         true,
		InstallationIDEnabled: true,
		TurnIdentityEnabled:   true,
		ClientIdentityEnabled: true,
	}
}

func parseOpenAIUUIDv7SessionIdentitySetting(raw string, present bool) (enabled, valid bool) {
	if !present {
		return openAIUUIDv7SessionIdentityDefault, false
	}
	switch raw {
	case "true":
		return true, true
	case "false":
		return false, true
	default:
		return openAIUUIDv7SessionIdentityDefault, false
	}
}

func parseDefaultTrueSetting(settings map[string]string, key string) bool {
	raw, present := settings[key]
	value, valid := parseOpenAIUUIDv7SessionIdentitySetting(raw, present)
	if !present || valid {
		return value
	}
	return true
}

func parseOpenAICodexFingerprintPolicy(settings map[string]string, generation uint64) (CodexFingerprintPolicySnapshot, bool) {
	policy := defaultOpenAICodexFingerprintPolicy(generation)
	bindings := []struct {
		key    string
		target *bool
	}{
		{SettingKeyEnableOpenAICodexFingerprintNormalization, &policy.MasterEnabled},
		{SettingKeyEnableOpenAICodexInstallationIDNormalization, &policy.InstallationIDEnabled},
		{SettingKeyEnableOpenAIUUIDv7SessionIdentity, &policy.TurnIdentityEnabled},
		{SettingKeyEnableOpenAICodexClientIdentityNormalization, &policy.ClientIdentityEnabled},
	}
	for _, binding := range bindings {
		raw, present := settings[binding.key]
		if !present {
			continue
		}
		value, valid := parseOpenAIUUIDv7SessionIdentitySetting(raw, true)
		if !valid {
			return policy, false
		}
		*binding.target = value
	}
	return policy, true
}

// GetOpenAICodexFingerprintPolicy returns one atomic, default-on policy
// generation. A failed or malformed read uses the last-known-good snapshot and
// retries after a short TTL.
func (s *SettingService) GetOpenAICodexFingerprintPolicy(ctx context.Context) CodexFingerprintPolicySnapshot {
	if s == nil || s.settingRepo == nil {
		return defaultOpenAICodexFingerprintPolicy(0)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for attempt := 0; attempt < 3; attempt++ {
		generation := s.openAIUUIDv7SessionIdentityGeneration.Load()
		if cached, ok := s.openAIUUIDv7SessionIdentityCache.Load().(*cachedOpenAICodexFingerprintPolicy); ok &&
			cached != nil && cached.policy.Generation == generation && time.Now().UnixNano() < cached.expiresAt {
			if generation == s.openAIUUIDv7SessionIdentityGeneration.Load() {
				return cached.policy
			}
			continue
		}

		key := openAIUUIDv7SessionIdentitySFKey + ":" + strconv.FormatUint(generation, 10)
		value, _, _ := s.openAIUUIDv7SessionIdentitySF.Do(key, func() (any, error) {
			if cached, ok := s.openAIUUIDv7SessionIdentityCache.Load().(*cachedOpenAICodexFingerprintPolicy); ok &&
				cached != nil && cached.policy.Generation == generation && time.Now().UnixNano() < cached.expiresAt {
				return cached, nil
			}

			dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), openAIUUIDv7SessionIdentityDBTimeout)
			defer cancel()
			previous, _ := s.openAIUUIDv7SessionIdentityCache.Load().(*cachedOpenAICodexFingerprintPolicy)
			policy := defaultOpenAICodexFingerprintPolicy(generation)
			ttl := openAIUUIDv7SessionIdentityCacheTTL
			hasLastSuccessful := previous != nil && previous.hasLastSuccessful
			lastSuccessful := defaultOpenAICodexFingerprintPolicy(generation)
			if hasLastSuccessful {
				lastSuccessful = previous.lastSuccessful
				lastSuccessful.Generation = generation
			}
			values, err := s.settingRepo.GetMultiple(dbCtx, openAICodexFingerprintPolicySettingKeys)
			if err == nil {
				if parsed, valid := parseOpenAICodexFingerprintPolicy(values, generation); valid {
					policy = parsed
					hasLastSuccessful = true
					lastSuccessful = parsed
				} else {
					ttl = openAIUUIDv7SessionIdentityErrorTTL
					policy = lastSuccessful
					slog.Warn("invalid OpenAI Codex fingerprint policy; using last-known-good snapshot")
				}
			} else {
				ttl = openAIUUIDv7SessionIdentityErrorTTL
				policy = lastSuccessful
				slog.Warn("failed to get OpenAI Codex fingerprint policy; using last-known-good snapshot", "error", err)
			}
			resolved := &cachedOpenAICodexFingerprintPolicy{
				policy: policy, expiresAt: time.Now().Add(ttl).UnixNano(),
				hasLastSuccessful: hasLastSuccessful, lastSuccessful: lastSuccessful,
			}
			if generation == s.openAIUUIDv7SessionIdentityGeneration.Load() {
				if hook := s.openAIUUIDv7SessionIdentityBeforeCommit; hook != nil {
					hook()
				}
				s.openAIUUIDv7SessionIdentityMu.Lock()
				if generation == s.openAIUUIDv7SessionIdentityGeneration.Load() {
					s.openAIUUIDv7SessionIdentityCache.Store(resolved)
				}
				s.openAIUUIDv7SessionIdentityMu.Unlock()
			}
			return resolved, nil
		})
		resolved, ok := value.(*cachedOpenAICodexFingerprintPolicy)
		if !ok || resolved == nil {
			return defaultOpenAICodexFingerprintPolicy(generation)
		}
		if resolved.policy.Generation != s.openAIUUIDv7SessionIdentityGeneration.Load() {
			continue
		}
		return resolved.policy
	}
	if cached, ok := s.openAIUUIDv7SessionIdentityCache.Load().(*cachedOpenAICodexFingerprintPolicy); ok && cached != nil && cached.hasLastSuccessful {
		policy := cached.lastSuccessful
		policy.Generation = s.openAIUUIDv7SessionIdentityGeneration.Load()
		return policy
	}
	return defaultOpenAICodexFingerprintPolicy(s.openAIUUIDv7SessionIdentityGeneration.Load())
}

func (s *SettingService) IsOpenAIUUIDv7SessionIdentityEnabled(ctx context.Context) bool {
	return s.GetOpenAICodexFingerprintPolicy(ctx).TurnIdentityNormalizationEnabled()
}

func (s *SettingService) GetOpenAIUUIDv7SessionIdentityEnabled(ctx context.Context) bool {
	return s.IsOpenAIUUIDv7SessionIdentityEnabled(ctx)
}

func (s *SettingService) InvalidateOpenAIUUIDv7SessionIdentityCache() {
	if s == nil {
		return
	}
	s.openAIUUIDv7SessionIdentityMu.Lock()
	defer s.openAIUUIDv7SessionIdentityMu.Unlock()
	generation := s.openAIUUIDv7SessionIdentityGeneration.Load() + 1
	previous, _ := s.openAIUUIDv7SessionIdentityCache.Load().(*cachedOpenAICodexFingerprintPolicy)
	invalidated := &cachedOpenAICodexFingerprintPolicy{policy: defaultOpenAICodexFingerprintPolicy(generation)}
	if previous != nil {
		invalidated.hasLastSuccessful = previous.hasLastSuccessful
		invalidated.lastSuccessful = previous.lastSuccessful
		invalidated.lastSuccessful.Generation = generation
	}
	s.openAIUUIDv7SessionIdentityCache.Store(invalidated)
	if hook := s.openAIUUIDv7SessionIdentityBeforeGenerationStore; hook != nil {
		hook()
	}
	s.openAIUUIDv7SessionIdentityGeneration.Store(generation)
}

// PublishOpenAICodexFingerprintPolicy publishes a successfully persisted full
// policy generation without another database read.
func (s *SettingService) PublishOpenAICodexFingerprintPolicy(policy CodexFingerprintPolicySnapshot) {
	if s == nil {
		return
	}
	s.openAIUUIDv7SessionIdentityMu.Lock()
	defer s.openAIUUIDv7SessionIdentityMu.Unlock()
	generation := s.openAIUUIDv7SessionIdentityGeneration.Load() + 1
	policy.Generation = generation
	s.openAIUUIDv7SessionIdentityCache.Store(&cachedOpenAICodexFingerprintPolicy{
		policy: policy, expiresAt: time.Now().Add(openAIUUIDv7SessionIdentityCacheTTL).UnixNano(),
		hasLastSuccessful: true, lastSuccessful: policy,
	})
	if hook := s.openAIUUIDv7SessionIdentityBeforeGenerationStore; hook != nil {
		hook()
	}
	s.openAIUUIDv7SessionIdentityGeneration.Store(generation)
}

func (s *SettingService) PublishOpenAIUUIDv7SessionIdentity(enabled bool) {
	policy := defaultOpenAICodexFingerprintPolicy(0)
	if s != nil {
		if cached, ok := s.openAIUUIDv7SessionIdentityCache.Load().(*cachedOpenAICodexFingerprintPolicy); ok && cached != nil {
			policy = cached.policy
		}
	}
	policy.TurnIdentityEnabled = enabled
	s.PublishOpenAICodexFingerprintPolicy(policy)
}
