package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func legacyCPURequestBodyMayContainEmptyBase64InputImage(body []byte) bool {
	if len(body) == 0 || !openAIRequestBodyMayContainInputImageToken(body) {
		return false
	}
	input := gjson.GetBytes(body, "input")
	if !input.Exists() {
		return false
	}
	return openAIJSONValueMayContainEmptyBase64InputImage(input)
}

func TestOpenAIEmptyBase64InputImageCPUParity(t *testing.T) {
	bodies := []string{
		``, `null`, `[]`, `{}`, `{"input":"input_image"}`,
		`{"input":[{"type":"input_image","image_url":"data:image/png;base64,"}]}`,
		`{"input":[{"type":"input_image","image_url":"data:image/png;base64,YWJj"}]}`,
		`{"input":[{"type":"input_image","image_url":"data:image/png;BASE64,"}]}`,
		`{"input":[{"type":"input_image","image_url":"data:image/png;base64, "}]}`,
		`{"input":[{"type":"input_image","image_url":"data:image/png;base64,\t\n\r"}]}`,
		`{"input":[{"type":"input_image","image_url":"data:image/png;base64,\u2000"}]}`,
		`{"input":[{"type":"input_image","image_url":"data:image/png;base64,　"}]}`,
		`{"input":[{"type":"input_image","image_url":"data:image/png;base64,\u0041"}]}`,
		`{"input":[{"type":"input_image","image_url":"data:image/png;base64\u002c"}]}`,
		`{"inp\u0075t":[{"type":"input_\u0069mage","image_\u0075rl":"d\u0061ta:image/png\u003bbase64,"}]}`,
		`{"input":[{"type":"input_image","image_url":"data:image/png;base64,\\"}]}`,
		`{"input":[{"type":"input_image","image_url":"data:image/png;base64,\""}]}`,
		`{"input":[{"type":"input_image","image_url":"data:image/png;base64,\/"}]}`,
		`{"input":[{"content":[{"type":"input_image","image_url":"data:image/png;base64,"}]}]}`,
		`{"input":[{"type":"input_image","image_url":"data:image/png;base64,A"}],"input":[{"type":"input_image","image_url":"data:image/png;base64,"}]}`,
		`{"input":[{"type":"input_image","type":"input_text","image_url":"data:image/png;base64,"}]}`,
		`{"input":[{"type":"input_image","image_url":null,"image_url":"data:image/png;base64,"}]}`,
		`{"input":[{"type":"input_image","content":null,"content":[{"type":"input_image","image_url":"data:image/png;base64,"}]}]}`,
		`{"input":[{"type":"input_image","image_url":"data:image/png;base64,`,
		`{"input":[{"type":"input_image","image_url":"data:image/png;base64,}]}`,
		`garbage {"input":[{"type":"input_image","image_url":"data:image/png;base64,"}]}`,
	}
	// The fast filter is conservative even for incomplete input, preserving the
	// previous permissive gjson predicate ahead of the strict mutation path.
	for caseIndex, raw := range bodies {
		for end := 0; end <= len(raw); end++ {
			body := []byte(raw[:end])
			want := legacyCPURequestBodyMayContainEmptyBase64InputImage(body)
			if got := openAIRequestBodyMayContainEmptyBase64InputImage(body); got != want {
				t.Fatalf("case %d prefix %d: got %v, want %v, body=%q", caseIndex, end, got, want, body)
			}
		}
	}
}

func TestOpenAIEmptyBase64InputImageNonemptyImageNoAllocation(t *testing.T) {
	body := buildRequestPredicatesCPUBody(64<<10, "image", false)
	allocs := testing.AllocsPerRun(10, func() {
		if openAIRequestBodyMayContainEmptyBase64InputImage(body) {
			t.Fatal("nonempty image reported empty")
		}
	})
	if allocs != 0 {
		t.Fatalf("nonempty image predicate allocated %g times", allocs)
	}
}

func TestOpenAIResponsesToolSchemasCPUParity(t *testing.T) {
	values := []string{
		`"text"`, `"a\\\"b"`, `"\\\\"`, `"\u0022\u005c"`, `"\uD800"`, `"你好"`,
		`null`, `1e1000`, `{"nested":[{"parameters":{"type":null}}]}`,
		`"unterminated`, `"bad\q"`, `"bad\u123"`, `"` + string([]byte{0xff}) + `"`,
		`"literal` + "\x00" + `control"`,
	}
	types := []string{`"message"`, `"input_image"`, `"function_call_output"`, `"function"`, `"custom"`, `"additional_tools"`, `null`}
	bodies := [][]byte{
		nil, []byte(`null`), []byte(`[]`), []byte(`{}`),
		[]byte(`{"to\u006fls":[{"par\u0061meters":{"type":null,"required":null,"pat\u0074ern":"(?=drop)"}}]}`),
		[]byte(`{"input":[{"type":"message","type":"function","parameters":{"type":null,"pattern":"(?=x)"}}]}`),
		[]byte(`{"input":[{"type":"function","type":null,"parameters":{"type":null,"pattern":"(?=x)"}}]}`),
		[]byte(`{"input":[{"type":"function","type":"message","parameters":{"type":null,"pattern":"(?=x)"}}]}`),
		[]byte(`{"tools":[{"parameters":{"type":null,"type":"object","pattern":"(?=a)","pattern":"x"}}]}`),
		[]byte(`{"tools":[{"parameters":{"oneOf":[{"type":"object"},{"type":"object"}],"required":null}}]}`),
		[]byte(`{"tools":[{"function":{"parameters":{"type":null,"pattern":"(?=x)"}}}],"input":"ok"} trailing`),
	}
	for _, value := range values {
		for _, inputType := range types {
			bodies = append(bodies, []byte(`{"tools":[{"parameters":{"type":null,"pattern":"(?=remove)"}}],"input":[{"type":`+inputType+`,"content":`+value+`,"parameters":{"type":null,"required":null,"pattern":"(?!drop)"},"tools":[{"parameters":{"type":null}}]}]}`))
		}
	}
	for _, depth := range []int{125, 127, 128, 129, 132} {
		bodies = append(bodies, []byte(`{"input":[{"type":"message","content":`+strings.Repeat(`[`, depth)+`"image"`+strings.Repeat(`]`, depth)+`}],"tools":[{"parameters":{"type":null}}]}`))
	}
	for caseIndex, body := range bodies {
		for _, platform := range []string{PlatformOpenAI, PlatformGrok, PlatformAnthropic} {
			want, wantChanged, wantErr := legacyCPUSchemaSanitizeOpenAIResponsesToolSchemasForPlatform(body, platform)
			got, gotChanged, gotErr := sanitizeOpenAIResponsesToolSchemasForPlatform(body, platform)
			if !bytes.Equal(got, want) || gotChanged != wantChanged || fmt.Sprint(gotErr) != fmt.Sprint(wantErr) {
				t.Fatalf("case %d platform %s mismatch\ngot=%q changed=%v err=%v\nwant=%q changed=%v err=%v", caseIndex, platform, got, gotChanged, gotErr, want, wantChanged, wantErr)
			}
		}
	}
}

func TestOpenAIResponsesToolSchemaValidatedStringCPUParity(t *testing.T) {
	for _, text := range []string{
		"", "plain", `\`, `"`, `\"`, `\\"`, `\\\"`, "\x00\t\n\r", "😀你好", strings.Repeat(`\\\"hello`, 128),
	} {
		body, err := json.Marshal(text)
		if err != nil {
			t.Fatal(err)
		}
		current := openAIResponsesToolSchemaParser{body: body, validated: true}
		legacy := legacyCPUSchemaOpenAIResponsesToolSchemaParser{body: body}
		gotStart, gotEnd, gotErr := current.parseString()
		wantStart, wantEnd, wantErr := legacy.parseString()
		if gotStart != wantStart || gotEnd != wantEnd || fmt.Sprint(gotErr) != fmt.Sprint(wantErr) {
			t.Fatalf("string %q: current=(%d,%d,%v), legacy=(%d,%d,%v)", body, gotStart, gotEnd, gotErr, wantStart, wantEnd, wantErr)
		}
	}
}

func TestOpenAIResponsesToolSchemaValidationCPUParity(t *testing.T) {
	// Match the pre-merge validation contract, including the permissive JSON
	// treatment of duplicate members and isolated surrogate escapes.
	bodies := []string{
		``, `null`, `true`, `false`, `0`, `-0.00`, `1e100000000`, `-1e-999999`, `01`, `1.`, `1e`, `+1`,
		`[]`, `{}`, `"text"`, `"\uD800"`, `"\uDC00"`, `"\uD800\uDC00"`, `"\u0000"`,
		`"\u123"`, `"\uZZZZ"`, `"bad\q"`, `"unterminated`, "\"bad\x00control\"",
		`{"a":1,"a":2}`, `{"a":1,"\u0061":2}`, `{"a":1} null`, `{"a":true,}`, `[1,]`,
		`{"tools":[{"parameters":{"type":null,"type":"object"}}]}`,
		`{"to\u006fls":[{"parameters":{"one\u004ff":[{"type":"object"}]}}]}`,
		`{"input":"` + string([]byte{0xff}) + `"}`,
		`{"input":"你好😀"}`, `{"input":"` + strings.Repeat("A", 1<<20) + `"}`,
	}
	for _, depth := range []int{127, 128, 129, 132, 10001} {
		bodies = append(bodies, strings.Repeat(`[`, depth)+`0`+strings.Repeat(`]`, depth))
	}
	for i, raw := range bodies {
		body := []byte(raw)
		want, wantChanged, wantErr := legacyCPUSchemaSanitizeOpenAIResponsesToolParameterTypes(body)
		got, gotChanged, gotErr := sanitizeOpenAIResponsesToolParameterTypes(body)
		if !bytes.Equal(got, want) || gotChanged != wantChanged || fmt.Sprint(gotErr) != fmt.Sprint(wantErr) {
			t.Fatalf("case %d: validation changed: got changed=%v err=%v, want changed=%v err=%v", i, gotChanged, gotErr, wantChanged, wantErr)
		}
	}
}

func buildRequestPredicatesCPUBody(size int, kind string, repair bool) []byte {
	prefix := `{"model":"gpt-6-sol","input":[{"type":"message","content":[{"type":"input_image","image_url":"data:image/png;base64,`
	if kind == "text" {
		prefix = `{"model":"gpt-6-sol","input":[{"type":"message","content":[{"type":"input_text","text":"input_image example (text) `
	}
	parameters := `{"type":"object","properties":{"path":{"type":"string"}}}`
	if repair {
		parameters = `{"type":null,"required":null,"properties":{"path":{"type":"string","pattern":"(?=x)"}}}`
	}
	return []byte(prefix + strings.Repeat("A", size) + `"}]}],"tools":[{"type":"function","name":"read","parameters":` + parameters + `}]}`)
}

var requestPredicatesCPUBoolSink bool
var requestPredicatesCPUBytesSink []byte

func BenchmarkOpenAIEmptyBase64PredicateCPU(b *testing.B) {
	for _, size := range []int{1 << 20, 8 << 20, 32 << 20} {
		for _, kind := range []string{"image", "text"} {
			body := buildRequestPredicatesCPUBody(size, kind, false)
			for _, legacy := range []bool{true, false} {
				name := "current"
				predicate := openAIRequestBodyMayContainEmptyBase64InputImage
				if legacy {
					name, predicate = "legacy", legacyCPURequestBodyMayContainEmptyBase64InputImage
				}
				b.Run(fmt.Sprintf("%s/%dMiB/%s", kind, size>>20, name), func(b *testing.B) {
					b.SetBytes(int64(len(body)))
					b.ReportAllocs()
					for i := 0; i < b.N; i++ {
						requestPredicatesCPUBoolSink = predicate(body)
					}
				})
			}
		}
	}
}

func BenchmarkOpenAIResponsesToolSchemasCPU(b *testing.B) {
	for _, size := range []int{1 << 20, 8 << 20, 32 << 20} {
		for _, kind := range []string{"image", "text"} {
			for _, repair := range []bool{false, true} {
				body := buildRequestPredicatesCPUBody(size, kind, repair)
				for _, legacy := range []bool{true, false} {
					name := "current"
					sanitize := sanitizeOpenAIResponsesToolSchemasForPlatform
					if legacy {
						name, sanitize = "legacy", legacyCPUSchemaSanitizeOpenAIResponsesToolSchemasForPlatform
					}
					b.Run(fmt.Sprintf("%s/%dMiB/repair=%v/%s", kind, size>>20, repair, name), func(b *testing.B) {
						b.SetBytes(int64(len(body)))
						b.ReportAllocs()
						for i := 0; i < b.N; i++ {
							var err error
							requestPredicatesCPUBytesSink, _, err = sanitize(body, PlatformOpenAI)
							if err != nil {
								b.Fatal(err)
							}
						}
					})
				}
			}
		}
	}
}
