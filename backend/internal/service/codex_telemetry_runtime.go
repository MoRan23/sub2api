package service

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	openai "github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/google/uuid"
)

// Runtime mutations are ordered independently of business response delivery. A
// begin reserves capacity for both itself and its eventual result, so a burst of
// new requests cannot crowd completed attempts out of the bounded queue.
type codexTelemetryMutation struct {
	attempt *CodexTelemetryAttempt
	result  *CodexTelemetryResult
	retry   bool
}

type codexTelemetryRuntimeThread struct {
	Initialized    bool      `json:"initialized"`
	CurrentTurn    string    `json:"current_turn"`
	CurrentStarted time.Time `json:"current_started"`
}

type codexTelemetryRuntimeTurn struct {
	Profile      json.RawMessage      `json:"profile"`
	Explicit     bool                 `json:"explicit"`
	ThreadID     string               `json:"thread_id"`
	LastSeen     time.Time            `json:"last_seen"`
	Inflight     map[string]time.Time `json:"inflight"`
	PendingTools map[string]bool      `json:"pending_tools"`
	Continue     bool                 `json:"continue"`
	Sealed       bool                 `json:"sealed"`
	Incomplete   bool                 `json:"incomplete"`
	Attempts     int                  `json:"attempts"`
	Samples      int                  `json:"samples"`
	Result       CodexTelemetryResult `json:"result"`
}

type codexTelemetryRuntimeAttempt struct {
	TurnKey    string    `json:"turn_key"`
	SamplingID string    `json:"sampling_id"`
	Finished   bool      `json:"finished"`
	Retry      bool      `json:"retry"`
	StartedAt  time.Time `json:"started_at"`
}

const codexTelemetryBusinessLease = 90 * time.Second
const codexTelemetryActivityRetention = 7 * 24 * time.Hour

var errCodexTelemetryRuntimeCapacity = errors.New("Codex telemetry runtime capacity reached")

// SetStore replaces the startup-only memory store. An unavailable persistent
// store stays unavailable: production must never fall back to process memory.
func (s *CodexTelemetryService) SetStore(store CodexTelemetryStore) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	s.store = store
	s.mu.Unlock()
	if store == nil {
		return ErrCodexTelemetryInvalidState
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	p, err := store.ReadPolicy(ctx)
	if err != nil {
		return err
	}
	s.applySharedPolicy(p)
	return nil
}

func (s *CodexTelemetryService) SetPolicy(enabled, simulation, observation bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	store, stopped := s.store, s.stopped
	s.mu.Unlock()
	if stopped {
		return
	}
	if store == nil {
		s.applySharedPolicy(CodexTelemetryPolicy{Enabled: enabled, Simulation: simulation, Observation: observation})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	p, err := store.SyncPolicy(ctx, enabled, simulation, observation)
	if err != nil {
		// A failed disable still stops this node immediately. Enabling cannot
		// bypass the shared publication fence when storage is unavailable.
		if !enabled || (!simulation && !observation) {
			s.applySharedPolicy(CodexTelemetryPolicy{Enabled: false, Simulation: simulation, Observation: observation})
		}
		return
	}
	s.applySharedPolicy(p)
	s.publishPolicyChange()
}

func (s *CodexTelemetryService) applySharedPolicy(p CodexTelemetryPolicy) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return
	}
	if p.Epoch > 0 && p.Epoch < s.sharedEpoch {
		return
	}
	changed := s.sharedEpoch != p.Epoch || s.configured != p.Enabled || s.simulationEnabled != p.Simulation || s.observationEnabled != p.Observation
	s.configured, s.simulationEnabled, s.observationEnabled = p.Enabled, p.Simulation, p.Observation
	if p.Epoch > 0 {
		s.sharedEpoch = p.Epoch
	}
	if changed {
		s.resetLocked()
	}
}

func (s *CodexTelemetryService) refreshSharedPolicy(ctx context.Context) {
	s.mu.Lock()
	store := s.store
	s.mu.Unlock()
	if store == nil {
		return
	}
	if policy, err := store.ReadPolicy(ctx); err == nil {
		s.applySharedPolicy(policy)
		s.wakePersistentRuntime()
	}
}

// The caller owns s.mu; every return releases it.
func (s *CodexTelemetryService) beginPersistentLocked(ctx context.Context, profile codexTelemetryProfile, id uint64) *CodexTelemetryAttempt {
	if s.store == nil {
		s.skipLocked(profile, id, "storage_unavailable")
		s.mu.Unlock()
		return nil
	}
	if profile.input.OwnerAccountID == 0 {
		profile.input.OwnerAccountID = profile.input.AccountID
	}
	if profile.input.OSFamily == "" {
		profile.input.OSFamily = openai.DetectOSFamilyFromUserAgent(profile.input.UserAgent)
	}
	if profile.input.OSFamily != "windows" && profile.input.OSFamily != "macos" && profile.input.OSFamily != "linux" {
		profile.input.OSFamily = "unknown"
	}
	profile.simulationEnabled, profile.observationEnabled = s.simulationEnabled, s.observationEnabled
	if profile.input.OSFamily == "unknown" {
		profile.simulationEnabled = false
		profile.reasons = append(profile.reasons, "unknown_operating_system")
	}
	if !profile.simulationEnabled && !profile.observationEnabled {
		s.skipLocked(profile, id, "unknown_operating_system")
		s.mu.Unlock()
		return nil
	}
	profile.source = codexTelemetryRuntimeSource(profile)
	if profile.sessionID == "" || profile.threadID == "" {
		s.skipLocked(profile, id, "missing_outbound_session_or_thread")
	} else if profile.turnID == "" {
		s.skipLocked(profile, id, "missing_outbound_turn_identity")
	}
	if s.mutationReservations+2 > codexTelemetryQueueSize || len(s.mutations) == cap(s.mutations) {
		s.skipLocked(profile, id, "mutation_queue_full")
		s.mu.Unlock()
		return nil
	}
	attempt := &CodexTelemetryAttempt{service: s, epoch: s.epoch, id: id, profile: profile,
		attemptID: uuid.NewString(), policyEpoch: s.sharedEpoch, reservations: 2,
		poolKey: CodexTelemetryPoolKey{OwnerAccountID: profile.input.OwnerAccountID, OSFamily: codexTelemetryPoolOS(profile.input), InstallationID: profile.input.InstallationID}}
	if ctx != nil {
		attempt.contextDone = ctx.Done()
	}
	s.mutationReservations += 2
	s.runtimeAttempts[id] = attempt
	s.mutations <- codexTelemetryMutation{attempt: attempt}
	s.mu.Unlock()
	s.rememberTransport(profile.input)
	return attempt
}

func codexTelemetryRuntimeSource(profile codexTelemetryProfile) string {
	if profile.simulationEnabled && profile.observationEnabled {
		return "mixed"
	}
	if profile.simulationEnabled {
		return "simulated"
	}
	return "observed"
}

// A final wire UA may be unknown even though authorization selection is frozen.
// Do not merge two authorizations into the same missing-installation partition.
func codexTelemetryPoolOS(input CodexTelemetryInput) string {
	if os := NormalizeOpenAIOSFamily(input.CredentialOS); os != "" {
		return os
	}
	return input.OSFamily
}

func (s *CodexTelemetryService) submitPersistentResult(a *CodexTelemetryAttempt, result CodexTelemetryResult, retry bool) {
	result = copyCodexTelemetryRuntimeResult(result)
	s.mu.Lock()
	defer s.mu.Unlock()
	if a.invalid || a.epoch != s.epoch || !s.enabledLocked() {
		s.releaseMutationLocked(a, a.reservations)
		return
	}
	// One slot was reserved by Begin; no business goroutine waits for SQL.
	select {
	case s.mutations <- codexTelemetryMutation{attempt: a, result: &result, retry: retry}:
	default:
		a.invalid = true
		s.skipLocked(a.profile, a.id, "mutation_queue_full")
		s.releaseMutationLocked(a, a.reservations)
	}
}

func copyCodexTelemetryRuntimeResult(result CodexTelemetryResult) CodexTelemetryResult {
	if result.FinishedAt.IsZero() {
		result.FinishedAt = time.Now()
	}
	if result.EndTurn != nil {
		v := *result.EndTurn
		result.EndTurn = &v
	}
	if result.SendSucceeded != nil {
		v := *result.SendSucceeded
		result.SendSucceeded = &v
	}
	result.PendingToolCallIDs = append([]string(nil), result.PendingToolCallIDs...)
	result.EventWaitDurationsMS = append([]float64(nil), result.EventWaitDurationsMS...)
	result.EventWaitFailed = append([]bool(nil), result.EventWaitFailed...)
	if result.ServerTiming != nil {
		copy := make(map[string]float64, len(result.ServerTiming))
		for k, v := range result.ServerTiming {
			copy[k] = v
		}
		result.ServerTiming = copy
	}
	return result
}

func (s *CodexTelemetryService) releaseMutationLocked(a *CodexTelemetryAttempt, count int) {
	if count > a.reservations {
		count = a.reservations
	}
	a.reservations -= count
	s.mutationReservations -= count
	if a.reservations == 0 {
		delete(s.runtimeAttempts, a.id)
	}
}

func (s *CodexTelemetryService) mutationLoop() {
	defer s.wg.Done()
	for {
		select {
		case mutation := <-s.mutations:
			s.applyRuntimeMutation(mutation)
		case <-s.stop:
			return
		}
	}
}

func (s *CodexTelemetryService) applyRuntimeMutation(m codexTelemetryMutation) {
	a := m.attempt
	s.mu.Lock()
	store, valid := s.store, !a.invalid && a.epoch == s.epoch && s.enabledLocked()
	s.mu.Unlock()
	var err error
	if valid && store != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, err = store.TransactPool(ctx, a.poolKey, time.Now(), func(tx *CodexTelemetryPoolTransaction) error {
			if !tx.Policy.Enabled || (!tx.Policy.Simulation && !tx.Policy.Observation) {
				return ErrCodexTelemetryPolicyDisabled
			}
			if tx.Policy.Epoch != a.policyEpoch {
				return ErrCodexTelemetryPolicyChanged
			}
			if err := syncCodexTelemetryRuntimeEpoch(tx, time.Now()); err != nil {
				return err
			}
			if m.result == nil {
				return beginCodexTelemetryRuntime(tx, a)
			}
			return finishCodexTelemetryRuntime(tx, a, *m.result, m.retry)
		})
		cancel()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil && !errors.Is(err, ErrCodexTelemetryPolicyDisabled) && !errors.Is(err, ErrCodexTelemetryPolicyChanged) {
		reason := "storage_unavailable"
		if errors.Is(err, errCodexTelemetryRuntimeCapacity) {
			reason = "runtime_capacity"
		}
		s.skipLocked(a.profile, a.id, reason)
	}
	if m.result == nil && (err != nil || !valid) {
		a.invalid = true
		s.releaseMutationLocked(a, a.reservations)
	} else {
		s.releaseMutationLocked(a, 1)
	}
	if err == nil && valid {
		s.wakePersistentRuntime()
	}
}

func (s *CodexTelemetryService) wakePersistentRuntime() {
	select {
	case s.runtimeWake <- struct{}{}:
	default:
	}
}

func runtimeActivity[T any](tx *CodexTelemetryPoolTransaction, kind, key string) (T, bool, error) {
	var value T
	activity, ok := tx.Activities[CodexTelemetryActivityMapKey(kind, key)]
	if !ok {
		return value, false, nil
	}
	if err := json.Unmarshal(activity.Data, &value); err != nil {
		return value, true, ErrCodexTelemetryInvalidState
	}
	return value, true, nil
}

func putRuntimeActivity(tx *CodexTelemetryPoolTransaction, kind, key string, value any, now, due time.Time) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	tx.Activities[CodexTelemetryActivityMapKey(kind, key)] = CodexTelemetryActivity{Kind: kind, Key: key, Data: data, UpdatedAt: now, DueAt: due}
	return nil
}

func codexRuntimeTurnKey(profile codexTelemetryProfile, attemptID string) string {
	if profile.threadID == "" || profile.turnID == "" {
		return "request:" + attemptID
	}
	key := profile.threadID + ":" + profile.turnID
	// Reauthorization does not recreate the installation pool or its startup
	// markers, but old and new authorizations must never share turn aggregates.
	if profile.input.AuthorizationGeneration != "" {
		key += ":auth:" + profile.input.AuthorizationGeneration
	}
	return key
}

func beginCodexTelemetryRuntime(tx *CodexTelemetryPoolTransaction, a *CodexTelemetryAttempt) error {
	if _, found := tx.Activities[CodexTelemetryActivityMapKey("attempt", a.attemptID)]; found {
		return nil
	}
	profile := a.profile
	profile.poolID, profile.scenarioSeed = tx.Pool.ID, tx.Pool.Seed
	profile.simulationEnabled = tx.Policy.Simulation && profile.input.OSFamily != "unknown"
	profile.observationEnabled = tx.Policy.Observation
	profile.source = codexTelemetryRuntimeSource(profile)
	now := profile.started
	if tx.Pool.LastBusinessAt.Before(now) {
		tx.Pool.LastBusinessAt = now
	}
	key := codexRuntimeTurnKey(profile, a.attemptID)
	needed := 2 // attempt and its reserved eventual sampling record
	if _, exists := tx.Activities[CodexTelemetryActivityMapKey("turn", key)]; !exists {
		needed++
	}
	if err := pruneCodexTelemetryRuntime(tx, time.Now(), needed); err != nil {
		return err
	}
	thread, _, err := runtimeActivity[codexTelemetryRuntimeThread](tx, "thread", profile.threadID)
	if err != nil {
		return err
	}
	newest := !now.Before(thread.CurrentStarted)
	if newest && profile.threadID != "" && profile.turnID != "" && thread.CurrentTurn != "" && thread.CurrentTurn != key {
		if err := sealCodexTelemetryRuntime(tx, thread.CurrentTurn, now); err != nil {
			return err
		}
	}
	turn, exists, err := runtimeActivity[codexTelemetryRuntimeTurn](tx, "turn", key)
	if err != nil {
		return err
	}
	if !exists {
		profile.firstThread = !thread.Initialized && profile.threadID != "" && profile.sessionID != ""
		if profile.simulationEnabled && codexSimulatesClientBehavior(profile) {
			seed := tx.Pool.Seed + ":" + key
			profile.dynamicTool = simulatedInt(seed+":tool", 5) < 2
			profile.command = profile.dynamicTool && simulatedInt(seed+":command", 2) == 0
			profile.fileChange = simulatedInt(seed+":file", 5) == 0 && codexTelemetryWritablePolicy(profile.input.SandboxMode)
		}
		turn = codexTelemetryRuntimeTurn{Explicit: profile.threadID != "" && profile.turnID != "", ThreadID: profile.threadID, Inflight: map[string]time.Time{}, PendingTools: map[string]bool{}}
		if profile.firstThread && profile.simulationEnabled {
			if err := appendRuntimeAnalytics(tx, profile, codexInitializationEvents(profile), now); err != nil {
				return err
			}
			thread.Initialized = true
		}
	} else {
		prior, err := unmarshalCodexTelemetryProfile(turn.Profile)
		if err != nil {
			return err
		}
		profile.started, profile.firstThread = prior.started, prior.firstThread
		profile.dynamicTool, profile.command, profile.fileChange = prior.dynamicTool, prior.command, prior.fileChange
	}
	if turn.Inflight == nil {
		turn.Inflight = map[string]time.Time{}
	}
	if turn.PendingTools == nil {
		turn.PendingTools = map[string]bool{}
	}
	for _, id := range profile.input.ReturnedToolCallIDs {
		delete(turn.PendingTools, id)
	}
	turn.Inflight[a.attemptID] = time.Now().Add(codexTelemetryBusinessLease)
	turn.Attempts++
	if now.After(turn.LastSeen) {
		turn.LastSeen = now
	}
	profile.attemptCount, profile.samplingCount = turn.Attempts, turn.Samples
	turn.Profile, err = marshalCodexTelemetryProfile(profile)
	if err != nil {
		return err
	}
	if newest && profile.threadID != "" && profile.turnID != "" {
		thread.CurrentTurn = key
		thread.CurrentStarted = now
	}
	if profile.threadID != "" {
		if err := putRuntimeActivity(tx, "thread", profile.threadID, thread, now, time.Time{}); err != nil {
			return err
		}
	}
	if err := putRuntimeActivity(tx, "turn", key, turn, now, now.Add(codexTelemetryStateTTL)); err != nil {
		return err
	}
	if err := putRuntimeActivity(tx, "attempt", a.attemptID, codexTelemetryRuntimeAttempt{TurnKey: key, SamplingID: profile.input.SamplingID, StartedAt: now}, now, time.Time{}); err != nil {
		return err
	}
	metrics, err := loadRuntimeMetrics(tx)
	if err != nil {
		return err
	}
	metrics.touch(profile)
	return saveRuntimeMetrics(tx, metrics, now)
}

func codexTelemetryWritablePolicy(policy string) bool {
	switch strings.ReplaceAll(strings.ToLower(strings.TrimSpace(policy)), "-", "_") {
	case "workspace_write", "danger_full_access", "full_access", "external_sandbox":
		return true
	default:
		return false
	}
}

func finishCodexTelemetryRuntime(tx *CodexTelemetryPoolTransaction, a *CodexTelemetryAttempt, result CodexTelemetryResult, retry bool) error {
	attempt, exists, err := runtimeActivity[codexTelemetryRuntimeAttempt](tx, "attempt", a.attemptID)
	if err != nil || !exists || attempt.Finished {
		return err
	}
	turn, exists, err := runtimeActivity[codexTelemetryRuntimeTurn](tx, "turn", attempt.TurnKey)
	if err != nil || !exists {
		return err
	}
	profile, err := unmarshalCodexTelemetryProfile(turn.Profile)
	if err != nil {
		return err
	}
	attempt.Finished, attempt.Retry = true, retry
	if err := putRuntimeActivity(tx, "attempt", a.attemptID, attempt, result.FinishedAt, result.FinishedAt.Add(codexTelemetryActivityRetention)); err != nil {
		return err
	}
	delete(turn.Inflight, a.attemptID)
	metrics, err := loadRuntimeMetrics(tx)
	if err != nil {
		return err
	}
	physical := a.profile
	physical.poolID, physical.scenarioSeed = tx.Pool.ID, tx.Pool.Seed
	physical.source = codexTelemetryRuntimeSource(physical)
	if result.ServiceTier != "" {
		physical.serviceTier, physical.input.ServiceTier = result.ServiceTier, result.ServiceTier
	}
	metrics.recordAttempt(physical, codexTelemetryTerminalFromResult(result))
	if err := saveRuntimeMetrics(tx, metrics, result.FinishedAt); err != nil {
		return err
	}
	if turn.Sealed || turn.Incomplete {
		return putRuntimeActivity(tx, "turn", attempt.TurnKey, turn, result.FinishedAt, time.Time{})
	}
	if result.FinishedAt.After(turn.LastSeen) {
		turn.LastSeen = result.FinishedAt
	}
	if !retry {
		sampleID := attempt.SamplingID
		if sampleID == "" {
			sampleID = result.ResponseID
		}
		if sampleID == "" {
			sampleID = a.attemptID
		}
		sampleKey := attempt.TurnKey + ":" + sampleID
		if _, seen := tx.Activities[CodexTelemetryActivityMapKey("sampling", sampleKey)]; !seen {
			turn.Samples++
			mergeRuntimeResult(&turn.Result, result)
			if err := putRuntimeActivity(tx, "sampling", sampleKey, struct {
				Completed bool `json:"completed"`
			}{true}, result.FinishedAt, result.FinishedAt.Add(codexTelemetryActivityRetention)); err != nil {
				return err
			}
		}
		for _, id := range result.PendingToolCallIDs {
			turn.PendingTools[id] = true
		}
		if result.EndTurn != nil {
			turn.Continue = !*result.EndTurn
		}
	} else if turn.Result.FinishedAt.IsZero() {
		// A failed transport is observable, but is not a client retry or a
		// completed sampling. Preserve the failure for a later inferred summary.
		turn.Result.Status, turn.Result.HTTPStatus = result.Status, result.HTTPStatus
		turn.Result.FinishedAt = result.FinishedAt
	}
	profile.attemptCount, profile.samplingCount = turn.Attempts, turn.Samples
	profile.ended = turn.Result.FinishedAt
	if result.ServiceTier != "" {
		profile.serviceTier, profile.input.ServiceTier = result.ServiceTier, result.ServiceTier
	}
	turn.Profile, err = marshalCodexTelemetryProfile(profile)
	if err != nil {
		return err
	}
	if err := putRuntimeActivity(tx, "turn", attempt.TurnKey, turn, result.FinishedAt, turn.LastSeen.Add(codexTelemetryStateTTL)); err != nil {
		return err
	}
	thread, _, err := runtimeActivity[codexTelemetryRuntimeThread](tx, "thread", turn.ThreadID)
	if err != nil {
		return err
	}
	if turn.Explicit && thread.CurrentTurn != attempt.TurnKey {
		return sealCodexTelemetryRuntime(tx, attempt.TurnKey, result.FinishedAt)
	}
	return nil
}

func mergeRuntimeResult(total *CodexTelemetryResult, next CodexTelemetryResult) {
	input, cached, output, reasoning := total.InputTokens+next.InputTokens, total.CachedInputTokens+next.CachedInputTokens, total.OutputTokens+next.OutputTokens, total.ReasoningOutputTokens+next.ReasoningOutputTokens
	firstEvent, firstToken, firstMessage := earlierRuntimeTime(total.FirstEventAt, next.FirstEventAt), earlierRuntimeTime(total.FirstTokenAt, next.FirstTokenAt), earlierRuntimeTime(total.FirstAgentMessageAt, next.FirstAgentMessageAt)
	if !next.FinishedAt.Before(total.FinishedAt) {
		*total = next
	}
	total.InputTokens, total.CachedInputTokens, total.OutputTokens, total.ReasoningOutputTokens = input, cached, output, reasoning
	total.FirstEventAt, total.FirstTokenAt, total.FirstAgentMessageAt = firstEvent, firstToken, firstMessage
	// Per-event measurements belong to the physical-attempt metrics, never a
	// logical-turn summary or an accumulating persisted transcript.
	total.EventWaitDurationsMS, total.EventWaitFailed, total.ServerTiming = nil, nil, nil
	total.PendingToolCallIDs = nil
}

func earlierRuntimeTime(a, b time.Time) time.Time {
	if a.IsZero() || (!b.IsZero() && b.Before(a)) {
		return b
	}
	return a
}

func sealCodexTelemetryRuntime(tx *CodexTelemetryPoolTransaction, key string, now time.Time) error {
	turn, exists, err := runtimeActivity[codexTelemetryRuntimeTurn](tx, "turn", key)
	if err != nil || !exists || turn.Sealed || turn.Incomplete || !turn.Explicit || len(turn.Inflight) != 0 || len(turn.PendingTools) != 0 || turn.Continue {
		return err
	}
	profile, err := unmarshalCodexTelemetryProfile(turn.Profile)
	if err != nil {
		return err
	}
	turn.Sealed = true
	profile.reasons = append(profile.reasons, "next_explicit_turn_boundary")
	profile.attemptCount, profile.samplingCount, profile.ended = turn.Attempts, turn.Samples, turn.Result.FinishedAt
	terminal := codexTelemetryTerminalFromResult(turn.Result)
	if profile.simulationEnabled {
		if err := appendRuntimeAnalytics(tx, profile, codexTerminalEvents(profile, terminal), now); err != nil {
			return err
		}
		metrics, err := loadRuntimeMetrics(tx)
		if err != nil {
			return err
		}
		metrics.record(profile, terminal)
		if err := saveRuntimeMetrics(tx, metrics, now); err != nil {
			return err
		}
	}
	turn.Profile, err = marshalCodexTelemetryProfile(profile)
	if err != nil {
		return err
	}
	return putRuntimeActivity(tx, "turn", key, turn, now, now.Add(codexTelemetryActivityRetention))
}

func appendRuntimeAnalytics(tx *CodexTelemetryPoolTransaction, profile codexTelemetryProfile, events []codexAnalyticsEvent, now time.Time) error {
	if len(events) == 0 {
		return nil
	}
	profile.source = events[0].source
	for _, event := range events[1:] {
		if profile.source != event.source {
			profile.source = "mixed"
		}
	}
	if profile.source == "" {
		profile.source = codexTelemetryRuntimeSource(profile)
	}
	body, err := json.Marshal(map[string]any{"events": events})
	if err != nil {
		return err
	}
	return appendRuntimeBatch(tx, profile, "analytics", body, now)
}

func appendRuntimeBatch(tx *CodexTelemetryPoolTransaction, profile codexTelemetryProfile, kind string, body []byte, now time.Time) error {
	metadata, err := marshalCodexTelemetryProfile(profile)
	if err != nil {
		return err
	}
	tx.Batches = append(tx.Batches, CodexTelemetryBatch{ID: uuid.NewString(), Pool: tx.Pool, PolicyEpoch: tx.Policy.Epoch,
		AccountID: profile.input.AccountID, ProxyID: profile.input.ProxyID, Type: kind, Source: profile.source,
		UserAgent: profile.client.userAgent, Originator: profile.client.originator, ClientVersion: profile.client.version,
		Payload: append(json.RawMessage(nil), body...), Metadata: metadata, CreatedAt: now, NotBefore: now, Status: "pending"})
	return nil
}

func loadRuntimeMetrics(tx *CodexTelemetryPoolTransaction) (*codexTelemetryMetricStore, error) {
	activity, exists := tx.Activities[CodexTelemetryActivityMapKey("metrics", "current")]
	if !exists {
		return newCodexTelemetryMetricStore(), nil
	}
	return unmarshalCodexTelemetryMetricStore(activity.Data)
}

func saveRuntimeMetrics(tx *CodexTelemetryPoolTransaction, metrics *codexTelemetryMetricStore, now time.Time) error {
	data, err := marshalCodexTelemetryMetricStore(metrics)
	if err != nil {
		return err
	}
	tx.Activities[CodexTelemetryActivityMapKey("metrics", "current")] = CodexTelemetryActivity{Kind: "metrics", Key: "current", Data: data, UpdatedAt: now, DueAt: metrics.nextFlushAt()}
	return nil
}

func (s *CodexTelemetryService) pollPersistentRuntime(now time.Time) {
	if !s.pollMu.TryLock() {
		return
	}
	defer s.pollMu.Unlock()
	s.mu.Lock()
	store, enabled := s.store, s.enabledLocked()
	refresh := s.lastPolicyRefresh.IsZero() || !now.Before(s.lastPolicyRefresh.Add(30*time.Second))
	if refresh {
		s.lastPolicyRefresh = now
	}
	s.mu.Unlock()
	if store == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Repair missed Redis notifications without making Redis authoritative.
	if refresh {
		s.refreshSharedPolicy(ctx)
	}
	// Retention continues after a global disable. A node-local environment
	// override, however, must never mutate shared work owned by other nodes.
	_, forcedOffReason := CodexTelemetryEffectiveState(true)
	if refresh && forcedOffReason == "" {
		_ = store.Maintain(ctx, now)
	}
	if !enabled || !s.Enabled() {
		return
	}
	if refresh {
		s.heartbeatRuntimeAttempts(ctx, store, now)
	}
	keys, err := store.ListDuePools(ctx, now, 100)
	if err == nil {
		for _, key := range keys {
			_, _ = store.TransactPool(ctx, key, now, func(tx *CodexTelemetryPoolTransaction) error { return tickCodexTelemetryRuntime(tx, now) })
		}
	}
	s.dispatchPersistedBatches(ctx)
}

func tickCodexTelemetryRuntime(tx *CodexTelemetryPoolTransaction, now time.Time) error {
	if !tx.Policy.Enabled || (!tx.Policy.Simulation && !tx.Policy.Observation) {
		return ErrCodexTelemetryPolicyDisabled
	}
	if err := syncCodexTelemetryRuntimeEpoch(tx, now); err != nil {
		return err
	}
	for _, activity := range tx.Activities {
		if activity.Kind != "turn" || activity.DueAt.IsZero() || activity.DueAt.After(now) {
			continue
		}
		var turn codexTelemetryRuntimeTurn
		if err := json.Unmarshal(activity.Data, &turn); err != nil {
			return ErrCodexTelemetryInvalidState
		}
		if turn.Sealed || turn.Incomplete {
			continue
		}
		for id, leaseUntil := range turn.Inflight {
			if !leaseUntil.After(now) {
				delete(turn.Inflight, id)
			}
		}
		if len(turn.Inflight) == 0 && !turn.LastSeen.Add(codexTelemetryStateTTL).After(now) {
			turn.Incomplete = true
			if err := putRuntimeActivity(tx, "turn", activity.Key, turn, now, now.Add(codexTelemetryActivityRetention)); err != nil {
				return err
			}
		} else if len(turn.Inflight) > 0 {
			var next time.Time
			for _, expiry := range turn.Inflight {
				next = earlierRuntimeTime(next, expiry)
			}
			if err := putRuntimeActivity(tx, "turn", activity.Key, turn, now, next); err != nil {
				return err
			}
		}
	}
	if err := pruneCodexTelemetryRuntime(tx, now, 0); err != nil {
		return err
	}
	metrics, err := loadRuntimeMetrics(tx)
	if err != nil {
		return err
	}
	for _, batch := range metrics.flush(now) {
		batch.profile.source = batch.source
		if err := appendRuntimeBatch(tx, batch.profile, "metrics", batch.body, now); err != nil {
			return err
		}
	}
	return saveRuntimeMetrics(tx, metrics, now)
}

func (s *CodexTelemetryService) heartbeatRuntimeAttempts(ctx context.Context, store CodexTelemetryStore, now time.Time) {
	s.mu.Lock()
	groups := make(map[CodexTelemetryPoolKey][]string)
	for _, a := range s.runtimeAttempts {
		if a.invalid || a.epoch != s.epoch {
			continue
		}
		select {
		case <-a.contextDone:
			// Cancellation is not an explicit client interrupt. Give adapters a
			// lease interval to drain/record the final upstream frame; afterwards
			// release only local bookkeeping and let shared idle cleanup label the
			// unresolved turn incomplete, without inventing a terminal event.
			if a.contextCancelledAt.IsZero() {
				a.contextCancelledAt = now
			}
			if !a.contextCancelledAt.Add(codexTelemetryBusinessLease).After(now) {
				a.invalid = true
				s.releaseMutationLocked(a, a.reservations)
			}
			continue
		default:
		}
		groups[a.poolKey] = append(groups[a.poolKey], a.attemptID)
	}
	s.mu.Unlock()
	for key, ids := range groups {
		_, _ = store.TransactPool(ctx, key, now, func(tx *CodexTelemetryPoolTransaction) error {
			for _, id := range ids {
				a, exists, err := runtimeActivity[codexTelemetryRuntimeAttempt](tx, "attempt", id)
				if err != nil {
					return err
				}
				if !exists || a.Finished {
					continue
				}
				turn, exists, err := runtimeActivity[codexTelemetryRuntimeTurn](tx, "turn", a.TurnKey)
				if err != nil {
					return err
				}
				if !exists || turn.Incomplete || turn.Sealed {
					continue
				}
				if _, active := turn.Inflight[id]; !active {
					continue
				}
				turn.Inflight[id] = now.Add(codexTelemetryBusinessLease)
				due := turn.LastSeen.Add(codexTelemetryStateTTL)
				if due.Before(now) {
					due = now.Add(codexTelemetryBusinessLease)
				}
				if err := putRuntimeActivity(tx, "turn", a.TurnKey, turn, now, due); err != nil {
					return err
				}
			}
			return nil
		})
	}
}

func syncCodexTelemetryRuntimeEpoch(tx *CodexTelemetryPoolTransaction, now time.Time) error {
	state, exists, err := runtimeActivity[struct {
		Epoch int64 `json:"epoch"`
	}](tx, "policy", "epoch")
	if err != nil {
		return err
	}
	if exists && state.Epoch == tx.Policy.Epoch {
		return nil
	}
	if exists {
		// Keep pool startup/thread markers, but never publish pre-toggle samples
		// under a newer policy generation when telemetry is enabled again.
		for mapKey, activity := range tx.Activities {
			if activity.Kind != "turn" {
				continue
			}
			var turn codexTelemetryRuntimeTurn
			if err := json.Unmarshal(activity.Data, &turn); err != nil {
				return ErrCodexTelemetryInvalidState
			}
			if turn.Sealed || turn.Incomplete {
				continue
			}
			turn.Incomplete, turn.Inflight = true, nil
			data, err := json.Marshal(turn)
			if err != nil {
				return err
			}
			activity.Data, activity.DueAt, activity.UpdatedAt = data, now.Add(codexTelemetryActivityRetention), now
			tx.Activities[mapKey] = activity
		}
		metrics, err := loadRuntimeMetrics(tx)
		if err != nil {
			return err
		}
		metrics.states = make(map[string]*codexMetricState)
		if err := saveRuntimeMetrics(tx, metrics, now); err != nil {
			return err
		}
	}
	return putRuntimeActivity(tx, "policy", "epoch", struct {
		Epoch int64 `json:"epoch"`
	}{tx.Policy.Epoch}, now, time.Time{})
}

// Only short-lived aggregation/dedup rows participate in this cap. Stable pool
// and thread initialization markers never expire because traffic went quiet.
func pruneCodexTelemetryRuntime(tx *CodexTelemetryPoolTransaction, now time.Time, reserve int) error {
	type candidate struct {
		key     string
		updated time.Time
	}
	var removable []candidate
	count := 0
	for key, activity := range tx.Activities {
		completed := false
		switch activity.Kind {
		case "sampling":
			count++
			completed = true
		case "attempt":
			count++
			var a codexTelemetryRuntimeAttempt
			if err := json.Unmarshal(activity.Data, &a); err != nil {
				return ErrCodexTelemetryInvalidState
			}
			completed = a.Finished
			if !completed {
				turn, exists, err := runtimeActivity[codexTelemetryRuntimeTurn](tx, "turn", a.TurnKey)
				if err != nil {
					return err
				}
				completed = !exists || turn.Incomplete || turn.Sealed
			}
		case "turn":
			count++
			var turn codexTelemetryRuntimeTurn
			if err := json.Unmarshal(activity.Data, &turn); err != nil {
				return ErrCodexTelemetryInvalidState
			}
			completed = turn.Sealed || turn.Incomplete
			if !completed {
				count += len(turn.Inflight)
			}
		default:
			continue
		}
		if !completed {
			continue
		}
		if !activity.UpdatedAt.Add(codexTelemetryActivityRetention).After(now) {
			delete(tx.Activities, key)
			count--
			continue
		}
		removable = append(removable, candidate{key: key, updated: activity.UpdatedAt})
	}
	sort.Slice(removable, func(i, j int) bool {
		if removable[i].updated.Equal(removable[j].updated) {
			return removable[i].key < removable[j].key
		}
		return removable[i].updated.Before(removable[j].updated)
	})
	for _, entry := range removable {
		if count+reserve <= codexTelemetryMaxStates {
			break
		}
		delete(tx.Activities, entry.key)
		count--
	}
	if count+reserve > codexTelemetryMaxStates {
		return errCodexTelemetryRuntimeCapacity
	}
	return nil
}
