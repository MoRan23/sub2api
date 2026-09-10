package apicompat

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// ResponsesToChatCompletionsRequestWithPathMapping also describes where source
// text fields were sent. Paths identify JSON fields, not text values. An empty
// destination means the converter definitely discarded that field; an absent
// entry means there is no unambiguous whole-field correspondence (for example,
// several text blocks joined into one string).
func ResponsesToChatCompletionsRequestWithPathMapping(req *ResponsesRequest, opts *ResponsesToChatOptions) (*ChatCompletionsRequest, map[string]string, error) {
	out, err := ResponsesToChatCompletionsRequestWithOptions(req, opts)
	if err != nil {
		return nil, nil, err
	}
	paths := make(map[string]string)
	for i, tool := range req.Tools {
		mapDroppedResponsesChatSearchLocation(paths, fmt.Sprintf("tools.%d", i), tool)
	}

	var initial []ChatMessage
	var origins []map[string]string
	if strings.TrimSpace(req.Instructions) != "" {
		content, _ := json.Marshal(req.Instructions)
		initial = append(initial, ChatMessage{Role: "system", Content: content})
		origins = append(origins, map[string]string{"instructions": "content"})
	}
	input := bytesTrimSpace(req.Input)
	if len(input) == 0 || string(input) == "null" {
		if len(initial) != 0 {
			paths["instructions"] = "messages.0.content"
		}
		return out, paths, nil
	}
	var text string
	if json.Unmarshal(input, &text) == nil {
		if len(initial) != 0 {
			paths["instructions"] = "messages.0.content"
		}
		paths["input"] = fmt.Sprintf("messages.%d.content", len(initial))
		return out, paths, nil
	}
	var items []json.RawMessage
	if json.Unmarshal(input, &items) != nil {
		return out, paths, nil
	}

	// Reuse the builder without the reasoning lookup: reasoning never changes
	// message emission or normalization, and a caller's lookup must run only once.
	built, media, err := buildChatMessagesFromItems(initial, items, nil)
	if err != nil {
		return out, paths, nil
	}
	origins, ok := traceResponsesChatMessageOrigins(built, origins, items, paths)
	if !ok {
		return out, paths, nil
	}
	applyNormalizedChatPathMappings(paths, origins, built, media, out.Messages)
	return out, paths, nil
}

func applyNormalizedChatPathMappings(paths map[string]string, origins []map[string]string, built []ChatMessage, media toolOutputMediaByCallID, actual []ChatMessage) {
	// Name is not consulted by the normalizer. Tag only this private projection,
	// then let the actual normalizer carry provenance through last-wins replies,
	// message reordering, dropped calls and newly inserted media messages.
	for i := range built {
		built[i].Name = strconv.Itoa(i + 1)
	}
	normalized := normalizeChatMessagesWithToolOutputMedia(built, media)
	if len(normalized) != len(actual) {
		return
	}
	for i := range normalized {
		if normalized[i].Role != actual[i].Role {
			return
		}
	}
	destinations := make(map[int][]int)
	for dest, message := range normalized {
		origin, err := strconv.Atoi(message.Name)
		if err == nil && origin > 0 && origin <= len(origins) {
			destinations[origin-1] = append(destinations[origin-1], dest)
		}
	}
	for origin, fields := range origins {
		for source, suffix := range fields {
			switch {
			case suffix == "", len(destinations[origin]) == 0:
				paths[source] = ""
			case len(destinations[origin]) == 1:
				paths[source] = fmt.Sprintf("messages.%d.%s", destinations[origin][0], suffix)
				// Repeated call IDs can emit the same reply more than once. A single
				// destination map cannot express that relationship; leave it unmatched.
			}
		}
	}
}

// traceResponsesChatMessageOrigins mirrors only the builder's emission choices,
// never its content conversion. Each emitted source is attached to its actual
// built message before normalization; destination indices are not guessed from
// input positions or matched using equal text.
func traceResponsesChatMessageOrigins(built []ChatMessage, origins []map[string]string, items []json.RawMessage, paths map[string]string) ([]map[string]string, bool) {
	invalidCalls := make(map[string]bool)
	invalidEmptyOutputs := 0
	appendOrigin := func(role string, fields map[string]string) bool {
		if len(origins) >= len(built) || built[len(origins)].Role != role {
			return false
		}
		origins = append(origins, fields)
		return true
	}
	for i, raw := range items {
		raw = bytesTrimSpace(raw)
		if len(raw) == 0 || string(raw) == "null" {
			continue
		}
		prefix := fmt.Sprintf("input.%d", i)
		var item map[string]json.RawMessage
		if json.Unmarshal(raw, &item) != nil {
			var text string
			if json.Unmarshal(raw, &text) != nil || !appendOrigin("user", map[string]string{prefix: "content"}) {
				return nil, false
			}
			continue
		}
		kind := rawString(item["type"])
		role := chatCompletionsBridgeRole(rawString(item["role"]))
		switch kind {
		case "reasoning":
			continue
		case "function_call", "custom_tool_call", "tool_search_call":
			if kind == "function_call" {
				arguments := rawString(item["arguments"])
				if strings.TrimSpace(arguments) == "" {
					arguments = "{}"
				}
				if !json.Valid([]byte(arguments)) {
					if id := rawString(item["call_id"]); id != "" {
						invalidCalls[id] = true
					} else {
						invalidEmptyOutputs++
					}
					continue
				}
			}
			if n := len(origins); n == 0 || built[n-1].Role != "assistant" {
				if !appendOrigin("assistant", nil) {
					return nil, false
				}
			}
			continue
		case "function_call_output", "custom_tool_call_output", "tool_search_output":
			callID := rawString(item["call_id"])
			if callID == "" && invalidEmptyOutputs > 0 {
				invalidEmptyOutputs--
				continue
			}
			if invalidCalls[callID] {
				continue
			}
			var fields map[string]string
			var output string
			if json.Unmarshal(item["output"], &output) == nil {
				if _, _, rewritten := extractToolOutputMedia(item["output"]); !rewritten {
					fields = map[string]string{prefix + ".output": "content"}
				}
			}
			if !appendOrigin("tool", fields) {
				return nil, false
			}
			continue
		case "input_text", "text":
			if !appendOrigin("user", map[string]string{prefix + ".text": "content"}) {
				return nil, false
			}
			continue
		case "input_image":
			if !appendOrigin("user", nil) {
				return nil, false
			}
			continue
		case "additional_tools":
			var tools []ResponsesTool
			if json.Unmarshal(item["tools"], &tools) == nil {
				for j, tool := range tools {
					mapDroppedResponsesChatSearchLocation(paths, fmt.Sprintf("%s.tools.%d", prefix, j), tool)
				}
			}
			continue
		}
		if kind != "" && kind != "message" {
			continue
		}
		content := item["content"]
		contentPath := prefix + ".content"
		if len(bytesTrimSpace(content)) == 0 && rawString(item["text"]) != "" {
			content = item["text"]
			contentPath = prefix + ".text"
		}
		if !appendOrigin(role, responsesChatContentOrigins(content, contentPath, role)) {
			return nil, false
		}
	}
	return origins, len(origins) == len(built)
}

func responsesChatContentOrigins(raw json.RawMessage, prefix, role string) map[string]string {
	fields := make(map[string]string)
	raw = bytesTrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return fields
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		fields[prefix] = "content"
		return fields
	}
	var parts []json.RawMessage
	if json.Unmarshal(raw, &parts) != nil {
		var part map[string]json.RawMessage
		if json.Unmarshal(raw, &part) == nil && json.Unmarshal(part["text"], &text) == nil {
			if kind := rawString(part["type"]); kind == "input_image" || kind == "image_url" {
				fields[prefix+".text"] = ""
			} else {
				fields[prefix+".text"] = "content"
			}
		}
		return fields
	}
	var textPaths []string
	partIndex := 0
	hasImage := false
	for i, rawPart := range parts {
		var part map[string]json.RawMessage
		if json.Unmarshal(rawPart, &part) != nil {
			continue
		}
		path := fmt.Sprintf("%s.%d.text", prefix, i)
		switch rawString(part["type"]) {
		case "input_text", "output_text", "text", "":
			if rawString(part["text"]) == "" {
				if _, exists := part["text"]; exists {
					fields[path] = ""
				}
				continue
			}
			textPaths = append(textPaths, path)
			fields[path] = fmt.Sprintf("content.%d.text", partIndex)
			partIndex++
		case "input_image", "image_url":
			url := rawString(part["image_url"])
			if url == "" {
				url = rawNestedString(part["image_url"], "url")
			}
			if url != "" {
				hasImage = true
				partIndex++
			}
		default:
			if _, exists := part["text"]; exists {
				fields[path] = ""
			}
		}
	}
	if !hasImage || role != "user" {
		for _, path := range textPaths {
			delete(fields, path)
		}
		if len(textPaths) == 1 {
			fields[textPaths[0]] = "content"
		}
	}
	return fields
}

func mapDroppedResponsesChatSearchLocation(paths map[string]string, prefix string, tool ResponsesTool) {
	if tool.Type != "google_search" && tool.Type != "web_search" && !strings.HasPrefix(tool.Type, "web_search_") {
		return
	}
	// Missing or malformed locations are observations too. The whole server
	// search declaration is discarded, independent of its location's shape.
	paths[prefix+".user_location.timezone"] = ""
}
