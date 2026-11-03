//go:build integration

package repository

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// Exercise the production receipt parser, key/fingerprint builders, pricing,
// transactional billing, and usage-log writer together against migrated SQL.
// A hand-built UsageBillingCommand would miss schema constraints on the IDs
// that RecordUsage actually produces.
func TestOpenAIRecordUsage_EngineReceiptPreservesFirstChargeIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	f := newEngineReceiptGatewayFixture(t)
	receipt := "ceu_" + strings.Repeat("A", 43)

	// An accepted receipt without final usage does not claim the transaction.
	partial := f.input(receipt)
	partial.Result.CodexEngineUsageFinalized = false
	require.NoError(t, f.gateway.RecordUsage(ctx, partial))
	assertEngineReceiptGatewayEffects(t, ctx, f, 0, 0, 0)

	// The original delivery was lost. Its final replay is the first billable
	// delivery and must create one charge and one durable usage row.
	first := f.input(receipt)
	first.RequestPayloadHash = "first-observed-replay-after-lost-original"
	firstCost := f.expectedCost(t, first, 1)
	firstCtx := context.WithValue(ctx, ctxkey.ClientRequestID, "first-client")
	require.NoError(t, f.gateway.RecordUsage(firstCtx, first))
	assertEngineReceiptGatewayEffects(t, ctx, f, 1, firstCost.ActualCost, firstCost.TotalCost)
	originalLog := readEngineReceiptUsageLog(t, ctx, f.apiKey.ID)
	require.Equal(t, 4375, originalLog.inputTokens)
	require.Equal(t, 1000, originalLog.cacheReadTokens)
	require.Equal(t, 162, originalLog.outputTokens)
	require.True(t, strings.HasPrefix(originalLog.requestID, "ce:"))

	// Check the real persisted columns rather than assuming the schema permits
	// the generated key. usage_logs.request_id is narrower than the dedup key.
	var requestIDLimit, fingerprintLimit int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT character_maximum_length FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = 'usage_logs' AND column_name = 'request_id'
	`).Scan(&requestIDLimit))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT character_maximum_length FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = 'usage_billing_dedup' AND column_name = 'request_fingerprint'
	`).Scan(&fingerprintLimit))
	require.LessOrEqual(t, len(originalLog.requestID), requestIDLimit)
	var fingerprint string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT request_fingerprint FROM usage_billing_dedup
		WHERE request_id = $1 AND api_key_id = $2
	`, originalLog.requestID, f.apiKey.ID).Scan(&fingerprint))
	require.Len(t, fingerprint, 64)
	require.LessOrEqual(t, len(fingerprint), fingerprintLimit)

	// Recreate all persistence/services to exercise recovery from SQL rather
	// than an in-process claim. A new client ID, body, request-side alias, and
	// pricing multiplier must not replace the first charge or its usage row.
	f.cfg.Default.RateMultiplier = 4
	f.reopen(t)
	replay := f.input(receipt)
	replay.Result.RequestID = "different-upstream-http-request"
	replay.Result.UpstreamHeaders.Set("X-Request-Id", "different-upstream-http-request")
	replay.RequestPayloadHash = "different-client-id-and-retry-body"
	replay.OriginalModel = "a-new-request-side-alias"
	require.Greater(t, f.expectedCost(t, replay, 4).ActualCost, firstCost.ActualCost)
	replayCtx := context.WithValue(ctx, ctxkey.ClientRequestID, "retry-client")
	require.NoError(t, f.gateway.RecordUsage(replayCtx, replay))
	assertEngineReceiptGatewayEffects(t, ctx, f, 1, firstCost.ActualCost, firstCost.TotalCost)
	require.Equal(t, originalLog, readEngineReceiptUsageLog(t, ctx, f.apiKey.ID))

	// The real immutable-usage fingerprint must reject conflicting counters.
	conflict := f.input(receipt)
	conflict.Result.Usage.OutputTokens++
	require.ErrorIs(t, f.gateway.RecordUsage(ctx, conflict), service.ErrUsageBillingRequestConflict)
	assertEngineReceiptGatewayEffects(t, ctx, f, 1, firstCost.ActualCost, firstCost.TotalCost)
	require.Equal(t, originalLog, readEngineReceiptUsageLog(t, ctx, f.apiKey.ID))

	// Identical token counts on a different accepted execution remain billable.
	continuation := f.input("ceu_" + strings.Repeat("B", 42) + "A")
	nextCost := f.expectedCost(t, continuation, 4)
	require.NoError(t, f.gateway.RecordUsage(replayCtx, continuation))
	assertEngineReceiptGatewayEffects(t, ctx, f, 2,
		firstCost.ActualCost+nextCost.ActualCost, firstCost.TotalCost+nextCost.TotalCost)
}

func TestOpenAIRecordUsage_EngineReceiptConcurrentClaimIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	f := newEngineReceiptGatewayFixture(t)
	receipt := "ceu_" + strings.Repeat("A", 43)
	expected := f.expectedCost(t, f.input(receipt), 1)

	const attempts = 8
	outcomes := make(chan error, attempts)
	start := make(chan struct{})
	var workers sync.WaitGroup
	for i := range attempts {
		workers.Add(1)
		go func() {
			defer workers.Done()
			input := f.input(receipt)
			input.RequestPayloadHash = fmt.Sprintf("retry-body-%d", i)
			input.Result.RequestID = fmt.Sprintf("upstream-http-attempt-%d", i)
			clientCtx := context.WithValue(ctx, ctxkey.ClientRequestID, fmt.Sprintf("retry-client-%d", i))
			<-start
			outcomes <- f.gateway.RecordUsage(clientCtx, input)
		}()
	}
	close(start)
	workers.Wait()
	close(outcomes)
	for err := range outcomes {
		require.NoError(t, err)
	}
	assertEngineReceiptGatewayEffects(t, ctx, f, 1, expected.ActualCost, expected.TotalCost)
}

type engineReceiptGatewayFixture struct {
	cfg     *config.Config
	gateway *service.OpenAIGatewayService
	billing *service.BillingService
	keySvc  *service.APIKeyService
	user    *service.User
	apiKey  *service.APIKey
	account *service.Account
}

func newEngineReceiptGatewayFixture(t *testing.T) *engineReceiptGatewayFixture {
	t.Helper()
	client := testEntClient(t)
	f := &engineReceiptGatewayFixture{cfg: &config.Config{}}
	f.cfg.Default.RateMultiplier = 1
	f.user = mustCreateUser(t, client, &service.User{
		Email:   "engine-receipt-" + uuid.NewString() + "@example.com",
		Balance: 100,
	})
	f.apiKey = mustCreateApiKey(t, client, &service.APIKey{
		UserID:      f.user.ID,
		Key:         "sk-engine-receipt-" + uuid.NewString(),
		Name:        "engine-receipt",
		Quota:       100,
		RateLimit5h: 100,
	})
	f.account = mustCreateAccount(t, client, &service.Account{
		Name:     "engine-receipt-" + uuid.NewString(),
		Platform: service.PlatformOpenAI,
		Type:     service.AccountTypeAPIKey,
		Extra: map[string]any{
			"openai_api_key_mode": "codex_engine",
			"quota_limit":         100.0,
		},
	})
	require.True(t, f.account.IsCodexEngine())
	f.reopen(t)
	return f
}

func (f *engineReceiptGatewayFixture) reopen(t *testing.T) {
	t.Helper()
	client := testEntClient(t)
	users := NewUserRepository(client, integrationDB)
	f.billing = service.NewBillingService(f.cfg, nil)
	f.keySvc = service.NewAPIKeyService(NewAPIKeyRepository(client, integrationDB), users, nil, nil, nil, nil, f.cfg)
	f.gateway = service.NewOpenAIGatewayService(
		NewAccountRepository(client, integrationDB, nil),
		NewUsageLogRepository(client, integrationDB),
		NewUsageBillingRepository(client, integrationDB),
		users, nil, nil, nil, f.cfg, nil, nil, f.billing, nil, nil, nil,
		&service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil,
	)
}

func (f *engineReceiptGatewayFixture) input(receipt string) *service.OpenAIRecordUsageInput {
	return &service.OpenAIRecordUsageInput{
		Result: &service.OpenAIForwardResult{
			RequestID: "upstream-http-first",
			Model:     "gpt-5.1",
			Usage: service.OpenAIUsage{
				InputTokens: 5375, OutputTokens: 162, CacheReadInputTokens: 1000,
			},
			UpstreamHeaders: http.Header{
				"X-Codex-Engine-Usage-Id": {receipt},
				"X-Request-Id":            {"upstream-http-first"},
			},
			CodexEngineUsageFinalized: true,
			Duration:                  time.Second,
		},
		APIKey: f.apiKey, User: f.user, Account: f.account, APIKeyService: f.keySvc,
	}
}

func (f *engineReceiptGatewayFixture) expectedCost(t *testing.T, input *service.OpenAIRecordUsageInput, multiplier float64) *service.CostBreakdown {
	t.Helper()
	u := input.Result.Usage
	cost, err := f.billing.CalculateCost(input.Result.Model, service.UsageTokens{
		InputTokens:  u.InputTokens - u.CacheReadInputTokens,
		OutputTokens: u.OutputTokens, CacheReadTokens: u.CacheReadInputTokens,
	}, multiplier)
	require.NoError(t, err)
	require.Greater(t, cost.ActualCost, 0.0)
	return cost
}

type engineReceiptStoredUsage struct {
	id                        int64
	requestID, requestedModel string
	upstreamRequestID         sql.NullString
	inputTokens, outputTokens int
	cacheReadTokens           int
	actualCost, totalCost     float64
}

func readEngineReceiptUsageLog(t *testing.T, ctx context.Context, apiKeyID int64) engineReceiptStoredUsage {
	t.Helper()
	var row engineReceiptStoredUsage
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT id, request_id, requested_model, upstream_request_id, input_tokens,
		       output_tokens, cache_read_tokens, actual_cost, total_cost
		FROM usage_logs WHERE api_key_id = $1
	`, apiKeyID).Scan(&row.id, &row.requestID, &row.requestedModel, &row.upstreamRequestID,
		&row.inputTokens, &row.outputTokens, &row.cacheReadTokens, &row.actualCost, &row.totalCost))
	return row
}

func assertEngineReceiptGatewayEffects(t *testing.T, ctx context.Context, f *engineReceiptGatewayFixture, applied int, charged, upstreamCost float64) {
	t.Helper()
	var balance, keyQuota, usage5h, usage1d, usage7d, accountQuota float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT balance FROM users WHERE id = $1", f.user.ID).Scan(&balance))
	require.InDelta(t, 100-charged, balance, 0.00000001)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT quota_used, usage_5h, usage_1d, usage_7d FROM api_keys WHERE id = $1
	`, f.apiKey.ID).Scan(&keyQuota, &usage5h, &usage1d, &usage7d))
	for _, usage := range []float64{keyQuota, usage5h, usage1d, usage7d} {
		require.InDelta(t, charged, usage, 0.00000001)
	}
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT COALESCE((extra->>'quota_used')::numeric, 0) FROM accounts WHERE id = $1
	`, f.account.ID).Scan(&accountQuota))
	require.InDelta(t, upstreamCost, accountQuota, 0.00000001)
	for _, query := range []string{
		"SELECT COUNT(*) FROM usage_billing_dedup WHERE api_key_id = $1",
		"SELECT COUNT(*) FROM usage_logs WHERE api_key_id = $1",
	} {
		var count int
		require.NoError(t, integrationDB.QueryRowContext(ctx, query, f.apiKey.ID).Scan(&count))
		require.Equal(t, applied, count)
	}
}
