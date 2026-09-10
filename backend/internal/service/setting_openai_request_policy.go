package service

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
)

const (
	openAIRequestPolicyCacheTTL  = 60 * time.Second
	openAIRequestPolicyErrorTTL  = 5 * time.Second
	openAIRequestPolicyDBTimeout = 5 * time.Second
)

var openAIRequestPolicySettingKeys = []string{
	SettingKeyEnableOpenAIRequestTimezoneConversion,
	SettingKeyEnableOpenAIPassthroughTimezoneConversion,
	SettingKeyEnableOpenAICodexResidencyUS,
}

type cachedOpenAIRequestPolicy struct {
	policy            openai.RequestPolicy
	generation        uint64
	expiresAt         time.Time
	hasLastSuccessful bool
	lastSuccessful    openai.RequestPolicy
}

func parseOpenAIRequestPolicy(values map[string]string) (openai.RequestPolicy, bool) {
	policy := openai.DefaultRequestPolicy()
	bindings := []struct {
		key    string
		target *bool
	}{
		{SettingKeyEnableOpenAIRequestTimezoneConversion, &policy.TimezoneConversionEnabled},
		{SettingKeyEnableOpenAIPassthroughTimezoneConversion, &policy.PassthroughTimezoneConversionEnabled},
		{SettingKeyEnableOpenAICodexResidencyUS, &policy.CodexResidencyUS},
	}
	for _, binding := range bindings {
		raw, present := values[binding.key]
		if !present {
			continue
		}
		enabled, valid := parseOpenAIUUIDv7SessionIdentitySetting(raw, true)
		if !valid {
			return openai.DefaultRequestPolicy(), false
		}
		*binding.target = enabled
	}
	return policy, true
}

// GetOpenAIRequestPolicy returns an immutable snapshot independent of identity
// normalization. Invalid or unavailable storage keeps the last successful whole
// snapshot; a cold failure uses the compiled default and a short retry TTL.
func (s *SettingService) GetOpenAIRequestPolicy(ctx context.Context) openai.RequestPolicy {
	if s == nil || s.settingRepo == nil {
		return openai.DefaultRequestPolicy()
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for attempt := 0; attempt < 3; attempt++ {
		cached := s.openAIRequestPolicyCache.Load()
		if cached != nil && time.Now().Before(cached.expiresAt) {
			return cached.policy
		}
		var generation uint64
		if cached != nil {
			generation = cached.generation
		}
		_, _, _ = s.openAIRequestPolicySF.Do(strconv.FormatUint(generation, 10), func() (any, error) {
			previous := s.openAIRequestPolicyCache.Load()
			if previous != cached {
				return nil, nil
			}
			dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), openAIRequestPolicyDBTimeout)
			defer cancel()
			resolved := &cachedOpenAIRequestPolicy{policy: openai.DefaultRequestPolicy(), generation: generation}
			if previous != nil && previous.hasLastSuccessful {
				resolved.policy = previous.lastSuccessful
				resolved.lastSuccessful = previous.lastSuccessful
				resolved.hasLastSuccessful = true
			}
			ttl := openAIRequestPolicyCacheTTL
			values, err := s.settingRepo.GetMultiple(dbCtx, openAIRequestPolicySettingKeys)
			if err != nil {
				ttl = openAIRequestPolicyErrorTTL
				slog.Warn("failed to get OpenAI request policy; using last-known-good snapshot", "error", err)
			} else if policy, valid := parseOpenAIRequestPolicy(values); valid {
				resolved.policy = policy
				resolved.lastSuccessful = policy
				resolved.hasLastSuccessful = true
			} else {
				ttl = openAIRequestPolicyErrorTTL
				slog.Warn("invalid OpenAI request policy; using last-known-good snapshot")
			}
			resolved.expiresAt = time.Now().Add(ttl)
			// An admin publish/invalidation replaces the pointer. A stale read
			// must never overwrite that newer generation, even after a DB error.
			s.openAIRequestPolicyCache.CompareAndSwap(previous, resolved)
			return nil, nil
		})
	}
	if cached := s.openAIRequestPolicyCache.Load(); cached != nil && cached.hasLastSuccessful {
		return cached.lastSuccessful
	}
	return openai.DefaultRequestPolicy()
}

func (s *SettingService) InvalidateOpenAIRequestPolicyCache() {
	if s == nil {
		return
	}
	for {
		previous := s.openAIRequestPolicyCache.Load()
		invalidated := &cachedOpenAIRequestPolicy{policy: openai.DefaultRequestPolicy(), generation: 1}
		if previous != nil {
			invalidated.generation = previous.generation + 1
			invalidated.hasLastSuccessful = previous.hasLastSuccessful
			invalidated.lastSuccessful = previous.lastSuccessful
		}
		if s.openAIRequestPolicyCache.CompareAndSwap(previous, invalidated) {
			return
		}
	}
}

// PublishOpenAIRequestPolicy publishes only a successfully persisted whole
// policy, atomically with its generation and without a subsequent database read.
func (s *SettingService) PublishOpenAIRequestPolicy(policy openai.RequestPolicy) {
	if s == nil {
		return
	}
	for {
		previous := s.openAIRequestPolicyCache.Load()
		published := &cachedOpenAIRequestPolicy{
			policy: policy, generation: 1, expiresAt: time.Now().Add(openAIRequestPolicyCacheTTL),
			hasLastSuccessful: true, lastSuccessful: policy,
		}
		if previous != nil {
			published.generation = previous.generation + 1
		}
		if s.openAIRequestPolicyCache.CompareAndSwap(previous, published) {
			return
		}
	}
}

func (s *SettingService) publishAuthoritativeOpenAIRequestPolicy(values map[string]string) {
	policy, valid := parseOpenAIRequestPolicy(values)
	if !valid {
		s.InvalidateOpenAIRequestPolicyCache()
		slog.Warn("invalid OpenAI request policy after settings update; preserving last-known-good snapshot")
		return
	}
	s.PublishOpenAIRequestPolicy(policy)
}
