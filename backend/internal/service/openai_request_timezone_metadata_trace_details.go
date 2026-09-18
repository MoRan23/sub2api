package service

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// All positions refer to the JSON-decoded input text, not the HTTP JSON body:
// byte_start is zero-based, byte_end is exclusive, and line/column are one-based
// with Unicode code points counted as columns. Syntax and code/XML contexts are
// lexical hints, not authorization to transform an environment declaration.
type openAIEnvironmentTimezoneDetail struct {
	Kind           string `json:"kind"`
	Token          string `json:"token,omitempty"`
	ValueStatus    string `json:"value_status"`
	Line           int    `json:"line"`
	Column         int    `json:"column"`
	ByteStart      int    `json:"byte_start"`
	ByteEnd        int    `json:"byte_end"`
	Syntax         string `json:"syntax"`
	XMLContext     string `json:"xml_context"`
	InFencedCode   bool   `json:"in_fenced_code"`
	InInlineCode   bool   `json:"in_inline_code"`
	QuoteDepth     int    `json:"quote_depth"`
	FenceStartLine int    `json:"fence_start_line,omitempty"`
}

func buildOpenAIEnvironmentTimezoneDetail(text, line string, lineStart, lineNumber, from, to int, kind string,
	fenced bool, quoteDepth, fenceStartLine int, validatedZones map[string]bool) openAIEnvironmentTimezoneDetail {
	value := text[from:to]
	detail := openAIEnvironmentTimezoneDetail{
		Kind: kind, Line: lineNumber, Column: utf8.RuneCountInString(text[lineStart:from]) + 1,
		ByteStart: from, ByteEnd: to, Syntax: openAIEnvironmentTimezoneTokenSyntax(text, from, to, kind),
		XMLContext: openAIEnvironmentTimezoneXMLContext(text[:from]), InFencedCode: fenced,
		InInlineCode: !fenced && openAIEnvironmentTimezoneInlineCode(line, from-lineStart), QuoteDepth: quoteDepth,
	}
	if fenced {
		detail.FenceStartLine = fenceStartLine
	}
	switch kind {
	case "timezone_marker":
		detail.Token, detail.ValueStatus = strings.Clone(value), "marker"
	case "asia_shanghai", "america_los_angeles", "utc_or_gmt":
		detail.Token, detail.ValueStatus = strings.Clone(value), "valid_timezone"
	case "other_iana_candidate":
		valid, exists := validatedZones[value]
		if !exists {
			valid = validRequestTimezone(value)
			validatedZones[value] = valid
		}
		detail.ValueStatus = "invalid_iana"
		if valid {
			detail.Token, detail.ValueStatus = strings.Clone(value), "valid_timezone"
		}
	case "utc_offset_candidate":
		detail.ValueStatus = "invalid_offset"
		if validOpenAIEnvironmentTimezoneOffset(value) {
			detail.Token, detail.ValueStatus = strings.Clone(value), "valid_offset"
		}
	}
	return detail
}

func validOpenAIEnvironmentTimezoneOffset(value string) bool {
	value = strings.TrimPrefix(strings.TrimPrefix(value, "UTC"), "GMT")
	if len(value) < 2 || value[0] != '+' && value[0] != '-' {
		return false
	}
	value = value[1:]
	hour, minute := value, "0"
	if before, after, ok := strings.Cut(value, ":"); ok {
		hour, minute = before, after
		if len(minute) != 2 {
			return false
		}
	} else if len(value) == 4 {
		hour, minute = value[:2], value[2:]
	}
	if len(hour) < 1 || len(hour) > 2 {
		return false
	}
	h, hErr := strconv.Atoi(hour)
	m, mErr := strconv.Atoi(minute)
	// This validates numeric UTC-offset syntax, not a client's actual locale.
	return hErr == nil && mErr == nil && h >= 0 && h <= 23 && m >= 0 && m <= 59
}

func openAIEnvironmentTimezoneTokenSyntax(text string, from, to int, kind string) string {
	before, after := text[:from], text[to:]
	if kind == "timezone_marker" {
		trimmedAfter := strings.TrimLeft(after, " \t\r\n")
		if strings.HasSuffix(before, "</") && strings.HasPrefix(trimmedAfter, ">") {
			return "xml_close_tag"
		}
		if strings.HasSuffix(before, "<") && len(after) > 0 && (after[0] == '>' || after[0] == '/' || strings.ContainsRune(" \t\r\n", rune(after[0]))) {
			if strings.HasPrefix(trimmedAfter, "/>") {
				return "xml_self_closing_tag"
			}
			return "xml_open_tag"
		}
		if len(before) > 0 && len(after) > 0 && (before[len(before)-1] == '"' || before[len(before)-1] == '\'') && before[len(before)-1] == after[0] &&
			strings.HasPrefix(strings.TrimLeft(after[1:], " \t\r\n"), ":") {
			return "quoted_key"
		}
		if strings.HasPrefix(trimmedAfter, ":") || strings.HasPrefix(trimmedAfter, "=") {
			return "assignment_key"
		}
	}
	trimmedBefore := strings.TrimRight(before, " \t\r\n")
	for _, marker := range []string{"<timezone>", "<time_zone>", "<utc_offset>"} {
		if strings.HasSuffix(trimmedBefore, marker) {
			return "xml_value"
		}
	}
	return "word"
}

func openAIEnvironmentTimezoneXMLContext(before string) string {
	for len(before) > 0 {
		comment, cdata := strings.Index(before, "<!--"), strings.Index(before, "<![CDATA[")
		if comment < 0 && cdata < 0 {
			break
		}
		if comment >= 0 && (cdata < 0 || comment < cdata) {
			before = before[comment+len("<!--"):]
			end := strings.Index(before, "-->")
			if end < 0 {
				return "comment"
			}
			before = before[end+len("-->"):]
		} else {
			before = before[cdata+len("<![CDATA["):]
			end := strings.Index(before, "]]>")
			if end < 0 {
				return "cdata"
			}
			before = before[end+len("]]>"):]
		}
	}
	return "none"
}

func openAIEnvironmentTimezoneInlineCode(line string, column int) bool {
	for start := 0; start < len(line); {
		next := strings.IndexByte(line[start:], '`')
		if next < 0 {
			return false
		}
		start += next
		width := 1
		for start+width < len(line) && line[start+width] == '`' {
			width++
		}
		end := start + width
		for end < len(line) {
			closeAt := strings.IndexByte(line[end:], '`')
			if closeAt < 0 {
				return false
			}
			end += closeAt
			closeWidth := 1
			for end+closeWidth < len(line) && line[end+closeWidth] == '`' {
				closeWidth++
			}
			if closeWidth == width {
				if column >= start+width && column < end {
					return true
				}
				start = end + width
				break
			}
			end += closeWidth
		}
		if end >= len(line) || start > column {
			return false
		}
	}
	return false
}
