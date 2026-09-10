package service

import (
	"bytes"
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
)

// TimezoneScanResult distinguishes an absent observation from a completed scan
// with no matching fields. Values are deliberately bounded and never contain a
// complete environment message.
type TimezoneScanResult struct {
	ScanStatus string             `json:"scan_status"`
	Items      []TimezoneScanItem `json:"items"`
}

type TimezoneScanItem struct {
	Source      string `json:"source"`
	Path        string `json:"path"`
	Value       string `json:"value"`
	CurrentDate string `json:"current_date,omitempty"`
	Current     bool   `json:"current"`
	Status      string `json:"status,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

type TimezoneConversion struct {
	Source     string `json:"source"`
	Path       string `json:"path"`
	Original   string `json:"original"`
	Output     string `json:"output"`
	DateBefore string `json:"date_before,omitempty"`
	DateAfter  string `json:"date_after,omitempty"`
	Status     string `json:"status"`
	Reason     string `json:"reason,omitempty"`
	TimeBasis  string `json:"time_basis,omitempty"`
	ReceivedAt string `json:"received_at,omitempty"`
}

// RequestTimezoneState is frozen at ingress, before account-specific adaptation.
// Retries reuse PreparedBody; it is never a snapshot of the account's final body.
type RequestTimezoneState struct {
	Policy       openai.RequestPolicy
	AcceptedAt   time.Time
	Inbound      *TimezoneScanResult
	Conversions  []TimezoneConversion
	preparedBody []byte
	patches      []requestTimezoneBodyPatch
}

type requestTimezoneBodyPatch struct{ path, original, prepared string }

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
		value := gjson.GetBytes(body, patch.path)
		if value.Type != gjson.String || (value.String() != patch.original && value.String() != patch.prepared) {
			return bytes.Clone(body), false
		}
	}
	output := bytes.Clone(body)
	for _, patch := range s.patches {
		var err error
		output, err = sjson.SetBytes(output, patch.path, patch.prepared)
		if err != nil {
			return bytes.Clone(body), false
		}
	}
	return output, true
}

func CloneRequestTimezoneState(s *RequestTimezoneState) *RequestTimezoneState {
	if s == nil {
		return nil
	}
	copy := *s
	copy.preparedBody = bytes.Clone(s.preparedBody)
	copy.patches = append([]requestTimezoneBodyPatch(nil), s.patches...)
	copy.Conversions = append([]TimezoneConversion(nil), s.Conversions...)
	if s.Inbound != nil {
		inbound := *s.Inbound
		inbound.Items = append([]TimezoneScanItem{}, s.Inbound.Items...)
		copy.Inbound = &inbound
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
}

type requestTimezoneScanner struct {
	result      TimezoneScanResult
	occurrences []requestTimezoneOccurrence
	textBytes   int
	nodes       int
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
	state := &RequestTimezoneState{Policy: policy, AcceptedAt: acceptedAt, preparedBody: bytes.Clone(body)}
	enabled := policy.TimezoneConversionEnabled && (!passthrough || policy.PassthroughTimezoneConversionEnabled)
	if !enabled && !observe {
		return bytes.Clone(body), state
	}
	scan := scanOpenAIRequestTimezones(body)
	if observe {
		state.Inbound = &scan.result
	}
	if scan.result.ScanStatus != "complete" {
		for _, occurrence := range scan.occurrences {
			report := newTimezoneConversion(occurrence.item)
			report.Status, report.Reason = "skipped", "scan_"+scan.result.ScanStatus
			state.Conversions = append(state.Conversions, report)
		}
		return bytes.Clone(body), state
	}
	locationErr := InitializeOpenAIRequestTimezone()
	prepared := state.preparedBody
	for _, occurrence := range scan.occurrences {
		report := newTimezoneConversion(occurrence.item)
		switch {
		case !enabled:
			report.Status, report.Reason = "disabled", "conversion_disabled"
		case occurrence.item.Status != "valid":
			report.Status, report.Reason = "skipped", occurrence.item.Reason
		case occurrence.environment && !occurrence.item.Current:
			report.Status, report.Reason = "skipped", "historical"
		case locationErr != nil:
			report.Status, report.Reason = "skipped", "target_timezone_unavailable"
		case occurrence.hasDate && acceptedAt.IsZero():
			report.Status, report.Reason = "skipped", "accepted_at_unavailable"
		default:
			output := OpenAIRequestTimezone
			if occurrence.environment {
				output = occurrence.text
				// Apply text replacements backwards so both offsets describe the
				// original environment block, regardless of tag ordering.
				replacements := []timezoneTextReplacement{{occurrence.zoneStart, occurrence.zoneEnd, OpenAIRequestTimezone}}
				if occurrence.hasDate {
					report.DateAfter = acceptedAt.In(openAIRequestTimezoneLocation.location).Format("2006-01-02")
					report.TimeBasis = "gateway_received_at"
					report.ReceivedAt = acceptedAt.UTC().Format(time.RFC3339Nano)
					replacements = append(replacements, timezoneTextReplacement{occurrence.dateStart, occurrence.dateEnd, report.DateAfter})
				}
				if len(replacements) == 2 && replacements[0].start < replacements[1].start {
					replacements[0], replacements[1] = replacements[1], replacements[0]
				}
				for _, replacement := range replacements {
					output = output[:replacement.start] + replacement.value + output[replacement.end:]
				}
			}
			originalValue := occurrence.item.Value
			if occurrence.environment {
				originalValue = occurrence.text
			}
			var err error
			if originalValue != output {
				prepared, err = sjson.SetBytes(prepared, occurrence.item.Path, output)
			}
			if err != nil {
				// A partial patch must not escape. This is defensive: all paths
				// originate in the same valid JSON parsed by this scanner.
				for i := range state.Conversions {
					state.Conversions[i].Status, state.Conversions[i].Reason = "skipped", "patch_failed"
					state.Conversions[i].Output = state.Conversions[i].Original
					state.Conversions[i].DateAfter = state.Conversions[i].DateBefore
				}
				report.Status, report.Reason = "skipped", "patch_failed"
				state.Conversions = append(state.Conversions, report)
				state.patches = nil
				return bytes.Clone(body), state
			}
			if originalValue != output {
				state.patches = append(state.patches, requestTimezoneBodyPatch{occurrence.item.Path, originalValue, output})
			}
			report.Output = OpenAIRequestTimezone
			report.Status, report.Reason = "converted", "timezone_converted"
			if report.Original == report.Output && report.DateBefore == report.DateAfter {
				report.Status, report.Reason = "unchanged", "already_target"
			}
		}
		state.Conversions = append(state.Conversions, report)
	}
	state.preparedBody = prepared
	return bytes.Clone(prepared), state
}

type timezoneTextReplacement struct {
	start, end int
	value      string
}

func newTimezoneConversion(item TimezoneScanItem) TimezoneConversion {
	return TimezoneConversion{Source: item.Source, Path: item.Path, Original: item.Value, Output: item.Value, DateBefore: item.CurrentDate, DateAfter: item.CurrentDate}
}

func scanOpenAIRequestTimezones(body []byte) *requestTimezoneScanner {
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
	input := root.Get("input")
	if input.Exists() {
		if input.Type == gjson.String {
			s.scanText(input.String(), "input", true)
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
					s.scanSearchTimezone(tool.Get("user_location.timezone"), fmt.Sprintf("tools.%d.user_location.timezone", index))
				}
				index++
				return s.result.ScanStatus == "complete"
			})
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
			s.scanText(content.String(), fmt.Sprintf("%s.%d.content", path, i), current)
		} else if content.IsArray() {
			content.ForEach(func(key, part gjson.Result) bool {
				if !s.countNode() {
					return false
				}
				text := part.Get("text")
				if text.Type == gjson.String {
					kind := part.Get("type").String()
					if kind == "" || kind == "text" || kind == "input_text" {
						s.scanText(text.String(), fmt.Sprintf("%s.%d.content.%d.text", path, i, key.Int()), current)
					} else {
						s.countText(text.String())
					}
				}
				return s.result.ScanStatus == "complete"
			})
		}
		if current && len(s.occurrences) > before {
			if lastCurrent >= 0 {
				s.markHistorical(lastCurrent)
			}
			// Only the final candidate in the tail is current, even if it is
			// malformed or quoted; never fall back to an earlier valid block.
			for j := before; j < len(s.occurrences)-1; j++ {
				s.markHistorical(j)
			}
			lastCurrent = len(s.occurrences) - 1
		}
		return s.result.ScanStatus == "complete"
	})
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
	if s.occurrences[index].item.Status == "valid" {
		s.occurrences[index].item.Reason = "historical"
	}
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

func (s *requestTimezoneScanner) scanText(text, path string, current bool) {
	if !s.countText(text) {
		return
	}
	if countRequestTimezoneTag(text, "environment_context", false) == 0 && countRequestTimezoneTag(text, "environment_context", true) == 0 {
		return
	}
	occurrence := requestTimezoneOccurrence{environment: true, text: text, item: TimezoneScanItem{Source: "environment_context", Path: path, Current: current, Status: "invalid", Reason: "environment_not_standalone"}}
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
	if !current {
		occurrence.item.Reason = "historical"
	}
	s.add(occurrence)
}

func (s *requestTimezoneScanner) validEnvironmentXML(text string) bool {
	decoder := xml.NewDecoder(strings.NewReader(text))
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

func (s *requestTimezoneScanner) scanSearchTimezone(value gjson.Result, path string) {
	occurrence := requestTimezoneOccurrence{item: TimezoneScanItem{Source: "web_search", Path: path, Current: true, Status: "invalid"}}
	if !value.Exists() {
		occurrence.item.Reason = "timezone_missing"
	} else if value.Type == gjson.Null {
		occurrence.item.Reason = "timezone_null"
	} else if value.Type != gjson.String {
		occurrence.item.Reason = "timezone_not_string"
	} else {
		zone := value.String()
		if !s.countText(zone) {
			return
		}
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

func validRequestTimezone(zone string) bool {
	if zone == "" || strings.TrimSpace(zone) != zone || len(zone) > openAIRequestTimezoneValueLimit || (zone != "UTC" && !strings.Contains(zone, "/")) {
		return false
	}
	_, err := time.LoadLocation(zone)
	return err == nil
}
