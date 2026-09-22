package repository

import (
	"context"
	"database/sql"
	"encoding/json"
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

const codexStateOAuthOwner = `a.deleted_at IS NULL AND a.platform = 'openai' AND a.type = 'oauth'
 AND a.parent_account_id IS NULL
 AND (BTRIM(COALESCE(a.credentials->>'access_token','')) <> '' OR BTRIM(COALESCE(a.credentials->>'refresh_token','')) <> '')
 AND LOWER(BTRIM(COALESCE(a.credentials->>'auth_mode',''))) NOT IN ('personalaccesstoken','personal_access_token','agentidentity')
 AND LOWER(BTRIM(COALESCE(a.credentials->>'openai_auth_mode',''))) NOT IN ('personalaccesstoken','personal_access_token','agentidentity')`

const codexStateLiveAccount = codexStateOAuthOwner + `
 AND a.extra->'codex_turn_state'->>'enabled' = 'true'
 AND EXISTS (SELECT 1 FROM account_openai_oauth_credentials shared_grant
	WHERE shared_grant.account_id = a.id AND shared_grant.status = 'authorized'
	AND shared_grant.authorization_generation = s.authorization_generation
	AND shared_grant.state_generation::text = s.generation)`

const codexStateColumns = `s.owner_account_id, s.source_os, s.model, s.generation, s.version,
 s.encrypted_token, s.issued_at, s.expires_at, s.token_length, s.cipher_blocks,
	 s.source, s.shape, s.refresh_reason, s.last_business_at, s.last_collected_at,
	 s.next_collect_at, s.collector_paused, s.last_error,
	 s.demand_reason, s.demand_at, s.history_proof_observed_at,
		 s.collection_status, s.collection_reason,
		 s.collector_proxy_id, s.collector_extended_count, s.last_collector_proxy_id, s.collector_attempt_id,
	 EXISTS (SELECT 1 FROM openai_codex_state_business_leases state_lease
	 WHERE state_lease.owner_account_id=s.owner_account_id AND state_lease.model=s.model
	 AND state_lease.generation=s.generation AND state_lease.lease_until>NOW()),
	 s.authorization_generation::text, s.encrypted_cookie_bundle, s.cookie_bundle_expires_at`

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
func lockCodexStateGeneration(ctx context.Context, tx *sql.Tx, key service.CodexTurnStateKey, writeAccount bool) (bool, error) {
	accountLock := " FOR SHARE"
	if writeAccount {
		// Cooldown publication also updates this row. Take the write lock first
		// so simultaneous model outcomes cannot deadlock upgrading shared locks.
		accountLock = " FOR NO KEY UPDATE"
	}
	var found int64
	err := tx.QueryRowContext(ctx, `SELECT a.id FROM accounts a
		WHERE a.id = $1 AND `+codexStateOAuthOwner+`
		AND a.extra->'codex_turn_state'->>'enabled' = 'true'`+accountLock, key.OwnerAccountID).Scan(&found)
	if err != nil {
		return false, ignoreCodexStateNoRows(err)
	}
	err = tx.QueryRowContext(ctx, `SELECT account_id FROM account_openai_oauth_credentials
		WHERE account_id=$1 AND status='authorized' AND state_generation::text=$2
		FOR SHARE`, key.OwnerAccountID, key.Generation).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func ignoreCodexStateNoRows(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	return err
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
	live, err := lockCodexStateGeneration(ctx, tx, key, false)
	if err != nil || !live {
		return nil, err
	}
	// A lease is not evidence that a physical business request was sent. Only
	// MarkBusinessSent records activity. A new generation clears old runtime state.
	_, err = tx.ExecContext(ctx, `INSERT INTO openai_codex_state
		(owner_account_id, model, generation, last_business_at, source_os, authorization_generation)
		SELECT $1, $2, $3, $4, $6, authorization_generation FROM account_openai_oauth_credentials WHERE account_id=$1
		ON CONFLICT (owner_account_id, model) DO UPDATE SET
		generation = EXCLUDED.generation,
		authorization_generation = EXCLUDED.authorization_generation,
		source_os = CASE WHEN (openai_codex_state.generation = EXCLUDED.generation AND openai_codex_state.authorization_generation = EXCLUDED.authorization_generation) THEN openai_codex_state.source_os ELSE EXCLUDED.source_os END,
		version = CASE WHEN (openai_codex_state.generation = EXCLUDED.generation AND openai_codex_state.authorization_generation = EXCLUDED.authorization_generation)
			AND NOT (openai_codex_state.last_business_at > $4 AND openai_codex_state.last_business_at < $5 AND openai_codex_state.demand_reason <> '')
			THEN openai_codex_state.version ELSE openai_codex_state.version + 1 END,
		encrypted_token = CASE WHEN (openai_codex_state.generation = EXCLUDED.generation AND openai_codex_state.authorization_generation = EXCLUDED.authorization_generation) THEN openai_codex_state.encrypted_token ELSE '' END,
		encrypted_cookie_bundle = CASE WHEN (openai_codex_state.generation = EXCLUDED.generation AND openai_codex_state.authorization_generation = EXCLUDED.authorization_generation) THEN openai_codex_state.encrypted_cookie_bundle ELSE '' END,
		cookie_bundle_expires_at = CASE WHEN (openai_codex_state.generation = EXCLUDED.generation AND openai_codex_state.authorization_generation = EXCLUDED.authorization_generation) THEN openai_codex_state.cookie_bundle_expires_at ELSE NULL END,
		issued_at = CASE WHEN (openai_codex_state.generation = EXCLUDED.generation AND openai_codex_state.authorization_generation = EXCLUDED.authorization_generation) THEN openai_codex_state.issued_at ELSE NULL END,
		expires_at = CASE WHEN (openai_codex_state.generation = EXCLUDED.generation AND openai_codex_state.authorization_generation = EXCLUDED.authorization_generation) THEN openai_codex_state.expires_at ELSE NULL END,
		token_length = CASE WHEN (openai_codex_state.generation = EXCLUDED.generation AND openai_codex_state.authorization_generation = EXCLUDED.authorization_generation) THEN openai_codex_state.token_length ELSE 0 END,
		cipher_blocks = CASE WHEN (openai_codex_state.generation = EXCLUDED.generation AND openai_codex_state.authorization_generation = EXCLUDED.authorization_generation) THEN openai_codex_state.cipher_blocks ELSE 0 END,
		source = CASE WHEN (openai_codex_state.generation = EXCLUDED.generation AND openai_codex_state.authorization_generation = EXCLUDED.authorization_generation) THEN openai_codex_state.source ELSE '' END,
		shape = CASE WHEN (openai_codex_state.generation = EXCLUDED.generation AND openai_codex_state.authorization_generation = EXCLUDED.authorization_generation) THEN openai_codex_state.shape ELSE '' END,
		refresh_reason = CASE WHEN (openai_codex_state.generation = EXCLUDED.generation AND openai_codex_state.authorization_generation = EXCLUDED.authorization_generation) THEN openai_codex_state.refresh_reason ELSE '' END,
		last_collected_at = CASE WHEN (openai_codex_state.generation = EXCLUDED.generation AND openai_codex_state.authorization_generation = EXCLUDED.authorization_generation) THEN openai_codex_state.last_collected_at ELSE NULL END,
		next_collect_at = CASE WHEN (openai_codex_state.generation = EXCLUDED.generation AND openai_codex_state.authorization_generation = EXCLUDED.authorization_generation)
			AND (openai_codex_state.last_business_at <= $4 OR openai_codex_state.last_business_at >= $5 OR openai_codex_state.collector_paused
			OR openai_codex_state.last_error IN ('account_cooldown','collector_rate_limited'))
			THEN openai_codex_state.next_collect_at ELSE NULL END,
		collector_paused = CASE WHEN (openai_codex_state.generation = EXCLUDED.generation AND openai_codex_state.authorization_generation = EXCLUDED.authorization_generation) THEN openai_codex_state.collector_paused ELSE FALSE END,
		collector_proxy_id = CASE WHEN (openai_codex_state.generation = EXCLUDED.generation AND openai_codex_state.authorization_generation = EXCLUDED.authorization_generation) THEN openai_codex_state.collector_proxy_id ELSE NULL END,
		collector_extended_count = CASE WHEN (openai_codex_state.generation = EXCLUDED.generation AND openai_codex_state.authorization_generation = EXCLUDED.authorization_generation) THEN openai_codex_state.collector_extended_count ELSE 0 END,
		last_collector_proxy_id = CASE WHEN (openai_codex_state.generation = EXCLUDED.generation AND openai_codex_state.authorization_generation = EXCLUDED.authorization_generation) THEN openai_codex_state.last_collector_proxy_id ELSE NULL END,
		collector_attempt_id = CASE WHEN (openai_codex_state.generation = EXCLUDED.generation AND openai_codex_state.authorization_generation = EXCLUDED.authorization_generation)
			AND (openai_codex_state.last_business_at <= $4 OR openai_codex_state.last_business_at >= $5)
			THEN openai_codex_state.collector_attempt_id ELSE NULL END,
		last_error = CASE WHEN (openai_codex_state.generation = EXCLUDED.generation AND openai_codex_state.authorization_generation = EXCLUDED.authorization_generation) THEN openai_codex_state.last_error ELSE '' END,
		demand_reason = CASE WHEN (openai_codex_state.generation = EXCLUDED.generation AND openai_codex_state.authorization_generation = EXCLUDED.authorization_generation) AND (openai_codex_state.last_business_at <= $4 OR openai_codex_state.last_business_at >= $5) THEN openai_codex_state.demand_reason ELSE '' END,
		demand_at = CASE WHEN (openai_codex_state.generation = EXCLUDED.generation AND openai_codex_state.authorization_generation = EXCLUDED.authorization_generation) AND (openai_codex_state.last_business_at <= $4 OR openai_codex_state.last_business_at >= $5) THEN openai_codex_state.demand_at ELSE NULL END,
		history_proof_observed_at = CASE WHEN (openai_codex_state.generation = EXCLUDED.generation AND openai_codex_state.authorization_generation = EXCLUDED.authorization_generation) THEN openai_codex_state.history_proof_observed_at ELSE NULL END,
		collection_status = CASE WHEN (openai_codex_state.generation <> EXCLUDED.generation OR openai_codex_state.authorization_generation <> EXCLUDED.authorization_generation) THEN ''
			WHEN openai_codex_state.last_business_at <= $4 OR openai_codex_state.last_business_at >= $5 OR openai_codex_state.collector_paused
			OR openai_codex_state.last_error IN ('account_cooldown','collector_rate_limited')
			THEN openai_codex_state.collection_status ELSE 'idle' END,
		collection_reason = CASE WHEN (openai_codex_state.generation <> EXCLUDED.generation OR openai_codex_state.authorization_generation <> EXCLUDED.authorization_generation) THEN ''
			WHEN openai_codex_state.collection_reason = 'collector_proxy_changed' THEN openai_codex_state.collection_reason
			WHEN openai_codex_state.last_business_at <= $4 OR openai_codex_state.last_business_at >= $5 OR openai_codex_state.collector_paused
			OR openai_codex_state.last_error IN ('account_cooldown','collector_rate_limited')
			THEN openai_codex_state.collection_reason ELSE 'waiting_business_response' END,
		last_business_at = CASE WHEN (openai_codex_state.generation = EXCLUDED.generation AND openai_codex_state.authorization_generation = EXCLUDED.authorization_generation) THEN openai_codex_state.last_business_at ELSE EXCLUDED.last_business_at END,
		updated_at = NOW()`, key.OwnerAccountID, key.Model, key.Generation, time.Unix(0, 0).UTC(), now.Add(-service.CodexTurnStateActiveWindow).UTC(), service.NormalizeOpenAIOSFamily(key.OSFamily))
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
		(owner_account_id, model, generation, attempt_id, lease_until, source_os) VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (owner_account_id, model, generation, attempt_id)
		DO UPDATE SET lease_until = GREATEST(openai_codex_state_business_leases.lease_until, EXCLUDED.lease_until)`,
		key.OwnerAccountID, key.Model, key.Generation, attemptID, leaseUntil.UTC(), service.NormalizeOpenAIOSFamily(key.OSFamily))
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

func (r *openAICodexStateRepository) MarkBusinessSent(ctx context.Context, key service.CodexTurnStateKey, sentAt time.Time) error {
	if err := r.databaseAvailable(); err != nil {
		return err
	}
	if err := validateCodexStateKey(key); err != nil {
		return err
	}
	if sentAt.IsZero() {
		return errors.New("invalid Codex turn-state business timestamp")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	live, err := lockCodexStateGeneration(ctx, tx, key, false)
	if err != nil || !live {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE openai_codex_state
		SET last_business_at=GREATEST(last_business_at,$4), updated_at=NOW()
		WHERE owner_account_id=$1 AND model=$2 AND generation=$3`, key.OwnerAccountID, key.Model, key.Generation, sentAt.UTC())
	if err != nil {
		return err
	}
	return tx.Commit()
}

// CreateHistoryDemand consumes safe metadata, never a token. Policy and account
// locks fence configuration/credential changes; the state row lock serializes
// proof consumption with natural publication and collectors across instances.
func (r *openAICodexStateRepository) CreateHistoryDemand(ctx context.Context, proof service.CodexTurnStateHistoryProof, now time.Time) (bool, error) {
	if err := r.databaseAvailable(); err != nil {
		return false, err
	}
	key := service.CodexTurnStateKey{OwnerAccountID: proof.OwnerAccountID, OSFamily: proof.OSFamily, Model: proof.Model, Generation: proof.Generation}
	if err := validateCodexStateKey(key); err != nil {
		return false, err
	}
	// PostgreSQL timestamps have microsecond precision. Normalize before comparing
	// the persisted watermark so nanoseconds cannot make one proof appear new.
	proof.ObservedAt = proof.ObservedAt.UTC().Truncate(time.Microsecond)
	proof.BusinessAt = proof.BusinessAt.UTC().Truncate(time.Microsecond)
	if !validCodexStateHistoryProof(proof, now) {
		return false, nil
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	policyLive, err := lockCodexStateModelPolicy(ctx, tx, service.CodexTurnStateRecord{Model: proof.Model, ModelPolicyRevision: proof.ModelPolicyRevision})
	if err != nil || !policyLive {
		return false, err
	}
	live, err := lockCodexStateGeneration(ctx, tx, key, false)
	if err != nil || !live {
		return false, err
	}
	var extraJSON, credentialsJSON []byte
	var parentID sql.NullInt64
	var authorizationGeneration string
	err = tx.QueryRowContext(ctx, `SELECT a.extra,
		jsonb_build_object('plan_type', a.credentials->'plan_type', 'auth_mode', a.credentials->'auth_mode', 'openai_auth_mode', a.credentials->'openai_auth_mode'),
		a.parent_account_id, shared_grant.authorization_generation::text FROM accounts a JOIN account_openai_oauth_credentials shared_grant ON shared_grant.account_id=a.id
		WHERE a.id=$1 AND a.deleted_at IS NULL AND a.platform='openai' AND a.type='oauth'
		AND a.extra->'codex_turn_state'->>'enabled'='true'
		AND shared_grant.state_generation::text=$2 AND shared_grant.status='authorized'
		AND shared_grant.credential_epoch::text=$3`, key.OwnerAccountID, key.Generation, proof.CredentialEpoch).
		Scan(&extraJSON, &credentialsJSON, &parentID, &authorizationGeneration)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	account := service.Account{ID: key.OwnerAccountID, Platform: "openai", Type: "oauth"}
	if json.Unmarshal(extraJSON, &account.Extra) != nil || json.Unmarshal(credentialsJSON, &account.Credentials) != nil {
		return false, errors.New("invalid Codex turn-state account metadata")
	}
	if parentID.Valid {
		account.ParentAccountID = &parentID.Int64
	}
	if !service.IsCodexTurnStateAccount(&account) || service.CodexTurnStateAccountTypeForAccount(&account) != proof.AccountType {
		return false, nil
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO openai_codex_state
		(owner_account_id, model, generation, last_business_at, source_os, authorization_generation)
		SELECT $1,$2,$3,$4,$5,authorization_generation FROM account_openai_oauth_credentials WHERE account_id=$1
		ON CONFLICT (owner_account_id,model) DO NOTHING`, key.OwnerAccountID, key.Model, key.Generation, proof.BusinessAt.UTC(), service.NormalizeOpenAIOSFamily(key.OSFamily))
	if err != nil {
		return false, err
	}
	record, err := scanCodexState(tx.QueryRowContext(ctx, `SELECT `+codexStateColumns+` FROM openai_codex_state s
		WHERE s.owner_account_id=$1 AND s.model=$2 FOR UPDATE`, key.OwnerAccountID, key.Model))
	if err != nil {
		return false, err
	}
	if record.Generation == key.Generation && record.AuthorizationGeneration == authorizationGeneration && !proof.ObservedAt.After(record.HistoryProofObservedAt) {
		return false, nil
	}
	if record.Generation != key.Generation || record.AuthorizationGeneration != authorizationGeneration {
		*record = service.CodexTurnStateRecord{OwnerAccountID: key.OwnerAccountID, OSFamily: key.OSFamily, Model: key.Model, Generation: key.Generation, AuthorizationGeneration: authorizationGeneration, Version: record.Version}
	}
	// A historical diagnostic cannot establish the CAS version that produced it.
	// Consume it without clearing or superseding a currently valid target token.
	validTarget := record.EncryptedToken != "" && record.ExpiresAt.After(now) &&
		((proof.AccountType == "personal" && record.TokenLength == 292 && record.CipherBlocks == 10) ||
			(proof.AccountType == "team_business" && record.TokenLength == 332 && record.CipherBlocks == 12))
	createDemand := !validTarget
	if createDemand {
		record.DemandReason, record.DemandAt = "extended_shape", proof.ObservedAt
		if !record.CollectorPaused {
			record.CollectionStatus, record.CollectionReason = "pending", "extended_shape"
		}
	}
	if proof.BusinessAt.After(record.LastBusinessAt) {
		record.LastBusinessAt = proof.BusinessAt
	}
	// Increment Version even for consumption without demand. A slow collector or
	// natural request cannot overwrite newly consumed metadata with an older copy.
	_, err = tx.ExecContext(ctx, `UPDATE openai_codex_state SET generation=$3, version=version+1,
		encrypted_token=$4, issued_at=$5, expires_at=$6, token_length=$7, cipher_blocks=$8,
		source=$9, shape=$10, refresh_reason=$11, last_business_at=$12,
		last_collected_at=$13, next_collect_at=$14, collector_paused=$15, last_error=$16,
		demand_reason=$17, demand_at=$18, history_proof_observed_at=$19,
		collection_status=$20, collection_reason=$21,
		collector_proxy_id=$22, collector_extended_count=$23, last_collector_proxy_id=$24,
		collector_attempt_id=$25, source_os=$26, encrypted_cookie_bundle=$27, cookie_bundle_expires_at=$28,
		authorization_generation=(SELECT authorization_generation FROM account_openai_oauth_credentials WHERE account_id=$1), updated_at=NOW()
		WHERE owner_account_id=$1 AND model=$2`, key.OwnerAccountID, key.Model, key.Generation,
		record.EncryptedToken, codexStateNullableTime(record.IssuedAt), codexStateNullableTime(record.ExpiresAt),
		record.TokenLength, record.CipherBlocks, record.Source, record.Shape, record.RefreshReason,
		record.LastBusinessAt.UTC(), codexStateNullableTime(record.LastCollectedAt), codexStateNullableTime(record.NextCollectAt),
		record.CollectorPaused, record.LastError, record.DemandReason, codexStateNullableTime(record.DemandAt), proof.ObservedAt,
		record.CollectionStatus, record.CollectionReason, codexStateNullableID(record.CollectorProxyID),
		record.CollectorExtendedCount, codexStateNullableID(record.LastCollectorProxyID), codexStateNullableString(record.CollectorAttemptID), service.NormalizeOpenAIOSFamily(record.OSFamily),
		record.EncryptedCookieBundle, record.CookieBundleExpiresAt)
	if err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return createDemand, nil
}

func validCodexStateHistoryProof(proof service.CodexTurnStateHistoryProof, now time.Time) bool {
	if now.IsZero() || !proof.EnvelopeValid || !proof.Delivered || proof.CredentialEpoch == "" || proof.ModelPolicyRevision == "" ||
		proof.BusinessAt.IsZero() || proof.ObservedAt.IsZero() || proof.IssuedAt.IsZero() ||
		proof.BusinessAt.Before(now.Add(-service.CodexTurnStateActiveWindow)) || proof.BusinessAt.After(proof.ObservedAt) ||
		proof.ObservedAt.After(now) || proof.IssuedAt.After(proof.ObservedAt.Add(30*time.Second)) ||
		!proof.ExpiresAt.Equal(proof.IssuedAt.Add(service.CodexTurnStateLifetime)) || !proof.ExpiresAt.After(now) {
		return false
	}
	return (proof.AccountType == "personal" && proof.TokenLength == 312 && proof.CipherBlocks == 11) ||
		(proof.AccountType == "team_business" && proof.TokenLength == 356 && proof.CipherBlocks == 13)
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
	// A new attempt may still carry an old rate-limit diagnostic. Its crash
	// reservation is not a real account cooldown and must remain model-local.
	extendCooldown := (record.LastError == "collector_rate_limited" || record.LastError == "account_cooldown") &&
		!record.NextCollectAt.IsZero() && record.CollectionStatus != "collecting" && record.CollectorAttemptID == ""
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
	live, err := lockCodexStateGeneration(ctx, tx, record.Key(), extendCooldown)
	if err != nil || !live {
		return false, err
	}
	// Business activity may overlap collection. The UPDATE locks the state row
	// and rechecks its version after any concurrent writer commits, preventing
	// a late result from replacing a newer publication without waiting for leases.
	var changed bool
	err = tx.QueryRowContext(ctx, `WITH updated_state AS (UPDATE openai_codex_state SET
		version=version+1, encrypted_token=$5, issued_at=$6, expires_at=$7,
		token_length=$8, cipher_blocks=$9, source=$10, shape=$11, refresh_reason=$12,
		last_business_at=GREATEST(last_business_at,$13), last_collected_at=$14,
		next_collect_at=$15, collector_paused=$16, last_error=$17,
		demand_reason=$18, demand_at=$19,
		history_proof_observed_at=GREATEST(history_proof_observed_at,$20),
		collection_status=$21, collection_reason=$22,
		collector_proxy_id=$23, collector_extended_count=$24, last_collector_proxy_id=$25,
		collector_attempt_id=$26, source_os=$27, encrypted_cookie_bundle=$29, cookie_bundle_expires_at=$30, updated_at=NOW()
		WHERE owner_account_id=$1 AND model=$2 AND generation=$3 AND version=$4
		AND authorization_generation::text=$31
		AND authorization_generation=(SELECT authorization_generation FROM account_openai_oauth_credentials WHERE account_id=$1)
		RETURNING owner_account_id,next_collect_at), updated_cooldown AS (
		UPDATE accounts a SET codex_turn_state_retry_after=GREATEST(a.codex_turn_state_retry_after,s.next_collect_at)
		FROM updated_state s WHERE a.id=s.owner_account_id AND a.deleted_at IS NULL AND $28
		RETURNING a.id)
		SELECT EXISTS (SELECT 1 FROM updated_state)`,
		record.OwnerAccountID, record.Model, record.Generation, expectedVersion,
		record.EncryptedToken, codexStateNullableTime(record.IssuedAt), codexStateNullableTime(record.ExpiresAt),
		record.TokenLength, record.CipherBlocks, record.Source, record.Shape, record.RefreshReason,
		codexStateNullableTime(record.LastBusinessAt), codexStateNullableTime(record.LastCollectedAt),
		codexStateNullableTime(record.NextCollectAt), record.CollectorPaused, record.LastError,
		record.DemandReason, codexStateNullableTime(record.DemandAt), codexStateNullableTime(record.HistoryProofObservedAt),
		record.CollectionStatus, record.CollectionReason, codexStateNullableID(record.CollectorProxyID),
		record.CollectorExtendedCount, codexStateNullableID(record.LastCollectorProxyID), codexStateNullableString(record.CollectorAttemptID), service.NormalizeOpenAIOSFamily(record.OSFamily), extendCooldown,
		record.EncryptedCookieBundle, record.CookieBundleExpiresAt, record.AuthorizationGeneration).Scan(&changed)
	if err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return changed, nil
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
		AND (a.codex_turn_state_retry_after IS NULL OR a.codex_turn_state_retry_after <= NOW())
		AND NOT s.collector_paused AND (s.next_collect_at IS NULL OR s.next_collect_at <= NOW())
		AND (s.demand_reason <> '' OR s.encrypted_token = '' OR s.encrypted_cookie_bundle = ''
		 OR s.expires_at <= NOW() + ($3 * INTERVAL '1 second') OR s.cookie_bundle_expires_at <= NOW() + ($3 * INTERVAL '1 second'))
		AND CASE WHEN a.extra->'codex_turn_state' ? 'collector_proxy_ids' THEN
		 CASE WHEN jsonb_typeof(a.extra->'codex_turn_state'->'collector_proxy_ids') = 'array' THEN
		  EXISTS (SELECT 1 FROM jsonb_array_elements(a.extra->'codex_turn_state'->'collector_proxy_ids') AS collector_proxy(value)
		   WHERE jsonb_typeof(collector_proxy.value) = 'number' AND collector_proxy.value #>> '{}' ~ '^[1-9][0-9]*$')
		 ELSE FALSE END
		 ELSE a.extra->'codex_turn_state'->>'collector_proxy_id' ~ '^[1-9][0-9]*$' END
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
	var issued, expires, collected, next, demand, historyProof, cookieExpires sql.NullTime
	var collectorProxyID, lastCollectorProxyID sql.NullInt64
	var collectorAttemptID sql.NullString
	err := scanner.Scan(&record.OwnerAccountID, &record.OSFamily, &record.Model, &record.Generation, &record.Version,
		&record.EncryptedToken, &issued, &expires, &record.TokenLength, &record.CipherBlocks,
		&record.Source, &record.Shape, &record.RefreshReason, &record.LastBusinessAt,
		&collected, &next, &record.CollectorPaused, &record.LastError,
		&record.DemandReason, &demand, &historyProof, &record.CollectionStatus, &record.CollectionReason,
		&collectorProxyID, &record.CollectorExtendedCount, &lastCollectorProxyID, &collectorAttemptID, &record.BusinessInFlight,
		&record.AuthorizationGeneration, &record.EncryptedCookieBundle, &cookieExpires)
	if err != nil {
		return nil, err
	}
	record.IssuedAt, record.ExpiresAt = issued.Time, expires.Time
	record.LastCollectedAt, record.NextCollectAt = collected.Time, next.Time
	record.DemandAt, record.HistoryProofObservedAt = demand.Time, historyProof.Time
	record.CollectorProxyID, record.LastCollectorProxyID = collectorProxyID.Int64, lastCollectorProxyID.Int64
	record.CollectorAttemptID = collectorAttemptID.String
	if cookieExpires.Valid {
		record.CookieBundleExpiresAt = &cookieExpires.Time
	}
	return &record, nil
}

func codexStateNullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value.UTC()
}

func codexStateNullableID(value int64) any {
	if value == 0 {
		return nil
	}
	return value
}

func codexStateNullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func codexStateCollectorKey(ownerID int64) (string, error) {
	if ownerID <= 0 {
		return "", errors.New("invalid Codex turn-state collector account")
	}
	return fmt.Sprintf("openai:codex:state:collector:v1:%d", ownerID), nil
}

var _ service.CodexTurnStateRepository = (*openAICodexStateRepository)(nil)
var _ service.CodexTurnStateHistoryRepository = (*openAICodexStateRepository)(nil)
var _ service.CodexTurnStateOSActivationRepository = (*openAICodexStateRepository)(nil)
