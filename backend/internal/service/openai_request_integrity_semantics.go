package service

import (
	"reflect"
	"strings"
)

// These are deliberately small, lossless equivalences, not another run of the
// forwarding transformer. Unknown items, attributes and multimodal content are
// left comparable; recovery that deletes content never belongs in this list.
func canonicalizeRequestIntegrityCodex(body map[string]any, rules map[string]bool) []int {
	var inputIndices []int
	if input, ok := body["input"].([]any); ok {
		inputIndices = make([]int, len(input))
		for index := range input {
			inputIndices[index] = index
		}
	}
	if model, ok := body["model"].(string); ok && model != strings.TrimSpace(model) {
		body["model"] = strings.TrimSpace(model)
		rules["model_whitespace"] = true
	}
	if reasoning, ok := body["reasoning"].(map[string]any); ok {
		if reasoning["effort"] == "minimal" {
			reasoning = cloneRequestIntegrityObject(reasoning)
			body["reasoning"] = reasoning
			reasoning["effort"] = "none"
			rules["reasoning_effort_alias"] = true
		}
		model, _ := body["model"].(string)
		mode, _ := reasoning["mode"].(string)
		effort, _ := reasoning["effort"].(string)
		if !isOpenAIGPT6Model(model) && strings.EqualFold(strings.TrimSpace(mode), "pro") && strings.TrimSpace(effort) == "" {
			reasoning = cloneRequestIntegrityObject(reasoning)
			body["reasoning"] = reasoning
			reasoning["effort"] = "max"
			delete(reasoning, "mode")
			rules["reasoning_mode_alias"] = true
		}
	}
	if text, ok := body["input"].(string); ok {
		body["input"] = []any{map[string]any{"type": "message", "role": "user", "content": text}}
		inputIndices = []int{-1} // The original carrier is scalar, not input[0].
		rules["input_message_shape"] = true
	}
	// The forwarder removes this exact client-only field before promoting
	// system messages. Mirror that order so otherwise lossless promotion is
	// not mistaken for deleting a message with an unknown content attribute.
	if input, ok := body["input"].([]any); ok {
		owned := false
		for index, raw := range input {
			if item, ok := raw.(map[string]any); ok {
				if _, exists := item["internal_chat_message_metadata_passthrough"]; exists {
					if !owned {
						input = append([]any{}, input...)
						body["input"], owned = input, true
					}
					item = cloneRequestIntegrityObject(item)
					input[index] = item
					delete(item, "internal_chat_message_metadata_passthrough")
					rules["codex_input_metadata_removed"] = true
				}
			}
		}
	}
	inputIndices = canonicalizeRequestIntegritySystem(body, rules, inputIndices)
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
		owned := false
		for index, raw := range tools {
			tool, ok := raw.(map[string]any)
			if ok && tool["type"] == "function" {
				if flattened, changed := flattenRequestIntegrityFunction(tool); changed {
					if !owned {
						tools = append([]any{}, tools...)
						body["tools"], owned = tools, true
					}
					tools[index] = flattened
					rules["function_tool_shape"] = true
				}
			}
		}
	}
	if choice, ok := body["tool_choice"].(map[string]any); ok && choice["type"] == "function" {
		if flattened, changed := flattenRequestIntegrityFunction(choice); changed {
			body["tool_choice"] = flattened
			rules["function_choice_shape"] = true
		}
	}
	input, ok := body["input"].([]any)
	if !ok {
		return inputIndices
	}
	referenceIDs := codexItemReferenceIDMappings(input, false)
	itemIDs := codexInputItemIDs(input)
	callIDs := codexInputCallIDs(input)
	inputOwned := false
	for index, raw := range input {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		itemOwned := false
		writeItem := func() map[string]any {
			if !inputOwned {
				input = append([]any{}, input...)
				body["input"], inputOwned = input, true
			}
			if !itemOwned {
				item = cloneRequestIntegrityObject(item)
				input[index], itemOwned = item, true
			}
			return item
		}
		if item["role"] == "tool" && requestIntegrityOnlyKeys(item, "type", "role", "content", "tool_call_id", "call_id", "id") {
			callID := strings.TrimSpace(firstNonEmptyString(item["call_id"], item["tool_call_id"], item["id"]))
			if output, lossless := requestIntegrityLosslessText(item["content"]); lossless && callID != "" {
				writeItem()
				item = map[string]any{"type": "function_call_output", "call_id": callID, "output": output}
				input[index] = item
				rules["tool_result_shape"] = true
			}
		}
		typ, _ := item["type"].(string)
		if typ == "" && (item["role"] == "user" || item["role"] == "assistant" || item["role"] == "developer") {
			writeItem()
			typ, item["type"] = "message", "message"
			rules["input_message_shape"] = true
		}
		switch typ {
		case "message":
			if _, hasID := item["id"]; hasID {
				delete(writeItem(), "id")
				rules["replay_item_identity"] = true
			}
			if text, ok := item["content"].(string); ok {
				writeItem()["content"] = []any{map[string]any{"type": "input_text", "text": text}}
				rules["message_content_shape"] = true
			}
			if parts, ok := item["content"].([]any); ok {
				partsOwned := false
				for partIndex, rawPart := range parts {
					if part, ok := rawPart.(map[string]any); ok && (part["type"] == "text" || part["type"] == "output_text") {
						if !partsOwned {
							parts = append([]any{}, parts...)
							writeItem()["content"], partsOwned = parts, true
						}
						part = cloneRequestIntegrityObject(part)
						parts[partIndex] = part
						part["type"] = "input_text"
						rules["message_content_shape"] = true
					}
				}
			}
		case "reasoning":
			for _, key := range []string{"id", "call_id"} {
				if _, present := item[key]; present {
					delete(writeItem(), key)
					rules["replay_item_identity"] = true
				}
			}
			if item["summary"] == nil {
				writeItem()["summary"] = []any{}
				rules["reasoning_empty_summary"] = true
			}
		case "compaction_summary":
			if _, present := item["id"]; present {
				delete(writeItem(), "id")
				rules["replay_item_identity"] = true
			}
		case "item_reference":
			id, _ := item["id"].(string)
			id = strings.TrimSpace(id)
			if _, exists := itemIDs[id]; !exists && strings.HasPrefix(id, "call_") {
				if mapped, exists := referenceIDs[id]; exists {
					writeItem()["id"] = mapped
					rules["tool_call_identity"] = true
				} else if _, sameTurn := callIDs[id]; !sameTurn {
					writeItem()["id"] = normalizeCodexCallID(id)
					rules["tool_call_identity"] = true
				}
			}
		}
		if isCodexToolCallItemType(typ) {
			if id := firstNonEmptyString(item["call_id"], item["id"]); id != "" {
				normalized := normalizeCodexCallIDForItemType(typ, id)
				if item["call_id"] != normalized {
					writeItem()["call_id"] = normalized
					rules["tool_call_identity"] = true
				}
			}
			if _, present := item["id"]; present {
				delete(writeItem(), "id")
				rules["tool_call_identity"] = true
			}
		}
	}
	return inputIndices
}

func flattenRequestIntegrityFunction(tool map[string]any) (map[string]any, bool) {
	function, ok := tool["function"].(map[string]any)
	if !ok {
		return tool, false
	}
	for key, value := range function {
		if existing, present := tool[key]; present && !reflect.DeepEqual(existing, value) {
			return tool, false
		}
	}
	tool = cloneRequestIntegrityObject(tool)
	for key, value := range function {
		tool[key] = value
	}
	delete(tool, "function")
	return tool, true
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

func canonicalizeRequestIntegritySystem(body map[string]any, rules map[string]bool, indices []int) []int {
	input, ok := body["input"].([]any)
	if !ok {
		return indices
	}
	omit := true
	if text, ok := body["text"].(map[string]any); ok {
		if format, ok := text["format"].(map[string]any); ok && format["type"] == "json_object" {
			omit = false
		}
	}
	var promoted []string
	var retained []any
	var retainedIndices []int
	for index, raw := range input {
		item, ok := raw.(map[string]any)
		if !ok || item["role"] != "system" || !requestIntegrityOnlyKeys(item, "type", "role", "content", "id") {
			if retained != nil {
				retained = append(retained, raw)
				retainedIndices = append(retainedIndices, indices[index])
			}
			continue
		}
		text, lossless := requestIntegrityLosslessText(item["content"])
		if !lossless {
			if retained != nil {
				retained = append(retained, raw)
				retainedIndices = append(retainedIndices, indices[index])
			}
			continue
		}
		if retained == nil {
			retained = make([]any, index, len(input))
			copy(retained, input[:index])
			retainedIndices = make([]int, index, len(indices))
			copy(retainedIndices, indices[:index])
		}
		if text != "" {
			promoted = append(promoted, text)
		}
		if !omit {
			item = cloneRequestIntegrityObject(item)
			item["role"] = "developer"
			retained = append(retained, item)
			retainedIndices = append(retainedIndices, indices[index])
		}
		rules["system_instruction_promotion"] = true
	}
	if retained == nil {
		return indices
	}
	body["input"] = retained
	if len(promoted) > 0 {
		instructions := strings.Join(promoted, "\n\n")
		if existing, ok := body["instructions"].(string); ok && strings.TrimSpace(existing) != "" {
			instructions += "\n\n" + existing
		}
		body["instructions"] = instructions
	}
	return retainedIndices
}
