//go:build unit

package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func engineBillingUnitStore(t *testing.T) (*codexEngineBillingStore, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, mock.ExpectationsWereMet()); _ = db.Close() })
	return &codexEngineBillingStore{db: db}, mock
}

func engineBillingJobRows() *sqlmock.Rows {
	cols := strings.Split(codexEngineBillingJobColumns, ",")
	for i := range cols {
		cols[i] = strings.TrimSpace(cols[i])
	}
	return sqlmock.NewRows(cols)
}

func engineBillingClaimedRows() *sqlmock.Rows {
	return engineBillingClaimedRowsWithDelivery(service.CodexEngineBillingDelivered)
}

func engineBillingClaimedRowsWithDelivery(delivery string) *sqlmock.Rows {
	now := time.Now()
	return engineBillingJobRows().AddRow(int64(1), "client-original", int64(3), int64(4), int64(5),
		"identity", "engine-instance", []byte(`{"version":1,"multiplier":1}`), "receipt", "pending", "", int64(0), int64(0), now,
		"claim", now.Add(time.Minute), 1, nil, nil, "", now, now, nil, int64(0), delivery)
}

func engineBillingEvent() *service.CodexEngineBillingEvent {
	return &service.CodexEngineBillingEvent{JobID: 1, ClaimToken: "claim", EventID: "event-1", Fingerprint: strings.Repeat("a", 64), Sequence: 1,
		Summary:  service.CodexEngineBillingSummaryGroup{Kind: "model", Purpose: "response", Model: "gpt-test", Usage: map[string]int64{"input_tokens": 10, "output_tokens": 2}},
		Command:  service.UsageBillingCommand{Model: "gpt-test", BalanceCost: 2, InputTokens: 10, OutputTokens: 2},
		UsageLog: service.UsageLog{Model: "gpt-test", InputTokens: 10, OutputTokens: 2, TotalCost: 2, ActualCost: 2, RateMultiplier: 1}}
}

func expectEngineBillingClaim(mock sqlmock.Sqlmock) {
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT .* FROM codex_engine_billing_jobs WHERE id=\$1 AND claim_token=\$2`).WithArgs(int64(1), "claim").WillReturnRows(engineBillingClaimedRows())
	mock.ExpectQuery(`SELECT fingerprint,sequence FROM codex_engine_billing_received_events`).WithArgs(int64(1), "event-1").WillReturnRows(sqlmock.NewRows([]string{"fingerprint", "sequence"}).AddRow(strings.Repeat("a", 64), int64(1)))
}

func expectEngineBillingNewEvent(mock sqlmock.Sqlmock) {
	expectEngineBillingEventAndKey(mock)
	mock.ExpectQuery(dualWalletBalanceDeductSQL).WithArgs(float64(2), int64(4)).WillReturnRows(sqlmock.NewRows([]string{"total_balance", "sufficient"}).AddRow(float64(98), true))
}

func expectEngineBillingEventAndKey(mock sqlmock.Sqlmock) {
	mock.ExpectExec(`INSERT INTO codex_engine_billing_events`).WithArgs(int64(1), "event-1", strings.Repeat("a", 64), sqlmock.AnyArg(), int64(1), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`INSERT INTO usage_billing_dedup`).WithArgs(sqlmock.AnyArg(), int64(5), strings.Repeat("a", 64)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(1)))
	mock.ExpectQuery(`SELECT request_fingerprint.*FROM usage_billing_dedup_archive`).WithArgs(sqlmock.AnyArg(), int64(5)).WillReturnRows(sqlmock.NewRows([]string{"request_fingerprint"}))
}

func TestCodexEngineBillingStoreApplyEventAtomic(t *testing.T) {
	r, mock := engineBillingUnitStore(t)
	expectEngineBillingClaim(mock)
	expectEngineBillingNewEvent(mock)
	mock.ExpectQuery(`INSERT INTO usage_logs`).WillReturnRows(sqlmock.NewRows([]string{"id", "created_at"}).AddRow(int64(17), time.Now()))
	mock.ExpectExec(`UPDATE codex_engine_billing_events SET usage_log_id`).WithArgs(int64(1), "event-1", int64(17)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	e := engineBillingEvent()
	out, err := r.ApplyEvent(context.Background(), e)
	require.NoError(t, err)
	require.True(t, out.Applied)
	require.Equal(t, int64(17), e.UsageLog.ID)
	require.Equal(t, e.Command.RequestID, e.UsageLog.RequestID)
	require.Regexp(t, `^ce2:[A-Za-z0-9_-]{43}$`, e.Command.RequestID)
	require.Equal(t, int64(4), e.Command.UserID)
}

func TestCodexEngineBillingStoreDeliveryPendingStagesButDoesNotSettle(t *testing.T) {
	r, mock := engineBillingUnitStore(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT .* FROM codex_engine_billing_jobs WHERE id=\$1 AND claim_token=\$2`).WithArgs(int64(1), "claim").WillReturnRows(engineBillingClaimedRowsWithDelivery(service.CodexEngineBillingDeliveryPending))
	mock.ExpectRollback()
	_, err := r.ApplyEvent(context.Background(), engineBillingEvent())
	require.ErrorIs(t, err, service.ErrCodexEngineBillingDeliveryPending)
}

func TestCodexEngineBillingStoreDisconnectedDiscardsUsageAndCharge(t *testing.T) {
	r, mock := engineBillingUnitStore(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT .* FROM codex_engine_billing_jobs WHERE id=\$1 AND claim_token=\$2`).WithArgs(int64(1), "claim").WillReturnRows(engineBillingClaimedRowsWithDelivery(service.CodexEngineBillingNoCustomerCharge))
	mock.ExpectRollback()

	e := engineBillingEvent()
	e.Command.SubscriptionCost = 1
	e.Command.APIKeyQuotaCost = 3
	e.Command.APIKeyRateLimitCost = 4
	e.Command.AccountQuotaCost = 5
	e.PlatformQuotaCost = 6
	e.UsageLog.TotalCost = 2
	e.UsageLog.ActualCost = 2.5
	out, err := r.ApplyEvent(context.Background(), e)
	require.ErrorIs(t, err, service.ErrCodexEngineBillingClaimLost, "incomplete jobs must also skip post-apply cache effects")
	require.Nil(t, out, "no usage log, quota update, or billing ledger may be written")
	require.Zero(t, e.UsageLog.ID)
}

func TestCodexEngineBillingStoreFinalizeDeliveryIsIdempotentAndConflictSafe(t *testing.T) {
	r, mock := engineBillingUnitStore(t)
	query := `UPDATE codex_engine_billing_jobs SET delivery_outcome=\$2, updated_at=NOW\(\),.*WHERE id=\$1 AND \(delivery_outcome='pending' OR delivery_outcome=\$2\)`
	mock.ExpectBegin()
	mock.ExpectExec(query).WithArgs(int64(1), service.CodexEngineBillingDelivered).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	require.NoError(t, r.FinalizeDelivery(context.Background(), 1, service.CodexEngineBillingDelivered))
	mock.ExpectBegin()
	mock.ExpectExec(query).WithArgs(int64(1), service.CodexEngineBillingNoCustomerCharge).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()
	require.ErrorIs(t, r.FinalizeDelivery(context.Background(), 1, service.CodexEngineBillingNoCustomerCharge), service.ErrCodexEngineBillingConflict)
}

func TestCodexEngineBillingStoreFinalizeIncompleteDiscardsStagedFactsAtomically(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("discard failure=%t", fail), func(t *testing.T) {
			r, mock := engineBillingUnitStore(t)
			mock.ExpectBegin()
			mock.ExpectExec(`UPDATE codex_engine_billing_jobs SET delivery_outcome`).WithArgs(int64(1), service.CodexEngineBillingNoCustomerCharge).WillReturnResult(sqlmock.NewResult(0, 1))
			remove := mock.ExpectExec(`DELETE FROM codex_engine_billing_received_events WHERE job_id=\$1`).WithArgs(int64(1))
			if fail {
				remove.WillReturnError(errors.New("inbox unavailable"))
				mock.ExpectRollback()
			} else {
				remove.WillReturnResult(sqlmock.NewResult(0, 3))
				mock.ExpectCommit()
			}
			err := r.FinalizeDelivery(context.Background(), 1, service.CodexEngineBillingNoCustomerCharge)
			if fail {
				require.ErrorContains(t, err, "inbox unavailable")
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestCodexEngineBillingStoreCreateStartsDeliveryPending(t *testing.T) {
	r, mock := engineBillingUnitStore(t)
	now := time.Now()
	mock.ExpectQuery(`INSERT INTO codex_engine_billing_jobs`).WithArgs("client-new", int64(3), int64(4), int64(5), "identity", "engine-instance", `{"version":1}`, sqlmock.AnyArg()).WillReturnRows(engineBillingJobRows().AddRow(
		int64(2), "client-new", int64(3), int64(4), int64(5), "identity", "engine-instance", []byte(`{"version":1}`), "", "pending", "", int64(0), int64(0), now,
		"", nil, 0, nil, nil, "", now, now, nil, int64(0), service.CodexEngineBillingDeliveryPending))
	job, err := r.CreatePending(context.Background(), &service.CodexEngineBillingPending{ClientRequestID: "client-new", AccountID: 3, UserID: 4, APIKeyID: 5, IdentityDigest: "identity", InstanceID: "engine-instance", Snapshot: []byte(`{"version":1}`)})
	require.NoError(t, err)
	require.Equal(t, service.CodexEngineBillingDeliveryPending, job.DeliveryOutcome)
}

func TestCodexEngineBillingStoreUsageInsertFailureRollsBackCharge(t *testing.T) {
	r, mock := engineBillingUnitStore(t)
	expectEngineBillingClaim(mock)
	expectEngineBillingNewEvent(mock)
	mock.ExpectQuery(`INSERT INTO usage_logs`).WillReturnError(errors.New("usage insert failed"))
	mock.ExpectRollback()
	_, err := r.ApplyEvent(context.Background(), engineBillingEvent())
	require.ErrorContains(t, err, "usage insert failed")
}

func TestCodexEngineBillingStoreReplayDoesNotChargeAgain(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(map[bool]string{false: "same event different current price", true: "changed event facts"}[conflict], func(t *testing.T) {
			r, mock := engineBillingUnitStore(t)
			expectEngineBillingClaim(mock)
			mock.ExpectExec(`INSERT INTO codex_engine_billing_events`).WillReturnResult(sqlmock.NewResult(0, 0))
			fingerprint := strings.Repeat("a", 64)
			if conflict {
				fingerprint = strings.Repeat("b", 64)
			}
			mock.ExpectQuery(`SELECT fingerprint, sequence FROM codex_engine_billing_events`).WillReturnRows(sqlmock.NewRows([]string{"fingerprint", "sequence"}).AddRow(fingerprint, int64(1)))
			if conflict {
				mock.ExpectRollback()
			} else {
				mock.ExpectCommit()
			}
			e := engineBillingEvent()
			e.Command.BalanceCost = 99
			out, err := r.ApplyEvent(context.Background(), e)
			if conflict {
				require.ErrorIs(t, err, service.ErrCodexEngineBillingConflict)
			} else {
				require.NoError(t, err)
				require.False(t, out.Applied)
			}
		})
	}
}

func TestCodexEngineBillingStoreRejectsLostClaimAndWrongOwner(t *testing.T) {
	t.Run("lease", func(t *testing.T) {
		r, mock := engineBillingUnitStore(t)
		mock.ExpectBegin()
		mock.ExpectQuery(`SELECT .* FROM codex_engine_billing_jobs`).WillReturnRows(engineBillingJobRows())
		mock.ExpectRollback()
		_, err := r.ApplyEvent(context.Background(), engineBillingEvent())
		require.ErrorIs(t, err, service.ErrCodexEngineBillingClaimLost)
	})
	t.Run("owner", func(t *testing.T) {
		r, mock := engineBillingUnitStore(t)
		expectEngineBillingClaim(mock)
		mock.ExpectRollback()
		e := engineBillingEvent()
		e.Command.UserID = 999
		_, err := r.ApplyEvent(context.Background(), e)
		require.ErrorIs(t, err, service.ErrCodexEngineBillingConflict)
	})
}

func TestCodexEngineBillingStoreCreatePreservesFirstSnapshot(t *testing.T) {
	r, mock := engineBillingUnitStore(t)
	mock.ExpectQuery(`INSERT INTO codex_engine_billing_jobs`).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`SELECT .* FROM codex_engine_billing_jobs WHERE client_request_id`).WillReturnRows(engineBillingClaimedRows())
	j, err := r.CreatePending(context.Background(), &service.CodexEngineBillingPending{ClientRequestID: "client-original", AccountID: 3, UserID: 4, APIKeyID: 5, IdentityDigest: "identity", InstanceID: "engine-instance", Snapshot: []byte(`{"multiplier":9}`)})
	require.NoError(t, err)
	require.JSONEq(t, `{"version":1,"multiplier":1}`, string(j.Snapshot))
}

func TestCodexEngineBillingStoreProgressNeedsLiveClaimAndSeal(t *testing.T) {
	r, mock := engineBillingUnitStore(t)
	p := &service.CodexEngineBillingProgress{JobID: 1, ClaimToken: "claim", State: service.CodexEngineBillingSealedState}
	require.Error(t, r.SaveProgress(context.Background(), p))
	p.TerminalSeal = []byte(`{"sealed":true}`)
	mock.ExpectExec(`UPDATE codex_engine_billing_jobs SET cursor`).WillReturnResult(sqlmock.NewResult(0, 0))
	require.ErrorIs(t, r.SaveProgress(context.Background(), p), service.ErrCodexEngineBillingClaimLost)
}

func TestCodexEngineBillingSnapshotContainsNoCredentialsOrBodies(t *testing.T) {
	for _, raw := range []string{`null`, `[]`, `{"nested":{"api_key":"secret"}}`, `{"items":[{"refresh_token":"secret"}]}`, `{"body":"prompt"}`} {
		require.Error(t, validateEngineBillingSnapshot([]byte(raw)), raw)
	}
	require.NoError(t, validateEngineBillingSnapshot([]byte(`{"api_key_id":12,"model":"gpt-test","price":{"input":1.5}}`)))
}

func TestCodexEngineBillingStoreReadSummaryPreservesMissingCounters(t *testing.T) {
	r, mock := engineBillingUnitStore(t)
	mock.ExpectQuery(`SELECT e.summary`).WithArgs(int64(1), int64(3)).WillReturnRows(sqlmock.NewRows([]string{"kind", "purpose", "model", "tier", "key", "sum"}).
		AddRow("model", "response", "gpt-test", nil, "input_tokens", "12").
		AddRow("model", "response", "gpt-test", nil, "output_tokens", "3").
		AddRow("search", "search", "gpt-test", "priority", nil, nil))
	groups, err := r.ReadSummary(context.Background(), 1, 3)
	require.NoError(t, err)
	require.Len(t, groups, 2)
	require.Equal(t, map[string]int64{"input_tokens": 12, "output_tokens": 3}, groups[0].Usage)
	require.Nil(t, groups[0].ServiceTier)
	require.Empty(t, groups[1].Usage)
	require.Equal(t, "priority", *groups[1].ServiceTier)
}

func TestCodexEngineBillingStoreClaimIsBounded(t *testing.T) {
	r, mock := engineBillingUnitStore(t)
	mock.ExpectQuery(`WITH due AS .* AND delivery_outcome='delivered'`).WithArgs(100, sqlmock.AnyArg(), int64(60000)).WillReturnRows(engineBillingClaimedRows())
	jobs, err := r.ClaimDue(context.Background(), 10000, time.Minute)
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	require.Equal(t, "claim", jobs[0].ClaimToken)
	_, err = r.ClaimDue(context.Background(), 1, 24*time.Hour)
	require.Error(t, err)
}

func TestCodexEngineBillingStoreRejectsDifferentEventAtSameSequence(t *testing.T) {
	r, mock := engineBillingUnitStore(t)
	expectEngineBillingClaim(mock)
	mock.ExpectExec(`INSERT INTO codex_engine_billing_events`).WillReturnError(&pq.Error{Code: "23505", Constraint: "codex_engine_billing_events_job_id_sequence_key"})
	mock.ExpectRollback()
	_, err := r.ApplyEvent(context.Background(), engineBillingEvent())
	require.ErrorIs(t, err, service.ErrCodexEngineBillingConflict)
}

func TestCodexEngineBillingStoreNotDispatchedRequiresUnboundReceipt(t *testing.T) {
	r, mock := engineBillingUnitStore(t)
	mock.ExpectExec(`UPDATE codex_engine_billing_jobs.*WHERE id=\$1 AND state IN \('pending','not_dispatched'\) AND receipt_id=''`).WithArgs(int64(1)).WillReturnResult(sqlmock.NewResult(0, 0))
	require.ErrorIs(t, r.MarkNotDispatched(context.Background(), 1), service.ErrCodexEngineBillingConflict)
}
