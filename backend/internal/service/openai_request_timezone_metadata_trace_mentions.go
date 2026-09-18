package service

import (
	"regexp"
	"strings"
)

const (
	openAIEnvironmentTimezoneTextLimit   = 1 << 20
	openAIEnvironmentTimezoneMatchLimit  = 4096
	openAIEnvironmentTimezoneDetailLimit = 64
)

// This is a lexical diagnostic of frozen input, not a timezone parser or an
// environment classifier. A mention (including one in an example) does not
// make text eligible for conversion. Details contain only recognized markers
// or validated timezone values and their positions, never arbitrary excerpts,
// unrecognized candidate values, or hashes.
type openAIEnvironmentTimezoneText struct {
	ScanStatus            string                             `json:"scan_status"`
	Reason                string                             `json:"reason,omitempty"`
	ScannedBytes          int                                `json:"scanned_bytes"`
	MentionCount          int                                `json:"mention_count"`
	Mentions              []openAIEnvironmentTimezoneMention `json:"mentions"`
	LocationBasis         string                             `json:"location_basis"`
	MarkerCounts          map[string]int                     `json:"marker_counts"`
	Matches               []openAIEnvironmentTimezoneDetail  `json:"matches"`
	OmittedMatchCount     int                                `json:"omitted_match_count"`
	UnscannedBytes        int                                `json:"unscanned_bytes"`
	TimezoneTagOpenCount  int                                `json:"timezone_tag_open_count"`
	TimezoneTagCloseCount int                                `json:"timezone_tag_close_count"`
}

type openAIEnvironmentTimezoneMention struct {
	Kind            string `json:"kind"`
	Count           int    `json:"count"`
	FencedLineCount int    `json:"fenced_line_count"`
	QuotedLineCount int    `json:"quoted_line_count"`
	OtherLineCount  int    `json:"other_line_count"`
}

var openAIEnvironmentTimezoneTextPattern = regexp.MustCompile(`(?i:timezone|time_zone|utc_offset)|时区|(?:Africa|America|Antarctica|Arctic|Asia|Atlantic|Australia|Europe|Indian|Pacific|Etc)/(?:[A-Za-z0-9_+-]+/)*[A-Za-z0-9_+-]+|(?:UTC|GMT)(?:[+-][0-9]{1,2}(?::?[0-9]{2})?)?|[+-][0-9]{2}:[0-9]{2}`)

func buildOpenAIEnvironmentTimezoneText(text string) openAIEnvironmentTimezoneText {
	result := openAIEnvironmentTimezoneText{
		ScanStatus: "complete", ScannedBytes: len(text), Mentions: []openAIEnvironmentTimezoneMention{},
		LocationBasis: "decoded_text_utf8",
		MarkerCounts:  map[string]int{"timezone": 0, "time_zone": 0, "utc_offset": 0, "时区": 0},
		Matches:       []openAIEnvironmentTimezoneDetail{},
	}
	if result.ScannedBytes > openAIEnvironmentTimezoneTextLimit {
		result.ScannedBytes = openAIEnvironmentTimezoneTextLimit
		result.ScanStatus, result.Reason = "limited", "text_limit"
	}
	counts := [6]openAIEnvironmentTimezoneMention{
		{Kind: "asia_shanghai"}, {Kind: "america_los_angeles"}, {Kind: "utc_or_gmt"},
		{Kind: "other_iana_candidate"}, {Kind: "utc_offset_candidate"}, {Kind: "timezone_marker"},
	}
	// These are line-context hints, not a full Markdown parse. Inline code stays
	// in other_line_count; fenced content takes precedence over quoted lines.
	var fence byte
	fenceWidth := 0
	fenceStartLine := 0
	lineNumber := 1
	validatedZones := make(map[string]bool)
	end := result.ScannedBytes
	for start := 0; start < end; {
		lineEnd := end
		if newline := strings.IndexByte(text[start:end], '\n'); newline >= 0 {
			lineEnd = start + newline
		}
		line := text[start:lineEnd]
		content := strings.TrimLeft(line, " \t\r")
		quoteDepth := 0
		for strings.HasPrefix(content, ">") {
			quoteDepth++
			content = strings.TrimLeft(content[1:], " \t\r")
		}
		marker, width, tail := openAIEnvironmentTimezoneFence(content)
		fenced := fence != 0
		activeFenceStartLine := fenceStartLine
		if fence == 0 && marker != 0 {
			fence, fenceWidth, fenced = marker, width, true
			fenceStartLine, activeFenceStartLine = lineNumber, lineNumber
		} else if fence != 0 && marker == fence && width >= fenceWidth && strings.TrimSpace(tail) == "" {
			fence, fenceWidth = 0, 0
			fenceStartLine = 0
		}
		for offset := 0; offset < len(line); {
			span := openAIEnvironmentTimezoneTextPattern.FindStringIndex(line[offset:])
			if span == nil {
				break
			}
			from, to := start+offset+span[0], start+offset+span[1]
			offset += span[1]
			value := text[from:to]
			if !openAIEnvironmentTimezoneMentionBoundary(text, value, from, to) {
				continue
			}
			if result.MentionCount == openAIEnvironmentTimezoneMatchLimit {
				result.ScanStatus, result.Reason, result.ScannedBytes = "limited", "match_limit", from
				return finishOpenAIEnvironmentTimezoneText(result, counts, text)
			}
			index := 5
			canonicalMarker := openAIEnvironmentTimezoneCanonicalMarker(value)
			switch {
			case canonicalMarker != "":
				index = 5
			case value == "Asia/Shanghai":
				index = 0
			case value == "America/Los_Angeles":
				index = 1
			case value == "UTC" || value == "GMT":
				index = 2
			case strings.Contains(value, "/"):
				index = 3
			case value[0] == '+' || value[0] == '-' || strings.HasPrefix(value, "UTC") || strings.HasPrefix(value, "GMT"):
				index = 4
			}
			count := &counts[index]
			count.Count++
			result.MentionCount++
			if index == 5 {
				result.MarkerCounts[canonicalMarker]++
			}
			if len(result.Matches) < openAIEnvironmentTimezoneDetailLimit {
				result.Matches = append(result.Matches, buildOpenAIEnvironmentTimezoneDetail(text[:end], line, start, lineNumber, from, to, count.Kind, fenced, quoteDepth, activeFenceStartLine, validatedZones))
			}
			switch {
			case fenced:
				count.FencedLineCount++
			case quoteDepth > 0:
				count.QuotedLineCount++
			default:
				count.OtherLineCount++
			}
		}
		start = lineEnd + 1
		lineNumber++
	}
	return finishOpenAIEnvironmentTimezoneText(result, counts, text)
}

func finishOpenAIEnvironmentTimezoneText(result openAIEnvironmentTimezoneText, counts [6]openAIEnvironmentTimezoneMention, text string) openAIEnvironmentTimezoneText {
	for _, count := range counts {
		if count.Count > 0 {
			result.Mentions = append(result.Mentions, count)
		}
	}
	result.OmittedMatchCount = result.MentionCount - len(result.Matches)
	result.UnscannedBytes = len(text) - result.ScannedBytes
	result.TimezoneTagOpenCount = countOpenAIEnvironmentTimezoneTraceTags(text, result.ScannedBytes, false)
	result.TimezoneTagCloseCount = countOpenAIEnvironmentTimezoneTraceTags(text, result.ScannedBytes, true)
	return result
}

func countOpenAIEnvironmentTimezoneTraceTags(text string, end int, closing bool) int {
	count := countRequestTimezoneTag(text[:end], "timezone", closing)
	prefix := "<timezone"
	if closing {
		prefix = "</timezone"
	}
	// The source helper accepts EOF as a boundary. A truncated prefix is not
	// EOF in the actual text: consult the next byte before counting the tag.
	if end < len(text) && strings.HasSuffix(text[:end], prefix) && !strings.ContainsRune(">/ \t\r\n", rune(text[end])) {
		count--
	}
	return count
}

func openAIEnvironmentTimezoneCanonicalMarker(value string) string {
	for _, marker := range []string{"timezone", "time_zone", "utc_offset", "时区"} {
		if strings.EqualFold(value, marker) {
			return marker
		}
	}
	return ""
}

func openAIEnvironmentTimezoneMentionBoundary(text, value string, from, to int) bool {
	if value == "时区" {
		return true
	}
	if strings.EqualFold(value, "timezone") || strings.EqualFold(value, "time_zone") || strings.EqualFold(value, "utc_offset") {
		return (from == 0 || !openAIEnvironmentTimezoneWordByte(text[from-1])) &&
			(to == len(text) || !openAIEnvironmentTimezoneWordByte(text[to]))
	}
	// Test against the original text, including just beyond the byte budget, so
	// a truncated token cannot become a false complete UTC or fixed-zone hit.
	if from > 0 && openAIEnvironmentTimezoneTokenByte(text[from-1]) {
		return false
	}
	if to < len(text) && text[to] == ':' && len(value) > 3 &&
		(strings.HasPrefix(value, "UTC") || strings.HasPrefix(value, "GMT")) {
		return false
	}
	return to == len(text) || !openAIEnvironmentTimezoneTokenByte(text[to])
}

func openAIEnvironmentTimezoneTokenByte(c byte) bool {
	return openAIEnvironmentTimezoneWordByte(c) || c == '/' || c == '+' || c == '-'
}

func openAIEnvironmentTimezoneWordByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_'
}

func openAIEnvironmentTimezoneFence(line string) (byte, int, string) {
	if len(line) < 3 || line[0] != '`' && line[0] != '~' {
		return 0, 0, ""
	}
	width := 1
	for width < len(line) && line[width] == line[0] {
		width++
	}
	if width < 3 {
		return 0, 0, ""
	}
	return line[0], width, line[width:]
}
