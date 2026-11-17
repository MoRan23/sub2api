package service

import (
	"crypto/rand"
	"math/big"
	"strings"

	"github.com/tidwall/gjson"
)

func ExtractContentModerationText(protocol string, body []byte) string {
	return ExtractContentModerationInput(protocol, body).Text
}

func ExtractContentModerationInput(protocol string, body []byte) ContentModerationInput {
	return extractContentModerationInput(protocol, body, true)
}

// Keyword checks share semantic moderation's current-user boundaries, but must
// inspect client-supplied reminder blocks as ordinary user text.
func extractContentModerationKeywordText(protocol string, body []byte) string {
	return extractContentModerationInput(protocol, body, false).Text
}

type moderationTextCollector struct {
	filterReminders bool
	keywordParts    *[]string
}

func extractContentModerationInput(protocol string, body []byte, filterReminders bool) ContentModerationInput {
	collector := moderationTextCollector{filterReminders: filterReminders}
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return ContentModerationInput{}
	}
	return collector.extract(protocol, parseRawJSONView(body))
}

// Both policies use the same latest-user boundary. Collect their different text
// views in one pass: keywords include reminder text, semantic moderation does
// not. Only the owned result may leave this synchronous borrowed-body reader.
func extractContentModerationInputs(protocol string, body []byte) (ContentModerationInput, string) {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return ContentModerationInput{}, ""
	}
	var keywords []string
	collector := moderationTextCollector{filterReminders: true, keywordParts: &keywords}
	content := collector.extract(protocol, parseRawJSONView(body))
	return content, strings.Clone(normalizeContentModerationText(strings.Join(keywords, "\n")))
}

func (collector moderationTextCollector) extract(protocol string, root gjson.Result) ContentModerationInput {
	var parts []string
	var images []string
	switch protocol {
	case ContentModerationProtocolAnthropicMessages:
		collector.collectLastAnthropicUserMessage(root.Get("messages"), &parts, &images)
	case ContentModerationProtocolOpenAIChat:
		collector.collectLastRoleMessage(root.Get("messages"), "user", &parts, &images)
	case ContentModerationProtocolOpenAIResponses:
		collector.collectLastResponsesInput(root.Get("input"), &parts, &images)
	case ContentModerationProtocolGemini:
		collector.collectLastGeminiContent(root.Get("contents"), &parts, &images)
	case ContentModerationProtocolOpenAIImages:
		collector.addModerationText(&parts, root.Get("prompt").String())
		collector.collectContentValue(root.Get("images"), &parts, &images)
	case ContentModerationProtocolTypeSafeSystemOne:
		// System One carries no client-harness reminder blocks, so a literal
		// <system-reminder> is ordinary user text and must never be skipped.
		moderationTextCollector{keywordParts: collector.keywordParts}.collectSystemOneRoot(root, &parts)
	default:
		collector.collectLastResponsesInput(root.Get("input"), &parts, &images)
		collector.collectLastRoleMessage(root.Get("messages"), "user", &parts, &images)
		collector.collectLastGeminiContent(root.Get("contents"), &parts, &images)
	}
	out := ContentModerationInput{
		Text:   strings.Join(parts, "\n"),
		Images: normalizeModerationImages(images),
	}
	if collector.filterReminders {
		out.Normalize()
	} else {
		out.Text = normalizeContentModerationText(out.Text)
	}
	// gjson strings may alias the request. Even a single short text/image must
	// not retain the complete history after an asynchronous task is enqueued.
	out.Text = strings.Clone(out.Text)
	return out
}

// collectSystemOneInput moderates every client-controlled text of a System One
// request: question IDs, every question field except the validated type,
// unknown top-level extension fields, and the evaluated state. Object keys are
// sent to Jev as part of the JSON, so they are moderated like values.
func (collector moderationTextCollector) collectSystemOneInput(body []byte, parts *[]string) {
	collector.collectSystemOneRoot(parseRawJSONView(body), parts)
}

func (collector moderationTextCollector) collectSystemOneRoot(root gjson.Result, parts *[]string) {
	questions := root.Get("questions")
	if !questions.IsObject() {
		collector.collectSystemOneText(questions, parts)
	}
	questions.ForEach(func(id, question gjson.Result) bool {
		collector.addModerationText(parts, id.String())
		if !question.IsObject() {
			collector.collectSystemOneText(question, parts)
			return true
		}
		question.ForEach(func(field, value gjson.Result) bool {
			switch field.String() {
			case "type":
				return true
			case "instructions", "criteria":
			default:
				collector.addModerationText(parts, field.String())
			}
			collector.collectSystemOneText(value, parts)
			return true
		})
		return true
	})
	root.ForEach(func(field, value gjson.Result) bool {
		switch field.String() {
		case "model", "stream", "state", "questions":
			return true
		}
		collector.addModerationText(parts, field.String())
		collector.collectSystemOneText(value, parts)
		return true
	})
	collector.collectSystemOneText(root.Get("state"), parts)
}

func (collector moderationTextCollector) collectSystemOneText(value gjson.Result, parts *[]string) {
	switch {
	case !value.Exists():
		return
	case value.Type == gjson.String:
		collector.addModerationText(parts, value.String())
	case value.IsArray():
		value.ForEach(func(_, child gjson.Result) bool {
			collector.collectSystemOneText(child, parts)
			return true
		})
	case value.IsObject():
		value.ForEach(func(key, child gjson.Result) bool {
			collector.addModerationText(parts, key.String())
			collector.collectSystemOneText(child, parts)
			return true
		})
	}
}

func (collector moderationTextCollector) collectLastRoleMessage(messages gjson.Result, role string, parts *[]string, images *[]string) {
	if !messages.IsArray() {
		return
	}
	last := lastModerationArrayItem(messages)
	if !last.Exists() {
		return
	}
	if strings.ToLower(strings.TrimSpace(last.Get("role").String())) != role {
		return
	}
	var candidate []string
	var candidateImages []string
	collector.collectContentValue(last.Get("content"), &candidate, &candidateImages)
	if !hasModerationText(candidate) && len(candidateImages) == 0 {
		return
	}
	*parts = append(*parts, candidate...)
	*images = append(*images, candidateImages...)
}

func (collector moderationTextCollector) collectLastAnthropicUserMessage(messages gjson.Result, parts *[]string, images *[]string) {
	if !messages.IsArray() {
		return
	}
	// Anthropic requests may append a system message after the user turn. It
	// does not represent a new model/tool turn, so retain the latest user
	// content while continuing to reject assistant/tool-ended loops.
	var last gjson.Result
	messages.ForEach(func(_, item gjson.Result) bool {
		if strings.ToLower(strings.TrimSpace(item.Get("role").String())) != "system" {
			last = item
		}
		return true
	})
	if strings.ToLower(strings.TrimSpace(last.Get("role").String())) != "user" {
		return
	}
	var candidate []string
	var candidateImages []string
	collector.collectAnthropicUserContentValue(last.Get("content"), &candidate, &candidateImages)
	if !hasModerationText(candidate) && len(candidateImages) == 0 {
		return
	}
	*parts = append(*parts, candidate...)
	*images = append(*images, candidateImages...)
}

func (collector moderationTextCollector) collectAnthropicUserContentValue(value gjson.Result, parts *[]string, images *[]string) {
	switch {
	case !value.Exists():
		return
	case value.Type == gjson.String:
		if collector.keywordParts != nil || !collector.filterReminders || !isAnthropicSystemReminderText(value.String()) {
			collector.addModerationText(parts, value.String())
		}
	case value.IsArray():
		value.ForEach(func(_, item gjson.Result) bool {
			collector.collectAnthropicUserContentValue(item, parts, images)
			return true
		})
	case value.IsObject():
		fields := moderationObjectFields(value)
		typ := strings.ToLower(strings.TrimSpace(fields["type"].String()))
		switch typ {
		case "", "text", "input_text", "message":
			if text := fields["text"]; text.Exists() && (collector.keywordParts != nil || !collector.filterReminders || !isAnthropicSystemReminderText(text.String())) {
				collector.addModerationText(parts, text.String())
			}
			if content := fields["content"]; content.Exists() {
				collector.collectAnthropicUserContentValue(content, parts, images)
			}
		case "image_url", "input_image", "image":
			collector.collectContentObject(fields, parts, images)
		}
	}
}

func isAnthropicSystemReminderText(text string) bool {
	return strings.HasPrefix(strings.TrimSpace(text), "<system-reminder>")
}

func (collector moderationTextCollector) collectLastResponsesInput(input gjson.Result, parts *[]string, images *[]string) {
	switch {
	case !input.Exists():
		return
	case input.Type == gjson.String:
		collector.addModerationText(parts, input.String())
	case input.IsArray():
		collector.collectResponsesUserItem(lastModerationArrayItem(input), parts, images)
	case input.IsObject():
		collector.collectResponsesUserItem(input, parts, images)
	}
}

func lastModerationArrayItem(array gjson.Result) (last gjson.Result) {
	array.ForEach(func(_, value gjson.Result) bool {
		last = value
		return true
	})
	return last
}

func (collector moderationTextCollector) collectResponsesUserItem(item gjson.Result, parts *[]string, images *[]string) {
	fields := moderationObjectFields(item)
	role := strings.ToLower(strings.TrimSpace(fields["role"].String()))
	if role != "" && role != "user" {
		return
	}
	// The old predicate collected all content to test emptiness, then collected
	// it a second time. Keep the identical sequence once, including the legacy
	// content duplication for an item that also carries a top-level text field.
	var candidate, candidateImages []string
	collector.collectContentValue(fields["content"], &candidate, &candidateImages)
	if fields["type"].String() == "input_text" || fields["text"].Exists() {
		collector.collectContentObject(fields, &candidate, &candidateImages)
	}
	if !hasModerationText(candidate) && len(candidateImages) == 0 {
		return
	}
	*parts = append(*parts, candidate...)
	*images = append(*images, candidateImages...)
}

func (collector moderationTextCollector) isResponsesUserTextItem(item gjson.Result) bool {
	role := strings.ToLower(strings.TrimSpace(item.Get("role").String()))
	if role == "user" {
		return collector.responseItemHasModerationText(item)
	}
	if role != "" {
		return false
	}
	return collector.responseItemHasModerationText(item)
}

func (collector moderationTextCollector) responseItemHasModerationText(item gjson.Result) bool {
	var parts []string
	var images []string
	collector.collectContentValue(item.Get("content"), &parts, &images)
	if item.Get("type").String() == "input_text" || item.Get("text").Exists() {
		collector.collectContentValue(item, &parts, &images)
	}
	return normalizeContentModerationText(strings.Join(parts, "\n")) != "" || len(images) > 0
}

func (collector moderationTextCollector) collectLastGeminiContent(contents gjson.Result, parts *[]string, images *[]string) {
	if !contents.IsArray() {
		return
	}
	last := lastModerationArrayItem(contents)
	if !last.Exists() {
		return
	}
	role := strings.ToLower(strings.TrimSpace(last.Get("role").String()))
	if role != "" && role != "user" {
		return
	}
	var candidate []string
	var candidateImages []string
	if arr := last.Get("parts"); arr.IsArray() {
		arr.ForEach(func(_, part gjson.Result) bool {
			collector.addModerationText(&candidate, part.Get("text").String())
			addGeminiModerationImage(&candidateImages, part)
			return true
		})
	}
	if !hasModerationText(candidate) && len(candidateImages) == 0 {
		return
	}
	*parts = append(*parts, candidate...)
	*images = append(*images, candidateImages...)
}

func (collector moderationTextCollector) collectContentValue(value gjson.Result, parts *[]string, images *[]string) {
	switch {
	case !value.Exists():
		return
	case value.Type == gjson.String:
		collector.addModerationText(parts, value.String())
	case value.IsArray():
		value.ForEach(func(_, item gjson.Result) bool {
			collector.collectContentValue(item, parts, images)
			return true
		})
	case value.IsObject():
		collector.collectContentObject(moderationObjectFields(value), parts, images)
	}
}

// Get() rescans the object for each absent property, repeatedly walking a large
// inline image. Project the relevant fields in one pass. First duplicate wins,
// matching the previous GJSON lookups (including explicit null).
func moderationObjectFields(object gjson.Result) map[string]gjson.Result {
	if !object.IsObject() {
		return nil
	}
	fields := make(map[string]gjson.Result)
	object.ForEach(func(key, value gjson.Result) bool {
		switch key.Str {
		case "type", "role", "text", "content", "image_url", "url", "source",
			"media_type", "mediaType", "mime_type", "mimeType", "data", "base64",
			"inline_data", "inlineData", "file_data", "fileData", "file_uri", "fileUri":
			if _, present := fields[key.Str]; !present {
				fields[key.Str] = value
			} else {
				switch key.Str {
				case "source", "image_url", "file_data", "fileData":
					// A GJSON nested path can continue at a later duplicate parent
					// when the first parent lacks its child. Keep that rare fallback.
					fields["$duplicate:"+key.Str] = object
				}
			}
		}
		return true
	})
	return fields
}

func (collector moderationTextCollector) collectContentObject(fields map[string]gjson.Result, parts *[]string, images *[]string) {
	typ := strings.ToLower(strings.TrimSpace(fields["type"].String()))
	imageURL := fields["image_url"]
	imageURLChild := imageURL.Get("url").String()
	if duplicate := fields["$duplicate:image_url"]; duplicate.Exists() {
		imageURLChild = duplicate.Get("image_url.url").String()
	}
	addModerationImage(images, imageURLChild)
	addModerationImage(images, imageURL.String())
	addModerationImage(images, fields["url"].String())
	source := moderationObjectFields(fields["source"])
	sourceData, data := source["data"].String(), fields["data"].String()
	sourceMediaType, sourceMediaTypeCamel := source["media_type"].String(), source["mediaType"].String()
	if duplicate := fields["$duplicate:source"]; duplicate.Exists() {
		sourceData = duplicate.Get("source.data").String()
		sourceMediaType = duplicate.Get("source.media_type").String()
		sourceMediaTypeCamel = duplicate.Get("source.mediaType").String()
	}
	addModerationImageData(images, sourceMediaType, sourceData)
	addModerationImageData(images, sourceMediaTypeCamel, sourceData)
	addModerationImageData(images, fields["media_type"].String(), data)
	addModerationImageData(images, fields["mime_type"].String(), data)
	addModerationImageData(images, fields["mimeType"].String(), data)
	addModerationImage(images, sourceData)
	addModerationImage(images, data)
	addModerationImage(images, fields["base64"].String())
	switch typ {
	case "", "text", "input_text", "message":
		if text := fields["text"]; text.Exists() {
			collector.addModerationText(parts, text.String())
		}
		if content := fields["content"]; content.Exists() {
			collector.collectContentValue(content, parts, images)
		}
	}
}

func addGeminiModerationImage(images *[]string, part gjson.Result) {
	fields := moderationObjectFields(part)
	if inlineData := fields["inline_data"]; inlineData.IsObject() {
		dataFields := moderationObjectFields(inlineData)
		mimeType := strings.TrimSpace(dataFields["mime_type"].String())
		data := strings.TrimSpace(dataFields["data"].String())
		if mimeType != "" && data != "" {
			addModerationImageData(images, mimeType, data)
		}
	}
	if inlineData := fields["inlineData"]; inlineData.IsObject() {
		dataFields := moderationObjectFields(inlineData)
		mimeType := strings.TrimSpace(dataFields["mimeType"].String())
		data := strings.TrimSpace(dataFields["data"].String())
		if mimeType != "" && data != "" {
			addModerationImageData(images, mimeType, data)
		}
	}
	fileURI, fileURICamel := fields["file_data"].Get("file_uri").String(), fields["fileData"].Get("fileUri").String()
	if duplicate := fields["$duplicate:file_data"]; duplicate.Exists() {
		fileURI = duplicate.Get("file_data.file_uri").String()
	}
	if duplicate := fields["$duplicate:fileData"]; duplicate.Exists() {
		fileURICamel = duplicate.Get("fileData.fileUri").String()
	}
	addModerationImage(images, fileURI)
	addModerationImage(images, fileURICamel)
}

func addModerationImageData(images *[]string, mimeType string, data string) {
	mimeType = strings.TrimSpace(mimeType)
	data = strings.TrimSpace(data)
	if mimeType == "" || data == "" {
		return
	}
	// Concatenation creates exactly one owned result; do not clone it again.
	*images = append(*images, "data:"+mimeType+";base64,"+data)
}

func addModerationImage(images *[]string, image string) {
	image = strings.TrimSpace(image)
	if image == "" {
		return
	}
	if strings.HasPrefix(image, "data:") || strings.HasPrefix(image, "http://") || strings.HasPrefix(image, "https://") {
		*images = append(*images, strings.Clone(image))
	}
}

func normalizeModerationImages(images []string) []string {
	out := make([]string, 0, len(images))
	seen := make(map[string]struct{}, len(images))
	for _, image := range images {
		image = strings.TrimSpace(image)
		if image == "" {
			continue
		}
		if _, ok := seen[image]; ok {
			continue
		}
		seen[image] = struct{}{}
		out = append(out, image)
	}
	return out
}

func limitContentModerationImages(images []string) []string {
	if len(images) <= maxContentModerationInputImages {
		return images
	}
	idx, err := rand.Int(rand.Reader, big.NewInt(int64(len(images))))
	if err != nil {
		return images[:maxContentModerationInputImages]
	}
	return []string{images[int(idx.Int64())]}
}

func (collector moderationTextCollector) addModerationText(parts *[]string, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if collector.keywordParts != nil {
		*collector.keywordParts = append(*collector.keywordParts, text)
	}
	if collector.filterReminders && strings.Contains(text, "<system-reminder>") {
		return
	}
	*parts = append(*parts, text)
}

func normalizeContentModerationText(text string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
}

func hasModerationText(parts []string) bool {
	for _, part := range parts {
		if strings.TrimSpace(part) != "" {
			return true
		}
	}
	return false
}
