package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
	"github.com/redis/go-redis/v9"
)

// PostgreSQL is authoritative. There is intentionally no in-memory or Redis
// token cache which could survive an account's configuration being disabled.
type openAICodexStateRepository struct {
	db  *sql.DB
	rdb *redis.Client
}

func NewOpenAICodexStateRepository(db *sql.DB, rdb *redis.Client) service.CodexTurnStateRepository {
	return &openAICodexStateRepository{db: db, rdb: rdb}
}

const codexStateLiveAccount = `a.deleted_at IS NULL AND a.platform = 'openai' AND a.type = 'oauth'
 AND a.extra->'codex_turn_state'->>'enabled' = 'true'
 AND a.extra->>'codex_turn_state_generation' = s.generation`

const codexStateColumns = `s.owner_account_id, s.model, s.generation, s.version,
 s.encrypted_token, s.issued_at, s.expires_at, s.token_length, s.cipher_blocks,
 s.source, s.shape, s.refresh_reason, s.last_business_at, s.last_collected_at,
 s.next_collect_at, s.collector_paused, s.last_error`

func validateCodexStateKey(key service.CodexTurnStateKey) error {
	if key.OwnerAccountID <= 0 || strings.TrimSpace(key.Model) == "" || strings.TrimSpace(key.Generation) == "" {
		return errors.New("invalid Codex turn-state key")
	}
	return nil
}

func (r *openAICodexStateRepository) databaseAvailable() error {
	if r == nil || r.db == nil {
		return errors.New("Codex turn-state database unavailable")
	}
	return nil
}

// The shared row lock conflicts with account configuration's FOR NO KEY UPDATE
// lock, so a disabled/replaced generation cannot race a successful publication.
func lockCodexStateGeneration(ctx context.Context, tx *sql.Tx, key service.CodexTurnStateKey) (bool, error) {
	var found int64
	err := tx.QueryRowContext(ctx, `SELECT a.id FROM accounts a
		WHERE a.id = $1 AND a.deleted_at IS NULL AND a.platform = 'openai' AND a.type = 'oauth'
		AND a.extra->'codex_turn_state'->>'enabled' = 'true'
		AND a.extra->>'codex_turn_state_generation' = $2 FOR SHARE`, key.OwnerAccountID, key.Generation).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// The revision row is the policy's publication barrier. Settings saves update
// the model list and revision in one statement, which cannot commit while this
// shared lock is held. Acquire it before the account lock everywhere: settings
// writes never need account locks, and absent default settings need a real row
// so their first explicit update is fenced too.
func lockCodexStateModelPolicy(ctx context.Context, tx *sql.Tx, record service.CodexTurnStateRecord) (bool, error) {
	if record.ModelPolicyRevision == "" {
		return false, nil
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO settings (key, value, updated_at)
		VALUES ($1, '', NOW()) ON CONFLICT (key) DO NOTHING`, service.SettingKeyCodexTurnStateModelsRevision); err != nil {
		return false, err
	}
	var lockedRevision string
	if err := tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=$1 FOR SHARE`,
		service.SettingKeyCodexTurnStateModelsRevision).Scan(&lockedRevision); err != nil {
		return false, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT key, value FROM settings WHERE key IN ($1,$2)`,
		service.SettingKeyCodexTurnStateModels, service.SettingKeyCodexTurnStateModelsRevision)
	if err != nil {
		return false, err
	}
	defer func() { _ = rows.Close() }()
	values := make(map[string]string, 2)
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return false, err
		}
		values[key] = value
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	models, revision, err := service.ParseCodexTurnStateModelPolicyValues(values)
	return err == nil && revision == record.ModelPolicyRevision && slices.Contains(models, record.Model), err
}

func (r *openAICodexStateRepository) BeginBusiness(ctx context.Context, key service.CodexTurnStateKey, attemptID string, now, leaseUntil time.Time) (*service.CodexTurnStateRecord, error) {
	if err := r.databaseAvailable(); err != nil {
		return nil, err
	}
	if err := validateCodexStateKey(key); err != nil {
		return nil, err
	}
	if strings.TrimSpace(attemptID) == "" || now.IsZero() || !leaseUntil.After(now) {
		return nil, errors.New("invalid Codex turn-state business lease")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	live, err := lockCodexStateGeneration(ctx, tx, key)
	if err != nil || !live {
		return nil, err
	}
	// Touches within one generation do not increment the token's CAS version.
	// A new generation atomically erases every old token and collector restriction.
	_, err = tx.ExecContext(ctx, `INSERT INTO openai_codex_state
		(owner_account_id, model, generation, last_business_at)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (owner_account_id, model) DO UPDATE SET
		generation = EXCLUDED.generation,
		version = CASE WHEN openai_codex_state.generation = EXCLUDED.generation THEN openai_codex_state.version ELSE openai_codex_state.version + 1 END,
		encrypted_token = CASE WHEN openai_codex_state.generation = EXCLUDED.generation THEN openai_codex_state.encrypted_token ELSE '' END,
		issued_at = CASE WHEN openai_codex_state.generation = EXCLUDED.generation THEN openai_codex_state.issued_at ELSE NULL END,
		expires_at = CASE WHEN openai_codex_state.generation = EXCLUDED.generation THEN openai_codex_state.expires_at ELSE NULL END,
		token_length = CASE WHEN openai_codex_state.generation = EXCLUDED.generation THEN openai_codex_state.token_length ELSE 0 END,
		cipher_blocks = CASE WHEN openai_codex_state.generation = EXCLUDED.generation THEN openai_codex_state.cipher_blocks ELSE 0 END,
		source = CASE WHEN openai_codex_state.generation = EXCLUDED.generation THEN openai_codex_state.source ELSE '' END,
		shape = CASE WHEN openai_codex_state.generation = EXCLUDED.generation THEN openai_codex_state.shape ELSE '' END,
		refresh_reason = CASE WHEN openai_codex_state.generation = EXCLUDED.generation THEN openai_codex_state.refresh_reason ELSE '' END,
		last_collected_at = CASE WHEN openai_codex_state.generation = EXCLUDED.generation THEN openai_codex_state.last_collected_at ELSE NULL END,
		next_collect_at = CASE WHEN openai_codex_state.generation = EXCLUDED.generation THEN openai_codex_state.next_collect_at ELSE NULL END,
		collector_paused = CASE WHEN openai_codex_state.generation = EXCLUDED.generation THEN openai_codex_state.collector_paused ELSE FALSE END,
		last_error = CASE WHEN openai_codex_state.generation = EXCLUDED.generation THEN openai_codex_state.last_error ELSE '' END,
		last_business_at = GREATEST(openai_codex_state.last_business_at, EXCLUDED.last_business_at), updated_at = NOW()`,
		key.OwnerAccountID, key.Model, key.Generation, now.UTC())
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM openai_codex_state_business_leases
		WHERE owner_account_id = $1 AND model = $2 AND (lease_until <= $3 OR generation <> $4)`,
		key.OwnerAccountID, key.Model, now.UTC(), key.Generation)
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO openai_codex_state_business_leases
		(owner_account_id, model, generation, attempt_id, lease_until) VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (owner_account_id, model, generation, attempt_id)
		DO UPDATE SET lease_until = GREATEST(openai_codex_state_business_leases.lease_until, EXCLUDED.lease_until)`,
		key.OwnerAccountID, key.Model, key.Generation, attemptID, leaseUntil.UTC())
	if err != nil {
		return nil, err
	}
	record, err := scanCodexState(tx.QueryRowContext(ctx, `SELECT `+codexStateColumns+` FROM openai_codex_state s
		WHERE s.owner_account_id=$1 AND s.model=$2 AND s.generation=$3`, key.OwnerAccountID, key.Model, key.Generation))
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return record, nil
}

func (r *openAICodexStateRepository) EndBusiness(ctx context.Context, key service.CodexTurnStateKey, attemptID string) error {
	if err := r.databaseAvailable(); err != nil {
		return err
	}
	if err := validateCodexStateKey(key); err != nil {
		return err
	}
	_, err := r.db.ExecContext(ctx, `DELETE FROM openai_codex_state_business_leases
		WHERE owner_account_id=$1 AND model=$2 AND generation=$3 AND attempt_id=$4`,
		key.OwnerAccountID, key.Model, key.Generation, attemptID)
	return err
}

func (r *openAICodexStateRepository) Get(ctx context.Context, key service.CodexTurnStateKey) (*service.CodexTurnStateRecord, error) {
	if err := r.databaseAvailable(); err != nil {
		return nil, err
	}
	if err := validateCodexStateKey(key); err != nil {
		return nil, err
	}
	record, err := scanCodexState(r.db.QueryRowContext(ctx, `SELECT `+codexStateColumns+`
		FROM openai_codex_state s JOIN accounts a ON a.id=s.owner_account_id
		WHERE s.owner_account_id=$1 AND s.model=$2 AND s.generation=$3 AND `+codexStateLiveAccount,
		key.OwnerAccountID, key.Model, key.Generation))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return record, err
}

func (r *openAICodexStateRepository) SaveCAS(ctx context.Context, record service.CodexTurnStateRecord, expectedVersion int64) (bool, error) {
	if err := r.databaseAvailable(); err != nil {
		return false, err
	}
	if err := validateCodexStateKey(record.Key()); err != nil {
		return false, err
	}
	if expectedVersion < 1 {
		return false, errors.New("invalid Codex turn-state version")
	}
	// A snapshot taken after waiting for the policy lock must see the previous
	// writer's committed pair, regardless of the database's default isolation.
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	policyLive, err := lockCodexStateModelPolicy(ctx, tx, record)
	if err != nil || !policyLive {
		return false, err
	}
	live, err := lockCodexStateGeneration(ctx, tx, record.Key())
	if err != nil || !live {
		return false, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE openai_codex_state SET
		version=version+1, encrypted_token=$5, issued_at=$6, expires_at=$7,
		token_length=$8, cipher_blocks=$9, source=$10, shape=$11, refresh_reason=$12,
		last_business_at=GREATEST(last_business_at,$13), last_collected_at=$14,
		next_collect_at=$15, collector_paused=$16, last_error=$17, updated_at=NOW()
		WHERE owner_account_id=$1 AND model=$2 AND generation=$3 AND version=$4`,
		record.OwnerAccountID, record.Model, record.Generation, expectedVersion,
		record.EncryptedToken, codexStateNullableTime(record.IssuedAt), codexStateNullableTime(record.ExpiresAt),
		record.TokenLength, record.CipherBlocks, record.Source, record.Shape, record.RefreshReason,
		codexStateNullableTime(record.LastBusinessAt), codexStateNullableTime(record.LastCollectedAt),
		codexStateNullableTime(record.NextCollectAt), record.CollectorPaused, record.LastError)
	if err != nil {
		return false, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return changed == 1, nil
}

func (r *openAICodexStateRepository) ListActive(ctx context.Context, since time.Time, limit int) ([]service.CodexTurnStateRecord, error) {
	if err := r.databaseAvailable(); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 10000 {
		limit = 1000
	}
	// Filter before LIMIT so fresh tokens, cooldowns, and learning-only accounts
	// cannot starve due work. Refresh lead tracks the service policy constant.
	return r.list(ctx, `SELECT `+codexStateColumns+` FROM openai_codex_state s
		JOIN accounts a ON a.id=s.owner_account_id WHERE s.last_business_at >= $1 AND `+codexStateLiveAccount+`
		AND NOT s.collector_paused AND (s.next_collect_at IS NULL OR s.next_collect_at <= NOW())
		AND (s.encrypted_token = '' OR s.expires_at <= NOW() + ($3 * INTERVAL '1 second'))
		AND a.extra->'codex_turn_state'->>'collector_proxy_id' ~ '^[1-9][0-9]*$'
		AND NOT EXISTS (SELECT 1 FROM openai_codex_state_business_leases l
		 WHERE l.owner_account_id=s.owner_account_id AND l.model=s.model
		 AND l.generation=s.generation AND l.lease_until>NOW())
		ORDER BY s.next_collect_at ASC NULLS FIRST, s.expires_at ASC NULLS FIRST,
		s.last_business_at DESC, s.owner_account_id, s.model LIMIT $2`, since.UTC(), limit, int64(service.CodexTurnStateRefreshAhead/time.Second))
}

func (r *openAICodexStateRepository) ListByAccount(ctx context.Context, ownerID int64) ([]service.CodexTurnStateRecord, error) {
	if err := r.databaseAvailable(); err != nil {
		return nil, err
	}
	return r.list(ctx, `SELECT `+codexStateColumns+` FROM openai_codex_state s
		JOIN accounts a ON a.id=s.owner_account_id WHERE s.owner_account_id=$1 AND `+codexStateLiveAccount+`
		ORDER BY s.model`, ownerID)
}

func (r *openAICodexStateRepository) ListByAccounts(ctx context.Context, ownerIDs []int64) ([]service.CodexTurnStateRecord, error) {
	if len(ownerIDs) == 0 {
		return []service.CodexTurnStateRecord{}, nil
	}
	if err := r.databaseAvailable(); err != nil {
		return nil, err
	}
	return r.list(ctx, `SELECT `+codexStateColumns+` FROM openai_codex_state s
		JOIN accounts a ON a.id=s.owner_account_id WHERE s.owner_account_id = ANY($1) AND `+codexStateLiveAccount+`
		ORDER BY s.owner_account_id, s.model`, pq.Array(ownerIDs))
}

func (r *openAICodexStateRepository) list(ctx context.Context, query string, args ...any) ([]service.CodexTurnStateRecord, error) {
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	records := make([]service.CodexTurnStateRecord, 0)
	for rows.Next() {
		record, err := scanCodexState(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, *record)
	}
	return records, rows.Err()
}

func (r *openAICodexStateRepository) HasBusiness(ctx context.Context, key service.CodexTurnStateKey, now time.Time) (bool, error) {
	if err := r.databaseAvailable(); err != nil {
		return false, err
	}
	if err := validateCodexStateKey(key); err != nil {
		return false, err
	}
	var found bool
	err := r.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM openai_codex_state_business_leases l
		JOIN openai_codex_state s ON s.owner_account_id=l.owner_account_id AND s.model=l.model AND s.generation=l.generation
		JOIN accounts a ON a.id=s.owner_account_id
		WHERE l.owner_account_id=$1 AND l.model=$2 AND l.generation=$3 AND l.lease_until>$4 AND `+codexStateLiveAccount+`)`,
		key.OwnerAccountID, key.Model, key.Generation, now.UTC()).Scan(&found)
	return found, err
}

type codexStateScanner interface{ Scan(...any) error }

func scanCodexState(scanner codexStateScanner) (*service.CodexTurnStateRecord, error) {
	var record service.CodexTurnStateRecord
	var issued, expires, collected, next sql.NullTime
	err := scanner.Scan(&record.OwnerAccountID, &record.Model, &record.Generation, &record.Version,
		&record.EncryptedToken, &issued, &expires, &record.TokenLength, &record.CipherBlocks,
		&record.Source, &record.Shape, &record.RefreshReason, &record.LastBusinessAt,
		&collected, &next, &record.CollectorPaused, &record.LastError)
	if err != nil {
		return nil, err
	}
	record.IssuedAt, record.ExpiresAt = issued.Time, expires.Time
	record.LastCollectedAt, record.NextCollectAt = collected.Time, next.Time
	return &record, nil
}

func codexStateNullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value.UTC()
}

func codexStateCollectorKey(ownerID int64) (string, error) {
	if ownerID <= 0 {
		return "", errors.New("invalid Codex turn-state collector account")
	}
	return fmt.Sprintf("openai:codex:state:collector:v1:%d", ownerID), nil
}

var _ service.CodexTurnStateRepository = (*openAICodexStateRepository)(nil)
