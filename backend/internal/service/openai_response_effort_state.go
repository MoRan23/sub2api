package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// OpenAIResponseEffortState is a small, versioned continuation value. A known
// record with Override=false means subsequent turns use their own baseline.
type OpenAIResponseEffortState struct {
	Version   int               `json:"version"`
	AccountID int64             `json:"account_id"`
	Identity  string            `json:"identity"`
	Override  bool              `json:"override"`
	Effort    OpenAIEffortValue `json:"effort"`
	ExpiresAt int64             `json:"expires_at"`
}
type OpenAIResponseEffortCache interface {
	SetOpenAIResponseEffort(context.Context, int64, int64, string, OpenAIResponseEffortState, time.Duration) error
	GetOpenAIResponseEffort(context.Context, int64, int64, string) (OpenAIResponseEffortState, bool, error)
}
type OpenAIResponseEffortStore interface {
	BindResponseEffort(context.Context, int64, int64, string, OpenAIResponseEffortState, time.Duration) error
	GetResponseEffort(context.Context, int64, int64, string) (OpenAIResponseEffortState, bool, error)
}
type openAIResponseEffortBinding struct{ state OpenAIResponseEffortState }

const maxOpenAIEffortResponseIDLen = 1024

func cleanupExpiredResponseEffortBindings(bindings map[string]openAIResponseEffortBinding, now time.Time, maxScan int) {
	scanned := 0
	for key, binding := range bindings {
		if binding.state.ExpiresAt <= now.UnixMilli() {
			delete(bindings, key)
		}
		scanned++
		if scanned >= maxScan {
			break
		}
	}
}

func (s *defaultOpenAIWSStateStore) BindResponseEffort(ctx context.Context, groupID, userID int64, responseID string, state OpenAIResponseEffortState, ttl time.Duration) error {
	id := normalizeOpenAIWSResponseID(responseID)
	if id == "" || len(id) > maxOpenAIEffortResponseIDLen || userID <= 0 || len(state.Identity) > 128 || len(state.Effort.Value) > maxReasoningEffortValueLen || (state.Override && !state.Effort.valid()) {
		return nil
	}
	s.maybeCleanup()
	ttl = normalizeOpenAIWSTTL(ttl)
	state.Version = 1
	state.ExpiresAt = time.Now().Add(ttl).UnixMilli()
	key := fmt.Sprintf("%d:%d:%s", groupID, userID, id)
	s.responseEffortMu.Lock()
	if s.responseEfforts == nil {
		s.responseEfforts = make(map[string]openAIResponseEffortBinding, 256)
	}
	ensureBindingCapacity(s.responseEfforts, key, openAIWSStateStoreMaxEntriesPerMap)
	s.responseEfforts[key] = openAIResponseEffortBinding{state: state}
	s.responseEffortMu.Unlock()
	cache, ok := s.cache.(OpenAIResponseEffortCache)
	if !ok {
		return nil
	}
	bounded, cancel := withOpenAIWSStateStoreRedisTimeout(ctx)
	defer cancel()
	return cache.SetOpenAIResponseEffort(bounded, groupID, userID, id, state, ttl)
}
func (s *defaultOpenAIWSStateStore) GetResponseEffort(ctx context.Context, groupID, userID int64, responseID string) (OpenAIResponseEffortState, bool, error) {
	id := normalizeOpenAIWSResponseID(responseID)
	if id == "" || len(id) > maxOpenAIEffortResponseIDLen || userID <= 0 {
		return OpenAIResponseEffortState{}, false, nil
	}
	s.maybeCleanup()
	key := fmt.Sprintf("%d:%d:%s", groupID, userID, id)
	now := time.Now().UnixMilli()
	s.responseEffortMu.RLock()
	local, found := s.responseEfforts[key]
	s.responseEffortMu.RUnlock()
	if found && local.state.ExpiresAt > now {
		return local.state, true, nil
	}
	cache, ok := s.cache.(OpenAIResponseEffortCache)
	if !ok {
		return OpenAIResponseEffortState{}, false, nil
	}
	bounded, cancel := withOpenAIWSStateStoreRedisTimeout(ctx)
	defer cancel()
	state, found, err := cache.GetOpenAIResponseEffort(bounded, groupID, userID, id)
	if err != nil || !found || state.Version != 1 || state.ExpiresAt <= now || len(state.Identity) > 128 || len(state.Effort.Value) > maxReasoningEffortValueLen || (state.Override && !state.Effort.valid()) {
		return OpenAIResponseEffortState{}, false, err
	}
	s.responseEffortMu.Lock()
	if s.responseEfforts == nil {
		s.responseEfforts = make(map[string]openAIResponseEffortBinding, 256)
	}
	ensureBindingCapacity(s.responseEfforts, key, openAIWSStateStoreMaxEntriesPerMap)
	s.responseEfforts[key] = openAIResponseEffortBinding{state: state}
	s.responseEffortMu.Unlock()
	return state, true, nil
}

func openAIEffortIdentity(account *Account) string {
	if account == nil {
		return ""
	}
	// Only credential identity scalars are hashed, never the request or catalog.
	identity := fmt.Sprintf("%d:%s:%s:%d:%s:%d:%s:%s", account.ID, account.Type, account.OpenAIOAuthCredentialOS, account.OpenAIOAuthCredentialOwnerID, account.OpenAIOAuthAuthorizationGeneration, account.OpenAIOAuthCredentialRevision, account.OpenAIOAuthCredentialEpoch, account.GetCredential("base_url"))
	if account.ProxyID != nil {
		identity += fmt.Sprintf(":proxy:%d", *account.ProxyID)
	}
	if !account.IsOpenAIOAuth() {
		identity += "\x00" + account.GetCredential("api_key") + "\x00" + account.GetCredential("access_token")
	}
	sum := sha256.Sum256([]byte(identity))
	return hex.EncodeToString(sum[:])
}

func openAIEffortUserID(c *gin.Context) int64 {
	if c == nil {
		return 0
	}
	if key := getAPIKeyFromContext(c); key != nil {
		return key.UserID
	}
	return 0
}

// PrepareOpenAIReasoningEffortContext loads at most one tiny continuation value.
// Calling again with a selected account verifies identity without another read.
func (s *OpenAIGatewayService) PrepareOpenAIReasoningEffortContext(ctx context.Context, c *gin.Context, body []byte, account *Account) (context.Context, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	view := parseRawJSONView(body)
	previous := strings.TrimSpace(view.Get("previous_response_id").Str)
	if len(previous) > maxOpenAIEffortResponseIDLen {
		return ctx, &ReasoningEffortContinuationError{Reason: "previous_response_id exceeds the 1024-byte limit"}
	}
	model := strings.TrimSpace(view.Get("model").Str)
	if len(model) > maxReasoningEffortModelLen {
		return ctx, &ReasoningEffortContinuationError{Reason: "model exceeds the 200-byte limit"}
	}
	state := currentOpenAIEffortState(ctx)
	created := state == nil || state.previousID != previous
	identityChanged := false
	if created {
		ctx = context.WithValue(ctx, requestedReasoningEffortContextKey{}, "")
		state = &openAIEffortRequestState{previousID: strings.Clone(previous), originalModel: strings.Clone(model)}
		if previous != "" && s != nil {
			if store, ok := s.getOpenAIWSStateStore().(OpenAIResponseEffortStore); ok {
				value, found, err := store.GetResponseEffort(ctx, getOpenAIGroupIDFromContext(c), openAIEffortUserID(c), previous)
				if err == nil {
					state.inherited, state.found = value, found
				}
			}
		}
		ctx = context.WithValue(ctx, openAIEffortContextKey{}, state)
	}
	if account != nil && state.found && (state.inherited.AccountID != account.ID || state.inherited.Identity != openAIEffortIdentity(account)) {
		identityChanged = true
		copyState := *state
		copyState.found = false
		state = &copyState
		ctx = context.WithValue(ctx, openAIEffortContextKey{}, state)
		selected := selectOpenAIEffort(body, state)
		state.requested, state.effective, state.override = selected.value, selected.value, selected.override
		state.selection, state.prepared = selected, true
		ctx = context.WithValue(ctx, requestedReasoningEffortContextKey{}, selected.value.Value)
	}
	if !created && !identityChanged {
		return ctx, nil
	}
	selected := selectOpenAIEffort(body, state)
	if selected.oversized {
		return ctx, &ReasoningEffortContinuationError{Reason: "effort string exceeds the 64-byte limit"}
	}
	if created {
		state.requested, state.effective, state.override = selected.value, selected.value, selected.override
		state.selection, state.prepared = selected, true
	}
	if created && selected.value.valid() {
		ctx = WithRequestedReasoningEffort(ctx, selected.value.Value)
	} else if created && !selected.unknown {
		if requested := canonicalReasoningEffortFromModelSuffix(model); requested != "" {
			ctx = WithRequestedReasoningEffort(ctx, requested)
		}
	}
	if policy, ok := ctx.Value(openAIReasoningEffortPolicyContextKey{}).(openAIReasoningEffortPolicy); ok && policy.maxEffort != "" && selected.unknown {
		return ctx, &ReasoningEffortContinuationError{Reason: "continuation effort unavailable for the selected upstream; send a complete request or configuration_update"}
	}
	return ctx, nil
}

func (s *OpenAIGatewayService) prepareOpenAIWSEffort(ctx context.Context, c *gin.Context, body []byte, account *Account, hooks *OpenAIWSIngressHooks) (context.Context, []byte, error) {
	// A fresh per-turn state prevents later frames mutating contexts retained by
	// bounded asynchronous accounting. The previous response remains a tiny hit.
	ctx = context.WithValue(ctx, openAIEffortContextKey{}, (*openAIEffortRequestState)(nil))
	ctx = context.WithValue(ctx, requestedReasoningEffortContextKey{}, "")
	if hooks != nil {
		ctx = WithOpenAIReasoningEffortPolicy(ctx, hooks.MaxReasoningEffort, hooks.ReasoningEffortMappings, hooks.MaxReasoningEffortOverLimit)
	}
	ctx, err := s.PrepareOpenAIReasoningEffortContext(ctx, c, body, account)
	if err != nil {
		return ctx, body, err
	}
	result, _, err := ApplyOpenAIReasoningEffortPolicyFromContext(ctx, body)
	return ctx, result, err
}

// RecordOpenAIResponseEffort binds only the final successfully accepted state.
// A complete request (no previous_response_id) naturally starts a fresh lineage.
func (s *OpenAIGatewayService) RecordOpenAIResponseEffort(ctx context.Context, c *gin.Context, body []byte, responseID string, account *Account) error {
	if s == nil || account == nil || responseID == "" {
		return nil
	}
	store, ok := s.getOpenAIWSStateStore().(OpenAIResponseEffortStore)
	if !ok {
		return nil
	}
	state := currentOpenAIEffortState(ctx)
	selected := selectOpenAIEffort(body, state)
	if state != nil && state.previousID != "" && !state.effective.valid() && !state.found {
		return nil
	}
	if selected.unknown {
		return nil
	}
	value := selected.value
	override := selected.override
	if state != nil && state.effective.valid() {
		value = state.effective
		override = state.override
	}
	return store.BindResponseEffort(ctx, getOpenAIGroupIDFromContext(c), openAIEffortUserID(c), responseID, OpenAIResponseEffortState{AccountID: account.ID, Identity: openAIEffortIdentity(account), Override: override, Effort: value}, s.openAIWSResponseStickyTTL())
}
