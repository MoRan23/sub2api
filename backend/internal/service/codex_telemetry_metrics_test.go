package service

import (
	"fmt"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func codexMetricsTestProfile() codexTelemetryProfile {
	proxyID := int64(19)
	return codexTelemetryProfile{
		client: codexTelemetryClient{localID: 7, name: "Test account", accountID: "upstream-account",
			userAgent: "codex_cli_rs/0.154.0 (Linux 6.8; x86_64)", originator: "codex_cli_rs", version: "0.154.0",
			accessToken: "credential-not-in-payload", proxyURL: "http://user:proxy-secret@localhost:8888"},
		sessionID: "root-session", threadID: "thread", turnID: "turn-one", model: "gpt-5.4", started: time.Unix(1_800_000_000, 0),
		simulationEnabled: true, observationEnabled: true, scenarioSeed: "test-pool-seed",
		input: CodexTelemetryInput{OwnerAccountID: 7, InstallationID: "test-installation", ProxyID: &proxyID},
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

func codexMetricsTestEventMetric(kind string, success bool, waits ...float64) CodexTelemetryEventMetric {
	event := CodexTelemetryEventMetric{Kind: kind, Success: success, Count: uint64(len(waits)), WaitCount: uint64(len(waits)), WaitBuckets: make([]uint64, len(codexHistogramBounds)+1)}
	for index, wait := range waits {
		if index == 0 || wait < event.WaitMinMS {
			event.WaitMinMS = wait
		}
		if wait > event.WaitMaxMS {
			event.WaitMaxMS = wait
		}
		event.WaitSumMS += wait
		event.WaitBuckets[sort.SearchFloat64s(codexHistogramBounds, wait)]++
	}
	return event
}

func TestCodexTelemetryMetricsDescriptorContract(t *testing.T) {
	names := make(map[string]bool)
	for _, descriptor := range codexMetricDescriptors {
		require.False(t, names[descriptor.name], descriptor.name)
		names[descriptor.name] = true
		require.True(t, codexStatsigMetricAllowed(descriptor.name), descriptor.name)
		require.Contains(t, []string{"sum", "histogram"}, descriptor.kind)
	}
	for _, name := range []string{"codex.api_request", "codex.api_request.duration_ms", "codex.conversation.turn.count", "exec_server_client_requests_total", "codex.responses_api_engine_iapi_ttft.duration_ms", "codex.responses_api_engine_service_tbt.duration_ms", "codex.responses_api_engine_service_ttft.duration_ms", "codex.tool.call", "codex.tool.call.duration_ms", "codex.turn.cost_microusd", "codex.turn.token_usage"} {
		require.False(t, codexStatsigMetricAllowed(name), name)
	}
	require.True(t, codexStatsigMetricAllowed("codex.turn.tool.call"))
	require.True(t, names["codex.sse_event"])
}

func TestCodexTelemetryMetricsHistogramKeepsEachObservation(t *testing.T) {
	store, profile := newCodexTelemetryMetricStore(), codexMetricsTestProfile()
	require.Empty(t, store.touch(profile), "startup joins the periodic delta batch")
	store.record(profile, codexTelemetryTerminal{status: "completed", finished: profile.started.Add(100 * time.Millisecond)})
	profile.turnID = "turn-two"
	store.record(profile, codexTelemetryTerminal{status: "completed", finished: profile.started.Add(300 * time.Millisecond)})
	require.Empty(t, store.flush(profile.started.Add(59*time.Second)))
	batches := store.flush(profile.started.Add(time.Minute))
	require.Len(t, batches, 1)
	require.Equal(t, "simulated", batches[0].source)
	point := codexMetricsTestMetric(batches[0].body, "codex.turn.e2e_duration_ms").Get("histogram.dataPoints.0")
	require.EqualValues(t, 2, point.Get("count").Uint())
	require.Equal(t, float64(400), point.Get("sum").Float())
	require.Equal(t, float64(100), point.Get("min").Float())
	require.Equal(t, float64(300), point.Get("max").Float())
	require.EqualValues(t, 1, point.Get("bucketCounts.6").Uint())
	require.EqualValues(t, 1, point.Get("bucketCounts.8").Uint())
	require.Equal(t, strconv.FormatInt(profile.started.UnixNano(), 10), point.Get("startTimeUnixNano").String())
	require.Equal(t, strconv.FormatInt(profile.started.Add(time.Minute).UnixNano(), 10), point.Get("timeUnixNano").String())
	valuePoint := codexMetricsTestMetric(batches[0].body, "codex.turn.tool.call").Get("histogram.dataPoints.0")
	require.Len(t, valuePoint.Get("explicitBounds").Array(), len(codexValueHistogramBounds))
	require.Len(t, point.Get("explicitBounds").Array(), len(codexHistogramBounds))
	require.Empty(t, store.flush(profile.started.Add(2*time.Minute)))
	profile.started = profile.started.Add(150 * time.Second)
	store.record(profile, codexTelemetryTerminal{status: "completed", finished: profile.started.Add(time.Second)})
	batches = store.flush(profile.started.Add(30 * time.Second))
	require.Len(t, batches, 1)
	point = codexMetricsTestMetric(batches[0].body, "codex.turn.e2e_duration_ms").Get("histogram.dataPoints.0")
	require.Equal(t, strconv.FormatInt(profile.started.Add(-30*time.Second).UnixNano(), 10), point.Get("startTimeUnixNano").String())
}

func TestCodexTelemetryMetricsPartitionAndStableStartup(t *testing.T) {
	store, base := newCodexTelemetryMetricStore(), codexMetricsTestProfile()
	profiles := []codexTelemetryProfile{base, base, base, base, base}
	profiles[1].model = "different-model"
	profiles[2].client.version = "0.155.0"
	profiles[3].client.localID = 8
	profiles[4].client.userAgent = "codex_vscode/0.154.0 (Mac OS 15.0; arm64)"
	profiles[4].input.InstallationID = "mac-installation"
	for _, profile := range profiles {
		store.touch(profile)
		store.record(profile, codexTelemetryTerminal{status: "completed", finished: profile.started.Add(time.Second)})
	}
	batches := store.flush(base.started.Add(time.Minute))
	require.Len(t, batches, len(profiles))
	startupCount := 0
	for _, batch := range batches {
		if codexMetricsTestMetric(batch.body, "codex.process.start").Exists() {
			startupCount++
		}
		require.Empty(t, batch.profile.threadID)
		require.NotContains(t, string(batch.body), "credential-not-in-payload")
		require.NotContains(t, string(batch.body), "proxy-secret")
		require.NotContains(t, string(batch.body), "test-installation")
	}
	require.Equal(t, 3, startupCount, "model and version changes must not start another process")
	store.flush(base.started.Add(10 * time.Minute))
	base.started = base.started.Add(11 * time.Minute)
	base.client.name, base.client.proxyURL = "Renamed", "http://another-proxy"
	store.touch(base)
	require.Empty(t, store.flush(base.started.Add(time.Minute)), "idle, rename and proxy switch do not repeat startup")
}

func TestCodexTelemetryMetricsOSCapabilities(t *testing.T) {
	for _, test := range []struct{ name, ua, expectedOS, snapshotSuccess, failure, service string }{
		{"windows", "codex-tui/0.154.0 (Windows 10.0; x86_64)", "Windows", "false", "write_failed", "codex_cli_rs"},
		{"macos", "Codex Desktop/0.154.0 (Mac OS 26.0; arm64)", "Mac_OS", "true", "none", "codex-app-server"},
		{"linux", "codex-tui/0.154.0 (Ubuntu 24.04; x86_64)", "Ubuntu", "true", "none", "codex_cli_rs"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, profile := newCodexTelemetryMetricStore(), codexMetricsTestProfile()
			profile.client.userAgent, profile.client.originator = test.ua, "unrecognized-client"
			store.touch(profile)
			batch := store.flush(profile.started.Add(time.Minute))[0]
			resource := gjson.GetBytes(batch.body, "resourceMetrics.0.resource")
			require.Equal(t, test.expectedOS, codexMetricsTestAttribute(resource, "os"))
			require.Equal(t, test.service, codexMetricsTestAttribute(resource, "service.name"))
			require.False(t, codexMetricsTestMetric(batch.body, "codex.windows_mxc.available").Exists())
			snapshot := codexMetricsTestMetric(batch.body, "codex.shell_snapshot").Get("sum.dataPoints.0")
			require.Equal(t, test.snapshotSuccess, codexMetricsTestAttribute(snapshot, "success"))
			require.Equal(t, test.failure, codexMetricsTestAttribute(snapshot, "failure_reason"))
			require.Equal(t, "other", codexMetricsTestAttribute(snapshot, "originator"))
			require.Empty(t, codexMetricsTestAttribute(snapshot, "service_name"))
		})
	}
}

func TestCodexTelemetryMetricsObservedTransportHasNoInventedClientTurn(t *testing.T) {
	for _, websocket := range []bool{false, true} {
		store, profile := newCodexTelemetryMetricStore(), codexMetricsTestProfile()
		profile.simulationEnabled, profile.websocket = false, websocket
		store.touch(profile)
		result := codexTelemetryTerminal{status: "failed", finished: profile.started.Add(20 * time.Second), result: CodexTelemetryResult{
			EventCount: 3, FailedEventCount: 1,
			EventMetrics: []CodexTelemetryEventMetric{
				codexMetricsTestEventMetric("response.output_text.delta", true, 2, 5),
				codexMetricsTestEventMetric("response.failed", false, 9),
			},
			SendDurationMS: 4, SendSucceeded: boolPointer(true), FirstTokenAt: profile.started.Add(time.Second),
			ServerTiming: map[string]float64{"engine_service_total_ms": 123, "engine_service_ttft_total_ms": 9},
		}}
		store.recordAttempt(profile, result)
		store.record(profile, result)
		batch := store.flush(profile.started.Add(2 * time.Minute))[0]
		require.Equal(t, "observed", batch.source)
		for _, name := range []string{"codex.process.start", "codex.thread.started", "codex.turn.e2e_duration_ms", "codex.turn.ttft.duration_ms", "codex.turn.ttfm.duration_ms", "codex.hooks.run"} {
			require.False(t, codexMetricsTestMetric(batch.body, name).Exists(), name)
		}
		eventName := "codex.sse_event"
		if websocket {
			eventName = "codex.websocket.event"
		}
		points := codexMetricsTestMetric(batch.body, eventName).Get("sum.dataPoints").Array()
		counts := map[string]uint64{}
		for _, point := range points {
			counts[codexMetricsTestAttribute(point, "kind")+":"+codexMetricsTestAttribute(point, "success")] = point.Get("asInt").Uint()
		}
		require.Equal(t, map[string]uint64{"response.output_text.delta:true": 2, "response.failed:false": 1}, counts)
		var waits float64
		for _, point := range codexMetricsTestMetric(batch.body, eventName+".duration_ms").Get("histogram.dataPoints").Array() {
			waits += point.Get("sum").Float()
		}
		require.Equal(t, float64(16), waits)
		require.Equal(t, float64(123), codexMetricsTestMetric(batch.body, "codex.responses_api_inference_time.duration_ms").Get("histogram.dataPoints.0.sum").Float())
		require.False(t, codexMetricsTestMetric(batch.body, "codex.responses_api_engine_service_ttft.duration_ms").Exists())
		request := codexMetricsTestMetric(batch.body, "codex.websocket.request")
		require.Equal(t, websocket, request.Exists())
		if websocket {
			require.Equal(t, "true", codexMetricsTestAttribute(request.Get("sum.dataPoints.0"), "success"))
			require.Equal(t, float64(4), codexMetricsTestMetric(batch.body, "codex.websocket.request.duration_ms").Get("histogram.dataPoints.0.sum").Float())
		}
	}
}

func TestCodexTelemetryMetricsSnapshotPreservesDedupAndDelta(t *testing.T) {
	store, profile := newCodexTelemetryMetricStore(), codexMetricsTestProfile()
	profile.poolID = "durable-pool"
	store.touch(profile)
	store.recordAttempt(profile, codexTelemetryTerminal{status: "completed", finished: profile.started.Add(time.Second), result: CodexTelemetryResult{EventCount: 2}})
	encoded, err := marshalCodexTelemetryMetricStore(store)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "credential-not-in-payload")
	require.NotContains(t, string(encoded), "proxy-secret")
	restored, err := unmarshalCodexTelemetryMetricStore(encoded)
	require.NoError(t, err)
	require.True(t, profile.started.Add(time.Minute).Equal(restored.nextFlushAt()))
	restored.touch(profile)
	original, actual := store.flush(profile.started.Add(time.Minute)), restored.flush(profile.started.Add(time.Minute))
	require.Len(t, actual, 1)
	require.JSONEq(t, string(original[0].body), string(actual[0].body))
	require.Equal(t, "mixed", actual[0].source)
	require.Empty(t, restored.flush(profile.started.Add(2*time.Minute)))
	require.Empty(t, restored.touch(profile))
	require.Empty(t, restored.flush(profile.started.Add(3*time.Minute)))
	_, err = unmarshalCodexTelemetryMetricStore([]byte(`{"version":99}`))
	require.Error(t, err)
}

func TestCodexTelemetryMetricsModesAndRetention(t *testing.T) {
	for _, sim := range []bool{false, true} {
		for _, observed := range []bool{false, true} {
			store, profile := newCodexTelemetryMetricStore(), codexMetricsTestProfile()
			profile.simulationEnabled, profile.observationEnabled = sim, observed
			store.touch(profile)
			store.recordAttempt(profile, codexTelemetryTerminal{status: "completed", finished: profile.started.Add(time.Second), result: CodexTelemetryResult{EventCount: 1}})
			store.record(profile, codexTelemetryTerminal{status: "completed", finished: profile.started.Add(time.Second)})
			batches := store.flush(profile.started.Add(2 * time.Minute))
			if !sim && !observed {
				require.Empty(t, batches)
				continue
			}
			require.Len(t, batches, 1)
			require.Equal(t, sim, codexMetricsTestMetric(batches[0].body, "codex.process.start").Exists())
			require.Equal(t, observed, codexMetricsTestMetric(batches[0].body, "codex.sse_event").Exists())
		}
	}
	store, profile := newCodexTelemetryMetricStore(), codexMetricsTestProfile()
	for index := range codexTelemetryMetricStateLimit + 1 {
		profile.model = fmt.Sprintf("model-%d", index)
		store.state(profile, profile.started.Add(time.Duration(index)*time.Millisecond))
	}
	require.Len(t, store.states, codexTelemetryMetricStateLimit)
	store.flush(profile.started.Add(6 * time.Minute))
	require.Empty(t, store.states)
	store.clear()
	require.Empty(t, store.clients)
}
