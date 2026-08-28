package service

import (
	"container/list"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	OpenAICodexWindowTTL             = 30 * 24 * time.Hour
	openAICodexWindowLocalMaxEntries = 64 * 1024
	openAICodexWindowStoreTimeout    = 500 * time.Millisecond
	openAICodexWindowKeyDomain       = "sub2api/openai-codex-window/v1/key"
	openAICodexCompactTurnDomain     = "sub2api/openai-codex-window/v2/compact"

	// Redis Lua numbers are IEEE-754 doubles. Keeping the persisted counter in
	// the exact integer range makes CAS comparisons deterministic.
	OpenAICodexWindowMaxNumber uint64 = 1<<53 - 1
)

var (
	ErrOpenAICodexWindowStoreUnavailable      = errors.New("openai Codex window store is unavailable")
	ErrOpenAICodexWindowStoredInvalid         = errors.New("stored OpenAI Codex window snapshot is invalid")
	ErrOpenAICodexWindowLegacyRequiresResolve = errors.New("legacy OpenAI Codex window snapshot requires resolve")
)

// OpenAICodexWindowSnapshot is the durable state for one resolved Codex
// thread. LastCompactDigest is an HMAC used only for idempotency and is never
// projected onto an upstream request.
type OpenAICodexWindowSnapshot struct {
	ThreadID          string `json:"thread_id"`
	Number            uint64 `json:"window_number"`
	ContextWindowID   string `json:"context_window_id"`
	LastCompactDigest string `json:"last_compact_digest"`
}

func (s OpenAICodexWindowSnapshot) WindowID() string {
	if ValidateOpenAICodexWindowSnapshot(s) != nil {
		return ""
	}
	return s.ThreadID + ":" + strconv.FormatUint(s.Number, 10)
}

func ValidateOpenAICodexWindowSnapshot(snapshot OpenAICodexWindowSnapshot) error {
	if _, err := canonicalUUIDv7(snapshot.ThreadID); err != nil {
		return errors.New("openai Codex window thread_id must be UUIDv7")
	}
	if snapshot.Number > OpenAICodexWindowMaxNumber {
		return errors.New("openai Codex window number is outside the exact CAS range")
	}
	if _, err := canonicalUUIDv7(snapshot.ContextWindowID); err != nil {
		return errors.New("openai Codex window context_window_id must be UUIDv7")
	}
	digest := strings.TrimSpace(snapshot.LastCompactDigest)
	if digest != snapshot.LastCompactDigest || (digest != "" && !validOpenAICodexWindowDigest(digest)) {
		return errors.New("openai Codex window compact digest must be a lowercase SHA-256 digest")
	}
	if snapshot.Number == 0 && digest != "" {
		return errors.New("openai Codex initial window cannot have a compact digest")
	}
	if snapshot.Number > 0 && digest == "" {
		return errors.New("openai Codex advanced window must have a compact digest")
	}
	return nil
}

type OpenAICodexWindowCommitStatus string

const (
	OpenAICodexWindowCommitAdvanced         OpenAICodexWindowCommitStatus = "advanced"
	OpenAICodexWindowCommitAlreadyCommitted OpenAICodexWindowCommitStatus = "already_committed"
	OpenAICodexWindowCommitStale            OpenAICodexWindowCommitStatus = "stale"
)

type OpenAICodexWindowCommitResult struct {
	Snapshot OpenAICodexWindowSnapshot
	Status   OpenAICodexWindowCommitStatus
}

// OpenAICodexWindowStore is deliberately narrower than GatewayCache so cache
// test doubles do not need to implement Codex-specific persistence.
type OpenAICodexWindowStore interface {
	ResolveOpenAICodexWindow(ctx context.Context, mappingKey string, candidate OpenAICodexWindowSnapshot, ttl time.Duration) (OpenAICodexWindowSnapshot, error)
	CommitOpenAICodexWindow(ctx context.Context, mappingKey string, expected OpenAICodexWindowSnapshot, compactDigest, proposedNextContextWindowID string, ttl time.Duration) (OpenAICodexWindowCommitResult, error)
}

// OpenAICodexWindowMappingKey isolates a window by credential owner, downstream
// API key and the already-resolved UUIDv7 thread without retaining those raw
// values in Redis.
func OpenAICodexWindowMappingKey(secret, credentialOwnerNamespace string, apiKeyID int64, threadID string) (string, error) {
	if apiKeyID < 0 {
		return "", errOpenAIOutboundSessionIdentityKeyEmpty
	}
	credentialOwnerNamespace = sanitizeSessionID(credentialOwnerNamespace)
	threadID, err := canonicalUUIDv7(threadID)
	if err != nil {
		return "", errOpenAIOutboundSessionIdentityKeyEmpty
	}
	return openAICodexHMAC(secret, openAICodexWindowKeyDomain,
		credentialOwnerNamespace, strconv.FormatInt(apiKeyID, 10), threadID)
}

// OpenAICodexCompactTurnDigest is the idempotency token for one successful
// compact installation. The full immutable pre-window identity is part of the
// domain so a compact result cannot be installed into a same-number context
// from another lineage.
func OpenAICodexCompactTurnDigest(secret, credentialOwnerNamespace string, apiKeyID int64, expected OpenAICodexWindowSnapshot, compactTurnID string) (string, error) {
	if apiKeyID < 0 || expected.Number >= OpenAICodexWindowMaxNumber || ValidateOpenAICodexWindowSnapshot(expected) != nil {
		return "", errOpenAIOutboundSessionIdentityKeyEmpty
	}
	credentialOwnerNamespace = sanitizeSessionID(credentialOwnerNamespace)
	compactTurnID = sanitizeSessionID(compactTurnID)
	return openAICodexHMAC(secret, openAICodexCompactTurnDomain,
		credentialOwnerNamespace, strconv.FormatInt(apiKeyID, 10), expected.ThreadID,
		strconv.FormatUint(expected.Number, 10), expected.ContextWindowID, compactTurnID)
}

func validOpenAICodexWindowDigest(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validOpenAICodexWindowMappingKey(value string) bool {
	return validOpenAICodexWindowDigest(strings.TrimSpace(value)) && strings.TrimSpace(value) == value
}

func normalizeOpenAICodexWindowTTL(ttl time.Duration) time.Duration {
	if ttl <= 0 {
		return OpenAICodexWindowTTL
	}
	return ttl
}

func validateOpenAICodexWindowCommit(mappingKey string, expected OpenAICodexWindowSnapshot, compactDigest, proposedNextContextWindowID string) (OpenAICodexWindowSnapshot, string, error) {
	if !validOpenAICodexWindowMappingKey(mappingKey) {
		return OpenAICodexWindowSnapshot{}, "", errors.New("openai Codex window mapping key must be a lowercase SHA-256 digest")
	}
	if err := ValidateOpenAICodexWindowSnapshot(expected); err != nil {
		return OpenAICodexWindowSnapshot{}, "", fmt.Errorf("openai Codex expected window is invalid: %w", err)
	}
	if expected.Number >= OpenAICodexWindowMaxNumber {
		return OpenAICodexWindowSnapshot{}, "", errors.New("openai Codex window counter is exhausted")
	}
	if !validOpenAICodexWindowDigest(compactDigest) {
		return OpenAICodexWindowSnapshot{}, "", errors.New("openai Codex compact digest must be a lowercase SHA-256 digest")
	}
	canonicalContextWindowID, err := canonicalUUIDv7(proposedNextContextWindowID)
	if err != nil {
		return OpenAICodexWindowSnapshot{}, "", errors.New("openai Codex proposed context_window_id must be UUIDv7")
	}
	if canonicalContextWindowID == expected.ContextWindowID {
		return OpenAICodexWindowSnapshot{}, "", errors.New("openai Codex proposed context_window_id must differ from the expected context window")
	}
	return expected, canonicalContextWindowID, nil
}

type openAICodexWindowLocalEntry struct {
	snapshot         OpenAICodexWindowSnapshot
	expiresAt        time.Time
	pendingPromotion bool
	recency          *list.Element
}

type openAICodexWindowLocalStore struct {
	mu         sync.Mutex
	entries    map[string]*openAICodexWindowLocalEntry
	recency    *list.List
	maxEntries int
}

func newOpenAICodexWindowLocalStore(maxEntries int) *openAICodexWindowLocalStore {
	if maxEntries <= 0 {
		maxEntries = openAICodexWindowLocalMaxEntries
	}
	return &openAICodexWindowLocalStore{
		entries:    make(map[string]*openAICodexWindowLocalEntry, 256),
		recency:    list.New(),
		maxEntries: maxEntries,
	}
}

func (s *openAICodexWindowLocalStore) ResolveOpenAICodexWindow(_ context.Context, mappingKey string, candidate OpenAICodexWindowSnapshot, ttl time.Duration) (OpenAICodexWindowSnapshot, error) {
	if !validOpenAICodexWindowMappingKey(mappingKey) {
		return OpenAICodexWindowSnapshot{}, errors.New("openai Codex window mapping key must be a lowercase SHA-256 digest")
	}
	if err := ValidateOpenAICodexWindowSnapshot(candidate); err != nil {
		return OpenAICodexWindowSnapshot{}, err
	}
	ttl = normalizeOpenAICodexWindowTTL(ttl)
	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.liveEntryLocked(mappingKey, now)
	if entry == nil {
		entry = s.insertLocked(mappingKey, candidate, now.Add(ttl), false)
	} else {
		if entry.snapshot.ThreadID != candidate.ThreadID {
			return OpenAICodexWindowSnapshot{}, ErrOpenAICodexWindowStoredInvalid
		}
		if candidate.Number > entry.snapshot.Number {
			entry.snapshot = candidate
		}
		entry.expiresAt = now.Add(ttl)
		s.recency.MoveToFront(entry.recency)
	}
	return entry.snapshot, nil
}

func (s *openAICodexWindowLocalStore) CommitOpenAICodexWindow(_ context.Context, mappingKey string, expected OpenAICodexWindowSnapshot, compactDigest, proposedNextContextWindowID string, ttl time.Duration) (OpenAICodexWindowCommitResult, error) {
	expected, proposedNextContextWindowID, err := validateOpenAICodexWindowCommit(mappingKey, expected, compactDigest, proposedNextContextWindowID)
	if err != nil {
		return OpenAICodexWindowCommitResult{}, err
	}
	ttl = normalizeOpenAICodexWindowTTL(ttl)
	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.liveEntryLocked(mappingKey, now)
	if entry == nil {
		// A missing primary/local entry after an outage is promoted from the
		// exact snapshot used to build this request and advanced in one critical
		// section. No invalid intermediate generation is exposed.
		snapshot := OpenAICodexWindowSnapshot{
			ThreadID:          expected.ThreadID,
			Number:            expected.Number + 1,
			ContextWindowID:   proposedNextContextWindowID,
			LastCompactDigest: compactDigest,
		}
		entry = s.insertLocked(mappingKey, snapshot, now.Add(ttl), false)
		return OpenAICodexWindowCommitResult{Snapshot: entry.snapshot, Status: OpenAICodexWindowCommitAdvanced}, nil
	}
	if entry.snapshot.ThreadID != expected.ThreadID {
		return OpenAICodexWindowCommitResult{}, ErrOpenAICodexWindowStoredInvalid
	}
	if ValidateOpenAICodexWindowSnapshot(entry.snapshot) != nil {
		return OpenAICodexWindowCommitResult{}, ErrOpenAICodexWindowStoredInvalid
	}
	if entry.snapshot.LastCompactDigest == compactDigest {
		if entry.snapshot.Number != expected.Number+1 || entry.snapshot.ContextWindowID == expected.ContextWindowID {
			return OpenAICodexWindowCommitResult{}, ErrOpenAICodexWindowStoredInvalid
		}
		entry.expiresAt = now.Add(ttl)
		s.recency.MoveToFront(entry.recency)
		return OpenAICodexWindowCommitResult{Snapshot: entry.snapshot, Status: OpenAICodexWindowCommitAlreadyCommitted}, nil
	}
	if entry.snapshot.Number != expected.Number || entry.snapshot.ContextWindowID != expected.ContextWindowID {
		entry.expiresAt = now.Add(ttl)
		s.recency.MoveToFront(entry.recency)
		return OpenAICodexWindowCommitResult{Snapshot: entry.snapshot, Status: OpenAICodexWindowCommitStale}, nil
	}
	entry.snapshot.Number = expected.Number + 1
	entry.snapshot.ContextWindowID = proposedNextContextWindowID
	entry.snapshot.LastCompactDigest = compactDigest
	entry.expiresAt = now.Add(ttl)
	s.recency.MoveToFront(entry.recency)
	return OpenAICodexWindowCommitResult{Snapshot: entry.snapshot, Status: OpenAICodexWindowCommitAdvanced}, nil
}

func (s *openAICodexWindowLocalStore) liveEntryLocked(mappingKey string, now time.Time) *openAICodexWindowLocalEntry {
	entry := s.entries[mappingKey]
	if entry != nil && !entry.expiresAt.After(now) {
		s.removeLocked(mappingKey, entry)
		return nil
	}
	return entry
}

func (s *openAICodexWindowLocalStore) insertLocked(mappingKey string, snapshot OpenAICodexWindowSnapshot, expiresAt time.Time, pending bool) *openAICodexWindowLocalEntry {
	for len(s.entries) >= s.maxEntries {
		oldest := s.recency.Back()
		if oldest == nil {
			break
		}
		key, _ := oldest.Value.(string)
		s.removeLocked(key, s.entries[key])
	}
	element := s.recency.PushFront(mappingKey)
	entry := &openAICodexWindowLocalEntry{snapshot: snapshot, expiresAt: expiresAt, pendingPromotion: pending, recency: element}
	s.entries[mappingKey] = entry
	return entry
}

func (s *openAICodexWindowLocalStore) removeLocked(mappingKey string, entry *openAICodexWindowLocalEntry) {
	delete(s.entries, mappingKey)
	if entry != nil && entry.recency != nil {
		s.recency.Remove(entry.recency)
	}
}

func (s *openAICodexWindowLocalStore) markPending(mappingKey string, ttl time.Duration) {
	s.mu.Lock()
	if entry := s.liveEntryLocked(mappingKey, time.Now()); entry != nil {
		entry.pendingPromotion = true
		entry.expiresAt = time.Now().Add(normalizeOpenAICodexWindowTTL(ttl))
		s.recency.MoveToFront(entry.recency)
	}
	s.mu.Unlock()
}

func (s *openAICodexWindowLocalStore) current(mappingKey string) (OpenAICodexWindowSnapshot, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.liveEntryLocked(mappingKey, time.Now())
	if entry == nil {
		return OpenAICodexWindowSnapshot{}, false
	}
	s.recency.MoveToFront(entry.recency)
	return entry.snapshot, true
}

// acceptPrimary mirrors a successful primary result without ever regressing a
// newer local fallback result that raced with it. Equal generations use the
// primary digest as the deterministic convergence winner.
func (s *openAICodexWindowLocalStore) acceptPrimary(mappingKey string, snapshot OpenAICodexWindowSnapshot, ttl time.Duration) OpenAICodexWindowSnapshot {
	ttl = normalizeOpenAICodexWindowTTL(ttl)
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.liveEntryLocked(mappingKey, now)
	if entry == nil {
		entry = s.insertLocked(mappingKey, snapshot, now.Add(ttl), false)
		return entry.snapshot
	}
	if entry.snapshot.ThreadID != snapshot.ThreadID {
		entry.pendingPromotion = true
		return entry.snapshot
	}
	if snapshot.Number >= entry.snapshot.Number {
		entry.snapshot = snapshot
		entry.pendingPromotion = false
	} else {
		entry.pendingPromotion = true
	}
	entry.expiresAt = now.Add(ttl)
	s.recency.MoveToFront(entry.recency)
	return entry.snapshot
}

var processOpenAICodexWindowLocalStore = newOpenAICodexWindowLocalStore(openAICodexWindowLocalMaxEntries)

// OpenAICodexWindowRuntimeStore provides Redis-first persistence with a bounded
// process-local fallback. Resolve promotes a pending local winner when Redis
// recovers, so a process never moves its window number backwards.
type OpenAICodexWindowRuntimeStore struct {
	primary OpenAICodexWindowStore
	local   *openAICodexWindowLocalStore
}

func NewOpenAICodexWindowRuntimeStore(primary OpenAICodexWindowStore) *OpenAICodexWindowRuntimeStore {
	return &OpenAICodexWindowRuntimeStore{primary: primary, local: processOpenAICodexWindowLocalStore}
}

func newOpenAICodexWindowRuntimeStoreWithLocal(primary OpenAICodexWindowStore, local *openAICodexWindowLocalStore) *OpenAICodexWindowRuntimeStore {
	if local == nil {
		local = newOpenAICodexWindowLocalStore(openAICodexWindowLocalMaxEntries)
	}
	return &OpenAICodexWindowRuntimeStore{primary: primary, local: local}
}

func (s *OpenAICodexWindowRuntimeStore) ResolveOpenAICodexWindow(ctx context.Context, mappingKey string, candidate OpenAICodexWindowSnapshot, ttl time.Duration) (OpenAICodexWindowSnapshot, error) {
	if s == nil || s.local == nil {
		return OpenAICodexWindowSnapshot{}, ErrOpenAICodexWindowStoreUnavailable
	}
	ttl = normalizeOpenAICodexWindowTTL(ttl)
	localWinner, err := s.local.ResolveOpenAICodexWindow(ctx, mappingKey, candidate, ttl)
	if err != nil {
		return OpenAICodexWindowSnapshot{}, err
	}
	if s.primary == nil {
		s.local.markPending(mappingKey, ttl)
		return localWinner, nil
	}
	primaryCtx, cancel := context.WithTimeout(ctx, openAICodexWindowStoreTimeout)
	primaryWinner, err := s.primary.ResolveOpenAICodexWindow(primaryCtx, mappingKey, localWinner, ttl)
	cancel()
	if err != nil {
		if !errors.Is(err, ErrOpenAICodexWindowStoreUnavailable) {
			return OpenAICodexWindowSnapshot{}, err
		}
		s.local.markPending(mappingKey, ttl)
		return localWinner, nil
	}
	if ValidateOpenAICodexWindowSnapshot(primaryWinner) != nil || primaryWinner.ThreadID != localWinner.ThreadID || primaryWinner.Number < localWinner.Number {
		return OpenAICodexWindowSnapshot{}, ErrOpenAICodexWindowStoredInvalid
	}
	return s.local.acceptPrimary(mappingKey, primaryWinner, ttl), nil
}

func (s *OpenAICodexWindowRuntimeStore) CommitOpenAICodexWindow(ctx context.Context, mappingKey string, expected OpenAICodexWindowSnapshot, compactDigest, proposedNextContextWindowID string, ttl time.Duration) (OpenAICodexWindowCommitResult, error) {
	if s == nil || s.local == nil {
		return OpenAICodexWindowCommitResult{}, ErrOpenAICodexWindowStoreUnavailable
	}
	expected, proposedNextContextWindowID, err := validateOpenAICodexWindowCommit(mappingKey, expected, compactDigest, proposedNextContextWindowID)
	if err != nil {
		return OpenAICodexWindowCommitResult{}, err
	}
	ttl = normalizeOpenAICodexWindowTTL(ttl)
	localCandidate, hasLocalCandidate := s.local.current(mappingKey)
	if s.primary != nil {
		primaryReady := true
		if hasLocalCandidate {
			primaryCtx, cancel := context.WithTimeout(ctx, openAICodexWindowStoreTimeout)
			primaryWinner, resolveErr := s.primary.ResolveOpenAICodexWindow(primaryCtx, mappingKey, localCandidate, ttl)
			cancel()
			if resolveErr != nil {
				if !errors.Is(resolveErr, ErrOpenAICodexWindowStoreUnavailable) {
					return OpenAICodexWindowCommitResult{}, resolveErr
				}
				primaryReady = false
			} else if ValidateOpenAICodexWindowSnapshot(primaryWinner) != nil || primaryWinner.ThreadID != expected.ThreadID || primaryWinner.Number < localCandidate.Number {
				return OpenAICodexWindowCommitResult{}, ErrOpenAICodexWindowStoredInvalid
			} else {
				s.local.acceptPrimary(mappingKey, primaryWinner, ttl)
			}
		}
		if primaryReady {
			primaryCtx, cancel := context.WithTimeout(ctx, openAICodexWindowStoreTimeout)
			result, commitErr := s.primary.CommitOpenAICodexWindow(primaryCtx, mappingKey, expected, compactDigest, proposedNextContextWindowID, ttl)
			cancel()
			if commitErr != nil {
				if !errors.Is(commitErr, ErrOpenAICodexWindowStoreUnavailable) {
					return OpenAICodexWindowCommitResult{}, commitErr
				}
			} else if validateOpenAICodexWindowCommitResult(expected, compactDigest, proposedNextContextWindowID, result) != nil {
				return OpenAICodexWindowCommitResult{}, ErrOpenAICodexWindowStoredInvalid
			} else {
				s.local.acceptPrimary(mappingKey, result.Snapshot, ttl)
				return result, nil
			}
		}
	}

	result, err := s.local.CommitOpenAICodexWindow(ctx, mappingKey, expected, compactDigest, proposedNextContextWindowID, ttl)
	if err == nil {
		s.local.markPending(mappingKey, ttl)
	}
	return result, err
}

func validateOpenAICodexWindowCommitResult(expected OpenAICodexWindowSnapshot, compactDigest, proposedNextContextWindowID string, result OpenAICodexWindowCommitResult) error {
	if ValidateOpenAICodexWindowSnapshot(result.Snapshot) != nil || result.Snapshot.ThreadID != expected.ThreadID {
		return ErrOpenAICodexWindowStoredInvalid
	}
	switch result.Status {
	case OpenAICodexWindowCommitAdvanced:
		if result.Snapshot.Number != expected.Number+1 || result.Snapshot.ContextWindowID != proposedNextContextWindowID || result.Snapshot.LastCompactDigest != compactDigest {
			return ErrOpenAICodexWindowStoredInvalid
		}
	case OpenAICodexWindowCommitAlreadyCommitted:
		if result.Snapshot.Number != expected.Number+1 || result.Snapshot.ContextWindowID == expected.ContextWindowID || result.Snapshot.LastCompactDigest != compactDigest {
			return ErrOpenAICodexWindowStoredInvalid
		}
	case OpenAICodexWindowCommitStale:
		if result.Snapshot.LastCompactDigest == compactDigest ||
			(result.Snapshot.Number == expected.Number && result.Snapshot.ContextWindowID == expected.ContextWindowID) {
			return ErrOpenAICodexWindowStoredInvalid
		}
	default:
		return ErrOpenAICodexWindowStoredInvalid
	}
	return nil
}

func (s *OpenAIGatewayService) openAICodexWindowRuntimeStore() *OpenAICodexWindowRuntimeStore {
	var primary OpenAICodexWindowStore
	if s != nil && s.cache != nil {
		primary, _ = s.cache.(OpenAICodexWindowStore)
	}
	return NewOpenAICodexWindowRuntimeStore(primary)
}

func (s *OpenAIGatewayService) ResolveOpenAICodexWindowSnapshot(ctx context.Context, mappingKey, threadID, contextWindowIDCandidate string) (OpenAICodexWindowSnapshot, error) {
	threadID, err := canonicalUUIDv7(threadID)
	if err != nil {
		return OpenAICodexWindowSnapshot{}, fmt.Errorf("resolve openai Codex window: %w", err)
	}
	contextWindowIDCandidate, err = canonicalUUIDv7(contextWindowIDCandidate)
	if err != nil {
		return OpenAICodexWindowSnapshot{}, fmt.Errorf("resolve openai Codex window: %w", err)
	}
	return s.openAICodexWindowRuntimeStore().ResolveOpenAICodexWindow(ctx, mappingKey, OpenAICodexWindowSnapshot{
		ThreadID:        threadID,
		ContextWindowID: contextWindowIDCandidate,
	}, OpenAICodexWindowTTL)
}

func (s *OpenAIGatewayService) CommitOpenAICodexWindowSnapshot(ctx context.Context, mappingKey string, expected OpenAICodexWindowSnapshot, compactDigest string) (OpenAICodexWindowCommitResult, error) {
	proposedNextContextWindowID, err := newOpenAICodexContextWindowID()
	if err != nil {
		return OpenAICodexWindowCommitResult{}, fmt.Errorf("commit openai Codex window: %w", err)
	}
	return s.openAICodexWindowRuntimeStore().CommitOpenAICodexWindow(ctx, mappingKey, expected, compactDigest, proposedNextContextWindowID, OpenAICodexWindowTTL)
}

func newOpenAICodexContextWindowID() (string, error) {
	generated, err := uuid.NewV7()
	if err != nil {
		return "", errors.New("generate openai Codex context_window_id")
	}
	return generated.String(), nil
}

var _ OpenAICodexWindowStore = (*openAICodexWindowLocalStore)(nil)
var _ OpenAICodexWindowStore = (*OpenAICodexWindowRuntimeStore)(nil)
