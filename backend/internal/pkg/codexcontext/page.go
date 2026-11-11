// Package codexcontext recognizes client-supplied Codex context without changing
// the request. Compatibility forwarding and prompt auditing share these rules.
package codexcontext

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
)

var selectedPageName = regexp.MustCompile(`^codex_apps_selected_page_part_([1-9][0-9]*)_of_([1-9][0-9]*)$`)

// PageOutput accepts only standalone desktop page context, never a model tool
// result identified by a nonempty call ID. The output remains untrusted text.
func PageOutput(kind, namespace, name, output string, callID any, hasCallID bool) (string, bool) {
	if kind != "function_call_output" || namespace != "agent" {
		return "", false
	}
	if hasCallID {
		id, ok := callID.(string)
		if !ok || strings.TrimSpace(id) != "" {
			return "", false
		}
	}
	if !ValidPageContext(name, output) {
		return "", false
	}
	return "<external_" + name + ">\n" + output + "\n</external_" + name + ">", true
}

func ValidPageContext(name, output string) bool {
	if name == "codex_apps_open_page" {
		return ValidPageSnapshot(output)
	}
	parts := selectedPageName.FindStringSubmatch(name)
	if len(parts) != 3 {
		return false
	}
	part, err := strconv.Atoi(parts[1])
	if err != nil {
		return false
	}
	total, err := strconv.Atoi(parts[2])
	if err != nil || part > total {
		return false
	}
	header, snapshot, ok := strings.Cut(output, "\n")
	if !ok || strings.TrimSuffix(header, "\r") != fmt.Sprintf("Companion Page snapshot part %d of %d:", part, total) || strings.TrimSpace(snapshot) == "" {
		return false
	}
	return total > 1 || ValidPageSnapshot(snapshot)
}

func ValidPageSnapshot(text string) bool {
	decoder := json.NewDecoder(strings.NewReader(text))
	if !consumeUniqueValue(decoder) {
		return false
	}
	if _, err := decoder.Token(); err != io.EOF {
		return false
	}
	var snapshot map[string]json.RawMessage
	if json.Unmarshal([]byte(text), &snapshot) != nil {
		return false
	}
	id, exists := snapshot["page_id"]
	if !exists {
		return false
	}
	if strings.TrimSpace(string(id)) == "null" {
		return true
	}
	var value string
	return json.Unmarshal(id, &value) == nil && strings.TrimSpace(value) != ""
}

func consumeUniqueValue(decoder *json.Decoder) bool {
	token, err := decoder.Token()
	if err != nil {
		return false
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return true
	}
	switch delim {
	case '{':
		members := make(map[string]struct{})
		for decoder.More() {
			token, err := decoder.Token()
			key, ok := token.(string)
			if err != nil || !ok {
				return false
			}
			if _, duplicate := members[key]; duplicate {
				return false
			}
			members[key] = struct{}{}
			if !consumeUniqueValue(decoder) {
				return false
			}
		}
		end, err := decoder.Token()
		return err == nil && end == json.Delim('}')
	case '[':
		for decoder.More() {
			if !consumeUniqueValue(decoder) {
				return false
			}
		}
		end, err := decoder.Token()
		return err == nil && end == json.Delim(']')
	default:
		return false
	}
}
