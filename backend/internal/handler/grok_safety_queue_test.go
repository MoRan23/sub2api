package handler

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type grokSafetyQueuedSettlement struct {
	command         *service.UsageBillingCommand
	row             *service.UsageLog
	contextErr      error
	deadline        bool
	clientRequestID any
}

type grokSafetyQueueBillingRepo struct {
	service.UsageBillingRepository
	mu       sync.Mutex
	atomic   []grokSafetyQueuedSettlement
	ordinary int
}

func (r *grokSafetyQueueBillingRepo) Apply(_ context.Context, _ *service.UsageBillingCommand) (*service.UsageBillingApplyResult, error) {
	r.mu.Lock()
	r.ordinary++
	r.mu.Unlock()
	return &service.UsageBillingApplyResult{}, nil
}

func (r *grokSafetyQueueBillingRepo) ApplyWithUsageLog(ctx context.Context, cmd *service.UsageBillingCommand, row *service.UsageLog) (*service.UsageBillingApplyResult, error) {
	_, deadline := ctx.Deadline()
	r.mu.Lock()
	r.atomic = append(r.atomic, grokSafetyQueuedSettlement{command: cmd, row: row, contextErr: ctx.Err(), deadline: deadline, clientRequestID: ctx.Value(ctxkey.ClientRequestID)})
	r.mu.Unlock()
	return &service.UsageBillingApplyResult{}, nil
}

func (r *grokSafetyQueueBillingRepo) observations() ([]grokSafetyQueuedSettlement, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]grokSafetyQueuedSettlement(nil), r.atomic...), r.ordinary
}

func newGrokSafetyQueueFixture(t *testing.T, pool *service.UsageRecordWorkerPool) (*OpenAIGatewayHandler, *service.OpenAIRecordUsageInput, *grokSafetyQueueBillingRepo) {
	t.Helper()
	cfg := &config.Config{}
	cfg.Default.RateMultiplier = 1
	repo := &grokSafetyQueueBillingRepo{}
	svc := service.NewOpenAIGatewayService(
		nil, &frozenUsageLogRepo{}, repo, nil, nil, nil, nil, cfg, nil, nil,
		service.NewBillingService(cfg, nil), nil, nil, nil, &service.DeferredService{},
		nil, nil, nil, nil, nil, nil, nil,
	)
	fee, groupID := 0.125, int64(4)
	input := &service.OpenAIRecordUsageInput{
		APIKey: &service.APIKey{ID: 1, GroupID: &groupID, Group: &service.Group{ID: groupID, Platform: service.PlatformGrok,
			RateMultiplier: 1, GrokSafetyFeeEnabled: true, GrokSafetyFee: &fee}},
		User: &service.User{ID: 2}, Account: &service.Account{ID: 3, Platform: service.PlatformGrok, Type: service.AccountTypeAPIKey},
		Result: &service.OpenAIForwardResult{RequestID: "upstream-original", Model: "grok-queue-fixture",
			GrokSafetyRejection: &service.GrokSafetyRejection{CallID: "physical-attempt", Code: service.GrokSafetyRejectionCode, Fee: fee, HTTPStatus: 403}},
	}
	return &OpenAIGatewayHandler{gatewayService: svc, usageRecordWorkerPool: pool}, input, repo
}

func blockedGrokSafetyQueue(t *testing.T, full bool) (*service.UsageRecordWorkerPool, func()) {
	t.Helper()
	pool := service.NewUsageRecordWorkerPoolWithOptions(service.UsageRecordWorkerPoolOptions{
		WorkerCount: 1, QueueSize: 1, TaskTimeout: time.Second,
		OverflowPolicy: config.UsageRecordOverflowPolicyDrop, OverflowSamplePercent: 0, AutoScaleEnabled: false,
	})
	release, running := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(func() { unblock(); pool.Stop() })
	require.False(t, pool.Submit(func(context.Context) { close(running); <-release }).Dropped())
	select {
	case <-running:
	case <-time.After(time.Second):
		t.Fatal("fixture worker did not start")
	}
	if full {
		require.False(t, pool.Submit(func(context.Context) {}).Dropped())
	}
	return pool, unblock
}

func TestGrokSafetyFrozenUsageFullQueueUsesMandatoryFallback(t *testing.T) {
	pool, _ := blockedGrokSafetyQueue(t, true)
	h, input, repo := newGrokSafetyQueueFixture(t, pool)
	parent, cancel := context.WithCancel(context.Background())
	cancel()
	h.submitFrozenOpenAIUsageRecord(parent, input, usageRecordTaskOptions{})
	observed, ordinary := repo.observations()
	require.Len(t, observed, 1, "a full drop-policy pool must synchronously settle a known safety refusal")
	require.Zero(t, ordinary)
	require.NoError(t, observed[0].contextErr)
	require.True(t, observed[0].deadline)
	require.Equal(t, "grok-safety:physical-attempt", observed[0].command.RequestID)
	require.Equal(t, 0.125, observed[0].command.BalanceCost)
}

func TestGrokSafetyFrozenUsageCancellationRetainsIndependentEvidence(t *testing.T) {
	for _, cancelBeforeSubmit := range []bool{false, true} {
		name := "cancel_after_enqueue"
		if cancelBeforeSubmit {
			name = "cancel_before_enqueue"
		}
		t.Run(name, func(t *testing.T) {
			pool, unblock := blockedGrokSafetyQueue(t, false)
			h, input, repo := newGrokSafetyQueueFixture(t, pool)
			parent, cancel := context.WithCancel(context.WithValue(context.Background(), ctxkey.ClientRequestID, "client-trace"))
			defer cancel()
			if cancelBeforeSubmit {
				cancel()
			}
			h.submitFrozenOpenAIUsageRecord(parent, input, usageRecordTaskOptions{})
			cancel()
			input.Result.GrokSafetyRejection.CallID = "mutated-attempt"
			input.Result.GrokSafetyRejection.Fee = 100
			*input.APIKey.Group.GrokSafetyFee = 200
			unblock()
			pool.Stop()
			observed, ordinary := repo.observations()
			require.Len(t, observed, 1)
			require.Zero(t, ordinary)
			require.NoError(t, observed[0].contextErr)
			require.True(t, observed[0].deadline)
			require.Equal(t, "client-trace", observed[0].clientRequestID)
			require.Equal(t, "grok-safety:physical-attempt", observed[0].command.RequestID)
			require.Equal(t, 0.125, observed[0].row.GrokSafetyRejection.Fee)
			require.Equal(t, 0.125, observed[0].command.BalanceCost)
		})
	}
}

func TestGrokSafetyOrdinaryUsageKeepsConfiguredOverflowDrop(t *testing.T) {
	pool, unblock := blockedGrokSafetyQueue(t, true)
	h, input, repo := newGrokSafetyQueueFixture(t, pool)
	input.Result.GrokSafetyRejection = nil
	input.Result.Model = "gpt-4o"
	input.Result.Usage = service.OpenAIUsage{InputTokens: 100, OutputTokens: 10}
	h.submitFrozenOpenAIUsageRecord(context.Background(), input, usageRecordTaskOptions{})
	unblock()
	pool.Stop()
	observed, ordinary := repo.observations()
	require.Empty(t, observed)
	require.Zero(t, ordinary, "ordinary non-media usage must retain the configured overflow drop semantics")
}
