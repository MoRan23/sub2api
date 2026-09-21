package repository

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
)

const codexTelemetryRuntimePolicyKey = "codex_telemetry_runtime_policy"

type codexTelemetryStore struct{ db *sql.DB }

func NewCodexTelemetryStore(db *sql.DB) service.CodexTelemetryStore {
	return &codexTelemetryStore{db: db}
}

func (r *codexTelemetryStore) available() error {
	if r == nil || r.db == nil {
		return errors.New("Codex telemetry storage unavailable")
	}
	return nil
}

func codexTelemetryPolicyActive(p service.CodexTelemetryPolicy) bool {
	return p.Enabled && (p.Simulation || p.Observation)
}

func readCodexTelemetryPolicy(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, lock string) (service.CodexTelemetryPolicy, error) {
	var raw string
	err := q.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=$1`+lock, codexTelemetryRuntimePolicyKey).Scan(&raw)
	var p service.CodexTelemetryPolicy
	if err != nil {
		return p, err
	}
	if json.Unmarshal([]byte(raw), &p) != nil || p.Epoch <= 0 {
		return p, service.ErrCodexTelemetryInvalidState
	}
	if lock == " FOR SHARE" {
		actual, exists, err := readPublicCodexTelemetryPolicy(ctx, q)
		if err != nil {
			return p, err
		}
		if exists && !sameCodexTelemetryPreferences(p, actual) {
			// Shared holders never upgrade their lock. Publication fails closed
			// until ReadPolicy or the settings callback reconciles exclusively.
			return p, service.ErrCodexTelemetryPolicyChanged
		}
	}
	return p, nil
}

func sameCodexTelemetryPreferences(a, b service.CodexTelemetryPolicy) bool {
	return a.Enabled == b.Enabled && a.Simulation == b.Simulation && a.Observation == b.Observation
}

func readPublicCodexTelemetryPolicy(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (service.CodexTelemetryPolicy, bool, error) {
	var count int
	var enabled, simulation, observation sql.NullString
	err := q.QueryRowContext(ctx, `SELECT count(*),
		MAX(value) FILTER (WHERE key=$1), MAX(value) FILTER (WHERE key=$2), MAX(value) FILTER (WHERE key=$3)
		FROM settings WHERE key IN ($1,$2,$3)`, service.SettingKeyCodexTelemetryEnabled,
		service.SettingKeyCodexTelemetrySimulationEnabled, service.SettingKeyCodexTelemetryObservationEnabled).
		Scan(&count, &enabled, &simulation, &observation)
	if err != nil {
		return service.CodexTelemetryPolicy{}, false, err
	}
	parse := func(value sql.NullString) bool {
		// Match SettingService defaults, including malformed values failing closed.
		if !value.Valid || strings.TrimSpace(value.String) == "" {
			return true
		}
		parsed, err := strconv.ParseBool(strings.TrimSpace(value.String))
		return err == nil && parsed
	}
	return service.CodexTelemetryPolicy{Enabled: parse(enabled), Simulation: parse(simulation), Observation: parse(observation)}, count != 0, nil
}

func (r *codexTelemetryStore) ReadPolicy(ctx context.Context) (service.CodexTelemetryPolicy, error) {
	if err := r.available(); err != nil {
		return service.CodexTelemetryPolicy{}, err
	}
	p, err := readCodexTelemetryPolicy(ctx, r.db, "")
	if err != nil {
		return p, err
	}
	actual, exists, err := readPublicCodexTelemetryPolicy(ctx, r.db)
	if err != nil {
		return p, err
	}
	if !exists || sameCodexTelemetryPreferences(p, actual) {
		return p, nil
	}
	// Recover settings commits whose synchronous runtime callback failed. Sync
	// rereads the public values under the exclusive private-policy lock, so this
	// snapshot cannot overwrite a newer administrator change.
	return r.SyncPolicy(ctx, actual.Enabled, actual.Simulation, actual.Observation)
}

func (r *codexTelemetryStore) SyncPolicy(ctx context.Context, enabled, simulation, observation bool) (service.CodexTelemetryPolicy, error) {
	if err := r.available(); err != nil {
		return service.CodexTelemetryPolicy{}, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return service.CodexTelemetryPolicy{}, err
	}
	defer func() { _ = tx.Rollback() }()
	p, err := readCodexTelemetryPolicy(ctx, tx, " FOR UPDATE")
	if err != nil {
		return p, err
	}
	actual, exists, err := readPublicCodexTelemetryPolicy(ctx, tx)
	if err != nil {
		return p, err
	}
	if exists {
		enabled, simulation, observation = actual.Enabled, actual.Simulation, actual.Observation
	}
	if p.Enabled != enabled || p.Simulation != simulation || p.Observation != observation {
		p.Epoch++
		p.Enabled, p.Simulation, p.Observation = enabled, simulation, observation
		raw, err := json.Marshal(p)
		if err != nil {
			return p, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE settings SET value=$2,updated_at=NOW() WHERE key=$1`, codexTelemetryRuntimePolicyKey, string(raw)); err != nil {
			return p, err
		}
		// Lock order is always policy, then runtime rows. No stale producer can
		// publish into the new generation while this exclusive lock is held.
		if _, err = tx.ExecContext(ctx, `UPDATE codex_telemetry_batches SET
			status=CASE WHEN status='sending' THEN 'unknown' ELSE 'cancelled' END,
			error_code=CASE WHEN status='sending' THEN 'send_result_unknown' ELSE 'policy_changed' END,
			completed_at=NOW(),lease_until=NULL,updated_at=NOW()
			WHERE status IN ('queued','claimed','sending')`); err != nil {
			return p, err
		}
	}
	return p, tx.Commit()
}

func validateCodexTelemetryPoolKey(key service.CodexTelemetryPoolKey) error {
	if key.OwnerAccountID <= 0 || (key.OSFamily != "windows" && key.OSFamily != "macos" && key.OSFamily != "linux" && key.OSFamily != "unknown") || len(key.InstallationID) > 256 || strings.ContainsRune(key.InstallationID, '\x00') {
		return service.ErrCodexTelemetryInvalidState
	}
	return nil
}

const codexTelemetryPoolColumns = `id::text,owner_account_id,os_family,installation_id,seed::text,
	template_version,version,created_at,last_business_at`

func scanCodexTelemetryPool(row interface{ Scan(...any) error }) (service.CodexTelemetryPool, error) {
	var p service.CodexTelemetryPool
	var last sql.NullTime
	err := row.Scan(&p.ID, &p.Key.OwnerAccountID, &p.Key.OSFamily, &p.Key.InstallationID, &p.Seed,
		&p.TemplateVersion, &p.Version, &p.CreatedAt, &last)
	if last.Valid {
		p.LastBusinessAt = last.Time
	}
	return p, err
}

func (r *codexTelemetryStore) TransactPool(ctx context.Context, key service.CodexTelemetryPoolKey, now time.Time, fn func(*service.CodexTelemetryPoolTransaction) error) (*service.CodexTelemetryPool, error) {
	if err := r.available(); err != nil {
		return nil, err
	}
	if err := validateCodexTelemetryPoolKey(key); err != nil {
		return nil, err
	}
	if now.IsZero() || fn == nil {
		return nil, service.ErrCodexTelemetryInvalidState
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	policy, err := readCodexTelemetryPolicy(ctx, tx, " FOR SHARE")
	if err != nil {
		return nil, err
	}
	if !codexTelemetryPolicyActive(policy) {
		return nil, service.ErrCodexTelemetryPolicyDisabled
	}
	var owner int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM accounts WHERE id=$1 AND deleted_at IS NULL AND `+
		codexTurnStateOwnerExpression("credentials")+` FOR SHARE`, key.OwnerAccountID).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrCodexTelemetryInvalidState
	}
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO codex_telemetry_pools
		(id,owner_account_id,os_family,installation_id,seed,created_at,updated_at)
		VALUES($1::uuid,$2,$3,$4,$5::uuid,$6,$6) ON CONFLICT(owner_account_id,os_family,installation_id) DO NOTHING`,
		uuid.NewString(), key.OwnerAccountID, key.OSFamily, key.InstallationID, uuid.NewString(), now); err != nil {
		return nil, err
	}
	pool, err := scanCodexTelemetryPool(tx.QueryRowContext(ctx, `SELECT `+codexTelemetryPoolColumns+`
		FROM codex_telemetry_pools WHERE owner_account_id=$1 AND os_family=$2 AND installation_id=$3 FOR UPDATE`,
		key.OwnerAccountID, key.OSFamily, key.InstallationID))
	if err != nil {
		return nil, err
	}
	activities, err := loadCodexTelemetryActivities(ctx, tx, pool.ID)
	if err != nil {
		return nil, err
	}
	original := make(map[string]service.CodexTelemetryActivity, len(activities))
	for k, a := range activities {
		a.Data = append(json.RawMessage(nil), a.Data...)
		original[k] = a
	}
	state := &service.CodexTelemetryPoolTransaction{Policy: policy, Pool: pool, Activities: activities}
	if err := fn(state); err != nil {
		return nil, err
	}
	// Identity and generation are immutable, even if a callback accidentally
	// changes its copy. LastBusinessAt may only advance due to real traffic.
	if state.Policy != policy || state.Pool.ID != pool.ID || state.Pool.Key != key || state.Pool.Seed != pool.Seed || state.Pool.TemplateVersion <= 0 {
		return nil, service.ErrCodexTelemetryInvalidState
	}
	if state.Pool.LastBusinessAt.Before(pool.LastBusinessAt) {
		state.Pool.LastBusinessAt = pool.LastBusinessAt
	}
	if err = saveCodexTelemetryActivities(ctx, tx, pool.ID, original, state.Activities, now); err != nil {
		return nil, err
	}
	for i := range state.Batches {
		if err = insertCodexTelemetryBatch(ctx, tx, state.Batches[i], pool, policy, now); err != nil {
			return nil, err
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE codex_telemetry_pools SET template_version=$2,
		last_business_at=$3,version=version+1,updated_at=$4 WHERE id=$1::uuid AND version=$5`, pool.ID,
		state.Pool.TemplateVersion, nullableCodexTelemetryTime(state.Pool.LastBusinessAt), now, pool.Version)
	if err != nil {
		return nil, err
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		if err != nil {
			return nil, err
		}
		return nil, service.ErrCodexTelemetryInvalidState
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	state.Pool.Version = pool.Version + 1
	return &state.Pool, nil
}

func loadCodexTelemetryActivities(ctx context.Context, tx *sql.Tx, poolID string) (map[string]service.CodexTelemetryActivity, error) {
	rows, err := tx.QueryContext(ctx, `SELECT kind,activity_key,state,due_at,updated_at FROM codex_telemetry_activities WHERE pool_id=$1::uuid`, poolID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	activities := make(map[string]service.CodexTelemetryActivity)
	for rows.Next() {
		var a service.CodexTelemetryActivity
		var due sql.NullTime
		if err := rows.Scan(&a.Kind, &a.Key, &a.Data, &due, &a.UpdatedAt); err != nil {
			return nil, err
		}
		if due.Valid {
			a.DueAt = due.Time
		}
		activities[service.CodexTelemetryActivityMapKey(a.Kind, a.Key)] = a
	}
	return activities, rows.Err()
}

func saveCodexTelemetryActivities(ctx context.Context, tx *sql.Tx, poolID string, previous, current map[string]service.CodexTelemetryActivity, now time.Time) error {
	for k, a := range previous {
		if _, exists := current[k]; !exists {
			if _, err := tx.ExecContext(ctx, `DELETE FROM codex_telemetry_activities WHERE pool_id=$1::uuid AND kind=$2 AND activity_key=$3`, poolID, a.Kind, a.Key); err != nil {
				return err
			}
		}
	}
	keys := make([]string, 0, len(current))
	for k := range current {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		a := current[k]
		if a.Kind == "" || len(a.Kind) > 64 || a.Key == "" || len(a.Key) > 512 || k != service.CodexTelemetryActivityMapKey(a.Kind, a.Key) {
			return service.ErrCodexTelemetryInvalidState
		}
		if err := service.ValidateCodexTelemetryStoredJSON(a.Data); err != nil {
			return err
		}
		if old, ok := previous[k]; ok && bytes.Equal(old.Data, a.Data) && old.DueAt.Equal(a.DueAt) {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO codex_telemetry_activities(pool_id,kind,activity_key,state,due_at,updated_at)
			VALUES($1::uuid,$2,$3,$4::jsonb,$5,$6) ON CONFLICT(pool_id,kind,activity_key) DO UPDATE SET
			state=EXCLUDED.state,due_at=EXCLUDED.due_at,updated_at=EXCLUDED.updated_at`,
			poolID, a.Kind, a.Key, []byte(a.Data), nullableCodexTelemetryTime(a.DueAt), now); err != nil {
			return err
		}
	}
	return nil
}

func insertCodexTelemetryBatch(ctx context.Context, tx *sql.Tx, b service.CodexTelemetryBatch, pool service.CodexTelemetryPool, policy service.CodexTelemetryPolicy, now time.Time) error {
	if b.PolicyEpoch != 0 && b.PolicyEpoch != policy.Epoch {
		return service.ErrCodexTelemetryPolicyChanged
	}
	if (pool.Key.OSFamily == "unknown" && b.Source != "observed") ||
		(b.Source == "simulated" && !policy.Simulation) || (b.Source == "observed" && !policy.Observation) ||
		(b.Source == "mixed" && (!policy.Simulation || !policy.Observation)) {
		return service.ErrCodexTelemetryInvalidState
	}
	if b.ID == "" {
		b.ID = uuid.NewString()
	}
	if _, err := uuid.Parse(b.ID); err != nil || (b.Type != "analytics" && b.Type != "metrics") ||
		(b.Source != "observed" && b.Source != "simulated" && b.Source != "mixed") ||
		(b.ProxyID != nil && *b.ProxyID <= 0) || len(b.UserAgent) > 2048 || len(b.Originator) > 256 || len(b.ClientVersion) > 128 {
		return service.ErrCodexTelemetryInvalidState
	}
	if b.AccountID == 0 {
		b.AccountID = pool.Key.OwnerAccountID
	}
	if b.AccountID <= 0 {
		return service.ErrCodexTelemetryInvalidState
	}
	if b.CreatedAt.IsZero() {
		b.CreatedAt = now
	}
	if b.NotBefore.IsZero() {
		b.NotBefore = now
	}
	if len(b.Metadata) == 0 {
		b.Metadata = json.RawMessage(`{}`)
	}
	for _, data := range []json.RawMessage{b.Payload, b.Metadata} {
		if err := service.ValidateCodexTelemetryStoredJSON(data); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO codex_telemetry_batches
		(id,pool_id,policy_epoch,account_id,proxy_id,type,source,user_agent,originator,client_version,payload,metadata,not_before,created_at,updated_at)
		VALUES($1::uuid,$2::uuid,$3,$4,$5,$6,$7,$8,$9,$10,$11::jsonb,$12::jsonb,$13,$14,$15)`,
		b.ID, pool.ID, policy.Epoch, b.AccountID, b.ProxyID, b.Type, b.Source, b.UserAgent, b.Originator, b.ClientVersion,
		[]byte(b.Payload), []byte(b.Metadata), b.NotBefore, b.CreatedAt, now)
	return err
}

func nullableCodexTelemetryTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

func codexTelemetryLimit(limit int) int {
	if limit <= 0 {
		return 256
	}
	if limit > 4096 {
		return 4096
	}
	return limit
}

func (r *codexTelemetryStore) ListDuePools(ctx context.Context, now time.Time, limit int) ([]service.CodexTelemetryPoolKey, error) {
	if err := r.available(); err != nil {
		return nil, err
	}
	rows, err := r.db.QueryContext(ctx, `SELECT p.owner_account_id,p.os_family,p.installation_id
		FROM codex_telemetry_pools p JOIN codex_telemetry_activities a ON a.pool_id=p.id
		WHERE a.due_at <= $1 GROUP BY p.id ORDER BY MIN(a.due_at),p.id LIMIT $2`, now, codexTelemetryLimit(limit))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	keys := make([]service.CodexTelemetryPoolKey, 0)
	for rows.Next() {
		var key service.CodexTelemetryPoolKey
		if err := rows.Scan(&key.OwnerAccountID, &key.OSFamily, &key.InstallationID); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

const codexTelemetryBatchColumns = `b.id::text,b.sequence,b.policy_epoch,b.account_id,b.proxy_id,b.type,b.source,
	b.user_agent,b.originator,b.client_version,b.payload,b.metadata,b.created_at,b.not_before,b.status,
	COALESCE(b.claim_id::text,''),b.lease_until,b.started_at,b.completed_at,b.http_status,b.error_code,
	p.id::text,p.owner_account_id,p.os_family,p.installation_id,p.seed::text,p.template_version,p.version,p.created_at,p.last_business_at`

func scanCodexTelemetryBatch(row interface{ Scan(...any) error }) (service.CodexTelemetryBatch, error) {
	var b service.CodexTelemetryBatch
	var proxy sql.NullInt64
	var lease, started, completed, business sql.NullTime
	err := row.Scan(&b.ID, &b.Sequence, &b.PolicyEpoch, &b.AccountID, &proxy, &b.Type, &b.Source, &b.UserAgent, &b.Originator, &b.ClientVersion,
		&b.Payload, &b.Metadata, &b.CreatedAt, &b.NotBefore, &b.Status, &b.ClaimID, &lease, &started, &completed,
		&b.HTTPStatus, &b.ErrorCode, &b.Pool.ID, &b.Pool.Key.OwnerAccountID, &b.Pool.Key.OSFamily, &b.Pool.Key.InstallationID,
		&b.Pool.Seed, &b.Pool.TemplateVersion, &b.Pool.Version, &b.Pool.CreatedAt, &business)
	if proxy.Valid {
		b.ProxyID = &proxy.Int64
	}
	if lease.Valid {
		b.LeaseUntil = lease.Time
	}
	if started.Valid {
		b.StartedAt = started.Time
	}
	if completed.Valid {
		b.CompletedAt = completed.Time
	}
	if business.Valid {
		b.Pool.LastBusinessAt = business.Time
	}
	return b, err
}

func (r *codexTelemetryStore) ClaimBatches(ctx context.Context, now time.Time, limit int) ([]service.CodexTelemetryBatch, error) {
	if err := r.available(); err != nil {
		return nil, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	p, err := readCodexTelemetryPolicy(ctx, tx, " FOR SHARE")
	if err != nil {
		return nil, err
	}
	if !codexTelemetryPolicyActive(p) {
		return nil, nil
	}
	if err := maintainCodexTelemetryBatches(ctx, tx, now, false); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+codexTelemetryBatchColumns+` FROM codex_telemetry_batches b
		JOIN codex_telemetry_pools p ON p.id=b.pool_id WHERE b.status='queued' AND b.policy_epoch=$1 AND b.not_before<=$2
		AND NOT EXISTS (SELECT 1 FROM codex_telemetry_batches earlier WHERE earlier.pool_id=b.pool_id
			AND earlier.sequence<b.sequence AND earlier.status IN ('queued','claimed','sending'))
		ORDER BY b.sequence LIMIT $3 FOR UPDATE OF b SKIP LOCKED`, p.Epoch, now, codexTelemetryLimit(limit))
	if err != nil {
		return nil, err
	}
	batches := make([]service.CodexTelemetryBatch, 0)
	for rows.Next() {
		b, scanErr := scanCodexTelemetryBatch(rows)
		if scanErr != nil {
			_ = rows.Close()
			return nil, scanErr
		}
		batches = append(batches, b)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	for i := range batches {
		b := &batches[i]
		b.ClaimID, b.Status, b.LeaseUntil = uuid.NewString(), "claimed", now.Add(30*time.Second)
		if _, err := tx.ExecContext(ctx, `UPDATE codex_telemetry_batches SET claim_id=$2::uuid,
			status='claimed',lease_until=$3,updated_at=$4 WHERE id=$1::uuid`, b.ID, b.ClaimID, b.LeaseUntil, now); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return batches, nil
}

func (r *codexTelemetryStore) MarkSending(ctx context.Context, batchID, claimID string, epoch int64, now time.Time) (bool, error) {
	if err := r.available(); err != nil {
		return false, err
	}
	if _, err := uuid.Parse(batchID); err != nil {
		return false, service.ErrCodexTelemetryInvalidState
	}
	if _, err := uuid.Parse(claimID); err != nil {
		return false, service.ErrCodexTelemetryInvalidState
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	p, err := readCodexTelemetryPolicy(ctx, tx, " FOR SHARE")
	if err != nil {
		return false, err
	}
	if !codexTelemetryPolicyActive(p) || p.Epoch != epoch {
		return false, nil
	}
	result, err := tx.ExecContext(ctx, `UPDATE codex_telemetry_batches SET status='sending',started_at=$4,
		lease_until=$4::timestamptz+INTERVAL '30 seconds',updated_at=$4 WHERE id=$1::uuid AND claim_id=$2::uuid
		AND policy_epoch=$3 AND status='claimed' AND lease_until>$4 AND created_at>$4::timestamptz-INTERVAL '24 hours'`, batchID, claimID, epoch, now)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, tx.Commit()
}

var codexTelemetryErrorCodePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,127}$`)

func validateCodexTelemetryBatchResult(result service.CodexTelemetryBatchResult) error {
	switch result.Status {
	case "sent", "failed", "dropped", "cancelled", "skipped", "unknown":
	default:
		return service.ErrCodexTelemetryInvalidState
	}
	if result.HTTPStatus < 0 || result.HTTPStatus > 599 || (result.ErrorCode != "" && !codexTelemetryErrorCodePattern.MatchString(result.ErrorCode)) {
		return service.ErrCodexTelemetryInvalidState
	}
	return nil
}

func (r *codexTelemetryStore) CompleteBatch(ctx context.Context, batchID, claimID string, result service.CodexTelemetryBatchResult, now time.Time) (bool, error) {
	if err := r.available(); err != nil {
		return false, err
	}
	if err := validateCodexTelemetryBatchResult(result); err != nil {
		return false, err
	}
	if _, err := uuid.Parse(batchID); err != nil {
		return false, service.ErrCodexTelemetryInvalidState
	}
	if _, err := uuid.Parse(claimID); err != nil {
		return false, service.ErrCodexTelemetryInvalidState
	}
	res, err := r.db.ExecContext(ctx, `UPDATE codex_telemetry_batches SET status=$3,http_status=$4,
		error_code=$5,completed_at=$6,lease_until=NULL,updated_at=$6 WHERE id=$1::uuid AND claim_id=$2::uuid
		AND status IN ('claimed','sending') AND lease_until>$6`, batchID, claimID, result.Status, result.HTTPStatus, result.ErrorCode, now)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func maintainCodexTelemetryBatches(ctx context.Context, tx *sql.Tx, now time.Time, cleanup bool) error {
	queries := []string{
		`UPDATE codex_telemetry_batches SET status='unknown',completed_at=$1,lease_until=NULL,error_code='send_result_unknown',updated_at=$1 WHERE status='sending' AND lease_until<=$1`,
		`UPDATE codex_telemetry_batches SET status='dropped',completed_at=$1,lease_until=NULL,error_code='batch_expired',updated_at=$1 WHERE status IN ('queued','claimed') AND created_at<=$1::timestamptz-INTERVAL '24 hours'`,
		`UPDATE codex_telemetry_batches SET status='queued',claim_id=NULL,lease_until=NULL,updated_at=$1 WHERE status='claimed' AND lease_until<=$1`,
	}
	if cleanup {
		queries = append(queries, `DELETE FROM codex_telemetry_batches WHERE completed_at<=$1::timestamptz-INTERVAL '7 days'`)
	}
	for _, q := range queries {
		if _, err := tx.ExecContext(ctx, q, now); err != nil {
			return err
		}
	}
	return nil
}

func (r *codexTelemetryStore) Maintain(ctx context.Context, now time.Time) error {
	if err := r.available(); err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := readCodexTelemetryPolicy(ctx, tx, " FOR SHARE"); err != nil {
		return err
	}
	if err := maintainCodexTelemetryBatches(ctx, tx, now, true); err != nil {
		return fmt.Errorf("maintain Codex telemetry batches: %w", err)
	}
	return tx.Commit()
}
