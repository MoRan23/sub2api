package service

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

var codexSkillsMigrationNames = []string{
	"codex.thread.skills.enabled_total", "codex.thread.skills.kept_total",
	"codex.thread.skills.truncated", "codex.thread.skills.description_truncated_chars",
}

func telemetryLegacySkillsSnapshot(t *testing.T, profile codexTelemetryProfile, version int) codexMetricStoreSnapshot {
	t.Helper()
	snapshot := codexMetricMigrationLegacySnapshot(t, profile)
	snapshot.Version = version
	if version == 2 {
		snapshot.States[0].Pending = snapshot.States[0].Pending[:2]
	}
	for _, name := range codexSkillsMigrationNames {
		buckets := make([]uint64, 16) // Historical coarse distribution, independent of new bounds.
		buckets[1] = 2
		snapshot.States[0].Pending = append(snapshot.States[0].Pending, codexMetricAggregateSnapshot{
			Name: name, Kind: "histogram", Attributes: []any{}, Started: profile.started,
			Finished: profile.started.Add(time.Second), Count: 2, Sum: 4, Minimum: 1, Maximum: 3, Buckets: buckets,
		})
	}
	return snapshot
}

func TestCodexTelemetrySkillsBoundsAndV3RoundTrip(t *testing.T) {
	for index, name := range codexSkillsMigrationNames {
		t.Run(name, func(t *testing.T) {
			descriptor := codexMetricDescriptor{name: name, kind: "histogram"}
			bounds := codexMetricBounds(descriptor)
			counts := []int{513, 513, 2, 511}
			maxima := []float64{512, 512, 1, 131072}
			require.Len(t, bounds, counts[index])
			require.Equal(t, float64(0), bounds[0])
			require.Equal(t, maxima[index], bounds[len(bounds)-1])
			for i := 1; i < len(bounds); i++ {
				require.Greater(t, bounds[i], bounds[i-1])
				if index < 2 {
					require.Equal(t, float64(i), bounds[i])
				} else if index == 3 {
					require.Equal(t, math.Pow(2, 17*float64(i-1)/509), bounds[i])
				}
			}
			profile := runtimeTestAttempt(time.Unix(1800000000, 0).UTC(), "windows", "turn", "sample").profile
			store := newCodexTelemetryMetricStore()
			state, _ := store.state(profile, profile.started)
			state.add(descriptor, bounds[len(bounds)-1], profile, "completed", profile.started.Add(time.Second))
			encoded, err := marshalCodexTelemetryMetricStore(store)
			require.NoError(t, err)
			var snapshot codexMetricStoreSnapshot
			require.NoError(t, json.Unmarshal(encoded, &snapshot))
			require.Equal(t, 3, snapshot.Version)
			restored, err := unmarshalCodexTelemetryMetricStore(encoded)
			require.NoError(t, err)
			again, err := marshalCodexTelemetryMetricStore(restored)
			require.NoError(t, err)
			require.JSONEq(t, string(encoded), string(again))
			restored.states[codexMetricStateKey(profile)].add(descriptor, bounds[len(bounds)-1]+1, profile, "completed", profile.started.Add(2*time.Second))
			batches := restored.flush(profile.started.Add(time.Minute))
			require.Len(t, batches, 1)
			point := codexMetricsTestMetric(batches[0].body, name).Get("histogram.dataPoints.0")
			require.Len(t, point.Get("explicitBounds").Array(), len(bounds))
			buckets := point.Get("bucketCounts").Array()
			require.Len(t, buckets, len(bounds)+1)
			require.EqualValues(t, 1, buckets[len(bounds)-1].Uint())
			require.EqualValues(t, 1, buckets[len(bounds)].Uint())
			require.EqualValues(t, 2, point.Get("count").Uint())
		})
	}
}

func TestCodexTelemetryLegacySkillsMigrationPreservesPoolAndOtherData(t *testing.T) {
	for _, version := range []int{1, 2} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			store, ok := NewMemoryCodexTelemetryStore().(*MemoryCodexTelemetryStore)
			require.True(t, ok)
			now := time.Unix(1800000000, 0).UTC()
			attempt := runtimeTestAttempt(now, "windows", "turn", "sample")
			var original codexMetricStoreSnapshot
			var sealedID string
			sealed := []byte(`{"resourceMetrics":[{"scopeMetrics":[{"metrics":[]}]}]}`)
			pool, err := store.TransactPool(context.Background(), attempt.poolKey, now, func(tx *CodexTelemetryPoolTransaction) error {
				if err := syncCodexTelemetryRuntimeEpoch(tx, now); err != nil {
					return err
				}
				profile := attempt.profile
				profile.poolID, profile.scenarioSeed = tx.Pool.ID, tx.Pool.Seed
				original = telemetryLegacySkillsSnapshot(t, profile, version)
				tx.Activities[CodexTelemetryActivityMapKey("metrics", "current")] = CodexTelemetryActivity{Kind: "metrics", Key: "current", Data: codexMetricMigrationEncode(t, original), DueAt: now.Add(time.Minute)}
				if err := putRuntimeActivity(tx, "thread", "preserved", codexTelemetryRuntimeThread{Initialized: true}, now, time.Time{}); err != nil {
					return err
				}
				if err := appendRuntimeBatch(tx, profile, "metrics", sealed, now); err != nil {
					return err
				}
				sealedID = tx.Batches[0].ID
				return nil
			})
			require.NoError(t, err)
			restored, err := unmarshalCodexTelemetryMetricStore(codexMetricMigrationEncode(t, original))
			require.NoError(t, err)
			encoded, err := marshalCodexTelemetryMetricStore(restored)
			require.NoError(t, err)
			var migrated codexMetricStoreSnapshot
			require.NoError(t, json.Unmarshal(encoded, &migrated))
			expected := original.States[0]
			expected.Pending = expected.Pending[:2]
			require.Equal(t, []codexMetricStateSnapshot{expected}, migrated.States)
			require.Equal(t, original.Clients, migrated.Clients)
			reason := codexMetricLegacySkillsDiscarded
			if version == 1 {
				reason = codexMetricLegacyBytesAndSkillsDiscarded
			}
			require.Equal(t, reason, migrated.CompatibilityReason)
			again, err := unmarshalCodexTelemetryMetricStore(encoded)
			require.NoError(t, err)
			reencoded, err := marshalCodexTelemetryMetricStore(again)
			require.NoError(t, err)
			require.JSONEq(t, string(encoded), string(reencoded))
			after, err := store.TransactPool(context.Background(), attempt.poolKey, now.Add(time.Minute), func(tx *CodexTelemetryPoolTransaction) error {
				return tickCodexTelemetryRuntime(tx, now.Add(time.Minute))
			})
			require.NoError(t, err)
			require.Equal(t, pool.ID, after.ID)
			require.Equal(t, pool.Seed, after.Seed)
			require.Equal(t, pool.TemplateVersion, after.TemplateVersion)
			require.Equal(t, sealed, []byte(store.batches[sealedID].Payload))
			require.Contains(t, store.activities[attempt.poolKey], CodexTelemetryActivityMapKey("thread", "preserved"))
			require.Len(t, store.batches, 2)
			for id, batch := range store.batches {
				if id == sealedID {
					continue
				}
				require.EqualValues(t, 1, codexMetricsTestMetric(batch.Payload, "codex.process.start").Get("sum.dataPoints.0.asInt").Int())
				require.EqualValues(t, 400, codexMetricsTestMetric(batch.Payload, "codex.turn.e2e_duration_ms").Get("histogram.dataPoints.0.sum").Float())
				for _, name := range codexSkillsMigrationNames {
					require.False(t, codexMetricsTestMetric(batch.Payload, name).Exists())
				}
				require.NotContains(t, string(batch.Payload), reason)
			}
			activity := store.activities[attempt.poolKey][CodexTelemetryActivityMapKey("metrics", "current")]
			var saved codexMetricStoreSnapshot
			require.NoError(t, json.Unmarshal(activity.Data, &saved))
			require.Equal(t, 3, saved.Version)
			require.Equal(t, original.Clients, saved.Clients)
			require.True(t, saved.States[0].ExternalAgentSent)
			require.Empty(t, saved.States[0].Pending)
		})
	}
}

func TestCodexTelemetrySkillsMigrationRejectsInvalidLegacyShapes(t *testing.T) {
	for _, version := range []int{1, 2, 3} {
		for _, shape := range []string{"kind", "unit", "buckets"} {
			t.Run(fmt.Sprintf("v%d/%s", version, shape), func(t *testing.T) {
				snapshot := telemetryLegacySkillsSnapshot(t, codexTelemetryEventTestProfile(), 2)
				snapshot.Version = version
				item := &snapshot.States[0].Pending[2]
				switch shape {
				case "kind":
					item.Kind = "sum"
				case "unit":
					item.Unit = "ms"
				case "buckets":
					item.Buckets = []uint64{2}
				}
				_, err := unmarshalCodexTelemetryMetricStore(codexMetricMigrationEncode(t, snapshot))
				require.Error(t, err)
			})
		}
	}
}
