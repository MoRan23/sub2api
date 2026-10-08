package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// openAIIdentityCaptureBody is a synchronous, read-only view shared by identity
// capture stages. Only metadata is decoded: large input/history values remain
// borrowed bytes. Never retain this view in a capture, plan, or context cache.
type openAIIdentityCaptureBody struct {
	root                  map[string]json.RawMessage
	clientMetadata        map[string]json.RawMessage
	clientMetadataPresent bool
}

func newOpenAIIdentityCaptureBody(body []byte) openAIIdentityCaptureBody {
	var view openAIIdentityCaptureBody
	if len(body) == 0 || !utf8.Valid(body) {
		return view
	}
	root, err := decodeOpenAIIdentityBodyView(body)
	if err != nil || root == nil {
		return view
	}
	view.root = root
	if raw, present := root["client_metadata"]; present {
		view.clientMetadataPresent = true
		_ = json.Unmarshal(raw, &view.clientMetadata)
	}
	return view
}

func (view openAIIdentityCaptureBody) clientInstallationID(c *gin.Context, body []byte) string {
	if view.root != nil {
		var direct, turnMetadata string
		_ = json.Unmarshal(view.clientMetadata[codexInstallationIDKey], &direct)
		_ = json.Unmarshal(view.clientMetadata[openAIWSTurnMetadataHeader], &turnMetadata)
		return extractClientInstallationIDFromMetadata(c, strings.TrimSpace(direct), turnMetadata)
	}
	// Installation capture historically accepts a first JSON object followed by
	// trailing data, and replaces malformed UTF-8. Strict identity consumers do
	// neither. Keep that compatibility path without decoding normal histories.
	var decoded map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if len(body) > 0 && decoder.Decode(&decoded) == nil {
		return extractClientInstallationID(c, decoded)
	}
	return extractClientInstallationID(c, nil)
}

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
