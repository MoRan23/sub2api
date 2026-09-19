package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const (
	CodexTurnStateModelsMaxCount  = 64
	CodexTurnStateModelMaxBytes   = 256
	codexTurnStateModelsCacheTTL  = 5 * time.Second
	codexTurnStateModelsErrorTTL  = time.Second
	codexTurnStateModelsDBTimeout = 2 * time.Second
)

var ErrCodexTurnStateModelsUnavailable = errors.New("Codex turn-state model policy unavailable")

func DefaultCodexTurnStateModels() []string {
	return []string{"gpt-6-astra", "gpt-5.6-sol", "gpt-5.6-terra"}
}

// NormalizeCodexTurnStateModels preserves exact case and order. An empty slice
// is an explicit deny-all policy, and must never be replaced by defaults.
func NormalizeCodexTurnStateModels(models []string) ([]string, error) {
	if len(models) > CodexTurnStateModelsMaxCount {
		return nil, fmt.Errorf("codex_turn_state_models supports at most %d model IDs", CodexTurnStateModelsMaxCount)
	}
	result := make([]string, 0, len(models))
	seen := make(map[string]struct{}, len(models))
	for _, raw := range models {
		if strings.IndexFunc(raw, unicode.IsControl) >= 0 {
			return nil, errors.New("codex_turn_state_models IDs must not contain control characters")
		}
		model := strings.TrimSpace(raw)
		if model == "" || len(model) > CodexTurnStateModelMaxBytes {
			return nil, fmt.Errorf("each codex_turn_state_models ID must contain 1 to %d bytes", CodexTurnStateModelMaxBytes)
		}
		if strings.ContainsAny(model, "*?[]{}") || strings.IndexFunc(model, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) >= 0 {
			return nil, errors.New("codex_turn_state_models IDs must be exact IDs without whitespace, control characters, or wildcards")
		}
		if _, exists := seen[model]; !exists {
			seen[model] = struct{}{}
			result = append(result, model)
		}
	}
	return result, nil
}

func parseCodexTurnStateModels(values map[string]string) ([]string, error) {
	raw, present := values[SettingKeyCodexTurnStateModels]
	if !present {
		return DefaultCodexTurnStateModels(), nil
	}
	var models []string
	if json.Unmarshal([]byte(raw), &models) != nil || models == nil {
		return nil, ErrCodexTurnStateModelsUnavailable
	}
	models, err := NormalizeCodexTurnStateModels(models)
	if err != nil {
		return nil, ErrCodexTurnStateModelsUnavailable
	}
	return models, nil
}

type cachedCodexTurnStateModels struct {
	models    []string
	revision  string
	err       error
	expiresAt time.Time
	epoch     uint64
}

func codexTurnStateModelsRevision(models []string, persisted string) string {
	raw, _ := json.Marshal(models)
	digest := sha256.Sum256(raw)
	return strings.TrimSpace(persisted) + ":" + hex.EncodeToString(digest[:])
}

// ParseCodexTurnStateModelPolicyValues resolves authoritative settings rows for
// both service checks and repository publication guards. Absence uses the
// virtual defaults; malformed persisted rows never fall back to those defaults.
func ParseCodexTurnStateModelPolicyValues(values map[string]string) ([]string, string, error) {
	models, err := parseCodexTurnStateModels(values)
	if err != nil {
		return nil, "", err
	}
	return models, codexTurnStateModelsRevision(models, values[SettingKeyCodexTurnStateModelsRevision]), nil
}

// CodexTurnStateModelPolicy returns an immutable five-second snapshot. Shared
// settings storage provides cross-instance refresh; failed reads deny use until
// a successful retry rather than expanding access to the compiled defaults.
func (s *SettingService) CodexTurnStateModelPolicy(ctx context.Context) ([]string, string, error) {
	if s == nil || s.settingRepo == nil {
		return nil, "", ErrCodexTurnStateModelsUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for attempt := 0; attempt < 3; attempt++ {
		cached := s.codexTurnStateModelsCache.Load()
		if cached != nil && time.Now().Before(cached.expiresAt) {
			return slices.Clone(cached.models), cached.revision, cached.err
		}
		var epoch uint64
		if cached != nil {
			epoch = cached.epoch
		}
		_, _, _ = s.codexTurnStateModelsSF.Do(strconv.FormatUint(epoch, 10), func() (any, error) {
			if s.codexTurnStateModelsCache.Load() != cached {
				return nil, nil
			}
			dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), codexTurnStateModelsDBTimeout)
			defer cancel()
			values, err := s.settingRepo.GetMultiple(dbCtx, []string{SettingKeyCodexTurnStateModels, SettingKeyCodexTurnStateModelsRevision})
			resolved := &cachedCodexTurnStateModels{epoch: epoch, expiresAt: time.Now().Add(codexTurnStateModelsCacheTTL)}
			if err == nil {
				resolved.models, resolved.revision, err = ParseCodexTurnStateModelPolicyValues(values)
			}
			if err != nil {
				resolved.err = ErrCodexTurnStateModelsUnavailable
				resolved.expiresAt = time.Now().Add(codexTurnStateModelsErrorTTL)
			}
			if s.codexTurnStateModelsCache.CompareAndSwap(cached, resolved) && codexTurnStateModelPolicyChanged(cached, resolved) {
				s.notifyCodexTurnStateModelsListeners()
			}
			return nil, nil
		})
	}
	return nil, "", ErrCodexTurnStateModelsUnavailable
}

func (s *SettingService) GetCodexTurnStateModels(ctx context.Context) ([]string, error) {
	models, _, err := s.CodexTurnStateModelPolicy(ctx)
	return models, err
}

// CodexTurnStateModelPolicyAuthoritative bypasses the request-path cache. Use it
// at physical send and result publication boundaries so a saved exclusion takes
// effect without waiting for the refresh interval. Storage failures deny use.
func (s *SettingService) CodexTurnStateModelPolicyAuthoritative(ctx context.Context) ([]string, string, error) {
	if s == nil || s.settingRepo == nil {
		return nil, "", ErrCodexTurnStateModelsUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for attempt := 0; attempt < 3; attempt++ {
		previous := s.codexTurnStateModelsCache.Load()
		dbCtx, cancel := context.WithTimeout(ctx, codexTurnStateModelsDBTimeout)
		values, err := s.settingRepo.GetMultiple(dbCtx, []string{SettingKeyCodexTurnStateModels, SettingKeyCodexTurnStateModelsRevision})
		cancel()
		next := &cachedCodexTurnStateModels{expiresAt: time.Now().Add(codexTurnStateModelsCacheTTL)}
		if previous != nil {
			next.epoch = previous.epoch
		}
		if err == nil {
			next.models, next.revision, err = ParseCodexTurnStateModelPolicyValues(values)
		}
		if err != nil {
			next.err = ErrCodexTurnStateModelsUnavailable
			next.expiresAt = time.Now().Add(codexTurnStateModelsErrorTTL)
		}
		if s.codexTurnStateModelsCache.CompareAndSwap(previous, next) {
			if codexTurnStateModelPolicyChanged(previous, next) {
				s.notifyCodexTurnStateModelsListeners()
			}
			return slices.Clone(next.models), next.revision, next.err
		}
		if current := s.codexTurnStateModelsCache.Load(); current != nil && current.epoch == next.epoch && !codexTurnStateModelPolicyChanged(current, next) {
			// Concurrent reads of the same policy do not invalidate this result.
			return slices.Clone(next.models), next.revision, next.err
		}
		// A local writer may have published while this read was in flight. Re-read
		// instead of returning the older database snapshot or a cached policy.
	}
	return nil, "", ErrCodexTurnStateModelsUnavailable
}

func (s *SettingService) CodexTurnStateModelAllowed(ctx context.Context, finalModel string) (bool, error) {
	models, err := s.GetCodexTurnStateModels(ctx)
	return err == nil && slices.Contains(models, finalModel), err
}

func codexTurnStateModelPolicyChanged(previous, next *cachedCodexTurnStateModels) bool {
	return previous == nil || previous.revision != next.revision || (previous.err != nil) != (next.err != nil)
}

func (s *SettingService) publishCodexTurnStateModels(values map[string]string) {
	models, revision, err := ParseCodexTurnStateModelPolicyValues(values)
	for {
		previous := s.codexTurnStateModelsCache.Load()
		next := &cachedCodexTurnStateModels{models: models, err: err, epoch: 1, expiresAt: time.Now().Add(codexTurnStateModelsCacheTTL)}
		if previous != nil {
			next.epoch = previous.epoch + 1
		}
		next.revision = revision
		if s.codexTurnStateModelsCache.CompareAndSwap(previous, next) {
			if codexTurnStateModelPolicyChanged(previous, next) {
				s.notifyCodexTurnStateModelsListeners()
			}
			return
		}
	}
}

func (s *SettingService) InvalidateCodexTurnStateModelsCache() {
	if s == nil {
		return
	}
	for {
		previous := s.codexTurnStateModelsCache.Load()
		next := &cachedCodexTurnStateModels{epoch: 1, err: ErrCodexTurnStateModelsUnavailable}
		if previous != nil {
			next.epoch = previous.epoch + 1
		}
		if s.codexTurnStateModelsCache.CompareAndSwap(previous, next) {
			return
		}
	}
}

// Listeners must return promptly, and may enqueue cancellation work. They run
// after snapshot publication without holding the listener mutex.
func (s *SettingService) AddCodexTurnStateModelsListener(listener func()) {
	if s == nil || listener == nil {
		return
	}
	s.codexTurnStateModelsListenersMu.Lock()
	s.codexTurnStateModelsListeners = append(s.codexTurnStateModelsListeners, listener)
	s.codexTurnStateModelsListenersMu.Unlock()
}

func (s *SettingService) notifyCodexTurnStateModelsListeners() {
	s.codexTurnStateModelsListenersMu.Lock()
	listeners := append([]func(){}, s.codexTurnStateModelsListeners...)
	s.codexTurnStateModelsListenersMu.Unlock()
	for _, listener := range listeners {
		listener()
	}
}
