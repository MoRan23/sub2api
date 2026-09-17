package service

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func dailyThreadStabilityBody(t *testing.T, session, thread, parent, fork string) []byte {
	t.Helper()
	metadata := map[string]any{
		"session_id": session, "thread_id": thread,
		"turn_id": uuid.Must(uuid.NewV7()).String(),
	}
	if parent != "" {
		metadata["parent_thread_id"] = parent
	}
	if fork != "" {
		metadata["forked_from_thread_id"] = fork
	}
	nested, err := json.Marshal(metadata)
	require.NoError(t, err)
	body, err := json.Marshal(map[string]any{
		"model": "gpt-5.4", "stream": true, "input": "next turn",
		"client_metadata": map[string]any{
			"session_id": session, "thread_id": thread, openAIWSTurnMetadataHeader: string(nested),
		},
	})
	require.NoError(t, err)
	return body
}

// Each materialization uses a new HTTP request context and a different turn ID.
// Checking the projected carriers prevents a stable plan from hiding a second
// identity allocation during final wire construction.
func dailyThreadStabilityResolve(t *testing.T, svc *OpenAIGatewayService, account *Account, apiKeyID int64, session, thread, parent, fork string) OpenAICodexTurnIdentity {
	t.Helper()
	body := dailyThreadStabilityBody(t, session, thread, parent, fork)
	return dailyThreadStabilityResolveBody(t, svc, account, apiKeyID, body)
}

func dailyThreadStabilityResolveBody(t *testing.T, svc *OpenAIGatewayService, account *Account, apiKeyID int64, body []byte) OpenAICodexTurnIdentity {
	t.Helper()
	c := newOutboundIdentityTestContext(t, nil)
	c.Set("api_key", &APIKey{ID: apiKeyID})
	setOpenAIClientRequestedStream(c, true)
	plan, err := svc.ResolveOpenAIOAuthIdentityPlan(context.Background(), c, account,
		CaptureOpenAIOAuthIdentity(c, body, ""),
		OpenAIOAuthIdentityPlanOptions{TurnIdentityEnabled: true, InstallationPolicy: OpenAIOAuthInstallationPreserve})
	require.NoError(t, err)
	plan, err = FinalizeOpenAICodexWirePlan(plan, "turn", CodexModelCapabilities{})
	require.NoError(t, err)
	headers := make(http.Header)
	outbound, err := ApplyOpenAIOAuthIdentityPlan(headers, body, plan)
	require.NoError(t, err)
	identity := plan.TurnIdentity
	require.NoError(t, ValidateOpenAICodexTurnIdentity(identity))
	if svc.oauthDailySessionRotationEnabled(context.Background()) {
		require.False(t, IsNewOpenAICodexSession(c, identity.SessionID), "allocating a logical child must not mark the existing pool root as newly created")
	}
	for _, carrier := range []string{headers.Get(openAIWSTurnMetadataHeader), gjson.GetBytes(outbound, "client_metadata.x-codex-turn-metadata").String()} {
		require.Equal(t, identity.SessionID, gjson.Get(carrier, "session_id").String())
		require.Equal(t, identity.ThreadID, gjson.Get(carrier, "thread_id").String())
		require.Equal(t, identity.ParentThreadID, gjson.Get(carrier, "parent_thread_id").String())
		require.Equal(t, identity.ForkedFromThreadID, gjson.Get(carrier, "forked_from_thread_id").String())
	}
	return identity
}

func withDailyThreadStabilityStores(t *testing.T, daily bool, run func(*testing.T, *OpenAIGatewayService, *Account)) {
	t.Helper()
	for _, name := range []string{"local fallback", "primary store"} {
		t.Run(name, func(t *testing.T) {
			resetProcessCodexIdentityStore(t)
			svc, account := guardianIdentityService(t, daily)
			if name == "primary store" {
				svc.cache = &outboundIdentityGatewayCacheStub{}
			}
			run(t, svc, account)
		})
	}
}

func TestOAuthDailyThreadStabilityRepeatedHTTPMainThread(t *testing.T) {
	withDailyThreadStabilityStores(t, true, func(t *testing.T, svc *OpenAIGatewayService, account *Account) {
		session := uuid.Must(uuid.NewV7()).String()
		root := svc.oauthDailySessionRepo.(*fakeOAuthDailyAffinityRepository).affinity.StreamSessionID
		first := dailyThreadStabilityResolve(t, svc, account, 9972, session, session, "", "")
		require.Equal(t, root, first.SessionID)
		require.NotEqual(t, root, first.ThreadID)
		require.Equal(t, root, first.ParentThreadID)
		require.Equal(t, OpenAICodexTurnRelationDescendant, first.Relation)
		for range 4 {
			next := dailyThreadStabilityResolve(t, svc, account, 9972, session, session, "", "")
			require.Equal(t, first, next, "new HTTP turns in one logical thread must reuse its daily child")
		}
	})
}

func TestOAuthDailyThreadStabilityRealSubthreadsKeepMappedLineage(t *testing.T) {
	withDailyThreadStabilityStores(t, true, func(t *testing.T, svc *OpenAIGatewayService, account *Account) {
		session, thread := uuid.Must(uuid.NewV7()).String(), uuid.Must(uuid.NewV7()).String()
		main := dailyThreadStabilityResolve(t, svc, account, 9972, session, session, "", "")
		child := dailyThreadStabilityResolve(t, svc, account, 9972, session, thread, session, session)
		require.Equal(t, main.SessionID, child.SessionID)
		require.NotEqual(t, main.ThreadID, child.ThreadID)
		require.Equal(t, main.ThreadID, child.ParentThreadID, "a real child's parent is the materialized logical main thread")
		require.Equal(t, main.ThreadID, child.ForkedFromThreadID)
		for range 3 {
			require.Equal(t, child, dailyThreadStabilityResolve(t, svc, account, 9972, session, thread, session, session))
		}
		grandchildKey := uuid.Must(uuid.NewV7()).String()
		grandchild := dailyThreadStabilityResolve(t, svc, account, 9972, session, grandchildKey, thread, thread)
		require.Equal(t, child.ThreadID, grandchild.ParentThreadID)
		require.Equal(t, child.ThreadID, grandchild.ForkedFromThreadID)
		require.NotEqual(t, child.ThreadID, grandchild.ThreadID)
		require.Equal(t, grandchild, dailyThreadStabilityResolve(t, svc, account, 9972, session, grandchildKey, thread, thread))
		require.Equal(t, main, dailyThreadStabilityResolve(t, svc, account, 9972, session, session, "", ""))
	})
}

func TestOAuthDailyThreadStabilityIsolatesAPIKeyAccountAndLogicalSession(t *testing.T) {
	withDailyThreadStabilityStores(t, true, func(t *testing.T, svc *OpenAIGatewayService, account *Account) {
		session := uuid.Must(uuid.NewV7()).String()
		first := dailyThreadStabilityResolve(t, svc, account, 9972, session, session, "", "")
		otherKey := dailyThreadStabilityResolve(t, svc, account, 9973, session, session, "", "")
		otherAccount := *account
		otherAccount.ID++
		otherOwner := dailyThreadStabilityResolve(t, svc, &otherAccount, 9972, session, session, "", "")
		otherSession := uuid.Must(uuid.NewV7()).String()
		otherLogical := dailyThreadStabilityResolve(t, svc, account, 9972, otherSession, otherSession, "", "")
		// This fixture intentionally returns the same pooled root for every lookup;
		// the child mapping must still retain all ownership and logical boundaries.
		seen := map[string]bool{}
		for _, identity := range []OpenAICodexTurnIdentity{first, otherKey, otherOwner, otherLogical} {
			require.Equal(t, first.SessionID, identity.SessionID)
			require.False(t, seen[identity.ThreadID], "different ownership/logical tuples must not share a child")
			seen[identity.ThreadID] = true
		}
		require.Equal(t, first, dailyThreadStabilityResolve(t, svc, account, 9972, session, session, "", ""))
		require.Equal(t, otherKey, dailyThreadStabilityResolve(t, svc, account, 9973, session, session, "", ""))
		require.Equal(t, otherOwner, dailyThreadStabilityResolve(t, svc, &otherAccount, 9972, session, session, "", ""))
		require.Equal(t, otherLogical, dailyThreadStabilityResolve(t, svc, account, 9972, otherSession, otherSession, "", ""))
	})
}

func TestOAuthDailyThreadStabilityScopesChildrenToResolvedDailyRoot(t *testing.T) {
	withDailyThreadStabilityStores(t, true, func(t *testing.T, svc *OpenAIGatewayService, account *Account) {
		session, thread := uuid.Must(uuid.NewV7()).String(), uuid.Must(uuid.NewV7()).String()
		repo := svc.oauthDailySessionRepo.(*fakeOAuthDailyAffinityRepository)
		original := repo.affinity
		firstMain := dailyThreadStabilityResolve(t, svc, account, 9972, session, session, "", "")
		firstChild := dailyThreadStabilityResolve(t, svc, account, 9972, session, thread, session, session)
		// Simulate the repository choosing the next day's pool, without depending
		// on wall-clock midnight or resetting the process's identity store.
		repo.affinity.StreamSessionID = uuid.Must(uuid.NewV7()).String()
		repo.affinity.BusinessDate = OAuthDailyBusinessDate(time.Now().Add(24 * time.Hour))
		repo.affinity.Generation = uuid.Must(uuid.NewV7()).String()
		secondMain := dailyThreadStabilityResolve(t, svc, account, 9972, session, session, "", "")
		secondChild := dailyThreadStabilityResolve(t, svc, account, 9972, session, thread, session, session)
		require.Equal(t, repo.affinity.StreamSessionID, secondMain.SessionID)
		require.NotEqual(t, firstMain.SessionID, secondMain.SessionID)
		require.NotEqual(t, firstMain.ThreadID, secondMain.ThreadID)
		require.NotEqual(t, firstChild.ThreadID, secondChild.ThreadID, "real subthreads must also be scoped to the new daily root")
		require.Equal(t, secondMain.ThreadID, secondChild.ParentThreadID)
		require.Equal(t, secondMain.ThreadID, secondChild.ForkedFromThreadID)
		require.Equal(t, secondMain, dailyThreadStabilityResolve(t, svc, account, 9972, session, session, "", ""))
		require.Equal(t, secondChild, dailyThreadStabilityResolve(t, svc, account, 9972, session, thread, session, session))
		// Resolving an earlier frozen daily root must not collide with the next
		// day's session entry or replace its already materialized child mappings.
		repo.affinity = original
		require.Equal(t, firstMain, dailyThreadStabilityResolve(t, svc, account, 9972, session, session, "", ""))
		require.Equal(t, firstChild, dailyThreadStabilityResolve(t, svc, account, 9972, session, thread, session, session))
	})
}

func TestOAuthDailyThreadStabilityDisabledPreservesLegacyRootAndThreads(t *testing.T) {
	withDailyThreadStabilityStores(t, false, func(t *testing.T, svc *OpenAIGatewayService, account *Account) {
		session, thread := uuid.Must(uuid.NewV7()).String(), uuid.Must(uuid.NewV7()).String()
		main := dailyThreadStabilityResolve(t, svc, account, 9972, session, session, "", "")
		require.Equal(t, OpenAICodexTurnRelationRoot, main.Relation)
		require.Equal(t, main.SessionID, main.ThreadID)
		require.Empty(t, main.ParentThreadID)
		require.NotEqual(t, svc.oauthDailySessionRepo.(*fakeOAuthDailyAffinityRepository).affinity.StreamSessionID, main.SessionID)
		child := dailyThreadStabilityResolve(t, svc, account, 9972, session, thread, session, session)
		require.Equal(t, main.SessionID, child.SessionID)
		require.Equal(t, main.ThreadID, child.ParentThreadID)
		require.Equal(t, main.ThreadID, child.ForkedFromThreadID)
		for range 3 {
			require.Equal(t, main, dailyThreadStabilityResolve(t, svc, account, 9972, session, session, "", ""))
			require.Equal(t, child, dailyThreadStabilityResolve(t, svc, account, 9972, session, thread, session, session))
		}
	})
}

func TestOAuthDailyThreadStabilityWithoutClientIdentity(t *testing.T) {
	withDailyThreadStabilityStores(t, true, func(t *testing.T, svc *OpenAIGatewayService, account *Account) {
		// Neither the headers nor the body contain session/thread/installation IDs.
		body := []byte(`{"model":"gpt-5.4","stream":true,"input":"first request"}`)
		first := dailyThreadStabilityResolveBody(t, svc, account, 9972, body)
		require.Equal(t, svc.oauthDailySessionRepo.(*fakeOAuthDailyAffinityRepository).affinity.StreamSessionID, first.SessionID)
		require.Equal(t, first.SessionID, first.ParentThreadID)
		nextBody := []byte(`{"model":"gpt-5.4","stream":true,"input":"another request"}`)
		require.Equal(t, first, dailyThreadStabilityResolveBody(t, svc, account, 9972, nextBody))
		otherKey := dailyThreadStabilityResolveBody(t, svc, account, 9973, nextBody)
		require.NotEqual(t, first.ThreadID, otherKey.ThreadID)
	})
}

func TestOAuthDailyThreadStabilityConcurrentHTTPMaterialization(t *testing.T) {
	withDailyThreadStabilityStores(t, true, func(t *testing.T, svc *OpenAIGatewayService, account *Account) {
		const requestCount = 24
		session := uuid.Must(uuid.NewV7()).String()
		contexts := make([]*gin.Context, requestCount)
		bodies := make([][]byte, requestCount)
		for i := range requestCount {
			bodies[i] = dailyThreadStabilityBody(t, session, session, "", "")
			contexts[i] = newOutboundIdentityTestContext(t, nil)
			contexts[i].Set("api_key", &APIKey{ID: 9972})
			setOpenAIClientRequestedStream(contexts[i], true)
		}
		type result struct {
			identity OpenAICodexTurnIdentity
			newRoot  bool
			err      error
		}
		results := make(chan result, requestCount)
		start := make(chan struct{})
		for i := range requestCount {
			go func() {
				<-start
				c := contexts[i]
				plan, err := svc.ResolveOpenAIOAuthIdentityPlan(context.Background(), c, account,
					CaptureOpenAIOAuthIdentity(c, bodies[i], ""),
					OpenAIOAuthIdentityPlanOptions{TurnIdentityEnabled: true, InstallationPolicy: OpenAIOAuthInstallationPreserve})
				results <- result{identity: plan.TurnIdentity, newRoot: IsNewOpenAICodexSession(c, plan.TurnIdentity.SessionID), err: err}
			}()
		}
		close(start)
		var first OpenAICodexTurnIdentity
		for i := range requestCount {
			result := <-results
			require.NoError(t, result.err)
			require.NoError(t, ValidateOpenAICodexTurnIdentity(result.identity))
			require.False(t, result.newRoot)
			if i == 0 {
				first = result.identity
			}
			require.Equal(t, first, result.identity, "competing requests must use the single store winner")
		}
		require.Equal(t, first, dailyThreadStabilityResolve(t, svc, account, 9972, session, session, "", ""))
	})
}

func TestOAuthDailyThreadStabilityPrimaryStoreSurvivesLocalReset(t *testing.T) {
	resetProcessCodexIdentityStore(t)
	svc, account := guardianIdentityService(t, true)
	cache := &outboundIdentityGatewayCacheStub{}
	svc.cache = cache
	session, thread := uuid.Must(uuid.NewV7()).String(), uuid.Must(uuid.NewV7()).String()
	main := dailyThreadStabilityResolve(t, svc, account, 9972, session, session, "", "")
	child := dailyThreadStabilityResolve(t, svc, account, 9972, session, thread, session, session)
	processOpenAICodexTurnIdentityStore = newOpenAICodexIdentityLocalStore()
	otherService, _ := guardianIdentityService(t, true)
	otherService.cache = cache
	otherService.oauthDailySessionRepo = svc.oauthDailySessionRepo
	// Resolve the child first, as another gateway process can receive a subagent
	// before it receives the corresponding main thread's next turn.
	require.Equal(t, child, dailyThreadStabilityResolve(t, otherService, account, 9972, session, thread, session, session))
	require.Equal(t, main, dailyThreadStabilityResolve(t, otherService, account, 9972, session, session, "", ""))
}

func TestOAuthDailyThreadStabilityForwardObservesOneThreadAcrossHTTPRequests(t *testing.T) {
	resetProcessCodexIdentityStore(t)
	enableOpenAIIdentityPathFingerprintObservation(t)
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		openAICompatSSECompletedResponse("resp_daily_stable_first", "gpt-5.4"),
		openAICompatSSECompletedResponse("resp_daily_stable_second", "gpt-5.4"),
	}}
	svc, _ := newOpenAIIdentityPathService(t, true, upstream)
	daily, _ := guardianIdentityService(t, true)
	svc.settingService = daily.settingService
	svc.oauthDailySessionRepo = daily.oauthDailySessionRepo
	account := newOpenAIIdentityPathOAuthAccount(9971)
	session := uuid.Must(uuid.NewV7()).String()
	var first OpenAIOutboundSessionIdentity
	for i := range 2 {
		body := dailyThreadStabilityBody(t, session, session, "", "")
		c, _ := newOpenAIIdentityPathContext(t, "/v1/responses", body, 9972)
		result, err := svc.Forward(context.Background(), c, account, body)
		require.NoError(t, err)
		require.NotNil(t, result)
		identity := requireOpenAIIdentityPathPair(t, upstream.lastReq.Header, upstream.lastBody)
		require.Equal(t, daily.oauthDailySessionRepo.(*fakeOAuthDailyAffinityRepository).affinity.StreamSessionID, identity.SessionID)
		if i == 0 {
			first = identity
		}
		require.Equal(t, first, identity, "actual physical HTTP requests must carry the same daily child")
	}
	entries := SnapshotFingerprintObservations(0)
	require.Len(t, entries, 2)
	for _, entry := range entries {
		require.Equal(t, first.SessionID, entry.SessionID)
		require.Equal(t, first.ThreadID, entry.ThreadID)
		require.Equal(t, first.SessionID, entry.ParentThreadID)
		require.True(t, entry.DailyFixedRootEnabled)
	}
	snapshot := buildFingerprintObservationSnapshot("daily-stability", [32]byte{}, time.Now(), time.Now().Add(time.Minute), entries)
	require.Len(t, snapshot.sessionsByID, 1)
	require.Len(t, snapshot.threadsByID, 1)
	for _, node := range snapshot.sessionsByID {
		require.Equal(t, 1, node.summary.ThreadCount)
		require.Equal(t, 2, node.summary.ObservationCount)
	}
	for _, node := range snapshot.threadsByID {
		require.Equal(t, 2, node.summary.ObservationCount)
	}
}
