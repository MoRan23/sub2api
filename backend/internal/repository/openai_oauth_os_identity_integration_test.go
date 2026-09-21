//go:build integration

package repository

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Exercise the production facade, PostgreSQL daily-root repository, and Redis
// identity scripts together. No upstream transport or provider request is used.
func TestOAuthOSIdentityFacadeRealRedisIsolation(t *testing.T) {
	for _, dailyEnabled := range []bool{false, true} {
		t.Run("daily="+strconv.FormatBool(dailyEnabled), func(t *testing.T) {
			ctx := context.Background()
			client := testEntClient(t)
			accountID := newDailyOSRootTestAccount(t, nil)
			accountRepo := NewAccountRepository(client, integrationDB, nil)
			_, err := accountRepo.(service.OpenAIOAuthOSProfilesEnsurer).EnsureOpenAIOAuthOSProfiles(ctx, accountID)
			require.NoError(t, err)
			account, err := accountRepo.GetByID(ctx, accountID)
			require.NoError(t, err)
			require.True(t, service.OpenAIOAuthOSProfilesComplete(account.OpenAIOAuthOSProfiles))

			// Keep feature settings local to this test's SQL transaction. The
			// persistent roots and profiles use the ordinary production clients.
			// Seed the fixture directly: the public settings mutation API owns
			// a separate validation transaction and cannot nest inside this one.
			settingClient := testEntTx(t).Client()
			require.NoError(t, setMultipleSettings(ctx, settingClient, map[string]string{
				service.SettingKeyEnableOpenAICodexFingerprintNormalization:    "true",
				service.SettingKeyEnableOpenAICodexClientIdentityNormalization: "true",
				service.SettingKeyEnableOpenAICodexInstallationIDNormalization: "true",
				service.SettingKeyEnableOpenAIUUIDv7SessionIdentity:            "true",
				service.SettingKeyEnableOpenAIOAuthDailySessionRotation:        strconv.FormatBool(dailyEnabled),
			}))
			settingRepo := NewSettingRepository(settingClient)
			settings := service.NewSettingService(settingRepo, nil)
			rdb := testRedis(t)
			dailyRepo := NewOpenAIOAuthDailySessionRepository(client)
			cfg := &config.Config{JWT: config.JWTConfig{Secret: "oauth-os-redis-" + uuid.NewString()}}
			newGateway := func() *service.OpenAIGatewayService {
				svc := service.NewOpenAIGatewayService(
					accountRepo, nil, nil, nil, nil, nil,
					NewGatewayCache(rdb), cfg,
					nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
					settings, nil,
				)
				svc.SetOAuthDailySessionRepository(dailyRepo)
				return svc
			}
			receivedAt := time.Date(2026, 9, 21, 4, 0, 0, 0, time.UTC)
			body := []byte(`{"model":"gpt-5.4","stream":true,"input":"hello","client_metadata":{"session_id":"shared-logical-session","thread_id":"shared-logical-child","parent_thread_id":"shared-logical-session"}}`)
			resolve := func(svc *service.OpenAIGatewayService, osFamily string) service.OpenAIOAuthIdentityPlan {
				t.Helper()
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
				c.Request.Header.Set("User-Agent", account.OpenAIOAuthOSProfiles.Profiles[osFamily].UserAgent)
				c.Set("api_key", &service.APIKey{ID: 882})
				// This is the stream decision that the ingress captures before
				// calling the public identity facade.
				c.Set("openai_client_requested_stream", true)
				capture := service.CaptureOpenAIOAuthIdentity(c, body, "")
				capture.ReceivedAt = receivedAt
				plan, resolveErr := svc.GetOrResolveOpenAIOAuthOutboundIdentity(ctx, c, account, capture,
					service.OpenAIOAuthIdentityPlanOptions{TurnIdentityEnabled: true, InstallationPolicy: service.OpenAIOAuthInstallationAccountPin}, nil)
				require.NoError(t, resolveErr)
				require.True(t, plan.TurnIdentityEnabled)
				require.Equal(t, service.OpenAIOAuthIdentityResolvePrimary, plan.ResolveOutcome, "Redis must resolve the identity; local fallback is not sufficient")
				require.NoError(t, service.ValidateOpenAICodexTurnIdentity(plan.TurnIdentity))
				require.Equal(t, osFamily, plan.OSFamily)
				require.Equal(t, dailyEnabled, plan.DailyRootsEnabled)
				require.Equal(t, account.OpenAIOAuthOSProfiles.Profiles[osFamily].InstallationID, plan.InstallationID)
				return plan
			}

			gateway := newGateway()
			first := make(map[string]service.OpenAIOAuthIdentityPlan, 3)
			sessions, threads, parents := make(map[string]bool, 3), make(map[string]bool, 3), make(map[string]bool, 3)
			for _, osFamily := range service.OpenAIOAuthOSFamilies() {
				plan := resolve(gateway, osFamily)
				identity := plan.TurnIdentity
				require.False(t, sessions[identity.SessionID], "different OS profiles must not share a session")
				require.False(t, threads[identity.ThreadID], "the same logical child must be isolated across OS profiles")
				require.False(t, parents[identity.ParentThreadID], "the mapped logical parent must also be isolated across OS profiles")
				sessions[identity.SessionID], threads[identity.ThreadID], parents[identity.ParentThreadID] = true, true, true
				first[osFamily] = plan
			}

			// A new facade instance and fresh ingress contexts still consult the
			// same Redis namespace and preserve each OS's session, child, and parent.
			secondGateway := newGateway()
			for _, osFamily := range service.OpenAIOAuthOSFamilies() {
				for range 2 {
					repeated := resolve(secondGateway, osFamily)
					require.Equal(t, first[osFamily].TurnIdentity, repeated.TurnIdentity)
				}
			}

			var identityKeys []string
			var cursor uint64
			for {
				keys, next, scanErr := rdb.Scan(ctx, cursor, OpenAICodexTurnIdentityKeyPrefix+"*", 100).Result()
				require.NoError(t, scanErr)
				identityKeys = append(identityKeys, keys...)
				cursor = next
				if cursor == 0 {
					break
				}
			}
			require.Len(t, identityKeys, 6, "Redis must contain one session and one descendant mapping for each OS")
			poolCount, rootCount := dailyOSRootTestCounts(t, accountID)
			if dailyEnabled {
				require.Equal(t, 1, poolCount)
				require.Equal(t, 3, rootCount)
				pools, listErr := dailyRepo.(service.OAuthDailySessionPoolReader).ListOAuthDailySessionPools(ctx, []int64{accountID}, receivedAt)
				require.NoError(t, listErr)
				for _, osFamily := range service.OpenAIOAuthOSFamilies() {
					require.Equal(t, pools[accountID].OSRoots[osFamily].StreamSessionID, first[osFamily].TurnIdentity.SessionID)
				}
			} else {
				require.Zero(t, poolCount, "disabling daily rotation must not create daily pools")
				require.Zero(t, rootCount)
			}
		})
	}
}
