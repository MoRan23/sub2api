package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Keep the old copying input traversal as a differential and benchmark oracle.
func legacyIdentityContextWindowBody(body []byte, plan OpenAIOAuthIdentityPlan) ([]byte, error) {
	projection := openAICodexMetadataProjectionFromPlan(plan)
	if len(body) == 0 || !utf8.Valid(body) || !projection.wireActive || !projection.stableTurn ||
		!projection.wireProfile.RequestKind.hasTurnIdentity() || projection.wireProfile.ContextWindowID == "" ||
		plan.ProjectionMode == OpenAIOAuthIdentityProjectionHeadersOnly ||
		ValidateOpenAICodexWindowSnapshot(plan.Window) != nil || !gjson.ValidBytes(body) {
		return body, nil
	}
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return body, nil
	}
	out := body
	splice := func(path string, value gjson.Result) error {
		if value.Type != gjson.String {
			return nil
		}
		updated, changed := rewriteOpenAICodexContextWindowString(value.String(), plan.Window)
		if !changed {
			return nil
		}
		var err error
		out, err = sjson.SetBytes(out, path, updated)
		return err
	}
	for i, item := range input.Array() {
		typ := item.Get("type")
		if !item.IsObject() || item.Get("role").String() != "developer" ||
			(typ.Exists() && (typ.Type != gjson.String || typ.String() != "message")) {
			continue
		}
		content := item.Get("content")
		path := "input." + strconv.Itoa(i) + ".content"
		if content.Type == gjson.String {
			if err := splice(path, content); err != nil {
				return body, fmt.Errorf("splice OpenAI Codex context window text: %w", err)
			}
		} else if content.IsArray() {
			for j, part := range content.Array() {
				if !part.IsObject() || (part.Get("type").String() != "input_text" && part.Get("type").String() != "text") {
					continue
				}
				if err := splice(path+"."+strconv.Itoa(j)+".text", part.Get("text")); err != nil {
					return body, fmt.Errorf("splice OpenAI Codex context window text: %w", err)
				}
			}
		}
	}
	return out, nil
}

func TestOpenAIIdentityBodyViewMatchesLegacy(t *testing.T) {
	plan := contextBodyFinalPlan(t)
	text, err := json.Marshal(contextBodyBlock(contextBodyClientFirst, contextBodyClientCurrent, ""))
	require.NoError(t, err)
	fixtures := []string{
		"", "null", "[]", `{"input":null}`, `{"input":[]}`, `{"input":1} trailing`,
		`{"input":[null,4,"text",{"role":"developer","content":` + string(text) + `}]}`,
		` { "input" : [{"content":` + string(text) + `,"role":"developer","type":"message"}], "extension":1e1000 } `,
		`{"input":[{"role":"user","role":"developer","content":` + string(text) + `}]}`,
		`{"input":[{"role":"developer","role":"user","content":` + string(text) + `}]}`,
		`{"input":[{"role":"developer","type":null,"content":` + string(text) + `}]}`,
		`{"input":[{"role":"developer","type":"message","type":"other","content":` + string(text) + `}]}`,
		`{"input":[{"role":"developer","content":[null,{},false,{"type":"text","text":` + string(text) + `},{"type":"input_text","text":` + string(text) + `}]}]}`,
		`{"input":[{"role":"developer","content":[{"type":"input_image","text":` + string(text) + `},{"type":"input_text","type":"other","text":` + string(text) + `}]}]}`,
		`{"input":[],"input":[{"role":"developer","content":` + string(text) + `}]}`,
		`{"inpu\u0074":[{"ro\u006ce":"developer","content":` + string(text) + `}]}`,
		`{"input":[{"role":"developer","content":"` + string([]byte{0xff}) + `"}]}`,
	}
	for i, fixture := range fixtures {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			body := []byte(fixture)
			original := bytes.Clone(body)
			want, wantErr := legacyIdentityContextWindowBody(body, plan)
			got, gotErr := applyOpenAICodexContextWindowBody(body, plan)
			require.Equal(t, wantErr, gotErr)
			require.Equal(t, want, got)
			require.Equal(t, original, body)
		})
	}
}

func TestOpenAIIdentityBodyViewOwnership(t *testing.T) {
	plan := contextBodyFinalPlan(t)
	text, err := json.Marshal(contextBodyBlock(contextBodyClientFirst, contextBodyClientCurrent, ""))
	require.NoError(t, err)
	body := []byte(`{"input":[{"role":"user","content":"` + strings.Repeat("x", 1<<20) + `"},{"role":"developer","content":` + string(text) + `}]}`)
	original := bytes.Clone(body)
	updated, err := applyOpenAICodexContextWindowBody(body, plan)
	require.NoError(t, err)
	require.NotEqual(t, body, updated)
	require.Equal(t, original, body)
	want := bytes.Clone(updated)
	for i := range body {
		body[i] = 'x'
	}
	require.Equal(t, want, updated, "changed output owns its bytes")
	unchanged := []byte(`{"input":[{"role":"user","content":"hello"}]}`)
	view, err := applyOpenAICodexContextWindowBody(unchanged, plan)
	require.NoError(t, err)
	require.True(t, &view[0] == &unchanged[0], "unchanged output remains borrowed")
}

func TestOpenAIIdentityBodyViewShapeCompatibility(t *testing.T) {
	for _, body := range [][]byte{nil, []byte(`null`), []byte(`[]`), []byte(`{}`), []byte(` {"input":[]}`), []byte(`{"input":`), []byte(`{"x":"` + string([]byte{0xff}) + `"}`)} {
		require.Equal(t, gjson.ParseBytes(body).IsObject(), parseRawJSONView(body).IsObject())
		// A disabled passthrough identity never owns or changes the input.
		got, err := mergeOpenAIOAuthPassthroughIdentityBody(body, OpenAIOAuthIdentityPlan{})
		require.NoError(t, err)
		require.Equal(t, body, got)
	}
	plan := OpenAIOAuthIdentityPlan{ProjectionMode: OpenAIOAuthIdentityProjectionRegular,
		PromptCacheKey: OpenAICodexPromptCacheKeyPlan{Action: OpenAICodexPromptCacheKeySet, Value: "owned-key"}}
	body := []byte(` {"input":"opaque","extension":1e+07} `)
	original := bytes.Clone(body)
	got, err := applyOpenAICodexPromptCacheKeyBody(body, plan)
	require.NoError(t, err)
	require.Equal(t, original, body)
	want := bytes.Clone(got)
	for i := range body {
		body[i] = 'x'
	}
	require.Equal(t, want, got)
	require.Equal(t, "owned-key", gjson.GetBytes(got, "prompt_cache_key").String())
}

func identityBodyViewBenchmarkPlan(b *testing.B) OpenAIOAuthIdentityPlan {
	profile := ParseCodexWireProfile(`{"request_kind":"turn"}`)
	plan := OpenAIOAuthIdentityPlan{
		WireProfile:         profile,
		TurnIdentity:        OpenAICodexTurnIdentity{SessionID: codexWireTestSession, ThreadID: codexWireTestThread},
		Window:              OpenAICodexWindowSnapshot{ThreadID: codexWireTestThread, ContextWindowID: codexWireTestContextWindow},
		TurnIdentityEnabled: true, TurnIdentityRequested: true, ProjectionMode: OpenAIOAuthIdentityProjectionRegular,
	}
	final, err := FinalizeOpenAICodexWirePlan(plan, string(CodexWireRequestTurn), CodexModelCapabilities{})
	require.NoError(b, err)
	require.True(b, openAICodexMetadataProjectionFromPlan(final).stableTurn)
	return final
}

func BenchmarkOpenAIIdentityBodyViews(b *testing.B) {
	plan := identityBodyViewBenchmarkPlan(b)
	for _, size := range []int{1 << 20, 8 << 20, 32 << 20} {
		body := []byte(`{"input":[{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,` + strings.Repeat("A", size) + `"}]}]}`)
		for _, mode := range []struct {
			name string
			read func([]byte, OpenAIOAuthIdentityPlan) ([]byte, error)
		}{{"legacy", legacyIdentityContextWindowBody}, {"view", applyOpenAICodexContextWindowBody}} {
			b.Run(fmt.Sprintf("%dMiB/context/%s", size>>20, mode.name), func(b *testing.B) {
				b.ReportAllocs()
				b.SetBytes(int64(len(body)))
				for b.Loop() {
					got, err := mode.read(body, plan)
					if err != nil || len(got) != len(body) {
						b.Fatal("context projection changed user image", err)
					}
				}
			})
		}
		for _, legacy := range []bool{true, false} {
			b.Run(fmt.Sprintf("%dMiB/object_shape/legacy=%t", size>>20, legacy), func(b *testing.B) {
				b.ReportAllocs()
				b.SetBytes(int64(len(body)))
				for b.Loop() {
					var object bool
					if legacy {
						object = gjson.ParseBytes(body).IsObject()
					} else {
						object = parseRawJSONView(body).IsObject()
					}
					if !object {
						b.Fatal("lost object shape")
					}
				}
			})
		}
	}
}
