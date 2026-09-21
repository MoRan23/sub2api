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
	require.Len(t, stream.result.EventWaitDurationsMS, 3)
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
	require.Len(t, stream.result.EventWaitDurationsMS, codexTelemetryMaxEventSamples)
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
	require.Equal(t, []float64{5, 7}, turn.result.EventWaitDurationsMS, "downstream processing gaps must not be counted as read waits")
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
	require.EqualValues(t, 1, turn.result.FailedEventCount)
	require.Equal(t, []float64{1}, turn.result.EventWaitDurationsMS)
	require.Equal(t, []bool{true}, turn.result.EventWaitFailed)
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
