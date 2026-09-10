package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const (
	localDownstreamSessionA = "018f5c3c-6e3a-7abc-8def-1234567890ab"
	localDownstreamSessionB = "018f5c3c-6e3a-7abd-8def-1234567890ac"
	localDownstreamThreadA  = "018f5c3c-6e3a-7abe-8def-1234567890ad"
	localDownstreamThreadB  = "018f5c3c-6e3a-7abf-8def-1234567890ae"
)

func localDownstreamSessionRequest() OpenAICodexDownstreamSessionRequest {
	return OpenAICodexDownstreamSessionRequest{
		SessionMappingKeys: []string{strings.Repeat("a", 64), strings.Repeat("b", 64)},
		CandidateSessionID: localDownstreamSessionA,
	}
}

func localDownstreamThreadRequest() OpenAICodexDownstreamThreadRequest {
	return OpenAICodexDownstreamThreadRequest{
		SessionMappingKeys: localDownstreamSessionRequest().SessionMappingKeys,
		ThreadMappings: []OpenAICodexThreadAliasMapping{
			{SessionMappingKey: strings.Repeat("a", 64), ThreadMappingKey: strings.Repeat("c", 64)},
			{SessionMappingKey: strings.Repeat("b", 64), ThreadMappingKey: strings.Repeat("d", 64)},
		},
		SessionID:         localDownstreamSessionA,
		CandidateThreadID: localDownstreamThreadA,
	}
}

func TestCodexDownstreamLocalStoreValidatesSessionInputsBeforeClaim(t *testing.T) {
	for name, mutate := range map[string]func(*OpenAICodexDownstreamSessionRequest){
		"empty aliases":    func(r *OpenAICodexDownstreamSessionRequest) { r.SessionMappingKeys = nil },
		"invalid digest":   func(r *OpenAICodexDownstreamSessionRequest) { r.SessionMappingKeys[0] = "raw session" },
		"padded digest":    func(r *OpenAICodexDownstreamSessionRequest) { r.SessionMappingKeys[0] += " " },
		"uppercase digest": func(r *OpenAICodexDownstreamSessionRequest) { r.SessionMappingKeys[0] = strings.Repeat("A", 64) },
		"padded UUID":      func(r *OpenAICodexDownstreamSessionRequest) { r.CandidateSessionID += " " },
		"zero owner":       func(r *OpenAICodexDownstreamSessionRequest) { r.LegacyNamespace = "account:0" },
		"overflow owner":   func(r *OpenAICodexDownstreamSessionRequest) { r.LegacyNamespace = "account:9223372036854775808" },
		"no legacy owner": func(r *OpenAICodexDownstreamSessionRequest) {
			r.LegacySessionMappingKeys = []string{strings.Repeat("e", 64)}
		},
		"invalid legacy digest": func(r *OpenAICodexDownstreamSessionRequest) {
			r.LegacyNamespace, r.LegacySessionMappingKeys = "account:11", []string{"raw legacy"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			store := newOpenAICodexIdentityLocalStore()
			request := localDownstreamSessionRequest()
			mutate(&request)
			_, err := store.ResolveCodexDownstreamSession(context.Background(), request, time.Minute)
			require.Error(t, err)
			require.Empty(t, store.entries)
		})
	}
	store := newOpenAICodexIdentityLocalStore()
	result, err := store.ResolveCodexDownstreamSession(context.Background(), localDownstreamSessionRequest(), time.Minute)
	require.NoError(t, err)
	require.Empty(t, result.LegacyNamespace, "a fresh session needs no legacy source")
	require.False(t, result.Reused)
}

func TestCodexDownstreamLocalStoreValidatesThreadInputsBeforeClaim(t *testing.T) {
	for name, mutate := range map[string]func(*OpenAICodexDownstreamThreadRequest){
		"padded session UUID": func(r *OpenAICodexDownstreamThreadRequest) { r.SessionID += " " },
		"padded thread UUID":  func(r *OpenAICodexDownstreamThreadRequest) { r.CandidateThreadID += " " },
		"invalid thread key":  func(r *OpenAICodexDownstreamThreadRequest) { r.ThreadMappings[0].ThreadMappingKey = "raw child" },
		"unanchored thread": func(r *OpenAICodexDownstreamThreadRequest) {
			r.ThreadMappings[0].SessionMappingKey = strings.Repeat("f", 64)
		},
		"missing legacy owner": func(r *OpenAICodexDownstreamThreadRequest) {
			r.LegacyThreadMappings = []OpenAICodexThreadAliasMapping{{SessionMappingKey: strings.Repeat("e", 64), ThreadMappingKey: strings.Repeat("f", 64)}}
		},
		"invalid legacy key": func(r *OpenAICodexDownstreamThreadRequest) {
			r.LegacyNamespace = "account:11"
			r.LegacyThreadMappings = []OpenAICodexThreadAliasMapping{{SessionMappingKey: "raw legacy", ThreadMappingKey: strings.Repeat("f", 64)}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			store := newOpenAICodexIdentityLocalStore()
			_, err := store.ResolveCodexDownstreamSession(context.Background(), localDownstreamSessionRequest(), time.Minute)
			require.NoError(t, err)
			before := store.entries[downstreamSessionEntryKey(strings.Repeat("a", 64))].expires
			request := localDownstreamThreadRequest()
			mutate(&request)
			_, err = store.ResolveCodexDownstreamThread(context.Background(), request, time.Hour)
			require.Error(t, err)
			require.Len(t, store.entries, 2)
			require.Equal(t, before, store.entries[downstreamSessionEntryKey(strings.Repeat("a", 64))].expires)
		})
	}
}

func TestCodexDownstreamLocalStoreSessionMalformedOrConflictDoesNotRefreshAliases(t *testing.T) {
	for _, scenario := range []string{"malformed tuple", "padded UUID", "invalid namespace", "different UUID", "different source"} {
		t.Run(scenario, func(t *testing.T) {
			store := newOpenAICodexIdentityLocalStore()
			request := localDownstreamSessionRequest()
			_, err := store.ResolveCodexDownstreamSession(context.Background(), request, time.Minute)
			require.NoError(t, err)
			first := store.entries[downstreamSessionEntryKey(request.SessionMappingKeys[0])]
			second := store.entries[downstreamSessionEntryKey(request.SessionMappingKeys[1])]
			expectedErr := ErrOpenAIOutboundSessionIdentityStoredValueInvalid
			switch scenario {
			case "malformed tuple":
				second.identity.ThreadID = localDownstreamThreadA
			case "padded UUID":
				second.identity.SessionID += " "
				second.identity.ThreadID += " "
			case "invalid namespace":
				second.legacyNamespace = "account:0"
			case "different UUID":
				second.identity.SessionID, second.identity.ThreadID = localDownstreamSessionB, localDownstreamSessionB
				expectedErr = ErrOpenAICodexAliasConflict
			case "different source":
				second.legacyNamespace = "account:11"
				expectedErr = ErrOpenAICodexAliasConflict
			}
			firstExpiry, secondExpiry := first.expires, second.expires
			firstIdentity, secondIdentity := first.identity, second.identity
			_, err = store.ResolveCodexDownstreamSession(context.Background(), request, time.Hour)
			require.ErrorIs(t, err, expectedErr)
			require.Equal(t, firstExpiry, first.expires)
			require.Equal(t, secondExpiry, second.expires)
			require.Equal(t, firstIdentity, first.identity)
			require.Equal(t, secondIdentity, second.identity)
		})
	}
}

func TestCodexDownstreamLocalStoreThreadMalformedOrConflictDoesNotRefreshAliases(t *testing.T) {
	for _, scenario := range []string{"malformed session", "invalid namespace", "malformed thread", "different thread"} {
		t.Run(scenario, func(t *testing.T) {
			store := newOpenAICodexIdentityLocalStore()
			_, err := store.ResolveCodexDownstreamSession(context.Background(), localDownstreamSessionRequest(), time.Minute)
			require.NoError(t, err)
			request := localDownstreamThreadRequest()
			_, err = store.ResolveCodexDownstreamThread(context.Background(), request, time.Minute)
			require.NoError(t, err)
			session := store.entries[downstreamSessionEntryKey(request.SessionMappingKeys[1])]
			second := store.entries[downstreamThreadEntryKey(request.ThreadMappings[1].SessionMappingKey, request.ThreadMappings[1].ThreadMappingKey)]
			expectedErr := ErrOpenAIOutboundSessionIdentityStoredValueInvalid
			switch scenario {
			case "malformed session":
				session.identity.ThreadID = localDownstreamThreadB
			case "invalid namespace":
				session.legacyNamespace = "account:0"
			case "malformed thread":
				second.identity.ThreadID = localDownstreamSessionA
			case "different thread":
				second.identity.ThreadID = localDownstreamThreadB
				expectedErr = ErrOpenAICodexAliasConflict
			}
			expiries := make(map[string]time.Time, len(store.entries))
			for key, entry := range store.entries {
				expiries[key] = entry.expires
			}
			_, err = store.ResolveCodexDownstreamThread(context.Background(), request, time.Hour)
			require.ErrorIs(t, err, expectedErr)
			for key, expiry := range expiries {
				require.Equal(t, expiry, store.entries[key].expires, key)
			}
		})
	}
}

func TestCodexDownstreamSyntheticScopeRejectsPaddedUUID(t *testing.T) {
	require.NoError(t, validateCodexDownstreamScope("synthetic:"+localDownstreamSessionA, 0))
	for _, id := range []string{" " + localDownstreamSessionA, localDownstreamSessionA + " "} {
		require.ErrorIs(t, validateCodexDownstreamScope("synthetic:"+id, 0), ErrOpenAICodexDownstreamIdentityScopeMissing)
	}
}
