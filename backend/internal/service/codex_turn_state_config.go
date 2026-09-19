package service

import (
	"context"
	"encoding/json"
	"maps"
	"reflect"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/google/uuid"
)

func (s *adminServiceImpl) validateCodexTurnStateConfig(ctx context.Context, account *Account, config *CodexTurnStateConfig) error {
	if err := ValidateCodexTurnStateConfig(account, config); err != nil {
		return err
	}
	if config != nil && config.CollectorProxyID != nil {
		if s.proxyRepo == nil {
			return infraerrors.BadRequest("CODEX_TURN_STATE_PROXY_UNAVAILABLE", "collector proxy repository is unavailable")
		}
		if _, err := s.proxyRepo.GetByID(ctx, *config.CollectorProxyID); err != nil {
			return err
		}
	}
	return nil
}

const (
	CodexTurnStateExtraKey           = "codex_turn_state"
	CodexTurnStateGenerationExtraKey = "codex_turn_state_generation"
)

// CodexTurnStateConfig is explicit administrator configuration. State and tokens
// are stored separately and never accepted through this object.
type CodexTurnStateConfig struct {
	Enabled          bool   `json:"enabled"`
	AccountType      string `json:"account_type"`
	CollectorProxyID *int64 `json:"collector_proxy_id"`
}

func IsCodexTurnStateAccount(account *Account) bool {
	return account != nil && account.IsOpenAIOAuth() && !account.IsShadow() && !account.IsOpenAIPersonalAccessToken() && !account.IsOpenAIAgentIdentity()
}

func CodexTurnStateConfigForAccount(account *Account) CodexTurnStateConfig {
	config := CodexTurnStateConfig{AccountType: "auto"}
	if account == nil {
		return config
	}
	if raw, exists := account.Extra[CodexTurnStateExtraKey]; exists {
		if encoded, err := json.Marshal(raw); err == nil {
			_ = json.Unmarshal(encoded, &config)
		}
	}
	if config.AccountType == "" {
		config.AccountType = "auto"
	}
	if !IsCodexTurnStateAccount(account) {
		config.Enabled = false
	}
	return config
}

func CodexTurnStateGenerationForAccount(account *Account) string {
	if account == nil {
		return ""
	}
	value, _ := account.Extra[CodexTurnStateGenerationExtraKey].(string)
	return value
}

func CodexTurnStateAccountTypeForAccount(account *Account) string {
	if !IsCodexTurnStateAccount(account) {
		return ""
	}
	config := CodexTurnStateConfigForAccount(account)
	if config.AccountType == "personal" || config.AccountType == "team_business" {
		return config.AccountType
	}
	switch strings.ToLower(strings.TrimSpace(account.GetCredential("plan_type"))) {
	case "free", "go", "plus", "pro", "personal":
		return "personal"
	case "team", "business", "chatgptteam", "chatgptbusiness":
		return "team_business"
	default:
		return ""
	}
}

func ValidateCodexTurnStateConfig(account *Account, config *CodexTurnStateConfig) error {
	if config == nil {
		return nil
	}
	if !IsCodexTurnStateAccount(account) {
		return infraerrors.BadRequest("CODEX_TURN_STATE_UNSUPPORTED", "turn-state settings are supported only by non-shadow standard OpenAI OAuth accounts; configure shadow accounts on their credential parent")
	}
	switch config.AccountType {
	case "auto", "personal", "team_business":
	default:
		return infraerrors.BadRequest("CODEX_TURN_STATE_INVALID", "account_type must be auto, personal, or team_business")
	}
	if config.CollectorProxyID != nil && *config.CollectorProxyID <= 0 {
		return infraerrors.BadRequest("CODEX_TURN_STATE_INVALID", "collector_proxy_id must be positive or null")
	}
	return nil
}

func codexTurnStateConfigMap(config CodexTurnStateConfig) map[string]any {
	var proxy any
	if config.CollectorProxyID != nil {
		proxy = *config.CollectorProxyID
	}
	return map[string]any{"enabled": config.Enabled, "account_type": config.AccountType, "collector_proxy_id": proxy}
}

// StripCodexTurnStateManagedExtra removes server-owned configuration/runtime
// keys from untyped snapshots. Typed callers reapply configuration explicitly.
func StripCodexTurnStateManagedExtra(extra map[string]any) map[string]any {
	extra = maps.Clone(extra)
	for key := range extra {
		if key == CodexTurnStateExtraKey || strings.HasPrefix(key, "codex_turn_state_") {
			delete(extra, key)
		}
	}
	return extra
}

func PrepareCodexTurnStateForCreate(account *Account, config *CodexTurnStateConfig) error {
	if err := ValidateCodexTurnStateConfig(account, config); err != nil {
		return err
	}
	account.Extra = StripCodexTurnStateManagedExtra(account.Extra)
	if account.Extra == nil {
		account.Extra = make(map[string]any)
	}
	if IsCodexTurnStateAccount(account) && config != nil {
		value := CodexTurnStateConfig{AccountType: "auto"}
		if config != nil {
			value = *config
		}
		account.Extra[CodexTurnStateExtraKey] = codexTurnStateConfigMap(value)
		account.Extra[CodexTurnStateGenerationExtraKey] = uuid.NewString()
	}
	return nil
}

// CodexTurnStateCredentialKeys is the auth identity subset. Usage, model mapping,
// environment, and ordinary account metadata do not invalidate cached states.
var CodexTurnStateCredentialKeys = []string{"access_token", "refresh_token", "id_token", "chatgpt_account_id", "chatgpt_user_id", "organization_id", "client_id", "account_id", "auth_mode", "openai_auth_mode"}

func codexTurnStateCredentialsChanged(current, target *Account) bool {
	for _, key := range CodexTurnStateCredentialKeys {
		if !reflect.DeepEqual(current.Credentials[key], target.Credentials[key]) {
			return true
		}
	}
	// Auto classification is part of the configuration's meaning.
	return CodexTurnStateAccountTypeForAccount(current) != CodexTurnStateAccountTypeForAccount(target)
}

func preserveCodexTurnStateConfiguration(current, target *Account, requested *CodexTurnStateConfig) error {
	if err := ValidateCodexTurnStateConfig(target, requested); err != nil {
		return err
	}
	target.Extra = StripCodexTurnStateManagedExtra(target.Extra)
	if !IsCodexTurnStateAccount(target) {
		return nil
	}
	if _, configured := current.Extra[CodexTurnStateExtraKey]; !configured && requested == nil {
		return nil
	}
	if target.Extra == nil {
		target.Extra = make(map[string]any)
	}
	oldConfig := CodexTurnStateConfigForAccount(current)
	config := oldConfig
	if !IsCodexTurnStateAccount(current) {
		config = CodexTurnStateConfig{AccountType: "auto"}
	}
	if requested != nil {
		config = *requested
	}
	target.Extra[CodexTurnStateExtraKey] = codexTurnStateConfigMap(config)
	generation := CodexTurnStateGenerationForAccount(current)
	if generation == "" || !reflect.DeepEqual(oldConfig, config) || !IsCodexTurnStateAccount(current) || codexTurnStateCredentialsChanged(current, target) {
		generation = uuid.NewString()
	}
	target.Extra[CodexTurnStateGenerationExtraKey] = generation
	return nil
}
