package apicompat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// ChatConversionCheck reports the explicitly checked conversion semantics. It
// does not assert fidelity for arbitrary provider extensions. Values and user
// supplied object keys must never be included in this diagnostic contract.
type ChatConversionCheck struct {
	Status string                `json:"status"`
	Issues []ChatConversionIssue `json:"issues,omitempty"`
}

type ChatConversionIssue struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// ChatConversionError describes a client request which this OAuth adapter cannot
// preserve. Path consists only of fixed protocol field names and array indexes.
type ChatConversionError struct {
	Path   string
	Reason string
}

func (e *ChatConversionError) Error() string {
	return "unsupported chat conversion at " + e.Path + ": " + e.Reason
}

type chatConversionChecker struct {
	check *ChatConversionCheck
	tools map[string]int
	types map[string]bool
	calls map[string]bool
	done  map[string]bool
}

// CheckOpenAIOAuthChatConversion checks raw Chat JSON before typed decoding can
// discard content. Call it only for the OpenAI OAuth Chat-to-Responses route;
// native Responses and other providers have different compatibility contracts.
// It never modifies or retains the request bytes and never contacts upstream.
func CheckOpenAIOAuthChatConversion(body []byte) (*ChatConversionCheck, error) {
	root, ok := chatCheckObject(body)
	if !ok {
		return nil, chatCheckError("request", "invalid_json_object")
	}
	c := &chatConversionChecker{
		check: &ChatConversionCheck{Status: "checked"}, tools: map[string]int{},
		types: map[string]bool{}, calls: map[string]bool{}, done: map[string]bool{},
	}
	if err := c.checkOutput(root); err != nil {
		return nil, err
	}
	if err := c.checkTools(root); err != nil {
		return nil, err
	}
	if err := c.checkChoice(root); err != nil {
		return nil, err
	}
	messages, ok := chatCheckArray(root["messages"])
	if !ok {
		return nil, chatCheckError("messages", "invalid_messages")
	}
	for index, raw := range messages {
		if err := c.checkMessage(raw, fmt.Sprintf("messages.%d", index)); err != nil {
			return nil, err
		}
	}
	return c.check, nil
}

func chatCheckError(path, reason string) error {
	return &ChatConversionError{Path: path, Reason: reason}
}

func (c *chatConversionChecker) loss(path, reason string) {
	c.check.Status = "known_loss"
	if len(c.check.Issues) < 32 {
		c.check.Issues = append(c.check.Issues, ChatConversionIssue{Path: path, Reason: reason})
	}
}

func chatCheckPresent(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	return len(raw) > 0 && !bytes.Equal(raw, []byte("null"))
}

func chatCheckObject(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	var object map[string]json.RawMessage
	err := json.Unmarshal(raw, &object)
	return object, err == nil && object != nil
}

func chatCheckArray(raw json.RawMessage) ([]json.RawMessage, bool) {
	var array []json.RawMessage
	err := json.Unmarshal(raw, &array)
	return array, err == nil && array != nil
}

func chatCheckString(raw json.RawMessage) (string, bool) {
	var value string
	err := json.Unmarshal(raw, &value)
	return value, err == nil && chatCheckPresent(raw)
}

func chatCheckNonempty(raw json.RawMessage) bool {
	if !chatCheckPresent(raw) {
		return false
	}
	switch string(bytes.TrimSpace(raw)) {
	case `""`, "[]", "{}", "false":
		return false
	}
	return true
}

// Unsupported nested keys are reported at their known parent path, never by
// echoing a caller-controlled key into logs or API error messages.
func chatCheckKeys(object map[string]json.RawMessage, path, reason string, keys ...string) error {
	allowed := make(map[string]bool, len(keys))
	for _, key := range keys {
		allowed[key] = true
	}
	for key, value := range object {
		if !allowed[key] && chatCheckPresent(value) {
			return chatCheckError(path, reason)
		}
	}
	return nil
}

func chatCheckOptionalString(object map[string]json.RawMessage, key, path string) error {
	if chatCheckPresent(object[key]) {
		if _, ok := chatCheckString(object[key]); !ok {
			return chatCheckError(path+"."+key, "invalid_field_type")
		}
	}
	return nil
}

func chatCheckOptionalBool(object map[string]json.RawMessage, key, path string) error {
	if chatCheckPresent(object[key]) {
		var value bool
		if json.Unmarshal(object[key], &value) != nil {
			return chatCheckError(path+"."+key, "invalid_field_type")
		}
	}
	return nil
}

func (c *chatConversionChecker) checkOutput(root map[string]json.RawMessage) error {
	if err := chatCheckOptionalString(root, "instructions", "request"); err != nil {
		return err
	}
	if chatCheckNonempty(root["reasoning"]) {
		return chatCheckError("reasoning", "unsupported_reasoning_field")
	}
	for _, field := range []string{"max_tokens", "max_completion_tokens", "temperature", "top_p", "frequency_penalty", "presence_penalty"} {
		if !chatCheckPresent(root[field]) {
			continue
		}
		var number float64
		if json.Unmarshal(root[field], &number) != nil {
			return chatCheckError(field, "invalid_field_type")
		}
		reason := "oauth_sampling_parameter_ignored"
		if field == "max_tokens" || field == "max_completion_tokens" {
			var integer int
			if json.Unmarshal(root[field], &integer) != nil {
				return chatCheckError(field, "invalid_field_type")
			}
			reason = "oauth_output_limit_ignored"
		}
		c.loss(field, reason)
	}
	if chatCheckPresent(root["stop"]) {
		if _, ok := chatCheckString(root["stop"]); !ok {
			values, ok := chatCheckArray(root["stop"])
			if !ok {
				return chatCheckError("stop", "invalid_field_type")
			}
			for _, value := range values {
				if _, ok := chatCheckString(value); !ok {
					return chatCheckError("stop", "invalid_field_type")
				}
			}
		}
		c.loss("stop", "oauth_stop_ignored")
	}
	if chatCheckPresent(root["n"]) {
		var n int
		if json.Unmarshal(root["n"], &n) != nil || n < 1 {
			return chatCheckError("n", "invalid_output_constraint")
		}
		if n > 1 {
			return chatCheckError("n", "multiple_choices_unsupported")
		}
	}
	if chatCheckPresent(root["audio"]) {
		return chatCheckError("audio", "audio_output_unsupported")
	}
	if chatCheckPresent(root["modalities"]) {
		values, ok := chatCheckArray(root["modalities"])
		if !ok {
			return chatCheckError("modalities", "invalid_field_type")
		}
		for _, raw := range values {
			value, ok := chatCheckString(raw)
			if !ok || value != "text" {
				return chatCheckError("modalities", "audio_output_unsupported")
			}
		}
	}
	if chatCheckPresent(root["logprobs"]) {
		var enabled bool
		if json.Unmarshal(root["logprobs"], &enabled) != nil {
			return chatCheckError("logprobs", "invalid_field_type")
		}
		if enabled {
			return chatCheckError("logprobs", "logprobs_unsupported")
		}
	}
	if chatCheckPresent(root["top_logprobs"]) {
		var count int
		if json.Unmarshal(root["top_logprobs"], &count) != nil || count < 0 {
			return chatCheckError("top_logprobs", "invalid_field_type")
		}
		if count > 0 {
			return chatCheckError("top_logprobs", "logprobs_unsupported")
		}
	}
	if err := chatCheckOptionalString(root, "reasoning_effort", "request"); err != nil {
		return err
	}
	if chatCheckPresent(root["response_format"]) {
		return checkChatResponseFormat(root["response_format"])
	}
	return nil
}

func checkChatResponseFormat(raw json.RawMessage) error {
	format, ok := chatCheckObject(raw)
	if !ok {
		return chatCheckError("response_format", "invalid_response_format")
	}
	kind, _ := chatCheckString(format["type"])
	kind = strings.ToLower(strings.TrimSpace(kind))
	switch kind {
	case "text", "json_object":
		return chatCheckKeys(format, "response_format", "invalid_response_format", "type")
	case "json_schema":
		if err := chatCheckKeys(format, "response_format", "invalid_response_format", "type", "json_schema"); err != nil {
			return err
		}
		schema, ok := chatCheckObject(format["json_schema"])
		if !ok {
			return chatCheckError("response_format.json_schema", "invalid_response_format")
		}
		name, ok := chatCheckString(schema["name"])
		if !ok || strings.TrimSpace(name) == "" {
			return chatCheckError("response_format.json_schema.name", "invalid_response_format")
		}
		if _, ok := chatCheckObject(schema["schema"]); !ok {
			return chatCheckError("response_format.json_schema.schema", "invalid_response_format")
		}
		if err := chatCheckOptionalBool(schema, "strict", "response_format.json_schema"); err != nil {
			return err
		}
		if err := chatCheckOptionalString(schema, "description", "response_format.json_schema"); err != nil {
			return err
		}
		return chatCheckKeys(schema, "response_format.json_schema", "invalid_response_format", "name", "description", "schema", "strict")
	default:
		return chatCheckError("response_format.type", "unsupported_response_format")
	}
}

func (c *chatConversionChecker) checkTools(root map[string]json.RawMessage) error {
	if chatCheckPresent(root["parallel_tool_calls"]) {
		if err := chatCheckOptionalBool(root, "parallel_tool_calls", "request"); err != nil {
			return err
		}
	}
	for _, field := range []string{"tools", "functions"} {
		if !chatCheckPresent(root[field]) {
			continue
		}
		tools, ok := chatCheckArray(root[field])
		if !ok {
			return chatCheckError(field, "invalid_tool_definition")
		}
		for index, raw := range tools {
			path := fmt.Sprintf("%s.%d", field, index)
			tool, ok := chatCheckObject(raw)
			if !ok {
				return chatCheckError(path, "invalid_tool_definition")
			}
			if field == "functions" {
				if err := c.checkFunction(tool, path); err != nil {
					return err
				}
				continue
			}
			kind, _ := chatCheckString(tool["type"])
			c.types[kind] = true
			switch kind {
			case "function":
				if err := chatCheckKeys(tool, path, "unsupported_tool_field", "type", "function"); err != nil {
					return err
				}
				function, ok := chatCheckObject(tool["function"])
				if !ok {
					return chatCheckError(path+".function", "invalid_tool_definition")
				}
				if err := c.checkFunction(function, path+".function"); err != nil {
					return err
				}
			case "web_search":
				if err := chatCheckKeys(tool, path, "unsupported_tool_field", "type", "user_location"); err != nil {
					return err
				}
			case "code_execution":
				if err := chatCheckKeys(tool, path, "unsupported_tool_field", "type"); err != nil {
					return err
				}
			case "x_search":
				if err := chatCheckKeys(tool, path, "unsupported_tool_field", "type", "allowed_x_handles", "excluded_x_handles", "from_date", "to_date", "enable_image_understanding", "enable_video_understanding"); err != nil {
					return err
				}
				for _, key := range []string{"allowed_x_handles", "excluded_x_handles"} {
					if !chatCheckPresent(tool[key]) {
						continue
					}
					values, ok := chatCheckArray(tool[key])
					if !ok {
						return chatCheckError(path+"."+key, "invalid_field_type")
					}
					for _, value := range values {
						if _, ok := chatCheckString(value); !ok {
							return chatCheckError(path+"."+key, "invalid_field_type")
						}
					}
				}
				for _, key := range []string{"from_date", "to_date"} {
					if err := chatCheckOptionalString(tool, key, path); err != nil {
						return err
					}
				}
				for _, key := range []string{"enable_image_understanding", "enable_video_understanding"} {
					if err := chatCheckOptionalBool(tool, key, path); err != nil {
						return err
					}
				}
			default:
				return chatCheckError(path+".type", "unsupported_tool_type")
			}
		}
	}
	return nil
}

func (c *chatConversionChecker) checkFunction(function map[string]json.RawMessage, path string) error {
	name, ok := chatCheckString(function["name"])
	if !ok || strings.TrimSpace(name) == "" || strings.TrimSpace(name) != name {
		return chatCheckError(path+".name", "invalid_tool_definition")
	}
	c.tools[name]++
	if c.tools[name] > 1 {
		return chatCheckError(path+".name", "duplicate_tool_name")
	}
	if err := chatCheckOptionalString(function, "description", path); err != nil {
		return err
	}
	if err := chatCheckOptionalBool(function, "strict", path); err != nil {
		return err
	}
	if chatCheckPresent(function["parameters"]) {
		if _, ok := chatCheckObject(function["parameters"]); !ok {
			return chatCheckError(path+".parameters", "invalid_tool_definition")
		}
	}
	return chatCheckKeys(function, path, "unsupported_tool_field", "name", "description", "parameters", "strict")
}

func (c *chatConversionChecker) checkChoice(root map[string]json.RawMessage) error {
	if chatCheckPresent(root["tool_choice"]) {
		if chatCheckPresent(root["function_call"]) {
			return chatCheckError("function_call", "conflicting_tool_choice")
		}
		return c.checkToolChoice(root["tool_choice"], "tool_choice", false)
	}
	if chatCheckPresent(root["function_call"]) {
		return c.checkToolChoice(root["function_call"], "function_call", true)
	}
	return nil
}

func (c *chatConversionChecker) checkToolChoice(raw json.RawMessage, path string, legacy bool) error {
	if text, ok := chatCheckString(raw); ok {
		if text == "auto" || text == "none" || text == "required" {
			return nil
		}
		return chatCheckError(path, "invalid_tool_choice")
	}
	choice, ok := chatCheckObject(raw)
	if !ok {
		return chatCheckError(path, "invalid_tool_choice")
	}
	kind, _ := chatCheckString(choice["type"])
	if legacy {
		kind = "function"
	}
	switch kind {
	case "function":
		name, err := chatCheckFunctionReference(choice, path, legacy)
		if err != nil {
			return err
		}
		if c.tools[name] != 1 {
			return chatCheckError(path, "unknown_tool_reference")
		}
	case "allowed_tools":
		allowed := choice
		allowedPath := path
		if chatCheckPresent(choice["allowed_tools"]) {
			if err := chatCheckKeys(choice, path, "invalid_tool_choice", "type", "allowed_tools"); err != nil {
				return err
			}
			allowed, ok = chatCheckObject(choice["allowed_tools"])
			allowedPath += ".allowed_tools"
			if !ok {
				return chatCheckError(allowedPath, "invalid_tool_choice")
			}
		}
		if err := chatCheckKeys(allowed, allowedPath, "invalid_tool_choice", "type", "mode", "tools"); err != nil {
			return err
		}
		mode, _ := chatCheckString(allowed["mode"])
		if mode != "auto" && mode != "required" {
			return chatCheckError(allowedPath+".mode", "invalid_tool_choice")
		}
		tools, ok := chatCheckArray(allowed["tools"])
		if !ok || len(tools) == 0 {
			return chatCheckError(allowedPath+".tools", "invalid_tool_choice")
		}
		seen := map[string]bool{}
		for index, raw := range tools {
			referencePath := fmt.Sprintf("%s.tools.%d", allowedPath, index)
			reference, ok := chatCheckObject(raw)
			if !ok {
				return chatCheckError(referencePath, "invalid_tool_choice")
			}
			name, err := chatCheckFunctionReference(reference, referencePath, false)
			if err != nil {
				return err
			}
			if seen[name] || c.tools[name] != 1 {
				return chatCheckError(referencePath, "unknown_or_duplicate_tool_reference")
			}
			seen[name] = true
		}
	default:
		if !c.types[kind] || (kind != "web_search" && kind != "code_execution" && kind != "x_search") {
			return chatCheckError(path, "invalid_tool_choice")
		}
		return chatCheckKeys(choice, path, "invalid_tool_choice", "type")
	}
	return nil
}

func chatCheckFunctionReference(reference map[string]json.RawMessage, path string, legacy bool) (string, error) {
	if legacy {
		if err := chatCheckKeys(reference, path, "invalid_tool_choice", "name"); err != nil {
			return "", err
		}
	}
	if !legacy {
		kind, _ := chatCheckString(reference["type"])
		if kind != "function" {
			return "", chatCheckError(path+".type", "unsupported_tool_reference")
		}
	}
	if err := chatCheckKeys(reference, path, "invalid_tool_choice", "type", "name", "function"); err != nil {
		return "", err
	}
	nameRaw := reference["name"]
	if chatCheckPresent(reference["function"]) {
		function, ok := chatCheckObject(reference["function"])
		if !ok {
			return "", chatCheckError(path+".function", "invalid_tool_choice")
		}
		if err := chatCheckKeys(function, path+".function", "invalid_tool_choice", "name"); err != nil {
			return "", err
		}
		nameRaw = function["name"]
		if chatCheckPresent(reference["name"]) && !bytes.Equal(bytes.TrimSpace(reference["name"]), bytes.TrimSpace(nameRaw)) {
			return "", chatCheckError(path, "conflicting_tool_choice")
		}
	}
	name, ok := chatCheckString(nameRaw)
	if !ok || strings.TrimSpace(name) == "" || strings.TrimSpace(name) != name {
		return "", chatCheckError(path, "invalid_tool_choice")
	}
	return name, nil
}

func (c *chatConversionChecker) checkMessage(raw json.RawMessage, path string) error {
	message, ok := chatCheckObject(raw)
	if !ok {
		return chatCheckError(path, "invalid_message")
	}
	role, _ := chatCheckString(message["role"])
	switch role {
	case "system", "developer", "user", "assistant", "tool":
	case "function":
		return chatCheckError(path+".role", "legacy_function_history_unsupported")
	default:
		return chatCheckError(path+".role", "unsupported_role")
	}
	if chatCheckPresent(message["function_call"]) {
		return chatCheckError(path+".function_call", "legacy_function_history_unsupported")
	}
	for _, field := range []string{"name", "reasoning_content", "reasoning"} {
		if err := chatCheckOptionalString(message, field, path); err != nil {
			return err
		}
	}
	if role != "tool" && chatCheckNonempty(message["name"]) {
		return chatCheckError(path+".name", "unsupported_message_name")
	}
	if role != "tool" && chatCheckNonempty(message["tool_call_id"]) {
		return chatCheckError(path+".tool_call_id", "unsupported_message_field")
	}
	for _, field := range []string{"audio", "refusal"} {
		if chatCheckNonempty(message[field]) {
			return chatCheckError(path+"."+field, "unsupported_message_field")
		}
	}
	primary, _ := chatCheckString(message["reasoning_content"])
	alias, _ := chatCheckString(message["reasoning"])
	if primary != "" && alias != "" && primary != alias {
		return chatCheckError(path+".reasoning", "conflicting_reasoning_aliases")
	}
	if primary != "" || alias != "" {
		if role != "assistant" {
			return chatCheckError(path+".reasoning", "unsupported_message_field")
		}
		field := "reasoning_content"
		if primary == "" {
			field = "reasoning"
		}
		c.loss(path+"."+field, "assistant_reasoning_wrapped")
	}
	if err := c.checkContent(message["content"], path+".content", role); err != nil {
		return err
	}
	if role == "tool" {
		id, ok := chatCheckString(message["tool_call_id"])
		if !ok || strings.TrimSpace(id) == "" || !c.calls[id] {
			return chatCheckError(path+".tool_call_id", "unknown_tool_call_id")
		}
		if c.done[id] {
			return chatCheckError(path+".tool_call_id", "duplicate_tool_result")
		}
		c.done[id] = true
	}
	if !chatCheckPresent(message["tool_calls"]) {
		return nil
	}
	calls, ok := chatCheckArray(message["tool_calls"])
	if !ok {
		return chatCheckError(path+".tool_calls", "invalid_tool_call")
	}
	if len(calls) > 0 && role != "assistant" {
		return chatCheckError(path+".tool_calls", "unsupported_message_field")
	}
	for index, raw := range calls {
		callPath := fmt.Sprintf("%s.tool_calls.%d", path, index)
		call, ok := chatCheckObject(raw)
		if !ok {
			return chatCheckError(callPath, "invalid_tool_call")
		}
		if err := chatCheckKeys(call, callPath, "unsupported_tool_call_field", "id", "type", "function", "index"); err != nil {
			return err
		}
		if chatCheckPresent(call["index"]) {
			var index int
			if json.Unmarshal(call["index"], &index) != nil || index < 0 {
				return chatCheckError(callPath+".index", "invalid_field_type")
			}
		}
		kind, _ := chatCheckString(call["type"])
		if kind != "function" {
			return chatCheckError(callPath+".type", "unsupported_tool_call_type")
		}
		id, ok := chatCheckString(call["id"])
		if !ok || strings.TrimSpace(id) == "" {
			return chatCheckError(callPath+".id", "invalid_tool_call")
		}
		if c.calls[id] {
			return chatCheckError(callPath+".id", "duplicate_tool_call_id")
		}
		function, ok := chatCheckObject(call["function"])
		if !ok {
			return chatCheckError(callPath+".function", "invalid_tool_call")
		}
		if err := chatCheckKeys(function, callPath+".function", "unsupported_tool_call_field", "name", "arguments"); err != nil {
			return err
		}
		name, ok := chatCheckString(function["name"])
		if !ok || strings.TrimSpace(name) == "" {
			return chatCheckError(callPath+".function.name", "invalid_tool_call")
		}
		arguments, valid := chatCheckString(function["arguments"])
		if !valid && chatCheckPresent(function["arguments"]) {
			return chatCheckError(callPath+".function.arguments", "invalid_field_type")
		}
		if arguments == "" {
			c.loss(callPath+".function.arguments", "empty_tool_arguments_defaulted")
		}
		c.calls[id] = true
	}
	return nil
}

func (c *chatConversionChecker) checkContent(raw json.RawMessage, path, role string) error {
	if !chatCheckPresent(raw) {
		if role == "tool" {
			c.loss(path, "empty_tool_output_placeholder")
		}
		return nil
	}
	if text, ok := chatCheckString(raw); ok {
		if role == "tool" && text == "" {
			c.loss(path, "empty_tool_output_placeholder")
		}
		return nil
	}
	parts, ok := chatCheckArray(raw)
	if !ok {
		return chatCheckError(path, "unsupported_content")
	}
	hasToolText := false
	for index, raw := range parts {
		partPath := fmt.Sprintf("%s.%d", path, index)
		part, ok := chatCheckObject(raw)
		if !ok {
			return chatCheckError(partPath, "invalid_content_part")
		}
		kind, _ := chatCheckString(part["type"])
		switch kind {
		case "text":
			keys := []string{"type", "text"}
			// Input content preserves the raw breakpoint during conversion. The
			// assistant/tool routes flatten text and cannot preserve this policy.
			if role != "assistant" && role != "tool" {
				keys = append(keys, "prompt_cache_breakpoint")
			}
			if err := chatCheckKeys(part, partPath, "unsupported_content_part_field", keys...); err != nil {
				return err
			}
			if err := chatCheckOptionalString(part, "text", partPath); err != nil {
				return err
			}
			text, _ := chatCheckString(part["text"])
			hasToolText = hasToolText || text != ""
		case "thinking", "reasoning":
			if role != "assistant" {
				return chatCheckError(partPath+".type", "unsupported_content_part")
			}
			if err := chatCheckKeys(part, partPath, "unsupported_content_part_field", "type", "text", "thinking"); err != nil {
				return err
			}
			for _, key := range []string{"text", "thinking"} {
				if err := chatCheckOptionalString(part, key, partPath); err != nil {
					return err
				}
			}
			text, _ := chatCheckString(part["text"])
			thinking, _ := chatCheckString(part["thinking"])
			if text != "" && thinking != "" && text != thinking {
				return chatCheckError(partPath, "conflicting_reasoning_aliases")
			}
			if text != "" || thinking != "" {
				c.loss(partPath, "assistant_reasoning_wrapped")
			}
		case "image_url", "file":
			if role == "assistant" || role == "tool" {
				return chatCheckError(partPath+".type", "unsupported_content_part")
			}
			if err := c.checkMedia(part, partPath, kind); err != nil {
				return err
			}
		default:
			return chatCheckError(partPath+".type", "unsupported_content_part")
		}
	}
	if role == "tool" && !hasToolText {
		c.loss(path, "empty_tool_output_placeholder")
	}
	return nil
}

func (c *chatConversionChecker) checkMedia(part map[string]json.RawMessage, path, kind string) error {
	if err := chatCheckKeys(part, path, "unsupported_content_part_field", "type", kind, "prompt_cache_breakpoint"); err != nil {
		return err
	}
	if !chatCheckPresent(part[kind]) {
		c.loss(path, "empty_media_placeholder_omitted")
		return nil
	}
	media, ok := chatCheckObject(part[kind])
	if !ok {
		return chatCheckError(path+"."+kind, "invalid_content_part")
	}
	mediaPath := path + "." + kind
	if kind == "image_url" {
		if err := chatCheckKeys(media, mediaPath, "unsupported_content_part_field", "url", "detail"); err != nil {
			return err
		}
		for _, key := range []string{"url", "detail"} {
			if err := chatCheckOptionalString(media, key, mediaPath); err != nil {
				return err
			}
		}
		url, _ := chatCheckString(media["url"])
		detail, _ := chatCheckString(media["detail"])
		if detail != "" && detail != "auto" && detail != "low" && detail != "high" {
			return chatCheckError(mediaPath+".detail", "invalid_image_detail")
		}
		if url == "" || isEmptyBase64DataURI(url) {
			c.loss(path, "empty_media_placeholder_omitted")
		}
		return nil
	}
	if err := chatCheckKeys(media, mediaPath, "unsupported_content_part_field", "filename", "file_data", "file_id"); err != nil {
		return err
	}
	for _, key := range []string{"filename", "file_data", "file_id"} {
		if err := chatCheckOptionalString(media, key, mediaPath); err != nil {
			return err
		}
	}
	data, _ := chatCheckString(media["file_data"])
	id, _ := chatCheckString(media["file_id"])
	if data == "" && id == "" {
		c.loss(path, "empty_media_placeholder_omitted")
	}
	return nil
}
