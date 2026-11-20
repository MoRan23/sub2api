package service

import (
	"bytes"
	"encoding/json"
	"unicode/utf8"
)

// walkValidJSONObject borrows spans from a previously validated object. It never
// decodes value strings, including escaped multi-megabyte input values.
func walkValidJSONObject(raw []byte, visit func(key, value []byte) bool) bool {
	i := skipOpenAIWSJSONSpace(raw, 0)
	if i >= len(raw) || raw[i] != '{' {
		return false
	}
	i++
	for {
		i = skipOpenAIWSJSONSpace(raw, i)
		if raw[i] == '}' {
			return true
		}
		keyStart := i
		i = scanOpenAIWSJSONString(raw, i)
		key := raw[keyStart:i]
		i = skipOpenAIWSJSONSpace(raw, i) + 1 // colon
		i = skipOpenAIWSJSONSpace(raw, i)
		valueStart := i
		i = skipOpenAIWSJSONValue(raw, i)
		if !visit(key, raw[valueStart:i]) {
			return false
		}
		i = skipOpenAIWSJSONSpace(raw, i)
		if raw[i] == ',' {
			i++
		}
	}
}

func decodeOpenAIWSComparableObject(payload []byte) (map[string]any, error) {
	trimmed := bytes.TrimSpace(payload)
	var decoded map[string]any
	if len(trimmed) == 0 || trimmed[0] != '{' || !utf8.Valid(trimmed) || !json.Valid(trimmed) {
		err := decodeOpenAIJSONUseNumber(payload, &decoded)
		return decoded, err
	}
	decoded = make(map[string]any)
	var decodeErr error
	walkValidJSONObject(trimmed, func(rawKey, rawValue []byte) bool {
		var key string
		if decodeErr = json.Unmarshal(rawKey, &key); decodeErr != nil {
			return false
		}
		switch key {
		case "input", "previous_response_id", "client_metadata", "stream_options", "request_country", "access_programs":
			return true
		}
		var value any
		if decodeErr = decodeOpenAIJSONUseNumber(rawValue, &value); decodeErr != nil {
			return false
		}
		decoded[key] = value
		return true
	})
	return decoded, decodeErr
}

// Owned by one ingress goroutine. The cache only retains the small comparison
// result, never the input body; every outer-payload replacement invalidates it.
type openAIWSComparableCache struct {
	revision, computedRevision uint64
	ready                      bool
	canonical                  []byte
	err                        error
}

func (c *openAIWSComparableCache) invalidate() {
	c.revision++
	c.ready = false
	c.canonical, c.err = nil, nil
}

func (c *openAIWSComparableCache) get(payload []byte) ([]byte, error) {
	if !c.ready || c.computedRevision != c.revision {
		c.canonical, c.err = normalizeOpenAIWSPayloadWithoutInputAndPreviousResponseID(payload)
		c.computedRevision, c.ready = c.revision, true
	}
	return c.canonical, c.err
}

func openAIWSComparable(payload []byte, caches ...*openAIWSComparableCache) ([]byte, error) {
	if len(caches) != 0 && caches[0] != nil {
		return caches[0].get(payload)
	}
	return normalizeOpenAIWSPayloadWithoutInputAndPreviousResponseID(payload)
}
