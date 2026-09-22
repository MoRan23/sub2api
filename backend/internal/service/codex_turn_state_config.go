package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"reflect"
	"slices"
	"strings"
	"unicode"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/google/uuid"
)

func (s *adminServiceImpl) validateCodexTurnStateConfig(ctx context.Context, account *Account, config *CodexTurnStateConfig) error {
	if err := ValidateCodexTurnStateConfig(account, config); err != nil {
		return err
	}
	if config != nil && len(CodexTurnStateCollectorProxyIDs(*config)) > 0 {
		if s.proxyRepo == nil {
			return infraerrors.BadRequest("CODEX_TURN_STATE_PROXY_UNAVAILABLE", "collector proxy repository is unavailable")
		}
		for _, id := range CodexTurnStateCollectorProxyIDs(*config) {
			if _, err := s.proxyRepo.GetByID(ctx, id); err != nil {
				return err
			}
		}
	}
	return nil
}

const (
	CodexTurnStateExtraKey                = "codex_turn_state"
	CodexTurnStateGenerationExtraKey      = "codex_turn_state_generation"
	CodexTurnStateCredentialEpochExtraKey = "codex_turn_state_credential_epoch"
)

// CodexTurnStateConfig is explicit administrator configuration. State and tokens
// are stored separately and never accepted through this object.
type CodexTurnStateConfig struct {
	Enabled           bool    `json:"enabled"`
	AccountType       string  `json:"account_type"`
	CollectorProxyIDs []int64 `json:"collector_proxy_ids,omitzero"`
	CollectorProxyID  *int64  `json:"collector_proxy_id,omitempty"`
	// Nil preserves the existing value on update and defaults to true on create.
	UseTicketProxy *bool `json:"use_ticket_proxy,omitempty"`
}

// Presence matters: a malformed new list cannot resurrect a legacy destination.
// Keep omitted lists nil for legacy requests; an explicit [] stays non-nil.
func (config *CodexTurnStateConfig) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if raw, exists := fields["collector_proxy_ids"]; exists {
		raw = bytes.TrimSpace(raw)
		if len(raw) == 0 || raw[0] != '[' {
			return errors.New("collector_proxy_ids must be an array")
		}
	}
	if raw, exists := fields["use_ticket_proxy"]; exists {
		raw = bytes.TrimSpace(raw)
		if !bytes.Equal(raw, []byte("true")) && !bytes.Equal(raw, []byte("false")) {
			return errors.New("use_ticket_proxy must be a boolean")
		}
	}
	type plainConfig CodexTurnStateConfig
	var decoded plainConfig
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*config = CodexTurnStateConfig(decoded)
	return nil
}

// CodexTurnStateUseTicketProxy preserves the pre-switch behavior for old accounts
// and clients. The flag controls business routing, not ticket or Cookie admission.
func CodexTurnStateUseTicketProxy(config CodexTurnStateConfig) bool {
	return config.UseTicketProxy == nil || *config.UseTicketProxy
}

// CodexTurnStateCollectorProxyIDs returns an independent ordered copy. A present
// empty array explicitly clears collectors and must never fall back to legacy ID.
func CodexTurnStateCollectorProxyIDs(config CodexTurnStateConfig) []int64 {
	if config.CollectorProxyIDs != nil {
		return append([]int64{}, config.CollectorProxyIDs...)
	}
	if config.CollectorProxyID != nil {
		return []int64{*config.CollectorProxyID}
	}
	return []int64{}
}

func IsCodexTurnStateAccount(account *Account) bool {
	return account != nil && account.IsOpenAIOAuth() && !account.IsShadow() && !account.IsOpenAIPersonalAccessToken() && !account.IsOpenAIAgentIdentity() &&
		!strings.EqualFold(strings.TrimSpace(account.GetCredential("openai_auth_mode")), OpenAIAuthModeAgentIdentity)
}

func CodexTurnStateConfigForAccount(account *Account) CodexTurnStateConfig {
	config := CodexTurnStateConfig{AccountType: "auto", CollectorProxyIDs: []int64{}, UseTicketProxy: new(true)}
	if account == nil {
		return config
	}
	if raw, exists := account.Extra[CodexTurnStateExtraKey]; exists {
		if encoded, err := json.Marshal(raw); err == nil {
			var decoded CodexTurnStateConfig
			if err := json.Unmarshal(encoded, &decoded); err == nil {
				config = decoded
			}
		}
	}
	if config.AccountType == "" {
		config.AccountType = "auto"
	}
	config.UseTicketProxy = new(CodexTurnStateUseTicketProxy(config))
	config.CollectorProxyIDs = CodexTurnStateCollectorProxyIDs(config)
	config.CollectorProxyID = nil
	if len(config.CollectorProxyIDs) > 0 {
		firstID := config.CollectorProxyIDs[0]
		config.CollectorProxyID = &firstID
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

// CodexTurnStateCredentialEpochForAccount is a server-owned, opaque identity
// fence. It does not disclose or fingerprint credentials and is never exported.
func CodexTurnStateCredentialEpochForAccount(account *Account) string {
	if !IsCodexTurnStateAccount(account) {
		return ""
	}
	value, _ := account.Extra[CodexTurnStateCredentialEpochExtraKey].(string)
	if strings.TrimSpace(value) == "" {
		return ""
	}
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
	plan := strings.Map(func(r rune) rune {
		if r == '_' || r == '-' || unicode.IsSpace(r) {
			return -1
		}
		return unicode.ToLower(r)
	}, account.GetCredential("plan_type"))
	switch plan {
	case "free", "go", "plus", "pro", "personal", "chatgptpro", "prolite":
		return "personal"
	case "team", "business", "chatgptteam", "chatgptbusiness", "selfservebusinessprolite":
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
	seen := make(map[int64]struct{})
	for _, id := range CodexTurnStateCollectorProxyIDs(*config) {
		if id <= 0 {
			return infraerrors.BadRequest("CODEX_TURN_STATE_INVALID", "collector_proxy_ids must contain positive integer IDs")
		}
		if _, exists := seen[id]; exists {
			return infraerrors.BadRequest("CODEX_TURN_STATE_INVALID", "collector_proxy_ids must not contain duplicate IDs")
		}
		seen[id] = struct{}{}
	}
	return nil
}

func codexTurnStateConfigMap(config CodexTurnStateConfig) map[string]any {
	return CodexTurnStateConfigJSON(config)
}

// CodexTurnStateConfigJSON is the canonical stored representation. Legacy input
// remains supported, but new writes never persist a second conflicting carrier.
func CodexTurnStateConfigJSON(config CodexTurnStateConfig) map[string]any {
	return map[string]any{"enabled": config.Enabled, "account_type": config.AccountType, "collector_proxy_ids": CodexTurnStateCollectorProxyIDs(config), "use_ticket_proxy": CodexTurnStateUseTicketProxy(config)}
}

// ValidateCodexTurnStateConfigUpdate must run against the current locked row.
// An older client cannot silently collapse an existing multi-proxy list.
func ValidateCodexTurnStateConfigUpdate(current, target *Account, requested *CodexTurnStateConfig) error {
	if err := ValidateCodexTurnStateConfig(target, requested); err != nil {
		return err
	}
	if requested != nil && requested.CollectorProxyIDs == nil && len(CodexTurnStateCollectorProxyIDs(CodexTurnStateConfigForAccount(current))) > 1 {
		return infraerrors.BadRequest("CODEX_TURN_STATE_LEGACY_PROXY_UPDATE", "use collector_proxy_ids to update an account with multiple collector proxies")
	}
	return nil
}

func codexTurnStateConfigsEqual(a, b CodexTurnStateConfig) bool {
	// Routing preference does not change the contents or validity of a bundle.
	// Switching it must leave already collected tickets available immediately.
	return a.Enabled == b.Enabled && a.AccountType == b.AccountType && slices.Equal(CodexTurnStateCollectorProxyIDs(a), CodexTurnStateCollectorProxyIDs(b))
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
	if IsCodexTurnStateAccount(account) {
		account.Extra[CodexTurnStateCredentialEpochExtraKey] = uuid.NewString()
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

func codexTurnStateAuthCredentialsChanged(current, target *Account) bool {
	for _, key := range CodexTurnStateCredentialKeys {
		if !reflect.DeepEqual(current.Credentials[key], target.Credentials[key]) {
			return true
		}
	}
	return false
}

func codexTurnStateCredentialsChanged(current, target *Account) bool {
	// Auto classification affects cache admission, not the credential epoch.
	return codexTurnStateAuthCredentialsChanged(current, target) ||
		CodexTurnStateAccountTypeForAccount(current) != CodexTurnStateAccountTypeForAccount(target)
}

// CodexTurnStateCollectorProxyOnlyChanged permits carrying runtime state across
// a collector destination change, while keeping the new publication generation.
// Credential, qualification, admission-policy, and enablement changes never qualify.
func CodexTurnStateCollectorProxyOnlyChanged(current, target *Account) bool {
	if !IsCodexTurnStateAccount(current) || !IsCodexTurnStateAccount(target) || current.ID != target.ID {
		return false
	}
	previous, next := CodexTurnStateConfigForAccount(current), CodexTurnStateConfigForAccount(target)
	if !previous.Enabled || !next.Enabled || previous.AccountType != next.AccountType || slices.Equal(CodexTurnStateCollectorProxyIDs(previous), CodexTurnStateCollectorProxyIDs(next)) {
		return false
	}
	accountType := CodexTurnStateAccountTypeForAccount(current)
	if accountType == "" || accountType != CodexTurnStateAccountTypeForAccount(target) || codexTurnStateAuthCredentialsChanged(current, target) {
		return false
	}
	epoch := CodexTurnStateCredentialEpochForAccount(current)
	previousGeneration, nextGeneration := CodexTurnStateGenerationForAccount(current), CodexTurnStateGenerationForAccount(target)
	return epoch != "" && epoch == CodexTurnStateCredentialEpochForAccount(target) &&
		strings.TrimSpace(previousGeneration) != "" && strings.TrimSpace(nextGeneration) != "" && previousGeneration != nextGeneration
}

func preserveCodexTurnStateConfiguration(current, target *Account, requested *CodexTurnStateConfig) error {
	if err := ValidateCodexTurnStateConfigUpdate(current, target, requested); err != nil {
		return err
	}
	target.Extra = StripCodexTurnStateManagedExtra(target.Extra)
	if !IsCodexTurnStateAccount(target) {
		return nil
	}
	if target.Extra == nil {
		target.Extra = make(map[string]any)
	}
	epoch := CodexTurnStateCredentialEpochForAccount(current)
	if epoch == "" || !IsCodexTurnStateAccount(current) || codexTurnStateAuthCredentialsChanged(current, target) {
		epoch = uuid.NewString()
	}
	target.Extra[CodexTurnStateCredentialEpochExtraKey] = epoch
	if _, configured := current.Extra[CodexTurnStateExtraKey]; !configured && requested == nil {
		return nil
	}
	oldConfig := CodexTurnStateConfigForAccount(current)
	config := oldConfig
	if !IsCodexTurnStateAccount(current) {
		config = CodexTurnStateConfig{AccountType: "auto"}
	}
	if requested != nil {
		config = *requested
		if config.UseTicketProxy == nil {
			config.UseTicketProxy = new(CodexTurnStateUseTicketProxy(oldConfig))
		}
	}
	target.Extra[CodexTurnStateExtraKey] = codexTurnStateConfigMap(config)
	generation := CodexTurnStateGenerationForAccount(current)
	if generation == "" || !codexTurnStateConfigsEqual(oldConfig, config) || !IsCodexTurnStateAccount(current) || codexTurnStateCredentialsChanged(current, target) {
		generation = uuid.NewString()
	}
	target.Extra[CodexTurnStateGenerationExtraKey] = generation
	return nil
}
