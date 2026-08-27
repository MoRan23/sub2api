package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func groupApplicationRows(status string) *sqlmock.Rows {
	now := time.Date(2026, time.August, 28, 12, 0, 0, 0, time.UTC)
	templates, _ := json.Marshal(service.DefaultGroupApplicationTemplates())
	return sqlmock.NewRows([]string{
		"id", "user_id", "user_email", "group_id", "group_name", "contact_email", "reason", "locale", "status",
		"reply_phrase_snapshot", "templates_snapshot", "attachment_id", "attachment_name", "reviewed_by", "reviewed_at",
		"decision_reason", "completed_at", "revoked_by", "revoked_at", "last_email_kind", "last_email_status",
		"last_email_error", "created_at", "updated_at",
	}).AddRow(
		int64(41), int64(9), "user@example.com", int64(4), "Private", "applicant@example.com", "reason", "en",
		status, "CONFIRM", templates, int64(3), "agreement.pdf", nil, nil, "", nil, nil, nil, "", "", "", now, now,
	)
}

func terminalMailJob(kind, requiredStatus string) service.GroupApplicationMailJob {
	return service.GroupApplicationMailJob{
		Kind: kind, Recipient: "applicant@example.com", Subject: kind, HTMLBody: "<p>status</p>",
		MessageID: "<" + kind + "@sub2api.local>", RequiredStatus: requiredStatus,
	}
}

func expectApplicationTransitionLock(mock sqlmock.Sqlmock, status string) {
	mock.ExpectQuery(`SELECT user_id,group_id,status FROM group_applications WHERE id=\$1 FOR UPDATE`).
		WithArgs(int64(41)).
		WillReturnRows(sqlmock.NewRows([]string{"user_id", "group_id", "status"}).AddRow(int64(9), int64(4), status))
}

func expectMailQuiesced(mock sqlmock.Sqlmock, activeProcessing bool) {
	mock.ExpectExec(`(?s)UPDATE group_application_mail_outbox.*claim_expires_at.*application_id=\$1`).
		WithArgs(int64(41)).
		WillReturnResult(sqlmock.NewResult(0, 3))
	activeRows := sqlmock.NewRows([]string{"id"})
	if activeProcessing {
		activeRows.AddRow(int64(77))
	}
	mock.ExpectQuery(`(?s)SELECT id.*status='processing'.*FOR UPDATE`).
		WithArgs(int64(41)).
		WillReturnRows(activeRows)
}

func expectGetApplication(mock sqlmock.Sqlmock, status string) {
	mock.ExpectQuery(`(?s)SELECT a\.id.*FROM group_applications a.*WHERE a\.id=\$1`).
		WithArgs(int64(41)).
		WillReturnRows(groupApplicationRows(status))
}

func expectGroupApplicationUserLock(mock sqlmock.Sqlmock, userID int64) {
	mock.ExpectQuery(`SELECT id FROM users WHERE id=\$1 AND deleted_at IS NULL FOR UPDATE`).
		WithArgs(userID).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(userID))
}

func expectMailApplicationLock(mock sqlmock.Sqlmock, outboxID, applicationID int64) {
	mock.ExpectQuery(`(?s)SELECT a\.id.*FROM group_application_mail_outbox o.*JOIN group_applications a.*WHERE o\.id=\$1.*FOR UPDATE OF a`).
		WithArgs(outboxID).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(applicationID))
}

func TestGroupApplicationRepositoryCreateRejectsExistingApplicationOrAuthorization(t *testing.T) {
	repo, mock := newGroupApplicationOutboxTestRepository(t)
	application := &service.GroupApplication{UserID: 9, GroupID: 4, ContactEmail: "applicant@example.com", Reason: "reason", Locale: "en"}
	mock.ExpectBegin()
	expectGroupApplicationUserLock(mock, application.UserID)
	mock.ExpectQuery(`(?s)INSERT INTO group_applications.*NOT EXISTS.*group_applications existing.*NOT EXISTS.*user_allowed_groups allowed`).
		WithArgs(application.UserID, application.GroupID, application.ContactEmail, application.Reason, application.Locale).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectQuery(`(?s)SELECT.*EXISTS.*group_applications.*OR EXISTS.*user_allowed_groups`).
		WithArgs(application.UserID, application.GroupID).
		WillReturnRows(sqlmock.NewRows([]string{"conflict"}).AddRow(true))
	mock.ExpectRollback()

	created, err := repo.CreateApplication(context.Background(), application)

	require.Nil(t, created)
	require.ErrorIs(t, err, service.ErrGroupApplicationConflict)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGroupApplicationRepositoryCreateLocksUserAndCommitsBeforeReadback(t *testing.T) {
	repo, mock := newGroupApplicationOutboxTestRepository(t)
	application := &service.GroupApplication{UserID: 9, GroupID: 4, ContactEmail: "applicant@example.com", Reason: "reason", Locale: "en"}
	mock.ExpectBegin()
	expectGroupApplicationUserLock(mock, application.UserID)
	mock.ExpectQuery(`(?s)INSERT INTO group_applications.*RETURNING id`).
		WithArgs(application.UserID, application.GroupID, application.ContactEmail, application.Reason, application.Locale).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(41)))
	mock.ExpectCommit()
	mock.ExpectQuery(`(?s)SELECT a\.id.*FROM group_applications a.*WHERE a\.user_id=\$1 AND a\.id=\$2`).
		WithArgs(application.UserID, int64(41)).
		WillReturnRows(groupApplicationRows(service.GroupApplicationStatusPending))

	created, err := repo.CreateApplication(context.Background(), application)

	require.NoError(t, err)
	require.Equal(t, int64(41), created.ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGroupApplicationRepositoryCreateRejectsMissingOrSoftDeletedUser(t *testing.T) {
	repo, mock := newGroupApplicationOutboxTestRepository(t)
	application := &service.GroupApplication{UserID: 9, GroupID: 4, ContactEmail: "applicant@example.com", Reason: "reason", Locale: "en"}
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id FROM users WHERE id=\$1 AND deleted_at IS NULL FOR UPDATE`).
		WithArgs(application.UserID).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()

	created, err := repo.CreateApplication(context.Background(), application)

	require.Nil(t, created)
	require.ErrorIs(t, err, service.ErrGroupApplicationUnavailable)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGroupApplicationRepositoryTerminalTransitionCancelsQueuedApprovalMails(t *testing.T) {
	repo, mock := newGroupApplicationOutboxTestRepository(t)
	mail := terminalMailJob(service.GroupApplicationMailManualRejection, service.GroupApplicationStatusRejected)
	mock.ExpectBegin()
	expectApplicationTransitionLock(mock, service.GroupApplicationStatusAwaitingReply)
	expectMailQuiesced(mock, false)
	mock.ExpectExec(`UPDATE group_applications SET status='rejected'`).
		WithArgs(int64(41), int64(5), "reason").
		WillReturnResult(sqlmock.NewResult(0, 1))
	expectMailInsert(mock, 41, mail)
	expectGetApplication(mock, service.GroupApplicationStatusRejected)
	mock.ExpectCommit()

	application, err := repo.Reject(context.Background(), 41, 5, "reason", mail)

	require.NoError(t, err)
	require.Equal(t, service.GroupApplicationStatusRejected, application.Status)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGroupApplicationRepositoryTerminalTransitionWaitsForValidatedDelivery(t *testing.T) {
	repo, mock := newGroupApplicationOutboxTestRepository(t)
	mail := terminalMailJob(service.GroupApplicationMailManualRejection, service.GroupApplicationStatusRejected)
	mock.ExpectBegin()
	expectApplicationTransitionLock(mock, service.GroupApplicationStatusAwaitingReply)
	expectMailQuiesced(mock, true)
	mock.ExpectRollback()

	application, err := repo.Reject(context.Background(), 41, 5, "reason", mail)

	require.Nil(t, application)
	require.ErrorIs(t, err, service.ErrGroupApplicationMailDeliveryInProgress)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGroupApplicationRepositoryCompleteTracksWhetherItCreatedAuthorization(t *testing.T) {
	for _, test := range []struct {
		name     string
		inserted int64
		owned    bool
	}{
		{name: "new authorization", inserted: 1, owned: true},
		{name: "preexisting authorization", inserted: 0, owned: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo, mock := newGroupApplicationOutboxTestRepository(t)
			mail := terminalMailJob(service.GroupApplicationMailCompletion, service.GroupApplicationStatusCompleted)
			mock.ExpectBegin()
			expectApplicationTransitionLock(mock, service.GroupApplicationStatusAwaitingReply)
			expectMailQuiesced(mock, false)
			mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM groups`).
				WithArgs(int64(4)).
				WillReturnRows(sqlmock.NewRows([]string{"eligible"}).AddRow(true))
			mock.ExpectExec(`INSERT INTO user_allowed_groups`).
				WithArgs(int64(9), int64(4)).
				WillReturnResult(sqlmock.NewResult(0, test.inserted))
			mock.ExpectExec(`UPDATE group_applications SET access_grant_owned=\$2 WHERE id=\$1`).
				WithArgs(int64(41), test.owned).
				WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectExec(`UPDATE group_applications SET status='completed'`).
				WithArgs(int64(41)).
				WillReturnResult(sqlmock.NewResult(0, 1))
			expectMailInsert(mock, 41, mail)
			expectGetApplication(mock, service.GroupApplicationStatusCompleted)
			mock.ExpectCommit()

			application, err := repo.CompleteFromReply(context.Background(), 41, mail, nil)

			require.NoError(t, err)
			require.Equal(t, service.GroupApplicationStatusCompleted, application.Status)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestGroupApplicationRepositoryCompleteRollsBackWhenReceiptStoreFails(t *testing.T) {
	repo, mock := newGroupApplicationOutboxTestRepository(t)
	mail := terminalMailJob(service.GroupApplicationMailCompletion, service.GroupApplicationStatusCompleted)
	applicationID := int64(41)
	receipt := service.GroupApplicationReceipt{
		MailboxFingerprint: "mailbox", UIDValidity: 12, UID: 34,
		MessageID: "<reply@example.com>", FromAddress: "applicant@example.com",
		InReplyTo: "<approval@example.com>", References: "<approval@example.com>",
		ApplicationID: &applicationID, Result: "completed", ReplySHA256: "digest",
		EncryptedContent: "ciphertext",
	}
	receiptErr := errors.New("store receipt failed")
	mock.ExpectBegin()
	expectApplicationTransitionLock(mock, service.GroupApplicationStatusAwaitingReply)
	expectMailQuiesced(mock, false)
	mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM groups`).
		WithArgs(int64(4)).
		WillReturnRows(sqlmock.NewRows([]string{"eligible"}).AddRow(true))
	mock.ExpectExec(`INSERT INTO user_allowed_groups`).
		WithArgs(int64(9), int64(4)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE group_applications SET access_grant_owned=\$2 WHERE id=\$1`).
		WithArgs(applicationID, true).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE group_applications SET status='completed'`).
		WithArgs(applicationID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	expectMailInsert(mock, applicationID, mail)
	mock.ExpectExec(`INSERT INTO group_application_inbound_receipts`).
		WithArgs(
			receipt.MailboxFingerprint, int64(receipt.UIDValidity), int64(receipt.UID), receipt.MessageID,
			receipt.FromAddress, receipt.InReplyTo, receipt.References, applicationID,
			receipt.Result, receipt.ReplySHA256, receipt.EncryptedContent, receipt.ContentTruncated,
		).
		WillReturnError(receiptErr)
	mock.ExpectRollback()

	application, err := repo.CompleteFromReply(context.Background(), applicationID, mail, &receipt)

	require.Nil(t, application)
	require.ErrorIs(t, err, receiptErr)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGroupApplicationRepositoryRevokeDeletesOnlyOwnedAuthorization(t *testing.T) {
	repo, mock := newGroupApplicationOutboxTestRepository(t)
	mail := terminalMailJob(service.GroupApplicationMailRevocation, service.GroupApplicationStatusRevoked)
	mock.ExpectBegin()
	expectApplicationTransitionLock(mock, service.GroupApplicationStatusCompleted)
	expectMailQuiesced(mock, false)
	mock.ExpectExec(`(?s)DELETE FROM user_allowed_groups allowed.*application\.id=\$3.*application\.access_grant_owned=TRUE.*allowed\.created_at=application\.completed_at`).
		WithArgs(int64(9), int64(4), int64(41)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE group_applications SET status='revoked'.*access_grant_owned=FALSE`).
		WithArgs(int64(41), int64(5), "reason").
		WillReturnResult(sqlmock.NewResult(0, 1))
	expectMailInsert(mock, 41, mail)
	expectGetApplication(mock, service.GroupApplicationStatusRevoked)
	mock.ExpectCommit()

	application, err := repo.Revoke(context.Background(), 41, 5, "reason", mail)

	require.NoError(t, err)
	require.Equal(t, service.GroupApplicationStatusRevoked, application.Status)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGroupApplicationRepositoryRevokeRejectsAuthorizationOwnedElsewhere(t *testing.T) {
	repo, mock := newGroupApplicationOutboxTestRepository(t)
	mail := terminalMailJob(service.GroupApplicationMailRevocation, service.GroupApplicationStatusRevoked)
	mock.ExpectBegin()
	expectApplicationTransitionLock(mock, service.GroupApplicationStatusCompleted)
	expectMailQuiesced(mock, false)
	mock.ExpectExec(`(?s)DELETE FROM user_allowed_groups allowed.*application\.id=\$3.*allowed\.created_at=application\.completed_at`).
		WithArgs(int64(9), int64(4), int64(41)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM user_allowed_groups WHERE user_id=\$1 AND group_id=\$2\)`).
		WithArgs(int64(9), int64(4)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectRollback()

	application, err := repo.Revoke(context.Background(), 41, 5, "reason", mail)

	require.Nil(t, application)
	require.ErrorIs(t, err, service.ErrGroupApplicationAccessNotOwned)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGroupApplicationRepositoryRevokePreservesRecreatedAuthorization(t *testing.T) {
	repo, mock := newGroupApplicationOutboxTestRepository(t)
	mail := terminalMailJob(service.GroupApplicationMailRevocation, service.GroupApplicationStatusRevoked)
	mock.ExpectBegin()
	expectApplicationTransitionLock(mock, service.GroupApplicationStatusCompleted)
	expectMailQuiesced(mock, false)
	// access_grant_owned may still be true after the original grant was deleted.
	// The replacement has a different created_at and must not be removed.
	mock.ExpectExec(`(?s)DELETE FROM user_allowed_groups allowed.*application\.access_grant_owned=TRUE.*allowed\.created_at=application\.completed_at`).
		WithArgs(int64(9), int64(4), int64(41)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM user_allowed_groups WHERE user_id=\$1 AND group_id=\$2\)`).
		WithArgs(int64(9), int64(4)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectRollback()

	application, err := repo.Revoke(context.Background(), 41, 5, "reason", mail)

	require.Nil(t, application)
	require.ErrorIs(t, err, service.ErrGroupApplicationAccessNotOwned)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGroupApplicationRepositoryRetryApprovalConflictsWithAnotherActiveDelivery(t *testing.T) {
	repo, mock := newGroupApplicationOutboxTestRepository(t)
	mock.ExpectBegin()
	expectApplicationStatusLock(mock, 41, service.GroupApplicationStatusAwaitingReply)
	mock.ExpectQuery(`(?s)SELECT kind,status,required_application_status.*FOR UPDATE`).
		WithArgs(int64(71), int64(41)).
		WillReturnRows(sqlmock.NewRows([]string{"kind", "status", "required_application_status"}).
			AddRow(service.GroupApplicationMailApproval, "failed", service.GroupApplicationStatusAwaitingReply))
	mock.ExpectQuery(`(?s)SELECT EXISTS.*id<>\$3.*status IN \('pending','processing'\)`).
		WithArgs(int64(41), service.GroupApplicationStatusAwaitingReply, int64(71)).
		WillReturnRows(sqlmock.NewRows([]string{"active"}).AddRow(true))
	mock.ExpectRollback()

	err := repo.RetryMail(context.Background(), 41, 71)

	require.ErrorIs(t, err, service.ErrGroupApplicationMailDeliveryInProgress)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGroupApplicationRepositoryValidateMailClaimRefreshesLease(t *testing.T) {
	repo, mock := newGroupApplicationOutboxTestRepository(t)
	mock.ExpectQuery(`(?s)UPDATE group_application_mail_outbox o.*claim_expires_at=NOW\(\)\+INTERVAL '1 minute'.*RETURNING o\.id`).
		WithArgs(int64(71), "worker-1").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(71)))

	require.NoError(t, repo.ValidateMailClaim(context.Background(), 71, "worker-1"))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGroupApplicationRepositoryValidateMailClaimCancelsStateMismatch(t *testing.T) {
	repo, mock := newGroupApplicationOutboxTestRepository(t)
	mock.ExpectQuery(`(?s)UPDATE group_application_mail_outbox o.*RETURNING o\.id`).
		WithArgs(int64(71), "worker-1").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec(`(?s)UPDATE group_application_mail_outbox o.*status='cancelled'.*a\.status<>o\.required_application_status`).
		WithArgs(int64(71), "worker-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	err := repo.ValidateMailClaim(context.Background(), 71, "worker-1")

	require.ErrorIs(t, err, service.ErrGroupApplicationState)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGroupApplicationRepositoryValidateMailClaimTreatsStolenOwnerAsStateConflict(t *testing.T) {
	repo, mock := newGroupApplicationOutboxTestRepository(t)
	mock.ExpectQuery(`(?s)UPDATE group_application_mail_outbox o.*RETURNING o\.id`).
		WithArgs(int64(71), "worker-1").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec(`(?s)UPDATE group_application_mail_outbox o.*status='cancelled'.*a\.status<>o\.required_application_status`).
		WithArgs(int64(71), "worker-1").
		WillReturnResult(sqlmock.NewResult(0, 0))

	err := repo.ValidateMailClaim(context.Background(), 71, "worker-1")

	require.ErrorIs(t, err, service.ErrGroupApplicationState)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGroupApplicationRepositoryMarkMailSentDoesNotFailWhenSummaryStateChanged(t *testing.T) {
	repo, mock := newGroupApplicationOutboxTestRepository(t)
	mock.ExpectBegin()
	expectMailApplicationLock(mock, 71, 41)
	mock.ExpectQuery(`(?s)UPDATE group_application_mail_outbox.*status='sent'.*RETURNING application_id,kind,required_application_status`).
		WithArgs(int64(71), "worker-1").
		WillReturnRows(sqlmock.NewRows([]string{"application_id", "kind", "required_application_status"}).
			AddRow(int64(41), service.GroupApplicationMailApproval, service.GroupApplicationStatusAwaitingReply))
	mock.ExpectExec(`(?s)UPDATE group_applications.*last_email_status='sent'.*status=\$3`).
		WithArgs(int64(41), service.GroupApplicationMailApproval, service.GroupApplicationStatusAwaitingReply).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	require.NoError(t, repo.MarkMailSent(context.Background(), 71, "worker-1"))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGroupApplicationRepositoryMarkMailSentRollsBackWhenSummaryUpdateFails(t *testing.T) {
	repo, mock := newGroupApplicationOutboxTestRepository(t)
	summaryErr := errors.New("summary update failed")
	mock.ExpectBegin()
	expectMailApplicationLock(mock, 71, 41)
	mock.ExpectQuery(`(?s)UPDATE group_application_mail_outbox.*status='sent'.*RETURNING application_id,kind,required_application_status`).
		WithArgs(int64(71), "worker-1").
		WillReturnRows(sqlmock.NewRows([]string{"application_id", "kind", "required_application_status"}).
			AddRow(int64(41), service.GroupApplicationMailApproval, service.GroupApplicationStatusAwaitingReply))
	mock.ExpectExec(`(?s)UPDATE group_applications.*last_email_status='sent'.*status=\$3`).
		WithArgs(int64(41), service.GroupApplicationMailApproval, service.GroupApplicationStatusAwaitingReply).
		WillReturnError(summaryErr)
	mock.ExpectRollback()

	err := repo.MarkMailSent(context.Background(), 71, "worker-1")

	require.ErrorIs(t, err, summaryErr)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGroupApplicationRepositoryMarkMailSentRollsBackWhenClaimIsInvalid(t *testing.T) {
	repo, mock := newGroupApplicationOutboxTestRepository(t)
	mock.ExpectBegin()
	expectMailApplicationLock(mock, 71, 41)
	mock.ExpectQuery(`(?s)UPDATE group_application_mail_outbox.*status='sent'.*RETURNING application_id,kind,required_application_status`).
		WithArgs(int64(71), "worker-1").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()

	err := repo.MarkMailSent(context.Background(), 71, "worker-1")

	require.Error(t, err)
	require.Contains(t, err.Error(), "claim 71 is no longer valid")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGroupApplicationRepositoryTransitionReadsResultBeforeCommit(t *testing.T) {
	repo, mock := newGroupApplicationOutboxTestRepository(t)
	mail := approvalMailJob()
	mock.ExpectBegin()
	expectApplicationTransitionLock(mock, service.GroupApplicationStatusPending)
	mock.ExpectExec(`UPDATE group_applications SET status='awaiting_reply'`).
		WithArgs(int64(41), int64(5)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	expectMailInsert(mock, 41, mail)
	mock.ExpectQuery(`(?s)SELECT a\.id.*FROM group_applications a.*WHERE a\.id=\$1`).
		WithArgs(int64(41)).
		WillReturnError(sql.ErrConnDone)
	mock.ExpectRollback()

	application, err := repo.Approve(context.Background(), 41, 5, mail)

	require.Nil(t, application)
	require.ErrorIs(t, err, sql.ErrConnDone)
	require.NoError(t, mock.ExpectationsWereMet(), "a failed readback must roll back the transition instead of failing after commit")
}

func TestGroupApplicationRepositoryRetryClaimedMailDoesNotFailWhenSummaryStateChanged(t *testing.T) {
	repo, mock := newGroupApplicationOutboxTestRepository(t)
	retryAt := time.Date(2026, time.August, 28, 12, 1, 0, 0, time.UTC)
	mock.ExpectBegin()
	expectMailApplicationLock(mock, 71, 41)
	mock.ExpectQuery(`(?s)UPDATE group_application_mail_outbox.*status=\$3.*RETURNING application_id,kind,required_application_status`).
		WithArgs(int64(71), "worker-1", "pending", retryAt, "temporary").
		WillReturnRows(sqlmock.NewRows([]string{"application_id", "kind", "required_application_status"}).
			AddRow(int64(41), service.GroupApplicationMailApproval, service.GroupApplicationStatusAwaitingReply))
	mock.ExpectExec(`(?s)UPDATE group_applications.*last_email_status=\$3.*status=\$5`).
		WithArgs(int64(41), service.GroupApplicationMailApproval, "pending", "temporary", service.GroupApplicationStatusAwaitingReply).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	require.NoError(t, repo.RetryClaimedMail(context.Background(), 71, "worker-1", retryAt, false, "temporary"))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGroupApplicationRepositoryRetryClaimedMailRollsBackWhenSummaryUpdateFails(t *testing.T) {
	repo, mock := newGroupApplicationOutboxTestRepository(t)
	retryAt := time.Date(2026, time.August, 28, 12, 1, 0, 0, time.UTC)
	summaryErr := errors.New("summary update failed")
	mock.ExpectBegin()
	expectMailApplicationLock(mock, 71, 41)
	mock.ExpectQuery(`(?s)UPDATE group_application_mail_outbox.*status=\$3.*RETURNING application_id,kind,required_application_status`).
		WithArgs(int64(71), "worker-1", "pending", retryAt, "temporary").
		WillReturnRows(sqlmock.NewRows([]string{"application_id", "kind", "required_application_status"}).
			AddRow(int64(41), service.GroupApplicationMailApproval, service.GroupApplicationStatusAwaitingReply))
	mock.ExpectExec(`(?s)UPDATE group_applications.*last_email_status=\$3.*status=\$5`).
		WithArgs(int64(41), service.GroupApplicationMailApproval, "pending", "temporary", service.GroupApplicationStatusAwaitingReply).
		WillReturnError(summaryErr)
	mock.ExpectRollback()

	err := repo.RetryClaimedMail(context.Background(), 71, "worker-1", retryAt, false, "temporary")

	require.ErrorIs(t, err, summaryErr)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGroupApplicationRepositoryRetryClaimedMailRollsBackWhenClaimIsInvalid(t *testing.T) {
	repo, mock := newGroupApplicationOutboxTestRepository(t)
	retryAt := time.Date(2026, time.August, 28, 12, 1, 0, 0, time.UTC)
	mock.ExpectBegin()
	expectMailApplicationLock(mock, 71, 41)
	mock.ExpectQuery(`(?s)UPDATE group_application_mail_outbox.*status=\$3.*RETURNING application_id,kind,required_application_status`).
		WithArgs(int64(71), "worker-1", "pending", retryAt, "temporary").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()

	err := repo.RetryClaimedMail(context.Background(), 71, "worker-1", retryAt, false, "temporary")

	require.Error(t, err)
	require.Contains(t, err.Error(), "claim 71 is no longer valid")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGroupApplicationRepositoryCreateUnavailableWhenNoConflictExists(t *testing.T) {
	repo, mock := newGroupApplicationOutboxTestRepository(t)
	application := &service.GroupApplication{UserID: 9, GroupID: 4, ContactEmail: "applicant@example.com", Reason: "reason", Locale: "en"}
	mock.ExpectBegin()
	expectGroupApplicationUserLock(mock, application.UserID)
	mock.ExpectQuery(`(?s)INSERT INTO group_applications`).
		WithArgs(application.UserID, application.GroupID, application.ContactEmail, application.Reason, application.Locale).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectQuery(`(?s)SELECT.*EXISTS.*OR EXISTS`).
		WithArgs(application.UserID, application.GroupID).
		WillReturnRows(sqlmock.NewRows([]string{"conflict"}).AddRow(false))
	mock.ExpectRollback()

	created, err := repo.CreateApplication(context.Background(), application)

	require.Nil(t, created)
	require.ErrorIs(t, err, service.ErrGroupApplicationUnavailable)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGroupApplicationRepositoryStoreReceiptUsesMonotonicResultPriority(t *testing.T) {
	for _, test := range []struct {
		name     string
		affected int64
		stored   bool
	}{
		{name: "successful result upgrades a weaker receipt", affected: 1, stored: true},
		{name: "weaker duplicate cannot overwrite success", affected: 0, stored: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo, mock := newGroupApplicationOutboxTestRepository(t)
			receipt := service.GroupApplicationReceipt{
				MailboxFingerprint: "fingerprint", UIDValidity: 12, UID: 34,
				MessageID: "<reply@example.com>", FromAddress: "user@example.com",
				InReplyTo: "<approval@example.com>", References: "<approval@example.com>",
				Result: "completed", ReplySHA256: "digest", EncryptedContent: "ciphertext",
			}
			mock.ExpectExec(`(?s)ON CONFLICT\(mailbox_fingerprint,uid_validity,uid\) DO UPDATE SET.*WHERE CASE EXCLUDED\.result.*WHEN 'completed' THEN 100.*> CASE group_application_inbound_receipts\.result`).
				WithArgs(
					receipt.MailboxFingerprint, int64(receipt.UIDValidity), int64(receipt.UID), receipt.MessageID,
					receipt.FromAddress, receipt.InReplyTo, receipt.References, receipt.ApplicationID,
					receipt.Result, receipt.ReplySHA256, receipt.EncryptedContent, receipt.ContentTruncated,
				).
				WillReturnResult(sqlmock.NewResult(0, test.affected))

			stored, err := repo.StoreReceipt(context.Background(), receipt)

			require.NoError(t, err)
			require.Equal(t, test.stored, stored)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
