package service

import (
	_ "embed"
	"encoding/json"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
)

// Bundled metadata from openai/codex at 24462234b2aeeb27373e17bbe226baf9c0e97d3b
// (2026-09-23), codex-rs/models-manager/models.json. Only the main
// model_messages.instructions_template is omitted: pkg/openai embeds those
// separately for both catalog generation and requests without instructions.
//
//go:embed openai_codex_model_defaults.json
var bundledCodexModelDefaultsJSON []byte

var bundledCodexModelDefaults = loadBundledCodexModelDefaults()

func loadBundledCodexModelDefaults() map[string]json.RawMessage {
	var catalog struct {
		Models []json.RawMessage `json:"models"`
	}
	if err := json.Unmarshal(bundledCodexModelDefaultsJSON, &catalog); err != nil {
		panic("invalid bundled Codex model defaults")
	}
	models := make(map[string]json.RawMessage, len(catalog.Models))
	for _, raw := range catalog.Models {
		var model struct {
			Slug string `json:"slug"`
		}
		if err := json.Unmarshal(raw, &model); err != nil || model.Slug == "" {
			panic("invalid bundled Codex model entry")
		}
		models[model.Slug] = raw
	}
	return models
}

func bundledCodexModelDefault(modelID string) json.RawMessage {
	canonical := canonicalizeOpenAIModelAliasSpelling(modelID)
	if raw := bundledCodexModelDefaults[canonical]; raw != nil {
		return raw
	}
	// Only established aliases qualify; unknown model names must retain the
	// existing conservative fallback instead of inheriting unrelated metadata.
	if raw := bundledCodexModelDefaults[getNormalizedCodexModel(canonical)]; raw != nil {
		return raw
	}
	switch {
	case openai.IsKnownCodexModelVariant(canonical, "gpt-6"):
		return bundledCodexModelDefaults["gpt-6-astra"]
	case openai.IsKnownCodexModelVariant(canonical, "gpt-5.6"):
		return bundledCodexModelDefaults["gpt-5.6-sol"]
	}
	for _, family := range []string{"gpt-6-astra", "gpt-6-sol", "gpt-6-luna", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-5.5", "gpt-5.4"} {
		if openai.IsKnownCodexModelVariant(canonical, family) {
			return bundledCodexModelDefaults[family]
		}
	}
	return nil
}

func applyBundledCodexModelDefaults(descriptor *configuredCodexModelDescriptor, modelID string) {
	raw := bundledCodexModelDefault(modelID)
	if raw == nil {
		return
	}
	if err := json.Unmarshal(raw, descriptor); err != nil {
		panic("invalid bundled Codex model descriptor")
	}
	descriptor.ModelMessages.InstructionsTemplate = openai.CodexBaseInstructionsForModel(descriptor.Slug)
	descriptor.Slug = modelID
	// Visibility belongs to local account/group policy. Official availability,
	// retirement and upgrade notices cannot grant or remove a configured route.
	descriptor.Visibility = "list"
	descriptor.Upgrade = nil
	descriptor.AvailabilityNUX = nil
	// Transport and input/tool capabilities require evidence from the selected
	// account(s). A bundled ChatGPT catalog alone cannot enable Lite or image
	// input for an unknown third-party route. Existing route projection fills them.
	descriptor.UseResponsesLite = false
	descriptor.ToolMode = nil
	descriptor.SupportsSearchTool = false
	descriptor.InputModalities = []string{"text"}
}
