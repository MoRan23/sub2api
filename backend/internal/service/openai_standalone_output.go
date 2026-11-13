package service

import (
	"strings"
	"unsafe"

	"github.com/tidwall/gjson"
)

// Codex can supply named context (for example agent.proposed_plan) as a
// function_call_output without a call_id. It is not a result of a tool call.
// Keep this narrower than the general output-type predicate: malformed call
// IDs and other output types must still take the ordinary continuation checks.
func isCodexStandaloneFunctionOutput(item map[string]any) bool {
	typ, _ := item["type"].(string)
	if typ != "function_call_output" || item["call_id"] != nil {
		return false
	}
	name, _ := item["name"].(string)
	_, namespaceString := item["namespace"].(string)
	outputValid := false
	switch value := item["output"].(type) {
	case string:
		outputValid = true
	case []any:
		outputValid = value != nil
	}
	return codexStandaloneOutputFieldsValid(name, item["namespace"] == nil || namespaceString, outputValid)
}

func isCodexStandaloneFunctionOutputRaw(item gjson.Result) bool {
	if item.Get("type").Str != "function_call_output" || item.Get("call_id").Type != gjson.Null {
		return false
	}
	name := item.Get("name")
	namespace := item.Get("namespace")
	return codexStandaloneOutputFieldsValid(name.Str,
		namespace.Type == gjson.Null || namespace.Type == gjson.String,
		codexStandaloneOutputPayloadShape(item.Raw))
}

// The body already passes the request JSON validator. Only inspect the output
// token here: Get("output") would unescape a potentially multi-MiB string merely
// to learn its type. This synchronous read-only view never escapes the call.
func codexStandaloneOutputPayloadShape(object string) bool {
	raw := unsafe.Slice(unsafe.StringData(object), len(object))
	i := skipOpenAIWSJSONSpace(raw, 0)
	if i >= len(raw) || raw[i] != '{' {
		return false
	}
	for i++; i < len(raw); {
		i = skipOpenAIWSJSONSpace(raw, i)
		if i >= len(raw) || raw[i] != '"' {
			return false
		}
		keyStart := i
		i = scanOpenAIWSJSONString(raw, i)
		key := object[keyStart:i]
		i = skipOpenAIWSJSONSpace(raw, i)
		if i >= len(raw) || raw[i] != ':' {
			return false
		}
		i = skipOpenAIWSJSONSpace(raw, i+1)
		if i >= len(raw) {
			return false
		}
		// Six Unicode escapes are the longest spelling of "output".
		if key == `"output"` || (len(key) <= 38 && strings.Contains(key, `\`) && gjson.Parse(key).Str == "output") {
			return raw[i] == '"' || raw[i] == '['
		}
		i = skipOpenAIWSJSONValue(raw, i)
		i = skipOpenAIWSJSONSpace(raw, i)
		if i >= len(raw) || raw[i] != ',' {
			return false
		}
		i++
	}
	return false
}

func codexStandaloneOutputFieldsValid(name string, namespaceValid, outputValid bool) bool {
	return strings.TrimSpace(name) != "" && namespaceValid && outputValid
}

func codexToolOutputRequiresCallContext(item map[string]any) bool {
	typ, _ := item["type"].(string)
	return isCodexToolCallOutputItemType(typ) && !isCodexStandaloneFunctionOutput(item)
}

func codexToolOutputRequiresCallContextRaw(item gjson.Result) bool {
	return isCodexToolCallOutputItemType(item.Get("type").Str) && !isCodexStandaloneFunctionOutputRaw(item)
}
