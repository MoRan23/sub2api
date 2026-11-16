package service

import (
	"bytes"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/tidwall/gjson"
)

// This projection is borrowed only for synchronous observation of one immutable
// outbound body. Never store it in an observation or a request-independent cache.
// Large input, tools and extension fields are validated but neither decoded nor
// copied. Exact keys retain map decoding semantics; carriers separately retain
// the case-insensitive field matching of their historical struct decoder.
type fingerprintObservationBodyView struct {
	root          map[string]json.RawMessage
	clientCarrier json.RawMessage
	turnCarrier   json.RawMessage
	present       bool
	validObject   bool
	validCarrier  bool
}

func parseFingerprintObservationBodyView(body []byte) fingerprintObservationBodyView {
	view := fingerprintObservationBodyView{present: len(body) > 0}
	if len(body) == 0 || !json.Valid(body) {
		return view
	}
	validUTF8 := utf8.Valid(body)
	if bytes.Equal(bytes.TrimSpace(body), []byte("null")) {
		view.validCarrier = validUTF8
		return view
	}
	object := parseRawJSONView(body)
	if !object.IsObject() {
		return view
	}
	view.validObject, view.validCarrier = true, validUTF8
	view.root = make(map[string]json.RawMessage)
	object.ForEach(func(key, value gjson.Result) bool {
		name := key.Str
		if strings.ContainsRune(key.Raw, '\\') {
			// Preserve encoding/json's treatment of escaped member names.
			_ = json.Unmarshal([]byte(key.Raw), &name)
		}
		raw := json.RawMessage(body[value.Index : value.Index+len(value.Raw)])
		if strings.EqualFold(name, "client_metadata") {
			view.clientCarrier = raw
		}
		if strings.EqualFold(name, openAIWSTurnMetadataHeader) {
			view.turnCarrier = raw
		}
		switch name {
		case "client_metadata", "service_tier", "prompt_cache_key",
			"x-codex-installation-id", "installation_id", "session_id", "session-id",
			"thread_id", "thread-id", "x-codex-window-id", "window_id", "window_number",
			"context_window_id", "context-window-id", "x-codex-context-window-id", "x-codex-context_window_id",
			"x-openai-subagent", "x-codex-parent-thread-id", "parent_thread_id",
			"turn_id", "turn-id", "parent_turn_id", "root_turn_id", "turn_started_at_unix_ms",
			"guardian_classifier_source_thread_id":
			// Last duplicate wins, including when the later value is null.
			view.root[name] = raw
		}
		return true
	})
	return view
}
