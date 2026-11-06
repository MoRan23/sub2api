//go:build unit

package handler

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

const engineHandlerBillingInstance = "cei_1234567890abcdef1234567890abcdef"

// Protocol handler tests persist their dispatches but do not run reconciliation:
// independent receipt-event settlement is covered by the billing service tests.
type engineHandlerBillingStore struct {
	service.CodexEngineBillingStore
	mu   sync.Mutex
	jobs map[string]*service.CodexEngineBillingJob
}

func (s *engineHandlerBillingStore) CreatePending(ctx context.Context, pending *service.CodexEngineBillingPending) (*service.CodexEngineBillingJob, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.jobs == nil {
		s.jobs = make(map[string]*service.CodexEngineBillingJob)
	}
	if _, exists := s.jobs[pending.ClientRequestID]; exists {
		return nil, fmt.Errorf("duplicate dispatch correlation")
	}
	job := &service.CodexEngineBillingJob{ID: int64(len(s.jobs) + 1), CodexEngineBillingPending: *pending, State: service.CodexEngineBillingPendingState}
	job.Snapshot = append([]byte(nil), pending.Snapshot...)
	s.jobs[pending.ClientRequestID] = job
	return job, nil
}

func (s *engineHandlerBillingStore) ClaimDue(context.Context, int, time.Duration) ([]*service.CodexEngineBillingJob, error) {
	return nil, nil
}

func (s *engineHandlerBillingStore) MarkNotDispatched(ctx context.Context, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, job := range s.jobs {
		if job.ID == id {
			job.State = service.CodexEngineBillingNotDispatchedState
			return ctx.Err()
		}
	}
	return service.ErrCodexEngineBillingNotFound
}

type engineHandlerBillingUpstream struct {
	service.HTTPUpstream
	t        *testing.T
	store    *engineHandlerBillingStore
	accounts service.AccountRepository
}

func (u *engineHandlerBillingUpstream) Do(req *http.Request, proxyURL string, accountID int64, concurrency int) (*http.Response, error) {
	if req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, "/v1/engine/capacity") {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"object":"codex_engine.capacity","instance_id":"` + engineHandlerBillingInstance + `","capabilities":{"usage_receipts":true,"billing_contract":"confirmed-events"}}`))}, nil
	}
	account, err := u.accounts.GetByID(req.Context(), accountID)
	require.NoError(u.t, err)
	if account.IsCodexEngine() && !strings.HasSuffix(req.URL.Path, "/messages/count_tokens") {
		u.store.mu.Lock()
		job := u.store.jobs[req.Header.Get(service.CodexEngineBillingClientRequestHeader)]
		u.store.mu.Unlock()
		require.NotNil(u.t, job, "physical Engine send requires its own persisted billing correlation")
		require.Equal(u.t, accountID, job.AccountID)
		require.Equal(u.t, int64(99), job.APIKeyID)
		require.Equal(u.t, int64(100), job.UserID)
		require.Equal(u.t, engineHandlerBillingInstance, job.InstanceID)
		require.NotEmpty(u.t, job.Snapshot)
	}
	return u.HTTPUpstream.Do(req, proxyURL, accountID, concurrency)
}

func newEngineBillingHandlerGateway(t *testing.T, accounts service.AccountRepository, usageRepo service.UsageLogRepository, cfg *config.Config, upstream service.HTTPUpstream) *service.OpenAIGatewayService {
	t.Helper()
	store := &engineHandlerBillingStore{}
	transport := &engineHandlerBillingUpstream{HTTPUpstream: upstream, t: t, store: store, accounts: accounts}
	billing := service.NewCodexEngineBillingService(store, accounts, transport, cfg)
	t.Cleanup(billing.Stop)
	return service.ProvideOpenAIGatewayService(
		accounts, usageRepo, nil, nil, nil, nil, nil, cfg, nil, nil,
		service.NewBillingService(cfg, nil), nil, nil, transport, &service.DeferredService{},
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, billing, store, nil,
	)
}
