//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func engineBillingTTLUsageEvent(t *testing.T, sequence int64, aggregate int64, pair ...int64) service.CodexEngineBillingUsageEvent {
	t.Helper()
	usage := map[string]int64{"input_tokens": 20, "output_tokens": 2, "cached_input_tokens": 2, "cache_write_input_tokens": aggregate}
	applicable := []string{"input_tokens", "output_tokens", "cached_input_tokens", "cache_write_input_tokens"}
	if len(pair) > 0 {
		require.Len(t, pair, 2)
		usage["cache_write_30m_input_tokens"], usage["cache_write_12h_input_tokens"] = pair[0], pair[1]
		applicable = append(applicable, "cache_write_30m_input_tokens", "cache_write_12h_input_tokens")
	}
	raw, err := json.Marshal(map[string]any{
		"event_id": fmt.Sprintf("ttl-event-%d", sequence), "sequence": sequence,
		"kind": "model", "purpose": "response", "model": "gpt-test", "service_tier": "standard",
		"occurred_at": "2026-11-06T00:00:00Z", "usage_complete": true,
		"applicable_usage": applicable, "usage": usage,
	})
	require.NoError(t, err)
	event, err := service.ParseCodexEngineBillingUsageEvent(raw)
	require.NoError(t, err)
	return event
}

func engineBillingTTLReceived(event service.CodexEngineBillingUsageEvent) service.CodexEngineBillingReceivedEvent {
	return service.CodexEngineBillingReceivedEvent{
		EventID: event.EventID, Sequence: event.Sequence, Fingerprint: event.Fingerprint, Raw: event.Raw,
		Summary: service.CodexEngineBillingSummaryGroup{
			Kind: event.Kind, Purpose: event.Purpose, Model: event.Model, ServiceTier: &event.ServiceTier, Usage: event.UsageValues,
		},
	}
}

func newEngineBillingTTLJob(t *testing.T, f *engineBillingDBFixture) *service.CodexEngineBillingJob {
	t.Helper()
	wire := engineBillingV2SnapshotFixture()
	for _, value := range wire["prices"].(map[string]any) {
		card := value.(map[string]any)
		pricing := card["resolved"].(map[string]any)["BasePricing"].(map[string]any)
		pricing["CacheCreationPricePerToken"] = .3
		pricing["CacheCreation12hPricePerToken"] = .9
	}
	snapshot, err := json.Marshal(wire)
	require.NoError(t, err)
	return createEngineBillingSnapshotJob(t, f, snapshot)
}

func bindEngineBillingTTLReceipt(t *testing.T, f *engineBillingDBFixture, job *service.CodexEngineBillingJob) *service.CodexEngineBillingJob {
	t.Helper()
	job, err := f.store.BindReceipt(context.Background(), job.ID, f.instance, "ttl-receipt-"+uuid.NewString(), job.ClaimToken)
	require.NoError(t, err)
	return job
}

func assertEngineBillingTTLRows(t *testing.T, f *engineBillingDBFixture, jobID int64, want int) {
	t.Helper()
	for _, check := range []struct {
		query string
		id    int64
	}{
		{`SELECT COUNT(*) FROM usage_logs WHERE api_key_id=$1`, f.key.ID},
		{`SELECT COUNT(*) FROM usage_billing_dedup WHERE api_key_id=$1`, f.key.ID},
		{`SELECT COUNT(*) FROM codex_engine_billing_events WHERE job_id=$1`, jobID},
		{`SELECT COUNT(*) FROM codex_engine_billing_received_events WHERE job_id=$1 AND settled_at IS NOT NULL`, jobID},
	} {
		var count int
		require.NoError(t, integrationDB.QueryRow(check.query, check.id).Scan(&count))
		require.Equal(t, want, count, check.query)
	}
}

func TestCodexEngineBillingTTLRecoveryAndIdempotencyIntegration(t *testing.T) {
	ctx := context.Background()
	f := newEngineBillingDBFixture(t)
	job := f.delivered(t, newEngineBillingTTLJob(t, f))
	job = bindEngineBillingTTLReceipt(t, f, f.claim(t, job.ID))
	event := engineBillingTTLUsageEvent(t, 1, 6, 4, 2)
	f.stage(t, job, engineBillingTTLReceived(event))

	// Recover both the frozen prices and authoritative counters from PostgreSQL
	// after replacing the repository; this worker has no live pricing service.
	f.store = NewCodexEngineBillingStore(integrationDB)
	job, err := f.store.Get(ctx, job.ID)
	require.NoError(t, err)
	pending, err := f.store.ReadPendingReceived(ctx, job.ID, job.ClaimToken, 0, 100)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, event.Fingerprint, pending[0].Fingerprint)
	require.Equal(t, event.UsageValues, pending[0].Summary.Usage)
	event, err = service.ParseCodexEngineBillingUsageEvent(pending[0].Raw)
	require.NoError(t, err)
	require.Equal(t, 4, event.Usage.CacheCreation30mInputTokens)
	require.Equal(t, 2, event.Usage.CacheCreation12hInputTokens)
	worker := &service.OpenAIGatewayService{}
	for range 2 {
		require.NoError(t, worker.SettleCodexEngineBillingEvent(ctx, f.store, job, &event))
	}
	assertEngineBillingTTLRows(t, f, job.ID, 1)
	var input, aggregate, short, long, anthropicShort, anthropicLong int
	var writeCost, totalCost, actualCost, balance float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT input_tokens, cache_creation_tokens,
		cache_creation_30m_tokens, cache_creation_12h_tokens, cache_creation_5m_tokens, cache_creation_1h_tokens,
		cache_creation_cost, total_cost, actual_cost FROM usage_logs WHERE api_key_id=$1`, f.key.ID).
		Scan(&input, &aggregate, &short, &long, &anthropicShort, &anthropicLong, &writeCost, &totalCost, &actualCost))
	require.Equal(t, []int{12, 6, 4, 2, 0, 0}, []int{input, aggregate, short, long, anthropicShort, anthropicLong})
	// 12 ordinary input * .1 + 2 output * .5 + 2 cached * .02 + 4 short * .3 + 2 long * .9.
	require.InDelta(t, 3, writeCost, 1e-9)
	require.InDelta(t, 5.24, totalCost, 1e-9)
	require.InDelta(t, 15.72, actualCost, 1e-9)

	changed := engineBillingTTLUsageEvent(t, 1, 6, 3, 3)
	require.NotEqual(t, event.Fingerprint, changed.Fingerprint)
	before, err := f.store.Get(ctx, job.ID)
	require.NoError(t, err)
	require.ErrorIs(t, f.store.StageEvents(ctx, &service.CodexEngineBillingReceivedPage{
		JobID: job.ID, ClaimToken: job.ClaimToken, Cursor: "changed-ttl", Revision: before.Revision + 1,
		ConsumedSequence: 1, Events: []service.CodexEngineBillingReceivedEvent{engineBillingTTLReceived(changed)},
	}), service.ErrCodexEngineBillingConflict)
	require.ErrorIs(t, worker.SettleCodexEngineBillingEvent(ctx, f.store, job, &changed), service.ErrCodexEngineBillingConflict)
	after, err := f.store.Get(ctx, job.ID)
	require.NoError(t, err)
	require.Equal(t, before.Cursor, after.Cursor)
	require.Equal(t, before.Revision, after.Revision)
	groups, err := f.store.ReadSummary(ctx, job.ID, 1)
	require.NoError(t, err)
	require.Len(t, groups, 1)
	require.Equal(t, event.UsageValues, groups[0].Usage)
	assertEngineBillingTTLRows(t, f, job.ID, 1)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT balance FROM users WHERE id=$1`, f.user.ID).Scan(&balance))
	require.InDelta(t, 84.28, balance, 1e-9)
}

func TestCodexEngineBillingTTLMixedSummaryIntegration(t *testing.T) {
	ctx := context.Background()
	f := newEngineBillingDBFixture(t)
	job := f.delivered(t, newEngineBillingTTLJob(t, f))
	job = bindEngineBillingTTLReceipt(t, f, f.claim(t, job.ID))
	events := []service.CodexEngineBillingUsageEvent{
		engineBillingTTLUsageEvent(t, 1, 5),
		engineBillingTTLUsageEvent(t, 2, 0, 0, 0),
		engineBillingTTLUsageEvent(t, 3, 6, 4, 2),
	}
	for _, event := range events {
		f.stage(t, job, engineBillingTTLReceived(event))
	}
	for sequence := int64(1); sequence <= 3; sequence++ {
		groups, err := f.store.ReadSummary(ctx, job.ID, sequence)
		require.NoError(t, err)
		require.Len(t, groups, 1)
		usage := groups[0].Usage
		if sequence == 1 {
			require.EqualValues(t, 5, usage["cache_write_input_tokens"])
			require.NotContains(t, usage, "cache_write_30m_input_tokens")
			require.NotContains(t, usage, "cache_write_12h_input_tokens")
		} else {
			require.Contains(t, usage, "cache_write_30m_input_tokens")
			require.Contains(t, usage, "cache_write_12h_input_tokens")
			if sequence == 2 {
				require.Zero(t, usage["cache_write_30m_input_tokens"])
				require.Zero(t, usage["cache_write_12h_input_tokens"])
			} else {
				require.EqualValues(t, 11, usage["cache_write_input_tokens"])
				require.EqualValues(t, 4, usage["cache_write_30m_input_tokens"])
				require.EqualValues(t, 2, usage["cache_write_12h_input_tokens"])
			}
		}
	}
	worker := &service.OpenAIGatewayService{}
	for i := range events {
		for range 2 {
			require.NoError(t, worker.SettleCodexEngineBillingEvent(ctx, f.store, job, &events[i]))
		}
	}
	assertEngineBillingTTLRows(t, f, job.ID, 3)
	var aggregate, short, long int
	var totalCost, actualCost, balance float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT SUM(cache_creation_tokens), SUM(cache_creation_30m_tokens),
		SUM(cache_creation_12h_tokens), SUM(total_cost), SUM(actual_cost) FROM usage_logs WHERE api_key_id=$1`, f.key.ID).
		Scan(&aggregate, &short, &long, &totalCost, &actualCost))
	require.Equal(t, []int{11, 4, 2}, []int{aggregate, short, long})
	// Aggregate-only writes retain their generic .3 rate; the zero pair adds no writes.
	require.InDelta(t, 3.84+2.84+5.24, totalCost, 1e-9)
	require.InDelta(t, 35.76, actualCost, 1e-9)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT balance FROM users WHERE id=$1`, f.user.ID).Scan(&balance))
	require.InDelta(t, 64.24, balance, 1e-9)
}

func TestCodexEngineBillingTTLDeliveryGateIntegration(t *testing.T) {
	for _, outcome := range []string{service.CodexEngineBillingDelivered, service.CodexEngineBillingNoCustomerCharge} {
		t.Run(string(outcome), func(t *testing.T) {
			ctx := context.Background()
			f := newEngineBillingDBFixture(t)
			mustSeedQuotaRow(t, ctx, NewUserPlatformQuotaRepository(testEntClient(t)), f.user.ID, service.PlatformOpenAI)
			job := newEngineBillingTTLJob(t, f)
			assertEngineSnapshotNotClaimed(t, f.store, job.ID)
			job = bindEngineBillingTTLReceipt(t, f, claimEngineSnapshotAsLegacyWorker(t, f.store, job.ID))
			event := engineBillingTTLUsageEvent(t, 1, 6, 4, 2)
			f.stage(t, job, engineBillingTTLReceived(event))
			worker := &service.OpenAIGatewayService{}
			require.ErrorIs(t, worker.SettleCodexEngineBillingEvent(ctx, f.store, job, &event), service.ErrCodexEngineBillingDeliveryPending)
			assertEngineBillingTTLRows(t, f, job.ID, 0)
			assertEngineNoCustomerQuotaDebits(t, ctx, f.user.ID, f.key.ID, f.account.ID)
			var balance float64
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT balance FROM users WHERE id=$1`, f.user.ID).Scan(&balance))
			require.Equal(t, 100.0, balance)
			for range 2 {
				require.NoError(t, f.store.(service.CodexEngineBillingDeliveryStore).FinalizeDelivery(ctx, job.ID, outcome))
			}
			if outcome == service.CodexEngineBillingNoCustomerCharge {
				require.ErrorIs(t, worker.SettleCodexEngineBillingEvent(ctx, f.store, job, &event), service.ErrCodexEngineBillingClaimLost)
				assertEngineSnapshotDiscarded(t, f.store, job.ID, f.key.ID)
				assertEngineNoCustomerQuotaDebits(t, ctx, f.user.ID, f.key.ID, f.account.ID)
			} else {
				for range 2 {
					require.NoError(t, worker.SettleCodexEngineBillingEvent(ctx, f.store, job, &event))
				}
				assertEngineBillingTTLRows(t, f, job.ID, 1)
			}
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT balance FROM users WHERE id=$1`, f.user.ID).Scan(&balance))
			wantBalance := 100.0
			if outcome == service.CodexEngineBillingDelivered {
				wantBalance = 84.28
			}
			require.InDelta(t, wantBalance, balance, 1e-9)
		})
	}
}
