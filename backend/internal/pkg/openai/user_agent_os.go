package openai

import (
	"regexp"
	"strings"
)

var userAgentOSPattern = regexp.MustCompile(`(?i)\b(windows|mac[ \t]+os|macos|macintosh|darwin|linux|ubuntu|debian)\b`)

// Mobile platforms often advertise their desktop ancestor (Android includes
// Linux; iOS includes Mac OS X). iPad desktop mode can put its Mobile marker
// after the environment group, so reject explicit mobile evidence before
// narrowing desktop detection to that group.
var userAgentMobileOSPattern = regexp.MustCompile(`(?i)\b(android|iphone|ipad|ipod|ios|ipados|windows[ \t]+phone|windows[ \t]+ce|iemobile|mobile|blackberry|bb10|symbian|webos|kaios|harmonyos)\b`)

// DetectOSFamilyFromUserAgent returns windows, macos, linux, or an empty string
// when the operating system is unknown or conflicting. For structured User-Agents,
// only the first parenthesized environment group contributes OS evidence; terminal
// names and later client trailers must not override it. Bare OS hints are accepted
// for callers that already extracted an environment value.
func DetectOSFamilyFromUserAgent(userAgent string) string {
	if !validCodexUserAgentValue(userAgent) {
		return ""
	}
	if userAgentMobileOSPattern.MatchString(userAgent) {
		return ""
	}
	field := strings.TrimSpace(userAgent)
	if open := strings.IndexByte(field, '('); open >= 0 {
		field = field[open+1:]
		close := strings.IndexByte(field, ')')
		if close < 0 || strings.ContainsRune(field[:close], '(') {
			return ""
		}
		field = field[:close]
	} else if strings.ContainsRune(field, ')') {
		return ""
	}

	family := ""
	for _, match := range userAgentOSPattern.FindAllString(field, -1) {
		candidate := "macos"
		switch strings.ToLower(match) {
		case "windows":
			candidate = "windows"
		case "linux", "ubuntu", "debian":
			candidate = "linux"
		}
		if family != "" && family != candidate {
			return ""
		}
		family = candidate
	}
	return family
}
