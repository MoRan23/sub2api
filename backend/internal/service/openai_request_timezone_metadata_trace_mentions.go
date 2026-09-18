package service

import (
	"regexp"
	"strings"
)

const (
	openAIEnvironmentTimezoneTextLimit  = 1 << 20
	openAIEnvironmentTimezoneMatchLimit = 4096
)

// This is a lexical diagnostic of frozen input, not a timezone parser or an
// environment classifier. A mention (including one in an example) does not
// make text eligible for conversion. Only fixed categories and counts leave
// this helper; never retain matched text, offsets, excerpts, or hashes.
type openAIEnvironmentTimezoneText struct {
	ScanStatus   string                             `json:"scan_status"`
	Reason       string                             `json:"reason,omitempty"`
	ScannedBytes int                                `json:"scanned_bytes"`
	MentionCount int                                `json:"mention_count"`
	Mentions     []openAIEnvironmentTimezoneMention `json:"mentions"`
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
	end := result.ScannedBytes
	for start := 0; start < end; {
		lineEnd := end
		if newline := strings.IndexByte(text[start:end], '\n'); newline >= 0 {
			lineEnd = start + newline
		}
		line := text[start:lineEnd]
		content := strings.TrimLeft(line, " \t\r")
		quoted := false
		for strings.HasPrefix(content, ">") {
			quoted = true
			content = strings.TrimLeft(content[1:], " \t\r")
		}
		marker, width, tail := openAIEnvironmentTimezoneFence(content)
		fenced := fence != 0
		if fence == 0 && marker != 0 {
			fence, fenceWidth, fenced = marker, width, true
		} else if fence != 0 && marker == fence && width >= fenceWidth && strings.TrimSpace(tail) == "" {
			fence, fenceWidth = 0, 0
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
				return finishOpenAIEnvironmentTimezoneText(result, counts)
			}
			index := 5
			switch {
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
			switch {
			case fenced:
				count.FencedLineCount++
			case quoted:
				count.QuotedLineCount++
			default:
				count.OtherLineCount++
			}
		}
		start = lineEnd + 1
	}
	return finishOpenAIEnvironmentTimezoneText(result, counts)
}

func finishOpenAIEnvironmentTimezoneText(result openAIEnvironmentTimezoneText, counts [6]openAIEnvironmentTimezoneMention) openAIEnvironmentTimezoneText {
	for _, count := range counts {
		if count.Count > 0 {
			result.Mentions = append(result.Mentions, count)
		}
	}
	return result
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
