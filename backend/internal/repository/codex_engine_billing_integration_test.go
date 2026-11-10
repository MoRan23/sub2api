//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type engineBillingDBFixture struct {
	store    service.CodexEngineBillingStore
	user     *service.User
	key      *service.APIKey
	account  *service.Account
	instance string
}

func newEngineBillingDBFixture(t *testing.T) *engineBillingDBFixture {
	t.Helper()
	client := testEntClient(t)
	f := &engineBillingDBFixture{store: NewCodexEngineBillingStore(integrationDB), instance: "cei_" + strings.ReplaceAll(uuid.NewString(), "-", "")}
	f.user = mustCreateUser(t, client, &service.User{Email: "engine-billing-" + uuid.NewString() + "@example.com", Balance: 100})
	f.key = mustCreateApiKey(t, client, &service.APIKey{UserID: f.user.ID, Key: "sk-test-" + uuid.NewString(), Name: "engine-billing", Quota: 100})
	f.account = mustCreateAccount(t, client, &service.Account{Name: "engine-billing-" + uuid.NewString(), Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Extra: map[string]any{"openai_api_key_mode": "codex_engine"}})
	return f
}

func (f *engineBillingDBFixture) pending(t *testing.T) *service.CodexEngineBillingJob {
	t.Helper()
	j, err := f.store.CreatePending(context.Background(), &service.CodexEngineBillingPending{ClientRequestID: uuid.NewString(), AccountID: f.account.ID, UserID: f.user.ID, APIKeyID: f.key.ID, IdentityDigest: "digest-" + uuid.NewString(), InstanceID: f.instance, Snapshot: []byte(`{"version":1,"rate":1}`)})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := integrationDB.Exec(`DELETE FROM codex_engine_billing_received_events WHERE job_id=$1`, j.ID)
		require.NoError(t, err)
		_, err = integrationDB.Exec(`DELETE FROM codex_engine_billing_events WHERE job_id=$1`, j.ID)
		require.NoError(t, err)
		_, err = integrationDB.Exec(`DELETE FROM codex_engine_billing_jobs WHERE id=$1`, j.ID)
		require.NoError(t, err)
	})
	return j
}

func (f *engineBillingDBFixture) delivered(t *testing.T, j *service.CodexEngineBillingJob) *service.CodexEngineBillingJob {
	t.Helper()
	store, ok := f.store.(service.CodexEngineBillingDeliveryStore)
	require.True(t, ok)
	require.NoError(t, store.FinalizeDelivery(context.Background(), j.ID, service.CodexEngineBillingDelivered))
	j.DeliveryOutcome = service.CodexEngineBillingDelivered
	return j
}

func (f *engineBillingDBFixture) claim(t *testing.T, id int64) *service.CodexEngineBillingJob {
	t.Helper()
	jobs, err := f.store.ClaimDue(context.Background(), 100, time.Minute)
	require.NoError(t, err)
	for _, j := range jobs {
		if j.ID == id {
			return j
		}
	}
	t.Fatalf("job %d not claimed", id)
	return nil
}

func engineBillingDBEvent(j *service.CodexEngineBillingJob, sequence int64) *service.CodexEngineBillingEvent {
	received := engineBillingDBReceived(sequence, false)
	return &service.CodexEngineBillingEvent{JobID: j.ID, ClaimToken: j.ClaimToken, EventID: fmt.Sprintf("event-%d", sequence), Sequence: sequence, Fingerprint: received.Fingerprint,
		Summary:  received.Summary,
		Command:  service.UsageBillingCommand{Model: "gpt-test", InputTokens: 10, OutputTokens: 2, BalanceCost: 2},
		UsageLog: service.UsageLog{Model: "gpt-test", InputTokens: 10, OutputTokens: 2, InputCost: 1, OutputCost: 1, TotalCost: 2, ActualCost: 2, RateMultiplier: 1}}
}

func engineBillingDBReceived(sequence int64, partial bool) service.CodexEngineBillingReceivedEvent {
	u := map[string]int64{"input_tokens": 10, "output_tokens": 2, "cached_input_tokens": 0}
	if partial {
		delete(u, "output_tokens")
	}
	raw, _ := json.Marshal(map[string]any{"event_id": fmt.Sprintf("event-%d", sequence), "sequence": sequence, "kind": "model", "purpose": "response", "model": "gpt-test", "service_tier": "standard", "occurred_at": "2026-11-06T00:00:00Z", "usage_complete": !partial, "applicable_usage": []string{"input_tokens", "output_tokens", "cached_input_tokens"}, "usage": u})
	e, err := service.ParseCodexEngineBillingUsageEvent(raw)
	if err != nil {
		panic(err)
	}
	tier := "standard"
	return service.CodexEngineBillingReceivedEvent{EventID: e.EventID, Sequence: e.Sequence, Fingerprint: e.Fingerprint, Raw: e.Raw, Summary: service.CodexEngineBillingSummaryGroup{Kind: e.Kind, Purpose: e.Purpose, Model: e.Model, ServiceTier: &tier, Usage: e.UsageValues}}
}

func (f *engineBillingDBFixture) stage(t *testing.T, j *service.CodexEngineBillingJob, events ...service.CodexEngineBillingReceivedEvent) {
	t.Helper()
	current, err := f.store.Get(context.Background(), j.ID)
	require.NoError(t, err)
	through := current.ConsumedSequence
	if len(events) > 0 {
		through = max(through, events[len(events)-1].Sequence)
	}
	require.NoError(t, f.store.StageEvents(context.Background(), &service.CodexEngineBillingReceivedPage{JobID: j.ID, ClaimToken: j.ClaimToken, Cursor: fmt.Sprintf("cursor-%d", through), Revision: current.Revision + 1, ConsumedSequence: through, Events: events}))
}

func (f *engineBillingDBFixture) ack(t *testing.T, j *service.CodexEngineBillingJob, sequence int64) {
	t.Helper()
	e := engineBillingDBEvent(j, sequence)
	require.NoError(t, f.store.AckReceivedSettled(context.Background(), j.ID, j.ClaimToken, e.EventID, e.Fingerprint, e.Sequence))
}

func TestCodexEngineBillingStoreAtomicRecoveryIntegration(t *testing.T) {
	f := newEngineBillingDBFixture(t)
	ctx := context.Background()
	j := f.delivered(t, f.pending(t))
	j = f.claim(t, j.ID)
	var err error
	j, err = f.store.BindReceipt(ctx, j.ID, f.instance, "receipt-original", j.ClaimToken)
	require.NoError(t, err)
	f.stage(t, j, engineBillingDBReceived(1, false))
	first := engineBillingDBEvent(j, 1)
	require.ErrorIs(t, f.store.AckReceivedSettled(ctx, j.ID, j.ClaimToken, first.EventID, first.Fingerprint, first.Sequence), service.ErrCodexEngineBillingConflict, "receiving an event cannot acknowledge an absent ledger")
	out, err := f.store.ApplyEvent(ctx, first)
	require.NoError(t, err)
	require.True(t, out.Applied)
	require.ErrorIs(t, f.store.AckReceivedSettled(ctx, j.ID, j.ClaimToken, first.EventID, strings.Repeat("b", 64), first.Sequence), service.ErrCodexEngineBillingConflict, "acknowledgement must match the committed event facts")
	var balance float64
	require.NoError(t, integrationDB.QueryRow(`SELECT balance FROM users WHERE id=$1`, f.user.ID).Scan(&balance))
	require.Equal(t, 98.0, balance)
	var count int
	require.NoError(t, integrationDB.QueryRow(`SELECT COUNT(*) FROM usage_logs WHERE request_id=$1 AND api_key_id=$2`, first.Command.RequestID, f.key.ID).Scan(&count))
	require.Equal(t, 1, count)
	// Repeated startup migrations must preserve an in-flight receipt whose
	// ledger committed but whose cache effects have not yet been acknowledged.
	checkpoint, err := f.store.Get(ctx, j.ID)
	require.NoError(t, err)
	checkpointFacts, err := f.store.ReadPendingReceived(ctx, j.ID, j.ClaimToken, 0, 100)
	require.NoError(t, err)
	require.Len(t, checkpointFacts, 1)
	for range 2 {
		require.NoError(t, ApplyMigrations(ctx, integrationDB))
	}
	afterStartup, err := f.store.Get(ctx, j.ID)
	require.NoError(t, err)
	require.Equal(t, checkpoint, afterStartup)
	afterStartupFacts, err := f.store.ReadPendingReceived(ctx, j.ID, j.ClaimToken, 0, 100)
	require.NoError(t, err)
	require.Equal(t, checkpointFacts, afterStartupFacts)
	require.NoError(t, integrationDB.QueryRow(`SELECT COUNT(*) FROM codex_engine_billing_events
		WHERE job_id=$1 AND event_id=$2 AND fingerprint=$3 AND sequence=$4 AND billing_request_id=$5 AND usage_log_id=$6`,
		j.ID, first.EventID, first.Fingerprint, first.Sequence, first.Command.RequestID, first.UsageLog.ID).Scan(&count))
	require.Equal(t, 1, count, "startup preserves the exact committed event and usage-log link")
	require.NoError(t, integrationDB.QueryRow(`SELECT balance FROM users WHERE id=$1`, f.user.ID).Scan(&balance))
	require.Equal(t, 98.0, balance)

	// The service/process disappeared after ledger commit but before cache ack.
	_, err = integrationDB.Exec(`UPDATE codex_engine_billing_jobs SET lease_until=NOW()-INTERVAL '1 second' WHERE id=$1`, j.ID)
	require.NoError(t, err)
	f.store = NewCodexEngineBillingStore(integrationDB)
	reclaimed := f.claim(t, j.ID)
	require.NotEqual(t, j.ClaimToken, reclaimed.ClaimToken)
	pending, err := f.store.ReadPendingReceived(ctx, j.ID, reclaimed.ClaimToken, 0, 100)
	require.NoError(t, err)
	require.Len(t, pending, 1, "committed money still requires its cache acknowledgement")
	require.ErrorIs(t, f.store.AckReceivedSettled(ctx, j.ID, j.ClaimToken, first.EventID, first.Fingerprint, first.Sequence), service.ErrCodexEngineBillingClaimLost)
	_, err = f.store.ApplyEvent(ctx, engineBillingDBEvent(j, 1))
	require.ErrorIs(t, err, service.ErrCodexEngineBillingClaimLost)
	replay := engineBillingDBEvent(reclaimed, 1)
	replay.Command.BalanceCost = 9
	out, err = f.store.ApplyEvent(ctx, replay)
	require.NoError(t, err)
	require.False(t, out.Applied)
	f.stage(t, reclaimed, engineBillingDBReceived(2, false))
	second := engineBillingDBEvent(reclaimed, 2)
	second.UsageLog.Model = strings.Repeat("invalid-model-too-long", 100)
	_, err = f.store.ApplyEvent(ctx, second)
	require.Error(t, err)
	require.NoError(t, integrationDB.QueryRow(`SELECT balance FROM users WHERE id=$1`, f.user.ID).Scan(&balance))
	require.Equal(t, 98.0, balance)
	require.NoError(t, integrationDB.QueryRow(`SELECT COUNT(*) FROM codex_engine_billing_events WHERE job_id=$1`, j.ID).Scan(&count))
	require.Equal(t, 1, count)
	second = engineBillingDBEvent(reclaimed, 2)
	out, err = f.store.ApplyEvent(ctx, second)
	require.NoError(t, err)
	require.True(t, out.Applied)
	groups, err := f.store.ReadSummary(ctx, j.ID, 1)
	require.NoError(t, err)
	require.Len(t, groups, 1)
	require.Equal(t, int64(10), groups[0].Usage["input_tokens"])
	groups, err = f.store.ReadSummary(ctx, j.ID, 2)
	require.NoError(t, err)
	require.Equal(t, int64(20), groups[0].Usage["input_tokens"])
	progress := &service.CodexEngineBillingProgress{JobID: j.ID, ClaimToken: reclaimed.ClaimToken, Revision: 2, ConsumedSequence: 3, Cursor: "opaque-cursor", State: service.CodexEngineBillingSealedState, TerminalSeal: []byte(`{"last_sequence":2,"complete":true}`)}
	require.ErrorIs(t, f.store.SaveProgress(ctx, progress), service.ErrCodexEngineBillingClaimLost)
	progress.ConsumedSequence = 2
	require.ErrorIs(t, f.store.SaveProgress(ctx, progress), service.ErrCodexEngineBillingClaimLost, "ledger completion alone cannot seal before cache acknowledgement")
	f.ack(t, reclaimed, 1)
	f.ack(t, reclaimed, 1) // A repeated acknowledgement is idempotent.
	f.ack(t, reclaimed, 2)
	pending, err = f.store.ReadPendingReceived(ctx, j.ID, reclaimed.ClaimToken, 0, 100)
	require.NoError(t, err)
	require.Empty(t, pending)
	require.NoError(t, f.store.SaveProgress(ctx, progress))
	saved, err := f.store.Get(ctx, j.ID)
	require.NoError(t, err)
	require.Equal(t, service.CodexEngineBillingSealedState, saved.State)
	require.Empty(t, saved.ClaimToken)
	require.NoError(t, integrationDB.QueryRow(`SELECT balance FROM users WHERE id=$1`, f.user.ID).Scan(&balance))
	require.Equal(t, 96.0, balance)
}

func TestCodexEngineBillingStoreDisconnectDiscardsUsageIntegration(t *testing.T) {
	f := newEngineBillingDBFixture(t)
	ctx := context.Background()
	j := f.pending(t)
	jobs, err := f.store.ClaimDue(ctx, 100, time.Minute)
	require.NoError(t, err)
	for _, job := range jobs {
		require.NotEqual(t, j.ID, job.ID, "pending delivery is not eligible for receipt polling")
	}
	// Simulate an inbox staged by a stale process before the new claim filter.
	j.ClaimToken = "old-worker"
	_, err = integrationDB.Exec(`UPDATE codex_engine_billing_jobs SET claim_token=$2,lease_until=NOW()+INTERVAL '1 minute' WHERE id=$1`, j.ID, j.ClaimToken)
	require.NoError(t, err)
	j, err = f.store.BindReceipt(ctx, j.ID, f.instance, "disconnect-receipt", j.ClaimToken)
	require.NoError(t, err)
	f.stage(t, j, engineBillingDBReceived(1, false))
	event := engineBillingDBEvent(j, 1)
	_, err = f.store.ApplyEvent(ctx, event)
	require.ErrorIs(t, err, service.ErrCodexEngineBillingDeliveryPending)
	var balance float64
	require.NoError(t, integrationDB.QueryRow(`SELECT balance FROM users WHERE id=$1`, f.user.ID).Scan(&balance))
	require.Equal(t, 100.0, balance, "confirmed usage cannot be charged before delivery is finalized")
	var count int
	require.NoError(t, integrationDB.QueryRow(`SELECT COUNT(*) FROM codex_engine_billing_events WHERE job_id=$1`, j.ID).Scan(&count))
	require.Zero(t, count)

	deliveryStore, ok := f.store.(service.CodexEngineBillingDeliveryStore)
	require.True(t, ok)
	require.NoError(t, deliveryStore.FinalizeDelivery(ctx, j.ID, service.CodexEngineBillingNoCustomerCharge))
	_, err = f.store.ApplyEvent(ctx, event)
	require.ErrorIs(t, err, service.ErrCodexEngineBillingClaimLost)
	require.NoError(t, integrationDB.QueryRow(`SELECT balance FROM users WHERE id=$1`, f.user.ID).Scan(&balance))
	require.Equal(t, 100.0, balance, "disconnect usage does not debit the customer wallet")
	require.NoError(t, integrationDB.QueryRow(`SELECT COUNT(*) FROM usage_logs WHERE api_key_id=$1`, f.key.ID).Scan(&count))
	require.Zero(t, count, "incomplete delivery must not create usage records")
	require.NoError(t, integrationDB.QueryRow(`SELECT COUNT(*) FROM codex_engine_billing_received_events WHERE job_id=$1`, j.ID).Scan(&count))
	require.Zero(t, count, "already staged receipt facts are discarded")
	jobs, err = f.store.ClaimDue(ctx, 100, time.Minute)
	require.NoError(t, err)
	for _, job := range jobs {
		require.NotEqual(t, j.ID, job.ID, "incomplete delivery is terminal and never polled again")
	}
}

func TestCodexEngineBillingStoreCanonicalAcrossAccountsIntegration(t *testing.T) {
	f := newEngineBillingDBFixture(t)
	first := f.delivered(t, f.pending(t))
	first = f.claim(t, first.ID)
	canonical, err := f.store.BindReceipt(context.Background(), first.ID, f.instance, "shared-receipt", first.ClaimToken)
	require.NoError(t, err)
	other := newEngineBillingDBFixture(t)
	other.instance = f.instance
	alias := other.delivered(t, other.pending(t))
	alias = other.claim(t, alias.ID)
	bound, err := other.store.BindReceipt(context.Background(), alias.ID, other.instance, "shared-receipt", alias.ClaimToken)
	require.NoError(t, err)
	require.Equal(t, canonical.ID, bound.ID)
	require.Equal(t, f.user.ID, bound.UserID)
	require.Equal(t, f.key.ID, bound.APIKeyID)
	saved, err := other.store.Get(context.Background(), alias.ID)
	require.NoError(t, err)
	require.Equal(t, service.CodexEngineBillingLinkedState, saved.State)
	require.Empty(t, saved.ClaimToken)
	_, err = other.store.ApplyEvent(context.Background(), engineBillingDBEvent(alias, 1))
	require.ErrorIs(t, err, service.ErrCodexEngineBillingClaimLost)
	f.stage(t, canonical, engineBillingDBReceived(1, false))
	_, err = f.store.ApplyEvent(context.Background(), engineBillingDBEvent(canonical, 1))
	require.NoError(t, err)
}

func TestCodexEngineBillingStoreConcurrentClaimsAndEventsIntegration(t *testing.T) {
	f := newEngineBillingDBFixture(t)
	j := f.delivered(t, f.pending(t))
	claims := make(chan []*service.CodexEngineBillingJob, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rows, err := f.store.ClaimDue(context.Background(), 1, time.Minute)
			claims <- rows
			errs <- err
		}()
	}
	wg.Wait()
	close(claims)
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	var claimed *service.CodexEngineBillingJob
	var total int
	for rows := range claims {
		total += len(rows)
		if len(rows) > 0 {
			claimed = rows[0]
		}
	}
	require.Equal(t, 1, total)
	require.Equal(t, j.ID, claimed.ID)
	claimed, err := f.store.BindReceipt(context.Background(), j.ID, f.instance, "concurrent-receipt", claimed.ClaimToken)
	require.NoError(t, err)
	f.stage(t, claimed, engineBillingDBReceived(1, false))
	errs = make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := f.store.ApplyEvent(context.Background(), engineBillingDBEvent(claimed, 1))
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	var balance float64
	require.NoError(t, integrationDB.QueryRow(`SELECT balance FROM users WHERE id=$1`, f.user.ID).Scan(&balance))
	require.Equal(t, 98.0, balance)
}

func TestCodexEngineBillingStoreUndispatchedAndBlockedIntegration(t *testing.T) {
	f := newEngineBillingDBFixture(t)
	j := f.pending(t)
	require.NoError(t, f.store.MarkNotDispatched(context.Background(), j.ID))
	require.NoError(t, f.store.MarkNotDispatched(context.Background(), j.ID))
	saved, err := f.store.Get(context.Background(), j.ID)
	require.NoError(t, err)
	require.Equal(t, service.CodexEngineBillingNotDispatchedState, saved.State)
	require.Empty(t, saved.TerminalSeal)
	_, err = f.store.BindReceipt(context.Background(), j.ID, f.instance, "late", "stale-claim")
	require.ErrorIs(t, err, service.ErrCodexEngineBillingClaimLost)
	blocked := f.delivered(t, f.pending(t))
	blocked = f.claim(t, blocked.ID)
	require.NoError(t, f.store.SaveProgress(context.Background(), &service.CodexEngineBillingProgress{JobID: blocked.ID, ClaimToken: blocked.ClaimToken, State: service.CodexEngineBillingBlockedState, LastError: "identity_changed"}))
	saved, err = f.store.Get(context.Background(), blocked.ID)
	require.NoError(t, err)
	require.Equal(t, service.CodexEngineBillingBlockedState, saved.State)
	require.NotEmpty(t, saved.Snapshot)
	rows, err := f.store.ClaimDue(context.Background(), 100, time.Minute)
	require.NoError(t, err)
	require.Empty(t, rows)
}

func TestCodexEngineBillingStoreInboxPartialDoesNotBlockLaterPagesIntegration(t *testing.T) {
	f := newEngineBillingDBFixture(t)
	ctx := context.Background()
	j := f.delivered(t, f.pending(t))
	j = f.claim(t, j.ID)
	var err error
	j, err = f.store.BindReceipt(ctx, j.ID, f.instance, "partial-receipt", j.ClaimToken)
	require.NoError(t, err)
	first := make([]service.CodexEngineBillingReceivedEvent, 100)
	for i := range first {
		first[i] = engineBillingDBReceived(int64(i+1), i == 0)
	}
	f.stage(t, j, first...)
	f.stage(t, j, engineBillingDBReceived(101, false))
	stored, err := f.store.Get(ctx, j.ID)
	require.NoError(t, err)
	require.EqualValues(t, 101, stored.ConsumedSequence)
	require.Equal(t, "cursor-101", stored.Cursor)
	// The permanent partial event is retained, while another page can be priced.
	later, err := f.store.ReadPendingReceived(ctx, j.ID, j.ClaimToken, 100, 100)
	require.NoError(t, err)
	require.Len(t, later, 1)
	require.EqualValues(t, 101, later[0].Sequence)
	out, err := f.store.ApplyEvent(ctx, engineBillingDBEvent(j, 101))
	require.NoError(t, err)
	require.True(t, out.Applied)
	f.ack(t, j, 101)
	groups, err := f.store.ReadSummary(ctx, j.ID, 101)
	require.NoError(t, err)
	require.Len(t, groups, 1)
	require.EqualValues(t, 1010, groups[0].Usage["input_tokens"])
	require.EqualValues(t, 200, groups[0].Usage["output_tokens"])
	seal := []byte(`{"last_sequence":101,"reason":"source_closed_with_gaps","sealed_at":"2026-11-06T00:01:00Z"}`)
	p := &service.CodexEngineBillingProgress{JobID: j.ID, ClaimToken: j.ClaimToken, Cursor: stored.Cursor, Revision: stored.Revision, ConsumedSequence: 101, SettlementCursor: 100, SourceSeal: seal, TerminalSeal: seal, State: service.CodexEngineBillingSealedState}
	require.ErrorIs(t, f.store.SaveProgress(ctx, p), service.ErrCodexEngineBillingClaimLost, "a source seal cannot imply all local fees were settled")
	p.TerminalSeal = nil
	p.State = service.CodexEngineBillingBlockedState
	p.LastError = "upstream_identity_changed"
	require.NoError(t, f.store.SaveProgress(ctx, p))
	// Restart and credential change do not invalidate already-received facts.
	f.store = NewCodexEngineBillingStore(integrationDB)
	reclaimed := f.claim(t, j.ID)
	require.Equal(t, service.CodexEngineBillingBlockedState, reclaimed.State)
	require.JSONEq(t, string(seal), string(reclaimed.SourceSeal))
	require.EqualValues(t, 100, reclaimed.SettlementCursor)
	_, err = integrationDB.Exec(`UPDATE accounts SET credentials='{"api_key":"rotated"}'::jsonb WHERE id=$1`, f.account.ID)
	require.NoError(t, err)
	out, err = f.store.ApplyEvent(ctx, engineBillingDBEvent(reclaimed, 2))
	require.NoError(t, err)
	require.True(t, out.Applied)
	f.ack(t, reclaimed, 2)
	remaining, err := f.store.ReadPendingReceived(ctx, j.ID, reclaimed.ClaimToken, 0, 100)
	require.NoError(t, err)
	require.Len(t, remaining, 99)
	require.EqualValues(t, 1, remaining[0].Sequence)
	partial, err := service.ParseCodexEngineBillingUsageEvent(remaining[0].Raw)
	require.NoError(t, err)
	require.False(t, partial.UsageComplete)
	require.NotContains(t, partial.UsageValues, "output_tokens")
	var balance float64
	require.NoError(t, integrationDB.QueryRow(`SELECT balance FROM users WHERE id=$1`, f.user.ID).Scan(&balance))
	require.Equal(t, 96.0, balance)
}

func TestCodexEngineBillingStoreInboxConflictsRollBackCursorIntegration(t *testing.T) {
	f := newEngineBillingDBFixture(t)
	ctx := context.Background()
	j := f.delivered(t, f.pending(t))
	j = f.claim(t, j.ID)
	var err error
	j, err = f.store.BindReceipt(ctx, j.ID, f.instance, "conflict-receipt", j.ClaimToken)
	require.NoError(t, err)
	first := engineBillingDBReceived(1, false)
	f.stage(t, j, first)
	// A new event cannot reuse a previous immutable event ID at another sequence.
	conflict := engineBillingDBReceived(3, false)
	var raw map[string]any
	require.NoError(t, json.Unmarshal(conflict.Raw, &raw))
	raw["event_id"] = "event-1"
	changed, _ := json.Marshal(raw)
	e, err := service.ParseCodexEngineBillingUsageEvent(changed)
	require.NoError(t, err)
	conflict.EventID, conflict.Fingerprint, conflict.Raw = e.EventID, e.Fingerprint, e.Raw
	err = f.store.StageEvents(ctx, &service.CodexEngineBillingReceivedPage{JobID: j.ID, ClaimToken: j.ClaimToken, Cursor: "cursor-3", Revision: 2, ConsumedSequence: 3, Events: []service.CodexEngineBillingReceivedEvent{engineBillingDBReceived(2, false), conflict}})
	require.ErrorIs(t, err, service.ErrCodexEngineBillingConflict)
	stored, err := f.store.Get(ctx, j.ID)
	require.NoError(t, err)
	require.EqualValues(t, 1, stored.ConsumedSequence)
	require.Equal(t, "cursor-1", stored.Cursor)
	var count int
	require.NoError(t, integrationDB.QueryRow(`SELECT COUNT(*) FROM codex_engine_billing_received_events WHERE job_id=$1`, j.ID).Scan(&count))
	require.Equal(t, 1, count)
	// Identical retransmission after a committed page is idempotent.
	f.stage(t, j, first)
	_, err = f.store.ApplyEvent(ctx, engineBillingDBEvent(j, 2))
	require.ErrorIs(t, err, service.ErrCodexEngineBillingConflict, "billing cannot precede durable received evidence")
}
