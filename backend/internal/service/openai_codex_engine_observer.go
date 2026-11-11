package service

import (
	"bytes"
	"strings"
	"unicode/utf8"

	"github.com/tidwall/gjson"
)

// Image base64 is ASCII and can be many MiB. Avoid allocating its lowercase
// copy merely to detect the small partial-image marker. Non-ASCII text keeps
// the original Unicode lowercasing behavior.
func codexEngineContainsPartialImage(raw string) bool {
	const marker = "partial_image"
	matched := 0
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if c >= utf8.RuneSelf {
			return strings.Contains(strings.ToLower(raw), marker)
		}
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		if c == marker[matched] {
			matched++
			if matched == len(marker) {
				return true
			}
		} else if c == marker[0] {
			matched = 1
		} else {
			matched = 0
		}
	}
	return false
}

func codexEngineStreamStartsSemanticTTFT(data []byte, event string) bool {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("[DONE]")) {
		return false
	}
	event = strings.TrimSpace(event)
	if event == "" && gjson.ValidBytes(trimmed) {
		event = strings.TrimSpace(gjson.GetBytes(trimmed, "type").String())
	}
	switch event {
	case "response.failed":
		return false
	case "error":
		return !openAIStreamFailedEventShouldFailover(trimmed, extractOpenAISSEErrorMessage(trimmed))
	default:
		return !openAIStreamEventIsMetadata(event)
	}
}

// Text deltas do not need image JSON materialization. Escaped keys take the
// general path as well, preserving JSON key matching for compatible providers.
func codexEngineMayHaveImages(data []byte) bool {
	if bytes.IndexByte(data, '\\') >= 0 {
		return true
	}
	for _, key := range []string{`"data"`, `"output"`, `"item"`, `"images"`, `"b64_json"`, `"url"`} {
		if bytes.Contains(data, []byte(key)) {
			return true
		}
	}
	return false
}

// These borrowed fields live only for one observation. Indexing the root once
// avoids rescanning a large image result for each absent top-level field.
type codexEngineImageView struct {
	data, output, responseOutput, eventType, item gjson.Result
}

func newCodexEngineImageView(root gjson.Result) (view codexEngineImageView) {
	root.ForEach(func(key, value gjson.Result) bool {
		var field *gjson.Result
		switch key.Str {
		case "data":
			field = &view.data
		case "output":
			field = &view.output
		case "type":
			field = &view.eventType
		case "item":
			field = &view.item
		case "response":
			// GJSON paths select the first matching leaf, including null. A
			// duplicate response whose output is missing does not hide a later
			// response.output; caching only the first response would change that.
			if !view.responseOutput.Exists() {
				view.responseOutput = value.Get("output")
			}
		}
		if field != nil && !field.Exists() {
			*field = value
		}
		return true
	})
	return view
}

func observeCodexEngineImages(result *OpenAIForwardResult, data []byte, stream *openAIImageOutputCounter) (parsed *gjson.Result) {
	if len(data) > 0 && codexEngineMayHaveImages(data) && gjson.ValidBytes(data) {
		// One temporary string, shared by both JSON-response and incremental
		// SSE accounting, replaces repeated copies of the image/output subtree.
		root := gjson.ParseBytes(data)
		parsed = &root
		view := newCodexEngineImageView(root)
		current := newOpenAIImageOutputCounter()
		current.ownStrings = true
		current.addDataArray(view.data)
		current.addOutputArray(view.output)
		current.addOutputArray(view.responseOutput)
		if sizes := current.Sizes(); len(sizes) > 0 {
			result.ImageOutputSizes = sizes
		}
		count := current.Count()
		if count == 0 {
			count = int(root.Get("usage.images").Int())
			if count <= 0 {
				count = int(root.Get("tool_usage.image_gen.images").Int())
			}
			if count <= 0 && strings.HasSuffix(strings.TrimSpace(view.eventType.String()), ".completed") && (root.Get("b64_json").Exists() || root.Get("url").Exists()) {
				count = 1
			}
		}
		result.ImageCount = max(result.ImageCount, count)
		if stream != nil {
			stream.ownStrings = true
			stream.addDataArray(view.data)
			switch strings.TrimSpace(view.eventType.String()) {
			case "response.output_item.done":
				stream.addImageOutputItem(view.item)
			case "response.completed", "response.done":
				stream.addOutputArray(view.responseOutput)
			case "image_generation.completed", "image_edit.completed":
				if item := view.item; item.Exists() {
					stream.addImageOutputItem(item)
				} else if output := view.output; output.Exists() {
					stream.addImageOutputItem(output)
				} else {
					stream.addImageOutputItem(root)
				}
			}
		}
	}
	if stream != nil {
		result.ImageCount = max(result.ImageCount, stream.Count())
		if sizes := stream.Sizes(); len(sizes) > 0 {
			result.ImageOutputSizes = sizes
		}
	}
	return parsed
}
