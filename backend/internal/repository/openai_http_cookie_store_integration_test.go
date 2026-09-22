//go:build integration

package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openaicookies"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func createOpenAIHTTPCookieFixture(t *testing.T) (openaicookies.Scope, service.SecretEncryptor) {
	t.Helper()
	ctx := context.Background()
	scope := openaicookies.Scope{OSFamily: "windows"}
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO accounts
		(name, platform, type, credentials, extra) VALUES ($1,'openai','oauth','{}','{}')
		RETURNING id`, t.Name()).Scan(&scope.OwnerAccountID))
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM accounts WHERE id=$1`, scope.OwnerAccountID)
	})
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO account_openai_oauth_os_credentials
		(account_id, os_family, credentials, status) VALUES ($1,$2,'{}','authorized')
		RETURNING authorization_generation::text`, scope.OwnerAccountID, scope.OSFamily).Scan(&scope.AuthorizationGeneration))
	return scope, &AESEncryptor{key: []byte("0123456789abcdef0123456789abcdef")}
}

func openAIHTTPCookieEntry(name, path string) openaicookies.Entry {
	now := time.Now().UTC()
	entry := openaicookies.Entry{
		Name: name, Value: "synthetic-cookie-value-" + name, Domain: "chatgpt.com", Path: path,
		HostOnly: true, Secure: true, HTTPOnly: true, Quoted: true, SameSite: http.SameSiteLaxMode,
		ExpiresAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now,
	}
	entry.Key = openaicookies.CookieKey(entry.Name, entry.Domain, entry.Path)
	return entry
}

func mergeOpenAIHTTPCookies(ctx context.Context, store openaicookies.Store, scope openaicookies.Scope, mutations []openaicookies.Mutation) error {
	_, err := store.Merge(ctx, scope, mutations)
	return err
}

func TestOpenAIHTTPCookieStoreLatestEntriesEncryptedAndAbsoluteExpiry(t *testing.T) {
	ctx := context.Background()
	scope, encryptor := createOpenAIHTTPCookieFixture(t)
	first := NewOpenAIHTTPCookieStore(integrationDB, encryptor)
	second := NewOpenAIHTTPCookieStore(integrationDB, encryptor)
	entry := openAIHTTPCookieEntry("__oailb", "/backend-api")
	// Include sub-microsecond precision: PostgreSQL's metadata must never change
	// the absolute expiry that the encrypted cookie restores after a restart.
	entry.ExpiresAt = entry.ExpiresAt.Truncate(time.Microsecond).Add(987 * time.Nanosecond)
	before, err := second.Load(ctx, scope)
	require.NoError(t, err)
	require.Empty(t, before.Entries)
	require.Empty(t, before.Versions)
	firstVersions, err := first.Merge(ctx, scope, []openaicookies.Mutation{{Key: entry.Key, Entry: &entry}})
	require.NoError(t, err)
	loaded, err := second.Load(ctx, scope)
	require.NoError(t, err)
	require.Equal(t, []openaicookies.Entry{entry}, loaded.Entries)
	require.Equal(t, firstVersions, loaded.Versions)

	var ciphertext string
	var expiresAt time.Time
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT encrypted_entry, expires_at
		FROM openai_http_cookies WHERE owner_account_id=$1 AND entry_key=$2`, scope.OwnerAccountID, entry.Key).Scan(&ciphertext, &expiresAt))
	for _, secret := range []string{entry.Name, entry.Value, entry.Domain, entry.Path} {
		require.NotContains(t, ciphertext, secret)
	}
	require.Equal(t, entry.ExpiresAt.Truncate(time.Microsecond), expiresAt)
	plaintext, err := encryptor.Decrypt(ciphertext)
	require.NoError(t, err)
	var payload openAIHTTPCookiePayload
	require.NoError(t, json.Unmarshal([]byte(plaintext), &payload))
	require.Equal(t, scope, payload.Scope)
	require.Equal(t, entry, payload.Entry)

	entry.Value = "replacement-cookie-value"
	entry.UpdatedAt = entry.UpdatedAt.Add(time.Second)
	secondVersions, err := second.Merge(ctx, scope, []openaicookies.Mutation{{Key: entry.Key, Entry: &entry}})
	require.NoError(t, err)
	require.Greater(t, secondVersions[entry.Key], firstVersions[entry.Key])
	restarted := NewOpenAIHTTPCookieStore(integrationDB, encryptor)
	loaded, err = restarted.Load(ctx, scope)
	require.NoError(t, err)
	require.Equal(t, []openaicookies.Entry{entry}, loaded.Entries, "loading and re-saving must preserve the absolute expiry")
	deletedVersions, err := first.Merge(ctx, scope, []openaicookies.Mutation{{Key: entry.Key}})
	require.NoError(t, err)
	require.Greater(t, deletedVersions[entry.Key], secondVersions[entry.Key])
	loaded, err = second.Load(ctx, scope)
	require.NoError(t, err)
	require.Empty(t, loaded.Entries, "another store must immediately observe deletion")
	require.Equal(t, deletedVersions, loaded.Versions, "deletion must retain an invalidation revision")
}

func TestOpenAIHTTPCookieStoreConcurrentMergesPreserveOtherEntries(t *testing.T) {
	ctx := context.Background()
	scope, encryptor := createOpenAIHTTPCookieFixture(t)
	stores := []openaicookies.Store{NewOpenAIHTTPCookieStore(integrationDB, encryptor), NewOpenAIHTTPCookieStore(integrationDB, encryptor)}
	const writers = 8
	start := make(chan struct{})
	errors := make(chan error, writers)
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			entry := openAIHTTPCookieEntry("__cf_bm", fmt.Sprintf("/cookies/%d", i))
			<-start
			errors <- mergeOpenAIHTTPCookies(ctx, stores[i%len(stores)], scope, []openaicookies.Mutation{{Key: entry.Key, Entry: &entry}})
		}(i)
	}
	close(start)
	wg.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	loaded, err := stores[0].Load(ctx, scope)
	require.NoError(t, err)
	require.Len(t, loaded.Entries, writers, "responses merge individual identities instead of replacing a stale jar snapshot")
	require.Len(t, loaded.Versions, writers)
}

func TestOpenAIHTTPCookieStoreConcurrentIdentityRevisionsFollowCommittedValue(t *testing.T) {
	ctx := context.Background()
	scope, encryptor := createOpenAIHTTPCookieFixture(t)
	stores := []openaicookies.Store{NewOpenAIHTTPCookieStore(integrationDB, encryptor), NewOpenAIHTTPCookieStore(integrationDB, encryptor)}
	const writers = 8
	type result struct {
		revision int64
		value    string
		err      error
	}
	start := make(chan struct{})
	results := make(chan result, writers)
	for i := 0; i < writers; i++ {
		go func(i int) {
			entry := openAIHTTPCookieEntry("__oailb", "/")
			entry.Value = fmt.Sprintf("synthetic-concurrent-cookie-%d", i)
			<-start
			versions, err := stores[i%len(stores)].Merge(ctx, scope, []openaicookies.Mutation{{Key: entry.Key, Entry: &entry}})
			results <- result{revision: versions[entry.Key], value: entry.Value, err: err}
		}(i)
	}
	close(start)
	var latest result
	seen := make(map[int64]bool, writers)
	for i := 0; i < writers; i++ {
		outcome := <-results
		require.NoError(t, outcome.err)
		require.Greater(t, outcome.revision, int64(0))
		require.False(t, seen[outcome.revision], "every mutation must get a distinct revision")
		seen[outcome.revision] = true
		if outcome.revision > latest.revision {
			latest = outcome
		}
	}
	loaded, err := stores[0].Load(ctx, scope)
	require.NoError(t, err)
	require.Len(t, loaded.Entries, 1)
	require.Equal(t, latest.value, loaded.Entries[0].Value, "the maximum revision must identify the final committed value")
	require.Equal(t, latest.revision, loaded.Versions[loaded.Entries[0].Key])
}

type openAIHTTPCookieRoundTripFunc func(*http.Request) (*http.Response, error)

func (f openAIHTTPCookieRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func newOpenAIHTTPCookieTestClient(store openaicookies.Store) *http.Client {
	manager := openaicookies.NewManager(store)
	return &http.Client{Transport: manager.Wrap(openAIHTTPCookieRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		header := make(http.Header)
		header.Set("X-Test-Observed-Cookie", request.Header.Get("Cookie"))
		if cookie := request.Header.Get("X-Test-Set-Cookie"); cookie != "" {
			header.Add("Set-Cookie", cookie)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(strings.NewReader("")), Request: request}, nil
	}))}
}

func requestOpenAIHTTPCookieTestClient(t *testing.T, client *http.Client, scope openaicookies.Scope, setCookie string) string {
	t.Helper()
	ctx := openaicookies.WithScope(context.Background(), scope)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://chatgpt.com/backend-api/responses", nil)
	require.NoError(t, err)
	request.Header.Set("X-Test-Set-Cookie", setCookie)
	response, err := client.Do(request)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	return response.Header.Get("X-Test-Observed-Cookie")
}

func TestOpenAIHTTPCookieStoreManagersInvalidateRemoteSessionChanges(t *testing.T) {
	for _, remoteSession := range []bool{false, true} {
		name := "remote_deletion"
		if remoteSession {
			name = "remote_session_replacement"
		}
		t.Run(name, func(t *testing.T) {
			scope, encryptor := createOpenAIHTTPCookieFixture(t)
			firstStore := NewOpenAIHTTPCookieStore(integrationDB, encryptor)
			secondStore := NewOpenAIHTTPCookieStore(integrationDB, encryptor)
			first := newOpenAIHTTPCookieTestClient(firstStore)
			second := newOpenAIHTTPCookieTestClient(secondStore)
			require.Empty(t, requestOpenAIHTTPCookieTestClient(t, first, scope, "__oailb=local-session-a; Path=/; Secure; HttpOnly"))
			require.Equal(t, "__oailb=local-session-a", requestOpenAIHTTPCookieTestClient(t, first, scope, ""))
			before, err := firstStore.Load(context.Background(), scope)
			require.NoError(t, err)
			require.Empty(t, before.Entries, "a session's value must never be persisted")

			remoteCookie := "__oailb=; Max-Age=0; Path=/; Secure; HttpOnly"
			if remoteSession {
				remoteCookie = "__oailb=remote-session-b; Path=/; Secure; HttpOnly"
			}
			require.Empty(t, requestOpenAIHTTPCookieTestClient(t, second, scope, remoteCookie), "another process cannot read the local session value")
			// The first manager did not observe an intermediate persistent cookie.
			// A revision tombstone must still invalidate its previous local value.
			require.Empty(t, requestOpenAIHTTPCookieTestClient(t, first, scope, ""), "a remote deletion or session replacement must invalidate the old local session")
			if remoteSession {
				require.Equal(t, "__oailb=remote-session-b", requestOpenAIHTTPCookieTestClient(t, second, scope, ""))
			}
			snapshot, err := firstStore.Load(context.Background(), scope)
			require.NoError(t, err)
			require.Empty(t, snapshot.Entries)
			key := openaicookies.CookieKey("__oailb", "chatgpt.com", "/")
			require.Greater(t, snapshot.Versions[key], before.Versions[key])
			var ciphertext sql.NullString
			var expiry sql.NullTime
			require.NoError(t, integrationDB.QueryRowContext(context.Background(), `SELECT encrypted_entry, expires_at
				FROM openai_http_cookies WHERE owner_account_id=$1 AND entry_key=$2`, scope.OwnerAccountID, key).Scan(&ciphertext, &expiry))
			require.False(t, ciphertext.Valid, "session invalidation stores only metadata, never a session value")
			require.False(t, expiry.Valid)
		})
	}
}

func TestOpenAIHTTPCookieStoreAuthorizationGenerationFence(t *testing.T) {
	ctx := context.Background()
	scope, encryptor := createOpenAIHTTPCookieFixture(t)
	store := NewOpenAIHTTPCookieStore(integrationDB, encryptor)
	entry := openAIHTTPCookieEntry("__oailb", "/")
	mutation := []openaicookies.Mutation{{Key: entry.Key, Entry: &entry}}
	require.NoError(t, mergeOpenAIHTTPCookies(ctx, store, scope, mutation))
	_, err := integrationDB.ExecContext(ctx, `UPDATE account_openai_oauth_os_credentials
		SET state_generation=gen_random_uuid(), credential_epoch=gen_random_uuid()
		WHERE account_id=$1 AND os_family=$2`, scope.OwnerAccountID, scope.OSFamily)
	require.NoError(t, err)
	loaded, err := store.Load(ctx, scope)
	require.NoError(t, err)
	require.Len(t, loaded.Entries, 1, "token refresh does not replace the authorization's cookie pool")

	next := scope
	next.AuthorizationGeneration = uuid.NewString()
	_, err = integrationDB.ExecContext(ctx, `UPDATE account_openai_oauth_os_credentials
		SET authorization_generation=$3::uuid WHERE account_id=$1 AND os_family=$2`, scope.OwnerAccountID, scope.OSFamily, next.AuthorizationGeneration)
	require.NoError(t, err)
	_, err = store.Load(ctx, scope)
	require.ErrorIs(t, err, openaicookies.ErrStaleScope)
	require.ErrorIs(t, mergeOpenAIHTTPCookies(ctx, store, scope, mutation), openaicookies.ErrStaleScope)
	loaded, err = store.Load(ctx, next)
	require.NoError(t, err)
	require.Empty(t, loaded.Entries)
	require.Empty(t, loaded.Versions)
	var oldRows int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM openai_http_cookies
		WHERE owner_account_id=$1 AND authorization_generation::text=$2`, scope.OwnerAccountID, scope.AuthorizationGeneration).Scan(&oldRows))
	require.Zero(t, oldRows, "loading the new authorization cleans up the previous generation")
	require.NoError(t, mergeOpenAIHTTPCookies(ctx, store, next, mutation))
	_, err = integrationDB.ExecContext(ctx, `UPDATE account_openai_oauth_os_credentials
		SET status='unauthorized' WHERE account_id=$1 AND os_family=$2`, next.OwnerAccountID, next.OSFamily)
	require.NoError(t, err)
	_, err = store.Load(ctx, next)
	require.ErrorIs(t, err, openaicookies.ErrStaleScope)
	require.ErrorIs(t, mergeOpenAIHTTPCookies(ctx, store, next, mutation), openaicookies.ErrStaleScope)
}

func TestOpenAIHTTPCookieStoreWaitsForRevocation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	scope, encryptor := createOpenAIHTTPCookieFixture(t)
	store := NewOpenAIHTTPCookieStore(integrationDB, encryptor)
	entry := openAIHTTPCookieEntry("__oailb", "/")
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	var ownerID int64
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT id FROM accounts WHERE id=$1 FOR NO KEY UPDATE`, scope.OwnerAccountID).Scan(&ownerID))
	_, err = tx.ExecContext(ctx, `UPDATE account_openai_oauth_os_credentials SET status='unauthorized',
		authorization_generation=gen_random_uuid() WHERE account_id=$1 AND os_family=$2`, scope.OwnerAccountID, scope.OSFamily)
	require.NoError(t, err)
	loadResult := make(chan error, 1)
	mergeResult := make(chan error, 1)
	go func() { _, err := store.Load(ctx, scope); loadResult <- err }()
	go func() {
		mergeResult <- mergeOpenAIHTTPCookies(ctx, store, scope, []openaicookies.Mutation{{Key: entry.Key, Entry: &entry}})
	}()
	select {
	case <-loadResult:
		t.Fatal("load escaped the pending account configuration lock")
	case <-mergeResult:
		t.Fatal("merge escaped the pending account configuration lock")
	case <-time.After(50 * time.Millisecond):
	}
	require.NoError(t, tx.Commit())
	require.ErrorIs(t, <-loadResult, openaicookies.ErrStaleScope)
	require.ErrorIs(t, <-mergeResult, openaicookies.ErrStaleScope)
}

func TestOpenAIHTTPCookieStoreRejectsSessionsAndCorruption(t *testing.T) {
	ctx := context.Background()
	scope, encryptor := createOpenAIHTTPCookieFixture(t)
	store := NewOpenAIHTTPCookieStore(integrationDB, encryptor)
	entry := openAIHTTPCookieEntry("__oailb", "/")
	session := entry
	session.ExpiresAt = time.Time{}
	require.ErrorIs(t, mergeOpenAIHTTPCookies(ctx, store, scope, []openaicookies.Mutation{{Key: session.Key, Entry: &session}}), openaicookies.ErrStoreCorrupt)
	ephemeral := openaicookies.Scope{EphemeralID: "authorization-flow"}
	_, err := store.Load(ctx, ephemeral)
	require.ErrorIs(t, err, openaicookies.ErrInvalidScope)
	require.ErrorIs(t, mergeOpenAIHTTPCookies(ctx, store, ephemeral, nil), openaicookies.ErrInvalidScope)
	mismatch := entry
	mismatch.Key = openaicookies.CookieKey("__cf_bm", entry.Domain, entry.Path)
	require.ErrorIs(t, mergeOpenAIHTTPCookies(ctx, store, scope, []openaicookies.Mutation{{Key: mismatch.Key, Entry: &mismatch}}), openaicookies.ErrStoreCorrupt)

	expired := entry
	expired.ExpiresAt = time.Now().UTC().Add(-time.Minute)
	require.NoError(t, mergeOpenAIHTTPCookies(ctx, store, scope, []openaicookies.Mutation{{Key: expired.Key, Entry: &expired}}))
	loaded, err := store.Load(ctx, scope)
	require.NoError(t, err)
	require.Empty(t, loaded.Entries)
	require.Greater(t, loaded.Versions[entry.Key], int64(0))
	require.NoError(t, mergeOpenAIHTTPCookies(ctx, store, scope, []openaicookies.Mutation{{Key: entry.Key, Entry: &entry}}))
	_, err = integrationDB.ExecContext(ctx, `UPDATE openai_http_cookies SET expires_at=NOW()-INTERVAL '1 minute' WHERE owner_account_id=$1`, scope.OwnerAccountID)
	require.NoError(t, err)
	loaded, err = store.Load(ctx, scope)
	require.NoError(t, err)
	require.Empty(t, loaded.Entries, "expired persisted entries must not be replayed")
	require.Greater(t, loaded.Versions[entry.Key], int64(0), "expired identities must still invalidate older local sessions")
	var expiredRows int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM openai_http_cookies WHERE owner_account_id=$1`, scope.OwnerAccountID).Scan(&expiredRows))
	require.Equal(t, 1, expiredRows, "retain each current identity's invalidation revision")
	require.NoError(t, mergeOpenAIHTTPCookies(ctx, store, scope, []openaicookies.Mutation{{Key: entry.Key, Entry: &entry}}))
	_, err = integrationDB.ExecContext(ctx, `UPDATE openai_http_cookies SET encrypted_entry=$2 WHERE owner_account_id=$1`, scope.OwnerAccountID, "invalid-ciphertext-secret")
	require.NoError(t, err)
	_, err = store.Load(ctx, scope)
	require.ErrorIs(t, err, openaicookies.ErrStoreCorrupt)
	require.Equal(t, "cookie_store_corrupt", err.Error(), "database and decrypt errors must not disclose ciphertext or values")
}
