package service

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestCodexEngineImageViewPreservesGJSONLookup(t *testing.T) {
	for _, raw := range []string{
		`{}`,
		`[]`,
		`null`,
		`{"data":[],"output":[],"response":{"output":[]},"type":"response.done","item":{}}`,
		`{"d\u0061ta":[1],"out\u0070ut":[2],"re\u0073ponse":{"out\u0070ut":[3]},"ty\u0070e":"response.done","it\u0065m":{}}`,
		`{"data":null,"data":[1],"output":null,"output":[2],"type":null,"type":"response.done","item":null,"item":{}}`,
		`{"data":[1],"d\u0061ta":[2],"output":[3],"out\u0070ut":[4],"type":"first","type":"second","item":{"id":1},"item":{"id":2}}`,
		`{"response":null,"response":{"output":[1]}}`,
		`{"response":{},"response":{"output":[1]}}`,
		`{"response":{"output":null},"response":{"output":[1]}}`,
		`{"response":{"output":[1],"output":[2]},"response":{"output":[3]}}`,
		`{"response":false,"response":"not an object","response":[],"response":{"output":[1]}}`,
		`{"re\u0073ponse":{"missing":true},"response":{"out\u0070ut":null,"output":[1]}}`,
		`{"response":{"output":[1]},"response":{"output":null}}`,
		`{"response":{"missing":true}}`,
	} {
		t.Run(raw, func(t *testing.T) {
			require.True(t, gjson.Valid(raw))
			root := gjson.Parse(raw)
			view := newCodexEngineImageView(root)
			for path, got := range map[string]gjson.Result{
				"data": view.data, "output": view.output, "response.output": view.responseOutput,
				"type": view.eventType, "item": view.item,
			} {
				want := root.Get(path)
				require.Equal(t, want.Exists(), got.Exists(), path)
				if want.Exists() {
					require.Equal(t, want, got, path)
				}
			}
		})
	}
}

func TestCodexEngineImageViewObserverMatchesOriginal(t *testing.T) {
	fixtures := []struct {
		name   string
		frames []string
	}{
		{"JSON data and multiple sizes", []string{
			`{"data":[{"url":"fixture://one","size":"1024x1024"},{"b64_json":"BBBB","size":"512x512"}]}`,
		}},
		{"JSON output and partial images", []string{
			`{"output":[{"type":"image_generation_call","id":"one","result":"AAAA","size":"1024x1024"},{"type":"image_generation_call","id":"two","result":"BBBB","size":"512x512"},{"type":"image_generation_call","id":"partial","partial_image":true,"result":"CCCC"}]}`,
		}},
		{"SSE item then terminal deduplication", []string{
			`{"type":"response.output_item.done","item":{"type":"image_generation_call","id":"one","result":"AAAA"}}`,
			`{"type":"response.completed","response":{"output":[{"type":"image_generation_call","id":"one","result":"AAAA","size":"1024x1024"},{"type":"image_generation_call","id":"two","result":"BBBB","size":"512x512"}]}}`,
			`{"type":"response.done","response":{"output":[{"type":"image_generation_call","id":"two","result":"BBBB"}]}}`,
		}},
		{"escaped JSON and SSE keys", []string{
			`{"d\u0061ta":[{"url":"fixture://escaped","size":"512x512"}]}`,
			`{"ty\u0070e":"response.output_item.done","it\u0065m":{"type":"image_generation_call","id":"one","result":"AAAA"}}`,
			`{"type":"response.completed","re\u0073ponse":{"out\u0070ut":[{"type":"image_generation_call","id":"one","result":"AAAA","size":"1024x1024"}]}}`,
		}},
		{"first duplicate direct fields include null", []string{
			`{"data":null,"data":[{"url":"fixture://ignored"}],"output":null,"output":[{"result":"AAAA"}]}`,
			`{"type":null,"type":"image_generation.completed","item":null,"item":{"result":"AAAA"},"b64_json":null}`,
			`{"type":"image_generation.completed","item":null,"item":{"result":"AAAA"},"output":{"result":"BBBB"}}`,
		}},
		{"duplicate parent finds first existing leaf", []string{
			`{"type":"response.completed","response":null,"response":{"output":[{"id":"one","result":"AAAA"}]}}`,
			`{"type":"response.completed","response":{},"response":{"output":[{"id":"two","result":"BBBB"}]}}`,
			`{"type":"response.completed","response":{"output":null},"response":{"output":[{"id":"ignored","result":"CCCC"}]}}`,
		}},
		{"image completion fallback branches", []string{
			`{"type":"image_generation.completed","item":{"result":"AAAA","id":"one","size":"1024x1024"}}`,
			`{"type":"image_edit.completed","output":{"result":"BBBB","id":"two","size":"512x512"}}`,
			`{"type":"image_generation.completed","b64_json":"CCCC","id":"three","size":"256x256"}`,
			`{"type":"image_edit.completed","url":null}`,
		}},
		{"usage fallbacks and invalid frames", []string{
			`{"usage":{"images":5}}`,
			`{"tool_usage":{"image_gen":{"images":6}}}`,
			`{"data":[{"url":"fixture://one"}]}`,
			`{"data":[{"url":"fixture://invalid"}]} trailing`,
			`{"output":`, `[DONE]`, ``,
		}},
	}
	for _, fixture := range fixtures {
		for _, streaming := range []bool{false, true} {
			t.Run(fixture.name+"/stream="+map[bool]string{false: "false", true: "true"}[streaming], func(t *testing.T) {
				want, got := &OpenAIForwardResult{}, &OpenAIForwardResult{}
				var wantStream, gotStream *openAIImageOutputCounter
				if streaming {
					wantStream, gotStream = newOpenAIImageOutputCounter(), newOpenAIImageOutputCounter()
				}
				for _, frame := range fixture.frames {
					wantParsed := originalCodexEngineImageObservation(want, []byte(frame), wantStream)
					gotParsed := observeCodexEngineImages(got, []byte(frame), gotStream)
					require.Equal(t, want, got, frame)
					require.Equal(t, wantStream, gotStream, frame)
					require.Equal(t, wantParsed, gotParsed, frame)
				}
			})
		}
	}
}

func TestCodexEngineImageViewKeepsRetainedMetadataOwned(t *testing.T) {
	result := &OpenAIForwardResult{}
	stream := newOpenAIImageOutputCounter()
	payload := []byte(`{"type":"response.completed","response":{"output":[{"type":"image_generation_call","id":"owned-image-id","size":"1024x1024","result":"` + strings.Repeat("A", 1<<20) + `"}]}}`)
	parsed := observeCodexEngineImages(result, payload, stream)
	require.NotNil(t, parsed)
	require.Equal(t, 1, result.ImageCount)
	for _, value := range append(stream.Sizes(), stream.seenOrder...) {
		require.False(t, codexEngineStringWithinBacking(value, parsed.Raw))
	}
	for _, value := range result.ImageOutputSizes {
		require.False(t, codexEngineStringWithinBacking(value, parsed.Raw))
	}
	clear(payload)
	observeCodexEngineImages(result, []byte(`{"type":"response.output_text.delta","delta":"next frame"}`), stream)
	require.Equal(t, []string{"owned-image-id"}, stream.seenOrder)
	require.Equal(t, []string{"1024x1024"}, result.ImageOutputSizes)
}

// Frozen pre-index implementation: differential tests retain its direct GJSON
// path lookups, including duplicate-container and explicit-null behavior.
func originalCodexEngineImageObservation(result *OpenAIForwardResult, data []byte, stream *openAIImageOutputCounter) (parsed *gjson.Result) {
	if len(data) > 0 && codexEngineMayHaveImages(data) && gjson.ValidBytes(data) {
		root := gjson.ParseBytes(data)
		parsed = &root
		current := newOpenAIImageOutputCounter()
		current.ownStrings = true
		current.addDataArray(root.Get("data"))
		current.addOutputArray(root.Get("output"))
		current.addOutputArray(root.Get("response.output"))
		if sizes := current.Sizes(); len(sizes) > 0 {
			result.ImageOutputSizes = sizes
		}
		count := current.Count()
		if count == 0 {
			count = int(root.Get("usage.images").Int())
			if count <= 0 {
				count = int(root.Get("tool_usage.image_gen.images").Int())
			}
			if count <= 0 && strings.HasSuffix(strings.TrimSpace(root.Get("type").String()), ".completed") && (root.Get("b64_json").Exists() || root.Get("url").Exists()) {
				count = 1
			}
		}
		result.ImageCount = max(result.ImageCount, count)
		if stream != nil {
			stream.ownStrings = true
			stream.addDataArray(root.Get("data"))
			switch strings.TrimSpace(root.Get("type").String()) {
			case "response.output_item.done":
				stream.addImageOutputItem(root.Get("item"))
			case "response.completed", "response.done":
				stream.addOutputArray(root.Get("response.output"))
			case "image_generation.completed", "image_edit.completed":
				if item := root.Get("item"); item.Exists() {
					stream.addImageOutputItem(item)
				} else if output := root.Get("output"); output.Exists() {
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
