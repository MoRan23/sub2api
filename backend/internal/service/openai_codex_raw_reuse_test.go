package service

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/sjson"
)

func TestOpenAICodexRawReuseCanonicalization(t *testing.T) {
	for _, test := range []struct {
		name  string
		body  string
		reuse bool
	}{
		{"native", `{"input":[{"role":"user","content":"native"}],"store":false,"stream":true}`, true},
		{"nested arrays", `{"input":[[],[{"a:b":"text:with:colon","array":[null,true,42]}]]}`, true},
		{"escaped delimiters", `{"text":"colon: quote\" backslash\\ slash\/ tab\t newline\n end"}`, true},
		{"escaped literal unicode", `{"text":"literal \\uD800 and \\\\\" quote"}`, true},
		{"unicode", `{"\u0061":"\u4e2d\u6587","emoji":"\uD83D\uDE00"}`, true},
		{"numbers", `{"negative":-0,"fraction":1.2500,"exponent":1e1000,"integer":900719925474099312345}`, true},
		{"top duplicate", `{"model":"first","model":"last","input":[]}`, false},
		{"nested duplicate", `{"input":[{"role":"user","role":"assistant","content":"text"}]}`, false},
		{"escaped duplicate", `{"a":1,"\u0061":2}`, false},
		{"discarded subtree", `{"a":{"b":{"c":1}},"a":{"d":2}}`, false},
		{"duplicate null", `{"a":null,"a":null}`, false},
		{"high surrogate", `{"text":"\uD800"}`, false},
		{"low surrogate", `{"text":"\uDC00"}`, false},
		{"unpaired surrogate", `{"text":"\uD800\u1234"}`, false},
		{"reversed surrogate", `{"text":"\uDC00\uD800"}`, false},
		{"high followed by literal escape", `{"text":"\uD800\\uDC00"}`, false},
		{"invalid UTF8", "{\"text\":\"\xff\xfe\"}", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := []byte(test.body)
			var decoded map[string]any
			// The production guard has this same prerequisite: malformed JSON is
			// rejected by the existing decoder before the counter can be called.
			require.NoError(t, decodeOpenAIJSONUseNumber(body, &decoded))
			require.Equal(t, test.reuse, canReuseDecodedOpenAICodexBody(body, decoded))
		})
	}
}

func TestOpenAICodexRawReuseManyEscapes(t *testing.T) {
	for _, fragment := range []string{`\n`, `\\`, `\"`, `\u0061`, `\uD83D\uDE00`, `\n\\\"\u0061:`, `plain: text`} {
		body := []byte(`{"text":"` + strings.Repeat(fragment, 65536) + `","nested":{"value":true}}`)
		var decoded map[string]any
		require.NoError(t, decodeOpenAIJSONUseNumber(body, &decoded))
		require.True(t, canReuseDecodedOpenAICodexBody(body, decoded), "fragment %s", fragment)
	}
}

func BenchmarkCodexRawReuseGuard(b *testing.B) {
	for _, size := range []int{1 << 20, 8 << 20} {
		for _, fragment := range []string{"x", `\n`, `\\`, `\"`, `\u0061`} {
			body := []byte(`{"text":"` + strings.Repeat(fragment, size/len(fragment)) + `","nested":{"value":true}}`)
			var decoded map[string]any
			if err := decodeOpenAIJSONUseNumber(body, &decoded); err != nil {
				b.Fatal(err)
			}
			b.Run(fmt.Sprintf("%dMiB/%s", size>>20, fragment), func(b *testing.B) {
				b.ReportAllocs()
				b.SetBytes(int64(len(body)))
				for b.Loop() {
					if !canReuseDecodedOpenAICodexBody(body, decoded) {
						b.Fatal("valid unique object should reuse its bytes")
					}
				}
			})
		}
	}
}

func TestOpenAICodexNoopWirePreservesLegacySemantics(t *testing.T) {
	for _, extension := range []string{
		`"extension":{"text":"literal \\\" and \uD83D\uDE00","n":1e1000}`,
		`"extension":{"duplicate":"first","duplicate":"last"}`,
		`"extension":{"\u0061":1,"a":2}`,
		`"extension":{"text":"\uD800"}`,
		"\"extension\":{\"text\":\"\xff\"}",
	} {
		body := []byte(`{"model":"gpt-6-luna","store":false,"stream":true,"input":[{"type":"message","role":"user","content":"native"}],` + extension + `}`)
		for _, patched := range []bool{false, true} {
			attemptBody := body
			if patched {
				var err error
				// Forward flushes prior raw model patches before ensureReqBody.
				attemptBody, err = sjson.SetBytes(body, "model", "gpt-6-sol")
				require.NoError(t, err)
			}
			var want, got map[string]any
			require.NoError(t, decodeOpenAIJSONUseNumber(attemptBody, &want))
			require.NoError(t, decodeOpenAIJSONUseNumber(attemptBody, &got))
			legacyCPUApplyCodexOAuthTransform(want, codexOAuthTransformOptions{})
			result := applyCodexOAuthTransformWithOptions(got, codexOAuthTransformOptions{})
			wire := attemptBody
			if result.Modified || !canReuseDecodedOpenAICodexBody(attemptBody, got) {
				var err error
				wire, err = marshalOpenAIUpstreamJSON(got)
				require.NoError(t, err)
			}
			var actual map[string]any
			require.NoError(t, decodeOpenAIJSONUseNumber(wire, &actual))
			require.Equal(t, want, actual)
			if !canReuseDecodedOpenAICodexBody(attemptBody, got) {
				canonical, err := marshalOpenAIUpstreamJSON(want)
				require.NoError(t, err)
				require.Equal(t, canonical, wire, "unsafe raw input must keep the legacy canonical wire bytes")
			}
		}
	}
}
