package securityaudit

import (
	"encoding/json"
	"errors"
	"regexp"
	"regexp/syntax"
	"strings"
	"unicode/utf8"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"golang.org/x/text/unicode/norm"
)

// Positions are zero-based rune offsets in the original extracted text segment.
// End is exclusive; SegmentIndex follows extraction order, before prioritizing.
type CBRNMatchEvidence struct {
	Rule         string `json:"rule"`
	Content      string `json:"content"`
	Source       string `json:"source"`
	SegmentIndex int    `json:"segment_index"`
	Start        int    `json:"start"`
	End          int    `json:"end"`
}

type CBRNMatch struct {
	Topic  CBRNMatchEvidence `json:"topic"`
	Intent CBRNMatchEvidence `json:"intent"`
}

type promptCBRNRule struct {
	rule string
	re   *regexp.Regexp
}

// Published with a configuration snapshot; all fields remain immutable.
type promptCBRNMatcher struct {
	topics  []promptCBRNRule
	intents []promptCBRNRule
}

func validateCBRNRules(topics, intents []string) error {
	_, err := newPromptCBRNMatcher(topics, intents)
	return err
}

func newPromptCBRNMatcher(topics, intents []string) (*promptCBRNMatcher, error) {
	if len(topics)+len(intents) > MaxBlockedKeywords {
		return nil, infraerrors.BadRequest("prompt_audit_too_many_cbrn_rules", "主题和操作意图规则合计不能超过 10000 条")
	}
	m := &promptCBRNMatcher{}
	var err error
	if m.topics, err = compileCBRNRules(topics); err != nil {
		return nil, err
	}
	if m.intents, err = compileCBRNRules(intents); err != nil {
		return nil, err
	}
	return m, nil
}

func compileCBRNRules(rules []string) ([]promptCBRNRule, error) {
	if err := validateBlockedKeywords(rules); err != nil {
		return nil, err
	}
	compiled := make([]promptCBRNRule, 0, len(rules))
	for index, rule := range rules {
		if strings.TrimSpace(rule) == "" {
			continue
		}
		var re *regexp.Regexp
		var err error
		if strings.HasPrefix(rule, promptKeywordRegexPrefix) {
			// Normalize input text, never regex syntax or escapes.
			re, err = compilePromptKeywordRegex(rule, index)
		} else {
			re, err = regexp.Compile("(?i:" + regexp.QuoteMeta(normalizeCBRNText(rule)) + ")")
		}
		if err != nil {
			return nil, err
		}
		parsed, parseErr := syntax.Parse(re.String(), syntax.Perl)
		if parseErr != nil {
			return nil, parseErr
		}
		if cbrnCanMatchEmpty(parsed) {
			return nil, infraerrors.BadRequest("prompt_audit_cbrn_empty_match", "组合规则不能匹配空字符串")
		}
		compiled = append(compiled, promptCBRNRule{rule: rule, re: re})
	}
	return compiled, nil
}

// Empty-width assertions can match no characters even if MatchString("") is
// false (notably \b). Reject every structurally nullable expression so these
// assertions cannot stand in for an operational intent.
func cbrnCanMatchEmpty(expression *syntax.Regexp) bool {
	switch expression.Op {
	case syntax.OpEmptyMatch, syntax.OpBeginLine, syntax.OpEndLine, syntax.OpBeginText, syntax.OpEndText, syntax.OpWordBoundary, syntax.OpNoWordBoundary, syntax.OpStar, syntax.OpQuest:
		return true
	case syntax.OpLiteral:
		return len(expression.Rune) == 0
	case syntax.OpCapture, syntax.OpPlus:
		return cbrnCanMatchEmpty(expression.Sub[0])
	case syntax.OpRepeat:
		return expression.Min == 0 || cbrnCanMatchEmpty(expression.Sub[0])
	case syntax.OpConcat:
		for _, part := range expression.Sub {
			if !cbrnCanMatchEmpty(part) {
				return false
			}
		}
		return true
	case syntax.OpAlternate:
		for _, part := range expression.Sub {
			if cbrnCanMatchEmpty(part) {
				return true
			}
		}
	}
	return false
}

type cbrnTextSegment struct {
	original, normalized string
	index                int
	latest               bool
	ready                bool
}

func (m *promptCBRNMatcher) Match(req Request) (*CBRNMatch, error) {
	if m == nil || len(m.topics) == 0 || len(m.intents) == 0 {
		return nil, nil
	}
	var document any
	if err := json.Unmarshal(req.Body, &document); err != nil {
		return nil, errors.New("prompt audit request JSON is invalid")
	}
	root, _ := document.(map[string]any)
	protocol := strings.ToLower(strings.TrimSpace(req.Protocol))
	if protocol == "gemini" || protocol == "gemini_generate_content" {
		return m.matchCBRNGemini(root), nil
	}
	latest := latestCBRNUserTexts(req.Protocol, root)
	if len(latest) == 0 {
		return nil, nil
	}
	extracted := extractProtocolSegments(req.Protocol, document)
	return m.matchCBRNSegments(extracted, latest, 0, nil), nil
}

func (m *promptCBRNMatcher) matchCBRNSegments(extracted []promptSegment, latest []string, offset int, originalIndexes []int) *CBRNMatch {
	latestIndexes := cbrnLatestSegmentIndexes(extracted, latest)
	if len(latestIndexes) == 0 {
		return nil
	}
	segments := make([]cbrnTextSegment, 0, len(extracted))
	for index, segment := range extracted {
		originalIndex := offset + index
		if originalIndexes != nil {
			originalIndex = originalIndexes[index]
		}
		segments = append(segments, cbrnTextSegment{original: segment.text, index: originalIndex, latest: latestIndexes[index]})
	}
	// Most requests have no current operational intent. Do not normalize their
	// history or scan the topic list unless the current input passes this gate.
	intent := matchCBRNList(m.intents, segments, true)
	if intent == nil {
		return nil
	}
	topic := matchCBRNList(m.topics, segments, false)
	if topic == nil {
		return nil
	}
	return &CBRNMatch{Topic: *topic, Intent: *intent}
}

// Gemini batch/predict members are independent requests, not conversation
// turns. A member must supply both conditions within its own prompt/history.
// Offsets follow extractGeminiRoot's flat order for the administrator evidence.
func (m *promptCBRNMatcher) matchCBRNGemini(root map[string]any) *CBRNMatch {
	if root == nil {
		return nil
	}
	match, offset := m.matchCBRNGeminiUnit(root, 0)
	if match != nil {
		return match
	}
	if requests, ok := root["requests"].([]any); ok {
		for _, value := range requests {
			if request, ok := value.(map[string]any); ok {
				var consumed int
				match, consumed = m.matchCBRNGeminiUnit(request, offset)
				if match != nil {
					return match
				}
				offset += consumed
			}
		}
	}
	return nil
}

func (m *promptCBRNMatcher) matchCBRNGeminiUnit(root map[string]any, offset int) (*CBRNMatch, int) {
	headers := extractGeminiSystemInstruction(root["systemInstruction"])
	headers = append(headers, extractGeminiSystemInstruction(root["system_instruction"])...)
	conversation := append([]promptSegment(nil), headers...)
	conversation = append(conversation, extractGemini(root["contents"])...)
	conversation = append(conversation, extractGemini(root["content"])...)
	latest, _ := cbrnGeminiRootUserTexts(map[string]any{"contents": root["contents"], "content": root["content"]})
	if match := m.matchCBRNSegments(conversation, latest, offset, nil); match != nil {
		return match, 0
	}
	consumed := len(conversation)
	if instances, ok := root["instances"].([]any); ok {
		for _, instance := range instances {
			prompt := extractGeminiInstances([]any{instance})
			if len(prompt) == 0 {
				continue
			}
			// Shared system instructions apply to each instance; sibling prompts
			// and conversation turns do not become its carried history.
			segments := append(append([]promptSegment(nil), headers...), prompt...)
			indexes := make([]int, len(segments))
			for index := range headers {
				indexes[index] = offset + index
			}
			for index := range prompt {
				indexes[len(headers)+index] = offset + consumed + index
			}
			if match := m.matchCBRNSegments(segments, promptSegmentTexts(prompt), 0, indexes); match != nil {
				return match, 0
			}
			consumed += len(prompt)
		}
	}
	return nil, consumed
}

func matchCBRNList(rules []promptCBRNRule, segments []cbrnTextSegment, latestOnly bool) *CBRNMatchEvidence {
	for _, rule := range rules {
		// Within one rule prefer the current input, then carried history. Rule
		// order remains the primary priority, independent of message position.
		for _, current := range []bool{true, false} {
			if latestOnly && !current {
				break
			}
			for index := range segments {
				segment := &segments[index]
				if segment.latest != current || segment.original == "" {
					continue
				}
				if !segment.ready {
					segment.normalized = normalizeCBRNText(segment.original)
					segment.ready = true
				}
				location := rule.re.FindStringIndex(segment.normalized)
				if location == nil || location[0] == location[1] {
					continue
				}
				start, end := cbrnOriginalSpan(segment.original, segment.normalized, location[0], location[1])
				source := "history"
				if segment.latest {
					source = "latest_user"
				}
				return &CBRNMatchEvidence{
					Rule: rule.rule, Content: boundedCBRNContent(segment.original[start:end]),
					Source: source, SegmentIndex: segment.index,
					Start: utf8.RuneCountInString(segment.original[:start]), End: utf8.RuneCountInString(segment.original[:end]),
				}
			}
		}
	}
	return nil
}

func boundedCBRNContent(value string) string {
	count := 0
	for index := range value {
		if count == MaxBlockedRegexRunes {
			return value[:index] + "…"
		}
		count++
	}
	return strings.Clone(value)
}

func normalizeCBRNText(value string) string {
	return stripCBRNZeroWidth(norm.NFKC.String(value))
}

func stripCBRNZeroWidth(value string) string {
	if !strings.ContainsAny(value, "\u200b\u200c\u200d\u2060\ufeff") {
		return value
	}
	return strings.Map(func(r rune) rune {
		switch r {
		case '\u200b', '\u200c', '\u200d', '\u2060', '\ufeff':
			return -1
		default:
			return r
		}
	}, value)
}

// Project only actual hits instead of allocating an offset entry per request
// byte. Each normalization segment is indivisible (e.g. e + combining accent,
// or a compatibility ligature); expanded characters map to that original span.
func cbrnOriginalSpan(original, normalized string, start, end int) (int, int) {
	if original == normalized {
		return start, end
	}
	originalStart, originalEnd, outputOffset := -1, -1, 0
	for position := 0; position < len(original); {
		size := norm.NFKC.NextBoundaryInString(original[position:], true)
		if size <= 0 {
			size = len(original) - position
		}
		next := position + size
		output := normalizeCBRNText(original[position:next])
		outputEnd := outputOffset + len(output)
		if len(output) > 0 && start < outputEnd && end > outputOffset {
			if originalStart < 0 {
				originalStart = position
			}
			originalEnd = next
		}
		outputOffset = outputEnd
		position = next
		if outputOffset >= end {
			break
		}
	}
	if originalStart < 0 {
		return 0, 0
	}
	return originalStart, originalEnd
}

// Extract the actual latest user message separately from the flat audit view:
// adjacent user messages are not one turn, and an image-only latest message
// must not accidentally reuse an older user's operational intent.
func latestCBRNUserTexts(protocol string, root map[string]any) []string {
	if root == nil {
		return nil
	}
	protocol = strings.ToLower(strings.TrimSpace(protocol))
	switch protocol {
	case "openai_chat_completions", "openai_chat", "chat_completions", "anthropic_messages", "claude_messages", "messages":
		return cbrnLatestMessageTexts(root["messages"], false)
	case "openai_responses", "responses", "responses_websocket":
		if frameType := stringValue(root["type"]); frameType != "" || protocol == "responses_websocket" {
			if frameType != "response.create" {
				return nil
			}
			if input, exists := root["input"]; exists && input != nil {
				return cbrnLatestResponsesTexts(input)
			}
			if response, ok := root["response"].(map[string]any); ok {
				return cbrnLatestResponsesTexts(response["input"])
			}
			return nil
		}
		return cbrnLatestResponsesTexts(root["input"])
	case "gemini", "gemini_generate_content":
		return cbrnLatestGeminiTexts(root)
	case "openai_images", "grok_media", "media", "images":
		return extractMediaPrompts(root)
	case "typesafe_systemone":
		return promptSegmentTexts(extractSystemOneSegments(root))
	default:
		if _, exists := root["messages"]; exists {
			return cbrnLatestMessageTexts(root["messages"], false)
		}
		if _, exists := root["input"]; exists {
			return cbrnLatestResponsesTexts(root["input"])
		}
		if _, exists := root["contents"]; exists {
			return cbrnLatestGeminiTexts(root)
		}
		return extractMediaPrompts(root)
	}
}

func cbrnLatestResponsesTexts(value any) []string {
	switch typed := value.(type) {
	case string:
		return []string{typed}
	case map[string]any:
		return cbrnLatestMessageTexts([]any{typed}, true)
	default:
		return cbrnLatestMessageTexts(value, true)
	}
}

func cbrnLatestMessageTexts(value any, responses bool) []string {
	items, _ := value.([]any)
	for index := len(items) - 1; index >= 0; index-- {
		if text, ok := items[index].(string); ok && responses {
			return []string{text}
		}
		message, ok := items[index].(map[string]any)
		if !ok {
			continue
		}
		role := strings.ToLower(stringValue(message["role"]))
		if role != "user" && !(responses && role == "") {
			continue
		}
		if responses {
			// An absent role on a function/tool result is not an implicit user.
			kind := stringValue(message["type"])
			if kind != "" && kind != "message" && kind != "input_text" {
				continue
			}
		}
		if content, exists := message["content"]; exists {
			return contentTexts(content)
		}
		if responses {
			return []string{stringValue(message["text"])}
		}
		return nil
	}
	return nil
}

func cbrnLatestGeminiTexts(root map[string]any) []string {
	if requests, ok := root["requests"].([]any); ok {
		for index := len(requests) - 1; index >= 0; index-- {
			if request, ok := requests[index].(map[string]any); ok {
				if text, found := cbrnGeminiRootUserTexts(request); found {
					return text
				}
			}
		}
	}
	text, _ := cbrnGeminiRootUserTexts(root)
	return text
}

func cbrnGeminiRootUserTexts(root map[string]any) ([]string, bool) {
	if instances, ok := root["instances"].([]any); ok {
		for index := len(instances) - 1; index >= 0; index-- {
			if instance, ok := instances[index].(map[string]any); ok {
				if prompt, exists := instance["prompt"]; exists {
					return []string{stringValue(prompt)}, true
				}
			}
		}
	}
	for _, field := range []string{"content", "contents"} {
		items, ok := root[field].([]any)
		if !ok {
			if item, ok := root[field].(map[string]any); ok {
				items = []any{item}
			}
		}
		for index := len(items) - 1; index >= 0; index-- {
			item, ok := items[index].(map[string]any)
			if !ok {
				continue
			}
			role := strings.ToLower(stringValue(item["role"]))
			if role != "" && role != "user" {
				continue
			}
			return promptSegmentTexts(extractGemini(item)), true
		}
	}
	return nil, false
}

func cbrnLatestSegmentIndexes(segments []promptSegment, texts []string) map[int]bool {
	indexes := make(map[int]bool)
	position := len(segments) - 1
	for textIndex := len(texts) - 1; textIndex >= 0; textIndex-- {
		text := texts[textIndex]
		if strings.TrimSpace(text) == "" {
			continue
		}
		for position >= 0 {
			index := position
			position--
			if isUserSegment(segments[index]) && segments[index].text == text {
				indexes[index] = true
				break
			}
		}
	}
	return indexes
}
