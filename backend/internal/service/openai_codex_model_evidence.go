package service

import (
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
)

// CodexModelEvidence contains only upstream declarations from one physical
// response. Header hints never stand in for the response body's model.
type CodexModelEvidence struct {
	UpstreamKind               string `json:"upstream_kind,omitempty"`
	UpstreamResponseModel      string `json:"upstream_response_model,omitempty"`
	ModelRelation              string `json:"model_relation"`
	ModelConflict              bool   `json:"model_conflict"`
	ModelEvidenceSource        string `json:"model_evidence_source,omitempty"`
	SafetyBufferingEnabled     *bool  `json:"safety_buffering_enabled,omitempty"`
	SafetyBufferingFasterModel string `json:"safety_buffering_faster_model,omitempty"`
	HeaderEvidenceScope        string `json:"header_evidence_scope,omitempty"`
}

func (e CodexModelEvidence) clone() CodexModelEvidence {
	if e.SafetyBufferingEnabled != nil {
		v := *e.SafetyBufferingEnabled
		e.SafetyBufferingEnabled = &v
	}
	return e
}

type codexModelEvidenceObserver struct {
	model                       upstreamResponseModelObserver
	firstSource, terminalSource string
	headers                     CodexModelEvidence
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

func (o *codexModelEvidenceObserver) observeHeaders(headers http.Header, scope string) {
	for key := range headers {
		if strings.EqualFold(key, "x-codex-safety-buffering-enabled") || strings.EqualFold(key, "x-codex-safety-buffering-faster-model") {
			if o.headers.HeaderEvidenceScope != "" && o.headers.HeaderEvidenceScope != scope {
				// Never label a previous connection hint as a new response's
				// evidence when metadata supplies only one of the two fields.
				o.headers.SafetyBufferingEnabled = nil
				o.headers.SafetyBufferingFasterModel = ""
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
}

func (o *codexModelEvidenceObserver) snapshot(sentModel string) CodexModelEvidence {
	e := o.headers.clone()
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
