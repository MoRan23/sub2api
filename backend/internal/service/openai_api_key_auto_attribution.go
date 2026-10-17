package service

import infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"

const OpenAIAPIKeyAutoAttributionEnabledExtraKey = "openai_api_key_auto_attribution_enabled"

// IsOpenAIAPIKeyAutoAttributionEnabled is an enqueue preference. Engine retains
// its existing periodic attribution independently of this generic API-key flag.
func (a *Account) IsOpenAIAPIKeyAutoAttributionEnabled() bool {
	if a == nil || !a.IsOpenAIApiKey() || a.IsCodexEngine() {
		return false
	}
	mode, exists := a.Extra[OpenAIAPIKeyModeExtraKey]
	if exists && mode != "generic" {
		return false
	}
	enabled, _ := a.Extra[OpenAIAPIKeyAutoAttributionEnabledExtraKey].(bool)
	return enabled
}

func HasOpenAIAPIKeyAutoAttributionSetting(extra map[string]any) bool {
	_, exists := extra[OpenAIAPIKeyAutoAttributionEnabledExtraKey]
	return exists
}

func ValidateOpenAIAPIKeyAutoAttribution(platform, accountType string, extra map[string]any) error {
	value, exists := extra[OpenAIAPIKeyAutoAttributionEnabledExtraKey]
	if !exists {
		return nil
	}
	if _, ok := value.(bool); !ok {
		return infraerrors.BadRequest("OPENAI_API_KEY_AUTO_ATTRIBUTION_INVALID", "openai_api_key_auto_attribution_enabled must be a boolean")
	}
	// Keep the preference when switching between generic and Engine; it only
	// applies in generic mode and never disables Engine's automatic tests.
	if platform != PlatformOpenAI || accountType != AccountTypeAPIKey {
		return infraerrors.BadRequest("OPENAI_API_KEY_AUTO_ATTRIBUTION_UNSUPPORTED", "automatic API-key attribution is only supported by OpenAI API key accounts")
	}
	return nil
}
