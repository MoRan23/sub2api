package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

type openAIExcelTool struct {
	Name      string
	Namespace string
	Kind      string
	Spec      map[string]any
}

func openAIExcelParseTools(source map[string]any) (map[string]openAIExcelTool, error) {
	tools := make(map[string]openAIExcelTool)
	if source["tool_choice"] == "none" {
		return tools, nil
	}
	var walk func(any, string, int) error
	walk = func(raw any, namespace string, depth int) error {
		if depth > 16 {
			return errors.New("excel tool namespace nesting is too deep")
		}
		if raw == nil {
			return nil
		}
		entries, ok := raw.([]any)
		if !ok {
			return errors.New("excel tools must be an array")
		}
		for _, entry := range entries {
			spec, ok := entry.(map[string]any)
			if !ok {
				return errors.New("invalid Excel tool definition")
			}
			kind := openAIExcelString(spec["type"])
			name := strings.TrimSpace(openAIExcelString(spec["name"]))
			if kind != "function" && kind != "custom" && kind != "namespace" {
				return fmt.Errorf("tool type %q is not supported by Excel client-tool relay", kind)
			}
			if name == "" || len(name) > 256 {
				return errors.New("excel client tool has an invalid name")
			}
			key := name
			if namespace != "" {
				key = namespace + "." + name
			}
			if kind == "namespace" {
				if err := walk(spec["tools"], key, depth+1); err != nil {
					return err
				}
				continue
			}
			if _, exists := tools[key]; exists {
				return errors.New("duplicate Excel client tool name")
			}
			if len(tools) >= 512 {
				return errors.New("too many Excel client tools")
			}
			tools[key] = openAIExcelTool{Name: name, Namespace: namespace, Kind: kind, Spec: spec}
		}
		return nil
	}
	if err := walk(source["tools"], "", 0); err != nil {
		return nil, err
	}
	if choice := openAIExcelMap(source["tool_choice"]); choice != nil {
		name := openAIExcelString(choice["name"])
		if ns := openAIExcelString(choice["namespace"]); ns != "" {
			name = ns + "." + name
		}
		tool, ok := tools[name]
		if !ok {
			return nil, errors.New("excel tool_choice does not name a declared client tool")
		}
		return map[string]openAIExcelTool{name: tool}, nil
	}
	if choice := openAIExcelString(source["tool_choice"]); choice != "" && choice != "auto" && choice != "required" {
		return nil, errors.New("unsupported Excel tool_choice")
	}
	return tools, nil
}

func (s *OpenAIExcelWireState) toolInstructions() (string, error) {
	if len(s.tools) == 0 {
		return "This request is relayed by an external OpenAI Responses API client, not by the live Excel workbook. Do not call server-injected Excel, Office, connector, workbook, or web-search tools. Return the answer as assistant text.", nil
	}
	keys := make([]string, 0, len(s.tools))
	for key := range s.tools {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	catalog := make([]any, 0, len(keys))
	for _, key := range keys {
		tool := s.tools[key]
		entry := map[string]any{"name": key, "type": tool.Kind}
		if tool.Namespace != "" {
			entry["namespace"] = tool.Namespace
			entry["tool"] = tool.Name
		}
		if description := openAIExcelString(tool.Spec["description"]); description != "" {
			entry["description"] = description
		}
		if tool.Kind == "function" {
			entry["parameters"] = openAIExcelToolSchema(tool.Spec)
		} else if format := openAIExcelMap(tool.Spec["format"]); format != nil {
			entry["format"] = format
		}
		catalog = append(catalog, entry)
	}
	raw, err := json.Marshal(catalog)
	if err != nil {
		return "", err
	}
	parallel := "Make independent client calls as separate run_officejs calls; each carries exactly one catalog-tool object."
	if !s.parallel {
		parallel = "Make at most one run_officejs client-tool call in this response."
	}
	if s.required {
		parallel += " This response must call a catalog tool; a text-only answer does not satisfy the caller's tool_choice."
	}
	return `This request is relayed by an external Codex Responses API client, not by the live Excel workbook. The native run_officejs function is a transport endpoint: its output is intercepted and delivered to the client, and no Office code is executed. Only the client tools in the catalog below are available. Do not use other Excel, Office, connector, workbook, list_skills or web-search tools.
Call a catalog tool through one native run_officejs call. The outer arguments contain summary, extended_summary, destructive=false, references=[], and code. The code field is JSON text, never JavaScript or OfficeJS. For a function tool encode {"name":"catalog.name","arguments":{...}}; for a custom tool encode {"name":"catalog.name","input":"raw input"}. Serialize the complete inner JSON object, including all quotes and backslashes, before placing it in code. Do not nest a second run_officejs wrapper. Use the exact name shown in the catalog, including any declared namespace. Do not add a display-only host prefix such as functions.; do not remove functions. when it is part of a catalog name. Follow each catalog schema exactly. Returned tool results belong to that client tool. Never repeat calls whose output is already in the history. Do not claim workspace access is unavailable when the catalog supplies a suitable tool. If fulfilling the request requires a client tool, do not stop at commentary or a plan saying you will act: make the actual tool call in the same response. A request that needs no tool may be answered directly. Native update_plan is permitted only when update_plan appears in the catalog and matches its schema; after its result, take the next substantive action through run_officejs when the task requires it.
` + parallel + "\nAvailable client tools:\n" + string(raw), nil
}

// Keep the reminder next to the stable tool catalog, before conversation
// history. Appending it after history would break the shared prompt prefix on
// every subsequent tool turn.
func (s *OpenAIExcelWireState) toolProtocolReminder() string {
	if len(s.tools) == 0 {
		return ""
	}
	keys := make([]string, 0, len(s.tools))
	hasCustom := false
	for name, tool := range s.tools {
		keys = append(keys, name)
		hasCustom = hasCustom || tool.Kind == "custom"
	}
	sort.Strings(keys)
	reminder := `Reminder: when the task requires a client tool, use the outer native run_officejs transport in the same response; do not merely say you will act. It never executes Office code here. Put exactly one catalog-tool JSON object as JSON text in code, not JavaScript or another transport envelope. Use the exact catalog name, retaining its declared namespace; a display-only host prefix must not be added or stripped from a real catalog name. Escape quotes and backslashes when serializing the inner JSON. A request that needs no tool may be answered directly. Client tools: ` + strings.Join(keys, ", ") + ". Other native tools are unavailable."
	if hasCustom {
		reminder += ` Custom tools use input, not arguments: {"name":"TOOL_NAME","input":"RAW_INPUT"}.`
	}
	if tool, ok := s.tools["apply_patch"]; ok && tool.Kind == "custom" {
		reminder += " For apply_patch, put the complete raw patch in input; never use arguments.patch."
	}
	if _, ok := s.tools["update_plan"]; ok {
		reminder += " Native update_plan is allowed for progress; after its result, take the next substantive action through run_officejs when the task requires it."
	}
	return reminder
}

func openAIExcelToolSchema(spec map[string]any) map[string]any {
	for _, key := range []string{"parameters", "inputSchema", "input_schema"} {
		if schema := openAIExcelMap(spec[key]); schema != nil {
			return schema
		}
	}
	return map[string]any{}
}

func (s *OpenAIExcelWireState) translateHistory(ctx context.Context, raw any) ([]any, error) {
	var items []any
	switch input := raw.(type) {
	case string:
		return []any{openAIExcelMessage("user", input)}, nil
	case []any:
		items = input
	case nil:
		return []any{}, nil
	default:
		return nil, errors.New("invalid Excel input")
	}
	encrypted := false
	for _, rawItem := range items {
		item := openAIExcelMap(rawItem)
		if openAIExcelString(item["type"]) == "reasoning" && openAIExcelString(item["encrypted_content"]) != "" {
			encrypted = true
		}
	}
	result := make([]any, 0, len(items))
	origins := make(map[string]string)
	for _, rawItem := range items {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		item := openAIExcelMap(rawItem)
		if item == nil {
			return nil, errors.New("invalid Excel input item")
		}
		delete(item, "internal_chat_message_metadata_passthrough")
		kind := openAIExcelString(item["type"])
		switch kind {
		case "item_reference":
			return nil, errors.New("excel upstream requires full input instead of item_reference")
		case "reasoning":
			if ciphertext := openAIExcelString(item["encrypted_content"]); ciphertext != "" {
				result = append(result, map[string]any{"type": "reasoning", "summary": []any{}, "encrypted_content": ciphertext})
			}
		case "function_call", "custom_tool_call":
			callID := openAIExcelString(item["call_id"])
			if callID == "" {
				return nil, errors.New("excel tool history is missing call_id")
			}
			var native map[string]any
			if s.options.History != nil {
				stored, err := s.options.History.LoadExcelNativeCall(ctx, s.options.HistoryScope, callID)
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				if err != nil && !errors.Is(err, ErrOpenAIExcelHistoryNotFound) {
					return nil, ErrOpenAIExcelHistoryStorageUnavailable
				}
				if len(stored) > 0 {
					if len(stored) > openAIExcelMaxItemBytes || openAIExcelJSON(stored, &native) != nil || openAIExcelString(native["call_id"]) != callID {
						return nil, errors.New("invalid Excel native tool history")
					}
				}
			}
			if native == nil {
				if encrypted {
					return nil, ErrOpenAIExcelHistoryNotFound
				}
				var err error
				native, err = openAIExcelRestoreCall(item)
				if err != nil {
					return nil, err
				}
			}
			origins[callID] = openAIExcelString(native["name"])
			result = append(result, native)
		case "function_call_output", "custom_tool_call_output":
			callID := openAIExcelString(item["call_id"])
			if callID == "" {
				return nil, errors.New("excel tool output is missing call_id")
			}
			origin := origins[callID]
			if origin == "" {
				return nil, errors.New("excel tool output requires its full call history")
			}
			if origin == "update_plan" {
				item["output"] = `{"status":"ok"}`
			}
			if openAIExcelIsTransport(origin) {
				item["type"] = "function_call_output"
			}
			item["id"] = "fc_" + callID
			if value, present := item["output"]; !present || value == nil || value == "" {
				item["output"] = "(tool call succeeded with no output)"
			}
			result = append(result, item)
		default:
			result = append(result, item)
		}
	}
	return result, nil
}

func openAIExcelRestoreCall(item map[string]any) (map[string]any, error) {
	name := openAIExcelString(item["name"])
	if namespace := openAIExcelString(item["namespace"]); namespace != "" {
		name = namespace + "." + name
	}
	if name == "" {
		return nil, errors.New("excel history is missing the client tool name")
	}
	callID := openAIExcelString(item["call_id"])
	envelope := map[string]any{"name": name}
	if openAIExcelString(item["type"]) == "custom_tool_call" {
		input, ok := item["input"].(string)
		if !ok {
			return nil, errors.New("excel custom tool history is incomplete")
		}
		envelope["input"] = input
	} else {
		var arguments map[string]any
		if err := openAIExcelJSON([]byte(openAIExcelString(item["arguments"])), &arguments); err != nil || arguments == nil {
			return nil, errors.New("excel function tool history is incomplete")
		}
		if name == "update_plan" {
			arguments = openAIExcelRestorePlan(arguments)
			raw, _ := json.Marshal(arguments)
			return map[string]any{"type": "function_call", "id": "fc_" + callID, "call_id": callID, "name": "update_plan", "arguments": string(raw), "status": "completed"}, nil
		}
		envelope["arguments"] = arguments
	}
	code, _ := json.Marshal(envelope)
	args, _ := json.Marshal(map[string]any{"summary": "Run client tool " + name, "extended_summary": "Relay " + name + " through the external client", "code": string(code), "destructive": false, "references": []any{}})
	return map[string]any{"type": "function_call", "id": "fc_" + callID, "call_id": callID, "name": openAIExcelTransportTool, "arguments": string(args), "status": "completed"}, nil
}

func openAIExcelIsTransport(name string) bool {
	return name == openAIExcelTransportTool || name == "functions."+openAIExcelTransportTool
}

// Decode only JSON, including fenced/assigned JSON occasionally produced by a
// model. This function never evaluates the surrounding text as code.
func openAIExcelDecodeEnvelope(value any) (map[string]any, error) {
	if object := openAIExcelMap(value); object != nil {
		return object, nil
	}
	text, ok := value.(string)
	if !ok || len(text) > openAIExcelMaxItemBytes {
		return nil, errors.New("invalid Excel tool transport envelope")
	}
	for _, candidate := range []string{text, openAIExcelRepairBackslashes(text)} {
		var decoded map[string]any
		if openAIExcelJSON([]byte(candidate), &decoded) == nil && decoded != nil {
			return decoded, nil
		}
		attempts := 0
		for offset, r := range candidate {
			if r != '{' {
				continue
			}
			attempts++
			if attempts > 64 {
				break
			}
			decoder := json.NewDecoder(strings.NewReader(candidate[offset:]))
			decoder.UseNumber()
			if decoder.Decode(&decoded) == nil && decoded != nil {
				return decoded, nil
			}
		}
	}
	return nil, errors.New("invalid Excel tool transport envelope")
}

func openAIExcelRepairBackslashes(text string) string {
	var out strings.Builder
	inString := false
	for i := 0; i < len(text); i++ {
		ch := text[i]
		if ch == '"' {
			inString = !inString
			_ = out.WriteByte(ch)
			continue
		}
		if ch != '\\' || !inString {
			_ = out.WriteByte(ch)
			continue
		}
		valid := i+1 < len(text) && strings.ContainsRune(`"\/bfnrt`, rune(text[i+1]))
		if i+5 < len(text) && text[i+1] == 'u' {
			_, err := strconv.ParseUint(text[i+2:i+6], 16, 16)
			valid = err == nil
		}
		_ = out.WriteByte('\\')
		if valid {
			i++
			_ = out.WriteByte(text[i])
		} else {
			_ = out.WriteByte('\\')
		}
	}
	return out.String()
}

func (s *OpenAIExcelWireState) translateNativeCall(ctx context.Context, native map[string]any) (map[string]any, error) {
	name := openAIExcelString(native["name"])
	transport := openAIExcelIsTransport(name)
	var envelope map[string]any
	if transport {
		var args map[string]any
		if !openAIExcelObjectValue(native["arguments"], &args) {
			return nil, errors.New("invalid Excel native tool arguments")
		}
		var err error
		envelope, err = openAIExcelDecodeEnvelope(args["code"])
		if err != nil {
			return nil, err
		}
		for depth := 0; openAIExcelIsTransport(openAIExcelString(envelope["name"])) && depth < 2; depth++ {
			arguments := openAIExcelMap(envelope["arguments"])
			if str, ok := envelope["arguments"].(string); ok {
				if openAIExcelJSON([]byte(str), &arguments) != nil {
					return nil, errors.New("invalid nested Excel tool envelope")
				}
			}
			envelope, err = openAIExcelDecodeEnvelope(arguments["code"])
			if err != nil {
				return nil, err
			}
		}
		name = openAIExcelString(envelope["name"])
	}
	name = strings.TrimPrefix(name, "codex_client__")
	tool, allowed := s.tools[name]
	if !allowed {
		return nil, fmt.Errorf("excel returned undeclared client tool %q", name)
	}
	callID := openAIExcelString(native["call_id"])
	if callID == "" {
		return nil, errors.New("excel native tool is missing call_id")
	}
	client := map[string]any{"type": tool.Kind + "_call", "name": tool.Name, "call_id": callID}
	if tool.Namespace != "" {
		client["namespace"] = tool.Namespace
	}
	id := openAIExcelString(native["id"])
	if id == "" {
		id = "fc_" + callID
	}
	client["id"] = id
	if tool.Kind == "custom" {
		client["type"] = "custom_tool_call"
		client["id"] = "ctc_" + callID
		value := native["input"]
		if transport {
			value = envelope["input"]
		}
		input, ok := value.(string)
		if !ok {
			return nil, errors.New("excel custom tool input is not text")
		}
		client["input"] = input
	} else {
		value := native["arguments"]
		if transport {
			value = envelope["arguments"]
		}
		args := openAIExcelMap(value)
		if encoded, ok := value.(string); ok {
			if !openAIExcelObjectValue(encoded, &args) {
				return nil, errors.New("excel function arguments are not valid JSON")
			}
		}
		if args == nil {
			return nil, errors.New("excel function arguments must be an object")
		}
		if !transport && name == "update_plan" {
			args = openAIExcelNormalizePlan(args)
		}
		schema := openAIExcelToolSchema(tool.Spec)
		if !openAIExcelMatchesSchema(args, schema, schema, 0) {
			return nil, fmt.Errorf("excel arguments do not match declared tool %q", name)
		}
		encoded, _ := json.Marshal(args)
		client["arguments"] = string(encoded)
	}
	if s.options.History == nil || s.options.HistoryScope == "" {
		return nil, ErrOpenAIExcelHistoryStorageUnavailable
	}
	raw, err := json.Marshal(native)
	if err != nil || len(raw) > openAIExcelMaxItemBytes {
		return nil, errors.New("excel native tool exceeds size limit")
	}
	if err = s.options.History.StoreExcelNativeCall(ctx, s.options.HistoryScope, callID, raw); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrOpenAIExcelHistoryStorageUnavailable
	}
	return client, nil
}

// Accept JSON-string and object forms for function arguments without accepting
// scalar values, which cannot be represented as a client argument map.
func openAIExcelObjectValue(value any, destination *map[string]any) bool {
	if destination == nil {
		return false
	}
	if object := openAIExcelMap(value); object != nil {
		*destination = object
		return true
	}
	text, ok := value.(string)
	if !ok || strings.TrimSpace(text) == "" {
		return false
	}
	var object map[string]any
	if openAIExcelJSON([]byte(text), &object) != nil || object == nil {
		return false
	}
	*destination = object
	return true
}

func openAIExcelNormalizePlan(arguments map[string]any) map[string]any {
	plan, ok := arguments["plan"].([]any)
	if !ok {
		return arguments
	}
	result := make([]any, 0, len(plan))
	for _, raw := range plan {
		item := openAIExcelMap(raw)
		step := openAIExcelString(item["step"])
		if step == "" {
			step = openAIExcelString(item["description"])
		}
		if step == "" {
			step = openAIExcelString(item["title"])
		}
		status := openAIExcelString(item["status"])
		switch strings.ToLower(strings.ReplaceAll(status, "-", "_")) {
		case "pending", "not_started", "todo":
			status = "pending"
		case "in_progress", "in progress", "running", "doing":
			status = "in_progress"
		case "completed", "complete", "done":
			status = "completed"
		}
		if step != "" && status != "" {
			result = append(result, map[string]any{"step": step, "status": status})
		}
	}
	out := map[string]any{"plan": result}
	explanation := openAIExcelString(arguments["explanation"])
	if explanation == "" {
		explanation = openAIExcelString(arguments["summary"])
	}
	if explanation != "" {
		out["explanation"] = explanation
	}
	return out
}

func openAIExcelRestorePlan(arguments map[string]any) map[string]any {
	plan, ok := arguments["plan"].([]any)
	if !ok {
		return arguments
	}
	result := make([]any, 0, len(plan))
	for i, raw := range plan {
		item := openAIExcelMap(raw)
		result = append(result, map[string]any{"id": fmt.Sprintf("step%d", i+1), "description": item["step"], "status": item["status"], "result": ""})
	}
	summary := openAIExcelString(arguments["explanation"])
	if summary == "" {
		summary = "Update task plan"
	}
	return map[string]any{"summary": summary, "plan": result}
}

// Validate the structural JSON-schema vocabulary used by client function tools.
// Unsupported annotations are left to the client; local refs remain bounded.
func openAIExcelMatchesSchema(value any, schema, root map[string]any, depth int) bool {
	if depth > 32 {
		return false
	}
	if len(schema) == 0 {
		return true
	}
	if ref := openAIExcelString(schema["$ref"]); ref != "" {
		if !strings.HasPrefix(ref, "#/") {
			return false
		}
		target := any(root)
		for _, part := range strings.Split(ref[2:], "/") {
			key := strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
			target = openAIExcelMap(target)[key]
		}
		resolved := openAIExcelMap(target)
		if resolved == nil || !openAIExcelMatchesSchema(value, resolved, root, depth+1) {
			return false
		}
	}
	if enum, ok := schema["enum"].([]any); ok {
		found := false
		for _, candidate := range enum {
			if reflect.DeepEqual(value, candidate) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if constant, ok := schema["const"]; ok && !reflect.DeepEqual(value, constant) {
		return false
	}
	for _, key := range []string{"anyOf", "oneOf", "allOf"} {
		if list, ok := schema[key].([]any); ok {
			matched := 0
			for _, candidate := range list {
				if openAIExcelMatchesSchema(value, openAIExcelMap(candidate), root, depth+1) {
					matched++
				}
			}
			if key == "anyOf" && matched == 0 || key == "oneOf" && matched != 1 || key == "allOf" && matched != len(list) {
				return false
			}
		}
	}
	if alternatives, ok := schema["type"].([]any); ok {
		for _, kind := range alternatives {
			copySchema := make(map[string]any, len(schema))
			for k, v := range schema {
				copySchema[k] = v
			}
			copySchema["type"] = kind
			if openAIExcelMatchesSchema(value, copySchema, root, depth+1) {
				return true
			}
		}
		return false
	}
	switch openAIExcelString(schema["type"]) {
	case "object":
		object, ok := value.(map[string]any)
		if !ok {
			return false
		}
		if required, ok := schema["required"].([]any); ok {
			for _, raw := range required {
				if key, ok := raw.(string); ok {
					if _, present := object[key]; !present {
						return false
					}
				}
			}
		}
		properties := openAIExcelMap(schema["properties"])
		for key, val := range object {
			if property, present := properties[key]; present {
				if !openAIExcelMatchesSchema(val, openAIExcelMap(property), root, depth+1) {
					return false
				}
			} else if schema["additionalProperties"] == false {
				return false
			} else if extra := openAIExcelMap(schema["additionalProperties"]); extra != nil && !openAIExcelMatchesSchema(val, extra, root, depth+1) {
				return false
			}
		}
	case "array":
		array, ok := value.([]any)
		if !ok {
			return false
		}
		for _, val := range array {
			if !openAIExcelMatchesSchema(val, openAIExcelMap(schema["items"]), root, depth+1) {
				return false
			}
		}
	case "string":
		if _, ok := value.(string); !ok {
			return false
		}
	case "number":
		if _, ok := value.(json.Number); !ok {
			return false
		}
	case "integer":
		number, ok := value.(json.Number)
		if !ok {
			return false
		}
		if _, err := number.Int64(); err != nil {
			return false
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return false
		}
	case "null":
		if value != nil {
			return false
		}
	}
	return true
}
