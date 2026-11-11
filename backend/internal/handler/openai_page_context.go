package handler

import (
	"encoding/json"

	"github.com/Wei-Shaw/sub2api/internal/pkg/codexcontext"
)

// Desktop injects page snapshots as standalone outputs, without a model tool call.
// Keep them as untrusted user text, including on history replay and mixed bootstrap turns.
func normalizeCodexPageContext(body []byte) ([]byte, bool) {
	if !mayContainCodexCallOutputCandidate(body, "codex_apps_open_page", "codex_apps_selected_page_part_") || !hasUniqueJSONMembers(body) {
		return body, false
	}
	var request map[string]json.RawMessage
	if json.Unmarshal(body, &request) != nil {
		return body, false
	}
	var input []json.RawMessage
	if json.Unmarshal(request["input"], &input) != nil {
		return body, false
	}
	changed := false
	for i, raw := range input {
		var item map[string]json.RawMessage
		if json.Unmarshal(raw, &item) != nil {
			continue
		}
		readString := func(key string) string {
			var value string
			_ = json.Unmarshal(item[key], &value)
			return value
		}
		callID, hasCallID := item["call_id"]
		var callIDValue any
		if hasCallID && json.Unmarshal(callID, &callIDValue) != nil {
			continue
		}
		name, output := readString("name"), readString("output")
		text, valid := codexcontext.PageOutput(readString("type"), readString("namespace"), name, output, callIDValue, hasCallID)
		if !valid {
			continue
		}
		message := map[string]any{
			"type": "message",
			"role": "user",
			"content": []map[string]string{{
				"type": "input_text",
				"text": text,
			}},
		}
		encoded, err := json.Marshal(message)
		if err != nil {
			return body, false
		}
		input[i] = encoded
		changed = true
	}
	if !changed {
		return body, false
	}
	encodedInput, err := json.Marshal(input)
	if err != nil {
		return body, false
	}
	request["input"] = encodedInput
	encoded, err := json.Marshal(request)
	if err != nil {
		return body, false
	}
	return encoded, true
}

func validCodexPageContext(name, output string) bool {
	return codexcontext.ValidPageContext(name, output)
}

func validCodexPageSnapshot(text string) bool {
	return codexcontext.ValidPageSnapshot(text)
}
