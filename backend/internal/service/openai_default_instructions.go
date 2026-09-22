package service

import (
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// defaultCodexSynthInstructions uses the same pinned official template as the
// synthetic model catalog and account tests, selected by the final upstream model.
func defaultCodexSynthInstructions(model string) string {
	return strings.TrimSpace(openai.CodexBaseInstructionsForModel(model))
}

// shouldApplyDefaultCodexInstructions treats only absent/null/blank instructions
// and absent prompt-bearing system/developer messages as a missing prompt. An
// invalid non-string instruction is kept for upstream validation, not overwritten.
func shouldApplyDefaultCodexInstructions(body map[string]any) bool {
	if body == nil {
		return false
	}
	if value, exists := body["instructions"]; exists && value != nil {
		text, ok := value.(string)
		if !ok || strings.TrimSpace(text) != "" {
			return false
		}
	}
	input, _ := body["input"].([]any)
	for _, raw := range input {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		role, _ := item["role"].(string)
		if role != "system" && role != "developer" {
			continue
		}
		if codexPromptContentPresent(item["content"]) {
			return false
		}
	}
	return true
}

func codexPromptContentPresent(value any) bool {
	switch content := value.(type) {
	case nil:
		return false
	case string:
		return strings.TrimSpace(content) != ""
	case []any:
		for _, raw := range content {
			part, ok := raw.(map[string]any)
			if !ok {
				return true
			}
			text, ok := part["text"].(string)
			if !ok || strings.TrimSpace(text) != "" {
				return true
			}
		}
		return false
	default:
		// Unknown content is caller-owned, so do not silently supplement it.
		return true
	}
}

func applyDefaultCodexInstructions(body map[string]any, model string) bool {
	if !shouldApplyDefaultCodexInstructions(body) {
		return false
	}
	instructions := defaultCodexSynthInstructions(model)
	if instructions == "" {
		return false
	}
	body["instructions"] = instructions
	return true
}

func applyDefaultCodexInstructionsBody(body []byte, model string) ([]byte, bool, error) {
	if len(body) == 0 {
		return body, false, nil
	}
	value := gjson.GetBytes(body, "instructions")
	if value.Exists() && value.Type != gjson.Null && (value.Type != gjson.String || strings.TrimSpace(value.String()) != "") {
		return body, false, nil
	}
	var decoded map[string]any
	if err := decodeOpenAIJSONUseNumber(body, &decoded); err != nil {
		return body, false, err
	}
	if strings.TrimSpace(model) == "" {
		model, _ = decoded["model"].(string)
	}
	if !applyDefaultCodexInstructions(decoded, model) {
		return body, false, nil
	}
	updated, err := sjson.SetBytes(body, "instructions", decoded["instructions"])
	if err != nil {
		return body, false, err
	}
	return updated, true, nil
}

func applyDefaultCodexInstructionsWSBody(body []byte) ([]byte, error) {
	frameType := strings.TrimSpace(gjson.GetBytes(body, "type").String())
	if (frameType != "" && frameType != "response.create") || gjson.GetBytes(body, "generate").Type == gjson.False {
		return body, nil
	}
	updated, _, err := applyDefaultCodexInstructionsBody(body, "")
	return updated, err
}
