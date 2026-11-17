package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Frozen pre-optimization transform and input filter for differential checks.
// Unchanged leaf helpers are shared; the old unconditional copies and Modified
// flag are retained here for same-process retry preparation benchmarks.
func legacyCPUApplyCodexOAuthTransform(reqBody map[string]any, opts codexOAuthTransformOptions) codexTransformResult {
	result := codexTransformResult{}
	if normalizeOpenAIOAuthResponsesCompatibilityFields(reqBody) {
		result.Modified = true
	}
	// 工具续链需求会影响存储策略与 input 过滤逻辑。
	needsToolContinuation := NeedsToolContinuation(reqBody)

	model := ""
	if v, ok := reqBody["model"].(string); ok {
		model = v
	}
	normalizedModel := strings.TrimSpace(model)
	if normalizedModel != "" {
		if model != normalizedModel {
			reqBody["model"] = normalizedModel
			result.Modified = true
		}
		result.NormalizedModel = normalizedModel
	}

	if opts.IsCompact {
		if _, ok := reqBody["store"]; ok {
			delete(reqBody, "store")
			result.Modified = true
		}
		if _, ok := reqBody["stream"]; ok {
			delete(reqBody, "stream")
			result.Modified = true
		}
	} else {
		// OAuth 走 ChatGPT internal API 时，store 必须为 false；显式 true 也会强制覆盖。
		// 避免上游返回 "Store must be set to false"。
		if v, ok := reqBody["store"].(bool); !ok || v {
			reqBody["store"] = false
			result.Modified = true
		}
		if v, ok := reqBody["stream"].(bool); !ok || !v {
			reqBody["stream"] = true
			result.Modified = true
		}
	}

	// Strip parameters unsupported by ChatGPT internal Codex endpoint.
	for _, key := range openAICodexOAuthUnsupportedFields {
		if _, ok := reqBody[key]; ok {
			delete(reqBody, key)
			result.Modified = true
		}
	}
	if options, present := reqBody["stream_options"]; present {
		filtered, changed := filterOpenAICodexStreamOptions(options, opts.IsCompact)
		if changed {
			if filtered == nil {
				delete(reqBody, "stream_options")
			} else {
				reqBody["stream_options"] = filtered
			}
			result.Modified = true
		}
	}

	// 请求带 reasoning 时补齐 include:["reasoning.encrypted_content"]，与真实 Codex 对齐
	// （compact 端点形态不同，单独处理，此处跳过）。
	if !opts.IsCompact && ensureCodexReasoningInclude(reqBody) {
		result.Modified = true
	}

	// 兼容遗留的 functions 和 function_call，转换为 tools 和 tool_choice
	if functionsRaw, ok := reqBody["functions"]; ok {
		if functions, k := functionsRaw.([]any); k {
			tools := make([]any, 0, len(functions))
			for _, f := range functions {
				tools = append(tools, map[string]any{
					"type":     "function",
					"function": f,
				})
			}
			reqBody["tools"] = tools
		}
		delete(reqBody, "functions")
		result.Modified = true
	}

	if fcRaw, ok := reqBody["function_call"]; ok {
		if fcStr, ok := fcRaw.(string); ok {
			// e.g. "auto", "none"
			reqBody["tool_choice"] = fcStr
		} else if fcObj, ok := fcRaw.(map[string]any); ok {
			// e.g. {"name": "my_func"}
			if name, ok := fcObj["name"].(string); ok && strings.TrimSpace(name) != "" {
				reqBody["tool_choice"] = map[string]any{
					"type": "function",
					"name": name,
				}
			}
		}
		delete(reqBody, "function_call")
		result.Modified = true
	}

	if normalizeCodexTools(reqBody) {
		result.Modified = true
	}
	if normalizeCodexToolChoice(reqBody) {
		result.Modified = true
	}

	if v, ok := reqBody["prompt_cache_key"].(string); ok {
		result.PromptCacheKey = strings.TrimSpace(v)
		if isOpenAICompatMessagesBridgeRequestBody(reqBody) {
			delete(reqBody, "prompt_cache_key")
			result.Modified = true
		}
	}

	// ChatGPT internal Codex endpoint does not accept role:"system".
	// Mirror its text into instructions because Codex OAuth requires it. Some
	// callers must also keep the guidance in input as developer (notably
	// Responses JSON object mode), while Chat Completions compatibility can
	// omit text-only messages after promoting them losslessly.
	if extractSystemMessagesFromInput(reqBody, opts.OmitPromotedSystemMessagesFromInput) {
		result.Modified = true
	}

	if isCodexSparkModel(normalizedModel) {
		// The server-owned image policy must not hide a missing caller prompt.
		if applyDefaultCodexInstructions(reqBody, normalizedModel) {
			result.Modified = true
		}
		if applyCodexSparkImageUnsupportedInstructions(reqBody) {
			result.Modified = true
		}
	}
	// gpt-5.3-codex-spark rejects the image_generation tool upstream (HTTP 400,
	// param=tools); Codex CLI advertises it by default, so strip it for spark.
	if isCodexSparkModel(normalizedModel) && stripCodexSparkImageGenerationTools(reqBody) {
		result.Modified = true
	}

	// 续链场景保留 item_reference 与 id，避免 call_id 上下文丢失。
	if input, ok := reqBody["input"].([]any); ok {
		if normalizedInput, modified := normalizeCodexToolRoleMessages(input); modified {
			input = normalizedInput
			result.Modified = true
		}
		if normalizedInput, modified := normalizeCodexMessageContentText(input); modified {
			input = normalizedInput
			result.Modified = true
		}
		input = legacyCPUFilterCodexInput(input, codexInputFilterOptions{
			PreserveReferences: needsToolContinuation,
			PreserveCallIDs:    opts.PreserveToolCallIDs,
		})
		reqBody["input"] = input
		result.Modified = true
	} else if inputStr, ok := reqBody["input"].(string); ok {
		// ChatGPT codex endpoint requires input to be a list, not a string.
		// Convert string input to the expected message array format.
		trimmed := strings.TrimSpace(inputStr)
		if trimmed != "" {
			reqBody["input"] = []any{
				map[string]any{
					"type":    "message",
					"role":    "user",
					"content": inputStr,
				},
			}
		} else {
			reqBody["input"] = []any{}
		}
		result.Modified = true
	}

	// Runs after input filtering so it sees the final replayed history. The
	// compact endpoint has its own wire shape and is left untouched.
	if !opts.IsCompact && ensureOpenAIOAuthWebSearchToolForHistory(reqBody, opts.ResponsesLite) {
		result.Modified = true
	}

	return result
}
func legacyCPUFilterCodexInput(input []any, opts codexInputFilterOptions) []any {
	filtered := make([]any, 0, len(input))
	referenceIDMappings := codexItemReferenceIDMappings(input, opts.PreserveCallIDs)
	inputItemIDs := codexInputItemIDs(input)
	inputCallIDs := codexInputCallIDs(input)
	for _, item := range input {
		m, ok := item.(map[string]any)
		if !ok {
			filtered = append(filtered, item)
			continue
		}
		typ, _ := m["type"].(string)
		if isCodexStandaloneFunctionOutput(m) {
			// Independent named context has an item ID, not a corresponding call.
			// Retain its fields and shared immutable contents without copying.
			filtered = append(filtered, m)
			continue
		}

		// chatgpt.com codex (OAuth path) runs with store=false (forced by
		// applyCodexOAuthTransform). Replaying a reasoning item with its rs_*
		// id but no encrypted_content 404s upstream ("Item with id 'rs_...'
		// not found") — the 404 is triggered by the id lookup, not by the
		// reasoning item itself. So strip the id (always, independent of
		// PreserveReferences) yet keep the item: under store=false
		// encrypted_content is the official channel for carrying reasoning
		// context across turns, and dropping the whole item silently degrades
		// multi-turn agent reasoning. Preserve encrypted_content/content/
		// summary and every other field verbatim. Upstream additionally
		// requires a summary field — a missing one is rejected with 400
		// "Missing required parameter 'input[N].summary'" — so backfill an
		// empty array when it is absent. Contracts verified end-to-end against
		// chatgpt.com codex (gpt-5.5); see issue #1957.
		// compaction_summary items (cmp_*) are the other encrypted_content
		// carrier. Verified against the live backend: they require
		// encrypted_content (a missing one is rejected with 400), and with it
		// present the cmp_* id does not 404 whether kept or stripped. Being
		// neither reasoning nor tool calls, they flow through the generic path
		// below (id stripped when !PreserveReferences, encrypted_content
		// preserved either way), which is safe and needs no special-casing.
		if typ == "reasoning" {
			newItem := make(map[string]any, len(m))
			for key, value := range m {
				if key == "id" || key == "call_id" {
					// rs_* id replayed under store=false 404s; strip it.
					continue
				}
				newItem[key] = value
			}
			if summary, ok := newItem["summary"]; !ok || summary == nil {
				// Upstream requires a summary field; an empty array satisfies it.
				newItem["summary"] = []any{}
			}
			filtered = append(filtered, newItem)
			continue
		}

		// 仅修正真正的 tool/function call 标识，避免误改普通 message/reasoning id；
		// 若 item_reference 指向 legacy call_* 标识，则仅修正该引用本身。
		fixCallIDPrefix := func(id string) string {
			return normalizeCodexFilterCallID(typ, id, opts.PreserveCallIDs)
		}

		if typ == "item_reference" {
			if !opts.PreserveReferences {
				continue
			}
			newItem := make(map[string]any, len(m))
			for key, value := range m {
				newItem[key] = value
			}
			if id, ok := newItem["id"].(string); ok && strings.HasPrefix(strings.TrimSpace(id), "call_") {
				trimmedID := strings.TrimSpace(id)
				_, referencesExistingItem := inputItemIDs[trimmedID]
				if !referencesExistingItem {
					if normalizedID, mapped := referenceIDMappings[trimmedID]; mapped {
						newItem["id"] = normalizedID
					} else if _, hasSameTurnCall := inputCallIDs[trimmedID]; !hasSameTurnCall {
						// A bare call_* reference is a legacy function-call identifier.
						// Normalize it even when its call item lives in an earlier turn.
						newItem["id"] = normalizeCodexCallID(trimmedID)
					}
				}
			}
			filtered = append(filtered, newItem)
			continue
		}

		newItem := m
		copied := false
		// 仅在需要修改字段时创建副本，避免直接改写原始输入。
		ensureCopy := func() {
			if copied {
				return
			}
			newItem = make(map[string]any, len(m))
			for key, value := range m {
				newItem[key] = value
			}
			copied = true
		}

		if isCodexToolCallItemType(typ) {
			callID, ok := m["call_id"].(string)
			if !ok || strings.TrimSpace(callID) == "" {
				if id, ok := m["id"].(string); ok && strings.TrimSpace(id) != "" {
					callID = id
					ensureCopy()
					newItem["call_id"] = callID
				}
			}

			if callID != "" {
				fixedCallID := fixCallIDPrefix(callID)
				if fixedCallID != callID {
					ensureCopy()
					newItem["call_id"] = fixedCallID
				}
			}
		}

		if !isCodexToolCallItemType(typ) {
			ensureCopy()
			delete(newItem, "call_id")
		}

		if codexInputItemRequiresName(typ) {
			if strings.TrimSpace(firstNonEmptyString(m["name"])) == "" {
				name := firstNonEmptyString(m["tool_name"])
				if name == "" {
					if function, ok := m["function"].(map[string]any); ok {
						name = firstNonEmptyString(function["name"])
					}
				}
				if name == "" {
					name = "tool"
				}
				ensureCopy()
				newItem["name"] = name
			}
		}

		if !opts.PreserveReferences {
			ensureCopy()
			delete(newItem, "id")
		} else if id, ok := m["id"].(string); ok && shouldStripOpenAIResponsesInputItemID(typ, id) {
			ensureCopy()
			delete(newItem, "id")
		}

		filtered = append(filtered, newItem)
	}
	return filtered
}

func TestCodexInputFilterCPUParity(t *testing.T) {
	fixtures := []string{
		"null", "[]",
		`[null,5,"unchanged",{"type":"message","role":"user","content":"native"}]`,
		`[{"type":"reasoning","summary":[],"encrypted_content":"opaque"},{"type":"reasoning","id":null,"call_id":"c","summary":null},{"type":"reasoning","content":"unchanged"}]`,
		`[{"type":"message","id":"msg_1","call_id":"irrelevant","role":"user","content":"native"},{"type":"message","id":7,"content":"typed id"}]`,
		`[{"type":"function_call","id":"call_x","name":"f","arguments":"{}"},{"type":"function_call_output","call_id":"call_x","output":"result"},{"type":"item_reference","id":"call_x"}]`,
		`[{"type":"custom_tool_call","call_id":"call_x","tool_name":"f"},{"type":"custom_tool_call_output","call_id":"call_x","output":"result"},{"type":"item_reference","id":"call_missing"}]`,
		`[{"type":"function_call","id":"call_x","call_id":"custom","function":{"name":"f"}},{"type":"item_reference","id":"call_x"},{"type":"item_reference","id":"call_prior"}]`,
		`[{"type":"function_call_output","name":"agent.proposed_plan","namespace":null,"call_id":null,"id":"independent","output":{"content":"standalone"}}]`,
		`[{"type":"function_call","call_id":"fc_x","name":"f"},{"type":"function_call_output","call_id":"fc_x","output":"native"}]`,
		`[{"type":"item_reference","id":null},{"type":"unknown","call_id":null,"id":null},{"type":"tool_call","id":null,"name":null}]`,
	}
	for _, fixture := range fixtures {
		for _, preserveReferences := range []bool{false, true} {
			for _, preserveCallIDs := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/refs_%t/calls_%t", fixture, preserveReferences, preserveCallIDs), func(t *testing.T) {
					var input []any
					require.NoError(t, decodeOpenAIJSONUseNumber([]byte(fixture), &input))
					before, err := marshalOpenAIUpstreamJSON(input)
					require.NoError(t, err)
					opts := codexInputFilterOptions{PreserveReferences: preserveReferences, PreserveCallIDs: preserveCallIDs}
					want := legacyCPUFilterCodexInput(input, opts)
					got, changed := filterCodexInputWithChanges(input, opts)
					require.Equal(t, want, got)
					after, err := marshalOpenAIUpstreamJSON(input)
					require.NoError(t, err)
					require.Equal(t, before, after, "filter must not mutate input items")
					encoded, err := marshalOpenAIUpstreamJSON(got)
					require.NoError(t, err)
					require.Equal(t, !bytes.Equal(before, encoded), changed, "changed must describe actual JSON changes")
				})
			}
		}
	}
}

func TestCodexTransformCPUParity(t *testing.T) {
	fixtures := []string{
		`{"model":"gpt-6-luna","store":false,"stream":true,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"native"}]}]}`,
		`{"model":"gpt-6-luna","store":false,"stream":true,"input":[]}`,
		`{"model":"gpt-6-luna","input":"legacy","reasoning":{"effort":"high"}}`,
		`{"model":" gpt-6-luna ","input":[{"role":"system","content":"system"},{"role":"tool","tool_call_id":"call_x","content":"tool"}],"functions":[{"name":"f","parameters":{"type":"object"}}],"function_call":{"name":"f"}}`,
		`{"model":"gpt-6-luna","store":false,"stream":true,"input":[{"type":"reasoning","summary":[],"encrypted_content":"opaque"},{"type":"function_call","call_id":"fc_x","name":"f","arguments":"{}"},{"type":"function_call_output","call_id":"fc_x","output":"done"}],"tools":[{"type":"function","name":"f","parameters":{"type":"object"}}]}`,
		`{"model":"gpt-6-luna","store":false,"stream":true,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":123}],"internal_chat_message_metadata_passthrough":{"x":1}}],"text":{"format":{"type":"json_object"}}}`,
		`{"model":"gpt-5.3-codex-spark","input":[{"role":"system","content":"x"},{"role":"user","content":"native"}],"tools":[{"type":"image_generation"}]}`,
		`{"model":"gpt-6-luna","store":false,"stream":true,"input":[{"type":"function_call_output","name":"agent.proposed_plan","call_id":null,"output":{"content":"plan"}},{"type":"web_search_call","status":"completed"}]}`,
	}
	for _, fixture := range fixtures {
		for mask := 0; mask < 16; mask++ {
			opts := codexOAuthTransformOptions{IsCompact: mask&1 != 0, PreserveToolCallIDs: mask&2 != 0, OmitPromotedSystemMessagesFromInput: mask&4 != 0, ResponsesLite: mask&8 != 0}
			var before, want, got map[string]any
			require.NoError(t, decodeOpenAIJSONUseNumber([]byte(fixture), &before))
			require.NoError(t, decodeOpenAIJSONUseNumber([]byte(fixture), &want))
			require.NoError(t, decodeOpenAIJSONUseNumber([]byte(fixture), &got))
			wantResult := legacyCPUApplyCodexOAuthTransform(want, opts)
			gotResult := applyCodexOAuthTransformWithOptions(got, opts)
			require.Equal(t, want, got, "fixture %s options %+v", fixture, opts)
			require.Equal(t, wantResult.NormalizedModel, gotResult.NormalizedModel)
			require.Equal(t, wantResult.PromptCacheKey, gotResult.PromptCacheKey)
			if !gotResult.Modified {
				require.True(t, reflect.DeepEqual(before, got), "false modified must never hide a real edit")
			}
		}
	}
}

func TestCodexNativeTransformDoesNotForceSerialization(t *testing.T) {
	body := []byte(`{"model":"gpt-6-luna","store":false,"stream":true,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"native"}]},{"type":"reasoning","summary":[],"encrypted_content":"opaque"}]}`)
	for _, attempts := range []int{1, 5, 15} {
		for attempt := 0; attempt < attempts; attempt++ {
			var request map[string]any
			require.NoError(t, decodeOpenAIJSONUseNumber(body, &request))
			result := applyCodexOAuthTransformWithOptions(request, codexOAuthTransformOptions{})
			require.False(t, result.Modified, "native retry should reuse its bytes")
		}
	}
}

var codexTransformCPUBenchmarkSink []byte

func BenchmarkCodexTransformRetryCPU(b *testing.B) {
	for _, size := range []int{1 << 20, 8 << 20} {
		for _, image := range []bool{false, true} {
			content := map[string]any{"type": "input_text", "text": strings.Repeat("x", size)}
			if image {
				content = map[string]any{"type": "input_image", "image_url": "data:image/png;base64," + strings.Repeat("A", size)}
			}
			body, err := json.Marshal(map[string]any{"model": "gpt-6-luna", "store": false, "stream": true, "input": []any{map[string]any{"type": "message", "role": "user", "content": []any{content}}}})
			if err != nil {
				b.Fatal(err)
			}
			for _, attempts := range []int{1, 5, 15} {
				for _, legacy := range []bool{true, false} {
					mode := "sparse"
					if legacy {
						mode = "legacy"
					}
					b.Run(fmt.Sprintf("%dMiB/image_%t/attempts_%d/%s", size>>20, image, attempts, mode), func(b *testing.B) {
						b.ReportAllocs()
						b.SetBytes(int64(len(body) * attempts))
						for b.Loop() {
							for attempt := 0; attempt < attempts; attempt++ {
								var request map[string]any
								if err := decodeOpenAIJSONUseNumber(body, &request); err != nil {
									b.Fatal(err)
								}
								var result codexTransformResult
								if legacy {
									result = legacyCPUApplyCodexOAuthTransform(request, codexOAuthTransformOptions{})
								} else {
									result = applyCodexOAuthTransformWithOptions(request, codexOAuthTransformOptions{})
								}
								out := body
								if result.Modified || (!legacy && !canReuseDecodedOpenAICodexBody(body, request)) {
									out, err = marshalOpenAIUpstreamJSON(request)
									if err != nil {
										b.Fatal(err)
									}
								}
								codexTransformCPUBenchmarkSink = out
							}
						}
					})
				}
			}
		}
	}
}
