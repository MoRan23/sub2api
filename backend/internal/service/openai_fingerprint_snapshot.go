package service

import (
	"crypto/hmac"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	fingerprintObservationSnapshotTTL   = 5 * time.Minute
	fingerprintObservationSnapshotLimit = 32
	fingerprintObservationPageDefault   = 20
	fingerprintObservationPageMaximum   = 100
)

var (
	// ErrFingerprintObservationSnapshotNotFound is returned for an unknown or
	// expired snapshot token. Callers should create a fresh snapshot instead of
	// retrying the token because expired snapshot data is scrubbed immediately.
	ErrFingerprintObservationSnapshotNotFound = errors.New("fingerprint observation snapshot not found or expired")
	// ErrFingerprintObservationNodeNotFound is returned when a node ID does not
	// belong to the supplied snapshot or is not valid for the requested level.
	ErrFingerprintObservationNodeNotFound = errors.New("fingerprint observation node not found")
	// ErrFingerprintObservationCursorInvalid is returned when a cursor is
	// malformed, modified, or reused for a different node/list level.
	ErrFingerprintObservationCursorInvalid = errors.New("fingerprint observation cursor is invalid")
)

// FingerprintObservationUserSummary is the top-level pagination unit. Counts
// cover the complete immutable snapshot, not just the current page.
type FingerprintObservationUserSummary struct {
	NodeID                       string    `json:"node_id"`
	UserID                       int64     `json:"user_id"`
	Username                     string    `json:"username"`
	Email                        string    `json:"email"`
	Unattributed                 bool      `json:"unattributed"`
	FirstObservedAt              time.Time `json:"first_observed_at"`
	LastObservedAt               time.Time `json:"last_observed_at"`
	ObservationCount             int       `json:"observation_count"`
	UnattributedObservationCount int       `json:"unattributed_observation_count"`
	APIKeyCount                  int       `json:"api_key_count"`
	SessionCount                 int       `json:"session_count"`
	ThreadCount                  int       `json:"thread_count"`
}

// FingerprintObservationAPIKeySummary describes one API key beneath a user.
// It never contains the raw API key value.
type FingerprintObservationAPIKeySummary struct {
	NodeID                       string    `json:"node_id"`
	UserID                       int64     `json:"user_id"`
	APIKeyID                     int64     `json:"api_key_id"`
	APIKeyName                   string    `json:"api_key_name"`
	Unattributed                 bool      `json:"unattributed"`
	FirstObservedAt              time.Time `json:"first_observed_at"`
	LastObservedAt               time.Time `json:"last_observed_at"`
	ObservationCount             int       `json:"observation_count"`
	UnattributedObservationCount int       `json:"unattributed_observation_count"`
	SessionCount                 int       `json:"session_count"`
	ThreadCount                  int       `json:"thread_count"`
}

// FingerprintObservationSessionSummary describes one outbound root session.
// Unattributed is true only for the synthetic branch that contains all rows
// whose final wire session ID was empty; SessionID remains empty in that case.
type FingerprintObservationSessionSummary struct {
	NodeID                     string    `json:"node_id"`
	UserID                     int64     `json:"user_id"`
	APIKeyID                   int64     `json:"api_key_id"`
	SessionID                  string    `json:"session_id"`
	Unattributed               bool      `json:"unattributed"`
	FirstObservedAt            time.Time `json:"first_observed_at"`
	LastObservedAt             time.Time `json:"last_observed_at"`
	ObservationCount           int       `json:"observation_count"`
	UnthreadedObservationCount int       `json:"unthreaded_observation_count"`
	ThreadCount                int       `json:"thread_count"`
	ChildThreadCount           int       `json:"child_thread_count"`
	HasRootThread              bool      `json:"has_root_thread"`
	HasUnthreaded              bool      `json:"has_unthreaded"`
}

type FingerprintObservationThreadRelation string

const (
	FingerprintObservationThreadRelationRoot       FingerprintObservationThreadRelation = "root"
	FingerprintObservationThreadRelationDescendant FingerprintObservationThreadRelation = "descendant"
	FingerprintObservationThreadRelationUnthreaded FingerprintObservationThreadRelation = "unthreaded"
)

// FingerprintObservationThreadSummary describes one actual thread or the
// explicit unthreaded branch for rows whose final wire thread ID was empty.
type FingerprintObservationThreadSummary struct {
	NodeID             string                               `json:"node_id"`
	SessionID          string                               `json:"session_id"`
	ThreadID           string                               `json:"thread_id"`
	ParentThreadID     string                               `json:"parent_thread_id"`
	ForkedFromThreadID string                               `json:"forked_from_thread_id"`
	Relation           FingerprintObservationThreadRelation `json:"relation"`
	Unthreaded         bool                                 `json:"unthreaded"`
	FirstObservedAt    time.Time                            `json:"first_observed_at"`
	LastObservedAt     time.Time                            `json:"last_observed_at"`
	ObservationCount   int                                  `json:"observation_count"`
}

// FingerprintObservationUserPage uses page-number pagination because users
// are the only rows rendered before disclosure. SnapshotToken must be reused
// when requesting another page from the same view.
type FingerprintObservationUserPage struct {
	SnapshotToken string                              `json:"snapshot_token"`
	Items         []FingerprintObservationUserSummary `json:"items"`
	Total         int                                 `json:"total"`
	Page          int                                 `json:"page"`
	PageSize      int                                 `json:"page_size"`
	Pages         int                                 `json:"pages"`
}

type FingerprintObservationAPIKeyPage struct {
	Items      []FingerprintObservationAPIKeySummary `json:"items"`
	Total      int                                   `json:"total"`
	NextCursor string                                `json:"next_cursor"`
}

type FingerprintObservationSessionPage struct {
	Items      []FingerprintObservationSessionSummary `json:"items"`
	Total      int                                    `json:"total"`
	NextCursor string                                 `json:"next_cursor"`
}

type FingerprintObservationThreadPage struct {
	Items      []FingerprintObservationThreadSummary `json:"items"`
	Total      int                                   `json:"total"`
	NextCursor string                                `json:"next_cursor"`
}

type FingerprintObservationEntryPage struct {
	Items      []FingerprintObservationEntry `json:"items"`
	Total      int                           `json:"total"`
	NextCursor string                        `json:"next_cursor"`
}

type fingerprintObservationSnapshotStore struct {
	mu               sync.Mutex
	snapshots        map[string]*fingerprintObservationSnapshot
	order            []string
	ttl              time.Duration
	max              int
	now              func() time.Time
	random           io.Reader
	scheduleExpiry   bool
	expiryTimer      *time.Timer
	expiryGeneration uint64
}

type fingerprintObservationSnapshot struct {
	token        string
	createdAt    time.Time
	expiresAt    time.Time
	cursorKey    [32]byte
	entries      []FingerprintObservationEntry
	users        []*fingerprintObservationSnapshotUser
	usersByID    map[string]*fingerprintObservationSnapshotUser
	apiKeysByID  map[string]*fingerprintObservationSnapshotAPIKey
	sessionsByID map[string]*fingerprintObservationSnapshotSession
	threadsByID  map[string]*fingerprintObservationSnapshotThread
}

type fingerprintObservationSnapshotUser struct {
	summary FingerprintObservationUserSummary
	apiKeys []*fingerprintObservationSnapshotAPIKey
	byKey   map[fingerprintObservationSnapshotAPIKeyGroupKey]*fingerprintObservationSnapshotAPIKey
}

type fingerprintObservationSnapshotUserGroupKey struct {
	id       int64
	fallback string
}

type fingerprintObservationSnapshotAPIKeyGroupKey struct {
	id       int64
	fallback string
}

type fingerprintObservationSnapshotAPIKey struct {
	summary   FingerprintObservationAPIKeySummary
	sessions  []*fingerprintObservationSnapshotSession
	bySession map[fingerprintObservationSnapshotSessionKey]*fingerprintObservationSnapshotSession
}

type fingerprintObservationSnapshotSessionKey struct {
	id           string
	unattributed bool
	sequenceID   uint64
}

type fingerprintObservationSnapshotSession struct {
	summary  FingerprintObservationSessionSummary
	threads  []*fingerprintObservationSnapshotThread
	byThread map[fingerprintObservationSnapshotThreadKey]*fingerprintObservationSnapshotThread
}

type fingerprintObservationSnapshotThreadKey struct {
	id         string
	unthreaded bool
}

type fingerprintObservationSnapshotThread struct {
	summary FingerprintObservationThreadSummary
	entries []FingerprintObservationEntry
}

var globalFingerprintObservationSnapshotStore = func() *fingerprintObservationSnapshotStore {
	store := newFingerprintObservationSnapshotStore(
		fingerprintObservationSnapshotTTL,
		fingerprintObservationSnapshotLimit,
		time.Now,
		cryptorand.Reader,
	)
	store.scheduleExpiry = true
	return store
}()

func newFingerprintObservationSnapshotStore(ttl time.Duration, max int, now func() time.Time, random io.Reader) *fingerprintObservationSnapshotStore {
	if ttl <= 0 {
		ttl = fingerprintObservationSnapshotTTL
	}
	if max <= 0 {
		max = fingerprintObservationSnapshotLimit
	}
	if now == nil {
		now = time.Now
	}
	if random == nil {
		random = cryptorand.Reader
	}
	return &fingerprintObservationSnapshotStore{
		snapshots: make(map[string]*fingerprintObservationSnapshot),
		ttl:       ttl,
		max:       max,
		now:       now,
		random:    random,
	}
}

// CreateFingerprintObservationSnapshot copies the current ring and returns an
// opaque token. The observer lock remains held until the snapshot is installed
// so a concurrent disable cannot clear the store and then have old data added
// back afterward.
func CreateFingerprintObservationSnapshot() (string, error) {
	o := globalFingerprintObserver
	if o == nil {
		return globalFingerprintObservationSnapshotStore.create(nil)
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.enabled.Load() {
		return "", ErrFingerprintObservationSnapshotNotFound
	}
	entries := o.snapshotLocked(0)
	return globalFingerprintObservationSnapshotStore.create(entries)
}

// PageFingerprintObservationUsers returns users without splitting one user's
// API-key/session tree across pages.
func PageFingerprintObservationUsers(snapshotToken string, page, pageSize int) (FingerprintObservationUserPage, error) {
	return globalFingerprintObservationSnapshotStore.pageUsers(snapshotToken, page, pageSize)
}

func ListFingerprintObservationAPIKeys(snapshotToken, userNodeID, cursor string, limit int) (FingerprintObservationAPIKeyPage, error) {
	return globalFingerprintObservationSnapshotStore.listAPIKeys(snapshotToken, userNodeID, cursor, limit)
}

func ListFingerprintObservationSessions(snapshotToken, apiKeyNodeID, cursor string, limit int) (FingerprintObservationSessionPage, error) {
	return globalFingerprintObservationSnapshotStore.listSessions(snapshotToken, apiKeyNodeID, cursor, limit)
}

func ListFingerprintObservationThreads(snapshotToken, sessionNodeID, cursor string, limit int) (FingerprintObservationThreadPage, error) {
	return globalFingerprintObservationSnapshotStore.listThreads(snapshotToken, sessionNodeID, cursor, limit)
}

func ListFingerprintObservationEntries(snapshotToken, threadNodeID, cursor string, limit int) (FingerprintObservationEntryPage, error) {
	return globalFingerprintObservationSnapshotStore.listEntries(snapshotToken, threadNodeID, cursor, limit)
}

func (s *fingerprintObservationSnapshotStore) create(entries []FingerprintObservationEntry) (string, error) {
	if s == nil {
		return "", ErrFingerprintObservationSnapshotNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.pruneExpiredLocked(now)

	var token string
	var cursorKey [32]byte
	for attempts := 0; attempts < 4; attempts++ {
		var material [56]byte
		if _, err := io.ReadFull(s.random, material[:]); err != nil {
			return "", fmt.Errorf("create fingerprint observation snapshot token: %w", err)
		}
		token = base64.RawURLEncoding.EncodeToString(material[:24])
		copy(cursorKey[:], material[24:])
		if _, exists := s.snapshots[token]; !exists {
			break
		}
		token = ""
	}
	if token == "" {
		return "", errors.New("create fingerprint observation snapshot token: repeated collision")
	}

	for len(s.snapshots) >= s.max && len(s.order) > 0 {
		s.removeLocked(s.order[0])
	}
	ownedEntries := make([]FingerprintObservationEntry, len(entries))
	copy(ownedEntries, entries)
	snapshot := buildFingerprintObservationSnapshot(token, cursorKey, now, now.Add(s.ttl), ownedEntries)
	s.snapshots[token] = snapshot
	s.order = append(s.order, token)
	s.scheduleExpiryLocked()
	return token, nil
}

func (s *fingerprintObservationSnapshotStore) pageUsers(token string, page, pageSize int) (FingerprintObservationUserPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot, err := s.getLocked(token)
	if err != nil {
		return FingerprintObservationUserPage{}, err
	}
	page, pageSize = normalizeFingerprintObservationPage(page, pageSize)
	total := len(snapshot.users)
	pages := (total + pageSize - 1) / pageSize
	if pages < 1 {
		pages = 1
	}
	if page > pages {
		page = pages
	}
	start := total
	if page <= pages {
		start = (page - 1) * pageSize
		if start > total {
			start = total
		}
	}
	end := start + pageSize
	if end > total {
		end = total
	}
	items := make([]FingerprintObservationUserSummary, end-start)
	for i := start; i < end; i++ {
		items[i-start] = snapshot.users[i].summary
	}
	return FingerprintObservationUserPage{
		SnapshotToken: token,
		Items:         items,
		Total:         total,
		Page:          page,
		PageSize:      pageSize,
		Pages:         pages,
	}, nil
}

func (s *fingerprintObservationSnapshotStore) listAPIKeys(token, userNodeID, cursor string, limit int) (FingerprintObservationAPIKeyPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot, err := s.getLocked(token)
	if err != nil {
		return FingerprintObservationAPIKeyPage{}, err
	}
	user := snapshot.usersByID[userNodeID]
	if user == nil {
		return FingerprintObservationAPIKeyPage{}, ErrFingerprintObservationNodeNotFound
	}
	start, limit, err := snapshot.pageBounds("api_keys", userNodeID, cursor, limit, len(user.apiKeys))
	if err != nil {
		return FingerprintObservationAPIKeyPage{}, err
	}
	end := min(start+limit, len(user.apiKeys))
	items := make([]FingerprintObservationAPIKeySummary, end-start)
	for i := start; i < end; i++ {
		items[i-start] = user.apiKeys[i].summary
	}
	return FingerprintObservationAPIKeyPage{Items: items, Total: len(user.apiKeys), NextCursor: snapshot.nextCursor("api_keys", userNodeID, end, len(user.apiKeys))}, nil
}

func (s *fingerprintObservationSnapshotStore) listSessions(token, apiKeyNodeID, cursor string, limit int) (FingerprintObservationSessionPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot, err := s.getLocked(token)
	if err != nil {
		return FingerprintObservationSessionPage{}, err
	}
	apiKey := snapshot.apiKeysByID[apiKeyNodeID]
	if apiKey == nil {
		return FingerprintObservationSessionPage{}, ErrFingerprintObservationNodeNotFound
	}
	start, limit, err := snapshot.pageBounds("sessions", apiKeyNodeID, cursor, limit, len(apiKey.sessions))
	if err != nil {
		return FingerprintObservationSessionPage{}, err
	}
	end := min(start+limit, len(apiKey.sessions))
	items := make([]FingerprintObservationSessionSummary, end-start)
	for i := start; i < end; i++ {
		items[i-start] = apiKey.sessions[i].summary
	}
	return FingerprintObservationSessionPage{Items: items, Total: len(apiKey.sessions), NextCursor: snapshot.nextCursor("sessions", apiKeyNodeID, end, len(apiKey.sessions))}, nil
}

func (s *fingerprintObservationSnapshotStore) listThreads(token, sessionNodeID, cursor string, limit int) (FingerprintObservationThreadPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot, err := s.getLocked(token)
	if err != nil {
		return FingerprintObservationThreadPage{}, err
	}
	session := snapshot.sessionsByID[sessionNodeID]
	if session == nil {
		return FingerprintObservationThreadPage{}, ErrFingerprintObservationNodeNotFound
	}
	start, limit, err := snapshot.pageBounds("threads", sessionNodeID, cursor, limit, len(session.threads))
	if err != nil {
		return FingerprintObservationThreadPage{}, err
	}
	end := min(start+limit, len(session.threads))
	items := make([]FingerprintObservationThreadSummary, end-start)
	for i := start; i < end; i++ {
		items[i-start] = session.threads[i].summary
	}
	return FingerprintObservationThreadPage{Items: items, Total: len(session.threads), NextCursor: snapshot.nextCursor("threads", sessionNodeID, end, len(session.threads))}, nil
}

func (s *fingerprintObservationSnapshotStore) listEntries(token, threadNodeID, cursor string, limit int) (FingerprintObservationEntryPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot, err := s.getLocked(token)
	if err != nil {
		return FingerprintObservationEntryPage{}, err
	}
	thread := snapshot.threadsByID[threadNodeID]
	if thread == nil {
		return FingerprintObservationEntryPage{}, ErrFingerprintObservationNodeNotFound
	}
	start, limit, err := snapshot.pageBounds("entries", threadNodeID, cursor, limit, len(thread.entries))
	if err != nil {
		return FingerprintObservationEntryPage{}, err
	}
	end := min(start+limit, len(thread.entries))
	items := make([]FingerprintObservationEntry, end-start)
	copy(items, thread.entries[start:end])
	return FingerprintObservationEntryPage{Items: items, Total: len(thread.entries), NextCursor: snapshot.nextCursor("entries", threadNodeID, end, len(thread.entries))}, nil
}

func (s *fingerprintObservationSnapshotStore) getLocked(token string) (*fingerprintObservationSnapshot, error) {
	now := s.now()
	if s.pruneExpiredLocked(now) {
		s.scheduleExpiryLocked()
	}
	snapshot := s.snapshots[token]
	if snapshot == nil || !now.Before(snapshot.expiresAt) {
		if snapshot != nil {
			s.removeLocked(token)
		}
		return nil, ErrFingerprintObservationSnapshotNotFound
	}
	return snapshot, nil
}

func (s *fingerprintObservationSnapshotStore) pruneExpiredLocked(now time.Time) bool {
	if len(s.order) == 0 {
		return false
	}
	oldLength := len(s.order)
	removed := false
	kept := s.order[:0]
	for _, token := range s.order {
		snapshot := s.snapshots[token]
		if snapshot == nil {
			continue
		}
		if !now.Before(snapshot.expiresAt) {
			snapshot.scrub()
			delete(s.snapshots, token)
			removed = true
			continue
		}
		kept = append(kept, token)
	}
	for i := len(kept); i < oldLength; i++ {
		s.order[i] = ""
	}
	s.order = kept
	return removed
}

func (s *fingerprintObservationSnapshotStore) clear() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expiryGeneration++
	if s.expiryTimer != nil {
		s.expiryTimer.Stop()
		s.expiryTimer = nil
	}
	for token, snapshot := range s.snapshots {
		if snapshot != nil {
			snapshot.scrub()
		}
		delete(s.snapshots, token)
	}
	for i := range s.order {
		s.order[i] = ""
	}
	s.order = nil
}

func (s *fingerprintObservationSnapshotStore) scheduleExpiryLocked() {
	if !s.scheduleExpiry {
		return
	}
	s.expiryGeneration++
	generation := s.expiryGeneration
	if s.expiryTimer != nil {
		s.expiryTimer.Stop()
		s.expiryTimer = nil
	}
	var earliest time.Time
	for _, snapshot := range s.snapshots {
		if snapshot != nil && (earliest.IsZero() || snapshot.expiresAt.Before(earliest)) {
			earliest = snapshot.expiresAt
		}
	}
	if earliest.IsZero() {
		return
	}
	delay := earliest.Sub(s.now())
	if delay < 0 {
		delay = 0
	}
	s.expiryTimer = time.AfterFunc(delay, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if generation != s.expiryGeneration {
			return
		}
		s.expiryTimer = nil
		s.pruneExpiredLocked(s.now())
		s.scheduleExpiryLocked()
	})
}

func (s *fingerprintObservationSnapshotStore) removeLocked(token string) {
	snapshot := s.snapshots[token]
	if snapshot != nil {
		snapshot.scrub()
	}
	delete(s.snapshots, token)
	for i, orderedToken := range s.order {
		if orderedToken != token {
			continue
		}
		copy(s.order[i:], s.order[i+1:])
		s.order[len(s.order)-1] = ""
		s.order = s.order[:len(s.order)-1]
		break
	}
}

func buildFingerprintObservationSnapshot(token string, cursorKey [32]byte, createdAt, expiresAt time.Time, entries []FingerprintObservationEntry) *fingerprintObservationSnapshot {
	snapshot := &fingerprintObservationSnapshot{
		token:        token,
		createdAt:    createdAt,
		expiresAt:    expiresAt,
		cursorKey:    cursorKey,
		entries:      entries,
		users:        []*fingerprintObservationSnapshotUser{},
		usersByID:    make(map[string]*fingerprintObservationSnapshotUser),
		apiKeysByID:  make(map[string]*fingerprintObservationSnapshotAPIKey),
		sessionsByID: make(map[string]*fingerprintObservationSnapshotSession),
		threadsByID:  make(map[string]*fingerprintObservationSnapshotThread),
	}
	usersByKey := make(map[fingerprintObservationSnapshotUserGroupKey]*fingerprintObservationSnapshotUser)
	for _, entry := range entries {
		userKey := fingerprintObservationUserGroupKey(entry)
		user := usersByKey[userKey]
		if user == nil {
			userNodeID := snapshot.nodeID("usr", strconv.FormatInt(userKey.id, 10), userKey.fallback)
			user = &fingerprintObservationSnapshotUser{
				summary: FingerprintObservationUserSummary{
					NodeID:       userNodeID,
					UserID:       entry.UserID,
					Username:     entry.Username,
					Email:        entry.Email,
					Unattributed: entry.UserID == 0,
				},
				apiKeys: []*fingerprintObservationSnapshotAPIKey{},
				byKey:   make(map[fingerprintObservationSnapshotAPIKeyGroupKey]*fingerprintObservationSnapshotAPIKey),
			}
			usersByKey[userKey] = user
			snapshot.users = append(snapshot.users, user)
			snapshot.usersByID[userNodeID] = user
		}
		observeFingerprintSnapshotWindow(&user.summary.FirstObservedAt, &user.summary.LastObservedAt, entry.Timestamp)
		user.summary.ObservationCount++
		if entry.SessionID == "" {
			user.summary.UnattributedObservationCount++
		}

		apiKeyKey := fingerprintObservationAPIKeyGroupKey(entry)
		apiKey := user.byKey[apiKeyKey]
		if apiKey == nil {
			apiKeyNodeID := snapshot.nodeID("key", user.summary.NodeID, strconv.FormatInt(apiKeyKey.id, 10), apiKeyKey.fallback)
			apiKey = &fingerprintObservationSnapshotAPIKey{
				summary: FingerprintObservationAPIKeySummary{
					NodeID:       apiKeyNodeID,
					UserID:       entry.UserID,
					APIKeyID:     entry.APIKeyID,
					APIKeyName:   entry.APIKeyName,
					Unattributed: entry.APIKeyID == 0,
				},
				sessions:  []*fingerprintObservationSnapshotSession{},
				bySession: make(map[fingerprintObservationSnapshotSessionKey]*fingerprintObservationSnapshotSession),
			}
			user.byKey[apiKeyKey] = apiKey
			user.apiKeys = append(user.apiKeys, apiKey)
			snapshot.apiKeysByID[apiKeyNodeID] = apiKey
		}
		observeFingerprintSnapshotWindow(&apiKey.summary.FirstObservedAt, &apiKey.summary.LastObservedAt, entry.Timestamp)
		apiKey.summary.ObservationCount++
		if entry.SessionID == "" {
			apiKey.summary.UnattributedObservationCount++
		}

		sessionKey := fingerprintObservationSnapshotSessionKey{id: entry.SessionID, unattributed: entry.SessionID == ""}
		sessionIdentity := entry.SessionID
		if sessionKey.unattributed {
			// An empty session ID is an observation without an identity, not a
			// shared synthetic session. Keep every such wire record independent.
			sessionKey.sequenceID = entry.SequenceID
			sessionIdentity = "@unattributed:" + strconv.FormatUint(entry.SequenceID, 10)
		}
		session := apiKey.bySession[sessionKey]
		if session == nil {
			sessionNodeID := snapshot.nodeID("ses", apiKey.summary.NodeID, sessionIdentity)
			session = &fingerprintObservationSnapshotSession{
				summary: FingerprintObservationSessionSummary{
					NodeID:       sessionNodeID,
					UserID:       entry.UserID,
					APIKeyID:     entry.APIKeyID,
					SessionID:    entry.SessionID,
					Unattributed: sessionKey.unattributed,
				},
				threads:  []*fingerprintObservationSnapshotThread{},
				byThread: make(map[fingerprintObservationSnapshotThreadKey]*fingerprintObservationSnapshotThread),
			}
			apiKey.bySession[sessionKey] = session
			apiKey.sessions = append(apiKey.sessions, session)
			snapshot.sessionsByID[sessionNodeID] = session
		}
		observeFingerprintSnapshotWindow(&session.summary.FirstObservedAt, &session.summary.LastObservedAt, entry.Timestamp)
		session.summary.ObservationCount++
		if entry.ThreadID == "" {
			session.summary.UnthreadedObservationCount++
		}

		threadKey := fingerprintObservationSnapshotThreadKey{id: entry.ThreadID, unthreaded: entry.ThreadID == ""}
		thread := session.byThread[threadKey]
		if thread == nil {
			threadIdentity := entry.ThreadID
			if threadKey.unthreaded {
				threadIdentity = "@unthreaded"
			}
			threadNodeID := snapshot.nodeID("thr", session.summary.NodeID, threadIdentity)
			relation := FingerprintObservationThreadRelationDescendant
			if threadKey.unthreaded {
				relation = FingerprintObservationThreadRelationUnthreaded
			} else if entry.SessionID != "" && entry.ThreadID == entry.SessionID {
				relation = FingerprintObservationThreadRelationRoot
			}
			thread = &fingerprintObservationSnapshotThread{
				summary: FingerprintObservationThreadSummary{
					NodeID:             threadNodeID,
					SessionID:          entry.SessionID,
					ThreadID:           entry.ThreadID,
					ParentThreadID:     entry.ParentThreadID,
					ForkedFromThreadID: entry.ForkedFromThreadID,
					Relation:           relation,
					Unthreaded:         threadKey.unthreaded,
				},
				entries: []FingerprintObservationEntry{},
			}
			session.byThread[threadKey] = thread
			session.threads = append(session.threads, thread)
			snapshot.threadsByID[threadNodeID] = thread
		}
		if thread.summary.ParentThreadID == "" {
			thread.summary.ParentThreadID = entry.ParentThreadID
		}
		if thread.summary.ForkedFromThreadID == "" {
			thread.summary.ForkedFromThreadID = entry.ForkedFromThreadID
		}
		observeFingerprintSnapshotWindow(&thread.summary.FirstObservedAt, &thread.summary.LastObservedAt, entry.Timestamp)
		thread.summary.ObservationCount++
		thread.entries = append(thread.entries, entry)
	}

	for _, user := range snapshot.users {
		user.summary.APIKeyCount = len(user.apiKeys)
		for _, apiKey := range user.apiKeys {
			for _, session := range apiKey.sessions {
				reorderFingerprintSnapshotThreads(session)
				if !session.summary.Unattributed {
					apiKey.summary.SessionCount++
					user.summary.SessionCount++
				}
				for _, thread := range session.threads {
					switch {
					case thread.summary.Unthreaded:
						session.summary.HasUnthreaded = true
					case thread.summary.Relation == FingerprintObservationThreadRelationRoot:
						session.summary.ThreadCount++
						session.summary.HasRootThread = true
					default:
						session.summary.ThreadCount++
						session.summary.ChildThreadCount++
					}
				}
				apiKey.summary.ThreadCount += session.summary.ThreadCount
				user.summary.ThreadCount += session.summary.ThreadCount
			}
		}
	}
	return snapshot
}

func reorderFingerprintSnapshotThreads(session *fingerprintObservationSnapshotSession) {
	if session == nil || len(session.threads) < 2 {
		return
	}
	ordered := make([]*fingerprintObservationSnapshotThread, 0, len(session.threads))
	for _, thread := range session.threads {
		if thread.summary.Relation == FingerprintObservationThreadRelationRoot {
			ordered = append(ordered, thread)
		}
	}
	for _, thread := range session.threads {
		if !thread.summary.Unthreaded && thread.summary.Relation != FingerprintObservationThreadRelationRoot {
			ordered = append(ordered, thread)
		}
	}
	for _, thread := range session.threads {
		if thread.summary.Unthreaded {
			ordered = append(ordered, thread)
		}
	}
	session.threads = ordered
}

func observeFingerprintSnapshotWindow(first, last *time.Time, observedAt time.Time) {
	if first.IsZero() || observedAt.Before(*first) {
		*first = observedAt
	}
	if last.IsZero() || observedAt.After(*last) {
		*last = observedAt
	}
}

func fingerprintObservationUserGroupKey(entry FingerprintObservationEntry) fingerprintObservationSnapshotUserGroupKey {
	key := fingerprintObservationSnapshotUserGroupKey{id: entry.UserID}
	if entry.UserID != 0 {
		return key
	}
	// Context-less observations should not combine unrelated callers merely
	// because both are missing a database ID. Names are diagnostic snapshots,
	// so normalize case/space and follow the legacy UI's email-first fallback.
	if email := strings.ToLower(strings.TrimSpace(entry.Email)); email != "" {
		key.fallback = "email:" + email
	} else if username := strings.ToLower(strings.TrimSpace(entry.Username)); username != "" {
		key.fallback = "username:" + username
	} else {
		key.fallback = "unknown"
	}
	return key
}

func fingerprintObservationAPIKeyGroupKey(entry FingerprintObservationEntry) fingerprintObservationSnapshotAPIKeyGroupKey {
	key := fingerprintObservationSnapshotAPIKeyGroupKey{id: entry.APIKeyID}
	if entry.APIKeyID == 0 {
		key.fallback = strings.ToLower(strings.TrimSpace(entry.APIKeyName))
	}
	return key
}

func (s *fingerprintObservationSnapshot) nodeID(kind string, parts ...string) string {
	mac := hmac.New(sha256.New, s.cursorKey[:])
	_, _ = mac.Write([]byte("fingerprint-observation/node/v1\x00"))
	_, _ = mac.Write([]byte(kind))
	for _, part := range parts {
		_, _ = mac.Write([]byte{0})
		_, _ = mac.Write([]byte(part))
	}
	digest := mac.Sum(nil)
	return kind + "_" + base64.RawURLEncoding.EncodeToString(digest[:15])
}

func (s *fingerprintObservationSnapshot) pageBounds(scope, nodeID, cursor string, limit, total int) (int, int, error) {
	limit = normalizeFingerprintObservationLimit(limit)
	if cursor == "" {
		return 0, limit, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil || len(raw) != 17 || raw[0] != 1 {
		return 0, 0, ErrFingerprintObservationCursorInvalid
	}
	payload := raw[:5]
	expectedMAC := s.cursorMAC(scope, nodeID, payload)
	if !hmac.Equal(raw[5:], expectedMAC) {
		return 0, 0, ErrFingerprintObservationCursorInvalid
	}
	offset := int(binary.BigEndian.Uint32(raw[1:5]))
	if offset < 0 || offset > total {
		return 0, 0, ErrFingerprintObservationCursorInvalid
	}
	return offset, limit, nil
}

func (s *fingerprintObservationSnapshot) nextCursor(scope, nodeID string, next, total int) string {
	if next >= total {
		return ""
	}
	var payload [5]byte
	payload[0] = 1
	binary.BigEndian.PutUint32(payload[1:], uint32(next))
	mac := s.cursorMAC(scope, nodeID, payload[:])
	raw := make([]byte, 0, len(payload)+len(mac))
	raw = append(raw, payload[:]...)
	raw = append(raw, mac...)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func (s *fingerprintObservationSnapshot) cursorMAC(scope, nodeID string, payload []byte) []byte {
	mac := hmac.New(sha256.New, s.cursorKey[:])
	_, _ = mac.Write([]byte("fingerprint-observation/cursor/v1\x00"))
	_, _ = mac.Write([]byte(scope))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(nodeID))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write(payload)
	return mac.Sum(nil)[:12]
}

func (s *fingerprintObservationSnapshot) scrub() {
	if s == nil {
		return
	}
	var zeroEntry FingerprintObservationEntry
	for i := range s.entries {
		s.entries[i] = zeroEntry
	}
	for _, user := range s.users {
		if user == nil {
			continue
		}
		for _, apiKey := range user.apiKeys {
			if apiKey == nil {
				continue
			}
			for _, session := range apiKey.sessions {
				if session == nil {
					continue
				}
				for _, thread := range session.threads {
					if thread == nil {
						continue
					}
					for i := range thread.entries {
						thread.entries[i] = zeroEntry
					}
					thread.entries = nil
					thread.summary = FingerprintObservationThreadSummary{}
				}
				session.threads = nil
				clear(session.byThread)
				session.byThread = nil
				session.summary = FingerprintObservationSessionSummary{}
			}
			apiKey.sessions = nil
			clear(apiKey.bySession)
			apiKey.bySession = nil
			apiKey.summary = FingerprintObservationAPIKeySummary{}
		}
		user.apiKeys = nil
		clear(user.byKey)
		user.byKey = nil
		user.summary = FingerprintObservationUserSummary{}
	}
	s.token = ""
	s.entries = nil
	s.users = nil
	clear(s.usersByID)
	s.usersByID = nil
	clear(s.apiKeysByID)
	s.apiKeysByID = nil
	clear(s.sessionsByID)
	s.sessionsByID = nil
	clear(s.threadsByID)
	s.threadsByID = nil
	clear(s.cursorKey[:])
}

func normalizeFingerprintObservationPage(page, pageSize int) (int, int) {
	if page < 1 {
		page = 1
	}
	return page, normalizeFingerprintObservationLimit(pageSize)
}

func normalizeFingerprintObservationLimit(limit int) int {
	if limit < 1 {
		return fingerprintObservationPageDefault
	}
	if limit > fingerprintObservationPageMaximum {
		return fingerprintObservationPageMaximum
	}
	return limit
}

// Kept package-local for focused tests and diagnostics without exposing the
// synthetic sentinel strings used only in HMAC inputs.
func fingerprintObservationSnapshotIsOpaque(token string) bool {
	if strings.TrimSpace(token) != token || token == "" {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	return err == nil && len(raw) == 24
}
