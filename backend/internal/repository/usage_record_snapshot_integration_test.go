//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestUsageRecordSnapshotDetachedBillingIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	f := newEngineReceiptGatewayFixture(t)
	// Reuse only fixture construction; this test exclusively exercises ordinary
	// API-key billing, with the corresponding mode persisted in SQL as well.
	f.account.Extra[service.OpenAIAPIKeyModeExtraKey] = "generic"
	f.account.Extra[service.AccountExtraUpstreamRequestIDHeader] = "X-Request-Id"
	_, err := integrationDB.ExecContext(ctx, `UPDATE accounts SET extra = extra || '{"openai_api_key_mode":"generic","upstream_request_id_header":"X-Request-Id"}'::jsonb WHERE id = $1`, f.account.ID)
	require.NoError(t, err)
	require.False(t, f.account.IsCodexEngine())
	f.apiKey.User = f.user
	f.cfg.Billing.InflightReservation = config.InflightReservationConfig{Enabled: true, TTLSeconds: 60}
	client := testEntClient(t)
	rdb := testRedis(t)
	cache := NewBillingCache(rdb)
	users := NewUserRepository(client, integrationDB)
	billingCache := service.NewBillingCacheService(cache, users, nil, NewAPIKeyRepository(client, integrationDB), nil, nil, f.cfg, nil)
	t.Cleanup(billingCache.Stop)
	f.gateway = service.NewOpenAIGatewayService(NewAccountRepository(client, integrationDB, nil),
		NewUsageLogRepository(client, integrationDB), NewUsageBillingRepository(client, integrationDB),
		users, nil, nil, nil, f.cfg, nil, nil, f.billing, nil, billingCache, nil,
		&service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil)
	require.NoError(t, cache.SetUserBalance(ctx, f.user.ID, 100))
	parent, parentCancel := context.WithCancel(context.WithValue(ctx, ctxkey.ClientRequestID, "snapshot-client"))
	defer parentCancel()
	reservation, err := billingCache.ReserveInflight(parent, f.user, nil, nil, 1)
	require.NoError(t, err)
	require.NotNil(t, reservation)
	release := reservation.Acquire()
	t.Cleanup(release)
	original := f.input("")
	original.RequestPayloadHash = "independent-payload-fingerprint"
	original.QuotaPlatform = service.PlatformOpenAI
	expected := f.expectedCost(t, original, 1)
	frozen := service.SnapshotOpenAIRecordUsageInput(original)
	original.Result.Usage.InputTokens, original.Result.Usage.OutputTokens = 99999, 99999
	original.Result.Model = "changed-model"
	original.Result.UpstreamHeaders.Set("X-Request-Id", "changed-header")
	original.User.Balance = 0
	original.APIKey.Quota, original.APIKey.RateLimit5h = 0, 0
	original.Account.Extra["quota_limit"] = 0.0
	original.Account.Extra[service.OpenAIAPIKeyModeExtraKey] = "codex_engine"
	parentCancel()
	reservation.HandlerDone()
	zkey, _ := billingInflightKeys(f.user.ID)
	held, err := rdb.ZCard(ctx, zkey).Result()
	require.NoError(t, err)
	require.EqualValues(t, 1, held)
	workerCtx := context.WithValue(context.Background(), ctxkey.ClientRequestID, "snapshot-client")
	require.NoError(t, f.gateway.RecordUsage(workerCtx, frozen))
	// Cache deduction is visible while the billing reference is still held.
	balance, err := cache.GetUserBalance(ctx, f.user.ID)
	require.NoError(t, err)
	require.InDelta(t, 100-expected.ActualCost, balance, 1e-8)
	release()
	held, err = rdb.ZCard(ctx, zkey).Result()
	require.NoError(t, err)
	require.Zero(t, held)
	assertEngineReceiptGatewayEffects(t, ctx, f, 1, expected.ActualCost, expected.TotalCost)
	row := readEngineReceiptUsageLog(t, ctx, f.apiKey.ID)
	require.Equal(t, "client:snapshot-client", row.requestID)
	require.Equal(t, "upstream-http-first", row.upstreamRequestID.String)
	require.Equal(t, "gpt-5.1", row.requestedModel)
	require.Equal(t, 4375, row.inputTokens)
	require.Equal(t, 162, row.outputTokens)
	require.Equal(t, 1000, row.cacheReadTokens)
	// A second execution of the same owned record retains SQL idempotency.
	require.NoError(t, f.gateway.RecordUsage(workerCtx, service.SnapshotOpenAIRecordUsageInput(frozen)))
	assertEngineReceiptGatewayEffects(t, ctx, f, 1, expected.ActualCost, expected.TotalCost)
	require.Equal(t, row, readEngineReceiptUsageLog(t, ctx, f.apiKey.ID))
}

func TestUsageRecordSnapshotDetachedBillingWaitsForUserRowLockIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	f := newEngineReceiptGatewayFixture(t)
	f.account.Extra[service.OpenAIAPIKeyModeExtraKey] = "generic"
	f.account.Extra[service.AccountExtraUpstreamRequestIDHeader] = "X-Request-Id"
	_, err := integrationDB.ExecContext(ctx, `UPDATE accounts SET extra = extra || '{"openai_api_key_mode":"generic","upstream_request_id_header":"X-Request-Id"}'::jsonb WHERE id = $1`, f.account.ID)
	require.NoError(t, err)
	f.apiKey.User = f.user
	f.cfg.Billing.InflightReservation = config.InflightReservationConfig{Enabled: true, TTLSeconds: 60}
	client, rdb := testEntClient(t), testRedis(t)
	cache := NewBillingCache(rdb)
	users := NewUserRepository(client, integrationDB)
	billingCache := service.NewBillingCacheService(cache, users, nil, NewAPIKeyRepository(client, integrationDB), nil, nil, f.cfg, nil)
	t.Cleanup(billingCache.Stop)
	gateway := service.NewOpenAIGatewayService(NewAccountRepository(client, integrationDB, nil),
		NewUsageLogRepository(client, integrationDB), NewUsageBillingRepository(client, integrationDB),
		users, nil, nil, nil, f.cfg, nil, nil, f.billing, nil, billingCache, nil,
		&service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil)
	userID, keyID, accountID := f.user.ID, f.apiKey.ID, f.account.ID
	require.NoError(t, cache.SetUserBalance(ctx, userID, 100))
	parent, parentCancel := context.WithCancel(context.WithValue(ctx, ctxkey.ClientRequestID, "snapshot-locked-client"))
	defer parentCancel()
	reservation, err := billingCache.ReserveInflight(parent, f.user, nil, nil, 1)
	require.NoError(t, err)
	require.NotNil(t, reservation)
	t.Cleanup(reservation.HandlerDone)
	release := reservation.Acquire()
	t.Cleanup(release)
	original := f.input("")
	original.RequestPayloadHash = "locked-independent-payload-fingerprint"
	original.QuotaPlatform = service.PlatformOpenAI
	expected := f.expectedCost(t, original, 1)
	frozen := service.SnapshotOpenAIRecordUsageInput(original)
	zkey, _ := billingInflightKeys(userID)

	// This transaction owns the exact row consumed by the ordinary balance
	// deduction CTE. Other connections can still read its committed old balance.
	holder, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = holder.Rollback() }()
	var lockedID int64
	require.NoError(t, holder.QueryRowContext(ctx, `SELECT id FROM users WHERE id = $1 FOR UPDATE`, userID).Scan(&lockedID))
	require.Equal(t, userID, lockedID)
	var holderPID int
	require.NoError(t, holder.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&holderPID))

	workerCtx, workerCancel := context.WithTimeout(context.WithValue(context.Background(), ctxkey.ClientRequestID, "snapshot-locked-client"), 20*time.Second)
	type settlementObservation struct {
		err, cacheErr, reservationErr error
		balance                      float64
		heldBeforeRelease            int64
	}
	observed := make(chan settlementObservation, 1)
	workerDone := make(chan struct{})
	t.Cleanup(func() {
		workerCancel()
		_ = holder.Rollback()
		select {
		case <-workerDone:
		case <-time.After(5 * time.Second):
			t.Error("detached billing worker did not exit during cleanup")
		}
	})
	go func(record *service.OpenAIRecordUsageInput) {
		defer close(workerDone)
		// Observe the cache and reservation at the same completion boundary at
		// which the production task releases its acquired billing reference.
		result := func() settlementObservation {
			defer release()
			result := settlementObservation{err: gateway.RecordUsage(workerCtx, record)}
			if result.err == nil {
				result.balance, result.cacheErr = cache.GetUserBalance(workerCtx, userID)
				result.heldBeforeRelease, result.reservationErr = rdb.ZCard(workerCtx, zkey).Result()
			}
			return result
		}()
		observed <- result
	}(frozen)

	waitingOnUserRow := func() bool {
		var waiting bool
		err := integrationDB.QueryRowContext(ctx, `SELECT EXISTS (
			SELECT 1 FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock'
			  AND query LIKE '%WITH target AS (%' AND query LIKE '%UPDATE users AS u%'
			  AND $1 = ANY(pg_blocking_pids(pid))
		)`, holderPID).Scan(&waiting)
		return err == nil && waiting
	}
	require.Eventually(t, waitingOnUserRow, 5*time.Second, 10*time.Millisecond,
		"ordinary RecordUsage must actually wait in the PostgreSQL balance deduction")
	parentCancel()
	reservation.HandlerDone()
	require.ErrorIs(t, parent.Err(), context.Canceled)

	// Clear the former handler graph while SQL is blocked. The fixture retains
	// only identity scalars for later assertions, not any of these source objects.
	*original.Result = service.OpenAIForwardResult{}
	*original.User = service.User{}
	*original.APIKey = service.APIKey{}
	*original.Account = service.Account{}
	*original = service.OpenAIRecordUsageInput{}
	original = nil
	f.user, f.apiKey, f.account = &service.User{ID: userID}, &service.APIKey{ID: keyID}, &service.Account{ID: accountID}
	require.Eventually(t, waitingOnUserRow, 5*time.Second, 10*time.Millisecond,
		"parent cancellation must leave the detached worker waiting on the row lock")
	select {
	case result := <-observed:
		t.Fatalf("billing returned before its SQL lock was released: %v", result.err)
	default:
	}
	held, err := rdb.ZCard(ctx, zkey).Result()
	require.NoError(t, err)
	require.EqualValues(t, 1, held)
	balance, err := cache.GetUserBalance(ctx, userID)
	require.NoError(t, err)
	require.Equal(t, 100.0, balance)
	var persistedBalance float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT balance FROM users WHERE id = $1`, userID).Scan(&persistedBalance))
	require.Equal(t, 100.0, persistedBalance)

	require.NoError(t, holder.Commit())
	var result settlementObservation
	select {
	case result = <-observed:
	case <-ctx.Done():
		t.Fatal("detached billing did not complete after unlocking: ", ctx.Err())
	}
	require.NoError(t, result.err)
	require.NoError(t, result.cacheErr)
	require.NoError(t, result.reservationErr)
	require.InDelta(t, 100-expected.ActualCost, result.balance, 1e-8)
	require.EqualValues(t, 1, result.heldBeforeRelease)
	held, err = rdb.ZCard(ctx, zkey).Result()
	require.NoError(t, err)
	require.Zero(t, held)
	assertEngineReceiptGatewayEffects(t, ctx, f, 1, expected.ActualCost, expected.TotalCost)
	row := readEngineReceiptUsageLog(t, ctx, keyID)
	require.Equal(t, "client:snapshot-locked-client", row.requestID)
	require.Equal(t, "upstream-http-first", row.upstreamRequestID.String)
	require.Equal(t, "gpt-5.1", row.requestedModel)
	require.Equal(t, 4375, row.inputTokens)
	require.Equal(t, 162, row.outputTokens)
	require.Equal(t, 1000, row.cacheReadTokens)
	// The completed record still has the same idempotency identity after the
	// handler graph has disappeared, including the synchronous cache effect.
	require.NoError(t, gateway.RecordUsage(workerCtx, service.SnapshotOpenAIRecordUsageInput(frozen)))
	assertEngineReceiptGatewayEffects(t, ctx, f, 1, expected.ActualCost, expected.TotalCost)
	require.Equal(t, row, readEngineReceiptUsageLog(t, ctx, keyID))
	balance, err = cache.GetUserBalance(ctx, userID)
	require.NoError(t, err)
	require.InDelta(t, 100-expected.ActualCost, balance, 1e-8)
}
