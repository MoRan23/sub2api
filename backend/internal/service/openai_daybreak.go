package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	OpenAIDaybreakBlueEnabledKey = "openai_daybreak_blue_enabled"
	OpenAIDaybreakRedEnabledKey  = "openai_daybreak_red_enabled"
)

// OpenAIDaybreakModel separates approval tier from the wire program. In
// particular, Astra and 6.1 Sol require Red approval but send daybreak_blue.
type OpenAIDaybreakModel struct {
	Model        string `json:"model"`
	RequiredTier string `json:"required_tier"`
	Cyber        string `json:"cyber"`
}

type OpenAIDaybreakCapabilities struct {
	CheckedAt     *time.Time            `json:"checked_at"`
	BlueAvailable bool                  `json:"blue_available"`
	RedAvailable  bool                  `json:"red_available"`
	Models        []OpenAIDaybreakModel `json:"models"`
	Reason        string                `json:"reason"`
	// Evidence for the management transaction, never supplied by the client.
	CredentialOwnerID       int64  `json:"-"`
	CredentialOS            string `json:"-"`
	AuthorizationGeneration string `json:"-"`
	CredentialIdentity      string `json:"-"`
}

// This is a tier policy, not an availability list: every entry also requires
// the selected authorization's live available_access_programs declaration.
// Unknown models are not classified by prefix or by the account's plan name.
// https://help.openai.com/en/articles/20001258-openai-daybreak-trusted-access-for-cyber-overview
// https://developers.openai.com/api/docs/guides/daybreak (2026-10-05)
var openAIDaybreakModels = map[string]OpenAIDaybreakModel{
	"gpt-5.5":                  {Model: "gpt-5.5", RequiredTier: "blue", Cyber: "daybreak_blue"},
	"gpt-5.6-sol":              {Model: "gpt-5.6-sol", RequiredTier: "blue", Cyber: "daybreak_blue"},
	"gpt-6-sol":                {Model: "gpt-6-sol", RequiredTier: "blue", Cyber: "daybreak_blue"},
	"gpt-6-luna":               {Model: "gpt-6-luna", RequiredTier: "blue", Cyber: "daybreak_blue"},
	"gpt-daybreak-blue-latest": {Model: "gpt-daybreak-blue-latest", RequiredTier: "blue", Cyber: "daybreak_blue"},
	"gpt-6-astra":              {Model: "gpt-6-astra", RequiredTier: "red", Cyber: "daybreak_blue"},
	"gpt-6.1-sol":              {Model: "gpt-6.1-sol", RequiredTier: "red", Cyber: "daybreak_blue"},
	"gpt-5.5-cyber":            {Model: "gpt-5.5-cyber", RequiredTier: "red", Cyber: "daybreak_red"},
	"gpt-5.6-cyber":            {Model: "gpt-5.6-cyber", RequiredTier: "red", Cyber: "daybreak_red"},
	"gpt-daybreak-red-latest":  {Model: "gpt-daybreak-red-latest", RequiredTier: "red", Cyber: "daybreak_red"},
}

func IsOpenAIDaybreakAccount(account *Account) bool {
	return account != nil && ((account.IsOpenAIOAuth() &&
		!account.IsOpenAIPersonalAccessToken() && !account.IsOpenAIAgentIdentity()) ||
		(account.IsOpenAIApiKey() && !account.IsCodexEngine()))
}

// OpenAIDaybreakAPIKeyCredentialIdentity binds capability evidence to the real
// upstream credential and endpoint. It contains no plaintext credential and is
// used only internally for cache isolation and the locked settings save.
func OpenAIDaybreakAPIKeyCredentialIdentity(account *Account) string {
	if account == nil || !account.IsOpenAIApiKey() || account.IsCodexEngine() {
		return ""
	}
	mode, _ := account.Extra[OpenAIAPIKeyModeExtraKey].(string)
	if mode == "" {
		mode = "generic"
	}
	var overrides map[string]string
	if account.IsHeaderOverrideEnabled() {
		// Do not mutate the account's header-override cache: namespace lookup also
		// runs concurrently with forwarding for this shared account snapshot.
		overrides = resolveHeaderOverrides(stringMappingFromRaw(account.Credentials[credKeyHeaderOverrides]))
	}
	identity, _ := json.Marshal(struct {
		ID       int64             `json:"id"`
		ParentID *int64            `json:"parent_id"`
		BaseURL  string            `json:"base_url"`
		APIKey   string            `json:"api_key"`
		Mode     string            `json:"mode"`
		Headers  map[string]string `json:"headers"`
	}{account.ID, account.ParentAccountID, account.GetOpenAIBaseURL(), account.GetOpenAIApiKey(), mode, overrides})
	return fmt.Sprintf("%x", sha256.Sum256(identity))
}

func openAIDaybreakCapabilitiesNamespace(account *Account) string {
	if identity := OpenAIDaybreakAPIKeyCredentialIdentity(account); identity != "" {
		return openAIOutboundSessionIdentityNamespace(account) + "/daybreak/apikey/" + identity
	}
	return openAICodexModelCapabilitiesNamespace(account)
}

// Only the actual catalog supplies access programs. API-key catalogs may use
// the OpenAI data/id envelope; adapt that shape without adding static metadata,
// mapped aliases, or any locally completed capabilities.
func (s *OpenAIGatewayService) observeOpenAIDaybreakAPIKeyCatalog(account *Account, raw []byte, observedAt time.Time) {
	if s == nil || account == nil || !account.IsOpenAIApiKey() || !IsOpenAIDaybreakAccount(account) {
		return
	}
	var envelope struct {
		Models json.RawMessage `json:"models"`
		Data   []struct {
			ID                      string          `json:"id"`
			AvailableAccessPrograms json.RawMessage `json:"available_access_programs,omitempty"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return
	}
	if envelope.Models == nil {
		if envelope.Data == nil {
			return
		}
		type model struct {
			Slug                    string          `json:"slug"`
			AvailableAccessPrograms json.RawMessage `json:"available_access_programs,omitempty"`
		}
		models := make([]model, 0, len(envelope.Data))
		for _, item := range envelope.Data {
			models = append(models, model{item.ID, item.AvailableAccessPrograms})
		}
		raw, _ = json.Marshal(struct {
			Models []model `json:"models"`
		}{models})
	}
	s.codexModelCapabilities.observeManifest(openAIDaybreakCapabilitiesNamespace(account), raw, observedAt)
}

func openAIDaybreakEnabled(account *Account, key string) bool {
	if !IsOpenAIDaybreakAccount(account) {
		return false
	}
	enabled, _ := account.Extra[key].(bool)
	return enabled
}

func parseOpenAIDaybreakAccessPrograms(raw json.RawMessage) (known, blue, red bool) {
	var programs struct {
		Cyber []string `json:"cyber"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &programs) != nil || programs.Cyber == nil {
		return false, false, false
	}
	for _, program := range programs.Cyber {
		switch program {
		case "daybreak_blue":
			blue = true
		case "daybreak_red":
			red = true
		}
	}
	return true, blue, red
}

func openAIDaybreakModelSupported(model OpenAIDaybreakModel, capabilities CodexModelCapabilities) bool {
	if !capabilities.Known || !capabilities.AccessProgramsKnown {
		return false
	}
	return model.Cyber == "daybreak_blue" && capabilities.DaybreakBlue ||
		model.Cyber == "daybreak_red" && capabilities.DaybreakRed
}

// Reuse the diagnostic token refresh contract only while acquiring credentials:
// it keeps refresh CAS but does not disable accounts on a refresh failure. The
// catalog itself still uses the normal cache, singleflight and observations.
func openAIModelsTokenContext(ctx context.Context, auxiliary bool) context.Context {
	if auxiliary {
		return withOpenAICandyTest(withoutDiagnosticDaybreakAttempt(ctx), &openAICandyTestAttempt{})
	}
	return ctx
}

// fetchOpenAIDaybreakCatalog freezes the actual credential source. The caller's
// business account and its switches are never replaced by the owner's extra.
func (s *OpenAIGatewayService) fetchOpenAIDaybreakCatalog(ctx context.Context, account *Account) (*Account, *OpenAIModelsResponse, error) {
	if s == nil || !IsOpenAIDaybreakAccount(account) {
		return nil, nil, infraerrors.New(http.StatusBadRequest, "OPENAI_DAYBREAK_ACCOUNT_UNSUPPORTED", "Daybreak settings require an OpenAI OAuth or ordinary API Key account")
	}
	ctx, cancel := context.WithTimeout(withoutDiagnosticDaybreakAttempt(ctx), codexModelsManifestRequestTimeout)
	defer cancel()
	credential, err := resolveCredentialAccount(ctx, s.accountRepo, account)
	if err != nil {
		return nil, nil, openAIModelsCredentialError(err)
	}
	if !IsOpenAIDaybreakAccount(credential) {
		return nil, nil, infraerrors.New(http.StatusBadRequest, "OPENAI_DAYBREAK_ACCOUNT_UNSUPPORTED", "The credential source must use OpenAI OAuth or an ordinary API Key")
	}
	if credential.IsOpenAIOAuth() {
		credential, err = ResolveOpenAIOAuthCredentialAccount(ctx, s.accountRepo, credential, credential.OpenAIOAuthCredentialOS)
		if err != nil {
			return nil, nil, openAIModelsCredentialError(err)
		}
	}
	// Keep the selected account's proxy and business settings, while binding the
	// model request to the same authorization as this inference attempt.
	scoped := *account
	scoped.OpenAIOAuthCredentialOS = credential.OpenAIOAuthCredentialOS
	scoped.OpenAIOAuthCredentialOwnerID = credential.OpenAIOAuthCredentialOwnerID
	scoped.OpenAIOAuthAuthorizationGeneration = credential.OpenAIOAuthAuthorizationGeneration
	manifest, err := s.fetchCodexModelsManifest(ctx, &scoped, "", "", true)
	if err == nil && manifest != nil && !manifest.observedAt.IsZero() && time.Now().Before(manifest.observedAt.Add(codexModelCapabilityCacheTTL)) {
		// The smaller capability cache may have evicted an otherwise fresh catalog.
		// Restore from the actual observation time, never extend it on cache reads.
		if credential.IsOpenAIApiKey() {
			s.observeOpenAIDaybreakAPIKeyCatalog(credential, manifest.upstreamSourceBody, manifest.observedAt)
		} else {
			s.codexModelCapabilities.observeManifest(openAIDaybreakCapabilitiesNamespace(credential), manifest.Body, manifest.observedAt)
		}
	}
	return credential, manifest, err
}

func (s *OpenAIGatewayService) GetOpenAIDaybreakCapabilities(ctx context.Context, account *Account) (*OpenAIDaybreakCapabilities, error) {
	credential, manifest, err := s.fetchOpenAIDaybreakCatalog(ctx, account)
	if err != nil {
		return nil, err
	}
	result := &OpenAIDaybreakCapabilities{
		Models:                  make([]OpenAIDaybreakModel, 0),
		CredentialOwnerID:       credential.OpenAIOAuthCredentialOwnerID,
		CredentialOS:            credential.OpenAIOAuthCredentialOS,
		AuthorizationGeneration: credential.OpenAIOAuthAuthorizationGeneration,
		CredentialIdentity:      OpenAIDaybreakAPIKeyCredentialIdentity(credential),
	}
	if result.CredentialOwnerID == 0 {
		result.CredentialOwnerID = credential.ID
	}
	if manifest != nil && !manifest.observedAt.IsZero() {
		checked := manifest.observedAt
		result.CheckedAt = &checked
	}
	namespace := openAIDaybreakCapabilitiesNamespace(credential)
	for _, model := range openAIDaybreakModels {
		capabilities := s.openAICodexModelCapabilities(namespace, model.Model)
		if !openAIDaybreakModelSupported(model, capabilities) {
			continue
		}
		result.Models = append(result.Models, model)
		result.BlueAvailable = true // Blue is the prerequisite for either tier.
		result.RedAvailable = result.RedAvailable || model.RequiredTier == "red"
	}
	sort.Slice(result.Models, func(i, j int) bool { return result.Models[i].Model < result.Models[j].Model })
	if !result.BlueAvailable {
		result.Reason = "未从当前上游凭据的 available_access_programs 中获取到支持的 Daybreak 模型"
	} else if !result.RedAvailable {
		result.Reason = "当前上游凭据未声明支持 Red 档模型"
	}
	return result, nil
}

// Only absent fields can be filled. Invalid/explicit null objects and values
// remain the client's responsibility and must reach upstream without repair.
func openAIDaybreakInputSkipReason(body []byte) string {
	if !gjson.ValidBytes(body) {
		return "invalid_request"
	}
	root := gjson.ParseBytes(body)
	if !root.IsObject() {
		return "invalid_request"
	}
	if frameType := root.Get("type"); frameType.Exists() && frameType.String() != "response.create" {
		return "non_inference"
	}
	if generate := root.Get("generate"); generate.Type == gjson.False {
		return "prewarm"
	}
	programs := root.Get("access_programs")
	if programs.Exists() && !programs.IsObject() {
		return "invalid_access_programs"
	}
	if programs.Get("cyber").Exists() {
		return "client_supplied"
	}
	return ""
}

// applyOpenAIDaybreak runs only at the physical inference send boundary. It
// never writes the source body used for another account attempt or prewarming.
func (s *OpenAIGatewayService) applyOpenAIDaybreak(ctx context.Context, account *Account, body []byte) []byte {
	patched, _, _ := s.applyOpenAIDaybreakWithDecision(ctx, account, body)
	return patched
}

// The decision travels with this physical request/frame, never in shared turn
// state. Observers still read the value from the final outbound body.
func (s *OpenAIGatewayService) applyOpenAIDaybreakWithDecision(ctx context.Context, account *Account, body []byte) ([]byte, string, error) {
	return s.applyOpenAIDaybreakWithContext(ctx, nil, account, body)
}

func (s *OpenAIGatewayService) applyOpenAIDaybreakWithContext(ctx context.Context, c *gin.Context, account *Account, body []byte) ([]byte, string, error) {
	if account == nil || account.Platform != PlatformOpenAI {
		return body, "not_oauth", nil
	}
	var settings *SettingService
	if s != nil {
		settings = s.settingService
	}
	preferences, diagnostic := diagnosticDaybreakInferencePreferences(ctx, account)
	diagnostic = diagnostic && diagnosticDaybreakInferenceBody(body)
	if !diagnostic && !openAIDaybreakPolicyEnabled(ctx, settings) {
		patched, changed, err := stripOpenAIRequestCyber(body)
		if changed {
			return patched, "global_disabled_stripped", err
		}
		return patched, "global_disabled", err
	}
	if ctx != nil {
		if disabled, _ := ctx.Value(openAIDaybreakInjectionDisabledKey{}).(bool); disabled {
			return body, "excluded_endpoint", nil
		}
	}
	if reason := openAIDaybreakInputSkipReason(body); reason != "" {
		return body, reason, nil
	}
	if !IsOpenAIDaybreakAccount(account) {
		return body, "not_oauth", nil
	}
	if !diagnostic {
		preferences = ReadDiagnosticDaybreakPreferences(account)
	}
	if !preferences.Blue {
		return body, "blue_disabled", nil
	}
	if s == nil {
		return body, "catalog_unavailable", nil
	}
	model, recognized := openAIDaybreakModels[strings.TrimSpace(gjson.GetBytes(body, "model").String())]
	if !recognized {
		return body, "unrecognized_model", nil
	}
	if model.RequiredTier == "red" && !preferences.Red {
		return body, "red_disabled", nil
	}
	if !diagnostic {
		group := s.openAIDaybreakGroup(ctx, c)
		if group == nil {
			return body, "group_unavailable", nil
		}
		if !group.OpenAIDaybreakBlueEnabled {
			return body, "group_blue_disabled", nil
		}
		if model.RequiredTier == "red" && !group.OpenAIDaybreakRedEnabled {
			return body, "group_red_disabled", nil
		}
	}
	capabilities := s.openAICodexModelCapabilities(openAIDaybreakCapabilitiesNamespace(account), model.Model)
	if !capabilities.Known {
		credential, _, err := s.fetchOpenAIDaybreakCatalog(ctx, account)
		if err != nil {
			return body, "catalog_unavailable", nil
		}
		capabilities = s.openAICodexModelCapabilities(openAIDaybreakCapabilitiesNamespace(credential), model.Model)
	}
	if !openAIDaybreakModelSupported(model, capabilities) {
		return body, "capability_unavailable", nil
	}
	patched, err := sjson.SetBytes(body, "access_programs.cyber", model.Cyber)
	if err != nil {
		return body, "patch_failed", nil
	}
	return patched, "automatic", nil
}
