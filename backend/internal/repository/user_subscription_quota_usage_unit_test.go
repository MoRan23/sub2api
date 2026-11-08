//go:build unit

package repository

import (
	"context"
	"math"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestConsumeTotalQuotaSubscriptionUsage_PreservesLedgerPrecision(t *testing.T) {
	for _, tc := range []struct {
		name       string
		total      string
		used       string
		cost       float64
		firstDebit string
		lastDebit  string
	}{
		{name: "smallest stored balance", total: "1", used: "0.9999999999", cost: 1, firstDebit: "0.0000000001", lastDebit: "0.9999999999"},
		{name: "large historical balance retains its final digit", total: "9999999999", used: "9999999998.9999999999", cost: 1, firstDebit: "0.0000000001", lastDebit: "0.9999999999"},
		{name: "smallest stored charge", total: "1", used: "0", cost: 1e-10, firstDebit: "0.0000000001"},
		{name: "half storage unit rounds once", total: "1", used: "0", cost: 1.5e-10, firstDebit: "0.0000000002"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, mock, tx := beginUsageBillingUnitTx(t)
			mock.ExpectQuery(`(?s)SELECT id, quota_total_usd, quota_used_usd.*FOR UPDATE`).
				WithArgs(int64(42), sqlmock.AnyArg()).
				WillReturnRows(sqlmock.NewRows([]string{"id", "quota_total_usd", "quota_used_usd"}).
					AddRow(int64(1), tc.total, tc.used).
					AddRow(int64(2), "1", "1.25"))
			mock.ExpectExec(`(?s)UPDATE user_subscription_quota_events.*quota_used_usd = quota_used_usd \+ \$1`).
				WithArgs(tc.firstDebit, int64(1)).WillReturnResult(sqlmock.NewResult(0, 1))
			if tc.lastDebit != "" {
				// The already-overdrawn last batch retains the overflow charge.
				mock.ExpectExec(`(?s)UPDATE user_subscription_quota_events.*quota_used_usd = quota_used_usd \+ \$1`).
					WithArgs(tc.lastDebit, int64(2)).WillReturnResult(sqlmock.NewResult(0, 1))
			}
			mock.ExpectCommit()
			require.NoError(t, consumeTotalQuotaSubscriptionUsage(context.Background(), tx, 42, tc.cost))
			require.NoError(t, tx.Commit())
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestConsumeTotalQuotaSubscriptionUsage_RejectsNonFiniteCost(t *testing.T) {
	for _, cost := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		require.Error(t, consumeTotalQuotaSubscriptionUsage(context.Background(), nil, 42, cost))
	}
}
