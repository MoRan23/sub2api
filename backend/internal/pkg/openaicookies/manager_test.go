package openaicookies

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type memoryStore struct {
	mu       sync.Mutex
	entries  map[Scope]map[string]Entry
	versions map[Scope]map[string]int64
	version  int64
	loadErr  error
	mergeErr error
	loads    int
}

func (s *memoryStore) Load(_ context.Context, scope Scope) (Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loads++
	if s.loadErr != nil {
		return Snapshot{}, s.loadErr
	}
	var result []Entry
	for _, entry := range s.entries[scope] {
		result = append(result, entry)
	}
	versions := make(map[string]int64)
	for key, version := range s.versions[scope] {
		versions[key] = version
	}
	return Snapshot{Entries: result, Versions: versions}, nil
}

func (s *memoryStore) Merge(_ context.Context, scope Scope, changes []Mutation) (map[string]int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.mergeErr != nil {
		return nil, s.mergeErr
	}
	if s.entries == nil {
		s.entries = make(map[Scope]map[string]Entry)
	}
	if s.entries[scope] == nil {
		s.entries[scope] = make(map[string]Entry)
	}
	if s.versions == nil {
		s.versions = make(map[Scope]map[string]int64)
	}
	if s.versions[scope] == nil {
		s.versions[scope] = make(map[string]int64)
	}
	versions := make(map[string]int64)
	for _, change := range changes {
		s.version++
		s.versions[scope][change.Key] = s.version
		versions[change.Key] = s.version
		if change.Entry == nil {
			delete(s.entries[scope], change.Key)
		} else {
			s.entries[scope][change.Key] = *change.Entry
		}
	}
	return versions, nil
}

var testScope = Scope{OwnerAccountID: 1, OSFamily: "windows", AuthorizationGeneration: "00000000-0000-4000-8000-000000000001"}

func localCookieClient(t *testing.T, manager *Manager, handler http.HandlerFunc) *http.Client {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	address := server.Listener.Addr().String()
	transport := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}} // Local test certificate only.
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: manager.Wrap(transport)}
}

func cookieRequest(t *testing.T, client *http.Client, scope Scope, path string, headers http.Header, diagnostics *[]Diagnostic) {
	t.Helper()
	ctx := WithScope(context.Background(), scope)
	if diagnostics != nil {
		ctx = WithObserver(ctx, func(d Diagnostic) { *diagnostics = append(*diagnostics, d) })
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://chatgpt.com"+path, nil)
	require.NoError(t, err)
	request.Header = headers.Clone()
	response, err := client.Do(request)
	require.NoError(t, err)
	_, err = io.Copy(io.Discard, response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
}

func TestManagerHTTPAuxiliaryResponsesPersistenceAndSession(t *testing.T) {
	store := &memoryStore{}
	manager := NewManager(store)
	var sent []string
	client := localCookieClient(t, manager, func(w http.ResponseWriter, r *http.Request) {
		sent = append(sent, r.Header.Get("Cookie"))
		if r.URL.Path == "/backend-api/codex/plugins/list" {
			w.Header().Add("Set-Cookie", "__oailb=route; Path=/; Max-Age=3600; Secure; HttpOnly")
			w.Header().Add("Set-Cookie", "__cflb=session; Path=/; Secure")
			w.Header().Add("Set-Cookie", "login_session=private; Path=/; Max-Age=3600")
		}
		w.WriteHeader(http.StatusOK)
	})
	cookieRequest(t, client, testScope, "/backend-api/codex/plugins/list", http.Header{"Cookie": []string{"caller=must-not-forward"}}, nil)
	require.Empty(t, sent[0])
	var diagnostics []Diagnostic
	cookieRequest(t, client, testScope, "/backend-api/codex/responses?model=other", nil, &diagnostics)
	require.True(t, strings.Contains(sent[1], "__oailb=") && strings.Contains(sent[1], "__cflb="))
	require.False(t, strings.Contains(sent[1], "login_session="))
	require.Equal(t, "mixed", diagnostics[0].Source)
	require.Equal(t, []string{"__cflb", "__oailb"}, diagnostics[0].Names)
	loaded, err := store.Load(context.Background(), testScope)
	require.NoError(t, err)
	require.Len(t, loaded.Entries, 1)
	newManager := NewManager(store)
	entries, err := newManager.load(context.Background(), testScope)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "__oailb", entries[0].Name)
	for _, scope := range []Scope{{OwnerAccountID: 2, OSFamily: "windows", AuthorizationGeneration: testScope.AuthorizationGeneration}, {OwnerAccountID: 1, OSFamily: "linux", AuthorizationGeneration: testScope.AuthorizationGeneration}, {OwnerAccountID: 1, OSFamily: "windows", AuthorizationGeneration: "new-generation"}} {
		entries, err = manager.load(context.Background(), scope)
		require.NoError(t, err)
		require.Empty(t, entries)
	}
}

func TestManagerAbsoluteExpiryDeletionAndSessionReplacement(t *testing.T) {
	store := &memoryStore{}
	manager := NewManager(store)
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	manager.now = func() time.Time { return now }
	target, _ := url.Parse("https://chatgpt.com/backend-api/codex/responses")
	entry, deleted, accepted := normalize(target, &http.Cookie{Name: "__oailb", Value: "route", MaxAge: 240, Expires: now.Add(time.Hour), Path: "/"}, now)
	require.True(t, accepted)
	require.False(t, deleted)
	require.Equal(t, now.Add(240*time.Second), entry.ExpiresAt)
	_, _, err := manager.merge(context.Background(), testScope, []Mutation{{Key: entry.Key, Entry: &entry}})
	require.NoError(t, err)
	now = now.Add(239 * time.Second)
	entries, err := manager.load(context.Background(), testScope)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, entry.ExpiresAt, entries[0].ExpiresAt)
	now = now.Add(time.Second)
	entries, err = manager.load(context.Background(), testScope)
	require.NoError(t, err)
	require.Empty(t, entries)
	entry.ExpiresAt = now.Add(time.Hour)
	_, _, err = manager.merge(context.Background(), testScope, []Mutation{{Key: entry.Key, Entry: &entry}})
	require.NoError(t, err)
	entry.ExpiresAt = time.Time{}
	_, _, err = manager.merge(context.Background(), testScope, []Mutation{{Key: entry.Key, Entry: &entry}})
	require.NoError(t, err)
	loaded, err := store.Load(context.Background(), testScope)
	require.NoError(t, err)
	require.Empty(t, loaded.Entries, "a session replacement must delete the persistent version")
	for _, cookie := range []*http.Cookie{{Name: "__oailb", Path: "/", MaxAge: -1}, {Name: "__oailb", Path: "/", Expires: now.Add(-time.Second)}} {
		changes := normalizeResponse(target, []*http.Cookie{cookie}, now)
		require.Len(t, changes, 1)
		require.Nil(t, changes[0].Entry)
		_, _, err = manager.merge(context.Background(), testScope, changes)
		require.NoError(t, err)
	}
	entries, err = manager.load(context.Background(), testScope)
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestCookieDomainPathAndHostPolicy(t *testing.T) {
	now := time.Now()
	origin, _ := url.Parse("https://chatgpt.com/backend-api/codex/plugins/list")
	var entries []Entry
	for _, cookie := range []*http.Cookie{{Name: "__oailb", Value: "host", Path: "/"}, {Name: "__oailb", Value: "domain", Domain: ".chatgpt.com", Path: "/backend-api"}, {Name: "__cflb", Value: "default"}} {
		entry, _, ok := normalize(origin, cookie, now)
		require.True(t, ok)
		entries = append(entries, entry)
	}
	for _, check := range []struct {
		url   string
		count int
	}{{"https://chatgpt.com/backend-api/codex/responses", 2}, {"https://chatgpt.com/backend-api/codex/plugins/item", 3}, {"https://sub.chatgpt.com/backend-api/codex/responses", 1}, {"https://chatgpt.com/backend-api-other", 1}} {
		r, _ := http.NewRequest(http.MethodGet, check.url, nil)
		d := apply(r, entries, now)
		require.Equal(t, check.count, d.SentCount)
	}
	for _, target := range []string{"http://chatgpt.com/", "https://api.openai.com/", "https://chatgpt.com.evil.example/", "https://sub.chat.openai.com/"} {
		u, _ := url.Parse(target)
		require.False(t, AllowedURL(u))
	}
	for _, cookie := range []*http.Cookie{{Name: "session", Value: "blocked"}, {Name: "__oailb", Value: "blocked", Domain: "evil.example"}, {Name: "__oailb", Value: "blocked", Domain: "com"}} {
		_, _, accepted := normalize(origin, cookie, now)
		require.False(t, accepted)
	}
}

func TestManagerStoreFailureAndEphemeralFlow(t *testing.T) {
	store := &memoryStore{loadErr: errors.New("private transport details")}
	manager := NewManager(store)
	var sent string
	client := localCookieClient(t, manager, func(w http.ResponseWriter, r *http.Request) {
		sent = r.Header.Get("Cookie")
		w.Header().Add("Set-Cookie", "__oailb=route; Path=/; Max-Age=3600")
	})
	var diagnostics []Diagnostic
	cookieRequest(t, client, testScope, "/", http.Header{"cookie": []string{"caller=blocked"}}, &diagnostics)
	require.Empty(t, sent)
	require.Equal(t, ErrStoreUnavailable.Error(), diagnostics[0].Reason)
	require.False(t, diagnostics[0].Sent)
	temporary := Scope{EphemeralID: "one-authorization-flow"}
	cookieRequest(t, client, temporary, "/", nil, nil)
	cookieRequest(t, client, temporary, "/", nil, &diagnostics)
	require.True(t, strings.Contains(sent, "__oailb="))
	require.Equal(t, "memory", diagnostics[len(diagnostics)-1].Source)
	manager.ClearEphemeral(temporary.EphemeralID)
	cookieRequest(t, client, temporary, "/", nil, nil)
	require.Empty(t, sent)
	require.Equal(t, 1, store.loads, "temporary flows never access persistent storage")
}

func TestManagerPerEntryMergeLatestStorageAndFailedPublication(t *testing.T) {
	store := &memoryStore{}
	first, second := NewManager(store), NewManager(store)
	target, _ := url.Parse("https://chatgpt.com/")
	now := time.Now()
	left, _, ok := normalize(target, &http.Cookie{Name: "__oailb", Value: "one", Path: "/", MaxAge: 3600}, now)
	require.True(t, ok)
	right, _, ok := normalize(target, &http.Cookie{Name: "__cflb", Value: "two", Path: "/", MaxAge: 3600}, now)
	require.True(t, ok)
	var wait sync.WaitGroup
	for index, manager := range []*Manager{first, second} {
		entry := []Entry{left, right}[index]
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, _, err := manager.merge(context.Background(), testScope, []Mutation{{Key: entry.Key, Entry: &entry}})
			if err != nil {
				t.Errorf("merge failed: %s", safeReason(err))
			}
		}()
	}
	wait.Wait()
	for _, manager := range []*Manager{first, second} {
		entries, err := manager.load(context.Background(), testScope)
		require.NoError(t, err)
		require.Len(t, entries, 2)
	}
	_, _, err := first.merge(context.Background(), testScope, []Mutation{{Key: left.Key}})
	require.NoError(t, err)
	entries, err := second.load(context.Background(), testScope)
	require.NoError(t, err)
	require.Len(t, entries, 1, "each request observes deletion from another process")
	store.mergeErr = ErrStaleScope
	left.ExpiresAt = time.Time{}
	_, _, err = first.merge(context.Background(), testScope, []Mutation{{Key: left.Key, Entry: &left}})
	require.ErrorIs(t, err, ErrStaleScope)
	entries, err = first.load(context.Background(), testScope)
	require.NoError(t, err)
	require.Len(t, entries, 1, "failed session publication cannot change process memory")
}

func TestManagerRedirectUsesTargetPathAndRejectsForeignHost(t *testing.T) {
	manager := NewManager(&memoryStore{})
	var destinationCookie string
	client := localCookieClient(t, manager, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/start":
			w.Header().Add("Set-Cookie", "__oailb=route; Path=/allowed; Max-Age=3600")
			http.Redirect(w, r, "/allowed/step", http.StatusFound)
		case "/allowed/step":
			require.True(t, strings.Contains(r.Header.Get("Cookie"), "__oailb="))
			http.Redirect(w, r, "https://untrusted.example/finish", http.StatusFound)
		default:
			destinationCookie = r.Header.Get("Cookie")
		}
	})
	cookieRequest(t, client, testScope, "/start", nil, nil)
	require.Empty(t, destinationCookie)
}
