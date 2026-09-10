package repository

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

const (
	downstreamTestSessionA = "018f5c3c-6e3a-7abc-8def-1234567890ab"
	downstreamTestSessionB = "018f5c3c-6e3a-7abd-8def-1234567890ac"
	downstreamTestThreadA  = "018f5c3c-6e3a-7abe-8def-1234567890ad"
	downstreamTestThreadB  = "018f5c3c-6e3a-7abf-8def-1234567890ae"
)

func newDownstreamIdentityRedisTestStore(t *testing.T) (*openAICodexDownstreamIdentityRedisStore, *miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return &openAICodexDownstreamIdentityRedisStore{rdb: rdb}, mr, rdb
}

func downstreamTestDigest(character string) string { return strings.Repeat(character, 64) }

func downstreamTestSessionRequest() service.OpenAICodexDownstreamSessionRequest {
	return service.OpenAICodexDownstreamSessionRequest{
		SessionMappingKeys:       []string{downstreamTestDigest("a"), downstreamTestDigest("b")},
		LegacySessionMappingKeys: []string{downstreamTestDigest("c")},
		LegacyNamespace:          "account:11",
		CandidateSessionID:       downstreamTestSessionB,
	}
}

func downstreamTestThreadRequest(session service.OpenAICodexDownstreamSessionResolution) service.OpenAICodexDownstreamThreadRequest {
	return service.OpenAICodexDownstreamThreadRequest{
		SessionMappingKeys: []string{downstreamTestDigest("a"), downstreamTestDigest("b")},
		ThreadMappings: []service.OpenAICodexThreadAliasMapping{
			{SessionMappingKey: downstreamTestDigest("a"), ThreadMappingKey: downstreamTestDigest("d")},
			{SessionMappingKey: downstreamTestDigest("b"), ThreadMappingKey: downstreamTestDigest("e")},
		},
		LegacyThreadMappings: []service.OpenAICodexThreadAliasMapping{
			{SessionMappingKey: downstreamTestDigest("c"), ThreadMappingKey: downstreamTestDigest("f")},
		},
		SessionID:         session.SessionID,
		LegacyNamespace:   session.LegacyNamespace,
		CandidateThreadID: downstreamTestThreadB,
	}
}

func TestOpenAICodexDownstreamRedisSessionInheritsAndPreservesSource(t *testing.T) {
	store, mr, rdb := newDownstreamIdentityRedisTestStore(t)
	ctx := context.Background()
	request := downstreamTestSessionRequest()
	legacyKey := OpenAICodexSessionIdentityRedisKey(request.LegacySessionMappingKeys[0])
	legacyPayload := `{"session_id":"` + downstreamTestSessionA + `"}`
	require.NoError(t, rdb.Set(ctx, legacyKey, legacyPayload, 10*time.Minute).Err())

	first, err := store.ResolveCodexDownstreamSession(ctx, request, time.Minute)
	require.NoError(t, err)
	require.Equal(t, downstreamTestSessionA, first.SessionID)
	require.Equal(t, "account:11", first.LegacyNamespace)
	require.True(t, first.Reused)
	for _, mapping := range request.SessionMappingKeys {
		key := OpenAICodexDownstreamSessionIdentityRedisKey(mapping)
		raw, getErr := mr.Get(key)
		require.NoError(t, getErr)
		require.JSONEq(t, `{"session_id":"`+downstreamTestSessionA+`","legacy_namespace":"account:11"}`, raw)
		require.Equal(t, time.Minute, mr.TTL(key))
	}

	mr.FastForward(30 * time.Second)
	request.LegacyNamespace = "account:22"
	request.LegacySessionMappingKeys = []string{downstreamTestDigest("d")}
	otherLegacyKey := OpenAICodexSessionIdentityRedisKey(request.LegacySessionMappingKeys[0])
	// Once the downstream record exists, another account's old state is irrelevant.
	require.NoError(t, rdb.Set(ctx, otherLegacyKey, "malformed", 10*time.Minute).Err())
	second, err := store.ResolveCodexDownstreamSession(ctx, request, time.Minute)
	require.NoError(t, err)
	require.Equal(t, first, second)
	require.Equal(t, time.Minute, mr.TTL(OpenAICodexDownstreamSessionIdentityRedisKey(request.SessionMappingKeys[0])))
	stored, err := mr.Get(legacyKey)
	require.NoError(t, err)
	require.Equal(t, legacyPayload, stored)
	require.Equal(t, 9*time.Minute+30*time.Second, mr.TTL(legacyKey), "migration must not refresh legacy TTL")
}

func TestOpenAICodexDownstreamRedisFreshSessionNeverAdoptsLaterLegacySource(t *testing.T) {
	store, mr, _ := newDownstreamIdentityRedisTestStore(t)
	request := downstreamTestSessionRequest()
	first, err := store.ResolveCodexDownstreamSession(context.Background(), request, 0)
	require.NoError(t, err)
	require.Equal(t, downstreamTestSessionB, first.SessionID)
	require.Empty(t, first.LegacyNamespace)
	require.False(t, first.Reused)
	require.Equal(t, 30*24*time.Hour, mr.TTL(OpenAICodexDownstreamSessionIdentityRedisKey(request.SessionMappingKeys[0])))

	require.NoError(t, mr.Set(OpenAICodexSessionIdentityRedisKey(request.LegacySessionMappingKeys[0]), `{"session_id":"`+downstreamTestSessionA+`"}`))
	second, err := store.ResolveCodexDownstreamSession(context.Background(), request, time.Minute)
	require.NoError(t, err)
	require.Equal(t, first.SessionID, second.SessionID)
	require.Empty(t, second.LegacyNamespace)
	require.True(t, second.Reused)
}

func TestOpenAICodexDownstreamRedisConcurrentFirstAccountWins(t *testing.T) {
	store, mr, _ := newDownstreamIdentityRedisTestStore(t)
	ctx := context.Background()
	requests := []service.OpenAICodexDownstreamSessionRequest{downstreamTestSessionRequest(), downstreamTestSessionRequest()}
	requests[1].LegacyNamespace = "account:22"
	requests[1].LegacySessionMappingKeys = []string{downstreamTestDigest("d")}
	require.NoError(t, mr.Set(OpenAICodexSessionIdentityRedisKey(requests[0].LegacySessionMappingKeys[0]), `{"session_id":"`+downstreamTestSessionA+`"}`))
	require.NoError(t, mr.Set(OpenAICodexSessionIdentityRedisKey(requests[1].LegacySessionMappingKeys[0]), `{"session_id":"`+downstreamTestSessionB+`"}`))

	const callers = 24
	results := make([]service.OpenAICodexDownstreamSessionResolution, callers)
	errors := make([]error, callers)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			results[index], errors[index] = store.ResolveCodexDownstreamSession(ctx, requests[index%2], time.Minute)
		}(i)
	}
	close(start)
	wg.Wait()
	for i := range results {
		require.NoError(t, errors[i])
		require.Equal(t, results[0], results[i])
	}
	expectedSession := map[string]string{"account:11": downstreamTestSessionA, "account:22": downstreamTestSessionB}
	require.Equal(t, expectedSession[results[0].LegacyNamespace], results[0].SessionID)
}

func TestOpenAICodexDownstreamRedisThreadLazilyInheritsChosenSessionSource(t *testing.T) {
	store, mr, rdb := newDownstreamIdentityRedisTestStore(t)
	ctx := context.Background()
	sessionRequest := downstreamTestSessionRequest()
	legacySessionKey := OpenAICodexSessionIdentityRedisKey(sessionRequest.LegacySessionMappingKeys[0])
	require.NoError(t, rdb.Set(ctx, legacySessionKey, `{"session_id":"`+downstreamTestSessionA+`"}`, 10*time.Minute).Err())
	session, err := store.ResolveCodexDownstreamSession(ctx, sessionRequest, time.Minute)
	require.NoError(t, err)
	request := downstreamTestThreadRequest(session)
	legacyThreadKey := OpenAICodexThreadIdentityRedisKey(request.LegacyThreadMappings[0].SessionMappingKey, request.LegacyThreadMappings[0].ThreadMappingKey)
	legacyThreadPayload := `{"session_id":"` + downstreamTestSessionA + `","thread_id":"` + downstreamTestThreadA + `"}`
	require.NoError(t, rdb.Set(ctx, legacyThreadKey, legacyThreadPayload, 10*time.Minute).Err())
	mr.FastForward(30 * time.Second)
	thread, err := store.ResolveCodexDownstreamThread(ctx, request, time.Minute)
	require.NoError(t, err)
	require.Equal(t, downstreamTestThreadA, thread.ThreadID)
	require.Equal(t, downstreamTestSessionA, thread.SessionID)
	for _, mapping := range request.SessionMappingKeys {
		require.Equal(t, time.Minute, mr.TTL(OpenAICodexDownstreamSessionIdentityRedisKey(mapping)))
	}
	for _, mapping := range request.ThreadMappings {
		key := OpenAICodexDownstreamThreadIdentityRedisKey(mapping.SessionMappingKey, mapping.ThreadMappingKey)
		require.Equal(t, time.Minute, mr.TTL(key))
		stored, getErr := mr.Get(key)
		require.NoError(t, getErr)
		require.JSONEq(t, legacyThreadPayload, stored)
	}
	stored, err := mr.Get(legacyThreadKey)
	require.NoError(t, err)
	require.Equal(t, legacyThreadPayload, stored)
	require.Equal(t, 9*time.Minute+30*time.Second, mr.TTL(legacyThreadKey))
	require.Equal(t, 9*time.Minute+30*time.Second, mr.TTL(legacySessionKey))

	// A retry after selecting another account still resolves the already claimed child.
	request.LegacyThreadMappings = nil
	threadAgain, err := store.ResolveCodexDownstreamThread(ctx, request, time.Minute)
	require.NoError(t, err)
	require.Equal(t, thread, threadAgain)
	request.LegacyNamespace = "account:22"
	_, err = store.ResolveCodexDownstreamThread(ctx, request, time.Minute)
	require.ErrorIs(t, err, service.ErrOpenAICodexSessionWinnerChanged)
}

func TestOpenAICodexDownstreamRedisChangedLegacySessionCannotSupplyChild(t *testing.T) {
	store, mr, _ := newDownstreamIdentityRedisTestStore(t)
	ctx := context.Background()
	sessionRequest := downstreamTestSessionRequest()
	legacySessionKey := OpenAICodexSessionIdentityRedisKey(sessionRequest.LegacySessionMappingKeys[0])
	require.NoError(t, mr.Set(legacySessionKey, `{"session_id":"`+downstreamTestSessionA+`"}`))
	session, err := store.ResolveCodexDownstreamSession(ctx, sessionRequest, time.Minute)
	require.NoError(t, err)
	request := downstreamTestThreadRequest(session)
	require.NoError(t, mr.Set(legacySessionKey, `{"session_id":"`+downstreamTestSessionB+`"}`))
	legacyThreadKey := OpenAICodexThreadIdentityRedisKey(request.LegacyThreadMappings[0].SessionMappingKey, request.LegacyThreadMappings[0].ThreadMappingKey)
	require.NoError(t, mr.Set(legacyThreadKey, "irrelevant old thread"))
	thread, err := store.ResolveCodexDownstreamThread(ctx, request, time.Minute)
	require.NoError(t, err)
	require.Equal(t, downstreamTestThreadB, thread.ThreadID)
}

func TestOpenAICodexDownstreamRedisMappingKeysIsolateCallers(t *testing.T) {
	store, _, _ := newDownstreamIdentityRedisTestStore(t)
	ctx := context.Background()
	first := downstreamTestSessionRequest()
	first.LegacySessionMappingKeys = nil
	first.LegacyNamespace = ""
	first.CandidateSessionID = downstreamTestSessionA
	second := first
	second.SessionMappingKeys = []string{downstreamTestDigest("d")}
	second.CandidateSessionID = downstreamTestSessionB
	a, err := store.ResolveCodexDownstreamSession(ctx, first, time.Minute)
	require.NoError(t, err)
	b, err := store.ResolveCodexDownstreamSession(ctx, second, time.Minute)
	require.NoError(t, err)
	require.NotEqual(t, a.SessionID, b.SessionID)
}

func TestOpenAICodexDownstreamRedisSessionInvalidRecordsFailBeforeClaim(t *testing.T) {
	for name, payload := range map[string]string{
		"malformed":         "not-json",
		"missing namespace": `{"session_id":"` + downstreamTestSessionA + `"}`,
		"wrong UUID":        `{"session_id":"018f5c3c-6e3a-4abc-8def-1234567890ab","legacy_namespace":""}`,
		"extra field":       `{"session_id":"` + downstreamTestSessionA + `","legacy_namespace":"","extra":true}`,
		"duplicate field":   `{"session_id":"` + downstreamTestSessionA + `","session_id":"` + downstreamTestSessionA + `","legacy_namespace":""}`,
		"invalid owner":     `{"session_id":"` + downstreamTestSessionA + `","legacy_namespace":"account:0"}`,
		"oversized owner":   `{"session_id":"` + downstreamTestSessionA + `","legacy_namespace":"account:9223372036854775808"}`,
	} {
		t.Run(name, func(t *testing.T) {
			store, mr, _ := newDownstreamIdentityRedisTestStore(t)
			request := downstreamTestSessionRequest()
			badKey := OpenAICodexDownstreamSessionIdentityRedisKey(request.SessionMappingKeys[1])
			require.NoError(t, mr.Set(badKey, payload))
			_, err := store.ResolveCodexDownstreamSession(context.Background(), request, time.Minute)
			require.ErrorIs(t, err, service.ErrOpenAIOutboundSessionIdentityStoredValueInvalid)
			require.False(t, mr.Exists(OpenAICodexDownstreamSessionIdentityRedisKey(request.SessionMappingKeys[0])))
			stored, err := mr.Get(badKey)
			require.NoError(t, err)
			require.Equal(t, payload, stored)
		})
	}
	t.Run("invalid legacy", func(t *testing.T) {
		store, mr, _ := newDownstreamIdentityRedisTestStore(t)
		request := downstreamTestSessionRequest()
		require.NoError(t, mr.Set(OpenAICodexSessionIdentityRedisKey(request.LegacySessionMappingKeys[0]), `{"session_id":"invalid"}`))
		_, err := store.ResolveCodexDownstreamSession(context.Background(), request, time.Minute)
		require.ErrorIs(t, err, service.ErrOpenAIOutboundSessionIdentityStoredValueInvalid)
		require.False(t, mr.Exists(OpenAICodexDownstreamSessionIdentityRedisKey(request.SessionMappingKeys[0])))
	})
	t.Run("wrong Redis type", func(t *testing.T) {
		store, mr, rdb := newDownstreamIdentityRedisTestStore(t)
		request := downstreamTestSessionRequest()
		badKey := OpenAICodexDownstreamSessionIdentityRedisKey(request.SessionMappingKeys[1])
		require.NoError(t, rdb.LPush(context.Background(), badKey, "list").Err())
		_, err := store.ResolveCodexDownstreamSession(context.Background(), request, time.Minute)
		require.ErrorIs(t, err, service.ErrOpenAIOutboundSessionIdentityStoredValueInvalid)
		require.False(t, mr.Exists(OpenAICodexDownstreamSessionIdentityRedisKey(request.SessionMappingKeys[0])))
	})
}

func TestOpenAICodexDownstreamRedisSessionConflictingAliasesAreNotClobbered(t *testing.T) {
	for _, conflict := range []struct{ session, namespace string }{
		{downstreamTestSessionB, "account:11"},
		{downstreamTestSessionA, "account:22"},
	} {
		t.Run(conflict.session+conflict.namespace, func(t *testing.T) {
			store, mr, _ := newDownstreamIdentityRedisTestStore(t)
			request := downstreamTestSessionRequest()
			payloads := []string{
				fmt.Sprintf(`{"session_id":%q,"legacy_namespace":"account:11"}`, downstreamTestSessionA),
				fmt.Sprintf(`{"session_id":%q,"legacy_namespace":%q}`, conflict.session, conflict.namespace),
			}
			for i, payload := range payloads {
				require.NoError(t, mr.Set(OpenAICodexDownstreamSessionIdentityRedisKey(request.SessionMappingKeys[i]), payload))
			}
			_, err := store.ResolveCodexDownstreamSession(context.Background(), request, time.Minute)
			require.ErrorIs(t, err, service.ErrOpenAICodexAliasConflict)
			for i, payload := range payloads {
				key := OpenAICodexDownstreamSessionIdentityRedisKey(request.SessionMappingKeys[i])
				stored, getErr := mr.Get(key)
				require.NoError(t, getErr)
				require.Equal(t, payload, stored)
				require.Zero(t, mr.TTL(key))
			}
		})
	}
}

func TestOpenAICodexDownstreamRedisThreadRejectsInvalidOrConflictingState(t *testing.T) {
	for _, scenario := range []string{"malformed session", "missing session", "wrong session", "malformed thread", "duplicate thread", "foreign thread", "conflicting thread", "malformed legacy thread"} {
		t.Run(scenario, func(t *testing.T) {
			store, mr, _ := newDownstreamIdentityRedisTestStore(t)
			ctx := context.Background()
			sessionRequest := downstreamTestSessionRequest()
			require.NoError(t, mr.Set(OpenAICodexSessionIdentityRedisKey(sessionRequest.LegacySessionMappingKeys[0]), `{"session_id":"`+downstreamTestSessionA+`"}`))
			session, err := store.ResolveCodexDownstreamSession(ctx, sessionRequest, time.Minute)
			require.NoError(t, err)
			request := downstreamTestThreadRequest(session)
			sessionKey := OpenAICodexDownstreamSessionIdentityRedisKey(request.SessionMappingKeys[1])
			firstKey := OpenAICodexDownstreamThreadIdentityRedisKey(request.ThreadMappings[0].SessionMappingKey, request.ThreadMappings[0].ThreadMappingKey)
			secondKey := OpenAICodexDownstreamThreadIdentityRedisKey(request.ThreadMappings[1].SessionMappingKey, request.ThreadMappings[1].ThreadMappingKey)
			expectedErr := service.ErrOpenAIOutboundSessionIdentityStoredValueInvalid
			switch scenario {
			case "malformed session":
				require.NoError(t, mr.Set(sessionKey, "bad"))
			case "missing session":
				mr.Del(sessionKey)
				expectedErr = service.ErrOpenAICodexSessionWinnerChanged
			case "wrong session":
				require.NoError(t, mr.Set(sessionKey, `{"session_id":"`+downstreamTestSessionB+`","legacy_namespace":"account:11"}`))
				expectedErr = service.ErrOpenAICodexSessionWinnerChanged
			case "malformed thread":
				require.NoError(t, mr.Set(secondKey, "bad"))
			case "duplicate thread":
				require.NoError(t, mr.Set(secondKey, `{"session_id":"`+session.SessionID+`","thread_id":"`+downstreamTestThreadA+`","thread_id":"`+downstreamTestThreadA+`"}`))
			case "foreign thread":
				require.NoError(t, mr.Set(secondKey, `{"session_id":"`+downstreamTestSessionB+`","thread_id":"`+downstreamTestThreadA+`"}`))
			case "conflicting thread":
				require.NoError(t, mr.Set(firstKey, `{"session_id":"`+session.SessionID+`","thread_id":"`+downstreamTestThreadA+`"}`))
				require.NoError(t, mr.Set(secondKey, `{"session_id":"`+session.SessionID+`","thread_id":"`+downstreamTestThreadB+`"}`))
				expectedErr = service.ErrOpenAICodexAliasConflict
			case "malformed legacy thread":
				key := OpenAICodexThreadIdentityRedisKey(request.LegacyThreadMappings[0].SessionMappingKey, request.LegacyThreadMappings[0].ThreadMappingKey)
				require.NoError(t, mr.Set(key, `{"session_id":"`+session.SessionID+`","thread_id":"`+session.SessionID+`"}`))
			}
			beforeFirst, beforeFirstErr := mr.Get(firstKey)
			beforeSecond, beforeSecondErr := mr.Get(secondKey)
			_, err = store.ResolveCodexDownstreamThread(ctx, request, time.Minute)
			require.ErrorIs(t, err, expectedErr)
			afterFirst, afterFirstErr := mr.Get(firstKey)
			afterSecond, afterSecondErr := mr.Get(secondKey)
			require.Equal(t, beforeFirst, afterFirst)
			require.Equal(t, beforeFirstErr, afterFirstErr)
			require.Equal(t, beforeSecond, afterSecond)
			require.Equal(t, beforeSecondErr, afterSecondErr)
		})
	}
}

func TestOpenAICodexDownstreamRedisInputValidation(t *testing.T) {
	store, mr, _ := newDownstreamIdentityRedisTestStore(t)
	for name, mutate := range map[string]func(*service.OpenAICodexDownstreamSessionRequest){
		"no aliases":      func(r *service.OpenAICodexDownstreamSessionRequest) { r.SessionMappingKeys = nil },
		"raw logical key": func(r *service.OpenAICodexDownstreamSessionRequest) { r.SessionMappingKeys = []string{"user-session"} },
		"padded digest": func(r *service.OpenAICodexDownstreamSessionRequest) {
			r.SessionMappingKeys = []string{" " + downstreamTestDigest("a")}
		},
		"missing namespace": func(r *service.OpenAICodexDownstreamSessionRequest) { r.LegacyNamespace = "" },
		"non-account scope": func(r *service.OpenAICodexDownstreamSessionRequest) { r.LegacyNamespace = "downstream:v1" },
		"invalid candidate": func(r *service.OpenAICodexDownstreamSessionRequest) { r.CandidateSessionID = "bad" },
		"padded candidate": func(r *service.OpenAICodexDownstreamSessionRequest) {
			r.CandidateSessionID = " " + downstreamTestSessionA
		},
	} {
		t.Run(name, func(t *testing.T) {
			request := downstreamTestSessionRequest()
			mutate(&request)
			_, err := store.ResolveCodexDownstreamSession(context.Background(), request, time.Minute)
			require.Error(t, err)
			require.Empty(t, mr.Keys())
		})
	}
}

func TestOpenAICodexDownstreamRedisThreadRejectsPaddedUUIDBeforeWriting(t *testing.T) {
	for _, field := range []string{"session", "thread"} {
		t.Run(field, func(t *testing.T) {
			store, mr, _ := newDownstreamIdentityRedisTestStore(t)
			request := downstreamTestThreadRequest(service.OpenAICodexDownstreamSessionResolution{SessionID: downstreamTestSessionA, LegacyNamespace: "account:11"})
			if field == "session" {
				request.SessionID += " "
			} else {
				request.CandidateThreadID += " "
			}
			_, err := store.ResolveCodexDownstreamThread(context.Background(), request, time.Minute)
			require.Error(t, err)
			require.Empty(t, mr.Keys())
		})
	}
}
