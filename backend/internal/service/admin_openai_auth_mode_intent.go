package service

import (
	"maps"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

func explicitOpenAIAuthModeChange(current *Account, requested bool, credentials map[string]any) (bool, map[string]any, error) {
	if !requested {
		return false, credentials, nil
	}
	invalid := func() (bool, map[string]any, error) {
		return false, nil, infraerrors.BadRequest("OPENAI_AUTH_MODE_CHANGE_INVALID", "an explicit credential mode change requires a non-shadow OpenAI OAuth account and a valid auth_mode")
	}
	if current == nil || !current.IsOpenAIOAuth() || current.IsShadow() {
		return invalid()
	}
	mode, exists := "", false
	for _, key := range []string{openAIAuthModeCredentialKey, openAIAuthModeLegacyCredentialKey} {
		raw, supplied := credentials[key]
		if !supplied {
			continue
		}
		value, ok := raw.(string)
		if !ok {
			return invalid()
		}
		var normalized string
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "", "oauth", "chatgpt":
			normalized = "oauth"
		case "personalaccesstoken", "personal_access_token":
			normalized = OpenAIAuthModePersonalAccessToken
		case "agentidentity":
			normalized = OpenAIAuthModeAgentIdentity
		default:
			return invalid()
		}
		if exists && normalized != mode {
			return invalid()
		}
		mode, exists = normalized, true
	}
	if !exists {
		return invalid()
	}
	currentMode := "oauth"
	if current.IsOpenAIPersonalAccessToken() {
		currentMode = OpenAIAuthModePersonalAccessToken
	}
	if current.IsOpenAIAgentIdentity() {
		currentMode = OpenAIAuthModeAgentIdentity
	}
	if mode == currentMode {
		return false, credentials, nil
	}
	// Explicit mode transitions clear the legacy alias so an old PAT marker
	// cannot override the newly selected mode after ordinary credential merging.
	out := maps.Clone(credentials)
	out[openAIAuthModeCredentialKey] = mode
	out[openAIAuthModeLegacyCredentialKey] = ""
	return true, out, nil
}
