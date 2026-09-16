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
	if s == nil || s.settingRepo == nil {
		return true
	}
	if ctx == nil {
		ctx = context.Background()
	}
	raw, err := s.settingRepo.GetValue(ctx, SettingKeyCodexTelemetryEnabled)
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
		telemetry.SetEnabled(s.IsCodexTelemetryEnabled(context.Background()))
	}
}
