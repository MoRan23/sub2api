package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/require"
)

func TestCodexTelemetry0171CompressionKeepsSealedPayload(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_HEADERS", "x-unrelated=not-forwarded")
	t.Setenv("OTEL_EXPORTER_OTLP_COMPRESSION", "none")
	now := time.Now().UTC()
	profile := codexMetricsTestProfile()
	profile.source = "observed"
	store := NewMemoryCodexTelemetryStore()
	key := CodexTelemetryPoolKey{OwnerAccountID: profile.client.localID, OSFamily: "linux", InstallationID: "compression-test"}
	payload := []byte(`{"resourceMetrics":[{"scopeMetrics":[{"metrics":[]}]}]}`)
	_, err := store.TransactPool(context.Background(), key, now, func(tx *CodexTelemetryPoolTransaction) error {
		return appendRuntimeBatch(tx, profile, "metrics", payload, now)
	})
	require.NoError(t, err)
	claimed, err := store.ClaimBatches(context.Background(), now, 1)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	decoder, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1))
	require.NoError(t, err)
	defer decoder.Close()
	received := make(chan codexTelemetrySent, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, readErr := io.ReadAll(r.Body)
		if readErr != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		received <- codexTelemetrySent{headers: r.Header.Clone(), body: body}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	s := &CodexTelemetryService{metricsURL: server.URL}
	compressor := codexTelemetryCompressor{}
	defer compressor.close()
	send := func(_ context.Context, req *http.Request, _ CodexTelemetryInput, _ bool) (*http.Response, error) {
		return server.Client().Do(req)
	}
	for range 2 {
		code, reason, cancelled := s.send(context.Background(), send, codexTelemetryJob{profile: profile, metrics: true, body: claimed[0].Payload}, &compressor)
		require.Equal(t, http.StatusServiceUnavailable, code)
		require.Equal(t, "upstream_http_error", reason)
		require.False(t, cancelled)
		wire := <-received
		require.Equal(t, "zstd", wire.headers.Get("Content-Encoding"))
		require.Equal(t, "application/json", wire.headers.Get("Content-Type"))
		require.Equal(t, "OTel-OTLP-Exporter-Rust/0.31.0", wire.headers.Get("User-Agent"))
		require.Empty(t, wire.headers.Get("x-unrelated"))
		decoded, decodeErr := decoder.DecodeAll(wire.body, nil)
		require.NoError(t, decodeErr)
		require.Equal(t, payload, decoded)
		require.Equal(t, payload, []byte(claimed[0].Payload))
	}
	recovered, err := store.ClaimBatches(context.Background(), now.Add(31*time.Second), 1)
	require.NoError(t, err)
	require.Len(t, recovered, 1)
	require.Equal(t, payload, []byte(recovered[0].Payload))
	require.Equal(t, claimed[0].Metadata, recovered[0].Metadata)
}

func TestCodexTelemetry0171WorkersCompressOnlyMetrics(t *testing.T) {
	t.Setenv("CODEX_TELEMETRY_ENABLED", "true")
	s := NewCodexTelemetryService(nil)
	t.Cleanup(s.Stop)
	received := make(chan codexTelemetrySent, 16)
	s.SetSender(func(_ context.Context, req *http.Request, _ CodexTelemetryInput, metrics bool) (*http.Response, error) {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		received <- codexTelemetrySent{headers: req.Header.Clone(), body: body, metrics: metrics}
		return &http.Response{StatusCode: http.StatusAccepted, Body: io.NopCloser(strings.NewReader("{}"))}, nil
	})
	payload := []byte(`{"events":[],"resourceMetrics":[]}`)
	s.mu.Lock()
	for i := range 16 {
		profile := codexMetricsTestProfile()
		profile.client.localID = int64(i % 4)
		s.enqueueLocked(codexTelemetryJob{profile: profile, metrics: i%8 < 4, body: payload, epoch: s.epoch, entry: &CodexTelemetryObservation{Status: "queued"}})
	}
	s.mu.Unlock()
	require.Eventually(t, func() bool {
		return s.Observations(CodexTelemetryObservationQuery{}).Counters.Sent == 16
	}, 5*time.Second, time.Millisecond)
	s.Stop()
	decoder, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1))
	require.NoError(t, err)
	defer decoder.Close()
	for range 16 {
		wire := <-received
		if wire.metrics {
			require.Equal(t, "zstd", wire.headers.Get("Content-Encoding"))
			decoded, decodeErr := decoder.DecodeAll(wire.body, nil)
			require.NoError(t, decodeErr)
			require.Equal(t, payload, decoded)
			continue
		}
		require.Empty(t, wire.headers.Get("Content-Encoding"))
		require.Equal(t, payload, wire.body)
	}
}

func TestCodexTelemetry0171StopCancelsCompressedSends(t *testing.T) {
	t.Setenv("CODEX_TELEMETRY_ENABLED", "true")
	s := NewCodexTelemetryService(nil)
	t.Cleanup(s.Stop)
	entered := make(chan struct{}, 4)
	s.SetSender(func(ctx context.Context, req *http.Request, _ CodexTelemetryInput, _ bool) (*http.Response, error) {
		if req.Header.Get("Content-Encoding") != "zstd" {
			return nil, fmt.Errorf("missing metrics compression")
		}
		entered <- struct{}{}
		<-ctx.Done()
		return nil, ctx.Err()
	})
	s.mu.Lock()
	for i := range 4 {
		profile := codexMetricsTestProfile()
		profile.client.localID = int64(i)
		s.enqueueLocked(codexTelemetryJob{profile: profile, metrics: true, body: []byte(`{"resourceMetrics":[]}`), epoch: s.epoch, entry: &CodexTelemetryObservation{Status: "queued"}})
	}
	s.mu.Unlock()
	for range 4 {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("metrics worker did not send")
		}
	}
	s.Stop()
	view := s.Observations(CodexTelemetryObservationQuery{})
	require.EqualValues(t, 4, view.Counters.Cancelled)
	require.Zero(t, view.QueueDepth)
}

func TestCodexTelemetryAuthStorageLockSchemaPreservesProvidedSamples(t *testing.T) {
	profile := codexMetricsTestProfile()
	profile.simulationEnabled = false
	store := newCodexTelemetryMetricStore()
	state, _ := store.state(profile, profile.started)
	labels := map[string]string{"store": "file", "lock_mode": "exclusive", "operation": "retire_refresh_token", "originator": "codex_cli_rs", "outcome": "acquired", "contended": "true"}
	want := map[string]string{
		"codex.auth_storage.lock_acquire":       "sum",
		"codex.auth_storage.lock_wait.duration": "histogram",
		"codex.auth_storage.lock_hold.duration": "histogram",
	}
	for _, descriptor := range codexMetricDescriptors {
		kind, exists := want[descriptor.name]
		if !exists {
			continue
		}
		require.Equal(t, kind, descriptor.kind)
		require.False(t, codexMetricIsStartup(descriptor.name))
		if kind == "histogram" {
			require.Equal(t, "ms", descriptor.unit)
		}
		if descriptor.name == "codex.auth_storage.lock_hold.duration" {
			state.addWithSource(descriptor, 25, profile, "", profile.started.Add(time.Second), map[string]string{"store": "file", "lock_mode": "exclusive", "operation": "retire_refresh_token", "originator": "codex_cli_rs"}, "observed")
			continue
		}
		state.addWithSource(descriptor, 25, profile, "", profile.started.Add(time.Second), labels, "observed")
	}
	encoded, err := marshalCodexTelemetryMetricStore(store)
	require.NoError(t, err)
	restored, err := unmarshalCodexTelemetryMetricStore(encoded)
	require.NoError(t, err)
	batches := restored.flush(profile.started.Add(time.Minute))
	require.Len(t, batches, 1)
	for name, kind := range want {
		metric := codexMetricsTestMetric(batches[0].body, name)
		require.True(t, metric.Exists(), name)
		point := metric.Get(kind + ".dataPoints.0")
		require.Equal(t, "file", codexMetricsTestAttribute(point, "store"))
		require.Equal(t, "exclusive", codexMetricsTestAttribute(point, "lock_mode"))
		require.Equal(t, "retire_refresh_token", codexMetricsTestAttribute(point, "operation"))
		if kind == "histogram" {
			require.Equal(t, float64(25), point.Get("sum").Float())
		}
		if name == "codex.auth_storage.lock_hold.duration" {
			require.Len(t, point.Get("attributes").Array(), 4)
			require.Empty(t, codexMetricsTestAttribute(point, "outcome"))
			continue
		}
		require.Len(t, point.Get("attributes").Array(), 6)
		require.Equal(t, "acquired", codexMetricsTestAttribute(point, "outcome"))
		require.Equal(t, "true", codexMetricsTestAttribute(point, "contended"))
	}
}

func BenchmarkCodexTelemetry0171Compression(b *testing.B) {
	for _, batchCount := range []int{1, 8} {
		payload := struct {
			ResourceMetrics []json.RawMessage `json:"resourceMetrics"`
		}{}
		for i := range batchCount {
			store, profile := newCodexTelemetryMetricStore(), codexMetricsTestProfile()
			profile.model = fmt.Sprintf("model-%d", i)
			store.touch(profile)
			store.record(profile, codexTelemetryTerminal{status: "completed", finished: profile.started.Add(time.Duration(i+1) * time.Second)})
			batches := store.flush(profile.started.Add(time.Minute))
			resource := struct {
				ResourceMetrics []json.RawMessage `json:"resourceMetrics"`
			}{}
			if err := json.Unmarshal(batches[0].body, &resource); err != nil {
				b.Fatal(err)
			}
			payload.ResourceMetrics = append(payload.ResourceMetrics, resource.ResourceMetrics...)
		}
		body, err := json.Marshal(payload)
		if err != nil {
			b.Fatal(err)
		}
		b.Run(fmt.Sprintf("%d-batch", batchCount), func(b *testing.B) {
			compressor := codexTelemetryCompressor{}
			defer compressor.close()
			compressed, err := compressor.encode(body)
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.SetBytes(int64(len(body)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				compressed, err := compressor.encode(body)
				if err != nil || bytes.Equal(body, compressed) {
					b.Fatal("compression failed", err)
				}
			}
			b.ReportMetric(float64(len(compressed))/float64(len(body))*100, "wire-percent")
			b.ReportMetric(float64(len(body)), "body-bytes")
		})
	}
}
