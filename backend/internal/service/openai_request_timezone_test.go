package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func timezoneTestPolicy() openai.RequestPolicy {
	return openai.RequestPolicy{TimezoneConversionEnabled: true, PassthroughTimezoneConversionEnabled: true}
}

func timezoneTestBody(t testing.TB, value any) []byte {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func timezoneTestEnvironment(zone, date string) string {
	return "<environment_context>\n<cwd>/project</cwd>\n<current_date>" + date + "</current_date>\n<timezone>" + zone + "</timezone>\n</environment_context>"
}

func timezoneTestAcceptedAt() time.Time { return time.Date(2026, 9, 10, 2, 30, 0, 0, time.UTC) }

func TestOpenAIRequestTimezoneCurrentTailAndUntouchedData(t *testing.T) {
	historical := timezoneTestEnvironment("Asia/Shanghai", "2026-08-01")
	current := timezoneTestEnvironment("Asia/Tokyo", "2026-09-10")
	body := timezoneTestBody(t, map[string]any{
		"input": []any{
			map[string]any{"role": "user", "content": historical},
			map[string]any{"role": "assistant", "content": "Earlier answer"},
			map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": current}}},
			map[string]any{"role": "user", "content": "What date is it?"},
		},
		"tools":    []any{map[string]any{"type": "web_search_preview", "user_location": map[string]any{"timezone": "Europe/Paris", "country": "FR", "city": "Paris", "region": "IDF", "unknown": json.RawMessage(`{"exact":9007199254740993}`)}}},
		"timezone": "Europe/Berlin", "timestamp": json.RawMessage(`9007199254740993`),
	})
	out, state := PrepareOpenAIRequestTimezone(body, timezoneTestPolicy(), timezoneTestAcceptedAt(), false, true)
	if got := gjson.GetBytes(out, "input.0.content").String(); got != historical {
		t.Fatalf("historical input changed: %s", got)
	}
	got := gjson.GetBytes(out, "input.2.content.0.text").String()
	if !strings.Contains(got, "<current_date>2026-09-09</current_date>") || !strings.Contains(got, "<timezone>America/Los_Angeles</timezone>") {
		t.Fatalf("wrong current environment: %s", got)
	}
	if got := gjson.GetBytes(out, "tools.0.user_location.timezone").String(); got != OpenAIRequestTimezone {
		t.Fatalf("search timezone = %s", got)
	}
	for _, path := range []string{"timezone", "timestamp", "tools.0.user_location.country", "tools.0.user_location.city", "tools.0.user_location.region", "tools.0.user_location.unknown"} {
		if gjson.GetBytes(out, path).Raw != gjson.GetBytes(body, path).Raw {
			t.Errorf("unrelated field %s changed", path)
		}
	}
	if state.Inbound.ScanStatus != "complete" || len(state.Conversions) != 3 {
		t.Fatalf("unexpected observation: %+v", state)
	}
	if state.Conversions[0].Reason != "historical" || state.Conversions[1].TimeBasis != "gateway_received_at" || state.Conversions[1].ReceivedAt != "2026-09-10T02:30:00Z" {
		t.Fatalf("wrong conversion reasons: %+v", state.Conversions)
	}
	observed := ScanOpenAIRequestTimezones(out)
	if observed.Items[1].Value != OpenAIRequestTimezone || observed.Items[1].CurrentDate != "2026-09-09" {
		t.Fatalf("outbound scanner did not read actual body: %+v", observed)
	}
}

func TestOpenAIRequestTimezoneDoesNotFallBackToEarlierCandidate(t *testing.T) {
	valid := timezoneTestEnvironment("Asia/Shanghai", "2026-09-10")
	cases := map[string]string{
		"quoted":           "> " + valid,
		"fenced":           "```xml\n" + valid + "\n```",
		"mixed":            valid + "\nPlease summarize this example.",
		"broken":           "<environment_context><timezone>Asia/Shanghai</timezone>",
		"duplicate":        "<environment_context><timezone>Asia/Shanghai</timezone><timezone>UTC</timezone></environment_context>",
		"invalid_date":     timezoneTestEnvironment("Asia/Shanghai", "2026-02-30"),
		"invalid_timezone": timezoneTestEnvironment("PST", "2026-09-10"),
		"nested_timezone":  "<environment_context><example><timezone>Asia/Shanghai</timezone></example></environment_context>",
		"broken_other_tag": "<environment_context><cwd>/project<timezone>Asia/Shanghai</timezone></environment_context>",
		"comment_timezone": "<environment_context><!-- <timezone>Asia/Shanghai</timezone> --><current_date>2020-01-01</current_date></environment_context>",
		"comment_date":     "<environment_context><timezone>Asia/Shanghai</timezone><!-- <current_date>2020-01-01</current_date> --></environment_context>",
		"cdata_timezone":   "<environment_context><![CDATA[<timezone>Asia/Shanghai</timezone>]]><current_date>2020-01-01</current_date></environment_context>",
		"cdata_date":       "<environment_context><timezone>Asia/Shanghai</timezone><![CDATA[<current_date>2020-01-01</current_date>]]></environment_context>",
	}
	for name, invalid := range cases {
		t.Run(name, func(t *testing.T) {
			body := timezoneTestBody(t, map[string]any{"messages": []any{map[string]any{"role": "user", "content": valid}, map[string]any{"role": "user", "content": invalid}}})
			out, state := PrepareOpenAIRequestTimezone(body, timezoneTestPolicy(), timezoneTestAcceptedAt(), false, true)
			if !bytes.Equal(out, body) {
				t.Fatalf("unsafe candidate or earlier block was changed: %s", out)
			}
			if state.Inbound.Items[0].Current || !state.Inbound.Items[1].Current {
				t.Fatalf("incorrect current candidate: %+v", state.Inbound)
			}
			if (strings.HasPrefix(name, "comment_") || strings.HasPrefix(name, "cdata_")) && state.Conversions[1].Reason != "quoted_xml_content" {
				t.Fatalf("XML quotation skip reason: %s", state.Conversions[1].Reason)
			}
		})
	}
}

func TestOpenAIRequestTimezoneStringInputAndMessageBlocks(t *testing.T) {
	env := timezoneTestEnvironment("Asia/Shanghai", "2026-09-10")
	t.Run("string input", func(t *testing.T) {
		body := timezoneTestBody(t, map[string]any{"input": env})
		out, _ := PrepareOpenAIRequestTimezone(body, timezoneTestPolicy(), timezoneTestAcceptedAt(), false, false)
		if !strings.Contains(gjson.GetBytes(out, "input").String(), OpenAIRequestTimezone) {
			t.Fatal("string input not converted")
		}
	})
	t.Run("only last content candidate", func(t *testing.T) {
		body := timezoneTestBody(t, map[string]any{"messages": []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": env}, map[string]any{"type": "text", "text": env}}}}})
		out, state := PrepareOpenAIRequestTimezone(body, timezoneTestPolicy(), timezoneTestAcceptedAt(), false, true)
		if gjson.GetBytes(out, "messages.0.content.0.text").String() != env || gjson.GetBytes(out, "messages.0.content.1.text").String() == env {
			t.Fatal("wrong candidate converted")
		}
		if state.Inbound.Items[0].Reason != "historical" {
			t.Fatal("earlier tail candidate not historical")
		}
	})
	t.Run("split content", func(t *testing.T) {
		body := timezoneTestBody(t, map[string]any{"messages": []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "<environment_context><timezone>Asia/Shanghai</timezone>"}, map[string]any{"type": "text", "text": "</environment_context>"}}}}})
		out, _ := PrepareOpenAIRequestTimezone(body, timezoneTestPolicy(), timezoneTestAcceptedAt(), false, true)
		if !bytes.Equal(out, body) {
			t.Fatal("split environment changed")
		}
	})
	t.Run("assistant tail", func(t *testing.T) {
		body := timezoneTestBody(t, map[string]any{"messages": []any{map[string]any{"role": "user", "content": env}, map[string]any{"role": "assistant", "content": "answer"}}})
		out, _ := PrepareOpenAIRequestTimezone(body, timezoneTestPolicy(), timezoneTestAcceptedAt(), false, true)
		if !bytes.Equal(out, body) {
			t.Fatal("old user environment changed")
		}
	})
}

func TestOpenAIRequestTimezoneDateAndDST(t *testing.T) {
	for _, tc := range []struct{ instant, date string }{
		{"2026-01-10T07:59:59Z", "2026-01-09"},
		{"2026-01-10T08:00:00Z", "2026-01-10"},
		{"2026-07-10T06:59:59Z", "2026-07-09"},
		{"2026-07-10T07:00:00Z", "2026-07-10"},
		{"2026-03-08T09:59:59Z", "2026-03-08"},
		{"2026-03-08T10:00:00Z", "2026-03-08"},
		{"2026-11-01T08:59:59Z", "2026-11-01"},
		{"2026-11-01T09:00:00Z", "2026-11-01"},
	} {
		t.Run(tc.instant, func(t *testing.T) {
			instant, err := time.Parse(time.RFC3339, tc.instant)
			if err != nil {
				t.Fatal(err)
			}
			body := timezoneTestBody(t, map[string]any{"input": timezoneTestEnvironment("Asia/Shanghai", "2026-09-10")})
			out, _ := PrepareOpenAIRequestTimezone(body, timezoneTestPolicy(), instant, false, true)
			if !strings.Contains(gjson.GetBytes(out, "input").String(), "<current_date>"+tc.date+"</current_date>") {
				t.Fatalf("wrong date at %s: %s", tc.instant, out)
			}
		})
	}
}

func TestOpenAIRequestTimezoneFieldsRemainAtomicAndMissingFieldsAreNotAdded(t *testing.T) {
	for _, tc := range []struct {
		name, env string
		changes   bool
	}{
		{"missing date", "<environment_context><timezone>UTC</timezone></environment_context>", true},
		{"missing zone", "<environment_context><current_date>2026-09-10</current_date></environment_context>", false},
		{"invalid date", timezoneTestEnvironment("UTC", "09/10/2026"), false},
		{"invalid zone", timezoneTestEnvironment("Local", "2026-09-10"), false},
		{"timezone attributes", "<environment_context><timezone name='zone'>UTC</timezone></environment_context>", false},
		{"unknown similarly named tag", "<environment_context><timezone_notes>keep me</timezone_notes><timezone>UTC</timezone></environment_context>", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := timezoneTestBody(t, map[string]any{"input": tc.env})
			out, _ := PrepareOpenAIRequestTimezone(body, timezoneTestPolicy(), timezoneTestAcceptedAt(), false, true)
			if bytes.Equal(out, body) == tc.changes {
				t.Fatalf("changes=%v, got %s", tc.changes, out)
			}
			if tc.name == "missing date" && strings.Contains(gjson.GetBytes(out, "input").String(), "current_date") {
				t.Fatal("missing date added")
			}
		})
	}
}

func TestOpenAIRequestTimezoneLimitsRollbackWholeRequest(t *testing.T) {
	for _, kind := range []string{"text", "items", "timezone"} {
		t.Run(kind, func(t *testing.T) {
			request := map[string]any{"input": timezoneTestEnvironment("UTC", "2026-09-10")}
			switch kind {
			case "text":
				request["input"] = []any{map[string]any{"role": "user", "content": timezoneTestEnvironment("UTC", "2026-09-10")}, map[string]any{"role": "user", "content": strings.Repeat("x", openAIRequestTimezoneTextLimit)}}
			case "items":
				tools := make([]any, openAIRequestTimezoneItemLimit)
				for i := range tools {
					tools[i] = map[string]any{"type": "web_search", "user_location": map[string]any{"timezone": "UTC"}}
				}
				request["tools"] = tools
			case "timezone":
				request["tools"] = []any{map[string]any{"type": "web_search", "user_location": map[string]any{"timezone": strings.Repeat("z", 129)}}}
			}
			body := timezoneTestBody(t, request)
			out, state := PrepareOpenAIRequestTimezone(body, timezoneTestPolicy(), timezoneTestAcceptedAt(), false, true)
			if !bytes.Equal(out, body) || state.Inbound.ScanStatus != "limited" {
				t.Fatalf("limit did not rollback request: %s", state.Inbound.ScanStatus)
			}
			for _, item := range state.Inbound.Items {
				if len(item.Value) > 128 {
					t.Fatal("unbounded value escaped")
				}
			}
		})
	}
	for _, body := range [][]byte{[]byte(`{"input":`), []byte(`[]`), []byte(`null`)} {
		out, state := PrepareOpenAIRequestTimezone(body, timezoneTestPolicy(), timezoneTestAcceptedAt(), false, true)
		if !bytes.Equal(out, body) || state.Inbound.ScanStatus == "complete" {
			t.Fatalf("unsupported input changed: %s", out)
		}
	}
}

func TestOpenAIRequestTimezoneSearchValidation(t *testing.T) {
	tools := []any{
		map[string]any{"type": "web_search", "user_location": map[string]any{"timezone": nil}},
		map[string]any{"type": "web_search_preview"},
		map[string]any{"type": "web_search", "user_location": map[string]any{"timezone": 8}},
		map[string]any{"type": "web_search", "user_location": map[string]any{"timezone": "EST"}},
		map[string]any{"type": "function", "name": "web_search", "user_location": map[string]any{"timezone": "UTC"}},
	}
	body := timezoneTestBody(t, map[string]any{"tools": tools, "user_location": map[string]any{"timezone": "UTC"}})
	out, state := PrepareOpenAIRequestTimezone(body, timezoneTestPolicy(), timezoneTestAcceptedAt(), false, true)
	if !bytes.Equal(out, body) {
		t.Fatal("invalid or unrelated search fields changed")
	}
	want := []string{"timezone_null", "timezone_missing", "timezone_not_string", "invalid_timezone"}
	if len(state.Conversions) != len(want) {
		t.Fatalf("wrong reports %+v", state.Conversions)
	}
	for i, reason := range want {
		if state.Conversions[i].Reason != reason {
			t.Errorf("reason[%d] = %s; want %s", i, state.Conversions[i].Reason, reason)
		}
	}
}

func TestOpenAIRequestTimezoneNodeBudgetRollsBackTextlessStructures(t *testing.T) {
	env := timezoneTestEnvironment("UTC", "2026-09-10")
	for _, kind := range []string{"messages", "content", "tools", "xml"} {
		t.Run(kind, func(t *testing.T) {
			request := map[string]any{"input": env}
			switch kind {
			case "messages":
				messages := make([]any, openAIRequestTimezoneNodeLimit+1)
				messages[0] = map[string]any{"role": "user", "content": env}
				for i := 1; i < len(messages); i++ {
					messages[i] = map[string]any{"role": "user", "content": ""}
				}
				request["input"] = messages
			case "content":
				parts := make([]any, openAIRequestTimezoneNodeLimit+1)
				parts[0] = map[string]any{"type": "input_text", "text": env}
				for i := 1; i < len(parts); i++ {
					parts[i] = map[string]any{"type": "input_image"}
				}
				request["input"] = []any{map[string]any{"role": "user", "content": parts}}
			case "tools":
				tools := make([]any, openAIRequestTimezoneNodeLimit+1)
				for i := range tools {
					tools[i] = map[string]any{"type": "function"}
				}
				request["tools"] = tools
			case "xml":
				request["input"] = "<environment_context><padding>" + strings.Repeat("<x/>", openAIRequestTimezoneNodeLimit/2+1) + "</padding><timezone>UTC</timezone><current_date>2026-09-10</current_date></environment_context>"
			}
			body := timezoneTestBody(t, request)
			out, state := PrepareOpenAIRequestTimezone(body, timezoneTestPolicy(), timezoneTestAcceptedAt(), false, true)
			if !bytes.Equal(out, body) || state.Inbound.ScanStatus != "limited" {
				t.Fatalf("node budget did not atomically preserve request: %s", state.Inbound.ScanStatus)
			}
			retried, _ := state.ApplyToBody(body)
			if !bytes.Equal(retried, body) {
				t.Fatal("limited scan retained conversion patches")
			}
			if got := ScanOpenAIRequestTimezones(body); got.ScanStatus != "limited" {
				t.Fatalf("outbound scanner did not share traversal bound: %s", got.ScanStatus)
			}
		})
	}
}

func TestOpenAIRequestTimezoneIndependentSwitchesAndFrozenRetry(t *testing.T) {
	body := timezoneTestBody(t, map[string]any{"model": "first", "input": timezoneTestEnvironment("UTC", "2026-09-10")})
	for _, enabled := range []bool{false, true} {
		for _, passthroughEnabled := range []bool{false, true} {
			for _, passthrough := range []bool{false, true} {
				policy := timezoneTestPolicy()
				policy.TimezoneConversionEnabled, policy.PassthroughTimezoneConversionEnabled = enabled, passthroughEnabled
				out, observed := PrepareOpenAIRequestTimezone(body, policy, timezoneTestAcceptedAt(), passthrough, true)
				unobserved, hidden := PrepareOpenAIRequestTimezone(body, policy, timezoneTestAcceptedAt(), passthrough, false)
				if !bytes.Equal(out, unobserved) || hidden.Inbound != nil || observed.Inbound == nil {
					t.Fatal("observability changed semantics")
				}
				if changed := !bytes.Equal(out, body); changed != (enabled && (!passthrough || passthroughEnabled)) {
					t.Fatal("switch precedence mismatch")
				}
			}
		}
	}
	out, state := PrepareOpenAIRequestTimezone(body, timezoneTestPolicy(), timezoneTestAcceptedAt(), false, true)
	modelChanged, err := sjson.SetBytes(body, "model", "second")
	if err != nil {
		t.Fatal(err)
	}
	retry, ok := state.ApplyToBody(modelChanged)
	if !ok || gjson.GetBytes(retry, "model").String() != "second" || gjson.GetBytes(retry, "input").String() != gjson.GetBytes(out, "input").String() {
		t.Fatal("retry lost model or frozen environment")
	}
	again, ok := state.ApplyToBody(retry)
	if !ok || !bytes.Equal(again, retry) {
		t.Fatal("frozen patches not idempotent")
	}
	mutated, _ := sjson.SetBytes(modelChanged, "input", "new user content")
	unchanged, ok := state.ApplyToBody(mutated)
	if ok || !bytes.Equal(unchanged, mutated) {
		t.Fatal("changed source path incorrectly patched")
	}
	missing, _ := sjson.DeleteBytes(modelChanged, "input")
	unchanged, ok = state.ApplyToBody(missing)
	if ok || !bytes.Equal(unchanged, missing) {
		t.Fatal("missing source path incorrectly patched")
	}
	nextDay := timezoneTestAcceptedAt().Add(24 * time.Hour)
	newRequest, _ := PrepareOpenAIRequestTimezone(body, timezoneTestPolicy(), nextDay, false, false)
	if bytes.Equal(newRequest, out) {
		t.Fatal("new request incorrectly reused previous date")
	}
	if !bytes.Equal(state.PreparedBody(), out) {
		t.Fatal("retry snapshot changed")
	}
}

func TestOpenAIRequestTimezoneSnapshotDeepCopy(t *testing.T) {
	body := timezoneTestBody(t, map[string]any{"input": timezoneTestEnvironment("UTC", "2026-09-10")})
	out, state := PrepareOpenAIRequestTimezone(body, timezoneTestPolicy(), timezoneTestAcceptedAt(), false, true)
	clone := CloneRequestTimezoneState(state)
	clone.Inbound.Items[0].Value = "changed"
	clone.Conversions[0].Output = "changed"
	clone.preparedBody[0] = '!'
	clone.patches[0].prepared = "changed"
	snapshot := state.PreparedBody()
	snapshot[0] = '!'
	body[0] = '!'
	out[0] = '!'
	if state.Inbound.Items[0].Value != "UTC" || state.Conversions[0].Output != OpenAIRequestTimezone || state.preparedBody[0] != '{' || state.patches[0].prepared == "changed" {
		t.Fatal("snapshot alias escaped")
	}
}

func TestOpenAIRequestTimezoneApplyIsAtomic(t *testing.T) {
	body := timezoneTestBody(t, map[string]any{"input": timezoneTestEnvironment("UTC", "2026-09-10"), "tools": []any{map[string]any{"type": "web_search", "user_location": map[string]any{"timezone": "UTC"}}}})
	_, state := PrepareOpenAIRequestTimezone(body, timezoneTestPolicy(), timezoneTestAcceptedAt(), false, true)
	mutated, _ := sjson.SetBytes(body, "tools.0.user_location.timezone", "Europe/London")
	out, ok := state.ApplyToBody(mutated)
	if ok || !bytes.Equal(out, mutated) {
		t.Fatal("part of a frozen patch group was applied")
	}
}

func TestOpenAIRequestTimezoneConcurrentSnapshots(t *testing.T) {
	body := timezoneTestBody(t, map[string]any{"input": timezoneTestEnvironment("UTC", "2026-09-10")})
	want, state := PrepareOpenAIRequestTimezone(body, timezoneTestPolicy(), timezoneTestAcceptedAt(), false, true)
	var workers sync.WaitGroup
	for i := 0; i < 16; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for j := 0; j < 10; j++ {
				cloned := CloneRequestTimezoneState(state)
				got, ok := cloned.ApplyToBody(body)
				if !ok || !bytes.Equal(got, want) {
					t.Error("concurrent frozen patch changed")
				}
				cloned.Inbound.Items[0].Value = "isolated"
				cloned.Conversions[0].Output = "isolated"
				if scan := ScanOpenAIRequestTimezones(got); scan.Items[0].Value != OpenAIRequestTimezone {
					t.Error("concurrent final scan changed")
				}
			}
		}()
	}
	workers.Wait()
}

func BenchmarkOpenAIRequestTimezone(b *testing.B) {
	for _, historySize := range []int{0, 64 << 10} {
		b.Run(fmt.Sprintf("history_%d", historySize), func(b *testing.B) {
			body := timezoneTestBody(b, map[string]any{"input": []any{map[string]any{"role": "assistant", "content": strings.Repeat("x", historySize)}, map[string]any{"role": "user", "content": timezoneTestEnvironment("Asia/Shanghai", "2026-09-10")}}})
			b.ReportAllocs()
			b.SetBytes(int64(len(body)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				PrepareOpenAIRequestTimezone(body, timezoneTestPolicy(), timezoneTestAcceptedAt(), false, true)
			}
		})
	}
}
