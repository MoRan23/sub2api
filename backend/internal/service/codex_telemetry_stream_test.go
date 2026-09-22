package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCodexTelemetryStreamOutputTiming(t *testing.T) {
	started := time.Unix(100, 0)
	var stream codexTelemetryStream
	stream.observe([]byte(`{"type":"response.created","response":{"id":"resp"}}`), "", started, time.Millisecond)
	require.Equal(t, started, stream.result.FirstEventAt)
	require.True(t, stream.result.FirstTokenAt.IsZero())
	require.True(t, stream.result.FirstAgentMessageAt.IsZero())
	stream.observe([]byte(`{"type":"response.function_call_arguments.delta","delta":"private argument"}`), "", started.Add(time.Second), time.Millisecond)
	require.True(t, stream.result.FirstTokenAt.IsZero())
	stream.observe([]byte(`{"type":"response.reasoning_summary_text.delta","delta":"private reasoning"}`), "", started.Add(2*time.Second), time.Millisecond)
	require.Equal(t, started.Add(2*time.Second), stream.result.FirstTokenAt)
	require.True(t, stream.result.FirstAgentMessageAt.IsZero())
	stream.observe([]byte(`{"type":"response.output_item.added","item":{"id":"msg","type":"message","role":"assistant","content":[]}}`), "", started.Add(3*time.Second), time.Millisecond)
	require.Equal(t, started.Add(3*time.Second), stream.result.FirstAgentMessageAt)
	stream.observe([]byte(`{"type":"response.output_text.delta","delta":"private answer"}`), "", started.Add(4*time.Second), time.Millisecond)
	require.Equal(t, started.Add(3*time.Second), stream.result.FirstAgentMessageAt)
	require.Equal(t, started.Add(2*time.Second), stream.result.FirstTokenAt)
}

func TestCodexTelemetryStreamFailureIsStickyAndTypeWins(t *testing.T) {
	for _, event := range []string{"error", "response.failed", "response.incomplete"} {
		t.Run(event, func(t *testing.T) {
			var stream codexTelemetryStream
			at := time.Now()
			stream.observe([]byte(fmt.Sprintf(`{"type":%q,"response":{"id":"r","status":"completed"}}`, event)), "response.completed", at, -1)
			stream.observe([]byte(`{"type":"response.completed","response":{"id":"r","status":"completed"}}`), "", at.Add(time.Second), -1)
			require.NotEqual(t, "completed", stream.result.Status)
			require.EqualValues(t, 1, stream.result.FailedEventCount)
		})
	}
	var fallback codexTelemetryStream
	fallback.observe([]byte(`{"response":{"id":"r","status":"completed"}}`), "response.completed", time.Now(), -1)
	require.Equal(t, "completed", fallback.result.Status)
}

func TestCodexTelemetryStreamDeduplicatesIDsWithoutRetainingPayload(t *testing.T) {
	var stream codexTelemetryStream
	at := time.Now()
	frames := []string{
		`{"type":"response.output_item.added","sequence_number":1,"item":{"id":"fc","call_id":"call1","type":"function_call","arguments":"private argument"}}`,
		`{"type":"response.output_item.done","sequence_number":2,"item":{"id":"fc","call_id":"call1","type":"function_call","arguments":"private argument","end_turn":false}}`,
		`{"type":"response.completed","sequence_number":3,"response":{"id":"r","status":"completed","end_turn":true,"output":[{"id":"fc","call_id":"call1","type":"function_call"}],"usage":{"input_tokens":8,"output_tokens":3}}}`,
	}
	for _, raw := range frames {
		stream.observe([]byte(raw), "", at, time.Millisecond)
	}
	for _, raw := range frames {
		stream.observe([]byte(raw), "", at.Add(time.Second), time.Millisecond)
	}
	require.EqualValues(t, 3, stream.result.EventCount)
	require.Len(t, stream.result.EventMetrics, 3)
	for _, metric := range stream.result.EventMetrics {
		require.EqualValues(t, 1, metric.WaitCount)
	}
	require.Equal(t, []string{"call1"}, stream.result.PendingToolCallIDs)
	require.NotNil(t, stream.result.EndTurn)
	require.False(t, *stream.result.EndTurn)
	require.EqualValues(t, 8, stream.result.InputTokens)
	encoded, err := json.Marshal(stream.result)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "private argument")
}

func TestCodexTelemetryStreamBoundedTimingAndAllowedServerFields(t *testing.T) {
	var stream codexTelemetryStream
	for i := 0; i < 600; i++ {
		stream.observe([]byte(`{"type":"response.output_text.delta","delta":"x"}`), "", time.Now(), time.Millisecond)
	}
	require.EqualValues(t, 600, stream.result.EventCount)
	require.Len(t, stream.result.EventMetrics, 1)
	metric := stream.result.EventMetrics[0]
	require.Equal(t, "response.output_text.delta", metric.Kind)
	require.True(t, metric.Success)
	require.EqualValues(t, 600, metric.Count)
	require.EqualValues(t, 600, metric.WaitCount)
	require.Equal(t, float64(600), metric.WaitSumMS)
	require.Equal(t, float64(1), metric.WaitMinMS)
	require.Equal(t, float64(1), metric.WaitMaxMS)
	require.EqualValues(t, 600, metric.WaitBuckets[1])
	stream.observe([]byte(`{"type":"responsesapi.websocket_timing","timing_metrics":{"engine_service_total_ms":12.5,"engine_iapi_ttft_total_ms":-3,"engine_service_ttft_total_ms":"12","responses_duration_excl_engine_and_client_tool_time_ms":1e999,"private":"secret","other":7}}`), "", time.Now(), 0)
	require.Equal(t, map[string]float64{"engine_service_total_ms": 12.5}, stream.result.ServerTiming)
}

func TestCodexTelemetryWSReadTimingAndControlFrames(t *testing.T) {
	turn := &codexTelemetryWSTurn{}
	at := time.Now()
	turn.sent(at, at.Add(4*time.Millisecond), nil)
	turn.observeRead([]byte(`{"type":"ping"}`), "", at, at.Add(time.Second), nil)
	turn.observeRead([]byte(`{"type":"pong"}`), "", at, at.Add(time.Second), nil)
	require.Zero(t, turn.result.EventCount)
	turn.observeRead([]byte(`{"type":"response.created","response":{"id":"r"}}`), "", at.Add(time.Second), at.Add(time.Second+5*time.Millisecond), nil)
	turn.observeRead([]byte(`{"type":"response.output_text.delta","delta":"answer"}`), "", at.Add(5*time.Second), at.Add(5*time.Second+7*time.Millisecond), nil)
	require.EqualValues(t, 2, turn.result.EventCount)
	require.Len(t, turn.result.EventMetrics, 2)
	require.Equal(t, float64(5), turn.result.EventMetrics[0].WaitSumMS)
	require.Equal(t, float64(7), turn.result.EventMetrics[1].WaitSumMS, "downstream processing gaps must not be counted as read waits")
	require.Equal(t, float64(4), turn.result.SendDurationMS)
	turn.observeRead(nil, "", at.Add(6*time.Second), at.Add(7*time.Second), errors.New("private socket address"))
	require.Empty(t, turn.result.Status, "a receive failure alone does not fabricate an upstream terminal")
	require.False(t, turn.result.ExplicitClientInterrupt)
	require.EqualValues(t, 1, turn.result.FailedEventCount)
	turn.observe([]byte(`{"type":"response.completed","response":{"status":"completed"}}`), "")
	require.Equal(t, "completed", turn.result.Status, "a cancelled read can be retried on the same physical stream")
}

func TestCodexTelemetryWSCancelRequiresUpstreamConfirmation(t *testing.T) {
	turn := &codexTelemetryWSTurn{}
	turn.requestCancel()
	require.False(t, turn.result.ExplicitClientInterrupt)
	turn.observe([]byte(`{"type":"response.cancelled","response":{"status":"cancelled"}}`), "")
	require.True(t, turn.result.ExplicitClientInterrupt)
}

func TestCodexTelemetryWSRepairedDocumentsRemainOnePhysicalFrame(t *testing.T) {
	turn := &codexTelemetryWSTurn{}
	at := time.Now()
	turn.observeRead([]byte(`{"type":"response.created","response":{"id":"r"}}`), "", at, at.Add(time.Millisecond), nil)
	turn.observeBuffered([]byte(`{"type":"response.output_text.delta","delta":"answer"}`))
	turn.observeBuffered([]byte(`{"type":"response.failed","response":{"status":"failed"}}`))
	require.EqualValues(t, 1, turn.result.EventCount)
	require.Zero(t, turn.result.FailedEventCount, "buffered document parsing updates metadata, not physical frame statistics")
	require.Len(t, turn.result.EventMetrics, 1)
	require.Equal(t, "response.created", turn.result.EventMetrics[0].Kind)
	require.EqualValues(t, 1, turn.result.EventMetrics[0].Count)
	require.Equal(t, float64(1), turn.result.EventMetrics[0].WaitSumMS)
	require.True(t, turn.result.EventMetrics[0].Success)
	require.Equal(t, "failed", turn.result.Status)
	require.Equal(t, at.Add(time.Millisecond), turn.result.FirstTokenAt)
}

func TestCodexTelemetryWSDeliveryDoesNotRewriteUpstreamTerminal(t *testing.T) {
	turn := &codexTelemetryWSTurn{}
	turn.observe([]byte(`{"type":"response.completed","response":{"status":"completed"}}`), "")
	turn.markDelivery(false)
	turn.markDelivery(true)
	require.Equal(t, "completed", turn.result.Status)
	require.Equal(t, "rejected", turn.result.DeliveryStatus, "draining the upstream stream cannot undo a downstream write failure")
	failure := &codexTelemetryWSTurn{}
	failure.observe([]byte(`{"type":"response.failed","response":{"status":"failed"}}`), "")
	failure.markDelivery(true)
	require.Equal(t, "failed", failure.result.Status)
	require.Equal(t, "delivered", failure.result.DeliveryStatus, "an upstream failure event can be delivered successfully")
}

func TestCodexTelemetryWSSendOutcomeIsIndependentOfResponse(t *testing.T) {
	turn := &codexTelemetryWSTurn{}
	turn.observe([]byte(`{"type":"response.completed","response":{"status":"completed"}}`), "")
	at := time.Now()
	turn.sent(at, at.Add(time.Millisecond), errors.New("write acknowledgement lost"))
	turn.writeFailed()
	require.Equal(t, "completed", turn.result.Status)
	require.NotNil(t, turn.result.SendSucceeded)
	require.False(t, *turn.result.SendSucceeded)
}

func TestCodexTelemetryStreamMetricsSeparateSuccessAndPreserveAllWaits(t *testing.T) {
	var stream codexTelemetryStream
	at := time.Now()
	for i := 0; i < 600; i++ {
		status := "completed"
		if i%2 != 0 {
			status = "failed"
		}
		stream.observe([]byte(fmt.Sprintf(`{"type":"response.done","response":{"id":"response-%d","status":%q}}`, i, status)), "", at, time.Duration(i+1)*time.Millisecond)
	}
	require.EqualValues(t, 600, stream.result.EventCount)
	require.EqualValues(t, 300, stream.result.FailedEventCount)
	require.Len(t, stream.result.EventMetrics, 2)
	var sum float64
	for _, metric := range stream.result.EventMetrics {
		require.Equal(t, "response.done", metric.Kind)
		require.EqualValues(t, 300, metric.Count)
		require.EqualValues(t, 300, metric.WaitCount)
		var buckets uint64
		for _, count := range metric.WaitBuckets {
			buckets += count
		}
		require.Equal(t, metric.WaitCount, buckets)
		sum += metric.WaitSumMS
	}
	require.Equal(t, float64(600*601/2), sum)
}

func TestCodexTelemetryStreamMetricsOverflowKeepsBothOutcomes(t *testing.T) {
	var stream codexTelemetryStream
	for i := 0; i < 600; i++ {
		stream.recordKnownEventMetric(fmt.Sprintf("future_reviewed_kind_%d", i), true, time.Millisecond)
	}
	for i := 0; i < 20; i++ {
		stream.recordKnownEventMetric("parse_error", false, 2*time.Millisecond)
	}
	require.EqualValues(t, 620, stream.result.EventCount)
	require.EqualValues(t, 20, stream.result.FailedEventCount)
	require.Len(t, stream.result.EventMetrics, codexTelemetryMaxEventKinds)
	require.Len(t, stream.eventMetrics, codexTelemetryMaxEventKinds)
	var count, waits uint64
	var sum float64
	unknownOutcomes := map[bool]uint64{}
	for _, metric := range stream.result.EventMetrics {
		count += metric.Count
		waits += metric.WaitCount
		sum += metric.WaitSumMS
		if metric.Kind == "unknown" {
			unknownOutcomes[metric.Success] = metric.Count
		}
	}
	require.EqualValues(t, 620, count)
	require.EqualValues(t, 620, waits)
	require.Equal(t, float64(640), sum)
	require.Equal(t, map[bool]uint64{true: 474, false: 20}, unknownOutcomes)
}

func TestCodexTelemetryStreamUnknownKindsAndHTTPDurationAbsence(t *testing.T) {
	var stream codexTelemetryStream
	at := time.Now()
	for _, raw := range []string{`{}`, `{"type":"private text\nmessage"}`, `{"type":"response.output_text.delta","delta":"text"}`} {
		stream.observe([]byte(raw), "", at, -1)
	}
	stream.observe([]byte("[DONE]"), "", at, 5*time.Millisecond)
	require.EqualValues(t, 3, stream.result.EventCount)
	require.Len(t, stream.result.EventMetrics, 2)
	require.Equal(t, "unknown", stream.result.EventMetrics[0].Kind)
	require.EqualValues(t, 2, stream.result.EventMetrics[0].Count)
	for _, metric := range stream.result.EventMetrics {
		require.Zero(t, metric.WaitCount)
		require.Empty(t, metric.WaitBuckets)
		require.Zero(t, metric.WaitSumMS)
	}
}

func TestCodexTelemetryRuntimeCopiesEventMetricBuckets(t *testing.T) {
	result := CodexTelemetryResult{EventCount: 2, FailedEventCount: 1, EventMetrics: []CodexTelemetryEventMetric{
		{Kind: "response.completed", Success: true, Count: 1, WaitCount: 1, WaitSumMS: 5, WaitMinMS: 5, WaitMaxMS: 5, WaitBuckets: []uint64{0, 1}},
		{Kind: "error", Success: false, Count: 1, WaitCount: 1, WaitSumMS: 7, WaitMinMS: 7, WaitMaxMS: 7, WaitBuckets: []uint64{0, 0, 1}},
	}}
	copy := copyCodexTelemetryRuntimeResult(result)
	result.EventMetrics[0].Count = 9
	result.EventMetrics[0].WaitBuckets[1] = 9
	result.EventMetrics[1].WaitBuckets[2] = 8
	require.EqualValues(t, 1, copy.EventMetrics[0].Count)
	require.Equal(t, []uint64{0, 1}, copy.EventMetrics[0].WaitBuckets)
	require.Equal(t, []uint64{0, 0, 1}, copy.EventMetrics[1].WaitBuckets)
	var turn CodexTelemetryResult
	mergeRuntimeResult(&turn, copy)
	require.Empty(t, turn.EventMetrics, "attempt histograms must not be persisted in logical turn summaries")
	require.Zero(t, turn.EventCount)
	require.Zero(t, turn.FailedEventCount)
	require.NotEmpty(t, copy.EventMetrics)
}

func TestCodexTelemetryStreamEventKindsUseFixedProtocolVocabulary(t *testing.T) {
	known := []string{
		"response.created", "response.metadata", "codex.response.metadata", "response.output_item.done",
		"response.content_part.done", "response.reasoning_summary_part.added", "response.reasoning_summary_text.done",
		"response.reasoning_text.delta", "response.custom_tool_call_input.delta", "response.function_call_arguments.done",
		"response.output_text.delta", "response.refusal.delta", "response.mcp_call_arguments.delta",
		"response.image_generation_call.partial_image", "response.audio_transcript.done", "response.output_audio.delta",
		"response.completed", "response.done", "response.cancelled", "responsesapi.websocket_timing", "parse_error", "error",
	}
	for _, kind := range known {
		require.Equal(t, kind, codexTelemetryEventKind(kind))
	}
	unknown := []string{
		"", "4b857f6e-c8b4-4e81-923f-03419f65bcef", "sk-private-credential-value",
		"eyJhbGciOiJIUzI1NiJ9.eyJzZWNyZXQiOiJwcml2YXRlIn0.signature",
		"response.new_tool_event", "response.output_text.delta.secret", "private text",
	}
	var stream codexTelemetryStream
	for _, kind := range unknown {
		require.Equal(t, "unknown", codexTelemetryEventKind(kind))
		raw, err := json.Marshal(map[string]string{"type": kind})
		require.NoError(t, err)
		stream.observe(raw, "", time.Now(), time.Millisecond)
	}
	require.Len(t, stream.result.EventMetrics, 1)
	require.Equal(t, "unknown", stream.result.EventMetrics[0].Kind)
	require.EqualValues(t, len(unknown), stream.result.EventMetrics[0].Count)
	encoded, err := json.Marshal(stream.result)
	require.NoError(t, err)
	for _, raw := range unknown[1:] {
		require.NotContains(t, string(encoded), raw)
	}
}
