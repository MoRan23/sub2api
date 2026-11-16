package service

import (
	"bytes"
	"encoding/json"
	"strconv"
	"unicode/utf8"
)

// Only the closed, ordinary delta shape bypasses map decoding. Unknown fields,
// escaped/duplicate envelope keys and non-integer indices use the existing path.
func anthropicSSEDeltaView(raw []byte, eventName string) (index int, deltaType string, ok bool) {
	if eventName != "" && eventName != "content_block_delta" {
		return 0, "", false
	}
	if !json.Valid(raw) || !utf8.Valid(raw) {
		return 0, "", false
	}
	var seen uint8
	var delta []byte
	if !walkValidJSONObject(raw, func(key, value []byte) bool {
		value = bytes.TrimSpace(value)
		var bit uint8
		switch string(key) {
		case `"type"`:
			bit = 1
			if !bytes.Equal(value, []byte(`"content_block_delta"`)) {
				return false
			}
		case `"index"`:
			bit = 2
			n, err := strconv.ParseUint(string(value), 10, 31)
			if err != nil {
				return false
			}
			index = int(n)
		case `"delta"`:
			bit, delta = 4, value
		default:
			return false
		}
		if seen&bit != 0 {
			return false
		}
		seen |= bit
		return true
	}) || seen != 7 {
		return 0, "", false
	}
	seen = 0
	var field uint8
	if !walkValidJSONObject(delta, func(key, value []byte) bool {
		value = bytes.TrimSpace(value)
		var bit uint8
		switch string(key) {
		case `"type"`:
			bit = 1
			switch string(value) {
			case `"text_delta"`:
				deltaType = "text_delta"
			case `"thinking_delta"`:
				deltaType = "thinking_delta"
			case `"input_json_delta"`:
				deltaType = "input_json_delta"
			case `"signature_delta"`:
				deltaType = "signature_delta"
			default:
				return false
			}
		case `"text"`, `"thinking"`, `"partial_json"`, `"signature"`:
			bit = 2
			if len(value) == 0 || value[0] != '"' {
				return false
			}
			switch string(key) {
			case `"text"`:
				field = 1
			case `"thinking"`:
				field = 2
			case `"partial_json"`:
				field = 3
			case `"signature"`:
				field = 4
			}
		default:
			return false
		}
		if seen&bit != 0 {
			return false
		}
		seen |= bit
		return true
	}) || seen != 3 {
		return 0, "", false
	}
	return index, deltaType, (deltaType == "text_delta" && field == 1) ||
		(deltaType == "thinking_delta" && field == 2) ||
		(deltaType == "input_json_delta" && field == 3) ||
		(deltaType == "signature_delta" && field == 4)
}
