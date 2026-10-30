package service

import (
	"encoding/base64"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/util/logredact"
	"github.com/tidwall/gjson"
)

// OpenAIHeaderDiagnostics is an administrator-only, bounded summary. Never add
// the encoded identity header or its decoded object to this structure.
type OpenAIHeaderDiagnostics struct {
	ImagegenRequestID  string `json:"imagegen_request_id,omitempty"`
	AuthorizationError string `json:"authorization_error,omitempty"`
	IdentityErrorCode  string `json:"identity_error_code,omitempty"`
}

func isOpenAIDiagnosticHeader(key string) bool {
	return strings.EqualFold(key, "x-codex-imagegen-request-id") || strings.EqualFold(key, "x-openai-authorization-error") || strings.EqualFold(key, "x-error-json")
}

func (d *OpenAIHeaderDiagnostics) clone() *OpenAIHeaderDiagnostics {
	if d == nil {
		return nil
	}
	out := *d
	return &out
}

func sanitizeOpenAIHeaderDiagnostics(d *OpenAIHeaderDiagnostics) *OpenAIHeaderDiagnostics {
	if d == nil {
		return nil
	}
	out := &OpenAIHeaderDiagnostics{
		ImagegenRequestID:  safeOpenAIDiagnosticID(d.ImagegenRequestID),
		AuthorizationError: safeOpenAIAuthorizationSummary(d.AuthorizationError),
		IdentityErrorCode:  safeOpenAIIdentityErrorCode(d.IdentityErrorCode),
	}
	if *out == (OpenAIHeaderDiagnostics{}) {
		return nil
	}
	return out
}

func safeOpenAIDiagnosticID(raw string) string {
	raw = strings.TrimSpace(raw)
	if len(raw) > 256 {
		return ""
	}
	if strings.HasPrefix(raw, "sk-") || strings.HasPrefix(raw, "sess-") || (strings.HasPrefix(raw, "eyJ") && strings.Contains(raw, ".")) {
		return ""
	}
	for _, c := range raw {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '-', c == '.':
		default:
			return ""
		}
	}
	return raw
}

func safeOpenAIIdentityErrorCode(raw string) string {
	if len(raw) > 128 {
		return ""
	}
	raw = safeOpenAIDiagnosticID(raw)
	// A code is an identifier, not a place to retain accidentally returned keys.
	if redactContentModerationSecrets(raw) != raw {
		return ""
	}
	return raw
}

func safeOpenAIAuthorizationSummary(raw string) string {
	if len(raw) > 4096 {
		return "[oversized authorization diagnostic]"
	}
	if gjson.Valid(raw) {
		value := gjson.Parse(raw)
		if value.IsObject() || value.IsArray() {
			return "[structured authorization diagnostic]"
		}
	}
	raw = logredact.RedactText(raw, "authorization", "cookie", "token", "api_key", "secret")
	raw = redactContentModerationSecrets(sanitizeUpstreamErrorMessage(raw))
	return truncateString(strings.Join(strings.Fields(raw), " "), 512)
}

func openAIHeaderDiagnosticsFromHeaders(headers http.Header) *OpenAIHeaderDiagnostics {
	// Reject ambiguous duplicates rather than choosing a different value from
	// the HTTP implementation. Header names remain case-insensitive in fixtures.
	values := func(name string) []string {
		var result []string
		for key, entries := range headers {
			if strings.EqualFold(key, name) {
				result = append(result, entries...)
			}
		}
		return result
	}
	d := &OpenAIHeaderDiagnostics{}
	if v := values("x-codex-imagegen-request-id"); len(v) == 1 {
		d.ImagegenRequestID = v[0]
	}
	if v := values("x-openai-authorization-error"); len(v) == 1 {
		d.AuthorizationError = v[0]
	}
	if v := values("x-error-json"); len(v) == 1 && len(v[0]) <= 16*1024 {
		if decoded, err := base64.StdEncoding.DecodeString(v[0]); err == nil && len(decoded) <= 8*1024 && gjson.ValidBytes(decoded) {
			if code := gjson.GetBytes(decoded, "error.code"); code.Type == gjson.String {
				d.IdentityErrorCode = code.String()
			}
		}
	}
	return sanitizeOpenAIHeaderDiagnostics(d)
}
