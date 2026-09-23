package service

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
	_ "time/tzdata"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	OpenAIRequestTimezone           = "America/Los_Angeles"
	openAIRequestTimezoneTextLimit  = 1 << 20
	openAIRequestTimezoneItemLimit  = 16
	openAIRequestTimezoneValueLimit = 128
	openAIRequestTimezoneNodeLimit  = 16 << 10

	// Environment source classification is separate from parse validity and from
	// current/history. Text mentioning an environment tag is not an environment
	// declaration. A mapped source inherits only provenance, never observed values.
	TimezoneEnvironmentSourceMetadata           = "metadata"
	TimezoneEnvironmentSourceMapped             = "mapped"
	TimezoneEnvironmentSourceReference          = "reference"
	TimezoneEnvironmentSourceStructuralFallback = "structural_fallback"
)

// TimezoneScanResult distinguishes an absent observation from a completed scan
// with no matching fields. Values are deliberately bounded and never contain a
// complete environment message.
type TimezoneScanResult struct {
	ScanStatus string             `json:"scan_status"`
	Items      []TimezoneScanItem `json:"items"`
}

type TimezoneScanItem struct {
	Source            string                      `json:"source"`
	Path              string                      `json:"path"`
	Value             string                      `json:"value"`
	CurrentDate       string                      `json:"current_date,omitempty"`
	Current           bool                        `json:"current"`
	Status            string                      `json:"status,omitempty"`
	Reason            string                      `json:"reason,omitempty"`
	Location          *RequestLocationObservation `json:"location,omitempty"`
	EnvironmentSource string                      `json:"environment_source,omitempty"`
}

// RequestLocationObservation contains only bounded, displayable location fields.
// It never retains unknown location extensions or other request content.
type RequestLocationObservation struct {
	Type     string `json:"type"`
	Country  string `json:"country"`
	Region   string `json:"region"`
	City     string `json:"city"`
	Timezone string `json:"timezone"`
}

func openAIRequestSearchLocation() RequestLocationObservation {
	return RequestLocationObservation{Type: "approximate", Country: "US", Region: "Washington", City: "Seattle", Timezone: OpenAIRequestTimezone}
}

type TimezoneConversion struct {
	Source            string                      `json:"source"`
	Path              string                      `json:"path"`
	Original          string                      `json:"original"`
	Output            string                      `json:"output"`
	DateBefore        string                      `json:"date_before,omitempty"`
	DateAfter         string                      `json:"date_after,omitempty"`
	Status            string                      `json:"status"`
	Reason            string                      `json:"reason,omitempty"`
	TimeBasis         string                      `json:"time_basis,omitempty"`
	ReceivedAt        string                      `json:"received_at,omitempty"`
	LocationBefore    *RequestLocationObservation `json:"location_before,omitempty"`
	LocationAfter     *RequestLocationObservation `json:"location_after,omitempty"`
	LocationAdded     bool                        `json:"location_added,omitempty"`
	EnvironmentSource string                      `json:"environment_source,omitempty"`
}

// RequestTimezoneState retains immutable sources captured at ingress or at the
// first neutral Responses conversion. Attempts project those sources onto their
// frozen egress target; an account's final body is never used as a new baseline.
type RequestTimezoneState struct {
	Policy               openai.RequestPolicy
	AcceptedAt           time.Time
	Inbound              *TimezoneScanResult
	Conversions          []TimezoneConversion
	preparedBody         []byte
	patches              []requestTimezoneBodyPatch
	alphaSearch          bool
	Target               RequestLocationObservation
	EgressLocation       *OpenAIEgressLocationSnapshot
	projectionSources    []requestTimezoneProjectionSource
	projectionScanStatus string
	passthrough          bool
}

// A source retains ingress eligibility and offsets. Retargeting never scans an
// account-adapted body, and replay history keeps its original semantic boundary.
type requestTimezoneProjectionSource struct {
	occurrence      requestTimezoneOccurrence
	acceptedAt      time.Time
	toolType        string
	fixedDate       string
	scanStatus      string
	enabled         bool
	containerExists bool
}

type requestTimezoneBodyPatch struct {
	path, original, prepared string // Canonical JSON, including string quotes.
	originalExists           bool
	containerPath            string
	toolType                 string
	containerExists          bool
}

func (s *RequestTimezoneState) PreparedBody() []byte {
	if s == nil {
		return nil
	}
	return bytes.Clone(s.preparedBody)
}

// ApplyToBody reuses only frozen content patches, preserving later model/account
// adaptation. A changed or missing source path rejects the entire patch group;
// callers must never rescan the adapted body to select another environment.
func (s *RequestTimezoneState) ApplyToBody(body []byte) ([]byte, bool) {
	if s == nil || len(s.patches) == 0 {
		return bytes.Clone(body), true
	}
	if !gjson.ValidBytes(body) {
		return bytes.Clone(body), false
	}
	for _, patch := range s.patches {
		if patch.containerPath != "" {
			container := gjson.GetBytes(body, patch.containerPath)
			if patch.toolType != "" {
				if !gjson.GetBytes(body, "tools").IsArray() || !container.IsObject() || container.Get("type").String() != patch.toolType {
					return bytes.Clone(body), false
				}
			} else if container.Exists() && !container.IsObject() {
				return bytes.Clone(body), false
			}
		}
		value := gjson.GetBytes(body, patch.path)
		if !value.Exists() && !patch.originalExists {
			continue
		}
		canonical, ok := canonicalRequestTimezonePatch(value)
		if !ok || (canonical != patch.original && canonical != patch.prepared) {
			return bytes.Clone(body), false
		}
	}
	output := bytes.Clone(body)
	for _, patch := range s.patches {
		var err error
		output, err = sjson.SetRawBytes(output, patch.path, []byte(patch.prepared))
		if err != nil {
			return bytes.Clone(body), false
		}
	}
	return output, true
}

func canonicalRequestTimezonePatch(value gjson.Result) (string, bool) {
	if !value.Exists() || len(value.Raw) > openAIRequestTimezoneTextLimit {
		return "", false
	}
	budget := openAIRequestTimezoneProvenanceBudget{}
	canonical, err := canonicalOpenAIRequestTimezoneProvenanceJSON([]byte(value.Raw), &budget)
	return string(canonical), err == nil
}

func CloneRequestTimezoneState(s *RequestTimezoneState) *RequestTimezoneState {
	if s == nil {
		return nil
	}
	copy := *s
	copy.preparedBody = bytes.Clone(s.preparedBody)
	copy.patches = append([]requestTimezoneBodyPatch(nil), s.patches...)
	copy.Conversions = cloneTimezoneConversions(s.Conversions)
	copy.Inbound = cloneFingerprintTimezoneScan(s.Inbound)
	copy.projectionSources = append([]requestTimezoneProjectionSource(nil), s.projectionSources...)
	for i := range copy.projectionSources {
		copy.projectionSources[i].occurrence.item.Location = cloneRequestLocation(s.projectionSources[i].occurrence.item.Location)
	}
	if s.EgressLocation != nil {
		value := *s.EgressLocation
		copy.EgressLocation = &value
	}
	return &copy
}

var openAIRequestTimezoneLocation struct {
	sync.Once
	location *time.Location
	err      error
}

// InitializeOpenAIRequestTimezone is called at startup. The embedded IANA
// database also makes this work in minimal images without altering time.Local.
func InitializeOpenAIRequestTimezone() error {
	openAIRequestTimezoneLocation.Do(func() {
		openAIRequestTimezoneLocation.location, openAIRequestTimezoneLocation.err = time.LoadLocation(OpenAIRequestTimezone)
	})
	if openAIRequestTimezoneLocation.err != nil {
		return fmt.Errorf("load OpenAI request timezone %q: %w", OpenAIRequestTimezone, openAIRequestTimezoneLocation.err)
	}
	return nil
}

type requestTimezoneOccurrence struct {
	item               TimezoneScanItem
	text               string
	environment        bool
	zoneStart, zoneEnd int
	dateStart, dateEnd int
	hasDate            bool
	eligible           bool
	currentBoundary    bool
	locationPath       string
	locationRaw        string
	locationPresent    bool
}

type requestTimezoneScanner struct {
	result             TimezoneScanResult
	occurrences        []requestTimezoneOccurrence
	textBytes          int
	nodes              int
	structuralFallback bool
}

// ScanOpenAIRequestTimezones only inspects the supplied bytes; it never applies
// conversion or substitutes the configured target for an actual outbound value.
func ScanOpenAIRequestTimezones(body []byte) *TimezoneScanResult {
	scan := scanOpenAIRequestTimezones(body)
	return &scan.result
}

// PrepareOpenAIRequestTimezone takes its clock from ingress. It is deterministic
// for a given body, policy and acceptedAt, including during failover at midnight.
func PrepareOpenAIRequestTimezone(body []byte, policy openai.RequestPolicy, acceptedAt time.Time, passthrough, observe bool) ([]byte, *RequestTimezoneState) {
	return prepareOpenAIRequestTimezoneBody(body, policy, acceptedAt, passthrough, observe, false)
}

// alphaSearch is an explicit ingress classification, not a guess based on JSON
// shape. Only a search_query may create an absent standalone search location.
func prepareOpenAIRequestTimezoneBody(body []byte, policy openai.RequestPolicy, acceptedAt time.Time, passthrough, observe, alphaSearch bool) ([]byte, *RequestTimezoneState) {
	state := &RequestTimezoneState{Policy: policy, AcceptedAt: acceptedAt, preparedBody: bytes.Clone(body), alphaSearch: alphaSearch, Target: openAIRequestSearchLocation(), passthrough: passthrough}
	enabled := policy.TimezoneConversionEnabled && (!passthrough || policy.PassthroughTimezoneConversionEnabled)
	if !enabled && !observe {
		return bytes.Clone(body), state
	}
	scan := scanOpenAIRequestTimezoneIngress(body, alphaSearch)
	state.projectionScanStatus = scan.result.ScanStatus
	for _, occurrence := range scan.occurrences {
		source := requestTimezoneProjectionSource{occurrence: occurrence, acceptedAt: acceptedAt, scanStatus: scan.result.ScanStatus, enabled: enabled}
		source.occurrence.item.Location = cloneRequestLocation(occurrence.item.Location)
		source.containerExists = gjson.GetBytes(body, strings.TrimSuffix(occurrence.locationPath, ".user_location")).Exists()
		if strings.HasPrefix(occurrence.locationPath, "tools.") {
			source.toolType = gjson.GetBytes(body, strings.TrimSuffix(occurrence.locationPath, ".user_location")+".type").String()
		}
		state.projectionSources = append(state.projectionSources, source)
	}
	if observe {
		state.Inbound = &scan.result
	}
	state.buildTargetProjection()
	prepared, ok := state.ApplyToBody(body)
	if !ok {
		return bytes.Clone(body), state
	}
	state.preparedBody = prepared
	return bytes.Clone(prepared), state
}

func (state *RequestTimezoneState) buildTargetProjection() {
	state.patches = nil
	state.Conversions = nil
	location, locationErr := time.LoadLocation(state.Target.Timezone)
	for _, source := range state.projectionSources {
		occurrence, acceptedAt := source.occurrence, source.acceptedAt
		report := newTimezoneConversion(occurrence.item)
		switch {
		case source.scanStatus != "complete":
			report.Status, report.Reason = "skipped", "scan_"+source.scanStatus
		case occurrence.environment && !occurrence.eligible:
			// Ordinary quoted text is not a conversion source, even when its tags
			// happen to be well formed. Do not report it as a disabled real source.
			report.Status, report.Reason = "skipped", "environment_metadata_missing"
		case !source.enabled:
			report.Status, report.Reason = "disabled", "conversion_disabled"
		case !occurrence.eligible:
			report.Status, report.Reason = "skipped", "environment_metadata_missing"
			if !occurrence.environment {
				report.Reason = "standalone_search_source_required"
				if occurrence.item.Reason == "location_container_not_object" {
					report.Reason = occurrence.item.Reason
				}
			}
		case occurrence.environment && occurrence.item.Status != "valid":
			report.Status, report.Reason = "skipped", occurrence.item.Reason
		case locationErr != nil:
			report.Status, report.Reason = "skipped", "target_timezone_unavailable"
		case occurrence.hasDate && occurrence.item.Current && acceptedAt.IsZero():
			report.Status, report.Reason = "skipped", "accepted_at_unavailable"
		default:
			patch := requestTimezoneBodyPatch{path: occurrence.item.Path, originalExists: true}
			if occurrence.environment {
				output := occurrence.text
				// Apply text replacements backwards so both offsets describe the
				// original environment block, regardless of tag ordering.
				replacements := []timezoneTextReplacement{{occurrence.zoneStart, occurrence.zoneEnd, state.Target.Timezone}}
				// History shares the target timezone but retains its recorded date.
				// Only the frozen current candidate uses this request's ingress date.
				if occurrence.hasDate && occurrence.item.Current {
					report.DateAfter = acceptedAt.In(location).Format("2006-01-02")
					report.TimeBasis = "gateway_received_at"
					report.ReceivedAt = acceptedAt.UTC().Format(time.RFC3339Nano)
					replacements = append(replacements, timezoneTextReplacement{occurrence.dateStart, occurrence.dateEnd, report.DateAfter})
				} else if occurrence.hasDate && source.fixedDate != "" {
					report.DateAfter = source.fixedDate
					replacements = append(replacements, timezoneTextReplacement{occurrence.dateStart, occurrence.dateEnd, source.fixedDate})
				}
				if len(replacements) == 2 && replacements[0].start < replacements[1].start {
					replacements[0], replacements[1] = replacements[1], replacements[0]
				}
				for _, replacement := range replacements {
					output = output[:replacement.start] + replacement.value + output[replacement.end:]
				}
				original, _ := json.Marshal(occurrence.text)
				preparedText, _ := json.Marshal(output)
				patch.original, patch.prepared = string(original), string(preparedText)
			} else {
				location := state.Target
				encoded, _ := json.Marshal(location)
				canonical, _ := canonicalRequestTimezonePatch(gjson.ParseBytes(encoded))
				patch.path, patch.originalExists = occurrence.locationPath, occurrence.locationPresent
				patch.original, patch.prepared = occurrence.locationRaw, canonical
				patch.containerPath = strings.TrimSuffix(occurrence.locationPath, ".user_location")
				patch.containerExists = source.containerExists
				if strings.HasPrefix(patch.containerPath, "tools.") {
					patch.toolType = source.toolType
				}
				report.LocationAfter = &location
				report.LocationAdded = !occurrence.locationPresent
			}
			// Track even an unchanged source: another account may have a different
			// egress timezone and must still project this frozen source.
			state.patches = append(state.patches, patch)
			report.Output = state.Target.Timezone
			report.Status, report.Reason = "converted", "timezone_converted"
			if occurrence.environment && !occurrence.item.Current {
				report.Reason = "historical_timezone_converted"
			}
			if !occurrence.environment {
				report.Reason = "location_normalized"
				if report.LocationAdded {
					report.Reason = "location_added"
				}
			}
			if patch.originalExists && patch.original == patch.prepared {
				report.Status, report.Reason = "unchanged", "already_target"
			}
		}
		state.Conversions = append(state.Conversions, report)
	}
}

type timezoneTextReplacement struct {
	start, end int
	value      string
}

func newTimezoneConversion(item TimezoneScanItem) TimezoneConversion {
	return TimezoneConversion{Source: item.Source, Path: item.Path, Original: item.Value, Output: item.Value, DateBefore: item.CurrentDate, DateAfter: item.CurrentDate, LocationBefore: cloneRequestLocation(item.Location), LocationAfter: cloneRequestLocation(item.Location), EnvironmentSource: item.EnvironmentSource}
}

func scanOpenAIRequestTimezones(body []byte) *requestTimezoneScanner {
	return scanOpenAIRequestTimezonesWithSource(body, false)
}

func scanOpenAIRequestTimezonesWithSource(body []byte, alphaSearch bool) *requestTimezoneScanner {
	return scanOpenAIRequestTimezonesWithOptions(body, alphaSearch, false)
}

// Structural eligibility is decided only on frozen ingress bytes. Final scans
// must not grant new eligibility after an adapter removed conflicting metadata.
func scanOpenAIRequestTimezoneIngress(body []byte, alphaSearch bool) *requestTimezoneScanner {
	return scanOpenAIRequestTimezonesWithOptions(body, alphaSearch, true)
}

func scanOpenAIRequestTimezonesWithOptions(body []byte, alphaSearch, structuralFallback bool) *requestTimezoneScanner {
	s := &requestTimezoneScanner{result: TimezoneScanResult{ScanStatus: "complete", Items: []TimezoneScanItem{}}}
	if !gjson.ValidBytes(body) {
		s.result.ScanStatus = "parse_failed"
		return s
	}
	root := gjson.ParseBytes(body)
	if !root.IsObject() {
		s.result.ScanStatus = "not_applicable"
		return s
	}
	s.structuralFallback = structuralFallback && requestTimezoneEnvironmentMetadataBlocker(root) == ""
	input := root.Get("input")
	if input.Exists() {
		if input.Type == gjson.String {
			s.scanText(input.String(), "input", false, false)
		} else if input.IsArray() {
			s.scanMessages(input, "input")
		}
	} else if messages := root.Get("messages"); messages.IsArray() {
		s.scanMessages(messages, "messages")
	}
	if s.result.ScanStatus == "complete" {
		if tools := root.Get("tools"); tools.IsArray() {
			index := 0
			tools.ForEach(func(_, tool gjson.Result) bool {
				if !s.countNode() {
					return false
				}
				typeName := tool.Get("type").String()
				if isRequestTimezoneWebSearchType(typeName) {
					s.scanSearchLocation(tool.Get("user_location"), fmt.Sprintf("tools.%d.user_location", index), true)
				}
				index++
				return s.result.ScanStatus == "complete"
			})
		}
	}
	// Never infer the endpoint from a payload's shape. A pure time query retains
	// its requested offset and does not acquire a search location.
	location := root.Get("settings.user_location")
	queries := root.Get("commands.search_query")
	addAlphaLocation := alphaSearch && queries.IsArray() && queries.Get("#").Int() > 0
	if s.result.ScanStatus == "complete" && (location.Exists() || addAlphaLocation) && s.countNode() {
		settings := root.Get("settings")
		if alphaSearch && settings.Exists() && !settings.IsObject() {
			s.add(requestTimezoneOccurrence{locationPath: "settings.user_location", item: TimezoneScanItem{Source: "web_search", Path: "settings.user_location.timezone", Current: true, Status: "invalid", Reason: "location_container_not_object"}})
		} else {
			s.scanSearchLocation(location, "settings.user_location", alphaSearch)
		}
	}
	for _, occurrence := range s.occurrences {
		s.result.Items = append(s.result.Items, occurrence.item)
	}
	return s
}

func isRequestTimezoneWebSearchType(name string) bool {
	if name == "web_search" || name == "web_search_preview" {
		return true
	}
	for _, prefix := range []string{"web_search_preview_", "web_search_"} {
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		suffix := strings.TrimPrefix(name, prefix)
		for _, layout := range []string{"20060102", "2006_01_02"} {
			if parsed, err := time.Parse(layout, suffix); err == nil && parsed.Format(layout) == suffix {
				return true
			}
		}
	}
	return false
}

func (s *requestTimezoneScanner) scanMessages(messages gjson.Result, path string) {
	lastCurrent := -1
	// A single forward pass discovers the final user streak without allocating
	// the full history array. Encountering a non-user retires its last candidate.
	messages.ForEach(func(key, message gjson.Result) bool {
		if !s.countNode() {
			return false
		}
		i := key.Int()
		content := message.Get("content")
		current := message.Get("role").String() == "user"
		if !current && lastCurrent >= 0 {
			s.markHistorical(lastCurrent)
			lastCurrent = -1
		}
		before := len(s.occurrences)
		if content.Type == gjson.String {
			s.scanText(content.String(), fmt.Sprintf("%s.%d.content", path, i), false, false)
		} else if content.IsArray() {
			kinds := message.Get("internal_chat_message_metadata_passthrough.content_item_kinds")
			content.ForEach(func(key, part gjson.Result) bool {
				if !s.countNode() {
					return false
				}
				kind := part.Get("type").String()
				marker := kinds.Get(fmt.Sprintf("%d", key.Int()))
				eligible := current && kind == "input_text" && kinds.IsArray() && marker.Type == gjson.String && marker.String() == "environments.environment_context"
				text := part.Get("text")
				if text.Type == gjson.String {
					if kind == "" || kind == "text" || kind == "input_text" {
						beforeText := len(s.occurrences)
						s.scanText(text.String(), fmt.Sprintf("%s.%d.content.%d.text", path, i, key.Int()), eligible, eligible)
						if s.structuralFallback && path == "input" && current && kind == "input_text" &&
							!hasRequestTimezoneEnvironmentMetadata(message, part) && len(s.occurrences) > beforeText {
							occurrence := &s.occurrences[len(s.occurrences)-1]
							trimmed := strings.TrimSpace(occurrence.text)
							if occurrence.item.Status == "valid" && occurrence.hasDate {
								occurrence.eligible = true
								occurrence.item.Current = true
								occurrence.item.EnvironmentSource = TimezoneEnvironmentSourceStructuralFallback
							} else if strings.HasPrefix(trimmed, "<environment_context>") &&
								(countRequestTimezoneTag(trimmed, "environment_context", true) == 0 || strings.HasSuffix(trimmed, "</environment_context>")) {
								// An unusable trailing environment cannot make an older one
								// current. Prose before or after a complete block is a reference,
								// not a boundary that retires the earlier current environment.
								occurrence.currentBoundary = true
							}
						}
					} else {
						s.countText(text.String())
					}
				} else if eligible {
					// An explicitly declared but missing/non-string environment must
					// remain an invalid source, not disappear from the observation.
					s.scanText("", fmt.Sprintf("%s.%d.content.%d.text", path, i, key.Int()), true, true)
				}
				return s.result.ScanStatus == "complete"
			})
		}
		candidate := -1
		for j := before; j < len(s.occurrences); j++ {
			if s.occurrences[j].eligible || s.occurrences[j].currentBoundary {
				candidate = j
			}
		}
		if current && candidate >= 0 {
			if lastCurrent >= 0 {
				s.markHistorical(lastCurrent)
			}
			// Only the final candidate in the tail is current, even if it is
			// malformed or quoted; never refresh an earlier block's date instead.
			for j := before; j < candidate; j++ {
				s.markHistorical(j)
			}
			lastCurrent = candidate
			if s.occurrences[candidate].currentBoundary {
				s.markHistorical(candidate)
				lastCurrent = -1
			}
		}
		return s.result.ScanStatus == "complete"
	})
}

// Only a labeling field or an invalid metadata container blocks the missing-tag
// fallback. Normal metadata objects can contain unrelated information, such as
// executed_tool_calls, without declaring any content kind. Explicit null, empty,
// malformed or misplaced content_item_kinds still remain authoritative.
func requestTimezoneEnvironmentMetadataBlocker(object gjson.Result) string {
	if object.Get("content_item_kinds").Exists() {
		return "content_item_kinds_present"
	}
	for _, path := range []string{"internal_chat_message_metadata_passthrough", "metadata"} {
		metadata := object.Get(path)
		if !metadata.Exists() {
			continue
		}
		if !metadata.IsObject() {
			return "metadata_invalid"
		}
		if metadata.Get("content_item_kinds").Exists() {
			return "content_item_kinds_present"
		}
	}
	return ""
}

func hasRequestTimezoneEnvironmentMetadata(message, part gjson.Result) bool {
	return requestTimezoneEnvironmentMetadataBlocker(message) != "" || requestTimezoneEnvironmentMetadataBlocker(part) != ""
}

func (s *requestTimezoneScanner) countNode() bool {
	if s.result.ScanStatus != "complete" {
		return false
	}
	if s.nodes >= openAIRequestTimezoneNodeLimit {
		s.result.ScanStatus = "limited"
		return false
	}
	s.nodes++
	return true
}

func (s *requestTimezoneScanner) markHistorical(index int) {
	s.occurrences[index].item.Current = false
}

func (s *requestTimezoneScanner) countText(value string) bool {
	if len(value) > openAIRequestTimezoneTextLimit-s.textBytes {
		s.result.ScanStatus = "limited"
		return false
	}
	s.textBytes += len(value)
	return true
}

func (s *requestTimezoneScanner) add(occurrence requestTimezoneOccurrence) {
	if len(s.occurrences) >= openAIRequestTimezoneItemLimit {
		s.result.ScanStatus = "limited"
		return
	}
	s.occurrences = append(s.occurrences, occurrence)
}

func (s *requestTimezoneScanner) scanText(text, path string, current, eligible bool) {
	if !s.countText(text) {
		return
	}
	if !eligible && countRequestTimezoneTag(text, "environment_context", false) == 0 && countRequestTimezoneTag(text, "environment_context", true) == 0 {
		return
	}
	source := TimezoneEnvironmentSourceReference
	if eligible {
		source = TimezoneEnvironmentSourceMetadata
	}
	occurrence := requestTimezoneOccurrence{environment: true, eligible: eligible, text: text, item: TimezoneScanItem{Source: "environment_context", Path: path, Current: current, Status: "invalid", Reason: "environment_not_standalone", EnvironmentSource: source}}
	trimmed := strings.TrimSpace(text)
	const open, close = "<environment_context>", "</environment_context>"
	if !strings.HasPrefix(trimmed, open) || !strings.HasSuffix(trimmed, close) || countRequestTimezoneTag(text, "environment_context", false) != 1 || countRequestTimezoneTag(text, "environment_context", true) != 1 {
		s.add(occurrence)
		return
	}
	if strings.Contains(text, "```") || strings.Contains(text, "~~~") {
		s.add(occurrence)
		return
	}
	// Textual tag offsets must never refer to XML comments or CDATA examples.
	// Reject the entire candidate instead of changing quoted text or its date.
	if strings.Contains(text, "<!--") || strings.Contains(text, "<![CDATA[") {
		occurrence.item.Reason = "quoted_xml_content"
		s.add(occurrence)
		return
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), ">") {
			s.add(occurrence)
			return
		}
	}
	if !s.validEnvironmentXML(trimmed) {
		occurrence.item.Reason = "malformed_or_duplicate_tags"
		s.add(occurrence)
		return
	}
	zoneStart, zoneEnd, zonePresent, zoneValid := timezoneTagRange(text, "timezone")
	dateStart, dateEnd, datePresent, dateValid := timezoneTagRange(text, "current_date")
	if !zoneValid || !dateValid {
		occurrence.item.Reason = "malformed_or_duplicate_tags"
		s.add(occurrence)
		return
	}
	if datePresent && len(strings.TrimSpace(text[dateStart:dateEnd])) <= 10 {
		occurrence.item.CurrentDate = strings.TrimSpace(text[dateStart:dateEnd])
	}
	if !zonePresent {
		occurrence.item.Reason = "timezone_missing"
		s.add(occurrence)
		return
	}
	zone := text[zoneStart:zoneEnd]
	if len(zone) > openAIRequestTimezoneValueLimit {
		s.result.ScanStatus = "limited"
		return
	}
	occurrence.item.Value = zone
	if !validRequestTimezone(zone) {
		occurrence.item.Reason = "invalid_timezone"
		s.add(occurrence)
		return
	}
	if datePresent {
		date := text[dateStart:dateEnd]
		parsed, err := time.Parse("2006-01-02", date)
		if err != nil || parsed.Format("2006-01-02") != date || parsed.Year() < 1 {
			occurrence.item.Reason = "invalid_current_date"
			s.add(occurrence)
			return
		}
	}
	occurrence.zoneStart, occurrence.zoneEnd, occurrence.dateStart, occurrence.dateEnd, occurrence.hasDate = zoneStart, zoneEnd, dateStart, dateEnd, datePresent
	occurrence.item.Status, occurrence.item.Reason = "valid", ""
	s.add(occurrence)
}

func (s *requestTimezoneScanner) validEnvironmentXML(text string) bool {
	// The scanner owns the cumulative text/node budgets. Also bound this
	// individual parser so a future direct caller cannot bypass the text limit.
	if len(text) > openAIRequestTimezoneTextLimit {
		s.result.ScanStatus = "limited"
		return false
	}
	decoder := xml.NewDecoder(io.LimitReader(strings.NewReader(text), openAIRequestTimezoneTextLimit))
	depth := 0
	children := map[string]bool{}
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return depth == 0
		}
		if err != nil {
			return false
		}
		if !s.countNode() {
			return false
		}
		switch token := token.(type) {
		case xml.StartElement:
			depth++
			if depth == 2 {
				if children[token.Name.Local] {
					return false
				}
				children[token.Name.Local] = true
			}
			if (token.Name.Local == "timezone" || token.Name.Local == "current_date") && depth != 2 {
				return false
			}
		case xml.EndElement:
			depth--
		case xml.Comment, xml.Directive, xml.ProcInst:
			return false
		}
	}
}

// timezoneTagRange returns offsets into the original text without reserializing
// any unrelated environment fields. Both missing tags are allowed; half-tags,
// attributes, nested content and duplicate target tags are not.
func timezoneTagRange(text, name string) (start, end int, present, valid bool) {
	open, close := "<"+name+">", "</"+name+">"
	opens, closes := countRequestTimezoneTag(text, name, false), countRequestTimezoneTag(text, name, true)
	if opens == 0 && closes == 0 {
		return 0, 0, false, true
	}
	if opens != 1 || closes != 1 {
		return 0, 0, true, false
	}
	start, end = strings.Index(text, open), strings.Index(text, close)
	if start < 0 || end < start+len(open) {
		return 0, 0, true, false
	}
	start += len(open)
	if strings.ContainsAny(text[start:end], "<>") {
		return 0, 0, true, false
	}
	return start, end, true, true
}

func countRequestTimezoneTag(text, name string, closing bool) int {
	prefix := "<" + name
	if closing {
		prefix = "</" + name
	}
	count := 0
	for {
		index := strings.Index(text, prefix)
		if index < 0 {
			return count
		}
		end := index + len(prefix)
		if end == len(text) || strings.ContainsRune(">/ \t\r\n", rune(text[end])) {
			count++
		}
		text = text[end:]
	}
}

func (s *requestTimezoneScanner) scanSearchLocation(location gjson.Result, path string, eligible bool) {
	occurrence := requestTimezoneOccurrence{eligible: eligible, locationPath: path, locationPresent: location.Exists(), item: TimezoneScanItem{Source: "web_search", Path: path + ".timezone", Current: true, Status: "invalid"}}
	if location.Exists() {
		if !s.countText(location.Raw) {
			return
		}
		budget := openAIRequestTimezoneProvenanceBudget{nodes: s.nodes, textBytes: s.textBytes}
		canonical, err := canonicalOpenAIRequestTimezoneProvenanceJSON([]byte(location.Raw), &budget)
		s.nodes = budget.nodes
		if err != nil {
			s.result.ScanStatus = "limited"
			return
		}
		occurrence.locationRaw = string(canonical)
	}
	if location.IsObject() {
		observed := &RequestLocationObservation{}
		for _, field := range []struct {
			name   string
			target *string
		}{
			{"type", &observed.Type}, {"country", &observed.Country}, {"region", &observed.Region}, {"city", &observed.City}, {"timezone", &observed.Timezone},
		} {
			value := location.Get(field.name)
			if value.Type == gjson.String {
				if len(value.String()) > openAIRequestTimezoneValueLimit {
					s.result.ScanStatus = "limited"
					return
				}
				*field.target = value.String()
			}
		}
		occurrence.item.Location = observed
	}
	value := location.Get("timezone")
	if !value.Exists() {
		occurrence.item.Reason = "timezone_missing"
		if !location.Exists() {
			occurrence.item.Reason = "location_missing"
		}
	} else if value.Type == gjson.Null {
		occurrence.item.Reason = "timezone_null"
	} else if value.Type != gjson.String {
		occurrence.item.Reason = "timezone_not_string"
	} else {
		zone := value.String()
		if len(zone) > openAIRequestTimezoneValueLimit {
			s.result.ScanStatus = "limited"
			return
		}
		occurrence.item.Value = zone
		if validRequestTimezone(zone) {
			occurrence.item.Status = "valid"
		} else {
			occurrence.item.Reason = "invalid_timezone"
		}
	}
	s.add(occurrence)
}

func cloneRequestLocation(location *RequestLocationObservation) *RequestLocationObservation {
	if location == nil {
		return nil
	}
	copy := *location
	return &copy
}

func cloneTimezoneConversions(conversions []TimezoneConversion) []TimezoneConversion {
	if conversions == nil {
		return nil
	}
	result := append([]TimezoneConversion{}, conversions...)
	for i := range result {
		result[i].LocationBefore = cloneRequestLocation(result[i].LocationBefore)
		result[i].LocationAfter = cloneRequestLocation(result[i].LocationAfter)
	}
	return result
}

func validRequestTimezone(zone string) bool {
	if zone == "" || strings.TrimSpace(zone) != zone || len(zone) > openAIRequestTimezoneValueLimit || (zone != "UTC" && !strings.Contains(zone, "/")) {
		return false
	}
	_, err := time.LoadLocation(zone)
	return err == nil
}
