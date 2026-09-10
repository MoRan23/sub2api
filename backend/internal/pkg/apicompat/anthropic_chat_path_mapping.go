package apicompat

import (
	"encoding/json"
	"fmt"
	"strings"
)

// AnthropicToChatCompletionsRequestWithPathMapping preserves the direct Chat
// bridge's behavior while tracing whole text fields through its actual message
// converter and tool-pair normalizer. Empty destinations mean dropped fields;
// joined or multiply emitted fields without a unique destination stay absent.
func AnthropicToChatCompletionsRequestWithPathMapping(req *AnthropicRequest) (*ChatCompletionsRequest, map[string]string, error) {
	out, err := AnthropicToChatCompletionsRequest(req)
	if err != nil {
		return nil, nil, err
	}
	paths := make(map[string]string)
	for i, tool := range req.Tools {
		if strings.HasPrefix(tool.Type, "web_search") {
			paths[fmt.Sprintf("tools.%d.user_location.timezone", i)] = ""
		}
	}
	var built []ChatMessage
	var origins []map[string]string
	if len(req.System) > 0 {
		parts, err := parseAnthropicSystemContentParts(req.System)
		if err != nil {
			return out, paths, nil
		}
		fields := anthropicChatJoinedTextOrigins(req.System, "system", true)
		if text := joinResponsesContentPartText(parts); text != "" {
			content, _ := json.Marshal(text)
			built = append(built, ChatMessage{Role: "system", Content: content})
			origins = append(origins, fields)
		} else {
			for source := range fields {
				paths[source] = ""
			}
		}
	}
	for i, message := range req.Messages {
		converted, err := anthropicMsgToChatMessages(message)
		if err != nil {
			return out, paths, nil
		}
		prefix := fmt.Sprintf("messages.%d.content", i)
		fields := anthropicChatMessageOrigins(message, prefix, paths)
		if len(fields) != len(converted) {
			// A future converter branch must not silently associate old source
			// indices with a different emitted message structure.
			fields = make([]map[string]string, len(converted))
		}
		built = append(built, converted...)
		origins = append(origins, fields...)
	}
	applyNormalizedChatPathMappings(paths, origins, built, nil, out.Messages)
	return out, paths, nil
}

func anthropicChatMessageOrigins(message AnthropicMessage, prefix string, dropped map[string]string) []map[string]string {
	var text string
	if json.Unmarshal(message.Content, &text) == nil {
		return []map[string]string{{prefix: "content"}}
	}
	if message.Role == "assistant" {
		return []map[string]string{anthropicChatJoinedTextOrigins(message.Content, prefix, false)}
	}
	var blocks []AnthropicContentBlock
	if json.Unmarshal(message.Content, &blocks) != nil {
		return nil
	}
	var origins []map[string]string
	hasImage := false
	for i, block := range blocks {
		if block.Type != "tool_result" {
			continue
		}
		_, media := convertToolResultOutput(block)
		if len(media) > 0 {
			hasImage = true
		}
		fields := anthropicChatJoinedTextOrigins(block.Content, fmt.Sprintf("%s.%d.content", prefix, i), false)
		origins = append(origins, fields)
	}
	fields := make(map[string]string)
	var textPaths []string
	partIndex := 0
	for i, block := range blocks {
		path := fmt.Sprintf("%s.%d.text", prefix, i)
		switch block.Type {
		case "text":
			if block.Text == "" {
				dropped[path] = ""
				continue
			}
			textPaths = append(textPaths, path)
			fields[path] = fmt.Sprintf("content.%d.text", partIndex)
			partIndex++
		case "image":
			if anthropicImageToDataURI(block.Source) != "" {
				hasImage = true
				partIndex++
			}
		}
	}
	if hasImage {
		return append(origins, fields)
	}
	if len(textPaths) == 0 {
		return origins
	}
	fields = make(map[string]string)
	if len(textPaths) == 1 {
		fields[textPaths[0]] = "content"
	}
	return append(origins, fields)
}

// The direct bridge joins system/assistant/tool-result texts. A field mapping
// can represent one surviving text but not a substring of a joined message.
func anthropicChatJoinedTextOrigins(raw json.RawMessage, prefix string, filterBilling bool) map[string]string {
	fields := make(map[string]string)
	var text string
	if len(raw) == 0 {
		return fields
	}
	if json.Unmarshal(raw, &text) == nil {
		if filterBilling && (text == "" || isAnthropicBillingHeaderText(text)) {
			fields[prefix] = ""
		} else if text != "" {
			fields[prefix] = "content"
		}
		return fields
	}
	var blocks []AnthropicContentBlock
	if json.Unmarshal(raw, &blocks) != nil {
		return fields
	}
	var surviving []string
	for i, block := range blocks {
		if block.Type != "text" {
			continue
		}
		path := fmt.Sprintf("%s.%d.text", prefix, i)
		if block.Text == "" || (filterBilling && isAnthropicBillingHeaderText(block.Text)) {
			fields[path] = ""
		} else {
			surviving = append(surviving, path)
		}
	}
	if len(surviving) == 1 {
		fields[surviving[0]] = "content"
	}
	return fields
}
