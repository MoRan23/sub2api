package service

import infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"

const OpenAIAPIKeyModeExtraKey = "openai_api_key_mode"

// IsCodexEngine identifies the platform API adapter, independently of generic
// passthrough and capability probe settings.
func (a *Account) IsCodexEngine() bool {
	return a != nil && a.IsOpenAIApiKey() && a.Extra[OpenAIAPIKeyModeExtraKey] == "codex_engine"
}

func ValidateOpenAIAPIKeyMode(platform, accountType string, extra map[string]any) error {
	value, exists := extra[OpenAIAPIKeyModeExtraKey]
	if !exists {
		return nil
	}
	mode, ok := value.(string)
	if !ok || (mode != "generic" && mode != "codex_engine") {
		return infraerrors.BadRequest("OPENAI_API_KEY_MODE_INVALID", "openai_api_key_mode must be generic or codex_engine")
	}
	if platform != PlatformOpenAI || accountType != AccountTypeAPIKey {
		return infraerrors.BadRequest("OPENAI_API_KEY_MODE_UNSUPPORTED", "access mode is only supported by OpenAI API key accounts")
	}
	return nil
}
