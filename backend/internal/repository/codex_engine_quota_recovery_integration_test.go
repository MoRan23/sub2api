//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestCodexEngineBillingRecoveryPreservesTotalQuotaAdmissionIntegration(t *testing.T) {
	for _, scenario := range []struct {
		name            string
		appendPermanent bool
		delivered       bool
	}{
		{"expired admission batch", false, true},
		{"permanent credit appended after admission", true, true},
		{"undelivered expired admission batch", false, false},
		{"undelivered with new permanent credit", true, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			ctx := context.Background()
			client := testEntClient(t)
			f := newEngineReceiptGatewayFixture(t)
			mustSeedQuotaRow(t, ctx, NewUserPlatformQuotaRepository(client), f.user.ID, service.PlatformOpenAI)
			group := mustCreateGroup(t, client, &service.Group{
				Name: "engine-quota-recovery-" + uuid.NewString(), Platform: service.PlatformOpenAI,
				SubscriptionType: service.SubscriptionTypeTotalQuota, RateMultiplier: 1,
			})
			f.apiKey.GroupID, f.apiKey.Group = &group.ID, group
			require.NoError(t, client.APIKey.UpdateOneID(f.apiKey.ID).SetGroupID(group.ID).Exec(ctx))
			subs := NewUserSubscriptionRepository(client).(*userSubscriptionRepository)
			now := time.Now().UTC()
			expires := now.Add(time.Hour)
			sub := &service.UserSubscription{
				UserID: f.user.ID, GroupID: group.ID, StartsAt: now.Add(-time.Hour),
				ExpiresAt: &expires, Status: service.SubscriptionStatusActive,
			}
			require.NoError(t, subs.Create(ctx, sub))
			original := &service.UserSubscriptionQuotaEvent{
				UserSubscriptionID: sub.ID, QuotaTotalUSD: .01, StartsAt: sub.StartsAt,
				ExpiresAt: &expires, SourceKind: service.SubscriptionQuotaSourceAdminAssign,
			}
			require.NoError(t, subs.CreateQuotaEvent(ctx, original))
			admission, err := subs.GetQuotaSpendSnapshot(ctx, sub.ID, now)
			require.NoError(t, err)
			require.NotNil(t, admission)
			require.Equal(t, []int64{original.ID}, admission.EventIDs)

			input := f.input("ceu_" + uuid.NewString())
			input.Subscription = sub
			input.Result.Usage = service.OpenAIUsage{InputTokens: 10000, OutputTokens: 10000}
			expected := f.expectedCost(t, input, 1)
			require.Greater(t, expected.ActualCost, original.QuotaTotalUSD, "the late request also exercises the original overflow batch")
			snapshot, err := f.gateway.CaptureCodexEngineBillingSnapshot(service.WithTotalQuotaSpendSnapshot(ctx, admission), input)
			require.NoError(t, err)
			store := NewCodexEngineBillingStore(integrationDB)
			job, err := store.CreatePending(ctx, &service.CodexEngineBillingPending{
				ClientRequestID: uuid.NewString(), AccountID: f.account.ID, UserID: f.user.ID, APIKeyID: f.apiKey.ID,
				IdentityDigest: "digest-" + uuid.NewString(), InstanceID: "cei_" + uuid.NewString(), Snapshot: snapshot,
			})
			require.NoError(t, err)
			jobID := job.ID
			t.Cleanup(func() {
				for _, table := range []string{"codex_engine_billing_received_events", "codex_engine_billing_events"} {
					_, err := integrationDB.Exec("DELETE FROM "+table+" WHERE job_id=$1", jobID)
					require.NoError(t, err)
				}
				_, err := integrationDB.Exec(`DELETE FROM codex_engine_billing_jobs WHERE id=$1`, jobID)
				require.NoError(t, err)
			})

			// The request is still executing when its original credit expires. A new
			// grant belongs to subsequent requests and must never absorb this charge.
			_, err = integrationDB.ExecContext(ctx, `UPDATE user_subscription_quota_events SET expires_at=$2 WHERE id=$1`, original.ID, now.Add(-time.Minute))
			require.NoError(t, err)
			_, err = integrationDB.ExecContext(ctx, `UPDATE user_subscriptions SET expires_at=$2 WHERE id=$1`, sub.ID, now.Add(-time.Minute))
			require.NoError(t, err)
			var appended *service.UserSubscriptionQuotaEvent
			if scenario.appendPermanent {
				appended = &service.UserSubscriptionQuotaEvent{
					UserSubscriptionID: sub.ID, QuotaTotalUSD: 10, StartsAt: now,
					SourceKind: service.SubscriptionQuotaSourceAdminAssign,
				}
				require.NoError(t, subs.CreateQuotaEvent(ctx, appended))
				require.NoError(t, subs.RetireDepletedQuotaEventsOnAppend(ctx, sub.ID, appended.ID, now))
			}

			// Recreate the storage and current pricing dependencies. Recovery still
			// uses the original admitted quota batches from durable metadata.
			worker := &engineBillingDBFixture{store: NewCodexEngineBillingStore(integrationDB)}
			assertEngineSnapshotNotClaimed(t, worker.store, job.ID)
			wantCharge := 0.0
			if scenario.delivered {
				wantCharge = expected.ActualCost
				require.NoError(t, worker.store.(service.CodexEngineBillingDeliveryStore).FinalizeDelivery(ctx, job.ID, service.CodexEngineBillingDelivered))
				job = worker.claim(t, job.ID)
			} else {
				job = claimEngineSnapshotAsLegacyWorker(t, worker.store, job.ID)
			}
			job, err = worker.store.BindReceipt(ctx, job.ID, job.InstanceID, "receipt-"+uuid.NewString(), job.ClaimToken)
			require.NoError(t, err)
			var persisted struct {
				Version                 int  `json:"version"`
				TotalQuotaBilling       bool `json:"total_quota_billing"`
				TotalQuotaSpendSnapshot struct {
					SubscriptionID  int64     `json:"subscription_id"`
					EventIDs        []int64   `json:"event_ids"`
					OverflowEventID int64     `json:"overflow_event_id"`
					TakenAt         time.Time `json:"taken_at"`
				} `json:"total_quota_spend_snapshot"`
			}
			require.NoError(t, json.Unmarshal(job.Snapshot, &persisted))
			require.Equal(t, 4, persisted.Version)
			require.True(t, persisted.TotalQuotaBilling)
			require.Equal(t, admission.SubscriptionID, persisted.TotalQuotaSpendSnapshot.SubscriptionID)
			require.Equal(t, admission.EventIDs, persisted.TotalQuotaSpendSnapshot.EventIDs)
			require.Equal(t, admission.OverflowEventID, persisted.TotalQuotaSpendSnapshot.OverflowEventID)
			require.True(t, admission.TakenAt.Equal(persisted.TotalQuotaSpendSnapshot.TakenAt))
			raw, err := json.Marshal(map[string]any{
				"event_id": "late-model-event", "sequence": 1, "kind": "model", "purpose": "response",
				"model": input.Result.Model, "service_tier": "standard", "occurred_at": now.Format(time.RFC3339Nano),
				"usage_complete": true, "applicable_usage": []string{"input_tokens", "output_tokens", "cached_input_tokens"},
				"usage": map[string]int64{"input_tokens": 10000, "output_tokens": 10000, "cached_input_tokens": 0},
			})
			require.NoError(t, err)
			event, err := service.ParseCodexEngineBillingUsageEvent(raw)
			require.NoError(t, err)
			worker.stage(t, job, service.CodexEngineBillingReceivedEvent{
				EventID: event.EventID, Sequence: event.Sequence, Fingerprint: event.Fingerprint, Raw: event.Raw,
				Summary: service.CodexEngineBillingSummaryGroup{
					Kind: event.Kind, Purpose: event.Purpose, Model: event.Model, ServiceTier: &event.ServiceTier, Usage: event.UsageValues,
				},
			})
			gateway := newEngineCurrentPricingWorker(t, f.cfg)
			if scenario.delivered {
				for range 2 {
					require.NoError(t, gateway.SettleCodexEngineBillingEvent(context.Background(), worker.store, job, &event))
				}
			} else {
				require.NoError(t, worker.store.(service.CodexEngineBillingDeliveryStore).FinalizeDelivery(ctx, job.ID, service.CodexEngineBillingNoCustomerCharge))
				require.ErrorIs(t, gateway.SettleCodexEngineBillingEvent(ctx, worker.store, job, &event), service.ErrCodexEngineBillingClaimLost)
				assertEngineSnapshotDiscarded(t, worker.store, job.ID, f.apiKey.ID)
			}

			var originalUsed, balance, totalUsage float64
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT quota_used_usd FROM user_subscription_quota_events WHERE id=$1`, original.ID).Scan(&originalUsed))
			require.InDelta(t, wantCharge, originalUsed, 1e-8)
			if appended != nil {
				var appendedUsed float64
				require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT quota_used_usd FROM user_subscription_quota_events WHERE id=$1`, appended.ID).Scan(&appendedUsed))
				require.Zero(t, appendedUsed, "new permanent credit is reserved for subsequent requests")
			}
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT balance FROM users WHERE id=$1`, f.user.ID).Scan(&balance))
			require.Equal(t, f.user.Balance, balance)
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT SUM(quota_used_usd) FROM user_subscription_quota_events WHERE user_subscription_id=$1`, sub.ID).Scan(&totalUsage))
			require.InDelta(t, wantCharge, totalUsage, 1e-8)
			if !scenario.delivered {
				assertEngineNoCustomerQuotaDebits(t, ctx, f.user.ID, f.apiKey.ID, f.account.ID)
				return
			}
			var actual, total float64
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT actual_cost, total_cost FROM usage_logs WHERE api_key_id=$1`, f.apiKey.ID).Scan(&actual, &total))
			require.InDelta(t, wantCharge, actual, 1e-8)
			require.InDelta(t, expected.TotalCost, total, 1e-8)
			for _, query := range []string{
				`SELECT COUNT(*) FROM usage_logs WHERE api_key_id=$1`,
				`SELECT COUNT(*) FROM usage_billing_dedup WHERE api_key_id=$1`,
			} {
				var count int
				require.NoError(t, integrationDB.QueryRowContext(ctx, query, f.apiKey.ID).Scan(&count))
				require.Equal(t, 1, count)
			}
			var settled int
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM codex_engine_billing_received_events WHERE job_id=$1 AND settled_at IS NOT NULL`, job.ID).Scan(&settled))
			require.Equal(t, 1, settled)
		})
	}
}
