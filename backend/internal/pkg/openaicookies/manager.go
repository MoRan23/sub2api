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

// Manager applies explicitly supplied ticket bundles. Its only mutable jar is
// isolated, process-local memory for unbound OAuth flows. Bound cookie values
// are persisted with the ticket only; this manager has no storage dependency.
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

// Wrap does not install an http.Client.Jar and never handles websocket upgrades.
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

func removeHeader(header http.Header, target string) {
	for name := range header {
		if strings.EqualFold(name, target) {
			delete(header, name)
		}
	}
}

func restoreBundleRequest(request *http.Request, stripWithoutFallback bool) {
	if restore, ok := request.Context().Value(fallbackKey{}).(func(*http.Request)); ok && restore != nil {
		restore(request)
	} else if stripWithoutFallback {
		removeHeader(request.Header, "Cookie")
		removeHeader(request.Header, "x-codex-turn-state")
	}
}

func (t *transport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request == nil {
		return nil, errors.New("cookie_request_missing")
	}
	if websocketUpgrade(request) {
		return t.next.RoundTrip(request)
	}
	policy, enabled := request.Context().Value(bundleKey{}).(bundlePolicy)
	scope, _ := ScopeFromContext(request.Context())
	attempt := attemptFromContext(request.Context())
	if policy.bypass || !enabled && scope.EphemeralID == "" {
		attempt.invalidate()
		return t.next.RoundTrip(request)
	}
	clone := request.Clone(request.Context())
	if clone.Header == nil {
		clone.Header = make(http.Header)
	}
	if enabled {
		if guard, ok := clone.Context().Value(guardKey{}).(func(*http.Request) bool); ok && guard != nil && !guard(clone) {
			attempt.invalidate()
			restoreBundleRequest(clone, false)
			return t.next.RoundTrip(clone)
		}
	}
	if t.manager == nil || !scope.Valid() || enabled && !scope.Persistent() {
		attempt.invalidate()
		restoreBundleRequest(clone, true)
		report(clone.Context(), Diagnostic{Reason: ErrInvalidScope.Error(), Source: "none"})
		return t.next.RoundTrip(clone)
	}
	if !AllowedURL(clone.URL) {
		attempt.invalidate()
		restoreBundleRequest(clone, true)
		report(clone.Context(), Diagnostic{Reason: "cookie_host_not_allowed", Source: "none"})
		return t.next.RoundTrip(clone)
	}
	if !enabled {
		removeHeader(clone.Header, "Cookie")
		return t.roundTripEphemeral(clone, scope)
	}
	now := t.manager.now()
	if !policy.bundle.Fresh() && !policy.bundle.ValidAt(now) {
		attempt.invalidate()
		restoreBundleRequest(clone, true)
		report(clone.Context(), Diagnostic{Reason: ErrBundleExpired.Error(), Source: "none"})
		return t.next.RoundTrip(clone)
	}
	sequence := attempt.begin(scope, t.manager.now)
	removeHeader(clone.Header, "Cookie")
	// Only cookies eligible for this physical URL belong to its candidate. The
	// caller's raw Cookie header is never read or copied into an accepted bundle.
	entries := make([]Entry, 0, len(policy.bundle.Entries))
	for _, entry := range policy.bundle.Entries {
		if entryMatches(entry, clone.URL, now) {
			entries = append(entries, entry)
		}
	}
	diagnostic := apply(clone, entries, now)
	if diagnostic.Sent {
		diagnostic.Source = "bundle"
	}
	report(clone.Context(), diagnostic)
	response, err := t.next.RoundTrip(clone)
	if response != nil && err == nil && response.StatusCode >= 200 && response.StatusCode < 300 {
		receivedAt := t.manager.now()
		entries = mergeEntries(entries, normalizeResponse(clone.URL, response.Cookies(), receivedAt))
		attempt.capture(sequence, entries, receivedAt, diagnostic)
		if attempt != nil && sequence != 0 {
			diagnostic.Reason = "cookie_staged"
			report(clone.Context(), diagnostic)
		}
	}
	return response, err
}

func (t *transport) roundTripEphemeral(request *http.Request, scope Scope) (*http.Response, error) {
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
	diagnostic := apply(request, entries, now)
	if diagnostic.Sent {
		diagnostic.Source = "memory"
	}
	report(request.Context(), diagnostic)
	response, err := t.next.RoundTrip(request)
	if response != nil && err == nil && response.StatusCode >= 200 && response.StatusCode < 400 {
		changes := normalizeResponse(request.URL, response.Cookies(), m.now())
		m.mu.Lock()
		if m.memory[scope] == nil {
			m.memory[scope] = make(map[string]Entry)
		}
		for _, change := range changes {
			if change.Entry == nil {
				delete(m.memory[scope], change.Key)
			} else {
				m.memory[scope][change.Key] = *change.Entry
			}
		}
		m.mu.Unlock()
	}
	return response, err
}

func mergeEntries(base []Entry, changes []mutation) []Entry {
	entries := make(map[string]Entry, len(base)+len(changes))
	for _, entry := range base {
		entries[entry.Key] = entry
	}
	for _, change := range changes {
		if change.Entry == nil {
			delete(entries, change.Key)
			continue
		}
		entry := *change.Entry
		if previous, ok := entries[change.Key]; ok {
			entry.CreatedAt = previous.CreatedAt
		}
		entries[change.Key] = entry
	}
	result := make([]Entry, 0, len(entries))
	for _, entry := range entries {
		result = append(result, entry)
	}
	return result
}

func apply(request *http.Request, entries []Entry, now time.Time) Diagnostic {
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
	diagnostic := Diagnostic{Reason: "cookie_empty", Source: "none"}
	names := make(map[string]bool)
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
		}
		diagnostic.Cookies = append(diagnostic.Cookies, metadata)
	}
	for name := range names {
		diagnostic.Names = append(diagnostic.Names, name)
	}
	sort.Strings(diagnostic.Names)
	if diagnostic.SentCount > 0 {
		diagnostic.Sent, diagnostic.Reason = true, "cookie_sent"
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

func normalizeResponse(target *url.URL, cookies []*http.Cookie, now time.Time) []mutation {
	changes := make(map[string]mutation)
	for index, cookie := range cookies {
		entry, remove, accepted := normalize(target, cookie, now)
		if !accepted {
			continue
		}
		mutation := mutation{Key: entry.Key}
		if !remove {
			entry.CreatedAt = now.Add(time.Duration(index) * time.Nanosecond)
			mutation.Entry = &entry
		}
		changes[entry.Key] = mutation
	}
	result := make([]mutation, 0, len(changes))
	for _, change := range changes {
		result = append(result, change)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Key < result[j].Key })
	return result
}
