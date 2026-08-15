package service

import (
	"container/list"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
)

// openAICodexTurnStateHeader is an opaque turn-continuation token minted by
// OpenAI. Sub2API relays it verbatim and only tracks which normalized outbound
// identity received it; the token itself is never parsed or persisted.
const openAICodexTurnStateHeader = "x-codex-turn-state"

const (
	openAICodexTurnStateKeyDomain       = "sub2api/openai-codex-turn-state/v1"
	openAICodexTurnStateIdentityDomain  = "sub2api/openai-codex-turn-state-identity/v1"
	openAICodexTurnStateLocalMaxEntries = 64 * 1024
	openAICodexTurnStateStoreTimeout    = 500 * time.Millisecond
)

var ErrOpenAICodexTurnStateOriginNotFound = errors.New("openai codex turn-state origin not found")

// OpenAICodexTurnStateOrigin is safe to persist: it contains neither the
// opaque turn-state value nor raw client identity inputs.
type OpenAICodexTurnStateOrigin struct {
	CredentialOwnerNamespace string    `json:"credential_owner_namespace"`
	TurnIdentityDigest       string    `json:"turn_identity_digest"`
	ExpiresAt                time.Time `json:"expires_at"`
}

// OpenAICodexTurnStateOriginStore is optional. The Redis-backed GatewayCache
// implements it in production; deployments without it retain the bounded
// local fallback and fail open on an unknown origin.
type OpenAICodexTurnStateOriginStore interface {
	GetOpenAICodexTurnStateOrigin(ctx context.Context, mappingKey string) (OpenAICodexTurnStateOrigin, error)
	SetOpenAICodexTurnStateOrigin(ctx context.Context, mappingKey string, origin OpenAICodexTurnStateOrigin, ttl time.Duration) error
	DeleteOpenAICodexTurnStateOrigin(ctx context.Context, mappingKey string) error
}

type openAICodexTurnStateLocalEntry struct {
	origin  OpenAICodexTurnStateOrigin
	recency *list.Element
}

type openAICodexTurnStateLocalStore struct {
	mu         sync.Mutex
	entries    map[string]*openAICodexTurnStateLocalEntry
	recency    *list.List
	maxEntries int
}

func newOpenAICodexTurnStateLocalStore(maxEntries int) *openAICodexTurnStateLocalStore {
	if maxEntries <= 0 {
		maxEntries = openAICodexTurnStateLocalMaxEntries
	}
	return &openAICodexTurnStateLocalStore{
		entries:    make(map[string]*openAICodexTurnStateLocalEntry, 256),
		recency:    list.New(),
		maxEntries: maxEntries,
	}
}

func (s *openAICodexTurnStateLocalStore) GetOpenAICodexTurnStateOrigin(_ context.Context, mappingKey string) (OpenAICodexTurnStateOrigin, error) {
	mappingKey = strings.TrimSpace(mappingKey)
	if mappingKey == "" {
		return OpenAICodexTurnStateOrigin{}, ErrOpenAICodexTurnStateOriginNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.entries[mappingKey]
	if !ok {
		return OpenAICodexTurnStateOrigin{}, ErrOpenAICodexTurnStateOriginNotFound
	}
	if !entry.origin.ExpiresAt.IsZero() && time.Now().After(entry.origin.ExpiresAt) {
		s.removeLocked(mappingKey, entry)
		return OpenAICodexTurnStateOrigin{}, ErrOpenAICodexTurnStateOriginNotFound
	}
	s.recency.MoveToFront(entry.recency)
	return entry.origin, nil
}

func (s *openAICodexTurnStateLocalStore) SetOpenAICodexTurnStateOrigin(_ context.Context, mappingKey string, origin OpenAICodexTurnStateOrigin, ttl time.Duration) error {
	mappingKey = strings.TrimSpace(mappingKey)
	origin, ok := normalizeOpenAICodexTurnStateOrigin(origin)
	if mappingKey == "" || !ok {
		return errors.New("invalid openai codex turn-state origin")
	}
	if ttl <= 0 {
		ttl = time.Hour
	}
	if origin.ExpiresAt.IsZero() {
		origin.ExpiresAt = time.Now().Add(ttl)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if entry, exists := s.entries[mappingKey]; exists {
		entry.origin = origin
		s.recency.MoveToFront(entry.recency)
		return nil
	}
	for len(s.entries) >= s.maxEntries {
		oldest := s.recency.Back()
		if oldest == nil {
			break
		}
		key, _ := oldest.Value.(string)
		if entry := s.entries[key]; entry != nil {
			s.removeLocked(key, entry)
		} else {
			s.recency.Remove(oldest)
		}
	}
	element := s.recency.PushFront(mappingKey)
	s.entries[mappingKey] = &openAICodexTurnStateLocalEntry{origin: origin, recency: element}
	return nil
}

func (s *openAICodexTurnStateLocalStore) DeleteOpenAICodexTurnStateOrigin(_ context.Context, mappingKey string) error {
	mappingKey = strings.TrimSpace(mappingKey)
	if mappingKey == "" {
		return nil
	}
	s.mu.Lock()
	if entry := s.entries[mappingKey]; entry != nil {
		s.removeLocked(mappingKey, entry)
	}
	s.mu.Unlock()
	return nil
}

func (s *openAICodexTurnStateLocalStore) removeLocked(key string, entry *openAICodexTurnStateLocalEntry) {
	delete(s.entries, key)
	if entry != nil && entry.recency != nil {
		s.recency.Remove(entry.recency)
	}
}

var processOpenAICodexTurnStateOriginStore = newOpenAICodexTurnStateLocalStore(openAICodexTurnStateLocalMaxEntries)

type openAICodexTurnStateMetricsStore struct {
	noteTotal          atomic.Int64
	guardKeepTotal     atomic.Int64
	guardStripOwner    atomic.Int64
	guardStripIdentity atomic.Int64
	guardUnknownTotal  atomic.Int64
	storeErrorTotal    atomic.Int64
	localFallbackTotal atomic.Int64
}

type OpenAICodexTurnStateMetricsSnapshot struct {
	NoteTotal          int64 `json:"note_total"`
	GuardKeepTotal     int64 `json:"guard_keep_total"`
	GuardStripOwner    int64 `json:"guard_strip_owner_total"`
	GuardStripIdentity int64 `json:"guard_strip_identity_total"`
	GuardUnknownTotal  int64 `json:"guard_unknown_total"`
	StoreErrorTotal    int64 `json:"store_error_total"`
	LocalFallbackTotal int64 `json:"local_fallback_total"`
}

var openAICodexTurnStateMetrics openAICodexTurnStateMetricsStore

func SnapshotOpenAICodexTurnStateMetrics() OpenAICodexTurnStateMetricsSnapshot {
	m := &openAICodexTurnStateMetrics
	return OpenAICodexTurnStateMetricsSnapshot{
		NoteTotal:          m.noteTotal.Load(),
		GuardKeepTotal:     m.guardKeepTotal.Load(),
		GuardStripOwner:    m.guardStripOwner.Load(),
		GuardStripIdentity: m.guardStripIdentity.Load(),
		GuardUnknownTotal:  m.guardUnknownTotal.Load(),
		StoreErrorTotal:    m.storeErrorTotal.Load(),
		LocalFallbackTotal: m.localFallbackTotal.Load(),
	}
}

// OpenAICodexTurnStateProvenanceKey isolates opaque state by downstream API
// key without retaining either input in the resulting Redis/local key.
func OpenAICodexTurnStateProvenanceKey(secret string, apiKeyID int64, state string) (string, error) {
	secret = strings.TrimSpace(secret)
	state = strings.TrimSpace(state)
	if secret == "" {
		return "", errOpenAIOutboundSessionIdentityKeySecret
	}
	if state == "" {
		return "", errOpenAIOutboundSessionIdentityKeyEmpty
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(openAICodexTurnStateKeyDomain))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(strconv.FormatInt(apiKeyID, 10)))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(state))
	return hex.EncodeToString(mac.Sum(nil)), nil
}

// OpenAICodexTurnStateIdentityDigest intentionally excludes projection mode,
// endpoint aliases, beta features, and User-Agent/version. A state minted on a
// normal Responses request must remain valid for compact and WS reconnects
// when the installation and UUID turn tuple are unchanged.
func OpenAICodexTurnStateIdentityDigest(plan OpenAIOAuthIdentityPlan) string {
	effectiveInstallation := ""
	if plan.InstallationEnabled {
		effectiveInstallation = strings.TrimSpace(plan.InstallationID)
	} else if plan.InstallationPolicy == OpenAIOAuthInstallationPreserve {
		effectiveInstallation = strings.TrimSpace(plan.Capture.ClientInstallationID)
	}
	parts := []string{
		openAICodexTurnStateIdentityDomain,
		effectiveInstallation,
		strconv.FormatBool(plan.TurnIdentityEnabled),
	}
	if plan.TurnIdentityEnabled {
		parts = append(parts,
			strings.TrimSpace(plan.TurnIdentity.SessionID),
			strings.TrimSpace(plan.TurnIdentity.ThreadID),
			strings.TrimSpace(plan.TurnIdentity.ParentThreadID),
			strings.TrimSpace(plan.TurnIdentity.ForkedFromThreadID),
			string(plan.TurnIdentity.Relation),
		)
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

type openAICodexTurnStateRequestOrigin struct {
	apiKeyID  int64
	namespace string
	digest    string
}

func openAICodexTurnStateRequestOriginFromContext(c *gin.Context, account *Account) (openAICodexTurnStateRequestOrigin, bool) {
	if account == nil || !account.IsOpenAIOAuth() {
		return openAICodexTurnStateRequestOrigin{}, false
	}
	plan, ok := OpenAIOAuthIdentityPlanFromContext(c)
	if !ok || strings.TrimSpace(plan.CredentialOwnerNamespace) == "" {
		return openAICodexTurnStateRequestOrigin{}, false
	}
	return openAICodexTurnStateRequestOrigin{
		apiKeyID:  plan.APIKeyID,
		namespace: strings.TrimSpace(plan.CredentialOwnerNamespace),
		digest:    OpenAICodexTurnStateIdentityDigest(plan),
	}, true
}

func normalizeOpenAICodexTurnStateOrigin(origin OpenAICodexTurnStateOrigin) (OpenAICodexTurnStateOrigin, bool) {
	origin.CredentialOwnerNamespace = strings.TrimSpace(origin.CredentialOwnerNamespace)
	origin.TurnIdentityDigest = strings.TrimSpace(origin.TurnIdentityDigest)
	return origin, origin.CredentialOwnerNamespace != "" && origin.TurnIdentityDigest != ""
}

func (s *OpenAIGatewayService) primaryOpenAICodexTurnStateOriginStore() OpenAICodexTurnStateOriginStore {
	if s != nil && s.cache != nil {
		if store, ok := s.cache.(OpenAICodexTurnStateOriginStore); ok {
			return store
		}
	}
	return nil
}

func (s *OpenAIGatewayService) openAICodexTurnStateMappingKey(origin openAICodexTurnStateRequestOrigin, state string) (string, bool) {
	secret := ""
	if s != nil && s.cfg != nil {
		secret = s.cfg.JWT.Secret
	}
	key, err := OpenAICodexTurnStateProvenanceKey(secret, origin.apiKeyID, state)
	if err != nil {
		return "", false
	}
	return key, true
}

func openAICodexTurnStateCacheContext(c *gin.Context) (context.Context, context.CancelFunc) {
	base := context.Background()
	if c != nil && c.Request != nil && c.Request.Context() != nil {
		base = context.WithoutCancel(c.Request.Context())
	}
	return context.WithTimeout(base, openAICodexTurnStateStoreTimeout)
}

func (s *OpenAIGatewayService) putOpenAICodexTurnStateOrigin(c *gin.Context, key string, origin OpenAICodexTurnStateOrigin, ttl time.Duration) {
	store := s.primaryOpenAICodexTurnStateOriginStore()
	if store == nil {
		openAICodexTurnStateMetrics.localFallbackTotal.Add(1)
		_ = processOpenAICodexTurnStateOriginStore.SetOpenAICodexTurnStateOrigin(context.Background(), key, origin, ttl)
		return
	}
	ctx, cancel := openAICodexTurnStateCacheContext(c)
	defer cancel()
	if err := store.SetOpenAICodexTurnStateOrigin(ctx, key, origin, ttl); err != nil {
		openAICodexTurnStateMetrics.storeErrorTotal.Add(1)
		openAICodexTurnStateMetrics.localFallbackTotal.Add(1)
		slog.WarnContext(ctx, "openai_codex_turn_state_store_error", "operation", "set", "mapping_digest", openAICodexMappingLogDigest(key), "error", err)
	}
	_ = processOpenAICodexTurnStateOriginStore.SetOpenAICodexTurnStateOrigin(context.Background(), key, origin, ttl)
}

func (s *OpenAIGatewayService) getOpenAICodexTurnStateOrigin(c *gin.Context, key string) (OpenAICodexTurnStateOrigin, bool) {
	store := s.primaryOpenAICodexTurnStateOriginStore()
	if store != nil {
		ctx, cancel := openAICodexTurnStateCacheContext(c)
		defer cancel()
		origin, err := store.GetOpenAICodexTurnStateOrigin(ctx, key)
		if errors.Is(err, ErrOpenAICodexTurnStateOriginNotFound) {
			localOrigin, localErr := processOpenAICodexTurnStateOriginStore.GetOpenAICodexTurnStateOrigin(context.Background(), key)
			if localErr != nil {
				return OpenAICodexTurnStateOrigin{}, false
			}
			localOrigin, valid := normalizeOpenAICodexTurnStateOrigin(localOrigin)
			ttl := time.Until(localOrigin.ExpiresAt)
			if !valid || localOrigin.ExpiresAt.IsZero() || ttl <= 0 {
				_ = processOpenAICodexTurnStateOriginStore.DeleteOpenAICodexTurnStateOrigin(context.Background(), key)
				return OpenAICodexTurnStateOrigin{}, false
			}
			openAICodexTurnStateMetrics.localFallbackTotal.Add(1)
			if promoteErr := store.SetOpenAICodexTurnStateOrigin(ctx, key, localOrigin, ttl); promoteErr != nil {
				openAICodexTurnStateMetrics.storeErrorTotal.Add(1)
				slog.WarnContext(ctx, "openai_codex_turn_state_store_error", "operation", "promote", "mapping_digest", openAICodexMappingLogDigest(key), "error", promoteErr)
			}
			return localOrigin, true
		}
		if err != nil {
			openAICodexTurnStateMetrics.storeErrorTotal.Add(1)
			openAICodexTurnStateMetrics.localFallbackTotal.Add(1)
			slog.WarnContext(ctx, "openai_codex_turn_state_store_error", "operation", "get", "mapping_digest", openAICodexMappingLogDigest(key), "error", err)
		} else {
			origin, valid := normalizeOpenAICodexTurnStateOrigin(origin)
			if !valid || (!origin.ExpiresAt.IsZero() && time.Now().After(origin.ExpiresAt)) {
				_ = store.DeleteOpenAICodexTurnStateOrigin(ctx, key)
				_ = processOpenAICodexTurnStateOriginStore.DeleteOpenAICodexTurnStateOrigin(context.Background(), key)
				return OpenAICodexTurnStateOrigin{}, false
			}
			ttl := time.Until(origin.ExpiresAt)
			if origin.ExpiresAt.IsZero() || ttl <= 0 {
				ttl = s.openAIWSSessionStickyTTL()
				origin.ExpiresAt = time.Now().Add(ttl)
			}
			_ = processOpenAICodexTurnStateOriginStore.SetOpenAICodexTurnStateOrigin(context.Background(), key, origin, ttl)
			return origin, true
		}
	} else {
		openAICodexTurnStateMetrics.localFallbackTotal.Add(1)
	}
	origin, err := processOpenAICodexTurnStateOriginStore.GetOpenAICodexTurnStateOrigin(context.Background(), key)
	if err != nil {
		return OpenAICodexTurnStateOrigin{}, false
	}
	return origin, true
}

func extractOpenAICodexTurnState(upstream http.Header) string {
	if upstream == nil {
		return ""
	}
	return strings.TrimSpace(upstream.Get(openAICodexTurnStateHeader))
}

// relayOpenAICodexTurnState prepares the downstream response header but does
// not bind provenance. The caller must bind only after the response is really
// committed; this keeps abandoned first-output attempts out of the store.
func (s *OpenAIGatewayService) relayOpenAICodexTurnState(c *gin.Context, account *Account, upstream http.Header) string {
	if account == nil || !account.IsOpenAIOAuth() || c == nil || c.Writer == nil {
		return ""
	}
	return relayOpenAICodexTurnStateHeader(c.Writer.Header(), upstream)
}

func relayOpenAICodexTurnStateHeader(dst http.Header, upstream http.Header) string {
	if dst == nil {
		return ""
	}
	canonical := http.CanonicalHeaderKey(openAICodexTurnStateHeader)
	state := extractOpenAICodexTurnState(upstream)
	dst.Del(canonical)
	if state == "" {
		return ""
	}
	dst.Set(canonical, state)
	return state
}

func stageOpenAICodexTurnState(dst *http.Header, account *Account, upstream http.Header) {
	if dst == nil || account == nil || !account.IsOpenAIOAuth() {
		return
	}
	canonical := http.CanonicalHeaderKey(openAICodexTurnStateHeader)
	state := extractOpenAICodexTurnState(upstream)
	if state == "" {
		if *dst != nil {
			(*dst).Del(canonical)
		}
		return
	}
	if *dst == nil {
		*dst = http.Header{}
	}
	(*dst).Set(canonical, state)
}

// noteOpenAICodexTurnStateProvenance binds an explicitly committed state to
// the final credential owner and normalized identity used for this request.
func (s *OpenAIGatewayService) noteOpenAICodexTurnStateProvenance(c *gin.Context, account *Account, state string) {
	requestOrigin, ok := openAICodexTurnStateRequestOriginFromContext(c, account)
	if !ok {
		return
	}
	state = strings.TrimSpace(state)
	if state == "" {
		return
	}
	key, ok := s.openAICodexTurnStateMappingKey(requestOrigin, state)
	if !ok {
		return
	}
	ttl := s.openAIWSSessionStickyTTL()
	origin := OpenAICodexTurnStateOrigin{
		CredentialOwnerNamespace: requestOrigin.namespace,
		TurnIdentityDigest:       requestOrigin.digest,
		ExpiresAt:                time.Now().Add(ttl),
	}
	s.putOpenAICodexTurnStateOrigin(c, key, origin, ttl)
	openAICodexTurnStateMetrics.noteTotal.Add(1)
}

func (s *OpenAIGatewayService) noteStagedOpenAICodexTurnStateCommitted(c *gin.Context, account *Account, staged http.Header) {
	state := extractOpenAICodexTurnState(staged)
	if state != "" {
		s.noteOpenAICodexTurnStateProvenance(c, account, state)
	}
}

// guardOpenAICodexTurnStateEcho strips only a state with known-incompatible
// provenance. Unknown, expired, or unavailable provenance remains fail-open to
// preserve pre-upgrade and externally minted state.
func (s *OpenAIGatewayService) guardOpenAICodexTurnStateEcho(c *gin.Context, account *Account, h http.Header) {
	if s == nil || h == nil {
		return
	}
	state := strings.TrimSpace(h.Get(openAICodexTurnStateHeader))
	if state == "" {
		return
	}
	requestOrigin, ok := openAICodexTurnStateRequestOriginFromContext(c, account)
	if !ok {
		return
	}
	key, ok := s.openAICodexTurnStateMappingKey(requestOrigin, state)
	if !ok {
		return
	}
	origin, found := s.getOpenAICodexTurnStateOrigin(c, key)
	if !found {
		openAICodexTurnStateMetrics.guardUnknownTotal.Add(1)
		return
	}
	if origin.CredentialOwnerNamespace != requestOrigin.namespace {
		h.Del(openAICodexTurnStateHeader)
		openAICodexTurnStateMetrics.guardStripOwner.Add(1)
		return
	}
	if origin.TurnIdentityDigest != requestOrigin.digest {
		h.Del(openAICodexTurnStateHeader)
		openAICodexTurnStateMetrics.guardStripIdentity.Add(1)
		return
	}
	openAICodexTurnStateMetrics.guardKeepTotal.Add(1)
}

var _ OpenAICodexTurnStateOriginStore = (*openAICodexTurnStateLocalStore)(nil)
