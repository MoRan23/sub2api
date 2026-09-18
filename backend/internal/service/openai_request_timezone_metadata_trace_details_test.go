package service

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenAIEnvironmentTimezoneTextDetailUnicodePositions(t *testing.T) {
	const text = "头🙂 timezone=Asia/Shanghai\r\n> 时区: UTC+08:00\r\n"
	summary := buildOpenAIEnvironmentTimezoneText(text)
	require.Equal(t, "decoded_text_utf8", summary.LocationBasis)
	require.Equal(t, len(text), summary.ScannedBytes)
	require.Zero(t, summary.UnscannedBytes)
	want := []struct {
		token               string
		line, column, quote int
	}{
		{"timezone", 1, 4, 0},
		{"Asia/Shanghai", 1, 13, 0},
		{"时区", 2, 3, 1},
		{"UTC+08:00", 2, 7, 1},
	}
	require.Len(t, summary.Matches, len(want))
	for i, expected := range want {
		actual := summary.Matches[i]
		require.Equal(t, expected.token, actual.Token)
		require.Equal(t, expected.line, actual.Line, expected.token)
		require.Equal(t, expected.column, actual.Column, expected.token)
		require.Equal(t, expected.quote, actual.QuoteDepth, expected.token)
		require.Equal(t, strings.Index(text, expected.token), actual.ByteStart, expected.token)
		require.Equal(t, actual.ByteStart+len(expected.token), actual.ByteEnd, expected.token)
		require.Equal(t, expected.token, text[actual.ByteStart:actual.ByteEnd])
	}
}

func TestOpenAIEnvironmentTimezoneTextDetailSyntaxAndXMLContext(t *testing.T) {
	type expectedMatch struct {
		token, syntax, context string
	}
	for _, tc := range []struct {
		name, text  string
		open, close int
		matches     []expectedMatch
	}{
		{"XML tags and value", "<timezone>UTC</timezone>", 1, 1, []expectedMatch{{"timezone", "xml_open_tag", "none"}, {"UTC", "xml_value", "none"}, {"timezone", "xml_close_tag", "none"}}},
		{"multiline XML value", "<timezone>\nAsia/Shanghai\n</timezone>", 1, 1, []expectedMatch{{"timezone", "xml_open_tag", "none"}, {"Asia/Shanghai", "xml_value", "none"}, {"timezone", "xml_close_tag", "none"}}},
		{"incomplete XML value", "<timezone>Asia/Shanghai", 1, 0, []expectedMatch{{"timezone", "xml_open_tag", "none"}, {"Asia/Shanghai", "xml_value", "none"}}},
		{"self closing tag", "<timezone/>", 1, 0, []expectedMatch{{"timezone", "xml_self_closing_tag", "none"}}},
		{"quoted JSON key", `{"timezone":"UTC"}`, 0, 0, []expectedMatch{{"timezone", "quoted_key", "none"}, {"UTC", "word", "none"}}},
		{"assignment keys", "time_zone = America/Los_Angeles\nutc_offset:+08:00", 0, 0, []expectedMatch{{"time_zone", "assignment_key", "none"}, {"America/Los_Angeles", "word", "none"}, {"utc_offset", "assignment_key", "none"}, {"+08:00", "word", "none"}}},
		{"plain words", "timezone UTC", 0, 0, []expectedMatch{{"timezone", "word", "none"}, {"UTC", "word", "none"}}},
		{"XML comment", "<!--\n<timezone>UTC</timezone>\n-->", 1, 1, []expectedMatch{{"timezone", "xml_open_tag", "comment"}, {"UTC", "xml_value", "comment"}, {"timezone", "xml_close_tag", "comment"}}},
		{"CDATA", "<![CDATA[\n<timezone>GMT</timezone>\n]]>", 1, 1, []expectedMatch{{"timezone", "xml_open_tag", "cdata"}, {"GMT", "xml_value", "cdata"}, {"timezone", "xml_close_tag", "cdata"}}},
		{"XML context ends before next match", "<!-- UTC --> GMT <![CDATA[Asia/Shanghai]]> America/Los_Angeles", 0, 0, []expectedMatch{{"UTC", "word", "comment"}, {"GMT", "word", "none"}, {"Asia/Shanghai", "word", "cdata"}, {"America/Los_Angeles", "word", "none"}}},
		{"CDATA delimiters inside comment remain comment", "<!-- <![CDATA[ UTC ]]> GMT --> Asia/Shanghai", 0, 0, []expectedMatch{{"UTC", "word", "comment"}, {"GMT", "word", "comment"}, {"Asia/Shanghai", "word", "none"}}},
		{"comment delimiters inside CDATA remain CDATA", "<![CDATA[ <!-- UTC --> GMT ]]> Asia/Shanghai", 0, 0, []expectedMatch{{"UTC", "word", "cdata"}, {"GMT", "word", "cdata"}, {"Asia/Shanghai", "word", "none"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			summary := buildOpenAIEnvironmentTimezoneText(tc.text)
			require.Len(t, summary.Matches, len(tc.matches))
			require.Equal(t, tc.open, summary.TimezoneTagOpenCount)
			require.Equal(t, tc.close, summary.TimezoneTagCloseCount)
			for i, expected := range tc.matches {
				actual := summary.Matches[i]
				require.Equal(t, expected.token, actual.Token)
				require.Equal(t, expected.syntax, actual.Syntax, expected.token)
				require.Equal(t, expected.context, actual.XMLContext, expected.token)
			}
		})
	}
}

func TestOpenAIEnvironmentTimezoneTextDetailOverlappingContexts(t *testing.T) {
	const text = "> > ```xml\n> > Asia/Shanghai\n> > ```\n> `UTC`\nplain GMT"
	summary := buildOpenAIEnvironmentTimezoneText(text)
	require.Len(t, summary.Matches, 3)
	fenced := summary.Matches[0]
	require.Equal(t, "Asia/Shanghai", fenced.Token)
	require.Equal(t, 2, fenced.Line)
	require.Equal(t, 2, fenced.QuoteDepth)
	require.True(t, fenced.InFencedCode)
	require.Equal(t, 1, fenced.FenceStartLine)
	inline := summary.Matches[1]
	require.Equal(t, "UTC", inline.Token)
	require.Equal(t, 4, inline.Line)
	require.Equal(t, 1, inline.QuoteDepth)
	require.True(t, inline.InInlineCode)
	require.False(t, inline.InFencedCode)
	require.Zero(t, inline.FenceStartLine)
	ordinary := summary.Matches[2]
	require.Equal(t, "GMT", ordinary.Token)
	require.Zero(t, ordinary.QuoteDepth)
	require.False(t, ordinary.InFencedCode)
	require.False(t, ordinary.InInlineCode)
	require.Zero(t, ordinary.FenceStartLine)
	for _, mention := range summary.Mentions {
		require.Equal(t, mention.Count, mention.FencedLineCount+mention.QuotedLineCount+mention.OtherLineCount, "detail contexts may overlap but aggregate contexts remain exclusive")
	}
}

func TestOpenAIEnvironmentTimezoneTextDetailLimitsAndCompleteCounts(t *testing.T) {
	for _, count := range []int{openAIEnvironmentTimezoneDetailLimit, openAIEnvironmentTimezoneDetailLimit + 1} {
		t.Run(fmt.Sprintf("%d matches", count), func(t *testing.T) {
			text := strings.Repeat("timezone ", count)
			summary := buildOpenAIEnvironmentTimezoneText(text)
			require.Equal(t, "complete", summary.ScanStatus)
			require.Empty(t, summary.Reason)
			require.Equal(t, count, summary.MentionCount)
			require.Len(t, summary.Matches, openAIEnvironmentTimezoneDetailLimit)
			require.Equal(t, count-openAIEnvironmentTimezoneDetailLimit, summary.OmittedMatchCount)
			require.Equal(t, count, summary.MarkerCounts["timezone"])
			require.Zero(t, summary.UnscannedBytes)
			require.Equal(t, len(text), summary.ScannedBytes)
		})
	}
	t.Run("all fixed markers and tags are counted after detail cap", func(t *testing.T) {
		text := strings.Repeat("UTC ", openAIEnvironmentTimezoneDetailLimit) + "<timezone>GMT</timezone> time_zone utc_offset 时区"
		summary := buildOpenAIEnvironmentTimezoneText(text)
		require.Equal(t, "complete", summary.ScanStatus)
		require.Len(t, summary.Matches, openAIEnvironmentTimezoneDetailLimit)
		require.Equal(t, openAIEnvironmentTimezoneDetailLimit+6, summary.MentionCount)
		require.Equal(t, 6, summary.OmittedMatchCount)
		require.Equal(t, map[string]int{"timezone": 2, "time_zone": 1, "utc_offset": 1, "时区": 1}, summary.MarkerCounts)
		require.Equal(t, 1, summary.TimezoneTagOpenCount)
		require.Equal(t, 1, summary.TimezoneTagCloseCount)
		require.Zero(t, summary.UnscannedBytes)
	})
	t.Run("empty text still provides all marker counters", func(t *testing.T) {
		summary := buildOpenAIEnvironmentTimezoneText("")
		require.Equal(t, map[string]int{"timezone": 0, "time_zone": 0, "utc_offset": 0, "时区": 0}, summary.MarkerCounts)
		require.Empty(t, summary.Matches)
		require.Zero(t, summary.OmittedMatchCount)
		require.Zero(t, summary.UnscannedBytes)
	})
	t.Run("marker spelling remains exact while counters are canonical", func(t *testing.T) {
		summary := buildOpenAIEnvironmentTimezoneText("TimeZone TIME_ZONE UTC_OFFSET 时区")
		require.Equal(t, map[string]int{"timezone": 1, "time_zone": 1, "utc_offset": 1, "时区": 1}, summary.MarkerCounts)
		require.Len(t, summary.Matches, 4)
		for i, token := range []string{"TimeZone", "TIME_ZONE", "UTC_OFFSET", "时区"} {
			require.Equal(t, token, summary.Matches[i].Token)
			require.Equal(t, "marker", summary.Matches[i].ValueStatus)
		}
	})
}

func TestOpenAIEnvironmentTimezoneTextDetailValidationAndRedaction(t *testing.T) {
	const secret = "DO_NOT_LOG_detail_private_candidate"
	for _, tc := range []struct {
		text, token, status string
	}{
		{"timezone", "timezone", "marker"},
		{"Asia/Shanghai", "Asia/Shanghai", "valid_timezone"},
		{"America/Los_Angeles", "America/Los_Angeles", "valid_timezone"},
		{"Europe/Paris", "Europe/Paris", "valid_timezone"},
		{"UTC", "UTC", "valid_timezone"},
		{"GMT", "GMT", "valid_timezone"},
		{"UTC+08:00", "UTC+08:00", "valid_offset"},
		{"GMT-07:00", "GMT-07:00", "valid_offset"},
		{"+05:30", "+05:30", "valid_offset"},
		{"-23:59", "-23:59", "valid_offset"},
		{"Asia/" + secret, "", "invalid_iana"},
		{"+24:00", "", "invalid_offset"},
		{"UTC+08:60", "", "invalid_offset"},
	} {
		t.Run(tc.status+" "+tc.text, func(t *testing.T) {
			summary := buildOpenAIEnvironmentTimezoneText(tc.text)
			require.Equal(t, 1, summary.MentionCount)
			require.Len(t, summary.Matches, 1)
			match := summary.Matches[0]
			require.Equal(t, tc.token, match.Token)
			require.Equal(t, tc.status, match.ValueStatus)
			require.Zero(t, match.ByteStart)
			require.Equal(t, len(tc.text), match.ByteEnd)
			encoded, err := json.Marshal(summary)
			require.NoError(t, err)
			require.NotContains(t, string(encoded), secret)
			if tc.token == "" {
				require.NotContains(t, string(encoded), tc.text, "invalid values must not escape through another diagnostic field")
			}
		})
	}
}

func TestOpenAIEnvironmentTimezoneTextDetailUnicodeFoldUsesFixedMarkerKey(t *testing.T) {
	summary := buildOpenAIEnvironmentTimezoneText("utc_offſet")
	require.Equal(t, map[string]int{"timezone": 0, "time_zone": 0, "utc_offset": 1, "时区": 0}, summary.MarkerCounts)
	require.Equal(t, 1, summary.MentionCount)
	require.Len(t, summary.Matches, 1)
	require.Equal(t, "timezone_marker", summary.Matches[0].Kind)
	require.Equal(t, "marker", summary.Matches[0].ValueStatus)
}

func TestOpenAIEnvironmentTimezoneTextDetailBudgetDoesNotInventSyntax(t *testing.T) {
	for _, tagPrefix := range []string{"<timezone", "</timezone"} {
		t.Run("truncated "+tagPrefix+"_secret>", func(t *testing.T) {
			text := strings.Repeat(" ", openAIEnvironmentTimezoneTextLimit-len(tagPrefix)) + tagPrefix + "_secret>"
			summary := buildOpenAIEnvironmentTimezoneText(text)
			require.Equal(t, "limited", summary.ScanStatus)
			require.Equal(t, "text_limit", summary.Reason)
			require.Zero(t, summary.TimezoneTagOpenCount, "a truncated non-timezone tag must not look like an opening timezone tag")
			require.Zero(t, summary.TimezoneTagCloseCount, "a truncated non-timezone tag must not look like a closing timezone tag")
			require.Zero(t, summary.MentionCount)
			require.Positive(t, summary.UnscannedBytes)
		})
	}
	t.Run("assignment delimiter beyond budget cannot classify scanned token", func(t *testing.T) {
		text := "timezone" + strings.Repeat(" ", openAIEnvironmentTimezoneTextLimit) + ":"
		summary := buildOpenAIEnvironmentTimezoneText(text)
		require.Equal(t, "limited", summary.ScanStatus)
		require.Equal(t, "text_limit", summary.Reason)
		require.Len(t, summary.Matches, 1)
		require.Equal(t, "timezone", summary.Matches[0].Token)
		require.Equal(t, "word", summary.Matches[0].Syntax)
		require.Equal(t, len(text)-summary.ScannedBytes, summary.UnscannedBytes)
		require.Positive(t, summary.UnscannedBytes)
	})
}
