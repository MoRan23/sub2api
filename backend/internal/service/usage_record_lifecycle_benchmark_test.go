package service

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"runtime"
	"runtime/metrics"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/tidwall/gjson"
)

// BenchmarkUsageRecordLifecycle complements the forced-GC retained-heap probe.
// Each op copies and parses one body, enqueues usage, and waits for local billing
// to complete. Both ownership modes use the same bounded producer/worker counts,
// actual pool, body processing and pricing/command construction. No simulated
// network/database latency, sleeps or forced GC occur in the timed section.
//
// Run -benchtime=2s -count=3 -benchmem with a fixed GOMAXPROCS. p95-e2e-ns
// includes body handling, enqueue wait, worker work and producer wake-up. Runtime
// CPU is Go's user+GC+scavenger estimate; collect process CPU externally as well.
func BenchmarkUsageRecordLifecycle(b *testing.B) {
	for _, kind := range []string{"ws_replay", "systemone_response", "grok_request"} {
		for _, payloadBytes := range []int{8 << 10, 1 << 20} {
			for _, frozen := range []bool{false, true} {
				ownership := "legacy_capture"
				if frozen {
					ownership = "owned_snapshot"
				}
				b.Run(fmt.Sprintf("%s/bytes=%d/%s", kind, payloadBytes, ownership), func(b *testing.B) {
					benchmarkUsageRecordLifecycle(b, kind, payloadBytes, frozen)
				})
			}
		}
	}
}

type usageLifecyclePayloadKey struct{}

type usageLifecycleFixture struct {
	body     []byte
	key      *APIKey
	user     *User
	account  *Account
	sub      *UserSubscription
	billing  *BillingService
	checksum atomic.Uint64
}

func benchmarkUsageRecordLifecycle(b *testing.B, kind string, payloadBytes int, frozen bool) {
	benchmarkUsageRecordLifecycleWithAuthentication(b, kind, payloadBytes, frozen, false)
}

func benchmarkUsageRecordLifecycleWithAuthentication(b *testing.B, kind string, payloadBytes int, frozen, authenticate bool) {
	b.StopTimer()
	const producers, workers, queueSize = 16, 4, 128
	key, user, account, sub := usageSnapshotTestSubjects()
	var authentication *APIKeyService
	if authenticate {
		authentication = newUsageLifecycleAuthentication(b, key)
	}
	// The same representative immutable subject graph is shared by all requests
	// in both modes. Per-request results and body buffers are always distinct.
	prices := &PricingService{pricingData: map[string]*LiteLLMModelPricing{
		"gpt-4o": {Mode: "chat", InputCostPerToken: 2.5e-6, OutputCostPerToken: 10e-6},
	}}
	fixture := &usageLifecycleFixture{key: key, user: user, account: account, sub: sub, billing: NewBillingService(&config.Config{}, prices)}
	if err := fixture.settle(context.Background(), "gpt-4o", 1000, 100, key, user, account, sub, ""); err != nil {
		b.Fatalf("local billing fixture: %v", err)
	}
	prefix, suffix := "{\"model\":\"gpt-4o\",\"content\":\"", "\",\"usage\":{\"input_tokens\":1000,\"output_tokens\":100}}"
	fixture.body = []byte(prefix + strings.Repeat("x", payloadBytes-len(prefix)-len(suffix)) + suffix)
	pool := NewUsageRecordWorkerPoolWithOptions(UsageRecordWorkerPoolOptions{
		WorkerCount: workers, QueueSize: queueSize, TaskTimeout: time.Minute, OverflowPolicy: config.UsageRecordOverflowPolicyDrop,
	})
	defer pool.Stop()
	durations := make([]int64, b.N)
	var completed, failures atomic.Int64
	var producersDone sync.WaitGroup
	// Forced collection is deliberately outside the timing/allocation boundary.
	runtime.GC()
	cpuBefore := usageLifecycleCPUSeconds()
	b.ReportAllocs()
	b.SetBytes(int64(payloadBytes))
	b.ResetTimer()
	b.StartTimer()
	for producer := 0; producer < producers; producer++ {
		producersDone.Add(1)
		go func(first int) {
			defer producersDone.Done()
			done := make(chan error)
			for i := first; i < b.N; i += producers {
				start := time.Now()
				var task UsageRecordTask
				var err error
				if authentication == nil {
					task, err = fixture.task(kind, frozen, done)
				} else {
					var authenticated *APIKey
					authenticated, err = authentication.GetByKey(context.Background(), key.Key)
					if err == nil {
						task, err = fixture.taskWithSubjects(kind, frozen, done, authenticated, authenticated.User)
					}
				}
				if err != nil {
					failures.Add(1)
					continue
				}
				if mode := pool.Submit(task); mode != UsageRecordSubmitModeEnqueued {
					// At most 16 requests are in flight with a 128-task queue.
					// Reject unexpected execution modes rather than skew latency.
					failures.Add(1)
					continue
				}
				if err := <-done; err != nil {
					failures.Add(1)
				}
				durations[i] = time.Since(start).Nanoseconds()
				completed.Add(1)
			}
		}(producer)
	}
	producersDone.Wait()
	b.StopTimer()
	cpuSeconds := usageLifecycleCPUSeconds() - cpuBefore
	pool.Stop()
	if failures.Load() != 0 || completed.Load() != int64(b.N) {
		b.Fatalf("incomplete lifecycle: completed=%d failures=%d expected=%d", completed.Load(), failures.Load(), b.N)
	}
	slices.Sort(durations)
	b.ReportMetric(float64(durations[(len(durations)-1)*95/100]), "p95-e2e-ns")
	b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "requests/s")
	b.ReportMetric(cpuSeconds*1e9/float64(b.N), "runtime-cpu-ns/op")
	b.ReportMetric(producers, "producers")
	b.ReportMetric(workers, "workers")
	runtime.KeepAlive(fixture.checksum.Load())
}

func (f *usageLifecycleFixture) task(kind string, frozen bool, done chan<- error) (UsageRecordTask, error) {
	return f.taskWithSubjects(kind, frozen, done, f.key, f.user)
}

func (f *usageLifecycleFixture) taskWithSubjects(kind string, frozen bool, done chan<- error, key *APIKey, user *User) (UsageRecordTask, error) {
	body := append([]byte(nil), f.body...)
	if !json.Valid(body) {
		return nil, fmt.Errorf("invalid lifecycle fixture JSON")
	}
	fields := gjson.GetManyBytes(body, "model", "usage.input_tokens", "usage.output_tokens")
	model, inputTokens, outputTokens := fields[0].String(), int(fields[1].Int()), int(fields[2].Int())
	parent := context.WithValue(context.WithValue(context.Background(), usageLifecyclePayloadKey{}, body), ctxkey.RequestID, "lifecycle-request")
	headers := http.Header{"X-Upstream-Id": {"upstream-request"}}
	if kind == "systemone_response" {
		result := &SystemOneForwardResult{ForwardResult: ForwardResult{RequestID: "lifecycle-request", Model: model,
			Usage: ClaudeUsage{InputTokens: inputTokens, OutputTokens: outputTokens}, UpstreamHeaders: headers}, Body: body}
		input := &RecordUsageInput{Result: &result.ForwardResult, APIKey: key, User: user, Account: f.account, Subscription: f.sub}
		if frozen {
			record := SnapshotRecordUsageInput(input)
			requestID := strings.Clone(parent.Value(ctxkey.RequestID).(string))
			return func(ctx context.Context) {
				done <- f.settle(context.WithValue(ctx, ctxkey.RequestID, requestID), record.Result.Model,
					record.Result.Usage.InputTokens, record.Result.Usage.OutputTokens, record.APIKey, record.User, record.Account, record.Subscription, record.RequestPayloadHash)
			}, nil
		}
		return func(ctx context.Context) {
			done <- f.settle(context.WithValue(ctx, ctxkey.RequestID, parent.Value(ctxkey.RequestID)), result.Model,
				result.Usage.InputTokens, result.Usage.OutputTokens, input.APIKey, input.User, input.Account, input.Subscription, input.RequestPayloadHash)
			runtime.KeepAlive(result)
			runtime.KeepAlive(parent)
		}, nil
	}
	result := &OpenAIForwardResult{RequestID: "lifecycle-request", Model: model,
		Usage: OpenAIUsage{InputTokens: inputTokens, OutputTokens: outputTokens}, UpstreamHeaders: headers}
	if kind == "ws_replay" {
		result.wsReplayInput = []json.RawMessage{body}
	}
	input := &OpenAIRecordUsageInput{Result: result, APIKey: key, User: user, Account: f.account, Subscription: f.sub}
	if frozen {
		if kind == "grok_request" {
			// The production caller hashes the request before submission.
			input.RequestPayloadHash = HashUsageRequestPayload(body)
		}
		record := SnapshotOpenAIRecordUsageInput(input)
		requestID := strings.Clone(parent.Value(ctxkey.RequestID).(string))
		return func(ctx context.Context) {
			done <- f.settle(context.WithValue(ctx, ctxkey.RequestID, requestID), record.Result.Model,
				record.Result.Usage.InputTokens, record.Result.Usage.OutputTokens, record.APIKey, record.User, record.Account, record.Subscription, record.RequestPayloadHash)
		}, nil
	}
	return func(ctx context.Context) {
		payloadHash := ""
		if kind == "grok_request" {
			// The legacy task built the request fingerprint inside its closure.
			payloadHash = HashUsageRequestPayload(body)
		}
		done <- f.settle(context.WithValue(ctx, ctxkey.RequestID, parent.Value(ctxkey.RequestID)), result.Model,
			result.Usage.InputTokens, result.Usage.OutputTokens, input.APIKey, input.User, input.Account, input.Subscription, payloadHash)
		runtime.KeepAlive(result)
		runtime.KeepAlive(parent)
	}, nil
}

// Real local price lookup and command construction give both modes identical
// worker work. No external repo/cache, artificial delay or synthetic busy loop.
func (f *usageLifecycleFixture) settle(ctx context.Context, model string, inputTokens, outputTokens int, key *APIKey, user *User, account *Account, sub *UserSubscription, hash string) error {
	cost, err := f.billing.CalculateCost(model, UsageTokens{InputTokens: inputTokens, OutputTokens: outputTokens}, key.Group.RateMultiplier)
	if err != nil {
		return err
	}
	log := &UsageLog{UserID: user.ID, APIKeyID: key.ID, AccountID: account.ID, Model: model, InputTokens: inputTokens, OutputTokens: outputTokens}
	cmd := buildUsageBillingCommand(resolveUsageBillingRequestID(ctx, "lifecycle-request"), log, &postUsageBillingParams{
		Cost: cost, User: user, APIKey: key, Account: account, Subscription: sub, RequestPayloadHash: hash, AccountRateMultiplier: account.BillingRateMultiplier(),
	})
	if cmd == nil || cmd.BalanceCost <= 0 {
		return fmt.Errorf("invalid lifecycle billing command")
	}
	f.checksum.Add(math.Float64bits(cmd.BalanceCost) ^ uint64(len(cmd.RequestPayloadHash)))
	return nil
}

func usageLifecycleCPUSeconds() float64 {
	samples := []metrics.Sample{
		{Name: "/cpu/classes/user:cpu-seconds"},
		{Name: "/cpu/classes/gc/total:cpu-seconds"},
		{Name: "/cpu/classes/scavenge/total:cpu-seconds"},
	}
	metrics.Read(samples)
	var seconds float64
	for _, sample := range samples {
		if sample.Value.Kind() == metrics.KindFloat64 {
			seconds += sample.Value.Float64()
		}
	}
	return seconds
}
