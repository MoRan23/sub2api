package service

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func TestOpenAIEnvironmentMetadataTraceEligibility(t *testing.T) {
	for _, tc := range []struct {
		name, eligibility, marker string
		mutate                    func(map[string]any)
	}{
		{"valid", "eligible", "exact", func(map[string]any) {}},
		{"missing", "metadata_missing", "missing", func(m map[string]any) { delete(m, "internal_chat_message_metadata_passthrough") }},
		{"null metadata", "kinds_not_array", "missing", func(m map[string]any) { m["internal_chat_message_metadata_passthrough"] = nil }},
		{"object kinds", "kinds_not_array", "missing", func(m map[string]any) {
			m["internal_chat_message_metadata_passthrough"] = map[string]any{"content_item_kinds": map[string]any{"0": openAIEnvironmentMetadataMarker}}
		}},
		{"wrong index", "marker_mismatch", "nonstring", func(m map[string]any) {
			m["internal_chat_message_metadata_passthrough"] = map[string]any{"content_item_kinds": []any{nil, openAIEnvironmentMetadataMarker}}
		}},
		{"empty kinds", "index_out_of_range", "missing", func(m map[string]any) {
			m["internal_chat_message_metadata_passthrough"] = map[string]any{"content_item_kinds": []any{}}
		}},
		{"ordinary marker", "marker_mismatch", "other_string", func(m map[string]any) {
			m["internal_chat_message_metadata_passthrough"] = map[string]any{"content_item_kinds": []any{"user_message"}}
		}},
		{"padded marker", "marker_mismatch", "case_or_whitespace_mismatch", func(m map[string]any) {
			m["internal_chat_message_metadata_passthrough"] = map[string]any{"content_item_kinds": []any{" Environments.environment_context "}}
		}},
		{"wrong role", "role_not_user", "exact", func(m map[string]any) { m["role"] = "assistant" }},
		{"wrong content type", "content_not_input_text", "exact", func(m map[string]any) { m["content"].([]any)[0].(map[string]any)["type"] = "text" }},
		{"malformed xml", "eligible", "exact", func(m map[string]any) {
			m["content"].([]any)[0].(map[string]any)["text"] = "<environment_context><timezone>Asia/Shanghai</timezone>"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			message := timezoneTestMessage(timezoneTestEnvironment("Asia/Shanghai", "2026-09-18"))
			tc.mutate(message)
			body := timezoneTestBody(t, map[string]any{"input": []any{message}})
			_, state := PrepareOpenAIRequestTimezone(body, timezoneTestPolicy(), timezoneTestAcceptedAt(), false, true)
			trace := buildOpenAIEnvironmentMetadataTrace(body, state)
			require.Equal(t, "complete", trace.Status)
			require.Equal(t, "complete", trace.SourceScanStatus)
			require.NotEmpty(t, trace.AcceptedAt)
			require.True(t, trace.ConversionEnabled)
			require.True(t, trace.PassthroughEnabled)
			require.Len(t, trace.Items, 1)
			item := trace.Items[0]
			require.Equal(t, tc.eligibility, item.Eligibility)
			require.Equal(t, tc.marker, item.Kinds.MarkerClass)
			require.Equal(t, "input.0.content.0.text", item.Path)
			require.True(t, item.TextPresent)
			require.Equal(t, "string", item.TextType)
			require.Positive(t, item.TextBytes)
			if tc.name == "wrong index" {
				require.Equal(t, []int{1}, item.Kinds.ExpectedIndices)
			}
			if tc.name == "malformed xml" {
				require.Equal(t, "invalid", item.ParseStatus)
				require.Equal(t, "environment_not_standalone", item.ParseReason)
				require.Equal(t, "skipped", item.PreparationStatus)
			} else if tc.name == "valid" {
				require.Equal(t, "valid", item.ParseStatus)
				require.Equal(t, "converted", item.PreparationStatus)
			}
		})
	}
}

func TestOpenAIEnvironmentMetadataTraceAlternativeLocations(t *testing.T) {
	message := timezoneTestMessage(timezoneTestEnvironment("Asia/Shanghai", "2026-09-18"))
	delete(message, "internal_chat_message_metadata_passthrough")
	message["metadata"] = map[string]any{"content_item_kinds": []any{openAIEnvironmentMetadataMarker}}
	message["content_item_kinds"] = []any{openAIEnvironmentMetadataMarker}
	part := message["content"].([]any)[0].(map[string]any)
	part["metadata"] = map[string]any{"content_item_kinds": []any{openAIEnvironmentMetadataMarker}}
	part["internal_chat_message_metadata_passthrough"] = map[string]any{"content_item_kinds": []any{openAIEnvironmentMetadataMarker}}
	body := timezoneTestBody(t, map[string]any{
		"input": []any{message},
		"internal_chat_message_metadata_passthrough": map[string]any{"content_item_kinds": []any{openAIEnvironmentMetadataMarker}},
	})
	_, state := PrepareOpenAIRequestTimezone(body, timezoneTestPolicy(), timezoneTestAcceptedAt(), false, true)
	trace := buildOpenAIEnvironmentMetadataTrace(body, state)
	require.Len(t, trace.Items, 1)
	require.Equal(t, "metadata_missing", trace.Items[0].Eligibility)
	require.Len(t, trace.Items[0].AlternateKinds, 5)
	for _, alternative := range trace.Items[0].AlternateKinds {
		require.Equal(t, "exact", alternative.Kinds.MarkerClass)
		require.Equal(t, []int{0}, alternative.Kinds.ExpectedIndices)
	}
}

func TestOpenAIEnvironmentMetadataTraceLimits(t *testing.T) {
	input := make([]any, 12)
	for i := range input {
		input[i] = timezoneTestMessage(timezoneTestEnvironment("Asia/Shanghai", "2026-09-18"))
	}
	body := timezoneTestBody(t, map[string]any{"input": input})
	_, state := PrepareOpenAIRequestTimezone(body, timezoneTestPolicy(), timezoneTestAcceptedAt(), false, true)
	trace := buildOpenAIEnvironmentMetadataTrace(body, state)
	require.Equal(t, 12, trace.ItemCount)
	require.Len(t, trace.Items, openAIEnvironmentMetadataTraceItemLimit)
	require.True(t, trace.ItemsTruncated)
	large := bytes.Repeat([]byte(" "), openAIEnvironmentMetadataTraceBodyLimit+1)
	trace = buildOpenAIEnvironmentMetadataTrace(large, state)
	require.Equal(t, "body_limit", trace.Reason)
	require.Empty(t, trace.Items)
	for _, tc := range []struct {
		body   []byte
		state  *RequestTimezoneState
		reason string
	}{
		{body, nil, "state_missing"}, {[]byte(`{"input":`), state, "invalid_json"}, {[]byte(`[]`), state, "root_not_object"},
	} {
		require.Equal(t, tc.reason, buildOpenAIEnvironmentMetadataTrace(tc.body, tc.state).Reason)
	}
	kinds := make([]any, 70)
	for i := range kinds {
		kinds[i] = openAIEnvironmentMetadataMarker
	}
	encoded, err := json.Marshal(kinds)
	require.NoError(t, err)
	kindsTrace := buildOpenAIEnvironmentMetadataKindsTrace(gjson.ParseBytes(encoded), 69)
	require.Equal(t, 70, kindsTrace.Count)
	require.Equal(t, "exact", kindsTrace.MarkerClass, "the corresponding marker is checked even beyond the alternative-index scan limit")
	require.Len(t, kindsTrace.ExpectedIndices, openAIEnvironmentMetadataTraceKindsLimit)
	require.True(t, kindsTrace.SearchTruncated)
}

func TestOpenAIEnvironmentMetadataTraceStructuredLogDoesNotLeak(t *testing.T) {
	const secret = "DO_NOT_LOG_request_credentials_private_text"
	message := timezoneTestMessage(timezoneTestEnvironment("Asia/Shanghai", "2026-09-18") + secret)
	message["role"] = secret
	message["content"].([]any)[0].(map[string]any)["type"] = secret
	message["internal_chat_message_metadata_passthrough"] = map[string]any{
		"content_item_kinds": []any{secret, map[string]any{secret: secret}}, secret: secret,
	}
	body := timezoneTestBody(t, map[string]any{"input": []any{message}, "credentials": secret, "instructions": secret})
	state := &RequestTimezoneState{
		Conversions: []TimezoneConversion{
			{Source: "environment_context", Path: "input.0.content.0.text", Original: secret, Output: secret, Status: secret, Reason: secret, EnvironmentSource: secret},
			{Source: "environment_context", Path: secret, Status: secret, Reason: secret},
		},
		Inbound: &TimezoneScanResult{Items: []TimezoneScanItem{{Source: "environment_context", Path: "input.0.content.0.text", Value: secret, CurrentDate: secret, Status: secret, Reason: secret}}},
	}
	var output bytes.Buffer
	core := zapcore.NewCore(zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()), zapcore.AddSync(&output), zap.InfoLevel)
	ctx := logger.IntoContext(context.Background(), zap.New(core).With(zap.String("request_id", "req-test-123")))
	logOpenAIEnvironmentMetadataTrace(ctx, 1465, "http", body, state)
	require.NotContains(t, output.String(), secret)
	require.NotContains(t, output.String(), "Asia/Shanghai")
	require.NotContains(t, output.String(), "2026-09-18")
	var logged map[string]any
	require.NoError(t, json.Unmarshal(output.Bytes(), &logged))
	require.Equal(t, "openai.environment_metadata_trace", logged["msg"])
	require.Equal(t, "req-test-123", logged["request_id"])
	require.Equal(t, float64(1465), logged["account_id"])
	require.Equal(t, "http", logged["stage"])
	require.Equal(t, true, logged[logger.OpsSystemLogSkipField])
	trace := logged["metadata_trace"].(map[string]any)
	items := trace["items"].([]any)
	first := items[0].(map[string]any)
	require.Equal(t, "other", first["role"])
	require.Equal(t, "other", first["content_item_type"])
	require.Equal(t, "other", first["parse_reason"])
	require.Equal(t, "unsupported", items[1].(map[string]any)["path"])
	output.Reset()
	logOpenAIEnvironmentMetadataTrace(ctx, 1465, secret, body, state)
	require.NotContains(t, output.String(), secret, "unexpected stage values are not forwarded")
	for _, stage := range []string{"ingress_before_timezone", "ws_frame_before_timezone"} {
		output.Reset()
		logOpenAIEnvironmentMetadataTrace(ctx, 1465, stage, body, state)
		require.Contains(t, output.String(), stage)
	}
	output.Reset()
	logOpenAIEnvironmentMetadataTrace(ctx, 1465, "http", []byte(`{"input":"ordinary text"}`), &RequestTimezoneState{})
	require.Empty(t, output.String(), "requests with no environment candidates should not produce per-request logs")
}

func TestOpenAIEnvironmentMetadataTraceNeverMutatesBodyOrState(t *testing.T) {
	body := timezoneTestBody(t, map[string]any{"input": timezoneTestInput(timezoneTestEnvironment("Asia/Shanghai", "2026-09-18"))})
	_, state := PrepareOpenAIRequestTimezone(body, timezoneTestPolicy(), timezoneTestAcceptedAt(), false, true)
	original := bytes.Clone(body)
	stateJSON, err := json.Marshal(state)
	require.NoError(t, err)
	buildOpenAIEnvironmentMetadataTrace(body, state)
	require.Equal(t, original, body)
	afterJSON, err := json.Marshal(state)
	require.NoError(t, err)
	require.Equal(t, stateJSON, afterJSON)
	for _, path := range []string{"input.bad.content.0.text", "input.0.content.-1.text", "input.0.content.0.secret", "input.00.content.0.text", strings.Repeat("secret", 20)} {
		_, _, _, ok := openAIEnvironmentTracePath(path)
		require.False(t, ok, path)
	}
}
