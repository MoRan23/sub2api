package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestNewOpenAICodexSessionFirstResolutionAndReuse(t *testing.T) {
	for _, primary := range []bool{false, true} {
		name := "local"
		if primary {
			name = "primary"
		}
		t.Run(name, func(t *testing.T) {
			resetProcessCodexIdentityStore(t)
			svc := &OpenAIGatewayService{cfg: &config.Config{JWT: config.JWTConfig{Secret: "fresh-session-secret"}}}
			if primary {
				svc.cache = &outboundIdentityGatewayCacheStub{}
			}
			account := &Account{ID: 301, Type: AccountTypeOAuth}
			logical := OpenAICodexLogicalTurnIdentity{SessionKey: "fresh-session", ThreadKey: "fresh-session"}
			c := newAuthenticatedOutboundIdentityTestContext(t, nil)
			first, ok, err := svc.resolveOpenAICodexTurnIdentity(context.Background(), c, account, logical)
			require.NoError(t, err)
			require.True(t, ok)
			require.True(t, IsNewOpenAICodexSession(c, first.SessionID))
			require.False(t, IsNewOpenAICodexSession(c, first.SessionID+" "), "the marker requires the exact session ID")
			require.False(t, IsNewOpenAICodexSession(c, ""))

			next := newAuthenticatedOutboundIdentityTestContext(t, nil)
			reused, ok, err := svc.resolveOpenAICodexTurnIdentity(context.Background(), next, account, logical)
			require.NoError(t, err)
			require.True(t, ok)
			require.Equal(t, first, reused)
			require.False(t, IsNewOpenAICodexSession(next, reused.SessionID))

			_, _, err = svc.resolveOpenAICodexTurnIdentity(context.Background(), c, account, logical)
			require.NoError(t, err)
			require.False(t, IsNewOpenAICodexSession(c, first.SessionID), "a fresh resolution must clear the previous marker")
		})
	}
}

func TestNewOpenAICodexSessionExistingPrimaryWinner(t *testing.T) {
	resetProcessCodexIdentityStore(t)
	const secret = "fresh-session-primary-winner"
	cache := &outboundIdentityGatewayCacheStub{}
	svc := &OpenAIGatewayService{cache: cache, cfg: &config.Config{JWT: config.JWTConfig{Secret: secret}}}
	account := &Account{ID: 302, Type: AccountTypeOAuth}
	logical := OpenAICodexLogicalTurnIdentity{SessionKey: "existing-session", ThreadKey: "existing-session"}
	mappingKey, err := OpenAICodexSessionMappingKey(secret, "account:302", 41, logical.SessionKey)
	require.NoError(t, err)
	_, err = cache.identityStore().GetOrCreateCodexSession(context.Background(), mappingKey, testOutboundSessionUUID, time.Hour)
	require.NoError(t, err)

	c := newAuthenticatedOutboundIdentityTestContext(t, nil)
	identity, ok, err := svc.resolveOpenAICodexTurnIdentity(context.Background(), c, account, logical)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, testOutboundSessionUUID, identity.SessionID)
	require.False(t, IsNewOpenAICodexSession(c, identity.SessionID), "the existing primary winner must override a new local candidate")
}

type codexFreshSessionUnreadablePrimaryCache struct {
	outboundIdentityGatewayCacheStub
	winner string
	err    error
}

func (s *codexFreshSessionUnreadablePrimaryCache) ResolveCodexDownstreamSession(context.Context, OpenAICodexDownstreamSessionRequest, time.Duration) (OpenAICodexDownstreamSessionResolution, error) {
	return OpenAICodexDownstreamSessionResolution{SessionID: s.winner}, s.err
}

func TestNewOpenAICodexSessionUnreadablePrimaryDoesNotMark(t *testing.T) {
	for _, tc := range []struct {
		name   string
		winner string
		err    error
	}{
		{name: "read_error", err: errors.New("identity store unavailable")},
		{name: "invalid_winner", winner: "invalid-session-id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetProcessCodexIdentityStore(t)
			const secret = "fresh-session-unreadable-primary"
			cache := &codexFreshSessionUnreadablePrimaryCache{winner: tc.winner, err: tc.err}
			svc := &OpenAIGatewayService{cache: cache, cfg: &config.Config{JWT: config.JWTConfig{Secret: secret}}}
			account := &Account{ID: 307, Type: AccountTypeOAuth}
			logical := OpenAICodexLogicalTurnIdentity{SessionKey: "existing-session", ThreadKey: "existing-session"}
			mappingKey, err := OpenAICodexSessionMappingKey(secret, "account:307", 41, logical.SessionKey)
			require.NoError(t, err)
			_, err = cache.identityStore().GetOrCreateCodexSession(context.Background(), mappingKey, testOutboundSessionUUID, time.Hour)
			require.NoError(t, err)

			c := newAuthenticatedOutboundIdentityTestContext(t, nil)
			identity, ok, err := svc.resolveOpenAICodexTurnIdentity(context.Background(), c, account, logical)
			if tc.err != nil {
				require.ErrorIs(t, err, ErrOpenAICodexDownstreamIdentityStoreUnavailable)
			} else {
				require.ErrorIs(t, err, ErrOpenAIOutboundSessionIdentityStoredValueInvalid)
			}
			require.True(t, ok)
			require.Empty(t, identity, "a cold failure must not invent a competing session")
			require.False(t, IsNewOpenAICodexSession(c, testOutboundSessionUUID))
			marker, _ := c.Get(newOpenAICodexSessionContextKey)
			require.Empty(t, marker)
		})
	}
}

func TestNewOpenAICodexSessionAccountSwitchClearsMarker(t *testing.T) {
	resetProcessCodexIdentityStore(t)
	svc := &OpenAIGatewayService{cfg: &config.Config{JWT: config.JWTConfig{Secret: "fresh-session-account-switch"}}}
	firstAccount := &Account{ID: 303, Type: AccountTypeOAuth}
	secondAccount := &Account{ID: 304, Type: AccountTypeOAuth}
	logical := OpenAICodexLogicalTurnIdentity{SessionKey: "same-client-session", ThreadKey: "same-client-session"}
	c := newAuthenticatedOutboundIdentityTestContext(t, nil)
	fresh, _, err := svc.resolveOpenAICodexTurnIdentity(context.Background(), c, firstAccount, logical)
	require.NoError(t, err)
	require.True(t, IsNewOpenAICodexSession(c, fresh.SessionID))
	reused, _, err := svc.resolveOpenAICodexTurnIdentity(context.Background(), c, secondAccount, logical)
	require.NoError(t, err)
	require.Equal(t, fresh, reused, "account switches reuse the existing downstream session")
	require.False(t, IsNewOpenAICodexSession(c, fresh.SessionID))
	next := newAuthenticatedOutboundIdentityTestContext(t, nil)
	stable, _, err := svc.resolveOpenAICodexTurnIdentity(context.Background(), next, firstAccount, logical)
	require.NoError(t, err)
	require.Equal(t, fresh, stable)
	require.False(t, IsNewOpenAICodexSession(next, stable.SessionID))
}

type codexFreshSessionThreadFailureCache struct {
	outboundIdentityGatewayCacheStub
}

func (*codexFreshSessionThreadFailureCache) ResolveCodexDownstreamThread(context.Context, OpenAICodexDownstreamThreadRequest, time.Duration) (OpenAICodexTurnIdentity, error) {
	return OpenAICodexTurnIdentity{}, ErrOpenAICodexAliasConflict
}

func TestNewOpenAICodexSessionFailedResolutionDoesNotMark(t *testing.T) {
	resetProcessCodexIdentityStore(t)
	cache := &codexFreshSessionThreadFailureCache{}
	svc := &OpenAIGatewayService{cache: cache, cfg: &config.Config{JWT: config.JWTConfig{Secret: "fresh-session-failure"}}}
	account := &Account{ID: 305, Type: AccountTypeOAuth}
	c := newAuthenticatedOutboundIdentityTestContext(t, nil)
	c.Set(newOpenAICodexSessionContextKey, testOutboundSessionUUID)
	_, ok, err := svc.resolveOpenAICodexTurnIdentity(context.Background(), c, account, OpenAICodexLogicalTurnIdentity{
		SessionKey: "new-session-with-failed-thread", ThreadKey: "descendant-thread",
	})
	require.ErrorIs(t, err, ErrOpenAICodexAliasConflict)
	require.True(t, ok)
	require.False(t, IsNewOpenAICodexSession(c, testOutboundSessionUUID))
	marker, _ := c.Get(newOpenAICodexSessionContextKey)
	require.Empty(t, marker, "creating the session is insufficient when resolving the full identity fails")

	c.Set(newOpenAICodexSessionContextKey, testOutboundSessionUUID)
	_, ok, err = svc.resolveOpenAICodexTurnIdentity(context.Background(), c, account, OpenAICodexLogicalTurnIdentity{})
	require.NoError(t, err)
	require.False(t, ok)
	require.False(t, IsNewOpenAICodexSession(c, testOutboundSessionUUID))
}

func TestNewOpenAICodexSessionNilContext(t *testing.T) {
	resetProcessCodexIdentityStore(t)
	svc := &OpenAIGatewayService{cfg: &config.Config{JWT: config.JWTConfig{Secret: "fresh-session-nil-context"}}}
	identity, ok, err := svc.resolveOpenAICodexTurnIdentity(context.Background(), nil, &Account{ID: 306, Type: AccountTypeOAuth}, OpenAICodexLogicalTurnIdentity{
		SessionKey: "nil-context-session", ThreadKey: "nil-context-session",
	})
	require.ErrorIs(t, err, ErrOpenAICodexDownstreamIdentityScopeMissing)
	require.True(t, ok)
	require.Empty(t, identity)
	require.False(t, IsNewOpenAICodexSession(nil, identity.SessionID))
}

func TestNewOpenAICodexSessionResolutionStateResets(t *testing.T) {
	state := &openAICodexIdentityResolutionState{
		ctx: context.Background(), local: newOpenAICodexIdentityLocalStore(),
		sessionDigest: "freshness-state", sessionDigests: []string{"freshness-state"},
	}
	require.NoError(t, state.resolveSession())
	require.True(t, state.sessionCreated)
	firstID := state.sessionID
	require.NoError(t, state.resolveSession())
	require.Equal(t, firstID, state.sessionID)
	require.False(t, state.sessionCreated)
}
