package securityaudit

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// ClaimEnforcementNotification leases one due item. The worker must check the
// claim immediately before SMTP and acknowledge only after SMTP succeeds.
// No network work happens while the database transaction is held.
func (r *PostgreSQLRepository) ClaimEnforcementNotification(ctx context.Context, workerID string, now time.Time, lease time.Duration) (*EnforcementNotification, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("prompt audit database unavailable")
	}
	workerID = strings.TrimSpace(workerID)
	if workerID == "" || len(workerID) > 128 {
		return nil, errors.New("notification worker identity required")
	}
	if lease <= 0 {
		lease = 2 * time.Minute
	}
	var n EnforcementNotification
	var categories, groups []byte
	err := r.db.QueryRowContext(ctx, `
		WITH exhausted_candidates AS (
			SELECT id FROM prompt_audit_enforcement_notifications
			WHERE sent_at IS NULL AND attempts>=5 AND (claimed_at IS NULL OR claimed_at<=$2)
			ORDER BY available_at,id FOR UPDATE SKIP LOCKED LIMIT 100
		), exhausted AS (
			UPDATE prompt_audit_enforcement_notifications n SET sent_at=$1,claimed_at=NULL,last_error='delivery_failed',updated_at=$1
			FROM exhausted_candidates e WHERE n.id=e.id
		), candidate AS (
			SELECT n.id FROM prompt_audit_enforcement_notifications n
			JOIN prompt_audit_enforcement_states s ON s.user_id=n.user_id AND s.epoch=n.epoch
			JOIN users u ON u.id=n.user_id AND u.deleted_at IS NULL
			WHERE n.sent_at IS NULL AND n.available_at<=$1 AND n.attempts<5
			AND (n.claimed_at IS NULL OR n.claimed_at <= $2)
			ORDER BY n.available_at,n.id FOR UPDATE OF n SKIP LOCKED LIMIT 1
		), claimed AS (
			UPDATE prompt_audit_enforcement_notifications n
			SET claimed_at=$1, claimed_by=$3,claim_version=n.claim_version+1,
			claimed_payload_version=n.payload_version,claimed_hit_count=n.hit_count,claimed_auto_banned=n.auto_banned,
			pending_categories='[]'::jsonb,pending_rule_group_names='[]'::jsonb,pending_first_hit_at=NULL,
			attempts=n.attempts+1,updated_at=$1
			FROM candidate c WHERE n.id=c.id RETURNING n.*
		)
		SELECT c.id,c.claim_version,c.claimed_by,c.claimed_payload_version,c.user_id,c.epoch,COALESCE(u.email,''),COALESCE(u.username,''),u.status,
		c.kind,c.categories,c.rule_group_names,c.hit_count,c.violation_count,c.window_hours,c.ban_threshold,
		c.auto_banned,c.first_hit_at,c.last_hit_at,c.attempts
		FROM claimed c JOIN users u ON u.id=c.user_id`, now.UTC(), now.Add(-lease).UTC(), workerID).
		Scan(&n.ID, &n.ClaimVersion, &n.ClaimedBy, &n.DeliveryVersion, &n.UserID, &n.Epoch, &n.Email, &n.Username, &n.UserStatus, &n.Kind,
			&categories, &groups, &n.HitCount, &n.ViolationCount, &n.WindowHours, &n.BanThreshold,
			&n.AutoBanned, &n.FirstHitAt, &n.LastHitAt, &n.Attempts)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(categories, &n.Categories); err != nil {
		return nil, err
	}
	if err = json.Unmarshal(groups, &n.RuleGroupNames); err != nil {
		return nil, err
	}
	return &n, nil
}

// EnforcementNotificationCurrent rejects claims invalidated by unbanning,
// lease takeover, recipient changes or a merged change such as promotion to a
// ban notice. A changed recipient requires a new claim with the current email.
func (r *PostgreSQLRepository) EnforcementNotificationCurrent(ctx context.Context, id, claimVersion int64, claimedEmail string) (bool, error) {
	var current bool
	err := r.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM prompt_audit_enforcement_notifications n JOIN prompt_audit_enforcement_states s ON s.user_id=n.user_id AND s.epoch=n.epoch JOIN users u ON u.id=n.user_id AND u.deleted_at IS NULL WHERE n.id=$1 AND n.claim_version=$2 AND n.sent_at IS NULL AND n.claimed_at IS NOT NULL AND n.payload_version=n.claimed_payload_version AND COALESCE(u.email,'')=$3)`, id, claimVersion, claimedEmail).Scan(&current)
	return current, err
}

// CompleteEnforcementNotification consumes only the payload that was claimed.
// A hit merged while SMTP was in progress stays pending for another delivery.
func (r *PostgreSQLRepository) CompleteEnforcementNotification(ctx context.Context, id, claimVersion int64, now time.Time) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var sentVersion int64
	err = tx.QueryRowContext(ctx, `UPDATE prompt_audit_enforcement_notifications n
		SET sent_at=CASE WHEN n.payload_version=n.claimed_payload_version THEN $3::timestamptz ELSE NULL END,
		claimed_at=NULL,claimed_by=NULL,
		available_at=CASE WHEN n.payload_version=n.claimed_payload_version OR (n.auto_banned AND NOT n.claimed_auto_banned) THEN $3::timestamptz ELSE COALESCE(n.pending_first_hit_at,$3::timestamptz)+make_interval(mins=>n.merge_minutes) END,
		merge_until=CASE WHEN n.payload_version<>n.claimed_payload_version THEN COALESCE(n.pending_first_hit_at,$3::timestamptz)+make_interval(mins=>n.merge_minutes) ELSE n.merge_until END,
		first_hit_at=CASE WHEN n.payload_version<>n.claimed_payload_version THEN COALESCE(n.pending_first_hit_at,$3::timestamptz) ELSE n.first_hit_at END,
		hit_count=CASE WHEN n.payload_version<>n.claimed_payload_version THEN GREATEST(n.hit_count-n.claimed_hit_count,0) ELSE n.hit_count END,
		categories=CASE WHEN n.payload_version<>n.claimed_payload_version THEN n.pending_categories ELSE n.categories END,
		rule_group_names=CASE WHEN n.payload_version<>n.claimed_payload_version THEN n.pending_rule_group_names ELSE n.rule_group_names END,
		kind=CASE WHEN n.payload_version<>n.claimed_payload_version THEN CASE WHEN jsonb_array_length(n.pending_categories)>1 THEN 'mixed' ELSE COALESCE(n.pending_categories->>0,n.kind) END ELSE n.kind END,
		auto_banned=CASE WHEN n.payload_version<>n.claimed_payload_version THEN n.auto_banned AND NOT n.claimed_auto_banned ELSE n.auto_banned END,
		attempts=CASE WHEN n.payload_version<>n.claimed_payload_version THEN 0 ELSE n.attempts END,
		last_error=NULL,updated_at=$3
		WHERE n.id=$1 AND n.claim_version=$2 AND n.sent_at IS NULL AND n.claimed_at IS NOT NULL
		AND EXISTS(SELECT 1 FROM prompt_audit_enforcement_states s WHERE s.user_id=n.user_id AND s.epoch=n.epoch)
		RETURNING n.claimed_payload_version`, id, claimVersion, now.UTC()).Scan(&sentVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrEnforcementNotificationLeaseLost
	}
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE prompt_audit_enforcement_hits SET email_status='sent' WHERE notification_id=$1 AND notification_version<=$2`, id, sentVersion); err != nil {
		return err
	}
	return tx.Commit()
}

// RetryEnforcementNotification applies a bounded backoff. Transport error text
// is intentionally not stored: it can contain SMTP credentials or recipients.
func (r *PostgreSQLRepository) RetryEnforcementNotification(ctx context.Context, id, claimVersion int64, now time.Time, errorMessage string) error {
	_ = errorMessage
	res, err := r.db.ExecContext(ctx, `UPDATE prompt_audit_enforcement_notifications n
		SET sent_at=CASE WHEN n.attempts>=5 THEN $3::timestamptz ELSE NULL END,
		claimed_at=NULL,claimed_by=NULL,
		available_at=$3::timestamptz + make_interval(secs => LEAST(900, 30 * CAST(power(2, LEAST(n.attempts,5)-1) AS INT))),
		last_error=CASE WHEN n.attempts>=5 THEN 'delivery_failed' ELSE 'delivery_retry' END,updated_at=$3
		WHERE n.id=$1 AND n.claim_version=$2 AND n.sent_at IS NULL AND n.claimed_at IS NOT NULL
		AND EXISTS(SELECT 1 FROM prompt_audit_enforcement_states s WHERE s.user_id=n.user_id AND s.epoch=n.epoch)`, id, claimVersion, now.UTC())
	return requireOneRow(res, err, ErrEnforcementNotificationLeaseLost)
}

// ReleaseEnforcementNotification makes a still-owned changed payload available
// again without treating a normal merge as an SMTP delivery failure.
func (r *PostgreSQLRepository) ReleaseEnforcementNotification(ctx context.Context, id, claimVersion int64, now time.Time) error {
	res, err := r.db.ExecContext(ctx, `UPDATE prompt_audit_enforcement_notifications SET claimed_at=NULL,claimed_by=NULL,attempts=GREATEST(attempts-1,0),updated_at=$3 WHERE id=$1 AND claim_version=$2 AND sent_at IS NULL AND claimed_at IS NOT NULL`, id, claimVersion, now.UTC())
	return requireOneRow(res, err, ErrEnforcementNotificationLeaseLost)
}

// PruneEnforcementHistory keeps the longest configurable window plus a day of
// overlap. It is independent from audit-detail deletion. The caller runs this
// at maintenance cadence, never on the request path.
func (r *PostgreSQLRepository) PruneEnforcementHistory(ctx context.Context, now time.Time, limit int) (int64, error) {
	if limit <= 0 || limit > 10000 {
		limit = 1000
	}
	res, err := r.db.ExecContext(ctx, `DELETE FROM prompt_audit_enforcement_hits WHERE id IN (SELECT h.id FROM prompt_audit_enforcement_hits h WHERE h.occurred_at<$1 AND NOT EXISTS(SELECT 1 FROM prompt_audit_events e WHERE e.id=h.event_id) ORDER BY h.occurred_at,h.id LIMIT $2)`, now.Add(-maxEnforcementRetention).UTC(), limit)
	if err != nil {
		return 0, err
	}
	deleted, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	_, err = r.db.ExecContext(ctx, `DELETE FROM prompt_audit_enforcement_notifications WHERE id IN (SELECT n.id FROM prompt_audit_enforcement_notifications n WHERE n.sent_at<$1 AND NOT EXISTS(SELECT 1 FROM prompt_audit_enforcement_hits h WHERE h.notification_id=n.id) ORDER BY n.sent_at,n.id LIMIT $2)`, now.Add(-maxEnforcementRetention).UTC(), limit)
	return deleted, err
}
