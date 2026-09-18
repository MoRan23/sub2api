package service

import (
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
)

const environmentDiagnosticMarkerLimit = 128

// These summaries are captured alongside ingress qualification, before private
// metadata is removed. They never retain message text, arbitrary metadata keys,
// workspace details or credentials, and never participate in conversion.
type openAIEnvironmentSourceDiagnostic struct {
	Path             string                                `json:"path"`
	Container        string                                `json:"container"`
	Role             string                                `json:"role"`
	ContentType      string                                `json:"content_type"`
	ContentIndex     int                                   `json:"content_index"`
	ContentCount     int                                   `json:"content_count"`
	MarkerStatus     string                                `json:"marker_status"`
	FallbackBlockers []string                              `json:"fallback_blockers"`
	Metadata         []openAIEnvironmentMetadataDiagnostic `json:"metadata"`
	Structure        openAIEnvironmentStructureDiagnostic  `json:"structure"`
}

type openAIEnvironmentMetadataDiagnostic struct {
	Location                     string `json:"location"`
	Type                         string `json:"type"`
	KindsType                    string `json:"kinds_type"`
	SelectedType                 string `json:"selected_type"`
	SelectedMarker               string `json:"selected_marker,omitempty"`
	SelectedMarkerBytes          int    `json:"selected_marker_bytes,omitempty"`
	SelectedEnvironmentAfterTrim bool   `json:"selected_environment_after_trim"`
	InspectedKinds               int    `json:"inspected_kinds"`
	EnvironmentIndices           []int  `json:"environment_indices,omitempty"`
	Truncated                    bool   `json:"truncated"`
}

type openAIEnvironmentStructureDiagnostic struct {
	TextBytes        int  `json:"text_bytes"`
	EnvironmentOpen  int  `json:"environment_open"`
	EnvironmentClose int  `json:"environment_close"`
	TimezoneOpen     int  `json:"timezone_open"`
	TimezoneClose    int  `json:"timezone_close"`
	DateOpen         int  `json:"date_open"`
	DateClose        int  `json:"date_close"`
	StartsWithBlock  bool `json:"starts_with_block"`
	EndsWithBlock    bool `json:"ends_with_block"`
	CodeFence        bool `json:"code_fence"`
	QuotedLine       bool `json:"quoted_line"`
	XMLComment       bool `json:"xml_comment"`
	CDATA            bool `json:"cdata"`
}

func environmentDiagnosticJSONType(value gjson.Result) string {
	switch {
	case !value.Exists():
		return "missing"
	case value.IsObject():
		return "object"
	case value.IsArray():
		return "array"
	default:
		return strings.ToLower(value.Type.String())
	}
}

func environmentDiagnosticEnum(value gjson.Result, known ...string) string {
	if value.Type == gjson.String {
		for _, candidate := range known {
			if value.String() == candidate {
				return candidate
			}
		}
		return "other_string"
	}
	return environmentDiagnosticJSONType(value)
}

// Kind names are diagnostic protocol enums, not arbitrary metadata strings.
// Preserve recognized namespaces only; unknown values still explain a mismatch
// through their type and length without reflecting arbitrary client text.
func environmentDiagnosticMarker(value gjson.Result) string {
	if value.Type != gjson.String {
		return ""
	}
	name := value.String()
	if len(name) == 0 {
		return "empty"
	}
	if len(name) > 128 {
		return "other_string"
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '.' || r == '-') {
			return "other_string"
		}
	}
	for _, prefix := range []string{"environments.", "memories.", "user.", "developer.", "agent.", "context.", "tools.", "compaction."} {
		if strings.HasPrefix(name, prefix) {
			return strings.Clone(name)
		}
	}
	if name == "user_message" {
		return "user_message"
	}
	return "other_string"
}

func environmentDiagnosticMetadata(object gjson.Result, scope string, index int) []openAIEnvironmentMetadataDiagnostic {
	var result []openAIEnvironmentMetadataDiagnostic
	for _, path := range []string{"internal_chat_message_metadata_passthrough", "content_item_kinds", "metadata.content_item_kinds"} {
		value := object.Get(path)
		if !value.Exists() {
			continue
		}
		kinds := value
		if path == "internal_chat_message_metadata_passthrough" {
			kinds = value.Get("content_item_kinds")
		}
		entry := openAIEnvironmentMetadataDiagnostic{Location: scope + "." + path, Type: environmentDiagnosticJSONType(value), KindsType: environmentDiagnosticJSONType(kinds), SelectedType: "not_applicable"}
		if index >= 0 {
			selected := kinds.Get(strconv.Itoa(index))
			entry.SelectedType = environmentDiagnosticJSONType(selected)
			entry.SelectedMarker = environmentDiagnosticMarker(selected)
			if selected.Type == gjson.String {
				entry.SelectedMarkerBytes = len(selected.String())
				entry.SelectedEnvironmentAfterTrim = strings.TrimSpace(selected.String()) == "environments.environment_context"
			}
		}
		if kinds.IsArray() {
			kinds.ForEach(func(key, item gjson.Result) bool {
				if entry.InspectedKinds >= environmentDiagnosticMarkerLimit {
					entry.Truncated = true
					return false
				}
				entry.InspectedKinds++
				if item.Type == gjson.String && item.String() == "environments.environment_context" {
					if len(entry.EnvironmentIndices) < 8 {
						entry.EnvironmentIndices = append(entry.EnvironmentIndices, int(key.Int()))
					} else {
						entry.Truncated = true
					}
				}
				return true
			})
		}
		result = append(result, entry)
	}
	return result
}

func (s *requestTimezoneScanner) captureEnvironmentDiagnostic(before int, container string, message, part gjson.Result, index, contentCount int) {
	if !s.captureDiagnostics || len(s.occurrences) <= before {
		return
	}
	occurrence := &s.occurrences[len(s.occurrences)-1]
	if !occurrence.environment {
		return
	}
	d := &openAIEnvironmentSourceDiagnostic{
		Path: occurrence.item.Path, Container: container,
		Role:         environmentDiagnosticEnum(message.Get("role"), "user", "assistant", "developer", "system", "tool", "function"),
		ContentType:  environmentDiagnosticEnum(part.Get("type"), "input_text", "text", "output_text"),
		ContentIndex: index, ContentCount: contentCount, FallbackBlockers: []string{},
		Metadata: append([]openAIEnvironmentMetadataDiagnostic{}, s.diagnosticRootMetadata...),
	}
	messageMetadata := environmentDiagnosticMetadata(message, "message", index)
	partMetadata := environmentDiagnosticMetadata(part, "part", index)
	d.Metadata = append(d.Metadata, messageMetadata...)
	d.Metadata = append(d.Metadata, partMetadata...)
	kinds := message.Get("internal_chat_message_metadata_passthrough.content_item_kinds")
	marker := kinds.Get(strconv.Itoa(index))
	switch {
	case !kinds.Exists():
		d.MarkerStatus = "kinds_missing"
	case !kinds.IsArray():
		d.MarkerStatus = "kinds_not_array"
	case index < 0:
		d.MarkerStatus = "content_not_array"
	case !marker.Exists():
		d.MarkerStatus = "index_missing"
	case marker.Type != gjson.String:
		d.MarkerStatus = "marker_not_string"
	case marker.String() != "environments.environment_context":
		d.MarkerStatus = "marker_mismatch"
	default:
		d.MarkerStatus = "matched"
	}
	for _, gate := range []struct {
		blocked bool
		reason  string
	}{
		{container != "input", "not_responses_input_array"},
		{d.Role != "user", "role_not_user"},
		{index < 0, "content_not_array"},
		{d.ContentType != "input_text", "content_not_input_text"},
		{len(s.diagnosticRootMetadata) != 0, "request_metadata_present"},
		{len(messageMetadata) != 0, "message_metadata_present"},
		{len(partMetadata) != 0, "part_metadata_present"},
		{occurrence.item.Status != "valid", "environment_structure_invalid"},
		{!occurrence.hasDate, "current_date_not_validated"},
	} {
		if gate.blocked {
			d.FallbackBlockers = append(d.FallbackBlockers, gate.reason)
		}
	}
	text := occurrence.text
	trimmed := strings.TrimSpace(text)
	d.Structure = openAIEnvironmentStructureDiagnostic{
		TextBytes:       len(text),
		EnvironmentOpen: countRequestTimezoneTag(text, "environment_context", false), EnvironmentClose: countRequestTimezoneTag(text, "environment_context", true),
		TimezoneOpen: countRequestTimezoneTag(text, "timezone", false), TimezoneClose: countRequestTimezoneTag(text, "timezone", true),
		DateOpen: countRequestTimezoneTag(text, "current_date", false), DateClose: countRequestTimezoneTag(text, "current_date", true),
		StartsWithBlock: strings.HasPrefix(trimmed, "<environment_context>"), EndsWithBlock: strings.HasSuffix(trimmed, "</environment_context>"),
		CodeFence: strings.Contains(text, "```") || strings.Contains(text, "~~~"), XMLComment: strings.Contains(text, "<!--"), CDATA: strings.Contains(text, "<![CDATA["),
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), ">") {
			d.Structure.QuotedLine = true
			break
		}
	}
	occurrence.environmentDiagnostic = d
}
