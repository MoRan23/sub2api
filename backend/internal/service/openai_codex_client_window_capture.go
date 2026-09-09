package service

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// A client window is a transition signal, never an outbound identity. Only a
// native Responses turn carrying both canonical metadata and Codex's complete
// developer window block can opt into the client-side rollover protocol.
func captureOpenAICodexClientWindow(c *gin.Context, body []byte, logical OpenAICodexLogicalTurnIdentity) OpenAICodexClientWindowSignal {
	var none OpenAICodexClientWindowSignal
	if c == nil || c.Request == nil || c.Request.URL == nil ||
		!strings.HasSuffix(strings.TrimRight(c.Request.URL.Path, "/"), "/responses") {
		return none
	}
	return captureOpenAICodexNativeClientWindow(body, logical)
}

// Established WS connections capture subsequent frames without an HTTP request
// context. Keep that transport authority explicit rather than allowing any
// context-free identity capture to initiate a client window transition.
func captureOpenAICodexWSClientWindow(body []byte, logical OpenAICodexLogicalTurnIdentity) OpenAICodexClientWindowSignal {
	kind := gjson.GetBytes(body, "type")
	if kind.Type != gjson.String || kind.String() != "response.create" {
		return OpenAICodexClientWindowSignal{}
	}
	return captureOpenAICodexNativeClientWindow(body, logical)
}

func captureOpenAICodexNativeClientWindow(body []byte, logical OpenAICodexLogicalTurnIdentity) OpenAICodexClientWindowSignal {
	var none OpenAICodexClientWindowSignal
	if !gjson.ValidBytes(body) || HasCompactionTriggerInInput(body) {
		return none
	}
	root := gjson.ParseBytes(body)
	if !root.IsObject() || root.Get("generate").Type == gjson.False {
		return none
	}
	if kind := root.Get("type"); kind.Exists() {
		if kind.Type != gjson.String || kind.String() != "response.create" {
			return none
		}
	} else if root.Get("stream").Type != gjson.True {
		return none
	}
	metadata := root.Get("client_metadata.x-codex-turn-metadata")
	if metadata.Type != gjson.String {
		return none
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(metadata.String()), &fields) != nil ||
		codexWireString(fields["request_kind"]) != string(CodexWireRequestTurn) {
		return none
	}
	thread := codexWireString(fields["thread_id"])
	session := codexWireString(fields["session_id"])
	number := codexWireUint64(fields["window_number"])
	current, err := canonicalUUIDv7(codexWireString(fields["context_window_id"]))
	if err != nil || number == nil || *number > OpenAICodexWindowMaxNumber || thread == "" ||
		thread != logical.ThreadKey || session == "" || session != logical.SessionKey ||
		codexWireString(fields["window_id"]) != thread+":"+strconv.FormatUint(*number, 10) {
		return none
	}
	var result OpenAICodexClientWindowSignal
	conflict := false
	inspect := func(value gjson.Result) {
		if value.Type != gjson.String {
			return
		}
		signal, agent, ok := parseOpenAICodexClientWindowBlock(value.String())
		if !ok || signal.Current != current {
			return
		}
		if name := codexWireString(fields["agent_name"]); name != "" && name != agent {
			conflict = true
			return
		}
		signal.Number, signal.Valid = *number, true
		if (signal.Number == 0 && (signal.First != signal.Current || signal.Previous != "")) ||
			(signal.Number > 0 && (signal.First == signal.Current || signal.Previous == "" || signal.Previous == signal.Current)) {
			conflict = true
			return
		}
		if result.Valid && result != signal {
			conflict = true
		}
		result = signal
	}
	for _, item := range root.Get("input").Array() {
		if !item.IsObject() || item.Get("role").String() != "developer" {
			continue
		}
		if kind := item.Get("type"); kind.Exists() && (kind.Type != gjson.String || kind.String() != "message") {
			continue
		}
		content := item.Get("content")
		if content.Type == gjson.String {
			inspect(content)
		} else if content.IsArray() {
			for _, part := range content.Array() {
				if part.IsObject() && (part.Get("type").String() == "input_text" || part.Get("type").String() == "text") {
					inspect(part.Get("text"))
				}
			}
		}
	}
	if conflict {
		return none
	}
	return result
}

func parseOpenAICodexClientWindowBlock(text string) (OpenAICodexClientWindowSignal, string, bool) {
	var signal OpenAICodexClientWindowSignal
	lines := strings.Split(strings.ReplaceAll(strings.TrimSpace(text), "\r\n", "\n"), "\n")
	if len(lines) < 5 || lines[0] != "<context_window>" || lines[len(lines)-1] != "</context_window>" || !strings.HasPrefix(lines[1], "Agent name: ") {
		return signal, "", false
	}
	agent := strings.TrimPrefix(lines[1], "Agent name: ")
	if strings.TrimSpace(agent) == "" {
		return signal, "", false
	}
	cursor := 2
	read := func(label string) (string, bool) {
		if cursor >= len(lines)-1 || !strings.HasPrefix(lines[cursor], label) {
			return "", false
		}
		id, err := canonicalUUIDv7(strings.TrimPrefix(lines[cursor], label))
		cursor++
		return id, err == nil
	}
	var ok bool
	if signal.First, ok = read("First context window id: "); !ok {
		return signal, agent, false
	}
	if signal.Current, ok = read("Current context window id: "); !ok {
		return signal, agent, false
	}
	if cursor < len(lines)-1 && strings.HasPrefix(lines[cursor], "Previous context window id: ") {
		if signal.Previous, ok = read("Previous context window id: "); !ok {
			return signal, agent, false
		}
	}
	for _, line := range lines[cursor : len(lines)-1] {
		if strings.Contains(line, "context window id:") || strings.Contains(line, "<context_window>") || strings.Contains(line, "</context_window>") {
			return signal, agent, false
		}
	}
	return signal, agent, true
}
