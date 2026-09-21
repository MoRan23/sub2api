package service

import (
	"context"
	"errors"
	"strconv"
	"strings"
)

// A missing key keeps upgrades compatible with the default-enabled migration.
// Explicit false and malformed values never silently enable collection.
func parseCodexTelemetryEnabled(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return true
	}
	enabled, err := strconv.ParseBool(raw)
	return err == nil && enabled
}

// IsCodexTelemetryEnabled returns the persisted preference. Environment forcing
// is applied by CodexTelemetryService so the configured value remains visible.
func (s *SettingService) IsCodexTelemetryEnabled(ctx context.Context) bool {
	return s.codexTelemetryPreference(ctx, SettingKeyCodexTelemetryEnabled)
}

func (s *SettingService) IsCodexTelemetrySimulationEnabled(ctx context.Context) bool {
	return s.codexTelemetryPreference(ctx, SettingKeyCodexTelemetrySimulationEnabled)
}

func (s *SettingService) IsCodexTelemetryObservationEnabled(ctx context.Context) bool {
	return s.codexTelemetryPreference(ctx, SettingKeyCodexTelemetryObservationEnabled)
}

func (s *SettingService) codexTelemetryPreference(ctx context.Context, key string) bool {
	if s == nil || s.settingRepo == nil {
		return true
	}
	if ctx == nil {
		ctx = context.Background()
	}
	raw, err := s.settingRepo.GetValue(ctx, key)
	if errors.Is(err, ErrSettingNotFound) {
		return true
	}
	if err != nil {
		return false
	}
	return parseCodexTelemetryEnabled(raw)
}

// SetCodexTelemetryService attaches the same runtime used by forwarding and the
// admin observation endpoint without replacing the existing onUpdate callback.
func (s *SettingService) SetCodexTelemetryService(telemetry *CodexTelemetryService) {
	if s == nil {
		return
	}
	s.settingsUpdateMu.Lock()
	defer s.settingsUpdateMu.Unlock()
	s.codexTelemetry = telemetry
	if telemetry != nil {
		telemetry.SetPolicy(s.IsCodexTelemetryEnabled(context.Background()), s.IsCodexTelemetrySimulationEnabled(context.Background()), s.IsCodexTelemetryObservationEnabled(context.Background()))
	}
}
