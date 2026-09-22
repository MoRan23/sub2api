package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

var codexMetricMigrationByteNames = []string{
	"codex.app_server.codex_home.size_bytes",
	"codex.sqlite.logs.write.bytes",
	"codex.sqlite.logs.write.max_entry_bytes",
}

// Construct the legacy wire shape independently of the current histogram
// implementation: all three byte metrics used these same 16 buckets in v1.
func codexMetricMigrationLegacySnapshot(t *testing.T, profile codexTelemetryProfile) codexMetricStoreSnapshot {
	t.Helper()
	encodedProfile, err := marshalCodexTelemetryProfile(profile)
	require.NoError(t, err)
	finished := profile.started.Add(30 * time.Second)
	msBuckets := make([]uint64, len(codexHistogramBounds)+1)
	msBuckets[6], msBuckets[8] = 1, 1
	state := codexMetricStateSnapshot{
		Profile: encodedProfile, LastSeen: finished, CollectedAt: profile.started,
		Source: "mixed", Turns: 2, Attempts: 3, ExternalAgentSent: true,
		Pending: []codexMetricAggregateSnapshot{
			{
				Name: "codex.process.start", Kind: "sum", Unit: "",
				Attributes: []any{map[string]any{"key": "originator", "value": map[string]any{"stringValue": "codex_cli_rs"}}},
				Started:    profile.started, Finished: finished, Count: 1, Sum: 1, Minimum: 1, Maximum: 1,
				Buckets: []uint64{0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0},
			},
			{
				Name: "codex.turn.e2e_duration_ms", Kind: "histogram", Unit: "ms",
				Attributes: []any{map[string]any{"key": "model", "value": map[string]any{"stringValue": profile.model}}},
				Started:    profile.started, Finished: finished, Count: 2, Sum: 400, Minimum: 100, Maximum: 300,
				Buckets: msBuckets,
			},
		},
	}
	for _, name := range codexMetricMigrationByteNames {
		state.Pending = append(state.Pending, codexMetricAggregateSnapshot{
			Name: name, Kind: "histogram", Unit: "", Attributes: []any{},
			Started: profile.started, Finished: finished, Count: 2, Sum: 98304, Minimum: 32768, Maximum: 65536,
			Buckets: []uint64{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 2},
		})
	}
	return codexMetricStoreSnapshot{
		Version: 1, States: []codexMetricStateSnapshot{state},
		Clients: map[string]time.Time{codexMetricClientKey(profile): finished},
	}
}

func codexMetricMigrationEncode(t *testing.T, snapshot codexMetricStoreSnapshot) []byte {
	t.Helper()
	encoded, err := json.Marshal(snapshot)
	require.NoError(t, err)
	return encoded
}

func TestCodexTelemetryMetricsLegacySnapshotMigratesOnlyByteHistograms(t *testing.T) {
	profile := runtimeTestAttempt(time.Unix(1_800_000_000, 0).UTC(), "windows", "turn", "sample").profile
	profile.poolID = "legacy-metric-pool"
	legacy := codexMetricMigrationLegacySnapshot(t, profile)
	restored, err := unmarshalCodexTelemetryMetricStore(codexMetricMigrationEncode(t, legacy))
	require.NoError(t, err)
	require.Len(t, restored.states, 1)
	require.Equal(t, legacy.Clients, restored.clients)
	require.Equal(t, profile.started.Add(time.Minute), restored.nextFlushAt())

	encoded, err := marshalCodexTelemetryMetricStore(restored)
	require.NoError(t, err)
	var migrated codexMetricStoreSnapshot
	require.NoError(t, json.Unmarshal(encoded, &migrated))
	require.Equal(t, 2, migrated.Version)
	require.Equal(t, "legacy_byte_histogram_discarded", migrated.CompatibilityReason)
	require.Len(t, migrated.States, 1)
	expected := legacy.States[0]
	expected.Pending = expected.Pending[:2]
	require.Equal(t, expected, migrated.States[0], "unchanged aggregates and state metadata must survive migration")
	v2, err := unmarshalCodexTelemetryMetricStore(encoded)
	require.NoError(t, err)
	require.Equal(t, "legacy_byte_histogram_discarded", v2.compatibilityReason)

	require.Empty(t, restored.touch(profile))
	require.Len(t, restored.states[codexMetricStateKey(profile)].pending, 2, "the client startup marker must prevent regeneration")
	batches := restored.flush(profile.started.Add(time.Minute))
	require.Len(t, batches, 1)
	require.NotContains(t, string(batches[0].body), "legacy_byte_histogram_discarded", "compatibility diagnostics must remain local")
	require.EqualValues(t, 1, codexMetricsTestMetric(batches[0].body, "codex.process.start").Get("sum.dataPoints.0.asInt").Int())
	point := codexMetricsTestMetric(batches[0].body, "codex.turn.e2e_duration_ms").Get("histogram.dataPoints.0")
	require.EqualValues(t, 2, point.Get("count").Uint())
	require.Equal(t, float64(400), point.Get("sum").Float())
	for _, name := range codexMetricMigrationByteNames {
		require.False(t, codexMetricsTestMetric(batches[0].body, name).Exists(), name)
	}
	require.Empty(t, restored.flush(profile.started.Add(2*time.Minute)))
}

func TestCodexTelemetryMetricsV2ByteHistogramRoundTripAcceptsNewSamples(t *testing.T) {
	for _, test := range []struct {
		name        string
		bucketCount int
		first       float64
		overflow    float64
	}{
		{"codex.app_server.codex_home.size_bytes", 8, 1048576, 1099511627777},
		{"codex.sqlite.logs.write.bytes", 19, 128, 16777217},
		{"codex.sqlite.logs.write.max_entry_bytes", 19, 128, 16777217},
	} {
		t.Run(test.name, func(t *testing.T) {
			profile := runtimeTestAttempt(time.Unix(1_800_000_000, 0).UTC(), "windows", "turn", "sample").profile
			store := newCodexTelemetryMetricStore()
			state, _ := store.state(profile, profile.started)
			descriptor := codexMetricDescriptor{name: test.name, kind: "histogram"}
			state.add(descriptor, test.first, profile, "completed", profile.started.Add(time.Second))
			encoded, err := marshalCodexTelemetryMetricStore(store)
			require.NoError(t, err)
			var snapshot codexMetricStoreSnapshot
			require.NoError(t, json.Unmarshal(encoded, &snapshot))
			require.Equal(t, 2, snapshot.Version)
			require.Len(t, snapshot.States[0].Pending[0].Buckets, test.bucketCount)

			restored, err := unmarshalCodexTelemetryMetricStore(encoded)
			require.NoError(t, err)
			reencoded, err := marshalCodexTelemetryMetricStore(restored)
			require.NoError(t, err)
			require.JSONEq(t, string(encoded), string(reencoded))
			restoredState := restored.states[codexMetricStateKey(profile)]
			require.NotNil(t, restoredState)
			restoredState.add(descriptor, test.overflow, profile, "completed", profile.started.Add(2*time.Second))
			batches := restored.flush(profile.started.Add(time.Minute))
			require.Len(t, batches, 1)
			point := codexMetricsTestMetric(batches[0].body, test.name).Get("histogram.dataPoints.0")
			require.EqualValues(t, 2, point.Get("count").Uint())
			require.Equal(t, test.first+test.overflow, point.Get("sum").Float())
			require.Equal(t, test.first, point.Get("min").Float())
			require.Equal(t, test.overflow, point.Get("max").Float())
			buckets := point.Get("bucketCounts").Array()
			require.Len(t, buckets, test.bucketCount)
			require.Len(t, point.Get("explicitBounds").Array(), test.bucketCount-1)
			require.EqualValues(t, 1, buckets[0].Uint())
			require.EqualValues(t, 1, buckets[len(buckets)-1].Uint())
		})
	}
}

func TestCodexTelemetryMetricsSnapshotMigrationRejectsInvalidShapes(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*codexMetricStoreSnapshot)
	}{
		{"unknown version", func(s *codexMetricStoreSnapshot) { s.Version = 99 }},
		{"v2 legacy byte buckets", func(s *codexMetricStoreSnapshot) { s.Version = 2 }},
		{"legacy byte bucket count", func(s *codexMetricStoreSnapshot) { s.States[0].Pending[2].Buckets = []uint64{2} }},
		{"legacy byte kind", func(s *codexMetricStoreSnapshot) { s.States[0].Pending[2].Kind = "sum" }},
		{"legacy byte unit", func(s *codexMetricStoreSnapshot) { s.States[0].Pending[2].Unit = "ms" }},
		{"unchanged legacy histogram", func(s *codexMetricStoreSnapshot) { s.States[0].Pending[1].Buckets = []uint64{2} }},
		{"v2 malformed histogram", func(s *codexMetricStoreSnapshot) {
			s.Version = 2
			s.States[0].Pending = s.States[0].Pending[:2]
			s.States[0].Pending[1].Buckets = []uint64{2}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			profile := runtimeTestAttempt(time.Unix(1_800_000_000, 0).UTC(), "windows", "turn", "sample").profile
			snapshot := codexMetricMigrationLegacySnapshot(t, profile)
			test.mutate(&snapshot)
			_, err := unmarshalCodexTelemetryMetricStore(codexMetricMigrationEncode(t, snapshot))
			require.Error(t, err)
		})
	}
}

func TestCodexTelemetryRuntimeMigratesPendingMetricsAndKeepsSealedPayload(t *testing.T) {
	store := NewMemoryCodexTelemetryStore().(*MemoryCodexTelemetryStore)
	now := time.Unix(1_800_000_000, 0).UTC()
	attempt := runtimeTestAttempt(now, "windows", "turn", "sample")
	var legacy codexMetricStoreSnapshot
	var sealedID string
	sealedPayload := []byte(`{"resourceMetrics":[{"scopeMetrics":[{"metrics":[{"name":"codex.app_server.codex_home.size_bytes","histogram":{"dataPoints":[{"explicitBounds":[0,5,10,25,50,75,100,250,500,750,1000,2500,5000,7500,10000],"bucketCounts":["0","0","0","0","0","0","0","0","0","0","0","0","0","0","0","1"],"count":"1","sum":32768}]}}]}]}]}`)
	_, err := store.TransactPool(context.Background(), attempt.poolKey, now, func(tx *CodexTelemetryPoolTransaction) error {
		if err := syncCodexTelemetryRuntimeEpoch(tx, now); err != nil {
			return err
		}
		profile := attempt.profile
		profile.poolID, profile.scenarioSeed = tx.Pool.ID, tx.Pool.Seed
		legacy = codexMetricMigrationLegacySnapshot(t, profile)
		tx.Activities[CodexTelemetryActivityMapKey("metrics", "current")] = CodexTelemetryActivity{
			Kind: "metrics", Key: "current", Data: codexMetricMigrationEncode(t, legacy),
			UpdatedAt: now, DueAt: now.Add(time.Minute),
		}
		if err := appendRuntimeBatch(tx, profile, "metrics", sealedPayload, now); err != nil {
			return err
		}
		sealedID = tx.Batches[0].ID
		return nil
	})
	require.NoError(t, err)
	require.Len(t, store.batches, 1)

	_, err = store.TransactPool(context.Background(), attempt.poolKey, now.Add(time.Minute), func(tx *CodexTelemetryPoolTransaction) error {
		return tickCodexTelemetryRuntime(tx, now.Add(time.Minute))
	})
	require.NoError(t, err, "legacy bounds must not block the pool's runtime transaction")
	require.Len(t, store.batches, 2)
	require.Equal(t, sealedPayload, []byte(store.batches[sealedID].Payload), "previously sealed OTLP must stay byte-for-byte unchanged")
	for id, batch := range store.batches {
		if id == sealedID {
			continue
		}
		require.Equal(t, "metrics", batch.Type)
		require.Equal(t, "mixed", batch.Source)
		require.NotContains(t, string(batch.Payload), "legacy_byte_histogram_discarded", "compatibility diagnostics must not enter OTLP")
		require.EqualValues(t, 1, codexMetricsTestMetric(batch.Payload, "codex.process.start").Get("sum.dataPoints.0.asInt").Int())
		require.Equal(t, float64(400), codexMetricsTestMetric(batch.Payload, "codex.turn.e2e_duration_ms").Get("histogram.dataPoints.0.sum").Float())
		for _, name := range codexMetricMigrationByteNames {
			require.False(t, codexMetricsTestMetric(batch.Payload, name).Exists(), name)
		}
	}
	activity := store.activities[attempt.poolKey][CodexTelemetryActivityMapKey("metrics", "current")]
	var saved codexMetricStoreSnapshot
	require.NoError(t, json.Unmarshal(activity.Data, &saved))
	require.Equal(t, 2, saved.Version)
	require.Equal(t, "legacy_byte_histogram_discarded", saved.CompatibilityReason)
	require.Equal(t, legacy.Clients, saved.Clients)
	require.Len(t, saved.States, 1)
	require.Empty(t, saved.States[0].Pending)
	require.True(t, saved.States[0].ExternalAgentSent)
	require.Equal(t, now.Add(time.Minute), saved.States[0].CollectedAt)
	require.True(t, activity.DueAt.IsZero())
}

func TestCodexTelemetryMetricsSnapshotDropsUnknownCompatibilityReason(t *testing.T) {
	profile := runtimeTestAttempt(time.Unix(1_800_000_000, 0).UTC(), "windows", "turn", "sample").profile
	snapshot := codexMetricMigrationLegacySnapshot(t, profile)
	snapshot.Version = 2
	snapshot.CompatibilityReason = "private-migration-error-token"
	snapshot.States[0].Pending = snapshot.States[0].Pending[:2]
	restored, err := unmarshalCodexTelemetryMetricStore(codexMetricMigrationEncode(t, snapshot))
	require.NoError(t, err)
	require.Empty(t, restored.compatibilityReason)
	encoded, err := marshalCodexTelemetryMetricStore(restored)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "private-migration-error-token")
	require.NotContains(t, string(encoded), "compatibility_reason")
	batches := restored.flush(profile.started.Add(time.Minute))
	require.Len(t, batches, 1)
	require.NotContains(t, string(batches[0].body), "private-migration-error-token")
}

var codexMetricMigrationEventDescriptors = []codexMetricDescriptor{
	{name: "codex.sse_event", kind: "sum"},
	{name: "codex.sse_event.duration_ms", kind: "histogram", unit: "ms"},
	{name: "codex.websocket.event", kind: "sum"},
	{name: "codex.websocket.event.duration_ms", kind: "histogram", unit: "ms"},
}

func codexMetricMigrationStringAttribute(key, value string) any {
	return map[string]any{"key": key, "value": map[string]any{"stringValue": value}}
}

func codexMetricMigrationLegacyEventSnapshot(t *testing.T, descriptor codexMetricDescriptor) codexMetricStoreSnapshot {
	t.Helper()
	profile := runtimeTestAttempt(time.Unix(1_800_000_000, 0).UTC(), "windows", "turn", "sample").profile
	snapshot := codexMetricMigrationLegacySnapshot(t, profile)
	aggregate := codexMetricAggregateSnapshot{
		Name: descriptor.name, Kind: descriptor.kind, Unit: descriptor.unit,
		Attributes: []any{
			codexMetricMigrationStringAttribute("model", profile.model),
			codexMetricMigrationStringAttribute("success", "true"),
		},
		Started: profile.started.Add(2 * time.Second), Finished: profile.started.Add(20 * time.Second),
		Count: 2, Sum: 5, Minimum: 2, Maximum: 3,
		Buckets: []uint64{0, 2, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0},
	}
	if descriptor.kind == "histogram" {
		aggregate.Sum, aggregate.Minimum, aggregate.Maximum = 400, 100, 300
		aggregate.Buckets = make([]uint64, len(codexHistogramBounds)+1)
		aggregate.Buckets[6], aggregate.Buckets[8] = 1, 1
	}
	snapshot.States[0].Pending = []codexMetricAggregateSnapshot{aggregate}
	snapshot.States[0].Source = "observed"
	return snapshot
}

func TestCodexTelemetryMetricsLegacyEventSnapshotAddsUnknownKind(t *testing.T) {
	for _, descriptor := range codexMetricMigrationEventDescriptors {
		t.Run(descriptor.name, func(t *testing.T) {
			legacy := codexMetricMigrationLegacyEventSnapshot(t, descriptor)
			restored, err := unmarshalCodexTelemetryMetricStore(codexMetricMigrationEncode(t, legacy))
			require.NoError(t, err)
			encoded, err := marshalCodexTelemetryMetricStore(restored)
			require.NoError(t, err)
			var migrated codexMetricStoreSnapshot
			require.NoError(t, json.Unmarshal(encoded, &migrated))
			require.Equal(t, 2, migrated.Version)
			require.Len(t, migrated.States[0].Pending, 1)
			expected := legacy.States[0].Pending[0]
			expected.Attributes = append([]any{codexMetricMigrationStringAttribute("kind", "unknown")}, expected.Attributes...)
			require.Equal(t, expected, migrated.States[0].Pending[0], "adding kind must preserve the legacy measurements")

			v2, err := unmarshalCodexTelemetryMetricStore(encoded)
			require.NoError(t, err)
			reencoded, err := marshalCodexTelemetryMetricStore(v2)
			require.NoError(t, err)
			require.JSONEq(t, string(encoded), string(reencoded), "unknown kind must survive a v2 reload")
			batches := v2.flush(legacy.States[0].CollectedAt.Add(time.Minute))
			require.Len(t, batches, 1)
			points := codexMetricsTestMetric(batches[0].body, descriptor.name).Get(descriptor.kind + ".dataPoints").Array()
			require.Len(t, points, 1)
			require.Equal(t, "unknown", codexMetricsTestAttribute(points[0], "kind"))
			require.Equal(t, "true", codexMetricsTestAttribute(points[0], "success"))
		})
	}
}

func TestCodexTelemetryMetricsLegacyEventMigrationMergesUnknownCollisions(t *testing.T) {
	for _, descriptor := range codexMetricMigrationEventDescriptors {
		for _, explicitFirst := range []bool{false, true} {
			order := "missing_first"
			if explicitFirst {
				order = "explicit_first"
			}
			t.Run(descriptor.name+"/"+order, func(t *testing.T) {
				legacy := codexMetricMigrationLegacyEventSnapshot(t, descriptor)
				missing := legacy.States[0].Pending[0]
				base := legacy.States[0].CollectedAt
				// The explicit series intentionally stores labels in another order.
				// Migration must use their canonical identity before merging.
				explicit := codexMetricAggregateSnapshot{
					Name: descriptor.name, Kind: descriptor.kind, Unit: descriptor.unit,
					Attributes: []any{
						codexMetricMigrationStringAttribute("success", "true"),
						codexMetricMigrationStringAttribute("kind", "unknown"),
						missing.Attributes[0],
					},
					Started: base.Add(time.Second), Finished: base.Add(25 * time.Second),
					Count: 1, Sum: 7, Minimum: 7, Maximum: 7,
					Buckets: []uint64{0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0},
				}
				distinct := codexMetricAggregateSnapshot{
					Name: descriptor.name, Kind: descriptor.kind, Unit: descriptor.unit,
					Attributes: []any{
						codexMetricMigrationStringAttribute("success", "false"),
						missing.Attributes[0],
					},
					Started: base.Add(3 * time.Second), Finished: base.Add(21 * time.Second),
					Count: 1, Sum: 9, Minimum: 9, Maximum: 9,
					Buckets: []uint64{0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0},
				}
				if descriptor.kind == "histogram" {
					explicit.Count, explicit.Sum, explicit.Minimum, explicit.Maximum = 2, 605, 5, 600
					explicit.Buckets = make([]uint64, len(codexHistogramBounds)+1)
					explicit.Buckets[1], explicit.Buckets[9] = 1, 1
					distinct.Sum, distinct.Minimum, distinct.Maximum = 75, 75, 75
					distinct.Buckets = make([]uint64, len(codexHistogramBounds)+1)
					distinct.Buckets[5] = 1
				}
				legacy.States[0].Pending = []codexMetricAggregateSnapshot{missing, distinct, explicit}
				if explicitFirst {
					legacy.States[0].Pending[0], legacy.States[0].Pending[2] = explicit, missing
				}
				restored, err := unmarshalCodexTelemetryMetricStore(codexMetricMigrationEncode(t, legacy))
				require.NoError(t, err)
				encoded, err := marshalCodexTelemetryMetricStore(restored)
				require.NoError(t, err)
				var migrated codexMetricStoreSnapshot
				require.NoError(t, json.Unmarshal(encoded, &migrated))
				require.Equal(t, 2, migrated.Version)
				require.Len(t, migrated.States[0].Pending, 2, "only identical success labels should merge")
				merged := missing
				merged.Attributes = []any{codexMetricMigrationStringAttribute("kind", "unknown"), missing.Attributes[0], missing.Attributes[1]}
				merged.Started, merged.Finished = explicit.Started, explicit.Finished
				merged.Count, merged.Sum = 3, 12
				merged.Minimum, merged.Maximum = 2, 7
				merged.Buckets = []uint64{0, 2, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
				if descriptor.kind == "histogram" {
					merged.Count, merged.Sum, merged.Minimum, merged.Maximum = 4, 1005, 5, 600
					merged.Buckets = make([]uint64, len(codexHistogramBounds)+1)
					merged.Buckets[1], merged.Buckets[6], merged.Buckets[8], merged.Buckets[9] = 1, 1, 1, 1
				}
				distinct.Attributes = []any{
					codexMetricMigrationStringAttribute("kind", "unknown"),
					missing.Attributes[0], codexMetricMigrationStringAttribute("success", "false"),
				}
				require.ElementsMatch(t, []codexMetricAggregateSnapshot{merged, distinct}, migrated.States[0].Pending,
					"merge must retain count, sum, extrema, buckets and the full time interval in either input order")

				v2, err := unmarshalCodexTelemetryMetricStore(encoded)
				require.NoError(t, err)
				reencoded, err := marshalCodexTelemetryMetricStore(v2)
				require.NoError(t, err)
				require.JSONEq(t, string(encoded), string(reencoded))
				batches := v2.flush(base.Add(time.Minute))
				require.Len(t, batches, 1)
				points := codexMetricsTestMetric(batches[0].body, descriptor.name).Get(descriptor.kind + ".dataPoints").Array()
				require.Len(t, points, 2)
				successes := make(map[string]bool)
				for _, point := range points {
					require.Equal(t, "unknown", codexMetricsTestAttribute(point, "kind"))
					success := codexMetricsTestAttribute(point, "success")
					successes[success] = true
					expected := distinct
					if success == "true" {
						expected = merged
					}
					if descriptor.kind == "sum" {
						require.EqualValues(t, expected.Sum, point.Get("asInt").Int())
					} else {
						require.Equal(t, expected.Count, point.Get("count").Uint())
						require.Equal(t, expected.Sum, point.Get("sum").Float())
					}
				}
				require.Equal(t, map[string]bool{"true": true, "false": true}, successes)
			})
		}
	}
}

func TestCodexTelemetryMetricsV2EventSnapshotNormalizesPrivateKinds(t *testing.T) {
	for _, descriptor := range codexMetricMigrationEventDescriptors {
		t.Run(descriptor.name, func(t *testing.T) {
			snapshot := codexMetricMigrationLegacyEventSnapshot(t, descriptor)
			snapshot.Version = 2
			original := snapshot.States[0].Pending[0]
			private := original
			private.Attributes = append([]any{codexMetricMigrationStringAttribute("kind", "private-token-value")}, original.Attributes...)
			unknown := original
			unknown.Attributes = []any{original.Attributes[1], codexMetricMigrationStringAttribute("kind", "unknown"), original.Attributes[0]}
			known := original
			known.Attributes = append([]any{codexMetricMigrationStringAttribute("kind", "response.completed")}, original.Attributes...)
			snapshot.States[0].Pending = []codexMetricAggregateSnapshot{private, known, unknown}

			restored, err := unmarshalCodexTelemetryMetricStore(codexMetricMigrationEncode(t, snapshot))
			require.NoError(t, err)
			encoded, err := marshalCodexTelemetryMetricStore(restored)
			require.NoError(t, err)
			require.NotContains(t, string(encoded), "private-token-value", "unknown event kinds must never survive normalization")
			var normalized codexMetricStoreSnapshot
			require.NoError(t, json.Unmarshal(encoded, &normalized))
			require.Len(t, normalized.States[0].Pending, 2)
			merged := original
			merged.Attributes = append([]any{codexMetricMigrationStringAttribute("kind", "unknown")}, original.Attributes...)
			merged.Count, merged.Sum = original.Count*2, original.Sum*2
			merged.Buckets = make([]uint64, len(original.Buckets))
			for index, count := range original.Buckets {
				merged.Buckets[index] = count * 2
			}
			require.ElementsMatch(t, []codexMetricAggregateSnapshot{merged, known}, normalized.States[0].Pending,
				"unrecognized kinds must merge with unknown while an allowlisted kind remains separate")

			v2, err := unmarshalCodexTelemetryMetricStore(encoded)
			require.NoError(t, err)
			reencoded, err := marshalCodexTelemetryMetricStore(v2)
			require.NoError(t, err)
			require.JSONEq(t, string(encoded), string(reencoded))
			batches := v2.flush(snapshot.States[0].CollectedAt.Add(time.Minute))
			require.Len(t, batches, 1)
			require.NotContains(t, string(batches[0].body), "private-token-value")
			points := codexMetricsTestMetric(batches[0].body, descriptor.name).Get(descriptor.kind + ".dataPoints").Array()
			require.Len(t, points, 2)
			kinds := make(map[string]bool)
			for _, point := range points {
				kinds[codexMetricsTestAttribute(point, "kind")] = true
				require.Equal(t, "true", codexMetricsTestAttribute(point, "success"))
			}
			require.Equal(t, map[string]bool{"unknown": true, "response.completed": true}, kinds)
		})
	}
}
