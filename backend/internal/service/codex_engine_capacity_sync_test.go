package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type capacitySyncTestRepo struct {
	mu       sync.Mutex
	accounts []Account
	limits   []int
	models   [][]string
	list     func(context.Context, int64, int) ([]Account, error)
}

func (r *capacitySyncTestRepo) ListCodexEngineCapacityCandidates(ctx context.Context, afterID int64, limit int) ([]Account, error) {
	if r.list != nil {
		return r.list(ctx, afterID, limit)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var result []Account
	for _, account := range r.accounts {
		if account.ID > afterID && len(result) < limit {
			result = append(result, account)
		}
	}
	return result, nil
}

func (r *capacitySyncTestRepo) ApplyCodexEngineCapacity(_ context.Context, _ *Account, limit int) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.limits = append(r.limits, limit)
	return true, nil
}

func (r *capacitySyncTestRepo) ApplyCodexEngineModels(_ context.Context, _ *Account, models []string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.models = append(r.models, models)
	return true, nil
}

type capacitySyncTestUpstream struct {
	HTTPUpstream
	do func(*http.Request, string, int64, int) (*http.Response, error)
}

func (u *capacitySyncTestUpstream) Do(req *http.Request, proxy string, id int64, concurrency int) (*http.Response, error) {
	return u.do(req, proxy, id, concurrency)
}

func capacitySyncTestAccount(id int64) Account {
	return Account{ID: id, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive,
		Schedulable: true, SchedulingRevision: 4, Concurrency: 50,
		Credentials: map[string]any{"base_url": "https://engine.example/prefix/v1/", "api_key": "test-engine-key"},
		Extra:       map[string]any{OpenAIAPIKeyModeExtraKey: "codex_engine"}}
}

func capacitySyncTestDocument(limit int) string {
	return fmt.Sprintf(`{"object":"codex_engine.capacity","concurrency_limit":%d,"available_accounts":2,"observed_at":"2026-11-04T10:00:00Z","refresh_interval_seconds":60}`, limit)
}

func capacitySyncTestResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func TestCodexEngineCapacitySyncRequestAndZero(t *testing.T) {
	account := capacitySyncTestAccount(42)
	proxyID := int64(9)
	account.ProxyID = &proxyID
	account.Proxy = &Proxy{ID: proxyID, Protocol: "http", Host: "proxy.example", Port: 8080}
	repo := &capacitySyncTestRepo{}
	nextLimit := 30
	upstream := &capacitySyncTestUpstream{do: func(req *http.Request, proxy string, id int64, concurrency int) (*http.Response, error) {
		require.Equal(t, http.MethodGet, req.Method)
		require.Equal(t, "https://engine.example/prefix/v1/engine/capacity", req.URL.String())
		require.Equal(t, "Bearer test-engine-key", req.Header.Get("Authorization"))
		require.Equal(t, "application/json", req.Header.Get("Accept"))
		require.True(t, HTTPUpstreamRedirectsDisabled(req.Context()))
		require.Equal(t, HTTPUpstreamProfileOpenAI, HTTPUpstreamProfileFromContext(req.Context()))
		require.Equal(t, "http://proxy.example:8080", proxy)
		require.EqualValues(t, 42, id)
		require.Equal(t, 50, concurrency)
		return capacitySyncTestResponse(200, capacitySyncTestDocument(nextLimit)), nil
	}}
	svc := NewCodexEngineCapacitySyncService(repo, upstream, nil)
	for _, limit := range []int{30, 20, 0, 30} {
		nextLimit = limit
		require.NoError(t, svc.syncAccount(t.Context(), &account))
		// A zero result must still be observed after the account is paused.
		account.Schedulable = limit != 0
	}
	require.Equal(t, []int{30, 20, 0, 30}, repo.limits)
}

func TestCodexEngineCapacitySyncFailurePreservesLastValue(t *testing.T) {
	valid := capacitySyncTestDocument(20)
	tests := []struct {
		name   string
		status int
		body   string
		err    error
	}{
		{"network", 0, "", errors.New("private network failure")},
		{"unauthorized", 401, "{}", nil},
		{"missing_endpoint", 404, "{}", nil},
		{"server_failure", 503, "{}", nil},
		{"redirect", 302, valid, nil},
		{"empty", 200, "{}", nil},
		{"wrong_object", 200, strings.Replace(valid, "codex_engine.capacity", "other.capacity", 1), nil},
		{"negative", 200, capacitySyncTestDocument(-1), nil},
		{"database_integer_overflow", 200, capacitySyncTestDocument(math.MaxInt32 + 1), nil},
		{"null_limit", 200, strings.Replace(valid, `"concurrency_limit":20`, `"concurrency_limit":null`, 1), nil},
		{"missing_limit", 200, strings.Replace(valid, `"concurrency_limit":20,`, "", 1), nil},
		{"fraction", 200, strings.Replace(valid, `"concurrency_limit":20`, `"concurrency_limit":2.5`, 1), nil},
		{"string", 200, strings.Replace(valid, `"concurrency_limit":20`, `"concurrency_limit":"20"`, 1), nil},
		{"trailing_json", 200, valid + "{}", nil},
		{"oversize", 200, valid + strings.Repeat(" ", codexEngineCapacityMaxBody), nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo := &capacitySyncTestRepo{limits: []int{30}}
			upstream := &capacitySyncTestUpstream{do: func(*http.Request, string, int64, int) (*http.Response, error) {
				if test.err != nil {
					return nil, test.err
				}
				return capacitySyncTestResponse(test.status, test.body), nil
			}}
			account := capacitySyncTestAccount(1)
			svc := NewCodexEngineCapacitySyncService(repo, upstream, nil)
			require.Error(t, svc.syncAccount(t.Context(), &account))
			require.Equal(t, []int{30}, repo.limits)
		})
	}
}

func TestCodexEngineCapacitySyncInvalidTransportAndProxy(t *testing.T) {
	for _, response := range []*http.Response{nil, {StatusCode: 200}} {
		repo := &capacitySyncTestRepo{}
		upstream := &capacitySyncTestUpstream{do: func(*http.Request, string, int64, int) (*http.Response, error) {
			return response, nil
		}}
		svc := NewCodexEngineCapacitySyncService(repo, upstream, nil)
		account := capacitySyncTestAccount(1)
		require.Error(t, svc.syncAccount(t.Context(), &account))
		require.Empty(t, repo.limits)
	}
	for _, proxy := range []*Proxy{nil, {ID: 10, Protocol: "http", Host: "wrong.example", Port: 8080}} {
		repo := &capacitySyncTestRepo{}
		upstream := &capacitySyncTestUpstream{do: func(*http.Request, string, int64, int) (*http.Response, error) {
			t.Fatal("missing or mismatched proxy must not fall back to direct")
			return nil, nil
		}}
		svc := NewCodexEngineCapacitySyncService(repo, upstream, nil)
		account := capacitySyncTestAccount(1)
		proxyID := int64(9)
		account.ProxyID, account.Proxy = &proxyID, proxy
		require.Error(t, svc.syncAccount(t.Context(), &account))
		require.Empty(t, repo.limits)
	}
}

func TestCodexEngineCapacitySyncIgnoresOptionalMetadata(t *testing.T) {
	repo := &capacitySyncTestRepo{}
	upstream := &capacitySyncTestUpstream{do: func(*http.Request, string, int64, int) (*http.Response, error) {
		return capacitySyncTestResponse(200, `{"object":"codex_engine.capacity","concurrency_limit":0,"refresh_interval_seconds":120,"future_metadata":{"enabled":true}}`), nil
	}}
	svc := NewCodexEngineCapacitySyncService(repo, upstream, nil)
	account := capacitySyncTestAccount(1)
	require.NoError(t, svc.syncAccount(t.Context(), &account))
	require.Equal(t, []int{0}, repo.limits)
	require.Equal(t, time.Minute, svc.interval)
}

func TestCodexEngineCapacitySyncPolicyAndCanceledResponse(t *testing.T) {
	account := capacitySyncTestAccount(1)
	repo := &capacitySyncTestRepo{}
	var calls int
	upstream := &capacitySyncTestUpstream{do: func(*http.Request, string, int64, int) (*http.Response, error) {
		calls++
		return capacitySyncTestResponse(200, capacitySyncTestDocument(20)), nil
	}}
	svc := NewCodexEngineCapacitySyncService(repo, upstream, nil)
	account.Credentials["base_url"] = "http://engine.example"
	require.Error(t, svc.syncAccount(t.Context(), &account))
	require.Zero(t, calls, "URL policy must run before attaching credentials")
	svc.cfg = &config.Config{}
	svc.cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	require.NoError(t, svc.syncAccount(t.Context(), &account))
	require.Equal(t, 1, calls)
	ctx, cancel := context.WithCancel(t.Context())
	upstream.do = func(*http.Request, string, int64, int) (*http.Response, error) {
		cancel()
		return capacitySyncTestResponse(200, capacitySyncTestDocument(0)), nil
	}
	require.ErrorIs(t, svc.syncAccount(ctx, &account), context.Canceled)
	require.Equal(t, []int{20}, repo.limits)
}

func TestCodexEngineCapacityIdentityRejectsLateConfigurationAndManualPause(t *testing.T) {
	original := capacitySyncTestAccount(1)
	for _, mutate := range []func(*Account){
		func(a *Account) { a.Credentials["base_url"] = "https://replacement.example" },
		func(a *Account) { a.Credentials["api_key"] = "replacement-key" },
		func(a *Account) { a.Platform = PlatformAnthropic },
		func(a *Account) { a.Type = AccountTypeOAuth },
		func(a *Account) { a.Extra[OpenAIAPIKeyModeExtraKey] = "generic" },
		func(a *Account) { p := int64(9); a.ProxyID = &p },
		func(a *Account) { a.SchedulingRevision++ },
		func(a *Account) { a.Schedulable = false },
	} {
		changed := capacitySyncTestAccount(1)
		mutate(&changed)
		require.False(t, CodexEngineCapacityIdentityMatches(&changed, &original))
	}
	require.True(t, CodexEngineCapacityIdentityMatches(&original, &original))
	changed := capacitySyncTestAccount(1)
	changed.Concurrency = 20
	require.True(t, CodexEngineCapacityIdentityMatches(&changed, &original), "in-use slot count is not an identity or capacity gate")
	require.NotContains(t, CodexEngineCapacityIdentity(&original), "test-engine-key")
}

func TestCodexEngineCapacitySyncIndependentAccountsAndStop(t *testing.T) {
	first, second := capacitySyncTestAccount(1), capacitySyncTestAccount(2)
	first.Schedulable = false
	repo := &capacitySyncTestRepo{accounts: []Account{first, second}}
	firstStarted := make(chan struct{})
	var firstCalls, secondCalls atomic.Int32
	upstream := &capacitySyncTestUpstream{do: func(req *http.Request, _ string, id int64, _ int) (*http.Response, error) {
		if id == 1 {
			if firstCalls.Add(1) == 1 {
				close(firstStarted)
			}
			<-req.Context().Done()
			return nil, req.Context().Err()
		}
		secondCalls.Add(1)
		if strings.HasSuffix(req.URL.Path, "/models") {
			return capacitySyncTestResponse(200, `{"object":"list","data":[{"id":"gpt-test"}]}`), nil
		}
		return capacitySyncTestResponse(200, capacitySyncTestDocument(30)), nil
	}}
	svc := NewCodexEngineCapacitySyncService(repo, upstream, nil)
	svc.interval = 10 * time.Millisecond
	svc.timeout = time.Second
	svc.Start()
	svc.Start()
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("paused Engine account was not scanned at startup")
	}
	require.Eventually(t, func() bool { return secondCalls.Load() >= 3 }, time.Second, time.Millisecond)
	require.EqualValues(t, 2, firstCalls.Load(), "slow account has only one pair of requests in flight")
	svc.Stop()
	count := secondCalls.Load()
	svc.Stop()
	svc.Start()
	time.Sleep(30 * time.Millisecond)
	require.Equal(t, count, secondCalls.Load(), "stopped service must not restart or leak requests")
	svc.mu.Lock()
	require.Empty(t, svc.inFlight)
	svc.mu.Unlock()
}

func TestCodexEngineCapacitySyncBoundedPages(t *testing.T) {
	var pages []int64
	repo := &capacitySyncTestRepo{list: func(_ context.Context, after int64, limit int) ([]Account, error) {
		require.Equal(t, 100, limit)
		pages = append(pages, after)
		if after > 0 {
			return nil, nil
		}
		accounts := make([]Account, 100)
		for i := range accounts {
			accounts[i].ID = int64(i + 1)
		}
		return accounts, nil
	}}
	svc := NewCodexEngineCapacitySyncService(repo, &capacitySyncTestUpstream{}, nil)
	svc.scan(t.Context())
	require.Equal(t, []int64{0, 100}, pages)
}

func TestCodexEngineCapacitySyncBoundsRequestsAndCancelsQueue(t *testing.T) {
	repo := &capacitySyncTestRepo{}
	for id := int64(1); id <= 20; id++ {
		repo.accounts = append(repo.accounts, capacitySyncTestAccount(id))
	}
	started := make(chan struct{}, 20)
	var active atomic.Int32
	upstream := &capacitySyncTestUpstream{do: func(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
		active.Add(1)
		defer active.Add(-1)
		started <- struct{}{}
		<-req.Context().Done()
		return nil, req.Context().Err()
	}}
	svc := NewCodexEngineCapacitySyncService(repo, upstream, nil)
	svc.timeout = time.Minute
	svc.Start()
	for range codexEngineCapacityParallelism * 2 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("worker did not start")
		}
	}
	require.EqualValues(t, codexEngineCapacityParallelism*2, active.Load())
	svc.mu.Lock()
	require.Len(t, svc.inFlight, codexEngineCapacityParallelism, "pending goroutines are bounded too")
	svc.mu.Unlock()
	svc.Stop()
	require.Zero(t, active.Load())
	require.Empty(t, svc.inFlight)
	require.Empty(t, started, "queued requests must not execute after cancellation")
}
