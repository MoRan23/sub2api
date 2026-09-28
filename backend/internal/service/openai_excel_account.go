package service

import (
	"maps"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/google/uuid"
)

const (
	OpenAIExcelUpstreamEnabledExtraKey    = "openai_excel_upstream_enabled"
	OpenAIUpstreamRouteGenerationExtraKey = "openai_upstream_route_generation"
	OpenAIUpstreamKindCodex               = "codex"
	OpenAIUpstreamKindExcel               = "excel"
)

// SupportsOpenAIExcelUpstream deliberately checks the business account, not a
// resolved credential owner: Spark must not inherit its parent's Excel route.
func SupportsOpenAIExcelUpstream(account *Account) bool {
	if !IsOpenAIOAuthOSProfileOwner(account) {
		return false
	}
	for _, key := range []string{openAIAuthModeCredentialKey, openAIAuthModeLegacyCredentialKey} {
		if strings.EqualFold(strings.TrimSpace(account.GetCredential(key)), "agent_identity") {
			return false
		}
	}
	return true
}

func (a *Account) IsOpenAIExcelUpstreamEnabled() bool {
	if !SupportsOpenAIExcelUpstream(a) {
		return false
	}
	enabled, _ := a.Extra[OpenAIExcelUpstreamEnabledExtraKey].(bool)
	return enabled
}

func (a *Account) OpenAIUpstreamKind() string {
	if a.IsOpenAIExcelUpstreamEnabled() {
		return OpenAIUpstreamKindExcel
	}
	return OpenAIUpstreamKindCodex
}

func (a *Account) OpenAIUpstreamRouteGeneration() string {
	// The generation is a private route fence. Keep reading it even after an
	// account changes credential type so an in-flight Excel/Codex job cannot
	// match an account that has gone through an ineligible state.
	if a == nil {
		return ""
	}
	return strings.TrimSpace(a.GetExtraString(OpenAIUpstreamRouteGenerationExtraKey))
}

// ValidateOpenAIExcelUpstreamExtra is shared by all explicit admin writes.
// Absence means unchanged; false is also accepted to remove an obsolete flag.
func ValidateOpenAIExcelUpstreamExtra(account *Account, extra map[string]any) error {
	raw, exists := extra[OpenAIExcelUpstreamEnabledExtraKey]
	if !exists {
		return nil
	}
	enabled, ok := raw.(bool)
	if !ok {
		return infraerrors.BadRequest("OPENAI_EXCEL_UPSTREAM_INVALID", "openai_excel_upstream_enabled must be a boolean")
	}
	if enabled && !SupportsOpenAIExcelUpstream(account) {
		return infraerrors.BadRequest("OPENAI_EXCEL_UPSTREAM_UNSUPPORTED", "Excel upstream requires a regular, non-shadow OpenAI OAuth account")
	}
	return nil
}

func prepareOpenAIExcelUpstreamForCreate(account *Account) error {
	if err := ValidateOpenAIExcelUpstreamExtra(account, account.Extra); err != nil {
		return err
	}
	account.Extra = maps.Clone(account.Extra)
	delete(account.Extra, OpenAIUpstreamRouteGenerationExtraKey)
	if !SupportsOpenAIExcelUpstream(account) {
		delete(account.Extra, OpenAIExcelUpstreamEnabledExtraKey)
	} else if account.IsOpenAIExcelUpstreamEnabled() {
		account.Extra[OpenAIUpstreamRouteGenerationExtraKey] = uuid.NewString()
	}
	return nil
}

// preserveOpenAIExcelUpstreamConfiguration runs with the current account row
// locked. Background snapshots may never change the selected upstream.
func preserveOpenAIExcelUpstreamConfiguration(current, target *Account, intent AccountConfigurationIntent) error {
	delete(target.Extra, OpenAIExcelUpstreamEnabledExtraKey)
	delete(target.Extra, OpenAIUpstreamRouteGenerationExtraKey)
	if !SupportsOpenAIExcelUpstream(target) {
		// Keep a generation on the ineligible account when the old snapshot had
		// one. If the account is leaving an enabled Excel route, advance it so
		// both the route kind and the generation change atomically.
		if SupportsOpenAIExcelUpstream(current) {
			target.Extra[OpenAIUpstreamRouteGenerationExtraKey] = uuid.NewString()
		} else if generation := current.OpenAIUpstreamRouteGeneration(); generation != "" {
			target.Extra[OpenAIUpstreamRouteGenerationExtraKey] = generation
		}
		return ValidateOpenAIExcelUpstreamExtra(target, intent.Extra)
	}
	if value, exists := current.Extra[OpenAIExcelUpstreamEnabledExtraKey]; exists && SupportsOpenAIExcelUpstream(current) {
		target.Extra[OpenAIExcelUpstreamEnabledExtraKey] = value
	}
	if value, explicit := intent.Extra[OpenAIExcelUpstreamEnabledExtraKey]; explicit {
		if err := ValidateOpenAIExcelUpstreamExtra(target, intent.Extra); err != nil {
			return err
		}
		target.Extra[OpenAIExcelUpstreamEnabledExtraKey] = value
	}
	if SupportsOpenAIExcelUpstream(current) != SupportsOpenAIExcelUpstream(target) || current.IsOpenAIExcelUpstreamEnabled() != target.IsOpenAIExcelUpstreamEnabled() {
		target.Extra[OpenAIUpstreamRouteGenerationExtraKey] = uuid.NewString()
	} else if generation := current.OpenAIUpstreamRouteGeneration(); generation != "" {
		target.Extra[OpenAIUpstreamRouteGenerationExtraKey] = generation
	}
	return nil
}
