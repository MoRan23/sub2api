package service

import (
	"bytes"
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	codexTelemetryBatchLifetime  = 24 * time.Hour
	codexTelemetryBatchRetention = 7 * 24 * time.Hour
	codexTelemetryClaimLease     = 30 * time.Second
)

// MemoryCodexTelemetryStore implements the durable store contract for tests.
// Production wiring uses PostgreSQL; this store deliberately has no network I/O.
type MemoryCodexTelemetryStore struct {
	mu           sync.Mutex
	policy       CodexTelemetryPolicy
	pools        map[CodexTelemetryPoolKey]CodexTelemetryPool
	activities   map[CodexTelemetryPoolKey]map[string]CodexTelemetryActivity
	batches      map[string]CodexTelemetryBatch
	nextSequence int64
}

var _ CodexTelemetryStore = (*MemoryCodexTelemetryStore)(nil)

func NewMemoryCodexTelemetryStore() CodexTelemetryStore {
	return &MemoryCodexTelemetryStore{
		policy:     CodexTelemetryPolicy{Epoch: 1, Enabled: true, Simulation: true, Observation: true},
		pools:      make(map[CodexTelemetryPoolKey]CodexTelemetryPool),
		activities: make(map[CodexTelemetryPoolKey]map[string]CodexTelemetryActivity),
		batches:    make(map[string]CodexTelemetryBatch),
	}
}

func (s *MemoryCodexTelemetryStore) SyncPolicy(ctx context.Context, enabled, simulation, observation bool) (CodexTelemetryPolicy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return CodexTelemetryPolicy{}, err
	}
	if s.policy.Enabled == enabled && s.policy.Simulation == simulation && s.policy.Observation == observation {
		return s.policy, nil
	}
	s.policy = CodexTelemetryPolicy{Epoch: s.policy.Epoch + 1, Enabled: enabled, Simulation: simulation, Observation: observation}
	now := time.Now()
	for id, batch := range s.batches {
		if !memoryCodexTelemetryTerminal(batch.Status) {
			if batch.Status == "sending" {
				// Publication already crossed the send boundary. A policy fence
				// cannot establish whether the remote endpoint accepted it.
				batch.Status, batch.ErrorCode = "unknown", "send_result_unknown"
			} else {
				batch.Status, batch.ErrorCode = "cancelled", "policy_changed"
			}
			batch.CompletedAt = now
			batch.LeaseUntil = time.Time{}
			s.batches[id] = batch
		}
	}
	return s.policy, nil
}

func (s *MemoryCodexTelemetryStore) ReadPolicy(ctx context.Context) (CodexTelemetryPolicy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return CodexTelemetryPolicy{}, err
	}
	return s.policy, nil
}

func (s *MemoryCodexTelemetryStore) TransactPool(ctx context.Context, key CodexTelemetryPoolKey, now time.Time, fn func(*CodexTelemetryPoolTransaction) error) (*CodexTelemetryPool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !memoryCodexTelemetryEnabled(s.policy) {
		return nil, ErrCodexTelemetryPolicyDisabled
	}
	if key.OwnerAccountID <= 0 || (key.OSFamily != "windows" && key.OSFamily != "macos" && key.OSFamily != "linux" && key.OSFamily != "unknown") || len(key.InstallationID) > 256 || strings.ContainsRune(key.InstallationID, '\x00') || fn == nil {
		return nil, ErrCodexTelemetryInvalidState
	}
	pool, exists := s.pools[key]
	if !exists {
		pool = CodexTelemetryPool{Key: key, ID: uuid.NewString(), Seed: uuid.NewString(), TemplateVersion: 1, CreatedAt: now}
	}
	tx := CodexTelemetryPoolTransaction{Policy: s.policy, Pool: pool, Activities: memoryCodexTelemetryCloneActivities(s.activities[key])}
	if err := fn(&tx); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if tx.Pool.TemplateVersion < 1 {
		return nil, ErrCodexTelemetryInvalidState
	}
	// Identity, seed, creation time and policy are immutable inside a callback.
	pool.TemplateVersion = tx.Pool.TemplateVersion
	if tx.Pool.LastBusinessAt.After(pool.LastBusinessAt) {
		pool.LastBusinessAt = tx.Pool.LastBusinessAt
	}
	pool.Version++
	for mapKey, activity := range tx.Activities {
		if activity.Kind == "" || len(activity.Kind) > 64 || activity.Key == "" || len(activity.Key) > 512 || mapKey != CodexTelemetryActivityMapKey(activity.Kind, activity.Key) {
			return nil, ErrCodexTelemetryInvalidState
		}
		if err := ValidateCodexTelemetryStoredJSON(activity.Data); err != nil {
			return nil, err
		}
		if previous, ok := s.activities[key][mapKey]; ok && bytes.Equal(previous.Data, activity.Data) && previous.DueAt.Equal(activity.DueAt) {
			activity.UpdatedAt = previous.UpdatedAt
		} else {
			activity.UpdatedAt = now
		}
		tx.Activities[mapKey] = activity
	}
	newBatches := make(map[string]CodexTelemetryBatch, len(tx.Batches))
	nextSequence := s.nextSequence
	for _, batch := range tx.Batches {
		if batch.PolicyEpoch != 0 && batch.PolicyEpoch != s.policy.Epoch {
			return nil, ErrCodexTelemetryPolicyChanged
		}
		if (key.OSFamily == "unknown" && batch.Source != "observed") ||
			(batch.Source == "simulated" && !s.policy.Simulation) ||
			(batch.Source == "observed" && !s.policy.Observation) ||
			(batch.Source == "mixed" && (!s.policy.Simulation || !s.policy.Observation)) {
			return nil, ErrCodexTelemetryInvalidState
		}
		if batch.ID == "" {
			batch.ID = uuid.NewString()
		}
		if _, err := uuid.Parse(batch.ID); err != nil ||
			(batch.Type != "analytics" && batch.Type != "metrics") ||
			(batch.Source != "observed" && batch.Source != "simulated" && batch.Source != "mixed") ||
			(batch.ProxyID != nil && *batch.ProxyID <= 0) || len(batch.UserAgent) > 2048 || len(batch.Originator) > 256 || len(batch.ClientVersion) > 128 {
			return nil, ErrCodexTelemetryInvalidState
		}
		if batch.AccountID == 0 {
			batch.AccountID = key.OwnerAccountID
		}
		if batch.AccountID <= 0 {
			return nil, ErrCodexTelemetryInvalidState
		}
		if _, exists := s.batches[batch.ID]; exists {
			return nil, ErrCodexTelemetryInvalidState
		}
		if _, exists := newBatches[batch.ID]; exists {
			return nil, ErrCodexTelemetryInvalidState
		}
		if len(batch.Payload) == 0 {
			return nil, ErrCodexTelemetryInvalidState
		}
		if err := ValidateCodexTelemetryStoredJSON(batch.Payload); err != nil {
			return nil, err
		}
		if len(batch.Metadata) == 0 {
			batch.Metadata = json.RawMessage(`{}`)
		}
		if err := ValidateCodexTelemetryStoredJSON(batch.Metadata); err != nil {
			return nil, err
		}
		batch.Pool, batch.PolicyEpoch = pool, s.policy.Epoch
		nextSequence++
		batch.Sequence = nextSequence
		batch.Status, batch.ClaimID, batch.ErrorCode = "queued", "", ""
		if batch.CreatedAt.IsZero() {
			batch.CreatedAt = now
		}
		if batch.NotBefore.IsZero() {
			batch.NotBefore = now
		}
		batch.LeaseUntil, batch.StartedAt, batch.CompletedAt = time.Time{}, time.Time{}, time.Time{}
		batch.HTTPStatus = 0
		newBatches[batch.ID] = memoryCodexTelemetryCloneBatch(batch)
	}
	s.pools[key] = pool
	s.nextSequence = nextSequence
	s.activities[key] = memoryCodexTelemetryCloneActivities(tx.Activities)
	for id, batch := range newBatches {
		s.batches[id] = batch
	}
	return &pool, nil
}

func (s *MemoryCodexTelemetryStore) ListDuePools(ctx context.Context, now time.Time, limit int) ([]CodexTelemetryPoolKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	limit = memoryCodexTelemetryLimit(limit)
	type duePool struct {
		key CodexTelemetryPoolKey
		due time.Time
		id  string
	}
	var due []duePool
	for key, activities := range s.activities {
		var earliest time.Time
		for _, activity := range activities {
			if !activity.DueAt.IsZero() && !activity.DueAt.After(now) && (earliest.IsZero() || activity.DueAt.Before(earliest)) {
				earliest = activity.DueAt
			}
		}
		if !earliest.IsZero() {
			due = append(due, duePool{key, earliest, s.pools[key].ID})
		}
	}
	sort.Slice(due, func(i, j int) bool {
		if due[i].due.Equal(due[j].due) {
			return due[i].id < due[j].id
		}
		return due[i].due.Before(due[j].due)
	})
	if len(due) > limit {
		due = due[:limit]
	}
	keys := make([]CodexTelemetryPoolKey, 0, len(due))
	for _, entry := range due {
		keys = append(keys, entry.key)
	}
	return keys, nil
}

func (s *MemoryCodexTelemetryStore) ClaimBatches(ctx context.Context, now time.Time, limit int) ([]CodexTelemetryBatch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.maintainLocked(now)
	if !memoryCodexTelemetryEnabled(s.policy) {
		return nil, nil
	}
	limit = memoryCodexTelemetryLimit(limit)
	// The oldest unfinished batch owns its pool's publication order. A delayed
	// head also fences later batches; otherwise network completion could publish
	// a turn before its thread initialization or startup activities.
	heads := make(map[string]CodexTelemetryBatch)
	for _, batch := range s.batches {
		if memoryCodexTelemetryTerminal(batch.Status) {
			continue
		}
		head, exists := heads[batch.Pool.ID]
		if !exists || batch.Sequence < head.Sequence {
			heads[batch.Pool.ID] = batch
		}
	}
	var due []CodexTelemetryBatch
	for _, batch := range heads {
		if batch.Status == "queued" && batch.PolicyEpoch == s.policy.Epoch && !batch.NotBefore.After(now) {
			due = append(due, batch)
		}
	}
	sort.Slice(due, func(i, j int) bool {
		return due[i].Sequence < due[j].Sequence
	})
	if len(due) > limit {
		due = due[:limit]
	}
	for i, batch := range due {
		batch.Status, batch.ClaimID, batch.LeaseUntil = "claimed", uuid.NewString(), now.Add(codexTelemetryClaimLease)
		s.batches[batch.ID] = batch
		due[i] = memoryCodexTelemetryCloneBatch(batch)
	}
	return due, nil
}

func (s *MemoryCodexTelemetryStore) MarkSending(ctx context.Context, id, claimID string, epoch int64, now time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if _, err := uuid.Parse(id); err != nil {
		return false, ErrCodexTelemetryInvalidState
	}
	if _, err := uuid.Parse(claimID); err != nil {
		return false, ErrCodexTelemetryInvalidState
	}
	batch, exists := s.batches[id]
	if !exists || batch.Status != "claimed" || claimID == "" || batch.ClaimID != claimID || batch.PolicyEpoch != epoch || epoch != s.policy.Epoch || !memoryCodexTelemetryEnabled(s.policy) || !now.Before(batch.LeaseUntil) || !now.Before(batch.CreatedAt.Add(codexTelemetryBatchLifetime)) {
		return false, nil
	}
	batch.Status, batch.StartedAt, batch.LeaseUntil = "sending", now, now.Add(codexTelemetryClaimLease)
	s.batches[id] = batch
	return true, nil
}

func (s *MemoryCodexTelemetryStore) CompleteBatch(ctx context.Context, id, claimID string, result CodexTelemetryBatchResult, now time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if !memoryCodexTelemetryTerminal(result.Status) || !memoryCodexTelemetrySafeErrorCode(result.ErrorCode) || result.HTTPStatus < 0 || result.HTTPStatus > 599 {
		return false, ErrCodexTelemetryInvalidState
	}
	if _, err := uuid.Parse(id); err != nil {
		return false, ErrCodexTelemetryInvalidState
	}
	if _, err := uuid.Parse(claimID); err != nil {
		return false, ErrCodexTelemetryInvalidState
	}
	batch, exists := s.batches[id]
	if !exists || (batch.Status != "claimed" && batch.Status != "sending") || claimID == "" || batch.ClaimID != claimID || !now.Before(batch.LeaseUntil) {
		return false, nil
	}
	batch.Status, batch.CompletedAt, batch.HTTPStatus, batch.ErrorCode = result.Status, now, result.HTTPStatus, result.ErrorCode
	batch.LeaseUntil = time.Time{}
	s.batches[id] = batch
	return true, nil
}

func (s *MemoryCodexTelemetryStore) Maintain(ctx context.Context, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	s.maintainLocked(now)
	return nil
}

func (s *MemoryCodexTelemetryStore) maintainLocked(now time.Time) {
	for id, batch := range s.batches {
		if memoryCodexTelemetryTerminal(batch.Status) {
			if !batch.CompletedAt.IsZero() && !batch.CompletedAt.Add(codexTelemetryBatchRetention).After(now) {
				delete(s.batches, id)
			}
			continue
		}
		if batch.Status == "sending" && !batch.LeaseUntil.After(now) {
			batch.Status, batch.CompletedAt, batch.ErrorCode, batch.LeaseUntil = "unknown", now, "send_result_unknown", time.Time{}
		} else if batch.Status != "sending" && !batch.CreatedAt.Add(codexTelemetryBatchLifetime).After(now) {
			batch.Status, batch.CompletedAt, batch.ErrorCode, batch.LeaseUntil = "dropped", now, "batch_expired", time.Time{}
		} else if batch.Status == "claimed" && !batch.LeaseUntil.After(now) {
			batch.Status, batch.ClaimID, batch.LeaseUntil = "queued", "", time.Time{}
		}
		s.batches[id] = batch
	}
}

func memoryCodexTelemetryEnabled(policy CodexTelemetryPolicy) bool {
	return policy.Enabled && (policy.Simulation || policy.Observation)
}

func memoryCodexTelemetryLimit(limit int) int {
	if limit <= 0 {
		return 256
	}
	if limit > 4096 {
		return 4096
	}
	return limit
}

func memoryCodexTelemetryTerminal(status string) bool {
	switch status {
	case "sent", "failed", "dropped", "cancelled", "skipped", "unknown":
		return true
	}
	return false
}

func memoryCodexTelemetrySafeErrorCode(code string) bool {
	if len(code) > 128 {
		return false
	}
	if len(code) > 0 && (code[0] < 'a' || code[0] > 'z') {
		return false
	}
	for _, r := range code {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
			return false
		}
	}
	return true
}

func memoryCodexTelemetryCloneActivities(src map[string]CodexTelemetryActivity) map[string]CodexTelemetryActivity {
	dst := make(map[string]CodexTelemetryActivity, len(src))
	for key, activity := range src {
		activity.Data = append(json.RawMessage(nil), activity.Data...)
		dst[key] = activity
	}
	return dst
}

func memoryCodexTelemetryCloneBatch(batch CodexTelemetryBatch) CodexTelemetryBatch {
	batch.Payload = append(json.RawMessage(nil), batch.Payload...)
	batch.Metadata = append(json.RawMessage(nil), batch.Metadata...)
	if batch.ProxyID != nil {
		proxyID := *batch.ProxyID
		batch.ProxyID = &proxyID
	}
	return batch
}

// ValidateCodexTelemetryStoredJSON guards the allowlisted summaries against an
// accidental request/response or credential object being persisted. Callers
// must still construct their DTOs explicitly, rather than filtering raw bodies.
func ValidateCodexTelemetryStoredJSON(data json.RawMessage) error {
	if len(data) == 0 {
		return nil
	}
	if len(data) > 2*1024*1024 {
		return ErrCodexTelemetryInvalidState
	}
	if !json.Valid(data) {
		return ErrCodexTelemetryInvalidState
	}
	// Walk every token, rather than unmarshalling into a map: a duplicate JSON
	// key could otherwise hide a secret-bearing earlier value while its bytes
	// would still be written into the stored payload.
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var safe func(int) bool
	safe = func(depth int) bool {
		if depth > 128 {
			return false
		}
		token, err := decoder.Token()
		if err != nil {
			return false
		}
		switch token {
		case json.Delim('{'):
			seen := make(map[string]bool)
			for decoder.More() {
				keyToken, err := decoder.Token()
				key, ok := keyToken.(string)
				if err != nil || !ok || seen[key] {
					return false
				}
				seen[key] = true
				normalized := strings.ToLower(strings.NewReplacer("-", "", "_", "", ".", "").Replace(key))
				switch normalized {
				case "accesstoken", "refreshtoken", "idtoken", "authorization", "credentials", "instructions", "messages", "proxyurl", "requestbody", "rawresponse", "apikey", "bearertoken":
					return false
				}
				if !safe(depth + 1) {
					return false
				}
			}
			end, err := decoder.Token()
			return err == nil && end == json.Delim('}')
		case json.Delim('['):
			for decoder.More() {
				if !safe(depth + 1) {
					return false
				}
			}
			end, err := decoder.Token()
			return err == nil && end == json.Delim(']')
		}
		return true
	}
	if !safe(0) {
		return ErrCodexTelemetryInvalidState
	}
	return nil
}
