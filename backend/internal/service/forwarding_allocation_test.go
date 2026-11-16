//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// Retain the old single-append tests against the new incremental implementation.
func appendRawJSON(initial json.RawMessage, fragment string) json.RawMessage {
	var b anthropicBufferedBlock
	b.appendInput(initial, fragment)
	if b.input == nil {
		return json.RawMessage{}
	}
	return b.input
}

func legacyAppendRawJSON(initial json.RawMessage, fragment string) json.RawMessage {
	var object map[string]json.RawMessage
	if len(initial) == 0 || (json.Unmarshal(initial, &object) == nil && object != nil && len(object) == 0) {
		return json.RawMessage(fragment)
	}
	return json.RawMessage(string(initial) + fragment)
}

func TestAnthropicBufferedInputDifferential(t *testing.T) {
	initials := []string{"", "{}", " { \n } \t", `{"existing":true}`, "null", "[]", "123", "true", `"string"`, "{", " \n", "}\n", "{\v}"}
	fragments := []string{"", "{", "}", " ", "\n", `"x":`, `"文字"`, ",", "null", "[", "]", "\\", "\x00", "\xff"}
	rng := rand.New(rand.NewSource(7))
	for _, initial := range initials {
		for trial := 0; trial < 100; trial++ {
			var b anthropicBufferedBlock
			want := json.RawMessage(initial)
			for n := 0; n < 30; n++ {
				fragment := fragments[rng.Intn(len(fragments))]
				want = legacyAppendRawJSON(want, fragment)
				b.appendInput(json.RawMessage(initial), fragment)
				require.Equal(t, string(want), string(b.input), "initial=%q step=%d", initial, n)
			}
		}
	}
}

func TestAnthropicBufferedContentPreservesBlocks(t *testing.T) {
	for _, signatures := range []bool{false, true} {
		response := &apicompat.AnthropicResponse{Content: []apicompat.AnthropicContentBlock{{Type: "thinking", Text: "initial", Thinking: "thought", Signature: "sig", Input: json.RawMessage(`{"existing":true}`)}}}
		var a anthropicBufferedContent
		a.reset(response)
		a.appendBlock(apicompat.AnthropicContentBlock{Type: "tool_use", Input: json.RawMessage("{}")})
		a.applyDelta(-1, &apicompat.AnthropicDelta{Type: "text_delta", Text: "ignored"}, signatures)
		a.applyDelta(2, &apicompat.AnthropicDelta{Type: "text_delta", Text: "ignored"}, signatures)
		for i := 0; i < 3; i++ {
			a.applyDelta(0, &apicompat.AnthropicDelta{Type: "text_delta", Text: "text"}, signatures)
			a.applyDelta(0, &apicompat.AnthropicDelta{Type: "thinking_delta", Thinking: "think"}, signatures)
			a.applyDelta(0, &apicompat.AnthropicDelta{Type: "signature_delta", Signature: "sign"}, signatures)
		}
		a.applyDelta(1, &apicompat.AnthropicDelta{Type: "input_json_delta", PartialJSON: `{"text":`}, signatures)
		a.applyDelta(1, &apicompat.AnthropicDelta{Type: "input_json_delta", PartialJSON: `"中文"}`}, signatures)
		a.finalize()
		require.Equal(t, "initialtexttexttext", response.Content[0].Text)
		require.Equal(t, "thoughtthinkthinkthink", response.Content[0].Thinking)
		wantSignature := "sig"
		if signatures {
			wantSignature += "signsignsign"
		}
		require.Equal(t, wantSignature, response.Content[0].Signature)
		require.Equal(t, `{"existing":true}`, string(response.Content[0].Input))
		require.Equal(t, `{"text":"中文"}`, string(response.Content[1].Input))
		a.reset(&apicompat.AnthropicResponse{})
		a.finalize()
		require.Empty(t, a.response.Content)
	}
}

func legacyWSComparable(payload []byte) ([]byte, error) {
	var decoded map[string]any
	if err := decodeOpenAIJSONUseNumber(payload, &decoded); err != nil {
		return nil, err
	}
	for _, key := range []string{"input", "previous_response_id", "client_metadata", "stream_options"} {
		delete(decoded, key)
	}
	if generate, ok := decoded["generate"].(bool); ok && !generate {
		delete(decoded, "generate")
	}
	return json.Marshal(decoded)
}

func TestWSComparableSelectiveDifferential(t *testing.T) {
	inputs := []string{
		`{"model":"test","input":"large"}`, `{"i\u006eput":"discard","nested":{"b":2,"a":1},"generate":false}`,
		`{"model":"first","model":"last","generate":true}`, `{"generate":false,"generate":true}`, `{"generate":true,"generate":false}`,
		`{"n":9007199254740993,"x":1e0,"y":1.0,"input":{"a":[1,2]}}`, `{"client_metadata":{},"previous_response_id":"resp_1","stream_options":[]}`,
		`null`, `[]`, `1`, `"foo"`, `{}`, `{"x":1} {}`, `{"x":"` + "\xff" + `"}`, `{"input":"` + "\xff" + `"}`, `{"x":1e9999}`,
	}
	for _, input := range inputs {
		want, we := legacyWSComparable([]byte(input))
		got, ge := normalizeOpenAIWSPayloadWithoutInputAndPreviousResponseID([]byte(input))
		require.Equal(t, we == nil, ge == nil, input)
		if we == nil {
			require.Equal(t, string(want), string(got), input)
		}
	}
}

func TestWSComparableCacheTurnRevisions(t *testing.T) {
	var cache openAIWSComparableCache
	first := []byte(`{"model":"m","store":true,"input":[{"text":"start"}]}`)
	state, err := buildOpenAIWSIngressPreviousTurnStrictState(first, &cache)
	require.NoError(t, err)
	canonical, _ := cache.get(first)
	require.True(t, &canonical[0] == &state.nonInputComparable[0])
	cache.invalidate()
	second := []byte(`{"model":"m","store":false,"previous_response_id":"resp_1","input":[]}`)
	ok, reason, err := shouldKeepIngressPreviousResponseIDWithStrictState(state, second, "resp_1", false, &cache)
	// store is a meaningful field; changing it still forces the existing repair.
	require.NoError(t, err)
	require.False(t, ok)
	require.Equal(t, "non_input_changed", reason)
	state, err = buildOpenAIWSIngressPreviousTurnStrictState(second, &cache)
	require.NoError(t, err)
	cache.invalidate()
	third := []byte(`{"model":"m","store":false,"previous_response_id":"resp_2","input":[]}`)
	ok, _, err = shouldKeepIngressPreviousResponseIDWithStrictState(state, third, "resp_2", false, &cache)
	require.NoError(t, err)
	require.True(t, ok)
	cache.invalidate()
	changed := bytes.Replace(third, []byte(`"m"`), []byte(`"n"`), 1)
	ok, _, err = shouldKeepIngressPreviousResponseIDWithStrictState(state, changed, "resp_2", false, &cache)
	require.NoError(t, err)
	require.False(t, ok)
}

func TestAnthropicSSEDeltaViewConservative(t *testing.T) {
	for _, pair := range [][2]string{{"text_delta", "text"}, {"thinking_delta", "thinking"}, {"input_json_delta", "partial_json"}, {"signature_delta", "signature"}} {
		raw := []byte(fmt.Sprintf(`{"type":"content_block_delta","index":2,"delta":{"type":%q,%q:"\\\"文本"}}`, pair[0], pair[1]))
		index, kind, ok := anthropicSSEDeltaView(raw, "")
		require.True(t, ok, string(raw))
		require.Equal(t, 2, index)
		require.Equal(t, pair[0], kind)
	}
	base := `{"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"hi"}}`
	for _, raw := range []string{
		strings.Replace(base, `"index":2`, `"index":2,"index":3`, 1), strings.Replace(base, `"index":2`, `"index":2.1`, 1),
		strings.Replace(base, `"index":2`, `"index":1e9999`, 1), strings.Replace(base, `"index":2`, `"index":-1`, 1),
		strings.Replace(base, `"index":2`, `"index":2,"usage":{}`, 1), strings.Replace(base, `"text":"hi"`, `"text":"hi","new_field":1`, 1),
		strings.Replace(base, `"text":"hi"`, `"text":null`, 1), strings.Replace(base, `"text":"hi"`, `"text":"hi","text":"bye"`, 1),
		strings.Replace(base, `"type":`, `"t\u0079pe":`, 1), "[]", `{"delta":null}`, base + " trailing",
	} {
		_, _, ok := anthropicSSEDeltaView([]byte(raw), "")
		require.False(t, ok, raw)
	}
	_, _, ok := anthropicSSEDeltaView([]byte(base), "message_delta")
	require.False(t, ok)
}

func TestAnthropicSSEDeltaViewPreservesForwardedEvents(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := newMinimalGatewayService()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	frames := []string{
		`{"type":"message_start","message":{"usage":{"input_tokens":3}}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"文字\\\""}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"thinking_delta","thinking":"thought"}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"signature_delta","signature":"sig"}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"q\":1}"}}`,
		// Both cases deliberately take the fallback and retain the original bytes.
		`{"type":"content_block_delta","index":0,"index":1,"delta":{"type":"text_delta","text":"duplicate"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"extension","extension":true}}`,
		`{"type":"message_delta","usage":{"output_tokens":7}}`,
		`{"type":"message_stop"}`,
	}
	var input, expected strings.Builder
	for _, frame := range frames {
		var envelope struct {
			Type string `json:"type"`
		}
		require.NoError(t, json.Unmarshal([]byte(frame), &envelope))
		input.WriteString("data: " + frame + "\n\n")
		expected.WriteString("event: " + envelope.Type + "\ndata: " + frame + "\n\n")
	}
	response := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(input.String()))}
	result, err := svc.handleStreamingResponse(context.Background(), response, c, &Account{ID: 1}, time.Now(), "model", "model", false)
	require.NoError(t, err)
	require.Equal(t, expected.String(), recorder.Body.String())
	require.Equal(t, 3, result.usage.InputTokens)
	require.Equal(t, 7, result.usage.OutputTokens)
}

var forwardingBenchBytes []byte
var forwardingBenchString string

func BenchmarkForwardingBufferedText(b *testing.B) {
	fragment := strings.Repeat("t", 256)
	for _, size := range []int{64 << 10, 256 << 10, 1 << 20} {
		for _, legacy := range []bool{true, false} {
			b.Run(fmt.Sprintf("bytes=%d/legacy=%t", size, legacy), func(b *testing.B) {
				b.ReportAllocs()
				b.SetBytes(int64(size))
				for n := 0; n < b.N; n++ {
					if legacy {
						var text string
						for k := 0; k < size/256; k++ {
							text += fragment
						}
						forwardingBenchString = text
					} else {
						response := &apicompat.AnthropicResponse{Content: []apicompat.AnthropicContentBlock{{Type: "text"}}}
						var a anthropicBufferedContent
						a.reset(response)
						delta := &apicompat.AnthropicDelta{Type: "text_delta", Text: fragment}
						for k := 0; k < size/256; k++ {
							a.applyDelta(0, delta, true)
						}
						a.finalize()
						forwardingBenchString = response.Content[0].Text
					}
				}
			})
		}
	}
}

func BenchmarkForwardingWSComparable(b *testing.B) {
	for _, size := range []int{512, 64 << 10, 1 << 20, 16 << 20} {
		for _, escaped := range []bool{false, true} {
			unit := "t"
			if escaped {
				unit = `\t`
			}
			payload := []byte(`{"model":"m","input":"` + strings.Repeat(unit, size/len(unit)) + `","reasoning":{"effort":"high"}}`)
			for _, legacy := range []bool{true, false} {
				b.Run(fmt.Sprintf("bytes=%d/escaped=%t/legacy=%t", size, escaped, legacy), func(b *testing.B) {
					b.ReportAllocs()
					b.SetBytes(int64(len(payload)))
					for n := 0; n < b.N; n++ {
						var err error
						if legacy {
							forwardingBenchBytes, err = legacyWSComparable(payload)
						} else {
							forwardingBenchBytes, err = normalizeOpenAIWSPayloadWithoutInputAndPreviousResponseID(payload)
						}
						if err != nil {
							b.Fatal(err)
						}
					}
				})
			}
		}
	}
}

func BenchmarkForwardingBufferedToolJSON(b *testing.B) {
	fragment := strings.Repeat("x", 256)
	for _, size := range []int{64 << 10, 256 << 10, 1 << 20} {
		for _, legacy := range []bool{true, false} {
			b.Run(fmt.Sprintf("bytes=%d/legacy=%t", size, legacy), func(b *testing.B) {
				b.ReportAllocs()
				b.SetBytes(int64(size))
				for n := 0; n < b.N; n++ {
					initial := json.RawMessage("{}")
					if legacy {
						value := legacyAppendRawJSON(initial, `{"text":"`)
						for k := 0; k < size/256; k++ {
							value = legacyAppendRawJSON(value, fragment)
						}
						forwardingBenchBytes = legacyAppendRawJSON(value, `"}`)
					} else {
						var value anthropicBufferedBlock
						value.appendInput(initial, `{"text":"`)
						for k := 0; k < size/256; k++ {
							value.appendInput(initial, fragment)
						}
						value.appendInput(initial, `"}`)
						forwardingBenchBytes = value.input
					}
				}
			})
		}
	}
}

func BenchmarkForwardingSSEDeltaView(b *testing.B) {
	raw := []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"` + strings.Repeat("x", 64) + `"}}`)
	for _, legacy := range []bool{true, false} {
		b.Run(fmt.Sprintf("legacy=%t", legacy), func(b *testing.B) {
			b.ReportAllocs()
			for n := 0; n < b.N; n++ {
				if legacy {
					var event map[string]any
					if err := json.Unmarshal(raw, &event); err != nil {
						b.Fatal(err)
					}
				} else {
					if _, _, ok := anthropicSSEDeltaView(raw, ""); !ok {
						b.Fatal("not delta")
					}
				}
			}
		})
	}
}
