package service

import (
	"container/heap"
	"container/list"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// OpenAIOutboundSessionIdentity is the server-owned pair of identifiers sent
// on OpenAI/Codex outbound requests.  The pair is generated together and is
// kept stable for a logical client mapping until the store entry expires.
type OpenAIOutboundSessionIdentity struct {
	SessionID string `json:"session_id"`
	ThreadID  string `json:"thread_id"`
}

// ValidateOpenAIOutboundSessionIdentity enforces the wire contract for
// server-owned identities.  Both values must be RFC 4122 UUIDv7 values and a
// pair may not reuse the same UUID.
func ValidateOpenAIOutboundSessionIdentity(identity OpenAIOutboundSessionIdentity) error {
	sessionRaw := identity.SessionID
	sessionID, err := uuid.Parse(sessionRaw)
	if err != nil || sessionRaw != sessionID.String() || sessionID.Version() != uuid.Version(7) || sessionID.Variant() != uuid.RFC4122 {
		return errors.New("openai outbound session identity session_id must be UUIDv7")
	}
	threadRaw := identity.ThreadID
	threadID, err := uuid.Parse(threadRaw)
	if err != nil || threadRaw != threadID.String() || threadID.Version() != uuid.Version(7) || threadID.Variant() != uuid.RFC4122 {
		return errors.New("openai outbound session identity thread_id must be UUIDv7")
	}
	if sessionID == threadID {
		return errors.New("openai outbound session identity pair must contain distinct UUIDs")
	}
	return nil
}

// OpenAIOutboundSessionIdentityStore atomically reads or creates a pair for a
// mapping key.  Implementations must return the existing value when another
// writer won the race, and should refresh the entry TTL on every successful
// read/create.
type OpenAIOutboundSessionIdentityStore interface {
	GetOrCreate(ctx context.Context, mappingKey string, candidate OpenAIOutboundSessionIdentity, ttl time.Duration) (OpenAIOutboundSessionIdentity, error)
}

const (
	// OpenAIOutboundSessionIdentityTTL is deliberately longer than normal
	// request/session caches.  It mirrors a client installation lifetime while
	// allowing inactive identities to be reclaimed.
	OpenAIOutboundSessionIdentityTTL = 30 * 24 * time.Hour

	openAIOutboundSessionIdentityDomain               = "sub2api/openai-outbound-session/v1"
	openAIOutboundSessionIdentityLocalStoreMaxEntries = 64 * 1024
)

const (
	OpenAIOutboundSessionLogicalKeySourceNone               = "none"
	OpenAIOutboundSessionLogicalKeySourceHeaderSession      = "header_session"
	OpenAIOutboundSessionLogicalKeySourceHeaderThread       = "header_thread"
	OpenAIOutboundSessionLogicalKeySourceHeaderConversation = "header_conversation"
	OpenAIOutboundSessionLogicalKeySourceHeaderAffinity     = "header_affinity"
	OpenAIOutboundSessionLogicalKeySourceClientMetadata     = "client_metadata"
	OpenAIOutboundSessionLogicalKeySourceTurnMetadata       = "x_codex_turn_metadata"
	OpenAIOutboundSessionLogicalKeySourceCallerSeed         = "caller_seed"
	OpenAIOutboundSessionLogicalKeySourcePromptCacheKey     = "prompt_cache_key"
)

// OpenAIOutboundSessionLogicalKeyResolution describes which explicit,
// session-scoped signal won. Request/message/response/installation identifiers
// are intentionally absent from the resolver's allowlist.
type OpenAIOutboundSessionLogicalKeyResolution struct {
	LogicalKey string
	Source     string
}

var openAIOutboundSessionDirectHeaderGroups = []struct {
	source string
	names  []string
}{
	{source: OpenAIOutboundSessionLogicalKeySourceHeaderSession, names: []string{"session-id", "session_id"}},
	{source: OpenAIOutboundSessionLogicalKeySourceHeaderThread, names: []string{"thread-id", "thread_id"}},
	{source: OpenAIOutboundSessionLogicalKeySourceHeaderConversation, names: []string{"conversation-id", "conversation_id"}},
}

var openAIOutboundSessionAffinityHeaders = []string{
	openCodeSessionAffinityHeader,
	openCodeSessionIDHeader,
	openCodeNativeSessionHeader,
	codeBuddyConversationHeader,
}

var openAIOutboundSessionJSONFields = []string{
	"session_id",
	"session-id",
	"thread_id",
	"thread-id",
	"conversation_id",
	"conversation-id",
}

var (
	errOpenAIOutboundSessionIdentityKeySecret = errors.New("openai outbound session identity secret is unavailable")
	errOpenAIOutboundSessionIdentityKeyEmpty  = errors.New("openai outbound session identity mapping key is empty")
	// errOpenAIOutboundSessionIdentityNamespace marks a credential-owner
	// resolution failure. Transport callers may fail closed for malformed
	// shadow-account topology while still treating Redis/UUID failures as
	// fail-open request-path conditions.
	errOpenAIOutboundSessionIdentityNamespace = errors.New("openai outbound session identity namespace resolution failed")
	// ErrOpenAIOutboundSessionIdentityStoredValueInvalid lets the service
	// distinguish unrepaired Redis corruption from ordinary store outages.
	ErrOpenAIOutboundSessionIdentityStoredValueInvalid = errors.New("stored OpenAI outbound session identity is invalid")
)

// ResolveOpenAIOutboundSessionLogicalKey returns the first valid, explicitly
// session-scoped key in the documented priority order:
//
//  1. session/thread/conversation headers (hyphen and underscore aliases)
//  2. known affinity headers
//  3. client_metadata session/thread/conversation fields
//  4. JSON x-codex-turn-metadata fields (header, then client_metadata/body)
//  5. a caller-selected seed (for compact/alpha paths and already-resolved keys)
//  6. body prompt_cache_key when no caller seed was selected
//
// Every candidate is normalized with sanitizeSessionID: invalid UTF-8,
// control characters, empty strings, and values longer than 255 characters
// are skipped. Installation, request, message, and response identifiers are
// never inspected, even when they are the only IDs present.
func ResolveOpenAIOutboundSessionLogicalKey(c *gin.Context, body []byte, callerSeed string) string {
	return ResolveOpenAIOutboundSessionLogicalKeyWithSource(c, body, callerSeed).LogicalKey
}

// ResolveOpenAIOutboundSessionLogicalKeyWithSource is the diagnostic form of
// ResolveOpenAIOutboundSessionLogicalKey. Source is a bounded enum-like value
// and never contains the raw logical key.
func ResolveOpenAIOutboundSessionLogicalKeyWithSource(c *gin.Context, body []byte, callerSeed string) OpenAIOutboundSessionLogicalKeyResolution {
	return resolveOpenAIOutboundSessionLogicalKeyWithTurnMetadata(c, body, callerSeed, "")
}

// ResolveOpenAIOutboundSessionLogicalKeyWithTurnMetadata is the explicit-
// metadata variant used by WebSocket builders whose caller has already
// extracted x-codex-turn-metadata but has not yet copied it into the outbound
// request headers/body. The explicit value follows the normal turn-metadata
// signals and still precedes callerSeed/prompt_cache_key.
func ResolveOpenAIOutboundSessionLogicalKeyWithTurnMetadata(c *gin.Context, body []byte, callerSeed, turnMetadata string) string {
	return resolveOpenAIOutboundSessionLogicalKeyWithTurnMetadata(c, body, callerSeed, turnMetadata).LogicalKey
}

func resolveOpenAIOutboundSessionLogicalKeyWithTurnMetadata(c *gin.Context, body []byte, callerSeed, explicitTurnMetadata string) OpenAIOutboundSessionLogicalKeyResolution {
	if c != nil && c.Request != nil {
		headers := c.Request.Header
		for _, group := range openAIOutboundSessionDirectHeaderGroups {
			if value := firstValidOpenAIOutboundSessionHeader(headers, group.names); value != "" {
				return OpenAIOutboundSessionLogicalKeyResolution{LogicalKey: value, Source: group.source}
			}
		}
		if value := firstValidOpenAIOutboundSessionHeader(headers, openAIOutboundSessionAffinityHeaders); value != "" {
			return OpenAIOutboundSessionLogicalKeyResolution{LogicalKey: value, Source: OpenAIOutboundSessionLogicalKeySourceHeaderAffinity}
		}
	}

	metadata, turnMetadata, promptCacheKey := openAIOutboundSessionBodySignals(body)
	if value := firstValidOpenAIOutboundSessionJSONField(metadata); value != "" {
		return OpenAIOutboundSessionLogicalKeyResolution{LogicalKey: value, Source: OpenAIOutboundSessionLogicalKeySourceClientMetadata}
	}

	if c != nil && c.Request != nil {
		for _, raw := range c.Request.Header.Values(openAIWSTurnMetadataHeader) {
			if value := openAIOutboundSessionTurnMetadataKey([]byte(raw)); value != "" {
				return OpenAIOutboundSessionLogicalKeyResolution{LogicalKey: value, Source: OpenAIOutboundSessionLogicalKeySourceTurnMetadata}
			}
		}
	}
	if value := openAIOutboundSessionTurnMetadataKey([]byte(strings.TrimSpace(explicitTurnMetadata))); value != "" {
		return OpenAIOutboundSessionLogicalKeyResolution{LogicalKey: value, Source: OpenAIOutboundSessionLogicalKeySourceTurnMetadata}
	}
	for _, raw := range turnMetadata {
		if value := openAIOutboundSessionTurnMetadataKey(raw); value != "" {
			return OpenAIOutboundSessionLogicalKeyResolution{LogicalKey: value, Source: OpenAIOutboundSessionLogicalKeySourceTurnMetadata}
		}
	}

	// The caller seed precedes the raw body prompt key intentionally. Compact
	// and alpha paths select request-local seeds before body normalization; a
	// stale ordinary prompt_cache_key must not overwrite that path decision.
	if value := sanitizeSessionID(callerSeed); value != "" {
		return OpenAIOutboundSessionLogicalKeyResolution{LogicalKey: value, Source: OpenAIOutboundSessionLogicalKeySourceCallerSeed}
	}
	if value := sanitizeSessionID(promptCacheKey); value != "" {
		return OpenAIOutboundSessionLogicalKeyResolution{LogicalKey: value, Source: OpenAIOutboundSessionLogicalKeySourcePromptCacheKey}
	}
	return OpenAIOutboundSessionLogicalKeyResolution{Source: OpenAIOutboundSessionLogicalKeySourceNone}
}

func resolveOpenAIOutboundSessionLogicalKey(c *gin.Context, body []byte, callerSeed string) string {
	return ResolveOpenAIOutboundSessionLogicalKey(c, body, callerSeed)
}

func firstValidOpenAIOutboundSessionHeader(headers http.Header, names []string) string {
	if headers == nil {
		return ""
	}
	for _, name := range names {
		// Normal HTTP requests use canonical map keys and are served by
		// Header.Values. Bridges/tests can construct Header maps manually with
		// non-canonical casing, so inspect those keys as a fallback as well.
		values := headers.Values(name)
		for _, raw := range values {
			if value := sanitizeSessionID(raw); value != "" {
				return value
			}
		}
		for key, nonCanonicalValues := range headers {
			if !strings.EqualFold(key, name) || key == http.CanonicalHeaderKey(name) {
				continue
			}
			for _, raw := range nonCanonicalValues {
				if value := sanitizeSessionID(raw); value != "" {
					return value
				}
			}
		}
	}
	return ""
}

// openAIOutboundSessionBodySignals parses only the small object maps needed by
// identity resolution. Unknown IDs remain opaque and cannot become a key.
func openAIOutboundSessionBodySignals(body []byte) (metadata map[string]json.RawMessage, turnMetadata [][]byte, promptCacheKey string) {
	if len(body) == 0 || !utf8.Valid(body) {
		return nil, nil, ""
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil || root == nil {
		return nil, nil, ""
	}
	if raw, ok := root["prompt_cache_key"]; ok {
		_ = json.Unmarshal(raw, &promptCacheKey)
	}
	if raw, ok := root[openAIWSTurnMetadataHeader]; ok {
		if decoded := normalizeOpenAIOutboundTurnMetadataRaw(raw); len(decoded) > 0 {
			turnMetadata = append(turnMetadata, decoded)
		}
	}
	rawMetadata, ok := root["client_metadata"]
	if !ok || json.Unmarshal(rawMetadata, &metadata) != nil || metadata == nil {
		return nil, turnMetadata, promptCacheKey
	}
	if raw, ok := metadata[openAIWSTurnMetadataHeader]; ok {
		if decoded := normalizeOpenAIOutboundTurnMetadataRaw(raw); len(decoded) > 0 {
			turnMetadata = append(turnMetadata, decoded)
		}
	}
	return metadata, turnMetadata, promptCacheKey
}

func normalizeOpenAIOutboundTurnMetadataRaw(raw json.RawMessage) []byte {
	if len(raw) == 0 || !utf8.Valid(raw) {
		return nil
	}
	var encoded string
	if json.Unmarshal(raw, &encoded) == nil {
		return []byte(strings.TrimSpace(encoded))
	}
	trimmed := strings.TrimSpace(string(raw))
	if strings.HasPrefix(trimmed, "{") {
		return []byte(trimmed)
	}
	return nil
}

func openAIOutboundSessionTurnMetadataKey(raw []byte) string {
	if len(raw) == 0 || !utf8.Valid(raw) {
		return ""
	}
	var metadata map[string]json.RawMessage
	if json.Unmarshal(raw, &metadata) != nil || metadata == nil {
		return ""
	}
	return firstValidOpenAIOutboundSessionJSONField(metadata)
}

func firstValidOpenAIOutboundSessionJSONField(object map[string]json.RawMessage) string {
	for _, field := range openAIOutboundSessionJSONFields {
		raw, ok := object[field]
		if !ok {
			continue
		}
		var value string
		if json.Unmarshal(raw, &value) != nil {
			continue
		}
		if value = sanitizeSessionID(value); value != "" {
			return value
		}
	}
	return ""
}

// openAIOutboundSessionIdentityLocalStore is intentionally process-local.  It
// is used when Redis is unavailable (or when a deployment has no Redis cache)
// so identity handling remains fail-open for the request path.
type openAIOutboundSessionIdentityLocalStore struct {
	mu          sync.Mutex
	entries     map[string]*openAIOutboundSessionIdentityLocalEntry
	recency     *list.List
	expirations openAIOutboundSessionIdentityExpiryHeap
	maxEntries  int
}

type openAIOutboundSessionIdentityLocalEntry struct {
	mappingKey       string
	identity         OpenAIOutboundSessionIdentity
	expires          time.Time
	pendingPromotion bool
	recencyElement   *list.Element
	expiryIndex      int
}

type openAIOutboundSessionIdentityExpiryHeap []*openAIOutboundSessionIdentityLocalEntry

func (h openAIOutboundSessionIdentityExpiryHeap) Len() int { return len(h) }

func (h openAIOutboundSessionIdentityExpiryHeap) Less(i, j int) bool {
	if h[i].expires.Equal(h[j].expires) {
		return h[i].mappingKey < h[j].mappingKey
	}
	return h[i].expires.Before(h[j].expires)
}

func (h openAIOutboundSessionIdentityExpiryHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].expiryIndex = i
	h[j].expiryIndex = j
}

func (h *openAIOutboundSessionIdentityExpiryHeap) Push(value any) {
	entry := value.(*openAIOutboundSessionIdentityLocalEntry)
	entry.expiryIndex = len(*h)
	*h = append(*h, entry)
}

func (h *openAIOutboundSessionIdentityExpiryHeap) Pop() any {
	old := *h
	last := len(old) - 1
	entry := old[last]
	old[last] = nil
	entry.expiryIndex = -1
	*h = old[:last]
	return entry
}

// OpenAIOutboundSessionIdentityRuntimeMetrics is a process snapshot suitable
// for the existing ops/runtime metric collectors. It carries counters and
// aggregate latency only; no logical key or identity value is retained.
type OpenAIOutboundSessionIdentityRuntimeMetrics struct {
	ResolveTotal             int64
	EmptyLogicalKeyTotal     int64
	PrimaryStoreSuccessTotal int64
	PrimaryStoreFailureTotal int64
	PrimaryStoreInvalidTotal int64
	LocalFallbackTotal       int64
	PromotionTotal           int64
	StoreLatencySamples      int64
	StoreLatencyTotalMicros  int64
	StoreLatencyMaxMicros    int64
}

type openAIOutboundSessionIdentityRuntimeMetricsStore struct {
	resolveTotal             atomic.Int64
	emptyLogicalKeyTotal     atomic.Int64
	primaryStoreSuccessTotal atomic.Int64
	primaryStoreFailureTotal atomic.Int64
	primaryStoreInvalidTotal atomic.Int64
	localFallbackTotal       atomic.Int64
	promotionTotal           atomic.Int64
	storeLatencySamples      atomic.Int64
	storeLatencyTotalMicros  atomic.Int64
	storeLatencyMaxMicros    atomic.Int64
}

var openAIOutboundSessionIdentityMetrics openAIOutboundSessionIdentityRuntimeMetricsStore

// SnapshotOpenAIOutboundSessionIdentityRuntimeMetrics returns bounded,
// label-free process metrics for identity-store health.
func SnapshotOpenAIOutboundSessionIdentityRuntimeMetrics() OpenAIOutboundSessionIdentityRuntimeMetrics {
	m := &openAIOutboundSessionIdentityMetrics
	return OpenAIOutboundSessionIdentityRuntimeMetrics{
		ResolveTotal:             m.resolveTotal.Load(),
		EmptyLogicalKeyTotal:     m.emptyLogicalKeyTotal.Load(),
		PrimaryStoreSuccessTotal: m.primaryStoreSuccessTotal.Load(),
		PrimaryStoreFailureTotal: m.primaryStoreFailureTotal.Load(),
		PrimaryStoreInvalidTotal: m.primaryStoreInvalidTotal.Load(),
		LocalFallbackTotal:       m.localFallbackTotal.Load(),
		PromotionTotal:           m.promotionTotal.Load(),
		StoreLatencySamples:      m.storeLatencySamples.Load(),
		StoreLatencyTotalMicros:  m.storeLatencyTotalMicros.Load(),
		StoreLatencyMaxMicros:    m.storeLatencyMaxMicros.Load(),
	}
}

func observeOpenAIOutboundSessionIdentityStoreLatency(start time.Time) {
	micros := time.Since(start).Microseconds()
	if micros < 0 {
		micros = 0
	}
	m := &openAIOutboundSessionIdentityMetrics
	m.storeLatencySamples.Add(1)
	m.storeLatencyTotalMicros.Add(micros)
	for {
		current := m.storeLatencyMaxMicros.Load()
		if micros <= current || m.storeLatencyMaxMicros.CompareAndSwap(current, micros) {
			return
		}
	}
}

func newOpenAIOutboundSessionIdentityLocalStore() *openAIOutboundSessionIdentityLocalStore {
	return newOpenAIOutboundSessionIdentityLocalStoreWithCapacity(openAIOutboundSessionIdentityLocalStoreMaxEntries)
}

func newOpenAIOutboundSessionIdentityLocalStoreWithCapacity(maxEntries int) *openAIOutboundSessionIdentityLocalStore {
	if maxEntries <= 0 {
		maxEntries = openAIOutboundSessionIdentityLocalStoreMaxEntries
	}
	store := &openAIOutboundSessionIdentityLocalStore{
		entries:    make(map[string]*openAIOutboundSessionIdentityLocalEntry),
		recency:    list.New(),
		maxEntries: maxEntries,
	}
	heap.Init(&store.expirations)
	return store
}

func (s *openAIOutboundSessionIdentityLocalStore) removeEntryLocked(entry *openAIOutboundSessionIdentityLocalEntry) {
	if entry == nil {
		return
	}
	if current, ok := s.entries[entry.mappingKey]; !ok || current != entry {
		return
	}
	delete(s.entries, entry.mappingKey)
	if entry.recencyElement != nil {
		s.recency.Remove(entry.recencyElement)
		entry.recencyElement = nil
	}
	if entry.expiryIndex >= 0 && entry.expiryIndex < len(s.expirations) && s.expirations[entry.expiryIndex] == entry {
		heap.Remove(&s.expirations, entry.expiryIndex)
	}
}

func (s *openAIOutboundSessionIdentityLocalStore) pruneExpiredLocked(now time.Time) {
	for len(s.expirations) > 0 {
		entry := s.expirations[0]
		if now.Before(entry.expires) {
			return
		}
		s.removeEntryLocked(entry)
	}
}

func (s *openAIOutboundSessionIdentityLocalStore) ensureRoomLocked(now time.Time) {
	s.pruneExpiredLocked(now)
	for len(s.entries) >= s.maxEntries {
		oldest := s.recency.Back()
		if oldest == nil {
			return
		}
		entry, _ := oldest.Value.(*openAIOutboundSessionIdentityLocalEntry)
		s.removeEntryLocked(entry)
	}
}

func (s *openAIOutboundSessionIdentityLocalStore) addEntryLocked(mappingKey string, identity OpenAIOutboundSessionIdentity, expires time.Time) *openAIOutboundSessionIdentityLocalEntry {
	entry := &openAIOutboundSessionIdentityLocalEntry{
		mappingKey:  mappingKey,
		identity:    identity,
		expires:     expires,
		expiryIndex: -1,
	}
	entry.recencyElement = s.recency.PushFront(entry)
	s.entries[mappingKey] = entry
	heap.Push(&s.expirations, entry)
	return entry
}

func (s *openAIOutboundSessionIdentityLocalStore) touchEntryLocked(entry *openAIOutboundSessionIdentityLocalEntry, expires time.Time) {
	entry.expires = expires
	if entry.recencyElement == nil {
		entry.recencyElement = s.recency.PushFront(entry)
	} else {
		s.recency.MoveToFront(entry.recencyElement)
	}
	if entry.expiryIndex < 0 {
		heap.Push(&s.expirations, entry)
	} else {
		heap.Fix(&s.expirations, entry.expiryIndex)
	}
}

func (s *openAIOutboundSessionIdentityLocalStore) GetOrCreate(_ context.Context, mappingKey string, candidate OpenAIOutboundSessionIdentity, ttl time.Duration) (OpenAIOutboundSessionIdentity, error) {
	mappingKey = strings.TrimSpace(mappingKey)
	if mappingKey == "" {
		return OpenAIOutboundSessionIdentity{}, errOpenAIOutboundSessionIdentityKeyEmpty
	}
	if ttl <= 0 {
		ttl = OpenAIOutboundSessionIdentityTTL
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneExpiredLocked(now)
	if current, ok := s.entries[mappingKey]; ok {
		if ValidateOpenAIOutboundSessionIdentity(current.identity) == nil {
			s.touchEntryLocked(current, now.Add(ttl))
			return current.identity, nil
		}
		s.removeEntryLocked(current)
	}
	if err := ValidateOpenAIOutboundSessionIdentity(candidate); err != nil {
		return OpenAIOutboundSessionIdentity{}, err
	}
	s.ensureRoomLocked(now)
	s.addEntryLocked(mappingKey, candidate, now.Add(ttl))
	return candidate, nil
}

// promote makes a primary-store winner authoritative in this process. It is
// used after a Redis race/recovery so subsequent Redis candidates are the last
// known winner instead of a newly generated pair.
func (s *openAIOutboundSessionIdentityLocalStore) promote(mappingKey string, identity OpenAIOutboundSessionIdentity, ttl time.Duration) error {
	_, err := s.promoteAndConsumePending(mappingKey, identity, ttl)
	return err
}

// promoteAndConsumePending makes the primary winner authoritative and consumes
// the fallback marker in the same critical section. This prevents concurrent
// recovery requests from counting the same fallback episode more than once.
func (s *openAIOutboundSessionIdentityLocalStore) promoteAndConsumePending(mappingKey string, identity OpenAIOutboundSessionIdentity, ttl time.Duration) (bool, error) {
	mappingKey = strings.TrimSpace(mappingKey)
	if mappingKey == "" {
		return false, errOpenAIOutboundSessionIdentityKeyEmpty
	}
	if err := ValidateOpenAIOutboundSessionIdentity(identity); err != nil {
		return false, err
	}
	if ttl <= 0 {
		ttl = OpenAIOutboundSessionIdentityTTL
	}
	now := time.Now()
	s.mu.Lock()
	s.pruneExpiredLocked(now)
	entry, ok := s.entries[mappingKey]
	hadPending := ok && entry.pendingPromotion
	if ok {
		entry.identity = identity
		entry.pendingPromotion = false
		s.touchEntryLocked(entry, now.Add(ttl))
	} else {
		s.ensureRoomLocked(now)
		s.addEntryLocked(mappingKey, identity, now.Add(ttl))
	}
	s.mu.Unlock()
	return hadPending, nil
}

func (s *openAIOutboundSessionIdentityLocalStore) markPendingPromotion(mappingKey string) {
	s.mu.Lock()
	s.pruneExpiredLocked(time.Now())
	entry, ok := s.entries[mappingKey]
	if ok {
		entry.pendingPromotion = true
		if entry.recencyElement != nil {
			s.recency.MoveToFront(entry.recencyElement)
		}
	}
	s.mu.Unlock()
}

func (s *openAIOutboundSessionIdentityLocalStore) hasPendingPromotion(mappingKey string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneExpiredLocked(time.Now())
	entry, ok := s.entries[mappingKey]
	return ok && entry.pendingPromotion
}

var processOpenAIOutboundSessionIdentityStore = newOpenAIOutboundSessionIdentityLocalStore()

// NewLocalOpenAIOutboundSessionIdentityStore creates an isolated in-process
// store.  The resolver uses a process-wide instance, while tests and callers
// that need isolation can construct their own instance.
func NewLocalOpenAIOutboundSessionIdentityStore() OpenAIOutboundSessionIdentityStore {
	return newOpenAIOutboundSessionIdentityLocalStore()
}

// OpenAIOutboundSessionIdentityKey derives the versioned, non-reversible
// mapping key used by the Redis store.  Length-prefixed fields are represented
// with NUL separators; all fields are canonicalized before HMAC so equivalent
// inputs cannot create multiple identities accidentally.
//
// The returned value is a lowercase SHA-256 HMAC digest and is safe to append
// to the repository's Redis key prefix.
func OpenAIOutboundSessionIdentityKey(secret, namespace string, apiKeyID int64, logicalKey string) (string, error) {
	secret = strings.TrimSpace(secret)
	namespace = sanitizeSessionID(namespace)
	logicalKey = sanitizeSessionID(logicalKey)
	if secret == "" {
		return "", errOpenAIOutboundSessionIdentityKeySecret
	}
	if namespace == "" || logicalKey == "" {
		return "", errOpenAIOutboundSessionIdentityKeyEmpty
	}
	message := openAIOutboundSessionIdentityDomain + "\x00" + namespace + "\x00" + strconv.FormatInt(apiKeyID, 10) + "\x00" + logicalKey
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(message))
	return hex.EncodeToString(mac.Sum(nil)), nil
}

// OpenAIOutboundSessionIdentityFallbackKey returns a namespaced process-local
// key when no configured HMAC secret is available.  It intentionally does not
// expose this value to Redis or logs.
func OpenAIOutboundSessionIdentityFallbackKey(namespace string, apiKeyID int64, logicalKey string) string {
	canonical := openAIOutboundSessionIdentityDomain + "\x00fallback\x00" + sanitizeSessionID(namespace) + "\x00" + strconv.FormatInt(apiKeyID, 10) + "\x00" + sanitizeSessionID(logicalKey)
	digest := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(digest[:])
}

func newOpenAIOutboundSessionIdentity() (OpenAIOutboundSessionIdentity, error) {
	// UUIDv7 collisions are extraordinarily unlikely, but the pair is a wire
	// contract and must be distinct even if a custom UUID source is injected in
	// a test or a clock/entropy edge case repeats a value.
	for attempt := 0; attempt < 3; attempt++ {
		sessionID, err := uuid.NewV7()
		if err != nil {
			return OpenAIOutboundSessionIdentity{}, fmt.Errorf("generate outbound session UUIDv7: %w", err)
		}
		threadID, err := uuid.NewV7()
		if err != nil {
			return OpenAIOutboundSessionIdentity{}, fmt.Errorf("generate outbound thread UUIDv7: %w", err)
		}
		if sessionID != threadID {
			return OpenAIOutboundSessionIdentity{SessionID: sessionID.String(), ThreadID: threadID.String()}, nil
		}
	}
	return OpenAIOutboundSessionIdentity{}, errors.New("generate outbound session identity: UUIDv7 pair collision")
}

func openAIOutboundSessionIdentityNamespace(account *Account) string {
	if account == nil {
		return "account:0"
	}
	return "account:" + strconv.FormatInt(account.ID, 10)
}

// resolveOpenAIOutboundSessionIdentityNamespace follows the credential-owner
// boundary for shadow accounts. Production services reuse
// resolveCredentialAccount's validation; lightweight tests/services without an
// account repository safely fall back to the already-materialized parent ID.
func (s *OpenAIGatewayService) resolveOpenAIOutboundSessionIdentityNamespace(ctx context.Context, account *Account) (string, error) {
	if account == nil || account.Type != AccountTypeOAuth || !account.IsShadow() {
		return openAIOutboundSessionIdentityNamespace(account), nil
	}
	// Production always has an account repository and validates the shadow's
	// parent through resolveCredentialAccount. Lightweight callers may only have
	// the materialized parent ID; retain that compatibility fallback, but reject
	// an unusable zero/negative parent instead of collapsing it into account:0.
	if account.ParentAccountID == nil || *account.ParentAccountID <= 0 {
		return "", fmt.Errorf("%w: shadow parent id is invalid", errOpenAIOutboundSessionIdentityNamespace)
	}
	if s == nil || s.accountRepo == nil {
		return "account:" + strconv.FormatInt(*account.ParentAccountID, 10), nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	credentialAccount, err := resolveCredentialAccount(ctx, s.accountRepo, account)
	if err != nil {
		return "", fmt.Errorf("resolve outbound session identity credential namespace: %w: %w", errOpenAIOutboundSessionIdentityNamespace, err)
	}
	return openAIOutboundSessionIdentityNamespace(credentialAccount), nil
}

// resolveOpenAIOutboundSessionIdentity resolves a stable pair for the logical
// request key.  A blank logical key deliberately leaves the caller's old
// behavior untouched (ok=false).  Cache failures fall back to the process
// store and do not reject the request path.
func (s *OpenAIGatewayService) resolveOpenAIOutboundSessionIdentity(ctx context.Context, c *gin.Context, account *Account, logicalKey string) (OpenAIOutboundSessionIdentity, bool, error) {
	openAIOutboundSessionIdentityMetrics.resolveTotal.Add(1)
	// Callers resolve the request's logical key against the final request body
	// before entering the identity store.  Keep this lower-level operation
	// deterministic: re-running header/body priority here could replace a
	// caller-selected compact seed with an unrelated header value.  We still
	// apply the same validation boundary so direct/internal callers cannot put
	// unsafe data into the mapping derivation.
	logicalKey = sanitizeSessionID(logicalKey)
	if logicalKey == "" {
		openAIOutboundSessionIdentityMetrics.emptyLogicalKeyTotal.Add(1)
		return OpenAIOutboundSessionIdentity{}, false, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	namespace, namespaceErr := s.resolveOpenAIOutboundSessionIdentityNamespace(ctx, account)
	if namespaceErr != nil {
		return OpenAIOutboundSessionIdentity{}, true, namespaceErr
	}
	apiKeyID := getAPIKeyIDFromContext(c)
	secret := ""
	if s != nil && s.cfg != nil {
		secret = s.cfg.JWT.Secret
	}
	mappingKey, keyErr := OpenAIOutboundSessionIdentityKey(secret, namespace, apiKeyID, logicalKey)
	if keyErr != nil {
		// A missing JWT secret is a deployment/configuration issue, but keeping
		// the request usable is preferable to turning it into a gateway error.
		mappingKey = OpenAIOutboundSessionIdentityFallbackKey(namespace, apiKeyID, logicalKey)
		slog.WarnContext(ctx, "openai_outbound_session_identity_fallback",
			"reason", "hmac_secret_unavailable",
			"account_namespace", namespace,
			"api_key_id", apiKeyID,
		)
	}
	freshCandidate, err := newOpenAIOutboundSessionIdentity()
	if err != nil {
		return OpenAIOutboundSessionIdentity{}, true, err
	}
	// Resolve the process winner first. If Redis was previously unavailable,
	// this is the pair that must be promoted when Redis recovers.
	localCandidate, err := processOpenAIOutboundSessionIdentityStore.GetOrCreate(ctx, mappingKey, freshCandidate, OpenAIOutboundSessionIdentityTTL)
	if err != nil {
		return OpenAIOutboundSessionIdentity{}, true, err
	}
	var store OpenAIOutboundSessionIdentityStore
	if s != nil && s.cache != nil {
		if candidateStore, ok := s.cache.(OpenAIOutboundSessionIdentityStore); ok {
			store = candidateStore
		}
	}
	if store == nil || keyErr != nil {
		openAIOutboundSessionIdentityMetrics.localFallbackTotal.Add(1)
		if keyErr == nil {
			processOpenAIOutboundSessionIdentityStore.markPendingPromotion(mappingKey)
			slog.WarnContext(ctx, "openai_outbound_session_identity_fallback",
				"reason", "primary_store_unavailable",
				"account_namespace", namespace,
				"api_key_id", apiKeyID,
			)
		}
		return localCandidate, true, nil
	}
	storeStarted := time.Now()
	identity, err := store.GetOrCreate(ctx, mappingKey, localCandidate, OpenAIOutboundSessionIdentityTTL)
	observeOpenAIOutboundSessionIdentityStoreLatency(storeStarted)
	if err != nil {
		if errors.Is(err, ErrOpenAIOutboundSessionIdentityStoredValueInvalid) {
			openAIOutboundSessionIdentityMetrics.primaryStoreInvalidTotal.Add(1)
		} else {
			openAIOutboundSessionIdentityMetrics.primaryStoreFailureTotal.Add(1)
		}
		openAIOutboundSessionIdentityMetrics.localFallbackTotal.Add(1)
		processOpenAIOutboundSessionIdentityStore.markPendingPromotion(mappingKey)
		slog.WarnContext(ctx, "openai_outbound_session_identity_fallback",
			"reason", "primary_store_error",
			"stored_value_invalid", errors.Is(err, ErrOpenAIOutboundSessionIdentityStoredValueInvalid),
			"account_namespace", namespace,
			"api_key_id", apiKeyID,
		)
		return localCandidate, true, nil
	}
	if validationErr := ValidateOpenAIOutboundSessionIdentity(identity); validationErr != nil {
		openAIOutboundSessionIdentityMetrics.primaryStoreInvalidTotal.Add(1)
		openAIOutboundSessionIdentityMetrics.localFallbackTotal.Add(1)
		processOpenAIOutboundSessionIdentityStore.markPendingPromotion(mappingKey)
		slog.WarnContext(ctx, "openai_outbound_session_identity_fallback",
			"reason", "primary_store_invalid_pair",
			"account_namespace", namespace,
			"api_key_id", apiKeyID,
		)
		return localCandidate, true, nil
	}
	openAIOutboundSessionIdentityMetrics.primaryStoreSuccessTotal.Add(1)
	// Always synchronize the process-local winner with a healthy primary read.
	// This matters after process startup: Redis may already contain pair B while
	// the local store tentatively seeded pair A. Without this promotion, a later
	// Redis outage would flip the request back to stale A.
	if hadPendingPromotion, promoteErr := processOpenAIOutboundSessionIdentityStore.promoteAndConsumePending(mappingKey, identity, OpenAIOutboundSessionIdentityTTL); promoteErr == nil && hadPendingPromotion {
		openAIOutboundSessionIdentityMetrics.promotionTotal.Add(1)
	}
	return identity, true, nil
}

// ApplyOpenAIOutboundSessionIdentityHeaders applies the pair to the canonical
// HTTP/WebSocket wire headers. It clears broader inbound aliases first and is
// the final writer for server-owned identity and request-correlation headers.
func ApplyOpenAIOutboundSessionIdentityHeaders(headers http.Header, identity OpenAIOutboundSessionIdentity) {
	applyOpenAIOutboundSessionIdentityHeaders(headers, identity)
}

func applyOpenAIOutboundSessionIdentityHeaders(headers http.Header, identity OpenAIOutboundSessionIdentity) {
	if headers == nil {
		return
	}
	// Identity headers are server-owned. Clear every accepted alias first so a
	// partial/invalid test identity, an account override, or a copied client
	// header cannot leave a conflicting value behind when this helper is the
	// final writer.
	deleteOpenAIOutboundSessionIdentityHeaders(headers)
	if sessionID := strings.TrimSpace(identity.SessionID); sessionID != "" {
		headers.Set("session-id", sessionID)
		headers.Set("session_id", sessionID)
	}
	if threadID := strings.TrimSpace(identity.ThreadID); threadID != "" {
		headers.Set("thread-id", threadID)
		headers.Set("conversation_id", threadID)
		// Codex uses the thread identifier as the stable request correlation id
		// for this outbound identity pair.
		headers.Set("x-client-request-id", threadID)
	}
}

// deleteOpenAIOutboundSessionIdentityHeaders removes identity aliases using
// case-insensitive map matching. http.Header canonicalization handles normal
// HTTP traffic, but bridges and tests can construct maps containing variants
// such as "Session_Id" that Header.Del would otherwise leave behind.
func deleteOpenAIOutboundSessionIdentityHeaders(headers http.Header) {
	if headers == nil {
		return
	}
	identityHeaders := [...]string{
		"session-id",
		"session_id",
		"thread-id",
		"thread_id",
		"conversation_id",
		"conversation-id",
		"x-client-request-id",
	}
	for key := range headers {
		for _, identityHeader := range identityHeaders {
			if strings.EqualFold(key, identityHeader) {
				delete(headers, key)
				break
			}
		}
	}
}

// MergeOpenAIOutboundSessionIdentityBody merges the pair into
// client_metadata.session_id/thread_id. Invalid or non-object JSON is
// returned unchanged with an error so callers can choose a header-only path.
func MergeOpenAIOutboundSessionIdentityBody(body []byte, identity OpenAIOutboundSessionIdentity) ([]byte, error) {
	return mergeOpenAIOutboundSessionIdentityBody(body, identity)
}

func mergeOpenAIOutboundSessionIdentityBody(body []byte, identity OpenAIOutboundSessionIdentity) ([]byte, error) {
	if len(body) == 0 {
		return body, nil
	}
	if !utf8.Valid(body) {
		return body, errors.New("decode OpenAI outbound identity body: invalid UTF-8")
	}
	if strings.TrimSpace(identity.SessionID) == "" && strings.TrimSpace(identity.ThreadID) == "" {
		return body, nil
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil {
		return body, fmt.Errorf("decode OpenAI outbound identity body: %w", err)
	}
	if root == nil {
		return body, errors.New("decode OpenAI outbound identity body: expected object")
	}
	// Re-encode the parsed object instead of patching raw bytes in place. JSON
	// permits duplicate object names, while encoding/json (used by the
	// resolver above) applies last-value-wins semantics. A raw sjson patch can
	// update only one duplicate path and leave an earlier client_metadata or
	// identity alias in the final wire body. The structured round-trip makes
	// that resolver/merge contract explicit for the root and client_metadata
	// objects: each name in those objects is emitted once.
	metadata := make(map[string]json.RawMessage)
	if rawMetadata, ok := root["client_metadata"]; ok {
		var decoded map[string]json.RawMessage
		if err := json.Unmarshal(rawMetadata, &decoded); err == nil && decoded != nil {
			metadata = decoded
		}
	}
	// Remove every accepted client_metadata identity alias before writing the
	// canonical pair. This prevents a client-supplied conversation_id/thread-id
	// from disagreeing with the server-owned session/thread values upstream.
	for _, field := range openAIOutboundSessionJSONFields {
		delete(metadata, field)
	}
	if sid := strings.TrimSpace(identity.SessionID); sid != "" {
		encoded, err := json.Marshal(sid)
		if err != nil {
			return body, fmt.Errorf("encode OpenAI session identity: %w", err)
		}
		metadata["session_id"] = encoded
	}
	if tid := strings.TrimSpace(identity.ThreadID); tid != "" {
		encoded, err := json.Marshal(tid)
		if err != nil {
			return body, fmt.Errorf("encode OpenAI thread identity: %w", err)
		}
		metadata["thread_id"] = encoded
	}
	encodedMetadata, err := json.Marshal(metadata)
	if err != nil {
		return body, fmt.Errorf("encode OpenAI client metadata: %w", err)
	}
	root["client_metadata"] = encodedMetadata
	out, err := json.Marshal(root)
	if err != nil {
		return body, fmt.Errorf("encode OpenAI outbound identity body: %w", err)
	}
	return out, nil
}
