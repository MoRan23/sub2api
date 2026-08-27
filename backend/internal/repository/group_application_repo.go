package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

type groupApplicationRepository struct {
	db *sql.DB
}

func NewGroupApplicationRepository(db *sql.DB) service.GroupApplicationRepository {
	return &groupApplicationRepository{db: db}
}

func (r *groupApplicationRepository) ListOptions(ctx context.Context, userID int64) ([]service.GroupApplicationOption, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT g.id, g.name, COALESCE(g.description, ''),
		       EXISTS(SELECT 1 FROM group_applications a WHERE a.user_id=$1 AND a.group_id=g.id AND a.status IN ('pending','awaiting_reply')),
		       EXISTS(SELECT 1 FROM group_applications a WHERE a.user_id=$1 AND a.group_id=g.id AND a.status='completed')
		FROM group_application_policies p
		JOIN groups g ON g.id=p.group_id
		WHERE p.enabled=TRUE AND p.attachment_id IS NOT NULL AND p.reply_phrase <> ''
		  AND g.deleted_at IS NULL AND g.status='active' AND g.is_exclusive=TRUE
		  AND g.subscription_type='standard'
		ORDER BY g.sort_order, g.id
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]service.GroupApplicationOption, 0)
	for rows.Next() {
		var item service.GroupApplicationOption
		if err := rows.Scan(&item.GroupID, &item.GroupName, &item.Description, &item.HasActive, &item.AlreadyCompleted); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (r *groupApplicationRepository) GetPolicy(ctx context.Context, groupID int64) (*service.GroupApplicationPolicy, error) {
	return scanGroupApplicationPolicy(r.db.QueryRowContext(ctx, policySelect+` WHERE p.group_id=$1`, groupID))
}

func (r *groupApplicationRepository) ListPolicies(ctx context.Context) ([]*service.GroupApplicationPolicy, error) {
	rows, err := r.db.QueryContext(ctx, policySelect+` ORDER BY g.sort_order, g.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]*service.GroupApplicationPolicy, 0)
	for rows.Next() {
		item, err := scanGroupApplicationPolicy(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

const policySelect = `
	SELECT p.group_id, g.name, p.enabled, p.reply_phrase, p.templates,
	       p.attachment_id, COALESCE(a.filename,''), COALESCE(a.byte_size,0), COALESCE(a.sha256,''),
	       p.created_at, p.updated_at
	FROM group_application_policies p
	JOIN groups g ON g.id=p.group_id
	LEFT JOIN group_application_attachments a ON a.id=p.attachment_id`

type sqlScanner interface {
	Scan(dest ...any) error
}

func scanGroupApplicationPolicy(scanner sqlScanner) (*service.GroupApplicationPolicy, error) {
	var item service.GroupApplicationPolicy
	var templates []byte
	var attachmentID sql.NullInt64
	if err := scanner.Scan(&item.GroupID, &item.GroupName, &item.Enabled, &item.ReplyPhrase, &templates,
		&attachmentID, &item.AttachmentName, &item.AttachmentSize, &item.AttachmentSHA256,
		&item.CreatedAt, &item.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, service.ErrGroupApplicationUnavailable
		}
		return nil, err
	}
	if attachmentID.Valid {
		item.AttachmentID = &attachmentID.Int64
	}
	if err := json.Unmarshal(templates, &item.Templates); err != nil {
		return nil, fmt.Errorf("decode group application templates: %w", err)
	}
	return &item, nil
}

func (r *groupApplicationRepository) SavePolicy(ctx context.Context, policy *service.GroupApplicationPolicy, attachment *service.GroupApplicationAttachment, adminID int64) (*service.GroupApplicationPolicy, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var eligible bool
	err = tx.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM groups
			WHERE id=$1 AND deleted_at IS NULL AND status='active'
			  AND is_exclusive=TRUE AND subscription_type='standard'
		)
	`, policy.GroupID).Scan(&eligible)
	if err != nil {
		return nil, err
	}
	if !eligible {
		return nil, service.ErrGroupApplicationUnavailable
	}

	var attachmentID *int64
	if attachment != nil {
		var id int64
		err = tx.QueryRowContext(ctx, `
			INSERT INTO group_application_attachments(filename,content_type,byte_size,sha256,data,created_by)
			VALUES($1,$2,$3,$4,$5,$6) RETURNING id
		`, attachment.Filename, attachment.ContentType, attachment.ByteSize, attachment.SHA256, attachment.Data, adminID).Scan(&id)
		if err != nil {
			return nil, err
		}
		attachmentID = &id
	} else {
		attachmentID = policy.AttachmentID
		if attachmentID == nil {
			var existing sql.NullInt64
			err = tx.QueryRowContext(ctx, `SELECT attachment_id FROM group_application_policies WHERE group_id=$1`, policy.GroupID).Scan(&existing)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return nil, err
			}
			if existing.Valid {
				id := existing.Int64
				attachmentID = &id
			}
		}
	}
	if policy.Enabled && attachmentID == nil {
		return nil, service.ErrGroupApplicationUnavailable
	}
	templates, err := json.Marshal(policy.Templates)
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO group_application_policies(group_id,enabled,reply_phrase,templates,attachment_id,updated_by)
		VALUES($1,$2,$3,$4,$5,$6)
		ON CONFLICT(group_id) DO UPDATE SET
			enabled=EXCLUDED.enabled, reply_phrase=EXCLUDED.reply_phrase,
			templates=EXCLUDED.templates, attachment_id=EXCLUDED.attachment_id,
			updated_by=EXCLUDED.updated_by, updated_at=NOW()
	`, policy.GroupID, policy.Enabled, policy.ReplyPhrase, templates, attachmentID, adminID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return r.GetPolicy(ctx, policy.GroupID)
}

func (r *groupApplicationRepository) GetAttachment(ctx context.Context, id int64) (*service.GroupApplicationAttachment, error) {
	var item service.GroupApplicationAttachment
	err := r.db.QueryRowContext(ctx, `SELECT id,filename,content_type,byte_size,sha256,data,created_at FROM group_application_attachments WHERE id=$1`, id).
		Scan(&item.ID, &item.Filename, &item.ContentType, &item.ByteSize, &item.SHA256, &item.Data, &item.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrGroupApplicationNotFound
	}
	return &item, err
}

func (r *groupApplicationRepository) CreateApplication(ctx context.Context, application *service.GroupApplication) (*service.GroupApplication, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var lockedUserID int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM users WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, application.UserID).Scan(&lockedUserID); errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrGroupApplicationUnavailable
	} else if err != nil {
		return nil, err
	}

	var id int64
	err = tx.QueryRowContext(ctx, `
		INSERT INTO group_applications(
			user_id,group_id,contact_email,reason,locale,status,
			reply_phrase_snapshot,templates_snapshot,attachment_id)
		SELECT $1,p.group_id,$3,$4,$5,'pending',p.reply_phrase,p.templates,p.attachment_id
			FROM group_application_policies p
			JOIN groups g ON g.id=p.group_id
			WHERE p.group_id=$2 AND p.enabled=TRUE AND p.attachment_id IS NOT NULL AND p.reply_phrase<>''
			  AND g.deleted_at IS NULL AND g.status='active' AND g.is_exclusive=TRUE AND g.subscription_type='standard'
			  AND NOT EXISTS (
				SELECT 1 FROM group_applications existing
				WHERE existing.user_id=$1 AND existing.group_id=$2
				  AND existing.status IN ('pending','awaiting_reply','completed')
			  )
			  AND NOT EXISTS (
				SELECT 1 FROM user_allowed_groups allowed
				WHERE allowed.user_id=$1 AND allowed.group_id=$2
			  )
			RETURNING id
		`, application.UserID, application.GroupID, application.ContactEmail, application.Reason, application.Locale).Scan(&id)
	if err != nil {
		var pqErr *pq.Error
		if errors.As(err, &pqErr) && pqErr.Code == "23505" {
			return nil, service.ErrGroupApplicationConflict
		}
		if errors.Is(err, sql.ErrNoRows) {
			var conflict bool
			conflictErr := tx.QueryRowContext(ctx, `
				SELECT
					EXISTS(
						SELECT 1 FROM group_applications
						WHERE user_id=$1 AND group_id=$2
						  AND status IN ('pending','awaiting_reply','completed')
					)
					OR EXISTS(
						SELECT 1 FROM user_allowed_groups
						WHERE user_id=$1 AND group_id=$2
					)
			`, application.UserID, application.GroupID).Scan(&conflict)
			if conflictErr != nil {
				return nil, conflictErr
			}
			if conflict {
				return nil, service.ErrGroupApplicationConflict
			}
			return nil, service.ErrGroupApplicationUnavailable
		}
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return r.GetUserApplication(ctx, application.UserID, id)
}

const applicationSelect = `
	SELECT a.id,a.user_id,COALESCE(u.email,''),a.group_id,g.name,a.contact_email,a.reason,a.locale,a.status,
	       a.reply_phrase_snapshot,a.templates_snapshot,a.attachment_id,COALESCE(att.filename,''),
	       a.reviewed_by,a.reviewed_at,COALESCE(a.decision_reason,''),a.completed_at,a.revoked_by,a.revoked_at,
	       COALESCE(a.last_email_kind,''),COALESCE(a.last_email_status,''),COALESCE(a.last_email_error,''),
	       a.created_at,a.updated_at
	FROM group_applications a
	JOIN users u ON u.id=a.user_id
	JOIN groups g ON g.id=a.group_id
	JOIN group_application_attachments att ON att.id=a.attachment_id`

func scanGroupApplication(scanner sqlScanner) (*service.GroupApplication, error) {
	return scanGroupApplicationWithTrailing(scanner)
}

func scanGroupApplicationWithTrailing(scanner sqlScanner, trailing ...any) (*service.GroupApplication, error) {
	var item service.GroupApplication
	var templates []byte
	var reviewedBy, revokedBy sql.NullInt64
	var reviewedAt, completedAt, revokedAt sql.NullTime
	destinations := []any{
		&item.ID, &item.UserID, &item.UserEmail, &item.GroupID, &item.GroupName, &item.ContactEmail,
		&item.Reason, &item.Locale, &item.Status, &item.ReplyPhraseSnapshot, &templates, &item.AttachmentID,
		&item.AttachmentName, &reviewedBy, &reviewedAt, &item.DecisionReason, &completedAt, &revokedBy,
		&revokedAt, &item.LastEmailKind, &item.LastEmailStatus, &item.LastEmailError, &item.CreatedAt, &item.UpdatedAt,
	}
	destinations = append(destinations, trailing...)
	err := scanner.Scan(destinations...)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrGroupApplicationNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(templates, &item.TemplatesSnapshot); err != nil {
		return nil, err
	}
	if reviewedBy.Valid {
		item.ReviewedBy = &reviewedBy.Int64
	}
	if revokedBy.Valid {
		item.RevokedBy = &revokedBy.Int64
	}
	if reviewedAt.Valid {
		value := reviewedAt.Time
		item.ReviewedAt = &value
	}
	if completedAt.Valid {
		value := completedAt.Time
		item.CompletedAt = &value
	}
	if revokedAt.Valid {
		value := revokedAt.Time
		item.RevokedAt = &value
	}
	return &item, nil
}

func (r *groupApplicationRepository) ListUserApplications(ctx context.Context, userID int64) ([]*service.GroupApplication, error) {
	rows, err := r.db.QueryContext(ctx, applicationSelect+` WHERE a.user_id=$1 ORDER BY a.created_at DESC,a.id DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanGroupApplications(rows)
}

func (r *groupApplicationRepository) GetUserApplication(ctx context.Context, userID, applicationID int64) (*service.GroupApplication, error) {
	return scanGroupApplication(r.db.QueryRowContext(ctx, applicationSelect+` WHERE a.user_id=$1 AND a.id=$2`, userID, applicationID))
}

func (r *groupApplicationRepository) GetApplication(ctx context.Context, applicationID int64) (*service.GroupApplication, error) {
	return scanGroupApplication(r.db.QueryRowContext(ctx, applicationSelect+` WHERE a.id=$1`, applicationID))
}

func (r *groupApplicationRepository) ListApplicationMails(ctx context.Context, applicationID int64) ([]service.GroupApplicationMailStatus, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id,kind,message_id,status,required_application_status,attempts,COALESCE(last_error,''),sent_at,created_at
		FROM group_application_mail_outbox WHERE application_id=$1 ORDER BY created_at DESC,id DESC
	`, applicationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]service.GroupApplicationMailStatus, 0)
	for rows.Next() {
		var item service.GroupApplicationMailStatus
		var sentAt sql.NullTime
		if err := rows.Scan(&item.ID, &item.Kind, &item.MessageID, &item.Status, &item.RequiredStatus, &item.Attempts, &item.LastError, &sentAt, &item.CreatedAt); err != nil {
			return nil, err
		}
		if sentAt.Valid {
			value := sentAt.Time
			item.SentAt = &value
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *groupApplicationRepository) ListApplicationCommunications(ctx context.Context, applicationID int64) ([]service.GroupApplicationCommunication, error) {
	items := make([]service.GroupApplicationCommunication, 0)
	outboundRows, err := r.db.QueryContext(ctx, `
		SELECT o.id,o.application_id,o.kind,o.recipient,o.subject,o.html_body,o.attachment_id,
		       COALESCE(a.filename,''),COALESCE(a.byte_size,0),o.message_id,o.status,o.required_application_status,o.attempts,
		       COALESCE(o.last_error,''),o.sent_at,COALESCE(o.sent_at,o.created_at)
		FROM group_application_mail_outbox o
		LEFT JOIN group_application_attachments a ON a.id=o.attachment_id
		WHERE o.application_id=$1
	`, applicationID)
	if err != nil {
		return nil, err
	}
	for outboundRows.Next() {
		var item service.GroupApplicationCommunication
		var attachmentID sql.NullInt64
		var sentAt sql.NullTime
		item.Direction = service.GroupApplicationCommunicationOutbound
		if err := outboundRows.Scan(
			&item.ID, &item.ApplicationID, &item.Kind, &item.ToAddress, &item.Subject, &item.HTMLBody,
			&attachmentID, &item.AttachmentName, &item.AttachmentSize, &item.MessageID, &item.Status,
			&item.RequiredStatus, &item.Attempts, &item.LastError, &sentAt, &item.OccurredAt,
		); err != nil {
			outboundRows.Close()
			return nil, err
		}
		if attachmentID.Valid {
			value := attachmentID.Int64
			item.AttachmentID = &value
		}
		if sentAt.Valid {
			value := sentAt.Time
			item.SentAt = &value
		}
		items = append(items, item)
	}
	if err := outboundRows.Err(); err != nil {
		outboundRows.Close()
		return nil, err
	}
	outboundRows.Close()

	inboundRows, err := r.db.QueryContext(ctx, `
		SELECT id,application_id,COALESCE(from_address,''),COALESCE(message_id,''),
		       COALESCE(in_reply_to,''),COALESCE(references_header,''),result,
		       COALESCE(reply_sha256,''),COALESCE(content_encrypted,''),content_truncated,processed_at
		FROM group_application_inbound_receipts
		WHERE application_id=$1
	`, applicationID)
	if err != nil {
		return nil, err
	}
	defer inboundRows.Close()
	for inboundRows.Next() {
		var item service.GroupApplicationCommunication
		item.Direction = service.GroupApplicationCommunicationInbound
		if err := inboundRows.Scan(
			&item.ID, &item.ApplicationID, &item.FromAddress, &item.MessageID, &item.InReplyTo,
			&item.References, &item.Result, &item.ReplySHA256, &item.EncryptedContent,
			&item.ContentTruncated, &item.OccurredAt,
		); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := inboundRows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].OccurredAt.Equal(items[j].OccurredAt) {
			if items[i].Direction == items[j].Direction {
				return items[i].ID < items[j].ID
			}
			return items[i].Direction < items[j].Direction
		}
		return items[i].OccurredAt.Before(items[j].OccurredAt)
	})
	return items, nil
}

func scanGroupApplications(rows *sql.Rows) ([]*service.GroupApplication, error) {
	items := make([]*service.GroupApplication, 0)
	for rows.Next() {
		item, err := scanGroupApplication(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *groupApplicationRepository) ListApplications(ctx context.Context, filter service.GroupApplicationListFilter) (*service.GroupApplicationListResult, error) {
	where := []string{"1=1"}
	args := make([]any, 0, 7)
	add := func(expr string, value any) {
		args = append(args, value)
		where = append(where, fmt.Sprintf(expr, len(args)))
	}
	if filter.Status != "" {
		add("a.status=$%d", filter.Status)
	}
	if filter.UserID > 0 {
		add("a.user_id=$%d", filter.UserID)
	}
	if filter.GroupID > 0 {
		add("a.group_id=$%d", filter.GroupID)
	}
	if filter.Search != "" {
		args = append(args, filter.Search)
		placeholder := len(args)
		where = append(where, fmt.Sprintf("(u.email ILIKE '%%'||$%d||'%%' OR a.contact_email ILIKE '%%'||$%d||'%%' OR g.name ILIKE '%%'||$%d||'%%')", placeholder, placeholder, placeholder))
	}
	clause := strings.Join(where, " AND ")
	var total int64
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM group_applications a JOIN users u ON u.id=a.user_id JOIN groups g ON g.id=a.group_id WHERE `+clause, args...).Scan(&total); err != nil {
		return nil, err
	}
	limit := filter.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	offset := filter.Offset
	args = append(args, limit, offset)
	rows, err := r.db.QueryContext(ctx, applicationSelect+` WHERE `+clause+fmt.Sprintf(` ORDER BY a.created_at DESC,a.id DESC LIMIT $%d OFFSET $%d`, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items, err := scanGroupApplications(rows)
	return &service.GroupApplicationListResult{Items: items, Total: total}, err
}

func insertGroupApplicationMail(ctx context.Context, tx *sql.Tx, applicationID int64, mail service.GroupApplicationMailJob) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO group_application_mail_outbox(application_id,kind,recipient,subject,html_body,attachment_id,message_id,required_application_status)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8)
	`, applicationID, mail.Kind, mail.Recipient, mail.Subject, mail.HTMLBody, mail.AttachmentID, mail.MessageID, mail.RequiredStatus)
	return err
}

func (r *groupApplicationRepository) transition(ctx context.Context, applicationID int64, allowed []string, update string, args []any, mail service.GroupApplicationMailJob, terminal bool, receipt *service.GroupApplicationReceipt, sideEffect func(*sql.Tx, int64, int64, int64) error) (*service.GroupApplication, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var userID, groupID int64
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT user_id,group_id,status FROM group_applications WHERE id=$1 FOR UPDATE`, applicationID).Scan(&userID, &groupID, &status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, service.ErrGroupApplicationNotFound
		}
		return nil, err
	}
	valid := false
	for _, candidate := range allowed {
		if status == candidate {
			valid = true
			break
		}
	}
	if !valid {
		return nil, service.ErrGroupApplicationState
	}
	if terminal {
		if err := quiesceGroupApplicationMail(ctx, tx, applicationID); err != nil {
			return nil, err
		}
	}
	if sideEffect != nil {
		if err := sideEffect(tx, applicationID, userID, groupID); err != nil {
			return nil, err
		}
	}
	queryArgs := []any{applicationID}
	queryArgs = append(queryArgs, args...)
	if _, err := tx.ExecContext(ctx, `UPDATE group_applications SET `+update+`,updated_at=NOW() WHERE id=$1`, queryArgs...); err != nil {
		return nil, err
	}
	if err := insertGroupApplicationMail(ctx, tx, applicationID, mail); err != nil {
		return nil, err
	}
	if receipt != nil {
		if _, err := storeGroupApplicationReceipt(ctx, tx, *receipt); err != nil {
			return nil, err
		}
	}
	application, err := scanGroupApplication(tx.QueryRowContext(ctx, applicationSelect+` WHERE a.id=$1`, applicationID))
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return application, nil
}

func quiesceGroupApplicationMail(ctx context.Context, tx *sql.Tx, applicationID int64) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE group_application_mail_outbox
		SET status='cancelled',claimed_at=NULL,claimed_by=NULL,claim_expires_at=NULL,
		    last_error='application status changed',updated_at=NOW()
		WHERE application_id=$1
		  AND (
			status='pending'
			OR (
				status='processing'
				AND COALESCE(claim_expires_at,claimed_at+INTERVAL '1 minute','-infinity'::timestamptz)<=NOW()
			)
		  )
	`, applicationID)
	if err != nil {
		return err
	}

	var activeID int64
	err = tx.QueryRowContext(ctx, `
		SELECT id
		FROM group_application_mail_outbox
		WHERE application_id=$1 AND status='processing'
		  AND COALESCE(claim_expires_at,claimed_at+INTERVAL '1 minute','-infinity'::timestamptz)>NOW()
		ORDER BY id
		LIMIT 1
		FOR UPDATE
	`, applicationID).Scan(&activeID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return service.ErrGroupApplicationMailDeliveryInProgress
}

func (r *groupApplicationRepository) Approve(ctx context.Context, applicationID, adminID int64, mail service.GroupApplicationMailJob) (*service.GroupApplication, error) {
	return r.transition(ctx, applicationID, []string{service.GroupApplicationStatusPending}, `status='awaiting_reply',reviewed_by=$2,reviewed_at=NOW(),decision_reason=NULL`, []any{adminID}, mail, false, nil, nil)
}

func (r *groupApplicationRepository) Reject(ctx context.Context, applicationID, adminID int64, reason string, mail service.GroupApplicationMailJob) (*service.GroupApplication, error) {
	return r.transition(ctx, applicationID, []string{service.GroupApplicationStatusPending, service.GroupApplicationStatusAwaitingReply}, `status='rejected',reviewed_by=$2,reviewed_at=NOW(),decision_reason=$3`, []any{adminID, reason}, mail, true, nil, nil)
}

func (r *groupApplicationRepository) CompleteFromReply(ctx context.Context, applicationID int64, mail service.GroupApplicationMailJob, receipt *service.GroupApplicationReceipt) (*service.GroupApplication, error) {
	return r.transition(ctx, applicationID, []string{service.GroupApplicationStatusAwaitingReply}, `status='completed',completed_at=NOW(),decision_reason=NULL`, nil, mail, true, receipt, func(tx *sql.Tx, applicationID, userID, groupID int64) error {
		var eligible bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM groups WHERE id=$1 AND deleted_at IS NULL AND status='active' AND is_exclusive=TRUE AND subscription_type='standard')`, groupID).Scan(&eligible); err != nil {
			return err
		}
		if !eligible {
			return service.ErrGroupApplicationUnavailable
		}
		result, err := tx.ExecContext(ctx, `INSERT INTO user_allowed_groups(user_id,group_id,created_at) VALUES($1,$2,NOW()) ON CONFLICT(user_id,group_id) DO NOTHING`, userID, groupID)
		if err != nil {
			return err
		}
		inserted, err := result.RowsAffected()
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE group_applications SET access_grant_owned=$2 WHERE id=$1`, applicationID, inserted == 1)
		return err
	})
}

func (r *groupApplicationRepository) RejectReplyMismatch(ctx context.Context, applicationID int64, mail service.GroupApplicationMailJob, receipt *service.GroupApplicationReceipt) (*service.GroupApplication, error) {
	return r.transition(ctx, applicationID, []string{service.GroupApplicationStatusAwaitingReply}, `status='rejected',reviewed_at=COALESCE(reviewed_at,NOW()),decision_reason='reply_mismatch'`, nil, mail, true, receipt, nil)
}

func (r *groupApplicationRepository) Revoke(ctx context.Context, applicationID, adminID int64, reason string, mail service.GroupApplicationMailJob) (*service.GroupApplication, error) {
	return r.transition(ctx, applicationID, []string{service.GroupApplicationStatusCompleted}, `status='revoked',revoked_by=$2,revoked_at=NOW(),decision_reason=$3,access_grant_owned=FALSE`, []any{adminID, reason}, mail, true, nil, func(tx *sql.Tx, applicationID, userID, groupID int64) error {
		result, err := tx.ExecContext(ctx, `
			DELETE FROM user_allowed_groups allowed
			USING group_applications application
			WHERE application.id=$3 AND application.access_grant_owned=TRUE
			  AND application.completed_at IS NOT NULL
			  AND allowed.user_id=$1 AND allowed.group_id=$2
			  AND allowed.created_at=application.completed_at
		`, userID, groupID, applicationID)
		if err != nil {
			return err
		}
		deleted, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if deleted > 0 {
			return nil
		}
		var grantExists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM user_allowed_groups WHERE user_id=$1 AND group_id=$2)`, userID, groupID).Scan(&grantExists); err != nil {
			return err
		}
		if grantExists {
			return service.ErrGroupApplicationAccessNotOwned
		}
		return nil
	})
}

func (r *groupApplicationRepository) EnqueueMail(ctx context.Context, applicationID int64, mail service.GroupApplicationMailJob) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var status string
	// Serialize all outbox producers for one application. In particular, this
	// prevents concurrent approval resend requests from both observing an empty
	// active set and inserting duplicate jobs.
	if err := tx.QueryRowContext(ctx, `SELECT status FROM group_applications WHERE id=$1 FOR UPDATE`, applicationID).Scan(&status); err != nil {
		return err
	}
	if status != mail.RequiredStatus {
		return service.ErrGroupApplicationState
	}
	if mail.Kind == service.GroupApplicationMailApproval {
		var active bool
		if err := tx.QueryRowContext(ctx, `
			SELECT EXISTS(
				SELECT 1 FROM group_application_mail_outbox
				WHERE application_id=$1 AND kind='approval' AND required_application_status=$2
				  AND status IN ('pending','processing')
			)
		`, applicationID, mail.RequiredStatus).Scan(&active); err != nil {
			return err
		}
		if active {
			return tx.Commit()
		}
	}
	if err := insertGroupApplicationMail(ctx, tx, applicationID, mail); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *groupApplicationRepository) RetryMail(ctx context.Context, applicationID, outboxID int64) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var applicationStatus string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM group_applications WHERE id=$1 FOR UPDATE`, applicationID).Scan(&applicationStatus); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return service.ErrGroupApplicationNotFound
		}
		return err
	}

	var kind, outboxStatus, requiredStatus string
	if err := tx.QueryRowContext(ctx, `
		SELECT kind,status,required_application_status
		FROM group_application_mail_outbox
		WHERE id=$1 AND application_id=$2
		FOR UPDATE
	`, outboxID, applicationID).Scan(&kind, &outboxStatus, &requiredStatus); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return service.ErrGroupApplicationState
		}
		return err
	}
	if outboxStatus != "failed" || applicationStatus != requiredStatus {
		return service.ErrGroupApplicationState
	}
	if kind == service.GroupApplicationMailApproval {
		var active bool
		if err := tx.QueryRowContext(ctx, `
			SELECT EXISTS(
				SELECT 1 FROM group_application_mail_outbox
				WHERE application_id=$1 AND kind='approval'
				  AND required_application_status=$2 AND id<>$3
				  AND status IN ('pending','processing')
			)
		`, applicationID, requiredStatus, outboxID).Scan(&active); err != nil {
			return err
		}
		if active {
			return service.ErrGroupApplicationMailDeliveryInProgress
		}
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE group_application_mail_outbox
		SET status='pending',attempts=0,available_at=NOW(),claimed_at=NULL,claimed_by=NULL,
		    claim_expires_at=NULL,last_error=NULL,updated_at=NOW()
		WHERE id=$1
	`, outboxID)
	if err != nil {
		var pqErr *pq.Error
		if errors.As(err, &pqErr) && pqErr.Code == "23505" && pqErr.Constraint == "group_application_mail_outbox_one_active_approval" {
			return service.ErrGroupApplicationMailDeliveryInProgress
		}
		return err
	}
	return tx.Commit()
}

func (r *groupApplicationRepository) ClaimMail(ctx context.Context, workerID string, limit int, lease time.Duration) ([]service.GroupApplicationMailJob, error) {
	// A worker sends outside the database transaction. Claiming one job at a
	// time prevents later jobs in a batch from expiring while SMTP is slow.
	limit = 1
	leaseSeconds := int64(lease / time.Second)
	if leaseSeconds < 1 {
		leaseSeconds = 60
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `
			UPDATE group_application_mail_outbox o
			SET status='cancelled',claimed_at=NULL,claimed_by=NULL,claim_expires_at=NULL,
			    last_error='application status changed',updated_at=NOW()
			FROM group_applications a WHERE a.id=o.application_id AND o.status IN ('pending','processing') AND a.status<>o.required_application_status
	`)
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `
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
	`)
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `
		WITH candidates AS (
				SELECT o.id FROM group_application_mail_outbox o
				JOIN group_applications a ON a.id=o.application_id AND a.status=o.required_application_status
				WHERE o.status IN ('pending','processing') AND o.available_at<=NOW()
				  AND (
					o.status='pending'
					OR COALESCE(o.claim_expires_at,o.claimed_at+($3*INTERVAL '1 second'),'-infinity'::timestamptz)<=NOW()
				  )
				ORDER BY o.id LIMIT $2 FOR UPDATE OF o SKIP LOCKED
			), claimed AS (
				UPDATE group_application_mail_outbox o
				SET status='processing',claimed_at=NOW(),claimed_by=$1,
				    claim_expires_at=NOW()+($3*INTERVAL '1 second'),updated_at=NOW()
				FROM candidates c WHERE o.id=c.id
			RETURNING o.*
		)
		SELECT c.id,c.application_id,c.kind,c.recipient,c.subject,c.html_body,c.attachment_id,c.message_id,c.required_application_status,c.attempts,
		       a.id,COALESCE(a.filename,''),COALESCE(a.content_type,''),COALESCE(a.byte_size,0),COALESCE(a.sha256,''),a.data,a.created_at
		FROM claimed c LEFT JOIN group_application_attachments a ON a.id=c.attachment_id ORDER BY c.id
	`, workerID, limit, leaseSeconds)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := make([]service.GroupApplicationMailJob, 0, limit)
	for rows.Next() {
		var job service.GroupApplicationMailJob
		var attachmentID, joinedID sql.NullInt64
		var name, contentType, sha sql.NullString
		var size sql.NullInt64
		var data []byte
		var created sql.NullTime
		if err := rows.Scan(&job.ID, &job.ApplicationID, &job.Kind, &job.Recipient, &job.Subject, &job.HTMLBody, &attachmentID, &job.MessageID, &job.RequiredStatus, &job.Attempts,
			&joinedID, &name, &contentType, &size, &sha, &data, &created); err != nil {
			return nil, err
		}
		if attachmentID.Valid {
			job.AttachmentID = &attachmentID.Int64
		}
		if joinedID.Valid {
			job.Attachment = &service.GroupApplicationAttachment{ID: joinedID.Int64, Filename: name.String, ContentType: contentType.String, ByteSize: size.Int64, SHA256: sha.String, Data: data, CreatedAt: created.Time}
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return jobs, nil
}

func (r *groupApplicationRepository) ValidateMailClaim(ctx context.Context, id int64, workerID string) error {
	var validatedID int64
	err := r.db.QueryRowContext(ctx, `
		UPDATE group_application_mail_outbox o
		SET claimed_at=NOW(),claim_expires_at=NOW()+INTERVAL '1 minute',updated_at=NOW()
		FROM group_applications a
		WHERE o.id=$1 AND o.claimed_by=$2 AND o.status='processing'
		  AND a.id=o.application_id AND a.status=o.required_application_status
		RETURNING o.id
	`, id, workerID).Scan(&validatedID)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}

	result, cancelErr := r.db.ExecContext(ctx, `
		UPDATE group_application_mail_outbox o
		SET status='cancelled',claimed_at=NULL,claimed_by=NULL,claim_expires_at=NULL,
		    last_error='application status changed',updated_at=NOW()
		FROM group_applications a
		WHERE o.id=$1 AND o.claimed_by=$2 AND o.status='processing'
		  AND a.id=o.application_id AND a.status<>o.required_application_status
	`, id, workerID)
	if cancelErr != nil {
		return cancelErr
	}
	if _, affectedErr := result.RowsAffected(); affectedErr != nil {
		return affectedErr
	}
	return service.ErrGroupApplicationState
}

func lockGroupApplicationForMailOutbox(ctx context.Context, tx *sql.Tx, outboxID int64) (int64, error) {
	var applicationID int64
	err := tx.QueryRowContext(ctx, `
		SELECT a.id
		FROM group_application_mail_outbox o
		JOIN group_applications a ON a.id=o.application_id
		WHERE o.id=$1
		FOR UPDATE OF a
	`, outboxID).Scan(&applicationID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("mail outbox claim %d is no longer valid", outboxID)
	}
	return applicationID, err
}

func (r *groupApplicationRepository) MarkMailSent(ctx context.Context, id int64, workerID string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	applicationID, err := lockGroupApplicationForMailOutbox(ctx, tx, id)
	if err != nil {
		return err
	}

	var returnedApplicationID int64
	var kind, requiredStatus string
	err = tx.QueryRowContext(ctx, `
		UPDATE group_application_mail_outbox
		SET status='sent',sent_at=NOW(),last_error=NULL,claimed_at=NULL,claimed_by=NULL,
		    claim_expires_at=NULL,updated_at=NOW()
		WHERE id=$1 AND claimed_by=$2 AND status='processing'
		RETURNING application_id,kind,required_application_status
	`, id, workerID).Scan(&returnedApplicationID, &kind, &requiredStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("mail outbox claim %d is no longer valid", id)
	}
	if err != nil {
		return err
	}
	if returnedApplicationID != applicationID {
		return fmt.Errorf("mail outbox %d application changed during claim finalization", id)
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE group_applications
		SET last_email_kind=$2,last_email_status='sent',last_email_error=NULL,updated_at=NOW()
		WHERE id=$1 AND status=$3
	`, applicationID, kind, requiredStatus)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (r *groupApplicationRepository) RetryClaimedMail(ctx context.Context, id int64, workerID string, retryAt time.Time, terminal bool, lastError string) error {
	status := "pending"
	if terminal {
		status = "failed"
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	applicationID, err := lockGroupApplicationForMailOutbox(ctx, tx, id)
	if err != nil {
		return err
	}

	var returnedApplicationID int64
	var kind, requiredStatus string
	err = tx.QueryRowContext(ctx, `
		UPDATE group_application_mail_outbox
		SET status=$3,attempts=attempts+1,available_at=$4,last_error=$5,
		    claimed_at=NULL,claimed_by=NULL,claim_expires_at=NULL,updated_at=NOW()
		WHERE id=$1 AND claimed_by=$2 AND status='processing'
		RETURNING application_id,kind,required_application_status
	`, id, workerID, status, retryAt, lastError).Scan(&returnedApplicationID, &kind, &requiredStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("mail outbox claim %d is no longer valid", id)
	}
	if err != nil {
		return err
	}
	if returnedApplicationID != applicationID {
		return fmt.Errorf("mail outbox %d application changed during claim finalization", id)
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE group_applications
		SET last_email_kind=$2,last_email_status=$3,last_email_error=$4,updated_at=NOW()
		WHERE id=$1 AND status=$5
	`, applicationID, kind, status, lastError, requiredStatus)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (r *groupApplicationRepository) FindApprovalByMessageIDs(ctx context.Context, exactMessageIDs, fallbackMessageIDs []string) (*service.GroupApplicationApprovalMatch, error) {
	match, err := r.findApprovalByMessageIDs(ctx, exactMessageIDs)
	if err == nil || !errors.Is(err, service.ErrGroupApplicationNotFound) {
		return match, err
	}
	return r.findApprovalByMessageIDs(ctx, fallbackMessageIDs)
}

func (r *groupApplicationRepository) findApprovalByMessageIDs(ctx context.Context, messageIDs []string) (*service.GroupApplicationApprovalMatch, error) {
	if len(messageIDs) == 0 {
		return nil, service.ErrGroupApplicationNotFound
	}
	rows, err := r.db.QueryContext(ctx, `
			WITH matched AS (
				SELECT o.application_id,o.message_id,o.status,o.sent_at,o.id
				FROM group_application_mail_outbox o
				JOIN group_applications a ON a.id=o.application_id
				WHERE a.status='awaiting_reply' AND o.kind='approval'
				  AND o.status IN ('processing','sent') AND o.message_id=ANY($1)
			), latest_per_application AS (
				SELECT DISTINCT ON (application_id) application_id,message_id,status,sent_at,id
				FROM matched
				ORDER BY application_id,(status='sent') DESC,sent_at DESC NULLS LAST,id DESC
			)
			SELECT base.*,latest.message_id
			FROM (`+applicationSelect+`) base
			JOIN latest_per_application latest ON latest.application_id=base.id
			ORDER BY (latest.status='sent') DESC,latest.sent_at DESC NULLS LAST,latest.id DESC
			LIMIT 2
		`, pq.Array(messageIDs))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var match *service.GroupApplicationApprovalMatch
	for rows.Next() {
		if match != nil {
			return nil, service.ErrGroupApplicationReplyAmbiguous
		}
		var matchedMessageID string
		application, scanErr := scanGroupApplicationWithTrailing(rows, &matchedMessageID)
		if scanErr != nil {
			return nil, scanErr
		}
		match = &service.GroupApplicationApprovalMatch{Application: application, MessageID: matchedMessageID}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if match == nil {
		return nil, service.ErrGroupApplicationNotFound
	}
	return match, nil
}

func (r *groupApplicationRepository) MaxProcessedUID(ctx context.Context, fingerprint string, uidValidity uint32) (uint32, bool, error) {
	var value sql.NullInt64
	err := r.db.QueryRowContext(ctx, `SELECT MAX(uid) FROM group_application_inbound_receipts WHERE mailbox_fingerprint=$1 AND uid_validity=$2`, fingerprint, int64(uidValidity)).Scan(&value)
	if err != nil {
		return 0, false, err
	}
	if !value.Valid {
		return 0, false, nil
	}
	return uint32(value.Int64), true, nil
}

type groupApplicationReceiptExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func (r *groupApplicationRepository) StoreReceipt(ctx context.Context, receipt service.GroupApplicationReceipt) (bool, error) {
	return storeGroupApplicationReceipt(ctx, r.db, receipt)
}

func storeGroupApplicationReceipt(ctx context.Context, execer groupApplicationReceiptExecer, receipt service.GroupApplicationReceipt) (bool, error) {
	result, err := execer.ExecContext(ctx, `
		INSERT INTO group_application_inbound_receipts(
			mailbox_fingerprint,uid_validity,uid,message_id,from_address,in_reply_to,references_header,
			application_id,result,reply_sha256,content_encrypted,content_truncated)
		VALUES($1,$2,$3,NULLIF($4,''),NULLIF($5,''),NULLIF($6,''),NULLIF($7,''),$8,$9,NULLIF($10,''),NULLIF($11,''),$12)
		ON CONFLICT(mailbox_fingerprint,uid_validity,uid) DO UPDATE SET
			message_id=EXCLUDED.message_id,
			from_address=EXCLUDED.from_address,
			in_reply_to=EXCLUDED.in_reply_to,
			references_header=EXCLUDED.references_header,
			application_id=EXCLUDED.application_id,
			result=EXCLUDED.result,
			reply_sha256=EXCLUDED.reply_sha256,
			content_encrypted=EXCLUDED.content_encrypted,
			content_truncated=EXCLUDED.content_truncated,
			processed_at=NOW()
		WHERE CASE EXCLUDED.result
				WHEN 'completed' THEN 100
				WHEN 'reply_mismatch' THEN 100
				WHEN 'ignored_sender' THEN 80
				WHEN 'unsupported_content' THEN 70
				WHEN 'automated' THEN 70
				WHEN 'state_conflict' THEN 50
				WHEN 'unavailable' THEN 50
				WHEN 'not_found' THEN 50
				ELSE 10
			END
			> CASE group_application_inbound_receipts.result
				WHEN 'completed' THEN 100
				WHEN 'reply_mismatch' THEN 100
				WHEN 'ignored_sender' THEN 80
				WHEN 'unsupported_content' THEN 70
				WHEN 'automated' THEN 70
				WHEN 'state_conflict' THEN 50
				WHEN 'unavailable' THEN 50
				WHEN 'not_found' THEN 50
				ELSE 10
			END
	`, receipt.MailboxFingerprint, int64(receipt.UIDValidity), int64(receipt.UID), receipt.MessageID, receipt.FromAddress, receipt.InReplyTo, receipt.References, receipt.ApplicationID, receipt.Result, receipt.ReplySHA256, receipt.EncryptedContent, receipt.ContentTruncated)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected == 1, nil
}
