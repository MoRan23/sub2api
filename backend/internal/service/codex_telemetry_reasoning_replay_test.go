package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestCodexReasoningReplayOptionalReportAndReasons(t *testing.T) {
	for _, tc := range []struct {
		name, reportJSON, report string
		counts                   CodexReasoningRemovalCounts
	}{
		{"absent", "", "unavailable", CodexReasoningRemovalCounts{}},
		{"null", `,"input_transformations":null`, "unavailable", CodexReasoningRemovalCounts{}},
		{"object", `,"input_transformations":{}`, "unavailable", CodexReasoningRemovalCounts{}},
		{"empty", `,"input_transformations":[]`, "no_removals_reported", CodexReasoningRemovalCounts{}},
		{"unrelated", `,"input_transformations":[null,1,{"type":"message","action":"removed"},{"type":"reasoning","action":"kept"}]`, "no_removals_reported", CodexReasoningRemovalCounts{}},
		{"unknown", `,"input_transformations":[{"type":"reasoning","action":"removed","reason":"private-new-reason"},{"type":"reasoning","action":"removed"},{"type":"reasoning","action":"removed","reason":123}]`, "removals_reported", CodexReasoningRemovalCounts{Other: 3}},
		{"six", `,"input_transformations":[{"type":"reasoning","action":"removed","reason":"organization_mismatch"},{"type":"reasoning","action":"removed","reason":"end_user_mismatch"},{"type":"reasoning","action":"removed","reason":"model_mismatch"},{"type":"reasoning","action":"removed","reason":"prefix_mismatch"},{"type":"reasoning","action":"removed","reason":"turn_boundary"},{"type":"reasoning","action":"removed","reason":"other"}]`, "prefix_mismatch_reported", CodexReasoningRemovalCounts{1, 1, 1, 1, 1, 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			frame := `{"type":"response.completed","response":{"id":"r","status":"completed"` + tc.reportJSON + `}}`
			var stream codexTelemetryStream
			stream.observe([]byte(frame), "", time.Now(), -1)
			require.Equal(t, &CodexReasoningReplayEvidence{Report: tc.report, RemovedCounts: tc.counts}, stream.result.ReasoningReplay)
			var evidence codexModelEvidenceObserver
			evidence.observePayload([]byte(frame))
			require.Equal(t, stream.result.ReasoningReplay, evidence.snapshot("").ReasoningReplay)
			encoded, err := json.Marshal(stream.result.ReasoningReplay)
			require.NoError(t, err)
			require.NotContains(t, string(encoded), "private-new-reason")
		})
	}
}

func TestCodexReasoningReplayOnlyCompletedSemantics(t *testing.T) {
	for _, tc := range []struct {
		name, frame string
		recorded    bool
	}{
		{"completed", `{"type":"response.completed","response":{"id":"r"}}`, true},
		{"interrupted", `{"type":"response.incomplete","response":{"id":"r","status":"incomplete","incomplete_details":{"reason":"interrupted"}}}`, true},
		{"buffered", `{"id":"r","status":"completed","input_transformations":[]}`, true},
		{"content-filter", `{"type":"response.incomplete","response":{"id":"r","incomplete_details":{"reason":"content_filter"}}}`, false},
		{"limit", `{"type":"response.incomplete","response":{"id":"r","incomplete_details":{"reason":"max_output_tokens"}}}`, false},
		{"incomplete", `{"type":"response.incomplete","response":{"id":"r"}}`, false},
		{"failed", `{"type":"response.failed","response":{"id":"r","status":"completed"}}`, false},
		{"error", `{"type":"response.completed","response":{"id":"r","error":{"code":"bad"}}}`, false},
		{"conflict", `{"type":"response.completed","response":{"id":"r","status":"failed"}}`, false},
		{"created", `{"type":"response.created","response":{"id":"r","input_transformations":[]}}`, false},
		{"invalid", `{"type":"response.completed","response":{"input_transformations":[]}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stream codexTelemetryStream
			stream.observe([]byte(tc.frame), "", time.Now(), -1)
			require.Equal(t, tc.recorded, stream.result.ReasoningReplay != nil)
			var evidence codexModelEvidenceObserver
			evidence.observePayload([]byte(tc.frame))
			require.Equal(t, tc.recorded, evidence.snapshot("").ReasoningReplay != nil)
		})
	}
	var failed codexTelemetryStream
	failed.observe([]byte(`{"type":"response.failed","response":{"id":"r"}}`), "", time.Now(), -1)
	failed.observe([]byte(`{"type":"response.completed","response":{"id":"r"}}`), "", time.Now(), -1)
	failed.readFailed(time.Now(), -1)
	require.Nil(t, failed.result.ReasoningReplay)
}

func TestCodexReasoningReplayAttemptAndOwnership(t *testing.T) {
	at := time.Now()
	frame := []byte(`{"type":"response.completed","response":{"id":"r","input_transformations":[{"type":"reasoning","action":"removed","reason":"prefix_mismatch"}]}}`)
	var stream codexTelemetryStream
	stream.observe(frame, "", at, -1)
	stream.observe(frame, "", at.Add(time.Second), -1)
	stream.readFailed(at.Add(2*time.Second), -1)
	require.EqualValues(t, 1, stream.result.ReasoningReplay.RemovedCounts.PrefixMismatch, "delivery failure does not erase upstream evidence")
	copy := copyCodexTelemetryRuntimeResult(stream.result)
	stream.result.ReasoningReplay.RemovedCounts.PrefixMismatch = 99
	require.EqualValues(t, 1, copy.ReasoningReplay.RemovedCounts.PrefixMismatch)
	var merged CodexTelemetryResult
	mergeRuntimeResult(&merged, copy)
	require.Nil(t, merged.ReasoningReplay, "physical report never enters a logical turn")
	var old CodexTelemetryResult
	require.NoError(t, json.Unmarshal([]byte(`{"Status":"completed"}`), &old))
	require.Nil(t, old.ReasoningReplay, "old snapshots stay unrecorded")

	ws := &codexTelemetryWSTurn{}
	ws.observeRead(frame, "", at, at.Add(time.Second), nil)
	ws.markDelivery(false)
	ws.observeRead(nil, "", at.Add(time.Second), at.Add(2*time.Second), errors.New("client closed"))
	require.EqualValues(t, 1, ws.result.ReasoningReplay.RemovedCounts.PrefixMismatch)
	recorder := &codexTelemetryHTTPRecorder{}
	http := &codexTelemetryHTTPCollector{attempt: recorder, status: 200}
	http.observe(frame, "")
	http.complete(errors.New("downstream delivery failed"))
	http.complete(nil)
	require.Len(t, recorder.retried, 1)
	require.Empty(t, recorder.finished)
	require.EqualValues(t, 1, recorder.retried[0].ReasoningReplay.RemovedCounts.PrefixMismatch)
	for i := range frame {
		frame[i] = ' '
	}
	require.Equal(t, "prefix_mismatch_reported", copy.ReasoningReplay.Report)
}

func TestCodexReasoningReplayMetricsAndV4Snapshot(t *testing.T) {
	for _, report := range []string{"unavailable", "no_removals_reported", "removals_reported", "prefix_mismatch_reported"} {
		t.Run(report, func(t *testing.T) {
			profile := codexMetricsTestProfile()
			profile.simulationEnabled = false
			profile.input.ProductSKU = "private-sku"
			store := newCodexTelemetryMetricStore()
			replay := &CodexReasoningReplayEvidence{Report: report}
			if report == "removals_reported" {
				replay.RemovedCounts.Other = 300
			}
			if report == "prefix_mismatch_reported" {
				replay.RemovedCounts.PrefixMismatch = 3
			}
			result := codexTelemetryTerminal{status: "failed", finished: profile.started.Add(time.Second), result: CodexTelemetryResult{ReasoningReplay: replay}}
			store.recordAttempt(profile, result)
			encoded, err := marshalCodexTelemetryMetricStore(store)
			require.NoError(t, err)
			require.EqualValues(t, 4, gjson.GetBytes(encoded, "version").Int())
			restored, err := unmarshalCodexTelemetryMetricStore(encoded)
			require.NoError(t, err)
			again, err := marshalCodexTelemetryMetricStore(restored)
			require.NoError(t, err)
			require.JSONEq(t, string(encoded), string(again))
			batches := restored.flush(profile.started.Add(2 * time.Minute))
			require.Len(t, batches, 1)
			require.Equal(t, "observed", batches[0].source)
			counter := codexMetricsTestMetric(batches[0].body, "codex.responses.reasoning_replay").Get("sum.dataPoints.0")
			require.EqualValues(t, 1, counter.Get("asInt").Int())
			require.Equal(t, report, codexMetricsTestAttribute(counter, "report"))
			require.Equal(t, "other", codexMetricsTestAttribute(counter, "product_sku"))
			points := codexMetricsTestMetric(batches[0].body, "codex.responses.reasoning_items_removed").Get("histogram.dataPoints").Array()
			if report == "unavailable" {
				require.Empty(t, points)
				return
			}
			require.Len(t, points, 6)
			for _, point := range points {
				require.EqualValues(t, 1, point.Get("count").Uint())
				require.JSONEq(t, `[0,1,2,4,8,16,32,64,128,256]`, point.Get("explicitBounds").Raw)
				reason := codexMetricsTestAttribute(point, "reason")
				require.Contains(t, codexReasoningRemovalReasons, reason)
				if reason == "other" && report == "removals_reported" {
					require.EqualValues(t, 1, point.Get("bucketCounts.10").Uint())
				} else if reason == "prefix_mismatch" && report == "prefix_mismatch_reported" {
					require.EqualValues(t, 1, point.Get("bucketCounts.3").Uint())
				} else {
					require.EqualValues(t, 1, point.Get("bucketCounts.0").Uint())
				}
			}
		})
	}
}

func TestCodexReasoningReplayDisabledObservationDoesNotSimulate(t *testing.T) {
	profile := codexMetricsTestProfile()
	profile.observationEnabled = false
	store := newCodexTelemetryMetricStore()
	store.touch(profile)
	result := codexTelemetryTerminal{status: "completed", finished: profile.started.Add(time.Second), result: CodexTelemetryResult{ReasoningReplay: &CodexReasoningReplayEvidence{Report: "no_removals_reported"}}}
	store.recordAttempt(profile, result)
	store.record(profile, result)
	for _, batch := range store.flush(profile.started.Add(2 * time.Minute)) {
		require.NotContains(t, string(batch.body), "codex.responses.reasoning")
	}
}

func TestCodexReasoningReplayRuntimeDedupAndLegacySnapshots(t *testing.T) {
	store := NewMemoryCodexTelemetryStore()
	now := time.Now().UTC()
	attempt := runtimeTestAttempt(now, "windows", "turn", "sample")
	runtimeTestMutation(t, store, attempt, nil, false)
	result := CodexTelemetryResult{Status: "completed", HTTPStatus: 200, FinishedAt: now.Add(time.Second), ReasoningReplay: &CodexReasoningReplayEvidence{Report: "no_removals_reported"}}
	runtimeTestMutation(t, store, attempt, &result, false)
	runtimeTestMutation(t, store, attempt, &result, false)
	require.Nil(t, runtimeTestReadTurn(t, store, attempt).Result.ReasoningReplay)
	_, err := store.TransactPool(context.Background(), attempt.poolKey, now, func(tx *CodexTelemetryPoolTransaction) error {
		metrics, err := loadRuntimeMetrics(tx)
		if err != nil {
			return err
		}
		seen := 0
		for _, state := range metrics.states {
			for _, metric := range state.pending {
				if metric.descriptor.name == "codex.responses.reasoning_replay" {
					seen++
					require.EqualValues(t, 1, metric.sum)
				}
			}
		}
		require.Equal(t, 1, seen)
		return nil
	})
	require.NoError(t, err)
	for _, version := range []int{1, 2, 3} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			original := newCodexTelemetryMetricStore()
			profile := codexMetricsTestProfile()
			profile.simulationEnabled = false
			original.recordAttempt(profile, codexTelemetryTerminal{status: "completed", finished: now, result: CodexTelemetryResult{}})
			encoded, err := marshalCodexTelemetryMetricStore(original)
			require.NoError(t, err)
			var snapshot codexMetricStoreSnapshot
			require.NoError(t, json.Unmarshal(encoded, &snapshot))
			snapshot.Version = version
			encoded, err = json.Marshal(snapshot)
			require.NoError(t, err)
			restored, err := unmarshalCodexTelemetryMetricStore(encoded)
			require.NoError(t, err)
			encoded, err = marshalCodexTelemetryMetricStore(restored)
			require.NoError(t, err)
			require.NotContains(t, string(encoded), "reasoning_replay")
			require.EqualValues(t, 4, gjson.GetBytes(encoded, "version").Int())
		})
	}
}

func BenchmarkCodexReasoningReplaySummary(b *testing.B) {
	for _, size := range []int{0, 64 << 10, 1 << 20, 8 << 20} {
		payload := []byte(`{"type":"response.completed","response":{"id":"r","input_transformations":[{"type":"reasoning","action":"removed","reason":"prefix_mismatch"}],"output":[{"text":"` + strings.Repeat("x", size) + `"}]}}`)
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				// Same borrowed immutable view used by fingerprint observation.
				v := codexReasoningReplayFromTerminal(gjson.Parse(openAIWSPayloadStringView(payload)), "response.completed")
				if v == nil || v.RemovedCounts.PrefixMismatch != 1 {
					b.Fatal(v)
				}
			}
		})
	}
}
