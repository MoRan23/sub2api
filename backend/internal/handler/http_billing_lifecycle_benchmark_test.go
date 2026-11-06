package handler

// This file is deliberately self-contained and uses APIs present in 8972274c2.
// Copy the identical fixture into the preserved baseline tree; production code
// selects legacy versus frozen ownership, never a benchmark-only switch.
import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"runtime/metrics"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

const (
	httpBillingClients       = 16
	httpBillingCreated       = "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_local\",\"status\":\"in_progress\"}}\n\n"
	httpBillingDelta         = "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"local synthetic answer chunk\"}\n\n"
	httpBillingCompleted     = "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_local\",\"model\":\"gpt-4o\",\"status\":\"completed\",\"usage\":{\"input_tokens\":1000,\"output_tokens\":100,\"total_tokens\":1100}}}\n\n"
	httpBillingResponseBytes = len(httpBillingCreated) + 16*len(httpBillingDelta) + len(httpBillingCompleted)
)

// Each operation is a closed-loop HTTP request plus its ordinary persistence
// settlement. HTTP EOF and settlement timestamps are recorded independently.
// Real loopback HTTP, auth, scheduling, SSE forwarding, pricing and worker code
// run here. Only repositories/cache storage are in-memory test implementations.
func BenchmarkHTTPBillingLifecycle(b *testing.B) {
	gin.SetMode(gin.ReleaseMode)
	logger.InitBootstrap()
	_ = logger.SetLevel("error")
	// APIKeyService has no public L1 cache Close API. Reuse one service across
	// payloads and calibration runs so their background caches do not accumulate.
	keys, apiKeys := newHTTPBillingAPIKeyService()
	for _, size := range []int{8 << 10, 1 << 20} {
		b.Run(fmt.Sprintf("request_bytes=%d", size), func(b *testing.B) {
			benchmarkHTTPBillingLifecycle(b, size, keys, apiKeys)
		})
	}
}

type httpBillingCompletion struct {
	done    chan struct{}
	at      time.Time
	err     error
	applied bool
}

type httpBillingStore struct {
	service.UsageBillingRepository
	mu            sync.Mutex
	pending       map[string]*httpBillingCompletion
	balance       float64
	applies, logs int64
}

func (s *httpBillingStore) completion(id string) *httpBillingCompletion {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c := s.pending[id]; c != nil {
		return c
	}
	c := &httpBillingCompletion{done: make(chan struct{})}
	s.pending[id] = c
	return c
}

func (s *httpBillingStore) Apply(ctx context.Context, cmd *service.UsageBillingCommand) (*service.UsageBillingApplyResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c := s.completion(cmd.RequestID)
	s.mu.Lock()
	defer s.mu.Unlock()
	if c.applied {
		return nil, fmt.Errorf("duplicate billing for %s", cmd.RequestID)
	}
	if cmd.BalanceCost <= 0 || cmd.InputTokens != 1000 || cmd.OutputTokens != 100 {
		return nil, fmt.Errorf("unexpected billing command: cost=%g tokens=%d/%d", cmd.BalanceCost, cmd.InputTokens, cmd.OutputTokens)
	}
	c.applied = true
	s.balance -= cmd.BalanceCost
	s.applies++
	value := s.balance
	return &service.UsageBillingApplyResult{Applied: true, NewBalance: &value}, nil
}

type httpBillingUsageLogs struct {
	service.UsageLogRepository
	store *httpBillingStore
}

func (r *httpBillingUsageLogs) Create(ctx context.Context, log *service.UsageLog) (bool, error) {
	c := r.store.completion(log.RequestID)
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	if !c.at.IsZero() {
		return false, fmt.Errorf("duplicate log %s", log.RequestID)
	}
	if err := ctx.Err(); err != nil {
		c.err = err
	}
	if !c.applied || log.ActualCost <= 0 || log.InputTokens != 1000 || log.OutputTokens != 100 {
		c.err = fmt.Errorf("usage did not settle: applied=%v cost=%g tokens=%d/%d", c.applied, log.ActualCost, log.InputTokens, log.OutputTokens)
	}
	c.at = time.Now()
	r.store.logs++
	close(c.done)
	return true, nil
}

type httpBillingBalanceCache struct {
	service.BillingCache
	mu      sync.Mutex
	balance float64
}

func (c *httpBillingBalanceCache) GetUserBalance(context.Context, int64) (float64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.balance, nil
}
func (c *httpBillingBalanceCache) DeductUserBalance(_ context.Context, _ int64, amount float64) error {
	c.mu.Lock()
	c.balance -= amount
	c.mu.Unlock()
	return nil
}

type httpBillingAPIKeys struct {
	service.APIKeyRepository
	key   service.APIKey
	reads atomic.Int64
}

func (r *httpBillingAPIKeys) GetByKeyForAuth(_ context.Context, key string) (*service.APIKey, error) {
	if key != r.key.Key {
		return nil, service.ErrAPIKeyNotFound
	}
	r.reads.Add(1)
	keyCopy, userCopy, groupCopy := r.key, *r.key.User, *r.key.Group
	keyCopy.User, keyCopy.Group = &userCopy, &groupCopy
	return &keyCopy, nil
}
func (r *httpBillingAPIKeys) GetByKey(ctx context.Context, key string) (*service.APIKey, error) {
	return r.GetByKeyForAuth(ctx, key)
}
func (r *httpBillingAPIKeys) UpdateLastUsed(context.Context, int64, time.Time) error { return nil }

type httpBillingAccounts struct {
	service.AccountRepository
	account service.Account
}

func (r *httpBillingAccounts) GetByID(_ context.Context, id int64) (*service.Account, error) {
	if id != r.account.ID {
		return nil, service.ErrAccountNotFound
	}
	a := r.account
	return &a, nil
}
func (r *httpBillingAccounts) ListSchedulableByGroupIDAndPlatform(context.Context, int64, string) ([]service.Account, error) {
	return []service.Account{r.account}, nil
}
func (r *httpBillingAccounts) ListSchedulableByPlatform(context.Context, string) ([]service.Account, error) {
	return []service.Account{r.account}, nil
}
func (r *httpBillingAccounts) ListSchedulableUngroupedByPlatform(context.Context, string) ([]service.Account, error) {
	return []service.Account{r.account}, nil
}

type httpBillingSlots struct {
	service.ConcurrencyCache
	mu     sync.Mutex
	active map[string]struct{}
}

func (s *httpBillingSlots) AcquireAccountSlot(_ context.Context, _ int64, limit int, id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.active) >= limit {
		return false, nil
	}
	s.active[id] = struct{}{}
	return true, nil
}
func (s *httpBillingSlots) ReleaseAccountSlot(_ context.Context, _ int64, id string) error {
	s.mu.Lock()
	delete(s.active, id)
	s.mu.Unlock()
	return nil
}
func (s *httpBillingSlots) GetAccountsLoadBatch(_ context.Context, accounts []service.AccountWithConcurrency) (map[int64]*service.AccountLoadInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[int64]*service.AccountLoadInfo, len(accounts))
	for _, account := range accounts {
		out[account.ID] = &service.AccountLoadInfo{AccountID: account.ID, CurrentConcurrency: len(s.active), LoadRate: len(s.active) * 100 / account.MaxConcurrency}
	}
	return out, nil
}

type httpBillingEnvironment struct {
	server, upstream *httptest.Server
	client           *http.Client
	pool             *service.UsageRecordWorkerPool
	billingCache     *service.BillingCacheService
	pricing          *service.PricingService
	store            *httpBillingStore
	keys             *httpBillingAPIKeys
	body             []byte
	upstreamCalls    atomic.Int64
}

func newHTTPBillingAPIKeyService() (*httpBillingAPIKeys, *service.APIKeyService) {
	cfg := &config.Config{RunMode: config.RunModeStandard}
	cfg.Default.RateMultiplier = 1
	cfg.APIKeyAuth.L1Size, cfg.APIKeyAuth.L1TTLSeconds = 1024, 3600
	groupID := int64(71)
	group := &service.Group{ID: groupID, Platform: service.PlatformOpenAI, Status: service.StatusActive, Hydrated: true, RateMultiplier: 1, ImageRateMultiplier: 1, VideoRateMultiplier: 1}
	keys := &httpBillingAPIKeys{key: service.APIKey{ID: 72, UserID: 73, Key: "sk-local-http-benchmark", Status: service.StatusActive, GroupID: &groupID, Group: group, User: &service.User{ID: 73, Status: service.StatusActive, Balance: 1e12}}}
	return keys, service.NewAPIKeyService(keys, nil, nil, nil, nil, nil, cfg)
}

func newHTTPBillingEnvironment(b *testing.B, size int, keys *httpBillingAPIKeys, apiKeys *service.APIKeyService) *httpBillingEnvironment {
	b.Helper()
	e := &httpBillingEnvironment{store: &httpBillingStore{pending: make(map[string]*httpBillingCompletion), balance: 1e12}, keys: keys}
	e.upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" || r.Header.Get("Authorization") != "Bearer benchmark-upstream" {
			http.Error(w, "unexpected upstream request", http.StatusBadRequest)
			return
		}
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			return
		}
		e.upstreamCalls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, httpBillingCreated)
		_ = http.NewResponseController(w).Flush()
		// A finite 16-delta answer exercises normal streaming work without sleeps.
		for i := 0; i < 16; i++ {
			_, _ = io.WriteString(w, httpBillingDelta)
			_ = http.NewResponseController(w).Flush()
		}
		_, _ = io.WriteString(w, httpBillingCompleted)
	}))
	cfg := &config.Config{RunMode: config.RunModeStandard}
	cfg.Default.RateMultiplier = 1
	cfg.Security.URLAllowlist.AllowPrivateHosts = true
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.APIKeyAuth.L1Size, cfg.APIKeyAuth.L1TTLSeconds = 1024, 3600
	cfg.Gateway.MaxAccountSwitches = 1
	cfg.Pricing.DataDir = b.TempDir()
	cfg.Pricing.UpdateIntervalHours = 100000
	if err := os.WriteFile(filepath.Join(cfg.Pricing.DataDir, "model_pricing.json"), []byte(`{"gpt-4o":{"mode":"chat","litellm_provider":"openai","input_cost_per_token":0.0000025,"output_cost_per_token":0.00001}}`), 0600); err != nil {
		b.Fatal(err)
	}
	e.pricing = service.NewPricingService(cfg, nil)
	if err := e.pricing.Initialize(); err != nil {
		b.Fatal(err)
	}
	groupID := int64(71)
	accounts := &httpBillingAccounts{account: service.Account{ID: 74, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Concurrency: 64, GroupIDs: []int64{groupID}, AccountGroups: []service.AccountGroup{{AccountID: 74, GroupID: groupID}}, Credentials: map[string]any{"api_key": "benchmark-upstream", "base_url": e.upstream.URL}, Extra: map[string]any{"openai_passthrough": true}}}
	concurrency := service.NewConcurrencyService(&httpBillingSlots{active: make(map[string]struct{})})
	e.billingCache = service.NewBillingCacheService(&httpBillingBalanceCache{balance: 1e12}, nil, nil, nil, nil, nil, cfg, nil)
	billing := service.NewBillingService(cfg, e.pricing)
	upstream := repository.NewHTTPUpstream(cfg)
	gateway := service.NewOpenAIGatewayService(accounts, &httpBillingUsageLogs{store: e.store}, e.store, nil, nil, nil, nil, cfg, nil, concurrency, billing, nil, e.billingCache, upstream, &service.DeferredService{}, nil, nil, service.NewModelPricingResolver(nil, billing), nil, nil, nil, nil)
	e.pool = service.NewUsageRecordWorkerPoolWithOptions(service.UsageRecordWorkerPoolOptions{WorkerCount: 4, QueueSize: 128, TaskTimeout: time.Minute, OverflowPolicy: config.UsageRecordOverflowPolicyDrop})
	h := NewOpenAIGatewayHandler(gateway, concurrency, e.billingCache, apiKeys, e.pool, nil, nil, nil, cfg)
	router := gin.New()
	router.Use(middleware.ClientRequestID(), middleware.RequestBodyLimit(2<<20), gin.HandlerFunc(middleware.NewAPIKeyAuthMiddleware(apiKeys, nil, cfg)))
	router.POST("/v1/responses", h.Responses)
	e.server = httptest.NewServer(router)
	e.client = &http.Client{Transport: &http.Transport{MaxIdleConns: 64, MaxIdleConnsPerHost: 32, MaxConnsPerHost: 32}, Timeout: 30 * time.Second}
	prefix, suffix := `{"model":"gpt-4o","stream":true,"input":"`, `","max_output_tokens":128}`
	e.body = []byte(prefix + strings.Repeat("x", size-len(prefix)-len(suffix)) + suffix)
	if !json.Valid(e.body) {
		b.Fatal("invalid fixture")
	}
	b.Cleanup(func() {
		e.server.Close()
		e.pool.Stop()
		e.billingCache.Stop()
		e.pricing.Stop()
		e.client.CloseIdleConnections()
		e.upstream.CloseClientConnections()
		e.upstream.Close()
	})
	return e
}

func (e *httpBillingEnvironment) request() (time.Time, time.Time, error) {
	req, err := http.NewRequest(http.MethodPost, e.server.URL+"/v1/responses", bytes.NewReader(e.body))
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer sk-local-http-benchmark")
	resp, err := e.client.Do(req)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		resp.Body.Close()
		return time.Time{}, time.Time{}, fmt.Errorf("HTTP %d: %s", resp.StatusCode, data)
	}
	responseBytes, err := io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	httpAt := time.Now()
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	if responseBytes != int64(httpBillingResponseBytes) || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		return time.Time{}, time.Time{}, fmt.Errorf("incomplete SSE response: bytes=%d want=%d content-type=%q", responseBytes, httpBillingResponseBytes, resp.Header.Get("Content-Type"))
	}
	id := resp.Header.Get("X-Client-Request-ID")
	if id == "" {
		return time.Time{}, time.Time{}, fmt.Errorf("missing client request ID")
	}
	id = "client:" + id
	c := e.store.completion(id)
	select {
	case <-c.done:
	case <-time.After(10 * time.Second):
		return time.Time{}, time.Time{}, fmt.Errorf("billing timeout for %s", id)
	}
	e.store.mu.Lock()
	delete(e.store.pending, id)
	e.store.mu.Unlock()
	return httpAt, c.at, c.err
}

func benchmarkHTTPBillingLifecycle(b *testing.B, size int, keys *httpBillingAPIKeys, apiKeys *service.APIKeyService) {
	b.StopTimer()
	e := newHTTPBillingEnvironment(b, size, keys, apiKeys)
	// Warm the actual auth cache, HTTP transports and pricing outside measurement.
	for i := 0; i < 32; i++ {
		if _, _, err := e.request(); err != nil {
			b.Fatal(err)
		}
		runtime.Gosched()
	}
	beforeReads := e.keys.reads.Load()
	beforeCalls := e.upstreamCalls.Load()
	e.store.mu.Lock()
	beforeApplies, beforeLogs := e.store.applies, e.store.logs
	e.store.mu.Unlock()
	httpDurations, settlementDurations := make([]int64, b.N), make([]int64, b.N)
	var lastHTTP, lastSettlement atomic.Int64
	var failures atomic.Int64
	var firstError atomic.Pointer[string]
	var wg sync.WaitGroup
	runtime.GC()
	cpuBefore := httpBillingCPUSeconds()
	b.ReportAllocs()
	b.SetBytes(int64(size))
	b.ResetTimer()
	b.StartTimer()
	started := time.Now()
	for p := 0; p < httpBillingClients; p++ {
		wg.Add(1)
		go func(first int) {
			defer wg.Done()
			for i := first; i < b.N; i += httpBillingClients {
				start := time.Now()
				httpAt, settledAt, err := e.request()
				if err != nil {
					failures.Add(1)
					message := err.Error()
					firstError.CompareAndSwap(nil, &message)
					continue
				}
				httpDurations[i] = httpAt.Sub(start).Nanoseconds()
				settlementDurations[i] = settledAt.Sub(start).Nanoseconds()
				httpBillingStoreMax(&lastHTTP, httpAt.Sub(started).Nanoseconds())
				httpBillingStoreMax(&lastSettlement, settledAt.Sub(started).Nanoseconds())
			}
		}(p)
	}
	wg.Wait()
	b.StopTimer()
	cpuSeconds := httpBillingCPUSeconds() - cpuBefore
	e.pool.Stop()
	e.billingCache.Stop()
	if failures.Load() != 0 {
		b.Fatalf("request failures=%d first=%s", failures.Load(), *firstError.Load())
	}
	e.store.mu.Lock()
	applies, logs, pending := e.store.applies-beforeApplies, e.store.logs-beforeLogs, len(e.store.pending)
	e.store.mu.Unlock()
	if applies != int64(b.N) || logs != int64(b.N) || pending != 0 || e.upstreamCalls.Load()-beforeCalls != int64(b.N) {
		b.Fatalf("unbalanced lifecycle: applies=%d logs=%d pending=%d upstream=%d expected=%d", applies, logs, pending, e.upstreamCalls.Load()-beforeCalls, b.N)
	}
	stats := e.pool.Stats()
	if stats.DroppedTasks != 0 || stats.FailedTasks != 0 || stats.SyncFallbackTasks != 0 {
		b.Fatalf("unexpected worker mode: %+v", stats)
	}
	slices.Sort(httpDurations)
	slices.Sort(settlementDurations)
	p95 := int(math.Ceil(float64(b.N)*.95)) - 1
	b.ReportMetric(float64(httpDurations[p95]), "p95-http-ns")
	b.ReportMetric(float64(settlementDurations[p95]), "p95-settled-ns")
	b.ReportMetric(float64(b.N)*1e9/float64(lastHTTP.Load()), "http-requests/s")
	b.ReportMetric(float64(b.N)*1e9/float64(lastSettlement.Load()), "settled-requests/s")
	b.ReportMetric(cpuSeconds*1e9/float64(b.N), "runtime-cpu-ns/request")
	b.ReportMetric(float64(e.keys.reads.Load()-beforeReads), "auth-storage-reads")
	b.ReportMetric(httpBillingClients, "clients")
	b.ReportMetric(4, "workers")
}

func httpBillingStoreMax(value *atomic.Int64, next int64) {
	for {
		old := value.Load()
		if next <= old || value.CompareAndSwap(old, next) {
			return
		}
	}
}
func httpBillingCPUSeconds() float64 {
	samples := []metrics.Sample{{Name: "/cpu/classes/user:cpu-seconds"}, {Name: "/cpu/classes/gc/total:cpu-seconds"}, {Name: "/cpu/classes/scavenge/total:cpu-seconds"}}
	metrics.Read(samples)
	var total float64
	for _, sample := range samples {
		if sample.Value.Kind() == metrics.KindFloat64 {
			total += sample.Value.Float64()
		}
	}
	return total
}
