// Frozen extraction implementation from before the CPU hot-path fix.
// Used only as a differential oracle and same-process benchmark baseline.
package service

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"strings"

	"github.com/tidwall/gjson"
)

func legacyCPUExtractContentModerationText(protocol string, body []byte) string {
	return legacyCPUExtractContentModerationInput(protocol, body).Text
}

func legacyCPUExtractContentModerationInput(protocol string, body []byte) ContentModerationInput {
	return legacyCPUExtractContentModerationInputWithReminders(protocol, body, true)
}

// Keyword checks share semantic moderation's current-user boundaries, but must
// inspect client-supplied reminder blocks as ordinary user text.
func legacyCPUExtractContentModerationKeywordText(protocol string, body []byte) string {
	return legacyCPUExtractContentModerationInputWithReminders(protocol, body, false).Text
}

type legacyCPUModerationTextCollector struct {
	filterReminders bool
}

func legacyCPUExtractContentModerationInputWithReminders(protocol string, body []byte, filterReminders bool) ContentModerationInput {
	collector := legacyCPUModerationTextCollector{filterReminders: filterReminders}
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return ContentModerationInput{}
	}
	var parts []string
	var images []string
	switch protocol {
	case ContentModerationProtocolAnthropicMessages:
		collector.legacyCPUCollectLastAnthropicUserMessage(gjson.GetBytes(body, "messages"), &parts, &images)
	case ContentModerationProtocolOpenAIChat:
		collector.legacyCPUCollectLastRoleMessage(gjson.GetBytes(body, "messages"), "user", &parts, &images)
	case ContentModerationProtocolOpenAIResponses:
		collector.legacyCPUCollectLastResponsesInput(gjson.GetBytes(body, "input"), &parts, &images)
	case ContentModerationProtocolGemini:
		collector.legacyCPUCollectLastGeminiContent(gjson.GetBytes(body, "contents"), &parts, &images)
	case ContentModerationProtocolOpenAIImages:
		collector.legacyCPUAddModerationText(&parts, gjson.GetBytes(body, "prompt").String())
		collector.legacyCPUCollectContentValue(gjson.GetBytes(body, "images"), &parts, &images)
	case ContentModerationProtocolTypeSafeSystemOne:
		// System One carries no client-harness reminder blocks, so a literal
		// <system-reminder> is ordinary user text and must never be skipped.
		legacyCPUModerationTextCollector{}.legacyCPUCollectSystemOneInput(body, &parts)
	default:
		collector.legacyCPUCollectLastResponsesInput(gjson.GetBytes(body, "input"), &parts, &images)
		collector.legacyCPUCollectLastRoleMessage(gjson.GetBytes(body, "messages"), "user", &parts, &images)
		collector.legacyCPUCollectLastGeminiContent(gjson.GetBytes(body, "contents"), &parts, &images)
	}
	out := ContentModerationInput{
		Text:   legacyCPUNormalizeContentModerationText(strings.Join(parts, "\n")),
		Images: legacyCPUNormalizeModerationImages(images),
	}
	if filterReminders {
		out.Text = legacyCPUModerationTrimRunes(legacyCPUNormalizeContentModerationText(out.Text), maxModerationInputRunes)
		out.Images = legacyCPUNormalizeModerationImages(out.Images)
	}
	return out
}

func legacyCPUModerationTrimRunes(text string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	return string(runes[:max])
}

// legacyCPUCollectSystemOneInput moderates every client-controlled text of a System One
// request: question IDs, every question field except the validated type,
// unknown top-level extension fields, and the evaluated state. Object keys are
// sent to Jev as part of the JSON, so they are moderated like values.
func (collector legacyCPUModerationTextCollector) legacyCPUCollectSystemOneInput(body []byte, parts *[]string) {
	root := gjson.ParseBytes(body)
	questions := root.Get("questions")
	if !questions.IsObject() {
		collector.legacyCPUCollectSystemOneText(questions, parts)
	}
	questions.ForEach(func(id, question gjson.Result) bool {
		collector.legacyCPUAddModerationText(parts, id.String())
		if !question.IsObject() {
			collector.legacyCPUCollectSystemOneText(question, parts)
			return true
		}
		question.ForEach(func(field, value gjson.Result) bool {
			switch field.String() {
			case "type":
				return true
			case "instructions", "criteria":
			default:
				collector.legacyCPUAddModerationText(parts, field.String())
			}
			collector.legacyCPUCollectSystemOneText(value, parts)
			return true
		})
		return true
	})
	root.ForEach(func(field, value gjson.Result) bool {
		switch field.String() {
		case "model", "stream", "state", "questions":
			return true
		}
		collector.legacyCPUAddModerationText(parts, field.String())
		collector.legacyCPUCollectSystemOneText(value, parts)
		return true
	})
	collector.legacyCPUCollectSystemOneText(root.Get("state"), parts)
}

func (collector legacyCPUModerationTextCollector) legacyCPUCollectSystemOneText(value gjson.Result, parts *[]string) {
	switch {
	case !value.Exists():
		return
	case value.Type == gjson.String:
		collector.legacyCPUAddModerationText(parts, value.String())
	case value.IsArray():
		value.ForEach(func(_, child gjson.Result) bool {
			collector.legacyCPUCollectSystemOneText(child, parts)
			return true
		})
	case value.IsObject():
		value.ForEach(func(key, child gjson.Result) bool {
			collector.legacyCPUAddModerationText(parts, key.String())
			collector.legacyCPUCollectSystemOneText(child, parts)
			return true
		})
	}
}

func (collector legacyCPUModerationTextCollector) legacyCPUCollectLastRoleMessage(messages gjson.Result, role string, parts *[]string, images *[]string) {
	if !messages.IsArray() {
		return
	}
	array := messages.Array()
	if len(array) == 0 {
		return
	}
	last := array[len(array)-1]
	if strings.ToLower(strings.TrimSpace(last.Get("role").String())) != role {
		return
	}
	var candidate []string
	var candidateImages []string
	collector.legacyCPUCollectContentValue(last.Get("content"), &candidate, &candidateImages)
	if legacyCPUNormalizeContentModerationText(strings.Join(candidate, "\n")) == "" && len(candidateImages) == 0 {
		return
	}
	*parts = append(*parts, candidate...)
	*images = append(*images, candidateImages...)
}

func (collector legacyCPUModerationTextCollector) legacyCPUCollectLastAnthropicUserMessage(messages gjson.Result, parts *[]string, images *[]string) {
	if !messages.IsArray() {
		return
	}
	array := messages.Array()
	if len(array) == 0 {
		return
	}
	// Anthropic requests may append a system message after the user turn. It
	// does not represent a new model/tool turn, so retain the latest user
	// content while continuing to reject assistant/tool-ended loops.
	lastUser := len(array) - 1
	for lastUser >= 0 && strings.ToLower(strings.TrimSpace(array[lastUser].Get("role").String())) == "system" {
		lastUser--
	}
	if lastUser < 0 || strings.ToLower(strings.TrimSpace(array[lastUser].Get("role").String())) != "user" {
		return
	}
	var candidate []string
	var candidateImages []string
	collector.legacyCPUCollectAnthropicUserContentValue(array[lastUser].Get("content"), &candidate, &candidateImages)
	if legacyCPUNormalizeContentModerationText(strings.Join(candidate, "\n")) == "" && len(candidateImages) == 0 {
		return
	}
	*parts = append(*parts, candidate...)
	*images = append(*images, candidateImages...)
}

func (collector legacyCPUModerationTextCollector) legacyCPUCollectAnthropicUserContentValue(value gjson.Result, parts *[]string, images *[]string) {
	switch {
	case !value.Exists():
		return
	case value.Type == gjson.String:
		if !collector.filterReminders || !legacyCPUIsAnthropicSystemReminderText(value.String()) {
			collector.legacyCPUAddModerationText(parts, value.String())
		}
	case value.IsArray():
		value.ForEach(func(_, item gjson.Result) bool {
			collector.legacyCPUCollectAnthropicUserContentValue(item, parts, images)
			return true
		})
	case value.IsObject():
		typ := strings.ToLower(strings.TrimSpace(value.Get("type").String()))
		switch typ {
		case "", "text", "input_text", "message":
			if value.Get("text").Exists() && (!collector.filterReminders || !legacyCPUIsAnthropicSystemReminderText(value.Get("text").String())) {
				collector.legacyCPUAddModerationText(parts, value.Get("text").String())
			}
			if value.Get("content").Exists() {
				collector.legacyCPUCollectAnthropicUserContentValue(value.Get("content"), parts, images)
			}
		case "image_url", "input_image", "image":
			collector.legacyCPUCollectContentValue(value, parts, images)
		}
	}
}

func legacyCPUIsAnthropicSystemReminderText(text string) bool {
	return strings.HasPrefix(strings.TrimSpace(text), "<system-reminder>")
}

func (collector legacyCPUModerationTextCollector) legacyCPUCollectLastResponsesInput(input gjson.Result, parts *[]string, images *[]string) {
	switch {
	case !input.Exists():
		return
	case input.Type == gjson.String:
		collector.legacyCPUAddModerationText(parts, input.String())
	case input.IsArray():
		array := input.Array()
		if len(array) == 0 {
			return
		}
		last := array[len(array)-1]
		if !collector.legacyCPUIsResponsesUserTextItem(last) {
			return
		}
		collector.legacyCPUCollectContentValue(last.Get("content"), parts, images)
		if last.Get("type").String() == "input_text" || last.Get("text").Exists() {
			collector.legacyCPUCollectContentValue(last, parts, images)
		}
	case input.IsObject():
		if collector.legacyCPUIsResponsesUserTextItem(input) {
			collector.legacyCPUCollectContentValue(input.Get("content"), parts, images)
			if input.Get("type").String() == "input_text" || input.Get("text").Exists() {
				collector.legacyCPUCollectContentValue(input, parts, images)
			}
		}
	}
}

func (collector legacyCPUModerationTextCollector) legacyCPUIsResponsesUserTextItem(item gjson.Result) bool {
	role := strings.ToLower(strings.TrimSpace(item.Get("role").String()))
	if role == "user" {
		return collector.legacyCPUResponseItemHasModerationText(item)
	}
	if role != "" {
		return false
	}
	return collector.legacyCPUResponseItemHasModerationText(item)
}

func (collector legacyCPUModerationTextCollector) legacyCPUResponseItemHasModerationText(item gjson.Result) bool {
	var parts []string
	var images []string
	collector.legacyCPUCollectContentValue(item.Get("content"), &parts, &images)
	if item.Get("type").String() == "input_text" || item.Get("text").Exists() {
		collector.legacyCPUCollectContentValue(item, &parts, &images)
	}
	return legacyCPUNormalizeContentModerationText(strings.Join(parts, "\n")) != "" || len(images) > 0
}

func (collector legacyCPUModerationTextCollector) legacyCPUCollectLastGeminiContent(contents gjson.Result, parts *[]string, images *[]string) {
	if !contents.IsArray() {
		return
	}
	array := contents.Array()
	if len(array) == 0 {
		return
	}
	last := array[len(array)-1]
	role := strings.ToLower(strings.TrimSpace(last.Get("role").String()))
	if role != "" && role != "user" {
		return
	}
	var candidate []string
	var candidateImages []string
	if arr := last.Get("parts"); arr.IsArray() {
		arr.ForEach(func(_, part gjson.Result) bool {
			collector.legacyCPUAddModerationText(&candidate, part.Get("text").String())
			legacyCPUAddGeminiModerationImage(&candidateImages, part)
			return true
		})
	}
	if legacyCPUNormalizeContentModerationText(strings.Join(candidate, "\n")) == "" && len(candidateImages) == 0 {
		return
	}
	*parts = append(*parts, candidate...)
	*images = append(*images, candidateImages...)
}

func (collector legacyCPUModerationTextCollector) legacyCPUCollectContentValue(value gjson.Result, parts *[]string, images *[]string) {
	switch {
	case !value.Exists():
		return
	case value.Type == gjson.String:
		collector.legacyCPUAddModerationText(parts, value.String())
	case value.IsArray():
		value.ForEach(func(_, item gjson.Result) bool {
			collector.legacyCPUCollectContentValue(item, parts, images)
			return true
		})
	case value.IsObject():
		typ := strings.ToLower(strings.TrimSpace(value.Get("type").String()))
		legacyCPUAddModerationImage(images, value.Get("image_url.url").String())
		legacyCPUAddModerationImage(images, value.Get("image_url").String())
		legacyCPUAddModerationImage(images, value.Get("url").String())
		legacyCPUAddModerationImageData(images, value.Get("source.media_type").String(), value.Get("source.data").String())
		legacyCPUAddModerationImageData(images, value.Get("source.mediaType").String(), value.Get("source.data").String())
		legacyCPUAddModerationImageData(images, value.Get("media_type").String(), value.Get("data").String())
		legacyCPUAddModerationImageData(images, value.Get("mime_type").String(), value.Get("data").String())
		legacyCPUAddModerationImageData(images, value.Get("mimeType").String(), value.Get("data").String())
		legacyCPUAddModerationImage(images, value.Get("source.data").String())
		legacyCPUAddModerationImage(images, value.Get("data").String())
		legacyCPUAddModerationImage(images, value.Get("base64").String())
		switch typ {
		case "", "text", "input_text", "message":
			if value.Get("text").Exists() {
				collector.legacyCPUAddModerationText(parts, value.Get("text").String())
			}
			if value.Get("content").Exists() {
				collector.legacyCPUCollectContentValue(value.Get("content"), parts, images)
			}
		case "image_url", "input_image", "image":
		}
	}
}

func legacyCPUAddGeminiModerationImage(images *[]string, part gjson.Result) {
	if inlineData := part.Get("inline_data"); inlineData.IsObject() {
		mimeType := strings.TrimSpace(inlineData.Get("mime_type").String())
		data := strings.TrimSpace(inlineData.Get("data").String())
		if mimeType != "" && data != "" {
			legacyCPUAddModerationImage(images, fmt.Sprintf("data:%s;base64,%s", mimeType, data))
		}
	}
	if inlineData := part.Get("inlineData"); inlineData.IsObject() {
		mimeType := strings.TrimSpace(inlineData.Get("mimeType").String())
		data := strings.TrimSpace(inlineData.Get("data").String())
		if mimeType != "" && data != "" {
			legacyCPUAddModerationImage(images, fmt.Sprintf("data:%s;base64,%s", mimeType, data))
		}
	}
	legacyCPUAddModerationImage(images, part.Get("file_data.file_uri").String())
	legacyCPUAddModerationImage(images, part.Get("fileData.fileUri").String())
}

func legacyCPUAddModerationImageData(images *[]string, mimeType string, data string) {
	mimeType = strings.TrimSpace(mimeType)
	data = strings.TrimSpace(data)
	if mimeType == "" || data == "" {
		return
	}
	legacyCPUAddModerationImage(images, fmt.Sprintf("data:%s;base64,%s", mimeType, data))
}

func legacyCPUAddModerationImage(images *[]string, image string) {
	image = strings.TrimSpace(image)
	if image == "" {
		return
	}
	if strings.HasPrefix(image, "data:") || strings.HasPrefix(image, "http://") || strings.HasPrefix(image, "https://") {
		*images = append(*images, image)
	}
}

func legacyCPUNormalizeModerationImages(images []string) []string {
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

func legacyCPULimitContentModerationImages(images []string) []string {
	if len(images) <= maxContentModerationInputImages {
		return images
	}
	idx, err := rand.Int(rand.Reader, big.NewInt(int64(len(images))))
	if err != nil {
		return images[:maxContentModerationInputImages]
	}
	return []string{images[int(idx.Int64())]}
}

func (collector legacyCPUModerationTextCollector) legacyCPUAddModerationText(parts *[]string, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if collector.filterReminders && strings.Contains(text, "<system-reminder>") {
		return
	}
	*parts = append(*parts, text)
}

func legacyCPUNormalizeContentModerationText(text string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
}
