//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func engineBillingV2SnapshotFixture() map[string]any {
	card := map[string]any{
		"resolved": map[string]any{"Mode": service.BillingModeToken, "BasePricing": map[string]any{
			"InputPricePerToken": .1, "OutputPricePerToken": .5, "CacheReadPricePerToken": .02,
		}},
		"stats_catalog":   map[string]any{"InputPricePerToken": .05, "OutputPricePerToken": .25},
		"time_multiplier": 1,
	}
	return map[string]any{
		"version": 2, "pricing_at": "2026-11-06T00:00:00Z",
		"requested_model": "gpt-test-alias", "upstream_model": "gpt-test", "billing_model": "gpt-test-alias",
		"account_type": service.AccountTypeAPIKey, "quota_platform": service.PlatformOpenAI,
		"account_rate": 2, "base_rate": 1, "token_rate": 3, "image_rate": 1,
		"prices": map[string]any{"gpt-test": card, "gpt-test-alias": card},
	}
}

func createEngineBillingSnapshotJob(t *testing.T, f *engineBillingDBFixture, snapshot []byte) *service.CodexEngineBillingJob {
	t.Helper()
	job, err := f.store.CreatePending(context.Background(), &service.CodexEngineBillingPending{
		ClientRequestID: uuid.NewString(), AccountID: f.account.ID, UserID: f.user.ID, APIKeyID: f.key.ID,
		IdentityDigest: "digest-" + uuid.NewString(), InstanceID: f.instance, Snapshot: snapshot,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		for _, table := range []string{"codex_engine_billing_received_events", "codex_engine_billing_events", "codex_engine_billing_jobs"} {
			column := "job_id"
			if table == "codex_engine_billing_jobs" {
				column = "id"
			}
			_, err := integrationDB.Exec("DELETE FROM "+table+" WHERE "+column+"=$1", job.ID)
			require.NoError(t, err)
		}
	})
	return job
}

func TestCodexEngineBillingSnapshotV2JSONBRecoveryIntegration(t *testing.T) {
	ctx := context.Background()
	f := newEngineBillingDBFixture(t)
	snapshot, err := json.Marshal(engineBillingV2SnapshotFixture())
	require.NoError(t, err)
	job := createEngineBillingSnapshotJob(t, f, snapshot)
	var columnType, version string
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`SELECT pg_typeof(snapshot)::text, snapshot->>'version' FROM codex_engine_billing_jobs WHERE id=$1`, job.ID).Scan(&columnType, &version))
	require.Equal(t, "jsonb", columnType)
	require.Equal(t, "2", version)
	require.JSONEq(t, string(snapshot), string(job.Snapshot))

	// A new repository and gateway represent recovery after the original
	// process disappears. No live pricing service is available to this worker.
	f.store = NewCodexEngineBillingStore(integrationDB)
	saved, err := f.store.Get(ctx, job.ID)
	require.NoError(t, err)
	require.JSONEq(t, string(snapshot), string(saved.Snapshot))
	require.NoError(t, f.store.(service.CodexEngineBillingDeliveryStore).FinalizeDelivery(ctx, job.ID, service.CodexEngineBillingDelivered))
	job = f.claim(t, saved.ID)
	job, err = f.store.BindReceipt(ctx, job.ID, f.instance, "receipt-"+uuid.NewString(), job.ClaimToken)
	require.NoError(t, err)
	received := engineBillingDBReceived(1, false)
	f.stage(t, job, received)
	event, err := service.ParseCodexEngineBillingUsageEvent(received.Raw)
	require.NoError(t, err)
	worker := &service.OpenAIGatewayService{}
	cmd, usage, err := worker.BuildCodexEngineBillingEvent(ctx, job, &event)
	require.NoError(t, err)
	// 10 input * .1 + 2 output * .5 = 2; the frozen customer rate is 3.
	require.InDelta(t, 2, usage.TotalCost, 1e-9)
	require.InDelta(t, 6, cmd.BalanceCost, 1e-9)
	require.NotNil(t, usage.AccountStatsCost)
	require.InDelta(t, 1, *usage.AccountStatsCost, 1e-9)
	for range 2 {
		require.NoError(t, worker.SettleCodexEngineBillingEvent(ctx, f.store, job, &event))
	}
	var balance, billedCost float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT balance FROM users WHERE id=$1`, f.user.ID).Scan(&balance))
	require.InDelta(t, 94, balance, 1e-9)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT actual_cost FROM usage_logs WHERE api_key_id=$1`, f.key.ID).Scan(&billedCost))
	require.InDelta(t, 6, billedCost, 1e-9)
	for _, check := range []struct {
		query string
		id    int64
	}{
		{`SELECT COUNT(*) FROM usage_logs WHERE api_key_id=$1`, f.key.ID},
		{`SELECT COUNT(*) FROM usage_billing_dedup WHERE api_key_id=$1`, f.key.ID},
		{`SELECT COUNT(*) FROM codex_engine_billing_events WHERE job_id=$1`, job.ID},
		{`SELECT COUNT(*) FROM codex_engine_billing_received_events WHERE job_id=$1 AND settled_at IS NOT NULL`, job.ID},
	} {
		var count int
		require.NoError(t, integrationDB.QueryRowContext(ctx, check.query, check.id).Scan(&count))
		require.Equal(t, 1, count)
	}
}

func newEngineCurrentPricingWorker(t *testing.T, cfg *config.Config) *service.OpenAIGatewayService {
	t.Helper()
	client := testEntClient(t)
	billing := service.NewBillingService(cfg, nil)
	channels := service.NewChannelService(NewChannelRepository(integrationDB), NewGroupRepository(client, integrationDB), nil, nil, nil)
	return service.NewOpenAIGatewayService(
		nil, nil, nil, nil, nil, nil, nil, cfg, nil, nil, billing, nil, nil, nil,
		nil, nil, nil, service.NewModelPricingResolver(channels, billing), channels, nil, nil, nil,
	)
}

func assertEngineNoCustomerQuotaDebits(t *testing.T, ctx context.Context, userID, keyID, accountID int64) {
	t.Helper()
	var quota, fiveHour, daily, weekly float64
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`SELECT quota_used, usage_5h, usage_1d, usage_7d FROM api_keys WHERE id=$1`, keyID).Scan(&quota, &fiveHour, &daily, &weekly))
	require.Equal(t, []float64{0, 0, 0, 0}, []float64{quota, fiveHour, daily, weekly})
	var platformDaily, platformWeekly, platformMonthly, engineDaily, engineWeekly, engineMonthly float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT daily_usage_usd, weekly_usage_usd, monthly_usage_usd,
		engine_daily_usage_usd, engine_weekly_usage_usd, engine_monthly_usage_usd FROM user_platform_quotas WHERE user_id=$1 AND platform='openai'`, userID).
		Scan(&platformDaily, &platformWeekly, &platformMonthly, &engineDaily, &engineWeekly, &engineMonthly))
	require.Equal(t, []float64{0, 0, 0, 0, 0, 0}, []float64{platformDaily, platformWeekly, platformMonthly, engineDaily, engineWeekly, engineMonthly})
	var accountTotal, accountDaily, accountWeekly float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COALESCE((extra->>'quota_used')::numeric,0),
		COALESCE((extra->>'quota_daily_used')::numeric,0), COALESCE((extra->>'quota_weekly_used')::numeric,0)
		FROM accounts WHERE id=$1`, accountID).Scan(&accountTotal, &accountDaily, &accountWeekly))
	require.Equal(t, []float64{0, 0, 0}, []float64{accountTotal, accountDaily, accountWeekly})
}

func assertEngineSnapshotNotClaimed(t *testing.T, store service.CodexEngineBillingStore, jobID int64) {
	t.Helper()
	jobs, err := store.ClaimDue(context.Background(), 100, time.Minute)
	require.NoError(t, err)
	for _, job := range jobs {
		require.NotEqual(t, jobID, job.ID, "pending delivery and discarded jobs must not be polled")
	}
}

func claimEngineSnapshotAsLegacyWorker(t *testing.T, store service.CodexEngineBillingStore, jobID int64) *service.CodexEngineBillingJob {
	t.Helper()
	// Simulate a worker from before the delivery gate that already claimed a
	// still-running request. New workers must never take this claim themselves.
	_, err := integrationDB.Exec(`UPDATE codex_engine_billing_jobs SET claim_token=$2, lease_until=NOW()+INTERVAL '1 minute' WHERE id=$1`, jobID, uuid.NewString())
	require.NoError(t, err)
	job, err := store.Get(context.Background(), jobID)
	require.NoError(t, err)
	return job
}

func assertEngineSnapshotDiscarded(t *testing.T, store service.CodexEngineBillingStore, jobID, keyID int64) {
	t.Helper()
	saved, err := store.Get(context.Background(), jobID)
	require.NoError(t, err)
	require.Equal(t, service.CodexEngineBillingNoCustomerCharge, saved.DeliveryOutcome)
	require.Empty(t, saved.ClaimToken)
	require.Nil(t, saved.LeaseUntil)
	assertEngineSnapshotNotClaimed(t, store, jobID)
	for _, check := range []struct {
		query string
		id    int64
	}{
		{`SELECT COUNT(*) FROM usage_logs WHERE api_key_id=$1`, keyID},
		{`SELECT COUNT(*) FROM usage_billing_dedup WHERE api_key_id=$1`, keyID},
		{`SELECT COUNT(*) FROM codex_engine_billing_events WHERE job_id=$1`, jobID},
		{`SELECT COUNT(*) FROM codex_engine_billing_received_events WHERE job_id=$1`, jobID},
	} {
		var count int
		require.NoError(t, integrationDB.QueryRow(check.query, check.id).Scan(&count))
		require.Zero(t, count)
	}
}

func TestCodexEngineBillingSnapshotV4CurrentPricingAndDeliveryIntegration(t *testing.T) {
	for _, delivered := range []bool{true, false} {
		t.Run(map[bool]string{true: "delivered", false: "no_customer_charge"}[delivered], func(t *testing.T) {
			ctx := context.Background()
			f := newEngineReceiptGatewayFixture(t)
			client := testEntClient(t)
			inputPrice, outputPrice := .1, .5
			group := mustCreateGroup(t, client, &service.Group{
				Name: "engine-current-price-" + uuid.NewString(), Platform: service.PlatformOpenAI, RateMultiplier: 3,
				ModelPricing: []service.ChannelModelPricing{{Models: []string{"gpt-5.1"}, Platform: service.PlatformOpenAI,
					BillingMode: service.BillingModeToken, InputPrice: &inputPrice, OutputPrice: &outputPrice}},
			})
			f.apiKey.GroupID, f.apiKey.Group = &group.ID, group
			require.NoError(t, client.APIKey.UpdateOneID(f.apiKey.ID).SetGroupID(group.ID).Exec(ctx))
			mustSeedQuotaRow(t, ctx, NewUserPlatformQuotaRepository(client), f.user.ID, service.PlatformOpenAI)
			input := f.input("ceu_" + uuid.NewString())
			snapshot, err := f.gateway.CaptureCodexEngineBillingSnapshot(ctx, input)
			require.NoError(t, err)
			var metadata map[string]any
			require.NoError(t, json.Unmarshal(snapshot, &metadata))
			require.Equal(t, float64(4), metadata["version"])
			for _, field := range []string{"prices", "price_refs", "price_cards", "token_prices"} {
				require.NotContains(t, metadata, field)
			}
			store := &engineBillingDBFixture{store: NewCodexEngineBillingStore(integrationDB), user: f.user, key: f.apiKey, account: f.account, instance: "cei_" + uuid.NewString()}
			job := createEngineBillingSnapshotJob(t, store, snapshot)
			assertEngineSnapshotNotClaimed(t, store.store, job.ID)
			// Current group prices change after dispatch, while the admitted rate
			// and payer remain in the durable request metadata.
			inputPrice, outputPrice = .2, 1
			group.ModelPricing[0].InputPrice, group.ModelPricing[0].OutputPrice = &inputPrice, &outputPrice
			require.NoError(t, NewGroupRepository(client, integrationDB).Update(ctx, group))
			store.store = NewCodexEngineBillingStore(integrationDB)
			if delivered {
				require.NoError(t, store.store.(service.CodexEngineBillingDeliveryStore).FinalizeDelivery(ctx, job.ID, service.CodexEngineBillingDelivered))
				job = store.claim(t, job.ID)
			} else {
				job = claimEngineSnapshotAsLegacyWorker(t, store.store, job.ID)
			}
			job, err = store.store.BindReceipt(ctx, job.ID, store.instance, "receipt-"+uuid.NewString(), job.ClaimToken)
			require.NoError(t, err)
			raw, err := json.Marshal(map[string]any{"event_id": "current-price-event", "sequence": 1, "kind": "model", "purpose": "response",
				"model": "gpt-5.1", "service_tier": "standard", "occurred_at": time.Now().UTC().Format(time.RFC3339Nano),
				"usage_complete": true, "applicable_usage": []string{"input_tokens", "output_tokens", "cached_input_tokens"},
				"usage": map[string]int64{"input_tokens": 10, "output_tokens": 2, "cached_input_tokens": 0}})
			require.NoError(t, err)
			event, err := service.ParseCodexEngineBillingUsageEvent(raw)
			require.NoError(t, err)
			store.stage(t, job, service.CodexEngineBillingReceivedEvent{EventID: event.EventID, Sequence: event.Sequence,
				Fingerprint: event.Fingerprint, Raw: event.Raw, Summary: service.CodexEngineBillingSummaryGroup{
					Kind: event.Kind, Purpose: event.Purpose, Model: event.Model, ServiceTier: &event.ServiceTier, Usage: event.UsageValues}})
			worker := newEngineCurrentPricingWorker(t, f.cfg)
			cmd, usage, err := worker.BuildCodexEngineBillingEvent(ctx, job, &event)
			require.NoError(t, err)
			require.InDelta(t, 4, usage.TotalCost, 1e-9)
			require.InDelta(t, 12, cmd.BalanceCost, 1e-9)
			wantCharge := 12.0
			if !delivered {
				wantCharge = 0
				require.ErrorIs(t, worker.SettleCodexEngineBillingEvent(ctx, store.store, job, &event), service.ErrCodexEngineBillingDeliveryPending)
				for range 2 {
					require.NoError(t, store.store.(service.CodexEngineBillingDeliveryStore).FinalizeDelivery(ctx, job.ID, service.CodexEngineBillingNoCustomerCharge))
				}
				require.ErrorIs(t, worker.SettleCodexEngineBillingEvent(ctx, store.store, job, &event), service.ErrCodexEngineBillingClaimLost)
				assertEngineSnapshotDiscarded(t, store.store, job.ID, f.apiKey.ID)
				assertEngineNoCustomerQuotaDebits(t, ctx, f.user.ID, f.apiKey.ID, f.account.ID)
			} else {
				for range 2 {
					require.NoError(t, worker.SettleCodexEngineBillingEvent(ctx, store.store, job, &event))
				}
			}
			var balance, actual, total float64
			var count int
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT balance FROM users WHERE id=$1`, f.user.ID).Scan(&balance))
			require.InDelta(t, 100-wantCharge, balance, 1e-9)
			if !delivered {
				return
			}
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT actual_cost, total_cost FROM usage_logs WHERE api_key_id=$1`, f.apiKey.ID).Scan(&actual, &total))
			require.InDelta(t, wantCharge, actual, 1e-9)
			require.InDelta(t, 4, total, 1e-9)
			for _, query := range []string{`SELECT COUNT(*) FROM usage_logs WHERE api_key_id=$1`, `SELECT COUNT(*) FROM usage_billing_dedup WHERE api_key_id=$1`} {
				require.NoError(t, integrationDB.QueryRowContext(ctx, query, f.apiKey.ID).Scan(&count))
				require.Equal(t, 1, count)
			}
		})
	}
}

func TestCodexEngineBillingSnapshotRejectsNestedSecretsIntegration(t *testing.T) {
	f := newEngineBillingDBFixture(t)
	for _, field := range []string{"api_key", "Authorization", "body", "prompt"} {
		t.Run(field, func(t *testing.T) {
			wire := engineBillingV2SnapshotFixture()
			wire["prices"].(map[string]any)["gpt-test"].(map[string]any)["nested"] = []any{map[string]any{field: "fixture-private-data"}}
			snapshot, err := json.Marshal(wire)
			require.NoError(t, err)
			clientID := uuid.NewString()
			job, err := f.store.CreatePending(context.Background(), &service.CodexEngineBillingPending{
				ClientRequestID: clientID, AccountID: f.account.ID, UserID: f.user.ID, APIKeyID: f.key.ID,
				IdentityDigest: "digest-" + uuid.NewString(), InstanceID: f.instance, Snapshot: snapshot,
			})
			require.ErrorContains(t, err, "prohibited secret or request field")
			require.Nil(t, job)
			var count int
			require.NoError(t, integrationDB.QueryRow(`SELECT COUNT(*) FROM codex_engine_billing_jobs WHERE client_request_id=$1`, clientID).Scan(&count))
			require.Zero(t, count)
		})
	}
}

func TestCodexEngineBillingSnapshotStorageLimitIntegration(t *testing.T) {
	f := newEngineBillingDBFixture(t)
	wire := engineBillingV2SnapshotFixture()
	wire["model_mapping_chain"] = ""
	raw, err := json.Marshal(wire)
	require.NoError(t, err)
	const storageLimit = 256 * 1024
	wire["model_mapping_chain"] = strings.Repeat("x", storageLimit-len(raw))
	raw, err = json.Marshal(wire)
	require.NoError(t, err)
	require.Len(t, raw, storageLimit)
	job := createEngineBillingSnapshotJob(t, f, raw)
	// PostgreSQL's JSONB display whitespace may exceed the compact input size.
	// The persisted boundary-sized snapshot still has to restore its prices.
	require.Greater(t, len(job.Snapshot), storageLimit)
	event, err := service.ParseCodexEngineBillingUsageEvent(engineBillingDBReceived(1, false).Raw)
	require.NoError(t, err)
	_, usage, err := (&service.OpenAIGatewayService{}).BuildCodexEngineBillingEvent(context.Background(), job, &event)
	require.NoError(t, err)
	require.InDelta(t, 2, usage.TotalCost, 1e-9)
	wire["model_mapping_chain"] = wire["model_mapping_chain"].(string) + "x"
	raw, err = json.Marshal(wire)
	require.NoError(t, err)
	require.Len(t, raw, storageLimit+1)
	rejected, err := f.store.CreatePending(context.Background(), &service.CodexEngineBillingPending{
		ClientRequestID: uuid.NewString(), AccountID: f.account.ID, UserID: f.user.ID, APIKeyID: f.key.ID,
		IdentityDigest: "digest-" + uuid.NewString(), InstanceID: f.instance, Snapshot: raw,
	})
	require.ErrorContains(t, err, "bounded JSON object")
	require.Nil(t, rejected)
}
