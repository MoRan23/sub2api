package service

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// The API key remains a separate mapping-key component. Upstream accounts,
// groups and credential owners never participate in this namespace.
const OpenAICodexDownstreamIdentityNamespace = "downstream:v1"

var (
	ErrOpenAICodexDownstreamIdentityScopeMissing     = errors.New("OpenAI Codex downstream identity requires an API key or a server-owned synthetic scope")
	ErrOpenAICodexDownstreamIdentityStoreUnavailable = errors.New("OpenAI Codex downstream identity store is unavailable")
)

type OpenAICodexDownstreamSessionRequest struct {
	SessionMappingKeys       []string
	LegacySessionMappingKeys []string
	LegacyNamespace          string
	CandidateSessionID       string
}

// LegacyNamespace is claimed atomically with SessionID. An empty value is a
// permanent decision to start a new session, not permission to migrate later.
type OpenAICodexDownstreamSessionResolution struct {
	SessionID       string `json:"session_id"`
	LegacyNamespace string `json:"legacy_namespace"`
	Reused          bool   `json:"-"`
}

type OpenAICodexDownstreamThreadRequest struct {
	SessionMappingKeys   []string
	ThreadMappings       []OpenAICodexThreadAliasMapping
	LegacyThreadMappings []OpenAICodexThreadAliasMapping
	SessionID            string
	LegacyNamespace      string
	CandidateThreadID    string
}

type OpenAICodexDownstreamIdentityStore interface {
	ResolveCodexDownstreamSession(context.Context, OpenAICodexDownstreamSessionRequest, time.Duration) (OpenAICodexDownstreamSessionResolution, error)
	ResolveCodexDownstreamThread(context.Context, OpenAICodexDownstreamThreadRequest, time.Duration) (OpenAICodexTurnIdentity, error)
}

func downstreamSessionEntryKey(key string) string { return "downstream-session:" + key }
func downstreamThreadEntryKey(session, thread string) string {
	return "downstream-thread:" + session + ":" + thread
}

func validateCodexDownstreamScope(namespace string, apiKeyID int64) error {
	if namespace == OpenAICodexDownstreamIdentityNamespace && apiKeyID > 0 {
		return nil
	}
	if strings.HasPrefix(namespace, "synthetic:") {
		candidate := strings.TrimPrefix(namespace, "synthetic:")
		if canonical, err := canonicalUUIDv7(candidate); err == nil && candidate == canonical {
			return nil
		}
	}
	return ErrOpenAICodexDownstreamIdentityScopeMissing
}

func validCodexDownstreamMappingKey(key string) bool {
	if len(key) != 64 || key != strings.ToLower(key) {
		return false
	}
	_, err := hex.DecodeString(key)
	return err == nil
}

func validCodexDownstreamLegacyNamespace(namespace string) bool {
	if namespace == "" {
		return true
	}
	if !strings.HasPrefix(namespace, "account:") {
		return false
	}
	id := strings.TrimPrefix(namespace, "account:")
	parsed, err := strconv.ParseInt(id, 10, 64)
	return err == nil && parsed > 0 && strconv.FormatInt(parsed, 10) == id
}

func validateCodexDownstreamLegacySource(namespace string, hasMappings bool) error {
	if !validCodexDownstreamLegacyNamespace(namespace) || (namespace == "" && hasMappings) {
		return errors.New("openai Codex downstream identity has invalid legacy namespace")
	}
	return nil
}

func validateCodexDownstreamSessionKeys(keys []string) error {
	if len(keys) == 0 {
		return errOpenAIOutboundSessionIdentityKeyEmpty
	}
	for _, key := range keys {
		if !validCodexDownstreamMappingKey(key) {
			return errors.New("openai Codex downstream session mapping key must be a lowercase SHA-256 digest")
		}
	}
	return nil
}

func validateCodexDownstreamThreadMappings(request OpenAICodexDownstreamThreadRequest) error {
	if err := validateCodexDownstreamSessionKeys(request.SessionMappingKeys); err != nil {
		return err
	}
	if len(request.ThreadMappings) == 0 {
		return errOpenAIOutboundSessionIdentityKeyEmpty
	}
	if err := validateCodexDownstreamLegacySource(request.LegacyNamespace, len(request.LegacyThreadMappings) > 0); err != nil {
		return err
	}
	sessions := make(map[string]struct{}, len(request.SessionMappingKeys))
	for _, key := range request.SessionMappingKeys {
		sessions[key] = struct{}{}
	}
	for _, mapping := range request.ThreadMappings {
		if !validCodexDownstreamMappingKey(mapping.SessionMappingKey) || !validCodexDownstreamMappingKey(mapping.ThreadMappingKey) {
			return errors.New("openai Codex downstream thread mapping keys must be lowercase SHA-256 digests")
		}
		if _, exists := sessions[mapping.SessionMappingKey]; !exists {
			return errors.New("openai Codex downstream thread alias has no corresponding session alias")
		}
	}
	for _, mapping := range request.LegacyThreadMappings {
		if !validCodexDownstreamMappingKey(mapping.SessionMappingKey) || !validCodexDownstreamMappingKey(mapping.ThreadMappingKey) {
			return errors.New("openai Codex legacy thread mapping keys must be lowercase SHA-256 digests")
		}
	}
	return nil
}

func validCodexDownstreamStoredIdentity(identity OpenAICodexTurnIdentity, root bool) bool {
	if err := ValidateOpenAICodexTurnIdentity(identity); err != nil {
		return false
	}
	return identity.SessionID == strings.TrimSpace(identity.SessionID) &&
		identity.ThreadID == strings.TrimSpace(identity.ThreadID) &&
		(identity.SessionID == identity.ThreadID) == root
}

func downstreamSessionFromEntry(entry *openAICodexLocalEntry) OpenAICodexDownstreamSessionResolution {
	return OpenAICodexDownstreamSessionResolution{SessionID: entry.identity.SessionID, LegacyNamespace: entry.legacyNamespace, Reused: true}
}

func (s *openAICodexIdentityLocalStore) lookupDownstreamSessionLocked(keys []string, ttl time.Duration) (OpenAICodexDownstreamSessionResolution, bool, error) {
	var winner OpenAICodexDownstreamSessionResolution
	for _, key := range keys {
		entry := s.entries[downstreamSessionEntryKey(key)]
		if entry == nil {
			continue
		}
		if !validCodexDownstreamStoredIdentity(entry.identity, true) || !validCodexDownstreamLegacyNamespace(entry.legacyNamespace) {
			return winner, false, ErrOpenAIOutboundSessionIdentityStoredValueInvalid
		}
		if winner.SessionID != "" && (winner.SessionID != entry.identity.SessionID || winner.LegacyNamespace != entry.legacyNamespace) {
			return winner, false, ErrOpenAICodexAliasConflict
		}
		winner = downstreamSessionFromEntry(entry)
	}
	for _, key := range keys {
		if entry := s.entries[downstreamSessionEntryKey(key)]; entry != nil {
			s.touchLocked(entry, ttl)
		}
	}
	return winner, winner.SessionID != "", nil
}

func (s *openAICodexIdentityLocalStore) cacheDownstreamSessionLocked(keys []string, resolved OpenAICodexDownstreamSessionResolution, ttl time.Duration) {
	identity := OpenAICodexTurnIdentity{SessionID: resolved.SessionID, ThreadID: resolved.SessionID, Relation: OpenAICodexTurnRelationRoot}
	for _, key := range keys {
		entryKey := downstreamSessionEntryKey(key)
		entry := s.entries[entryKey]
		if entry == nil {
			entry = s.putLocked(entryKey, identity, ttl)
		} else {
			entry.identity = identity
			s.touchLocked(entry, ttl)
		}
		entry.legacyNamespace = resolved.LegacyNamespace
	}
}

func (s *openAICodexIdentityLocalStore) ResolveCodexDownstreamSession(_ context.Context, request OpenAICodexDownstreamSessionRequest, ttl time.Duration) (OpenAICodexDownstreamSessionResolution, error) {
	if err := validateCodexDownstreamSessionKeys(request.SessionMappingKeys); err != nil {
		return OpenAICodexDownstreamSessionResolution{}, err
	}
	if err := validateCodexDownstreamLegacySource(request.LegacyNamespace, len(request.LegacySessionMappingKeys) > 0); err != nil {
		return OpenAICodexDownstreamSessionResolution{}, err
	}
	for _, key := range request.LegacySessionMappingKeys {
		if !validCodexDownstreamMappingKey(key) {
			return OpenAICodexDownstreamSessionResolution{}, errors.New("openai Codex legacy session mapping key must be a lowercase SHA-256 digest")
		}
	}
	root := OpenAICodexTurnIdentity{SessionID: request.CandidateSessionID, ThreadID: request.CandidateSessionID, Relation: OpenAICodexTurnRelationRoot}
	if !validCodexDownstreamStoredIdentity(root, true) {
		return OpenAICodexDownstreamSessionResolution{}, errors.New("openai Codex downstream session candidate must be a canonical UUIDv7")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(time.Now())
	resolved, found, err := s.lookupDownstreamSessionLocked(request.SessionMappingKeys, ttl)
	if err != nil {
		return resolved, err
	}
	if !found {
		for _, legacyKey := range request.LegacySessionMappingKeys {
			entry := s.entries[localSessionEntryKey(legacyKey)]
			if entry == nil {
				continue
			}
			if !validCodexDownstreamStoredIdentity(entry.identity, true) {
				return resolved, ErrOpenAIOutboundSessionIdentityStoredValueInvalid
			}
			if resolved.SessionID == "" {
				resolved = OpenAICodexDownstreamSessionResolution{SessionID: entry.identity.SessionID, LegacyNamespace: request.LegacyNamespace, Reused: true}
			}
		}
	}
	if resolved.SessionID == "" {
		resolved.SessionID = request.CandidateSessionID
	}
	s.cacheDownstreamSessionLocked(request.SessionMappingKeys, resolved, ttl)
	return resolved, nil
}

func (s *openAICodexIdentityLocalStore) lookupDownstreamThreadLocked(request OpenAICodexDownstreamThreadRequest, ttl time.Duration) (OpenAICodexTurnIdentity, bool, error) {
	for _, key := range request.SessionMappingKeys {
		entry := s.entries[downstreamSessionEntryKey(key)]
		if entry == nil {
			return OpenAICodexTurnIdentity{}, false, ErrOpenAICodexSessionWinnerChanged
		}
		if !validCodexDownstreamStoredIdentity(entry.identity, true) || !validCodexDownstreamLegacyNamespace(entry.legacyNamespace) {
			return OpenAICodexTurnIdentity{}, false, ErrOpenAIOutboundSessionIdentityStoredValueInvalid
		}
		if entry.identity.SessionID != request.SessionID || entry.legacyNamespace != request.LegacyNamespace {
			return OpenAICodexTurnIdentity{}, false, ErrOpenAICodexSessionWinnerChanged
		}
	}
	var winner OpenAICodexTurnIdentity
	for _, mapping := range request.ThreadMappings {
		entry := s.entries[downstreamThreadEntryKey(mapping.SessionMappingKey, mapping.ThreadMappingKey)]
		if entry == nil {
			continue
		}
		if !validCodexDownstreamStoredIdentity(entry.identity, false) || entry.identity.SessionID != request.SessionID {
			return winner, false, ErrOpenAIOutboundSessionIdentityStoredValueInvalid
		}
		if winner.ThreadID != "" && winner.ThreadID != entry.identity.ThreadID {
			return winner, false, ErrOpenAICodexAliasConflict
		}
		winner = entry.identity
	}
	if winner.ThreadID != "" {
		for _, mapping := range request.ThreadMappings {
			if entry := s.entries[downstreamThreadEntryKey(mapping.SessionMappingKey, mapping.ThreadMappingKey)]; entry != nil {
				s.touchLocked(entry, ttl)
			}
		}
		for _, key := range request.SessionMappingKeys {
			s.touchLocked(s.entries[downstreamSessionEntryKey(key)], ttl)
		}
	}
	return winner, winner.ThreadID != "", nil
}

func (s *openAICodexIdentityLocalStore) cacheDownstreamThreadLocked(request OpenAICodexDownstreamThreadRequest, identity OpenAICodexTurnIdentity, ttl time.Duration) {
	for _, mapping := range request.ThreadMappings {
		key := downstreamThreadEntryKey(mapping.SessionMappingKey, mapping.ThreadMappingKey)
		if entry := s.entries[key]; entry != nil {
			entry.identity = identity
			s.touchLocked(entry, ttl)
		} else {
			s.putLocked(key, identity, ttl)
		}
	}
	for _, key := range request.SessionMappingKeys {
		if entry := s.entries[downstreamSessionEntryKey(key)]; entry != nil {
			s.touchLocked(entry, ttl)
		}
	}
}

func (s *openAICodexIdentityLocalStore) ResolveCodexDownstreamThread(_ context.Context, request OpenAICodexDownstreamThreadRequest, ttl time.Duration) (OpenAICodexTurnIdentity, error) {
	candidate := OpenAICodexTurnIdentity{SessionID: request.SessionID, ThreadID: request.CandidateThreadID, Relation: OpenAICodexTurnRelationDescendant}
	if !validCodexDownstreamStoredIdentity(candidate, false) {
		return OpenAICodexTurnIdentity{}, errors.New("openai Codex downstream thread candidate must contain canonical UUIDv7 values")
	}
	if err := validateCodexDownstreamThreadMappings(request); err != nil {
		return OpenAICodexTurnIdentity{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(time.Now())
	identity, found, err := s.lookupDownstreamThreadLocked(request, ttl)
	if err != nil {
		return identity, err
	}
	if !found {
		for _, mapping := range request.LegacyThreadMappings {
			session := s.entries[localSessionEntryKey(mapping.SessionMappingKey)]
			if session == nil {
				continue
			}
			if !validCodexDownstreamStoredIdentity(session.identity, true) {
				return identity, ErrOpenAIOutboundSessionIdentityStoredValueInvalid
			}
			if session.identity.SessionID != request.SessionID {
				continue
			}
			entry := s.entries[localThreadEntryKey(mapping.SessionMappingKey, mapping.ThreadMappingKey)]
			if entry == nil {
				continue
			}
			if !validCodexDownstreamStoredIdentity(entry.identity, false) || entry.identity.SessionID != request.SessionID {
				return identity, ErrOpenAIOutboundSessionIdentityStoredValueInvalid
			}
			if identity.ThreadID == "" {
				identity = entry.identity
			}
		}
	}
	if identity.ThreadID == "" {
		identity = candidate
	}
	s.cacheDownstreamThreadLocked(request, identity, ttl)
	return identity, nil
}

type openAICodexDownstreamResolutionState struct {
	ctx               context.Context
	local             *openAICodexIdentityLocalStore
	store             OpenAICodexDownstreamIdentityStore
	primaryConfigured bool
	namespace         string
	apiKeyID          int64
	secret            string
	logical           OpenAICodexLogicalTurnIdentity
	aliases           []OpenAICodexLogicalTurnAlias
	sessionKeys       []string
	session           OpenAICodexDownstreamSessionResolution
	outcome           OpenAIOAuthIdentityResolveOutcome
}

func (state *openAICodexDownstreamResolutionState) sessionKey(namespace, logical string) string {
	key, err := OpenAICodexSessionMappingKey(state.secret, namespace, state.apiKeyID, logical)
	if err != nil {
		return openAICodexFallbackMappingKey(openAICodexSessionIdentityDomain, namespace, state.apiKeyID, logical)
	}
	return key
}

func (state *openAICodexDownstreamResolutionState) threadKey(namespace, session, thread string) string {
	key, err := OpenAICodexThreadMappingKey(state.secret, namespace, state.apiKeyID, session, thread, state.session.SessionID)
	if err != nil {
		return openAICodexFallbackMappingKey(openAICodexThreadIdentityDomain, namespace, state.apiKeyID, session, thread, state.session.SessionID)
	}
	return key
}

func (state *openAICodexDownstreamResolutionState) sessionMappingKeys(namespace string) []string {
	keys := []string{state.sessionKey(namespace, state.logical.SessionKey)}
	for _, alias := range state.aliases {
		keys = append(keys, state.sessionKey(namespace, alias.SessionKey))
	}
	return uniqueNonEmptyStrings(keys)
}

func (state *openAICodexDownstreamResolutionState) resolveSession(legacyNamespace string) error {
	fresh, err := newOpenAICodexRootIdentity()
	if err != nil {
		return err
	}
	request := OpenAICodexDownstreamSessionRequest{SessionMappingKeys: state.sessionKeys, CandidateSessionID: fresh.SessionID, LegacyNamespace: legacyNamespace}
	if legacyNamespace != "" {
		request.LegacySessionMappingKeys = state.sessionMappingKeys(legacyNamespace)
	}
	if !state.primaryConfigured {
		state.session, err = state.local.ResolveCodexDownstreamSession(state.ctx, request, OpenAIOutboundSessionIdentityTTL)
		state.outcome = OpenAIOAuthIdentityResolveFallback
		openAIOutboundSessionIdentityMetrics.localFallbackTotal.Add(1)
		return err
	}
	err = ErrOpenAICodexDownstreamIdentityStoreUnavailable
	started := time.Now()
	if state.store != nil {
		state.session, err = state.store.ResolveCodexDownstreamSession(state.ctx, request, OpenAIOutboundSessionIdentityTTL)
	}
	observeOpenAICodexStoreLatency(started)
	if err != nil {
		openAIOutboundSessionIdentityMetrics.primaryStoreFailureTotal.Add(1)
		state.local.mu.Lock()
		state.local.pruneLocked(time.Now())
		cached, found, localErr := state.local.lookupDownstreamSessionLocked(state.sessionKeys, OpenAIOutboundSessionIdentityTTL)
		if found && localErr == nil && !errors.Is(err, ErrOpenAICodexAliasConflict) && !errors.Is(err, ErrOpenAIOutboundSessionIdentityStoredValueInvalid) {
			state.local.cacheDownstreamSessionLocked(state.sessionKeys, cached, OpenAIOutboundSessionIdentityTTL)
			state.local.mu.Unlock()
			state.session = cached
			state.outcome = OpenAIOAuthIdentityResolveStoreError
			openAIOutboundSessionIdentityMetrics.localFallbackTotal.Add(1)
			return nil
		}
		state.local.mu.Unlock()
		return fmt.Errorf("resolve downstream Codex session: %w: %w", ErrOpenAICodexDownstreamIdentityStoreUnavailable, err)
	}
	if id, err := canonicalUUIDv7(state.session.SessionID); err != nil || id != state.session.SessionID || !validCodexDownstreamLegacyNamespace(state.session.LegacyNamespace) {
		return ErrOpenAIOutboundSessionIdentityStoredValueInvalid
	}
	state.local.mu.Lock()
	state.local.cacheDownstreamSessionLocked(state.sessionKeys, state.session, OpenAIOutboundSessionIdentityTTL)
	state.local.mu.Unlock()
	state.outcome = OpenAIOAuthIdentityResolvePrimary
	openAIOutboundSessionIdentityMetrics.primaryStoreSuccessTotal.Add(1)
	return nil
}

func (state *openAICodexDownstreamResolutionState) threadMappings(namespace, logicalThread string) []OpenAICodexThreadAliasMapping {
	keys := []OpenAICodexThreadAliasMapping{{SessionMappingKey: state.sessionKey(namespace, state.logical.SessionKey), ThreadMappingKey: state.threadKey(namespace, state.logical.SessionKey, logicalThread)}}
	if logicalThread == state.logical.ThreadKey {
		for _, alias := range state.aliases {
			if alias.SessionKey != alias.ThreadKey {
				keys = append(keys, OpenAICodexThreadAliasMapping{SessionMappingKey: state.sessionKey(namespace, alias.SessionKey), ThreadMappingKey: state.threadKey(namespace, alias.SessionKey, alias.ThreadKey)})
			}
		}
	}
	return uniqueOpenAICodexThreadAliasMappings(keys)
}

func (state *openAICodexDownstreamResolutionState) resolveThread(logicalThread string) (string, error) {
	if logicalThread == "" || logicalThread == state.logical.SessionKey {
		return state.session.SessionID, nil
	}
	fresh, err := newOpenAICodexDescendantIdentity(state.session.SessionID)
	if err != nil {
		return "", err
	}
	request := OpenAICodexDownstreamThreadRequest{SessionMappingKeys: state.sessionKeys, ThreadMappings: state.threadMappings(state.namespace, logicalThread), SessionID: state.session.SessionID, LegacyNamespace: state.session.LegacyNamespace, CandidateThreadID: fresh.ThreadID}
	if request.LegacyNamespace != "" {
		request.LegacyThreadMappings = state.threadMappings(request.LegacyNamespace, logicalThread)
	}
	if !state.primaryConfigured {
		identity, err := state.local.ResolveCodexDownstreamThread(state.ctx, request, OpenAIOutboundSessionIdentityTTL)
		openAIOutboundSessionIdentityMetrics.localFallbackTotal.Add(1)
		return identity.ThreadID, err
	}
	var identity OpenAICodexTurnIdentity
	err = ErrOpenAICodexDownstreamIdentityStoreUnavailable
	started := time.Now()
	if state.store != nil {
		identity, err = state.store.ResolveCodexDownstreamThread(state.ctx, request, OpenAIOutboundSessionIdentityTTL)
	}
	observeOpenAICodexStoreLatency(started)
	if err != nil {
		openAIOutboundSessionIdentityMetrics.primaryStoreFailureTotal.Add(1)
		state.local.mu.Lock()
		state.local.pruneLocked(time.Now())
		cached, found, localErr := state.local.lookupDownstreamThreadLocked(request, OpenAIOutboundSessionIdentityTTL)
		state.local.mu.Unlock()
		if found && localErr == nil && !errors.Is(err, ErrOpenAICodexAliasConflict) && !errors.Is(err, ErrOpenAIOutboundSessionIdentityStoredValueInvalid) && !errors.Is(err, ErrOpenAICodexSessionWinnerChanged) {
			state.outcome = OpenAIOAuthIdentityResolveStoreError
			openAIOutboundSessionIdentityMetrics.localFallbackTotal.Add(1)
			return cached.ThreadID, nil
		}
		return "", fmt.Errorf("resolve downstream Codex thread: %w: %w", ErrOpenAICodexDownstreamIdentityStoreUnavailable, err)
	}
	if !validCodexDownstreamStoredIdentity(identity, false) || identity.SessionID != state.session.SessionID {
		return "", ErrOpenAIOutboundSessionIdentityStoredValueInvalid
	}
	state.local.mu.Lock()
	state.local.cacheDownstreamThreadLocked(request, identity, OpenAIOutboundSessionIdentityTTL)
	state.local.mu.Unlock()
	openAIOutboundSessionIdentityMetrics.primaryStoreSuccessTotal.Add(1)
	return identity.ThreadID, nil
}

func (s *OpenAIGatewayService) resolveOpenAICodexTurnIdentityWithAliasesDetailed(ctx context.Context, c *gin.Context, account *Account, logical OpenAICodexLogicalTurnIdentity, aliases []OpenAICodexLogicalTurnAlias) (OpenAICodexTurnIdentity, bool, OpenAIOAuthIdentityResolveOutcome, error) {
	identity, ok, outcome, _, err := s.resolveOpenAICodexDownstreamTurnIdentityDetailed(ctx, c, account, logical, aliases, OpenAICodexDownstreamIdentityNamespace)
	return identity, ok, outcome, err
}

func (s *OpenAIGatewayService) resolveOpenAICodexDownstreamTurnIdentityDetailed(ctx context.Context, c *gin.Context, account *Account, logical OpenAICodexLogicalTurnIdentity, aliases []OpenAICodexLogicalTurnAlias, namespace string) (OpenAICodexTurnIdentity, bool, OpenAIOAuthIdentityResolveOutcome, string, error) {
	openAIOutboundSessionIdentityMetrics.resolveTotal.Add(1)
	if c != nil {
		c.Set(newOpenAICodexSessionContextKey, "")
	}
	logical = normalizeLogicalTuple(openAICodexLogicalTuple{session: logical.SessionKey, thread: logical.ThreadKey, parent: logical.ParentThreadKey, fork: logical.ForkedFromThreadKey}, logical.Source, logical.Explicit)
	if logical.SessionKey == "" {
		openAIOutboundSessionIdentityMetrics.emptyLogicalKeyTotal.Add(1)
		return OpenAICodexTurnIdentity{}, false, OpenAIOAuthIdentityResolveNone, "", nil
	}
	apiKeyID := getAPIKeyIDFromContext(c)
	if err := validateCodexDownstreamScope(namespace, apiKeyID); err != nil {
		return OpenAICodexTurnIdentity{}, true, OpenAIOAuthIdentityResolveNone, "", err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	state := &openAICodexDownstreamResolutionState{ctx: ctx, local: processOpenAICodexTurnIdentityStore, namespace: namespace, apiKeyID: apiKeyID, logical: logical}
	if s != nil && s.cfg != nil {
		state.secret = s.cfg.JWT.Secret
	}
	if s != nil && s.cache != nil {
		state.primaryConfigured = true
		state.store, _ = s.cache.(OpenAICodexDownstreamIdentityStore)
	}
	if logical.Explicit {
		for _, alias := range aliases {
			endpointAlias := alias.Source == OpenAIOutboundSessionLogicalKeySourceCallerSeed
			if !alias.Explicit && !endpointAlias {
				continue
			}
			alias.SessionKey, alias.ThreadKey = sanitizeSessionID(alias.SessionKey), sanitizeSessionID(alias.ThreadKey)
			if alias.SessionKey == "" || alias.ThreadKey == "" || (!endpointAlias && (alias.SessionKey != logical.SessionKey || alias.ThreadKey != logical.ThreadKey)) {
				continue
			}
			state.aliases = append(state.aliases, alias)
		}
	}
	state.sessionKeys = state.sessionMappingKeys(namespace)
	legacyNamespace := ""
	if namespace == OpenAICodexDownstreamIdentityNamespace && account != nil && account.ID > 0 {
		var err error
		legacyNamespace, err = s.resolveOpenAIOutboundSessionIdentityNamespace(ctx, account)
		if err != nil {
			return OpenAICodexTurnIdentity{}, true, OpenAIOAuthIdentityResolveNone, "", err
		}
	}
	if err := state.resolveSession(legacyNamespace); err != nil {
		return OpenAICodexTurnIdentity{}, true, OpenAIOAuthIdentityResolveStoreError, "", err
	}
	threadID, err := state.resolveThread(logical.ThreadKey)
	if err != nil {
		return OpenAICodexTurnIdentity{}, true, OpenAIOAuthIdentityResolveStoreError, state.session.LegacyNamespace, err
	}
	identity := OpenAICodexTurnIdentity{SessionID: state.session.SessionID, ThreadID: threadID, Relation: OpenAICodexTurnRelationDescendant}
	if identity.SessionID == identity.ThreadID {
		identity.Relation = OpenAICodexTurnRelationRoot
	}
	if logical.ParentThreadKey != "" {
		identity.ParentThreadID, err = state.resolveThread(logical.ParentThreadKey)
		if err != nil {
			return OpenAICodexTurnIdentity{}, true, OpenAIOAuthIdentityResolveStoreError, state.session.LegacyNamespace, err
		}
	}
	if logical.ForkedFromThreadKey != "" {
		identity.ForkedFromThreadID, err = state.resolveThread(logical.ForkedFromThreadKey)
		if err != nil {
			return OpenAICodexTurnIdentity{}, true, OpenAIOAuthIdentityResolveStoreError, state.session.LegacyNamespace, err
		}
	}
	if err := ValidateOpenAICodexTurnIdentity(identity); err != nil {
		return OpenAICodexTurnIdentity{}, true, OpenAIOAuthIdentityResolveStoreError, state.session.LegacyNamespace, err
	}
	if c != nil && !state.session.Reused {
		c.Set(newOpenAICodexSessionContextKey, identity.SessionID)
	}
	return identity, true, state.outcome, state.session.LegacyNamespace, nil
}

var _ OpenAICodexDownstreamIdentityStore = (*openAICodexIdentityLocalStore)(nil)
