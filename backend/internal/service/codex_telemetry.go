package service

import (
	"context"
	"encoding/json"
	"math/rand/v2"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	codexAnalyticsEndpointDefault = "https://chatgpt.com/backend-api/codex/analytics-events/events"
	codexMetricsEndpointDefault   = "https://ab.chatgpt.com/otlp/v1/metrics"
	codexStatsigAPIKeyDefault     = "client-MkRuleRQBd6qakfnDYqJVR9JuXcY57Ljly3vi5JVUIO"
	codexTelemetryQueueSize       = 256
	codexTelemetryTimeout         = 10 * time.Second
	codexTelemetryStateTTL        = 5 * time.Minute
	codexTelemetryMaxStates       = 4096
	codexTelemetryHistorySize     = 500
)

// CodexTelemetryInput is a value snapshot of the final request sent upstream.
// It deliberately contains no request/response body or mutable account pointer.
// Credentials only live in pending work; they are never returned by Observations.
type CodexTelemetryInput struct {
	AccountID          int64
	AccountName        string
	AccessToken        string `json:"-"`
	ChatGPTAccountID   string
	ProxyURL           string `json:"-"`
	UserAgent          string
	Originator         string
	Version            string
	SessionID          string
	ThreadID           string
	TurnID             string
	ParentThreadID     string
	ParentTurnID       string
	RootTurnID         string
	ForkedFromThreadID string
	ThreadSource       string
	TurnTrigger        string
	AgentName          string
	SubagentKind       string
	OpenAISubagent     string
	Sandbox            string
	SandboxMode        string
	ApprovalPolicy     string
	ApprovalsReviewer  string
	AutoReviewEnabled  *bool
	GuardianV2Enabled  *bool
	Model              string
	Effort             string
	ServiceTier        string
	WebSocket          bool
	StartedAt          time.Time
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
}

type codexTelemetryClient struct {
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
}

type codexTelemetryTerminal struct {
	status                           string
	body                             []byte // Only the normalized usage fields below, never response content.
	firstEvent, firstToken, finished time.Time
	explicitClientInterrupt          bool
	httpStatus                       int
	responseID                       string
}

type codexTelemetryTurn struct {
	profile  codexTelemetryProfile
	latest   uint64
	lastSeen time.Time
	finished bool
	pending  *CodexTelemetryResult
}

type codexTelemetryJob struct {
	profile codexTelemetryProfile
	body    []byte
	metrics bool
	epoch   uint64
	entry   *CodexTelemetryObservation
}

// CodexTelemetrySender allows routing through the application's auxiliary
// transport without coupling telemetry to account mutation or inference limits.
type CodexTelemetrySender func(context.Context, *http.Request, CodexTelemetryInput, bool) (*http.Response, error)

// CodexTelemetryService owns bounded queues and process-local diagnostic state.
// Account hashing selects a serial worker so an account's batches stay ordered.
type CodexTelemetryService struct {
	mu           sync.Mutex
	upstream     HTTPUpstream
	sender       CodexTelemetrySender
	configured   bool
	stopped      bool
	epoch        uint64
	epochCtx     context.Context
	epochCancel  context.CancelFunc
	stop         chan struct{}
	wg           sync.WaitGroup
	queues       [4][]codexTelemetryJob
	wake         [4]chan struct{}
	queueDepth   int
	threads      map[string]time.Time
	turns        map[string]*codexTelemetryTurn
	metrics      *codexTelemetryMetricStore
	observations []*CodexTelemetryObservation
	counters     CodexTelemetryCounters
	nextID       uint64
	nextAttempt  uint64
	randIntN     func(int) int
	analyticsURL string
	metricsURL   string
}

func NewCodexTelemetryService(upstream HTTPUpstream) *CodexTelemetryService {
	ctx, cancel := context.WithCancel(context.Background())
	s := &CodexTelemetryService{
		upstream: upstream, configured: true, epoch: 1, epochCtx: ctx, epochCancel: cancel,
		stop: make(chan struct{}), threads: make(map[string]time.Time), turns: make(map[string]*codexTelemetryTurn),
		metrics: newCodexTelemetryMetricStore(), randIntN: rand.IntN,
		analyticsURL: codexAnalyticsEndpointDefault, metricsURL: codexMetricsEndpointDefault,
	}
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
	return enabled && !s.stopped
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
	defer s.mu.Unlock()
	if s.stopped {
		return
	}
	s.configured = enabled
	if !s.enabledLocked() {
		s.resetLocked()
	}
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
	s.resetLocked()
	s.epochCancel()
	close(s.stop)
	s.mu.Unlock()
	s.wg.Wait()
}

type CodexTelemetryAttempt struct {
	service     *CodexTelemetryService
	turn        *codexTelemetryTurn
	epoch       uint64
	id          uint64
	profile     codexTelemetryProfile
	recorded    bool // protected by the service mutex
	once        sync.Once
	contextMu   sync.Mutex
	contextDone <-chan struct{}
	stopContext func() bool
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
	if input.StartedAt.IsZero() {
		input.StartedAt = time.Now()
	}
	profile := codexTelemetryProfile{
		client: codexTelemetryClient{localID: input.AccountID, name: input.AccountName, accessToken: input.AccessToken,
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
	s.expireLocked(time.Now())
	key := strconv.FormatInt(input.AccountID, 10) + ":" + input.ThreadID + ":" + input.TurnID
	if input.ThreadID == "" || input.TurnID == "" {
		key += ":attempt:" + strconv.FormatUint(id, 10)
	}
	turn := s.turns[key]
	if turn == nil {
		if codexSimulatesClientBehavior(profile) {
			profile.dynamicTool = s.randIntN(5) < 2
			profile.command = profile.dynamicTool && s.randIntN(2) == 0
			profile.fileChange = s.randIntN(5) == 0
		}
		if input.SessionID != "" && input.ThreadID != "" {
			threadKey := strconv.FormatInt(input.AccountID, 10) + ":" + input.ThreadID
			_, seen := s.threads[threadKey]
			profile.firstThread = !seen
			s.threads[threadKey] = input.StartedAt
		}
		turn = &codexTelemetryTurn{profile: profile}
		s.turns[key] = turn
		if input.SessionID != "" && input.ThreadID != "" {
			s.enqueueAnalyticsLocked(profile, id, codexInitializationEvents(profile))
		} else {
			s.skipLocked(profile, id, "missing_outbound_session_or_thread")
		}
	} else {
		// Preserve logical timing and choices, but report the final request's model.
		prior := turn.profile
		turn.profile = profile
		turn.profile.started, turn.profile.firstThread = prior.started, prior.firstThread
		turn.profile.dynamicTool, turn.profile.command, turn.profile.fileChange = prior.dynamicTool, prior.command, prior.fileChange
		turn.profile.attemptCount = prior.attemptCount + 1
	}
	for _, batch := range s.metrics.touch(profile) {
		s.enqueueMetricBatchLocked(batch)
	}
	turn.latest, turn.lastSeen, turn.pending = id, time.Now(), nil
	attempt := &CodexTelemetryAttempt{service: s, turn: turn, epoch: s.epoch, id: id, profile: profile}
	if ctx != nil {
		attempt.contextDone = ctx.Done()
	}
	s.mu.Unlock()
	if ctx != nil && ctx.Done() != nil {
		attempt.contextMu.Lock()
		attempt.stopContext = context.AfterFunc(ctx, func() { attempt.finishOnContextEnd() })
		attempt.contextMu.Unlock()
	}
	return attempt
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

// Retry records a physical attempt without committing a final logical turn.
// The original request context provides a final-failure fallback if no attempt follows.
func (a *CodexTelemetryAttempt) Retry(result CodexTelemetryResult) {
	if a == nil {
		return
	}
	s := a.service
	s.mu.Lock()
	if a.epoch != s.epoch || !s.enabledLocked() {
		s.mu.Unlock()
		return
	}
	s.recordAttemptLocked(a, result)
	if a.turn.finished || a.turn.latest != a.id {
		s.mu.Unlock()
		return
	}
	copyResult := result
	a.turn.pending = &copyResult
	a.turn.lastSeen = time.Now()
	s.mu.Unlock()
	// AfterFunc may already have run while this attempt was still active. Retry
	// must close its own pending result when the original context is already done.
	select {
	case <-a.contextDone:
		a.finishOnContextEnd()
	default:
	}
}

func (a *CodexTelemetryAttempt) finishOnContextEnd() {
	s := a.service
	s.mu.Lock()
	if a.epoch != s.epoch || a.turn.latest != a.id || a.turn.finished {
		s.mu.Unlock()
		return
	}
	// A cancelled request is finalized by its adapter after it has observed the
	// upstream terminal frame. Context fallback only closes attempts that already
	// handed control to a retry path; otherwise it would race a WS drain and turn
	// a real terminal response into a fabricated interruption.
	if a.turn.pending == nil {
		s.mu.Unlock()
		return
	}
	result := CodexTelemetryResult{Status: "interrupted", FinishedAt: time.Now()}
	if a.turn.pending != nil {
		result = *a.turn.pending
	}
	s.mu.Unlock()
	a.Finish(result)
}

func (s *CodexTelemetryService) finishAttempt(a *CodexTelemetryAttempt, result CodexTelemetryResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a.epoch != s.epoch || !s.enabledLocked() {
		return
	}
	s.recordAttemptLocked(a, result)
	if a.turn.finished || a.turn.latest != a.id {
		return
	}
	a.turn.finished, a.turn.lastSeen = true, time.Now()
	terminal := codexTelemetryTerminalFromResult(result)
	profile := a.turn.profile
	if result.ServiceTier != "" {
		profile.serviceTier, profile.input.ServiceTier = result.ServiceTier, result.ServiceTier
	}
	if profile.sessionID != "" && profile.threadID != "" && profile.turnID != "" {
		s.enqueueAnalyticsLocked(profile, a.id, codexTerminalEvents(profile, terminal))
	} else {
		s.skipLocked(profile, a.id, "missing_outbound_turn_identity")
	}
	s.metrics.record(profile, terminal)
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
		httpStatus: result.HTTPStatus, responseID: result.ResponseID}
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
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case now := <-ticker.C:
			s.flushMetrics(now)
		case <-s.stop:
			return
		}
	}
}

func (s *CodexTelemetryService) flushMetrics(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabledLocked() {
		return
	}
	s.expireLocked(now)
	for _, batch := range s.metrics.flush(now) {
		s.enqueueMetricBatchLocked(batch)
	}
}
