package openaicookies

import (
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// Manager stores only process-local cookies for explicitly scoped, unbound
// OAuth flows. Account-bound HTTP requests bypass this manager entirely.
// No cookie value is persisted or shared between authorization flows.
type Manager struct {
	now    func() time.Time
	mu     sync.Mutex
	memory map[Scope]map[string]Entry
}

func NewManager() *Manager {
	return &Manager{now: time.Now, memory: make(map[Scope]map[string]Entry)}
}

func (m *Manager) ClearEphemeral(id string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.memory, Scope{EphemeralID: id})
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

// Wrap keeps the client's shared Jar disabled. Only an explicit temporary
// authorization flow can learn cookies; ordinary requests retain their headers.
func (m *Manager) Wrap(next http.RoundTripper) http.RoundTripper {
	if next == nil {
		next = http.DefaultTransport
	}
	return &transport{manager: m, next: next}
}

func websocketUpgrade(request *http.Request) bool {
	for name, values := range request.Header {
		if strings.EqualFold(name, "Upgrade") {
			for _, value := range values {
				for _, token := range strings.Split(value, ",") {
					if strings.EqualFold(strings.TrimSpace(token), "websocket") {
						return true
					}
				}
			}
		}
	}
	return false
}

// EnabledForRequest selects temporary OAuth scopes only, never bound accounts.
func EnabledForRequest(request *http.Request) bool {
	if request == nil || websocketUpgrade(request) {
		return false
	}
	scope, ok := ScopeFromContext(request.Context())
	return ok && scope.Valid() && scope.EphemeralID != ""
}

func removeHeader(header http.Header, target string) {
	for name := range header {
		if strings.EqualFold(name, target) {
			delete(header, name)
		}
	}
}

func (t *transport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request == nil {
		return nil, errors.New("cookie_request_missing")
	}
	if t.manager == nil || !EnabledForRequest(request) {
		return t.next.RoundTrip(request)
	}
	clone := request.Clone(request.Context())
	if clone.Header == nil {
		clone.Header = make(http.Header)
	}
	// A flow never imports caller cookies. This also strips redirected cookies
	// before applying the new destination's HTTPS and host restrictions.
	removeHeader(clone.Header, "Cookie")
	if !AllowedURL(clone.URL) {
		return t.next.RoundTrip(clone)
	}
	scope, _ := ScopeFromContext(clone.Context())
	m := t.manager
	now := m.now()
	m.mu.Lock()
	entries := make([]Entry, 0, len(m.memory[scope]))
	for key, entry := range m.memory[scope] {
		if !entry.ExpiresAt.IsZero() && !entry.ExpiresAt.After(now) {
			delete(m.memory[scope], key)
			continue
		}
		entries = append(entries, entry)
	}
	m.mu.Unlock()
	apply(clone, entries, now)
	response, err := t.next.RoundTrip(clone)
	if response != nil && err == nil && response.StatusCode >= 200 && response.StatusCode < 400 {
		changes := normalizeResponse(clone.URL, response.Cookies(), m.now())
		m.mu.Lock()
		if m.memory[scope] == nil {
			m.memory[scope] = make(map[string]Entry)
		}
		for _, change := range changes {
			if change.Entry == nil {
				delete(m.memory[scope], change.Key)
			} else {
				entry := *change.Entry
				if previous, ok := m.memory[scope][change.Key]; ok {
					entry.CreatedAt = previous.CreatedAt
				}
				m.memory[scope][change.Key] = entry
			}
		}
		m.mu.Unlock()
	}
	return response, err
}

func apply(request *http.Request, entries []Entry, now time.Time) {
	jar := newJar()
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
		cookie := entry.cookie()
		cookie.Expires = time.Time{}
		jar.SetCookies(&url.URL{Scheme: "https", Host: entry.Domain, Path: entry.Path}, []*http.Cookie{cookie})
	}
	for _, cookie := range jar.Cookies(request.URL) {
		request.AddCookie(cookie)
	}
}

func normalizeResponse(target *url.URL, cookies []*http.Cookie, now time.Time) []mutation {
	changes := make(map[string]mutation)
	for index, cookie := range cookies {
		entry, remove, accepted := normalize(target, cookie, now)
		if !accepted {
			continue
		}
		change := mutation{Key: entry.Key}
		if !remove {
			entry.CreatedAt = now.Add(time.Duration(index) * time.Nanosecond)
			change.Entry = &entry
		}
		changes[entry.Key] = change
	}
	result := make([]mutation, 0, len(changes))
	for _, change := range changes {
		result = append(result, change)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Key < result[j].Key })
	return result
}
