package repository

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/lib/pq"
)

type codexEngineBillingStore struct{ db *sql.DB }

var _ service.CodexEngineBillingStore = (*codexEngineBillingStore)(nil)

func NewCodexEngineBillingStore(db *sql.DB) service.CodexEngineBillingStore {
	return &codexEngineBillingStore{db: db}
}

const codexEngineBillingJobColumns = `id, client_request_id, account_id, user_id, api_key_id,
	identity_digest, instance_id, snapshot, receipt_id, state, cursor, revision, consumed_sequence, next_attempt_at,
	claim_token, lease_until, attempt_count, linked_job_id, terminal_seal, last_error, created_at, updated_at, source_seal, settlement_cursor, delivery_outcome`

type engineBillingRowScanner interface{ Scan(...any) error }

func scanCodexEngineBillingJob(row engineBillingRowScanner) (*service.CodexEngineBillingJob, error) {
	j := &service.CodexEngineBillingJob{}
	var snapshot, seal, sourceSeal []byte
	err := row.Scan(&j.ID, &j.ClientRequestID, &j.AccountID, &j.UserID, &j.APIKeyID,
		&j.IdentityDigest, &j.InstanceID, &snapshot, &j.ReceiptID, &j.State, &j.Cursor, &j.Revision, &j.ConsumedSequence, &j.NextAttemptAt,
		&j.ClaimToken, &j.LeaseUntil, &j.AttemptCount, &j.LinkedJobID, &seal,
		&j.LastError, &j.CreatedAt, &j.UpdatedAt, &sourceSeal, &j.SettlementCursor, &j.DeliveryOutcome)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrCodexEngineBillingNotFound
	}
	j.Snapshot, j.TerminalSeal, j.SourceSeal = snapshot, seal, sourceSeal
	return j, err
}

func (r *codexEngineBillingStore) ready() error {
	if r == nil || r.db == nil {
		return errors.New("engine billing store db is nil")
	}
	return nil
}

func (r *codexEngineBillingStore) CreatePending(ctx context.Context, in *service.CodexEngineBillingPending) (*service.CodexEngineBillingJob, error) {
	if err := r.ready(); err != nil {
		return nil, err
	}
	if in == nil || !validEngineBillingText(in.ClientRequestID, 128) || !validEngineBillingText(in.IdentityDigest, 128) || !validEngineBillingText(in.InstanceID, 256) || in.AccountID <= 0 || in.UserID <= 0 || in.APIKeyID <= 0 {
		return nil, errors.New("invalid engine billing pending identity")
	}
	if err := validateEngineBillingSnapshot(in.Snapshot); err != nil {
		return nil, err
	}
	next := in.NextAttemptAt
	if next.IsZero() {
		next = time.Now()
	}
	j, err := scanCodexEngineBillingJob(r.db.QueryRowContext(ctx, `
		INSERT INTO codex_engine_billing_jobs (client_request_id, account_id, user_id, api_key_id, identity_digest, instance_id, snapshot, next_attempt_at, delivery_outcome)
		VALUES ($1,$2,$3,$4,$5,$6,$7::jsonb,$8,'pending')
		ON CONFLICT (client_request_id) DO NOTHING RETURNING `+codexEngineBillingJobColumns,
		in.ClientRequestID, in.AccountID, in.UserID, in.APIKeyID, in.IdentityDigest, in.InstanceID, string(in.Snapshot), next))
	if !errors.Is(err, service.ErrCodexEngineBillingNotFound) {
		return j, err
	}
	// The first dispatch owns the original request identity and billing policy.
	// Never replace them on a replay of the client request ID.
	j, err = scanCodexEngineBillingJob(r.db.QueryRowContext(ctx, `SELECT `+codexEngineBillingJobColumns+` FROM codex_engine_billing_jobs WHERE client_request_id=$1`, in.ClientRequestID))
	if err != nil {
		return nil, err
	}
	if j.AccountID != in.AccountID || j.UserID != in.UserID || j.APIKeyID != in.APIKeyID || j.IdentityDigest != in.IdentityDigest || j.InstanceID != in.InstanceID {
		return nil, service.ErrCodexEngineBillingConflict
	}
	return j, nil
}

func (r *codexEngineBillingStore) FinalizeDelivery(ctx context.Context, id int64, outcome string) error {
	if err := r.ready(); err != nil {
		return err
	}
	if id <= 0 || (outcome != service.CodexEngineBillingDelivered && outcome != service.CodexEngineBillingNoCustomerCharge) {
		return service.ErrCodexEngineBillingConflict
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE codex_engine_billing_jobs SET delivery_outcome=$2, updated_at=NOW(),
		next_attempt_at=CASE WHEN $2='delivered' THEN NOW() ELSE next_attempt_at END,
		claim_token=CASE WHEN $2='no_customer_charge' THEN '' ELSE claim_token END,
		lease_until=CASE WHEN $2='no_customer_charge' THEN NULL ELSE lease_until END
		WHERE id=$1 AND (delivery_outcome='pending' OR delivery_outcome=$2)`, id, outcome)
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
	if outcome == service.CodexEngineBillingNoCustomerCharge {
		// Incomplete delivery is terminal. Discard already staged facts together
		// with the outcome so a stale worker can never turn them into a usage log.
		if _, err := tx.ExecContext(ctx, `DELETE FROM codex_engine_billing_received_events WHERE job_id=$1`, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (r *codexEngineBillingStore) Get(ctx context.Context, id int64) (*service.CodexEngineBillingJob, error) {
	if err := r.ready(); err != nil {
		return nil, err
	}
	return scanCodexEngineBillingJob(r.db.QueryRowContext(ctx, `SELECT `+codexEngineBillingJobColumns+` FROM codex_engine_billing_jobs WHERE id=$1`, id))
}

func (r *codexEngineBillingStore) MarkNotDispatched(ctx context.Context, id int64) error {
	if err := r.ready(); err != nil {
		return err
	}
	result, err := r.db.ExecContext(ctx, `UPDATE codex_engine_billing_jobs SET state='not_dispatched',claim_token='',lease_until=NULL,updated_at=NOW()
		WHERE id=$1 AND state IN ('pending','not_dispatched') AND receipt_id=''`, id)
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
	return nil
}

func (r *codexEngineBillingStore) BindReceipt(ctx context.Context, id int64, instanceID, receipt, claimToken string) (*service.CodexEngineBillingJob, error) {
	if err := r.ready(); err != nil {
		return nil, err
	}
	if !validEngineBillingText(instanceID, 256) || !validEngineBillingText(receipt, 256) || claimToken == "" {
		return nil, service.ErrCodexEngineBillingConflict
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// Serialize receipt ownership before taking any job lock, including when
	// two new client requests discover the same receipt concurrently.
	digest := engineBillingIdentityHash(instanceID, receipt, "")
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(binary.BigEndian.Uint64(digest[:8]))); err != nil {
		return nil, err
	}
	j, err := scanCodexEngineBillingJob(tx.QueryRowContext(ctx, `SELECT `+codexEngineBillingJobColumns+` FROM codex_engine_billing_jobs WHERE id=$1 AND claim_token=$2 AND lease_until > NOW() AND state='pending' FOR UPDATE`, id, claimToken))
	if errors.Is(err, service.ErrCodexEngineBillingNotFound) {
		return nil, service.ErrCodexEngineBillingClaimLost
	}
	if err != nil {
		return nil, err
	}
	if (j.ReceiptID != "" && j.ReceiptID != receipt) || (j.InstanceID != "" && j.InstanceID != instanceID) {
		return nil, service.ErrCodexEngineBillingConflict
	}
	canonical, err := scanCodexEngineBillingJob(tx.QueryRowContext(ctx, `SELECT `+codexEngineBillingJobColumns+` FROM codex_engine_billing_jobs WHERE instance_id=$1 AND receipt_id=$2 AND state <> 'linked' FOR UPDATE`, instanceID, receipt))
	if err != nil && !errors.Is(err, service.ErrCodexEngineBillingNotFound) {
		return nil, err
	}
	if canonical != nil && canonical.ID != j.ID {
		if j.State == service.CodexEngineBillingSealedState {
			return nil, service.ErrCodexEngineBillingConflict
		}
		_, err = tx.ExecContext(ctx, `UPDATE codex_engine_billing_jobs SET receipt_id=$2, state='linked', linked_job_id=$3, instance_id=$4, claim_token='', lease_until=NULL, updated_at=NOW() WHERE id=$1`, id, receipt, canonical.ID, instanceID)
		if err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return canonical, nil
	}
	if j.State == service.CodexEngineBillingLinkedState {
		return nil, service.ErrCodexEngineBillingConflict
	}
	j, err = scanCodexEngineBillingJob(tx.QueryRowContext(ctx, `UPDATE codex_engine_billing_jobs SET receipt_id=$2, instance_id=$3, updated_at=NOW() WHERE id=$1 RETURNING `+codexEngineBillingJobColumns, id, receipt, instanceID))
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return j, nil
}

func (r *codexEngineBillingStore) ClaimDue(ctx context.Context, limit int, lease time.Duration) ([]*service.CodexEngineBillingJob, error) {
	if err := r.ready(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		return nil, nil
	}
	if limit > 100 {
		limit = 100
	}
	if lease < time.Second || lease > 15*time.Minute {
		return nil, errors.New("invalid engine billing lease duration")
	}
	rows, err := r.db.QueryContext(ctx, `WITH due AS (
		SELECT id FROM codex_engine_billing_jobs
		WHERE (state='pending' OR (state='blocked' AND EXISTS (
			SELECT 1 FROM codex_engine_billing_received_events e WHERE e.job_id=codex_engine_billing_jobs.id AND e.settled_at IS NULL)))
		AND delivery_outcome='delivered'
		AND next_attempt_at <= NOW() AND (lease_until IS NULL OR lease_until <= NOW())
		ORDER BY next_attempt_at, id LIMIT $1 FOR UPDATE SKIP LOCKED
	) UPDATE codex_engine_billing_jobs SET claim_token=$2, lease_until=NOW()+($3 * INTERVAL '1 millisecond'),
		attempt_count=attempt_count+1, updated_at=NOW() WHERE id IN (SELECT id FROM due)
		RETURNING `+codexEngineBillingJobColumns, limit, uuid.NewString(), lease.Milliseconds())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := make([]*service.CodexEngineBillingJob, 0, limit)
	for rows.Next() {
		j, err := scanCodexEngineBillingJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

func (r *codexEngineBillingStore) SaveProgress(ctx context.Context, p *service.CodexEngineBillingProgress) error {
	if err := r.ready(); err != nil {
		return err
	}
	if p == nil || p.JobID <= 0 || p.ClaimToken == "" || p.Revision < 0 || p.ConsumedSequence < 0 || p.SettlementCursor < 0 || p.SettlementCursor > p.ConsumedSequence || len(p.Cursor) > 8192 || len(p.LastError) > 2048 {
		return errors.New("invalid engine billing progress")
	}
	if p.State != service.CodexEngineBillingPendingState && p.State != service.CodexEngineBillingBlockedState && p.State != service.CodexEngineBillingSealedState {
		return errors.New("invalid engine billing state")
	}
	var seal any
	if p.State == service.CodexEngineBillingSealedState {
		if err := validateEngineBillingJSON(p.TerminalSeal); err != nil {
			return fmt.Errorf("invalid engine billing terminal seal: %w", err)
		}
		seal = string(p.TerminalSeal)
	} else if len(p.TerminalSeal) > 0 {
		return errors.New("engine billing terminal seal requires sealed state")
	}
	var sourceSeal any
	if len(p.SourceSeal) > 0 {
		if err := validateEngineBillingJSON(p.SourceSeal); err != nil {
			return fmt.Errorf("invalid engine billing source seal: %w", err)
		}
		sourceSeal = string(p.SourceSeal)
	}
	if p.State == service.CodexEngineBillingSealedState && sourceSeal == nil {
		sourceSeal = seal
	}
	next := p.NextAttemptAt
	if next.IsZero() {
		next = time.Now()
	}
	result, err := r.db.ExecContext(ctx, `UPDATE codex_engine_billing_jobs SET cursor=$3, revision=$4, next_attempt_at=$5,
		state=$6, terminal_seal=$7::jsonb, last_error=$8, consumed_sequence=$9, claim_token='', lease_until=NULL, updated_at=NOW(),
		source_seal=COALESCE($10::jsonb,source_seal), settlement_cursor=$11
		WHERE id=$1 AND claim_token=$2 AND lease_until > NOW() AND state IN ('pending','blocked') AND revision <= $4
		AND consumed_sequence <= $9
		AND (SELECT COUNT(*) FROM codex_engine_billing_received_events WHERE job_id=$1 AND sequence <= $9) = $9
		AND ($10::jsonb IS NULL OR (receipt_id <> '' AND instance_id <> '' AND (source_seal IS NULL OR source_seal=$10::jsonb) AND ($10::jsonb->>'last_sequence')::bigint=$9))
		AND ($6 <> 'sealed' OR (receipt_id <> '' AND instance_id <> '' AND COALESCE($10::jsonb,source_seal)=$7::jsonb
			AND NOT EXISTS (SELECT 1 FROM codex_engine_billing_received_events WHERE job_id=$1 AND settled_at IS NULL)))`,
		p.JobID, p.ClaimToken, p.Cursor, p.Revision, next, p.State, seal, p.LastError, p.ConsumedSequence, sourceSeal, p.SettlementCursor)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return service.ErrCodexEngineBillingClaimLost
	}
	return nil
}

func (r *codexEngineBillingStore) ApplyEvent(ctx context.Context, e *service.CodexEngineBillingEvent) (*service.UsageBillingApplyResult, error) {
	if err := r.ready(); err != nil {
		return nil, err
	}
	if e == nil || e.JobID <= 0 || e.ClaimToken == "" || e.Sequence <= 0 || !validEngineBillingText(e.EventID, 256) || !validEngineBillingText(e.Fingerprint, 64) {
		return nil, errors.New("invalid engine billing event")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	j, err := scanCodexEngineBillingJob(tx.QueryRowContext(ctx, `SELECT `+codexEngineBillingJobColumns+` FROM codex_engine_billing_jobs WHERE id=$1 AND claim_token=$2 AND lease_until > NOW() AND state IN ('pending','blocked') FOR UPDATE`, e.JobID, e.ClaimToken))
	if errors.Is(err, service.ErrCodexEngineBillingNotFound) {
		return nil, service.ErrCodexEngineBillingClaimLost
	}
	if err != nil {
		return nil, err
	}
	if j.DeliveryOutcome == service.CodexEngineBillingNoCustomerCharge {
		// Defensive for stale callers: incomplete responses have neither customer
		// debit nor usage/account-cost records, regardless of receipt contents.
		return nil, service.ErrCodexEngineBillingClaimLost
	}
	if j.DeliveryOutcome != service.CodexEngineBillingDelivered {
		return nil, service.ErrCodexEngineBillingDeliveryPending
	}
	if j.ReceiptID == "" || j.InstanceID == "" {
		return nil, service.ErrCodexEngineBillingConflict
	}
	var receivedFingerprint string
	var receivedSequence int64
	if err := tx.QueryRowContext(ctx, `SELECT fingerprint,sequence FROM codex_engine_billing_received_events WHERE job_id=$1 AND event_id=$2`, j.ID, e.EventID).Scan(&receivedFingerprint, &receivedSequence); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, service.ErrCodexEngineBillingConflict
		}
		return nil, err
	}
	if receivedFingerprint != e.Fingerprint || receivedSequence != e.Sequence {
		return nil, service.ErrCodexEngineBillingConflict
	}
	cmd, log := e.Command, e.UsageLog
	if err := bindEngineBillingEventIdentity(j, e, &cmd, &log); err != nil {
		return nil, err
	}
	summary, err := marshalEngineBillingSummary(e.Summary)
	if err != nil {
		return nil, err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO codex_engine_billing_events (job_id, event_id, fingerprint, billing_request_id, sequence, summary)
		VALUES ($1,$2,$3,$4,$5,$6::jsonb) ON CONFLICT (job_id,event_id) DO NOTHING`, j.ID, e.EventID, e.Fingerprint, cmd.RequestID, e.Sequence, string(summary))
	if err != nil {
		var constraintErr *pq.Error
		if errors.As(err, &constraintErr) && constraintErr.Code == "23505" {
			return nil, service.ErrCodexEngineBillingConflict
		}
		return nil, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if n == 0 {
		var fingerprint string
		var sequence int64
		if err := tx.QueryRowContext(ctx, `SELECT fingerprint, sequence FROM codex_engine_billing_events WHERE job_id=$1 AND event_id=$2`, j.ID, e.EventID).Scan(&fingerprint, &sequence); err != nil {
			return nil, err
		}
		if fingerprint != e.Fingerprint || sequence != e.Sequence {
			return nil, service.ErrCodexEngineBillingConflict
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return &service.UsageBillingApplyResult{Applied: false}, nil
	}
	billing := &usageBillingRepository{db: r.db}
	applied, err := billing.claimUsageBillingKey(ctx, tx, &cmd)
	if err != nil {
		return nil, err
	}
	if !applied {
		return nil, service.ErrCodexEngineBillingConflict
	}
	out := &service.UsageBillingApplyResult{Applied: true}
	if err := billing.applyUsageBillingEffects(ctx, tx, &cmd, out); err != nil {
		return nil, err
	}
	if e.PlatformQuotaCost > 0 {
		if err := incrementEngineBillingPlatformQuota(ctx, tx, j.UserID, e.Platform, e.PlatformQuotaCost, time.Now()); err != nil {
			return nil, err
		}
	}
	// Use the ordinary insert shape, but stay in this SQL transaction. Neither
	// the best-effort batcher nor an asynchronous usage worker participates.
	inserted, err := (&usageLogRepository{}).createSingle(ctx, tx, &log)
	if err != nil {
		return nil, err
	}
	if !inserted {
		return nil, service.ErrCodexEngineBillingConflict
	}
	if _, err := tx.ExecContext(ctx, `UPDATE codex_engine_billing_events SET usage_log_id=$3 WHERE job_id=$1 AND event_id=$2`, j.ID, e.EventID, log.ID); err != nil {
		return nil, err
	}
	// Keep the inbox pending until the caller acknowledges successful cache
	// updates. Replaying this committed event only retries those cache effects.
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	e.Command, e.UsageLog = cmd, log
	return out, nil
}

func bindEngineBillingEventIdentity(j *service.CodexEngineBillingJob, e *service.CodexEngineBillingEvent, cmd *service.UsageBillingCommand, log *service.UsageLog) error {
	for _, pair := range [][2]int64{{cmd.AccountID, j.AccountID}, {cmd.UserID, j.UserID}, {cmd.APIKeyID, j.APIKeyID}, {log.AccountID, j.AccountID}, {log.UserID, j.UserID}, {log.APIKeyID, j.APIKeyID}} {
		if pair[0] != 0 && pair[0] != pair[1] {
			return service.ErrCodexEngineBillingConflict
		}
	}
	for _, cost := range []float64{cmd.BalanceCost, cmd.SubscriptionCost, cmd.APIKeyQuotaCost, cmd.APIKeyRateLimitCost, cmd.AccountQuotaCost, e.PlatformQuotaCost} {
		if math.IsNaN(cost) || math.IsInf(cost, 0) || cost < 0 {
			return errors.New("invalid engine billing event cost")
		}
	}
	if cmd.InputTokens < 0 || cmd.OutputTokens < 0 || cmd.CacheCreationTokens < 0 || cmd.CacheReadTokens < 0 || cmd.ImageCount < 0 {
		return errors.New("invalid engine billing event usage")
	}
	digest := engineBillingIdentityHash(j.InstanceID, j.ReceiptID, e.EventID)
	cmd.RequestID = "ce2:" + base64.RawURLEncoding.EncodeToString(digest[:])
	cmd.RequestFingerprint = e.Fingerprint
	cmd.UserID, cmd.APIKeyID, cmd.AccountID = j.UserID, j.APIKeyID, j.AccountID
	cmd.Normalize()
	log.RequestID, log.UserID, log.APIKeyID, log.AccountID = cmd.RequestID, j.UserID, j.APIKeyID, j.AccountID
	return nil
}

func engineBillingIdentityHash(instanceID, receipt, event string) [32]byte {
	// Length-prefixed JSON avoids ambiguities in arbitrary upstream IDs.
	raw, _ := json.Marshal([]string{instanceID, receipt, event})
	return sha256.Sum256(raw)
}

func validEngineBillingText(s string, max int) bool {
	return s != "" && s == strings.TrimSpace(s) && len(s) <= max && !strings.ContainsRune(s, '\x00')
}

func validateEngineBillingJSON(raw json.RawMessage) error {
	if len(raw) == 0 || len(raw) > 256*1024 || !json.Valid(raw) {
		return errors.New("expected bounded JSON object")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return errors.New("expected JSON object")
	}
	return nil
}

func validateEngineBillingSnapshot(raw json.RawMessage) error {
	if err := validateEngineBillingJSON(raw); err != nil {
		return err
	}
	var walk func(json.RawMessage) error
	walk = func(value json.RawMessage) error {
		var object map[string]json.RawMessage
		if json.Unmarshal(value, &object) == nil && object != nil {
			for key, child := range object {
				switch strings.ToLower(key) {
				case "authorization", "api_key", "access_token", "refresh_token", "password", "credentials", "headers", "prompt", "body":
					return errors.New("engine billing snapshot contains a prohibited secret or request field")
				}
				if err := walk(child); err != nil {
					return err
				}
			}
			return nil
		}
		var list []json.RawMessage
		if json.Unmarshal(value, &list) == nil {
			for _, child := range list {
				if err := walk(child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk(raw)
}
