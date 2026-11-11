package securityaudit

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/lib/pq"
)

const (
	defaultEnforcementWindowHours  = 24
	defaultEnforcementThreshold    = 10
	defaultEnforcementMergeMinutes = 5
	maxEnforcementRetention        = 31 * 24 * time.Hour
)

func normalizeEnforcementPolicy(p EnforcementPolicy) EnforcementPolicy {
	if p.ViolationWindowHours <= 0 {
		p.ViolationWindowHours = defaultEnforcementWindowHours
	}
	if p.BanThreshold <= 0 {
		p.BanThreshold = defaultEnforcementThreshold
	}
	if p.EmailMergeMinutes <= 0 {
		p.EmailMergeMinutes = defaultEnforcementMergeMinutes
	}
	if p.ViolationWindowHours > 720 {
		p.ViolationWindowHours = 720
	}
	if p.BanThreshold > 10000 {
		p.BanThreshold = 10000
	}
	if p.EmailMergeMinutes > 60 {
		p.EmailMergeMinutes = 60
	}
	return p
}

func normalizeEnforcementKind(kind string) string {
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind != "jailbreak" && kind != "combination" {
		return "jailbreak"
	}
	return kind
}

// RecordEnforcedBlocking atomically records the blocking audit event and the
// compact enforcement ledger. It is deliberately separate from RecordBlocking
// so ordinary keyword and Guard events never participate in enforcement.
func (r *PostgreSQLRepository) RecordEnforcedBlocking(ctx context.Context, input EnforcementInput) (*EnforcementOutcome, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("prompt audit database unavailable")
	}
	if input.Result == nil {
		return nil, errors.New("prompt guard result required")
	}
	policy := normalizeEnforcementPolicy(input.Policy)
	if input.Snapshot.UserID <= 0 {
		return nil, errors.New("prompt enforcement user required")
	}
	requestKey := strings.TrimSpace(input.RequestKey)
	if requestKey == "" || len(requestKey) > 192 {
		return nil, errors.New("server-generated enforcement request key required")
	}
	kind := normalizeEnforcementKind(input.Kind)
	now := r.clock.Now().UTC()
	if input.StartedAt.IsZero() {
		input.StartedAt = now
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	var userStatus, userRole string
	if err = tx.QueryRowContext(ctx, `SELECT status, role FROM users WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, input.Snapshot.UserID).Scan(&userStatus, &userRole); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrEnforcementUserNotFound
		}
		return nil, err
	}
	var epoch, revision, ownedRevision sql.NullInt64
	var countingSince time.Time
	err = tx.QueryRowContext(ctx, `SELECT epoch, revision, owned_ban_revision, counting_since FROM prompt_audit_enforcement_states WHERE user_id=$1 FOR UPDATE`, input.Snapshot.UserID).
		Scan(&epoch, &revision, &ownedRevision, &countingSince)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err = tx.ExecContext(ctx, `INSERT INTO prompt_audit_enforcement_states(user_id,epoch,revision,counting_since,updated_at) VALUES($1,1,0,'epoch',$2) ON CONFLICT(user_id) DO NOTHING`, input.Snapshot.UserID, now); err != nil {
			return nil, err
		}
		if err = tx.QueryRowContext(ctx, `SELECT epoch, revision, owned_ban_revision, counting_since FROM prompt_audit_enforcement_states WHERE user_id=$1 FOR UPDATE`, input.Snapshot.UserID).Scan(&epoch, &revision, &ownedRevision, &countingSince); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	if !epoch.Valid || epoch.Int64 <= 0 {
		epoch.Int64 = 1
	}
	counting := input.StartedAt.UTC().After(countingSince) || input.StartedAt.UTC().Equal(countingSince)
	cutoff := now.Add(-time.Duration(policy.ViolationWindowHours) * time.Hour)
	if countingSince.After(cutoff) {
		cutoff = countingSince
	}

	var hitID sql.NullInt64
	err = tx.QueryRowContext(ctx, `
		INSERT INTO prompt_audit_enforcement_hits(user_id,epoch,request_key,kind,rule_group_name,occurred_at,counted)
		VALUES($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT(user_id,request_key) DO NOTHING
		RETURNING id`, input.Snapshot.UserID, epoch.Int64, requestKey, kind, truncateEnforcementText(input.RuleGroupName, 160), now, counting).Scan(&hitID)
	newHit := err == nil
	if !newHit && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if !newHit {
		outcome := &EnforcementOutcome{Recorded: true, UserStatus: userStatus}
		if err = tx.QueryRowContext(ctx, `SELECT kind,counted,epoch,violation_count,window_hours,ban_threshold,auto_banned,email_status,reason FROM prompt_audit_enforcement_hits WHERE user_id=$1 AND request_key=$2`, input.Snapshot.UserID, requestKey).
			Scan(&outcome.Kind, &outcome.Counted, &outcome.Epoch, &outcome.ViolationCount, &outcome.WindowHours, &outcome.BanThreshold, &outcome.AutoBanned, &outcome.EmailStatus, &outcome.Reason); err != nil {
			return nil, err
		}
		outcome.CanUnban = userStatus == "disabled" && ownedRevision.Valid && ownedRevision.Int64 == revision.Int64
		if err = tx.Commit(); err != nil {
			return nil, err
		}
		return outcome, nil
	}

	count := 0
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM prompt_audit_enforcement_hits WHERE user_id=$1 AND epoch=$2 AND counted=TRUE AND occurred_at >= $3`, input.Snapshot.UserID, epoch.Int64, cutoff).Scan(&count); err != nil {
		return nil, err
	}
	outcome := &EnforcementOutcome{Recorded: newHit, Counted: newHit && counting, Kind: kind, ViolationCount: count, WindowHours: policy.ViolationWindowHours, BanThreshold: policy.BanThreshold, Epoch: epoch.Int64, UserStatus: userStatus, EmailStatus: "not_requested"}
	if !counting {
		outcome.Reason = "cycle_reset"
	}
	owned := ownedRevision.Valid && ownedRevision.Int64 == revision.Int64
	autoBan := false
	if userStatus == "active" && userRole != "admin" && policy.AutoBanEnabled && count >= policy.BanThreshold && counting && newHit {
		autoBan = true
	}
	if userStatus == "disabled" && owned {
		outcome.AutoBanned = true
		outcome.CanUnban = true
	}

	var event *Event
	if newHit {
		job, insertErr := insertJob(ctx, tx, input.Snapshot.Redacted(), ModeBlocking, input.ConfigVersion, "done", 1)
		if insertErr != nil {
			return nil, insertErr
		}
		event, insertErr = insertEvent(ctx, tx, job.ID, input.Snapshot.Redacted(), input.ConfigVersion, input.Result)
		if insertErr != nil {
			return nil, insertErr
		}
		if _, err = tx.ExecContext(ctx, `UPDATE prompt_audit_enforcement_hits SET event_id=$2 WHERE id=$1`, hitID, event.ID); err != nil {
			return nil, err
		}
	}
	if autoBan {
		if _, err = tx.ExecContext(ctx, `UPDATE users SET status='disabled', updated_at=$2 WHERE id=$1 AND status='active'`, input.Snapshot.UserID, now); err != nil {
			return nil, err
		}
		// Match the normal user-status update contract: re-enabling a user must
		// never revive previously issued administrative MCP authorizations.
		if _, err = tx.ExecContext(ctx, `UPDATE mcp_oauth_grants SET revoked_at=$2 WHERE user_id=$1 AND revoked_at IS NULL`, input.Snapshot.UserID, now); err != nil {
			return nil, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE prompt_audit_enforcement_states SET revision=revision+1, owned_ban_revision=revision+1, ban_event_id=$2, updated_at=$3 WHERE user_id=$1`, input.Snapshot.UserID, nullableEventID(event), now); err != nil {
			return nil, err
		}
		outcome.AutoBanned = true
		outcome.CanUnban = true
		outcome.UserStatus = "disabled"
		outcome.UserChanged = true
	} else {
		outcome.CanUnban = owned && userStatus == "disabled"
	}
	if policy.EmailOnHit && newHit && counting {
		notificationID, notificationVersion, enqueueErr := upsertEnforcementNotification(ctx, tx, input.Snapshot.UserID, epoch.Int64, kind, input.RuleGroupName, count, policy, autoBan, now)
		if enqueueErr != nil {
			return nil, enqueueErr
		}
		if _, err = tx.ExecContext(ctx, `UPDATE prompt_audit_enforcement_hits SET notification_id=$2,notification_version=$3 WHERE id=$1`, hitID, notificationID, notificationVersion); err != nil {
			return nil, err
		}
		outcome.EmailStatus = "queued"
	}
	if newHit {
		if _, err = tx.ExecContext(ctx, `UPDATE prompt_audit_enforcement_hits SET violation_count=$2,window_hours=$3,ban_threshold=$4,auto_banned=$5,user_status=$6,email_status=$7,reason=$8 WHERE id=$1`, hitID, outcome.ViolationCount, outcome.WindowHours, outcome.BanThreshold, outcome.AutoBanned, outcome.UserStatus, outcome.EmailStatus, outcome.Reason); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return outcome, nil
}

func nullableEventID(event *Event) any {
	if event == nil {
		return nil
	}
	return event.ID
}

func truncateEnforcementText(value string, max int) string {
	value = strings.TrimSpace(value)
	r := []rune(value)
	if len(r) > max {
		return string(r[:max])
	}
	return value
}

func upsertEnforcementNotification(ctx context.Context, tx *sql.Tx, userID, epoch int64, kind, groupName string, count int, policy EnforcementPolicy, autoBanned bool, now time.Time) (int64, int64, error) {
	mergeUntil := now.Add(time.Duration(policy.EmailMergeMinutes) * time.Minute)
	availableAt := mergeUntil
	if autoBanned {
		availableAt = now
	}
	categories, _ := json.Marshal([]string{kind})
	groups := []string{}
	if strings.TrimSpace(groupName) != "" {
		groups = append(groups, truncateEnforcementText(groupName, 160))
	}
	groupJSON, _ := json.Marshal(groups)
	var id, version int64
	err := tx.QueryRowContext(ctx, `
		INSERT INTO prompt_audit_enforcement_notifications(user_id,epoch,available_at,merge_until,first_hit_at,last_hit_at,kind,categories,rule_group_names,hit_count,violation_count,window_hours,ban_threshold,auto_banned,merge_minutes)
		VALUES($1,$2,$12,$4,$3,$3,$5,$6::jsonb,$7::jsonb,1,$8,$9,$10,$11,$13)
		ON CONFLICT (user_id,epoch) WHERE sent_at IS NULL DO UPDATE SET
			last_hit_at=EXCLUDED.last_hit_at, hit_count=prompt_audit_enforcement_notifications.hit_count+1,
			payload_version=prompt_audit_enforcement_notifications.payload_version+1,
			pending_first_hit_at=CASE WHEN prompt_audit_enforcement_notifications.claimed_at IS NOT NULL THEN COALESCE(prompt_audit_enforcement_notifications.pending_first_hit_at,EXCLUDED.last_hit_at) ELSE prompt_audit_enforcement_notifications.pending_first_hit_at END,
			pending_categories=CASE WHEN prompt_audit_enforcement_notifications.claimed_at IS NOT NULL THEN COALESCE((SELECT jsonb_agg(DISTINCT x) FROM jsonb_array_elements(prompt_audit_enforcement_notifications.pending_categories || EXCLUDED.categories) AS t(x)), '[]'::jsonb) ELSE prompt_audit_enforcement_notifications.pending_categories END,
			pending_rule_group_names=CASE WHEN prompt_audit_enforcement_notifications.claimed_at IS NOT NULL THEN COALESCE((SELECT jsonb_agg(DISTINCT x) FROM jsonb_array_elements(prompt_audit_enforcement_notifications.pending_rule_group_names || EXCLUDED.rule_group_names) AS t(x)), '[]'::jsonb) ELSE prompt_audit_enforcement_notifications.pending_rule_group_names END,
			violation_count=EXCLUDED.violation_count,window_hours=EXCLUDED.window_hours,ban_threshold=EXCLUDED.ban_threshold,
			kind=CASE WHEN prompt_audit_enforcement_notifications.kind<>EXCLUDED.kind THEN 'mixed' ELSE prompt_audit_enforcement_notifications.kind END,
			categories=COALESCE((SELECT jsonb_agg(DISTINCT x) FROM jsonb_array_elements(prompt_audit_enforcement_notifications.categories || EXCLUDED.categories) AS t(x)), '[]'::jsonb),
			rule_group_names=COALESCE((SELECT jsonb_agg(DISTINCT x) FROM jsonb_array_elements(prompt_audit_enforcement_notifications.rule_group_names || EXCLUDED.rule_group_names) AS t(x)), '[]'::jsonb),
			auto_banned=prompt_audit_enforcement_notifications.auto_banned OR EXCLUDED.auto_banned,
			available_at=CASE WHEN EXCLUDED.auto_banned THEN EXCLUDED.available_at ELSE prompt_audit_enforcement_notifications.available_at END,
			updated_at=EXCLUDED.last_hit_at RETURNING id,payload_version`, userID, epoch, now, mergeUntil, kind, string(categories), string(groupJSON), count, policy.ViolationWindowHours, policy.BanThreshold, autoBanned, availableAt, policy.EmailMergeMinutes).Scan(&id, &version)
	return id, version, err
}

// UnbanPromptUser starts a new enforcement epoch. Historical events and hits
// are retained, while queued notifications for the old epoch are cancelled.
func (r *PostgreSQLRepository) UnbanPromptUser(ctx context.Context, userID int64) (*EnforcementOutcome, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("prompt audit database unavailable")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var status string
	if err = tx.QueryRowContext(ctx, `SELECT status FROM users WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, userID).Scan(&status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrEnforcementUserNotFound
		}
		return nil, err
	}
	var epoch, revision, owner sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT epoch,revision,owned_ban_revision FROM prompt_audit_enforcement_states WHERE user_id=$1 FOR UPDATE`, userID).Scan(&epoch, &revision, &owner)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrEnforcementUnbanOwnership
	}
	if err != nil {
		return nil, err
	}
	owned := owner.Valid && owner.Int64 == revision.Int64
	if status == "active" {
		return &EnforcementOutcome{Epoch: epoch.Int64, UserStatus: "active", Reason: "already_active"}, nil
	}
	if status != "disabled" || !owned {
		return nil, ErrEnforcementUnbanOwnership
	}
	now := r.clock.Now().UTC()
	if status == "disabled" {
		if _, err = tx.ExecContext(ctx, `UPDATE users SET status='active',updated_at=$2 WHERE id=$1`, userID, now); err != nil {
			return nil, err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE prompt_audit_enforcement_states SET epoch=epoch+1,revision=revision+1,owned_ban_revision=NULL,ban_event_id=NULL,counting_since=$2,updated_at=$2 WHERE user_id=$1`, userID, now); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE prompt_audit_enforcement_notifications SET sent_at=COALESCE(sent_at,$3),claimed_at=NULL,last_error='cycle_reset',updated_at=$3 WHERE user_id=$1 AND epoch=$2 AND sent_at IS NULL`, userID, epoch.Int64, now); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return &EnforcementOutcome{Recorded: true, Epoch: epoch.Int64 + 1, UserStatus: "active", CanUnban: false, Reason: "cycle_reset", UserChanged: status == "disabled"}, nil
}

func (r *PostgreSQLRepository) GetEventEnforcement(ctx context.Context, ids []int64) (map[int64]*EnforcementOutcome, error) {
	result := make(map[int64]*EnforcementOutcome)
	if len(ids) == 0 || r == nil || r.db == nil {
		return result, nil
	}
	rows, err := r.db.QueryContext(ctx, `SELECT h.event_id,h.kind,h.counted,h.epoch,h.violation_count,h.window_hours,h.ban_threshold,h.auto_banned,
		CASE WHEN n.id IS NULL OR h.email_status='sent' THEN h.email_status WHEN n.last_error='cycle_reset' THEN 'cancelled' WHEN n.last_error='delivery_failed' THEN 'failed' WHEN n.sent_at IS NOT NULL THEN 'sent' WHEN n.claimed_at IS NOT NULL THEN 'sending' WHEN n.last_error='delivery_retry' THEN 'retry' ELSE 'queued' END,
		h.reason,u.status,(u.status='disabled' AND u.deleted_at IS NULL AND s.owned_ban_revision=s.revision)
		FROM prompt_audit_enforcement_hits h JOIN prompt_audit_enforcement_states s ON s.user_id=h.user_id JOIN users u ON u.id=h.user_id LEFT JOIN prompt_audit_enforcement_notifications n ON n.id=h.notification_id WHERE h.event_id=ANY($1)`, pq.Array(ids))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		outcome := &EnforcementOutcome{Recorded: true}
		var canUnban sql.NullBool
		if err := rows.Scan(&id, &outcome.Kind, &outcome.Counted, &outcome.Epoch, &outcome.ViolationCount, &outcome.WindowHours, &outcome.BanThreshold, &outcome.AutoBanned, &outcome.EmailStatus, &outcome.Reason, &outcome.UserStatus, &canUnban); err != nil {
			return nil, err
		}
		outcome.CanUnban = canUnban.Bool
		result[id] = outcome
	}
	return result, rows.Err()
}
