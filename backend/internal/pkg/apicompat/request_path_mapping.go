package apicompat

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ComposeRequestPathMappings combines two consecutive, explicit adapter traces.
// Empty destinations mean a source was removed; absent entries mean an adapter
// cannot establish a unique association. A missing next-hop mapping must never
// silently fall back to the same path, since another source may now occupy it.
func ComposeRequestPathMappings(first, next map[string]string) map[string]string {
	combined := make(map[string]string, len(first))
	for source, intermediate := range first {
		if intermediate == "" {
			combined[source] = ""
		} else if destination, ok := next[intermediate]; ok {
			combined[source] = destination
		}
	}
	return combined
}

// ChatCompletionsToResponsesWithPathMapping also reports the provenance of
// message text and server-search location paths. Mapping never depends on text
// equality. The original conversion API and serialized payload are unchanged.
func ChatCompletionsToResponsesWithPathMapping(req *ChatCompletionsRequest) (*ResponsesRequest, map[string]string, error) {
	if req == nil {
		return nil, nil, fmt.Errorf("chat completions request is nil")
	}
	out, err := ChatCompletionsToResponses(req)
	if err != nil {
		return nil, nil, err
	}
	paths := make(map[string]string)
	outputIndex := 0
	for i, message := range req.Messages {
		items, err := chatMessageToResponsesItems(message)
		if err != nil {
			return nil, nil, err
		}
		source := fmt.Sprintf("messages.%d.content", i)
		destination := fmt.Sprintf("input.%d.content", outputIndex)
		parts := requestPathTextParts(message.Content, source)
		switch message.Role {
		case "assistant":
			// Assistant parts are concatenated, sometimes with reasoning text.
			// Only a single unadorned source has an exact text association.
			if len(parts) == 1 && message.ReasoningContent == "" && len(items) > 0 && items[0].Role == "assistant" {
				paths[parts[0].path] = destination + ".0.text"
			}
		case "tool", "function":
			if len(parts) == 1 && len(items) == 1 {
				paths[parts[0].path] = fmt.Sprintf("input.%d.output", outputIndex)
			}
		default:
			if requestPathIsString(message.Content) {
				paths[source] = destination
			} else {
				var content []ChatContentPart
				if json.Unmarshal(message.Content, &content) == nil {
					partIndex := 0
					for j, part := range content {
						converted := convertChatContentPartsToResponses([]ChatContentPart{part})
						path := fmt.Sprintf("%s.%d.text", source, j)
						if part.Type == "text" && part.Text != "" && len(converted) > 0 {
							paths[path] = fmt.Sprintf("%s.%d.text", destination, partIndex)
						} else {
							paths[path] = ""
						}
						partIndex += len(converted)
					}
				}
			}
		}
		outputIndex += len(items)
	}
	toolIndex := 0
	for i, tool := range req.Tools {
		converted := convertChatToolsToResponses([]ChatTool{tool}, nil)
		source := fmt.Sprintf("tools.%d.user_location.timezone", i)
		paths[source] = ""
		if strings.ToLower(strings.TrimSpace(tool.Type)) == "web_search" && len(converted) == 1 {
			paths[source] = fmt.Sprintf("tools.%d.user_location.timezone", toolIndex)
		}
		toolIndex += len(converted)
	}
	return out, paths, nil
}

// AnthropicToResponsesWithPathMapping exposes stable source paths through tool
// extraction, image filtering, system promotion and message expansion.
func AnthropicToResponsesWithPathMapping(req *AnthropicRequest) (*ResponsesRequest, map[string]string, error) {
	if req == nil {
		return nil, nil, fmt.Errorf("anthropic request is nil")
	}
	out, err := AnthropicToResponses(req)
	if err != nil {
		return nil, nil, err
	}
	paths := make(map[string]string)
	outputIndex := 0
	if len(req.System) > 0 {
		parts, err := parseAnthropicSystemContentParts(req.System)
		if err != nil {
			return nil, nil, err
		}
		if requestPathIsString(req.System) {
			paths["system"] = ""
			if len(parts) > 0 {
				paths["system"] = "input.0.content.0.text"
			}
		} else {
			var blocks []AnthropicContentBlock
			if json.Unmarshal(req.System, &blocks) == nil {
				partIndex := 0
				for i, block := range blocks {
					source := fmt.Sprintf("system.%d.text", i)
					paths[source] = ""
					if block.Type == "text" && block.Text != "" && !isAnthropicBillingHeaderText(block.Text) {
						paths[source] = fmt.Sprintf("input.0.content.%d.text", partIndex)
						partIndex++
					}
				}
			}
		}
		if len(parts) > 0 {
			outputIndex++
		}
	}
	for i, message := range req.Messages {
		items, err := anthropicMsgToResponsesItems(message)
		if err != nil {
			return nil, nil, err
		}
		source := fmt.Sprintf("messages.%d.content", i)
		if requestPathIsString(message.Content) {
			paths[source] = fmt.Sprintf("input.%d.content.0.text", outputIndex)
		} else {
			var blocks []AnthropicContentBlock
			if json.Unmarshal(message.Content, &blocks) == nil {
				if message.Role == "assistant" {
					mapAnthropicAssistantTextPaths(paths, blocks, source, outputIndex, items)
				} else {
					toolResults := 0
					for _, block := range blocks {
						if block.Type == "tool_result" {
							toolResults++
						}
					}
					partIndex := 0
					for j, block := range blocks {
						path := fmt.Sprintf("%s.%d.text", source, j)
						paths[path] = ""
						switch block.Type {
						case "text":
							if block.Text != "" {
								paths[path] = fmt.Sprintf("input.%d.content.%d.text", outputIndex+toolResults, partIndex)
								partIndex++
							}
						case "image":
							if anthropicImageToDataURI(block.Source) != "" {
								partIndex++
							}
						}
					}
				}
			}
		}
		outputIndex += len(items)
	}
	for i, tool := range req.Tools {
		source := fmt.Sprintf("tools.%d.user_location.timezone", i)
		paths[source] = ""
		if strings.HasPrefix(tool.Type, "web_search") {
			// The existing converter emits exactly one tool for every source.
			paths[source] = source
		}
	}
	return out, paths, nil
}

func mapAnthropicAssistantTextPaths(paths map[string]string, blocks []AnthropicContentBlock, source string, outputIndex int, items []ResponsesInputItem) {
	textPaths := make([]string, 0, len(blocks))
	for i, block := range blocks {
		path := fmt.Sprintf("%s.%d.text", source, i)
		if block.Type == "text" && block.Text != "" {
			textPaths = append(textPaths, path)
		} else {
			paths[path] = ""
		}
	}
	if len(textPaths) != 1 {
		return // Joined text has no unique whole-block source association.
	}
	for i, item := range items {
		if item.Type == "message" && item.Role == "assistant" {
			paths[textPaths[0]] = fmt.Sprintf("input.%d.content.0.text", outputIndex+i)
			return
		}
	}
}

type requestPathTextPart struct{ path string }

func requestPathIsString(raw json.RawMessage) bool {
	trimmed := bytesTrimSpace(raw)
	return len(trimmed) > 0 && trimmed[0] == '"'
}

func requestPathTextParts(raw json.RawMessage, prefix string) []requestPathTextPart {
	if requestPathIsString(raw) {
		return []requestPathTextPart{{path: prefix}}
	}
	var parts []map[string]json.RawMessage
	if json.Unmarshal(raw, &parts) != nil {
		return nil
	}
	var paths []requestPathTextPart
	for i, part := range parts {
		if requestPathIsString(part["text"]) {
			paths = append(paths, requestPathTextPart{path: fmt.Sprintf("%s.%d.text", prefix, i)})
		}
	}
	return paths
}
