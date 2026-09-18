package service

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	fingerprintMetadataMissing   = "missing"
	fingerprintMetadataValid     = "valid"
	fingerprintMetadataInvalid   = "invalid"
	fingerprintMetadataTruncated = "truncated"
	fingerprintNamespaceLimit    = 64
	fingerprintFunctionLimit     = 256
	fingerprintMetadataNameLimit = 256
)

// FingerprintMetadataStatus distinguishes absent wire fields from malformed
// ones. The observer never substitutes an identity plan or inbound metadata.
type FingerprintMetadataStatus struct {
	RequestKind            string `json:"request_kind"`
	HistoryIngestRequested string `json:"history_ingest_requested"`
	Compaction             string `json:"compaction"`
	ToolNamespacesInfo     string `json:"tool_namespaces_info"`
}

// Namespace and Function preserve the original inventory map keys separately
// from display names. Arrays have deterministic order and cannot lose entries
// when two long keys acquire the same displayed prefix after truncation.
type FingerprintToolNamespace struct {
	Namespace string                    `json:"namespace"`
	Name      string                    `json:"name"`
	Functions []FingerprintToolFunction `json:"functions"`
}

type FingerprintToolFunction struct {
	Function     string                `json:"function"`
	Name         string                `json:"name"`
	Direct       bool                  `json:"direct"`
	Deferred     bool                  `json:"deferred"`
	CodeModeName *string               `json:"code_mode_name,omitempty"`
	Source       FingerprintToolSource `json:"source"`
}

type FingerprintToolSource struct {
	Kind       string `json:"kind"`
	ServerName string `json:"server_name,omitempty"`
}

type fingerprintMetadataCarrier struct {
	fields map[string]json.RawMessage
	valid  bool
}

// Match finalFingerprintCodexWireProfile's canonical-body, compatibility-body,
// then final-header precedence. Unlike its permissive profile merger, presence
// of malformed data is retained so the UI cannot report a fallback as valid.
func fingerprintFinalMetadataCarriers(headers http.Header, body []byte) []fingerprintMetadataCarrier {
	var carriers []fingerprintMetadataCarrier
	appendCarrier := func(raw json.RawMessage, requireString bool) {
		_, fields, valid := decodeCodexWireNestedCarrier(raw, requireString)
		carriers = append(carriers, fingerprintMetadataCarrier{fields: fields, valid: valid})
	}
	if len(body) > 0 {
		var root struct {
			ClientMetadata json.RawMessage `json:"client_metadata"`
			TurnMetadata   json.RawMessage `json:"x-codex-turn-metadata"`
		}
		if !utf8.Valid(body) || json.Unmarshal(body, &root) != nil {
			return []fingerprintMetadataCarrier{{}}
		}
		if len(root.ClientMetadata) > 0 && string(root.ClientMetadata) != "null" {
			var client map[string]json.RawMessage
			if json.Unmarshal(root.ClientMetadata, &client) != nil {
				carriers = append(carriers, fingerprintMetadataCarrier{})
			} else if raw, present := client[openAIWSTurnMetadataHeader]; present {
				appendCarrier(raw, true)
			}
		}
		if len(root.TurnMetadata) > 0 {
			appendCarrier(root.TurnMetadata, false)
		}
	}
	for _, value := range headerValuesCaseInsensitive(headers, openAIWSTurnMetadataHeader) {
		appendCarrier(json.RawMessage(value), false)
	}
	return carriers
}

func fingerprintMetadataField(carriers []fingerprintMetadataCarrier, field string) (json.RawMessage, string) {
	for _, carrier := range carriers {
		if !carrier.valid {
			return nil, fingerprintMetadataInvalid
		}
		if raw, exists := carrier.fields[field]; exists {
			return raw, fingerprintMetadataValid
		}
	}
	return nil, fingerprintMetadataMissing
}

func populateFingerprintObservationMetadata(entry *FingerprintObservationEntry, headers http.Header, body []byte) {
	carriers := fingerprintFinalMetadataCarriers(headers, body)
	populateFingerprintObservationMetadataFromCarriers(entry, carriers)
}

func populateFingerprintObservationMetadataFromCarriers(entry *FingerprintObservationEntry, carriers []fingerprintMetadataCarrier) {
	status := &FingerprintMetadataStatus{}
	entry.MetadataStatus = status
	raw, state := fingerprintMetadataField(carriers, "request_kind")
	status.RequestKind = state
	if state == fingerprintMetadataValid {
		var value string
		kind, valid := ParseCodexWireRequestKind(codexWireString(raw))
		if json.Unmarshal(raw, &value) != nil || !valid {
			status.RequestKind = fingerprintMetadataInvalid
		} else {
			entry.RequestKind = kind
		}
	}
	raw, status.HistoryIngestRequested = fingerprintMetadataField(carriers, "history_ingest_requested")
	if status.HistoryIngestRequested == fingerprintMetadataValid {
		entry.HistoryIngestRequested = codexWireBool(raw)
		if entry.HistoryIngestRequested == nil {
			status.HistoryIngestRequested = fingerprintMetadataInvalid
		}
	}
	raw, status.Compaction = fingerprintMetadataField(carriers, "compaction")
	if status.Compaction == fingerprintMetadataValid {
		var metadata CodexCompactionTurnMetadata
		if json.Unmarshal(raw, &metadata) != nil || !metadata.Valid() {
			status.Compaction = fingerprintMetadataInvalid
		} else {
			entry.Compaction = &metadata
		}
	}
	raw, status.ToolNamespacesInfo = fingerprintMetadataField(carriers, "tool_namespaces_info")
	if status.ToolNamespacesInfo == fingerprintMetadataValid {
		entry.ToolNamespacesInfo, status.ToolNamespacesInfo = parseFingerprintToolNamespaces(raw)
	}
}

// Physical WS handshakes retain only the small typed compatibility projection.
// Tool inventories are body-only in the canonical header writer and must not be
// re-encoded from a possibly truncated observation as if they were complete.
func copyFingerprintObservationSafeMetadata(safe map[string]any, metadata map[string]json.RawMessage) {
	selected := make(map[string]json.RawMessage, 3)
	for _, field := range []string{"request_kind", "history_ingest_requested", "compaction"} {
		if raw, present := metadata[field]; present {
			selected[field] = raw
			safe[field] = nil // Retain invalid presence without arbitrary raw values.
		}
	}
	var entry FingerprintObservationEntry
	populateFingerprintObservationMetadataFromCarriers(&entry, []fingerprintMetadataCarrier{{fields: selected, valid: true}})
	if entry.MetadataStatus.RequestKind == fingerprintMetadataValid {
		safe["request_kind"] = entry.RequestKind
	}
	if entry.MetadataStatus.HistoryIngestRequested == fingerprintMetadataValid {
		safe["history_ingest_requested"] = *entry.HistoryIngestRequested
	}
	if entry.MetadataStatus.Compaction == fingerprintMetadataValid {
		safe["compaction"] = *entry.Compaction
	}
}

func fingerprintMetadataObject(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	var object map[string]json.RawMessage
	err := json.Unmarshal(raw, &object)
	return object, err == nil && object != nil
}

func fingerprintMetadataName(raw json.RawMessage, truncated *bool) (string, bool) {
	var name string
	if json.Unmarshal(raw, &name) != nil || name == "" || !utf8.ValidString(name) || strings.ContainsAny(name, "\x00\r\n") {
		return "", false
	}
	return fingerprintBoundMetadataName(name, truncated), true
}

func fingerprintBoundMetadataName(value string, truncated *bool) string {
	if utf8.RuneCountInString(value) > fingerprintMetadataNameLimit {
		value = string([]rune(value)[:fingerprintMetadataNameLimit])
		*truncated = true
	}
	return strings.Clone(value)
}

func fingerprintMetadataKeys(object map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func parseFingerprintToolNamespaces(raw json.RawMessage) ([]FingerprintToolNamespace, string) {
	object, valid := fingerprintMetadataObject(raw)
	if !valid {
		return nil, fingerprintMetadataInvalid
	}
	keys := fingerprintMetadataKeys(object)
	truncated := len(keys) > fingerprintNamespaceLimit
	if truncated {
		keys = keys[:fingerprintNamespaceLimit]
	}
	result := make([]FingerprintToolNamespace, 0, len(keys))
	functionCount := 0
	for _, key := range keys {
		namespace, valid := fingerprintMetadataObject(object[key])
		if !valid || key == "" || strings.ContainsAny(key, "\x00\r\n") {
			return nil, fingerprintMetadataInvalid
		}
		name, valid := fingerprintMetadataName(namespace["name"], &truncated)
		functions, functionsValid := fingerprintMetadataObject(namespace["functions"])
		if !valid || !functionsValid {
			return nil, fingerprintMetadataInvalid
		}
		item := FingerprintToolNamespace{Namespace: fingerprintBoundMetadataName(key, &truncated), Name: name, Functions: []FingerprintToolFunction{}}
		functionKeys := fingerprintMetadataKeys(functions)
		remaining := fingerprintFunctionLimit - functionCount
		if len(functionKeys) > remaining {
			truncated = true
			functionKeys = functionKeys[:remaining]
		}
		for _, functionKey := range functionKeys {
			function, valid := parseFingerprintToolFunction(functionKey, functions[functionKey], &truncated)
			if !valid {
				return nil, fingerprintMetadataInvalid
			}
			item.Functions = append(item.Functions, function)
			functionCount++
		}
		result = append(result, item)
	}
	if truncated {
		return result, fingerprintMetadataTruncated
	}
	return result, fingerprintMetadataValid
}

func parseFingerprintToolFunction(key string, raw json.RawMessage, truncated *bool) (FingerprintToolFunction, bool) {
	item := FingerprintToolFunction{}
	object, valid := fingerprintMetadataObject(raw)
	if !valid || key == "" || strings.ContainsAny(key, "\x00\r\n") {
		return item, false
	}
	name, valid := fingerprintMetadataName(object["name"], truncated)
	direct, deferred := codexWireBool(object["direct"]), codexWireBool(object["deferred"])
	if !valid || direct == nil || deferred == nil {
		return item, false
	}
	item.Function, item.Name = fingerprintBoundMetadataName(key, truncated), name
	item.Direct, item.Deferred = *direct, *deferred
	if rawCode, present := object["code_mode_name"]; present && string(rawCode) != "null" {
		codeName, valid := fingerprintMetadataName(rawCode, truncated)
		if !valid {
			return item, false
		}
		item.CodeModeName = &codeName
	}
	source, valid := fingerprintMetadataObject(object["source"])
	if !valid || json.Unmarshal(source["kind"], &item.Source.Kind) != nil {
		return item, false
	}
	switch item.Source.Kind {
	case "harness":
	case "mcp":
		item.Source.ServerName, valid = fingerprintMetadataName(source["server_name"], truncated)
		if !valid {
			return item, false
		}
	default:
		return item, false
	}
	return item, true
}

func cloneFingerprintObservationMetadata(entry *FingerprintObservationEntry) {
	if entry.MetadataStatus != nil {
		value := *entry.MetadataStatus
		entry.MetadataStatus = &value
	}
	if entry.HistoryIngestRequested != nil {
		entry.HistoryIngestRequested = boolPointer(*entry.HistoryIngestRequested)
	}
	if entry.Compaction != nil {
		value := *entry.Compaction
		entry.Compaction = &value
	}
	if entry.ToolNamespacesInfo != nil {
		entry.ToolNamespacesInfo = append([]FingerprintToolNamespace{}, entry.ToolNamespacesInfo...)
		for i := range entry.ToolNamespacesInfo {
			item := &entry.ToolNamespacesInfo[i]
			item.Functions = append([]FingerprintToolFunction{}, item.Functions...)
			for j := range item.Functions {
				if item.Functions[j].CodeModeName != nil {
					value := *item.Functions[j].CodeModeName
					item.Functions[j].CodeModeName = &value
				}
			}
		}
	}
}
