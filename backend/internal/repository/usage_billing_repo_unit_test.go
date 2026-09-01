//go:build unit

package repository

import (
	"context"
	"database/sql"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

const (
	dualWalletBalanceDeductSQL  = `(?s)WITH target AS \(.*gift_balance.*ordinary_debit.*gift_debit.*SELECT total_balance, sufficient FROM updated`
	reserveDualWalletHoldSQL    = `(?s)WITH target AS \(.*frozen_gift_balance.*ordinary_hold.*gift_hold.*SELECT total_balance, total_frozen, ordinary_hold, gift_hold FROM updated`
	updateHoldProvenanceSQL     = `(?s)UPDATE usage_billing_dedup.*SET ordinary_hold_amount = \$1, gift_hold_amount = \$2.*WHERE request_id = \$3 AND api_key_id = \$4`
	holdClaimSQL                = `(?s)SELECT ordinary_hold_amount, gift_hold_amount, hold_terminal_kind.*FROM usage_billing_dedup.*WHERE request_id = \$1 AND api_key_id = \$2.*FOR UPDATE`
	archivedHoldClaimSQL        = `(?s)SELECT ordinary_hold_amount, gift_hold_amount, hold_terminal_kind.*FROM usage_billing_dedup_archive.*WHERE request_id = \$1 AND api_key_id = \$2.*FOR UPDATE`
	markHoldCapturedSQL         = `(?s)UPDATE usage_billing_dedup.*SET hold_terminal_kind = \$1.*WHERE request_id = \$2 AND api_key_id = \$3 AND hold_terminal_kind = ''`
	markHoldReleasedSQL         = markHoldCapturedSQL
	markArchivedHoldReleasedSQL = `(?s)UPDATE usage_billing_dedup_archive.*SET hold_terminal_kind = \$1.*WHERE request_id = \$2 AND api_key_id = \$3 AND hold_terminal_kind = ''`
	settleDualWalletHoldSQL     = `(?s)UPDATE users.*SET balance = balance \+ \$1,.*gift_balance = gift_balance \+ \$2,.*frozen_balance = frozen_balance - \$3,.*frozen_gift_balance = frozen_gift_balance - \$4.*RETURNING balance \+ gift_balance, frozen_balance \+ frozen_gift_balance`
	releaseDualWalletHoldSQL    = `(?s)UPDATE users.*SET balance = balance \+ \$1,.*gift_balance = gift_balance \+ \$2,.*frozen_balance = frozen_balance - \$1,.*frozen_gift_balance = frozen_gift_balance - \$2.*RETURNING balance \+ gift_balance, frozen_balance \+ frozen_gift_balance`
	userExistsForBillingSQL     = `(?s)SELECT 1.*FROM users.*WHERE id = \$1 AND deleted_at IS NULL`
)

func beginUsageBillingUnitTx(t *testing.T) (*sql.DB, sqlmock.Sqlmock, *sql.Tx) {
	t.Helper()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectBegin()
	tx, err := db.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	return db, mock, tx
}

func TestDeductUsageBillingBalance_UsesOneAtomicDualWalletAllocation(t *testing.T) {
	tests := []struct {
		name          string
		amount        float64
		returnedTotal float64
		sufficient    bool
	}{
		{name: "ordinary balance covers cost", amount: 2.5, returnedTotal: 7.5, sufficient: true},
		{name: "cost crosses into gift wallet", amount: 7.5, returnedTotal: 2.5, sufficient: true},
		{name: "wallets exhausted and ordinary debt recorded", amount: 15, returnedTotal: -5, sufficient: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, mock, tx := beginUsageBillingUnitTx(t)
			mock.ExpectQuery(dualWalletBalanceDeductSQL).
				WithArgs(tt.amount, int64(42)).
				WillReturnRows(sqlmock.NewRows([]string{"total_balance", "sufficient"}).AddRow(tt.returnedTotal, tt.sufficient))
			mock.ExpectCommit()

			newBalance, sufficient, err := deductUsageBillingBalance(context.Background(), tx, 42, tt.amount)
			require.NoError(t, err)
			require.Equal(t, tt.sufficient, sufficient)
			require.InDelta(t, tt.returnedTotal, newBalance, 0.000001)
			require.NoError(t, tx.Commit())
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestDeductUsageBillingBalance_ReturnsUserNotFoundWithoutFallbackMutation(t *testing.T) {
	_, mock, tx := beginUsageBillingUnitTx(t)
	mock.ExpectQuery(dualWalletBalanceDeductSQL).
		WithArgs(10.0, int64(42)).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()

	_, _, err := deductUsageBillingBalance(context.Background(), tx, 42, 10)
	require.ErrorIs(t, err, service.ErrUserNotFound)
	require.NoError(t, tx.Rollback())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestApplyUsageBillingEffects_ReportsCombinedWalletOverdraft(t *testing.T) {
	_, mock, tx := beginUsageBillingUnitTx(t)
	mock.ExpectQuery(dualWalletBalanceDeductSQL).
		WithArgs(10.0, int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{"total_balance", "sufficient"}).AddRow(-5.0, false))
	mock.ExpectCommit()

	result := &service.UsageBillingApplyResult{Applied: true}
	err := (&usageBillingRepository{}).applyUsageBillingEffects(context.Background(), tx, &service.UsageBillingCommand{
		UserID:      42,
		BalanceCost: 10,
	}, result)
	require.NoError(t, err)
	require.NotNil(t, result.NewBalance)
	require.InDelta(t, -5.0, *result.NewBalance, 0.000001)
	require.True(t, result.BalanceOverdrafted)
	require.NoError(t, tx.Commit())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestReserveUsageBillingBatchImageBalance_RecordsWalletProvenance(t *testing.T) {
	_, mock, tx := beginUsageBillingUnitTx(t)
	cmd := &service.BatchImageBalanceHoldCommand{
		RequestID:  "batch_image_hold:imgbatch_cross-wallet",
		APIKeyID:   7,
		UserID:     42,
		HoldAmount: 2.5,
	}
	mock.ExpectQuery(reserveDualWalletHoldSQL).
		WithArgs(2.5, int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{"total_balance", "total_frozen", "ordinary_hold", "gift_hold"}).
			AddRow(7.5, 2.5, 1.5, 1.0))
	mock.ExpectExec(updateHoldProvenanceSQL).
		WithArgs(1.5, 1.0, cmd.RequestID, int64(7)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	result, err := reserveUsageBillingBatchImageBalance(context.Background(), tx, cmd)
	require.NoError(t, err)
	require.InDelta(t, 7.5, *result.NewBalance, 0.000001)
	require.InDelta(t, 2.5, *result.FrozenBalance, 0.000001)
	require.NoError(t, tx.Commit())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestReserveUsageBillingBatchImageBalance_RequiresCombinedAvailableBalance(t *testing.T) {
	_, mock, tx := beginUsageBillingUnitTx(t)
	mock.ExpectQuery(reserveDualWalletHoldSQL).
		WithArgs(10.0, int64(42)).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(userExistsForBillingSQL).
		WithArgs(int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	mock.ExpectRollback()

	_, err := reserveUsageBillingBatchImageBalance(context.Background(), tx, &service.BatchImageBalanceHoldCommand{UserID: 42, HoldAmount: 10})
	require.ErrorIs(t, err, service.ErrBatchImageInsufficientBalance)
	require.NoError(t, tx.Rollback())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCaptureUsageBillingBatchImageBalance_ReleasesEachWalletToItsSource(t *testing.T) {
	_, mock, tx := beginUsageBillingUnitTx(t)
	mock.ExpectQuery(holdClaimSQL).
		WithArgs(service.BatchImageHoldRequestID("imgbatch_capture"), int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"ordinary_hold_amount", "gift_hold_amount", "hold_terminal_kind"}).AddRow(0.6, 0.4, ""))
	mock.ExpectQuery(settleDualWalletHoldSQL).
		WithArgs(0.35, 0.4, 0.6, 0.4, int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{"total_balance", "total_frozen"}).AddRow(9.75, 0.0))
	mock.ExpectExec(markHoldCapturedSQL).
		WithArgs(batchImageHoldTerminalCaptured, service.BatchImageHoldRequestID("imgbatch_capture"), int64(7)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	result, err := captureUsageBillingBatchImageBalance(context.Background(), tx, &service.BatchImageBalanceHoldCommand{
		UserID: 42, APIKeyID: 7, BatchID: "imgbatch_capture", HoldAmount: 1, ActualAmount: 0.25,
	})
	require.NoError(t, err)
	require.InDelta(t, 9.75, *result.NewBalance, 0.000001)
	require.InDelta(t, 0.0, *result.FrozenBalance, 0.000001)
	require.NoError(t, tx.Commit())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCaptureUsageBillingBatchImageBalance_LegacyZeroProvenanceMeansOrdinaryHold(t *testing.T) {
	_, mock, tx := beginUsageBillingUnitTx(t)
	mock.ExpectQuery(holdClaimSQL).
		WithArgs(service.BatchImageHoldRequestID("imgbatch_legacy"), int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"ordinary_hold_amount", "gift_hold_amount", "hold_terminal_kind"}).AddRow(0.0, 0.0, ""))
	mock.ExpectQuery(settleDualWalletHoldSQL).
		WithArgs(0.75, 0.0, 1.0, 0.0, int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{"total_balance", "total_frozen"}).AddRow(9.75, 0.0))
	mock.ExpectExec(markHoldCapturedSQL).
		WithArgs(batchImageHoldTerminalCaptured, service.BatchImageHoldRequestID("imgbatch_legacy"), int64(7)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	result, err := captureUsageBillingBatchImageBalance(context.Background(), tx, &service.BatchImageBalanceHoldCommand{
		UserID: 42, APIKeyID: 7, BatchID: "imgbatch_legacy", HoldAmount: 1, ActualAmount: 0.25,
	})
	require.NoError(t, err)
	require.InDelta(t, 9.75, *result.NewBalance, 0.000001)
	require.NoError(t, tx.Commit())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCaptureUsageBillingBatchImageBalance_RejectsActualCostOverHoldBeforeMutation(t *testing.T) {
	_, mock, tx := beginUsageBillingUnitTx(t)
	mock.ExpectRollback()

	_, err := captureUsageBillingBatchImageBalance(context.Background(), tx, &service.BatchImageBalanceHoldCommand{
		UserID: 42, HoldAmount: 0.5, ActualAmount: 1,
	})
	require.ErrorIs(t, err, service.ErrBatchImageSettlementCostExceedsHold)
	require.NoError(t, tx.Rollback())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCaptureUsageBillingBatchImageBalance_ClampsToleranceSizedNegativeGiftRelease(t *testing.T) {
	_, mock, tx := beginUsageBillingUnitTx(t)
	mock.ExpectQuery(holdClaimSQL).
		WithArgs(service.BatchImageHoldRequestID("imgbatch_tolerance"), int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"ordinary_hold_amount", "gift_hold_amount", "hold_terminal_kind"}).
			AddRow(0.6, 0.4, ""))
	mock.ExpectQuery(settleDualWalletHoldSQL).
		WithArgs(0.0, 0.0, 0.6, 0.4, int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{"total_balance", "total_frozen"}).AddRow(9.0, 0.0))
	mock.ExpectExec(markHoldCapturedSQL).
		WithArgs(batchImageHoldTerminalCaptured, service.BatchImageHoldRequestID("imgbatch_tolerance"), int64(7)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	_, err := captureUsageBillingBatchImageBalance(context.Background(), tx, &service.BatchImageBalanceHoldCommand{
		UserID: 42, APIKeyID: 7, BatchID: "imgbatch_tolerance", HoldAmount: 1, ActualAmount: 1.000000005,
	})
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCaptureUsageBillingBatchImageBalance_AlreadyCapturedIsIdempotent(t *testing.T) {
	_, mock, tx := beginUsageBillingUnitTx(t)
	mock.ExpectQuery(holdClaimSQL).
		WithArgs(service.BatchImageHoldRequestID("imgbatch_captured"), int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"ordinary_hold_amount", "gift_hold_amount", "hold_terminal_kind"}).
			AddRow(0.6, 0.4, batchImageHoldTerminalCaptured))
	mock.ExpectCommit()

	result, err := captureUsageBillingBatchImageBalance(context.Background(), tx, &service.BatchImageBalanceHoldCommand{
		UserID: 42, APIKeyID: 7, BatchID: "imgbatch_captured", HoldAmount: 1, ActualAmount: 0.25,
	})
	require.NoError(t, err)
	require.Nil(t, result.NewBalance)
	require.NoError(t, tx.Commit())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCaptureUsageBillingBatchImageBalance_RejectsReleasedHoldBeforeWalletMutation(t *testing.T) {
	_, mock, tx := beginUsageBillingUnitTx(t)
	mock.ExpectQuery(holdClaimSQL).
		WithArgs(service.BatchImageHoldRequestID("imgbatch_released"), int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"ordinary_hold_amount", "gift_hold_amount", "hold_terminal_kind"}).
			AddRow(0.6, 0.4, batchImageHoldTerminalReleased))
	mock.ExpectRollback()

	_, err := captureUsageBillingBatchImageBalance(context.Background(), tx, &service.BatchImageBalanceHoldCommand{
		UserID: 42, APIKeyID: 7, BatchID: "imgbatch_released", HoldAmount: 1, ActualAmount: 0.25,
	})
	require.ErrorIs(t, err, service.ErrUsageBillingRequestConflict)
	require.NoError(t, tx.Rollback())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestReleaseUsageBillingBatchImageBalance_ReturnsEachFrozenWallet(t *testing.T) {
	_, mock, tx := beginUsageBillingUnitTx(t)
	mock.ExpectQuery(holdClaimSQL).
		WithArgs(service.BatchImageHoldRequestID("imgbatch_release"), int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"ordinary_hold_amount", "gift_hold_amount", "hold_terminal_kind"}).AddRow(0.6, 0.4, ""))
	mock.ExpectQuery(releaseDualWalletHoldSQL).
		WithArgs(0.6, 0.4, int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{"total_balance", "total_frozen"}).AddRow(10.0, 0.0))
	mock.ExpectExec(markHoldReleasedSQL).
		WithArgs(batchImageHoldTerminalReleased, service.BatchImageHoldRequestID("imgbatch_release"), int64(7)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	result, err := releaseUsageBillingBatchImageBalance(context.Background(), tx, &service.BatchImageBalanceHoldCommand{
		UserID: 42, APIKeyID: 7, BatchID: "imgbatch_release", HoldAmount: 1,
	})
	require.NoError(t, err)
	require.InDelta(t, 10.0, *result.NewBalance, 0.000001)
	require.InDelta(t, 0.0, *result.FrozenBalance, 0.000001)
	require.NoError(t, tx.Commit())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestReleaseUsageBillingBatchImageBalance_ArchivedReceiptMarksWinner(t *testing.T) {
	_, mock, tx := beginUsageBillingUnitTx(t)
	mock.ExpectQuery(holdClaimSQL).
		WithArgs(service.BatchImageHoldRequestID("imgbatch_archived"), int64(7)).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(archivedHoldClaimSQL).
		WithArgs(service.BatchImageHoldRequestID("imgbatch_archived"), int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"ordinary_hold_amount", "gift_hold_amount", "hold_terminal_kind"}).AddRow(0.6, 0.4, ""))
	mock.ExpectQuery(releaseDualWalletHoldSQL).
		WithArgs(0.6, 0.4, int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{"total_balance", "total_frozen"}).AddRow(10.0, 0.0))
	mock.ExpectExec(markArchivedHoldReleasedSQL).
		WithArgs(batchImageHoldTerminalReleased, service.BatchImageHoldRequestID("imgbatch_archived"), int64(7)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	result, err := releaseUsageBillingBatchImageBalance(context.Background(), tx, &service.BatchImageBalanceHoldCommand{
		UserID: 42, APIKeyID: 7, BatchID: "imgbatch_archived", HoldAmount: 1,
	})
	require.NoError(t, err)
	require.InDelta(t, 10.0, *result.NewBalance, 0.000001)
	require.NoError(t, tx.Commit())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestReleaseUsageBillingBatchImageBalance_SkipsWhenHoldNeverReserved(t *testing.T) {
	_, mock, tx := beginUsageBillingUnitTx(t)
	mock.ExpectQuery(holdClaimSQL).
		WithArgs(service.BatchImageHoldRequestID("imgbatch_phantom"), int64(7)).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(archivedHoldClaimSQL).
		WithArgs(service.BatchImageHoldRequestID("imgbatch_phantom"), int64(7)).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectCommit()

	result, err := releaseUsageBillingBatchImageBalance(context.Background(), tx, &service.BatchImageBalanceHoldCommand{
		UserID: 42, APIKeyID: 7, BatchID: "imgbatch_phantom", HoldAmount: 1,
	})
	require.NoError(t, err)
	require.Nil(t, result.NewBalance)
	require.Nil(t, result.FrozenBalance)
	require.NoError(t, tx.Commit())
	require.NoError(t, mock.ExpectationsWereMet())
}
