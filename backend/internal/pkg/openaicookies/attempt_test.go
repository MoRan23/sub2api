package openaicookies

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func stagedCookieRequest(t *testing.T, client *http.Client, path string) *Attempt {
	t.Helper()
	ctx, attempt := WithAttempt(WithScope(context.Background(), testScope))
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://chatgpt.com"+path, nil)
	require.NoError(t, err)
	response, err := client.Do(request)
	require.NoError(t, err)
	_, err = io.Copy(io.Discard, response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	return attempt
}

func TestAttemptOnlyAcceptedTargetChangesPersistentAndSessionCookies(t *testing.T) {
	store := &memoryStore{}
	manager := NewManager(store)
	client := localCookieClient(t, manager, func(w http.ResponseWriter, r *http.Request) {
		value := "replacement"
		if r.URL.Path == "/seed" {
			value = "accepted"
		}
		w.Header().Add("Set-Cookie", "__oailb="+value+"; Path=/; Max-Age=3600; Secure")
		w.Header().Add("Set-Cookie", "__cflb="+value+"; Path=/; Secure")
	})
	seed := stagedCookieRequest(t, client, "/seed")
	before, err := store.Load(context.Background(), testScope)
	require.NoError(t, err)
	require.Empty(t, before.Entries, "response headers alone cannot publish cookies")
	require.NoError(t, seed.Commit(context.Background()))
	before, err = store.Load(context.Background(), testScope)
	require.NoError(t, err)
	require.Len(t, before.Entries, 1, "session values stay out of persistent storage")
	previous, err := manager.load(context.Background(), testScope)
	require.NoError(t, err)
	require.Len(t, previous, 2)

	abnormal := stagedCookieRequest(t, client, "/abnormal-ticket")
	require.Equal(t, "cookie_staged", abnormal.Diagnostic().Reason)
	require.Zero(t, abnormal.Diagnostic().SavedCount)
	abnormal.Discard()
	require.Equal(t, "cookie_target_rejected", abnormal.Diagnostic().Reason)
	require.ErrorIs(t, abnormal.Commit(context.Background()), ErrAttemptClosed)
	request, err := http.NewRequestWithContext(WithScope(context.Background(), testScope), http.MethodGet, "https://chatgpt.com/plugins/list", nil)
	require.NoError(t, err)
	response, err := client.Do(request)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	after, err := store.Load(context.Background(), testScope)
	require.NoError(t, err)
	require.Equal(t, before, after, "neither rejected tickets nor auxiliary responses change saved entries or revisions")
	current, err := manager.load(context.Background(), testScope)
	require.NoError(t, err)
	require.ElementsMatch(t, previous, current)

	accepted := stagedCookieRequest(t, client, "/target-ticket")
	require.NoError(t, accepted.Commit(context.Background()))
	require.Equal(t, "cookie_updated", accepted.Diagnostic().Reason)
	require.Equal(t, 2, accepted.Diagnostic().SavedCount)
	current, err = manager.load(context.Background(), testScope)
	require.NoError(t, err)
	for _, entry := range current {
		require.True(t, entry.Value == "replacement")
	}
	committed, err := store.Load(context.Background(), testScope)
	require.NoError(t, err)
	require.NoError(t, accepted.Commit(context.Background()))
	after, err = store.Load(context.Background(), testScope)
	require.NoError(t, err)
	require.Equal(t, committed, after, "commit is idempotent")
	otherProcess, err := NewManager(store).load(context.Background(), testScope)
	require.NoError(t, err)
	require.Len(t, otherProcess, 1)
}

type cookieRoundTripFunc func(*http.Request) (*http.Response, error)

func (f cookieRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestAttemptFailedHTTPNeverStagesCookies(t *testing.T) {
	for _, test := range []struct {
		name string
		code int
		err  error
	}{{"HTTP rejection", 403, nil}, {"server failure", 500, nil}, {"redirect", 302, nil}, {"transport failure", 200, errors.New("synthetic failure")}} {
		t.Run(test.name, func(t *testing.T) {
			store := &memoryStore{}
			manager := NewManager(store)
			ctx, attempt := WithAttempt(WithScope(context.Background(), testScope))
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://chatgpt.com/", nil)
			require.NoError(t, err)
			transport := manager.Wrap(cookieRoundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: test.code, Header: http.Header{"Set-Cookie": {"__oailb=rejected; Path=/; Max-Age=3600"}}}, test.err
			}))
			_, err = transport.RoundTrip(request)
			require.ErrorIs(t, err, test.err)
			require.NoError(t, attempt.Commit(ctx), "even an erroneous acceptance call cannot publish failed HTTP cookies")
			snapshot, err := store.Load(context.Background(), testScope)
			require.NoError(t, err)
			require.Empty(t, snapshot.Entries)
			require.Empty(t, snapshot.Versions)
		})
	}
}

func TestAttemptLateAcceptedResponseCannotOverwriteNewerCookies(t *testing.T) {
	store := &memoryStore{}
	first, second := NewManager(store), NewManager(store)
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Set-Cookie", "__oailb="+strings.TrimPrefix(r.URL.Path, "/")+"; Path=/; Max-Age=3600")
		w.Header().Add("Set-Cookie", "__cflb=session; Path=/")
	}
	older := stagedCookieRequest(t, localCookieClient(t, first, handler), "/older")
	newer := stagedCookieRequest(t, localCookieClient(t, second, handler), "/newer")
	require.NoError(t, newer.Commit(context.Background()))
	before, err := store.Load(context.Background(), testScope)
	require.NoError(t, err)
	require.ErrorIs(t, older.Commit(context.Background()), ErrConflict)
	require.Equal(t, ErrConflict.Error(), older.Diagnostic().Reason)
	require.Zero(t, older.Diagnostic().SavedCount)
	after, err := store.Load(context.Background(), testScope)
	require.NoError(t, err)
	require.Equal(t, before, after)
	entries, err := first.load(context.Background(), testScope)
	require.NoError(t, err)
	require.Len(t, entries, 1, "failed session CAS cannot update process memory")
	require.True(t, entries[0].Value == "newer")
}

func TestAttemptCommitKeepsResponseExpiryAndSafeDiagnosticCopy(t *testing.T) {
	store := &memoryStore{}
	manager := NewManager(store)
	now := time.Now().UTC()
	manager.now = func() time.Time { return now }
	client := localCookieClient(t, manager, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Set-Cookie", "__oailb=seed; Path=/; Max-Age=10")
	})
	seed := stagedCookieRequest(t, client, "/")
	require.NoError(t, seed.Commit(context.Background()))
	pending := stagedCookieRequest(t, client, "/")
	metadata := pending.Diagnostic()
	require.Len(t, metadata.Cookies, 1)
	*metadata.Cookies[0].ExpiresAt = time.Time{}
	metadata.Names[0] = "changed"
	require.False(t, pending.Diagnostic().Cookies[0].ExpiresAt.IsZero())
	require.Equal(t, []string{"__oailb"}, pending.Diagnostic().Names)
	expiry := now.Add(10 * time.Second)
	now = now.Add(8 * time.Second)
	require.NoError(t, pending.Commit(context.Background()))
	entries, err := manager.load(context.Background(), testScope)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, expiry, entries[0].ExpiresAt)
	now = now.Add(3 * time.Second)
	entries, err = manager.load(context.Background(), testScope)
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestAttemptHelpersAcceptNilContext(t *testing.T) {
	ctx, attempt := WithAttempt(nil)
	require.NotNil(t, ctx)
	require.NoError(t, attempt.Commit(nil))
	require.Equal(t, Diagnostic{}, attempt.Diagnostic())
	var absent *Attempt
	require.NoError(t, absent.Commit(nil))
	absent.Discard()
}
