package service

import (
	"context"
	"encoding/json"
	"math/rand/v2"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/codexnative"
)

const (
	codexAnalyticsEndpointDefault = "https://chatgpt.com/backend-api/codex/analytics-events/events"
	codexMetricsEndpointDefault   = "https://ab.chatgpt.com/otlp/v1/metrics"
	codexStatsigAPIKeyDefault     = "client-MkRuleRQBd6qakfnDYqJVR9JuXcY57Ljly3vi5JVUIO"
	codexTelemetryQueueSize       = 256
	codexTelemetryTimeout         = 10 * time.Second
	codexTelemetryStateTTL        = 30 * time.Minute
	codexTelemetryMaxStates       = 4096
	codexTelemetryHistorySize     = 500
)

// CodexTelemetryInput is a value snapshot of the final request sent upstream.
// It deliberately contains no request/response body or mutable account pointer.
// Credentials only live in pending work; they are never returned by Observations.
type CodexTelemetryInput struct {
	// Transport-only hints, never emitted as telemetry data or observations.
	nativeHTTPScope         codexnative.Scope
	AccountID               int64
	OwnerAccountID          int64
	OSFamily                string
	CredentialOS            string `json:"-"`
	AuthorizationGeneration string `json:"-"`
	InstallationID          string
	ManagedInstallation     bool
	SamplingID              string
	ProxyID                 *int64
	ReturnedToolCallIDs     []string
	Shell                   string
	AccountName             string
	AccessToken             string `json:"-"`
	ChatGPTAccountID        string
	ProxyURL                string `json:"-"`
	UserAgent               string
	Originator              string
	Version                 string
	SessionID               string
	ThreadID                string
	TurnID                  string
	ParentThreadID          string
	ParentTurnID            string
	RootTurnID              string
	ForkedFromThreadID      string
	ThreadSource            string
	TurnTrigger             string
	AgentName               string
	SubagentKind            string
	OpenAISubagent          string
	Sandbox                 string
	SandboxMode             string
	ApprovalPolicy          string
	ApprovalsReviewer       string
	AutoReviewEnabled       *bool
	GuardianV2Enabled       *bool
	Model                   string
	Effort                  string
	ServiceTier             string
	WebSocket               bool
	StartedAt               time.Time
}

// CodexTelemetryResult contains measurements, not the upstream response body.
type CodexTelemetryResult struct {
	Status                  string
	ResponseID              string
	ServiceTier             string
	HTTPStatus              int
	InputTokens             int64
	CachedInputTokens       int64
	OutputTokens            int64
	ReasoningOutputTokens   int64
	FirstEventAt            time.Time
	FirstTokenAt            time.Time
	FinishedAt              time.Time
	ExplicitClientInterrupt bool
	FirstAgentMessageAt     time.Time
	RequestSentAt           time.Time
	DeliveryStatus          string
	EndTurn                 *bool
	PendingToolCallIDs      []string
	EventCount              int64
	FailedEventCount        int64
	EventMetrics            []CodexTelemetryEventMetric
	SendDurationMS          float64
	SendSucceeded           *bool
	ServerTiming            map[string]float64
}

// CodexTelemetryEventMetric is a bounded aggregate of physical stream events.
// Histogram buckets use codexHistogramBounds and include the final +Inf bucket.
// A missing exact read boundary increments Count without inventing a duration.
type CodexTelemetryEventMetric struct {
	Kind        string
	Success     bool
	Count       uint64
	WaitCount   uint64
	WaitSumMS   float64
	WaitMinMS   float64
	WaitMaxMS   float64
	WaitBuckets []uint64
}

type codexTelemetryClient struct {
	nativeHTTPScope                                                        codexnative.Scope
	localID                                                                int64
	name, accessToken, accountID, proxyURL, userAgent, originator, version string
}

type codexTelemetryProfile struct {
	client                                                              codexTelemetryClient
	sessionID, threadID, turnID, rootTurnID, model, effort, serviceTier string
	started                                                             time.Time
	firstThread, websocket, dynamicTool, command, fileChange            bool
	input                                                               CodexTelemetryInput
	attemptCount                                                        int
	simulationEnabled, observationEnabled                               bool
	poolID, scenarioSeed, source                                        string
	samplingCount, clientRetryCount                                     int
	ended                                                               time.Time
	reasons                                                             []string
	fieldSources                                                        map[string]string
}

type codexTelemetryTerminal struct {
	status                           string
	body                             []byte // Only the normalized usage fields below, never response content.
	firstEvent, firstToken, finished time.Time
	explicitClientInterrupt          bool
	httpStatus                       int
	responseID                       string
	result                           CodexTelemetryResult
}

type codexTelemetryTurn struct {
	profile  codexTelemetryProfile
	latest   uint64
	lastSeen time.Time
	finished bool
	pending  *CodexTelemetryResult
}

type codexTelemetryJob struct {
	profile   codexTelemetryProfile
	body      []byte
	metrics   bool
	epoch     uint64
	entry     *CodexTelemetryObservation
	persisted *CodexTelemetryBatch
}

// CodexTelemetrySender allows routing through the application's auxiliary
// transport without coupling telemetry to account mutation or inference limits.
type CodexTelemetrySender func(context.Context, *http.Request, CodexTelemetryInput, bool) (*http.Response, error)

// CodexTelemetryService owns bounded queues and process-local diagnostic state.
// Account hashing selects a serial worker so an account's batches stay ordered.
type CodexTelemetryService struct {
	mu                   sync.Mutex
	upstream             HTTPUpstream
	sender               CodexTelemetrySender
	configured           bool
	simulationEnabled    bool
	observationEnabled   bool
	stopped              bool
	epoch                uint64
	epochCtx             context.Context
	epochCancel          context.CancelFunc
	stop                 chan struct{}
	wg                   sync.WaitGroup
	queues               [4][]codexTelemetryJob
	wake                 [4]chan struct{}
	queueDepth           int
	threads              map[string]time.Time
	turns                map[string]*codexTelemetryTurn
	metrics              *codexTelemetryMetricStore
	observations         []*CodexTelemetryObservation
	counters             CodexTelemetryCounters
	nextID               uint64
	nextAttempt          uint64
	randIntN             func(int) int
	analyticsURL         string
	metricsURL           string
	store                CodexTelemetryStore
	accountRepo          AccountRepository
	proxyRepo            ProxyRepository
	transportInputs      map[CodexTelemetryPoolKey]CodexTelemetryInput
	sharedEpoch          int64
	mutations            chan codexTelemetryMutation
	mutationReservations int
	runtimeAttempts      map[uint64]*CodexTelemetryAttempt
	notifier             CodexTelemetryNotifier
	notifierCancel       context.CancelFunc
	pollMu               sync.Mutex
	lastPolicyRefresh    time.Time
	runtimeWake          chan struct{}
}

func NewCodexTelemetryService(upstream HTTPUpstream) *CodexTelemetryService {
	ctx, cancel := context.WithCancel(context.Background())
	s := &CodexTelemetryService{
		upstream: upstream, configured: true, simulationEnabled: true, observationEnabled: true, epoch: 1, sharedEpoch: 1, epochCtx: ctx, epochCancel: cancel,
		stop: make(chan struct{}), threads: make(map[string]time.Time), turns: make(map[string]*codexTelemetryTurn),
		metrics: newCodexTelemetryMetricStore(), randIntN: rand.IntN,
		analyticsURL: codexAnalyticsEndpointDefault, metricsURL: codexMetricsEndpointDefault,
		store: NewMemoryCodexTelemetryStore(), transportInputs: make(map[CodexTelemetryPoolKey]CodexTelemetryInput),
		mutations: make(chan codexTelemetryMutation, codexTelemetryQueueSize), runtimeAttempts: make(map[uint64]*CodexTelemetryAttempt),
		runtimeWake: make(chan struct{}, 1),
	}
	s.wg.Add(1)
	go s.mutationLoop()
	for i := range s.wake {
		s.wake[i] = make(chan struct{}, 1)
		s.wg.Add(1)
		go s.worker(i)
	}
	s.wg.Add(1)
	go s.flushLoop()
	return s
}

// SetSender must be configured before the service is exposed to requests.
func (s *CodexTelemetryService) SetSender(sender CodexTelemetrySender) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.sender = sender
	s.mu.Unlock()
}

func CodexTelemetryEffectiveState(configured bool) (bool, string) {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CODEX_TELEMETRY_ENABLED"))) {
	case "0", "false", "no", "off":
		return false, "CODEX_TELEMETRY_ENABLED"
	}
	return configured, ""
}

func (s *CodexTelemetryService) enabledLocked() bool {
	enabled, _ := CodexTelemetryEffectiveState(s.configured)
	return enabled && (s.simulationEnabled || s.observationEnabled) && !s.stopped
}

// Enabled avoids copying/parsing response bodies when collection is disabled.
func (s *CodexTelemetryService) Enabled() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.enabledLocked()
}

func (s *CodexTelemetryService) SetEnabled(enabled bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	simulation, observation := s.simulationEnabled, s.observationEnabled
	s.mu.Unlock()
	s.SetPolicy(enabled, simulation, observation)
}

// resetLocked invalidates callbacks from all prior requests and actively cancels
// sends. Re-enabling starts a new generation and cannot replay old work.
func (s *CodexTelemetryService) resetLocked() {
	s.epochCancel()
	s.epoch++
	s.epochCtx, s.epochCancel = context.WithCancel(context.Background())
	for i := range s.queues {
		for _, job := range s.queues[i] {
			s.finishObservationLocked(job.entry, "cancelled", 0, "telemetry_disabled")
		}
		s.queues[i] = nil
	}
	s.queueDepth = 0
	s.threads = make(map[string]time.Time)
	s.turns = make(map[string]*codexTelemetryTurn)
	s.metrics.clear()
	for _, attempt := range s.runtimeAttempts {
		attempt.invalid = true
		attempt.reservations = 0
	}
	s.runtimeAttempts = make(map[uint64]*CodexTelemetryAttempt)
	s.mutationReservations = 0
	for {
		select {
		case <-s.mutations:
		default:
			return
		}
	}
}

func (s *CodexTelemetryService) Stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		s.wg.Wait()
		return
	}
	s.stopped = true
	s.stopNotifierLocked()
	s.resetLocked()
	s.epochCancel()
	close(s.stop)
	s.mu.Unlock()
	s.wg.Wait()
}

type CodexTelemetryAttempt struct {
	service            *CodexTelemetryService
	turn               *codexTelemetryTurn
	epoch              uint64
	id                 uint64
	profile            codexTelemetryProfile
	recorded           bool // protected by the service mutex
	once               sync.Once
	contextMu          sync.Mutex
	contextDone        <-chan struct{}
	contextCancelledAt time.Time
	stopContext        func() bool
	attemptID          string
	poolKey            CodexTelemetryPoolKey
	policyEpoch        int64
	invalid            bool // protected by service.mu
	reservations       int  // protected by service.mu
}

func (s *CodexTelemetryService) Begin(ctx context.Context, input CodexTelemetryInput) *CodexTelemetryAttempt {
	if s == nil {
		return nil
	}
	if input.AutoReviewEnabled != nil {
		v := *input.AutoReviewEnabled
		input.AutoReviewEnabled = &v
	}
	if input.GuardianV2Enabled != nil {
		v := *input.GuardianV2Enabled
		input.GuardianV2Enabled = &v
	}
	if input.ProxyID != nil {
		v := *input.ProxyID
		input.ProxyID = &v
	}
	input.ReturnedToolCallIDs = append([]string(nil), input.ReturnedToolCallIDs...)
	if input.StartedAt.IsZero() {
		input.StartedAt = time.Now()
	}
	if input.nativeHTTPScope.Purpose == "" {
		input.nativeHTTPScope, _ = codexnative.ScopeFromContext(ctx)
		input.nativeHTTPScope.AccountID = input.AccountID
		input.nativeHTTPScope.SourceUserAgent = input.UserAgent
		input.nativeHTTPScope.Purpose = "telemetry"
	}
	if input.nativeHTTPScope.CanonicalUserAgent == "" {
		input.nativeHTTPScope.CanonicalUserAgent = CodexCanonicalUserAgent()
	}
	profile := codexTelemetryProfile{
		client: codexTelemetryClient{nativeHTTPScope: input.nativeHTTPScope, localID: input.AccountID, name: input.AccountName, accessToken: input.AccessToken,
			accountID: input.ChatGPTAccountID, proxyURL: input.ProxyURL, userAgent: input.UserAgent, originator: input.Originator, version: input.Version},
		sessionID: input.SessionID, threadID: input.ThreadID, turnID: input.TurnID, rootTurnID: input.RootTurnID,
		model: input.Model, effort: input.Effort, serviceTier: input.ServiceTier, started: input.StartedAt,
		websocket: input.WebSocket, input: input, attemptCount: 1,
	}
	s.mu.Lock()
	if !s.enabledLocked() {
		s.mu.Unlock()
		return nil
	}
	s.nextAttempt++
	id := s.nextAttempt
	s.counters.Attempts++
	if input.AccessToken == "" || input.ChatGPTAccountID == "" {
		s.skipLocked(profile, id, "missing_oauth_credentials")
		s.mu.Unlock()
		return nil
	}
	return s.beginPersistentLocked(ctx, profile, id)
}

func (a *CodexTelemetryAttempt) Finish(result CodexTelemetryResult) {
	if a == nil {
		return
	}
	a.once.Do(func() {
		a.contextMu.Lock()
		stop := a.stopContext
		a.contextMu.Unlock()
		if stop != nil {
			stop()
		}
		a.service.finishAttempt(a, result)
	})
}

// Retry records one physical transport attempt. It neither commits a logical
// client turn nor invents a client retry from the gateway's routing decision.
func (a *CodexTelemetryAttempt) Retry(result CodexTelemetryResult) {
	if a == nil {
		return
	}
	a.once.Do(func() { a.service.submitPersistentResult(a, result, true) })
}

func (a *CodexTelemetryAttempt) finishOnContextEnd() {
	// Adapters own terminal classification. Context cancellation alone cannot
	// distinguish disconnects, retries, and a client interrupt.
}

func (s *CodexTelemetryService) finishAttempt(a *CodexTelemetryAttempt, result CodexTelemetryResult) {
	s.submitPersistentResult(a, result, false)
}

func (s *CodexTelemetryService) recordAttemptLocked(a *CodexTelemetryAttempt, result CodexTelemetryResult) {
	if a.recorded {
		return
	}
	a.recorded = true
	profile := a.profile
	if result.ServiceTier != "" {
		profile.serviceTier, profile.input.ServiceTier = result.ServiceTier, result.ServiceTier
	}
	s.metrics.recordAttempt(profile, codexTelemetryTerminalFromResult(result))
}

func codexTelemetryTerminalFromResult(result CodexTelemetryResult) codexTelemetryTerminal {
	if result.FinishedAt.IsZero() {
		result.FinishedAt = time.Now()
	}
	measurement := result
	switch result.Status {
	case "completed", "failed", "interrupted", "cancelled":
	default:
		result.Status = "failed"
	}
	usage, _ := json.Marshal(map[string]any{"id": result.ResponseID, "service_tier": result.ServiceTier, "usage": map[string]any{
		"input_tokens": result.InputTokens, "output_tokens": result.OutputTokens,
		"input_tokens_details":  map[string]int64{"cached_tokens": result.CachedInputTokens},
		"output_tokens_details": map[string]int64{"reasoning_tokens": result.ReasoningOutputTokens},
	}})
	return codexTelemetryTerminal{status: result.Status, body: usage, firstEvent: result.FirstEventAt,
		firstToken: result.FirstTokenAt, finished: result.FinishedAt, explicitClientInterrupt: result.ExplicitClientInterrupt,
		httpStatus: result.HTTPStatus, responseID: result.ResponseID, result: measurement}
}

func (s *CodexTelemetryService) expireLocked(now time.Time) {
	for key, seen := range s.threads {
		if now.Sub(seen) > codexTelemetryStateTTL {
			delete(s.threads, key)
		}
	}
	for key, turn := range s.turns {
		if now.Sub(turn.lastSeen) > codexTelemetryStateTTL {
			delete(s.turns, key)
		}
	}
	// State caps bound memory even when many distinct clients arrive within the TTL.
	for len(s.threads) >= codexTelemetryMaxStates {
		for key := range s.threads {
			delete(s.threads, key)
			break
		}
	}
	for len(s.turns) >= codexTelemetryMaxStates {
		for key := range s.turns {
			delete(s.turns, key)
			break
		}
	}
}

func (s *CodexTelemetryService) flushLoop() {
	defer s.wg.Done()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case now := <-ticker.C:
			s.pollPersistentRuntime(now)
		case <-s.runtimeWake:
			s.pollPersistentRuntime(time.Now())
		case <-s.stop:
			return
		}
	}
}

func (s *CodexTelemetryService) flushMetrics(now time.Time) {
	s.pollPersistentRuntime(now)
}
