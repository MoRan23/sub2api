package service

import (
	"bytes"
	"fmt"
	"runtime"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestOpenAIRequestTimezoneUnchangedBodyUsesImmutableStorage(t *testing.T) {
	for _, body := range [][]byte{nil, {}, []byte(`{"model":"original","input":"hello"}`)} {
		_, state := prepareOpenAIRequestTimezoneBody(body, openai.RequestPolicy{}, timezoneTestAcceptedAt(), false, false, false)
		for _, active := range []*RequestTimezoneState{nil, state} {
			applied, ok := active.ApplyToBody(body)
			require.True(t, ok)
			undone, ok := active.UndoToBody(body)
			require.True(t, ok)
			projected, _, ok := active.ProjectToTarget(body, openAIRequestSearchLocation())
			require.True(t, ok)
			for _, output := range [][]byte{applied, undone, projected} {
				require.Equal(t, body, output)
				if len(body) > 0 {
					require.Same(t, &body[0], &output[0])
				} else {
					require.Equal(t, body == nil, output == nil)
				}
			}
		}
	}
}

func TestOpenAIRequestTimezonePublicScanOwnsDiagnosticStrings(t *testing.T) {
	body := []byte(`{"tools":[{"type":"web_search","user_location":{"type":"approximate","country":"GB","region":"England","city":"London","timezone":"UTC"}}]}`)
	scan := ScanOpenAIRequestTimezones(body)
	require.Len(t, scan.Items, 1)
	for i := range body {
		body[i] = '!'
	}
	require.Equal(t, "UTC", scan.Items[0].Value)
	require.Equal(t, &RequestLocationObservation{Type: "approximate", Country: "GB", Region: "England", City: "London", Timezone: "UTC"}, scan.Items[0].Location)
}

func TestOpenAIRequestTimezoneLargeUnchangedSnapshotsDoNotCopyBody(t *testing.T) {
	// A body-sized allocation must exceed the entire test budget. The scanner
	// still inspects the request, but there are no timezone fields to rewrite.
	body := []byte(`{"model":"original","opaque":"` + strings.Repeat("x", 4<<20) + `","input":[]}`)
	policy := timezoneTestPolicy()
	c, _ := newOpenAIIdentityPathContext(t, "/responses", body, 89)
	c.Request = c.Request.WithContext(openai.WithRequestPolicy(c.Request.Context(), policy))
	svc := &OpenAIGatewayService{}
	wasEnabled := globalFingerprintObserver.enabled.Swap(false)
	t.Cleanup(func() { globalFingerprintObserver.enabled.Store(wasEnabled) })
	// Warm up timezone loading before measuring request-owned allocations.
	_, warm := prepareOpenAIRequestTimezoneBody(nil, policy, timezoneTestAcceptedAt(), false, false, false)
	warm.WithTarget(openAIRequestSearchLocation())
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	svc.CaptureOpenAIRequestTimezone(c, body)
	prepared, state := prepareOpenAIRequestTimezoneBody(body, policy, timezoneTestAcceptedAt(), false, false, false)
	projected := state.WithTarget(openAIRequestSearchLocation())
	applied, ok := projected.ApplyToBody(prepared)
	undone, undoOK := projected.UndoToBody(applied)
	runtime.ReadMemStats(&after)
	runtime.KeepAlive(projected)
	runtime.KeepAlive(undone)
	require.True(t, ok)
	require.True(t, undoOK)
	require.Less(t, after.TotalAlloc-before.TotalAlloc, uint64(len(body)/2), "unchanged timezone snapshots allocated a request-sized buffer")
	captured, _ := c.Get(openAIRequestTimezoneCaptureKey)
	for _, output := range [][]byte{captured.(*openAIRequestTimezoneCapture).body, prepared, state.preparedBody, projected.preparedBody, applied, undone} {
		require.Same(t, &body[0], &output[0])
	}
}

func TestOpenAIRequestTimezoneSharedSnapshotsIsolateAccountEdits(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		t.Run(fmt.Sprintf("passthrough_%t", passthrough), func(t *testing.T) {
			body := timezoneTestBody(t, map[string]any{
				"model": "original",
				"input": timezoneTestInput(timezoneTestEnvironment("UTC", "2020-01-01")),
				"tools": []any{map[string]any{"type": "web_search", "user_location": map[string]any{"timezone": "UTC", "city": "London"}}},
			})
			original := bytes.Clone(body)
			prepared, baseline := prepareOpenAIRequestTimezoneBody(body, timezoneTestPolicy(), timezoneTestAcceptedAt(), passthrough, true, false)
			preparedSnapshot := bytes.Clone(prepared)
			first := baseline.WithTarget(openAIRequestSearchLocation())
			require.Same(t, &prepared[0], &first.preparedBody[0], "same-target attempt should share the immutable snapshot")
			first.Conversions[0].Output = "first account only"
			first.Inbound.Items[0].Value = "first account only"
			first.projectionSources[1].occurrence.item.Location.City = "first account only"
			firstBody, err := sjson.SetBytes(prepared, "model", "account-one")
			require.NoError(t, err)
			firstBody, err = sjson.SetBytes(firstBody, "input.0.content.0.text", "first account changed content")
			require.NoError(t, err)
			rejected, ok := first.ApplyToBody(firstBody)
			require.False(t, ok)
			require.Same(t, &firstBody[0], &rejected[0], "rejection should preserve the caller's unchanged immutable body")
			tokyo := RequestLocationObservation{Type: "approximate", Country: "JP", Region: "Tokyo", City: "Tokyo", Timezone: "Asia/Tokyo"}
			second := baseline.WithTarget(tokyo)
			secondBody, err := sjson.SetBytes(body, "model", "account-two")
			require.NoError(t, err)
			secondBody, ok = second.ApplyToBody(secondBody)
			require.True(t, ok)
			require.Equal(t, "account-two", gjson.GetBytes(secondBody, "model").String())
			require.Contains(t, gjson.GetBytes(secondBody, "input.0.content.0.text").String(), "<current_date>2026-09-10</current_date>")
			require.Equal(t, "Asia/Tokyo", gjson.GetBytes(secondBody, "tools.0.user_location.timezone").String())
			require.Equal(t, "UTC", baseline.Inbound.Items[0].Value)
			require.Equal(t, OpenAIRequestTimezone, baseline.Conversions[0].Output)
			require.Equal(t, "London", baseline.projectionSources[1].occurrence.item.Location.City)
			require.Equal(t, original, body, "account conversion changed the ingress baseline")
			require.Equal(t, preparedSnapshot, prepared, "account conversion changed the shared prepared snapshot")
			undone, ok := second.UndoToBody(secondBody)
			require.True(t, ok)
			require.Equal(t, "UTC", gjson.GetBytes(undone, "tools.0.user_location.timezone").String())
			require.Equal(t, "Asia/Tokyo", gjson.GetBytes(secondBody, "tools.0.user_location.timezone").String(), "undo changed the account's body in place")
			current, ok := second.ApplyToBody(secondBody)
			require.True(t, ok)
			require.Same(t, &secondBody[0], &current[0], "an already applied projection should share the body")
			retried, _, ok := second.ProjectToTarget(secondBody, tokyo)
			require.True(t, ok)
			require.Same(t, &secondBody[0], &retried[0], "retrying the same target should not undo and reapply the body")
		})
	}
}

func BenchmarkOpenAIRequestTimezoneImmutableSnapshots(b *testing.B) {
	for _, bodySize := range []int{4 << 20, 64 << 20} {
		b.Run(fmt.Sprintf("body_%d_MiB", bodySize>>20), func(b *testing.B) {
			body := []byte(`{"model":"original","opaque":"` + strings.Repeat("x", bodySize) + `","input":[]}`)
			b.ReportAllocs()
			b.SetBytes(int64(len(body)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				prepared, state := prepareOpenAIRequestTimezoneBody(body, timezoneTestPolicy(), timezoneTestAcceptedAt(), false, false, false)
				state = state.WithTarget(openAIRequestSearchLocation())
				output, _ := state.ApplyToBody(prepared)
				runtime.KeepAlive(output)
			}
		})
	}
}
