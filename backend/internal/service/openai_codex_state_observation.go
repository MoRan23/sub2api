package service

import (
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
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
	RequestSource            string     `json:"request_source,omitempty"`
	ResponseObservedShape    string     `json:"response_observed_shape,omitempty"`
	ResponseCipherBlocks     int        `json:"response_cipher_blocks,omitempty"`
	ResponseValidationReason string     `json:"response_validation_reason,omitempty"`
	ExpiresAt                *time.Time `json:"expires_at,omitempty"`
	RenewalReason            string     `json:"renewal_reason,omitempty"`
	ObservationID            string     `json:"observation_id,omitempty"`
	RequestSentAt            *time.Time `json:"request_sent_at,omitempty"`
	BusinessDelivered        *bool      `json:"business_delivered,omitempty"`
	SnapshotVersion          int64      `json:"snapshot_version,omitempty"`
	credentialEpoch          string
	envelopeEvidence         codexTurnStateObservationEnvelope
}

// Private admission evidence supports display-only account-type projection.
// It is never serialized or reconstructed from an observed token length.
type codexTurnStateObservationEnvelope struct {
	checked, valid      bool
	issuedAt, expiresAt time.Time
}

type codexTurnStateWireObservation struct {
	mu              sync.Mutex
	value           CodexTurnStateObservation
	ownerAccountID  int64
	sequence        uint64
	summarySequence uint64
	finished        bool
	observedAt      time.Time
	attempt         *CodexTurnStateAttempt
	sendStartedAt   time.Time
	logged          bool
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
	// This public diagnostic ID must remain independent of private runtime lease
	// or collector lock identities.
	observationID := uuid.NewString()
	reason := attempt.MaintenanceReason
	if reason == "" && attempt.Snapshot.Token == "" {
		switch {
		case !attempt.AccountEnabled:
			reason = "cache_disabled"
		case attempt.accountType == "":
			reason = "account_type_unknown"
		default:
			reason = "cache_unavailable"
		}
	}
	attempt.mu.Lock()
	credentialEpoch := attempt.credentialEpoch
	attempt.mu.Unlock()
	observation := &codexTurnStateWireObservation{ownerAccountID: attempt.OwnerAccountID, attempt: attempt, value: CodexTurnStateObservation{Enabled: attempt.Enabled, AccountEnabled: attempt.AccountEnabled, MaintenanceReason: reason, Action: "passthrough", Model: attempt.Model, RequestSource: "business", ObservationID: observationID, credentialEpoch: credentialEpoch}}
	if attempt.Snapshot.Token != "" {
		observation.value.Action = "injected"
		observation.value.Source = attempt.Snapshot.Source
		observation.value.SnapshotVersion = attempt.Snapshot.Version
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
	if observation.sendStartedAt.IsZero() {
		observation.sendStartedAt = time.Now()
	}
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
	if entry != nil {
		entry.CodexTurnState = &copy
	}
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
	globalFingerprintObserver.updateCodexTurnStateObservation(seq, observation.value)
}

func bindCodexTurnStateSummarySequence(observation *codexTurnStateWireObservation) {
	if observation == nil {
		return
	}
	observation.mu.Lock()
	if observation.summarySequence == 0 {
		observation.summarySequence = globalCodexTurnStateSummaryStore.nextSequence()
	}
	if !observation.sendStartedAt.IsZero() {
		sentAt := observation.sendStartedAt
		observation.value.RequestSentAt = &sentAt
	}
	globalFingerprintObserver.updateCodexTurnStateObservation(observation.sequence, observation.value)
	globalCodexTurnStateSummaryStore.update(observation.summarySequence, observation.ownerAccountID, observation.value, observation.finished, observation.observedAt)
	observation.logCompletionLocked()
	attempt := observation.attempt
	sentAt := observation.sendStartedAt
	observation.mu.Unlock()
	if attempt != nil {
		attempt.mu.Lock()
		if !attempt.historyPhysicalBound {
			attempt.historyPhysicalBound = true
			if sentAt.IsZero() {
				sentAt = attempt.preparedAt
			}
			attempt.businessSentAt = sentAt
		}
		finished, service := attempt.finished, attempt.historyService
		attempt.mu.Unlock()
		// WS response delivery can race the successful-write callback.
		recordCodexDeliveredHistory(attempt)
		if finished && service != nil {
			service.completeBusinessSent(attempt)
		}
	}
}

func finishOpenAICodexStateObservation(attempt *CodexTurnStateAttempt) {
	if attempt == nil {
		return
	}
	attempt.mu.Lock()
	observation := attempt.wireObservation
	finished, delivered := attempt.finished, attempt.historyDelivered
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
	if finished {
		observation.value.BusinessDelivered = &delivered
	}
	observation.observedAt = safe.ObservedAt
	// A send error or response without a state must not replace an earlier
	// actual observation. Only Observe can provide its response timestamp.
	observation.value.ResponseLength = safe.TokenLength
	observation.value.ResponseSource = safe.ResponseSource
	observation.value.ResponseObservedShape = safe.ObservedShape
	observation.value.ResponseCipherBlocks = safe.CipherBlocks
	observation.value.ResponseValidationReason = safe.ValidationReason
	observation.value.envelopeEvidence = codexTurnStateObservationEnvelope{checked: true, valid: safe.EnvelopeValid, issuedAt: safe.IssuedAt, expiresAt: safe.ExpiresAt}
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
	globalFingerprintObserver.updateCodexTurnStateObservation(observation.sequence, observation.value)
	globalCodexTurnStateSummaryStore.update(observation.summarySequence, observation.ownerAccountID, observation.value, true, observation.observedAt)
	observation.logCompletionLocked()
}

// Only a bound physical send may emit a completion record. In particular, a WS
// response can finish before the successful-write callback; failed writes never
// acquire a summary sequence and must not invent a completed physical request.
func (observation *codexTurnStateWireObservation) logCompletionLocked() {
	if observation.logged || !observation.finished || observation.summarySequence == 0 {
		return
	}
	observation.logged = true
	logCodexTurnStateObservation(observation.ownerAccountID, observation.value, observation.observedAt)
}

// All logged values are server identifiers or reduced diagnostics. Never add
// request headers, response bodies, token bytes/digests or configuration epochs.
func logCodexTurnStateObservation(ownerAccountID int64, value CodexTurnStateObservation, observedAt time.Time) {
	fields := []any{
		"account_id", ownerAccountID, "model", value.Model, "observation_id", value.ObservationID,
		"request_source", value.RequestSource, "outbound_action", value.Action, "outbound_source", value.Source,
		"outbound_length", value.OutboundLength, "maintenance_reason", value.MaintenanceReason,
		"response_length", value.ResponseLength, "response_shape", value.ResponseShape,
		"response_source", value.ResponseSource,
	}
	if value.RequestSentAt != nil {
		fields = append(fields, "request_sent_at", *value.RequestSentAt)
	}
	if !observedAt.IsZero() {
		fields = append(fields, "response_observed_at", observedAt)
	}
	if value.BusinessDelivered != nil {
		fields = append(fields, "business_delivered", *value.BusinessDelivered)
	}
	if value.Action == "injected" {
		fields = append(fields, "snapshot_version", value.SnapshotVersion, "snapshot_expires_at", value.ExpiresAt)
	}
	slog.Info("openai_codex_turn_state_observation_completed", fields...)
}

// A fast response can finish between the actual write and binding its observation
// row. Both publishers hold the snapshot mutex so either ordering retains the
// latest response summary instead of silently losing or reverting it.
func (observer *fingerprintObserver) updateCodexTurnStateObservation(seq uint64, value CodexTurnStateObservation) {
	if seq == 0 || observer == nil {
		return
	}
	observer.mu.Lock()
	defer observer.mu.Unlock()
	if !observer.enabled.Load() {
		return
	}
	for i := range observer.ring {
		if observer.ring[i].SequenceID == seq {
			copy := value
			observer.ring[i].CodexTurnState = &copy
			break
		}
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
