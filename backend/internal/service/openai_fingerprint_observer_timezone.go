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
	copyFingerprintObservationSafeMetadata(safe, metadata)
	encoded, _ := json.Marshal(safe)
	return string(encoded)
}

func cloneFingerprintTimezoneScan(scan *TimezoneScanResult) *TimezoneScanResult {
	if scan == nil {
		return nil
	}
	copy := *scan
	if scan.Items != nil {
		copy.Items = append([]TimezoneScanItem{}, scan.Items...)
		for i := range copy.Items {
			copy.Items[i].Location = cloneRequestLocation(copy.Items[i].Location)
		}
	}
	return &copy
}

func cloneFingerprintObservationEntry(entry FingerprintObservationEntry) FingerprintObservationEntry {
	cloneFingerprintObservationMetadata(&entry)
	if entry.CodexTurnState != nil {
		copy := *entry.CodexTurnState
		if copy.ExpiresAt != nil {
			expires := *copy.ExpiresAt
			copy.ExpiresAt = &expires
		}
		entry.CodexTurnState = &copy
	}
	if entry.EgressLocation != nil {
		snapshot := *entry.EgressLocation
		entry.EgressLocation = &snapshot
	}
	entry.RequestIntegrity = CloneRequestIntegrityObservation(entry.RequestIntegrity)
	entry.ConversionCheck = cloneOpenAIChatConversionCheck(entry.ConversionCheck)
	entry.InboundTimezoneObservations = cloneFingerprintTimezoneScan(entry.InboundTimezoneObservations)
	entry.OutboundTimezoneObservations = cloneFingerprintTimezoneScan(entry.OutboundTimezoneObservations)
	if entry.TimezoneConversions != nil {
		entry.TimezoneConversions = cloneTimezoneConversions(entry.TimezoneConversions)
	}
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
			entry.TimezoneTarget = state.Target.Timezone
		}
		if state.EgressLocation != nil {
			snapshot := *state.EgressLocation
			entry.EgressLocation = &snapshot
		}
		entry.InboundTimezoneObservations = cloneFingerprintTimezoneScan(state.Inbound)
		entry.TimezoneConversions = cloneTimezoneConversions(state.Conversions)
	}
	if body != nil {
		entry.OutboundTimezoneObservations = &scanOpenAIRequestTimezonesWithSource(body, state != nil && state.alphaSearch).result
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
