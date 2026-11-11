//go:build integration

package repository

import (
	"context"
	"testing"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/securityaudit"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestBatchDisableUsersRevokesPromptOwnershipForRepeatedDisabled(t *testing.T) {
	ctx := context.Background()
	user, countingSince := createPromptAuditBannedUser(t)
	repo := NewUserRepository(testEntClient(t), integrationDB).(service.UserEmailBlacklistRepository)
	auditRepo := securityaudit.NewPostgreSQLRepository(integrationDB)
	for revision := int64(9); revision <= 10; revision++ {
		disabled, err := repo.BatchDisableUsers(ctx, []int64{user.ID, user.ID})
		require.NoError(t, err)
		require.Empty(t, disabled, "preview reports actual status changes only")
		state := readPromptAuditUserState(t, user.ID)
		require.Equal(t, revision, state.revision, "duplicate input IDs are handled once")
		require.Equal(t, int64(3), state.epoch)
		require.Equal(t, countingSince, state.countingSince)
		require.False(t, state.ownedBanRevision.Valid)
		require.False(t, state.banEventID.Valid)
		_, err = auditRepo.UnbanPromptUser(ctx, user.ID)
		require.ErrorIs(t, err, securityaudit.ErrEnforcementUnbanOwnership,
			"a historical prompt audit must not restore an administrator-disabled user")
	}
	var status string
	require.NoError(t, integrationDB.QueryRow(`SELECT status FROM users WHERE id=$1`, user.ID).Scan(&status))
	require.Equal(t, service.StatusDisabled, status)
}

func TestBatchDisableUsersRevokesPromptOwnershipOnlyForEligibleTargets(t *testing.T) {
	ctx := context.Background()
	active, _ := createPromptAuditBannedUser(t)
	already, _ := createPromptAuditBannedUser(t)
	admin, _ := createPromptAuditBannedUser(t)
	deleted, _ := createPromptAuditBannedUser(t)
	_, err := integrationDB.Exec(`UPDATE users SET status='active' WHERE id=$1`, active.ID)
	require.NoError(t, err)
	_, err = integrationDB.Exec(`UPDATE users SET role='admin',status='active' WHERE id=$1`, admin.ID)
	require.NoError(t, err)
	_, err = integrationDB.Exec(`UPDATE users SET deleted_at=NOW() WHERE id=$1`, deleted.ID)
	require.NoError(t, err)
	adminBefore := readPromptAuditUserState(t, admin.ID)
	deletedBefore := readPromptAuditUserState(t, deleted.ID)
	repo := NewUserRepository(testEntClient(t), integrationDB).(service.UserEmailBlacklistRepository)
	disabled, err := repo.BatchDisableUsers(ctx, []int64{deleted.ID, already.ID, admin.ID, active.ID, active.ID, -1})
	require.NoError(t, err)
	require.Equal(t, []int64{active.ID}, disabled)
	for _, userID := range []int64{active.ID, already.ID} {
		var status string
		require.NoError(t, integrationDB.QueryRow(`SELECT status FROM users WHERE id=$1`, userID).Scan(&status))
		require.Equal(t, service.StatusDisabled, status)
		state := readPromptAuditUserState(t, userID)
		require.Equal(t, int64(9), state.revision)
		require.False(t, state.ownedBanRevision.Valid)
		require.False(t, state.banEventID.Valid)
	}
	require.Equal(t, adminBefore, readPromptAuditUserState(t, admin.ID))
	require.Equal(t, deletedBefore, readPromptAuditUserState(t, deleted.ID))
	var adminStatus string
	require.NoError(t, integrationDB.QueryRow(`SELECT status FROM users WHERE id=$1`, admin.ID).Scan(&adminStatus))
	require.Equal(t, service.StatusActive, adminStatus)
}

func TestBatchDisableUsersPromptOwnershipRollbackWithCallerTransaction(t *testing.T) {
	ctx := context.Background()
	user, _ := createPromptAuditBannedUser(t)
	_, err := integrationDB.Exec(`UPDATE users SET status='active' WHERE id=$1`, user.ID)
	require.NoError(t, err)
	before := readPromptAuditUserState(t, user.ID)
	repo := NewUserRepository(testEntClient(t), integrationDB).(service.UserEmailBlacklistRepository)
	tx, err := testEntClient(t).Tx(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })
	disabled, err := repo.BatchDisableUsers(dbent.NewTxContext(ctx, tx), []int64{user.ID})
	require.NoError(t, err)
	require.Equal(t, []int64{user.ID}, disabled)
	inTx, err := tx.Client().User.Get(ctx, user.ID)
	require.NoError(t, err)
	require.Equal(t, service.StatusDisabled, inTx.Status)
	require.NoError(t, tx.Rollback())
	var status string
	require.NoError(t, integrationDB.QueryRow(`SELECT status FROM users WHERE id=$1`, user.ID).Scan(&status))
	require.Equal(t, service.StatusActive, status)
	require.Equal(t, before, readPromptAuditUserState(t, user.ID))
}
