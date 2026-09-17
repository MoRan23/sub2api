package service

import (
	"reflect"
	"strings"
)

// These are deliberately small, lossless equivalences, not another run of the
// forwarding transformer. Unknown items, attributes and multimodal content are
// left comparable; recovery that deletes content never belongs in this list.
func canonicalizeRequestIntegrityCodex(body map[string]any, rules map[string]bool) {
	if model, ok := body["model"].(string); ok && model != strings.TrimSpace(model) {
		body["model"] = strings.TrimSpace(model)
		rules["model_whitespace"] = true
	}
	if reasoning, ok := body["reasoning"].(map[string]any); ok {
		if reasoning["effort"] == "minimal" {
			reasoning["effort"] = "none"
			rules["reasoning_effort_alias"] = true
		}
		model, _ := body["model"].(string)
		mode, _ := reasoning["mode"].(string)
		effort, _ := reasoning["effort"].(string)
		if !isOpenAIGPT6AstraModel(model) && strings.EqualFold(strings.TrimSpace(mode), "pro") && strings.TrimSpace(effort) == "" {
			reasoning["effort"] = "max"
			delete(reasoning, "mode")
			rules["reasoning_mode_alias"] = true
		}
	}
	if text, ok := body["input"].(string); ok {
		body["input"] = []any{map[string]any{"type": "message", "role": "user", "content": text}}
		rules["input_message_shape"] = true
	}
	canonicalizeRequestIntegritySystem(body, rules)
	if instructions, ok := body["instructions"].(string); body["instructions"] == nil || (ok && strings.TrimSpace(instructions) == "") {
		if _, present := body["instructions"]; present {
			rules["empty_instructions"] = true
		}
		delete(body, "instructions")
	}
	if _, hasTools := body["tools"]; !hasTools {
		if functions, ok := body["functions"].([]any); ok {
			tools := make([]any, 0, len(functions))
			for _, function := range functions {
				tools = append(tools, map[string]any{"type": "function", "function": function})
			}
			body["tools"] = tools
			delete(body, "functions")
			rules["legacy_function_tools"] = true
		}
	}
	if _, hasChoice := body["tool_choice"]; !hasChoice {
		if choice, exists := body["function_call"]; exists {
			switch value := choice.(type) {
			case string:
				body["tool_choice"] = value
				delete(body, "function_call")
				rules["legacy_function_choice"] = true
			case map[string]any:
				if name, ok := value["name"].(string); ok && name != "" && len(value) == 1 {
					body["tool_choice"] = map[string]any{"type": "function", "name": name}
					delete(body, "function_call")
					rules["legacy_function_choice"] = true
				}
			}
		}
	}
	if tools, ok := body["tools"].([]any); ok {
		for _, raw := range tools {
			tool, ok := raw.(map[string]any)
			if ok && tool["type"] == "function" && flattenRequestIntegrityFunction(tool) {
				rules["function_tool_shape"] = true
			}
		}
	}
	if choice, ok := body["tool_choice"].(map[string]any); ok && choice["type"] == "function" {
		if flattenRequestIntegrityFunction(choice) {
			rules["function_choice_shape"] = true
		}
	}
	// This helper only applies a bijective name mapping, with collision checks.
	// It never removes declarations, calls or their parameter schema.
	if _, changed, err := aliasOpenAIOAuthReservedToolNames(body); err == nil && changed {
		rules["reserved_tool_alias"] = true
	}
	input, ok := body["input"].([]any)
	if !ok {
		return
	}
	referenceIDs := codexItemReferenceIDMappings(input, false)
	itemIDs := codexInputItemIDs(input)
	callIDs := codexInputCallIDs(input)
	for index, raw := range input {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if item["role"] == "tool" && requestIntegrityOnlyKeys(item, "type", "role", "content", "tool_call_id", "call_id", "id") {
			callID := strings.TrimSpace(firstNonEmptyString(item["call_id"], item["tool_call_id"], item["id"]))
			if output, lossless := requestIntegrityLosslessText(item["content"]); lossless && callID != "" {
				item = map[string]any{"type": "function_call_output", "call_id": callID, "output": output}
				input[index] = item
				rules["tool_result_shape"] = true
			}
		}
		typ, _ := item["type"].(string)
		if typ == "" && (item["role"] == "user" || item["role"] == "assistant" || item["role"] == "developer") {
			typ, item["type"] = "message", "message"
			rules["input_message_shape"] = true
		}
		switch typ {
		case "message":
			if _, hasID := item["id"]; hasID {
				delete(item, "id")
				rules["replay_item_identity"] = true
			}
			if text, ok := item["content"].(string); ok {
				item["content"] = []any{map[string]any{"type": "input_text", "text": text}}
				rules["message_content_shape"] = true
			}
			if parts, ok := item["content"].([]any); ok {
				for _, rawPart := range parts {
					if part, ok := rawPart.(map[string]any); ok && (part["type"] == "text" || part["type"] == "output_text") {
						part["type"] = "input_text"
						rules["message_content_shape"] = true
					}
				}
			}
		case "reasoning":
			for _, key := range []string{"id", "call_id"} {
				if _, present := item[key]; present {
					delete(item, key)
					rules["replay_item_identity"] = true
				}
			}
			if item["summary"] == nil {
				item["summary"] = []any{}
				rules["reasoning_empty_summary"] = true
			}
		case "compaction_summary":
			if _, present := item["id"]; present {
				delete(item, "id")
				rules["replay_item_identity"] = true
			}
		case "item_reference":
			id, _ := item["id"].(string)
			id = strings.TrimSpace(id)
			if _, exists := itemIDs[id]; !exists && strings.HasPrefix(id, "call_") {
				if mapped, exists := referenceIDs[id]; exists {
					item["id"] = mapped
					rules["tool_call_identity"] = true
				} else if _, sameTurn := callIDs[id]; !sameTurn {
					item["id"] = normalizeCodexCallID(id)
					rules["tool_call_identity"] = true
				}
			}
		}
		if isCodexToolCallItemType(typ) {
			if id := firstNonEmptyString(item["call_id"], item["id"]); id != "" {
				normalized := normalizeCodexCallIDForItemType(typ, id)
				if item["call_id"] != normalized {
					item["call_id"] = normalized
					rules["tool_call_identity"] = true
				}
			}
			if _, present := item["id"]; present {
				delete(item, "id")
				rules["tool_call_identity"] = true
			}
		}
	}
}

func flattenRequestIntegrityFunction(tool map[string]any) bool {
	function, ok := tool["function"].(map[string]any)
	if !ok {
		return false
	}
	for key, value := range function {
		if existing, present := tool[key]; present && !reflect.DeepEqual(existing, value) {
			return false
		}
	}
	for key, value := range function {
		tool[key] = value
	}
	delete(tool, "function")
	return true
}

func requestIntegrityOnlyKeys(object map[string]any, keys ...string) bool {
	for key := range object {
		allowed := false
		for _, candidate := range keys {
			if key == candidate {
				allowed = true
				break
			}
		}
		if !allowed {
			return false
		}
	}
	return true
}

func requestIntegrityLosslessText(raw any) (string, bool) {
	if text, ok := raw.(string); ok {
		return text, true
	}
	parts, ok := raw.([]any)
	if !ok {
		return "", false
	}
	var result strings.Builder
	for _, rawPart := range parts {
		part, ok := rawPart.(map[string]any)
		if !ok || !requestIntegrityOnlyKeys(part, "type", "text") {
			return "", false
		}
		typ, _ := part["type"].(string)
		text, ok := part["text"].(string)
		if !ok || (typ != "text" && typ != "input_text" && typ != "output_text") {
			return "", false
		}
		result.WriteString(text)
	}
	return result.String(), true
}

func canonicalizeRequestIntegritySystem(body map[string]any, rules map[string]bool) {
	input, ok := body["input"].([]any)
	if !ok {
		return
	}
	omit := true
	if text, ok := body["text"].(map[string]any); ok {
		if format, ok := text["format"].(map[string]any); ok && format["type"] == "json_object" {
			omit = false
		}
	}
	var promoted []string
	retained := make([]any, 0, len(input))
	for _, raw := range input {
		item, ok := raw.(map[string]any)
		if !ok || item["role"] != "system" || !requestIntegrityOnlyKeys(item, "type", "role", "content", "id") {
			retained = append(retained, raw)
			continue
		}
		text, lossless := requestIntegrityLosslessText(item["content"])
		if !lossless {
			retained = append(retained, raw)
			continue
		}
		if text != "" {
			promoted = append(promoted, text)
		}
		if !omit {
			item["role"] = "developer"
			retained = append(retained, raw)
		}
		rules["system_instruction_promotion"] = true
	}
	body["input"] = retained
	if len(promoted) > 0 {
		instructions := strings.Join(promoted, "\n\n")
		if existing, ok := body["instructions"].(string); ok && strings.TrimSpace(existing) != "" {
			instructions += "\n\n" + existing
		}
		body["instructions"] = instructions
	}
}
