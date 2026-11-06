//go:build unit

package repository

import (
	"context"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestCodexEngineBillingStoreAckNeedsLiveClaimAndCommittedLedger(t *testing.T) {
	for _, tc := range []struct {
		name string
		live bool
		rows int64
		want error
	}{
		{name: "acknowledged", live: true, rows: 1},
		{name: "uncommitted or mismatched event", live: true, want: service.ErrCodexEngineBillingConflict},
		{name: "expired claim", want: service.ErrCodexEngineBillingClaimLost},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, mock := engineBillingUnitStore(t)
			mock.ExpectBegin()
			rows := sqlmock.NewRows([]string{"id"})
			if tc.live {
				rows.AddRow(int64(1))
			}
			mock.ExpectQuery(`SELECT id FROM codex_engine_billing_jobs.*FOR UPDATE`).WithArgs(int64(1), "claim").WillReturnRows(rows)
			if tc.live {
				mock.ExpectExec(`UPDATE codex_engine_billing_received_events e SET settled_at=.*FROM codex_engine_billing_events ledger.*ledger.usage_log_id IS NOT NULL`).
					WithArgs(int64(1), "event-1", strings.Repeat("a", 64), int64(1)).WillReturnResult(sqlmock.NewResult(0, tc.rows))
			}
			if tc.want == nil {
				mock.ExpectCommit()
			} else {
				mock.ExpectRollback()
			}
			err := r.AckReceivedSettled(context.Background(), 1, "claim", "event-1", strings.Repeat("a", 64), 1)
			require.ErrorIs(t, err, tc.want)
		})
	}
}
