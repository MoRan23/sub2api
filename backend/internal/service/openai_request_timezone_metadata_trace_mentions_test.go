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

func TestOpenAIEnvironmentTimezoneTextKinds(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		counts     map[string]int
	}{
		{"empty", "", map[string]int{}},
		{"ordinary text", "This environment excerpt is incomplete.", map[string]int{}},
		{"markers only", "timezone time_zone utc_offset 时区", map[string]int{"timezone_marker": 4}},
		{"platform timezones", "Asia/Shanghai America/Los_Angeles Asia/Shanghai", map[string]int{"asia_shanghai": 2, "america_los_angeles": 1}},
		{"UTC and GMT", "UTC GMT", map[string]int{"utc_or_gmt": 2}},
		{"other IANA candidates", "Europe/Paris Australia/Lord_Howe America/Argentina/Buenos_Aires Asia/Not_A_Real_Zone", map[string]int{"other_iana_candidate": 4}},
		{"offset candidates", "UTC+08:00 GMT-07:00 +05:30 -04:00", map[string]int{"utc_offset_candidate": 4}},
		{"markers with adjacent values", "timezone:Asia/Shanghai utc_offset:+08:00", map[string]int{"timezone_marker": 2, "asia_shanghai": 1, "utc_offset_candidate": 1}},
		{"tokens do not overlap", "Asia/Shanghai UTC+08:00 America/Los_Angeles", map[string]int{"asia_shanghai": 1, "utc_offset_candidate": 1, "america_los_angeles": 1}},
		{"XML comment and CDATA remain observable", "<!-- Asia/Shanghai --><![CDATA[America/Los_Angeles]]>", map[string]int{"asia_shanghai": 1, "america_los_angeles": 1}},
		{"word substrings", "NOTUTC GMTsuffix mytimezone timezone_suffix time_zone_extra utc_offset_extra prefixAsia/Shanghai", map[string]int{}},
		{"IANA suffix is not fixed timezone", "Asia/ShanghaiSuffix America/Los_AngelesSuffix", map[string]int{"other_iana_candidate": 2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			summary := buildOpenAIEnvironmentTimezoneText(tc.text)
			require.Equal(t, "complete", summary.ScanStatus)
			require.Empty(t, summary.Reason)
			require.Equal(t, len(tc.text), summary.ScannedBytes)
			got := map[string]int{}
			total := 0
			for _, mention := range summary.Mentions {
				require.NotContains(t, got, mention.Kind, "each kind appears only once")
				got[mention.Kind] = mention.Count
				require.Equal(t, mention.Count, mention.FencedLineCount+mention.QuotedLineCount+mention.OtherLineCount)
				require.Equal(t, mention.Count, mention.OtherLineCount)
				total += mention.Count
			}
			require.Equal(t, tc.counts, got)
			require.Equal(t, total, summary.MentionCount)
		})
	}
}

func TestOpenAIEnvironmentTimezoneTextLineContexts(t *testing.T) {
	for _, tc := range []struct {
		name, text            string
		fenced, quoted, other int
	}{
		{"plain quoted fenced and inline", "Asia/Shanghai\n> Asia/Shanghai\n```xml\nAsia/Shanghai\n```\n`Asia/Shanghai`", 1, 1, 2},
		{"same line repeated", "> Asia/Shanghai Asia/Shanghai", 0, 2, 0},
		{"tilde fence", "~~~xml\nAsia/Shanghai\n~~~\nAsia/Shanghai", 1, 0, 1},
		{"shorter marker does not close", "````xml\nAsia/Shanghai\n```\nAsia/Shanghai\n````\nAsia/Shanghai", 2, 0, 1},
		{"different marker does not close", "~~~\nAsia/Shanghai\n```\nAsia/Shanghai\n~~~\nAsia/Shanghai", 2, 0, 1},
		{"closing marker suffix does not close", "```\nAsia/Shanghai\n```still-open\nAsia/Shanghai\n```\nAsia/Shanghai", 2, 0, 1},
		{"longer marker closes", "```\nAsia/Shanghai\n```` \t\nAsia/Shanghai", 1, 0, 1},
		{"inline markers do not open fence", "Example `Asia/Shanghai` and ```inline```\nAsia/Shanghai", 0, 0, 2},
		{"quoted fence with CRLF", " \t> ```xml\r\n > Asia/Shanghai\r\n > ```\r\n > Asia/Shanghai", 1, 1, 0},
		{"nested quote", " \t> > Asia/Shanghai", 0, 1, 0},
		{"unclosed indented fence", " \t```xml\nAsia/Shanghai", 1, 0, 0},
		{"two character markers are ordinary", "``\nAsia/Shanghai\n~~\nAsia/Shanghai", 0, 0, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			summary := buildOpenAIEnvironmentTimezoneText(tc.text)
			require.Equal(t, "complete", summary.ScanStatus)
			require.Len(t, summary.Mentions, 1)
			mention := summary.Mentions[0]
			require.Equal(t, "asia_shanghai", mention.Kind)
			require.Equal(t, tc.fenced, mention.FencedLineCount)
			require.Equal(t, tc.quoted, mention.QuotedLineCount)
			require.Equal(t, tc.other, mention.OtherLineCount)
			require.Equal(t, tc.fenced+tc.quoted+tc.other, mention.Count)
			require.Equal(t, mention.Count, summary.MentionCount)
		})
	}
}

func TestOpenAIEnvironmentTimezoneTextScanLimits(t *testing.T) {
	t.Run("exact byte limit completes", func(t *testing.T) {
		text := strings.Repeat(" ", openAIEnvironmentTimezoneTextLimit-len("UTC")) + "UTC"
		summary := buildOpenAIEnvironmentTimezoneText(text)
		require.Equal(t, "complete", summary.ScanStatus)
		require.Equal(t, len(text), summary.ScannedBytes)
		require.Equal(t, 1, summary.MentionCount)
	})
	t.Run("unscanned suffix is not absence", func(t *testing.T) {
		text := strings.Repeat(" ", openAIEnvironmentTimezoneTextLimit) + "\nAsia/Shanghai"
		summary := buildOpenAIEnvironmentTimezoneText(text)
		require.Equal(t, "limited", summary.ScanStatus)
		require.Equal(t, "text_limit", summary.Reason)
		require.LessOrEqual(t, summary.ScannedBytes, openAIEnvironmentTimezoneTextLimit)
		require.Less(t, summary.ScannedBytes, len(text))
		require.Zero(t, summary.MentionCount)
		require.Empty(t, summary.Mentions)
	})
	for _, prefix := range []string{"UTC", "UTC+08"} {
		t.Run("truncated token after "+prefix, func(t *testing.T) {
			text := strings.Repeat(" ", openAIEnvironmentTimezoneTextLimit-len(prefix)) + "UTC+08:00"
			summary := buildOpenAIEnvironmentTimezoneText(text)
			require.Equal(t, "limited", summary.ScanStatus)
			require.Equal(t, "text_limit", summary.Reason)
			require.Zero(t, summary.MentionCount, "a partial UTC+08:00 token must not become a shorter timezone observation")
		})
	}
	t.Run("observations before byte limit survive", func(t *testing.T) {
		text := "Asia/Shanghai\n" + strings.Repeat(" ", openAIEnvironmentTimezoneTextLimit) + "\nAmerica/Los_Angeles"
		summary := buildOpenAIEnvironmentTimezoneText(text)
		require.Equal(t, "limited", summary.ScanStatus)
		require.Equal(t, "text_limit", summary.Reason)
		require.Equal(t, 1, summary.MentionCount)
		require.Len(t, summary.Mentions, 1)
		require.Equal(t, "asia_shanghai", summary.Mentions[0].Kind)
	})
	t.Run("exact match limit completes", func(t *testing.T) {
		text := strings.Repeat("UTC ", openAIEnvironmentTimezoneMatchLimit)
		summary := buildOpenAIEnvironmentTimezoneText(text)
		require.Equal(t, "complete", summary.ScanStatus)
		require.Equal(t, len(text), summary.ScannedBytes)
		require.Equal(t, openAIEnvironmentTimezoneMatchLimit, summary.MentionCount)
	})
	t.Run("extra match marks summary limited", func(t *testing.T) {
		text := strings.Repeat("UTC ", openAIEnvironmentTimezoneMatchLimit+1)
		summary := buildOpenAIEnvironmentTimezoneText(text)
		require.Equal(t, "limited", summary.ScanStatus)
		require.Equal(t, "match_limit", summary.Reason)
		require.Equal(t, openAIEnvironmentTimezoneMatchLimit, summary.MentionCount)
		require.LessOrEqual(t, summary.ScannedBytes, len(text))
		require.Len(t, summary.Mentions, 1)
		require.Equal(t, "utc_or_gmt", summary.Mentions[0].Kind)
		require.Equal(t, openAIEnvironmentTimezoneMatchLimit, summary.Mentions[0].Count)
	})
}

func TestOpenAIEnvironmentTimezoneTextSummaryDoesNotLeak(t *testing.T) {
	const secret = "DO_NOT_LOG_timezone_text_private_content"
	text := "<environment_context>\n<cwd>/private/" + secret + "</cwd>\n" +
		"<timezone>Asia/Shanghai</timezone>\nAmerica/Los_Angeles Europe/" + secret + " UTC+08:00"
	summary := buildOpenAIEnvironmentTimezoneText(text)
	encoded, err := json.Marshal(summary)
	require.NoError(t, err)
	for _, forbidden := range []string{secret, "<environment_context>", "/private/", "Asia/Shanghai", "America/Los_Angeles", "Europe/", "UTC+08:00"} {
		require.NotContains(t, string(encoded), forbidden)
	}
	allowedKinds := map[string]bool{"asia_shanghai": true, "america_los_angeles": true, "utc_or_gmt": true, "other_iana_candidate": true, "utc_offset_candidate": true, "timezone_marker": true}
	var fields map[string]any
	require.NoError(t, json.Unmarshal(encoded, &fields))
	require.Len(t, fields, 4)
	require.Equal(t, "complete", fields["scan_status"])
	require.NotContains(t, fields, "reason", "complete scans omit the empty reason")
	for _, field := range []string{"scanned_bytes", "mention_count"} {
		require.IsType(t, float64(0), fields[field])
	}
	for _, raw := range fields["mentions"].([]any) {
		mention := raw.(map[string]any)
		require.Len(t, mention, 5)
		require.True(t, allowedKinds[mention["kind"].(string)])
		for _, field := range []string{"count", "fenced_line_count", "quoted_line_count", "other_line_count"} {
			require.IsType(t, float64(0), mention[field])
		}
	}
}

func TestOpenAIEnvironmentTimezoneTextIncompleteStructuredLog(t *testing.T) {
	const secret = "DO_NOT_LOG_incomplete_timezone_text_secret"
	text := "<environment_context>\n<cwd>D:\\Code\\sub2api</cwd>\n<timezone>Asia/Shanghai</timezone>\n<private>" + secret
	body := timezoneTestBody(t, map[string]any{"input": []any{timezoneTestMessage(text)}})
	prepared, state := PrepareOpenAIRequestTimezone(body, timezoneTestPolicy(), timezoneTestAcceptedAt(), false, true)
	require.Equal(t, body, prepared, "incomplete environments must remain unchanged")
	beforeBody := bytes.Clone(body)
	beforeState, err := json.Marshal(state)
	require.NoError(t, err)
	var output bytes.Buffer
	core := zapcore.NewCore(zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()), zapcore.AddSync(&output), zap.InfoLevel)
	ctx := logger.IntoContext(context.Background(), zap.New(core))
	logOpenAIEnvironmentMetadataTrace(ctx, 1467, "ingress_before_timezone", body, state)
	for _, forbidden := range []string{secret, "D:\\Code\\sub2api", "Asia/Shanghai", "<private>"} {
		require.NotContains(t, output.String(), forbidden)
	}
	item := gjson.Parse(output.String()).Get("metadata_trace.items.0")
	require.Equal(t, "invalid", item.Get("parse_status").String())
	require.Equal(t, "missing_close", item.Get("shape_reason").String())
	require.Equal(t, "skipped", item.Get("preparation_status").String())
	summary := item.Get("timezone_text")
	require.Equal(t, "complete", summary.Get("scan_status").String())
	require.EqualValues(t, len(text), summary.Get("scanned_bytes").Int())
	require.Equal(t, int64(1), summary.Get("mentions.#(kind==\"asia_shanghai\").count").Int())
	require.Positive(t, summary.Get("mentions.#(kind==\"timezone_marker\").count").Int())
	require.Equal(t, beforeBody, body)
	afterState, err := json.Marshal(state)
	require.NoError(t, err)
	require.Equal(t, beforeState, afterState)
	require.Equal(t, prepared, state.PreparedBody())
}

func TestOpenAIEnvironmentTimezoneTextLimitedSourceStillLogs(t *testing.T) {
	body := timezoneTestBody(t, map[string]any{"input": []any{timezoneTestMessage("<environment_context>" + strings.Repeat(" ", openAIRequestTimezoneTextLimit) + "Asia/Shanghai")}})
	prepared, state := PrepareOpenAIRequestTimezone(body, timezoneTestPolicy(), timezoneTestAcceptedAt(), false, true)
	require.Equal(t, body, prepared)
	require.Equal(t, "limited", state.Inbound.ScanStatus)
	require.Empty(t, state.Conversions)
	var output bytes.Buffer
	core := zapcore.NewCore(zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()), zapcore.AddSync(&output), zap.InfoLevel)
	ctx := logger.IntoContext(context.Background(), zap.New(core))
	logOpenAIEnvironmentMetadataTrace(ctx, 1468, "ingress_before_timezone", body, state)
	require.NotEmpty(t, output.String(), "a source scan limit must not look like a complete request with no candidates")
	trace := gjson.Parse(output.String()).Get("metadata_trace")
	require.Equal(t, "limited", trace.Get("status").String())
	require.Equal(t, "source_scan_limited", trace.Get("reason").String())
	require.Equal(t, "limited", trace.Get("source_scan_status").String())
	require.Zero(t, trace.Get("item_count").Int())
	require.Empty(t, trace.Get("items").Array())
	require.NotContains(t, output.String(), "Asia/Shanghai")
}

func TestOpenAIEnvironmentTimezoneTextReferenceStructuredLog(t *testing.T) {
	text := "Example:\n> <environment_context>\n> <timezone>America/Los_Angeles</timezone>\n> </environment_context>"
	message := timezoneTestMessage(text)
	delete(message, "internal_chat_message_metadata_passthrough")
	body := timezoneTestBody(t, map[string]any{"input": []any{message}})
	prepared, state := PrepareOpenAIRequestTimezone(body, timezoneTestPolicy(), timezoneTestAcceptedAt(), false, true)
	require.Equal(t, body, prepared)
	var output bytes.Buffer
	core := zapcore.NewCore(zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()), zapcore.AddSync(&output), zap.InfoLevel)
	ctx := logger.IntoContext(context.Background(), zap.New(core))
	logOpenAIEnvironmentMetadataTrace(ctx, 1469, "ingress_before_timezone", body, state)
	item := gjson.Parse(output.String()).Get("metadata_trace.items.0")
	require.Equal(t, "reference", item.Get("environment_source").String())
	require.Equal(t, "skipped", item.Get("preparation_status").String())
	require.Equal(t, "environment_metadata_missing", item.Get("preparation_reason").String())
	mention := item.Get("timezone_text.mentions.#(kind==\"america_los_angeles\")")
	require.Equal(t, int64(1), mention.Get("count").Int())
	require.Equal(t, int64(1), mention.Get("quoted_line_count").Int())
	require.Zero(t, mention.Get("fenced_line_count").Int())
	require.Zero(t, mention.Get("other_line_count").Int())
	require.NotContains(t, output.String(), "America/Los_Angeles")
}
