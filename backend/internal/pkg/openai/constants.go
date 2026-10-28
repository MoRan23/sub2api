// Package openai provides helpers and types for OpenAI API integration.
package openai

import (
	_ "embed"
	"fmt"
	"strings"
	"time"
)

// Model represents an OpenAI model
type Model struct {
	ID          string `json:"id"`
	Object      string `json:"object"`
	Created     int64  `json:"created"`
	OwnedBy     string `json:"owned_by"`
	Type        string `json:"type"`
	DisplayName string `json:"display_name"`
}

// DefaultModels OpenAI models list
var DefaultModels = []Model{
	{ID: "gpt-5.6-sol", Object: "model", Created: 1780876800, OwnedBy: "openai", Type: "model", DisplayName: "GPT-5.6 Sol"},
	{ID: "gpt-6", Object: "model", Created: 1788480000, OwnedBy: "openai", Type: "model", DisplayName: "GPT-6 (Astra)"},
	{ID: "gpt-5.6", Object: "model", Created: 1780876800, OwnedBy: "openai", Type: "model", DisplayName: "GPT-5.6 (Sol)"},
	{ID: "gpt-5.6-terra", Object: "model", Created: 1780876800, OwnedBy: "openai", Type: "model", DisplayName: "GPT-5.6 Terra"},
	{ID: "gpt-5.6-luna", Object: "model", Created: 1780876800, OwnedBy: "openai", Type: "model", DisplayName: "GPT-5.6 Luna"},
	{ID: "gpt-6-astra", Object: "model", Created: 1788480000, OwnedBy: "openai", Type: "model", DisplayName: "GPT-6 Astra"},
	// The official Codex catalog does not provide a creation timestamp.
	{ID: "gpt-6.1-sol", Object: "model", Created: 0, OwnedBy: "openai", Type: "model", DisplayName: "GPT-6.1 Sol"},
	{ID: "gpt-6.1-terra", Object: "model", Created: 0, OwnedBy: "openai", Type: "model", DisplayName: "GPT-6.1 Terra"},
	{ID: "gpt-6-sol", Object: "model", Created: 1790121600, OwnedBy: "openai", Type: "model", DisplayName: "GPT-6 Sol"},
	{ID: "gpt-6-luna", Object: "model", Created: 1790121600, OwnedBy: "openai", Type: "model", DisplayName: "GPT-6 Luna"},
	{ID: "gpt-5.5", Object: "model", Created: 1776873600, OwnedBy: "openai", Type: "model", DisplayName: "GPT-5.5"},
	{ID: "gpt-5.3-codex-spark", Object: "model", Created: 1735689600, OwnedBy: "openai", Type: "model", DisplayName: "GPT-5.3 Codex Spark"},
	{ID: "codex-auto-review", Object: "model", Created: 1776902400, OwnedBy: "openai", Type: "model", DisplayName: "Codex Auto Review"},
	{ID: "gpt-5.2", Object: "model", Created: 1733875200, OwnedBy: "openai", Type: "model", DisplayName: "GPT-5.2"},
	{ID: "gpt-image-1", Object: "model", Created: 1733875200, OwnedBy: "openai", Type: "model", DisplayName: "GPT Image 1"},
	{ID: "gpt-image-1.5", Object: "model", Created: 1735689600, OwnedBy: "openai", Type: "model", DisplayName: "GPT Image 1.5"},
	{ID: "gpt-image-2", Object: "model", Created: 1738368000, OwnedBy: "openai", Type: "model", DisplayName: "GPT Image 2"},
	{ID: "gpt-image-2.5-flare", Object: "model", Created: 1788825600, OwnedBy: "openai", Type: "model", DisplayName: "GPT Image 2.5 Flare"},
	{ID: "gpt-image-2.5-sunburst", Object: "model", Created: 1788825600, OwnedBy: "openai", Type: "model", DisplayName: "GPT Image 2.5 Sunburst"},
}

// DefaultModelIDs returns the default model ID list
func DefaultModelIDs() []string {
	ids := make([]string, len(DefaultModels))
	for i, m := range DefaultModels {
		ids[i] = m.ID
	}
	return ids
}

// DefaultTestModel default model for testing OpenAI accounts
const DefaultTestModel = "gpt-5.6-sol"

// CodexUsageProbeModel is the model used for OAuth Codex usage probes.
const CodexUsageProbeModel = "codex-auto-review"

// DefaultInstructions is the retained GPT-5-Codex template for legacy Codex
// model catalog entries, account probes, and requests without client instructions.
//
//go:embed instructions.txt
var DefaultInstructions string

// instructionsGPT51 / instructionsGPT52 are retained compatibility templates:
// these older models are absent from the current official models manifest.
//
//go:embed instructions_gpt5_1.txt
var instructionsGPT51 string

//go:embed instructions_gpt5_2.txt
var instructionsGPT52 string

// Current templates below are copied from model_messages.instructions_template
// in openai/codex codex-rs/models-manager/models.json at
// e742db9f7ba98e63e502b310857d49d047888518 (Codex 0.165.0). The three GPT-5.6
// variants and codex-auto-review share one template. GPT-6.1 Terra explicitly
// shares Astra's identical main template; its other policies remain independent.
// Synthetic catalogs, account probes, and Responses requests without client
// instructions use the same model-specific templates.
//
//go:embed instructions_gpt5_5.txt
var instructionsGPT55 string

//go:embed instructions_gpt5_6.txt
var instructionsGPT56 string

//go:embed instructions_gpt6_astra.txt
var instructionsGPT6Astra string

//go:embed instructions_gpt6_1_sol.txt
var instructionsGPT61Sol string

//go:embed instructions_gpt6_sol.txt
var instructionsGPT6Sol string

//go:embed instructions_gpt6_luna.txt
var instructionsGPT6Luna string

//go:embed instructions_daybreak_blue.txt
var instructionsDaybreakBlue string

//go:embed instructions_daybreak_red.txt
var instructionsDaybreakRed string

// CodexGPT61SolMetadata is the complete official descriptor from openai/codex
// e742db9f7ba98e63e502b310857d49d047888518, codex-rs/models-manager/models.json.
//
//go:embed codex_gpt61_sol.json
var CodexGPT61SolMetadata []byte

// latestCodexInstructions retains the existing GPT-5.5 fallback for models
// without a known template; it is not evidence of an upstream model mapping.
func latestCodexInstructions() string {
	if v := strings.TrimSpace(instructionsGPT55); v != "" {
		return instructionsGPT55
	}
	return DefaultInstructions
}

// CanonicalizeOpenAIModelAliasSpelling normalizes provider prefixes, case,
// separators, and known compact spellings used by OpenAI model aliases.
func CanonicalizeOpenAIModelAliasSpelling(model string) string {
	model = strings.TrimSpace(model)
	if slash := strings.LastIndexByte(model, '/'); slash >= 0 {
		model = strings.TrimSpace(model[slash+1:])
	}
	model = strings.ToLower(model)
	if model == "" {
		return ""
	}

	normalized := strings.ReplaceAll(model, "_", "-")
	normalized = strings.Join(strings.Fields(normalized), "-")
	for strings.Contains(normalized, "--") {
		normalized = strings.ReplaceAll(normalized, "--", "-")
	}

	if strings.HasPrefix(normalized, "gpt5") {
		normalized = "gpt-5" + strings.TrimPrefix(normalized, "gpt5")
	}
	if !strings.HasPrefix(normalized, "gpt-") && !strings.Contains(normalized, "codex") {
		return ""
	}

	replacements := []struct {
		from string
		to   string
	}{
		{"gpt-5.4mini", "gpt-5.4-mini"},
		{"gpt-5.4nano", "gpt-5.4-nano"},
		{"gpt-5.3-codexspark", "gpt-5.3-codex-spark"},
		{"gpt-5.3codexspark", "gpt-5.3-codex-spark"},
		{"gpt-5.3codex", "gpt-5.3-codex"},
	}
	for _, replacement := range replacements {
		normalized = strings.ReplaceAll(normalized, replacement.from, replacement.to)
	}
	return normalized
}

// CodexBaseInstructionsForModel selects the official template for known models
// and established aliases. Older models and unknown names keep their existing
// compatibility fallback; no template is inferred from another new model.
func CodexBaseInstructionsForModel(model string) string {
	canonical := CanonicalizeOpenAIModelAliasSpelling(model)
	switch {
	case IsKnownCodexModelVariant(canonical, "gpt-6") || IsKnownCodexModelVariant(canonical, "gpt-6-astra"):
		if v := strings.TrimSpace(instructionsGPT6Astra); v != "" {
			return instructionsGPT6Astra
		}
	case IsGPT61TerraModelSpelling(canonical):
		// The official Terra and Astra main templates are byte-for-byte identical.
		return instructionsGPT6Astra
	case strings.HasPrefix(canonical, "gpt-6.1-terra"):
		return latestCodexInstructions()
	case IsKnownCodexModelVariant(canonical, "gpt-5.6") || IsKnownCodexModelVariant(canonical, "gpt-5.6-sol") || IsKnownCodexModelVariant(canonical, "gpt-5.6-terra") || IsKnownCodexModelVariant(canonical, "gpt-5.6-luna"):
		return instructionsGPT56
	case IsKnownCodexModelVariant(canonical, "gpt-6-sol"):
		return instructionsGPT6Sol
	case IsKnownCodexModelVariant(canonical, "gpt-6.1-sol"):
		return instructionsGPT61Sol
	case IsKnownCodexModelVariant(canonical, "gpt-6-luna"):
		return instructionsGPT6Luna
	case canonical == "codex-auto-review":
		return instructionsGPT56
	case canonical == "gpt-daybreak-blue-latest":
		return instructionsDaybreakBlue
	case canonical == "gpt-daybreak-red-latest":
		return instructionsDaybreakRed
	case strings.Contains(canonical, "codex"):
		return DefaultInstructions
	case strings.HasPrefix(canonical, "gpt-5.5"):
		return latestCodexInstructions()
	case strings.HasPrefix(canonical, "gpt-5.2"):
		if v := strings.TrimSpace(instructionsGPT52); v != "" {
			return instructionsGPT52
		}
	case strings.HasPrefix(canonical, "gpt-5.1"):
		if v := strings.TrimSpace(instructionsGPT51); v != "" {
			return instructionsGPT51
		}
	}
	return latestCodexInstructions()
}

// IsKnownCodexModelVariant recognizes only established effort, snapshot and
// compact variants. A model with an arbitrary suffix must not inherit another
// model's request normalization, capabilities or instruction template.
func IsKnownCodexModelVariant(model, family string) bool {
	canonical := CanonicalizeOpenAIModelAliasSpelling(model)
	canonical = strings.TrimSuffix(canonical, "-openai-compact")
	if canonical == family {
		return true
	}
	suffix, ok := strings.CutPrefix(canonical, family+"-")
	if !ok {
		return false
	}
	switch suffix {
	case "none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra":
		return true
	}
	_, err := time.Parse("2006-01-02", suffix)
	return err == nil
}

// IsGPT6SolOrLunaModelSpelling shares the same strict alias recognition as the
// model catalog and reasoning compatibility paths.
func IsGPT6SolOrLunaModelSpelling(model string) bool {
	return IsKnownCodexModelVariant(model, "gpt-6-sol") ||
		IsKnownCodexModelVariant(model, "gpt-6-luna")
}

// IsGPT61SolModelSpelling recognizes the published model and local effort/compact
// spellings. Invalid effort suffixes remain identifiable for request validation.
func IsGPT61SolModelSpelling(model string) bool {
	return IsKnownCodexModelVariant(model, "gpt-6.1-sol")
}

// IsGPT61TerraModelSpelling recognizes only established Terra variants, without
// granting Astra's pricing or other family-specific behavior.
func IsGPT61TerraModelSpelling(model string) bool {
	return IsKnownCodexModelVariant(model, "gpt-6.1-terra")
}

func RequiresResponsesForToolCalls(model string) bool {
	return IsGPT61SolModelSpelling(model) || IsGPT61TerraModelSpelling(model)
}

// ValidateGPT61SolReasoningEffort rejects disabled reasoning instead of silently
// increasing the client's requested effort on compatibility paths.
func ValidateGPT61SolReasoningEffort(model, effort string) error {
	if IsGPT61SolModelSpelling(model) {
		switch strings.ToLower(strings.TrimSpace(effort)) {
		case "none", "minimal":
			return fmt.Errorf("gpt-6.1-sol does not support reasoning effort %q; use low, medium, high, xhigh or max", effort)
		}
	}
	return nil
}
