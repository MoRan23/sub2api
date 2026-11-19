package apicompat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/tidwall/gjson"
)

func parseWireReasoningEffort(raw json.RawMessage) (string, bool, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return "", false, nil
	}
	if raw[0] == '"' {
		var value string
		err := json.Unmarshal(raw, &value)
		return value, false, err
	}
	if _, err := strconv.ParseUint(string(raw), 10, 64); err != nil || !json.Valid(raw) {
		return "", false, fmt.Errorf("reasoning effort must be a string or uint64")
	}
	return string(raw), true, nil
}

func marshalWireReasoningEffort(value string, number bool) (json.RawMessage, error) {
	if !number {
		return json.Marshal(value)
	}
	if _, err := strconv.ParseUint(value, 10, 64); err != nil || !json.Valid([]byte(value)) {
		return nil, fmt.Errorf("numeric reasoning effort must be a uint64")
	}
	return json.RawMessage(value), nil
}

func (r *ResponsesReasoning) UnmarshalJSON(data []byte) error {
	var wire struct {
		Effort  json.RawMessage `json:"effort"`
		Summary string          `json:"summary"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	effort, numeric, err := parseWireReasoningEffort(wire.Effort)
	if err != nil {
		return err
	}
	*r = ResponsesReasoning{Effort: effort, EffortIsNumber: numeric, Summary: wire.Summary}
	return nil
}

func (r ResponsesReasoning) MarshalJSON() ([]byte, error) {
	effort, err := marshalWireReasoningEffort(r.Effort, r.EffortIsNumber)
	if err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Effort  json.RawMessage `json:"effort"`
		Summary string          `json:"summary,omitempty"`
	}{effort, r.Summary})
}

func (r *ChatCompletionsRequest) UnmarshalJSON(data []byte) error {
	type plain ChatCompletionsRequest
	var next plain
	wire := struct {
		*plain
		Effort json.RawMessage `json:"reasoning_effort"`
	}{plain: &next}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	effort, numeric, err := parseWireReasoningEffort(wire.Effort)
	if err != nil {
		return err
	}
	*r = ChatCompletionsRequest(next)
	r.SetReasoningEffort(effort, numeric)
	return nil
}

func (r *ChatCompletionsRequest) SetReasoningEffort(effort string, numeric bool) {
	if numeric {
		r.ReasoningEffort = json.Number(effort)
	} else if effort != "" {
		r.ReasoningEffort = effort
	} else {
		r.ReasoningEffort = nil
	}
}

func (r *ChatCompletionsRequest) ReasoningEffortText() string {
	switch effort := r.ReasoningEffort.(type) {
	case string:
		return effort
	case json.Number:
		return string(effort)
	default:
		return ""
	}
}

func (r *ChatCompletionsRequest) NumericReasoningEffort() bool {
	_, numeric := r.ReasoningEffort.(json.Number)
	return numeric
}

// Chat/Anthropic bridges have no server-side Responses history. Silently
// dropping a continuation would lose both conversation and effective settings.
func validateResponsesBridgeContinuation(req *ResponsesRequest) error {
	if req.PreviousResponseID != "" {
		return fmt.Errorf("this protocol bridge cannot resume previous_response_id; resend the full conversation without previous_response_id")
	}
	return nil
}

// effectiveResponsesReasoning folds configuration updates into the target
// protocol's top-level field. It inspects small scalar fields without retaining
// or deserializing the full input tree, including audio/image payloads.
func effectiveResponsesReasoning(req *ResponsesRequest) (*ResponsesReasoning, error) {
	reasoning := req.Reasoning
	if !bytes.Contains(req.Input, []byte("configuration_update")) && !bytes.Contains(req.Input, []byte(`\u`)) {
		return reasoning, nil
	}
	var parseErr error
	gjson.GetBytes(req.Input, `#(type=="configuration_update")#.reasoning.effort`).ForEach(func(_, effort gjson.Result) bool {
		value, numeric, err := parseWireReasoningEffort(json.RawMessage(effort.Raw))
		if err != nil {
			parseErr = err
			return false
		}
		next := ResponsesReasoning{Effort: value, EffortIsNumber: numeric}
		if reasoning != nil {
			next.Summary = reasoning.Summary
		}
		reasoning = &next
		return true
	})
	return reasoning, parseErr
}
