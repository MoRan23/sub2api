package repository

import (
	"context"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestDaybreakLockedAPIKeySettingsDoNotRequireEvidence(t *testing.T) {
	current := &service.Account{ID: 42, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "old-key", "base_url": "https://old.invalid"}}
	target := *current
	target.Credentials = map[string]any{"api_key": "new-key", "base_url": "https://new.invalid"}
	// No database client or evidence is needed even when credentials change in
	// the same write as an explicit enable.
	err := validateDaybreakPatchLocked(context.Background(), nil, current, &target, map[string]any{
		service.OpenAIDaybreakBlueEnabledKey: true,
		service.OpenAIDaybreakRedEnabledKey:  true,
	})
	require.NoError(t, err)
}

func TestDaybreakLockedValidationRemainsRequired(t *testing.T) {
	for _, tc := range []struct {
		name        string
		accountType string
		extra       map[string]any
		patch       map[string]any
		reason      string
	}{
		{name: "OAuth evidence", accountType: service.AccountTypeOAuth, patch: map[string]any{service.OpenAIDaybreakBlueEnabledKey: true}, reason: "DAYBREAK_CAPABILITY_STALE"},
		{name: "Engine excluded", accountType: service.AccountTypeAPIKey, extra: map[string]any{service.OpenAIAPIKeyModeExtraKey: "codex_engine"}, patch: map[string]any{service.OpenAIDaybreakBlueEnabledKey: true}, reason: "DAYBREAK_ACCOUNT_UNSUPPORTED"},
		{name: "latest Blue disabled", accountType: service.AccountTypeAPIKey, extra: map[string]any{service.OpenAIDaybreakBlueEnabledKey: false}, patch: map[string]any{service.OpenAIDaybreakRedEnabledKey: true}, reason: "DAYBREAK_BLUE_REQUIRED"},
		{name: "invalid setting", accountType: service.AccountTypeAPIKey, patch: map[string]any{service.OpenAIDaybreakBlueEnabledKey: "true"}, reason: "DAYBREAK_SETTING_INVALID"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := &service.Account{ID: 42, Platform: service.PlatformOpenAI, Type: tc.accountType, Extra: tc.extra}
			err := validateDaybreakPatchLocked(context.Background(), nil, account, account, tc.patch)
			require.Equal(t, tc.reason, infraerrors.Reason(err))
		})
	}
}
