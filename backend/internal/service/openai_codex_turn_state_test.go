package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/util/responseheaders"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const (
	turnStateSessionA = "018f5c3c-6e3a-7abc-8def-1234567890ab"
	turnStateThreadA  = "018f5c3c-6e3a-7abd-8def-1234567890ac"
	turnStateSessionB = "018f5c3c-6e3a-7abe-8def-1234567890ad"
	turnStateTurnA    = "018f5c3c-6e3a-7abf-8def-1234567890ae"
	turnStateTurnB    = "018f5c3c-6e3a-7ac0-8def-1234567890af"
)

func newTurnStateTestContext(t *testing.T, apiKeyID int64, sessionID string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	if sessionID != "" {
		c.Request.Header.Set("session_id", sessionID)
	}
	if apiKeyID > 0 {
		c.Set("api_key", &APIKey{ID: apiKeyID})
	}
	return c, rec
}

func newTurnStateIdentityTestContext(t *testing.T, apiKeyID int64, namespace, installationID, sessionID, threadID string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	c, rec := newTurnStateTestContext(t, apiKeyID, sessionID)
	plan := OpenAIOAuthIdentityPlan{
		APIKeyID:                 apiKeyID,
		CredentialOwnerNamespace: namespace,
		InstallationPolicy:       OpenAIOAuthInstallationAccountPin,
		InstallationEnabled:      installationID != "",
		InstallationID:           installationID,
		TurnIdentityRequested:    sessionID != "",
		TurnIdentityEnabled:      sessionID != "",
		RequestTurn: OpenAICodexRequestTurnSnapshot{
			ID:              turnStateTurnA,
			StartedAtUnixMS: 1723000000000,
			Source:          openAICodexRequestTurnSourceGenerated,
			Generated:       true,
		},
		TurnIdentity: OpenAICodexTurnIdentity{
			SessionID: sessionID,
			ThreadID:  threadID,
			Relation:  OpenAICodexTurnRelationRoot,
		},
	}
	if sessionID != threadID {
		plan.TurnIdentity.Relation = OpenAICodexTurnRelationDescendant
	}
	SetOpenAIOAuthIdentityPlan(c, plan)
	return c, rec
}

func newTurnStateTestService() *OpenAIGatewayService {
	return &OpenAIGatewayService{cfg: &config.Config{JWT: config.JWTConfig{Secret: "turn-state-test-secret"}}}
}

func resetTurnStateLocalStore(t *testing.T, maxEntries int) *openAICodexTurnStateLocalStore {
	t.Helper()
	previous := processOpenAICodexTurnStateOriginStore
	store := newOpenAICodexTurnStateLocalStore(maxEntries)
	processOpenAICodexTurnStateOriginStore = store
	t.Cleanup(func() { processOpenAICodexTurnStateOriginStore = previous })
	return store
}

func oauthTurnStateAccount(id int64) *Account {
	return &Account{ID: id, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
}

func TestOpenAICodexTurnStateProvenanceKey(t *testing.T) {
	key, err := OpenAICodexTurnStateProvenanceKey("secret", 7, "opaque-state-value")
	require.NoError(t, err)
	require.Len(t, key, 64)
	require.NotContains(t, key, "opaque-state-value")
	same, err := OpenAICodexTurnStateProvenanceKey("secret", 7, "opaque-state-value")
	require.NoError(t, err)
	require.Equal(t, key, same)
	differentAPIKey, err := OpenAICodexTurnStateProvenanceKey("secret", 8, "opaque-state-value")
	require.NoError(t, err)
	differentState, err := OpenAICodexTurnStateProvenanceKey("secret", 7, "other-state")
	require.NoError(t, err)
	require.NotEqual(t, key, differentAPIKey)
	require.NotEqual(t, key, differentState)
	_, err = OpenAICodexTurnStateProvenanceKey("", 7, "state")
	require.Error(t, err)
	_, err = OpenAICodexTurnStateProvenanceKey("secret", 7, "")
	require.Error(t, err)
}

func TestOpenAICodexTurnStateIdentityDigestStableAcrossTransports(t *testing.T) {
	base := OpenAIOAuthIdentityPlan{
		ProjectionMode:        OpenAIOAuthIdentityProjectionRegular,
		InstallationPolicy:    OpenAIOAuthInstallationAccountPin,
		InstallationEnabled:   true,
		InstallationID:        "11111111-1111-4111-8111-111111111111",
		TurnIdentityRequested: true,
		TurnIdentityEnabled:   true,
		RequestTurn: OpenAICodexRequestTurnSnapshot{
			ID:              turnStateTurnA,
			StartedAtUnixMS: 1723000000000,
			Source:          openAICodexRequestTurnSourceGenerated,
			Generated:       true,
		},
		TurnIdentity: OpenAICodexTurnIdentity{
			SessionID: turnStateSessionA,
			ThreadID:  turnStateThreadA,
			Relation:  OpenAICodexTurnRelationDescendant,
		},
	}
	digest := OpenAICodexTurnStateIdentityDigest(base)
	transportVariant := base
	transportVariant.ProjectionMode = OpenAIOAuthIdentityProjectionCompact
	transportVariant.ClientIdentity.UserAgent = "codex-tui/next"
	transportVariant.ClientIdentity.Version = "next"
	require.Equal(t, digest, OpenAICodexTurnStateIdentityDigest(transportVariant))
	installationVariant := base
	installationVariant.InstallationID = "22222222-2222-4222-8222-222222222222"
	require.NotEqual(t, digest, OpenAICodexTurnStateIdentityDigest(installationVariant))
	threadVariant := base
	threadVariant.TurnIdentity.ThreadID = turnStateSessionB
	require.NotEqual(t, digest, OpenAICodexTurnStateIdentityDigest(threadVariant))
	turnVariant := base
	turnVariant.RequestTurn.ID = turnStateTurnB
	require.NotEqual(t, digest, OpenAICodexTurnStateIdentityDigest(turnVariant))
	timestampVariant := base
	timestampVariant.RequestTurn.StartedAtUnixMS++
	require.Equal(t, digest, OpenAICodexTurnStateIdentityDigest(timestampVariant))
	disabledVariant := base
	disabledVariant.TurnIdentityEnabled = false
	require.NotEqual(t, digest, OpenAICodexTurnStateIdentityDigest(disabledVariant))
}

func TestRelayOpenAICodexTurnStateBindsOnlyAfterCommit(t *testing.T) {
	store := resetTurnStateLocalStore(t, 16)
	svc := newTurnStateTestService()
	account := oauthTurnStateAccount(42)
	c, _ := newTurnStateIdentityTestContext(t, 7, "account:42", "11111111-1111-4111-8111-111111111111", turnStateSessionA, turnStateThreadA)
	upstream := http.Header{"X-Codex-Turn-State": []string{"opaque-A"}}
	state := svc.relayOpenAICodexTurnState(c, account, upstream)
	require.Equal(t, "opaque-A", state)
	key, err := OpenAICodexTurnStateProvenanceKey(svc.cfg.JWT.Secret, 7, state)
	require.NoError(t, err)
	_, err = store.GetOpenAICodexTurnStateOrigin(context.Background(), key)
	require.ErrorIs(t, err, ErrOpenAICodexTurnStateOriginNotFound)
	svc.noteOpenAICodexTurnStateProvenance(c, account, state)
	origin, err := store.GetOpenAICodexTurnStateOrigin(context.Background(), key)
	require.NoError(t, err)
	require.Equal(t, "account:42", origin.CredentialOwnerNamespace)
	plan, ok := OpenAIOAuthIdentityPlanFromContext(c)
	require.True(t, ok)
	require.Equal(t, OpenAICodexTurnStateIdentityDigest(plan), origin.TurnIdentityDigest)
	c.Writer.Header().Set(openAICodexTurnStateHeader, "stale")
	require.Empty(t, svc.relayOpenAICodexTurnState(c, account, http.Header{}))
	require.Empty(t, c.Writer.Header().Get(openAICodexTurnStateHeader))
}

func TestGuardOpenAICodexTurnStateEchoUsesCredentialOwnerAndIdentity(t *testing.T) {
	resetTurnStateLocalStore(t, 32)
	svc := newTurnStateTestService()
	account := oauthTurnStateAccount(42)
	c, _ := newTurnStateIdentityTestContext(t, 7, "owner:parent-42", "11111111-1111-4111-8111-111111111111", turnStateSessionA, turnStateThreadA)
	svc.noteOpenAICodexTurnStateProvenance(c, account, "opaque-A")
	h := http.Header{"X-Codex-Turn-State": []string{"opaque-A"}}
	svc.guardOpenAICodexTurnStateEcho(c, oauthTurnStateAccount(999), h)
	require.Equal(t, "opaque-A", h.Get(openAICodexTurnStateHeader))
	foreignContext, _ := newTurnStateIdentityTestContext(t, 7, "owner:other", "11111111-1111-4111-8111-111111111111", turnStateSessionA, turnStateThreadA)
	h.Set(openAICodexTurnStateHeader, "opaque-A")
	svc.guardOpenAICodexTurnStateEcho(foreignContext, oauthTurnStateAccount(43), h)
	require.Empty(t, h.Get(openAICodexTurnStateHeader))
	changedIdentity, _ := newTurnStateIdentityTestContext(t, 7, "owner:parent-42", "22222222-2222-4222-8222-222222222222", turnStateSessionA, turnStateThreadA)
	h.Set(openAICodexTurnStateHeader, "opaque-A")
	svc.guardOpenAICodexTurnStateEcho(changedIdentity, account, h)
	require.Empty(t, h.Get(openAICodexTurnStateHeader))
	changedTurn, _ := newTurnStateIdentityTestContext(t, 7, "owner:parent-42", "11111111-1111-4111-8111-111111111111", turnStateSessionA, turnStateThreadA)
	changedTurnPlan, ok := OpenAIOAuthIdentityPlanFromContext(changedTurn)
	require.True(t, ok)
	changedTurnPlan.RequestTurn.ID = turnStateTurnB
	SetOpenAIOAuthIdentityPlan(changedTurn, changedTurnPlan)
	h.Set(openAICodexTurnStateHeader, "opaque-A")
	svc.guardOpenAICodexTurnStateEcho(changedTurn, account, h)
	require.Empty(t, h.Get(openAICodexTurnStateHeader))
	unknown := http.Header{"X-Codex-Turn-State": []string{"externally-minted"}}
	svc.guardOpenAICodexTurnStateEcho(foreignContext, oauthTurnStateAccount(43), unknown)
	require.Equal(t, "externally-minted", unknown.Get(openAICodexTurnStateHeader))
}

func TestGuardOpenAICodexTurnStateEchoForPlanGuardsHeaderAndBody(t *testing.T) {
	resetTurnStateLocalStore(t, 32)
	svc := newTurnStateTestService()
	account := oauthTurnStateAccount(42)
	c, _ := newTurnStateIdentityTestContext(t, 7, "owner:42", "11111111-1111-4111-8111-111111111111", turnStateSessionA, turnStateThreadA)
	plan, ok := OpenAIOAuthIdentityPlanFromContext(c)
	require.True(t, ok)
	svc.noteOpenAICodexTurnStateProvenanceForPlan(c, account, "valid-state", plan)

	matchingHeader := http.Header{"X-Codex-Turn-State": []string{"valid-state"}}
	matchingBody := []byte(`{"client_metadata":{"keep":true,"x-codex-turn-state":"valid-state"}}`)
	require.Equal(t, matchingBody, svc.guardOpenAICodexTurnStateEchoForPlan(c, account, plan, matchingHeader, matchingBody))
	require.Equal(t, "valid-state", matchingHeader.Get(openAICodexTurnStateHeader))

	compatibleDuplicates := http.Header{}
	compatibleDuplicates.Add(openAICodexTurnStateHeader, " valid-state ")
	compatibleDuplicates.Add(openAICodexTurnStateHeader, "valid-state")
	svc.guardOpenAICodexTurnStateEchoForPlan(c, account, plan, compatibleDuplicates, nil)
	require.Equal(t, []string{"valid-state"}, compatibleDuplicates.Values(openAICodexTurnStateHeader))

	changed := plan
	changed.RequestTurn.ID = turnStateTurnB
	mixedDuplicates := http.Header{}
	mixedDuplicates.Add(openAICodexTurnStateHeader, "externally-minted")
	mixedDuplicates.Add(openAICodexTurnStateHeader, "valid-state")
	svc.guardOpenAICodexTurnStateEchoForPlan(c, account, changed, mixedDuplicates, nil)
	require.Empty(t, mixedDuplicates.Values(openAICodexTurnStateHeader))
	header := http.Header{"X-Codex-Turn-State": []string{"valid-state"}}
	body := []byte(` { "model":"gpt", "client_metadata": {"keep":true,"x-codex-turn-state":"valid-state"}, "tail":1 } `)
	guarded := svc.guardOpenAICodexTurnStateEchoForPlan(c, account, changed, header, body)
	require.Empty(t, header.Get(openAICodexTurnStateHeader))
	require.False(t, gjson.GetBytes(guarded, "client_metadata.x-codex-turn-state").Exists())
	require.True(t, gjson.GetBytes(guarded, "client_metadata.keep").Bool())
	require.Equal(t, int64(1), gjson.GetBytes(guarded, "tail").Int())

	foreignOwner := plan
	foreignOwner.CredentialOwnerNamespace = "owner:foreign"
	foreignBody := []byte(`{"client_metadata":{"x-codex-turn-state":"valid-state"}}`)
	require.False(t, gjson.GetBytes(svc.guardOpenAICodexTurnStateEchoForPlan(c, account, foreignOwner, nil, foreignBody), "client_metadata.x-codex-turn-state").Exists())

	unknownHeader := http.Header{"X-Codex-Turn-State": []string{"external-state"}}
	unknownBody := []byte(`{"client_metadata":{"x-codex-turn-state":"external-state"}}`)
	require.Equal(t, unknownBody, svc.guardOpenAICodexTurnStateEchoForPlan(c, account, changed, unknownHeader, unknownBody))
	require.Equal(t, "external-state", unknownHeader.Get(openAICodexTurnStateHeader))

	nonString := []byte(`{"client_metadata":{"x-codex-turn-state":{"opaque":true}}}`)
	require.Equal(t, nonString, svc.guardOpenAICodexTurnStateEchoForPlan(c, account, changed, nil, nonString))

	failingSvc := newTurnStateTestService()
	failingSvc.cache = failingTurnStateGatewayCache{}
	storeErrorBody := []byte(`{"client_metadata":{"x-codex-turn-state":"store-error-state"}}`)
	require.Equal(t, storeErrorBody, failingSvc.guardOpenAICodexTurnStateEchoForPlan(c, account, changed, nil, storeErrorBody))

	disabledPlan := changed
	disabledPlan.InstallationEnabled = false
	disabledPlan.TurnIdentityEnabled = false
	disabledPlan.TurnIdentityRequested = false
	disabledPlan.ClientIdentityEnabled = false
	disabledHeader := http.Header{"X-Codex-Turn-State": []string{"valid-state"}}
	disabledBody := []byte(" { \"client_metadata\" : { \"keep\" : true, \"x-codex-turn-state\" : \"valid-state\" } } \n")
	require.Equal(t, disabledBody, svc.guardOpenAICodexTurnStateEchoForPlan(c, account, disabledPlan, disabledHeader, disabledBody))
	require.Equal(t, "valid-state", disabledHeader.Get(openAICodexTurnStateHeader))
}

func TestOpenAICodexTurnStateFlagOffDoesNotAccessProvenanceStore(t *testing.T) {
	store := &countingTurnStateGatewayCache{}
	svc := newTurnStateTestService()
	svc.cache = store
	account := oauthTurnStateAccount(92)
	c, _ := newTurnStateIdentityTestContext(t, 7, "owner:92", "11111111-1111-4111-8111-111111111111", turnStateSessionA, turnStateSessionA)
	plan, ok := OpenAIOAuthIdentityPlanFromContext(c)
	require.True(t, ok)
	plan.TurnIdentityRequested = false
	plan.TurnIdentityEnabled = false
	plan.PolicySnapshot.MasterEnabled = false
	header := http.Header{}
	header.Set(openAICodexTurnStateHeader, "opaque-state")
	body := []byte(`{"client_metadata":{"x-codex-turn-state":"opaque-state"}}`)

	require.Equal(t, body, svc.guardOpenAICodexTurnStateEchoForPlan(c, account, plan, header, body))
	require.Equal(t, "opaque-state", header.Get(openAICodexTurnStateHeader))
	svc.noteOpenAICodexTurnStateProvenanceForPlan(c, account, "opaque-state", plan)
	require.Zero(t, store.getCalls)
	require.Zero(t, store.setCalls)
	require.Zero(t, store.deleteCalls)
}

type countingTurnStateGatewayCache struct {
	GatewayCache
	getCalls    int
	setCalls    int
	deleteCalls int
}

func (c *countingTurnStateGatewayCache) GetOpenAICodexTurnStateOrigin(context.Context, string) (OpenAICodexTurnStateOrigin, error) {
	c.getCalls++
	return OpenAICodexTurnStateOrigin{}, ErrOpenAICodexTurnStateOriginNotFound
}

func (c *countingTurnStateGatewayCache) SetOpenAICodexTurnStateOrigin(context.Context, string, OpenAICodexTurnStateOrigin, time.Duration) error {
	c.setCalls++
	return nil
}

func (c *countingTurnStateGatewayCache) DeleteOpenAICodexTurnStateOrigin(context.Context, string) error {
	c.deleteCalls++
	return nil
}

type failingTurnStateGatewayCache struct{ GatewayCache }

func (failingTurnStateGatewayCache) GetOpenAICodexTurnStateOrigin(context.Context, string) (OpenAICodexTurnStateOrigin, error) {
	return OpenAICodexTurnStateOrigin{}, errors.New("redis unavailable")
}
func (failingTurnStateGatewayCache) SetOpenAICodexTurnStateOrigin(context.Context, string, OpenAICodexTurnStateOrigin, time.Duration) error {
	return errors.New("redis unavailable")
}
func (failingTurnStateGatewayCache) DeleteOpenAICodexTurnStateOrigin(context.Context, string) error {
	return errors.New("redis unavailable")
}

type recoveringTurnStateGatewayCache struct {
	GatewayCache
	setCalls int
	origins  map[string]OpenAICodexTurnStateOrigin
}

func (c *recoveringTurnStateGatewayCache) GetOpenAICodexTurnStateOrigin(_ context.Context, key string) (OpenAICodexTurnStateOrigin, error) {
	if origin, ok := c.origins[key]; ok {
		return origin, nil
	}
	return OpenAICodexTurnStateOrigin{}, ErrOpenAICodexTurnStateOriginNotFound
}

func (c *recoveringTurnStateGatewayCache) SetOpenAICodexTurnStateOrigin(_ context.Context, key string, origin OpenAICodexTurnStateOrigin, _ time.Duration) error {
	c.setCalls++
	if c.setCalls == 1 {
		return errors.New("redis unavailable")
	}
	if c.origins == nil {
		c.origins = make(map[string]OpenAICodexTurnStateOrigin)
	}
	c.origins[key] = origin
	return nil
}

func (c *recoveringTurnStateGatewayCache) DeleteOpenAICodexTurnStateOrigin(_ context.Context, key string) error {
	delete(c.origins, key)
	return nil
}

func TestGuardOpenAICodexTurnStateEchoFailsOpenOnStoreError(t *testing.T) {
	resetTurnStateLocalStore(t, 16)
	svc := newTurnStateTestService()
	svc.cache = failingTurnStateGatewayCache{}
	c, _ := newTurnStateIdentityTestContext(t, 7, "owner:42", "11111111-1111-4111-8111-111111111111", turnStateSessionA, turnStateThreadA)
	h := http.Header{"X-Codex-Turn-State": []string{"opaque-A"}}
	svc.guardOpenAICodexTurnStateEcho(c, oauthTurnStateAccount(42), h)
	require.Equal(t, "opaque-A", h.Get(openAICodexTurnStateHeader))
}

func TestGuardOpenAICodexTurnStateEchoPromotesLocalFallbackAfterRedisRecovery(t *testing.T) {
	resetTurnStateLocalStore(t, 16)
	cache := &recoveringTurnStateGatewayCache{}
	svc := newTurnStateTestService()
	svc.cache = cache
	account := oauthTurnStateAccount(42)
	c, _ := newTurnStateIdentityTestContext(t, 7, "owner:42", "11111111-1111-4111-8111-111111111111", turnStateSessionA, turnStateThreadA)

	// The first Redis write fails, so provenance exists only in the bounded
	// process-local fallback.
	svc.noteOpenAICodexTurnStateProvenance(c, account, "opaque-recovery")
	require.Equal(t, 1, cache.setCalls)

	// Redis has recovered but still misses the failed write. The guard must use
	// the local winner and promote it instead of treating the state as unknown.
	h := http.Header{"X-Codex-Turn-State": []string{"opaque-recovery"}}
	svc.guardOpenAICodexTurnStateEcho(c, account, h)
	require.Equal(t, "opaque-recovery", h.Get(openAICodexTurnStateHeader))
	require.Equal(t, 2, cache.setCalls)
	key, err := OpenAICodexTurnStateProvenanceKey(svc.cfg.JWT.Secret, 7, "opaque-recovery")
	require.NoError(t, err)
	require.Contains(t, cache.origins, key)

	changedIdentity, _ := newTurnStateIdentityTestContext(t, 7, "owner:42", "22222222-2222-4222-8222-222222222222", turnStateSessionA, turnStateThreadA)
	h.Set(openAICodexTurnStateHeader, "opaque-recovery")
	svc.guardOpenAICodexTurnStateEcho(changedIdentity, account, h)
	require.Empty(t, h.Get(openAICodexTurnStateHeader))
}

func TestOpenAICompatSessionTurnStateUsesCredentialOwnerAndIdentityKey(t *testing.T) {
	resetTurnStateLocalStore(t, 16)
	svc := newTurnStateTestService()
	parent := oauthTurnStateAccount(42)
	shadow := oauthTurnStateAccount(999)
	parentContext, _ := newTurnStateIdentityTestContext(t, 7, "owner:parent-42", "11111111-1111-4111-8111-111111111111", turnStateSessionA, turnStateThreadA)

	svc.bindOpenAICompatSessionTurnState(context.Background(), parentContext, parent, "prompt-A", "opaque-compat")
	require.Equal(t, "opaque-compat", svc.getOpenAICompatSessionTurnState(context.Background(), parentContext, shadow, "prompt-A"), "shadow must share its credential owner's raw state")
	require.Empty(t, svc.getOpenAICompatSessionTurnState(context.Background(), parentContext, shadow, "prompt-B"))

	differentAPIKey, _ := newTurnStateIdentityTestContext(t, 8, "owner:parent-42", "11111111-1111-4111-8111-111111111111", turnStateSessionA, turnStateThreadA)
	require.Empty(t, svc.getOpenAICompatSessionTurnState(context.Background(), differentAPIKey, shadow, "prompt-A"))

	differentIdentity, _ := newTurnStateIdentityTestContext(t, 7, "owner:parent-42", "22222222-2222-4222-8222-222222222222", turnStateSessionA, turnStateThreadA)
	require.Empty(t, svc.getOpenAICompatSessionTurnState(context.Background(), differentIdentity, shadow, "prompt-A"))

	withoutPlan, _ := newTurnStateTestContext(t, 7, turnStateSessionA)
	require.Empty(t, svc.getOpenAICompatSessionTurnState(context.Background(), withoutPlan, parent, "prompt-A"))
}

func TestOpenAICodexTurnStateLocalStoreTTLAndLRU(t *testing.T) {
	store := newOpenAICodexTurnStateLocalStore(2)
	origin := OpenAICodexTurnStateOrigin{CredentialOwnerNamespace: "owner", TurnIdentityDigest: strings.Repeat("a", 64)}
	require.NoError(t, store.SetOpenAICodexTurnStateOrigin(context.Background(), "one", origin, time.Minute))
	require.NoError(t, store.SetOpenAICodexTurnStateOrigin(context.Background(), "two", origin, time.Minute))
	_, err := store.GetOpenAICodexTurnStateOrigin(context.Background(), "one")
	require.NoError(t, err)
	require.NoError(t, store.SetOpenAICodexTurnStateOrigin(context.Background(), "three", origin, time.Minute))
	_, err = store.GetOpenAICodexTurnStateOrigin(context.Background(), "two")
	require.ErrorIs(t, err, ErrOpenAICodexTurnStateOriginNotFound)
	expired := origin
	expired.ExpiresAt = time.Now().Add(-time.Second)
	require.NoError(t, store.SetOpenAICodexTurnStateOrigin(context.Background(), "expired", expired, time.Minute))
	_, err = store.GetOpenAICodexTurnStateOrigin(context.Background(), "expired")
	require.ErrorIs(t, err, ErrOpenAICodexTurnStateOriginNotFound)
}

func TestStagedTurnStateAbandonedAttemptDoesNotBind(t *testing.T) {
	resetTurnStateLocalStore(t, 16)
	svc := newTurnStateTestService()
	c, _ := newTurnStateIdentityTestContext(t, 7, "owner:A", "11111111-1111-4111-8111-111111111111", turnStateSessionA, turnStateThreadA)
	var staged http.Header
	stageOpenAICodexTurnState(&staged, oauthTurnStateAccount(1), http.Header{"X-Codex-Turn-State": []string{"opaque-A"}})
	other, _ := newTurnStateIdentityTestContext(t, 7, "owner:B", "22222222-2222-4222-8222-222222222222", turnStateSessionB, turnStateSessionB)
	h := http.Header{"X-Codex-Turn-State": []string{"opaque-A"}}
	svc.guardOpenAICodexTurnStateEcho(other, oauthTurnStateAccount(2), h)
	require.Equal(t, "opaque-A", h.Get(openAICodexTurnStateHeader))
	svc.noteStagedOpenAICodexTurnStateCommitted(c, oauthTurnStateAccount(1), staged)
	h.Set(openAICodexTurnStateHeader, "opaque-A")
	svc.guardOpenAICodexTurnStateEcho(other, oauthTurnStateAccount(2), h)
	require.Empty(t, h.Get(openAICodexTurnStateHeader))
}

func TestStreamingTurnStateAbandonedAttemptDoesNotReachWriter(t *testing.T) {
	store := resetTurnStateLocalStore(t, 16)
	svc := newTurnStateTestService()
	svc.toolCorrector = NewCodexToolCorrector()
	account := oauthTurnStateAccount(42)
	c, rec := newTurnStateIdentityTestContext(t, 7, "owner:42", "11111111-1111-4111-8111-111111111111", turnStateSessionA, turnStateThreadA)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type":       []string{"text/event-stream"},
			"X-Codex-Turn-State": []string{"abandoned-http-state"},
		},
		Body: io.NopCloser(strings.NewReader(
			"data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_empty\",\"status\":\"in_progress\"}}\n\n" +
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_empty\",\"status\":\"completed\",\"output\":[]}}\n\n",
		)),
	}

	_, err := svc.handleStreamingResponse(context.Background(), resp, c, account, time.Now(), "gpt-test", "gpt-test")
	require.Error(t, err)
	require.False(t, c.Writer.Written())
	require.Empty(t, rec.Header().Get(openAICodexTurnStateHeader))
	key, keyErr := OpenAICodexTurnStateProvenanceKey(svc.cfg.JWT.Secret, 7, "abandoned-http-state")
	require.NoError(t, keyErr)
	_, getErr := store.GetOpenAICodexTurnStateOrigin(context.Background(), key)
	require.ErrorIs(t, getErr, ErrOpenAICodexTurnStateOriginNotFound)
}

func TestPassthroughStreamingTurnStateAbandonedAttemptDoesNotReachWriter(t *testing.T) {
	store := resetTurnStateLocalStore(t, 16)
	svc := newTurnStateTestService()
	account := oauthTurnStateAccount(42)
	c, rec := newTurnStateIdentityTestContext(t, 7, "owner:42", "11111111-1111-4111-8111-111111111111", turnStateSessionA, turnStateThreadA)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type":       []string{"text/event-stream"},
			"X-Codex-Turn-State": []string{"abandoned-passthrough-state"},
		},
		Body: io.NopCloser(strings.NewReader(
			"data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_empty\",\"status\":\"in_progress\"}}\n\n" +
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_empty\",\"status\":\"completed\",\"output\":[]}}\n\n",
		)),
	}

	_, err := svc.handleStreamingResponsePassthrough(context.Background(), resp, c, account, time.Now(), "gpt-test", "gpt-test")
	require.Error(t, err)
	require.False(t, c.Writer.Written())
	require.Empty(t, rec.Header().Get(openAICodexTurnStateHeader))
	key, keyErr := OpenAICodexTurnStateProvenanceKey(svc.cfg.JWT.Secret, 7, "abandoned-passthrough-state")
	require.NoError(t, keyErr)
	_, getErr := store.GetOpenAICodexTurnStateOrigin(context.Background(), key)
	require.ErrorIs(t, getErr, ErrOpenAICodexTurnStateOriginNotFound)
}

func TestHandleNonStreamingResponseCommitsTurnStateProvenance(t *testing.T) {
	store := resetTurnStateLocalStore(t, 16)
	svc := newTurnStateTestService()
	account := oauthTurnStateAccount(42)
	c, rec := newTurnStateIdentityTestContext(t, 7, "owner:42", "11111111-1111-4111-8111-111111111111", turnStateSessionA, turnStateThreadA)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}, "X-Codex-Turn-State": []string{"opaque-A"}},
		Body:       io.NopCloser(strings.NewReader(`{"id":"resp_1","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)),
	}
	_, err := svc.handleNonStreamingResponse(context.Background(), resp, c, account, "gpt-test", "gpt-test")
	require.NoError(t, err)
	require.True(t, c.Writer.Written())
	require.Equal(t, "opaque-A", rec.Header().Get(openAICodexTurnStateHeader))
	key, err := OpenAICodexTurnStateProvenanceKey(svc.cfg.JWT.Secret, 7, "opaque-A")
	require.NoError(t, err)
	_, err = store.GetOpenAICodexTurnStateOrigin(context.Background(), key)
	require.NoError(t, err)
}

func TestRelayOpenAICodexTurnStateHeader_RelaysAndClearsTurnState(t *testing.T) {
	dst := http.Header{}
	src := http.Header{"X-Codex-Turn-State": []string{"blob-P"}}
	relayOpenAICodexTurnStateHeader(dst, src)
	require.Equal(t, "blob-P", dst.Get("X-Codex-Turn-State"))
	relayOpenAICodexTurnStateHeader(dst, http.Header{"Content-Type": []string{"application/json"}})
	require.Empty(t, dst.Get("X-Codex-Turn-State"))
}

func TestRelayOpenAICodexTurnStateIsOAuthOnly(t *testing.T) {
	upstream := http.Header{"X-Codex-Turn-State": []string{"blob-OAuth"}}

	oauthContext, oauthRecorder := newTurnStateTestContext(t, 7, "oauth-session")
	svc := newTurnStateTestService()
	require.Equal(t, "blob-OAuth", svc.relayOpenAICodexTurnState(oauthContext, oauthTurnStateAccount(1), upstream))
	require.Equal(t, "blob-OAuth", oauthRecorder.Header().Get(openAICodexTurnStateHeader))

	apiKeyContext, apiKeyRecorder := newTurnStateTestContext(t, 7, "api-key-session")
	apiKeyAccount := &Account{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	require.Empty(t, svc.relayOpenAICodexTurnState(apiKeyContext, apiKeyAccount, upstream))
	require.Empty(t, apiKeyRecorder.Header().Get(openAICodexTurnStateHeader))
}

func TestGenericPassthroughResponseHeadersDoNotRelayTurnState(t *testing.T) {
	dst := http.Header{}
	writeOpenAIPassthroughResponseHeaders(dst, http.Header{
		"Content-Type":       []string{"application/json"},
		"X-Codex-Turn-State": []string{"alpha-state"},
	}, nil)
	require.Empty(t, dst.Get(openAICodexTurnStateHeader))
}

func TestWriteOpenAIPassthroughResponseHeaders_RelaysReasoningIncluded(t *testing.T) {
	dst := http.Header{}
	src := http.Header{}
	src.Set("X-Reasoning-Included", "1")

	writeOpenAIPassthroughResponseHeaders(
		dst,
		src,
		responseheaders.CompileHeaderFilter(config.ResponseHeaderConfig{}),
	)
	require.Equal(t, "1", dst.Get("X-Reasoning-Included"))
}

func TestEnsureOpenAIRemoteCompactionV2BetaFeature(t *testing.T) {
	t.Run("absent_sets_feature", func(t *testing.T) {
		h := http.Header{}
		ensureOpenAIRemoteCompactionV2BetaFeature(h)
		require.Equal(t, "remote_compaction_v2", h.Get("x-codex-beta-features"))
	})
	t.Run("present_unchanged", func(t *testing.T) {
		h := http.Header{"X-Codex-Beta-Features": []string{"responses_websockets_v2, remote_compaction_v2"}}
		ensureOpenAIRemoteCompactionV2BetaFeature(h)
		require.Equal(t, "responses_websockets_v2, remote_compaction_v2", h.Get("x-codex-beta-features"))
	})
	t.Run("other_tokens_merged", func(t *testing.T) {
		h := http.Header{"X-Codex-Beta-Features": []string{"responses_websockets_v2"}}
		ensureOpenAIRemoteCompactionV2BetaFeature(h)
		require.Equal(t, "responses_websockets_v2,remote_compaction_v2", h.Get("x-codex-beta-features"))
	})
	t.Run("multi_line_values_merged_single_line", func(t *testing.T) {
		h := http.Header{}
		h.Add("x-codex-beta-features", "feature_a")
		h.Add("x-codex-beta-features", "feature_b")
		ensureOpenAIRemoteCompactionV2BetaFeature(h)
		require.Equal(t, []string{"feature_a,feature_b,remote_compaction_v2"}, h.Values("x-codex-beta-features"))
	})
}

func TestApplyOpenAICodexBetaFeatures(t *testing.T) {
	oauthAccount := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	apiKeyAccount := &Account{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	t.Run("oauth_default", func(t *testing.T) {
		c, _ := newTurnStateTestContext(t, 7, "sess-beta")
		h := http.Header{}
		applyOpenAICodexBetaFeatures(c, oauthAccount, h)
		require.Equal(t, "remote_compaction_v2", h.Get("x-codex-beta-features"))
	})
	t.Run("oauth_appends_to_client_declared_features", func(t *testing.T) {
		c, _ := newTurnStateTestContext(t, 7, "sess-beta")
		h := http.Header{"X-Codex-Beta-Features": []string{"some_other_feature"}}
		applyOpenAICodexBetaFeatures(c, oauthAccount, h)
		require.Equal(t, "some_other_feature,remote_compaction_v2", h.Get("x-codex-beta-features"))
		applyOpenAICodexBetaFeatures(c, oauthAccount, h)
		require.Equal(t, "some_other_feature,remote_compaction_v2", h.Get("x-codex-beta-features"))
	})
	t.Run("native_v2_forces_feature", func(t *testing.T) {
		c, _ := newTurnStateTestContext(t, 7, "sess-beta")
		MarkOpenAINativeCompactionV2(c)
		h := http.Header{"X-Codex-Beta-Features": []string{"some_other_feature"}}
		applyOpenAICodexBetaFeatures(c, oauthAccount, h)
		require.Contains(t, h.Get("x-codex-beta-features"), "remote_compaction_v2")
	})
	t.Run("native_v2_non_oauth", func(t *testing.T) {
		c, _ := newTurnStateTestContext(t, 7, "sess-beta")
		MarkOpenAINativeCompactionV2(c)
		h := http.Header{}
		applyOpenAICodexBetaFeatures(c, apiKeyAccount, h)
		require.Equal(t, "remote_compaction_v2", h.Get("x-codex-beta-features"))
	})
	t.Run("non_oauth_plain_untouched", func(t *testing.T) {
		c, _ := newTurnStateTestContext(t, 7, "sess-beta")
		h := http.Header{}
		applyOpenAICodexBetaFeatures(c, apiKeyAccount, h)
		require.Empty(t, h.Get("x-codex-beta-features"))
	})
}

func TestBuildOpenAIWSHeaders_CarriesSessionBetaFeatures(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := &OpenAIGatewayService{}
	decision := OpenAIWSProtocolDecision{Transport: OpenAIUpstreamTransportResponsesWebsocketV2}
	build := func(t *testing.T, account *Account, clientBeta string) http.Header {
		t.Helper()
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
		if clientBeta != "" {
			c.Request.Header.Set("x-codex-beta-features", clientBeta)
		}
		headers, _, err := svc.buildOpenAIWSHeaders(context.Background(), c, account, "test-token", decision, true, "", "", "", "gpt-5.6-codex", "")
		require.NoError(t, err)
		return headers
	}
	oauthAccount := installationTestOAuthAccount(nil)
	require.Equal(t, "remote_compaction_v2", build(t, oauthAccount, "").Get("x-codex-beta-features"))
	require.Equal(t, []string{"some_other_feature,remote_compaction_v2"}, build(t, oauthAccount, "some_other_feature").Values("x-codex-beta-features"))
	require.Empty(t, build(t, &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, "").Get("x-codex-beta-features"))
}
