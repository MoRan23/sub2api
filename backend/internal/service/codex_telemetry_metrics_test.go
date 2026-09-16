package service

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func codexMetricsTestProfile() codexTelemetryProfile {
	return codexTelemetryProfile{
		client: codexTelemetryClient{
			localID: 7, name: "Test account", accountID: "upstream-account",
			userAgent: "codex_cli_rs/0.154.0 (Linux 6.8; x86_64)", originator: "codex_cli_rs", version: "0.154.0",
			accessToken: "credential-not-in-payload", proxyURL: "http://user:proxy-secret@localhost:8888",
		},
		sessionID: "root-session", threadID: "thread", turnID: "turn-one", model: "gpt-5.4",
		started: time.Unix(1_800_000_000, 0),
	}
}

func codexMetricsTestMetric(body []byte, name string) gjson.Result {
	for _, metric := range gjson.GetBytes(body, "resourceMetrics.0.scopeMetrics.0.metrics").Array() {
		if metric.Get("name").String() == name {
			return metric
		}
	}
	return gjson.Result{}
}

func codexMetricsTestAttribute(point gjson.Result, name string) string {
	for _, attribute := range point.Get("attributes").Array() {
		if attribute.Get("key").String() == name {
			return attribute.Get("value.stringValue").String()
		}
	}
	return ""
}

func TestCodexTelemetryMetricsDescriptorContract(t *testing.T) {
	require.Len(t, codexMetricDescriptors, 66)
	names := make(map[string]bool)
	for _, descriptor := range codexMetricDescriptors {
		require.False(t, names[descriptor.name], "duplicate definition: %s", descriptor.name)
		names[descriptor.name] = true
		require.Contains(t, []string{"sum", "histogram"}, descriptor.kind)
	}
}

func TestCodexTelemetryMetricsHistogramKeepsEachObservation(t *testing.T) {
	store := newCodexTelemetryMetricStore()
	profile := codexMetricsTestProfile()
	store.touch(profile)
	store.record(profile, codexTelemetryTerminal{status: "completed", finished: profile.started.Add(100 * time.Millisecond)})
	profile.turnID = "turn-two"
	store.record(profile, codexTelemetryTerminal{status: "completed", finished: profile.started.Add(300 * time.Millisecond)})
	batches := store.flush(profile.started.Add(time.Minute))
	require.Len(t, batches, 1)
	require.Equal(t, 2, batches[0].turns)
	point := codexMetricsTestMetric(batches[0].body, "codex.turn.e2e_duration_ms").Get("histogram.dataPoints.0")
	require.EqualValues(t, 2, point.Get("count").Uint())
	require.Equal(t, float64(400), point.Get("sum").Float())
	require.Equal(t, float64(100), point.Get("min").Float())
	require.Equal(t, float64(300), point.Get("max").Float())
	buckets := point.Get("bucketCounts").Array()
	var count uint64
	for _, bucket := range buckets {
		count += bucket.Uint()
	}
	require.EqualValues(t, 2, count)
	require.EqualValues(t, 1, buckets[6].Uint(), "100 ms belongs to <=100 bucket")
	require.EqualValues(t, 1, buckets[8].Uint(), "300 ms belongs to <=500 bucket")
	hooks := codexMetricsTestMetric(batches[0].body, "codex.hooks.run.duration_ms").Get("histogram.dataPoints.0")
	require.EqualValues(t, 8, hooks.Get("count").Uint())
	require.Equal(t, float64(8), hooks.Get("sum").Float())
	require.Empty(t, store.flush(profile.started.Add(2*time.Minute)), "delta samples must not be resent")
}

func TestCodexTelemetryMetricsPartitionResourceAndModel(t *testing.T) {
	store := newCodexTelemetryMetricStore()
	base := codexMetricsTestProfile()
	profiles := []codexTelemetryProfile{base, base, base, base, base}
	profiles[1].model = "gpt-5.5"
	profiles[2].client.version = "0.155.0"
	profiles[3].client.localID = 8
	profiles[4].client.userAgent = "codex_vscode/0.154.0 (Mac OS 15.0; arm64)"
	profiles[4].client.originator = "codex_vscode"
	for _, profile := range profiles {
		store.touch(profile)
		store.record(profile, codexTelemetryTerminal{status: "completed", finished: profile.started.Add(100 * time.Millisecond)})
	}
	batches := store.flush(base.started.Add(time.Minute))
	require.Len(t, batches, len(profiles))
	for _, batch := range batches {
		point := codexMetricsTestMetric(batch.body, "codex.turn.e2e_duration_ms").Get("histogram.dataPoints.0")
		require.Equal(t, batch.profile.model, codexMetricsTestAttribute(point, "model"))
		require.Equal(t, batch.profile.client.version, codexMetricsTestAttribute(point, "app.version"))
		require.EqualValues(t, 1, point.Get("count").Uint())
		require.Equal(t, 1, batch.turns)
		require.Empty(t, batch.profile.sessionID)
		require.Empty(t, batch.profile.threadID)
		require.Empty(t, batch.profile.turnID)
		require.NotContains(t, string(batch.body), "credential-not-in-payload")
		require.NotContains(t, string(batch.body), "proxy-secret")
		require.NotContains(t, string(batch.body), "root-session")
		require.NotContains(t, string(batch.body), "turn-one")
	}
}

func TestCodexTelemetryMetricsHTTPHasNoWebSocketOrInventedTiming(t *testing.T) {
	store := newCodexTelemetryMetricStore()
	profile := codexMetricsTestProfile()
	startup := store.touch(profile)
	require.Len(t, startup, 1)
	require.Equal(t, 0, startup[0].turns)
	require.Empty(t, store.touch(profile), "startup sent once per client partition")
	anotherModel := profile
	anotherModel.model = "gpt-5.5"
	require.Empty(t, store.touch(anotherModel), "changing models does not start a new client process")
	for _, name := range startup[0].names {
		require.False(t, strings.HasPrefix(name, "codex.websocket."))
		require.False(t, strings.HasPrefix(name, "codex.turn."))
		require.False(t, strings.HasPrefix(name, "codex.responses_api"))
	}
	failed := codexTelemetryTerminal{status: "failed", finished: profile.started.Add(100 * time.Millisecond)}
	store.recordAttempt(profile, failed)
	store.record(profile, failed)
	batches := store.flush(profile.started.Add(time.Minute))
	require.Len(t, batches, 1)
	for _, name := range batches[0].names {
		require.False(t, strings.HasPrefix(name, "codex.websocket."))
	}
	require.False(t, codexMetricsTestMetric(batches[0].body, "codex.turn.ttft.duration_ms").Exists())
	require.False(t, codexMetricsTestMetric(batches[0].body, "codex.turn.ttfm.duration_ms").Exists())
}

func TestCodexTelemetryMetricsWebSocketResultsRemainSeparate(t *testing.T) {
	store := newCodexTelemetryMetricStore()
	profile := codexMetricsTestProfile()
	profile.websocket = true
	startup := store.touch(profile)
	require.NotContains(t, string(startup[0].body), "codex.websocket.", "success cannot be known at startup")
	for _, status := range []string{"failed", "completed"} {
		result := codexTelemetryTerminal{
			status: status, finished: profile.started.Add(300 * time.Millisecond),
			firstEvent: profile.started.Add(40 * time.Millisecond), firstToken: profile.started.Add(70 * time.Millisecond),
		}
		store.recordAttempt(profile, result)
		store.record(profile, result)
	}
	batches := store.flush(profile.started.Add(time.Minute))
	require.Len(t, batches, 1)
	require.Equal(t, 2, batches[0].attempts)
	metric := codexMetricsTestMetric(batches[0].body, "codex.websocket.request")
	points := metric.Get("sum.dataPoints").Array()
	require.Len(t, points, 2)
	successes := map[string]uint64{}
	for _, point := range points {
		successes[codexMetricsTestAttribute(point, "success")] = point.Get("asInt").Uint()
	}
	require.Equal(t, map[string]uint64{"true": 1, "false": 1}, successes)
	ttft := codexMetricsTestMetric(batches[0].body, "codex.turn.ttft.duration_ms").Get("histogram.dataPoints.0")
	require.Equal(t, float64(80), ttft.Get("sum").Float())
	require.EqualValues(t, 2, ttft.Get("count").Uint())
	var definitions int
	for _, item := range gjson.GetBytes(batches[0].body, "resourceMetrics.0.scopeMetrics.0.metrics").Array() {
		if item.Get("name").String() == "codex.websocket.request" {
			definitions++
		}
	}
	require.Equal(t, 1, definitions, "attribute variants are data points within one metric")
}

func TestCodexTelemetryMetricsRetryUsesPhysicalAttemptModel(t *testing.T) {
	store := newCodexTelemetryMetricStore()
	oldModel := codexMetricsTestProfile()
	oldModel.websocket = true
	store.touch(oldModel)
	store.recordAttempt(oldModel, codexTelemetryTerminal{
		status: "failed", finished: oldModel.started.Add(100 * time.Millisecond),
		firstEvent: oldModel.started.Add(40 * time.Millisecond),
	})
	newModel := oldModel
	newModel.model = "gpt-5.5"
	newModel.started = oldModel.started.Add(200 * time.Millisecond)
	store.touch(newModel)
	completed := codexTelemetryTerminal{
		status: "completed", finished: newModel.started.Add(300 * time.Millisecond),
		firstEvent: newModel.started.Add(50 * time.Millisecond), firstToken: newModel.started.Add(70 * time.Millisecond),
	}
	store.recordAttempt(newModel, completed)
	store.record(newModel, completed)
	batches := store.flush(newModel.started.Add(time.Minute))
	require.Len(t, batches, 2)
	byModel := map[string]codexTelemetryMetricBatch{}
	for _, batch := range batches {
		byModel[batch.profile.model] = batch
		require.Equal(t, 1, batch.attempts)
		require.Empty(t, batch.profile.turnID)
	}
	failed, succeeded := byModel[oldModel.model], byModel[newModel.model]
	require.Equal(t, 0, failed.turns, "retry is not a completed logical turn")
	require.Equal(t, 1, succeeded.turns)
	require.False(t, codexMetricsTestMetric(failed.body, "codex.hooks.run").Exists())
	failurePoint := codexMetricsTestMetric(failed.body, "codex.websocket.request").Get("sum.dataPoints.0")
	successPoint := codexMetricsTestMetric(succeeded.body, "codex.websocket.request").Get("sum.dataPoints.0")
	require.Equal(t, "false", codexMetricsTestAttribute(failurePoint, "success"))
	require.Equal(t, "true", codexMetricsTestAttribute(successPoint, "success"))
	require.Equal(t, oldModel.model, codexMetricsTestAttribute(failurePoint, "model"))
	require.Equal(t, newModel.model, codexMetricsTestAttribute(successPoint, "model"))
	require.EqualValues(t, 1, failurePoint.Get("asInt").Uint())
	require.EqualValues(t, 1, successPoint.Get("asInt").Uint())
	timing := codexMetricsTestMetric(succeeded.body, "codex.turn.ttft.duration_ms").Get("histogram.dataPoints.0")
	require.EqualValues(t, 1, timing.Get("count").Uint(), "logical completion must not duplicate attempt timing")
	require.Equal(t, float64(50), timing.Get("sum").Float())
}

func TestCodexTelemetryMetricsFailedHandshakeHasNoResponseEvent(t *testing.T) {
	store := newCodexTelemetryMetricStore()
	profile := codexMetricsTestProfile()
	profile.websocket = true
	store.recordAttempt(profile, codexTelemetryTerminal{status: "failed", finished: profile.started.Add(100 * time.Millisecond)})
	batches := store.flush(profile.started.Add(time.Minute))
	require.Len(t, batches, 1)
	request := codexMetricsTestMetric(batches[0].body, "codex.websocket.request").Get("sum.dataPoints.0")
	require.Equal(t, "false", codexMetricsTestAttribute(request, "success"))
	require.False(t, codexMetricsTestMetric(batches[0].body, "codex.websocket.event").Exists())
	require.False(t, codexMetricsTestMetric(batches[0].body, "codex.websocket.event.duration_ms").Exists())
}

func TestCodexTelemetryMetricsRetentionAndClear(t *testing.T) {
	store := newCodexTelemetryMetricStore()
	profile := codexMetricsTestProfile()
	for index := range codexTelemetryMetricStateLimit + 1 {
		profile.model = fmt.Sprintf("model-%d", index)
		store.state(profile, profile.started.Add(time.Duration(index)*time.Millisecond))
	}
	require.Len(t, store.states, codexTelemetryMetricStateLimit)
	store.flush(profile.started.Add(6 * time.Minute))
	require.Empty(t, store.states)
	store.touch(profile)
	store.record(profile, codexTelemetryTerminal{status: "completed", finished: profile.started.Add(time.Second)})
	store.clear()
	require.Empty(t, store.states)
	require.Empty(t, store.clients)
	require.Empty(t, store.flush(profile.started.Add(time.Minute)), "disabled data must never be replayed")
}

func TestCodexTelemetryMetricsHookInterruptRequiresClientSignal(t *testing.T) {
	store := newCodexTelemetryMetricStore()
	profile := codexMetricsTestProfile()
	for _, explicit := range []bool{false, true} {
		store.record(profile, codexTelemetryTerminal{
			status: "interrupted", explicitClientInterrupt: explicit, finished: profile.started.Add(time.Second),
		})
	}
	batches := store.flush(profile.started.Add(time.Minute))
	require.Len(t, batches, 1)
	points := codexMetricsTestMetric(batches[0].body, "codex.hooks.run").Get("sum.dataPoints").Array()
	require.Len(t, points, 2)
	hooks := map[string]uint64{}
	for _, point := range points {
		hooks[codexMetricsTestAttribute(point, "hook_name")] = point.Get("asInt").Uint()
		require.Equal(t, "success", codexMetricsTestAttribute(point, "status"), "simulated hooks completed even if the request was interrupted")
	}
	require.Equal(t, map[string]uint64{"Stop": 4, "Interrupt": 4}, hooks)
}
