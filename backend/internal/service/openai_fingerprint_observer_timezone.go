package service

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// cloneFingerprintObservationHeaders is the only request-header snapshot kept
// by pooled sockets. It is deliberately independent of the observation switch:
// a later frame may enable diagnostics, but credentials must never be retained.
// Preserve key spelling, duplicate values, and slice ownership so diagnostics
// describe the physical handshake rather than a normalized compatibility key.
func cloneFingerprintObservationHeaders(headers http.Header) http.Header {
	if headers == nil {
		return nil
	}
	result := make(http.Header)
	for name, values := range headers {
		switch strings.ToLower(name) {
		case "x-codex-window-id", "x-openai-subagent":
			result[name] = make([]string, len(values))
			for i, value := range values {
				result[name][i] = fingerprintObservationBoundedMetadataString(value)
			}
		case "user-agent", "originator", "openai-beta", "version",
			"x-openai-internal-codex-residency", codexInstallationIDKey,
			"session-id", "session_id", "thread-id", "thread_id",
			"conversation_id", "conversation-id", "x-client-request-id",
			"x-codex-parent-thread-id", "parent-thread-id", "parent_thread_id",
			"x-codex-forked-from-thread-id", "forked-from-thread-id", "forked_from_thread_id":
			result[name] = append([]string(nil), values...)
		case "x-codex-turn-metadata":
			if values == nil {
				result[name] = nil
				continue
			}
			result[name] = make([]string, len(values))
			for i, value := range values {
				result[name][i] = fingerprintObservationSafeTurnMetadata(value)
			}
		}
	}
	return result
}

func fingerprintObservationBoundedMetadataString(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 128 || strings.IndexFunc(value, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return ""
	}
	return value
}

func fingerprintObservationSafeTurnMetadata(value string) string {
	if strings.TrimSpace(value) == "" {
		return value
	}
	var metadata map[string]json.RawMessage
	if json.Unmarshal([]byte(value), &metadata) != nil || metadata == nil {
		// Preserve invalid-carrier semantics without saving arbitrary text, which
		// could contain credentials or request content unrelated to observation.
		return "null"
	}
	safe := make(map[string]any)
	for _, name := range []string{codexTurnMetadataInstallationIDKey, "session_id", "thread_id", "parent_thread_id", "forked_from_thread_id"} {
		if raw, exists := metadata[name]; exists {
			var scalar string
			if json.Unmarshal(raw, &scalar) == nil && strings.TrimSpace(string(raw)) != "null" {
				safe[name] = scalar
			} else {
				// A non-string known field remains invalid; do not copy its nested
				// object/array and accidentally retain an unrelated secret.
				safe[name] = nil
			}
		}
	}
	kind, valid := ParseCodexWireRequestKind(codexWireString(metadata["request_kind"]))
	if !valid {
		kind = CodexWireRequestTurn
	}
	for _, name := range []string{
		"turn_id", "parent_turn_id", "root_turn_id", "window_id", "context_window_id",
		"guardian_classifier_source_thread_id", "agent_name", "subagent_kind", "thread_source",
		"turn_trigger", "sandbox", "sandbox_mode",
	} {
		if raw, present := metadata[name]; present {
			safe[name] = nil
			value := fingerprintObservationBoundedMetadataString(codexWireString(raw))
			switch name {
			case "context_window_id", "guardian_classifier_source_thread_id":
				value = NormalizeFingerprintObservationUUIDv7(value)
			case "turn_id", "parent_turn_id", "root_turn_id":
				id, _ := ResolveCodexTurnID(value, kind)
				value = id.Value
			}
			if value != "" {
				safe[name] = value
			}
		}
	}
	for _, name := range []string{"window_number", "forked_from_ordinal_exclusive"} {
		if number := codexWireUint64(metadata[name]); number != nil {
			safe[name] = *number
		}
	}
	if started, present := codexWireInt64(metadata["turn_started_at_unix_ms"]); present {
		safe["turn_started_at_unix_ms"] = started
	}
	for _, name := range []string{"auto_review_enabled", "node_repl_auto_review_required", "node_repl_disabled"} {
		if enabled := codexWireBool(metadata[name]); enabled != nil {
			safe[name] = *enabled
		}
	}
	if workspaces := displayableCodexWorkspaces(metadata["workspaces"]); len(workspaces) > 0 {
		paths := make(map[string]any, len(workspaces))
		for _, path := range workspaces {
			paths[path] = nil
		}
		safe["workspaces"] = paths
	}
	for key, value := range fingerprintObservationExtraMetadata(parseCodexWireMetadataObject(metadata).ExtraMetadata) {
		safe[key] = value
	}
	copyFingerprintObservationSafeMetadata(safe, metadata)
	encoded, _ := json.Marshal(safe)
	return string(encoded)
}

func cloneFingerprintTimezoneScan(scan *TimezoneScanResult) *TimezoneScanResult {
	if scan == nil {
		return nil
	}
	copy := *scan
	copy.ScanStatus = strings.Clone(copy.ScanStatus)
	if scan.Items != nil {
		copy.Items = append([]TimezoneScanItem{}, scan.Items...)
		for i := range copy.Items {
			item := &copy.Items[i]
			item.Source = strings.Clone(item.Source)
			item.Path = strings.Clone(item.Path)
			item.Value = strings.Clone(item.Value)
			item.CurrentDate = strings.Clone(item.CurrentDate)
			item.Status = strings.Clone(item.Status)
			item.Reason = strings.Clone(item.Reason)
			item.EnvironmentSource = strings.Clone(item.EnvironmentSource)
			item.Location = cloneFingerprintTimezoneLocation(item.Location)
		}
	}
	return &copy
}

// Bounded observations can still be substrings of a complete parsed request.
// Give every retained string its own backing storage before it reaches the
// observation entry, so one small location cannot keep a large body alive.
func cloneFingerprintTimezoneLocation(location *RequestLocationObservation) *RequestLocationObservation {
	if location == nil {
		return nil
	}
	copy := *location
	copy.Type = strings.Clone(copy.Type)
	copy.Country = strings.Clone(copy.Country)
	copy.Region = strings.Clone(copy.Region)
	copy.City = strings.Clone(copy.City)
	copy.Timezone = strings.Clone(copy.Timezone)
	return &copy
}

func cloneFingerprintTimezoneConversions(conversions []TimezoneConversion) []TimezoneConversion {
	if conversions == nil {
		return nil
	}
	result := append([]TimezoneConversion{}, conversions...)
	for i := range result {
		item := &result[i]
		item.Source = strings.Clone(item.Source)
		item.Path = strings.Clone(item.Path)
		item.Original = strings.Clone(item.Original)
		item.Output = strings.Clone(item.Output)
		item.DateBefore = strings.Clone(item.DateBefore)
		item.DateAfter = strings.Clone(item.DateAfter)
		item.Status = strings.Clone(item.Status)
		item.Reason = strings.Clone(item.Reason)
		item.TimeBasis = strings.Clone(item.TimeBasis)
		item.ReceivedAt = strings.Clone(item.ReceivedAt)
		item.EnvironmentSource = strings.Clone(item.EnvironmentSource)
		item.LocationBefore = cloneFingerprintTimezoneLocation(item.LocationBefore)
		item.LocationAfter = cloneFingerprintTimezoneLocation(item.LocationAfter)
	}
	return result
}

func cloneFingerprintEgressLocation(location *OpenAIEgressLocationSnapshot) *OpenAIEgressLocationSnapshot {
	if location == nil {
		return nil
	}
	copy := *location
	copy.RouteKey = strings.Clone(copy.RouteKey)
	copy.RouteType = strings.Clone(copy.RouteType)
	copy.IPAddress = strings.Clone(copy.IPAddress)
	copy.Country = strings.Clone(copy.Country)
	copy.CountryCode = strings.Clone(copy.CountryCode)
	copy.Region = strings.Clone(copy.Region)
	copy.City = strings.Clone(copy.City)
	copy.Timezone = strings.Clone(copy.Timezone)
	copy.Status = strings.Clone(copy.Status)
	copy.Source = strings.Clone(copy.Source)
	copy.Reason = strings.Clone(copy.Reason)
	return &copy
}

func cloneFingerprintObservationEntry(entry FingerprintObservationEntry) FingerprintObservationEntry {
	cloneFingerprintObservationMetadata(&entry)
	if entry.Daybreak != nil {
		copy := *entry.Daybreak
		entry.Daybreak = &copy
	}
	if entry.ResponseEvidence != nil {
		copy := entry.ResponseEvidence.clone()
		entry.ResponseEvidence = &copy
	}
	entry.TimezoneTarget = strings.Clone(entry.TimezoneTarget)
	entry.TimezoneComparisonStatus = strings.Clone(entry.TimezoneComparisonStatus)
	entry.EgressLocation = cloneFingerprintEgressLocation(entry.EgressLocation)
	entry.RequestIntegrity = CloneRequestIntegrityObservation(entry.RequestIntegrity)
	entry.ConversionCheck = cloneOpenAIChatConversionCheck(entry.ConversionCheck)
	entry.InboundTimezoneObservations = cloneFingerprintTimezoneScan(entry.InboundTimezoneObservations)
	entry.OutboundTimezoneObservations = cloneFingerprintTimezoneScan(entry.OutboundTimezoneObservations)
	entry.TimezoneConversions = cloneFingerprintTimezoneConversions(entry.TimezoneConversions)
	return entry
}

func scrubFingerprintObservationEntry(entry *FingerprintObservationEntry) {
	if entry == nil {
		return
	}
	for _, scan := range []*TimezoneScanResult{entry.InboundTimezoneObservations, entry.OutboundTimezoneObservations} {
		if scan != nil {
			clear(scan.Items)
			*scan = TimezoneScanResult{}
		}
	}
	clear(entry.TimezoneConversions)
	clear(entry.ExtraMetadata)
	*entry = FingerprintObservationEntry{}
}

// SetFingerprintObservationTimezonePathMapping records only mappings established
// by a protocol adapter. An empty destination means that adapter removed the
// source. A non-nil mapping marks protocol adaptation: every observed source
// needs an explicit mapping, including unchanged paths. Missing mappings are
// never guessed from array order or equal values.
func SetFingerprintObservationTimezonePathMapping(c *gin.Context, paths map[string]string) {
	if c == nil || !IsFingerprintObservationEnabled() {
		return
	}
	owned := make(map[string]string, len(paths))
	for from, to := range paths {
		owned[from] = to
	}
	c.Set(fingerprintObservationTimezonePathMappingContextKey, owned)
}

func fingerprintObservationHeaderValue(headers http.Header, name string) string {
	var values []string
	for key, candidates := range headers {
		if strings.EqualFold(key, name) {
			values = append(values, candidates...)
		}
	}
	// Join rather than choose the first value: a broken final multi-value header
	// must not be presented as a successfully forced single "us" value.
	return strings.Join(values, ", ")
}

func populateFingerprintObservationTimezones(entry *FingerprintObservationEntry, state *RequestTimezoneState,
	body []byte, paths map[string]string) {
	// All call sites gate before constructing an entry; keep this boundary gated
	// as well so disabling observation never starts a final-body scanner.
	if entry == nil || !IsFingerprintObservationEnabled() {
		return
	}
	entry.TimezoneTarget = OpenAIRequestTimezone
	if state != nil {
		if state.Target.Timezone != "" {
			entry.TimezoneTarget = strings.Clone(state.Target.Timezone)
		}
		if state.EgressLocation != nil {
			entry.EgressLocation = cloneFingerprintEgressLocation(state.EgressLocation)
		}
		entry.InboundTimezoneObservations = cloneFingerprintTimezoneScan(state.Inbound)
		entry.TimezoneConversions = cloneFingerprintTimezoneConversions(state.Conversions)
	}
	if body != nil {
		scan := scanOpenAIRequestTimezonesWithSource(body, state != nil && state.alphaSearch)
		entry.OutboundTimezoneObservations = cloneFingerprintTimezoneScan(&scan.result)
	}
	entry.TimezoneComparisonStatus = compareFingerprintObservationTimezones(entry, paths)
}

func compareFingerprintObservationTimezones(entry *FingerprintObservationEntry, paths map[string]string) string {
	inbound, outbound := entry.InboundTimezoneObservations, entry.OutboundTimezoneObservations
	if inbound == nil || outbound == nil {
		return "not_collected"
	}
	for _, scan := range []*TimezoneScanResult{inbound, outbound} {
		if scan.ScanStatus != "complete" && scan.ScanStatus != "not_applicable" {
			return "incomplete"
		}
	}
	if len(inbound.Items) == 0 && len(outbound.Items) == 0 {
		return "not_applicable"
	}
	result := "matched"
	applicable := false
	matchedOutbound := make(map[int]bool, len(outbound.Items))
	for _, input := range inbound.Items {
		if isReferenceTimezoneEnvironment(input) {
			continue
		}
		applicable = true
		expectedValue, expectedDate := input.Value, input.CurrentDate
		expectedLocation := input.Location
		converted := false
		for _, conversion := range entry.TimezoneConversions {
			if conversion.Source == input.Source && conversion.Path == input.Path && conversion.Status == "converted" {
				expectedValue = conversion.Output
				expectedLocation = conversion.LocationAfter
				converted = true
				if conversion.DateAfter != "" {
					expectedDate = conversion.DateAfter
				}
				break
			}
		}
		destination := input.Path
		mapped, hasMapping := paths[input.Path]
		if hasMapping {
			destination = mapped
		}
		status, reason := "", ""
		if paths != nil && !hasMapping {
			status, reason = "unmatched", "source_path_changed"
		} else if destination == "" {
			status, reason = "not_sent", "adapter_removed_source"
		} else {
			found, sameSource, ambiguous := -1, false, false
			for i, output := range outbound.Items {
				if output.Source != input.Source {
					continue
				}
				sameSource = true
				if output.Path == destination {
					if found != -1 || matchedOutbound[i] {
						ambiguous = true
					}
					found = i
				}
			}
			switch {
			case ambiguous:
				status, reason = "unmatched", "ambiguous_source_mapping"
			case found >= 0:
				matchedOutbound[found] = true
				// Final adapters may remove the metadata that declared the source.
				// Recover its classification only through the frozen explicit
				// provenance mapping; every value still comes from the final scan.
				if hasMapping && input.Source == "environment_context" &&
					(input.EnvironmentSource == TimezoneEnvironmentSourceMetadata || input.EnvironmentSource == TimezoneEnvironmentSourceMapped) {
					if outbound.Items[found].EnvironmentSource != TimezoneEnvironmentSourceMetadata {
						outbound.Items[found].EnvironmentSource = TimezoneEnvironmentSourceMapped
					}
					outbound.Items[found].Current = input.Current
				}
				if input.Source == "environment_context" && input.EnvironmentSource == TimezoneEnvironmentSourceStructuralFallback &&
					(hasMapping || paths == nil) {
					outbound.Items[found].EnvironmentSource = TimezoneEnvironmentSourceStructuralFallback
					outbound.Items[found].Current = input.Current
				}
				actual := outbound.Items[found]
				missingUnchanged := !converted && input.Reason == "location_missing" && actual.Reason == "location_missing"
				if input.Source == "environment_context" && input.Status == "invalid" {
					status, reason = "incomplete", input.Reason
				} else if actual.Source == "environment_context" && actual.Status == "invalid" {
					status, reason = "incomplete", actual.Reason
				} else if !missingUnchanged && ((!converted && input.Status == "invalid" && input.Value == "") || (actual.Status == "invalid" && actual.Value == "")) {
					status, reason = "unmatched", "value_not_observable"
				} else if actual.Value != expectedValue || actual.CurrentDate != expectedDate {
					status, reason = "mismatched", "final_value_differs"
				} else if expectedLocation != nil && (actual.Location == nil || *actual.Location != *expectedLocation) {
					status, reason = "mismatched", "final_location_differs"
				}
			case sameSource:
				status, reason = "unmatched", "source_path_changed"
			default:
				status, reason = "not_sent", "source_not_in_final_body"
			}
		}
		if status != "" {
			result = fingerprintTimezoneComparisonWorse(result, status)
			for i := range entry.TimezoneConversions {
				conversion := &entry.TimezoneConversions[i]
				if conversion.Source == input.Source && conversion.Path == input.Path {
					conversion.Status, conversion.Reason = status, reason
				}
			}
		}
	}
	// New or relocated final sources without an adapter mapping are not evidence
	// that an inbound conversion succeeded, even when they use the target zone.
	for i, output := range outbound.Items {
		if !matchedOutbound[i] && !isReferenceTimezoneEnvironment(output) {
			applicable = true
			result = fingerprintTimezoneComparisonWorse(result, "unmatched")
		}
	}
	if !applicable {
		return "not_applicable"
	}
	return result
}

func isReferenceTimezoneEnvironment(item TimezoneScanItem) bool {
	return item.Source == "environment_context" && item.EnvironmentSource == TimezoneEnvironmentSourceReference
}

func fingerprintTimezoneComparisonWorse(current, candidate string) string {
	priority := map[string]int{"matched": 0, "mismatched": 1, "not_sent": 2, "incomplete": 3, "unmatched": 4}
	if priority[candidate] > priority[current] {
		return candidate
	}
	return current
}
