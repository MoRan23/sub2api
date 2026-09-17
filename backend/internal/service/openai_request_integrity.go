package service

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	requestIntegrityMaxBytes       = 8 << 20
	requestIntegrityMaxDepth       = 128
	requestIntegrityMaxNodes       = 16384
	requestIntegrityMaxDifferences = 32
)

// RequestIntegrityObservation contains diagnostics only. Neither request values
// nor user-supplied object keys, identifiers, credentials or hashes belong here.
type RequestIntegrityObservation struct {
	Mode             string   `json:"mode"`
	Status           string   `json:"status"`
	BaselineProtocol string   `json:"baseline_protocol"`
	BaselineStage    string   `json:"baseline_stage"`
	Attempt          int64    `json:"attempt"`
	Transport        string   `json:"transport"`
	ChangedFields    []string `json:"changed_fields,omitempty"`
	RuleCodes        []string `json:"rule_codes,omitempty"`
	Reason           string   `json:"reason,omitempty"`
	Truncated        bool     `json:"truncated,omitempty"`
}

func CloneRequestIntegrityObservation(value *RequestIntegrityObservation) *RequestIntegrityObservation {
	if value == nil {
		return nil
	}
	copy := *value
	copy.ChangedFields = append([]string(nil), value.ChangedFields...)
	copy.RuleCodes = append([]string(nil), value.RuleCodes...)
	return &copy
}

// RequestIntegrityCheckOptions supplies independently established adaptation
// evidence. ExpectedModel must come from the routing decision, never the wire.
// KnownRecovery annotates differences and never exempts them from comparison.
type RequestIntegrityCheckOptions struct {
	Transport     string
	ExpectedModel string
	TimezoneState *RequestTimezoneState
	KnownRecovery string
	// CompatTodoGuard is set only when this physical Messages adaptation
	// actually inserted the exact built-in todo guard into the outgoing body.
	CompatTodoGuard bool
}

// OpenAIRequestIntegrityState belongs to one accepted request or WS turn. The
// captured baseline and switch are immutable; only physical attempts advance.
type OpenAIRequestIntegrityState struct {
	enabled  bool
	protocol string
	stage    string
	baseline []byte
	parsed   map[string]any
	reason   string
	attempt  atomic.Int64
}

func NewOpenAIRequestIntegrityState(enabled bool, protocol string, baseline []byte) *OpenAIRequestIntegrityState {
	s := &OpenAIRequestIntegrityState{enabled: enabled, protocol: "responses", stage: "ingress"}
	switch protocol {
	case "messages", "chat_completions":
		s.protocol, s.stage = protocol, "responses_adapter_output"
	case "responses", "":
	default:
		s.protocol = "unknown"
	}
	if !enabled {
		return s
	}
	if len(baseline) == 0 {
		s.reason = "missing_baseline"
		return s
	}
	s.parsed, s.reason = decodeRequestIntegrityBody(baseline)
	if s.reason == "" {
		s.baseline = bytes.Clone(baseline)
	} else {
		s.reason = "baseline_" + s.reason
	}
	return s
}

// Check cannot reject, mutate or retry inference. Invalid/oversized bodies are
// explicitly skipped rather than reported as matching. It is safe for attempts
// on different accounts to inspect the same immutable baseline concurrently.
func (s *OpenAIRequestIntegrityState) Check(account *Account, wire []byte, opts RequestIntegrityCheckOptions) (result *RequestIntegrityObservation) {
	if s == nil || !s.enabled {
		return nil
	}
	result = &RequestIntegrityObservation{
		Mode: "observe", Status: "skipped", BaselineProtocol: s.protocol,
		BaselineStage: s.stage, Attempt: s.attempt.Add(1), Transport: requestIntegrityTransport(opts.Transport),
	}
	defer func() {
		if recover() != nil {
			// Diagnostics must never fail the primary request. Do not print the
			// panic value: a dependency could include request content in it.
			result.Status, result.Reason = "skipped", "checker_failure"
			result.ChangedFields, result.RuleCodes = nil, nil
			result.Truncated = false
		}
		logRequestIntegrityObservation(account, result)
	}()
	if s.reason != "" {
		result.Reason = s.reason
		return result
	}
	after, reason := decodeRequestIntegrityBody(wire)
	if reason != "" {
		result.Reason = "outbound_" + reason
		return result
	}
	before := cloneRequestIntegrityValue(s.parsed).(map[string]any)
	if requestIntegrityFieldsEqual(before, after) {
		result.Status = "unchanged"
		return result
	}
	rules := make(map[string]bool)
	if opts.TimezoneState != nil {
		prepared, ok := opts.TimezoneState.ApplyToBody(s.baseline)
		if ok && !bytes.Equal(prepared, s.baseline) {
			if adjusted, failure := decodeRequestIntegrityBody(prepared); failure == "" {
				before = adjusted
				rules["frozen_timezone_patch"] = true
			}
		}
	}
	if originalModel, ok := before["model"].(string); ok {
		expected := strings.TrimSpace(opts.ExpectedModel)
		if expected == "" && account != nil {
			expected = requestIntegrityMappedModel(account, originalModel)
		}
		if expected != "" && expected != originalModel {
			before["model"] = expected
			rules["account_model_mapping"] = true
		}
	}
	if account != nil && account.UsesOpenAICodexProtocol() {
		canonicalizeRequestIntegrityCodex(before, rules)
		canonicalizeRequestIntegrityCodex(after, rules)
		if s.protocol == "messages" && opts.CompatTodoGuard && appendOpenAICompatClaudeCodeTodoGuardToRequestBody(before) {
			rules["compat_todo_guard"] = true
		}
		for _, field := range []string{"max_output_tokens", "max_completion_tokens"} {
			if _, present := before[field]; present {
				if _, present = after[field]; !present {
					delete(before, field)
					rules["codex_unsupported_token_budget"] = true
				}
			}
		}
		// Only the exact embedded default for the independently resolved model
		// may be added to empty instructions or a promoted system prefix.
		rawInstructions, instructionsExist := s.parsed["instructions"]
		instructionText, instructionIsText := rawInstructions.(string)
		if !instructionsExist || rawInstructions == nil || (instructionIsText && strings.TrimSpace(instructionText) == "") {
			model, _ := before["model"].(string)
			if model != "" {
				expected := defaultCodexSynthInstructions(model)
				prefix, _ := before["instructions"].(string)
				if prefix != "" {
					expected = prefix + "\n\n" + expected
				}
				if actual, ok := after["instructions"].(string); ok && actual == expected {
					before["instructions"] = expected
					rules["codex_default_instructions"] = true
				}
			}
		}
	}
	collector := requestIntegrityDifferenceCollector{}
	for _, field := range requestIntegrityContentFields {
		left, leftOK := before[field]
		right, rightOK := after[field]
		collector.compare(field, left, right, leftOK, rightOK)
	}
	if len(collector.fields) == 0 {
		result.Status = "expected_transform"
	} else {
		result.Status = "difference"
		result.ChangedFields, result.Truncated = collector.fields, collector.truncated
		result.Reason = "content_changed"
		if opts.KnownRecovery != "" {
			rules[requestIntegrityRecoveryRule(opts.KnownRecovery)] = true
		}
		if requestIntegrityEncryptedReasoningRemoved(s.parsed, after) {
			rules["encrypted_reasoning_removed"] = true
		}
	}
	for rule := range rules {
		result.RuleCodes = append(result.RuleCodes, rule)
	}
	sort.Strings(result.RuleCodes)
	return result
}

var requestIntegrityContentFields = []string{
	"model", "input", "instructions", "reasoning", "tools", "tool_choice", "parallel_tool_calls",
	"text", "previous_response_id", "max_output_tokens", "max_completion_tokens", "max_tokens", "functions", "function_call",
}

func requestIntegrityMappedModel(account *Account, requested string) string {
	// GetMappedModel populates mutable per-Account caches. An observer must not
	// change shared routing state or introduce cache races on concurrent turns.
	raw, _ := account.Credentials["model_mapping"].(map[string]any)
	mapping := account.resolveModelMapping(raw)
	mapped := requested
	if candidate, ok := resolveRequestedModelInMapping(mapping, requested); ok {
		mapped = candidate
	} else if normalized := normalizeRequestedModelForLookup(account.Platform, requested); normalized != requested {
		if candidate, ok := resolveRequestedModelInMapping(mapping, normalized); ok {
			mapped = candidate
		}
	}
	return normalizeOpenAIModelForUpstream(account, mapped)
}

func requestIntegrityFieldsEqual(before, after map[string]any) bool {
	for _, key := range requestIntegrityContentFields {
		left, lok := before[key]
		right, rok := after[key]
		if lok != rok || !reflect.DeepEqual(left, right) {
			return false
		}
	}
	return true
}

func requestIntegrityTransport(transport string) string {
	switch transport {
	case "http", "ws", "http_bridge", "http_to_ws":
		return transport
	default:
		return "unknown"
	}
}

func requestIntegrityRecoveryRule(recovery string) string {
	switch recovery {
	case "invalid_encrypted_content", "encrypted_reasoning_removed":
		return "encrypted_reasoning_removed"
	case "previous_response_not_found", "previous_response_id_removed", "history_replay":
		return "continuation_recovery"
	default:
		return "known_recovery"
	}
}

// Decode with a token budget before allocating a full arbitrary-depth tree.
// Duplicate keys are ambiguous even if encoding/json would accept them.
func decodeRequestIntegrityBody(raw []byte) (map[string]any, string) {
	if len(raw) > requestIntegrityMaxBytes {
		return nil, "body_too_large"
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	nodes := 0
	value, reason := readRequestIntegrityJSON(decoder, 0, &nodes)
	if reason != "" {
		return nil, reason
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, "invalid_json"
	}
	body, ok := value.(map[string]any)
	if !ok || body == nil {
		return nil, "invalid_json"
	}
	return body, ""
}

func readRequestIntegrityJSON(decoder *json.Decoder, depth int, nodes *int) (any, string) {
	if depth > requestIntegrityMaxDepth {
		return nil, "depth_limit"
	}
	*nodes++
	if *nodes > requestIntegrityMaxNodes {
		return nil, "node_limit"
	}
	token, err := decoder.Token()
	if err != nil {
		return nil, "invalid_json"
	}
	switch token {
	case json.Delim('{'):
		object := make(map[string]any)
		for decoder.More() {
			keyToken, err := decoder.Token()
			key, ok := keyToken.(string)
			if err != nil || !ok {
				return nil, "invalid_json"
			}
			if _, duplicate := object[key]; duplicate {
				return nil, "duplicate_key"
			}
			*nodes++
			value, reason := readRequestIntegrityJSON(decoder, depth+1, nodes)
			if reason != "" {
				return nil, reason
			}
			object[key] = value
		}
		if end, err := decoder.Token(); err != nil || end != json.Delim('}') {
			return nil, "invalid_json"
		}
		return object, ""
	case json.Delim('['):
		array := make([]any, 0)
		for decoder.More() {
			value, reason := readRequestIntegrityJSON(decoder, depth+1, nodes)
			if reason != "" {
				return nil, reason
			}
			array = append(array, value)
		}
		if end, err := decoder.Token(); err != nil || end != json.Delim(']') {
			return nil, "invalid_json"
		}
		return array, ""
	default:
		if _, delimiter := token.(json.Delim); delimiter {
			return nil, "invalid_json"
		}
		return token, ""
	}
}

func cloneRequestIntegrityValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		copy := make(map[string]any, len(typed))
		for key, value := range typed {
			copy[key] = cloneRequestIntegrityValue(value)
		}
		return copy
	case []any:
		copy := make([]any, len(typed))
		for i, value := range typed {
			copy[i] = cloneRequestIntegrityValue(value)
		}
		return copy
	default:
		return value
	}
}

type requestIntegrityDifferenceCollector struct {
	fields    []string
	truncated bool
}

func (c *requestIntegrityDifferenceCollector) add(path string) {
	for _, existing := range c.fields {
		if existing == path {
			return
		}
	}
	if len(c.fields) >= requestIntegrityMaxDifferences {
		c.truncated = true
		return
	}
	c.fields = append(c.fields, path)
}

func (c *requestIntegrityDifferenceCollector) compare(path string, before, after any, beforePresent, afterPresent bool) {
	if c.truncated || (beforePresent == afterPresent && reflect.DeepEqual(before, after)) {
		return
	}
	if beforePresent != afterPresent {
		c.add(path)
		return
	}
	if left, ok := before.([]any); ok {
		if right, ok := after.([]any); ok {
			length := max(len(left), len(right))
			for i := 0; i < length && !c.truncated; i++ {
				var l, r any
				if i < len(left) {
					l = left[i]
				}
				if i < len(right) {
					r = right[i]
				}
				c.compare(path+"["+strconv.Itoa(i)+"]", l, r, i < len(left), i < len(right))
			}
			return
		}
	}
	if left, ok := before.(map[string]any); ok {
		if right, ok := after.(map[string]any); ok {
			keys := make(map[string]bool, len(left)+len(right))
			for key := range left {
				keys[key] = true
			}
			for key := range right {
				keys[key] = true
			}
			ordered := make([]string, 0, len(keys))
			for key := range keys {
				ordered = append(ordered, key)
			}
			sort.Strings(ordered)
			for _, key := range ordered {
				l, lok := left[key]
				r, rok := right[key]
				if !requestIntegritySafePathKey(key) {
					if lok != rok || !reflect.DeepEqual(l, r) {
						c.add(path + ".*")
					}
					continue
				}
				c.compare(path+"."+key, l, r, lok, rok)
			}
			return
		}
	}
	c.add(path)
}

func requestIntegritySafePathKey(key string) bool {
	switch key {
	case "type", "role", "content", "text", "image_url", "file_id", "file_url", "detail", "audio", "data", "format",
		"effort", "mode", "summary", "encrypted_content", "name", "description", "parameters", "strict", "function", "tools",
		"input", "output", "arguments", "call_id", "id", "properties", "required", "items", "additionalProperties",
		"enum", "anyOf", "oneOf", "allOf", "schema", "json_schema", "verbosity", "user_location", "timezone", "city",
		"country", "region", "search_context_size", "status":
		return true
	default:
		return false
	}
}

func requestIntegrityEncryptedReasoningRemoved(before, after map[string]any) bool {
	count := func(body map[string]any) int {
		input, _ := body["input"].([]any)
		total := 0
		for _, raw := range input {
			item, _ := raw.(map[string]any)
			if item["type"] == "reasoning" && item["encrypted_content"] != nil {
				total++
			}
		}
		return total
	}
	return count(before) > count(after)
}

type requestIntegrityLogKey struct {
	accountID int64
	reason    string
}
type requestIntegrityLogEntry struct {
	emitted, touched time.Time
	suppressed       uint64
}

var requestIntegrityLogState = struct {
	sync.Mutex
	entries map[requestIntegrityLogKey]requestIntegrityLogEntry
}{entries: make(map[requestIntegrityLogKey]requestIntegrityLogEntry)}

func logRequestIntegrityObservation(account *Account, result *RequestIntegrityObservation) {
	if result == nil || (result.Status != "difference" && result.Status != "skipped") {
		return
	}
	key := requestIntegrityLogKey{reason: result.Reason}
	if account != nil {
		key.accountID = account.ID
	}
	now := time.Now()
	requestIntegrityLogState.Lock()
	entry, exists := requestIntegrityLogState.entries[key]
	if exists && now.Sub(entry.emitted) < time.Minute {
		entry.suppressed++
		entry.touched = now
		requestIntegrityLogState.entries[key] = entry
		requestIntegrityLogState.Unlock()
		return
	}
	if !exists && len(requestIntegrityLogState.entries) >= 4096 {
		var oldestKey requestIntegrityLogKey
		var oldest time.Time
		for candidate, value := range requestIntegrityLogState.entries {
			if now.Sub(value.touched) > 10*time.Minute {
				delete(requestIntegrityLogState.entries, candidate)
				continue
			}
			if oldest.IsZero() || value.touched.Before(oldest) {
				oldestKey, oldest = candidate, value.touched
			}
		}
		if len(requestIntegrityLogState.entries) >= 4096 {
			delete(requestIntegrityLogState.entries, oldestKey)
		}
	}
	requestIntegrityLogState.entries[key] = requestIntegrityLogEntry{emitted: now, touched: now}
	requestIntegrityLogState.Unlock()
	slog.Warn("openai.request_integrity", "account_id", key.accountID, "status", result.Status,
		"reason", result.Reason, "fields", result.ChangedFields, "rules", result.RuleCodes,
		"attempt", result.Attempt, "transport", result.Transport, "suppressed", entry.suppressed)
}
