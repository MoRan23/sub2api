package service

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func codexStateTestToken(blocks int, issued time.Time) string {
	data := make([]byte, 57+blocks*16)
	data[0] = 0x80
	binary.BigEndian.PutUint64(data[1:9], uint64(issued.Unix()))
	for i := 9; i < len(data); i++ {
		data[i] = byte(i)
	}
	return base64.URLEncoding.EncodeToString(data)
}

func TestCodexTurnStateParserShapeAndTime(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, kind, shape string
		blocks, length    int
	}{
		{"personal", "personal", "target", 10, 292}, {"personal_extended", "personal", "extended", 11, 312},
		{"team", "team_business", "target", 12, 332}, {"team_extended", "team_business", "extended", 13, 356},
	} {
		t.Run(tc.name, func(t *testing.T) {
			token := codexStateTestToken(tc.blocks, now)
			parsed, err := ParseCodexTurnState(token, tc.kind, now)
			require.NoError(t, err)
			require.Equal(t, tc.length, len(token))
			require.Equal(t, tc.shape, parsed.Shape)
			require.Equal(t, now.Add(time.Hour), parsed.ExpiresAt)
		})
	}
	for _, tc := range []struct{ name, token, kind string }{
		{"wrong_type", codexStateTestToken(12, now), "personal"}, {"unknown", codexStateTestToken(10, now), ""},
		{"expired", codexStateTestToken(10, now.Add(-time.Hour)), "personal"}, {"future", codexStateTestToken(10, now.Add(time.Minute)), "personal"},
		{"trimmed", codexStateTestToken(10, now) + " ", "personal"}, {"padding", strings.TrimRight(codexStateTestToken(10, now), "="), "personal"},
		{"version", "A" + codexStateTestToken(10, now)[1:], "personal"},
	} {
		t.Run(tc.name, func(t *testing.T) { _, err := ParseCodexTurnState(tc.token, tc.kind, now); require.Error(t, err) })
	}
}

func TestCodexTurnStateMetadataOnly(t *testing.T) {
	require.Equal(t, []string{"abc"}, CodexTurnStateTokensFromEvent([]byte(`{"type":"response.metadata","headers":{"X-Codex-Turn-State":"abc"}}`)))
	require.Nil(t, CodexTurnStateTokensFromEvent([]byte(`{"type":"response.output_text.delta","headers":{"x-codex-turn-state":"untrusted"}}`)))
	require.Nil(t, CodexTurnStateTokensFromEvent([]byte(`{"type":"response.completed","response":{"metadata":{"headers":{"x-codex-turn-state":"untrusted"}}}}`)))
}

func TestCodexTurnStateSafeExpiredObservation(t *testing.T) {
	s, _, account := newCodexStateTestService(t)
	attempt, err := s.Prepare(context.Background(), account, "gpt-5")
	require.NoError(t, err)
	s.Observe(attempt, codexStateTestToken(10, s.now().Add(-time.Hour)))
	require.Equal(t, "expired", attempt.SafeObservation().Shape)
	require.NoError(t, s.Finish(context.Background(), attempt, false))
}

type codexStateMemoryRepo struct {
	mu      sync.Mutex
	records map[CodexTurnStateKey]CodexTurnStateRecord
	leases  map[CodexTurnStateKey]map[string]time.Time
	locks   map[int64]string
	getErr  error
	now     func() time.Time
}

func newCodexStateMemoryRepo() *codexStateMemoryRepo {
	return &codexStateMemoryRepo{records: map[CodexTurnStateKey]CodexTurnStateRecord{}, leases: map[CodexTurnStateKey]map[string]time.Time{}, locks: map[int64]string{}}
}
func (r *codexStateMemoryRepo) BeginBusiness(_ context.Context, k CodexTurnStateKey, id string, now, until time.Time) (*CodexTurnStateRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.records[k]
	if !ok {
		v = CodexTurnStateRecord{OwnerAccountID: k.OwnerAccountID, Model: k.Model, Generation: k.Generation, Version: 1, LastBusinessAt: time.Unix(0, 0)}
	}
	if v.LastBusinessAt.After(time.Unix(0, 0)) && v.LastBusinessAt.Before(now.Add(-CodexTurnStateActiveWindow)) {
		if v.DemandReason != "" || v.CollectorAttemptID != "" {
			v.Version++
		}
		clearIdleCodexTurnStateDemand(&v)
	}
	r.records[k] = v
	if r.leases[k] == nil {
		r.leases[k] = map[string]time.Time{}
	}
	r.leases[k][id] = until
	return &v, nil
}
func (r *codexStateMemoryRepo) MarkBusinessSent(_ context.Context, key CodexTurnStateKey, sentAt time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if record, ok := r.records[key]; ok && sentAt.After(record.LastBusinessAt) {
		record.LastBusinessAt = sentAt
		r.records[key] = record
	}
	return nil
}

func markCodexStateTestBusinessSent(t *testing.T, state *CodexTurnStateService, attempt *CodexTurnStateAttempt) {
	t.Helper()
	attempt.mu.Lock()
	attempt.businessSentAt = state.now()
	attempt.historyPhysicalBound = true
	attempt.mu.Unlock()
	if attempt.Enabled {
		require.NoError(t, state.repo.MarkBusinessSent(context.Background(), attempt.key, state.now()))
	}
}

func seedCodexStateTestDemand(t *testing.T, state *CodexTurnStateService, account *Account, model string) *CodexTurnStateAttempt {
	t.Helper()
	attempt, err := state.Prepare(context.Background(), account, model)
	require.NoError(t, err)
	require.NotNil(t, attempt)
	markCodexStateTestBusinessSent(t, state, attempt)
	state.Observe(attempt, codexStateTestToken(11, state.now()))
	require.NoError(t, state.Finish(context.Background(), attempt, true))
	return attempt
}
func (r *codexStateMemoryRepo) EndBusiness(_ context.Context, k CodexTurnStateKey, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.leases[k], id)
	return nil
}
func (r *codexStateMemoryRepo) Get(_ context.Context, k CodexTurnStateKey) (*CodexTurnStateRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.getErr != nil {
		return nil, r.getErr
	}
	v, ok := r.records[k]
	if !ok {
		return nil, nil
	}
	v.BusinessInFlight = r.businessInFlightLocked(k)
	return &v, nil
}

func (r *codexStateMemoryRepo) businessInFlightLocked(key CodexTurnStateKey) bool {
	now := time.Now()
	if r.now != nil {
		now = r.now()
	}
	for _, until := range r.leases[key] {
		if until.After(now) {
			return true
		}
	}
	return false
}

func (r *codexStateMemoryRepo) SaveCAS(_ context.Context, v CodexTurnStateRecord, expected int64) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	old, ok := r.records[v.Key()]
	if !ok || old.Version != expected {
		return false, nil
	}
	v.BusinessInFlight = false
	v.Version = expected + 1
	if old.LastBusinessAt.After(v.LastBusinessAt) {
		v.LastBusinessAt = old.LastBusinessAt
	}
	if old.HistoryProofObservedAt.After(v.HistoryProofObservedAt) {
		v.HistoryProofObservedAt = old.HistoryProofObservedAt
	}
	r.records[v.Key()] = v
	return true, nil
}
func (r *codexStateMemoryRepo) ListActive(_ context.Context, since time.Time, limit int) ([]CodexTurnStateRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []CodexTurnStateRecord
	for _, v := range r.records {
		if !v.LastBusinessAt.Before(since) && len(out) < limit && (v.DemandReason != "" || v.EncryptedToken != "") {
			v.BusinessInFlight = r.businessInFlightLocked(v.Key())
			out = append(out, v)
		}
	}
	return out, nil
}
func (r *codexStateMemoryRepo) ListByAccount(_ context.Context, id int64) ([]CodexTurnStateRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []CodexTurnStateRecord
	for _, v := range r.records {
		if v.OwnerAccountID == id {
			v.BusinessInFlight = r.businessInFlightLocked(v.Key())
			out = append(out, v)
		}
	}
	return out, nil
}
func (r *codexStateMemoryRepo) ListByAccounts(_ context.Context, ids []int64) ([]CodexTurnStateRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	owners := make(map[int64]bool, len(ids))
	for _, id := range ids {
		owners[id] = true
	}
	var records []CodexTurnStateRecord
	for _, record := range r.records {
		if owners[record.OwnerAccountID] {
			record.BusinessInFlight = r.businessInFlightLocked(record.Key())
			records = append(records, record)
		}
	}
	return records, nil
}
func (r *codexStateMemoryRepo) HasBusiness(_ context.Context, k CodexTurnStateKey, now time.Time) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, until := range r.leases[k] {
		if until.After(now) {
			return true, nil
		}
	}
	return false, nil
}
func (r *codexStateMemoryRepo) AcquireCollector(_ context.Context, id int64, owner string, _ time.Duration) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.locks[id] != "" {
		return false, nil
	}
	r.locks[id] = owner
	return true, nil
}
func (r *codexStateMemoryRepo) ReleaseCollector(_ context.Context, id int64, owner string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.locks[id] == owner {
		delete(r.locks, id)
	}
	return nil
}
func (r *codexStateMemoryRepo) PublishCancel(context.Context, CodexTurnStateKey) error { return nil }
func (r *codexStateMemoryRepo) SubscribeCancels(ctx context.Context, _ func(CodexTurnStateKey)) error {
	<-ctx.Done()
	return ctx.Err()
}

type codexStateTestAccounts struct {
	AccountRepository
	mu      sync.Mutex
	account *Account
}

func (r *codexStateTestAccounts) GetByID(_ context.Context, id int64) (*Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.account.ID != id {
		return nil, nil
	}
	return r.account, nil
}

type codexStateTestEncryptor struct{}

func (codexStateTestEncryptor) Encrypt(value string) (string, error) {
	return "encrypted:" + value, nil
}
func (codexStateTestEncryptor) Decrypt(value string) (string, error) {
	if !strings.HasPrefix(value, "encrypted:") {
		return "", errors.New("invalid")
	}
	return strings.TrimPrefix(value, "encrypted:"), nil
}

type codexStateTestCollector func(context.Context, CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error)

func (f codexStateTestCollector) Collect(ctx context.Context, in CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error) {
	return f(ctx, in)
}

func newCodexStateTestService(t *testing.T) (*CodexTurnStateService, *codexStateMemoryRepo, *Account) {
	t.Helper()
	a := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Credentials: map[string]any{"access_token": "test-token", "plan_type": "plus"}, Extra: map[string]any{"codex_turn_state": map[string]any{"enabled": true, "account_type": "personal", "collector_proxy_id": float64(2)}, "codex_turn_state_generation": "gen1"}}
	repo := newCodexStateMemoryRepo()
	s := NewCodexTurnStateService(repo, &codexStateTestAccounts{account: a}, codexStateTestEncryptor{}, nil)
	s.modelPolicy = newCodexStateTestModelPolicy("gpt-5", "gpt-5-mini", "gpt-5.4", "final-model", "other-model")
	s.now = func() time.Time { return time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC) }
	repo.now = func() time.Time { return s.now() }
	return s, repo, a
}

func TestCodexTurnStateNaturalLearningAvoidsCollector(t *testing.T) {
	s, repo, a := newCodexStateTestService(t)
	ctx := context.Background()
	var calls atomic.Int64
	s.collector = codexStateTestCollector(func(context.Context, CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error) {
		calls.Add(1)
		return CodexTurnStateCollectResult{}, nil
	})
	attempt, err := s.Prepare(ctx, a, "gpt-5")
	require.NoError(t, err)
	require.NotNil(t, attempt)
	markCodexStateTestBusinessSent(t, s, attempt)
	s.collect(ctx, attempt.key)
	require.Zero(t, calls.Load(), "a cold cache alone does not create collection demand")
	token := codexStateTestToken(10, s.now())
	s.ObserveHeaders(attempt, http.Header{"x-codex-turn-state": {token}})
	require.NoError(t, s.Finish(ctx, attempt, true))
	s.collect(ctx, attempt.key)
	require.Zero(t, calls.Load(), "natural target is sufficient")
	next, err := s.Prepare(ctx, a, "gpt-5")
	require.NoError(t, err)
	markCodexStateTestBusinessSent(t, s, next)
	require.Equal(t, token, next.Snapshot.Token)
	require.True(t, s.ValidateAttempt(ctx, next))
	record, _ := repo.Get(ctx, next.key)
	require.NotContains(t, record.EncryptedToken[:10], token)
	s.Observe(next, token)
	require.NoError(t, s.Finish(ctx, next, true))
	after, _ := repo.Get(ctx, next.key)
	require.Equal(t, record.ExpiresAt, after.ExpiresAt)
	require.Equal(t, record.Version, after.Version, "duplicate does not refresh record")
	status, err := s.GetStatus(ctx, a.ID)
	require.NoError(t, err)
	encoded, _ := json.Marshal(status)
	require.NotContains(t, string(encoded), token)
}

func TestCodexTurnStateDiscardAndStaleExtended(t *testing.T) {
	s, repo, a := newCodexStateTestService(t)
	ctx := context.Background()
	discard, _ := s.Prepare(ctx, a, "gpt-5")
	s.Observe(discard, codexStateTestToken(10, s.now()))
	require.NoError(t, s.Finish(ctx, discard, false))
	record, _ := repo.Get(ctx, discard.key)
	require.Empty(t, record.EncryptedToken)
	old, _ := s.Prepare(ctx, a, "gpt-5")
	markCodexStateTestBusinessSent(t, s, old)
	fresh, _ := s.Prepare(ctx, a, "gpt-5")
	markCodexStateTestBusinessSent(t, s, fresh)
	s.Observe(fresh, codexStateTestToken(10, s.now()))
	require.NoError(t, s.Finish(ctx, fresh, true))
	s.Observe(old, codexStateTestToken(11, s.now()))
	require.NoError(t, s.Finish(ctx, old, true))
	record, _ = repo.Get(ctx, old.key)
	require.NotEmpty(t, record.EncryptedToken, "late extended shape cannot clear a newer version")
	extended, _ := s.Prepare(ctx, a, "gpt-5")
	markCodexStateTestBusinessSent(t, s, extended)
	s.Observe(extended, codexStateTestToken(11, s.now()))
	require.NoError(t, s.Finish(ctx, extended, true))
	record, _ = repo.Get(ctx, extended.key)
	require.Empty(t, record.EncryptedToken)
	require.Equal(t, "extended_shape", record.RefreshReason)
}

func TestCodexTurnStateGenerationAndStorageFailClosed(t *testing.T) {
	s, repo, a := newCodexStateTestService(t)
	ctx := context.Background()
	attempt, _ := s.Prepare(ctx, a, "gpt-5")
	s.Observe(attempt, codexStateTestToken(10, s.now()))
	a.Extra["codex_turn_state_generation"] = "gen2"
	require.NoError(t, s.Finish(ctx, attempt, true))
	record, _ := repo.Get(ctx, attempt.key)
	require.Empty(t, record.EncryptedToken)
	require.False(t, s.ValidateAttempt(ctx, attempt))
	next, _ := s.Prepare(ctx, a, "gpt-5")
	repo.mu.Lock()
	repo.getErr = errors.New("database down")
	repo.mu.Unlock()
	require.False(t, s.ValidateAttempt(ctx, next))
	require.NoError(t, s.Finish(ctx, next, false))
}

func TestCodexTurnStatePhysicalCredentialsAreBoundToGeneration(t *testing.T) {
	s, _, account := newCodexStateTestService(t)
	stale := *account
	stale.Credentials = map[string]any{"access_token": "old-token", "plan_type": "plus"}
	attempt, err := s.Prepare(context.Background(), &stale, "gpt-5")
	require.NoError(t, err)
	require.NotNil(t, attempt)
	require.False(t, attempt.Enabled)
	require.Empty(t, attempt.Snapshot.Token)
	require.Equal(t, "physical_credentials_stale", attempt.MaintenanceReason)
	attempt, err = s.Prepare(context.Background(), account, "gpt-5")
	require.NoError(t, err)
	require.True(t, s.ValidateCredentialHeaders(context.Background(), attempt, http.Header{"authorization": {"Bearer test-token"}}))
	require.False(t, s.ValidateCredentialHeaders(context.Background(), attempt, http.Header{"authorization": {"Bearer old-token"}}))
	require.NoError(t, s.Finish(context.Background(), attempt, false))
}

func TestCodexTurnStateTeamAndUnknownType(t *testing.T) {
	s, repo, account := newCodexStateTestService(t)
	cfg := account.Extra["codex_turn_state"].(map[string]any)
	cfg["account_type"] = "team_business"
	attempt, err := s.Prepare(context.Background(), account, "gpt-5")
	require.NoError(t, err)
	s.Observe(attempt, codexStateTestToken(12, s.now()))
	require.NoError(t, s.Finish(context.Background(), attempt, true))
	next, err := s.Prepare(context.Background(), account, "gpt-5")
	require.NoError(t, err)
	require.Len(t, next.Snapshot.Token, 332)
	require.NoError(t, s.Finish(context.Background(), next, false))
	cfg["account_type"] = "auto"
	account.Credentials["plan_type"] = "unknown"
	account.Extra["codex_turn_state_generation"] = "gen2"
	unknown, err := s.Prepare(context.Background(), account, "gpt-5")
	require.NoError(t, err)
	require.Empty(t, unknown.Snapshot.Token)
	s.Observe(unknown, codexStateTestToken(10, s.now()))
	require.NoError(t, s.Finish(context.Background(), unknown, true))
	record, err := repo.Get(context.Background(), unknown.key)
	require.NoError(t, err)
	require.Empty(t, record.EncryptedToken)
}

func TestCodexTurnStateCollectorPacingAndActivity(t *testing.T) {
	s, repo, account := newCodexStateTestService(t)
	ctx := context.Background()
	attempt := seedCodexStateTestDemand(t, s, account, "gpt-5")
	var calls atomic.Int64
	s.collector = codexStateTestCollector(func(context.Context, CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error) {
		calls.Add(1)
		return CodexTurnStateCollectResult{StatusCode: 429, RetryAfter: 10 * time.Minute}, nil
	})
	s.collect(ctx, attempt.key)
	require.EqualValues(t, 1, calls.Load())
	record, _ := repo.Get(ctx, attempt.key)
	require.Equal(t, s.now().Add(10*time.Minute), record.NextCollectAt)
	s.collect(ctx, attempt.key)
	require.EqualValues(t, 1, calls.Load(), "retry-after is respected")
	record.NextCollectAt = time.Time{}
	record.LastBusinessAt = s.now().Add(-CodexTurnStateActiveWindow - time.Second)
	repo.mu.Lock()
	repo.records[attempt.key] = *record
	repo.mu.Unlock()
	s.collect(ctx, attempt.key)
	require.EqualValues(t, 1, calls.Load(), "idle models do not collect")
}

func TestCodexTurnStateSlowCollectorLosesToNaturalResponse(t *testing.T) {
	s, repo, a := newCodexStateTestService(t)
	ctx := context.Background()
	initial := seedCodexStateTestDemand(t, s, a, "gpt-5")
	started := make(chan struct{})
	release := make(chan struct{})
	cancelled := make(chan struct{})
	done := make(chan struct{})
	s.collector = codexStateTestCollector(func(ctx context.Context, _ CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error) {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
			close(cancelled)
		}
		return CodexTurnStateCollectResult{StatusCode: 200, Tokens: []string{codexStateTestToken(10, s.now().Add(-time.Minute))}}, nil
	})
	go func() { defer close(done); s.collect(ctx, initial.key) }()
	<-started
	natural, _ := s.Prepare(ctx, a, "gpt-5")
	token := codexStateTestToken(10, s.now())
	s.Observe(natural, token)
	require.NoError(t, s.Finish(ctx, natural, true))
	select {
	case <-cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("a committed natural target must cancel the active collector")
	}
	close(release)
	<-done
	record, _ := repo.Get(ctx, natural.key)
	plain, _ := s.encryptor.Decrypt(record.EncryptedToken)
	require.Equal(t, token, plain)
	require.Equal(t, "business", record.Source)
}

func TestCodexTurnStateAccountWideSingleFlightAndPause(t *testing.T) {
	s, repo, a := newCodexStateTestService(t)
	ctx := context.Background()
	first := seedCodexStateTestDemand(t, s, a, "gpt-5")
	second := seedCodexStateTestDemand(t, s, a, "gpt-5-mini")
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	var calls atomic.Int64
	s.collector = codexStateTestCollector(func(context.Context, CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error) {
		calls.Add(1)
		close(started)
		<-release
		return CodexTurnStateCollectResult{StatusCode: 401}, nil
	})
	other := NewCodexTurnStateService(repo, s.accounts, s.encryptor, s.collector)
	other.modelPolicy = s.modelPolicy
	other.now = s.now
	go func() { defer close(done); s.collect(ctx, first.key) }()
	<-started
	other.collect(ctx, second.key)
	require.EqualValues(t, 1, calls.Load())
	close(release)
	<-done
	other.collect(ctx, second.key)
	require.EqualValues(t, 1, calls.Load(), "authentication pause applies to all owner models")
}

func TestCodexTurnStateCollectorRequestIsolationAndCompletion(t *testing.T) {
	s, _, a := newCodexStateTestService(t)
	token := codexStateTestToken(10, s.now())
	var sessions []string
	collector := NewCodexTurnStateHTTPCollector(func(_ context.Context, in CodexTurnStateCollectRequest, request *http.Request) (*http.Response, error) {
		require.EqualValues(t, 99, in.ProxyID)
		require.Equal(t, http.MethodPost, request.Method)
		require.Empty(t, request.Header.Get("x-codex-turn-state"))
		sessions = append(sessions, request.Header.Get("session_id"))
		body, _ := io.ReadAll(request.Body)
		require.NotContains(t, string(body), "previous_response_id")
		require.NotContains(t, string(body), "client_metadata")
		require.Contains(t, string(body), "Reply with OK.")
		return &http.Response{StatusCode: 200, Header: http.Header{"X-Codex-Turn-State": {token}}, Body: io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\"}\n\n"))}, nil
	})
	for range 2 {
		result, err := collector.Collect(context.Background(), CodexTurnStateCollectRequest{Account: a, Model: "gpt-5", ProxyID: 99})
		require.NoError(t, err)
		require.Equal(t, []string{token}, result.Tokens)
	}
	require.NotEmpty(t, sessions[0])
	require.NotEqual(t, sessions[0], sessions[1])
	collector.Do = func(context.Context, CodexTurnStateCollectRequest, *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"X-Codex-Turn-State": {token}}, Body: io.NopCloser(strings.NewReader("data: {\"type\":\"error\"}\n\n"))}, nil
	}
	result, err := collector.Collect(context.Background(), CodexTurnStateCollectRequest{Account: a, Model: "gpt-5", ProxyID: 99})
	require.Error(t, err)
	require.Empty(t, result.Tokens)
}
