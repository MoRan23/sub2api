package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"runtime/pprof"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/stretchr/testify/require"
)

// All inputs are synthetic. Sizes describe the complete JSON body, including
// framing, so 8 MiB exercises the accepted limit and 8 MiB+1 the rejected limit.
func optimizationIntegrityFixture(size int, shape string) ([]byte, []byte, RequestIntegrityCheckOptions) {
	body := map[string]any{"model": "gpt-5", "instructions": "benchmark", "input": ""}
	if shape == "metadata" {
		body["metadata"] = ""
	}
	if shape == "timezone" {
		body["tools"] = []any{map[string]any{"type": "web_search_preview", "user_location": map[string]any{"timezone": "Asia/Tokyo", "country": "JP"}}}
	}
	paddingField := "input"
	if shape == "metadata" {
		paddingField = "metadata"
	}
	encoded, _ := json.Marshal(body)
	body[paddingField] = strings.Repeat("x", max(0, size-len(encoded)))
	baseline, _ := json.Marshal(body)
	wire := baseline
	opts := RequestIntegrityCheckOptions{Transport: "http"}
	if shape == "transformed" {
		wire = bytes.Replace(baseline, []byte(`"gpt-5"`), []byte(`"gpt-6"`), 1)
		opts.ExpectedModel = "gpt-6"
	}
	if shape == "timezone" {
		wire, opts.TimezoneState = PrepareOpenAIRequestTimezone(baseline, openai.DefaultRequestPolicy(), time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC), false, true)
	}
	return baseline, wire, opts
}

func BenchmarkOptimizationIntegrity(b *testing.B) {
	for _, size := range []int{64 << 10, 1 << 20, 4 << 20, 8 << 20, (8 << 20) + 1} {
		for _, shape := range []string{"content", "metadata", "transformed", "timezone"} {
			baseline, wire, opts := optimizationIntegrityFixture(size, shape)
			for _, sends := range []int{1, 5} {
				b.Run(fmt.Sprintf("bytes=%d/%s/sends=%d", size, shape, sends), func(b *testing.B) {
					account := integrityTestAccount()
					b.SetBytes(int64(len(baseline)))
					b.ReportAllocs()
					for b.Loop() {
						state := NewOpenAIRequestIntegrityState(true, "responses", baseline)
						for range sends {
							state.Check(account, wire, opts)
						}
					}
				})
			}
		}
	}
}

func BenchmarkOptimizationIntegrityParallel(b *testing.B) {
	for _, size := range []int{64 << 10, 1 << 20, 4 << 20, 8 << 20} {
		for _, sends := range []int{1, 5} {
			baseline, wire, opts := optimizationIntegrityFixture(size, "transformed")
			b.Run(fmt.Sprintf("bytes=%d/sends=%d", size, sends), func(b *testing.B) {
				b.SetBytes(int64(len(baseline)))
				b.ReportAllocs()
				b.RunParallel(func(pb *testing.PB) {
					account := integrityTestAccount()
					for pb.Next() {
						state := NewOpenAIRequestIntegrityState(true, "responses", baseline)
						for range sends {
							state.Check(account, wire, opts)
						}
					}
				})
			})
		}
	}
}

// The general matrix's multi-MiB input text intentionally retains the original
// timezone scanner limit. This supplemental fixture keeps inspected text small
// and pads ignored metadata, so every size exercises an actual frozen patch.
// Size is the larger of ingress and rewritten wire JSON, both within the limit.
func optimizationActiveTimezoneFixture(tb testing.TB, size int) ([]byte, []byte, RequestIntegrityCheckOptions) {
	tb.Helper()
	body := map[string]any{"model": "gpt-5", "input": "hello", "metadata": "", "tools": []any{map[string]any{"type": "web_search_preview", "user_location": map[string]any{"timezone": "Asia/Tokyo", "country": "JP"}}}}
	acceptedAt := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	empty, _ := json.Marshal(body)
	prepared, _ := PrepareOpenAIRequestTimezone(empty, openai.DefaultRequestPolicy(), acceptedAt, false, true)
	body["metadata"] = strings.Repeat("x", size-max(len(empty), len(prepared)))
	baseline, _ := json.Marshal(body)
	wire, timezone := PrepareOpenAIRequestTimezone(baseline, openai.DefaultRequestPolicy(), acceptedAt, false, true)
	if bytes.Equal(baseline, wire) || max(len(baseline), len(wire)) != size {
		tb.Fatal("timezone fixture must rewrite and fit the requested size")
	}
	return baseline, wire, RequestIntegrityCheckOptions{Transport: "http", ExpectedModel: "gpt-5", TimezoneState: timezone}
}

func BenchmarkOptimizationTimezoneProjection(b *testing.B) {
	for _, size := range []int{64 << 10, 1 << 20, 4 << 20, 8 << 20} {
		baseline, wire, opts := optimizationActiveTimezoneFixture(b, size)
		oldOpts := PerformanceReferenceIntegrityCheckOptions{Transport: opts.Transport, ExpectedModel: opts.ExpectedModel, TimezoneState: opts.TimezoneState}
		for _, sends := range []int{1, 5} {
			for _, legacy := range []bool{true, false} {
				b.Run(fmt.Sprintf("bytes=%d/sends=%d/legacy=%t", size, sends, legacy), func(b *testing.B) {
					account := integrityTestAccount()
					if legacy {
						got := NewOpenAIPerformanceReferenceIntegrityState(true, "responses", baseline).Check(account, wire, oldOpts)
						if got.Status != "expected_transform" {
							b.Fatalf("legacy fixture status: %s (%s)", got.Status, got.Reason)
						}
					} else {
						got := NewOpenAIRequestIntegrityState(true, "responses", baseline).Check(account, wire, opts)
						if got.Status != "expected_transform" {
							b.Fatalf("fixture status: %s (%s)", got.Status, got.Reason)
						}
					}
					b.ReportAllocs()
					b.SetBytes(int64(size))
					for b.Loop() {
						if legacy {
							state := NewOpenAIPerformanceReferenceIntegrityState(true, "responses", baseline)
							for range sends {
								state.Check(account, wire, oldOpts)
							}
						} else {
							state := NewOpenAIRequestIntegrityState(true, "responses", baseline)
							for range sends {
								state.Check(account, wire, opts)
							}
						}
					}
				})
			}
		}
	}
}

func optimizationTelemetryFixture(tb testing.TB, markers int) (CodexTelemetryStore, CodexTelemetryPoolKey, time.Time) {
	tb.Helper()
	store := NewMemoryCodexTelemetryStore()
	key := CodexTelemetryPoolKey{OwnerAccountID: 19, OSFamily: "windows", InstallationID: "benchmark-installation"}
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	_, err := store.TransactPool(context.Background(), key, now, func(tx *CodexTelemetryPoolTransaction) error {
		for i := range markers {
			key := fmt.Sprintf("thread-%09d", i)
			if err := tx.Activities.Put(CodexTelemetryActivity{Kind: "thread", Key: key, Data: json.RawMessage(`{"initialized":true}`), UpdatedAt: now}); err != nil {
				return err
			}
		}
		for i := range 16 {
			key := fmt.Sprintf("transient-%d", i)
			if err := tx.Activities.Put(CodexTelemetryActivity{Kind: "sampling", Key: key, Data: json.RawMessage(`{"status":"active"}`), DueAt: now.Add(time.Hour), UpdatedAt: now}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		tb.Fatal(err)
	}
	return store, key, now
}

func BenchmarkOptimizationTelemetryTransaction(b *testing.B) {
	for _, markers := range []int{1000, 10000, 100000} {
		b.Run(fmt.Sprintf("markers=%d/transients=16", markers), func(b *testing.B) {
			store, key, now := optimizationTelemetryFixture(b, markers)
			b.ReportAllocs()
			for b.Loop() {
				_, err := store.TransactPool(context.Background(), key, now, func(tx *CodexTelemetryPoolTransaction) error {
					tx.Pool.LastBusinessAt = now
					return nil
				})
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func TestOptimizationIntegrityReference(t *testing.T) {
	for _, size := range []int{64 << 10, 1 << 20, 4 << 20, 8 << 20} {
		baseline, wire, opts := optimizationActiveTimezoneFixture(t, size)
		t.Run(fmt.Sprintf("active_timezone_%d", size), func(t *testing.T) { optimizationCompareReference(t, baseline, wire, opts) })
	}
	for _, shape := range []string{"content", "metadata", "transformed", "timezone"} {
		baseline, wire, opts := optimizationIntegrityFixture(64<<10, shape)
		t.Run(shape, func(t *testing.T) { optimizationCompareReference(t, baseline, wire, opts) })
	}
	for _, tc := range []struct{ name, before, after string }{
		{"duplicate_ignored", `{"input":"a","metadata":{"x":1,"x":2}}`, `{"input":"a"}`},
		{"duplicate_content", `{"input":"a","input":"b"}`, `{"input":"b"}`},
		{"invalid_ignored", `{"input":"a","metadata":[}`, `{"input":"a"}`},
		{"trailing", `{"input":"a"} false`, `{"input":"a"}`},
		{"number", `{"input":"a","reasoning":{"budget":1.0}}`, `{"input":"a","reasoning":{"budget":1}}`},
		{"normalization", `{"model":"gpt-5","input":"a","reasoning":{"effort":"minimal"}}`, `{"model":"gpt-5","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"a"}]}],"reasoning":{"effort":"none"}}`},
		{"unknown_field", `{"input":[{"role":"user","content":"a","unknown":"keep"}]}`, `{"input":[{"role":"user","content":"a"}]}`},
		{"recovery", `{"input":[{"type":"reasoning","encrypted_content":"synthetic"}],"previous_response_id":"test"}`, `{"input":[]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			optimizationCompareReference(t, []byte(tc.before), []byte(tc.after), RequestIntegrityCheckOptions{KnownRecovery: "history_replay"})
		})
	}
	for _, n := range []int{requestIntegrityMaxDepth - 1, requestIntegrityMaxDepth, requestIntegrityMaxDepth + 1} {
		body := []byte(`{"input":"a","metadata":` + strings.Repeat("[", n) + "0" + strings.Repeat("]", n) + "}")
		t.Run(fmt.Sprintf("depth_%d", n), func(t *testing.T) { optimizationCompareReference(t, body, body, RequestIntegrityCheckOptions{}) })
	}
	for _, n := range []int{requestIntegrityMaxNodes - 4, requestIntegrityMaxNodes, requestIntegrityMaxNodes + 1} {
		body := []byte(`{"input":"a","metadata":[` + strings.Repeat("0,", n-1) + "0]}")
		t.Run(fmt.Sprintf("nodes_%d", n), func(t *testing.T) { optimizationCompareReference(t, body, body, RequestIntegrityCheckOptions{}) })
	}
}

func optimizationCompareReference(t *testing.T, baseline, wire []byte, opts RequestIntegrityCheckOptions) {
	t.Helper()
	current := NewOpenAIRequestIntegrityState(true, "responses", baseline)
	reference := NewOpenAIPerformanceReferenceIntegrityState(true, "responses", baseline)
	oldOpts := PerformanceReferenceIntegrityCheckOptions{Transport: opts.Transport, ExpectedModel: opts.ExpectedModel, TimezoneState: opts.TimezoneState, KnownRecovery: opts.KnownRecovery, CompatTodoGuard: opts.CompatTodoGuard}
	for range 5 {
		got, err := json.Marshal(current.Check(integrityTestAccount(), wire, opts))
		require.NoError(t, err)
		want, err := json.Marshal(reference.Check(integrityTestAccount(), wire, oldOpts))
		require.NoError(t, err)
		require.JSONEq(t, string(want), string(got))
	}
}

// Opt-in process workload: exact same compiled test is run before and after.
// live heap is sampled after forced GC while 8 states remain reachable;
// peak_heap_sampled is the maximum observed during the concurrent retry loop,
// not an exact high-water mark. The runner separately records OS working set.
func TestOptimizationMemoryProfile(t *testing.T) {
	if os.Getenv("SUB2API_PERFORMANCE_PROCESS") != "1" {
		t.Skip("opt-in process measurement")
	}
	baseline, wire, opts := optimizationIntegrityFixture(1<<20, "transformed")
	runtime.GC()
	var start runtime.MemStats
	runtime.ReadMemStats(&start)
	states := make([]*OpenAIRequestIntegrityState, 8)
	for i := range states {
		states[i] = NewOpenAIRequestIntegrityState(true, "responses", baseline)
	}
	runtime.GC()
	var held runtime.MemStats
	runtime.ReadMemStats(&held)
	var peak atomic.Uint64
	peak.Store(held.HeapAlloc)
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				var m runtime.MemStats
				runtime.ReadMemStats(&m)
				for old := peak.Load(); m.HeapAlloc > old; old = peak.Load() {
					if peak.CompareAndSwap(old, m.HeapAlloc) {
						break
					}
				}
			}
		}
	}()
	var wg sync.WaitGroup
	for _, state := range states {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 10 {
				for range 5 {
					state.Check(integrityTestAccount(), wire, opts)
				}
			}
		}()
	}
	wg.Wait()
	close(done)
	<-stopped
	runtime.GC()
	var end runtime.MemStats
	runtime.ReadMemStats(&end)
	if path := os.Getenv("SUB2API_PERFORMANCE_HEAP"); path != "" {
		f, err := os.Create(path)
		require.NoError(t, err)
		require.NoError(t, pprof.WriteHeapProfile(f))
		require.NoError(t, f.Close())
	}
	t.Logf("PERFORMANCE_MEMORY baseline_heap=%d held_heap=%d held_delta=%d end_heap=%d peak_heap_sampled=%d gc_cycles=%d total_alloc=%d", start.HeapAlloc, held.HeapAlloc, int64(held.HeapAlloc)-int64(start.HeapAlloc), end.HeapAlloc, peak.Load(), end.NumGC-start.NumGC, end.TotalAlloc-start.TotalAlloc)
	runtime.KeepAlive(states)
	runtime.KeepAlive(baseline)
	runtime.KeepAlive(wire)
}
