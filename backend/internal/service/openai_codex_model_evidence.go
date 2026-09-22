package service

import (
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openaicookies"
	"github.com/tidwall/gjson"
)

// CodexModelEvidence contains only upstream declarations from one physical
// response. Header hints never stand in for the response body's model.
type CodexModelEvidence struct {
	UpstreamResponseModel      string                 `json:"upstream_response_model,omitempty"`
	ModelRelation              string                 `json:"model_relation"`
	ModelConflict              bool                   `json:"model_conflict"`
	ModelEvidenceSource        string                 `json:"model_evidence_source,omitempty"`
	SafetyBufferingEnabled     *bool                  `json:"safety_buffering_enabled,omitempty"`
	SafetyBufferingFasterModel string                 `json:"safety_buffering_faster_model,omitempty"`
	HeaderEvidenceScope        string                 `json:"header_evidence_scope,omitempty"`
	CookieDiagnostic           *CodexCookieDiagnostic `json:"cookie_diagnostic,omitempty"`
}

type CodexCookieDiagnosticCookie struct {
	Name      string     `json:"name"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// Names and absolute expiry only: neither cookie values nor recoverable hashes.
type CodexCookieDiagnostic struct {
	SendState    string                        `json:"send_state,omitempty"`
	Sent         bool                          `json:"sent"`
	Source       string                        `json:"source,omitempty"`
	Names        []string                      `json:"names,omitempty"`
	Cookies      []CodexCookieDiagnosticCookie `json:"cookies,omitempty"`
	Reason       string                        `json:"reason,omitempty"`
	SentCount    int                           `json:"sent_count"`
	SavedCount   int                           `json:"saved_count"`
	DeletedCount int                           `json:"deleted_count"`
}

func codexCookieDiagnostic(value openaicookies.Diagnostic) *CodexCookieDiagnostic {
	d := &CodexCookieDiagnostic{SendState: value.SendState, Sent: value.Sent, Source: value.Source, Names: append([]string(nil), value.Names...), Reason: value.Reason, SentCount: value.SentCount, SavedCount: value.SavedCount, DeletedCount: value.DeletedCount}
	for _, cookie := range value.Cookies {
		item := CodexCookieDiagnosticCookie{Name: cookie.Name}
		if cookie.ExpiresAt != nil {
			expiry := *cookie.ExpiresAt
			item.ExpiresAt = &expiry
		}
		d.Cookies = append(d.Cookies, item)
	}
	return d
}

func (e CodexModelEvidence) clone() CodexModelEvidence {
	if e.SafetyBufferingEnabled != nil {
		v := *e.SafetyBufferingEnabled
		e.SafetyBufferingEnabled = &v
	}
	if e.CookieDiagnostic != nil {
		d := *e.CookieDiagnostic
		d.Names = append([]string(nil), d.Names...)
		d.Cookies = append([]CodexCookieDiagnosticCookie(nil), d.Cookies...)
		for i := range d.Cookies {
			if d.Cookies[i].ExpiresAt != nil {
				expiry := *d.Cookies[i].ExpiresAt
				d.Cookies[i].ExpiresAt = &expiry
			}
		}
		e.CookieDiagnostic = &d
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

func observeCodexModelHeaders(a *CodexTurnStateAttempt, headers http.Header, scope string) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.finished {
		a.modelEvidence.observeHeaders(headers, scope)
	}
}

func observeCodexModelEvent(a *CodexTurnStateAttempt, event []byte) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.finished {
		a.modelEvidence.observePayload(event)
	}
}

func observeCodexCookies(a *CodexTurnStateAttempt, diagnostic openaicookies.Diagnostic) {
	if a == nil {
		return
	}
	a.mu.Lock()
	if !a.finished {
		a.modelEvidence.headers.CookieDiagnostic = codexCookieDiagnostic(diagnostic)
	}
	deferred, observation, binding := a.deferHTTPActivity, a.wireObservation, a.OutboundBinding
	a.mu.Unlock()
	if !deferred {
		return
	}
	if observation == nil {
		// Account tests and auxiliary HTTP callers may have no Gin observation.
		// Their activity is still bound only to the actual transport boundary.
		if diagnostic.SendState == "sent" {
			a.mu.Lock()
			first := !a.historyPhysicalBound
			if first {
				a.historyPhysicalBound, a.businessSentAt = true, time.Now()
			}
			service := a.historyService
			a.mu.Unlock()
			if first && service != nil {
				recordCodexDeliveredHistory(a)
				service.completeBusinessSent(a)
			}
		}
		return
	}
	observation.mu.Lock()
	observation.value.CookieDiagnostic = codexCookieDiagnostic(diagnostic)
	firstSend := diagnostic.SendState == "sent" && !observation.httpSendReached
	if firstSend {
		observation.httpSendReached = true
		observation.sendStartedAt = time.Now()
		if binding.EgressKind == "proxy" || binding.EgressKind == "direct" {
			id := binding.ProxyID
			observation.value.ActualProxyID = &id
			observation.value.RouteSource = "account"
			if observation.value.Action == "injected" {
				observation.value.RouteSource = "bundle"
			}
		}
	} else if diagnostic.SendState == "not_sent" && !observation.httpSendReached {
		observation.value.ActualProxyID = nil
		observation.value.RouteSource = ""
		observation.value.RequestSentAt = nil
	}
	globalFingerprintObserver.updateCodexTurnStateObservation(observation.sequence, observation.value)
	observation.mu.Unlock()
	if firstSend {
		bindCodexTurnStateSummarySequence(observation)
	}
}

// ModelMismatch is an admission decision only for a completed collector
// response. Transport, authentication and stream errors retain precedence.
func (r CodexTurnStateCollectResult) ModelMismatch() bool {
	return r.completed && (r.ModelEvidence.ModelRelation == "different" || r.ModelEvidence.ModelRelation == "conflicting")
}
