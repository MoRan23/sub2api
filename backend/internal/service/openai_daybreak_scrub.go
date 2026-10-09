package service

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/tidwall/gjson"
)

type openAIDaybreakJSONMember struct {
	name       string
	start, end int
	value      gjson.Result
}

type openAIDaybreakJSONEdit struct {
	start, end int
	value      []byte
}

// These views only live while reading the immutable request. Editing raw member
// spans preserves unknown fields, exact numbers, and every non-target duplicate.
func openAIDaybreakJSONMembers(body []byte) []openAIDaybreakJSONMember {
	var members []openAIDaybreakJSONMember
	parseRawJSONView(body).ForEach(func(key, value gjson.Result) bool {
		name := key.Str
		if strings.ContainsRune(key.Raw, '\\') {
			_ = json.Unmarshal([]byte(key.Raw), &name)
		}
		members = append(members, openAIDaybreakJSONMember{
			name: name, start: key.Index, end: value.Index + len(value.Raw), value: value,
		})
		return true
	})
	return members
}

func applyOpenAIDaybreakJSONEdits(body []byte, edits []openAIDaybreakJSONEdit) []byte {
	if len(edits) == 0 {
		return body
	}
	size := len(body)
	for _, edit := range edits {
		size += len(edit.value) - (edit.end - edit.start)
	}
	result := make([]byte, 0, size)
	previous := 0
	for _, edit := range edits {
		result = append(result, body[previous:edit.start]...)
		result = append(result, edit.value...)
		previous = edit.end
	}
	return append(result, body[previous:]...)
}

// stripOpenAIAccessProgramsCyber is also used for the JSON value of a multipart
// access_programs field. It does not recurse into extensions or invalid types.
func stripOpenAIAccessProgramsCyber(body []byte) ([]byte, bool, error) {
	if !json.Valid(body) {
		return nil, false, errors.New("invalid access_programs JSON while applying the Daybreak policy")
	}
	if !parseRawJSONView(body).IsObject() {
		return body, false, nil
	}
	result, changed := stripOpenAIValidatedCyberObject(body)
	return result, changed, nil
}

func stripOpenAIValidatedCyberObject(body []byte) ([]byte, bool) {
	members := openAIDaybreakJSONMembers(body)
	var edits []openAIDaybreakJSONEdit
	for i := 0; i < len(members); {
		if members[i].name != "cyber" {
			i++
			continue
		}
		first := i
		for i < len(members) && members[i].name == "cyber" {
			i++
		}
		start, end := members[first].start, members[i-1].end
		if i < len(members) {
			// Consume the following comma when another member follows.
			end = members[i].start
		} else if first > 0 {
			// A final run consumes the preceding comma instead.
			start = members[first-1].end
		}
		edits = append(edits, openAIDaybreakJSONEdit{start: start, end: end})
	}
	return applyOpenAIDaybreakJSONEdits(body, edits), len(edits) > 0
}

// stripOpenAIRequestCyber removes all root access_programs.cyber occurrences,
// including escaped/duplicate keys. History, tool input and schemas are opaque.
// Invalid JSON is an error: a disabled policy must never silently forward a
// request whose target fields could not be inspected safely.
func stripOpenAIRequestCyber(body []byte) ([]byte, bool, error) {
	if len(body) == 0 {
		return body, false, nil
	}
	if !json.Valid(body) {
		return nil, false, errors.New("invalid request JSON while applying the Daybreak policy")
	}
	if !parseRawJSONView(body).IsObject() {
		return body, false, nil
	}
	var edits []openAIDaybreakJSONEdit
	for _, member := range openAIDaybreakJSONMembers(body) {
		if member.name != "access_programs" || !member.value.IsObject() {
			continue
		}
		start := member.value.Index
		patched, changed := stripOpenAIValidatedCyberObject(body[start:member.end])
		if changed {
			edits = append(edits, openAIDaybreakJSONEdit{start: start, end: member.end, value: patched})
		}
	}
	return applyOpenAIDaybreakJSONEdits(body, edits), len(edits) > 0, nil
}
