//go:build integration

package repository

import (
	"context"
	"database/sql"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestUserRepositoryRepeatedDisabledRevokesPromptAuditOwnership(t *testing.T) {
	ctx := context.Background()
	user, countingSince := createPromptAuditBannedUser(t)
	repo := NewUserRepository(testEntClient(t), integrationDB)
	var pendingID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO prompt_audit_enforcement_notifications
		(user_id,epoch,merge_until,first_hit_at,last_hit_at,kind,hit_count,auto_banned)
		VALUES ($1,3,NOW(),NOW(),NOW(),'combination',1,true) RETURNING id`, user.ID).Scan(&pendingID))
	for revision := int64(9); revision <= 10; revision++ {
		// Repeating the already disabled status is an explicit administrator
		// action and must revoke audit ownership on every submission.
		require.NoError(t, repo.Update(ctx, user, service.UserUpdateFields{Status: true}))
		state := readPromptAuditUserState(t, user.ID)
		require.Equal(t, int64(3), state.epoch)
		require.Equal(t, revision, state.revision)
		require.False(t, state.ownedBanRevision.Valid)
		require.False(t, state.banEventID.Valid)
		require.Equal(t, countingSince, state.countingSince)
		stored, err := repo.GetByID(ctx, user.ID)
		require.NoError(t, err)
		require.Equal(t, service.StatusDisabled, stored.Status)
	}
	var sentAt sql.NullTime
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT sent_at FROM prompt_audit_enforcement_notifications WHERE id=$1`, pendingID).Scan(&sentAt))
	require.False(t, sentAt.Valid, "manual disabling does not reset the counting cycle")
}

func TestUserRepositoryActiveResetsPromptAuditCycleAndPendingNotifications(t *testing.T) {
	ctx := context.Background()
	user, countingSince := createPromptAuditBannedUser(t)
	repo := NewUserRepository(testEntClient(t), integrationDB)
	for _, epoch := range []int64{2, 3} {
		_, err := integrationDB.ExecContext(ctx, `INSERT INTO prompt_audit_enforcement_notifications
			(user_id,epoch,merge_until,first_hit_at,last_hit_at,kind,claimed_at,claimed_by)
			VALUES ($1,$2,NOW(),NOW(),NOW(),'jailbreak',NOW(),'old-worker')`, user.ID, epoch)
		require.NoError(t, err)
	}
	var sentID, hitID int64
	sentTime := countingSince.Add(time.Hour)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO prompt_audit_enforcement_notifications
		(user_id,epoch,merge_until,first_hit_at,last_hit_at,kind,sent_at)
		VALUES ($1,1,NOW(),NOW(),NOW(),'jailbreak',$2) RETURNING id`, user.ID, sentTime).Scan(&sentID))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO prompt_audit_enforcement_hits
		(user_id,epoch,request_key,kind,counted,violation_count)
		VALUES ($1,3,'previous-cycle-hit','jailbreak',true,10) RETURNING id`, user.ID).Scan(&hitID))
	user.Status = service.StatusActive
	start := time.Now().UTC().Add(-time.Second)
	require.NoError(t, repo.Update(ctx, user, service.UserUpdateFields{Status: true}))
	state := readPromptAuditUserState(t, user.ID)
	require.Equal(t, int64(4), state.epoch)
	require.Equal(t, int64(9), state.revision)
	require.False(t, state.ownedBanRevision.Valid)
	require.False(t, state.banEventID.Valid)
	require.True(t, state.countingSince.After(start))
	require.True(t, state.countingSince.After(countingSince))
	stored, err := repo.GetByID(ctx, user.ID)
	require.NoError(t, err)
	require.Equal(t, service.StatusActive, stored.Status)
	var cancelled, pending int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FILTER (WHERE sent_at IS NOT NULL AND claimed_at IS NULL AND last_error='cycle_reset'),
		count(*) FILTER (WHERE sent_at IS NULL) FROM prompt_audit_enforcement_notifications WHERE user_id=$1 AND epoch IN (2,3)`, user.ID).Scan(&cancelled, &pending))
	require.Equal(t, 2, cancelled)
	require.Zero(t, pending)
	var originalSent time.Time
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT sent_at FROM prompt_audit_enforcement_notifications WHERE id=$1`, sentID).Scan(&originalSent))
	require.Equal(t, sentTime, originalSent)
	var historicalEpoch int64
	var counted bool
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT epoch,counted FROM prompt_audit_enforcement_hits WHERE id=$1`, hitID).Scan(&historicalEpoch, &counted))
	require.Equal(t, int64(3), historicalEpoch)
	require.True(t, counted, "cycle reset preserves historical evidence under its original epoch")
}

func TestUserRepositoryProfileUpdatePreservesPromptAuditState(t *testing.T) {
	ctx := context.Background()
	user, _ := createPromptAuditBannedUser(t)
	repo := NewUserRepository(testEntClient(t), integrationDB)
	_, err := integrationDB.ExecContext(ctx, `INSERT INTO prompt_audit_enforcement_notifications
		(user_id,epoch,merge_until,first_hit_at,last_hit_at,kind)
		VALUES ($1,3,NOW(),NOW(),NOW(),'combination')`, user.ID)
	require.NoError(t, err)
	beforeState, beforeNotification := promptAuditStatusRows(t, user.ID)
	// The caller's status can be stale. Only explicitly requested profile fields
	// may change; this must not relinquish the automatic penalty's ownership.
	user.Status = service.StatusActive
	user.Username = "profile-after-ban"
	user.Notes = "profile edit"
	require.NoError(t, repo.Update(ctx, user, service.UserUpdateFields{Username: true, Notes: true}))
	stored, err := repo.GetByID(ctx, user.ID)
	require.NoError(t, err)
	require.Equal(t, service.StatusDisabled, stored.Status)
	require.Equal(t, user.Username, stored.Username)
	afterState, afterNotification := promptAuditStatusRows(t, user.ID)
	require.JSONEq(t, beforeState, afterState)
	require.JSONEq(t, beforeNotification, afterNotification)
}

func TestUserRepositoryPromptAuditStatusResetRollsBackWithCallerTransaction(t *testing.T) {
	ctx := context.Background()
	user, _ := createPromptAuditBannedUser(t)
	repo := NewUserRepository(testEntClient(t), integrationDB)
	before := readPromptAuditUserState(t, user.ID)
	tx, err := testEntClient(t).Tx(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })
	user.Status = service.StatusActive
	require.NoError(t, repo.Update(dbent.NewTxContext(ctx, tx), user, service.UserUpdateFields{Status: true}))
	require.NoError(t, tx.Rollback())
	stored, err := repo.GetByID(ctx, user.ID)
	require.NoError(t, err)
	require.Equal(t, service.StatusDisabled, stored.Status)
	require.Equal(t, before, readPromptAuditUserState(t, user.ID))
}

func createPromptAuditBannedUser(t *testing.T) (*service.User, time.Time) {
	t.Helper()
	user := mustCreateUser(t, testEntClient(t), &service.User{Status: service.StatusDisabled})
	t.Cleanup(func() {
		_, err := integrationDB.Exec(`DELETE FROM users WHERE id=$1`, user.ID)
		require.NoError(t, err)
	})
	// Seed the persisted result of an automatic penalty. The update under test
	// always uses the real user repository, not the enforcement SQL helper.
	countingSince := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	_, err := integrationDB.Exec(`INSERT INTO prompt_audit_enforcement_states
		(user_id,epoch,revision,owned_ban_revision,ban_event_id,counting_since)
		VALUES ($1,3,8,8,99,$2)`, user.ID, countingSince)
	require.NoError(t, err)
	return user, countingSince
}

type promptAuditUserState struct {
	epoch, revision              int64
	ownedBanRevision, banEventID sql.NullInt64
	countingSince                time.Time
}

func readPromptAuditUserState(t *testing.T, userID int64) promptAuditUserState {
	t.Helper()
	var state promptAuditUserState
	require.NoError(t, integrationDB.QueryRow(`SELECT epoch,revision,owned_ban_revision,ban_event_id,counting_since
		FROM prompt_audit_enforcement_states WHERE user_id=$1`, userID).
		Scan(&state.epoch, &state.revision, &state.ownedBanRevision, &state.banEventID, &state.countingSince))
	return state
}

func promptAuditStatusRows(t *testing.T, userID int64) (string, string) {
	t.Helper()
	var state, notifications string
	require.NoError(t, integrationDB.QueryRow(`SELECT row_to_json(s)::text FROM prompt_audit_enforcement_states s WHERE user_id=$1`, userID).Scan(&state))
	require.NoError(t, integrationDB.QueryRow(`SELECT json_agg(n ORDER BY id)::text FROM prompt_audit_enforcement_notifications n WHERE user_id=$1`, userID).Scan(&notifications))
	return state, notifications
}
