package service

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestContentModerationExtractionCPUParity(t *testing.T) {
	fixtures := []string{
		``, `null`, `[]`, `{"input":]}`,
		`{"input":"hello <system-reminder>secret</system-reminder>"}`,
		`{"input":[{"role":"user","content":"old"},{"type":"function_call_output","output":"unreviewed tool"}]}`,
		`{"input":[{"role":"user","content":[{"type":"input_text","text":"hello"},{"type":"image","source":{"media_type":"image/png","mediaType":"image/jpeg","data":"AAAA"}}]}]}`,
		`{"input":{"role":"user","type":"input_text","text":"outer","content":[{"type":"text","text":"nested"}]}}`,
		`{"input":[{"content":[{"type":"text","text":"<system-reminder>ignore</system-reminder>"},{"type":"text","text":"actual"}]}]}`,
		`{"input":[{"role":"assistant","content":"skip"}],"messages":[{"role":"user","content":"chat"}],"contents":[{"parts":[{"text":"gemini"}]}]}`,
		`{"messages":[{"role":"user","content":[{"type":"text","text":"<system-reminder>hidden"},{"type":"text","text":"shown"}]},{"role":"system","content":"tail"}]}`,
		`{"messages":[{"role":"user","content":"old"},{"role":"assistant","content":"skip"}]}`,
		`{"contents":[{"role":"user","parts":[{"inline_data":{"mime_type":"image/png","data":"AAAA"}},{"inlineData":{"mimeType":"image/jpeg","data":"BBBB"}},{"text":"caption"},{"fileData":{"fileUri":"https://example.test/image"}}]}]}`,
		`{"prompt":"generate","images":[{"type":"custom-extension","url":"https://example.test/image","source":{"data":"data:image/png;base64,AAAA"},"data":"https://example.test/other","base64":"data:image/png;base64,BBBB"}]}`,
		`{"input":[{"role":"user","content":[{"type":"text","type":"image","text":"first","text":"second","image_url":null,"image_url":"https://wrong.test"},{"t\u0079pe":"text","te\u0078t":"escaped"}]}]}`,
		`{"questions":{"q1":{"type":"text","instructions":"<system-reminder>ordinary","criteria":"criteria"}},"extra":"extension","state":{"key":"value"}}`,
		`{"input":[{"role":"user","content":[{"type":"image","source":null,"source":{"media_type":"image/png","data":"YWJj"}},{"image_url":{},"image_url":{"url":"https://example.test/late"}},{"source":{"data":"AAAA"},"source":{"media_type":"image/png"}}]}]}`,
		`{"contents":[{"role":"user","parts":[{"file_data":null,"file_data":{"file_uri":"https://example.test/snake"},"fileData":{},"fileData":{"fileUri":"https://example.test/camel"}}]}]}`,
	}
	protocols := []string{ContentModerationProtocolAnthropicMessages, ContentModerationProtocolOpenAIChat,
		ContentModerationProtocolOpenAIResponses, ContentModerationProtocolGemini,
		ContentModerationProtocolOpenAIImages, ContentModerationProtocolTypeSafeSystemOne, "unknown"}
	for _, protocol := range protocols {
		for i, fixture := range fixtures {
			t.Run(fmt.Sprintf("%s/%d", protocol, i), func(t *testing.T) {
				body := []byte(fixture)
				want := legacyCPUExtractContentModerationInput(protocol, body)
				wantKeyword := legacyCPUExtractContentModerationKeywordText(protocol, body)
				got, keyword := extractContentModerationInputs(protocol, body)
				if !reflect.DeepEqual(got, want) || keyword != wantKeyword {
					t.Fatalf("combined differs: got=%+v keyword=%q want=%+v keyword=%q", got, keyword, want, wantKeyword)
				}
				if single := ExtractContentModerationInput(protocol, body); !reflect.DeepEqual(single, want) {
					t.Fatalf("semantic differs: got=%+v want=%+v", single, want)
				}
				if single := extractContentModerationKeywordText(protocol, body); single != wantKeyword {
					t.Fatalf("keyword differs: got=%q want=%q", single, wantKeyword)
				}
			})
		}
	}
}

func TestContentModerationBorrowedInputResultsOwnMemory(t *testing.T) {
	body := []byte(`{"input":[{"role":"assistant","content":"` + strings.Repeat("history", 10000) + `"},{"role":"user","content":[{"type":"input_text","text":"hello"},{"type":"input_image","image_url":"https://example.test/image"},{"type":"image","source":{"media_type":"image/png","data":"AAAA"}}]}]}`)
	content, keyword := extractContentModerationInputs(ContentModerationProtocolOpenAIResponses, body)
	for i := range body {
		body[i] = 'x'
	}
	if content.Text != "hello" || keyword != "hello" || !reflect.DeepEqual(content.Images, []string{"https://example.test/image", "data:image/png;base64,AAAA"}) {
		t.Fatalf("result retained borrowed input: %+v keyword=%q", content, keyword)
	}
}

func TestContentModerationTrimRunesCPUParity(t *testing.T) {
	for _, text := range []string{"", "ascii", "简繁體🙂text", "e\u0301\x00終", "bad\xff\xfeutf8", strings.Repeat("文", 10000)} {
		for _, limit := range []int{-1, 0, 1, 2, 3, 4, 5, 30, 10000} {
			if got, want := trimRunes(text, limit), legacyCPUModerationTrimRunes(text, limit); got != want {
				t.Fatalf("limit %d: got=%q want=%q", limit, got, want)
			}
		}
	}
}

func BenchmarkContentModerationInputCPU(b *testing.B) {
	for _, size := range []int{1 << 20, 8 << 20, 32 << 20} {
		for _, kind := range []string{"text", "image", "tool_tail"} {
			var content any = strings.Repeat("large text ", size/11)
			if kind == "image" {
				content = []any{map[string]any{"type": "input_image", "source": map[string]any{"media_type": "image/png", "data": strings.Repeat("A", size)}}, map[string]any{"type": "input_text", "text": "caption"}}
			}
			input := []any{map[string]any{"role": "user", "content": content}}
			if kind == "tool_tail" {
				input = append(input, map[string]any{"type": "function_call_output", "output": "done"})
			}
			body, _ := json.Marshal(map[string]any{"input": input})
			for _, impl := range []string{"legacy", "shared"} {
				b.Run(fmt.Sprintf("%dMiB/%s/%s", size>>20, kind, impl), func(b *testing.B) {
					b.ReportAllocs()
					b.SetBytes(int64(len(body)))
					for b.Loop() {
						if impl == "legacy" {
							_ = legacyCPUExtractContentModerationInput(ContentModerationProtocolOpenAIResponses, body)
							_ = legacyCPUExtractContentModerationKeywordText(ContentModerationProtocolOpenAIResponses, body)
						} else {
							_, _ = extractContentModerationInputs(ContentModerationProtocolOpenAIResponses, body)
						}
					}
				})
			}
		}
	}
}
