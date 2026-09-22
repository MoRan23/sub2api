package service

import (
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCodexTelemetryMetricsNativeByteHistogramBounds(t *testing.T) {
	for _, test := range []struct {
		name   string
		bounds []float64
	}{
		{"codex.app_server.codex_home.size_bytes", []float64{1048576, 10485760, 104857600, 1073741824, 10737418240, 107374182400, 1099511627776}},
		{"codex.sqlite.logs.write.bytes", []float64{128, 256, 512, 1024, 2048, 4096, 8192, 16384, 32768, 65536, 131072, 262144, 524288, 1048576, 2097152, 4194304, 8388608, 16777216}},
		{"codex.sqlite.logs.write.max_entry_bytes", []float64{128, 256, 512, 1024, 2048, 4096, 8192, 16384, 32768, 65536, 131072, 262144, 524288, 1048576, 2097152, 4194304, 8388608, 16777216}},
	} {
		t.Run(test.name, func(t *testing.T) {
			descriptor := codexMetricDescriptor{name: test.name, kind: "histogram"}
			require.Equal(t, test.bounds, codexMetricBounds(descriptor))
			aggregate := codexMetricAggregate{descriptor: descriptor}
			first, last := test.bounds[0], test.bounds[len(test.bounds)-1]
			for _, value := range []float64{first, first + 1, last, last + 1} {
				aggregate.observe(value, time.Unix(100, 0), time.Unix(101, 0))
			}
			point := aggregate.otlp()["histogram"].(map[string]any)["dataPoints"].([]any)[0].(map[string]any)
			require.Equal(t, test.bounds, point["explicitBounds"])
			buckets := make([]string, len(test.bounds)+1)
			for index := range buckets {
				buckets[index] = "0"
			}
			buckets[0], buckets[1], buckets[len(test.bounds)-1], buckets[len(test.bounds)] = "1", "1", "1", "1"
			require.Equal(t, buckets, point["bucketCounts"])
			require.Equal(t, "4", point["count"])
		})
	}
	require.Equal(t, codexValueHistogramBounds, codexMetricBounds(codexMetricDescriptor{name: "codex.rollout.size_bytes", kind: "histogram"}))
	require.Equal(t, codexHistogramBounds, codexMetricBounds(codexMetricDescriptor{name: "codex.sse_event.duration_ms", kind: "histogram", unit: "ms"}))
}

func TestCodexTelemetryMetricsHomeDirectoriesRemainSimulated(t *testing.T) {
	store, profile := newCodexTelemetryMetricStore(), codexMetricsTestProfile()
	store.touch(profile)
	batches := store.flush(profile.started.Add(time.Minute))
	require.Len(t, batches, 1)
	metric := codexMetricsTestMetric(batches[0].body, "codex.app_server.codex_home.size_bytes")
	points := metric.Get("histogram.dataPoints").Array()
	require.Len(t, points, 2)
	directories := map[string]bool{}
	for _, point := range points {
		directories[codexMetricsTestAttribute(point, "directory")] = true
		require.Equal(t, "false", codexMetricsTestAttribute(point, "compression_enabled"))
		require.EqualValues(t, 1, point.Get("count").Uint())
	}
	require.Equal(t, map[string]bool{"sessions": true, "archived_sessions": true}, directories)
	require.Equal(t, "simulated", batches[0].source)
	second := newCodexTelemetryMetricStore()
	second.touch(profile)
	repeated := second.flush(profile.started.Add(time.Minute))
	require.JSONEq(t, metric.Raw, codexMetricsTestMetric(repeated[0].body, "codex.app_server.codex_home.size_bytes").Raw)
}

func TestCodexTelemetryMetricsMergeCompleteEventDistributions(t *testing.T) {
	store, profile := newCodexTelemetryMetricStore(), codexMetricsTestProfile()
	profile.simulationEnabled, profile.websocket = false, true
	store.touch(profile)
	buckets := make([]uint64, len(codexHistogramBounds)+1)
	buckets[1] = 600
	event := CodexTelemetryEventMetric{Kind: "response.output_text.delta", Success: true, Count: 600,
		WaitCount: 600, WaitSumMS: 600, WaitMinMS: 1, WaitMaxMS: 1, WaitBuckets: buckets}
	store.recordAttempt(profile, codexTelemetryTerminal{finished: profile.started.Add(time.Second), result: CodexTelemetryResult{EventMetrics: []CodexTelemetryEventMetric{event}}})
	store.recordAttempt(profile, codexTelemetryTerminal{finished: profile.started.Add(2 * time.Second), result: CodexTelemetryResult{EventMetrics: []CodexTelemetryEventMetric{
		codexMetricsTestEventMetric("response.output_text.delta", true, 120001),
		codexMetricsTestEventMetric("response.failed", false, 5),
	}}})
	batches := store.flush(profile.started.Add(time.Minute))
	require.Len(t, batches, 1)
	points := codexMetricsTestMetric(batches[0].body, "codex.websocket.event.duration_ms").Get("histogram.dataPoints").Array()
	require.Len(t, points, 2)
	for _, point := range points {
		switch codexMetricsTestAttribute(point, "kind") {
		case "response.output_text.delta":
			require.Equal(t, "true", codexMetricsTestAttribute(point, "success"))
			require.EqualValues(t, 601, point.Get("count").Uint())
			require.Equal(t, float64(120601), point.Get("sum").Float())
			require.Equal(t, float64(1), point.Get("min").Float())
			require.Equal(t, float64(120001), point.Get("max").Float())
			require.EqualValues(t, 600, point.Get("bucketCounts.1").Uint())
			require.EqualValues(t, 1, point.Get("bucketCounts."+strconv.Itoa(len(codexHistogramBounds))).Uint())
		case "response.failed":
			require.Equal(t, "false", codexMetricsTestAttribute(point, "success"))
			require.EqualValues(t, 1, point.Get("count").Uint())
		default:
			t.Fatalf("unexpected event kind: %s", point.Raw)
		}
	}
	counts := codexMetricsTestMetric(batches[0].body, "codex.websocket.event").Get("sum.dataPoints").Array()
	require.Len(t, counts, 2)
	var total uint64
	for _, point := range counts {
		total += point.Get("asInt").Uint()
	}
	require.EqualValues(t, 602, total)
	require.Empty(t, store.flush(profile.started.Add(2*time.Minute)), "delta data must not be exported again")
}

func TestCodexTelemetryMetricsCountOnlyEventsHaveNoWaitSample(t *testing.T) {
	for _, aggregate := range []bool{false, true} {
		store, profile := newCodexTelemetryMetricStore(), codexMetricsTestProfile()
		profile.simulationEnabled = false
		store.touch(profile)
		result := CodexTelemetryResult{EventCount: 3, FailedEventCount: 1}
		if aggregate {
			result.EventMetrics = []CodexTelemetryEventMetric{{Kind: "response.created", Success: true, Count: 2}, {Kind: "response.failed", Success: false, Count: 1}}
		}
		store.recordAttempt(profile, codexTelemetryTerminal{finished: profile.started.Add(time.Second), result: result})
		batch := store.flush(profile.started.Add(time.Minute))[0]
		require.False(t, codexMetricsTestMetric(batch.body, "codex.sse_event.duration_ms").Exists())
		points := codexMetricsTestMetric(batch.body, "codex.sse_event").Get("sum.dataPoints").Array()
		require.Len(t, points, 2)
		if !aggregate {
			for _, point := range points {
				require.Equal(t, "unknown", codexMetricsTestAttribute(point, "kind"))
			}
		}
	}
}
