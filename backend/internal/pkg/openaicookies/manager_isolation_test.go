package openaicookies

import (
	"context"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestManagerCrossNodeMutationInvalidatesLocalSession(t *testing.T) {
	for _, persistentReplacement := range []bool{false, true} {
		store := &memoryStore{}
		manager, other := NewManager(store), NewManager(store)
		target, _ := url.Parse("https://chatgpt.com/")
		now := time.Now()
		entry, _, ok := normalize(target, &http.Cookie{Name: "__oailb", Value: "session", Path: "/"}, now)
		require.True(t, ok)
		_, _, err := manager.merge(context.Background(), testScope, []Mutation{{Key: entry.Key, Entry: &entry}})
		require.NoError(t, err)
		entry.Value = "new-value"
		entry.UpdatedAt = now.Add(-time.Hour) // Cross-node clock skew cannot select the old session.
		if persistentReplacement {
			entry.ExpiresAt = now.Add(time.Hour)
		}
		_, _, err = other.merge(context.Background(), testScope, []Mutation{{Key: entry.Key, Entry: &entry}})
		require.NoError(t, err)
		// The first node never reads the intermediate replacement before deletion.
		_, _, err = other.merge(context.Background(), testScope, []Mutation{{Key: entry.Key}})
		require.NoError(t, err)
		entries, err := manager.load(context.Background(), testScope)
		require.NoError(t, err)
		require.Empty(t, entries)
		snapshot, err := store.Load(context.Background(), testScope)
		require.NoError(t, err)
		require.Empty(t, snapshot.Entries)
		require.Len(t, snapshot.Versions, 1)
	}
}

func TestManagerWebsocketBypassAndMissingScopeCookieRemoval(t *testing.T) {
	store := &memoryStore{}
	manager := NewManager(store)
	var sent bool
	client := localCookieClient(t, manager, func(w http.ResponseWriter, r *http.Request) {
		sent = r.Header.Get("Cookie") != ""
		w.Header().Add("Set-Cookie", "__oailb=route; Path=/; Max-Age=3600")
	})
	request, err := http.NewRequest(http.MethodGet, "https://chatgpt.com/", nil)
	require.NoError(t, err)
	request.Header.Set("Cookie", "caller=blocked")
	response, err := client.Do(request)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	require.False(t, sent)
	cookieRequest(t, client, testScope, "/", http.Header{"Upgrade": []string{"h2c, WebSocket"}}, nil)
	require.Zero(t, store.loads, "websocket upgrades never access the HTTP jar")
	snapshot, err := store.Load(context.Background(), testScope)
	require.NoError(t, err)
	require.Empty(t, snapshot.Entries)
	require.Empty(t, snapshot.Versions)
}

func TestManagerPreservesCreationOrderWhenUpdatingCookie(t *testing.T) {
	manager := NewManager(&memoryStore{})
	now := time.Now()
	manager.now = func() time.Time { return now }
	var order []string
	client := localCookieClient(t, manager, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/first":
			w.Header().Add("Set-Cookie", "__oailb=first; Path=/; Max-Age=3600")
		case "/second":
			w.Header().Add("Set-Cookie", "__cflb=second; Path=/; Max-Age=3600")
		case "/update":
			w.Header().Add("Set-Cookie", "__oailb=updated; Path=/; Max-Age=3600")
		default:
			for _, cookie := range r.Cookies() {
				order = append(order, cookie.Name)
			}
		}
	})
	for _, path := range []string{"/first", "/second", "/update", "/observe"} {
		now = now.Add(time.Second)
		cookieRequest(t, client, testScope, path, nil, nil)
	}
	require.Equal(t, []string{"__oailb", "__cflb"}, order)
}

func TestCookieContextHelpersAcceptNil(t *testing.T) {
	scope, ok := ScopeFromContext(WithScope(nil, testScope))
	require.True(t, ok)
	require.Equal(t, testScope, scope)
	_, ok = ScopeFromContext(WithoutScope(nil))
	require.False(t, ok)
	called := false
	report(WithObserver(nil, func(Diagnostic) { called = true }), Diagnostic{})
	require.True(t, called)
}

type unavailableStore struct{}

func (unavailableStore) Load(ctx context.Context, _ Scope) (Snapshot, error) {
	<-ctx.Done()
	return Snapshot{}, ctx.Err()
}
func (unavailableStore) Merge(context.Context, Scope, []Mutation) (map[string]int64, error) {
	return nil, ErrStoreUnavailable
}

func TestManagerStoreTimeoutDoesNotCancelBusinessRequest(t *testing.T) {
	manager := NewManager(unavailableStore{})
	client := localCookieClient(t, manager, func(w http.ResponseWriter, r *http.Request) {
		require.Empty(t, r.Header.Get("Cookie"))
		w.WriteHeader(http.StatusOK)
	})
	var diagnostics []Diagnostic
	started := time.Now()
	cookieRequest(t, client, testScope, "/", nil, &diagnostics)
	require.Less(t, time.Since(started), 4*time.Second)
	require.Equal(t, ErrStoreUnavailable.Error(), diagnostics[0].Reason)
}
