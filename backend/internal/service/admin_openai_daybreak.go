package service

import (
	"context"
	"maps"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// OpenAIDaybreakCapabilityReader is optional for non-gateway admin integrations.
type OpenAIDaybreakCapabilityReader interface {
	GetOpenAIDaybreakCapabilities(context.Context, *Account) (*OpenAIDaybreakCapabilities, error)
}

func (s *adminServiceImpl) SetOpenAIDaybreakCapabilityReader(reader OpenAIDaybreakCapabilityReader) {
	s.daybreakCapabilities = reader
}

func (s *adminServiceImpl) GetAccountDaybreakCapabilities(ctx context.Context, id int64) (*OpenAIDaybreakCapabilities, error) {
	account, err := s.accountRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if s.daybreakCapabilities == nil {
		return nil, infraerrors.ServiceUnavailable("DAYBREAK_CAPABILITIES_UNAVAILABLE", "Daybreak capability lookup is unavailable")
	}
	return s.daybreakCapabilities.GetOpenAIDaybreakCapabilities(ctx, account)
}

type AccountDaybreakEvidence struct {
	CredentialOwnerID       int64
	CredentialOS            string
	AuthorizationGeneration string
}

type accountDaybreakEvidenceKey struct{}

func withAccountDaybreakEvidence(ctx context.Context, id int64, evidence AccountDaybreakEvidence) context.Context {
	previous, _ := ctx.Value(accountDaybreakEvidenceKey{}).(map[int64]AccountDaybreakEvidence)
	next := maps.Clone(previous)
	if next == nil {
		next = make(map[int64]AccountDaybreakEvidence)
	}
	next[id] = evidence
	return context.WithValue(ctx, accountDaybreakEvidenceKey{}, next)
}

func AccountDaybreakEvidenceFromContext(ctx context.Context, id int64) (AccountDaybreakEvidence, bool) {
	values, _ := ctx.Value(accountDaybreakEvidenceKey{}).(map[int64]AccountDaybreakEvidence)
	evidence, ok := values[id]
	return evidence, ok
}

func HasOpenAIDaybreakSettings(extra map[string]any) bool {
	_, blue := extra[OpenAIDaybreakBlueEnabledKey]
	_, red := extra[OpenAIDaybreakRedEnabledKey]
	return blue || red
}

// NormalizeOpenAIDaybreakSettings merges only requested preference keys. It is
// also used under the row lock, so concurrent Blue/Red edits cannot break their
// dependency. Disabling Blue deliberately overrides a submitted Red value.
func NormalizeOpenAIDaybreakSettings(account *Account, updates map[string]any) (map[string]any, error) {
	patch := maps.Clone(updates)
	if !HasOpenAIDaybreakSettings(patch) {
		return patch, nil
	}
	for _, key := range []string{OpenAIDaybreakBlueEnabledKey, OpenAIDaybreakRedEnabledKey} {
		if value, exists := patch[key]; exists {
			if _, ok := value.(bool); !ok {
				return nil, infraerrors.BadRequest("DAYBREAK_SETTING_INVALID", key+" must be a boolean")
			}
		}
	}
	if !IsOpenAIDaybreakAccount(account) {
		return nil, infraerrors.BadRequest("DAYBREAK_ACCOUNT_UNSUPPORTED", "Daybreak settings require an OpenAI OAuth account")
	}
	blue, _ := account.Extra[OpenAIDaybreakBlueEnabledKey].(bool)
	red, _ := account.Extra[OpenAIDaybreakRedEnabledKey].(bool)
	if value, exists := patch[OpenAIDaybreakBlueEnabledKey].(bool); exists {
		blue = value
		if !blue {
			patch[OpenAIDaybreakRedEnabledKey] = false
		}
	}
	if value, exists := patch[OpenAIDaybreakRedEnabledKey].(bool); exists {
		red = value
	}
	if red && !blue {
		return nil, infraerrors.BadRequest("DAYBREAK_BLUE_REQUIRED", "enable Daybreak Blue before Daybreak Red")
	}
	return patch, nil
}

func daybreakSettingsEnable(account *Account, patch map[string]any) bool {
	for _, key := range []string{OpenAIDaybreakBlueEnabledKey, OpenAIDaybreakRedEnabledKey} {
		value, explicit := patch[key].(bool)
		current, _ := account.Extra[key].(bool)
		if explicit && value && !current {
			return true
		}
	}
	return false
}

func (s *adminServiceImpl) prepareDaybreakUpdate(ctx context.Context, account *Account, updates map[string]any) (context.Context, map[string]any, error) {
	patch, err := NormalizeOpenAIDaybreakSettings(account, updates)
	if err != nil {
		return ctx, nil, err
	}
	if !daybreakSettingsEnable(account, patch) {
		return ctx, patch, nil
	}
	if s.daybreakCapabilities == nil {
		return ctx, nil, infraerrors.ServiceUnavailable("DAYBREAK_CAPABILITIES_UNAVAILABLE", "check Daybreak capabilities after OAuth authorization")
	}
	capability, err := s.daybreakCapabilities.GetOpenAIDaybreakCapabilities(ctx, account)
	if err != nil {
		return ctx, nil, err
	}
	red, _ := patch[OpenAIDaybreakRedEnabledKey].(bool)
	if capability == nil || !capability.BlueAvailable || (red && !capability.RedAvailable) {
		return ctx, nil, infraerrors.BadRequest("DAYBREAK_CAPABILITY_REQUIRED", "the current OAuth model catalog does not support the requested Daybreak tier")
	}
	ctx = withAccountDaybreakEvidence(ctx, account.ID, AccountDaybreakEvidence{CredentialOwnerID: capability.CredentialOwnerID, CredentialOS: capability.CredentialOS, AuthorizationGeneration: capability.AuthorizationGeneration})
	return ctx, patch, nil
}

func validateNewAccountDaybreak(account *Account) error {
	patch, err := NormalizeOpenAIDaybreakSettings(account, account.Extra)
	if err != nil {
		return err
	}
	for _, key := range []string{OpenAIDaybreakBlueEnabledKey, OpenAIDaybreakRedEnabledKey} {
		if enabled, _ := patch[key].(bool); enabled {
			return infraerrors.BadRequest("DAYBREAK_AUTHORIZE_FIRST", "new accounts start with Daybreak disabled; authorize the account and check capabilities before enabling it")
		}
	}
	account.Extra = patch
	return nil
}
