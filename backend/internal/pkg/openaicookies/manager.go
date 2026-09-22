package openaicookies

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// Manager shares persistent cookies through Store while keeping session cookies
// in this process. It never uses a transport pool key as a credential identity.
type Manager struct {
	store  Store
	now    func() time.Time
	mu     sync.Mutex
	memory map[Scope]map[string]memoryEntry
	scopes [64]chan struct{}
}

type memoryEntry struct {
	Entry
	version int64
}

func NewManager(store Store) *Manager {
	manager := &Manager{store: store, now: time.Now, memory: make(map[Scope]map[string]memoryEntry)}
	for index := range manager.scopes {
		manager.scopes[index] = make(chan struct{}, 1)
	}
	return manager
}

// ClearEphemeral ends an unbound authorization flow, including its session cookies.
func (m *Manager) ClearEphemeral(id string) {
	if m == nil {
		return
	}
	scope := Scope{EphemeralID: id}
	lock := m.scopeLock(scope)
	lock <- struct{}{}
	defer func() { <-lock }()
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.memory, scope)
}

func (m *Manager) scopeLock(scope Scope) chan struct{} {
	key := CookieKey(scope.OSFamily, scope.AuthorizationGeneration, scope.EphemeralID)
	digest := sha256.Sum256([]byte(key))
	return m.scopes[(uint64(scope.OwnerAccountID)+uint64(digest[0]))%uint64(len(m.scopes))]
}

type transport struct {
	manager *Manager
	next    http.RoundTripper
}

func (t *transport) CloseIdleConnections() {
	if closer, ok := t.next.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

// Wrap belongs immediately around the actual HTTP RoundTripper. It intentionally
// does not modify websocket dialers or install an http.Client.Jar.
func (m *Manager) Wrap(next http.RoundTripper) http.RoundTripper {
	if next == nil {
		next = http.DefaultTransport
	}
	return &transport{manager: m, next: next}
}

func (t *transport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request == nil {
		return nil, errors.New("cookie_request_missing")
	}
	// Websocket upgrades never read or update the HTTP jar, even if a future
	// caller reuses a scoped HTTP client for its handshake.
	for name, values := range request.Header {
		if !strings.EqualFold(name, "Upgrade") {
			continue
		}
		for _, value := range values {
			for _, token := range strings.Split(value, ",") {
				if strings.EqualFold(strings.TrimSpace(token), "websocket") {
					return t.next.RoundTrip(request)
				}
			}
		}
	}
	scope, scoped := ScopeFromContext(request.Context())
	if !scoped && !AllowedURL(request.URL) {
		return t.next.RoundTrip(request)
	}
	clone := request.Clone(request.Context())
	// Remove every spelling, including redirect headers from a previous hop.
	for name := range clone.Header {
		if strings.EqualFold(name, "Cookie") {
			delete(clone.Header, name)
		}
	}
	if clone.Header == nil {
		clone.Header = make(http.Header)
	}
	diagnostic := Diagnostic{Source: "none"}
	if !scoped {
		diagnostic.Reason = "cookie_scope_missing"
		report(clone.Context(), diagnostic)
		return t.next.RoundTrip(clone)
	}
	if !scope.Valid() {
		diagnostic.Reason = ErrInvalidScope.Error()
		report(clone.Context(), diagnostic)
		return t.next.RoundTrip(clone)
	}
	if !AllowedURL(clone.URL) {
		diagnostic.Reason = "cookie_host_not_allowed"
		report(clone.Context(), diagnostic)
		return t.next.RoundTrip(clone)
	}
	entries, err := t.manager.load(clone.Context(), scope)
	if err != nil {
		diagnostic.Reason = safeReason(err)
		report(clone.Context(), diagnostic)
		return t.next.RoundTrip(clone)
	}
	now := t.manager.now()
	diagnostic = apply(clone, entries, now)
	if !scope.Persistent() && diagnostic.Sent {
		diagnostic.Source = "memory"
	}
	report(clone.Context(), diagnostic)
	response, transportErr := t.next.RoundTrip(clone)
	if response != nil {
		changes := normalizeResponse(clone.URL, response.Cookies(), t.manager.now())
		if len(changes) != 0 {
			for index := range changes {
				if changes[index].Entry == nil {
					continue
				}
				for _, existing := range entries {
					if existing.Key == changes[index].Key {
						changes[index].Entry.CreatedAt = existing.CreatedAt
						break
					}
				}
			}
			saved, deleted, mergeErr := t.manager.merge(clone.Context(), scope, changes)
			diagnostic.SavedCount, diagnostic.DeletedCount = saved, deleted
			if mergeErr != nil {
				diagnostic.Reason = safeReason(mergeErr)
			} else {
				diagnostic.Reason = "cookie_updated"
			}
			report(clone.Context(), diagnostic)
		}
	}
	return response, transportErr
}

func safeReason(err error) string {
	switch {
	case errors.Is(err, ErrInvalidScope):
		return ErrInvalidScope.Error()
	case errors.Is(err, ErrStaleScope):
		return ErrStaleScope.Error()
	case errors.Is(err, ErrStoreCorrupt):
		return ErrStoreCorrupt.Error()
	default:
		return ErrStoreUnavailable.Error()
	}
}

func (m *Manager) load(ctx context.Context, scope Scope) ([]Entry, error) {
	if m == nil {
		return nil, ErrStoreUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	lock := m.scopeLock(scope)
	select {
	case lock <- struct{}{}:
	case <-ctx.Done():
		return nil, ErrStoreUnavailable
	}
	defer func() { <-lock }()
	var snapshot Snapshot
	if scope.Persistent() {
		if m.store == nil {
			return nil, ErrStoreUnavailable
		}
		var err error
		snapshot, err = m.store.Load(ctx, scope)
		if err != nil {
			m.mu.Lock()
			delete(m.memory, scope)
			m.mu.Unlock()
			return nil, err
		}
	}
	now := m.now()
	merged := make(map[string]Entry, len(snapshot.Entries))
	for _, entry := range snapshot.Entries {
		if !entry.Valid() || entry.ExpiresAt.IsZero() || snapshot.Versions[entry.Key] <= 0 {
			return nil, ErrStoreCorrupt
		}
		if entry.ExpiresAt.After(now) {
			merged[entry.Key] = entry
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, entry := range m.memory[scope] {
		if scope.Persistent() && snapshot.Versions[key] != entry.version {
			delete(m.memory[scope], key)
			continue
		}
		if !entry.ExpiresAt.IsZero() && !entry.ExpiresAt.After(now) {
			delete(m.memory[scope], key)
			continue
		}
		merged[key] = entry.Entry
	}
	result := make([]Entry, 0, len(merged))
	for _, entry := range merged {
		result = append(result, entry)
	}
	return result, nil
}

func apply(request *http.Request, entries []Entry, now time.Time) Diagnostic {
	jar := newJar()
	// Reconstruct in creation order to preserve the RFC same-path ordering.
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].CreatedAt.Equal(entries[j].CreatedAt) {
			return entries[i].Key < entries[j].Key
		}
		return entries[i].CreatedAt.Before(entries[j].CreatedAt)
	})
	for _, entry := range entries {
		if !entry.ExpiresAt.IsZero() && !entry.ExpiresAt.After(now) {
			continue
		}
		origin := &url.URL{Scheme: "https", Host: entry.Domain, Path: entry.Path}
		cookie := entry.cookie()
		cookie.Expires = time.Time{} // Expiry was checked using the manager clock.
		jar.SetCookies(origin, []*http.Cookie{cookie})
	}
	diagnostic := Diagnostic{Reason: "cookie_empty", Source: "none"}
	names := make(map[string]bool)
	persistent, session := false, false
	for _, cookie := range jar.Cookies(request.URL) {
		request.AddCookie(cookie)
		diagnostic.SentCount++
		names[cookie.Name] = true
	}
	for _, entry := range entries {
		if !names[entry.Name] || !entryMatches(entry, request.URL, now) {
			continue
		}
		metadata := DiagnosticCookie{Name: entry.Name}
		if !entry.ExpiresAt.IsZero() {
			expires := entry.ExpiresAt
			metadata.ExpiresAt = &expires
			persistent = true
		} else {
			session = true
		}
		diagnostic.Cookies = append(diagnostic.Cookies, metadata)
	}
	for name := range names {
		diagnostic.Names = append(diagnostic.Names, name)
	}
	sort.Strings(diagnostic.Names)
	if diagnostic.SentCount > 0 {
		diagnostic.Sent = true
		diagnostic.Reason = "cookie_sent"
	}
	switch {
	case persistent && session:
		diagnostic.Source = "mixed"
	case persistent:
		diagnostic.Source = "persistent"
	case session:
		diagnostic.Source = "memory"
	}
	return diagnostic
}

func entryMatches(entry Entry, target *url.URL, now time.Time) bool {
	if !entry.ExpiresAt.IsZero() && !entry.ExpiresAt.After(now) {
		return false
	}
	jar := newJar()
	cookie := entry.cookie()
	cookie.Expires = time.Time{}
	jar.SetCookies(&url.URL{Scheme: "https", Host: entry.Domain, Path: entry.Path}, []*http.Cookie{cookie})
	return len(jar.Cookies(target)) != 0
}

func normalizeResponse(target *url.URL, cookies []*http.Cookie, now time.Time) []Mutation {
	// Duplicate identities follow their order in the response: the last wins.
	changes := make(map[string]Mutation)
	for index, cookie := range cookies {
		entry, remove, accepted := normalize(target, cookie, now)
		if !accepted {
			continue
		}
		mutation := Mutation{Key: entry.Key}
		if !remove {
			entry.CreatedAt = now.Add(time.Duration(index) * time.Nanosecond)
			mutation.Entry = &entry
		}
		changes[entry.Key] = mutation
	}
	result := make([]Mutation, 0, len(changes))
	for _, change := range changes {
		result = append(result, change)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Key < result[j].Key })
	return result
}

func (m *Manager) merge(ctx context.Context, scope Scope, changes []Mutation) (int, int, error) {
	if m == nil {
		return 0, 0, ErrStoreUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	lock := m.scopeLock(scope)
	select {
	case lock <- struct{}{}:
	case <-ctx.Done():
		return 0, 0, ErrStoreUnavailable
	}
	defer func() { <-lock }()
	persistent := make([]Mutation, 0, len(changes))
	for _, change := range changes {
		if change.Entry == nil || change.Entry.ExpiresAt.IsZero() {
			persistent = append(persistent, Mutation{Key: change.Key})
		} else {
			persistent = append(persistent, change)
		}
	}
	// Every bound update, even replacing a persistent cookie with a session cookie,
	// passes the authorization fence. Local state changes only after commit.
	var versions map[string]int64
	if scope.Persistent() {
		if m.store == nil {
			return 0, 0, ErrStoreUnavailable
		}
		var err error
		versions, err = m.store.Merge(ctx, scope, persistent)
		if err != nil {
			return 0, 0, err
		}
		for _, change := range changes {
			if versions[change.Key] <= 0 {
				return 0, 0, ErrStoreCorrupt
			}
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.memory[scope] == nil {
		m.memory[scope] = make(map[string]memoryEntry)
	}
	saved, deleted := 0, 0
	for _, change := range changes {
		if change.Entry == nil {
			delete(m.memory[scope], change.Key)
			deleted++
			continue
		}
		saved++
		if !scope.Persistent() || change.Entry.ExpiresAt.IsZero() {
			m.memory[scope][change.Key] = memoryEntry{Entry: *change.Entry, version: versions[change.Key]}
		} else {
			delete(m.memory[scope], change.Key)
		}
	}
	if len(m.memory[scope]) == 0 {
		delete(m.memory, scope)
	}
	return saved, deleted, nil
}
