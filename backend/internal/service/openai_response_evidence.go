package service

import (
	"context"
	"errors"
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
	mu        sync.Mutex
	observer  codexModelEvidenceObserver
	sentModel string
	sequence  uint64
	accountID int64
	requestID string
}

// Reset only a request-bound attempt, never the ambient Gin state: auxiliary
// requests and late responses must not overwrite a different inference attempt.
func prepareOpenAIResponseEvidenceAttempt(request *http.Request, account *Account) {
	if request == nil {
		return
	}
	state, _ := request.Context().Value(openAIResponseEvidenceRequestKey{}).(*openAIResponseEvidenceState)
	if state == nil {
		return
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	state.requestID = ""
	state.observer = codexModelEvidenceObserver{}
	if account != nil {
		state.accountID = account.ID
	}
}

func openAIHeaderDiagnosticsForOps(c *gin.Context, accountID int64, requestID string) *OpenAIHeaderDiagnostics {
	state := responseEvidenceFromContext(c)
	if state == nil {
		return nil
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.accountID != 0 && state.accountID != accountID {
		return nil
	}
	if state.requestID != "" && requestID != "" && state.requestID != strings.TrimSpace(requestID) {
		return nil
	}
	return state.observer.headers.HeaderDiagnostics.clone()
}

// Failed handshakes have no response turn or successful handshake observation.
// Capture only their own response headers; an earlier turn's evidence is stale.
func observeOpenAIWSDialDiagnostics(c *gin.Context, account *Account, err error) {
	var dialErr *openAIWSDialError
	if !errors.As(err, &dialErr) || dialErr == nil {
		return
	}
	state := beginOpenAIResponseEvidence(c, "")
	state.mu.Lock()
	if account != nil {
		state.accountID = account.ID
	}
	state.requestID = strings.TrimSpace(dialErr.ResponseHeaders.Get("x-request-id"))
	requestID := state.requestID
	state.mu.Unlock()
	observeOpenAIResponseEvidenceHeaders(state, dialErr.ResponseHeaders, "connection")
	diagnostics := state.snapshot().HeaderDiagnostics
	if diagnostics == nil || account == nil {
		return
	}
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		Platform: account.Platform, AccountID: account.ID, AccountName: account.Name,
		ProxyID: opsUpstreamProxyID(account), ProxyName: opsUpstreamProxyName(account),
		UpstreamStatusCode: dialErr.StatusCode, UpstreamRequestID: requestID,
		HeaderDiagnostics: diagnostics, Kind: "ws_handshake_error", Stage: "handshake",
		Message: "Upstream WebSocket handshake failed",
	})
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
	return s.observer.snapshot(s.sentModel)
}

func (s *openAIResponseEvidenceState) publishLocked() {
	if s.sequence == 0 || globalFingerprintObserver == nil {
		return
	}
	globalFingerprintObserver.updateResponseEvidence(s.sequence, s.observer.snapshot(s.sentModel))
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
	return request.WithContext(context.WithValue(request.Context(), openAIResponseEvidenceRequestKey{}, state))
}

func observeOpenAIHTTPResponseEvidence(request *http.Request, response *http.Response) {
	if request == nil || response == nil {
		return
	}
	state, _ := request.Context().Value(openAIResponseEvidenceRequestKey{}).(*openAIResponseEvidenceState)
	if state == nil {
		return
	}
	state.mu.Lock()
	state.requestID = strings.TrimSpace(response.Header.Get("x-request-id"))
	state.mu.Unlock()
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
