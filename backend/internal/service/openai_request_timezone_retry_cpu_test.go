package service

import (
	"bytes"
	"fmt"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Preserve the previous validation and application as a differential oracle.
func legacyTimezoneRetryMatchesBody(s *RequestTimezoneState, body []byte) bool {
	if !gjson.ValidBytes(body) {
		return false
	}
	for _, patch := range s.patches {
		if patch.containerPath != "" {
			container := gjson.GetBytes(body, patch.containerPath)
			if patch.toolType != "" {
				if !gjson.GetBytes(body, "tools").IsArray() || !container.IsObject() || container.Get("type").String() != patch.toolType {
					return false
				}
			} else if container.Exists() && !container.IsObject() {
				return false
			}
		}
		value := gjson.GetBytes(body, patch.path)
		if !value.Exists() && !patch.originalExists {
			continue
		}
		canonical, ok := canonicalRequestTimezonePatch(value)
		if !ok || (canonical != patch.original && canonical != patch.prepared) {
			return false
		}
	}
	return true
}

func legacyTimezoneRetryApply(s *RequestTimezoneState, body []byte) ([]byte, bool) {
	if s == nil || len(s.patches) == 0 {
		return body, true
	}
	if !legacyTimezoneRetryMatchesBody(s, body) {
		return body, false
	}
	out := body
	for _, patch := range s.patches {
		if gjson.GetBytes(out, patch.path).Raw == patch.prepared {
			continue
		}
		var err error
		out, err = sjson.SetRawBytes(out, patch.path, []byte(patch.prepared))
		if err != nil {
			return body, false
		}
	}
	return out, true
}

func legacyTimezoneRetryUndo(s *RequestTimezoneState, body []byte) ([]byte, bool) {
	if s == nil || len(s.patches) == 0 {
		return body, true
	}
	if !legacyTimezoneRetryMatchesBody(s, body) {
		return body, false
	}
	out := body
	for _, patch := range s.patches {
		var err error
		if patch.originalExists {
			if gjson.GetBytes(out, patch.path).Raw != patch.original {
				out, err = sjson.SetRawBytes(out, patch.path, []byte(patch.original))
			}
		} else {
			out, err = sjson.DeleteBytes(out, patch.path)
		}
		if err != nil {
			return body, false
		}
		if !patch.containerExists && patch.containerPath != "" {
			container := gjson.GetBytes(out, patch.containerPath)
			if container.IsObject() && len(container.Map()) == 0 {
				out, err = sjson.DeleteBytes(out, patch.containerPath)
				if err != nil {
					return body, false
				}
			}
		}
	}
	return out, true
}

func legacyTimezoneRetryWithTarget(s *RequestTimezoneState, target RequestLocationObservation) *RequestTimezoneState {
	result := cloneRequestTimezoneStateMetadata(s)
	result.Target = target
	result.buildTargetProjection()
	if target != s.Target && len(s.preparedBody) != 0 {
		if neutral, ok := legacyTimezoneRetryUndo(s, s.preparedBody); ok {
			if prepared, valid := legacyTimezoneRetryApply(result, neutral); valid {
				result.preparedBody = prepared
			}
		}
	}
	return result
}

func timezoneRetryCPUFixture(size int) []byte {
	return []byte(fmt.Sprintf(`{"model":"original","input":[{"role":"user","content":[{"type":"input_text","text":%q}],"internal_chat_message_metadata_passthrough":{"content_item_kinds":["environments.environment_context"]}},{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,%s"}]}],"tools":[{"type":"web_search","user_location":{"timezone":"UTC","city":"London"}}]}`, timezoneTestEnvironment("UTC", "2020-01-01"), strings.Repeat("x", size)))
}

func TestRequestTimezoneRetryMetadataProjectionMatchesPreparedPath(t *testing.T) {
	body := timezoneRetryCPUFixture(4096)
	require.True(t, gjson.ValidBytes(body), "timezone retry fixture must be valid JSON")
	original := bytes.Clone(body)
	policy, accepted := timezoneTestPolicy(), timezoneTestAcceptedAt()
	for _, passthrough := range []bool{false, true} {
		for _, observe := range []bool{false, true} {
			_, legacy := prepareOpenAIRequestTimezoneBody(body, policy, accepted, passthrough, observe, false)
			baseline := prepareOpenAIRequestTimezoneSources(body, policy, accepted, passthrough, observe, false, nil)
			require.NotEmpty(t, baseline.Conversions, "fixture has no sources: scan=%s passthrough=%t observe=%t", baseline.projectionScanStatus, passthrough, observe)
			for _, target := range []RequestLocationObservation{
				openAIRequestSearchLocation(),
				{Type: "approximate", Country: "JP", Region: "Tokyo", City: "Tokyo", Timezone: "Asia/Tokyo"},
				{Type: "approximate", Country: "GB", Region: "England", City: "London", Timezone: "Europe/London"},
			} {
				for attempt := 0; attempt < 3; attempt++ {
					attemptBody, err := sjson.SetBytes(body, "model", fmt.Sprintf("account-%d", attempt))
					require.NoError(t, err)
					oldState := legacyTimezoneRetryWithTarget(legacy, target)
					want, wantOK := legacyTimezoneRetryApply(oldState, attemptBody)
					state := baseline.withTargetMetadata(target)
					got, gotOK := state.ApplyToBody(attemptBody)
					require.Equal(t, wantOK, gotOK)
					require.Equal(t, want, got)
					require.Equal(t, oldState.Conversions, state.Conversions)
					require.Equal(t, oldState.Inbound, state.Inbound)
					require.Equal(t, original, body)
					require.Same(t, &body[0], &baseline.preparedBody[0], "frozen sources must not materialize an unused target body")
					if !bytes.Equal(got, attemptBody) {
						require.NotSame(t, &attemptBody[0], &got[0])
					}
					state.Conversions[0].Output = "attempt-only"
					require.NotEqual(t, "attempt-only", baseline.Conversions[0].Output)
				}
			}
		}
	}
}

func TestRequestTimezoneRetryPatchValidationMatchesLegacy(t *testing.T) {
	state := &RequestTimezoneState{patches: []requestTimezoneBodyPatch{{
		path: "tools.0.user_location", original: `{"city":"London","timezone":"UTC"}`,
		prepared: `{"city":"Tokyo","timezone":"Asia/Tokyo"}`, originalExists: true,
		containerPath: "tools.0", toolType: "web_search", containerExists: true,
	}}}
	for _, body := range []string{
		`{"tools":[{"type":"web_search","user_location":{"city":"London","timezone":"UTC"}}]}`,
		`{"tools":[{"type":"web_search","user_location": {"timezone":"UTC", "city":"Lon\u0064on"}}]}`,
		`{"tools":[{"type":"web_search","user_location":{"city":"Tokyo","timezone":"Asia/Tokyo"}}]}`,
		`{"tools":[{"type":"web_search","user_location":{"city":"London","timezone":"Europe/London"}}]}`,
		`{"tools":[{"type":"web_search_preview","user_location":{"city":"London","timezone":"UTC"}}]}`,
		`{"tools":[{"type":"web_search","user_location":null}]}`,
		`{"tools":{"0":{"type":"web_search","user_location":{"city":"London","timezone":"UTC"}}}}`,
		`{"tools":[{"type":"web_search"}]}`,
		`{"tools":[]`,
	} {
		input := []byte(body)
		want, wantOK := legacyTimezoneRetryApply(state, input)
		got, gotOK := state.ApplyToBody(input)
		require.Equal(t, wantOK, gotOK, body)
		require.Equal(t, want, got, body)
	}
	oversized := `"` + strings.Repeat("x", openAIRequestTimezoneTextLimit) + `"`
	state = &RequestTimezoneState{patches: []requestTimezoneBodyPatch{{path: "input", original: oversized, prepared: oversized, originalExists: true}}}
	body := []byte(`{"input":` + oversized + `}`)
	require.False(t, state.matchesBody(body), "raw equality must retain the canonicalization size limit")
}

func TestRequestTimezoneCombinedFieldsMatchIndependentLookups(t *testing.T) {
	for _, raw := range []string{
		`null`, `[]`, `"text"`, `{}`,
		`{"content":[],"role":"user","internal_chat_message_metadata_passthrough":{"content_item_kinds":["a"]}}`,
		`{"content":null,"content":[1],"role":null,"role":"user","internal_chat_message_metadata_passthrough":null,"internal_chat_message_metadata_passthrough":{"content_item_kinds":["a"]}}`,
		`{"role":"user","content":[],"internal_chat_message_metadata_passthrough":{},"internal_chat_message_metadata_passthrough":{"content_item_kinds":["a"]}}`,
		`{"role":"user","content":[],"internal_chat_message_metadata_passthrough":{"content_item_kinds":null},"internal_chat_message_metadata_passthrough":{"content_item_kinds":["ignored"]}}`,
		`{"\u0072ole":"user","\u0063ontent":"text","internal_chat_message_metadata_passthrough":{"content_item_kinds":null}}`,
		`{"type":"input_text","text":"hello"}`,
		`{"type":null,"type":"input_text","text":null,"text":"ignored"}`,
		`{"\u0074ype":"input_text","\u0074ext":"hello"}`,
	} {
		root := gjson.Parse(raw)
		content, role, kinds := requestTimezoneMessageFields(root)
		kind, text := requestTimezoneTextPartFields(root)
		for path, value := range map[string]gjson.Result{
			"content": content, "role": role,
			"internal_chat_message_metadata_passthrough.content_item_kinds": kinds,
			"type": kind, "text": text,
		} {
			want := root.Get(path)
			require.Equal(t, want.Type, value.Type, raw+" / "+path)
			require.Equal(t, want.Raw, value.Raw, raw+" / "+path)
			require.Equal(t, want.String(), value.String(), raw+" / "+path)
		}
	}
}

func BenchmarkOpenAIRequestTimezoneRetryCPU(b *testing.B) {
	for _, size := range []int{1 << 20, 8 << 20, 32 << 20} {
		body := timezoneRetryCPUFixture(size)
		target := RequestLocationObservation{Type: "approximate", Country: "JP", Region: "Tokyo", City: "Tokyo", Timezone: "Asia/Tokyo"}
		_, legacy := prepareOpenAIRequestTimezoneBody(body, timezoneTestPolicy(), timezoneTestAcceptedAt(), false, true, false)
		baseline := prepareOpenAIRequestTimezoneSources(body, timezoneTestPolicy(), timezoneTestAcceptedAt(), false, true, false, nil)
		for _, attempts := range []int{1, 5, 8} {
			for _, version := range []string{"legacy", "sources_only"} {
				b.Run(fmt.Sprintf("%dMiB/%d_attempts/%s", size>>20, attempts, version), func(b *testing.B) {
					b.ReportAllocs()
					b.SetBytes(int64(len(body) * attempts))
					for i := 0; i < b.N; i++ {
						for attempt := 0; attempt < attempts; attempt++ {
							var output []byte
							if version == "legacy" {
								state := legacyTimezoneRetryWithTarget(legacy, target)
								output, _ = legacyTimezoneRetryApply(state, body)
							} else {
								state := baseline.withTargetMetadata(target)
								output, _ = state.ApplyToBody(body)
							}
							runtime.KeepAlive(output)
						}
					}
				})
			}
		}
	}
}

func BenchmarkOpenAIRequestTimezoneFieldScan(b *testing.B) {
	for _, size := range []int{1 << 20, 8 << 20, 32 << 20} {
		root := gjson.Parse(`{"role":"user","content":[{"type":"input_image","image_url":"` + strings.Repeat("x", size) + `"}]}`)
		part := root.Get("content.0")
		for _, version := range []string{"legacy", "combined"} {
			b.Run(fmt.Sprintf("%dMiB/%s", size>>20, version), func(b *testing.B) {
				b.ReportAllocs()
				b.SetBytes(int64(len(root.Raw)))
				for i := 0; i < b.N; i++ {
					if version == "legacy" {
						runtime.KeepAlive(root.Get("content"))
						runtime.KeepAlive(root.Get("role"))
						runtime.KeepAlive(root.Get("internal_chat_message_metadata_passthrough.content_item_kinds"))
						runtime.KeepAlive(part.Get("type"))
						runtime.KeepAlive(part.Get("text"))
					} else {
						content, role, kinds := requestTimezoneMessageFields(root)
						kind, text := requestTimezoneTextPartFields(part)
						runtime.KeepAlive(content)
						runtime.KeepAlive(role)
						runtime.KeepAlive(kinds)
						runtime.KeepAlive(kind)
						runtime.KeepAlive(text)
					}
				}
			})
		}
	}
}
