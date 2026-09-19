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
)

type CodexTurnStateShape struct {
	Shape        string
	TokenLength  int
	CipherBlocks int
	IssuedAt     time.Time
	ExpiresAt    time.Time
}

// ParseCodexTurnState checks only the public Fernet envelope. Without the
// upstream secret it cannot authenticate/decrypt the contents or assess quality.
func ParseCodexTurnState(token, accountType string, now time.Time) (CodexTurnStateShape, error) {
	result := CodexTurnStateShape{Shape: CodexTurnStateShapeInvalid, TokenLength: len(token)}
	if accountType != "personal" && accountType != "team_business" {
		return result, errors.New("account_type_unknown")
	}
	if strings.TrimSpace(token) != token || len(token) > 4096 || len(token) < 100 {
		return result, errors.New("invalid_encoding")
	}
	decoded, err := base64.URLEncoding.Strict().DecodeString(token)
	if err != nil {
		decoded, err = base64.RawURLEncoding.Strict().DecodeString(token)
	}
	if err != nil || len(decoded) < 73 || decoded[0] != 0x80 || (len(decoded)-57)%16 != 0 {
		return result, errors.New("invalid_envelope")
	}
	// Require canonical padded Base64url; the accepted target lengths include '='.
	if base64.URLEncoding.EncodeToString(decoded) != token {
		return result, errors.New("invalid_encoding")
	}
	ts := binary.BigEndian.Uint64(decoded[1:9])
	if ts > uint64(now.Add(30*time.Second).Unix()) {
		return result, errors.New("future_issued_at")
	}
	result.IssuedAt = time.Unix(int64(ts), 0).UTC()
	result.ExpiresAt = result.IssuedAt.Add(CodexTurnStateLifetime)
	if !result.ExpiresAt.After(now) {
		return result, errors.New("expired")
	}
	result.CipherBlocks = (len(decoded) - 57) / 16
	targetLen, targetBlocks, extendedLen := 292, 10, 312
	if accountType == "team_business" {
		targetLen, targetBlocks, extendedLen = 332, 12, 356
	}
	switch {
	case len(token) == targetLen && result.CipherBlocks == targetBlocks:
		result.Shape = CodexTurnStateShapeTarget
	case len(token) == extendedLen && result.CipherBlocks == targetBlocks+1:
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
