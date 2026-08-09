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

type OpenAICodexTurnRelation string

const (
	OpenAICodexTurnRelationRoot       OpenAICodexTurnRelation = "root"
	OpenAICodexTurnRelationDescendant OpenAICodexTurnRelation = "descendant"
)

// OpenAICodexTurnIdentity is the server-owned Codex turn identity projected
// onto an upstream request. A root thread shares its UUIDv7 with the session;
// descendants keep the session UUID and receive their own thread UUIDv7.
type OpenAICodexTurnIdentity struct {
	SessionID          string                  `json:"session_id"`
	ThreadID           string                  `json:"thread_id"`
	ParentThreadID     string                  `json:"parent_thread_id,omitempty"`
	ForkedFromThreadID string                  `json:"forked_from_thread_id,omitempty"`
	Relation           OpenAICodexTurnRelation `json:"relation,omitempty"`
}

// OpenAIOutboundSessionIdentity remains as a source-compatible name while the
// transport call sites migrate to the Codex lifecycle model.
type OpenAIOutboundSessionIdentity = OpenAICodexTurnIdentity

func canonicalUUIDv7(value string) (string, error) {
	raw := strings.TrimSpace(value)
	parsed, err := uuid.Parse(raw)
	if err != nil || raw != parsed.String() || parsed.Version() != uuid.Version(7) || parsed.Variant() != uuid.RFC4122 {
		return "", errors.New("value must be a canonical RFC 4122 UUIDv7")
	}
	return raw, nil
}

func normalizedOpenAICodexTurnRelation(identity OpenAICodexTurnIdentity) OpenAICodexTurnRelation {
	if identity.Relation != "" {
		return identity.Relation
	}
	if identity.SessionID == identity.ThreadID {
		return OpenAICodexTurnRelationRoot
	}
	return OpenAICodexTurnRelationDescendant
}

func ValidateOpenAICodexTurnIdentity(identity OpenAICodexTurnIdentity) error {
	sessionID, err := canonicalUUIDv7(identity.SessionID)
	if err != nil {
		return errors.New("openai Codex turn identity session_id must be UUIDv7")
	}
	threadID, err := canonicalUUIDv7(identity.ThreadID)
	if err != nil {
		return errors.New("openai Codex turn identity thread_id must be UUIDv7")
	}
	relation := normalizedOpenAICodexTurnRelation(identity)
	switch relation {
	case OpenAICodexTurnRelationRoot:
		if sessionID != threadID {
			return errors.New("openai Codex root identity must use session_id as thread_id")
		}
	case OpenAICodexTurnRelationDescendant:
		if sessionID == threadID {
			return errors.New("openai Codex descendant identity must use an independent thread_id")
		}
	default:
		return errors.New("openai Codex turn identity relation is invalid")
	}
	for name, value := range map[string]string{
		"parent_thread_id":      identity.ParentThreadID,
		"forked_from_thread_id": identity.ForkedFromThreadID,
	} {
		if strings.TrimSpace(value) == "" {
			continue
		}
		if _, err := canonicalUUIDv7(value); err != nil {
			return fmt.Errorf("openai Codex turn identity %s must be UUIDv7", name)
		}
	}
	return nil
}

func ValidateOpenAIOutboundSessionIdentity(identity OpenAIOutboundSessionIdentity) error {
	return ValidateOpenAICodexTurnIdentity(identity)
}

// OpenAICodexTurnIdentityStore intentionally stays narrower than GatewayCache.
// Session resolution is separate because the winning S is part of the thread
// digest and must be known before a descendant key can be derived.
type OpenAICodexTurnIdentityStore interface {
	GetOrCreateCodexSession(ctx context.Context, sessionMappingKey, candidateSessionID string, ttl time.Duration) (string, error)
	GetOrCreateCodexThread(ctx context.Context, sessionMappingKey, threadMappingKey, sessionID, candidateThreadID string, ttl time.Duration) (OpenAICodexTurnIdentity, error)
}

const (
	OpenAIOutboundSessionIdentityTTL = 30 * 24 * time.Hour

	openAICodexSessionIdentityDomain = "sub2api/openai-outbound-session/v2/session"
	openAICodexThreadIdentityDomain  = "sub2api/openai-outbound-session/v2/thread"
	openAICodexLocalStoreMaxEntries  = 64 * 1024
)

const (
	OpenAIOutboundSessionLogicalKeySourceNone               = "none"
	OpenAIOutboundSessionLogicalKeySourceTurnMetadata       = "x_codex_turn_metadata"
	OpenAIOutboundSessionLogicalKeySourceHeaderSession      = "header_session"
	OpenAIOutboundSessionLogicalKeySourceHeaderThread       = "header_thread"
	OpenAIOutboundSessionLogicalKeySourceHeaderConversation = "header_conversation"
	OpenAIOutboundSessionLogicalKeySourceHeaderAffinity     = "header_affinity"
	OpenAIOutboundSessionLogicalKeySourceClientMetadata     = "client_metadata"
	OpenAIOutboundSessionLogicalKeySourceCallerSeed         = "caller_seed"
	OpenAIOutboundSessionLogicalKeySourcePromptCacheKey     = "prompt_cache_key"
)

type OpenAICodexLogicalTurnIdentity struct {
	SessionKey          string
	ThreadKey           string
	ParentThreadKey     string
	ForkedFromThreadKey string
	Relation            OpenAICodexTurnRelation
	Source              string
	Explicit            bool
}

type OpenAIOutboundSessionLogicalKeyResolution struct {
	LogicalKey string
	Source     string
}

type openAICodexLogicalTuple struct {
	session string
	thread  string
	parent  string
	fork    string
}

var openAICodexIdentityConflictTotal atomic.Int64

func OpenAICodexIdentityConflictCount() int64 {
	return openAICodexIdentityConflictTotal.Load()
}

func tupleFromJSON(object map[string]json.RawMessage) openAICodexLogicalTuple {
	return openAICodexLogicalTuple{
		session: firstValidOpenAICodexJSONField(object, "session_id", "session-id"),
		thread:  firstValidOpenAICodexJSONField(object, "thread_id", "thread-id"),
		parent: firstValidOpenAICodexJSONField(object,
			"parent_thread_id", "parent-thread-id", "x-codex-parent-thread-id"),
		fork: firstValidOpenAICodexJSONField(object,
			"forked_from_thread_id", "forked-from-thread-id"),
	}
}

func firstValidOpenAICodexJSONField(object map[string]json.RawMessage, fields ...string) string {
	for _, field := range fields {
		raw, ok := object[field]
		if !ok {
			continue
		}
		var value string
		if json.Unmarshal(raw, &value) == nil {
			if value = sanitizeSessionID(value); value != "" {
				return value
			}
		}
	}
	return ""
}

func tupleFromTurnMetadata(raw []byte) openAICodexLogicalTuple {
	if len(raw) == 0 || !utf8.Valid(raw) {
		return openAICodexLogicalTuple{}
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || object == nil {
		return openAICodexLogicalTuple{}
	}
	return tupleFromJSON(object)
}

func normalizeLogicalTuple(tuple openAICodexLogicalTuple, source string, explicit bool) OpenAICodexLogicalTurnIdentity {
	session := sanitizeSessionID(tuple.session)
	thread := sanitizeSessionID(tuple.thread)
	if session == "" && thread != "" {
		session = thread
	}
	if thread == "" && session != "" {
		thread = session
	}
	relation := OpenAICodexTurnRelationRoot
	if session != "" && thread != "" && session != thread {
		relation = OpenAICodexTurnRelationDescendant
	}
	return OpenAICodexLogicalTurnIdentity{
		SessionKey:          session,
		ThreadKey:           thread,
		ParentThreadKey:     sanitizeSessionID(tuple.parent),
		ForkedFromThreadKey: sanitizeSessionID(tuple.fork),
		Relation:            relation,
		Source:              source,
		Explicit:            explicit,
	}
}

func noteTupleConflict(winner OpenAICodexLogicalTurnIdentity, candidate openAICodexLogicalTuple) {
	for _, pair := range [][2]string{
		{winner.SessionKey, sanitizeSessionID(candidate.session)},
		{winner.ThreadKey, sanitizeSessionID(candidate.thread)},
		{winner.ParentThreadKey, sanitizeSessionID(candidate.parent)},
		{winner.ForkedFromThreadKey, sanitizeSessionID(candidate.fork)},
	} {
		if pair[0] != "" && pair[1] != "" && pair[0] != pair[1] {
			openAICodexIdentityConflictTotal.Add(1)
		}
	}
}

func mergeRelatedLogicalKeys(target *OpenAICodexLogicalTurnIdentity, candidate openAICodexLogicalTuple) {
	if target.ParentThreadKey == "" {
		target.ParentThreadKey = sanitizeSessionID(candidate.parent)
	}
	if target.ForkedFromThreadKey == "" {
		target.ForkedFromThreadKey = sanitizeSessionID(candidate.fork)
	}
}

func ResolveOpenAICodexLogicalTurnIdentity(c *gin.Context, body []byte, callerSeed string) OpenAICodexLogicalTurnIdentity {
	return ResolveOpenAICodexLogicalTurnIdentityWithTurnMetadata(c, body, callerSeed, "")
}

func ResolveOpenAICodexLogicalTurnIdentityWithTurnMetadata(c *gin.Context, body []byte, callerSeed, explicitTurnMetadata string) OpenAICodexLogicalTurnIdentity {
	metadata, bodyTurnMetadata, promptCacheKey := openAIOutboundSessionBodySignals(body)
	candidates := make([]struct {
		tuple  openAICodexLogicalTuple
		source string
	}, 0, 8)

	// Codex defines the metadata-contained turn snapshot as the canonical source.
	if raw, ok := metadata[openAIWSTurnMetadataHeader]; ok {
		if decoded := normalizeOpenAIOutboundTurnMetadataRaw(raw); len(decoded) > 0 {
			candidates = append(candidates, struct {
				tuple  openAICodexLogicalTuple
				source string
			}{tupleFromTurnMetadata(decoded), OpenAIOutboundSessionLogicalKeySourceTurnMetadata})
		}
	}
	if c != nil && c.Request != nil {
		for _, raw := range headerValuesCaseInsensitive(c.Request.Header, openAIWSTurnMetadataHeader) {
			candidates = append(candidates, struct {
				tuple  openAICodexLogicalTuple
				source string
			}{tupleFromTurnMetadata([]byte(strings.TrimSpace(raw))), OpenAIOutboundSessionLogicalKeySourceTurnMetadata})
		}
	}
	if raw := strings.TrimSpace(explicitTurnMetadata); raw != "" {
		candidates = append(candidates, struct {
			tuple  openAICodexLogicalTuple
			source string
		}{tupleFromTurnMetadata([]byte(raw)), OpenAIOutboundSessionLogicalKeySourceTurnMetadata})
	}
	for _, raw := range bodyTurnMetadata {
		candidates = append(candidates, struct {
			tuple  openAICodexLogicalTuple
			source string
		}{tupleFromTurnMetadata(raw), OpenAIOutboundSessionLogicalKeySourceTurnMetadata})
	}

	if c != nil && c.Request != nil {
		headers := c.Request.Header
		canonical := openAICodexLogicalTuple{
			session: firstValidOpenAIOutboundSessionHeader(headers, []string{"session-id"}),
			thread:  firstValidOpenAIOutboundSessionHeader(headers, []string{"thread-id"}),
			parent:  firstValidOpenAIOutboundSessionHeader(headers, []string{"x-codex-parent-thread-id"}),
		}
		canonicalSource := OpenAIOutboundSessionLogicalKeySourceHeaderSession
		if canonical.session == "" && canonical.thread != "" {
			canonicalSource = OpenAIOutboundSessionLogicalKeySourceHeaderThread
		}
		candidates = append(candidates, struct {
			tuple  openAICodexLogicalTuple
			source string
		}{canonical, canonicalSource})
	}

	flatTuple := tupleFromJSON(metadata)
	// conversation aliases are compatibility inputs, not the canonical flat
	// session/thread projection.
	flatTuple.session = firstValidOpenAICodexJSONField(metadata, "session_id", "session-id")
	flatTuple.thread = firstValidOpenAICodexJSONField(metadata, "thread_id", "thread-id")
	candidates = append(candidates, struct {
		tuple  openAICodexLogicalTuple
		source string
	}{flatTuple, OpenAIOutboundSessionLogicalKeySourceClientMetadata})

	if c != nil && c.Request != nil {
		headers := c.Request.Header
		compat := openAICodexLogicalTuple{
			session: firstValidOpenAIOutboundSessionHeader(headers, []string{"session_id"}),
			thread:  firstValidOpenAIOutboundSessionHeader(headers, []string{"thread_id"}),
		}
		compatSource := OpenAIOutboundSessionLogicalKeySourceHeaderSession
		if compat.session == "" && compat.thread != "" {
			compatSource = OpenAIOutboundSessionLogicalKeySourceHeaderThread
		}
		if compat.session == "" && compat.thread == "" {
			if conversation := firstValidOpenAIOutboundSessionHeader(headers, []string{"conversation-id", "conversation_id"}); conversation != "" {
				compat.session, compat.thread = conversation, conversation
				compatSource = OpenAIOutboundSessionLogicalKeySourceHeaderConversation
			}
		}
		if compat.session == "" && compat.thread == "" {
			if affinity := firstValidOpenAIOutboundSessionHeader(headers, []string{
				openCodeSessionAffinityHeader, openCodeSessionIDHeader, openCodeNativeSessionHeader, codeBuddyConversationHeader,
			}); affinity != "" {
				compat.session, compat.thread = affinity, affinity
				compatSource = OpenAIOutboundSessionLogicalKeySourceHeaderAffinity
			}
		}
		candidates = append(candidates, struct {
			tuple  openAICodexLogicalTuple
			source string
		}{compat, compatSource})
	}
	if conversation := firstValidOpenAICodexJSONField(metadata, "conversation_id", "conversation-id"); conversation != "" {
		candidates = append(candidates, struct {
			tuple  openAICodexLogicalTuple
			source string
		}{openAICodexLogicalTuple{session: conversation, thread: conversation}, OpenAIOutboundSessionLogicalKeySourceClientMetadata})
	}

	winner := OpenAICodexLogicalTurnIdentity{Source: OpenAIOutboundSessionLogicalKeySourceNone}
	pendingRelated := OpenAICodexLogicalTurnIdentity{}
	for _, candidate := range candidates {
		if winner.SessionKey == "" && (candidate.tuple.session != "" || candidate.tuple.thread != "") {
			winner = normalizeLogicalTuple(candidate.tuple, candidate.source, true)
			if pendingRelated.ParentThreadKey != "" {
				winner.ParentThreadKey = pendingRelated.ParentThreadKey
			}
			if pendingRelated.ForkedFromThreadKey != "" {
				winner.ForkedFromThreadKey = pendingRelated.ForkedFromThreadKey
			}
			continue
		}
		if winner.SessionKey == "" {
			mergeRelatedLogicalKeys(&pendingRelated, candidate.tuple)
			continue
		}
		if winner.SessionKey != "" {
			noteTupleConflict(winner, candidate.tuple)
			mergeRelatedLogicalKeys(&winner, candidate.tuple)
		}
	}
	if winner.SessionKey != "" {
		return winner
	}
	if seed := sanitizeSessionID(callerSeed); seed != "" {
		return normalizeLogicalTuple(openAICodexLogicalTuple{session: seed, thread: seed}, OpenAIOutboundSessionLogicalKeySourceCallerSeed, false)
	}
	if seed := sanitizeSessionID(promptCacheKey); seed != "" {
		return normalizeLogicalTuple(openAICodexLogicalTuple{session: seed, thread: seed}, OpenAIOutboundSessionLogicalKeySourcePromptCacheKey, false)
	}
	return winner
}

func ResolveOpenAIOutboundSessionLogicalKey(c *gin.Context, body []byte, callerSeed string) string {
	return ResolveOpenAICodexLogicalTurnIdentity(c, body, callerSeed).SessionKey
}

func ResolveOpenAIOutboundSessionLogicalKeyWithSource(c *gin.Context, body []byte, callerSeed string) OpenAIOutboundSessionLogicalKeyResolution {
	resolved := ResolveOpenAICodexLogicalTurnIdentity(c, body, callerSeed)
	return OpenAIOutboundSessionLogicalKeyResolution{LogicalKey: resolved.SessionKey, Source: resolved.Source}
}

func ResolveOpenAIOutboundSessionLogicalKeyWithTurnMetadata(c *gin.Context, body []byte, callerSeed, turnMetadata string) string {
	return ResolveOpenAICodexLogicalTurnIdentityWithTurnMetadata(c, body, callerSeed, turnMetadata).SessionKey
}

func resolveOpenAIOutboundSessionLogicalKey(c *gin.Context, body []byte, callerSeed string) string {
	return ResolveOpenAIOutboundSessionLogicalKey(c, body, callerSeed)
}

func headerValuesCaseInsensitive(headers http.Header, name string) []string {
	if headers == nil {
		return nil
	}
	var values []string
	for key, candidates := range headers {
		if strings.EqualFold(key, name) {
			values = append(values, candidates...)
		}
	}
	return values
}

func firstValidOpenAIOutboundSessionHeader(headers http.Header, names []string) string {
	for _, name := range names {
		for _, raw := range headerValuesCaseInsensitive(headers, name) {
			if value := sanitizeSessionID(raw); value != "" {
				return value
			}
		}
	}
	return ""
}

func openAIOutboundSessionBodySignals(body []byte) (metadata map[string]json.RawMessage, turnMetadata [][]byte, promptCacheKey string) {
	if len(body) == 0 || !utf8.Valid(body) {
		return nil, nil, ""
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(body, &root) != nil || root == nil {
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
	if raw, ok := root["client_metadata"]; ok {
		_ = json.Unmarshal(raw, &metadata)
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
	tuple := tupleFromTurnMetadata(raw)
	if tuple.session != "" {
		return tuple.session
	}
	return tuple.thread
}

var (
	errOpenAIOutboundSessionIdentityKeySecret          = errors.New("openai outbound session identity secret is unavailable")
	errOpenAIOutboundSessionIdentityKeyEmpty           = errors.New("openai outbound session identity mapping key is empty")
	errOpenAIOutboundSessionIdentityNamespace          = errors.New("openai outbound session identity namespace resolution failed")
	ErrOpenAIOutboundSessionIdentityStoredValueInvalid = errors.New("stored OpenAI outbound session identity is invalid")
	ErrOpenAICodexSessionWinnerChanged                 = errors.New("OpenAI Codex session winner changed")
)

func openAICodexHMAC(secret, domain string, fields ...string) (string, error) {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return "", errOpenAIOutboundSessionIdentityKeySecret
	}
	for _, field := range fields {
		if sanitizeSessionID(field) == "" {
			return "", errOpenAIOutboundSessionIdentityKeyEmpty
		}
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(domain))
	for _, field := range fields {
		_, _ = mac.Write([]byte{0})
		_, _ = mac.Write([]byte(field))
	}
	return hex.EncodeToString(mac.Sum(nil)), nil
}

func OpenAICodexSessionMappingKey(secret, namespace string, apiKeyID int64, logicalSessionKey string) (string, error) {
	namespace = sanitizeSessionID(namespace)
	logicalSessionKey = sanitizeSessionID(logicalSessionKey)
	return openAICodexHMAC(secret, openAICodexSessionIdentityDomain, namespace, strconv.FormatInt(apiKeyID, 10), logicalSessionKey)
}

func OpenAICodexThreadMappingKey(secret, namespace string, apiKeyID int64, logicalSessionKey, logicalThreadKey, sessionID string) (string, error) {
	namespace = sanitizeSessionID(namespace)
	logicalSessionKey = sanitizeSessionID(logicalSessionKey)
	logicalThreadKey = sanitizeSessionID(logicalThreadKey)
	if _, err := canonicalUUIDv7(sessionID); err != nil {
		return "", errOpenAIOutboundSessionIdentityKeyEmpty
	}
	return openAICodexHMAC(secret, openAICodexThreadIdentityDomain, namespace, strconv.FormatInt(apiKeyID, 10), logicalSessionKey, logicalThreadKey, sessionID)
}

func openAICodexFallbackMappingKey(domain, namespace string, apiKeyID int64, fields ...string) string {
	parts := []string{domain, "fallback", sanitizeSessionID(namespace), strconv.FormatInt(apiKeyID, 10)}
	for _, field := range fields {
		parts = append(parts, sanitizeSessionID(field))
	}
	digest := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(digest[:])
}

// Deprecated helpers now derive V2 root-session keys and never address V1.
func OpenAIOutboundSessionIdentityKey(secret, namespace string, apiKeyID int64, logicalKey string) (string, error) {
	return OpenAICodexSessionMappingKey(secret, namespace, apiKeyID, logicalKey)
}

func OpenAIOutboundSessionIdentityFallbackKey(namespace string, apiKeyID int64, logicalKey string) string {
	return openAICodexFallbackMappingKey(openAICodexSessionIdentityDomain, namespace, apiKeyID, logicalKey)
}

func newUUIDv7Except(except string) (string, error) {
	for attempt := 0; attempt < 4; attempt++ {
		value, err := uuid.NewV7()
		if err != nil {
			return "", err
		}
		if value.String() != except {
			return value.String(), nil
		}
	}
	return "", errors.New("UUIDv7 collision")
}

func newOpenAICodexRootIdentity() (OpenAICodexTurnIdentity, error) {
	sessionID, err := newUUIDv7Except("")
	if err != nil {
		return OpenAICodexTurnIdentity{}, fmt.Errorf("generate outbound session UUIDv7: %w", err)
	}
	return OpenAICodexTurnIdentity{SessionID: sessionID, ThreadID: sessionID, Relation: OpenAICodexTurnRelationRoot}, nil
}

func newOpenAICodexDescendantIdentity(sessionID string) (OpenAICodexTurnIdentity, error) {
	threadID, err := newUUIDv7Except(sessionID)
	if err != nil {
		return OpenAICodexTurnIdentity{}, fmt.Errorf("generate outbound thread UUIDv7: %w", err)
	}
	return OpenAICodexTurnIdentity{SessionID: sessionID, ThreadID: threadID, Relation: OpenAICodexTurnRelationDescendant}, nil
}

type openAICodexLocalEntry struct {
	key              string
	identity         OpenAICodexTurnIdentity
	expires          time.Time
	pendingPromotion bool
	recencyElement   *list.Element
	expiryIndex      int
}

type openAICodexExpiryHeap []*openAICodexLocalEntry

func (h openAICodexExpiryHeap) Len() int { return len(h) }
func (h openAICodexExpiryHeap) Less(i, j int) bool {
	if h[i].expires.Equal(h[j].expires) {
		return h[i].key < h[j].key
	}
	return h[i].expires.Before(h[j].expires)
}
func (h openAICodexExpiryHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].expiryIndex, h[j].expiryIndex = i, j
}
func (h *openAICodexExpiryHeap) Push(value any) {
	entry := value.(*openAICodexLocalEntry)
	entry.expiryIndex = len(*h)
	*h = append(*h, entry)
}
func (h *openAICodexExpiryHeap) Pop() any {
	old := *h
	entry := old[len(old)-1]
	old[len(old)-1] = nil
	entry.expiryIndex = -1
	*h = old[:len(old)-1]
	return entry
}

type openAICodexIdentityLocalStore struct {
	mu          sync.Mutex
	entries     map[string]*openAICodexLocalEntry
	recency     *list.List
	expirations openAICodexExpiryHeap
	maxEntries  int
}

func newOpenAICodexIdentityLocalStore() *openAICodexIdentityLocalStore {
	return newOpenAICodexIdentityLocalStoreWithCapacity(openAICodexLocalStoreMaxEntries)
}

func newOpenAICodexIdentityLocalStoreWithCapacity(capacity int) *openAICodexIdentityLocalStore {
	if capacity <= 0 {
		capacity = openAICodexLocalStoreMaxEntries
	}
	store := &openAICodexIdentityLocalStore{entries: make(map[string]*openAICodexLocalEntry), recency: list.New(), maxEntries: capacity}
	heap.Init(&store.expirations)
	return store
}

func localSessionEntryKey(mappingKey string) string { return "session:" + mappingKey }
func localThreadEntryKey(sessionKey, threadKey string) string {
	return "thread:" + sessionKey + ":" + threadKey
}

func (s *openAICodexIdentityLocalStore) removeLocked(entry *openAICodexLocalEntry) {
	if entry == nil || s.entries[entry.key] != entry {
		return
	}
	delete(s.entries, entry.key)
	if entry.recencyElement != nil {
		s.recency.Remove(entry.recencyElement)
		entry.recencyElement = nil
	}
	if entry.expiryIndex >= 0 {
		heap.Remove(&s.expirations, entry.expiryIndex)
	}
}

func (s *openAICodexIdentityLocalStore) pruneLocked(now time.Time) {
	for len(s.expirations) > 0 && !now.Before(s.expirations[0].expires) {
		s.removeLocked(s.expirations[0])
	}
}

func (s *openAICodexIdentityLocalStore) touchLocked(entry *openAICodexLocalEntry, ttl time.Duration) {
	if ttl <= 0 {
		ttl = OpenAIOutboundSessionIdentityTTL
	}
	entry.expires = time.Now().Add(ttl)
	heap.Fix(&s.expirations, entry.expiryIndex)
	s.recency.MoveToFront(entry.recencyElement)
}

func (s *openAICodexIdentityLocalStore) putLocked(key string, identity OpenAICodexTurnIdentity, ttl time.Duration) *openAICodexLocalEntry {
	now := time.Now()
	s.pruneLocked(now)
	for len(s.entries) >= s.maxEntries {
		oldest, _ := s.recency.Back().Value.(*openAICodexLocalEntry)
		s.removeLocked(oldest)
	}
	if ttl <= 0 {
		ttl = OpenAIOutboundSessionIdentityTTL
	}
	entry := &openAICodexLocalEntry{key: key, identity: identity, expires: now.Add(ttl), expiryIndex: -1}
	entry.recencyElement = s.recency.PushFront(entry)
	s.entries[key] = entry
	heap.Push(&s.expirations, entry)
	return entry
}

func (s *openAICodexIdentityLocalStore) GetOrCreateCodexSession(_ context.Context, sessionMappingKey, candidateSessionID string, ttl time.Duration) (string, error) {
	if strings.TrimSpace(sessionMappingKey) == "" {
		return "", errOpenAIOutboundSessionIdentityKeyEmpty
	}
	if _, err := canonicalUUIDv7(candidateSessionID); err != nil {
		return "", err
	}
	key := localSessionEntryKey(sessionMappingKey)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(time.Now())
	if entry := s.entries[key]; entry != nil {
		s.touchLocked(entry, ttl)
		return entry.identity.SessionID, nil
	}
	root := OpenAICodexTurnIdentity{SessionID: candidateSessionID, ThreadID: candidateSessionID, Relation: OpenAICodexTurnRelationRoot}
	s.putLocked(key, root, ttl)
	return candidateSessionID, nil
}

func (s *openAICodexIdentityLocalStore) GetOrCreateCodexThread(_ context.Context, sessionMappingKey, threadMappingKey, sessionID, candidateThreadID string, ttl time.Duration) (OpenAICodexTurnIdentity, error) {
	if strings.TrimSpace(threadMappingKey) == "" {
		return OpenAICodexTurnIdentity{}, errOpenAIOutboundSessionIdentityKeyEmpty
	}
	candidate := OpenAICodexTurnIdentity{SessionID: sessionID, ThreadID: candidateThreadID, Relation: OpenAICodexTurnRelationDescendant}
	if err := ValidateOpenAICodexTurnIdentity(candidate); err != nil {
		return OpenAICodexTurnIdentity{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(time.Now())
	sessionEntry := s.entries[localSessionEntryKey(sessionMappingKey)]
	if sessionEntry == nil || sessionEntry.identity.SessionID != sessionID {
		return OpenAICodexTurnIdentity{}, ErrOpenAICodexSessionWinnerChanged
	}
	s.touchLocked(sessionEntry, ttl)
	key := localThreadEntryKey(sessionMappingKey, threadMappingKey)
	if entry := s.entries[key]; entry != nil {
		if entry.identity.SessionID != sessionID {
			return OpenAICodexTurnIdentity{}, ErrOpenAICodexSessionWinnerChanged
		}
		s.touchLocked(entry, ttl)
		return entry.identity, nil
	}
	s.putLocked(key, candidate, ttl)
	return candidate, nil
}

func (s *openAICodexIdentityLocalStore) promoteSession(mappingKey, sessionID string, ttl time.Duration) bool {
	key := localSessionEntryKey(mappingKey)
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.entries[key]
	hadPending := entry != nil && entry.pendingPromotion
	root := OpenAICodexTurnIdentity{SessionID: sessionID, ThreadID: sessionID, Relation: OpenAICodexTurnRelationRoot}
	if entry == nil {
		entry = s.putLocked(key, root, ttl)
	} else {
		entry.identity = root
		s.touchLocked(entry, ttl)
	}
	entry.pendingPromotion = false
	return hadPending
}

func (s *openAICodexIdentityLocalStore) promoteThread(sessionKey, threadKey string, identity OpenAICodexTurnIdentity, ttl time.Duration) bool {
	key := localThreadEntryKey(sessionKey, threadKey)
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.entries[key]
	hadPending := entry != nil && entry.pendingPromotion
	if entry == nil {
		entry = s.putLocked(key, identity, ttl)
	} else {
		entry.identity = identity
		s.touchLocked(entry, ttl)
	}
	entry.pendingPromotion = false
	return hadPending
}

func (s *openAICodexIdentityLocalStore) markPending(keys ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, key := range keys {
		if entry := s.entries[key]; entry != nil {
			entry.pendingPromotion = true
		}
	}
}

var processOpenAICodexTurnIdentityStore = newOpenAICodexIdentityLocalStore()

func NewLocalOpenAICodexTurnIdentityStore() OpenAICodexTurnIdentityStore {
	return newOpenAICodexIdentityLocalStore()
}

type OpenAIOutboundSessionIdentityRuntimeMetrics struct {
	ResolveTotal             int64
	ConflictTotal            int64
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

type openAICodexMetrics struct {
	resolveTotal, emptyLogicalKeyTotal, primaryStoreSuccessTotal                        atomic.Int64
	primaryStoreFailureTotal, primaryStoreInvalidTotal, localFallbackTotal              atomic.Int64
	promotionTotal, storeLatencySamples, storeLatencyTotalMicros, storeLatencyMaxMicros atomic.Int64
}

var openAIOutboundSessionIdentityMetrics openAICodexMetrics

func SnapshotOpenAIOutboundSessionIdentityRuntimeMetrics() OpenAIOutboundSessionIdentityRuntimeMetrics {
	m := &openAIOutboundSessionIdentityMetrics
	return OpenAIOutboundSessionIdentityRuntimeMetrics{
		ResolveTotal: m.resolveTotal.Load(), ConflictTotal: openAICodexIdentityConflictTotal.Load(), EmptyLogicalKeyTotal: m.emptyLogicalKeyTotal.Load(),
		PrimaryStoreSuccessTotal: m.primaryStoreSuccessTotal.Load(), PrimaryStoreFailureTotal: m.primaryStoreFailureTotal.Load(),
		PrimaryStoreInvalidTotal: m.primaryStoreInvalidTotal.Load(), LocalFallbackTotal: m.localFallbackTotal.Load(),
		PromotionTotal: m.promotionTotal.Load(), StoreLatencySamples: m.storeLatencySamples.Load(),
		StoreLatencyTotalMicros: m.storeLatencyTotalMicros.Load(), StoreLatencyMaxMicros: m.storeLatencyMaxMicros.Load(),
	}
}

func observeOpenAICodexStoreLatency(start time.Time) {
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

func openAIOutboundSessionIdentityNamespace(account *Account) string {
	if account == nil {
		return "account:0"
	}
	return "account:" + strconv.FormatInt(account.ID, 10)
}

func (s *OpenAIGatewayService) resolveOpenAIOutboundSessionIdentityNamespace(ctx context.Context, account *Account) (string, error) {
	if account == nil || account.Type != AccountTypeOAuth || !account.IsShadow() {
		return openAIOutboundSessionIdentityNamespace(account), nil
	}
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

type openAICodexIdentityResolutionState struct {
	ctx            context.Context
	store          OpenAICodexTurnIdentityStore
	local          *openAICodexIdentityLocalStore
	namespace      string
	apiKeyID       int64
	secret         string
	usePrimary     bool
	sessionDigest  string
	logicalSession string
	sessionID      string
}

func resolvePrimaryOpenAICodexStore(s *OpenAIGatewayService) OpenAICodexTurnIdentityStore {
	if s != nil && s.cache != nil {
		if store, ok := s.cache.(OpenAICodexTurnIdentityStore); ok {
			return store
		}
	}
	return nil
}

func (state *openAICodexIdentityResolutionState) resolveSession() error {
	fresh, err := newOpenAICodexRootIdentity()
	if err != nil {
		return err
	}
	localID, err := state.local.GetOrCreateCodexSession(state.ctx, state.sessionDigest, fresh.SessionID, OpenAIOutboundSessionIdentityTTL)
	if err != nil {
		return err
	}
	state.sessionID = localID
	if !state.usePrimary || state.store == nil {
		openAIOutboundSessionIdentityMetrics.localFallbackTotal.Add(1)
		if state.usePrimary {
			state.local.markPending(localSessionEntryKey(state.sessionDigest))
			slog.WarnContext(state.ctx, "openai_codex_turn_identity_fallback", "reason", "primary_store_unavailable", "account_namespace", state.namespace, "api_key_id", state.apiKeyID)
		}
		return nil
	}
	started := time.Now()
	winner, err := state.store.GetOrCreateCodexSession(state.ctx, state.sessionDigest, localID, OpenAIOutboundSessionIdentityTTL)
	observeOpenAICodexStoreLatency(started)
	if err != nil {
		if errors.Is(err, ErrOpenAIOutboundSessionIdentityStoredValueInvalid) {
			openAIOutboundSessionIdentityMetrics.primaryStoreInvalidTotal.Add(1)
		} else {
			openAIOutboundSessionIdentityMetrics.primaryStoreFailureTotal.Add(1)
		}
		openAIOutboundSessionIdentityMetrics.localFallbackTotal.Add(1)
		state.local.markPending(localSessionEntryKey(state.sessionDigest))
		slog.WarnContext(state.ctx, "openai_codex_turn_identity_fallback", "reason", "primary_store_error", "stored_value_invalid", errors.Is(err, ErrOpenAIOutboundSessionIdentityStoredValueInvalid), "account_namespace", state.namespace, "api_key_id", state.apiKeyID)
		return nil
	}
	if _, err := canonicalUUIDv7(winner); err != nil {
		openAIOutboundSessionIdentityMetrics.primaryStoreInvalidTotal.Add(1)
		openAIOutboundSessionIdentityMetrics.localFallbackTotal.Add(1)
		state.local.markPending(localSessionEntryKey(state.sessionDigest))
		return nil
	}
	openAIOutboundSessionIdentityMetrics.primaryStoreSuccessTotal.Add(1)
	if state.local.promoteSession(state.sessionDigest, winner, OpenAIOutboundSessionIdentityTTL) {
		openAIOutboundSessionIdentityMetrics.promotionTotal.Add(1)
	}
	state.sessionID = winner
	return nil
}

func (state *openAICodexIdentityResolutionState) resolveThread(logicalThread string) (string, error) {
	return state.resolveThreadAttempt(logicalThread, true)
}

func (state *openAICodexIdentityResolutionState) resolveThreadAttempt(logicalThread string, retryWinnerChange bool) (string, error) {
	logicalThread = sanitizeSessionID(logicalThread)
	if logicalThread == "" || logicalThread == state.logicalSession {
		return state.sessionID, nil
	}
	threadDigest, keyErr := OpenAICodexThreadMappingKey(state.secret, state.namespace, state.apiKeyID, state.logicalSession, logicalThread, state.sessionID)
	if keyErr != nil {
		threadDigest = openAICodexFallbackMappingKey(openAICodexThreadIdentityDomain, state.namespace, state.apiKeyID, state.logicalSession, logicalThread, state.sessionID)
	}
	fresh, err := newOpenAICodexDescendantIdentity(state.sessionID)
	if err != nil {
		return "", err
	}
	localIdentity, err := state.local.GetOrCreateCodexThread(state.ctx, state.sessionDigest, threadDigest, state.sessionID, fresh.ThreadID, OpenAIOutboundSessionIdentityTTL)
	if err != nil {
		if retryWinnerChange && errors.Is(err, ErrOpenAICodexSessionWinnerChanged) {
			if sessionErr := state.resolveSession(); sessionErr != nil {
				return "", sessionErr
			}
			return state.resolveThreadAttempt(logicalThread, false)
		}
		return "", err
	}
	if !state.usePrimary || state.store == nil || keyErr != nil {
		openAIOutboundSessionIdentityMetrics.localFallbackTotal.Add(1)
		if state.usePrimary && keyErr == nil {
			state.local.markPending(localThreadEntryKey(state.sessionDigest, threadDigest))
			slog.WarnContext(state.ctx, "openai_codex_turn_identity_fallback", "reason", "primary_store_unavailable", "mapping", "thread", "account_namespace", state.namespace, "api_key_id", state.apiKeyID)
		}
		return localIdentity.ThreadID, nil
	}
	started := time.Now()
	winner, err := state.store.GetOrCreateCodexThread(state.ctx, state.sessionDigest, threadDigest, state.sessionID, localIdentity.ThreadID, OpenAIOutboundSessionIdentityTTL)
	observeOpenAICodexStoreLatency(started)
	if err != nil {
		if retryWinnerChange && errors.Is(err, ErrOpenAICodexSessionWinnerChanged) {
			if sessionErr := state.resolveSession(); sessionErr != nil {
				return "", sessionErr
			}
			return state.resolveThreadAttempt(logicalThread, false)
		}
		if errors.Is(err, ErrOpenAIOutboundSessionIdentityStoredValueInvalid) {
			openAIOutboundSessionIdentityMetrics.primaryStoreInvalidTotal.Add(1)
		} else {
			openAIOutboundSessionIdentityMetrics.primaryStoreFailureTotal.Add(1)
		}
		openAIOutboundSessionIdentityMetrics.localFallbackTotal.Add(1)
		state.local.markPending(localThreadEntryKey(state.sessionDigest, threadDigest))
		slog.WarnContext(state.ctx, "openai_codex_turn_identity_fallback", "reason", "primary_store_error", "mapping", "thread", "stored_value_invalid", errors.Is(err, ErrOpenAIOutboundSessionIdentityStoredValueInvalid), "account_namespace", state.namespace, "api_key_id", state.apiKeyID)
		return localIdentity.ThreadID, nil
	}
	if winner.SessionID != state.sessionID || ValidateOpenAICodexTurnIdentity(winner) != nil || normalizedOpenAICodexTurnRelation(winner) != OpenAICodexTurnRelationDescendant {
		openAIOutboundSessionIdentityMetrics.primaryStoreInvalidTotal.Add(1)
		openAIOutboundSessionIdentityMetrics.localFallbackTotal.Add(1)
		state.local.markPending(localThreadEntryKey(state.sessionDigest, threadDigest))
		return localIdentity.ThreadID, nil
	}
	openAIOutboundSessionIdentityMetrics.primaryStoreSuccessTotal.Add(1)
	if state.local.promoteThread(state.sessionDigest, threadDigest, winner, OpenAIOutboundSessionIdentityTTL) {
		openAIOutboundSessionIdentityMetrics.promotionTotal.Add(1)
	}
	return winner.ThreadID, nil
}

func (s *OpenAIGatewayService) resolveOpenAICodexTurnIdentity(ctx context.Context, c *gin.Context, account *Account, logical OpenAICodexLogicalTurnIdentity) (OpenAICodexTurnIdentity, bool, error) {
	openAIOutboundSessionIdentityMetrics.resolveTotal.Add(1)
	logical = normalizeLogicalTuple(openAICodexLogicalTuple{session: logical.SessionKey, thread: logical.ThreadKey, parent: logical.ParentThreadKey, fork: logical.ForkedFromThreadKey}, logical.Source, logical.Explicit)
	if logical.SessionKey == "" {
		openAIOutboundSessionIdentityMetrics.emptyLogicalKeyTotal.Add(1)
		return OpenAICodexTurnIdentity{}, false, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	namespace, err := s.resolveOpenAIOutboundSessionIdentityNamespace(ctx, account)
	if err != nil {
		return OpenAICodexTurnIdentity{}, true, err
	}
	apiKeyID := getAPIKeyIDFromContext(c)
	secret := ""
	if s != nil && s.cfg != nil {
		secret = s.cfg.JWT.Secret
	}
	sessionDigest, keyErr := OpenAICodexSessionMappingKey(secret, namespace, apiKeyID, logical.SessionKey)
	if keyErr != nil {
		sessionDigest = openAICodexFallbackMappingKey(openAICodexSessionIdentityDomain, namespace, apiKeyID, logical.SessionKey)
		slog.WarnContext(ctx, "openai_codex_turn_identity_fallback", "reason", "hmac_secret_unavailable", "account_namespace", namespace, "api_key_id", apiKeyID)
	}
	local := processOpenAICodexTurnIdentityStore
	state := &openAICodexIdentityResolutionState{
		ctx: ctx, store: resolvePrimaryOpenAICodexStore(s), local: local, namespace: namespace,
		apiKeyID: apiKeyID, secret: secret, usePrimary: keyErr == nil,
		sessionDigest: sessionDigest, logicalSession: logical.SessionKey,
	}
	if err := state.resolveSession(); err != nil {
		return OpenAICodexTurnIdentity{}, true, err
	}
	threadID, err := state.resolveThread(logical.ThreadKey)
	if err != nil {
		return OpenAICodexTurnIdentity{}, true, err
	}
	identity := OpenAICodexTurnIdentity{SessionID: state.sessionID, ThreadID: threadID}
	identity.Relation = OpenAICodexTurnRelationDescendant
	if identity.SessionID == identity.ThreadID {
		identity.Relation = OpenAICodexTurnRelationRoot
	}
	if logical.ParentThreadKey != "" {
		if logical.ParentThreadKey == logical.ThreadKey {
			identity.ParentThreadID = identity.ThreadID
		} else {
			identity.ParentThreadID, err = state.resolveThread(logical.ParentThreadKey)
		}
		if err != nil {
			return OpenAICodexTurnIdentity{}, true, err
		}
	}
	if logical.ForkedFromThreadKey != "" {
		if logical.ForkedFromThreadKey == logical.ThreadKey {
			identity.ForkedFromThreadID = identity.ThreadID
		} else {
			identity.ForkedFromThreadID, err = state.resolveThread(logical.ForkedFromThreadKey)
		}
		if err != nil {
			return OpenAICodexTurnIdentity{}, true, err
		}
	}
	if err := ValidateOpenAICodexTurnIdentity(identity); err != nil {
		return OpenAICodexTurnIdentity{}, true, err
	}
	return identity, true, nil
}

func (s *OpenAIGatewayService) resolveOpenAIOutboundSessionIdentity(ctx context.Context, c *gin.Context, account *Account, logicalKey string) (OpenAIOutboundSessionIdentity, bool, error) {
	logicalKey = sanitizeSessionID(logicalKey)
	logical := normalizeLogicalTuple(openAICodexLogicalTuple{session: logicalKey, thread: logicalKey}, OpenAIOutboundSessionLogicalKeySourceCallerSeed, false)
	return s.resolveOpenAICodexTurnIdentity(ctx, c, account, logical)
}

var _ OpenAICodexTurnIdentityStore = (*openAICodexIdentityLocalStore)(nil)
