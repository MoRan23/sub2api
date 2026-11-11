package securityaudit

import (
	"encoding/json"
	"errors"
	"strings"
)

// All combination policies share the same immutable matcher. Domain-specific
// behavior is supplied entirely by their topic and latest-user intent rules.
func newPromptCombinationMatcher(topics, intents []string) (*promptCBRNMatcher, error) {
	return newPromptCombinedMatcher(topics, intents, "prompt_audit_too_many_combination_rules", "prompt_audit_combination_empty_match")
}

func validateCombinationRules(topics, intents []string) error {
	_, err := newPromptCombinationMatcher(topics, intents)
	return err
}

// Owned by one MatchCombination call. Sequential policies reuse the parsed
// segments and their lazily normalized text; nothing is cached across requests.
type preparedPromptCombination struct {
	batches [][]cbrnTextSegment
}

func preparePromptCombination(req Request) (*preparedPromptCombination, error) {
	var document any
	if err := json.Unmarshal(req.Body, &document); err != nil {
		return nil, errors.New("prompt audit request JSON is invalid")
	}
	return preparePromptCombinationDocument(req.Protocol, document), nil
}

func preparePromptCombinationDocument(protocol string, document any) *preparedPromptCombination {
	prepared := &preparedPromptCombination{}
	root, _ := document.(map[string]any)
	protocol = strings.ToLower(strings.TrimSpace(protocol))
	if protocol == "gemini" || protocol == "gemini_generate_content" {
		if root == nil {
			return prepared
		}
		offset := prepared.appendGeminiUnit(root, 0)
		if requests, ok := root["requests"].([]any); ok {
			for _, value := range requests {
				if request, ok := value.(map[string]any); ok {
					offset += prepared.appendGeminiUnit(request, offset)
				}
			}
		}
		return prepared
	}
	latest := latestCBRNUserTexts(protocol, root)
	if len(latest) > 0 {
		prepared.appendSegments(extractProtocolSegments(protocol, document), latest, 0, nil)
	}
	return prepared
}

func (prepared *preparedPromptCombination) appendSegments(extracted []promptSegment, latest []string, offset int, originalIndexes []int) {
	latestIndexes := cbrnLatestSegmentIndexes(extracted, latest)
	if len(latestIndexes) == 0 {
		return
	}
	segments := make([]cbrnTextSegment, 0, len(extracted))
	for index, segment := range extracted {
		originalIndex := offset + index
		if originalIndexes != nil {
			originalIndex = originalIndexes[index]
		}
		segments = append(segments, cbrnTextSegment{original: segment.text, index: originalIndex, latest: latestIndexes[index]})
	}
	prepared.batches = append(prepared.batches, segments)
}

// Batch/predict members remain independent inputs. Flat offsets retain the
// existing extraction order even when a member contains no current user text.
func (prepared *preparedPromptCombination) appendGeminiUnit(root map[string]any, offset int) int {
	headers := extractGeminiSystemInstruction(root["systemInstruction"])
	headers = append(headers, extractGeminiSystemInstruction(root["system_instruction"])...)
	conversation := append([]promptSegment(nil), headers...)
	conversation = append(conversation, extractGemini(root["contents"])...)
	conversation = append(conversation, extractGemini(root["content"])...)
	latest, _ := cbrnGeminiRootUserTexts(map[string]any{"contents": root["contents"], "content": root["content"]})
	prepared.appendSegments(conversation, latest, offset, nil)
	consumed := len(conversation)
	if instances, ok := root["instances"].([]any); ok {
		for _, instance := range instances {
			prompt := extractGeminiInstances([]any{instance})
			if len(prompt) == 0 {
				continue
			}
			// Shared system instructions apply, while sibling inputs do not.
			segments := append(append([]promptSegment(nil), headers...), prompt...)
			indexes := make([]int, len(segments))
			for index := range headers {
				indexes[index] = offset + index
			}
			for index := range prompt {
				indexes[len(headers)+index] = offset + consumed + index
			}
			prepared.appendSegments(segments, promptSegmentTexts(prompt), 0, indexes)
			consumed += len(prompt)
		}
	}
	return consumed
}
