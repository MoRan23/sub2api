package service

import "github.com/tidwall/gjson"

// OpenAIDaybreakObservation contains only a bounded field value and the decision
// made for this send. It never retains a prompt, response, or capability catalog.
type OpenAIDaybreakObservation struct {
	CyberPresent   bool   `json:"cyber_present"`
	CyberValue     string `json:"cyber_value,omitempty"`
	CyberType      string `json:"cyber_type"`
	Source         string `json:"source"`
	Reason         string `json:"reason"`
	ValueTruncated bool   `json:"value_truncated,omitempty"`
}

func observeOpenAIDaybreak(body []byte, decisions ...string) *OpenAIDaybreakObservation {
	// Handshakes and legacy header-only observations contain no request body.
	if len(body) == 0 {
		return nil
	}
	observation := &OpenAIDaybreakObservation{CyberType: "missing", Source: "unobserved", Reason: "decision_unobserved"}
	if !gjson.ValidBytes(body) || !gjson.ParseBytes(body).IsObject() {
		observation.CyberType, observation.Reason = "unavailable", "invalid_request"
		return observation
	}
	value := gjson.GetBytes(body, "access_programs.cyber")
	observation.CyberPresent = value.Exists()
	if value.Exists() {
		switch value.Type {
		case gjson.String:
			observation.CyberType, observation.CyberValue = "string", value.String()
		case gjson.Null:
			observation.CyberType, observation.CyberValue = "null", "null"
		case gjson.True, gjson.False:
			observation.CyberType, observation.CyberValue = "boolean", value.Raw
		case gjson.Number:
			observation.CyberType, observation.CyberValue = "number", value.Raw
		case gjson.JSON:
			observation.CyberType = "object"
			if value.IsArray() {
				observation.CyberType = "array"
			}
		}
		// Complex values are described by type only. Bound invalid scalar values
		// too, without truncating or otherwise changing the actual request.
		if runes := []rune(observation.CyberValue); len(runes) > 128 {
			observation.CyberValue = string(runes[:128])
			observation.ValueTruncated = true
		}
	}
	if len(decisions) == 0 || decisions[0] == "" {
		return observation
	}
	observation.Reason = decisions[0]
	switch {
	case observation.Reason == "automatic":
		if value.Type != gjson.String || (value.String() != "daybreak_blue" && value.String() != "daybreak_red") {
			observation.Reason = "final_value_changed"
			return observation
		}
		observation.Source = "automatic"
	case value.Exists():
		observation.Source = "client"
	default:
		observation.Source = "not_added"
	}
	return observation
}
