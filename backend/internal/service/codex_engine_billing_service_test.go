package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type engineBillingTestAccounts struct {
	AccountRepository
	get func(context.Context, int64) (*Account, error)
}

func (r *engineBillingTestAccounts) GetByID(ctx context.Context, id int64) (*Account, error) {
	return r.get(ctx, id)
}

type engineBillingTestStore struct {
	CodexEngineBillingStore
	create   func(context.Context, *CodexEngineBillingPending) (*CodexEngineBillingJob, error)
	claim    func(context.Context, int, time.Duration) ([]*CodexEngineBillingJob, error)
	progress func(context.Context, *CodexEngineBillingProgress) error
	bind     func(context.Context, int64, string, string, string) (*CodexEngineBillingJob, error)
	summary  func(context.Context, int64, int64) ([]CodexEngineBillingSummaryGroup, error)
	mu       sync.Mutex
	received map[int64]map[int64]CodexEngineBillingReceivedEvent
	settled  map[int64]map[int64]bool
}

func (s *engineBillingTestStore) CreatePending(ctx context.Context, p *CodexEngineBillingPending) (*CodexEngineBillingJob, error) {
	return s.create(ctx, p)
}
func (s *engineBillingTestStore) ClaimDue(ctx context.Context, n int, d time.Duration) ([]*CodexEngineBillingJob, error) {
	return s.claim(ctx, n, d)
}
func (s *engineBillingTestStore) SaveProgress(ctx context.Context, p *CodexEngineBillingProgress) error {
	return s.progress(ctx, p)
}
func (s *engineBillingTestStore) BindReceipt(ctx context.Context, id int64, instance, receipt, claim string) (*CodexEngineBillingJob, error) {
	return s.bind(ctx, id, instance, receipt, claim)
}
func (s *engineBillingTestStore) ReadSummary(ctx context.Context, id, sequence int64) ([]CodexEngineBillingSummaryGroup, error) {
	if s.summary != nil {
		return s.summary(ctx, id, sequence)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	groups := map[string]*CodexEngineBillingSummaryGroup{}
	for seq, event := range s.received[id] {
		if seq > sequence {
			continue
		}
		g := event.Summary
		tier := ""
		if g.ServiceTier != nil {
			tier = *g.ServiceTier
		}
		key := g.Kind + "/" + g.Purpose + "/" + g.Model + "/" + tier
		if groups[key] == nil {
			groups[key] = &CodexEngineBillingSummaryGroup{Kind: g.Kind, Purpose: g.Purpose, Model: g.Model, ServiceTier: g.ServiceTier, Usage: map[string]int64{}}
		}
		for k, v := range g.Usage {
			groups[key].Usage[k] += v
		}
	}
	out := make([]CodexEngineBillingSummaryGroup, 0, len(groups))
	for _, g := range groups {
		out = append(out, *g)
	}
	return out, nil
}
func (s *engineBillingTestStore) StageEvents(_ context.Context, p *CodexEngineBillingReceivedPage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.received == nil {
		s.received = map[int64]map[int64]CodexEngineBillingReceivedEvent{}
	}
	if s.received[p.JobID] == nil {
		s.received[p.JobID] = map[int64]CodexEngineBillingReceivedEvent{}
	}
	for _, e := range p.Events {
		s.received[p.JobID][e.Sequence] = e
	}
	return nil
}
func (s *engineBillingTestStore) ReadPendingReceived(_ context.Context, id int64, _ string, after int64, limit int) ([]CodexEngineBillingReceivedEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []CodexEngineBillingReceivedEvent{}
	for seq, e := range s.received[id] {
		if seq > after && !s.settled[id][seq] {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Sequence < out[j].Sequence })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
func (s *engineBillingTestStore) markSettled(id int64, e *CodexEngineBillingUsageEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.settled == nil {
		s.settled = map[int64]map[int64]bool{}
	}
	if s.settled[id] == nil {
		s.settled[id] = map[int64]bool{}
	}
	s.settled[id][e.Sequence] = true
}

func TestCodexEngineBillingIdentityOwnerAndOperationalSettings(t *testing.T) {
	a := capacitySyncTestAccount(42)
	identity := CodexEngineBillingIdentity(&a)
	require.Len(t, identity, 64)
	a.Schedulable = false
	a.Concurrency = 0
	a.SchedulingRevision++
	proxyID := int64(3)
	a.ProxyID = &proxyID
	require.Equal(t, identity, CodexEngineBillingIdentity(&a))
	a.Credentials["api_key"] = "new-owner"
	require.NotEqual(t, identity, CodexEngineBillingIdentity(&a))
	a.Credentials["api_key"] = "test-engine-key"
	a.Credentials["base_url"] = "https://other-engine.example/v1"
	require.NotEqual(t, identity, CodexEngineBillingIdentity(&a))
	a.Extra[OpenAIAPIKeyModeExtraKey] = "ordinary"
	require.Empty(t, CodexEngineBillingIdentity(&a))
}

func TestCodexEngineBillingPreparePersistsBeforeDispatch(t *testing.T) {
	a := capacitySyncTestAccount(42)
	pending := &CodexEngineBillingPending{ClientRequestID: "original-request", AccountID: a.ID, IdentityDigest: CodexEngineBillingIdentity(&a)}
	writes := 0
	store := &engineBillingTestStore{create: func(_ context.Context, p *CodexEngineBillingPending) (*CodexEngineBillingJob, error) {
		writes++
		return &CodexEngineBillingJob{CodexEngineBillingPending: *p, ID: 7}, nil
	}}
	accounts := &engineBillingTestAccounts{get: func(context.Context, int64) (*Account, error) { return &a, nil }}
	upstream := &capacitySyncTestUpstream{do: func(*http.Request, string, int64, int) (*http.Response, error) {
		return capacitySyncTestResponse(200, `{"object":"codex_engine.capacity","instance_id":"cei_0123456789abcdef0123456789abcdef","capabilities":{"usage_receipts":true,"billing_contract":"confirmed-events"}}`), nil
	}}
	s := NewCodexEngineBillingService(store, accounts, upstream, nil)
	job, err := s.Prepare(t.Context(), pending)
	require.NoError(t, err)
	require.EqualValues(t, 7, job.ID)
	require.Equal(t, 1, writes)
	a.Credentials["api_key"] = "changed"
	_, err = s.Prepare(t.Context(), pending)
	require.ErrorIs(t, err, ErrCodexEngineBillingConflict)
	require.Equal(t, 1, writes)
	a.Credentials["api_key"] = "test-engine-key"
	store.create = func(context.Context, *CodexEngineBillingPending) (*CodexEngineBillingJob, error) {
		return nil, errors.New("db unavailable")
	}
	_, err = s.Prepare(t.Context(), pending)
	require.ErrorContains(t, err, "db unavailable")
}

func TestCodexEngineBillingRetryDelayBounded(t *testing.T) {
	want := []time.Duration{2, 4, 8, 16, 32, 60, 60}
	for i, w := range want {
		require.Equal(t, w*time.Second, codexEngineBillingRetryDelay(i+1))
	}
	require.Equal(t, 2*time.Second, codexEngineBillingRetryDelay(-1))
}

func billingTestHTTPResponse(status int, body string) *http.Response {
	r := capacitySyncTestResponse(status, body)
	r.Header.Set(CodexEngineBillingContractHeader, CodexEngineBillingContract)
	r.Header.Set(CodexEngineBillingInstanceHeader, billingTestInstance)
	return r
}

func TestCodexEngineBillingReconcilePagesAndAtomicCursor(t *testing.T) {
	a := capacitySyncTestAccount(42)
	job := &CodexEngineBillingJob{CodexEngineBillingPending: CodexEngineBillingPending{AccountID: a.ID, InstanceID: billingTestInstance, ClientRequestID: "client-1", IdentityDigest: CodexEngineBillingIdentity(&a)}, ID: 1, ClaimToken: "claim", State: CodexEngineBillingPendingState}
	var progress *CodexEngineBillingProgress
	settled := map[string]bool{}
	store := &engineBillingTestStore{
		progress: func(_ context.Context, p *CodexEngineBillingProgress) error { copy := *p; progress = &copy; return nil },
		bind: func(_ context.Context, id int64, instance, receipt, claim string) (*CodexEngineBillingJob, error) {
			require.Equal(t, billingTestInstance, instance)
			require.Equal(t, "claim", claim)
			return job, nil
		},
		summary: func(_ context.Context, id, through int64) ([]CodexEngineBillingSummaryGroup, error) {
			require.EqualValues(t, 2, through)
			tier := "standard"
			return []CodexEngineBillingSummaryGroup{{Kind: "model", Purpose: "response", Model: "gpt-6-astra", ServiceTier: &tier, Usage: map[string]int64{"input_tokens": 240, "cached_input_tokens": 160, "output_tokens": 40}}}, nil
		},
	}
	requests := 0
	u := &capacitySyncTestUpstream{do: func(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
		requests++
		require.Equal(t, http.MethodGet, req.Method)
		require.Equal(t, "Bearer test-engine-key", req.Header.Get("Authorization"))
		require.True(t, HTTPUpstreamRedirectsDisabled(req.Context()))
		p := billingTestPage([]map[string]any{billingTestEvent("event1", 1), billingTestEvent("event2", 2)}, true)
		if requests == 1 {
			require.Equal(t, "client-1", req.URL.Query().Get("client_request_id"))
			p["events"] = []map[string]any{billingTestEvent("event1", 1)}
			p["page_through_sequence"] = 1
			p["has_more"] = true
			p["next_cursor"] = "opaque-page-1"
		} else {
			require.True(t, strings.HasSuffix(req.URL.Path, billingTestReceipt))
			require.Equal(t, "opaque-page-1", req.URL.Query().Get("cursor"))
			p["events"] = []map[string]any{billingTestEvent("event2", 2)}
		}
		return billingTestHTTPResponse(200, string(billingJSON(t, p))), nil
	}}
	s := NewCodexEngineBillingService(store, &engineBillingTestAccounts{get: func(context.Context, int64) (*Account, error) { return &a, nil }}, u, nil)
	s.SetSettler(func(_ context.Context, _ *CodexEngineBillingJob, e *CodexEngineBillingUsageEvent) error {
		settled[e.EventID] = true
		store.markSettled(job.ID, e)
		return nil
	})
	s.reconcile(t.Context(), job)
	require.Equal(t, 2, requests)
	require.Len(t, settled, 2)
	require.Equal(t, CodexEngineBillingSealedState, progress.State)
	require.EqualValues(t, 2, progress.ConsumedSequence)
	require.NotEmpty(t, progress.TerminalSeal)
}

func TestCodexEngineBillingUnpricedEventDoesNotBlockReceivingOrLaterSettlement(t *testing.T) {
	a := capacitySyncTestAccount(42)
	job := &CodexEngineBillingJob{CodexEngineBillingPending: CodexEngineBillingPending{AccountID: a.ID, InstanceID: billingTestInstance, ClientRequestID: "client-1", IdentityDigest: CodexEngineBillingIdentity(&a)}, ID: 1, ClaimToken: "claim"}
	var progress *CodexEngineBillingProgress
	store := &engineBillingTestStore{progress: func(_ context.Context, p *CodexEngineBillingProgress) error { progress = p; return nil }, bind: func(context.Context, int64, string, string, string) (*CodexEngineBillingJob, error) { return job, nil }}
	p := billingTestPage([]map[string]any{billingTestEvent("unpriced", 1), billingTestEvent("priced", 2)}, true)
	u := &capacitySyncTestUpstream{do: func(*http.Request, string, int64, int) (*http.Response, error) {
		return billingTestHTTPResponse(200, string(billingJSON(t, p))), nil
	}}
	s := NewCodexEngineBillingService(store, &engineBillingTestAccounts{get: func(context.Context, int64) (*Account, error) { return &a, nil }}, u, nil)
	var seen []string
	s.SetSettler(func(_ context.Context, _ *CodexEngineBillingJob, e *CodexEngineBillingUsageEvent) error {
		seen = append(seen, e.EventID)
		if e.EventID == "unpriced" {
			return errors.New("price missing")
		}
		store.markSettled(job.ID, e)
		return nil
	})
	s.reconcile(t.Context(), job)
	require.Equal(t, []string{"unpriced", "priced"}, seen)
	require.Equal(t, CodexEngineBillingPendingState, progress.State)
	require.EqualValues(t, 2, progress.ConsumedSequence)
	require.NotEmpty(t, progress.Cursor)
	require.NotEmpty(t, progress.SourceSeal)
	require.Empty(t, progress.TerminalSeal)
	require.Equal(t, "event_settlement_pending", progress.LastError)
}

func TestCodexEngineBillingQueryFailuresNeverReplayOrUseStandardUsage(t *testing.T) {
	for _, scenario := range []string{"404", "network", "invalid_contract", "instance_changed", "proxy_unavailable"} {
		t.Run(scenario, func(t *testing.T) {
			a := capacitySyncTestAccount(42)
			job := &CodexEngineBillingJob{CodexEngineBillingPending: CodexEngineBillingPending{AccountID: a.ID, InstanceID: billingTestInstance, ClientRequestID: "client-1", IdentityDigest: CodexEngineBillingIdentity(&a)}, ID: 1, ClaimToken: "claim"}
			var progress *CodexEngineBillingProgress
			store := &engineBillingTestStore{progress: func(_ context.Context, p *CodexEngineBillingProgress) error { progress = p; return nil }}
			calls := 0
			u := &capacitySyncTestUpstream{do: func(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
				calls++
				require.Equal(t, http.MethodGet, req.Method)
				switch scenario {
				case "404":
					return billingTestHTTPResponse(404, `{"error":{"code":"usage_receipt_not_found"}}`), nil
				case "network":
					return nil, errors.New("secret upstream error must not be logged")
				case "invalid_contract":
					return billingTestHTTPResponse(200, `{"usage":{"input_tokens":12345,"output_tokens":10}}`), nil
				}
				r := billingTestHTTPResponse(200, string(billingJSON(t, billingTestPage([]map[string]any{}, true))))
				if scenario == "instance_changed" {
					r.Header.Set(CodexEngineBillingInstanceHeader, "cei_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
				}
				return r, nil
			}}
			if scenario == "proxy_unavailable" {
				id := int64(9)
				a.ProxyID = &id
			}
			s := NewCodexEngineBillingService(store, &engineBillingTestAccounts{get: func(context.Context, int64) (*Account, error) { return &a, nil }}, u, nil)
			s.SetSettler(func(context.Context, *CodexEngineBillingJob, *CodexEngineBillingUsageEvent) error {
				t.Fatal("failure cannot manufacture billable usage")
				return nil
			})
			s.reconcile(t.Context(), job)
			require.NotNil(t, progress)
			require.Empty(t, progress.TerminalSeal)
			require.NotEqual(t, CodexEngineBillingSealedState, progress.State)
			if scenario == "proxy_unavailable" {
				require.Zero(t, calls)
			} else {
				require.Equal(t, 1, calls)
			}
			if scenario == "instance_changed" {
				require.Equal(t, CodexEngineBillingBlockedState, progress.State)
			} else {
				require.Equal(t, CodexEngineBillingPendingState, progress.State)
			}
			require.NotContains(t, fmt.Sprint(progress), "secret upstream")
		})
	}
}

func TestCodexEngineBillingConfirmedPageSurvivesCredentialEdit(t *testing.T) {
	a := capacitySyncTestAccount(42)
	job := &CodexEngineBillingJob{CodexEngineBillingPending: CodexEngineBillingPending{AccountID: a.ID, InstanceID: billingTestInstance, ClientRequestID: "client-1", IdentityDigest: CodexEngineBillingIdentity(&a)}, ID: 1, ClaimToken: "claim"}
	var progress *CodexEngineBillingProgress
	store := &engineBillingTestStore{progress: func(_ context.Context, p *CodexEngineBillingProgress) error { progress = p; return nil }, bind: func(context.Context, int64, string, string, string) (*CodexEngineBillingJob, error) { return job, nil }}
	requests := 0
	u := &capacitySyncTestUpstream{do: func(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
		requests++
		require.Equal(t, "Bearer test-engine-key", req.Header.Get("Authorization"))
		a.Credentials["api_key"] = "changed"
		p := billingTestPage([]map[string]any{billingTestEvent("one", 1), billingTestEvent("two", 2)}, false)
		p["events"] = []map[string]any{billingTestEvent("one", 1)}
		p["page_through_sequence"] = 1
		p["has_more"] = true
		return billingTestHTTPResponse(200, string(billingJSON(t, p))), nil
	}}
	s := NewCodexEngineBillingService(store, &engineBillingTestAccounts{get: func(context.Context, int64) (*Account, error) { return &a, nil }}, u, nil)
	var events []string
	s.SetSettler(func(_ context.Context, _ *CodexEngineBillingJob, event *CodexEngineBillingUsageEvent) error {
		events = append(events, event.EventID)
		store.markSettled(job.ID, event)
		return nil
	})
	s.reconcile(t.Context(), job)
	require.Equal(t, []string{"one"}, events)
	require.Equal(t, 1, requests)
	require.Equal(t, CodexEngineBillingBlockedState, progress.State)
	require.EqualValues(t, 1, progress.ConsumedSequence)
}

func TestCodexEngineBillingSlowAccountDoesNotBlockAnotherOrShutdown(t *testing.T) {
	a := capacitySyncTestAccount(42)
	makeJob := func(id int64) *CodexEngineBillingJob {
		return &CodexEngineBillingJob{CodexEngineBillingPending: CodexEngineBillingPending{AccountID: a.ID, InstanceID: billingTestInstance, ClientRequestID: fmt.Sprintf("client-%d", id), IdentityDigest: CodexEngineBillingIdentity(&a)}, ID: id, ClaimToken: "claim"}
	}
	jobs := []*CodexEngineBillingJob{makeJob(1), makeJob(2)}
	var claimed atomic.Bool
	completed := make(chan int64, 2)
	store := &engineBillingTestStore{
		claim: func(_ context.Context, n int, lease time.Duration) ([]*CodexEngineBillingJob, error) {
			require.GreaterOrEqual(t, n, 2)
			require.Equal(t, time.Minute, lease)
			if claimed.CompareAndSwap(false, true) {
				return jobs, nil
			}
			return nil, nil
		},
		bind: func(_ context.Context, id int64, _ string, _ string, _ string) (*CodexEngineBillingJob, error) {
			return jobs[id-1], nil
		},
		summary:  func(context.Context, int64, int64) ([]CodexEngineBillingSummaryGroup, error) { return nil, nil },
		progress: func(_ context.Context, p *CodexEngineBillingProgress) error { completed <- p.JobID; return nil },
	}
	u := &capacitySyncTestUpstream{do: func(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
		if req.URL.Query().Get("client_request_id") == "client-1" {
			<-req.Context().Done()
			return nil, req.Context().Err()
		}
		p := billingTestPage([]map[string]any{}, true)
		p["client_request_id"] = "client-2"
		return billingTestHTTPResponse(200, string(billingJSON(t, p))), nil
	}}
	s := NewCodexEngineBillingService(store, &engineBillingTestAccounts{get: func(context.Context, int64) (*Account, error) { return &a, nil }}, u, nil)
	s.SetSettler(func(context.Context, *CodexEngineBillingJob, *CodexEngineBillingUsageEvent) error {
		t.Fatal("zero source seal has no charge")
		return nil
	})
	s.Start()
	select {
	case id := <-completed:
		require.EqualValues(t, 2, id)
	case <-time.After(2 * time.Second):
		t.Fatal("slow account blocked independent job")
	}
	done := make(chan struct{})
	go func() { s.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stop did not cancel outstanding query")
	}
}

func TestCodexEngineBillingDiscoverySingleflightAndNoSecretCache(t *testing.T) {
	a := capacitySyncTestAccount(42)
	var queries atomic.Int64
	u := &capacitySyncTestUpstream{do: func(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
		queries.Add(1)
		if !strings.HasSuffix(req.URL.Path, "/v1/engine/capacity") {
			return nil, fmt.Errorf("unexpected discovery route: %s", req.URL.Path)
		}
		return capacitySyncTestResponse(200, `{"object":"codex_engine.capacity","instance_id":"`+billingTestInstance+`","capabilities":{"usage_receipts":true,"billing_contract":"confirmed-events"}}`), nil
	}}
	store := &engineBillingTestStore{create: func(_ context.Context, p *CodexEngineBillingPending) (*CodexEngineBillingJob, error) {
		return &CodexEngineBillingJob{CodexEngineBillingPending: *p, ID: 1}, nil
	}}
	s := NewCodexEngineBillingService(store, &engineBillingTestAccounts{get: func(context.Context, int64) (*Account, error) { return &a, nil }}, u, nil)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			job, err := s.Prepare(t.Context(), &CodexEngineBillingPending{AccountID: a.ID, IdentityDigest: CodexEngineBillingIdentity(&a), ClientRequestID: fmt.Sprintf("client-%d", i)})
			require.NoError(t, err)
			require.Equal(t, billingTestInstance, job.InstanceID)
		}(i)
	}
	wg.Wait()
	require.EqualValues(t, 1, queries.Load())
	s.mu.Lock()
	require.NotContains(t, fmt.Sprint(s.identities), "test-engine-key")
	s.mu.Unlock()
	a.Credentials["api_key"] = "another-owner"
	_, err := s.Prepare(t.Context(), &CodexEngineBillingPending{AccountID: a.ID, IdentityDigest: CodexEngineBillingIdentity(&a), ClientRequestID: "another"})
	require.NoError(t, err)
	require.EqualValues(t, 2, queries.Load())
}

func TestCodexEngineBillingInferenceMissingHeadersRemainRecoverable(t *testing.T) {
	s := NewCodexEngineBillingService(nil, nil, nil, nil)
	job := &CodexEngineBillingJob{CodexEngineBillingPending: CodexEngineBillingPending{InstanceID: billingTestInstance}}
	for _, h := range []http.Header{nil, {}, {CodexEngineBillingContractHeader: []string{CodexEngineBillingContract}}, {CodexEngineBillingInstanceHeader: []string{billingTestInstance}}} {
		require.NoError(t, s.ObserveResponse(job, h))
		require.Error(t, ValidateCodexEngineBillingHeaders(h, job.InstanceID), "receipt GET still requires both authoritative identity headers")
	}
	for _, h := range []http.Header{{CodexEngineBillingContractHeader: []string{"old-contract"}}, {CodexEngineBillingInstanceHeader: []string{"cei_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}, {CodexEngineBillingInstanceHeader: []string{billingTestInstance, billingTestInstance}}, {CodexEngineBillingContractHeader: []string{""}}} {
		require.ErrorIs(t, s.ObserveResponse(job, h), ErrCodexEngineBillingProtocol)
	}
}

func TestCodexEngineBillingUnpricedFirstPageDoesNotStarveLaterPages(t *testing.T) {
	a := capacitySyncTestAccount(42)
	job := &CodexEngineBillingJob{CodexEngineBillingPending: CodexEngineBillingPending{AccountID: a.ID, InstanceID: billingTestInstance, ClientRequestID: "client-1", IdentityDigest: CodexEngineBillingIdentity(&a)}, ID: 1, ClaimToken: "claim"}
	var progress *CodexEngineBillingProgress
	store := &engineBillingTestStore{progress: func(_ context.Context, p *CodexEngineBillingProgress) error { progress = p; return nil }, bind: func(context.Context, int64, string, string, string) (*CodexEngineBillingJob, error) { return job, nil }}
	events := make([]map[string]any, 101)
	for i := range events {
		events[i] = billingTestEvent(fmt.Sprintf("event-%d", i+1), i+1)
	}
	queries := 0
	u := &capacitySyncTestUpstream{do: func(*http.Request, string, int64, int) (*http.Response, error) {
		queries++
		p := billingTestPage(events, true)
		if queries == 1 {
			p["events"] = events[:100]
			p["page_through_sequence"] = 100
			p["has_more"] = true
			p["next_cursor"] = "page-1"
		} else {
			p["events"] = events[100:]
		}
		return billingTestHTTPResponse(200, string(billingJSON(t, p))), nil
	}}
	s := NewCodexEngineBillingService(store, &engineBillingTestAccounts{get: func(context.Context, int64) (*Account, error) { return &a, nil }}, u, nil)
	s.SetSettler(func(_ context.Context, j *CodexEngineBillingJob, e *CodexEngineBillingUsageEvent) error {
		if e.Sequence == 1 {
			return errors.New("price missing")
		}
		store.markSettled(j.ID, e)
		return nil
	})
	s.reconcile(t.Context(), job)
	require.Equal(t, 2, queries)
	require.EqualValues(t, 101, progress.ConsumedSequence)
	require.True(t, store.settled[job.ID][101], "an unpriced event on page one cannot hide page two")
	require.False(t, store.settled[job.ID][1])
	require.NotEmpty(t, progress.SourceSeal)
	require.Empty(t, progress.TerminalSeal)
	require.Equal(t, CodexEngineBillingPendingState, progress.State)
}

func TestCodexEngineBillingLocalInboxRoundRobinSurvivesRestartAndCredentialChange(t *testing.T) {
	a := capacitySyncTestAccount(42)
	job := &CodexEngineBillingJob{CodexEngineBillingPending: CodexEngineBillingPending{AccountID: a.ID, InstanceID: billingTestInstance, IdentityDigest: CodexEngineBillingIdentity(&a)}, ID: 1, ReceiptID: billingTestReceipt, State: CodexEngineBillingBlockedState, ClaimToken: "claim", ConsumedSequence: 402}
	var progress *CodexEngineBillingProgress
	store := &engineBillingTestStore{progress: func(_ context.Context, p *CodexEngineBillingProgress) error { progress = p; return nil }}
	staged := make([]CodexEngineBillingReceivedEvent, 0, 402)
	for i := 1; i <= 402; i++ {
		e, err := ParseCodexEngineBillingUsageEvent(billingJSON(t, billingTestEvent(fmt.Sprintf("event-%d", i), i)))
		require.NoError(t, err)
		staged = append(staged, CodexEngineBillingReceivedEvent{EventID: e.EventID, Sequence: e.Sequence, Fingerprint: e.Fingerprint, Raw: e.Raw})
	}
	require.NoError(t, store.StageEvents(t.Context(), &CodexEngineBillingReceivedPage{JobID: job.ID, Events: staged}))
	// Pretend the source seal and received events were persisted by the old process.
	job.SourceSeal = []byte(`{"last_sequence":402,"sealed_at":"2026-11-06T10:00:00Z","reason":"complete"}`)
	a.Credentials["api_key"] = "new-owner"
	accounts := &engineBillingTestAccounts{get: func(context.Context, int64) (*Account, error) {
		return nil, errors.New("must not reread mutable account to settle old facts")
	}}
	u := &capacitySyncTestUpstream{do: func(*http.Request, string, int64, int) (*http.Response, error) {
		return nil, errors.New("must not query sealed original source")
	}}
	s := NewCodexEngineBillingService(store, accounts, u, nil)
	settle := func(_ context.Context, j *CodexEngineBillingJob, e *CodexEngineBillingUsageEvent) error {
		if e.Sequence <= 401 {
			return errors.New("price missing")
		}
		store.markSettled(j.ID, e)
		return nil
	}
	s.SetSettler(settle)
	s.reconcile(t.Context(), job)
	require.EqualValues(t, 400, progress.SettlementCursor)
	require.False(t, store.settled[job.ID][402])
	require.Equal(t, CodexEngineBillingBlockedState, progress.State)
	// A new process resumes the local cursor rather than retrying the same 400.
	restored := *job
	restored.SettlementCursor = progress.SettlementCursor
	restored.SourceSeal = progress.SourceSeal
	s = NewCodexEngineBillingService(store, accounts, u, nil)
	s.SetSettler(settle)
	s.reconcile(t.Context(), &restored)
	require.True(t, store.settled[job.ID][402])
	require.Zero(t, progress.SettlementCursor)
	require.Empty(t, progress.TerminalSeal)
	// Once pricing becomes available every original fact settles locally, then seals.
	s.SetSettler(func(_ context.Context, j *CodexEngineBillingJob, e *CodexEngineBillingUsageEvent) error {
		store.markSettled(j.ID, e)
		return nil
	})
	s.reconcile(t.Context(), &restored)
	if progress.State != CodexEngineBillingSealedState {
		restored.SettlementCursor = progress.SettlementCursor
		s.reconcile(t.Context(), &restored)
	}
	require.Equal(t, CodexEngineBillingSealedState, progress.State)
	require.NotEmpty(t, progress.TerminalSeal)
}
