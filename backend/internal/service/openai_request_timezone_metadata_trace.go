package service

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

// Temporary metadata diagnostics. Keep payloads and arbitrary metadata values
// out of this file's output so it can be enabled on real requests safely.
const (
	openAIEnvironmentMetadataTraceBodyLimit  = 8 << 20
	openAIEnvironmentMetadataTraceItemLimit  = 8
	openAIEnvironmentMetadataTraceKindsLimit = 64
	openAIEnvironmentMetadataMarker          = "environments.environment_context"
)

type openAIEnvironmentMetadataTrace struct {
	BodyBytes          int                                  `json:"body_bytes"`
	AcceptedAt         string                               `json:"accepted_at"`
	ConversionEnabled  bool                                 `json:"conversion_enabled"`
	PassthroughEnabled bool                                 `json:"passthrough_enabled"`
	SourceScanStatus   string                               `json:"source_scan_status"`
	Status             string                               `json:"status"`
	Reason             string                               `json:"reason"`
	ItemCount          int                                  `json:"item_count"`
	ItemsTruncated     bool                                 `json:"items_truncated"`
	Items              []openAIEnvironmentMetadataTraceItem `json:"items"`
}

type openAIEnvironmentMetadataTraceItem struct {
	Path              string                                    `json:"path"`
	Role              string                                    `json:"role"`
	RoleType          string                                    `json:"role_type"`
	ContentType       string                                    `json:"content_type"`
	ContentItemType   string                                    `json:"content_item_type"`
	ContentCount      int                                       `json:"content_count"`
	ContentIndex      int                                       `json:"content_index"`
	TextPresent       bool                                      `json:"text_present"`
	TextType          string                                    `json:"text_type"`
	TextBytes         int                                       `json:"text_bytes"`
	MetadataPresent   bool                                      `json:"metadata_present"`
	MetadataType      string                                    `json:"metadata_type"`
	Kinds             openAIEnvironmentMetadataKindsTrace       `json:"kinds"`
	AlternateKinds    []openAIEnvironmentMetadataAlternateTrace `json:"alternate_kinds,omitempty"`
	Eligibility       string                                    `json:"eligibility"`
	EnvironmentSource string                                    `json:"environment_source"`
	Current           bool                                      `json:"current"`
	ParseStatus       string                                    `json:"parse_status"`
	ParseReason       string                                    `json:"parse_reason"`
	PreparationStatus string                                    `json:"preparation_status"`
	PreparationReason string                                    `json:"preparation_reason"`
}

type openAIEnvironmentMetadataKindsTrace struct {
	Present         bool   `json:"present"`
	Type            string `json:"type"`
	Count           int    `json:"count"`
	MarkerType      string `json:"marker_type"`
	MarkerClass     string `json:"marker_class"`
	ExpectedIndices []int  `json:"expected_indices"`
	SearchTruncated bool   `json:"search_truncated"`
}

type openAIEnvironmentMetadataAlternateTrace struct {
	Location string                              `json:"location"`
	Kinds    openAIEnvironmentMetadataKindsTrace `json:"kinds"`
}

// Callers gate OAuth/observation and call once per frozen request or WS turn.
// The body must be the original frozen source, never the final adapted payload.
// Preparation describes the ingress decision, not an observed outbound result;
// the existing final-body observer remains authoritative for actual transmission.
func logOpenAIEnvironmentMetadataTrace(ctx context.Context, accountID int64, stage string, body []byte, state *RequestTimezoneState) {
	trace := buildOpenAIEnvironmentMetadataTrace(body, state)
	if trace.Status == "complete" && trace.ItemCount == 0 {
		return
	}
	logger.FromContext(ctx).Info("openai.environment_metadata_trace",
		zap.Bool(logger.OpsSystemLogSkipField, true),
		zap.Int64("account_id", accountID),
		zap.String("stage", openAIEnvironmentTraceEnum(stage, "http", "http_ingress", "ws", "ws_frame", "ingress_before_timezone", "ws_frame_before_timezone")),
		zap.Any("metadata_trace", trace))
}

func buildOpenAIEnvironmentMetadataTrace(body []byte, state *RequestTimezoneState) openAIEnvironmentMetadataTrace {
	trace := openAIEnvironmentMetadataTrace{BodyBytes: len(body), Status: "skipped", SourceScanStatus: "unavailable", Items: []openAIEnvironmentMetadataTraceItem{}}
	if state != nil {
		if !state.AcceptedAt.IsZero() {
			trace.AcceptedAt = state.AcceptedAt.UTC().Format(time.RFC3339Nano)
		}
		trace.ConversionEnabled = state.Policy.TimezoneConversionEnabled
		trace.PassthroughEnabled = state.Policy.PassthroughTimezoneConversionEnabled
		if state.Inbound != nil {
			trace.SourceScanStatus = openAIEnvironmentTraceEnum(state.Inbound.ScanStatus, "complete", "limited", "parse_failed", "not_applicable")
		}
	}
	if len(body) > openAIEnvironmentMetadataTraceBodyLimit {
		trace.Reason = "body_limit"
		return trace
	}
	if state == nil {
		trace.Reason = "state_missing"
		return trace
	}
	if !gjson.ValidBytes(body) {
		trace.Reason = "invalid_json"
		return trace
	}
	root := gjson.ParseBytes(body)
	if !root.IsObject() {
		trace.Reason = "root_not_object"
		return trace
	}
	trace.Status = "complete"
	for _, conversion := range state.Conversions {
		if conversion.Source != "environment_context" {
			continue
		}
		trace.ItemCount++
		if len(trace.Items) == openAIEnvironmentMetadataTraceItemLimit {
			trace.ItemsTruncated = true
			continue
		}
		trace.Items = append(trace.Items, buildOpenAIEnvironmentMetadataTraceItem(root, conversion, state.Inbound))
	}
	return trace
}

func buildOpenAIEnvironmentMetadataTraceItem(root gjson.Result, conversion TimezoneConversion, inbound *TimezoneScanResult) openAIEnvironmentMetadataTraceItem {
	item := openAIEnvironmentMetadataTraceItem{
		Path: "unsupported", ContentIndex: -1, Eligibility: "unsupported_path",
		EnvironmentSource: openAIEnvironmentTraceEnum(conversion.EnvironmentSource, "metadata", "mapped", "reference"),
		ParseStatus:       "unavailable", PreparationStatus: openAIEnvironmentTraceEnum(conversion.Status, "skipped", "disabled", "converted", "unchanged", "unmatched", "not_sent", "incomplete", "mismatched"),
		PreparationReason: openAIEnvironmentTraceReason(conversion.Reason),
	}
	if inbound != nil {
		for _, source := range inbound.Items {
			if source.Source == "environment_context" && source.Path == conversion.Path {
				item.Current = source.Current
				item.ParseStatus = openAIEnvironmentTraceEnum(source.Status, "valid", "invalid")
				item.ParseReason = openAIEnvironmentTraceReason(source.Reason)
				break
			}
		}
	}
	path, messagePath, index, ok := openAIEnvironmentTracePath(conversion.Path)
	if !ok {
		return item
	}
	item.Path, item.ContentIndex = path, index
	message := root.Get(messagePath)
	content, role := message.Get("content"), message.Get("role")
	part := gjson.Result{}
	if index >= 0 && content.IsArray() {
		part = content.Get(strconv.Itoa(index))
	}
	text := root.Get(path)
	item.RoleType = openAIEnvironmentTraceJSONType(role)
	item.Role = openAIEnvironmentTraceStringEnum(role, "user", "assistant", "developer", "system", "tool")
	item.ContentType = openAIEnvironmentTraceJSONType(content)
	item.ContentCount = openAIEnvironmentTraceArrayCount(content)
	item.ContentItemType = openAIEnvironmentTraceStringEnum(part.Get("type"), "input_text", "text", "output_text", "input_image", "input_file", "input_audio")
	item.TextPresent, item.TextType = text.Exists(), openAIEnvironmentTraceJSONType(text)
	if text.Type == gjson.String {
		item.TextBytes = len(text.String())
	}
	metadata := message.Get("internal_chat_message_metadata_passthrough")
	kinds := metadata.Get("content_item_kinds")
	item.MetadataPresent, item.MetadataType = metadata.Exists(), openAIEnvironmentTraceJSONType(metadata)
	item.Kinds = buildOpenAIEnvironmentMetadataKindsTrace(kinds, index)
	switch {
	case item.Role != "user":
		item.Eligibility = "role_not_user"
	case !content.IsArray() || item.ContentItemType != "input_text":
		item.Eligibility = "content_not_input_text"
	case !metadata.Exists():
		item.Eligibility = "metadata_missing"
	case !kinds.IsArray():
		item.Eligibility = "kinds_not_array"
	case index < 0 || index >= item.Kinds.Count:
		item.Eligibility = "index_out_of_range"
	case item.Kinds.MarkerClass != "exact":
		item.Eligibility = "marker_mismatch"
	default:
		item.Eligibility = "eligible"
	}
	for _, alternate := range []struct {
		location string
		kinds    gjson.Result
	}{
		{"message.content_item_kinds", message.Get("content_item_kinds")},
		{"message.metadata.content_item_kinds", message.Get("metadata.content_item_kinds")},
		{"contentpart.internal_chat_message_metadata_passthrough.content_item_kinds", part.Get("internal_chat_message_metadata_passthrough.content_item_kinds")},
		{"contentpart.metadata.content_item_kinds", part.Get("metadata.content_item_kinds")},
		{"root.internal_chat_message_metadata_passthrough.content_item_kinds", root.Get("internal_chat_message_metadata_passthrough.content_item_kinds")},
	} {
		if alternate.kinds.Exists() {
			item.AlternateKinds = append(item.AlternateKinds, openAIEnvironmentMetadataAlternateTrace{
				Location: alternate.location, Kinds: buildOpenAIEnvironmentMetadataKindsTrace(alternate.kinds, index),
			})
		}
	}
	return item
}

// Validate and reconstruct paths before using or logging them. Never accept an
// arbitrary metadata key or user string as a JSON path in diagnostic output.
func openAIEnvironmentTracePath(path string) (safe, message string, index int, ok bool) {
	if path == "input" {
		return path, "", -1, true
	}
	parts := strings.Split(path, ".")
	if (len(parts) != 3 && len(parts) != 5) || (parts[0] != "input" && parts[0] != "messages") || parts[2] != "content" {
		return "", "", -1, false
	}
	parseIndex := func(value string) (int, bool) {
		n, err := strconv.Atoi(value)
		return n, err == nil && n >= 0 && n <= openAIEnvironmentMetadataTraceBodyLimit && strconv.Itoa(n) == value
	}
	messageIndex, valid := parseIndex(parts[1])
	if !valid {
		return "", "", -1, false
	}
	message = parts[0] + "." + strconv.Itoa(messageIndex)
	if len(parts) == 3 {
		return message + ".content", message, -1, true
	}
	index, valid = parseIndex(parts[3])
	if !valid || parts[4] != "text" {
		return "", "", -1, false
	}
	return message + ".content." + strconv.Itoa(index) + ".text", message, index, true
}

func buildOpenAIEnvironmentMetadataKindsTrace(kinds gjson.Result, index int) openAIEnvironmentMetadataKindsTrace {
	trace := openAIEnvironmentMetadataKindsTrace{Present: kinds.Exists(), Type: openAIEnvironmentTraceJSONType(kinds), ExpectedIndices: []int{}, MarkerType: "missing", MarkerClass: "missing"}
	if !kinds.IsArray() {
		return trace
	}
	trace.Count = openAIEnvironmentTraceArrayCount(kinds)
	if index >= 0 && index < trace.Count {
		marker := kinds.Get(strconv.Itoa(index))
		trace.MarkerType = openAIEnvironmentTraceJSONType(marker)
		trace.MarkerClass = "nonstring"
		if marker.Type == gjson.String {
			switch value := marker.String(); {
			case value == openAIEnvironmentMetadataMarker:
				trace.MarkerClass = "exact"
			case strings.EqualFold(strings.TrimSpace(value), openAIEnvironmentMetadataMarker):
				trace.MarkerClass = "case_or_whitespace_mismatch"
			default:
				trace.MarkerClass = "other_string"
			}
		}
	}
	trace.SearchTruncated = trace.Count > openAIEnvironmentMetadataTraceKindsLimit
	kinds.ForEach(func(key, value gjson.Result) bool {
		if key.Int() >= openAIEnvironmentMetadataTraceKindsLimit {
			return false
		}
		if value.Type == gjson.String && value.String() == openAIEnvironmentMetadataMarker {
			trace.ExpectedIndices = append(trace.ExpectedIndices, int(key.Int()))
		}
		return true
	})
	return trace
}

func openAIEnvironmentTraceArrayCount(value gjson.Result) int {
	if value.IsArray() {
		return int(value.Get("#").Int())
	}
	return 0
}

func openAIEnvironmentTraceJSONType(value gjson.Result) string {
	if !value.Exists() {
		return "missing"
	}
	switch value.Type {
	case gjson.Null:
		return "null"
	case gjson.String:
		return "string"
	case gjson.Number:
		return "number"
	case gjson.True, gjson.False:
		return "boolean"
	case gjson.JSON:
		if value.IsArray() {
			return "array"
		}
		if value.IsObject() {
			return "object"
		}
	}
	return "other"
}

func openAIEnvironmentTraceStringEnum(value gjson.Result, allowed ...string) string {
	if value.Type != gjson.String {
		return openAIEnvironmentTraceJSONType(value)
	}
	return openAIEnvironmentTraceEnum(value.String(), allowed...)
}

func openAIEnvironmentTraceEnum(value string, allowed ...string) string {
	for _, candidate := range allowed {
		if value == candidate {
			return value
		}
	}
	return "other"
}

func openAIEnvironmentTraceReason(value string) string {
	return openAIEnvironmentTraceEnum(value, "", "environment_metadata_missing", "conversion_disabled", "environment_not_standalone", "quoted_xml_content",
		"malformed_or_duplicate_tags", "timezone_missing", "invalid_timezone", "invalid_current_date", "target_timezone_unavailable", "accepted_at_unavailable",
		"timezone_converted", "historical_timezone_converted", "already_target", "patch_failed", "source_changed_before_apply", "scan_limited", "scan_parse_failed", "scan_not_applicable",
		"source_path_changed", "adapter_removed_source", "ambiguous_source_mapping", "value_not_observable", "final_value_differs", "source_not_in_final_body")
}
