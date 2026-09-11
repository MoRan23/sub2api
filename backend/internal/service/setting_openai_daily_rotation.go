package service

import (
	"context"
	"strconv"
)

// IsOpenAIOAuthDailySessionRotationEnabled returns the runtime switch for the
// daily OAuth session pool. Missing or malformed settings fail closed.
func (s *SettingService) IsOpenAIOAuthDailySessionRotationEnabled(ctx context.Context) bool {
	if s == nil || s.settingRepo == nil {
		return false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	raw, err := s.settingRepo.GetValue(ctx, SettingKeyEnableOpenAIOAuthDailySessionRotation)
	if err != nil {
		return false
	}
	v, err := strconv.ParseBool(raw)
	return err == nil && v
}

// GetOpenAIOAuthDailySessionRotationEnabled is an alias kept for callers that
// use the Get... naming convention of other OpenAI settings.
func (s *SettingService) GetOpenAIOAuthDailySessionRotationEnabled(ctx context.Context) bool {
	return s.IsOpenAIOAuthDailySessionRotationEnabled(ctx)
}
