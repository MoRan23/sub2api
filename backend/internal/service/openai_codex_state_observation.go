package service

import (
	"net/http"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const codexStateWireObservationKey = "openai_codex_state_wire_observation"

// CodexTurnStateObservation deliberately contains neither tokens nor fingerprints
// of tokens. Its outbound length is populated from the actual physical send.
type CodexTurnStateObservation struct {
	Enabled                  bool       `json:"enabled"`
	AccountEnabled           bool       `json:"account_enabled"`
	MaintenanceReason        string     `json:"maintenance_reason,omitempty"`
	Action                   string     `json:"action"`
	Source                   string     `json:"source,omitempty"`
	Model                    string     `json:"model"`
	OutboundLength           int        `json:"outbound_length"`
	OutboundHeaderLength     int        `json:"outbound_header_length,omitempty"`
	OutboundBodyLength       int        `json:"outbound_body_length,omitempty"`
	OutboundCarrier          string     `json:"outbound_carrier,omitempty"`
	ResponseLength           int        `json:"response_length,omitempty"`
	ResponseShape            string     `json:"response_shape,omitempty"`
	ResponseSource           string     `json:"response_source,omitempty"`
	ResponseObservedShape    string     `json:"response_observed_shape,omitempty"`
	ResponseCipherBlocks     int        `json:"response_cipher_blocks,omitempty"`
	ResponseValidationReason string     `json:"response_validation_reason,omitempty"`
	ExpiresAt                *time.Time `json:"expires_at,omitempty"`
	RenewalReason            string     `json:"renewal_reason,omitempty"`
}

type codexTurnStateWireObservation struct {
	mu             sync.Mutex
	value          CodexTurnStateObservation
	ownerAccountID int64
	window         uint64
	sequence       uint64
	finished       bool
	observedAt     time.Time
}

// codexStateBodyPatch is private request-local evidence, never input from a
// client. The entire adaptation is checked to change only the state carrier.
type codexStateBodyPatch struct{ expected string }

func newCodexStateBodyPatch(before, after []byte) *codexStateBodyPatch {
	left, leftErr := decodeRequestIntegrityBody(before)
	right, rightErr := decodeRequestIntegrityBody(after)
	if leftErr != "" || rightErr != "" {
		return nil
	}
	state := gjson.GetBytes(after, "client_metadata.x-codex-turn-state")
	if state.Type != gjson.String {
		return nil
	}
	if gjson.GetBytes(before, "client_metadata.x-codex-turn-state").Raw == state.Raw {
		return nil
	}
	beforeMetadata, beforePresent := left["client_metadata"]
	afterMetadata, ok := right["client_metadata"].(map[string]any)
	if !ok {
		return nil
	}
	copyMetadata := make(map[string]any, len(afterMetadata))
	for key, value := range afterMetadata {
		copyMetadata[key] = value
	}
	if original, ok := beforeMetadata.(map[string]any); ok {
		if old, existed := original[openAICodexTurnStateHeader]; existed {
			copyMetadata[openAICodexTurnStateHeader] = old
		} else {
			delete(copyMetadata, openAICodexTurnStateHeader)
		}
		right["client_metadata"] = copyMetadata
	} else if !beforePresent {
		delete(copyMetadata, openAICodexTurnStateHeader)
		if len(copyMetadata) != 0 {
			return nil
		}
		delete(right, "client_metadata")
	} else {
		return nil
	}
	if !reflect.DeepEqual(left, right) {
		return nil
	}
	return &codexStateBodyPatch{expected: state.String()}
}

func noteOpenAICodexStatePatch(c *gin.Context, attempt *CodexTurnStateAttempt, before, after []byte) {
	if capture := openAIIntegrityCaptureFromContext(c); capture != nil {
		capture.mu.Lock()
		capture.codexStatePatch = newCodexStateBodyPatch(before, after)
		capture.mu.Unlock()
	}
	if c == nil {
		return
	}
	c.Set(codexStateWireObservationKey, (*codexTurnStateWireObservation)(nil))
	if attempt == nil {
		return
	}
	observation := &codexTurnStateWireObservation{ownerAccountID: attempt.OwnerAccountID, window: globalFingerprintObserver.codexStateObservationWindow(), value: CodexTurnStateObservation{Enabled: attempt.Enabled, AccountEnabled: attempt.AccountEnabled, MaintenanceReason: attempt.MaintenanceReason, Action: "passthrough", Model: attempt.Model}}
	if attempt.Snapshot.Token != "" {
		observation.value.Action = "injected"
		observation.value.Source = attempt.Snapshot.Source
		expires := attempt.Snapshot.ExpiresAt
		observation.value.ExpiresAt = &expires
	}
	attempt.mu.Lock()
	attempt.wireObservation = observation
	attempt.mu.Unlock()
	c.Set(codexStateWireObservationKey, observation)
}

func codexStateWireObservation(c *gin.Context) *codexTurnStateWireObservation {
	if c == nil {
		return nil
	}
	value, _ := c.Get(codexStateWireObservationKey)
	observation, _ := value.(*codexTurnStateWireObservation)
	return observation
}

func populateCodexTurnStateObservation(c *gin.Context, entry *FingerprintObservationEntry, headers http.Header, body []byte, frame bool) *codexTurnStateWireObservation {
	observation := codexStateWireObservation(c)
	if observation == nil {
		return nil
	}
	bodyLength := 0
	if state := gjson.GetBytes(body, "client_metadata.x-codex-turn-state"); state.Type == gjson.String {
		bodyLength = len(state.String())
	}
	observation.mu.Lock()
	headerLength := observation.value.OutboundHeaderLength
	if !frame {
		headerLength = codexTurnStateHeaderLength(headers)
	}
	observation.value.OutboundHeaderLength = headerLength
	observation.value.OutboundBodyLength = bodyLength
	observation.value.OutboundLength = headerLength
	switch {
	case headerLength > 0 && bodyLength > 0:
		observation.value.OutboundCarrier = "header_and_body"
		if frame {
			observation.value.OutboundCarrier = "ws_handshake_and_frame"
		}
	case headerLength > 0:
		observation.value.OutboundCarrier = "header"
		if frame {
			observation.value.OutboundCarrier = "ws_handshake"
		}
	case bodyLength > 0:
		observation.value.OutboundCarrier = "body"
		if frame {
			observation.value.OutboundCarrier = "ws_frame"
		}
	}
	if headerLength == 0 || (frame && bodyLength > 0) {
		observation.value.OutboundLength = bodyLength
	}
	if observation.value.Action != "injected" && observation.value.OutboundLength > 0 {
		observation.value.Source = "client"
	}
	copy := observation.value
	observation.mu.Unlock()
	entry.CodexTurnState = &copy
	return observation
}

// Compute this directly from physical request headers, before the safe header
// snapshot discards token values. The retained integer cannot recover a token.
func codexTurnStateHeaderLength(headers http.Header) int {
	for name, values := range headers {
		if strings.EqualFold(name, openAICodexTurnStateHeader) && len(values) > 0 {
			return len(values[0])
		}
	}
	return 0
}

func observeCodexTurnStateWSHandshakeLength(attempt *CodexTurnStateAttempt, length int) {
	if attempt == nil {
		return
	}
	attempt.mu.Lock()
	observation := attempt.wireObservation
	attempt.mu.Unlock()
	if observation != nil {
		observation.mu.Lock()
		observation.value.OutboundHeaderLength = length
		observation.mu.Unlock()
	}
}

func bindCodexTurnStateObservationSequence(observation *codexTurnStateWireObservation, seq uint64) {
	if observation == nil {
		return
	}
	observation.mu.Lock()
	defer observation.mu.Unlock()
	observation.sequence = seq
	globalFingerprintObserver.updateCodexTurnStateObservation(seq, observation.ownerAccountID, observation.value, observation.finished, observation.observedAt, observation.window)
}

func finishOpenAICodexStateObservation(attempt *CodexTurnStateAttempt) {
	if attempt == nil {
		return
	}
	attempt.mu.Lock()
	observation := attempt.wireObservation
	attempt.mu.Unlock()
	if observation == nil {
		return
	}
	safe := attempt.SafeObservation()
	observation.mu.Lock()
	defer observation.mu.Unlock()
	if observation.finished {
		return
	}
	observation.finished = true
	observation.observedAt = safe.ObservedAt
	// A send error or response without a state must not replace an earlier
	// actual observation. Only Observe can provide its response timestamp.
	observation.value.ResponseLength = safe.TokenLength
	observation.value.ResponseSource = safe.ResponseSource
	observation.value.ResponseObservedShape = safe.ObservedShape
	observation.value.ResponseCipherBlocks = safe.CipherBlocks
	observation.value.ResponseValidationReason = safe.ValidationReason
	switch safe.Shape {
	case CodexTurnStateShapeTarget:
		observation.value.ResponseShape = "target"
	case CodexTurnStateShapeExtended:
		observation.value.ResponseShape = "suspect"
	case "expired":
		observation.value.ResponseShape = "expired"
	case "":
	default:
		observation.value.ResponseShape = "unknown"
	}
	observation.value.RenewalReason = safe.RefreshReason
	globalFingerprintObserver.updateCodexTurnStateObservation(observation.sequence, observation.ownerAccountID, observation.value, true, observation.observedAt, observation.window)
}

// A fast response can finish between the actual write and binding its observation
// row. Both publishers hold the snapshot mutex so either ordering retains the
// latest response summary instead of silently losing or reverting it.
func (observer *fingerprintObserver) updateCodexTurnStateObservation(seq uint64, ownerAccountID int64, value CodexTurnStateObservation, finished bool, observedAt time.Time, window uint64) {
	if seq == 0 || observer == nil {
		return
	}
	observer.mu.Lock()
	defer observer.mu.Unlock()
	if !observer.enabled.Load() || window == 0 || window != observer.codexStateIndex.window+1 || seq > observer.seq || seq <= observer.codexStateIndex.discardThrough {
		return
	}
	for i := range observer.ring {
		if observer.ring[i].SequenceID == seq {
			copy := value
			observer.ring[i].CodexTurnState = &copy
			break
		}
	}
	if finished {
		observer.codexStateIndex.record(seq, ownerAccountID, value, observedAt)
	}
}

func codexStatePatchMatches(patch *codexStateBodyPatch, body map[string]any) bool {
	if patch == nil {
		return true
	}
	metadata, ok := body["client_metadata"].(map[string]any)
	if !ok {
		return false
	}
	state, ok := metadata[openAICodexTurnStateHeader].(string)
	return ok && state == patch.expected
}
