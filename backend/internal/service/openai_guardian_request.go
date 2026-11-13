package service

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const openAIGuardianHeader = "x-codex-guardian"
const openAIGuardianCaptureKey = "openai_guardian_request_capture"

type openAIGuardianPolicyContextKey struct{}

func withOpenAIGuardianPolicyContext(ctx context.Context, c *gin.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	kind := captureOpenAIGuardian(c).kind
	if current, _ := ctx.Value(openAIGuardianPolicyContextKey{}).(string); current == kind {
		return ctx
	}
	return context.WithValue(ctx, openAIGuardianPolicyContextKey{}, kind)
}

func openAIGuardianPolicyKind(ctx context.Context, account *Account) string {
	if ctx == nil || account == nil || !account.UsesOpenAICodexProtocol() {
		return ""
	}
	kind, _ := ctx.Value(openAIGuardianPolicyContextKey{}).(string)
	return kind
}

// A request-scoped capture keeps reconnects and account retries independent of
// later header overrides. Strings are immutable; no body or credentials survive.
type openAIGuardianCapture struct {
	values []string
	kind   string
}

func openAIGuardianKind(headers http.Header) string {
	return openAIGuardianKindFromValues(headerValuesCaseInsensitive(headers, openAIGuardianHeader))
}

func openAIGuardianKindFromValues(values []string) string {
	if len(values) == 1 && (values[0] == "reviewer" || values[0] == "classifier") {
		return values[0]
	}
	return ""
}

func captureOpenAIGuardian(c *gin.Context) openAIGuardianCapture {
	if c == nil || c.Request == nil {
		return openAIGuardianCapture{}
	}
	if value, exists := c.Get(openAIGuardianCaptureKey); exists {
		if capture, ok := value.(openAIGuardianCapture); ok {
			return capture
		}
	}
	values := headerValuesCaseInsensitive(c.Request.Header, openAIGuardianHeader)
	capture := openAIGuardianCapture{values: values, kind: openAIGuardianKindFromValues(values)}
	c.Set(openAIGuardianCaptureKey, capture)
	return capture
}

func openAIGuardianKindForAccount(c *gin.Context, account *Account) string {
	if account == nil || !account.UsesOpenAICodexProtocol() {
		return ""
	}
	return captureOpenAIGuardian(c).kind
}

func projectOpenAIGuardianHeader(c *gin.Context, account *Account, headers http.Header) {
	if account == nil || !account.UsesOpenAICodexProtocol() || headers == nil {
		return
	}
	deleteOpenAIHeaderEqualFold(headers, openAIGuardianHeader)
	if values := captureOpenAIGuardian(c).values; len(values) > 0 {
		headers[http.CanonicalHeaderKey(openAIGuardianHeader)] = append([]string(nil), values...)
	}
}

func openAIGuardianCompatibility(headers http.Header) string {
	values := headerValuesCaseInsensitive(headers, openAIGuardianHeader)
	if len(values) == 0 {
		return ""
	}
	// Header maps may contain differently cased spellings. Their iteration
	// order is unspecified; preserve multiplicity without making socket identity
	// depend on map iteration order. This helper already owns the returned slice.
	sort.Strings(values)
	encoded, _ := json.Marshal(values)
	return string(encoded)
}

func applyOpenAIGuardianBody(body []byte, kind string) ([]byte, error) {
	if kind == "" || !gjson.GetBytes(body, "service_tier").Exists() {
		return body, nil
	}
	// Localized immutable patch; the original retry snapshot remains untouched.
	return sjson.DeleteBytes(body, "service_tier")
}

type FingerprintGuardianField struct {
	Status  string `json:"status"`
	Type    string `json:"type"`
	Present bool   `json:"present"`
	Value   string `json:"value,omitempty"`
}

// All fields are bounded scalars. Parent response IDs and invalid raw values
// intentionally never enter the observation ring or administrator DTO.
type FingerprintGuardianRequest struct {
	Status             string                   `json:"status"`
	Kind               string                   `json:"kind,omitempty"`
	Source             string                   `json:"source"`
	Applied            bool                     `json:"applied"`
	Reason             string                   `json:"reason"`
	ServiceTier        string                   `json:"service_tier,omitempty"`
	RoutingHintPresent bool                     `json:"routing_hint_present"`
	Credits            FingerprintGuardianField `json:"credits"`
	ParentResponse     FingerprintGuardianField `json:"parent_response"`
}

func observeOpenAIGuardianField(raw gjson.Result, credits bool) FingerprintGuardianField {
	field := FingerprintGuardianField{Status: "missing", Type: "missing"}
	if !raw.Exists() {
		return field
	}
	field.Present, field.Status = true, "invalid"
	switch raw.Type {
	case gjson.Null:
		field.Type = "null"
	case gjson.True, gjson.False:
		field.Type = "boolean"
	case gjson.Number:
		field.Type = "number"
	case gjson.JSON:
		if raw.IsArray() {
			field.Type = "array"
		} else {
			field.Type = "object"
		}
	case gjson.String:
		field.Type = "string"
		if credits {
			if raw.Str == "true" || raw.Str == "false" {
				field.Status, field.Value = "valid", strings.Clone(raw.Str)
			}
		} else if len(raw.Str) <= 1024 && strings.TrimSpace(raw.Str) != "" && !strings.ContainsAny(raw.Str, "\x00\r\n") {
			field.Status = "valid"
		}
	}
	return field
}

func observeOpenAIGuardianRequest(account *Account, headers http.Header, body []byte) *FingerprintGuardianRequest {
	observation := &FingerprintGuardianRequest{Status: "absent", Source: FingerprintObservationEventHTTP, Reason: "not_requested"}
	if values := headerValuesCaseInsensitive(headers, openAIGuardianHeader); len(values) > 0 {
		observation.Kind = openAIGuardianKindFromValues(values)
		observation.Status, observation.Reason = "invalid", "invalid_marker"
		if observation.Kind != "" {
			observation.Status = "valid"
			observation.Reason = "non_codex_upstream"
			if account != nil && account.UsesOpenAICodexProtocol() {
				observation.Applied, observation.Reason = true, "guardian_contract"
			}
		}
	}
	observation.Credits = FingerprintGuardianField{Status: "missing", Type: "missing"}
	observation.ParentResponse = observation.Credits
	if len(body) > 0 {
		fields := gjson.GetManyBytes(body, "client_metadata.guardian_credits_requested", "client_metadata.parent_response_id", "service_tier")
		observation.Credits = observeOpenAIGuardianField(fields[0], true)
		observation.ParentResponse = observeOpenAIGuardianField(fields[1], false)
		populateOpenAIGuardianServiceTier(observation, fields[2])
	}
	observation.RoutingHintPresent = len(headerValuesCaseInsensitive(headers, openAICodexRoutingHintHeader)) > 0
	return observation
}

func populateOpenAIGuardianServiceTier(observation *FingerprintGuardianRequest, tier gjson.Result) {
	if tier.Type == gjson.String && len(tier.Str) <= 32 {
		observation.ServiceTier = strings.Clone(tier.Str)
	}
}
