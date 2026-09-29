package service

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
)

const openAIExcelDiagnosticToolLimit = 32

// This wrapper retains only bounded, sanitized metadata. It must never retain
// the upstream item, arguments, schema, headers or response body.
type openAIExcelToolDiagnosticError struct {
	cause      error
	diagnostic map[string]any
}

func (e *openAIExcelToolDiagnosticError) Error() string { return "excel tool translation failed" }
func (e *openAIExcelToolDiagnosticError) Unwrap() error { return e.cause }

func openAIExcelToolDiagnostic(err error) map[string]any {
	var failure *openAIExcelToolDiagnosticError
	if errors.As(err, &failure) {
		return failure.diagnostic
	}
	return nil
}

// Tool identifiers are useful for resolving a mismatch, unlike argument or
// schema values. Reject malformed identifiers in their entirety, not by taking
// a prefix that might be a credential, URL or fragment of user content.
func openAIExcelDiagnosticIdentifier(value any) map[string]any {
	out := openAIExcelDiagnosticShape(value)
	text, ok := value.(string)
	if !ok {
		out["redacted"] = value != nil
		return out
	}
	if len(text) > 128 {
		out["redacted"] = true
		return out
	}
	allowed := true
	lower := strings.ToLower(text)
	if strings.HasPrefix(lower, "sk-") || strings.HasPrefix(lower, "sk_") || strings.HasPrefix(text, "eyJ") {
		allowed = false
	}
	for i, c := range text {
		letter := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_'
		continuation := i > 0 && (c >= '0' && c <= '9' || c == '.' || c == '-' || c == ':')
		if !letter && !continuation {
			allowed = false
			break
		}
	}
	out["redacted"] = !allowed
	if allowed {
		out["value"] = text
	}
	return out
}

func openAIExcelDiagnosticShape(value any) map[string]any {
	switch v := value.(type) {
	case nil:
		return map[string]any{"type": "null"}
	case string:
		return map[string]any{"type": "string", "bytes": len(v)}
	case map[string]any:
		return map[string]any{"type": "object", "count": len(v)}
	case []any:
		return map[string]any{"type": "array", "count": len(v)}
	case bool:
		return map[string]any{"type": "boolean"}
	case json.Number, float64, int, int64:
		return map[string]any{"type": "number"}
	default:
		// Parsed JSON numbers are deliberately not formatted into a log value.
		return map[string]any{"type": "scalar"}
	}
}

func (s *OpenAIExcelWireState) toolFailureDiagnostic(stage string, native, candidate map[string]any, transport bool, code any, depth int) map[string]any {
	keys := make([]string, 0, len(s.tools))
	for key := range s.tools {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	total := len(keys)
	if len(keys) > openAIExcelDiagnosticToolLimit {
		keys = keys[:openAIExcelDiagnosticToolLimit]
	}
	catalog := make([]any, 0, len(keys))
	for _, key := range keys {
		tool := s.tools[key]
		catalog = append(catalog, map[string]any{
			"name": openAIExcelDiagnosticIdentifier(tool.Name), "namespace": openAIExcelDiagnosticIdentifier(tool.Namespace), "kind": tool.Kind,
		})
	}
	diagnostic := map[string]any{
		"version": 1, "stage": stage, "transport": transport, "relay_depth": depth,
		"native_name": openAIExcelDiagnosticIdentifier(native["name"]), "native_namespace": openAIExcelDiagnosticIdentifier(native["namespace"]),
		"native_type": openAIExcelDiagnosticIdentifier(native["type"]), "call_id_present": openAIExcelString(native["call_id"]) != "",
		"arguments": openAIExcelDiagnosticShape(native["arguments"]), "input": openAIExcelDiagnosticShape(native["input"]),
		"catalog": catalog, "catalog_total": total, "catalog_truncated": total > len(keys),
		"parallel": s.parallel, "tool_required": s.required,
	}
	if transport {
		diagnostic["code"] = openAIExcelDiagnosticShape(code)
	}
	if candidate != nil {
		diagnostic["candidate_name"] = openAIExcelDiagnosticIdentifier(candidate["name"])
		diagnostic["candidate_namespace"] = openAIExcelDiagnosticIdentifier(candidate["namespace"])
		diagnostic["candidate_arguments"] = openAIExcelDiagnosticShape(candidate["arguments"])
		diagnostic["candidate_input"] = openAIExcelDiagnosticShape(candidate["input"])
		name, nameOK := candidate["name"].(string)
		namespace, namespaceOK := candidate["namespace"].(string)
		if nameOK && (namespaceOK || candidate["namespace"] == nil) {
			tool, matched := s.resolveClientTool(name, namespace)
			diagnostic["name_matches_declared"] = matched
			if openAIExcelIsDefaultNamespace(namespace) {
				_, count := s.defaultNamespaceTool(name)
				diagnostic["default_namespace_candidates"] = count
			}
			if matched {
				diagnostic["matched_name"] = openAIExcelDiagnosticIdentifier(tool.Name)
				diagnostic["matched_namespace"] = openAIExcelDiagnosticIdentifier(tool.Namespace)
				diagnostic["matched_kind"] = tool.Kind
			}
		}
	}
	return diagnostic
}
