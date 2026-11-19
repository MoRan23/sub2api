package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// OpenAIEffortValue preserves the provider's string/uint64 union. Value is a
// decimal integer for number values; it never passes through float64.
type OpenAIEffortValue struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

func (v OpenAIEffortValue) valid() bool {
	if v.Type == "string" {
		return v.Value != "" && len(v.Value) <= maxReasoningEffortValueLen
	}
	if v.Type != "number" {
		return false
	}
	_, err := strconv.ParseUint(v.Value, 10, 64)
	return err == nil && v.Value != "" && !strings.ContainsAny(v.Value, "+-.eE")
}

func effortScalar(value gjson.Result) (OpenAIEffortValue, bool) {
	switch value.Type {
	case gjson.String:
		if value.Str != "" && len(value.Str) <= maxReasoningEffortValueLen {
			return OpenAIEffortValue{Type: "string", Value: strings.Clone(value.Str)}, true
		}
	case gjson.Number:
		if len(value.Raw) > 20 {
			return OpenAIEffortValue{}, false
		}
		v := OpenAIEffortValue{Type: "number", Value: value.Raw}
		if v.valid() {
			v.Value = strings.Clone(value.Raw)
			return v, true
		}
	}
	return OpenAIEffortValue{}, false
}

func effortScalarAny(value any) (OpenAIEffortValue, bool) {
	switch v := value.(type) {
	case string:
		if v == "" || len(v) > maxReasoningEffortValueLen {
			return OpenAIEffortValue{}, false
		}
		return OpenAIEffortValue{Type: "string", Value: strings.Clone(v)}, true
	case json.Number:
		e := OpenAIEffortValue{Type: "number", Value: string(v)}
		return e, e.valid()
	case uint64:
		return OpenAIEffortValue{Type: "number", Value: strconv.FormatUint(v, 10)}, true
	case float64:
		// Legacy map callers use json.Unmarshal. Only exactly representable
		// integers can be recovered; raw JSON paths retain the full uint64 range.
		if v >= 0 && v <= 9007199254740991 && v == float64(uint64(v)) {
			return OpenAIEffortValue{Type: "number", Value: strconv.FormatUint(uint64(v), 10)}, true
		}
	}
	return OpenAIEffortValue{}, false
}

type openAIEffortSelection struct {
	value     OpenAIEffortValue
	path      string
	override  bool
	unknown   bool
	oversized bool
}
type openAIEffortContextKey struct{}
type openAIEffortRequestState struct {
	previousID    string
	originalModel string
	inherited     OpenAIResponseEffortState
	found         bool
	requested     OpenAIEffortValue
	effective     OpenAIEffortValue
	override      bool
	selection     openAIEffortSelection
	prepared      bool
}

// selectOpenAIEffort scans input once without materializing its JSON tree.
// Shadowed historical updates are deliberately ignored, including invalid ones.
func selectOpenAIEffort(body []byte, state *openAIEffortRequestState) openAIEffortSelection {
	selection := openAIEffortSelection{}
	index := 0
	view := parseRawJSONView(body)
	// Most requests have no configuration items. A vectorized byte search avoids
	// walking large message strings twice. Unicode escapes may spell the item
	// type differently, so their presence always takes the structural path.
	if bytes.Contains(body, []byte("configuration_update")) || bytes.Contains(body, []byte(`\u`)) {
		view.Get("input").ForEach(func(_, item gjson.Result) bool {
			if item.Get("type").Str == "configuration_update" {
				if field := item.Get("reasoning.effort"); field.Exists() {
					value, _ := effortScalar(field)
					selection = openAIEffortSelection{value: value, path: "input." + strconv.Itoa(index) + ".reasoning.effort", override: true, unknown: !value.valid(), oversized: field.Type == gjson.String && len(field.Str) > maxReasoningEffortValueLen}
				}
			}
			index++
			return true
		})
	}
	if selection.path != "" {
		return selection
	}
	previous := strings.TrimSpace(view.Get("previous_response_id").Str)
	if previous != "" {
		if state == nil || state.previousID != previous || !state.found {
			return openAIEffortSelection{unknown: true}
		}
		if state.inherited.Override {
			return openAIEffortSelection{value: state.inherited.Effort, override: true, unknown: !state.inherited.Effort.valid()}
		}
	}
	for _, path := range []string{"reasoning.effort", "reasoning_effort", "output_config.effort"} {
		field := view.Get(path)
		if field.Exists() {
			value, ok := effortScalar(field)
			return openAIEffortSelection{value: value, path: path, unknown: !ok, oversized: field.Type == gjson.String && len(field.Str) > maxReasoningEffortValueLen}
		}
	}
	return selection
}

func currentOpenAIEffortState(ctx context.Context) *openAIEffortRequestState {
	if ctx == nil {
		return nil
	}
	value, _ := ctx.Value(openAIEffortContextKey{}).(*openAIEffortRequestState)
	return value
}

// EffectiveOpenAIReasoningEffort returns the effective final scalar captured by
// policy resolution, including opaque provider values. Unknown stays nil.
func EffectiveOpenAIReasoningEffort(ctx context.Context) *string {
	state := currentOpenAIEffortState(ctx)
	if state == nil || !state.effective.valid() {
		return nil
	}
	value := state.effective.Value
	return &value
}

func effectiveOpenAIEffortForBody(ctx context.Context, body []byte, models ...string) *string {
	if state := currentOpenAIEffortState(ctx); state != nil {
		value := state.effective
		if len(body) > 0 {
			selected := selectOpenAIEffort(body, state)
			value = selected.value
		}
		if !value.valid() {
			if state.selection.unknown {
				return nil
			}
			return extractOpenAIReasoningEffortFromBody(nil, append(models, state.originalModel)...)
		}
		text := value.Value
		if value.Type == "string" {
			if normalized := normalizeOpenAIReasoningEffortForModel(text, firstNonEmpty(models...)); normalized != "" {
				text = normalized
			}
		}
		return &text
	}
	return extractOpenAIReasoningEffortFromBody(body, models...)
}

func EffectiveOpenAIReasoningEffortForBody(ctx context.Context, body []byte, models ...string) *string {
	return effectiveOpenAIEffortForBody(ctx, body, models...)
}

func OpenAIReasoningEffortContextResolved(ctx context.Context) bool {
	return currentOpenAIEffortState(ctx) != nil
}

func freezeOpenAIForwardRequestedReasoningEffort(result *OpenAIForwardResult, ctx context.Context) {
	if result == nil || !OpenAIReasoningEffortContextResolved(ctx) {
		return
	}
	result.RequestedReasoningEffort = RequestedReasoningEffortFromContext(ctx)
	result.RequestedReasoningEffortResolved = true
}

// CaptureFinalOpenAIReasoningEffort captures only the final small scalar after
// compatibility rewrites, before forwarding publishes the turn to observers.
func CaptureFinalOpenAIReasoningEffort(ctx context.Context, body []byte) {
	state := currentOpenAIEffortState(ctx)
	if state == nil || len(body) == 0 {
		return
	}
	selected := selectOpenAIEffort(body, state)
	state.effective, state.override = selected.value, selected.override
}

func updateEffortSelection(body []byte, selected openAIEffortSelection, value string) ([]byte, error) {
	if selected.path != "" {
		return sjson.SetBytes(body, selected.path, value)
	}
	update := map[string]any{"type": "configuration_update", "reasoning": map[string]any{"effort": value}}
	input := parseRawJSONView(body).Get("input")
	if input.Type == gjson.String {
		return sjson.SetBytes(body, "input", []any{map[string]any{"role": "user", "content": input.Str}, update})
	}
	if !input.Exists() || input.Type == gjson.Null {
		return sjson.SetBytes(body, "input", []any{update})
	}
	if !input.IsArray() {
		return body, &ReasoningEffortContinuationError{Reason: "input must be a string or array to apply an inherited effort override"}
	}
	return sjson.SetBytes(body, "input.-1", update)
}

// ReasoningEffortContinuationError prevents an active cap from being bypassed
// through a missing continuation record or an unordered custom provider value.
type ReasoningEffortContinuationError struct{ Reason string }

func (e *ReasoningEffortContinuationError) Error() string {
	return "reasoning effort policy: " + e.Reason
}

func applyEffectiveOpenAIEffortPolicy(ctx context.Context, body []byte, maxEffort string, mappings []ReasoningEffortMapping, overLimit string) ([]byte, bool, error) {
	state := currentOpenAIEffortState(ctx)
	var selected openAIEffortSelection
	if state != nil && state.prepared {
		selected = state.selection
	} else {
		selected = selectOpenAIEffort(body, state)
	}
	maxRank, hasMax := reasoningEffortRank(maxEffort)
	if selected.oversized {
		return body, false, &ReasoningEffortContinuationError{Reason: "effort string exceeds the 64-byte limit"}
	}
	if selected.unknown && hasMax {
		return body, false, &ReasoningEffortContinuationError{Reason: "effective effort is unavailable; resend a complete request or an explicit configuration_update"}
	}
	if state != nil {
		state.requested = selected.value
		state.effective = selected.value
		state.override = selected.override
	}
	if !selected.value.valid() {
		return body, false, nil
	}
	if !hasMax && len(mappings) == 0 {
		return body, false, nil
	}
	original := selected.value
	effective := original.Value
	mapping, mapped := selectTypedReasoningEffortMapping(mappings, original, strings.TrimSpace(gjson.GetBytes(body, "model").Str))
	if mapped {
		effective = mapping.To
	}
	if mapped && isReasoningEffortMappingDeny(effective) {
		requested := original.Value
		if original.Type == "string" {
			if known := normalizeReasoningEffortMappingSource(requested); known != "" {
				requested = known
			}
		}
		return body, false, &ReasoningEffortMappingDeniedError{Requested: requested}
	}
	rank, recognized := reasoningEffortRank(effective)
	opaque := original.Type == "number" || (normalizeReasoningEffortMappingSource(original.Value) == "" && original.Value != "none")
	if hasMax && opaque && (!mapped || strings.TrimSpace(mapping.Model) == "") {
		return body, false, &ReasoningEffortContinuationError{Reason: fmt.Sprintf("effort %q requires an explicit model-scoped mapping under this group's limit", original.Value)}
	}
	if recognized {
		effective = NormalizeMaxReasoningEffort(effective)
		if hasMax && rank > maxRank {
			if NormalizeMaxReasoningEffortOverLimit(overLimit) == ReasoningEffortOverLimitDeny {
				return body, false, &ReasoningEffortOverLimitError{Requested: effective, Max: NormalizeMaxReasoningEffort(maxEffort)}
			}
			effective = NormalizeMaxReasoningEffort(maxEffort)
		}
	}
	changed := effective != original.Value || (mapped && original.Type == "number")
	if state != nil && changed {
		state.effective = OpenAIEffortValue{Type: "string", Value: effective}
	}
	if !changed {
		return body, false, nil
	}
	updated, err := updateEffortSelection(body, selected, effective)
	return updated, err == nil, err
}

func explicitOpenAIEffortFromMap(body map[string]any) (OpenAIEffortValue, bool) {
	var selected OpenAIEffortValue
	present := false
	if input, ok := body["input"].([]any); ok {
		for _, v := range input {
			if item, ok := v.(map[string]any); ok && item["type"] == "configuration_update" {
				if reasoning, ok := item["reasoning"].(map[string]any); ok {
					if field, exists := reasoning["effort"]; exists {
						selected, _ = effortScalarAny(field)
						present = true
					}
				}
			}
		}
	}
	if present {
		return selected, true
	}
	if previous, ok := body["previous_response_id"].(string); ok && strings.TrimSpace(previous) != "" {
		return OpenAIEffortValue{}, true
	}
	for _, key := range []string{"reasoning", "reasoning_effort", "output_config"} {
		if key == "reasoning_effort" {
			if field, exists := body[key]; exists {
				value, _ := effortScalarAny(field)
				return value, true
			}
			continue
		}
		if object, ok := body[key].(map[string]any); ok {
			if field, exists := object["effort"]; exists {
				value, _ := effortScalarAny(field)
				return value, true
			}
		}
	}
	return OpenAIEffortValue{}, false
}
