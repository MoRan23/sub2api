package securityaudit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

// Opt-in measurement only: explicit GC is not part of production audit code.
// Peak heap is sampled every millisecond, not an OS RSS/high-water measurement.
func TestPromptAuditHeapRetention(t *testing.T) {
	if os.Getenv("PROMPT_AUDIT_HEAP_MEASURE") != "1" {
		t.Skip("set PROMPT_AUDIT_HEAP_MEASURE=1 for controlled heap samples")
	}
	t.Logf("toolchain=%s experiments=greenteagc,jsonv2 gomaxprocs=%d", runtime.Version(), runtime.GOMAXPROCS(0))
	for round := 1; round <= 3; round++ {
		for _, scenario := range []string{"text_snapshot", "image_async_handoff"} {
			for _, references := range []int{1, 5, 8} {
				for _, old := range []bool{true, false} {
					runtime.GC()
					runtime.GC() // clear sync.Pool victim caches before the baseline
					var base, peakSample, held, released runtime.MemStats
					runtime.ReadMemStats(&base)
					var peak atomic.Uint64
					peak.Store(base.HeapAlloc)
					done, stopped := make(chan struct{}), make(chan struct{})
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
								if m.HeapAlloc > peak.Load() {
									peak.Store(m.HeapAlloc)
								}
							}
						}
					}()
					started := time.Now()
					holder := promptHeapFixture(t, scenario, old, references)
					runtime.ReadMemStats(&peakSample)
					close(done)
					<-stopped
					if peakSample.HeapAlloc > peak.Load() {
						peak.Store(peakSample.HeapAlloc)
					}
					runtime.GC()
					runtime.GC() // distinguish live request references from decoder pool caches
					runtime.ReadMemStats(&held)
					runtime.KeepAlive(holder)
					holder = nil
					runtime.GC()
					runtime.GC()
					runtime.ReadMemStats(&released)
					t.Logf("heap round=%d scenario=%s references=%d old=%t allocated=%d sampled_peak_delta=%d held_after_gc_delta=%d released_after_gc_delta=%d elapsed_ms=%d", round, scenario, references, old,
						peakSample.TotalAlloc-base.TotalAlloc, int64(peak.Load())-int64(base.HeapAlloc), int64(held.HeapAlloc)-int64(base.HeapAlloc), int64(released.HeapAlloc)-int64(base.HeapAlloc), time.Since(started).Milliseconds())
					retained := int64(held.HeapAlloc) - int64(base.HeapAlloc)
					if scenario == "image_async_handoff" && !old {
						require.Less(t, retained, int64(1<<20), "short text and role must not retain the image body")
					} else {
						require.Greater(t, retained, int64(4<<20), "fixture must retain its expected body or scan text")
						require.Less(t, retained, int64(12<<20), "retry references must share one payload")
					}
					require.Less(t, int64(released.HeapAlloc)-int64(base.HeapAlloc), int64(1<<20), "request completion must release body and parsed text")
				}
			}
		}
	}
}

//go:noinline
func promptHeapFixture(t *testing.T, scenario string, old bool, references int) any {
	text := "safe current input"
	root := map[string]any{"input": text}
	if scenario == "text_snapshot" {
		root["input"] = strings.Repeat("safe text ", (8<<20)/10)
	} else {
		root["input"] = []any{map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "input_text", "text": text},
			map[string]any{"type": "input_image", "image_url": "data:image/png;base64," + strings.Repeat("A", 8<<20)},
		}}}
	}
	body, err := json.Marshal(root)
	require.NoError(t, err)
	req := Request{Protocol: "responses", Body: body}
	if scenario == "text_snapshot" {
		if old {
			// Original snapshot extraction and its full-string []rune/redaction
			// materialization. Reuse already frozen text across synthetic retries.
			var document any
			require.NoError(t, json.Unmarshal(req.Body, &document))
			segments := normalizeSegmentsLatestUserFirst(extractProtocolSegments(req.Protocol, document))
			scan, metadata := buildPrioritizedScanText(segments)
			preview := originalHeapPromptPreview(metadata, DefaultPromptPreviewMaxRunes)
			full := originalTrimRunes(strings.TrimSpace(strings.ReplaceAll(metadata, "\x00", "")), DefaultFullPromptMaxRunes)
			digest := sha256.Sum256([]byte(metadata))
			snapshot := PromptSnapshot{Protocol: req.Protocol, Stage: "http", ScanText: scan, FullPrompt: full, RedactedPreview: preview, PromptHash: hex.EncodeToString(digest[:]), PromptLength: utf8.RuneCountInString(metadata), MessageCount: len(segments)}
			values := make([]PromptSnapshot, references)
			for i := range values {
				values[i] = snapshot
			}
			return values
		}
		req = preparePromptRequest(req)
		snapshot, err := req.prepared.snapshot(req, false)
		require.NoError(t, err)
		values := make([]PromptSnapshot, references)
		for i := range values {
			values[i] = snapshot
		}
		return values
	}
	if old {
		// Before optimization Coordinator.Clone and Enqueue.Clone ran before
		// the blocked worker decoded the retained request.
		copy := req.Clone().Clone()
		values := make([]Request, references)
		for i := range values {
			values[i] = copy
		}
		return values
	}
	req = preparePromptRequest(req)
	require.NoError(t, req.prepared.prepare(req))
	req.Body = nil
	values := make([]Request, references)
	for i := range values {
		values[i] = req
	}
	return values
}

func originalHeapPromptPreview(value string, limit int) string {
	redacted := strings.TrimSpace(originalRedactPreview(value, limit))
	if redacted == "" {
		return ""
	}
	runes := []rune(redacted)
	truncated := strings.HasSuffix(redacted, "…")
	if truncated && len(runes) > 0 {
		runes = runes[:len(runes)-1]
	}
	if len(runes) == 0 {
		return "***…"
	}
	if len(runes) < 32 {
		if truncated {
			return "***…"
		}
		return "***"
	}
	keep := min(len(runes)/4, 24)
	preview := string(runes[:keep]) + "***"
	if truncated || keep < len(runes) {
		preview += "…"
	}
	return preview
}

// Compare decoded string ownership separately from global encoder/decoder
// sync.Pool caches, which require two forced test GCs to expire.
func TestPromptDecodedTextOwnershipHeap(t *testing.T) {
	if os.Getenv("PROMPT_AUDIT_HEAP_MEASURE") != "1" {
		t.Skip("opt-in heap measurement")
	}
	for round := 1; round <= 3; round++ {
		for _, own := range []bool{false, true} {
			runtime.GC()
			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			value := promptDecodedTextFixture(t, own)
			runtime.GC()
			runtime.GC()
			runtime.ReadMemStats(&after)
			runtime.KeepAlive(value)
			t.Logf("ownership round=%d owned=%t retained=%d", round, own, int64(after.HeapAlloc)-int64(before.HeapAlloc))
			require.Equal(t, "user", value.segments[0].role)
			require.Equal(t, "short text", value.segments[0].text)
			require.Less(t, int64(after.HeapAlloc)-int64(before.HeapAlloc), int64(1<<20))
		}
	}
}

//go:noinline
func promptDecodedTextFixture(t *testing.T, own bool) *preparedPromptInput {
	body := []byte(`{"input":[{"role":"user","content":[{"type":"input_text","text":"short text"},{"type":"input_image","image_url":"data:image/png;base64,` + strings.Repeat("A", 8<<20) + `"}]}]}`)
	req := Request{Protocol: "responses", Body: body}
	req = preparePromptRequest(req)
	require.NoError(t, req.prepared.prepare(req))
	if own {
		for i := range req.prepared.segments {
			req.prepared.segments[i].text = strings.Clone(req.prepared.segments[i].text)
			req.prepared.segments[i].role = strings.Clone(req.prepared.segments[i].role)
		}
		for _, batch := range req.prepared.combination.batches {
			for i := range batch {
				batch[i].original = strings.Clone(batch[i].original)
			}
		}
	}
	return req.prepared
}

func TestPreparedPromptTailMatchAndRetryReferences(t *testing.T) {
	active, err := ActiveFromStorage(storageConfig{KeywordAllGroups: true, KeywordBlockingEnabled: true, BlockedKeywords: []string{"tail-match"}}, true, prefixEncryptor{})
	require.NoError(t, err)
	service := &PromptService{config: &fakeConfigStore{active: true, cfg: active}}
	for _, attempts := range []int{1, 5, 8} {
		t.Run(fmt.Sprint(attempts), func(t *testing.T) {
			body, err := json.Marshal(map[string]any{"input": strings.Repeat("safe ", 1<<18) + "tail-match"})
			require.NoError(t, err)
			req := preparePromptRequest(Request{Protocol: "responses", Body: body})
			decision, err := service.CheckKeyword(context.Background(), req)
			require.NoError(t, err)
			require.Equal(t, DecisionBlock, decision.Kind)
			require.Equal(t, "tail-match", decision.Result.matchedContent)
			want, err := req.prepared.snapshot(req, false)
			require.NoError(t, err)
			for i := range body {
				body[i] = '!'
			}
			req.Body = nil
			for range attempts {
				got, err := req.prepared.snapshot(req, false)
				require.NoError(t, err)
				require.Equal(t, want, got)
			}
		})
	}
}
