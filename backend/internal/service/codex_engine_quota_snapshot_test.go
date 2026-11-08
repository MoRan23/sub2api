package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func engineQuotaSnapshotFixture() (*OpenAIGatewayService, *OpenAIRecordUsageInput, *TotalQuotaSpendSnapshot) {
	group := &Group{ID: 4, SubscriptionType: SubscriptionTypeTotalQuota, RateMultiplier: 1}
	in := &OpenAIRecordUsageInput{
		Account: engineReceiptTestAccount(), User: &User{ID: 1},
		APIKey:       &APIKey{ID: 2, GroupID: &group.ID, Group: group},
		Subscription: &UserSubscription{ID: 5}, Result: &OpenAIForwardResult{Model: "gpt-5.4"},
	}
	svc := &OpenAIGatewayService{billingService: &BillingService{fallbackPrices: map[string]*ModelPricing{
		"gpt-5.4": {InputPricePerToken: .1, OutputPricePerToken: .2, CacheReadPricePerToken: .01},
	}}}
	admitted := &TotalQuotaSpendSnapshot{SubscriptionID: 5, EventIDs: []int64{11, 12}, OverflowEventID: 12,
		TakenAt: time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)}
	return svc, in, admitted
}

type engineQuotaSnapshotStore struct {
	engineFrozenStoreStub
	admitted *TotalQuotaSpendSnapshot
}

func (s *engineQuotaSnapshotStore) ApplyEvent(ctx context.Context, e *CodexEngineBillingEvent) (*UsageBillingApplyResult, error) {
	s.admitted, _ = TotalQuotaSpendSnapshotFromContext(ctx)
	return s.engineFrozenStoreStub.ApplyEvent(ctx, e)
}

func TestCodexEngineQuotaSnapshotSurvivesRecoveryAndOwnsAdmission(t *testing.T) {
	svc, in, admitted := engineQuotaSnapshotFixture()
	raw, err := svc.CaptureCodexEngineBillingSnapshot(WithTotalQuotaSpendSnapshot(context.Background(), admitted), in)
	require.NoError(t, err)
	f, err := decodeCodexEnginePricingSnapshot(raw)
	require.NoError(t, err)
	require.Equal(t, 2, f.Version)
	require.True(t, f.TotalQuotaBilling)
	require.Equal(t, admitted.EventIDs, f.TotalQuotaSpendSnapshot.EventIDs)

	// The request ends and its mutable input is reused. Recovery has neither the
	// request context nor a subscription service to reload today's quota batches.
	admitted.EventIDs[0] = 999
	admitted.OverflowEventID = 999
	otherRaw, err := svc.CaptureCodexEngineBillingSnapshot(WithTotalQuotaSpendSnapshot(context.Background(), &TotalQuotaSpendSnapshot{
		SubscriptionID: 5, EventIDs: []int64{21}, OverflowEventID: 21, TakenAt: admitted.TakenAt,
	}), in)
	require.NoError(t, err)
	require.NotEqual(t, string(raw), string(otherRaw))
	job := &CodexEngineBillingJob{ID: 10, ClaimToken: "claim", CodexEngineBillingPending: CodexEngineBillingPending{
		UserID: 1, APIKeyID: 2, AccountID: in.Account.ID, Snapshot: raw,
	}}
	event := engineFrozenTestModelEvent()
	event.Model = "gpt-5.4"
	store := &engineQuotaSnapshotStore{}
	worker := &OpenAIGatewayService{}
	for range 2 {
		// Even an unrelated ambient snapshot must not override the persisted one.
		err = worker.SettleCodexEngineBillingEvent(WithTotalQuotaSpendSnapshot(context.Background(), admitted), store, job, event)
		require.NoError(t, err)
		require.Equal(t, []int64{11, 12}, store.admitted.EventIDs)
		require.Equal(t, int64(12), store.admitted.OverflowEventID)
		require.Equal(t, int64(5), store.admitted.SubscriptionID)
		require.Equal(t, admitted.TakenAt, store.admitted.TakenAt)
	}
	require.Equal(t, 1, store.applied)
	require.Equal(t, int64(5), *store.received.Command.SubscriptionID)
	require.Positive(t, store.received.Command.SubscriptionCost)
	require.Zero(t, store.received.Command.BalanceCost)
}

func TestCodexEngineQuotaSnapshotRejectsMissingOrInvalidAdmission(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*TotalQuotaSpendSnapshot) *TotalQuotaSpendSnapshot
	}{
		{"missing", func(*TotalQuotaSpendSnapshot) *TotalQuotaSpendSnapshot { return nil }},
		{"different subscription", func(q *TotalQuotaSpendSnapshot) *TotalQuotaSpendSnapshot { q.SubscriptionID++; return q }},
		{"empty batches", func(q *TotalQuotaSpendSnapshot) *TotalQuotaSpendSnapshot { q.EventIDs = nil; return q }},
		{"duplicate batches", func(q *TotalQuotaSpendSnapshot) *TotalQuotaSpendSnapshot { q.EventIDs = []int64{12, 12}; return q }},
		{"invalid batch", func(q *TotalQuotaSpendSnapshot) *TotalQuotaSpendSnapshot { q.EventIDs[0] = 0; return q }},
		{"unadmitted overflow", func(q *TotalQuotaSpendSnapshot) *TotalQuotaSpendSnapshot { q.OverflowEventID = 99; return q }},
		{"missing time", func(q *TotalQuotaSpendSnapshot) *TotalQuotaSpendSnapshot { q.TakenAt = time.Time{}; return q }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, in, q := engineQuotaSnapshotFixture()
			raw, err := svc.CaptureCodexEngineBillingSnapshot(WithTotalQuotaSpendSnapshot(context.Background(), tc.mutate(q)), in)
			require.Error(t, err)
			require.Empty(t, raw)
		})
	}
	svc, in, admitted := engineQuotaSnapshotFixture()
	raw, err := svc.CaptureCodexEngineBillingSnapshot(WithTotalQuotaSpendSnapshot(context.Background(), admitted), in)
	require.NoError(t, err)
	f, err := decodeCodexEnginePricingSnapshot(raw)
	require.NoError(t, err)
	f.TotalQuotaSpendSnapshot = nil
	raw, err = json.Marshal(f)
	require.NoError(t, err)
	_, err = decodeCodexEnginePricingSnapshot(raw)
	require.ErrorContains(t, err, "quota spend snapshot unavailable")
}

func TestCodexEngineQuotaSnapshotLegacyJobsRemainReadable(t *testing.T) {
	job, f := engineFrozenTestSnapshot(t)
	f.SubscriptionBilling = true
	id := int64(5)
	f.SubscriptionID = &id
	var err error
	job.Snapshot, err = json.Marshal(f)
	require.NoError(t, err)
	store := &engineQuotaSnapshotStore{}
	ctx := WithTotalQuotaSpendSnapshot(context.Background(), &TotalQuotaSpendSnapshot{SubscriptionID: 99, EventIDs: []int64{100}})
	require.NoError(t, (&OpenAIGatewayService{}).SettleCodexEngineBillingEvent(ctx, store, job, engineFrozenTestModelEvent()))
	require.Nil(t, store.admitted, "legacy jobs must not acquire another request's admission snapshot")
	require.Equal(t, id, *store.received.Command.SubscriptionID)
}
