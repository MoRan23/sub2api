package handler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/runtimeobs"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type frozenUsageBillingRepo struct {
	service.UsageBillingRepository
	err   error
	calls int
}

func (r *frozenUsageBillingRepo) Apply(context.Context, *service.UsageBillingCommand) (*service.UsageBillingApplyResult, error) {
	r.calls++
	return &service.UsageBillingApplyResult{Applied: false}, r.err
}

type frozenUsageLogRepo struct {
	service.UsageLogRepository
}

func (*frozenUsageLogRepo) Create(context.Context, *service.UsageLog) (bool, error) {
	return true, nil
}

func TestFrozenUsageRecordSettlementCounters(t *testing.T) {
	// Run the actual billing services, including their repository error return,
	// through both helpers and all normal/stopped/missing-pool execution modes.
	for _, provider := range []string{"gateway", "openai"} {
		for _, mode := range []string{"queued", "stopped", "missing"} {
			for _, failed := range []bool{false, true} {
				outcome := "success"
				if failed {
					outcome = "error"
				}
				t.Run(provider+"/"+mode+"/"+outcome, func(t *testing.T) {
					var pool *service.UsageRecordWorkerPool
					if mode != "missing" {
						pool = service.NewUsageRecordWorkerPoolWithOptions(service.UsageRecordWorkerPoolOptions{
							WorkerCount: 1, QueueSize: 1, TaskTimeout: time.Second, OverflowPolicy: config.UsageRecordOverflowPolicySync,
						})
						t.Cleanup(pool.Stop)
						if mode == "stopped" {
							pool.Stop()
						}
					}
					repo := &frozenUsageBillingRepo{}
					if failed {
						repo.err = errors.New("settlement fixture failure")
					}
					cfg := &config.Config{}
					cfg.Default.RateMultiplier = 1
					billing := service.NewBillingService(cfg, nil)
					logs, deferred := &frozenUsageLogRepo{}, &service.DeferredService{}
					key, user := &service.APIKey{ID: 1}, &service.User{ID: 2}
					account := &service.Account{ID: 3, Type: service.AccountTypeAPIKey}
					options := usageRecordTaskOptions{component: "settlement_test", failureEvent: "settlement_test.failed"}
					before := runtimeobs.Default.SettlementSnapshot()
					if provider == "gateway" {
						account.Platform = service.PlatformAnthropic
						svc := service.NewGatewayService(
							nil, nil, logs, repo, nil, nil, nil, nil, cfg, nil, nil, billing, nil, nil, nil, nil, deferred,
							nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
						)
						h := &GatewayHandler{gatewayService: svc, usageRecordWorkerPool: pool}
						h.submitFrozenUsageRecord(context.Background(), &service.RecordUsageInput{
							APIKey: key, User: user, Account: account,
							Result: &service.ForwardResult{RequestID: "settlement-test", Model: "claude-sonnet-4-5", Usage: service.ClaudeUsage{InputTokens: 100, OutputTokens: 10}},
						}, options)
					} else {
						account.Platform = service.PlatformOpenAI
						svc := service.NewOpenAIGatewayService(
							nil, logs, repo, nil, nil, nil, nil, cfg, nil, nil, billing, nil, nil, nil, deferred,
							nil, nil, nil, nil, nil, nil, nil,
						)
						h := &OpenAIGatewayHandler{gatewayService: svc, usageRecordWorkerPool: pool}
						h.submitFrozenOpenAIUsageRecord(context.Background(), &service.OpenAIRecordUsageInput{
							APIKey: key, User: user, Account: account,
							Result: &service.OpenAIForwardResult{RequestID: "settlement-test", Model: "gpt-4o", Usage: service.OpenAIUsage{InputTokens: 100, OutputTokens: 10}},
						}, options)
					}
					if pool != nil {
						pool.Stop()
					}
					after := runtimeobs.Default.SettlementSnapshot()
					require.Equal(t, 1, repo.calls)
					if failed {
						require.Equal(t, before.Succeeded, after.Succeeded)
						require.Equal(t, before.Failed+1, after.Failed)
					} else {
						require.Equal(t, before.Succeeded+1, after.Succeeded)
						require.Equal(t, before.Failed, after.Failed)
					}
				})
			}
		}
	}
}

func TestFrozenUsageRecordSettlementCountersExcludeEngineNoop(t *testing.T) {
	h := &OpenAIGatewayHandler{gatewayService: &service.OpenAIGatewayService{}}
	before := runtimeobs.Default.SettlementSnapshot()
	h.submitFrozenOpenAIUsageRecord(context.Background(), &service.OpenAIRecordUsageInput{
		Result: &service.OpenAIForwardResult{},
		Account: &service.Account{Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
			Extra: map[string]any{service.OpenAIAPIKeyModeExtraKey: "codex_engine"}},
	}, usageRecordTaskOptions{})
	require.Equal(t, before, runtimeobs.Default.SettlementSnapshot())
}
