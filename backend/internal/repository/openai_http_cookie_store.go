package repository

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openaicookies"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// openAIHTTPCookieStore reads PostgreSQL on every request. Process-local session
// cookies are deliberately outside this store's persistence boundary.
type openAIHTTPCookieStore struct {
	db        *sql.DB
	encryptor service.SecretEncryptor
}

func NewOpenAIHTTPCookieStore(db *sql.DB, encryptor service.SecretEncryptor) openaicookies.Store {
	return &openAIHTTPCookieStore{db: db, encryptor: encryptor}
}

// The authenticated ciphertext also binds the entry to its authorization scope,
// so copying a ciphertext to another database row cannot change its owner.
type openAIHTTPCookiePayload struct {
	Version int
	Scope   openaicookies.Scope
	Entry   openaicookies.Entry
}

func (s *openAIHTTPCookieStore) begin(ctx context.Context, scope openaicookies.Scope) (*sql.Tx, error) {
	if !scope.Persistent() {
		return nil, openaicookies.ErrInvalidScope
	}
	if s == nil || s.db == nil || s.encryptor == nil {
		return nil, openaicookies.ErrStoreUnavailable
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, openaicookies.ErrStoreUnavailable
	}
	// Configuration changes acquire the account lock before changing OS slots.
	// Match that order and hold both shared locks through the read/write commit.
	var ownerID int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM accounts
		WHERE id=$1 AND deleted_at IS NULL AND `+codexTurnStateOwnerExpression("credentials")+`
		FOR SHARE`, scope.OwnerAccountID).Scan(&ownerID)
	if err == nil {
		err = tx.QueryRowContext(ctx, `SELECT account_id FROM account_openai_oauth_os_credentials
			WHERE account_id=$1 AND os_family=$2 AND authorization_generation::text=$3
			AND status='authorized' FOR SHARE`, scope.OwnerAccountID, scope.OSFamily, scope.AuthorizationGeneration).Scan(&ownerID)
	}
	if err != nil {
		_ = tx.Rollback()
		if errors.Is(err, sql.ErrNoRows) {
			return nil, openaicookies.ErrStaleScope
		}
		return nil, openaicookies.ErrStoreUnavailable
	}
	return tx, nil
}

func pruneOpenAIHTTPCookies(ctx context.Context, tx *sql.Tx, scope openaicookies.Scope) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM openai_http_cookies
		WHERE owner_account_id=$1 AND os_family=$2
		AND authorization_generation::text<>$3`,
		scope.OwnerAccountID, scope.OSFamily, scope.AuthorizationGeneration)
	if err != nil {
		return openaicookies.ErrStoreUnavailable
	}
	return nil
}

func (s *openAIHTTPCookieStore) Load(ctx context.Context, scope openaicookies.Scope) (openaicookies.Snapshot, error) {
	tx, err := s.begin(ctx, scope)
	if err != nil {
		return openaicookies.Snapshot{}, err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UTC()
	if err := pruneOpenAIHTTPCookies(ctx, tx, scope); err != nil {
		return openaicookies.Snapshot{}, err
	}
	// Keep identity revisions even for deletions and expired cookies. Other
	// processes use them to invalidate local session values without persisting any
	// session value. Entries and revisions come from one SQL statement snapshot.
	rows, err := tx.QueryContext(ctx, `SELECT entry_key, encrypted_entry, expires_at, revision
		FROM openai_http_cookies WHERE owner_account_id=$1 AND os_family=$2
		AND authorization_generation::text=$3 ORDER BY entry_key`,
		scope.OwnerAccountID, scope.OSFamily, scope.AuthorizationGeneration)
	if err != nil {
		return openaicookies.Snapshot{}, openaicookies.ErrStoreUnavailable
	}
	defer func() { _ = rows.Close() }()
	snapshot := openaicookies.Snapshot{Entries: make([]openaicookies.Entry, 0), Versions: make(map[string]int64)}
	for rows.Next() {
		var key string
		var ciphertext sql.NullString
		var expiresAt sql.NullTime
		var revision int64
		if err := rows.Scan(&key, &ciphertext, &expiresAt, &revision); err != nil {
			return openaicookies.Snapshot{}, openaicookies.ErrStoreUnavailable
		}
		if !validOpenAIHTTPCookieKey(key) || revision <= 0 || ciphertext.Valid != expiresAt.Valid {
			return openaicookies.Snapshot{}, openaicookies.ErrStoreCorrupt
		}
		snapshot.Versions[key] = revision
		if !ciphertext.Valid || !expiresAt.Time.After(now) {
			continue
		}
		plaintext, err := s.encryptor.Decrypt(ciphertext.String)
		if err != nil {
			return openaicookies.Snapshot{}, openaicookies.ErrStoreCorrupt
		}
		var payload openAIHTTPCookiePayload
		if json.Unmarshal([]byte(plaintext), &payload) != nil || payload.Version != 1 ||
			payload.Scope != scope || payload.Entry.Key != key || !validPersistentOpenAIHTTPCookie(payload.Entry) ||
			!payload.Entry.ExpiresAt.UTC().Truncate(time.Microsecond).Equal(expiresAt.Time) {
			return openaicookies.Snapshot{}, openaicookies.ErrStoreCorrupt
		}
		// Use the original absolute timestamp from the encrypted payload. Database
		// timestamp precision must not turn loading into a fresh Max-Age interval.
		if payload.Entry.ExpiresAt.After(now) {
			snapshot.Entries = append(snapshot.Entries, payload.Entry)
		}
	}
	if rows.Err() != nil || rows.Close() != nil {
		return openaicookies.Snapshot{}, openaicookies.ErrStoreUnavailable
	}
	if tx.Commit() != nil {
		return openaicookies.Snapshot{}, openaicookies.ErrStoreUnavailable
	}
	return snapshot, nil
}

func validPersistentOpenAIHTTPCookie(entry openaicookies.Entry) bool {
	return entry.Valid() && !entry.ExpiresAt.IsZero()
}

func validOpenAIHTTPCookieKey(key string) bool {
	if len(key) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(key)
	return err == nil && hex.EncodeToString(decoded) == key
}

func (s *openAIHTTPCookieStore) Merge(ctx context.Context, scope openaicookies.Scope, mutations []openaicookies.Mutation) (map[string]int64, error) {
	if !scope.Persistent() {
		return nil, openaicookies.ErrInvalidScope
	}
	if s == nil || s.db == nil || s.encryptor == nil {
		return nil, openaicookies.ErrStoreUnavailable
	}
	// Coalesce repeated identities using their last mutation, then lock entries in
	// a stable order to avoid deadlocks between overlapping response cookie sets.
	latest := make(map[string]*openaicookies.Entry, len(mutations))
	for _, mutation := range mutations {
		if !validOpenAIHTTPCookieKey(mutation.Key) || mutation.Entry != nil &&
			(mutation.Entry.Key != mutation.Key || !validPersistentOpenAIHTTPCookie(*mutation.Entry)) {
			return nil, openaicookies.ErrStoreCorrupt
		}
		latest[mutation.Key] = mutation.Entry
	}
	keys := make([]string, 0, len(latest))
	ciphertexts := make(map[string]string, len(latest))
	for key, entry := range latest {
		keys = append(keys, key)
		if entry == nil {
			continue
		}
		plaintext, err := json.Marshal(openAIHTTPCookiePayload{Version: 1, Scope: scope, Entry: *entry})
		if err != nil {
			return nil, openaicookies.ErrStoreCorrupt
		}
		ciphertext, err := s.encryptor.Encrypt(string(plaintext))
		if err != nil || ciphertext == "" {
			return nil, openaicookies.ErrStoreUnavailable
		}
		ciphertexts[key] = ciphertext
	}
	sort.Strings(keys)
	tx, err := s.begin(ctx, scope)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UTC()
	if err := pruneOpenAIHTTPCookies(ctx, tx, scope); err != nil {
		return nil, err
	}
	versions := make(map[string]int64, len(keys))
	for _, key := range keys {
		entry := latest[key]
		var ciphertext, expiresAt any
		if entry != nil && entry.ExpiresAt.After(now) {
			ciphertext, expiresAt = ciphertexts[key], entry.ExpiresAt.UTC().Truncate(time.Microsecond)
		}
		// Generate the conflict-update revision after acquiring the conflicting
		// row lock: the INSERT default may have been evaluated before waiting.
		var revision int64
		err = tx.QueryRowContext(ctx, `INSERT INTO openai_http_cookies
			(owner_account_id, os_family, authorization_generation, entry_key, encrypted_entry, expires_at)
			VALUES ($1,$2,$3::uuid,$4,$5,$6)
			ON CONFLICT (owner_account_id, os_family, authorization_generation, entry_key)
			DO UPDATE SET encrypted_entry=EXCLUDED.encrypted_entry, expires_at=EXCLUDED.expires_at,
				revision=nextval('openai_http_cookie_revision_seq')
			RETURNING revision`, scope.OwnerAccountID, scope.OSFamily, scope.AuthorizationGeneration, key, ciphertext, expiresAt).Scan(&revision)
		if err != nil {
			return nil, openaicookies.ErrStoreUnavailable
		}
		versions[key] = revision
	}
	if tx.Commit() != nil {
		return nil, openaicookies.ErrStoreUnavailable
	}
	return versions, nil
}

var _ openaicookies.Store = (*openAIHTTPCookieStore)(nil)
