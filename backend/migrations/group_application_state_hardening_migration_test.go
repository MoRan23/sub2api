package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGroupApplicationStateHardeningMigrationPreservesGrantOwnership(t *testing.T) {
	content, err := FS.ReadFile("233_group_application_state_hardening.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS access_grant_owned BOOLEAN NOT NULL DEFAULT FALSE")
	require.Contains(t, sql, "application.status = 'completed'")
	require.Contains(t, sql, "allowed.created_at = application.completed_at")
	require.Contains(t, sql, "SET access_grant_owned = TRUE")
	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS claim_expires_at TIMESTAMPTZ")
	require.Contains(t, sql, "group_applications_one_open_or_completed_per_user_group")
	require.Contains(t, sql, "status IN ('pending', 'awaiting_reply', 'completed')")
	require.Contains(t, sql, "group_application_mail_outbox_one_active_approval")
	require.Contains(t, sql, "kind = 'approval' AND status IN ('pending', 'processing')")
}
