package apicompat

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// ResponsesToAnthropicRequestWithPathMapping performs the normal conversion and
// reports the destination of supported source text and search timezone fields.
// An empty destination means the field was discarded; an absent entry means its
// provenance is unknown. Paths are structural: identical text never associates
// an input with a different message or content block.
func ResponsesToAnthropicRequestWithPathMapping(req *ResponsesRequest) (*AnthropicRequest, map[string]string, error) {
	if req == nil {
		return nil, nil, fmt.Errorf("responses request is nil")
	}
	out, err := ResponsesToAnthropicRequest(req)
	if err != nil {
		return nil, nil, err
	}
	paths := make(map[string]string)
	messages, textPaths := responsesAnthropicPathMessages(req.Input)
	if responsesAnthropicPathShapeMatches(messages, out.Messages) {
		for source, target := range textPaths {
			paths[source] = target
		}
		for i, message := range messages {
			for j, source := range message.sources {
				if source == "" {
					continue
				}
				target := fmt.Sprintf("messages.%d.content", i)
				if !responsesAnthropicPathIsString(message.message.Content) {
					target += fmt.Sprintf(".%d.text", j)
				}
				paths[source] = target
			}
		}
	}
	for i, tool := range req.Tools {
		// The scanner also reports absent and malformed location fields. Map
		// their structural position regardless of the current field value.
		source := fmt.Sprintf("tools.%d.user_location.timezone", i)
		paths[source] = ""
		switch tool.Type {
		case "web_search", "google_search", "web_search_20250305":
			// The converter emits exactly one tool for each source tool.
			if len(out.Tools) == len(req.Tools) {
				paths[source] = source
			}
		}
	}
	return out, paths, nil
}

type responsesAnthropicPathMessage struct {
	message AnthropicMessage
	// One source text path per parsed block. Strings are one text block when
	// merging; the final content representation decides the destination suffix.
	sources []string
}

type responsesAnthropicPathBlock struct {
	block  AnthropicContentBlock
	source string
}

func responsesAnthropicPathIsString(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	return len(raw) > 0 && raw[0] == '"'
}

func responsesAnthropicPathMessages(raw json.RawMessage) ([]responsesAnthropicPathMessage, map[string]string) {
	paths := make(map[string]string)
	var text string
	if json.Unmarshal(raw, &text) == nil {
		content, _ := json.Marshal(text)
		message := responsesAnthropicPathMessage{message: AnthropicMessage{Role: "user", Content: content}}
		if responsesAnthropicPathIsString(raw) {
			message.sources = []string{"input"}
			paths["input"] = ""
		}
		return []responsesAnthropicPathMessage{message}, paths
	}
	var items []ResponsesInputItem
	if json.Unmarshal(raw, &items) != nil {
		return nil, paths
	}
	var messages []responsesAnthropicPathMessage
	for i, item := range items {
		prefix := fmt.Sprintf("input.%d.content", i)
		switch {
		case item.Role == "system" || item.Role == "developer":
			// These fields are joined with instructions in system. There is no
			// independent destination field, so leave their mapping unknown.
			continue
		case item.Type == "function_call":
			responsesAnthropicPathDiscard(item.Content, prefix, paths)
			input := json.RawMessage("{}")
			if item.Arguments != "" {
				input = json.RawMessage(item.Arguments)
			}
			messages = append(messages, responsesAnthropicPathFromBlocks("assistant", []responsesAnthropicPathBlock{{block: AnthropicContentBlock{
				Type: "tool_use", ID: fromResponsesCallIDToAnthropic(item.CallID), Name: item.Name, Input: input,
			}}}))
		case item.Type == "function_call_output":
			responsesAnthropicPathDiscard(item.Content, prefix, paths)
			messages = append(messages, responsesAnthropicPathFromBlocks("user", []responsesAnthropicPathBlock{{block: AnthropicContentBlock{
				Type: "tool_result", ToolUseID: fromResponsesCallIDToAnthropic(item.CallID), Content: responsesFunctionOutputToAnthropicContent(item),
			}}}))
		case item.Type == "reasoning":
			responsesAnthropicPathDiscard(item.Content, prefix, paths)
		default:
			role := "user"
			var content json.RawMessage
			if item.Role == "assistant" {
				role = "assistant"
				content, _ = convertResponsesAssistantToAnthropicContent(item.Content)
			} else {
				content, _ = convertResponsesUserToAnthropicContent(item.Content)
			}
			sources := responsesAnthropicPathContent(item.Content, prefix, role, paths)
			if anthropicContentIsEmpty(content) || (role == "assistant" && anthropicContentIsOnlyBlankText(content)) {
				continue
			}
			messages = append(messages, responsesAnthropicPathMessage{message: AnthropicMessage{Role: role, Content: content}, sources: sources})
		}
	}
	// Match the payload converter's two merges around tool pairing. Provenance
	// travels with blocks throughout reordering and removal.
	messages = responsesAnthropicPathMerge(messages)
	messages = responsesAnthropicPathPairTools(messages)
	messages = responsesAnthropicPathMerge(messages)
	return messages, paths
}

func responsesAnthropicPathDiscard(raw json.RawMessage, prefix string, paths map[string]string) {
	if responsesAnthropicPathIsString(raw) {
		paths[prefix] = ""
		return
	}
	var parts []map[string]json.RawMessage
	if json.Unmarshal(raw, &parts) != nil {
		return
	}
	for i, part := range parts {
		if responsesAnthropicPathIsString(part["text"]) {
			paths[fmt.Sprintf("%s.%d.text", prefix, i)] = ""
		}
	}
}

func responsesAnthropicPathContent(raw json.RawMessage, prefix, role string, paths map[string]string) []string {
	if responsesAnthropicPathIsString(raw) {
		paths[prefix] = ""
		return []string{prefix}
	}
	var parts []ResponsesContentPart
	if json.Unmarshal(raw, &parts) != nil {
		// Malformed content is passed through by the converter; do not infer
		// provenance for a shape outside its supported content schema.
		return nil
	}
	responsesAnthropicPathDiscard(raw, prefix, paths)
	var sources []string
	for i, part := range parts {
		textType := part.Type == "text" || (role == "user" && part.Type == "input_text") || (role == "assistant" && part.Type == "output_text")
		if textType && part.Text != "" {
			sources = append(sources, fmt.Sprintf("%s.%d.text", prefix, i))
		} else if role == "user" && part.Type == "input_image" && dataURIToAnthropicImageSource(part.ImageURL) != nil {
			sources = append(sources, "")
		}
	}
	return sources
}

func responsesAnthropicPathBlocks(message responsesAnthropicPathMessage) []responsesAnthropicPathBlock {
	blocks := parseContentBlocks(message.message.Content)
	out := make([]responsesAnthropicPathBlock, len(blocks))
	for i, block := range blocks {
		out[i].block = block
		if i < len(message.sources) {
			out[i].source = message.sources[i]
		}
	}
	return out
}

func responsesAnthropicPathFromBlocks(role string, blocks []responsesAnthropicPathBlock) responsesAnthropicPathMessage {
	values := make([]AnthropicContentBlock, len(blocks))
	sources := make([]string, len(blocks))
	for i, block := range blocks {
		values[i], sources[i] = block.block, block.source
	}
	return responsesAnthropicPathMessage{message: anthropicMessageFromBlocks(role, values), sources: sources}
}

func responsesAnthropicPathMerge(messages []responsesAnthropicPathMessage) []responsesAnthropicPathMessage {
	var out []responsesAnthropicPathMessage
	for _, message := range messages {
		if len(out) == 0 || out[len(out)-1].message.Role != message.message.Role {
			out = append(out, message)
			continue
		}
		last := &out[len(out)-1]
		blocks := append(responsesAnthropicPathBlocks(*last), responsesAnthropicPathBlocks(message)...)
		*last = responsesAnthropicPathFromBlocks(last.message.Role, blocks)
	}
	return out
}

func responsesAnthropicPathPairTools(messages []responsesAnthropicPathMessage) []responsesAnthropicPathMessage {
	results := make(map[string]responsesAnthropicPathBlock)
	for _, message := range messages {
		if message.message.Role == "user" {
			for _, block := range responsesAnthropicPathBlocks(message) {
				if block.block.Type == "tool_result" && block.block.ToolUseID != "" {
					results[block.block.ToolUseID] = block
				}
			}
		}
	}
	var out []responsesAnthropicPathMessage
	for _, message := range messages {
		blocks := responsesAnthropicPathBlocks(message)
		switch message.message.Role {
		case "assistant":
			var uses, others, kept []responsesAnthropicPathBlock
			for _, block := range blocks {
				if block.block.Type == "tool_use" {
					uses = append(uses, block)
					if _, exists := results[block.block.ID]; exists {
						kept = append(kept, block)
					}
				} else {
					others = append(others, block)
				}
			}
			if len(uses) == 0 {
				out = append(out, message)
				continue
			}
			if len(others)+len(kept) > 0 {
				out = append(out, responsesAnthropicPathFromBlocks("assistant", append(others, kept...)))
			}
			if len(kept) > 0 {
				paired := make([]responsesAnthropicPathBlock, 0, len(kept))
				for _, use := range kept {
					paired = append(paired, results[use.block.ID])
				}
				out = append(out, responsesAnthropicPathFromBlocks("user", paired))
			}
		case "user":
			var others []responsesAnthropicPathBlock
			hasResult := false
			for _, block := range blocks {
				if block.block.Type == "tool_result" {
					hasResult = true
				} else {
					others = append(others, block)
				}
			}
			if !hasResult {
				out = append(out, message)
			} else if len(others) > 0 {
				out = append(out, responsesAnthropicPathFromBlocks("user", others))
			}
		default:
			out = append(out, message)
		}
	}
	return out
}

func responsesAnthropicPathShapeMatches(mapped []responsesAnthropicPathMessage, actual []AnthropicMessage) bool {
	if len(mapped) != len(actual) {
		return false
	}
	for i, message := range mapped {
		if message.message.Role != actual[i].Role || responsesAnthropicPathIsString(message.message.Content) != responsesAnthropicPathIsString(actual[i].Content) {
			return false
		}
		left, right := parseContentBlocks(message.message.Content), parseContentBlocks(actual[i].Content)
		if len(left) != len(right) {
			return false
		}
		for j, block := range left {
			if block.Type != right[j].Type || block.ID != right[j].ID || block.ToolUseID != right[j].ToolUseID {
				return false
			}
		}
	}
	return true
}
