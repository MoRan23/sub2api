package service

import (
	"context"
	"maps"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/google/uuid"
)

// AccountConfigurationIntent is an immutable, request-local write intent. Only
// typed admin entrypoints create it; a credentials/extra snapshot is not intent.
type AccountConfigurationIntent struct {
	Extra          map[string]any
	Environment    *string
	CodexTurnState *CodexTurnStateConfig
}

type accountConfigurationIntentKey struct{}
type accountConfigurationIntentScope struct {
	ids    map[int64]struct{}
	intent AccountConfigurationIntent
}

func withAccountConfigurationIntent(ctx context.Context, ids []int64, extra map[string]any, environment *string, codex ...*CodexTurnStateConfig) context.Context {
	intent := AccountConfigurationIntent{Extra: make(map[string]any)}
	if len(codex) > 0 && codex[0] != nil {
		value := *codex[0]
		if value.CollectorProxyID != nil {
			proxyID := *value.CollectorProxyID
			value.CollectorProxyID = &proxyID
		}
		intent.CodexTurnState = &value
	}
	for _, key := range []string{openAIInstallationPinEnabledKey, "enable_tls_fingerprint", "tls_fingerprint_profile_id"} {
		if value, exists := extra[key]; exists {
			intent.Extra[key] = value
		}
	}
	if environment != nil {
		value := *environment
		intent.Environment = &value
	}
	scope := accountConfigurationIntentScope{ids: make(map[int64]struct{}, len(ids)), intent: intent}
	for _, id := range ids {
		scope.ids[id] = struct{}{}
	}
	return context.WithValue(ctx, accountConfigurationIntentKey{}, scope)
}

// AccountConfigurationIntentFromContext returns a copy scoped to this account.
func AccountConfigurationIntentFromContext(ctx context.Context, id int64) AccountConfigurationIntent {
	scope, _ := ctx.Value(accountConfigurationIntentKey{}).(accountConfigurationIntentScope)
	if _, allowed := scope.ids[id]; !allowed {
		return AccountConfigurationIntent{}
	}
	intent := scope.intent
	intent.Extra = maps.Clone(intent.Extra)
	if intent.Environment != nil {
		value := *intent.Environment
		intent.Environment = &value
	}
	if intent.CodexTurnState != nil {
		value := *intent.CodexTurnState
		if value.CollectorProxyID != nil {
			proxyID := *value.CollectorProxyID
			value.CollectorProxyID = &proxyID
		}
		intent.CodexTurnState = &value
	}
	return intent
}

// PreserveAccountConfiguration must run with current loaded under the account
// row lock. It preserves only managed identity/transport fields, not all extra.
func PreserveAccountConfiguration(current, target *Account, intent AccountConfigurationIntent) error {
	target.Extra = maps.Clone(target.Extra)
	if target.Extra == nil {
		target.Extra = make(map[string]any)
	}
	copyCurrent := func(key string) {
		delete(target.Extra, key)
		if value, exists := current.Extra[key]; exists {
			target.Extra[key] = value
		}
	}
	for _, key := range []string{"enable_tls_fingerprint", "tls_fingerprint_profile_id"} {
		if value, explicit := intent.Extra[key]; explicit {
			target.Extra[key] = value
		} else {
			copyCurrent(key)
		}
	}
	delete(target.Extra, openAIInstallationRotateEnabledKey)
	delete(target.Extra, openAIPinnedInstallationIDKey)
	delete(target.Extra, openAIInstallationPinEnabledKey)
	if isOpenAICodexInstallationOwner(target) {
		if isOpenAICodexInstallationOwner(current) {
			copyCurrent(openAIPinnedInstallationIDKey)
			copyCurrent(openAIInstallationPinEnabledKey)
		} else {
			// Conversion creates a new server-owned identity inside the row lock.
			target.Extra[openAIPinnedInstallationIDKey] = uuid.NewString()
		}
		if value, explicit := intent.Extra[openAIInstallationPinEnabledKey]; explicit {
			target.Extra[openAIInstallationPinEnabledKey] = value
		}
	}
	if target.Platform == PlatformOpenAI {
		target.Credentials = maps.Clone(target.Credentials)
		if target.Credentials == nil {
			target.Credentials = make(map[string]any)
		}
		delete(target.Credentials, "user_agent")
		if !target.IsShadow() && current.Platform == PlatformOpenAI && !current.IsShadow() {
			if value, exists := current.Credentials["user_agent"]; exists {
				target.Credentials["user_agent"] = value
			}
		}
		if intent.Environment != nil {
			if !isOpenAIEnvironmentFingerprintAccount(target) {
				return infraerrors.BadRequest("OPENAI_ENVIRONMENT_FINGERPRINT_UNSUPPORTED", "environment fingerprints are supported only by non-shadow OpenAI OAuth/API-key accounts")
			}
			ua, err := BuildOpenAIUserAgentWithEnvironment(target.GetOpenAIUserAgent(), *intent.Environment)
			if err != nil {
				return infraerrors.BadRequest("OPENAI_ENVIRONMENT_FINGERPRINT_INVALID", err.Error())
			}
			target.Credentials["user_agent"] = ua
		}
		if current.Platform != target.Platform || current.Type != target.Type {
			EnsureOpenAIAccountUserAgent(target)
		}
	}
	return preserveCodexTurnStateConfiguration(current, target, intent.CodexTurnState)
}

// AccountInstallationRegenerator is deliberately separate from AccountRepository
// so read-only gateway repositories need not implement admin writes.
type AccountInstallationRegenerator interface {
	RegenerateOpenAIInstallationID(context.Context, int64, string) (string, error)
}
