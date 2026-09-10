package service

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

var downstreamCompactDeliveredEvents = [][]byte{
	[]byte(`{"type":"response.output_item.done","item":{"id":"cmp_downstream","type":"compaction"}}`),
	[]byte(`{"type":"response.completed","response":{"status":"completed","output":[{"id":"cmp_downstream","type":"compaction"}]}}`),
}

// A migrated window retains its legacy storage key, even though all current
// identity and compaction digests belong to the downstream API key namespace.
func newDownstreamCompactionFixture(t *testing.T) (*OpenAIGatewayService, *Account, OpenAIOAuthIdentityPlan) {
	t.Helper()
	svc, account, plan := newOpenAICodexWSCompactWindowTestPlan(t, "downstream-compaction-"+t.Name(), 9821)
	legacyKey, err := OpenAICodexWindowMappingKey(svc.cfg.JWT.Secret, "account:1111", plan.APIKeyID, plan.Window.ThreadID)
	require.NoError(t, err)
	plan, err = BindOpenAICodexWindowToPlan(plan, normalizeOpenAICodexWindowHistory(plan.Window), legacyKey)
	require.NoError(t, err)
	initial, err := svc.ResolveOpenAICodexWindowSnapshot(context.Background(), legacyKey, plan.Window.ThreadID, plan.Window.ContextWindowID)
	require.NoError(t, err)
	require.Equal(t, plan.Window, initial)
	return svc, account, plan
}

func prepareDownstreamCompactionCommit(t *testing.T, transport string, svc *OpenAIGatewayService, account *Account, plan OpenAIOAuthIdentityPlan, events [][]byte) func() OpenAIOAuthIdentityPlan {
	t.Helper()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/compact", nil)
	SetOpenAIOAuthIdentityPlan(c, plan)
	var delivery openAICodexCompactionDelivery
	for _, event := range events {
		delivery.ObserveDeliveredEvent(event)
	}
	switch transport {
	case "http":
		return func() OpenAIOAuthIdentityPlan {
			svc.commitOpenAICodexCompactionAfterDelivery(context.Background(), c, account, plan, CodexCompactionModeRemoteV2, &delivery)
			result, _ := OpenAIOAuthIdentityPlanFromContext(c)
			return result
		}
	case "passthrough":
		svc.prepareOpenAIPassthroughCompactWindow(context.Background(), c, account, c.Request.Clone(context.Background()), []byte(`{"model":"gpt-5.4","input":[]}`), plan)
		require.True(t, openAIPassthroughCompactWindowActive(c))
		return func() OpenAIOAuthIdentityPlan {
			svc.commitDeliveredOpenAIPassthroughCompactWindow(context.Background(), c, account, http.StatusOK, &delivery, "")
			result, _ := OpenAIOAuthIdentityPlanFromContext(c)
			return result
		}
	case "websocket":
		wsDelivery := openAICodexWSCompactionDeliveryForPlan(account, plan)
		require.NotNil(t, wsDelivery)
		for _, event := range events {
			observeOpenAICodexWSCompactionDelivery(wsDelivery, event)
		}
		return func() OpenAIOAuthIdentityPlan {
			svc.commitOpenAICodexWSCompactionAfterDelivery(context.Background(), c, account, &plan, wsDelivery, "")
			return plan
		}
	default:
		t.Fatalf("unexpected transport %q", transport)
		return nil
	}
}

func TestDownstreamCompactionUsesMigratedWindowAcrossAccounts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, transport := range []string{"http", "passthrough", "websocket"} {
		t.Run(transport, func(t *testing.T) {
			svc, firstAccount, firstPlan := newDownstreamCompactionFixture(t)
			secondAccount := *firstAccount
			secondAccount.ID++
			secondPlan := firstPlan
			secondPlan.CredentialOwnerNamespace = fmt.Sprintf("account:%d", secondAccount.ID)

			firstCommit := prepareDownstreamCompactionCommit(t, transport, svc, &secondAccount, secondPlan, downstreamCompactDeliveredEvents)()
			require.Equal(t, uint64(1), firstCommit.Window.Number)
			require.NotEqual(t, firstPlan.Window.ContextWindowID, firstCommit.Window.ContextWindowID)
			require.Equal(t, firstPlan.WindowMappingKey, firstCommit.WindowMappingKey)
			require.Equal(t, firstPlan.TurnIdentity, firstCommit.TurnIdentity)

			// A replay on the previous credential still names the same compact turn.
			replayed := prepareDownstreamCompactionCommit(t, transport, svc, firstAccount, firstPlan, downstreamCompactDeliveredEvents)()
			require.Equal(t, firstCommit.Window, replayed.Window)
			expectedDigest, err := OpenAICodexCompactTurnDigest(svc.cfg.JWT.Secret, firstPlan.TurnIdentityNamespace, firstPlan.APIKeyID, firstPlan.Window, firstPlan.RequestTurn.ID)
			require.NoError(t, err)
			require.Equal(t, expectedDigest, firstCommit.Window.LastCompactDigest)

			nextWindow, err := svc.ResolveOpenAICodexWindowSnapshot(context.Background(), secondPlan.WindowMappingKey, secondPlan.Window.ThreadID, secondPlan.Window.ContextWindowID)
			require.NoError(t, err)
			require.Equal(t, firstCommit.Window, nextWindow)
		})
	}
}

func TestDownstreamCompactionUnsuccessfulDeliveryKeepsSharedWindow(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, transport := range []string{"http", "passthrough", "websocket"} {
		for _, terminal := range []string{"response.failed", "response.cancelled", "response.incomplete", ""} {
			t.Run(transport+"/"+terminal, func(t *testing.T) {
				svc, account, plan := newDownstreamCompactionFixture(t)
				account.ID++
				plan.CredentialOwnerNamespace = fmt.Sprintf("account:%d", account.ID)
				events := [][]byte{downstreamCompactDeliveredEvents[0]}
				if terminal != "" {
					events = append(events, []byte(fmt.Sprintf(`{"type":%q}`, terminal)))
				}
				result := prepareDownstreamCompactionCommit(t, transport, svc, account, plan, events)()
				require.Equal(t, plan.Window, result.Window)
				stored, err := svc.ResolveOpenAICodexWindowSnapshot(context.Background(), plan.WindowMappingKey, plan.Window.ThreadID, plan.Window.ContextWindowID)
				require.NoError(t, err)
				require.Equal(t, plan.Window, stored)
			})
		}
	}
}

func TestDownstreamCompactionReplayOfLegacyDigestDoesNotAdvanceAgain(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, transport := range []string{"http", "passthrough", "websocket"} {
		t.Run(transport, func(t *testing.T) {
			svc, account, frozen := newDownstreamCompactionFixture(t)
			legacyDigest, err := OpenAICodexCompactTurnDigest(svc.cfg.JWT.Secret, "account:1111", frozen.APIKeyID, frozen.Window, frozen.RequestTurn.ID)
			require.NoError(t, err)
			legacyCommit, err := svc.CommitOpenAICodexWindowSnapshot(context.Background(), frozen.WindowMappingKey, frozen.Window, legacyDigest)
			require.NoError(t, err)
			require.Equal(t, OpenAICodexWindowCommitAdvanced, legacyCommit.Status)

			// The old digest remains opaque. Matching the frozen generation prevents
			// its post-migration replay from installing a second replacement window.
			replayed := prepareDownstreamCompactionCommit(t, transport, svc, account, frozen, downstreamCompactDeliveredEvents)()
			require.Equal(t, legacyCommit.Snapshot, replayed.Window)
			require.Equal(t, uint64(1), replayed.Window.Number)
			require.Equal(t, legacyDigest, replayed.Window.LastCompactDigest)
		})
	}
}

func TestDownstreamCompactionConcurrentAccountsAndTransportsAdvanceOnce(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc, account, original := newDownstreamCompactionFixture(t)
	commits := make([]func() OpenAIOAuthIdentityPlan, 0, 12)
	for index := range 12 {
		selected := *account
		selected.ID += int64(index)
		plan := original
		plan.CredentialOwnerNamespace = fmt.Sprintf("account:%d", selected.ID)
		transport := []string{"http", "passthrough", "websocket"}[index%3]
		commits = append(commits, prepareDownstreamCompactionCommit(t, transport, svc, &selected, plan, downstreamCompactDeliveredEvents))
	}
	var group sync.WaitGroup
	results := make(chan OpenAIOAuthIdentityPlan, len(commits))
	for _, commit := range commits {
		group.Add(1)
		go func() {
			defer group.Done()
			results <- commit()
		}()
	}
	group.Wait()
	close(results)
	winner, err := svc.ResolveOpenAICodexWindowSnapshot(context.Background(), original.WindowMappingKey, original.Window.ThreadID, original.Window.ContextWindowID)
	require.NoError(t, err)
	require.Equal(t, uint64(1), winner.Number)
	for result := range results {
		require.Equal(t, winner, result.Window)
	}
}
