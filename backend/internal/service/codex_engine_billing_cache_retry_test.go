package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func (s *engineFrozenStoreStub) AckReceivedSettled(_ context.Context, jobID int64, _, eventID, fingerprint string, sequence int64) error {
	if sequence <= 0 || s.seen[fmt.Sprintf("%d/%s", jobID, eventID)] != fingerprint {
		return ErrCodexEngineBillingConflict
	}
	return nil
}

func (s *engineBillingTestStore) AckReceivedSettled(_ context.Context, jobID int64, _, eventID, fingerprint string, sequence int64) error {
	s.mu.Lock()
	e, ok := s.received[jobID][sequence]
	s.mu.Unlock()
	if !ok || e.EventID != eventID || e.Fingerprint != fingerprint {
		return ErrCodexEngineBillingConflict
	}
	s.markSettled(jobID, &CodexEngineBillingUsageEvent{Sequence: sequence})
	return nil
}

type engineBillingCacheRetryStore struct {
	*engineBillingTestStore
	ledger     *engineFrozenStoreStub
	applyCalls int
	ackCalls   int
	ackErr     error
}

func (s *engineBillingCacheRetryStore) ApplyEvent(ctx context.Context, e *CodexEngineBillingEvent) (*UsageBillingApplyResult, error) {
	s.applyCalls++
	return s.ledger.ApplyEvent(ctx, e)
}

func (s *engineBillingCacheRetryStore) AckReceivedSettled(ctx context.Context, jobID int64, claim, eventID, fingerprint string, sequence int64) error {
	if s.ackErr != nil {
		return s.ackErr
	}
	if err := s.ledger.AckReceivedSettled(ctx, jobID, claim, eventID, fingerprint, sequence); err != nil {
		return err
	}
	if err := s.engineBillingTestStore.AckReceivedSettled(ctx, jobID, claim, eventID, fingerprint, sequence); err != nil {
		return err
	}
	s.ackCalls++
	return nil
}

type engineBillingRetryCache struct {
	BillingCache
	fail  string
	err   error
	calls map[string]int
}

func (c *engineBillingRetryCache) call(operation string) error {
	c.calls[operation]++
	if c.fail == operation {
		return c.err
	}
	return nil
}

func (c *engineBillingRetryCache) InvalidateUserBalance(context.Context, int64) error {
	return c.call("balance")
}

func (c *engineBillingRetryCache) InvalidateAPIKeyRateLimit(context.Context, int64) error {
	return c.call("key rate")
}

func (c *engineBillingRetryCache) InvalidateSubscriptionCache(context.Context, int64, int64) error {
	return c.call("subscription")
}

func (c *engineBillingRetryCache) MergeUserPlatformQuotaEngineUsage(context.Context, int64, string, *UserPlatformQuotaCacheEntry, time.Duration) error {
	return c.call("platform quota")
}

type engineBillingRetryQuotaRepo struct{ UserPlatformQuotaRepository }

func (*engineBillingRetryQuotaRepo) GetByUserPlatform(context.Context, int64, string) (*UserPlatformQuotaRecord, error) {
	return &UserPlatformQuotaRecord{EngineDailyUsageUSD: 20.4}, nil
}

func TestCodexEngineBillingCacheFailureRetriesAfterLedgerCommit(t *testing.T) {
	for _, operation := range []string{"balance", "key rate", "subscription", "platform quota", "ack"} {
		t.Run(operation, func(t *testing.T) {
			ctx := t.Context()
			job, snapshot := engineFrozenTestSnapshot(t)
			job.ReceiptID = billingTestReceipt
			if operation == "subscription" {
				groupID, subscriptionID := int64(10), int64(20)
				snapshot.GroupID, snapshot.SubscriptionID, snapshot.SubscriptionBilling = &groupID, &subscriptionID, true
				var err error
				job.Snapshot, err = json.Marshal(snapshot)
				require.NoError(t, err)
			}
			facts := billingTestEvent("retry-event", 1)
			facts["model"] = "gpt-engine-fixture"
			event, err := ParseCodexEngineBillingUsageEvent(billingJSON(t, facts))
			require.NoError(t, err)
			store := &engineBillingCacheRetryStore{engineBillingTestStore: &engineBillingTestStore{}, ledger: &engineFrozenStoreStub{}}
			require.NoError(t, store.StageEvents(ctx, &CodexEngineBillingReceivedPage{JobID: job.ID, Events: []CodexEngineBillingReceivedEvent{{EventID: event.EventID, Sequence: event.Sequence, Fingerprint: event.Fingerprint, Raw: event.Raw}}}))
			failure := errors.New("transient " + operation + " outage")
			cache := &engineBillingRetryCache{fail: operation, err: failure, calls: map[string]int{}}
			if operation == "ack" {
				store.ackErr = failure
			}
			newWorker := func() *CodexEngineBillingService {
				gateway := &OpenAIGatewayService{billingCacheService: &BillingCacheService{cache: cache, userPlatformQuotaRepo: &engineBillingRetryQuotaRepo{}}}
				worker := NewCodexEngineBillingService(store, nil, nil, nil)
				worker.SetSettler(func(ctx context.Context, job *CodexEngineBillingJob, event *CodexEngineBillingUsageEvent) error {
					return gateway.SettleCodexEngineBillingEvent(ctx, store, job, event)
				})
				return worker
			}
			pending, err := newWorker().settleReceived(ctx, job)
			require.ErrorIs(t, err, failure)
			require.True(t, pending)
			require.Equal(t, 1, store.ledger.applied)
			require.Zero(t, store.ackCalls)
			// A fresh worker replays durable received facts against the same ledger.
			cache.fail, store.ackErr = "", nil
			restarted := newWorker()
			pending, err = restarted.settleReceived(ctx, job)
			require.NoError(t, err)
			require.False(t, pending)
			require.Equal(t, 2, store.applyCalls)
			require.Equal(t, 1, store.ledger.applied, "cache retry must never apply another monetary delta")
			require.Equal(t, 1, store.ackCalls)
			if operation != "ack" {
				require.Equal(t, 2, cache.calls[operation])
			}
			pending, err = restarted.settleReceived(ctx, job)
			require.NoError(t, err)
			require.False(t, pending)
			require.Equal(t, 2, store.applyCalls, "acknowledgement terminates the durable retry")
		})
	}
}
