package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

// StageEvents acknowledges only durable receipt ingestion. Partial counters and
// missing frozen prices cannot prevent later confirmed events being received.
func (r *codexEngineBillingStore) StageEvents(ctx context.Context, page *service.CodexEngineBillingReceivedPage) error {
	if err := r.ready(); err != nil {
		return err
	}
	if page == nil || page.JobID <= 0 || page.ClaimToken == "" || page.Revision < 1 || page.ConsumedSequence < 0 || len(page.Cursor) == 0 || len(page.Cursor) > 8192 || len(page.Events) > 200 {
		return errors.New("invalid engine billing received page")
	}
	// Parse again at the persistence boundary. Persist only the canonical billing
	// projection, never arbitrary response diagnostics, prompts or secret fields.
	events := make([]service.CodexEngineBillingReceivedEvent, len(page.Events))
	for i, in := range page.Events {
		parsed, err := service.ParseCodexEngineBillingUsageEvent(in.Raw)
		if err != nil || parsed.EventID != in.EventID || parsed.Sequence != in.Sequence || parsed.Fingerprint != in.Fingerprint || in.Sequence > page.ConsumedSequence || i > 0 && in.Sequence != events[i-1].Sequence+1 {
			return service.ErrCodexEngineBillingConflict
		}
		var tier *string
		if parsed.ServiceTier != "" {
			value := parsed.ServiceTier
			tier = &value
		}
		events[i] = service.CodexEngineBillingReceivedEvent{EventID: parsed.EventID, Sequence: parsed.Sequence, Fingerprint: parsed.Fingerprint, Raw: parsed.Raw,
			Summary: service.CodexEngineBillingSummaryGroup{Kind: parsed.Kind, Purpose: parsed.Purpose, Model: parsed.Model, ServiceTier: tier, Usage: parsed.UsageValues}}
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	j, err := scanCodexEngineBillingJob(tx.QueryRowContext(ctx, `SELECT `+codexEngineBillingJobColumns+` FROM codex_engine_billing_jobs WHERE id=$1 AND claim_token=$2 AND lease_until > NOW() AND state='pending' FOR UPDATE`, page.JobID, page.ClaimToken))
	if errors.Is(err, service.ErrCodexEngineBillingNotFound) {
		return service.ErrCodexEngineBillingClaimLost
	}
	if err != nil {
		return err
	}
	if j.InstanceID == "" || j.ReceiptID == "" || len(j.SourceSeal) > 0 || page.ConsumedSequence < j.ConsumedSequence || page.Revision < j.Revision {
		return service.ErrCodexEngineBillingConflict
	}
	next := j.ConsumedSequence
	for _, e := range events {
		if e.Sequence > next {
			if e.Sequence != next+1 {
				return service.ErrCodexEngineBillingConflict
			}
			next = e.Sequence
		}
		summary, err := marshalEngineBillingSummary(e.Summary)
		if err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `INSERT INTO codex_engine_billing_received_events (job_id,event_id,sequence,fingerprint,summary,facts)
			VALUES ($1,$2,$3,$4,$5::jsonb,$6::jsonb) ON CONFLICT (job_id,event_id) DO NOTHING`, j.ID, e.EventID, e.Sequence, e.Fingerprint, string(summary), string(e.Raw))
		if err != nil {
			var constraint *pq.Error
			if errors.As(err, &constraint) && constraint.Code == "23505" {
				return service.ErrCodexEngineBillingConflict
			}
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			var fingerprint string
			var sequence int64
			if err := tx.QueryRowContext(ctx, `SELECT fingerprint,sequence FROM codex_engine_billing_received_events WHERE job_id=$1 AND event_id=$2`, j.ID, e.EventID).Scan(&fingerprint, &sequence); err != nil {
				return err
			}
			if fingerprint != e.Fingerprint || sequence != e.Sequence {
				return service.ErrCodexEngineBillingConflict
			}
		}
	}
	if next != page.ConsumedSequence {
		return service.ErrCodexEngineBillingConflict
	}
	_, err = tx.ExecContext(ctx, `UPDATE codex_engine_billing_jobs SET cursor=$2,revision=$3,consumed_sequence=$4,updated_at=NOW() WHERE id=$1`, j.ID, page.Cursor, page.Revision, page.ConsumedSequence)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (r *codexEngineBillingStore) ReadPendingReceived(ctx context.Context, jobID int64, claimToken string, afterSequence int64, limit int) ([]service.CodexEngineBillingReceivedEvent, error) {
	if err := r.ready(); err != nil {
		return nil, err
	}
	if jobID <= 0 || claimToken == "" || afterSequence < 0 || limit < 1 || limit > 200 {
		return nil, errors.New("invalid engine billing received query")
	}
	var live bool
	if err := r.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM codex_engine_billing_jobs WHERE id=$1 AND claim_token=$2 AND lease_until > NOW() AND state IN ('pending','blocked'))`, jobID, claimToken).Scan(&live); err != nil {
		return nil, err
	}
	if !live {
		return nil, service.ErrCodexEngineBillingClaimLost
	}
	rows, err := r.db.QueryContext(ctx, `SELECT event_id,sequence,fingerprint,summary,facts FROM codex_engine_billing_received_events
		WHERE job_id=$1 AND settled_at IS NULL AND sequence>$2 ORDER BY sequence LIMIT $3`, jobID, afterSequence, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]service.CodexEngineBillingReceivedEvent, 0, limit)
	for rows.Next() {
		var e service.CodexEngineBillingReceivedEvent
		var summary, raw []byte
		if err := rows.Scan(&e.EventID, &e.Sequence, &e.Fingerprint, &summary, &raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(summary, &e.Summary); err != nil {
			return nil, err
		}
		e.Raw = raw
		items = append(items, e)
	}
	return items, rows.Err()
}

// AckReceivedSettled completes the durable retry only after both the ledger and
// cache effects succeeded. The ledger remains the authority for money applied.
func (r *codexEngineBillingStore) AckReceivedSettled(ctx context.Context, jobID int64, claimToken, eventID, fingerprint string, sequence int64) error {
	if err := r.ready(); err != nil {
		return err
	}
	if jobID <= 0 || claimToken == "" || sequence <= 0 || !validEngineBillingText(eventID, 256) || !validEngineBillingText(fingerprint, 64) {
		return errors.New("invalid engine billing settlement acknowledgement")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var claimedID int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM codex_engine_billing_jobs WHERE id=$1 AND claim_token=$2 AND lease_until > NOW() AND state IN ('pending','blocked') FOR UPDATE`, jobID, claimToken).Scan(&claimedID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return service.ErrCodexEngineBillingClaimLost
		}
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE codex_engine_billing_received_events e SET settled_at=COALESCE(e.settled_at,NOW())
		FROM codex_engine_billing_events ledger
		WHERE e.job_id=$1 AND e.event_id=$2 AND e.fingerprint=$3 AND e.sequence=$4
		AND ledger.job_id=e.job_id AND ledger.event_id=e.event_id AND ledger.fingerprint=e.fingerprint
		AND ledger.sequence=e.sequence AND ledger.usage_log_id IS NOT NULL`, jobID, eventID, fingerprint, sequence)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return service.ErrCodexEngineBillingConflict
	}
	return tx.Commit()
}
