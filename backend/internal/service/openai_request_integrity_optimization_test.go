package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenAIRequestIntegritySelectiveValidation(t *testing.T) {
	for _, body := range []string{
		`{"input":"ok","metadata":{"x":1,"\u0078":2}}`,
		`{"input":"ok","metadata":{},"\u006detadata":[]}`,
		`{"input":"ok","metadata":[{"nested":{"same":null,"same":false}}]}`,
		`{"input":"ok","metadata":[true,]}`,
	} {
		optimizationCompareReference(t, []byte(body), []byte(`{"input":"ok"}`), RequestIntegrityCheckOptions{})
		optimizationCompareReference(t, []byte(`{"input":"ok"}`), []byte(body), RequestIntegrityCheckOptions{})
	}
	body, _, reason := decodeRequestIntegrityDocument([]byte(`{"input":[{"role":"user","content":"ok","extension":{"secret":true}}],"metadata":{"private":"discard"}}`))
	require.Empty(t, reason)
	require.NotContains(t, body, "metadata")
	require.Contains(t, body["input"].([]any)[0].(map[string]any), "extension")
}

func TestOpenAIRequestIntegrityCopyOnWriteConcurrentNormalizations(t *testing.T) {
	baseline := []byte(`{"model":" gpt-5 ","instructions":"existing","reasoning":{"effort":"minimal"},"tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object","properties":{"extension":{"type":"string"}}}}}],"tool_choice":{"type":"function","function":{"name":"f"}},"input":[{"role":"system","content":"system","internal_chat_message_metadata_passthrough":{}},{"role":"user","id":"msg_1","content":[{"type":"text","text":"hello","extra":"retain"},{"type":"output_text","text":"world"}]},{"type":"reasoning","id":"r","call_id":"c","summary":null,"encrypted_content":"opaque"},{"type":"compaction_summary","id":"s","content":"summary"},{"role":"tool","tool_call_id":"call_1","content":"result"},{"type":"function_call","id":"call_1","name":"f","arguments":"{}"},{"type":"item_reference","id":"call_external"}]}`)
	var normalized map[string]any
	require.NoError(t, json.Unmarshal(baseline, &normalized))
	canonicalizePerformanceReferenceIntegrityCodex(normalized, make(map[string]bool))
	wire, err := json.Marshal(normalized)
	require.NoError(t, err)
	opts := RequestIntegrityCheckOptions{ExpectedModel: "gpt-5"}
	optimizationCompareReference(t, baseline, wire, opts)
	state := NewOpenAIRequestIntegrityState(true, "responses", baseline)
	before, err := json.Marshal(state.parsed)
	require.NoError(t, err)
	results := make(chan *RequestIntegrityObservation, 32)
	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() { results <- state.Check(integrityTestAccount(), wire, opts) })
	}
	wg.Wait()
	close(results)
	for result := range results {
		require.Equal(t, "expected_transform", result.Status)
	}
	after, err := json.Marshal(state.parsed)
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.Equal(t, "unchanged", state.Check(integrityTestAccount(), baseline, RequestIntegrityCheckOptions{}).Status)
}

func TestOpenAIRequestIntegrityTimezoneProjectionMatchesEncodedDocument(t *testing.T) {
	location := `{"type":"approximate","country":"US","region":"Washington","city":"Seattle","timezone":"America/Los_Angeles"}`
	for _, tc := range []struct {
		name, body, path, original, container, toolType, prepared string
		exists                                                    bool
	}{
		{"replace", `{"input":"ok","tools":[{"type":"web_search","user_location":{"timezone":"UTC"}}]}`, "tools.0.user_location", `{"timezone":"UTC"}`, "tools.0", "web_search", location, true},
		{"insert", " \n {\"input\":\"ok\",\"tools\":[{\"type\":\"web_search\"   }]} \t", "tools.0.user_location", "", "tools.0", "web_search", location, false},
		{"empty_settings", " \n {\"input\":\"ok\",\"settings\":{   }} \t", "settings.user_location", "", "settings", "", location, false},
		{"absent_settings", " \n {\"input\":\"ok\"   } \t", "settings.user_location", "", "settings", "", location, false},
		{"null_location", `{"input":"ok","tools":[{"type":"web_search","user_location":null}]}`, "tools.0.user_location", `null`, "tools.0", "web_search", location, true},
		{"text", `{"input":[{"role":"user","content":[{"type":"input_text","text":"source"}]}]}`, "input.0.content.0.text", `"source"`, "", "", `"prepared"`, true},
		{"ignored_text", `{"input":"ok","messages":[{"content":"source"}]}`, "messages.0.content", `"source"`, "", "", `"prepared"`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			timezone := &RequestTimezoneState{patches: []requestTimezoneBodyPatch{{path: tc.path, original: tc.original, prepared: tc.prepared, originalExists: tc.exists, containerPath: tc.container, toolType: tc.toolType}}}
			assertIntegrityTimezoneProjection(t, []byte(tc.body), timezone)
		})
	}
	// Actual depth and complete-document node limits still apply when most of
	// the body is an unobserved field and the inserted value is very small.
	for _, remaining := range []int{0, 12, 13, 14} {
		base := []byte(`{"input":"ok","tools":[{"type":"web_search"}],"metadata":[]}`)
		_, nodes, reason := decodeRequestIntegrityDocument(base)
		require.Empty(t, reason)
		count := requestIntegrityMaxNodes - nodes - remaining
		body := bytes.Replace(base, []byte(`"metadata":[]`), []byte(`"metadata":[`+strings.Repeat("0,", count-1)+`0]`), 1)
		t.Run(fmt.Sprintf("nodes_remaining_%d", remaining), func(t *testing.T) {
			assertIntegrityTimezoneProjection(t, body, &RequestTimezoneState{patches: []requestTimezoneBodyPatch{{path: "tools.0.user_location", prepared: location, containerPath: "tools.0", toolType: "web_search"}}})
		})
	}
	for _, depth := range []int{requestIntegrityMaxDepth - 3, requestIntegrityMaxDepth - 2} {
		prepared := strings.Repeat("[", depth) + "0" + strings.Repeat("]", depth)
		t.Run(fmt.Sprintf("prepared_depth_%d", depth), func(t *testing.T) {
			assertIntegrityTimezoneProjection(t, []byte(`{"input":"ok","tools":[{"type":"web_search"}]}`), &RequestTimezoneState{patches: []requestTimezoneBodyPatch{{path: "tools.0.user_location", prepared: prepared, containerPath: "tools.0", toolType: "web_search"}}})
		})
	}
	for _, delta := range []int{-1, 0, 1} {
		base := []byte(`{"input":"ok","tools":[{"type":"web_search"}],"metadata":""}`)
		insertion := len(`,"user_location":`) + len(location)
		padding := requestIntegrityMaxBytes - insertion - len(base) + delta
		body := bytes.Replace(base, []byte(`"metadata":""`), []byte(`"metadata":"`+strings.Repeat("x", padding)+`"`), 1)
		t.Run(fmt.Sprintf("adjusted_bytes_delta_%d", delta), func(t *testing.T) {
			assertIntegrityTimezoneProjection(t, body, &RequestTimezoneState{patches: []requestTimezoneBodyPatch{{path: "tools.0.user_location", prepared: location, containerPath: "tools.0", toolType: "web_search"}}})
		})
	}
}

func assertIntegrityTimezoneProjection(t *testing.T, baseline []byte, timezone *RequestTimezoneState) {
	t.Helper()
	state := NewOpenAIRequestIntegrityState(true, "responses", baseline)
	require.Empty(t, state.reason)
	frozen, err := json.Marshal(state.parsed)
	require.NoError(t, err)
	wire, ok := timezone.ApplyToBody(baseline)
	legacy, reason := decodeRequestIntegrityBody(wire)
	wantChanged := ok && !bytes.Equal(baseline, wire) && reason == ""
	got, changed := state.projectTimezone(timezone)
	require.Equal(t, wantChanged, changed)
	if wantChanged {
		require.Equal(t, legacy, got)
	} else {
		require.Equal(t, state.parsed, got)
	}
	after, err := json.Marshal(state.parsed)
	require.NoError(t, err)
	require.Equal(t, frozen, after)
}

func TestOpenAIRequestIntegrityTimezoneProjectionIsAllOrNothing(t *testing.T) {
	body := []byte(`{"input":[{"content":"source"},{"content":"wrong"}]}`)
	timezone := &RequestTimezoneState{patches: []requestTimezoneBodyPatch{
		{path: "input.0.content", original: `"source"`, prepared: `"prepared"`, originalExists: true},
		{path: "input.1.content", original: `"source"`, prepared: `"prepared"`, originalExists: true},
	}}
	assertIntegrityTimezoneProjection(t, body, timezone)
}

func TestOpenAIRequestIntegrityTimezoneProjectionConcurrentTargets(t *testing.T) {
	body := []byte(`{"input":"hello","tools":[{"type":"web_search","user_location":{"timezone":"UTC","city":"London"}}]}`)
	firstBody, first := PrepareOpenAIRequestTimezone(body, timezoneTestPolicy(), timezoneTestAcceptedAt(), false, true)
	second := first.WithTarget(RequestLocationObservation{Type: "approximate", Country: "JP", Region: "Tokyo", City: "Tokyo", Timezone: "Asia/Tokyo"})
	secondBody, ok := second.ApplyToBody(body)
	require.True(t, ok)
	state := NewOpenAIRequestIntegrityState(true, "responses", body)
	frozen, err := json.Marshal(state.parsed)
	require.NoError(t, err)
	results := make(chan *RequestIntegrityObservation, 32)
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Go(func() {
			wire, timezone := firstBody, first
			if i%2 != 0 {
				wire, timezone = secondBody, second
			}
			results <- state.Check(integrityTestAccount(), wire, RequestIntegrityCheckOptions{TimezoneState: timezone})
		})
	}
	wg.Wait()
	close(results)
	for result := range results {
		require.Equal(t, "expected_transform", result.Status)
	}
	after, err := json.Marshal(state.parsed)
	require.NoError(t, err)
	require.Equal(t, frozen, after)
}
