package service

import (
	"context"
	"net/http"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const openAIResponseEvidenceContextKey = "openai_response_evidence"

type openAIResponseEvidenceRequestKey struct{}

// This state is scoped to one physical HTTP attempt or WS turn. It retains
// declarations only, never response bodies, credentials, cookies or opaque state.
type openAIResponseEvidenceState struct {
	mu           sync.Mutex
	observer     codexModelEvidenceObserver
	sentModel    string
	sequence     uint64
	upstreamKind string
}

func beginOpenAIResponseEvidence(c *gin.Context, sentModel string) *openAIResponseEvidenceState {
	state := &openAIResponseEvidenceState{sentModel: strings.TrimSpace(sentModel)}
	if c != nil {
		c.Set(openAIResponseEvidenceContextKey, state)
		if observer := upstreamResponseModelObserverFromContext(c); observer != nil {
			observer.evidence = state
		}
	}
	return state
}

func responseEvidenceFromContext(c *gin.Context) *openAIResponseEvidenceState {
	if c == nil {
		return nil
	}
	value, _ := c.Get(openAIResponseEvidenceContextKey)
	state, _ := value.(*openAIResponseEvidenceState)
	return state
}

func (s *openAIResponseEvidenceState) snapshot() CodexModelEvidence {
	if s == nil {
		return CodexModelEvidence{ModelRelation: "not_reported"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	evidence := s.observer.snapshot(s.sentModel)
	evidence.UpstreamKind = s.upstreamKind
	return evidence
}

func markOpenAIResponseEvidenceUpstreamKind(state *openAIResponseEvidenceState, kind string) {
	if state == nil {
		return
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	state.upstreamKind = strings.TrimSpace(kind)
	state.publishLocked()
}

func (s *openAIResponseEvidenceState) publishLocked() {
	if s.sequence == 0 || globalFingerprintObserver == nil {
		return
	}
	evidence := s.observer.snapshot(s.sentModel)
	evidence.UpstreamKind = s.upstreamKind
	globalFingerprintObserver.updateResponseEvidence(s.sequence, evidence)
}

func bindOpenAIResponseEvidence(state *openAIResponseEvidenceState, sequence uint64) {
	if state == nil {
		return
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	state.sequence = sequence
	state.publishLocked()
}

func observeOpenAIResponseEvidenceHeaders(state *openAIResponseEvidenceState, headers http.Header, scope string) {
	if state == nil {
		return
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	state.observer.observeHeaders(headers, scope)
	state.publishLocked()
}

func observeOpenAIResponseEvidenceEvent(state *openAIResponseEvidenceState, payload []byte) {
	if state == nil {
		return
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	state.observer.observePayload(payload)
	state.publishLocked()
}

func markOpenAIResponseEvidenceHTTPRequest(request *http.Request, c *gin.Context) *http.Request {
	if request == nil {
		return nil
	}
	state := responseEvidenceFromContext(c)
	if state == nil {
		state = beginOpenAIResponseEvidence(c, "")
	}
	// The final projection already captured the model. Reuse that scalar rather
	// than copying a potentially large prompt just to inspect its model field.
	if c != nil {
		state.mu.Lock()
		if state.sentModel == "" {
			state.sentModel = c.GetString(OpsUpstreamModelKey)
		}
		state.mu.Unlock()
	}
	return withOpenAIExcelGatewayContext(request.WithContext(context.WithValue(request.Context(), openAIResponseEvidenceRequestKey{}, state)), c)
}

func observeOpenAIHTTPResponseEvidence(request *http.Request, response *http.Response) {
	if request == nil || response == nil {
		return
	}
	state, _ := request.Context().Value(openAIResponseEvidenceRequestKey{}).(*openAIResponseEvidenceState)
	if state == nil {
		return
	}
	observeOpenAIResponseEvidenceHeaders(state, response.Header, "response")
	if response.Request == nil {
		response.Request = request
	}
	response.Request = response.Request.WithContext(context.WithValue(response.Request.Context(), openAIResponseEvidenceRequestKey{}, state))
}

func observeOpenAIHTTPResponseEvidencePayload(response *http.Response, payload []byte) {
	if response == nil || response.Request == nil {
		return
	}
	state, _ := response.Request.Context().Value(openAIResponseEvidenceRequestKey{}).(*openAIResponseEvidenceState)
	observeOpenAIResponseEvidenceEvent(state, payload)
}

func responseEvidenceModelFromBody(body []byte) string {
	return strings.TrimSpace(gjson.GetBytes(body, "model").String())
}

func (o *fingerprintObserver) updateResponseEvidence(sequence uint64, evidence CodexModelEvidence) {
	if o == nil || sequence == 0 || !o.enabled.Load() {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.enabled.Load() {
		return
	}
	for index := range o.ring {
		if o.ring[index].SequenceID == sequence {
			snapshot := evidence.clone()
			o.ring[index].ResponseEvidence = &snapshot
			return
		}
	}
}
