package service

import (
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
)

// CodexModelEvidence contains only upstream declarations from one physical
// response. Header hints never stand in for the response body's model.
type CodexModelEvidence struct {
	UpstreamResponseModel      string                   `json:"upstream_response_model,omitempty"`
	ModelRelation              string                   `json:"model_relation"`
	ModelConflict              bool                     `json:"model_conflict"`
	ModelEvidenceSource        string                   `json:"model_evidence_source,omitempty"`
	SafetyBufferingEnabled     *bool                    `json:"safety_buffering_enabled,omitempty"`
	SafetyBufferingFasterModel string                   `json:"safety_buffering_faster_model,omitempty"`
	HeaderEvidenceScope        string                   `json:"header_evidence_scope,omitempty"`
	HeaderDiagnostics          *OpenAIHeaderDiagnostics `json:"header_diagnostics,omitempty"`
	GuardianAssessment         *CodexGuardianEvidence   `json:"guardian_assessment,omitempty"`
}

// CodexGuardianEvidence summarizes declarations, never execution permission.
// No action IDs, tool content or history are retained. Repeated metadata is
// idempotent and memory usage is constant even for arbitrarily long streams.
type CodexGuardianEvidence struct {
	Status         string `json:"status"`
	InvalidPresent bool   `json:"invalid_present,omitempty"`
}

func (e CodexModelEvidence) clone() CodexModelEvidence {
	e.HeaderDiagnostics = e.HeaderDiagnostics.clone()
	if e.GuardianAssessment != nil {
		v := *e.GuardianAssessment
		e.GuardianAssessment = &v
	}
	if e.SafetyBufferingEnabled != nil {
		v := *e.SafetyBufferingEnabled
		e.SafetyBufferingEnabled = &v
	}
	return e
}

type codexModelEvidenceObserver struct {
	model                                                           upstreamResponseModelObserver
	firstSource, terminalSource                                     string
	headers                                                         CodexModelEvidence
	guardianSeen, guardianInvalid, guardianReview, guardianNoReview bool
}

func (o *codexModelEvidenceObserver) observePayload(payload []byte) {
	if !gjson.ValidBytes(payload) {
		return
	}
	eventType := strings.TrimSpace(gjson.GetBytes(payload, "type").String())
	for _, path := range []string{"response.model", "model"} {
		value := gjson.GetBytes(payload, path)
		if value.Type != gjson.String || strings.TrimSpace(value.String()) == "" {
			continue
		}
		terminal := isUpstreamResponseModelTerminalEvent(eventType)
		o.model.Observe(value.String(), terminal)
		if terminal {
			o.terminalSource = path
		} else if o.firstSource == "" {
			o.firstSource = path
		}
		break
	}
	if eventType == "response.metadata" {
		o.observeGuardian(payload)
		// Protocol metadata headers are response-local, including on WS.
		headers := make(http.Header)
		gjson.GetBytes(payload, "headers").ForEach(func(key, value gjson.Result) bool {
			if value.Type == gjson.String {
				headers.Add(key.String(), value.String())
			} else if value.IsArray() {
				for _, item := range value.Array() {
					if item.Type == gjson.String {
						headers.Add(key.String(), item.String())
					}
				}
			}
			return true
		})
		o.observeHeaders(headers, "response")
	}
}

func (o *codexModelEvidenceObserver) observeGuardian(payload []byte) {
	guardian := gjson.GetBytes(payload, "metadata.guardian")
	if !guardian.Exists() {
		return
	}
	o.guardianSeen = true
	responseID, itemID := gjson.GetBytes(payload, "response_id"), guardian.Get("item_id")
	review := guardian.Get("requires_review")
	if !guardian.IsObject() || responseID.Type != gjson.String || responseID.String() == "" || itemID.Type != gjson.String || itemID.String() == "" || (review.Type != gjson.True && review.Type != gjson.False) {
		o.guardianInvalid = true
		return
	}
	if review.Bool() {
		o.guardianReview = true
	} else {
		o.guardianNoReview = true
	}
}

func (o *codexModelEvidenceObserver) observeHeaders(headers http.Header, scope string) {
	hasDiagnostics := false
	for key := range headers {
		hasDiagnostics = hasDiagnostics || isOpenAIDiagnosticHeader(key)
	}
	for key := range headers {
		if strings.EqualFold(key, "x-codex-safety-buffering-enabled") || strings.EqualFold(key, "x-codex-safety-buffering-faster-model") || isOpenAIDiagnosticHeader(key) {
			if o.headers.HeaderEvidenceScope != "" && o.headers.HeaderEvidenceScope != scope {
				// Never label a previous connection hint as a new response's
				// evidence when metadata supplies only one of the two fields.
				o.headers.SafetyBufferingEnabled = nil
				o.headers.SafetyBufferingFasterModel = ""
				o.headers.HeaderDiagnostics = nil
			}
			break
		}
	}
	for key, values := range headers {
		switch strings.ToLower(key) {
		case "x-codex-safety-buffering-enabled":
			var parsed *bool
			valid := len(values) > 0
			for _, raw := range values {
				value := strings.ToLower(strings.TrimSpace(raw))
				if value != "true" && value != "false" {
					valid = false
					break
				}
				b := value == "true"
				if parsed != nil && *parsed != b {
					valid = false
					break
				}
				parsed = &b
			}
			if valid {
				o.headers.SafetyBufferingEnabled = parsed
			} else {
				o.headers.SafetyBufferingEnabled = nil
			}
			o.headers.HeaderEvidenceScope = scope
		case "x-codex-safety-buffering-faster-model":
			o.headers.SafetyBufferingFasterModel = ""
			if len(values) == 1 {
				o.headers.SafetyBufferingFasterModel = normalizeObservedUpstreamResponseModel(values[0])
			}
			o.headers.HeaderEvidenceScope = scope
		}
	}
	if hasDiagnostics {
		parsed := openAIHeaderDiagnosticsFromHeaders(headers)
		if parsed == nil {
			parsed = &OpenAIHeaderDiagnostics{}
		}
		merged := o.headers.HeaderDiagnostics.clone()
		if merged == nil {
			merged = &OpenAIHeaderDiagnostics{}
		}
		for key := range headers {
			switch strings.ToLower(key) {
			case "x-codex-imagegen-request-id":
				merged.ImagegenRequestID = parsed.ImagegenRequestID
			case "x-openai-authorization-error":
				merged.AuthorizationError = parsed.AuthorizationError
			case "x-error-json":
				merged.IdentityErrorCode = parsed.IdentityErrorCode
			}
		}
		if *merged == (OpenAIHeaderDiagnostics{}) {
			merged = nil
		}
		o.headers.HeaderDiagnostics = merged
		o.headers.HeaderEvidenceScope = scope
	}
}

func (o *codexModelEvidenceObserver) snapshot(sentModel string) CodexModelEvidence {
	e := o.headers.clone()
	if o.guardianSeen {
		status := "invalid"
		switch {
		case o.guardianReview && o.guardianNoReview:
			status = "mixed"
		case o.guardianReview:
			status = "review_required"
		case o.guardianNoReview:
			status = "review_not_required"
		}
		e.GuardianAssessment = &CodexGuardianEvidence{Status: status, InvalidPresent: o.guardianInvalid}
	}
	e.UpstreamResponseModel, e.ModelConflict = o.model.Model(), o.model.Conflict()
	e.ModelEvidenceSource = o.firstSource
	if o.terminalSource != "" {
		e.ModelEvidenceSource = o.terminalSource
	}
	switch {
	case e.UpstreamResponseModel == "":
		e.ModelRelation = "not_reported"
	case e.ModelConflict:
		e.ModelRelation = "conflicting"
	case strings.EqualFold(strings.TrimSpace(sentModel), e.UpstreamResponseModel):
		e.ModelRelation = "exact"
	case codexResponseModelAliasSpellingMatches(sentModel, e.UpstreamResponseModel):
		e.ModelRelation = "known_alias"
	default:
		e.ModelRelation = "different"
	}
	return e
}

func codexResponseModelAliasSpellingMatches(sentModel, responseModel string) bool {
	// Reuse only the established OpenAI spelling aliases. Family, dated build,
	// latest and pricing normalization do not establish response identity.
	sent := canonicalizeOpenAIModelAliasSpelling(sentModel)
	return sent != "" && sent == canonicalizeOpenAIModelAliasSpelling(responseModel)
}
