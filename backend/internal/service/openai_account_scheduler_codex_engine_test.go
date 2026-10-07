package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPartitionOpenAIChatGPTSubscriptionAccountsIncludesCodexEngine(t *testing.T) {
	subscription := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"plan_type": "plus"}}
	engine := &Account{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Extra: map[string]any{OpenAIAPIKeyModeExtraKey: "codex_engine"}}
	generic := &Account{ID: 3, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	freeOAuth := &Account{ID: 4, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"plan_type": "free"}}
	wrongPlatform := &Account{ID: 5, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Extra: map[string]any{OpenAIAPIKeyModeExtraKey: "codex_engine"}}

	preferred, regular := partitionOpenAIChatGPTSubscriptionAccounts([]*Account{generic, engine, freeOAuth, subscription, wrongPlatform, nil})

	require.Equal(t, []*Account{engine, subscription}, preferred)
	require.Equal(t, []*Account{generic, freeOAuth, wrongPlatform, nil}, regular)
	require.False(t, engine.IsOAuth(), "the scheduling classification must not change authentication")
	require.False(t, engine.IsOpenAIChatGPTSubscription(), "Engine is not a ChatGPT subscription")
	require.True(t, engine.IsOpenAIApiKey())
}

func TestOpenAIGatewayService_SubscriptionPriorityTreatsCodexEngineAsPreferred(t *testing.T) {
	for _, source := range []string{"repository", "snapshot", "snapshot_db_fallback"} {
		for _, scenario := range []struct {
			name              string
			subscriptionFirst bool
			priorityEnabled   bool
			engineAvailable   bool
			want              int64
		}{
			{name: "engine competes with OAuth subscription", priorityEnabled: true, engineAvailable: true, want: 21702},
			{name: "OAuth subscription wins within preferred pool", subscriptionFirst: true, priorityEnabled: true, engineAvailable: true, want: 21701},
			{name: "ordinary API key wins when preference is disabled", engineAvailable: true, want: 21703},
			{name: "ordinary API key remains capacity fallback", priorityEnabled: true, want: 21703},
		} {
			t.Run(source+"/"+scenario.name, func(t *testing.T) {
				resetOpenAIAdvancedSchedulerSettingCacheForTest()
				defer resetOpenAIAdvancedSchedulerSettingCacheForTest()
				groupID := int64(10124)
				accounts := []Account{
					{ID: 21701, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 10, GroupIDs: []int64{groupID}, Credentials: map[string]any{"plan_type": "pro"}},
					{ID: 21702, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 5, GroupIDs: []int64{groupID}, Extra: map[string]any{OpenAIAPIKeyModeExtraKey: "codex_engine"}},
					{ID: 21703, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 0, GroupIDs: []int64{groupID}},
				}
				if scenario.subscriptionFirst {
					accounts[0].Priority = 1
				}
				prioritySetting := "false"
				if scenario.priorityEnabled {
					prioritySetting = "true"
				}
				repo := schedulerTestOpenAIAccountRepo{accounts: accounts}
				svc := &OpenAIGatewayService{
					accountRepo:      repo,
					cache:            &schedulerTestGatewayCache{},
					cfg:              newSchedulerTestSubscriptionPriorityConfig(),
					rateLimitService: newOpenAIAdvancedSchedulerRateLimitService("true", "", prioritySetting),
					concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{
						acquireResults: map[int64]bool{21701: scenario.subscriptionFirst, 21702: scenario.engineAvailable, 21703: true},
					}),
				}
				svc.cfg.Gateway.OpenAIWS.SchedulerScoreWeights.Load = 0
				svc.cfg.Gateway.OpenAIWS.SchedulerScoreWeights.Queue = 0
				if source == "snapshot" {
					svc.schedulerSnapshot = &SchedulerSnapshotService{cache: &openAISnapshotCacheStub{
						snapshotAccounts: []*Account{&accounts[0], &accounts[1], &accounts[2]},
						accountsByID:     map[int64]*Account{21701: &accounts[0], 21702: &accounts[1], 21703: &accounts[2]},
					}}
				} else if source == "snapshot_db_fallback" {
					svc.schedulerSnapshot = &SchedulerSnapshotService{accountRepo: repo}
				}

				selection, decision, err := svc.SelectAccountWithScheduler(context.Background(), &groupID, "", "", "gpt-6.1-sol", nil, OpenAIUpstreamTransportAny, false)
				require.NoError(t, err)
				require.NotNil(t, selection)
				require.NotNil(t, selection.Account)
				require.Equal(t, scenario.want, selection.Account.ID)
				require.Equal(t, openAIAccountScheduleLayerLoadBalance, decision.Layer)
				if scenario.want == 21702 {
					require.Equal(t, AccountTypeAPIKey, decision.SelectedAccountType)
					require.True(t, selection.Account.IsCodexEngine())
					require.False(t, selection.Account.IsOAuth())
				}
				if selection.ReleaseFunc != nil {
					selection.ReleaseFunc()
				}
			})
		}
	}
}
