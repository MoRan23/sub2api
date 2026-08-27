package repository

import (
	"context"
	"encoding/json"
	"regexp"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

const (
	groupApplicationStatusLockSQL = `SELECT status FROM group_applications WHERE id=$1 FOR UPDATE`
	activeApprovalMailSQL         = `
			SELECT EXISTS(
				SELECT 1 FROM group_application_mail_outbox
				WHERE application_id=$1 AND kind='approval' AND required_application_status=$2
				  AND status IN ('pending','processing')
			)
		`
	insertGroupApplicationMailSQL = `
			INSERT INTO group_application_mail_outbox(application_id,kind,recipient,subject,html_body,attachment_id,message_id,required_application_status)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8)
		`
	cancelStaleApplicationMailSQL = `
				UPDATE group_application_mail_outbox o
				SET status='cancelled',claimed_at=NULL,claimed_by=NULL,claim_expires_at=NULL,
				    last_error='application status changed',updated_at=NOW()
				FROM group_applications a WHERE a.id=o.application_id AND o.status IN ('pending','processing') AND a.status<>o.required_application_status
			`
	cancelDuplicateApprovalMailSQL = `
				UPDATE group_application_mail_outbox dup
				SET status='cancelled',last_error='duplicate approval mail',claimed_at=NULL,claimed_by=NULL,claim_expires_at=NULL,updated_at=NOW()
				WHERE dup.kind='approval' AND dup.status='pending'
				  AND EXISTS (
					SELECT 1 FROM group_application_mail_outbox active
					WHERE active.application_id=dup.application_id
					  AND active.kind='approval'
					  AND active.required_application_status=dup.required_application_status
					  AND active.status IN ('pending','processing')
					  AND active.id<>dup.id
					  AND (active.status='processing' OR active.id<dup.id)
				  )
		`
)

func newGroupApplicationOutboxTestRepository(t *testing.T) (*groupApplicationRepository, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return &groupApplicationRepository{db: db}, mock
}

func approvalMailJob() service.GroupApplicationMailJob {
	return service.GroupApplicationMailJob{
		Kind:           service.GroupApplicationMailApproval,
		Recipient:      "applicant@example.com",
		Subject:        "approved",
		HTMLBody:       "<p>approved</p>",
		MessageID:      "<approval-1@example.com>",
		RequiredStatus: service.GroupApplicationStatusAwaitingReply,
	}
}

func expectApplicationStatusLock(mock sqlmock.Sqlmock, applicationID int64, status string) {
	mock.ExpectQuery(regexp.QuoteMeta(groupApplicationStatusLockSQL)).
		WithArgs(applicationID).
		WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow(status))
}

func expectMailInsert(mock sqlmock.Sqlmock, applicationID int64, mail service.GroupApplicationMailJob) {
	mock.ExpectExec(regexp.QuoteMeta(insertGroupApplicationMailSQL)).
		WithArgs(applicationID, mail.Kind, mail.Recipient, mail.Subject, mail.HTMLBody, mail.AttachmentID, mail.MessageID, mail.RequiredStatus).
		WillReturnResult(sqlmock.NewResult(1, 1))
}

func TestGroupApplicationRepositoryEnqueueApprovalMailIsIdempotentWhileActive(t *testing.T) {
	repo, mock := newGroupApplicationOutboxTestRepository(t)
	mail := approvalMailJob()

	mock.ExpectBegin()
	expectApplicationStatusLock(mock, 41, service.GroupApplicationStatusAwaitingReply)
	mock.ExpectQuery(regexp.QuoteMeta(activeApprovalMailSQL)).
		WithArgs(int64(41), service.GroupApplicationStatusAwaitingReply).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectCommit()

	err := repo.EnqueueMail(context.Background(), 41, mail)

	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGroupApplicationRepositoryEnqueueApprovalMailInsertsWhenNoActiveJobExists(t *testing.T) {
	repo, mock := newGroupApplicationOutboxTestRepository(t)
	mail := approvalMailJob()

	mock.ExpectBegin()
	expectApplicationStatusLock(mock, 41, service.GroupApplicationStatusAwaitingReply)
	mock.ExpectQuery(regexp.QuoteMeta(activeApprovalMailSQL)).
		WithArgs(int64(41), service.GroupApplicationStatusAwaitingReply).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	expectMailInsert(mock, 41, mail)
	mock.ExpectCommit()

	err := repo.EnqueueMail(context.Background(), 41, mail)

	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGroupApplicationRepositoryEnqueueNonApprovalMailIsNotDeduplicated(t *testing.T) {
	repo, mock := newGroupApplicationOutboxTestRepository(t)
	mail := approvalMailJob()
	mail.Kind = service.GroupApplicationMailCompletion
	mail.RequiredStatus = service.GroupApplicationStatusCompleted
	mail.MessageID = "<completion-1@example.com>"

	mock.ExpectBegin()
	expectApplicationStatusLock(mock, 41, service.GroupApplicationStatusCompleted)
	expectMailInsert(mock, 41, mail)
	mock.ExpectCommit()

	err := repo.EnqueueMail(context.Background(), 41, mail)

	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGroupApplicationRepositoryClaimMailCancelsDuplicatePendingApprovalsBeforeSingleClaim(t *testing.T) {
	repo, mock := newGroupApplicationOutboxTestRepository(t)

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(cancelStaleApplicationMailSQL)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta(cancelDuplicateApprovalMailSQL)).
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectQuery(`(?s)WITH candidates AS .*claim_expires_at.*FOR UPDATE OF o SKIP LOCKED`).
		WithArgs("worker-1", 1, int64(60)).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "application_id", "kind", "recipient", "subject", "html_body", "attachment_id",
			"message_id", "required_application_status", "attempts", "attachment_joined_id",
			"filename", "content_type", "byte_size", "sha256", "data", "created_at",
		}))
	mock.ExpectCommit()

	jobs, err := repo.ClaimMail(context.Background(), "worker-1", 20, time.Minute)

	require.NoError(t, err)
	require.Empty(t, jobs)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGroupApplicationRepositoryFindApprovalOnlyMatchesOpenReplyWorkflowAndDeliveryUncertainty(t *testing.T) {
	repo, mock := newGroupApplicationOutboxTestRepository(t)
	mock.ExpectQuery(`(?s)WHERE a\.status='awaiting_reply' AND o\.kind='approval'.*o\.status IN \('processing','sent'\) AND o\.message_id=ANY\(\$1\)`).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))

	match, err := repo.FindApprovalByMessageIDs(
		context.Background(),
		[]string{"<group-application-7-approval-3d579da3-3c63-4b1c-9684-954714939bd5@sub2api.local>"},
		nil,
	)

	require.Nil(t, match)
	require.ErrorIs(t, err, service.ErrGroupApplicationNotFound)
	require.NoError(t, mock.ExpectationsWereMet())
}

func groupApplicationApprovalMatchRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "user_id", "user_email", "group_id", "group_name", "contact_email", "reason", "locale", "status",
		"reply_phrase_snapshot", "templates_snapshot", "attachment_id", "attachment_name", "reviewed_by", "reviewed_at",
		"decision_reason", "completed_at", "revoked_by", "revoked_at", "last_email_kind", "last_email_status",
		"last_email_error", "created_at", "updated_at", "matched_message_id",
	})
}

func addGroupApplicationApprovalMatchRow(rows *sqlmock.Rows, applicationID int64, messageID string) *sqlmock.Rows {
	now := time.Date(2026, time.August, 27, 12, 0, 0, 0, time.UTC)
	templates, _ := json.Marshal(service.DefaultGroupApplicationTemplates())
	return rows.AddRow(
		applicationID, int64(9), "user@example.com", int64(4), "Private", "applicant@example.com", "reason", "en",
		service.GroupApplicationStatusAwaitingReply, "CONFIRM", templates, int64(3), "agreement.pdf", nil, nil, "", nil,
		nil, nil, service.GroupApplicationMailApproval, "sent", "", now, now, messageID,
	)
}

func TestGroupApplicationRepositoryFindApprovalAcceptsMultipleMessagesForOneApplication(t *testing.T) {
	repo, mock := newGroupApplicationOutboxTestRepository(t)
	const first = "<group-application-7-approval-3d579da3-3c63-4b1c-9684-954714939bd5@sub2api.local>"
	const latest = "<group-application-7-approval-29aa2182-7493-46a9-9075-118cc82c9203@sub2api.local>"
	rows := addGroupApplicationApprovalMatchRow(groupApplicationApprovalMatchRows(), 7, latest)
	mock.ExpectQuery(`(?s)WITH matched AS .*DISTINCT ON \(application_id\).*LIMIT 2`).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(rows)

	match, err := repo.FindApprovalByMessageIDs(context.Background(), []string{first, latest}, nil)

	require.NoError(t, err)
	require.Equal(t, int64(7), match.Application.ID)
	require.Equal(t, latest, match.MessageID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGroupApplicationRepositoryFindApprovalRejectsCrossApplicationAmbiguity(t *testing.T) {
	repo, mock := newGroupApplicationOutboxTestRepository(t)
	rows := groupApplicationApprovalMatchRows()
	addGroupApplicationApprovalMatchRow(rows, 7, "<first@sub2api.local>")
	addGroupApplicationApprovalMatchRow(rows, 8, "<second@sub2api.local>")
	mock.ExpectQuery(`(?s)WITH matched AS .*DISTINCT ON \(application_id\).*LIMIT 2`).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(rows)

	match, err := repo.FindApprovalByMessageIDs(context.Background(), []string{"<first@sub2api.local>", "<second@sub2api.local>"}, nil)

	require.Nil(t, match)
	require.ErrorIs(t, err, service.ErrGroupApplicationReplyAmbiguous)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGroupApplicationRepositoryFindApprovalExactMatchTakesPriorityOverFallback(t *testing.T) {
	repo, mock := newGroupApplicationOutboxTestRepository(t)
	exact := "<exact@sub2api.local>"
	fallback := "<embedded@sub2api.local>"
	rows := addGroupApplicationApprovalMatchRow(groupApplicationApprovalMatchRows(), 7, exact)
	mock.ExpectQuery(`(?s)WITH matched AS .*LIMIT 2`).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(rows)

	match, err := repo.FindApprovalByMessageIDs(context.Background(), []string{exact}, []string{fallback})

	require.NoError(t, err)
	require.Equal(t, int64(7), match.Application.ID)
	require.Equal(t, exact, match.MessageID)
	require.NoError(t, mock.ExpectationsWereMet(), "fallback query must not run after an exact match")
}

func TestGroupApplicationRepositoryFindApprovalFallsBackOnlyAfterExactNotFound(t *testing.T) {
	repo, mock := newGroupApplicationOutboxTestRepository(t)
	exact := "<rewritten@provider.example>"
	fallback := "<embedded@sub2api.local>"
	mock.ExpectQuery(`(?s)WITH matched AS .*LIMIT 2`).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(groupApplicationApprovalMatchRows())
	mock.ExpectQuery(`(?s)WITH matched AS .*LIMIT 2`).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(addGroupApplicationApprovalMatchRow(groupApplicationApprovalMatchRows(), 7, fallback))

	match, err := repo.FindApprovalByMessageIDs(context.Background(), []string{exact}, []string{fallback})

	require.NoError(t, err)
	require.Equal(t, int64(7), match.Application.ID)
	require.Equal(t, fallback, match.MessageID)
	require.NoError(t, mock.ExpectationsWereMet())
}
