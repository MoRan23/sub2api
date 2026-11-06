package service

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

// BenchmarkUsageRecordQueueRetainedHeap measures live heap while billing is
// deliberately backlogged, rather than cumulative allocation volume. Each task
// has a distinct 1 MiB payload so sharing cannot hide request-body retention.
// Run with -benchtime=1x -count=5; the useful metrics are retained-B/task and
// retained/payload. ns/op includes GC and is not a throughput measurement.
func BenchmarkUsageRecordQueueRetainedHeap(b *testing.B) {
	const payloadBytes = 1 << 20
	for _, kind := range []string{"ws_replay", "systemone_response", "grok_request"} {
		for _, queued := range []int{32, 128} {
			b.Run(fmt.Sprintf("%s/queued=%d", kind, queued), func(b *testing.B) {
				var retainedTotal int64
				for i := 0; i < b.N; i++ {
					retainedTotal += measureUsageRecordQueuedHeap(b, kind, queued, payloadBytes)
				}
				retainedPerTask := float64(retainedTotal) / float64(b.N*queued)
				b.ReportMetric(retainedPerTask, "retained-B/task")
				b.ReportMetric(retainedPerTask/payloadBytes, "retained/payload")
			})
		}
	}
}

func measureUsageRecordQueuedHeap(b *testing.B, kind string, queued, payloadBytes int) int64 {
	b.Helper()
	pool := NewUsageRecordWorkerPoolWithOptions(UsageRecordWorkerPoolOptions{
		WorkerCount: 1, QueueSize: queued + 1, TaskTimeout: time.Minute,
		OverflowPolicy: config.UsageRecordOverflowPolicyDrop,
	})
	gate, started := make(chan struct{}), make(chan struct{})
	var release sync.Once
	b.Cleanup(func() {
		release.Do(func() { close(gate) })
		pool.Stop()
	})
	if mode := pool.Submit(func(context.Context) { close(started); <-gate }); mode != UsageRecordSubmitModeEnqueued {
		b.Fatalf("blocking task submission: %s", mode)
	}
	<-started
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	var completed atomic.Int64
	for i := 0; i < queued; i++ {
		task := usageRecordRetentionFixture(kind, payloadBytes)
		if mode := pool.Submit(func(ctx context.Context) {
			task(ctx)
			completed.Add(1)
		}); mode != UsageRecordSubmitModeEnqueued {
			b.Fatalf("billing task submission: %s", mode)
		}
	}
	if waiting := pool.Stats().WaitingTasks; waiting != uint64(queued) {
		b.Fatalf("waiting tasks: got %d, want %d", waiting, queued)
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	retained := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	release.Do(func() { close(gate) })
	pool.Stop()
	if got := completed.Load(); got != int64(queued) {
		b.Fatalf("completed tasks: got %d, want %d", got, queued)
	}
	return retained
}

// Keep these payloads and queue sizes identical to the pre-projection baseline.
// Each case now passes through the same production boundary as its handler.
func usageRecordRetentionFixture(kind string, payloadBytes int) UsageRecordTask {
	payload := make([]byte, payloadBytes)
	payload[0], payload[len(payload)-1] = 'x', 'y'
	switch kind {
	case "ws_replay":
		result := &OpenAIForwardResult{
			Model: "gpt-5", Usage: OpenAIUsage{InputTokens: 100, OutputTokens: 10},
			wsReplayInput: []json.RawMessage{payload},
		}
		record := SnapshotOpenAIRecordUsageInput(&OpenAIRecordUsageInput{Result: result})
		return func(context.Context) { runtime.KeepAlive(record) }
	case "systemone_response":
		result := &SystemOneForwardResult{
			ForwardResult: ForwardResult{Model: "test-model", Usage: ClaudeUsage{InputTokens: 100, OutputTokens: 10}},
			Body:          payload,
		}
		record := SnapshotRecordUsageInput(&RecordUsageInput{Result: &result.ForwardResult})
		return func(context.Context) { runtime.KeepAlive(record) }
	case "grok_request":
		record := SnapshotOpenAIRecordUsageInput(&OpenAIRecordUsageInput{RequestPayloadHash: HashUsageRequestPayload(payload)})
		return func(context.Context) { runtime.KeepAlive(record) }
	default:
		panic("unknown usage retention fixture: " + kind)
	}
}
