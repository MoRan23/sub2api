//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	dbmigrations "github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

func TestMigration234BackfillsHistoricalBatchImageHoldTerminals(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()
	migrationSQL, err := dbmigrations.FS.ReadFile("234_add_gift_balance.sql")
	require.NoError(t, err)

	baseAPIKeyID := time.Now().UnixNano()
	type migrationCase struct {
		name             string
		holdArchived     bool
		captureArchived  *bool
		releaseArchived  *bool
		expectedTerminal string
	}
	hot := false
	archived := true
	cases := []migrationCase{
		{name: "hot_hold_hot_capture", captureArchived: &hot, expectedTerminal: "captured"},
		{name: "hot_hold_archive_release", releaseArchived: &archived, expectedTerminal: "released"},
		{name: "archive_hold_hot_capture", holdArchived: true, captureArchived: &hot, expectedTerminal: "captured"},
		{name: "archive_hold_archive_release", holdArchived: true, releaseArchived: &archived, expectedTerminal: "released"},
		{name: "pending_hold", expectedTerminal: ""},
		{
			name:             "capture_wins_corrupt_dual_terminal",
			captureArchived:  &archived,
			releaseArchived:  &hot,
			expectedTerminal: "captured",
		},
	}

	for i, tc := range cases {
		apiKeyID := baseAPIKeyID + int64(i)
		batchID := fmt.Sprintf("migration-234-%d-%s", baseAPIKeyID, tc.name)
		insertUsageBillingMigrationReceipt(t, tx, tc.holdArchived, "batch_image_hold:"+batchID, apiKeyID)
		if tc.captureArchived != nil {
			insertUsageBillingMigrationReceipt(t, tx, *tc.captureArchived, "batch_image_capture:"+batchID, apiKeyID)
		}
		if tc.releaseArchived != nil {
			insertUsageBillingMigrationReceipt(t, tx, *tc.releaseArchived, "batch_image_release:"+batchID, apiKeyID)
		}
	}

	_, err = tx.ExecContext(ctx, string(migrationSQL))
	require.NoError(t, err)

	for i, tc := range cases {
		apiKeyID := baseAPIKeyID + int64(i)
		batchID := fmt.Sprintf("migration-234-%d-%s", baseAPIKeyID, tc.name)
		table := "usage_billing_dedup"
		if tc.holdArchived {
			table = "usage_billing_dedup_archive"
		}
		var terminal string
		err := tx.QueryRowContext(ctx, `
			SELECT hold_terminal_kind
			FROM `+table+`
			WHERE request_id = $1 AND api_key_id = $2
		`, "batch_image_hold:"+batchID, apiKeyID).Scan(&terminal)
		require.NoError(t, err, tc.name)
		require.Equal(t, tc.expectedTerminal, terminal, tc.name)
	}
}

func insertUsageBillingMigrationReceipt(t *testing.T, tx sqlExecutor, archived bool, requestID string, apiKeyID int64) {
	t.Helper()
	table := "usage_billing_dedup"
	columns := "request_id, api_key_id, request_fingerprint"
	values := "$1, $2, $3"
	if archived {
		table = "usage_billing_dedup_archive"
		columns += ", created_at"
		values += ", NOW()"
	}
	_, err := tx.ExecContext(context.Background(),
		"INSERT INTO "+table+" ("+columns+") VALUES ("+values+")",
		requestID, apiKeyID, "2342342342342342342342342342342342342342342342342342342342342342",
	)
	require.NoError(t, err)
}
