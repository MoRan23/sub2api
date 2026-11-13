package service

import (
	"maps"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const (
	ForceOpenAIFastExtraKey      = "force_openai_fast"
	ForceOpenAIUltrafastExtraKey = "force_openai_ultrafast"
	FreeOpenAIFastExtraKey       = "free_openai_fast"
	FreeOpenAIUltrafastExtraKey  = "free_openai_ultrafast"
)

var openAIAccountFastSettingKeys = [...]string{
	ForceOpenAIFastExtraKey, ForceOpenAIUltrafastExtraKey,
	FreeOpenAIFastExtraKey, FreeOpenAIUltrafastExtraKey,
}

// Fast preferences belong to the business account, including Engine accounts
// and credential shadows. Shared credentials do not inherit these settings.
func (a *Account) IsForceOpenAIFastEnabled() bool {
	return a != nil && a.Platform == PlatformOpenAI && a.getExtraBool(ForceOpenAIFastExtraKey)
}

func (a *Account) IsForceOpenAIUltrafastEnabled() bool {
	return a != nil && a.Platform == PlatformOpenAI && a.getExtraBool(ForceOpenAIUltrafastExtraKey)
}

func (a *Account) IsFreeOpenAIFastEnabled() bool {
	return a != nil && a.Platform == PlatformOpenAI && a.getExtraBool(FreeOpenAIFastExtraKey)
}

func (a *Account) IsFreeOpenAIUltrafastEnabled() bool {
	return a != nil && a.Platform == PlatformOpenAI && a.getExtraBool(FreeOpenAIUltrafastExtraKey)
}

func HasOpenAIAccountFastSettings(extra map[string]any) bool {
	for _, key := range openAIAccountFastSettingKeys {
		if _, exists := extra[key]; exists {
			return true
		}
	}
	return false
}

func ValidateOpenAIAccountFastSettings(platform string, extra map[string]any) error {
	for _, key := range openAIAccountFastSettingKeys {
		value, exists := extra[key]
		if !exists {
			continue
		}
		if _, ok := value.(bool); !ok {
			return infraerrors.BadRequest("OPENAI_ACCOUNT_FAST_INVALID", key+" must be a boolean")
		}
		if platform != PlatformOpenAI {
			return infraerrors.BadRequest("OPENAI_ACCOUNT_FAST_UNSUPPORTED", "account Fast settings require an OpenAI account")
		}
	}
	fast, _ := extra[ForceOpenAIFastExtraKey].(bool)
	ultrafast, _ := extra[ForceOpenAIUltrafastExtraKey].(bool)
	if fast && ultrafast {
		return infraerrors.BadRequest("OPENAI_ACCOUNT_FAST_CONFLICT", "force_openai_fast and force_openai_ultrafast cannot both be enabled")
	}
	return nil
}

// ValidateOpenAIAccountFastSettingsPatch checks the effective force pair. An
// omitted key preserves its current value, so switching tiers requires an
// explicit false for the previously enabled tier. Recheck under the row lock.
func ValidateOpenAIAccountFastSettingsPatch(account *Account, patch map[string]any) error {
	if !HasOpenAIAccountFastSettings(patch) {
		return nil
	}
	if account == nil {
		return ErrAccountNotFound
	}
	if err := ValidateOpenAIAccountFastSettings(account.Platform, patch); err != nil {
		return err
	}
	effective := map[string]any{
		ForceOpenAIFastExtraKey:      account.IsForceOpenAIFastEnabled(),
		ForceOpenAIUltrafastExtraKey: account.IsForceOpenAIUltrafastEnabled(),
	}
	for _, key := range openAIAccountFastSettingKeys {
		if value, explicit := patch[key]; explicit {
			effective[key] = value
		}
	}
	return ValidateOpenAIAccountFastSettings(account.Platform, effective)
}

func preserveOpenAIAccountFastSettings(current, target *Account, patch map[string]any) error {
	if err := ValidateOpenAIAccountFastSettingsPatch(current, patch); err != nil {
		return err
	}
	target.Extra = maps.Clone(target.Extra)
	if target.Extra == nil {
		target.Extra = make(map[string]any)
	}
	for _, key := range openAIAccountFastSettingKeys {
		delete(target.Extra, key)
		if target.Platform != PlatformOpenAI {
			continue
		}
		if value, explicit := patch[key]; explicit {
			target.Extra[key] = value
		} else if value, exists := current.Extra[key]; exists {
			target.Extra[key] = value
		}
	}
	return nil
}
