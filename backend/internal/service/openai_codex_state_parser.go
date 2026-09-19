package service

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const (
	CodexTurnStateShapeTarget   = "target"
	CodexTurnStateShapeExtended = "extended"
	CodexTurnStateShapeInvalid  = "invalid"

	CodexTurnStateObservedPersonalTarget       = "personal_target"
	CodexTurnStateObservedPersonalExtended     = "personal_extended"
	CodexTurnStateObservedTeamBusinessTarget   = "team_business_target"
	CodexTurnStateObservedTeamBusinessExtended = "team_business_extended"
)

type CodexTurnStateShape struct {
	Shape        string
	TokenLength  int
	CipherBlocks int
	IssuedAt     time.Time
	ExpiresAt    time.Time
}

// CodexTurnStateEnvelope describes observable public fields only. ObservedShape
// names an envelope shape; it does not identify the account's subscription.
type CodexTurnStateEnvelope struct {
	TokenLength      int
	CipherBlocks     int
	IssuedAt         time.Time
	ExpiresAt        time.Time
	ObservedShape    string
	ValidationReason string
}

// InspectCodexTurnStateEnvelope validates the public Fernet envelope without
// consulting the account type. Without the upstream secret it cannot authenticate
// or decrypt the contents, infer a subscription, or assess model quality.
// Time-invalid envelopes retain their structural shape for passive observation.
func InspectCodexTurnStateEnvelope(token string, now time.Time) (CodexTurnStateEnvelope, error) {
	result := CodexTurnStateEnvelope{ObservedShape: CodexTurnStateShapeInvalid, TokenLength: len(token)}
	reject := func(reason string) (CodexTurnStateEnvelope, error) {
		result.ValidationReason = reason
		return result, errors.New(reason)
	}
	if strings.TrimSpace(token) != token || len(token) > 4096 || len(token) < 100 {
		return reject("invalid_encoding")
	}
	decoded, err := base64.URLEncoding.Strict().DecodeString(token)
	if err != nil {
		decoded, err = base64.RawURLEncoding.Strict().DecodeString(token)
	}
	if err != nil || len(decoded) < 73 || decoded[0] != 0x80 || (len(decoded)-57)%16 != 0 {
		return reject("invalid_envelope")
	}
	// Require canonical padded Base64url; the accepted target lengths include '='.
	if base64.URLEncoding.EncodeToString(decoded) != token {
		return reject("invalid_encoding")
	}
	result.CipherBlocks = (len(decoded) - 57) / 16
	switch {
	case len(token) == 292 && result.CipherBlocks == 10:
		result.ObservedShape = CodexTurnStateObservedPersonalTarget
	case len(token) == 312 && result.CipherBlocks == 11:
		result.ObservedShape = CodexTurnStateObservedPersonalExtended
	case len(token) == 332 && result.CipherBlocks == 12:
		result.ObservedShape = CodexTurnStateObservedTeamBusinessTarget
	case len(token) == 356 && result.CipherBlocks == 13:
		result.ObservedShape = CodexTurnStateObservedTeamBusinessExtended
	}
	ts := binary.BigEndian.Uint64(decoded[1:9])
	if ts > uint64(now.Add(30*time.Second).Unix()) {
		return reject("future_issued_at")
	}
	result.IssuedAt = time.Unix(int64(ts), 0).UTC()
	result.ExpiresAt = result.IssuedAt.Add(CodexTurnStateLifetime)
	if !result.ExpiresAt.After(now) {
		return reject("expired")
	}
	if result.ObservedShape == CodexTurnStateShapeInvalid {
		return reject("unexpected_shape")
	}
	return result, nil
}

// ParseCodexTurnState applies account-specific cache admission after inspecting
// the public envelope. Unknown account types remain ineligible regardless of the
// observed shape, and extended states remain signals rather than cache targets.
func ParseCodexTurnState(token, accountType string, now time.Time) (CodexTurnStateShape, error) {
	result := CodexTurnStateShape{Shape: CodexTurnStateShapeInvalid, TokenLength: len(token)}
	if accountType != "personal" && accountType != "team_business" {
		return result, errors.New("account_type_unknown")
	}
	envelope, err := InspectCodexTurnStateEnvelope(token, now)
	result.CipherBlocks, result.IssuedAt, result.ExpiresAt = envelope.CipherBlocks, envelope.IssuedAt, envelope.ExpiresAt
	if err != nil {
		return result, err
	}
	switch {
	case accountType == "personal" && envelope.ObservedShape == CodexTurnStateObservedPersonalTarget,
		accountType == "team_business" && envelope.ObservedShape == CodexTurnStateObservedTeamBusinessTarget:
		result.Shape = CodexTurnStateShapeTarget
	case accountType == "personal" && envelope.ObservedShape == CodexTurnStateObservedPersonalExtended,
		accountType == "team_business" && envelope.ObservedShape == CodexTurnStateObservedTeamBusinessExtended:
		result.Shape = CodexTurnStateShapeExtended
	default:
		return result, errors.New("unexpected_shape")
	}
	return result, nil
}

// Only response.metadata carries protocol state. No arbitrary nested headers or
// generated output text is searched for tokens.
func CodexTurnStateTokensFromEvent(data []byte) []string {
	var event struct {
		Type    string                     `json:"type"`
		Headers map[string]json.RawMessage `json:"headers"`
	}
	if json.Unmarshal(data, &event) != nil || event.Type != "response.metadata" {
		return nil
	}
	var values []string
	appendHeaders := func(headers map[string]json.RawMessage) {
		for key, raw := range headers {
			if !strings.EqualFold(key, "x-codex-turn-state") {
				continue
			}
			var value string
			if json.Unmarshal(raw, &value) == nil && value != "" {
				values = append(values, value)
				continue
			}
			var list []string
			if json.Unmarshal(raw, &list) == nil {
				values = append(values, list...)
			}
		}
	}
	appendHeaders(event.Headers)
	return values
}
