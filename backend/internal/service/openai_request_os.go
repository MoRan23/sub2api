package service

import (
	"encoding/xml"
	"io"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const (
	openAIRequestOSBodyLimit        = 8 << 20
	openAIRequestOSMessageLimit     = 4096
	openAIRequestOSPartLimit        = 16 << 10
	openAIRequestOSTextLimit        = 64 << 10
	openAIRequestOSTotalTextLimit   = 1 << 20
	openAIRequestOSEnvironmentLimit = 32
	openAIRequestOSXMLNodeLimit     = 128
	openAIRequestOSXMLDepthLimit    = 8
)

// captureOpenAIRequestOS observes ingress identity without changing the body.
// A recognizable UA wins. Otherwise, only the last standalone environment in
// the final consecutive user messages may supply operating-system evidence.
func captureOpenAIRequestOS(c *gin.Context, body []byte) (userAgent, osFamily, source string) {
	if c != nil && c.Request != nil {
		userAgent = c.Request.UserAgent()
	}
	if osFamily = openai.DetectOSFamilyFromUserAgent(userAgent); osFamily != "" {
		return userAgent, osFamily, "user_agent"
	}
	if osFamily = openAIRequestOSFromBody(body); osFamily != "" {
		return userAgent, osFamily, "environment_context"
	}
	return userAgent, "", ""
}

func openAIRequestOSFromBody(body []byte) string {
	if len(body) == 0 || len(body) > openAIRequestOSBodyLimit || !gjson.ValidBytes(body) {
		return ""
	}
	root := gjson.ParseBytes(body)
	if !root.IsObject() {
		return ""
	}
	messages := root.Get("input")
	chatMessages := root.Get("messages")
	if messages.Exists() && chatMessages.Exists() {
		return ""
	}
	if !messages.Exists() {
		messages = chatMessages
	}
	if !messages.IsArray() {
		return ""
	}

	var current []gjson.Result
	messageCount := 0
	limited := false
	messages.ForEach(func(_, message gjson.Result) bool {
		messageCount++
		if messageCount > openAIRequestOSMessageLimit {
			limited = true
			return false
		}
		kind := message.Get("type").String()
		if message.Get("role").String() != "user" || (kind != "" && kind != "message") {
			current = current[:0]
		} else {
			current = append(current, message)
		}
		return true
	})
	if limited {
		return ""
	}

	selected := ""
	textBytes, parts, environments := 0, 0, 0
	observe := func(text string) bool {
		if len(text) > openAIRequestOSTextLimit || len(text) > openAIRequestOSTotalTextLimit-textBytes {
			limited = true
			return false
		}
		textBytes += len(text)
		trimmed := strings.TrimSpace(text)
		if !strings.HasPrefix(trimmed, "<environment_context>") {
			return true
		}
		environments++
		if environments > openAIRequestOSEnvironmentLimit {
			limited = true
			return false
		}
		// An unusable newer declaration must not revive an older environment.
		selected = openAIRequestOSFromEnvironment(trimmed)
		return true
	}
	for _, message := range current {
		content := message.Get("content")
		if content.Type == gjson.String {
			if openAIRequestOSContentAllowed(message, gjson.Result{}, 0) && !observe(content.String()) {
				return ""
			}
		} else if content.IsArray() {
			content.ForEach(func(index, part gjson.Result) bool {
				parts++
				if parts > openAIRequestOSPartLimit {
					limited = true
					return false
				}
				kind := part.Get("type").String()
				if (kind == "text" || kind == "input_text") && openAIRequestOSContentAllowed(message, part, int(index.Int())) {
					if text := part.Get("text"); text.Type == gjson.String {
						return observe(text.String())
					}
				}
				return true
			})
			if limited {
				return ""
			}
		}
	}
	return selected
}

// Explicit content-kind metadata is authoritative. Unlabeled standalone blocks
// remain usable for clients that omit the internal Codex metadata.
func openAIRequestOSContentAllowed(message, part gjson.Result, index int) bool {
	for _, object := range []gjson.Result{message, part} {
		for _, container := range []gjson.Result{object, object.Get("internal_chat_message_metadata_passthrough"), object.Get("metadata")} {
			if container.Exists() && !container.IsObject() {
				return false
			}
			if kinds := container.Get("content_item_kinds"); kinds.Exists() {
				if !kinds.IsArray() || len(kinds.Raw) > 8<<10 {
					return false
				}
				value := kinds.Get(strconv.Itoa(index))
				if value.Type != gjson.String || value.String() != "environments.environment_context" {
					return false
				}
			}
		}
	}
	return true
}

func openAIRequestOSFromEnvironment(text string) string {
	if len(text) > openAIRequestOSTextLimit || !strings.HasSuffix(text, "</environment_context>") ||
		strings.Contains(text, "```") || strings.Contains(text, "~~~") || strings.Contains(text, "<![CDATA[") {
		return ""
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), ">") {
			return ""
		}
	}
	type field struct {
		name, text string
		children   bool
	}
	var stack []field
	explicit, pathFamily := "", ""
	hasExplicit, explicitInvalid, pathConflict := false, false, false
	nodes, roots := 0, 0
	decoder := xml.NewDecoder(strings.NewReader(text))
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		nodes++
		if err != nil || nodes > openAIRequestOSXMLNodeLimit {
			return ""
		}
		switch token := token.(type) {
		case xml.StartElement:
			if token.Name.Space != "" || len(stack) >= openAIRequestOSXMLDepthLimit {
				return ""
			}
			if len(stack) == 0 {
				roots++
				if roots != 1 || token.Name.Local != "environment_context" {
					return ""
				}
			} else {
				stack[len(stack)-1].children = true
			}
			stack = append(stack, field{name: token.Name.Local})
		case xml.CharData:
			if len(stack) == 0 {
				if strings.TrimSpace(string(token)) != "" {
					return ""
				}
			} else {
				stack[len(stack)-1].text += string(token)
			}
		case xml.EndElement:
			if len(stack) == 0 {
				return ""
			}
			value := stack[len(stack)-1]
			if len(stack) == 2 && (value.name == "os" || value.name == "platform" || value.name == "operating_system") {
				hasExplicit = true
				family := openai.DetectOSFamilyFromUserAgent(value.text)
				if value.children || family == "" || (explicit != "" && explicit != family) {
					explicitInvalid = true
				}
				explicit = family
			}
			if !value.children {
				var names []string
				for _, item := range stack {
					names = append(names, item.name)
				}
				if openAIRequestOSWorkspacePath(strings.Join(names, "/")) {
					if family := openAIRequestOSFromPath(value.text); family != "" {
						if pathFamily != "" && pathFamily != family {
							pathConflict = true
						}
						pathFamily = family
					}
				}
			}
			stack = stack[:len(stack)-1]
		case xml.Comment, xml.Directive, xml.ProcInst:
			return ""
		}
	}
	if len(stack) != 0 || roots != 1 {
		return ""
	}
	if hasExplicit {
		if explicitInvalid {
			return ""
		}
		return explicit
	}
	if pathConflict {
		return ""
	}
	return pathFamily
}

func openAIRequestOSWorkspacePath(path string) bool {
	switch path {
	case "environment_context/cwd", "environment_context/working_directory", "environment_context/workspace", "environment_context/workspace_root",
		"environment_context/workspace_roots/root", "environment_context/filesystem/workspace_roots/root":
		return true
	default:
		return false
	}
}

func openAIRequestOSFromPath(path string) string {
	path = strings.TrimSpace(path)
	if len(path) >= 3 && ((path[0] >= 'A' && path[0] <= 'Z') || (path[0] >= 'a' && path[0] <= 'z')) &&
		path[1] == ':' && (path[2] == '\\' || path[2] == '/') {
		return "windows"
	}
	if strings.HasPrefix(path, `\\`) {
		segments := strings.Split(strings.TrimPrefix(path, `\\`), `\`)
		if len(segments) >= 2 && segments[0] != "" && segments[1] != "" {
			return "windows"
		}
	}
	if strings.HasPrefix(path, "/Users/") {
		return "macos"
	}
	if strings.HasPrefix(path, "/home/") {
		return "linux"
	}
	return ""
}
