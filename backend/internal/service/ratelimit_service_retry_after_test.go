//go:build unit

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestHandle429_RetryAfterFallbackAndPlatformResetPriority(t *testing.T) {
	absoluteReset := time.Now().UTC().Truncate(time.Second).Add(time.Hour)
	pastDate := time.Now().UTC().Add(-time.Hour).Format(http.TimeFormat)
	tests := []struct {
		name            string
		platform        string
		headers         map[string]string
		body            string
		fallbackEnabled bool
		wantDelay       time.Duration
		wantAbsolute    time.Time
		wantNoMark      bool
	}{
		{name: "seconds", platform: PlatformOpenAI, headers: map[string]string{"Retry-After": "90"}, fallbackEnabled: true, wantDelay: 90 * time.Second},
		{name: "fractional_seconds", platform: PlatformOpenAI, headers: map[string]string{"Retry-After": "1.25"}, fallbackEnabled: true, wantDelay: 1250 * time.Millisecond},
		{name: "http_date", platform: PlatformOpenAI, headers: map[string]string{"Retry-After": absoluteReset.Format(http.TimeFormat)}, wantAbsolute: absoluteReset},
		{name: "anthropic_without_reset", platform: PlatformAnthropic, headers: map[string]string{"Retry-After": "45"}, wantDelay: 45 * time.Second},
		{name: "gemini_without_reset", platform: PlatformGemini, headers: map[string]string{"Retry-After": "45"}, wantDelay: 45 * time.Second},
		{name: "invalid_aggregate_with_fallback_disabled", platform: PlatformAnthropic, headers: map[string]string{"anthropic-ratelimit-unified-reset": "invalid", "Retry-After": "45"}, wantDelay: 45 * time.Second},
		{name: "invalid_aggregate_and_retry_after_use_default", platform: PlatformAnthropic, headers: map[string]string{"anthropic-ratelimit-unified-reset": "invalid", "Retry-After": "invalid"}, fallbackEnabled: true, wantDelay: 12 * time.Second},
		{name: "invalid_retry_after_uses_default", platform: PlatformOpenAI, headers: map[string]string{"Retry-After": "invalid"}, fallbackEnabled: true, wantDelay: 12 * time.Second},
		{name: "past_date_uses_default", platform: PlatformOpenAI, headers: map[string]string{"Retry-After": pastDate}, fallbackEnabled: true, wantDelay: 12 * time.Second},
		{name: "negative_uses_default", platform: PlatformOpenAI, headers: map[string]string{"Retry-After": "-10"}, fallbackEnabled: true, wantDelay: 12 * time.Second},
		{name: "zero_uses_default", platform: PlatformOpenAI, headers: map[string]string{"Retry-After": "0"}, fallbackEnabled: true, wantDelay: 12 * time.Second},
		{name: "invalid_with_fallback_disabled", platform: PlatformOpenAI, headers: map[string]string{"Retry-After": "invalid"}, wantNoMark: true},
		{name: "missing_with_fallback_disabled", platform: PlatformOpenAI, wantNoMark: true},
		{name: "codex_window_reset_wins", platform: PlatformOpenAI, headers: map[string]string{
			"Retry-After": "90", "x-codex-primary-used-percent": "100", "x-codex-primary-reset-after-seconds": "1800", "x-codex-primary-window-minutes": "300",
		}, wantDelay: 30 * time.Minute},
		{name: "anthropic_window_reset_wins", platform: PlatformAnthropic, headers: map[string]string{
			"Retry-After": "90", "anthropic-ratelimit-unified-5h-utilization": "1.02", "anthropic-ratelimit-unified-5h-reset": strconv.FormatInt(absoluteReset.Unix(), 10),
		}, wantAbsolute: absoluteReset},
		{name: "aggregate_reset_wins", platform: PlatformAnthropic, headers: map[string]string{
			"Retry-After": "90", "anthropic-ratelimit-unified-reset": strconv.FormatInt(absoluteReset.Unix(), 10),
		}, wantAbsolute: absoluteReset},
		{name: "openai_body_reset_wins", platform: PlatformOpenAI, headers: map[string]string{"Retry-After": "90"},
			body: fmt.Sprintf(`{"error":{"type":"usage_limit_reached","resets_at":%d}}`, absoluteReset.Unix()), wantAbsolute: absoluteReset},
		{name: "invalid_aggregate_still_uses_valid_body_reset", platform: PlatformOpenAI,
			headers: map[string]string{"Retry-After": "90", "anthropic-ratelimit-unified-reset": "invalid"},
			body:    fmt.Sprintf(`{"error":{"type":"usage_limit_reached","resets_at":%d}}`, absoluteReset.Unix()), wantAbsolute: absoluteReset},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			accountRepo := &rateLimit429AccountRepoStub{}
			settingRepo := newMockSettingRepo()
			settings, err := json.Marshal(RateLimit429CooldownSettings{Enabled: tt.fallbackEnabled, CooldownSeconds: 12})
			require.NoError(t, err)
			settingRepo.data[SettingKeyRateLimit429CooldownSettings] = string(settings)
			svc := NewRateLimitService(accountRepo, nil, &config.Config{}, nil, nil)
			svc.SetSettingService(NewSettingService(settingRepo, &config.Config{}))
			headers := make(http.Header)
			for key, value := range tt.headers {
				headers.Set(key, value)
			}
			account := &Account{ID: 42, Platform: tt.platform, Type: AccountTypeOAuth}
			before := time.Now()
			svc.handle429(context.Background(), account, headers, []byte(tt.body))
			after := time.Now()
			if tt.wantNoMark {
				require.Zero(t, accountRepo.rateLimitCalls)
				return
			}
			require.Equal(t, 1, accountRepo.rateLimitCalls)
			require.Equal(t, account.ID, accountRepo.lastRateLimitID)
			if !tt.wantAbsolute.IsZero() {
				require.True(t, tt.wantAbsolute.Equal(accountRepo.lastRateLimitReset))
			} else {
				require.False(t, accountRepo.lastRateLimitReset.Before(before.Add(tt.wantDelay)))
				require.False(t, accountRepo.lastRateLimitReset.After(after.Add(tt.wantDelay)))
			}
		})
	}
}
