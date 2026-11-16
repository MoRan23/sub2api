//go:build linux && unit

package service

import (
	"encoding/json"
	"io"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
)

// Run one scenario per process so the previous algorithm cannot contaminate
// peak RSS or GC-retained memory. Fixtures are built before measurements.
func TestPerformanceProcessSample(t *testing.T) {
	scenario := os.Getenv("SUB2_PERF_SCENARIO")
	if scenario == "" {
		t.Skip("opt-in standalone process measurement")
	}
	legacy := os.Getenv("SUB2_PERF_LEGACY") == "true"
	n := 32
	fragment := strings.Repeat("x", 256)
	var operation func()
	switch scenario {
	case "text_1m":
		operation = func() {
			if legacy {
				var value string
				for i := 0; i < (1<<20)/256; i++ {
					value += fragment
				}
				forwardingBenchString = value
				return
			}
			response := &apicompat.AnthropicResponse{Content: []apicompat.AnthropicContentBlock{{Type: "text"}}}
			var a anthropicBufferedContent
			a.reset(response)
			delta := &apicompat.AnthropicDelta{Type: "text_delta", Text: fragment}
			for i := 0; i < (1<<20)/256; i++ {
				a.applyDelta(0, delta, true)
			}
			a.finalize()
			forwardingBenchString = response.Content[0].Text
		}
	case "tool_1m":
		operation = func() {
			initial := json.RawMessage("{}")
			if legacy {
				value := legacyAppendRawJSON(initial, `{"text":"`)
				for i := 0; i < (1<<20)/256; i++ {
					value = legacyAppendRawJSON(value, fragment)
				}
				forwardingBenchBytes = legacyAppendRawJSON(value, `"}`)
				return
			}
			var value anthropicBufferedBlock
			value.appendInput(initial, `{"text":"`)
			for i := 0; i < (1<<20)/256; i++ {
				value.appendInput(initial, fragment)
			}
			value.appendInput(initial, `"}`)
			forwardingBenchBytes = value.input
		}
	case "ws_16m":
		payload := []byte(`{"model":"m","input":"` + strings.Repeat("x", 16<<20) + `","reasoning":{"effort":"high"}}`)
		operation = func() {
			var e error
			if legacy {
				forwardingBenchBytes, e = legacyWSComparable(payload)
			} else {
				forwardingBenchBytes, e = normalizeOpenAIWSPayloadWithoutInputAndPreviousResponseID(payload)
			}
			if e != nil {
				t.Fatal(e)
			}
		}
	case "xlsx_1000", "xlsx_200000":
		if legacy {
			t.Skip("old export was browser XLSX; not a Go implementation")
		}
		rows := 1000
		if scenario == "xlsx_200000" {
			rows, n = 200000, 3
		}
		row := usageExportRow(&UsageLog{Model: "synthetic", InputTokens: 128, OutputTokens: 64, CreatedAt: time.Unix(100, 0)}, UsageExportSpec{Language: "en"})
		operation = func() {
			w, e := newUsageXLSX(io.Discard, "en")
			if e != nil {
				t.Fatal(e)
			}
			for i := 0; i < rows; i++ {
				if e = w.WriteRow(row); e != nil {
					t.Fatal(e)
				}
			}
			if e = w.Close(); e != nil {
				t.Fatal(e)
			}
		}
	default:
		t.Fatal("unknown measurement scenario")
	}
	operation()
	forwardingBenchBytes = nil
	forwardingBenchString = ""
	runtime.GC()
	var before, after runtime.MemStats
	var cpuBefore, cpuAfter syscall.Rusage
	runtime.ReadMemStats(&before)
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &cpuBefore)
	samples := make([]time.Duration, n)
	start := time.Now()
	for i := range samples {
		at := time.Now()
		operation()
		samples[i] = time.Since(at)
	}
	wall := time.Since(start)
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &cpuAfter)
	runtime.ReadMemStats(&after)
	forwardingBenchBytes = nil
	forwardingBenchString = ""
	runtime.GC()
	var stable runtime.MemStats
	runtime.ReadMemStats(&stable)
	slices.Sort(samples)
	cpu := func(v syscall.Rusage) float64 {
		return float64(v.Utime.Sec+v.Stime.Sec) + float64(v.Utime.Usec+v.Stime.Usec)/1e6
	}
	rss := int64(0)
	if status, e := os.ReadFile("/proc/self/status"); e == nil {
		for _, line := range strings.Split(string(status), "\n") {
			if strings.HasPrefix(line, "VmHWM:") {
				v := strings.Fields(line)
				if len(v) > 1 {
					rss, _ = strconv.ParseInt(v[1], 10, 64)
					rss *= 1024
				}
			}
		}
	}
	result := map[string]any{"scenario": scenario, "legacy": legacy, "iterations": n, "wall_ms_per_op": float64(wall.Microseconds()) / 1000 / float64(n), "p95_ms": float64(samples[(n*95+99)/100-1].Microseconds()) / 1000, "cpu_ms_per_op": (cpu(cpuAfter) - cpu(cpuBefore)) * 1000 / float64(n), "bytes_per_op": (after.TotalAlloc - before.TotalAlloc) / uint64(n), "allocs_per_op": (after.Mallocs - before.Mallocs) / uint64(n), "stable_heap_bytes": stable.HeapAlloc, "peak_rss_bytes": rss, "go_version": runtime.Version()}
	data, _ := json.Marshal(result)
	t.Log(string(data))
}
