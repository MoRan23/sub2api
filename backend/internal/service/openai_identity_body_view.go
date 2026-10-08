package service

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/tidwall/gjson"
)

// decodeOpenAIIdentityBodyView borrows top-level values from an immutable body.
// Identity projection only replaces or deletes map entries; it must not mutate
// these slices. In particular, input/history is never decoded or copied just to
// update client_metadata. The view is request-local and must not enter a cache.
func decodeOpenAIIdentityBodyView(body []byte) (map[string]json.RawMessage, error) {
	view := parseRawJSONView(body)
	if !view.IsObject() || !json.Valid(body) {
		// Preserve the standard decoder's error behavior on malformed inputs.
		var root map[string]json.RawMessage
		err := json.Unmarshal(body, &root)
		return root, err
	}
	root := make(map[string]json.RawMessage)
	view.ForEach(func(key, value gjson.Result) bool {
		name := key.Str
		if strings.ContainsRune(key.Raw, '\\') {
			// GJSON and encoding/json differ on unmatched surrogate escapes.
			// Decode only escaped keys with the standard decoder so distinct
			// unknown fields cannot collapse to the same map entry.
			_ = json.Unmarshal([]byte(key.Raw), &name)
		}
		start, end := value.Index, value.Index+len(value.Raw)
		// Keep the last occurrence, matching encoding/json for duplicate keys.
		// A full slice expression also prevents appending into the next field.
		root[name] = body[start:end:end]
		return true
	})
	return root, nil
}

// marshalOpenAIIdentityBodyView writes already validated raw values straight
// into the final body. encoding/json would first copy the entire input/history
// into its encoder buffer, then copy that buffer again to the caller's writer.
// Values are either borrowed from valid input or produced by JSON encoders in
// identity projection; callers must not supply arbitrary unvalidated JSON here.
func marshalOpenAIIdentityBodyView(root map[string]json.RawMessage) ([]byte, error) {
	keys := make([]string, 0, len(root))
	for key := range root {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	encodedKeys := make([][]byte, len(keys))
	size := 2
	for i, key := range keys {
		encodedKey, err := marshalJSONWithoutHTMLEscape(key)
		if err != nil {
			return nil, err
		}
		encodedKeys[i] = encodedKey
		size += len(encodedKey) + 1 + len(root[key])
		if len(root[key]) == 0 {
			return nil, errors.New("empty raw OpenAI identity body value")
		}
		if i > 0 {
			size++
		}
	}
	out := make([]byte, 0, size)
	out = append(out, '{')
	for i, key := range keys {
		if i > 0 {
			out = append(out, ',')
		}
		out = append(out, encodedKeys[i]...)
		out = append(out, ':')
		out = append(out, root[key]...)
	}
	return append(out, '}'), nil
}
