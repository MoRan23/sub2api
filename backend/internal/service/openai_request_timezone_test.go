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

func timezoneTestMessage(text string) map[string]any {
	return map[string]any{
		"type": "message", "role": "user",
		"content": []any{map[string]any{"type": "input_text", "text": text}},
		"internal_chat_message_metadata_passthrough": map[string]any{
			"content_item_kinds": []any{"environments.environment_context"},
		},
	}
}

func timezoneTestInput(text string) []any { return []any{timezoneTestMessage(text)} }

func assertTimezoneTestSearchLocation(t testing.TB, body []byte, path string) {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal([]byte(gjson.GetBytes(body, path).Raw), &got); err != nil {
		t.Fatalf("invalid location at %s: %v", path, err)
	}
	want := map[string]string{"type": "approximate", "country": "US", "region": "Washington", "city": "Seattle", "timezone": "America/Los_Angeles"}
	if len(got) != len(want) {
		t.Fatalf("location must replace the whole object, got %s", gjson.GetBytes(body, path).Raw)
	}
	for key, value := range want {
		if got[key] != value {
			t.Errorf("%s.%s = %v; want %s", path, key, got[key], value)
		}
	}
}

func timezoneTestAcceptedAt() time.Time { return time.Date(2026, 9, 10, 2, 30, 0, 0, time.UTC) }

func TestOpenAIRequestTimezoneCurrentTailAndUntouchedData(t *testing.T) {
	historical := timezoneTestEnvironment("Asia/Shanghai", "2026-08-01")
	current := timezoneTestEnvironment("Asia/Tokyo", "2026-09-10")
	body := timezoneTestBody(t, map[string]any{
		"input": []any{
			timezoneTestMessage(historical),
			map[string]any{"role": "assistant", "content": "Earlier answer"},
			timezoneTestMessage(current),
			map[string]any{"role": "user", "content": "What date is it?"},
		},
		"tools":    []any{map[string]any{"type": "web_search_preview", "search_context_size": "high", "unknown": json.RawMessage(`{"exact":9007199254740993}`), "user_location": map[string]any{"timezone": "Europe/Paris", "country": "FR", "city": "Paris", "region": "IDF", "unknown": json.RawMessage(`{"exact":9007199254740993}`)}}},
		"timezone": "Europe/Berlin", "timestamp": json.RawMessage(`9007199254740993`),
	})
	out, state := PrepareOpenAIRequestTimezone(body, timezoneTestPolicy(), timezoneTestAcceptedAt(), false, true)
	if got := gjson.GetBytes(out, "input.0.content.0.text").String(); got != timezoneTestEnvironment(OpenAIRequestTimezone, "2026-08-01") {
		t.Fatalf("historical timezone or date is wrong: %s", got)
	}
	got := gjson.GetBytes(out, "input.2.content.0.text").String()
	if !strings.Contains(got, "<current_date>2026-09-09</current_date>") || !strings.Contains(got, "<timezone>America/Los_Angeles</timezone>") {
		t.Fatalf("wrong current environment: %s", got)
	}
	if got := gjson.GetBytes(out, "tools.0.user_location.timezone").String(); got != OpenAIRequestTimezone {
		t.Fatalf("search timezone = %s", got)
	}
	assertTimezoneTestSearchLocation(t, out, "tools.0.user_location")
	for _, path := range []string{"timezone", "timestamp", "tools.0.search_context_size", "tools.0.unknown", "input.0.internal_chat_message_metadata_passthrough", "input.2.internal_chat_message_metadata_passthrough"} {
		if gjson.GetBytes(out, path).Raw != gjson.GetBytes(body, path).Raw {
			t.Errorf("unrelated field %s changed", path)
		}
	}
	if state.Inbound.ScanStatus != "complete" || len(state.Conversions) != 3 {
		t.Fatalf("unexpected observation: %+v", state)
	}
	if state.Conversions[0].Reason != "historical_timezone_converted" || state.Conversions[0].Status != "converted" || state.Conversions[0].DateAfter != "2026-08-01" || state.Conversions[0].TimeBasis != "" || state.Conversions[0].ReceivedAt != "" || state.Conversions[1].TimeBasis != "gateway_received_at" || state.Conversions[1].ReceivedAt != "2026-09-10T02:30:00Z" {
		t.Fatalf("wrong conversion reasons: %+v", state.Conversions)
	}
	observed := ScanOpenAIRequestTimezones(out)
	if observed.Items[1].Value != OpenAIRequestTimezone || observed.Items[1].CurrentDate != "2026-09-09" {
		t.Fatalf("outbound scanner did not read actual body: %+v", observed)
	}
}

func TestOpenAIRequestTimezoneDoesNotRefreshEarlierDateForInvalidCurrentCandidate(t *testing.T) {
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
			body := timezoneTestBody(t, map[string]any{"messages": []any{timezoneTestMessage(valid), timezoneTestMessage(invalid)}})
			out, state := PrepareOpenAIRequestTimezone(body, timezoneTestPolicy(), timezoneTestAcceptedAt(), false, true)
			if gjson.GetBytes(out, "messages.0.content.0.text").String() != timezoneTestEnvironment(OpenAIRequestTimezone, "2026-09-10") || gjson.GetBytes(out, "messages.1.content.0.text").String() != invalid {
				t.Fatalf("unsafe candidate changed or historical date was refreshed: %s", out)
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
		if !bytes.Equal(out, body) {
			t.Fatal("unmarked string input converted")
		}
	})
	t.Run("only last content candidate date", func(t *testing.T) {
		message := timezoneTestMessage(env)
		message["content"] = []any{map[string]any{"type": "input_text", "text": env}, map[string]any{"type": "input_text", "text": env}}
		message["internal_chat_message_metadata_passthrough"] = map[string]any{"content_item_kinds": []any{"environments.environment_context", "environments.environment_context"}}
		body := timezoneTestBody(t, map[string]any{"messages": []any{message}})
		out, state := PrepareOpenAIRequestTimezone(body, timezoneTestPolicy(), timezoneTestAcceptedAt(), false, true)
		if gjson.GetBytes(out, "messages.0.content.0.text").String() != timezoneTestEnvironment(OpenAIRequestTimezone, "2026-09-10") || gjson.GetBytes(out, "messages.0.content.1.text").String() != timezoneTestEnvironment(OpenAIRequestTimezone, "2026-09-09") {
			t.Fatal("wrong candidate date refreshed")
		}
		if state.Inbound.Items[0].Current || !state.Inbound.Items[1].Current || state.Inbound.Items[0].Reason != "" {
			t.Fatal("earlier tail candidate classification is wrong")
		}
	})
	t.Run("split content", func(t *testing.T) {
		message := timezoneTestMessage(env)
		message["content"] = []any{map[string]any{"type": "input_text", "text": "<environment_context><timezone>Asia/Shanghai</timezone>"}, map[string]any{"type": "input_text", "text": "</environment_context>"}}
		message["internal_chat_message_metadata_passthrough"] = map[string]any{"content_item_kinds": []any{"environments.environment_context", "environments.environment_context"}}
		body := timezoneTestBody(t, map[string]any{"messages": []any{message}})
		out, _ := PrepareOpenAIRequestTimezone(body, timezoneTestPolicy(), timezoneTestAcceptedAt(), false, true)
		if !bytes.Equal(out, body) {
			t.Fatal("split environment changed")
		}
	})
	t.Run("assistant tail", func(t *testing.T) {
		body := timezoneTestBody(t, map[string]any{"messages": []any{timezoneTestMessage(env), map[string]any{"role": "assistant", "content": "answer"}}})
		out, _ := PrepareOpenAIRequestTimezone(body, timezoneTestPolicy(), timezoneTestAcceptedAt(), false, true)
		if gjson.GetBytes(out, "messages.0.content.0.text").String() != timezoneTestEnvironment(OpenAIRequestTimezone, "2026-09-10") {
			t.Fatal("old user environment must retain its date while converting timezone")
		}
	})
}

func TestOpenAIRequestTimezoneRequiresExactContentMetadata(t *testing.T) {
	env := timezoneTestEnvironment("Asia/Shanghai", "2026-09-10")
	for _, tc := range []struct {
		name   string
		modify func(map[string]any)
	}{
		{"null kinds", func(m map[string]any) {
			m["internal_chat_message_metadata_passthrough"] = map[string]any{"content_item_kinds": nil}
		}},
		{"scalar metadata", func(m map[string]any) {
			m["internal_chat_message_metadata_passthrough"] = "environments.environment_context"
		}},
		{"object kinds", func(m map[string]any) {
			m["internal_chat_message_metadata_passthrough"] = map[string]any{"content_item_kinds": map[string]any{"0": "environments.environment_context"}}
		}},
		{"scalar kinds", func(m map[string]any) {
			m["internal_chat_message_metadata_passthrough"] = map[string]any{"content_item_kinds": "environments.environment_context"}
		}},
		{"wrong kind", func(m map[string]any) {
			m["internal_chat_message_metadata_passthrough"] = map[string]any{"content_item_kinds": []any{"other.environment_context"}}
		}},
		{"wrong index", func(m map[string]any) {
			m["internal_chat_message_metadata_passthrough"] = map[string]any{"content_item_kinds": []any{nil, "environments.environment_context"}}
		}},
		{"wrong case", func(m map[string]any) {
			m["internal_chat_message_metadata_passthrough"] = map[string]any{"content_item_kinds": []any{"Environments.environment_context"}}
		}},
		{"padded marker", func(m map[string]any) {
			m["internal_chat_message_metadata_passthrough"] = map[string]any{"content_item_kinds": []any{" environments.environment_context "}}
		}},
		{"nonstring marker", func(m map[string]any) {
			m["internal_chat_message_metadata_passthrough"] = map[string]any{"content_item_kinds": []any{true}}
		}},
		{"assistant role", func(m map[string]any) { m["role"] = "assistant" }},
		{"developer role", func(m map[string]any) { m["role"] = "developer" }},
		{"missing role", func(m map[string]any) { delete(m, "role") }},
		{"content string", func(m map[string]any) { m["content"] = env }},
		{"ordinary text", func(m map[string]any) { m["content"] = []any{map[string]any{"type": "text", "text": env}} }},
		{"untyped text", func(m map[string]any) { m["content"] = []any{map[string]any{"text": env}} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			message := timezoneTestMessage(env)
			tc.modify(message)
			body := timezoneTestBody(t, map[string]any{"input": []any{message}})
			out, state := PrepareOpenAIRequestTimezone(body, timezoneTestPolicy(), timezoneTestAcceptedAt(), false, true)
			if !bytes.Equal(out, body) || len(state.patches) != 0 {
				t.Fatalf("ineligible environment changed: %s", out)
			}
			if len(state.Conversions) != 1 || state.Conversions[0].Status != "skipped" {
				t.Fatalf("ineligible environment must be reported as skipped: %+v", state.Conversions)
			}
			observed := ScanOpenAIRequestTimezones(out)
			if len(observed.Items) != 1 || observed.Items[0].Value != "Asia/Shanghai" || observed.Items[0].CurrentDate != "2026-09-10" {
				t.Fatalf("observer lost actual unmarked environment: %+v", observed)
			}
		})
	}
}

func TestOpenAIRequestTimezoneMetadataMatchesContentIndex(t *testing.T) {
	env := timezoneTestEnvironment("Asia/Shanghai", "2026-09-10")
	message := timezoneTestMessage(env)
	message["content"] = []any{
		map[string]any{"type": "input_text", "text": env},
		map[string]any{"type": "input_text", "text": env},
		map[string]any{"type": "input_text", "text": env},
	}
	message["internal_chat_message_metadata_passthrough"] = map[string]any{"content_item_kinds": []any{nil, "environments.environment_context", "user_message"}}
	body := timezoneTestBody(t, map[string]any{"input": []any{message}})
	out, _ := PrepareOpenAIRequestTimezone(body, timezoneTestPolicy(), timezoneTestAcceptedAt(), false, true)
	if gjson.GetBytes(out, "input.0.content.1.text").String() != timezoneTestEnvironment(OpenAIRequestTimezone, "2026-09-09") {
		t.Fatalf("eligible middle item was not current: %s", out)
	}
	for _, path := range []string{"input.0.content.0.text", "input.0.content.2.text"} {
		if gjson.GetBytes(out, path).String() != env {
			t.Fatalf("unmarked item %s changed", path)
		}
	}
}

func TestOpenAIRequestTimezoneHistoricalDateDoesNotDependOnIngressClock(t *testing.T) {
	body := timezoneTestBody(t, map[string]any{"input": []any{
		timezoneTestMessage(timezoneTestEnvironment("Asia/Shanghai", "2026-01-10")),
		timezoneTestMessage(timezoneTestEnvironment("Europe/London", "2026-07-10")),
		map[string]any{"role": "assistant", "content": "previous answer"},
	}})
	var first []byte
	for _, accepted := range []time.Time{{}, timezoneTestAcceptedAt(), timezoneTestAcceptedAt().Add(48 * time.Hour)} {
		out, state := PrepareOpenAIRequestTimezone(body, timezoneTestPolicy(), accepted, false, true)
		if gjson.GetBytes(out, "input.0.content.0.text").String() != timezoneTestEnvironment(OpenAIRequestTimezone, "2026-01-10") || gjson.GetBytes(out, "input.1.content.0.text").String() != timezoneTestEnvironment(OpenAIRequestTimezone, "2026-07-10") {
			t.Fatalf("historical dates changed at %s: %s", accepted, out)
		}
		if first != nil && !bytes.Equal(first, out) {
			t.Fatal("historical dates depend on this request's clock")
		}
		first = out
		for _, report := range state.Conversions {
			if report.Status != "converted" || report.Reason != "historical_timezone_converted" || report.DateBefore != report.DateAfter || report.TimeBasis != "" || report.ReceivedAt != "" {
				t.Fatalf("misleading historical conversion report: %+v", report)
			}
		}
		retry, ok := state.ApplyToBody(body)
		if !ok || !bytes.Equal(retry, out) {
			t.Fatal("retry did not retain historical patches")
		}
		again, repeated := PrepareOpenAIRequestTimezone(out, timezoneTestPolicy(), accepted.Add(24*time.Hour), false, true)
		if !bytes.Equal(again, out) {
			t.Fatal("repeated history conversion changed the date")
		}
		for _, report := range repeated.Conversions {
			if report.Status != "unchanged" || report.Reason != "already_target" {
				t.Fatalf("already converted history not recognized: %+v", report)
			}
		}
	}
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
			body := timezoneTestBody(t, map[string]any{"input": timezoneTestInput(timezoneTestEnvironment("Asia/Shanghai", "2026-09-10"))})
			out, _ := PrepareOpenAIRequestTimezone(body, timezoneTestPolicy(), instant, false, true)
			if !strings.Contains(gjson.GetBytes(out, "input.0.content.0.text").String(), "<current_date>"+tc.date+"</current_date>") {
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
			body := timezoneTestBody(t, map[string]any{"input": timezoneTestInput(tc.env)})
			out, _ := PrepareOpenAIRequestTimezone(body, timezoneTestPolicy(), timezoneTestAcceptedAt(), false, true)
			if bytes.Equal(out, body) == tc.changes {
				t.Fatalf("changes=%v, got %s", tc.changes, out)
			}
			if tc.name == "missing date" && strings.Contains(gjson.GetBytes(out, "input.0.content.0.text").String(), "current_date") {
				t.Fatal("missing date added")
			}
		})
	}
}

func TestOpenAIRequestTimezoneLimitsRollbackWholeRequest(t *testing.T) {
	for _, kind := range []string{"text", "items", "timezone"} {
		t.Run(kind, func(t *testing.T) {
			request := map[string]any{"input": timezoneTestInput(timezoneTestEnvironment("UTC", "2026-09-10"))}
			switch kind {
			case "text":
				request["input"] = []any{timezoneTestMessage(timezoneTestEnvironment("UTC", "2026-09-10")), map[string]any{"role": "user", "content": strings.Repeat("x", openAIRequestTimezoneTextLimit)}}
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
		map[string]any{"type": "web_search", "user_location": nil},
		map[string]any{"type": "web_search", "user_location": "not a location"},
		map[string]any{"type": "web_search", "user_location": []any{"UTC"}},
		map[string]any{"type": "web_search_preview_2025_03_11", "user_location": map[string]any{"timezone": "UTC"}},
		map[string]any{"type": "web_search_custom", "user_location": map[string]any{"timezone": "UTC"}},
		map[string]any{"type": "web_search", "user_location": map[string]any{"type": "approximate", "country": "US", "region": "California", "city": "Los Angeles", "timezone": "America/Los_Angeles"}},
	}
	body := timezoneTestBody(t, map[string]any{"tools": tools, "user_location": map[string]any{"timezone": "UTC"}})
	out, state := PrepareOpenAIRequestTimezone(body, timezoneTestPolicy(), timezoneTestAcceptedAt(), false, true)
	for _, i := range []int{0, 1, 2, 3, 5, 6, 7, 8, 10} {
		assertTimezoneTestSearchLocation(t, out, fmt.Sprintf("tools.%d.user_location", i))
	}
	for _, path := range []string{"tools.4", "tools.9", "user_location"} {
		if gjson.GetBytes(out, path).Raw != gjson.GetBytes(body, path).Raw {
			t.Fatalf("unrelated search field %s changed", path)
		}
	}
	if len(state.Conversions) != 9 {
		t.Fatalf("wrong reports %+v", state.Conversions)
	}
	for _, conversion := range state.Conversions {
		if conversion.Status != "converted" || conversion.LocationAfter == nil || conversion.LocationAfter.City != "Seattle" {
			t.Fatalf("incomplete location conversion: %+v", conversion)
		}
	}
}

func TestOpenAIRequestTimezoneAlphaSearchLocation(t *testing.T) {
	for _, tc := range []struct {
		name     string
		location string
		status   string
	}{
		{name: "valid", location: `{"timezone":"Asia/Shanghai","city":"Shanghai","unknown":9007199254740993}`, status: "converted"},
		{name: "already target", location: `{"type":"approximate","country":"US","region":"Washington","city":"Seattle","timezone":"America/Los_Angeles"}`, status: "unchanged"},
		{name: "previous Los Angeles target", location: `{"type":"approximate","country":"US","region":"California","city":"Los Angeles","timezone":"America/Los_Angeles"}`, status: "converted"},
		{name: "only target timezone", location: `{"timezone":"America/Los_Angeles"}`, status: "converted"},
		{name: "missing timezone", location: `{"city":"Shanghai"}`, status: "converted"},
		{name: "null timezone", location: `{"timezone":null}`, status: "converted"},
		{name: "number timezone", location: `{"timezone":8}`, status: "converted"},
		{name: "invalid zone", location: `{"timezone":"Not/AZone"}`, status: "converted"},
		{name: "null location", location: `null`, status: "converted"},
		{name: "scalar location", location: `17`, status: "converted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{"model":"gpt-5.6-sol","commands":{"time":[{"utc_offset":"+08:00"}],"search_query":[{"q":"Beijing time"}]},"settings":{"user_location":` + tc.location + `,"unknown":9007199254740993}}`)
			out, state := prepareOpenAIRequestTimezoneBody(body, timezoneTestPolicy(), timezoneTestAcceptedAt(), false, true, true)
			assertTimezoneTestSearchLocation(t, out, "settings.user_location")
			for _, path := range []string{"model", "commands", "settings.unknown"} {
				if gjson.GetBytes(out, path).Raw != gjson.GetBytes(body, path).Raw {
					t.Fatalf("alpha unrelated field %s changed", path)
				}
			}
			if len(state.Conversions) != 1 || state.Conversions[0].Path != "settings.user_location.timezone" || state.Conversions[0].Status != tc.status {
				t.Fatalf("unexpected alpha search conversion: %+v", state.Conversions)
			}
			if len(state.Inbound.Items) != 1 || state.Inbound.Items[0].Source != "web_search" {
				t.Fatalf("unexpected inbound search observation: %+v", state.Inbound)
			}
			actual := ScanOpenAIRequestTimezones(out)
			if len(actual.Items) != 1 || actual.Items[0].Value != OpenAIRequestTimezone || actual.Items[0].Location == nil || actual.Items[0].Location.Country != "US" {
				t.Fatalf("unexpected final search observation: %+v", actual)
			}
		})
	}
}

func TestOpenAIRequestTimezoneAlphaSearchDoesNotAddLocation(t *testing.T) {
	for _, body := range [][]byte{
		[]byte(`{"model":"gpt-5.6-sol","commands":{"time":[{"utc_offset":"+08:00"}]}}`),
		[]byte(`{"model":"gpt-5.6-sol","commands":{},"settings":{"search_context_size":"high"}}`),
	} {
		out, state := prepareOpenAIRequestTimezoneBody(body, timezoneTestPolicy(), timezoneTestAcceptedAt(), false, true, true)
		if !bytes.Equal(body, out) || len(state.Conversions) != 0 || len(state.Inbound.Items) != 0 {
			t.Fatalf("missing location was changed or fabricated: %s, %+v", out, state)
		}
	}
}

func TestOpenAIRequestTimezoneAlphaSearchRequiresExplicitSourceForSettings(t *testing.T) {
	for _, raw := range []string{
		`{"commands":{"search_query":[{"q":"weather"}],"time":[{"utc_offset":"+08:00"}]}}`,
		`{"commands":{"search_query":[{"q":"weather"}]},"settings":{"user_location":{"timezone":"Asia/Shanghai","city":"Shanghai"}}}`,
	} {
		body := []byte(raw)
		ordinary, _ := PrepareOpenAIRequestTimezone(body, timezoneTestPolicy(), timezoneTestAcceptedAt(), false, true)
		if !bytes.Equal(ordinary, body) {
			t.Fatal("ordinary Responses payload guessed alpha endpoint from JSON shape")
		}
		out, state := prepareOpenAIRequestTimezoneBody(body, timezoneTestPolicy(), timezoneTestAcceptedAt(), false, true, true)
		assertTimezoneTestSearchLocation(t, out, "settings.user_location")
		if gjson.GetBytes(out, "commands").Raw != gjson.GetBytes(body, "commands").Raw {
			t.Fatal("search or time commands changed")
		}
		added := !gjson.GetBytes(body, "settings.user_location").Exists()
		if len(state.Conversions) != 1 || state.Conversions[0].LocationAdded != added {
			t.Fatalf("incorrect added observation: %+v", state.Conversions)
		}
		again, ok := state.ApplyToBody(out)
		if !ok || !bytes.Equal(again, out) {
			t.Fatal("alpha location patch was not idempotent")
		}
	}
	for _, commands := range []string{`{"time":[{"utc_offset":"+08:00"}]}`, `{"open":[{"ref_id":"https://example.com"}]}`, `{"search_query":[]}`, `{"search_query":"weather"}`, `{}`} {
		body := []byte(`{"commands":` + commands + `,"settings":{"search_context_size":"high"}}`)
		out, _ := prepareOpenAIRequestTimezoneBody(body, timezoneTestPolicy(), timezoneTestAcceptedAt(), false, true, true)
		if !bytes.Equal(out, body) {
			t.Fatalf("non-search commands acquired a location: %s", out)
		}
	}
	// A pre-existing location is normalized even for a pure time request; the
	// requested time offset remains independent of the client's search location.
	body := []byte(`{"commands":{"time":[{"utc_offset":"+08:00"}]},"settings":{"user_location":null}}`)
	out, _ := prepareOpenAIRequestTimezoneBody(body, timezoneTestPolicy(), timezoneTestAcceptedAt(), false, true, true)
	assertTimezoneTestSearchLocation(t, out, "settings.user_location")
	if gjson.GetBytes(out, "commands.time.0.utc_offset").String() != "+08:00" {
		t.Fatal("query target timezone changed")
	}
}

func TestOpenAIRequestTimezoneLocationFrozenPatchOriginalAndAdded(t *testing.T) {
	for _, tc := range []struct{ name, tool string }{
		{"missing", `{"type":"web_search"}`},
		{"null", `{"type":"web_search","user_location":null}`},
		{"number", `{"type":"web_search","user_location":8}`},
		{"object", `{"type":"web_search","user_location":{"timezone":"UTC","city":"London","unknown":9007199254740993}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{"model":"first","tools":[` + tc.tool + `],"timestamp":9007199254740993}`)
			out, state := PrepareOpenAIRequestTimezone(body, timezoneTestPolicy(), timezoneTestAcceptedAt(), false, true)
			assertTimezoneTestSearchLocation(t, out, "tools.0.user_location")
			if len(state.Conversions) != 1 || state.Conversions[0].LocationAdded != (tc.name == "missing") {
				t.Fatalf("missing and existing values conflated: %+v", state.Conversions)
			}
			changed, _ := sjson.SetBytes(body, "model", "second")
			retry, ok := state.ApplyToBody(changed)
			if !ok || gjson.GetBytes(retry, "model").String() != "second" || gjson.GetBytes(retry, "timestamp").Raw != "9007199254740993" {
				t.Fatalf("frozen patch lost unrelated values: %s", retry)
			}
			assertTimezoneTestSearchLocation(t, retry, "tools.0.user_location")
			again, ok := state.ApplyToBody(retry)
			if !ok || !bytes.Equal(again, retry) {
				t.Fatal("whole-object patch is not idempotent")
			}
			mutated, _ := sjson.SetRawBytes(body, "tools.0.user_location", []byte(`{"timezone":"Europe/London","city":"Changed"}`))
			actual, ok := state.ApplyToBody(mutated)
			if ok || !bytes.Equal(actual, mutated) {
				t.Fatal("changed location was overwritten by frozen patch")
			}
			if tc.name != "missing" {
				missing, _ := sjson.DeleteBytes(body, "tools.0.user_location")
				actual, ok := state.ApplyToBody(missing)
				if ok || !bytes.Equal(actual, missing) {
					t.Fatal("existing location removed after capture was re-added")
				}
			} else {
				nullLocation, _ := sjson.SetBytes(body, "tools.0.user_location", nil)
				actual, ok := state.ApplyToBody(nullLocation)
				if ok || !bytes.Equal(actual, nullLocation) {
					t.Fatal("missing source and later explicit null were conflated")
				}
			}
		})
	}
}

func TestOpenAIRequestTimezoneLocationFrozenPatchRequiresSameSourceKind(t *testing.T) {
	for _, location := range []string{"", `,"user_location":{"timezone":"UTC"}`} {
		body := []byte(`{"tools":[{"type":"web_search"` + location + `}]}`)
		_, state := PrepareOpenAIRequestTimezone(body, timezoneTestPolicy(), timezoneTestAcceptedAt(), false, true)
		for _, tc := range []struct{ path, value string }{
			{"tools.0.type", `"function"`},
			{"tools.0", `null`},
			{"tools", `[]`},
			{"tools", `{}`},
		} {
			mutated, err := sjson.SetRawBytes(body, tc.path, []byte(tc.value))
			if err != nil {
				t.Fatal(err)
			}
			out, ok := state.ApplyToBody(mutated)
			if ok || !bytes.Equal(out, mutated) {
				t.Fatalf("location patched a changed source at %s (%s): %s", tc.path, tc.value, out)
			}
		}
		mutated, _ := sjson.SetBytes(body, "tools.0.search_context_size", "high")
		out, ok := state.ApplyToBody(mutated)
		if !ok || gjson.GetBytes(out, "tools.0.search_context_size").String() != "high" {
			t.Fatalf("compatible tool option was not preserved: %s", out)
		}
		assertTimezoneTestSearchLocation(t, out, "tools.0.user_location")
	}
	body := []byte(`{"commands":{"search_query":[{"q":"weather"}]}}`)
	_, state := prepareOpenAIRequestTimezoneBody(body, timezoneTestPolicy(), timezoneTestAcceptedAt(), false, true, true)
	for _, settings := range []string{`null`, `[]`, `"user content"`, `3`} {
		mutated, err := sjson.SetRawBytes(body, "settings", []byte(settings))
		if err != nil {
			t.Fatal(err)
		}
		out, ok := state.ApplyToBody(mutated)
		if ok || !bytes.Equal(out, mutated) {
			t.Fatalf("location patch replaced incompatible settings %s: %s", settings, out)
		}
		prepared, initial := prepareOpenAIRequestTimezoneBody(mutated, timezoneTestPolicy(), timezoneTestAcceptedAt(), false, true, true)
		if !bytes.Equal(prepared, mutated) || len(initial.Conversions) != 1 || initial.Conversions[0].Status != "skipped" || initial.Conversions[0].Reason != "location_container_not_object" {
			t.Fatalf("incompatible initial settings were not skipped: %s, %+v", prepared, initial.Conversions)
		}
		reapplied, ok := initial.ApplyToBody(mutated)
		if !ok || !bytes.Equal(reapplied, prepared) {
			t.Fatalf("prepared and reapplied alpha body disagree: %s, %s", prepared, reapplied)
		}
	}
	// Creating a compatible settings object for an unrelated option is safe;
	// the frozen location must not clobber that later option.
	mutated, _ := sjson.SetBytes(body, "settings.search_context_size", "high")
	out, ok := state.ApplyToBody(mutated)
	if !ok || gjson.GetBytes(out, "settings.search_context_size").String() != "high" {
		t.Fatalf("unrelated settings were not preserved: %s", out)
	}
	assertTimezoneTestSearchLocation(t, out, "settings.user_location")
}

func TestOpenAIRequestTimezoneXMLParserTextBoundary(t *testing.T) {
	const prefix = "<environment_context><padding>"
	const suffix = "</padding></environment_context>"
	for _, extra := range []int{0, 1} {
		scanner := &requestTimezoneScanner{result: TimezoneScanResult{ScanStatus: "complete"}}
		text := prefix + strings.Repeat("x", openAIRequestTimezoneTextLimit-len(prefix)-len(suffix)+extra) + suffix
		valid := scanner.validEnvironmentXML(text)
		if valid != (extra == 0) {
			t.Fatalf("XML parser text boundary %d: valid=%v", len(text), valid)
		}
		if extra > 0 && (scanner.result.ScanStatus != "limited" || scanner.nodes != 0) {
			t.Fatal("oversized XML must be rejected before parsing any tokens")
		}
	}
}

func TestOpenAIRequestTimezoneNodeBudgetRollsBackTextlessStructures(t *testing.T) {
	env := timezoneTestEnvironment("UTC", "2026-09-10")
	for _, kind := range []string{"messages", "content", "tools", "xml"} {
		t.Run(kind, func(t *testing.T) {
			request := map[string]any{"input": timezoneTestInput(env)}
			switch kind {
			case "messages":
				messages := make([]any, openAIRequestTimezoneNodeLimit+1)
				messages[0] = timezoneTestMessage(env)
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
				message := timezoneTestMessage(env)
				message["content"] = parts
				request["input"] = []any{message}
			case "tools":
				tools := make([]any, openAIRequestTimezoneNodeLimit+1)
				for i := range tools {
					tools[i] = map[string]any{"type": "function"}
				}
				request["tools"] = tools
			case "xml":
				request["input"] = timezoneTestInput("<environment_context><padding>" + strings.Repeat("<x/>", openAIRequestTimezoneNodeLimit/2+1) + "</padding><timezone>UTC</timezone><current_date>2026-09-10</current_date></environment_context>")
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
	body := timezoneTestBody(t, map[string]any{"model": "first", "input": timezoneTestInput(timezoneTestEnvironment("UTC", "2026-09-10")), "tools": []any{map[string]any{"type": "web_search"}}})
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
	if !ok || gjson.GetBytes(retry, "model").String() != "second" || gjson.GetBytes(retry, "input.0.content.0.text").String() != gjson.GetBytes(out, "input.0.content.0.text").String() {
		t.Fatal("retry lost model or frozen environment")
	}
	again, ok := state.ApplyToBody(retry)
	if !ok || !bytes.Equal(again, retry) {
		t.Fatal("frozen patches not idempotent")
	}
	mutated, _ := sjson.SetBytes(modelChanged, "input.0.content.0.text", "new user content")
	unchanged, ok := state.ApplyToBody(mutated)
	if ok || !bytes.Equal(unchanged, mutated) {
		t.Fatal("changed source path incorrectly patched")
	}
	missing, _ := sjson.DeleteBytes(modelChanged, "input.0.content.0.text")
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
	body := timezoneTestBody(t, map[string]any{"input": timezoneTestInput(timezoneTestEnvironment("UTC", "2026-09-10")), "tools": []any{map[string]any{"type": "web_search", "user_location": map[string]any{"timezone": "UTC", "city": "London"}}}})
	out, state := PrepareOpenAIRequestTimezone(body, timezoneTestPolicy(), timezoneTestAcceptedAt(), false, true)
	clone := CloneRequestTimezoneState(state)
	clone.Inbound.Items[0].Value = "changed"
	clone.Conversions[0].Output = "changed"
	clone.preparedBody[0] = '!'
	clone.patches[0].prepared = "changed"
	clone.Inbound.Items[1].Location.City = "changed inbound"
	clone.Conversions[1].LocationBefore.City = "changed before"
	clone.Conversions[1].LocationAfter.City = "changed after"
	snapshot := state.PreparedBody()
	snapshot[0] = '!'
	body[0] = '!'
	out[0] = '!'
	if state.Inbound.Items[0].Value != "UTC" || state.Conversions[0].Output != OpenAIRequestTimezone || state.preparedBody[0] != '{' || state.patches[0].prepared == "changed" {
		t.Fatal("snapshot alias escaped")
	}
	if state.Inbound.Items[1].Location.City != "London" || state.Conversions[1].LocationBefore.City != "London" || state.Conversions[1].LocationAfter.City != "Seattle" {
		t.Fatal("location observation alias escaped")
	}
}

func TestOpenAIRequestTimezoneApplyIsAtomic(t *testing.T) {
	body := timezoneTestBody(t, map[string]any{"input": timezoneTestInput(timezoneTestEnvironment("UTC", "2026-09-10")), "tools": []any{map[string]any{"type": "web_search", "user_location": map[string]any{"timezone": "UTC"}}}})
	_, state := PrepareOpenAIRequestTimezone(body, timezoneTestPolicy(), timezoneTestAcceptedAt(), false, true)
	mutated, _ := sjson.SetBytes(body, "tools.0.user_location.timezone", "Europe/London")
	out, ok := state.ApplyToBody(mutated)
	if ok || !bytes.Equal(out, mutated) {
		t.Fatal("part of a frozen patch group was applied")
	}
}

func TestOpenAIRequestTimezoneConcurrentSnapshots(t *testing.T) {
	body := timezoneTestBody(t, map[string]any{"input": timezoneTestInput(timezoneTestEnvironment("UTC", "2026-09-10"))})
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
			body := timezoneTestBody(b, map[string]any{"input": []any{map[string]any{"role": "assistant", "content": strings.Repeat("x", historySize)}, timezoneTestMessage(timezoneTestEnvironment("Asia/Shanghai", "2026-09-10"))}})
			b.ReportAllocs()
			b.SetBytes(int64(len(body)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				PrepareOpenAIRequestTimezone(body, timezoneTestPolicy(), timezoneTestAcceptedAt(), false, true)
			}
		})
	}
}
