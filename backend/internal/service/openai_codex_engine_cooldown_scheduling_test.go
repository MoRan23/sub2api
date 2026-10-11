package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func codexEngineSchedulingTestAccount(id, groupID int64, engine bool) Account {
	account := Account{
		ID: id, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Status: StatusActive, Schedulable: true, Concurrency: 1,
		GroupIDs: []int64{groupID}, Priority: int(id),
		Credentials: map[string]any{"model_mapping": map[string]any{"gpt-5.6-sol": "gpt-5.6-sol"}},
	}
	if engine {
		account.Extra = map[string]any{OpenAIAPIKeyModeExtraKey: "codex_engine"}
	}
	return account
}

func newCodexEngineSchedulingTestService(t *testing.T, mode string, accounts []Account) *OpenAIGatewayService {
	t.Helper()
	cfg := newSchedulerTestSubscriptionPriorityConfig()
	cfg.Gateway.Scheduling.LoadBatchEnabled = mode == "legacy load batch"
	enabled := "false"
	if mode == "advanced" {
		enabled = "true"
	}
	t.Cleanup(resetOpenAIAdvancedSchedulerSettingCacheForTest)
	return &OpenAIGatewayService{
		cfg: cfg,
		accountRepo: schedulerGroupAwareOpenAIAccountRepo{
			schedulerTestOpenAIAccountRepo{accounts: accounts},
		},
		cache:              &schedulerTestGatewayCache{},
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
		rateLimitService:   newOpenAIAdvancedSchedulerRateLimitService(enabled, "false", "false"),
	}
}

func installCodexEngineSchedulingTestCooldown(svc *OpenAIGatewayService, account *Account) {
	resp := &http.Response{StatusCode: http.StatusServiceUnavailable, Header: make(http.Header)}
	resp.Header.Set(codexEngineUnavailableScopeHeader, "deployment")
	resp.Header.Set("Retry-After", "30")
	svc.observeCodexEngineDeploymentUnavailable(account, resp, &codexEngineResponseError{
		status: http.StatusServiceUnavailable, code: "capacity_unavailable",
	}, time.Now())
}

func TestCodexEngineDeploymentCooldownSchedulingPreservesEligibility(t *testing.T) {
	for _, mode := range []string{"legacy", "legacy load batch", "advanced"} {
		for _, candidate := range []string{"allowed", "none", "other group", "unsupported model", "disabled", "scheduling disabled"} {
			t.Run(mode+"/"+candidate, func(t *testing.T) {
				ctx := context.Background()
				groupID := int64(601)
				engine := codexEngineSchedulingTestAccount(1, groupID, true)
				accounts := []Account{engine}
				if candidate != "none" {
					alternative := codexEngineSchedulingTestAccount(2, groupID, false)
					switch candidate {
					case "other group":
						alternative.GroupIDs = []int64{groupID + 1}
					case "unsupported model":
						alternative.Credentials = map[string]any{"model_mapping": map[string]any{"other-model": "other-model"}}
					case "disabled":
						alternative.Status = StatusDisabled
					case "scheduling disabled":
						alternative.Schedulable = false
					}
					accounts = append(accounts, alternative)
				}
				svc := newCodexEngineSchedulingTestService(t, mode, accounts)
				installCodexEngineSchedulingTestCooldown(svc, &engine)

				selection, _, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", "", "gpt-5.6-sol", nil, OpenAIUpstreamTransportAny, false)
				if candidate == "allowed" {
					require.NoError(t, err)
					require.NotNil(t, selection)
					require.Equal(t, int64(2), selection.Account.ID)
					if selection.ReleaseFunc != nil {
						selection.ReleaseFunc()
					}
				} else {
					require.ErrorIs(t, err, ErrNoAvailableAccounts)
					require.Nil(t, selection, "deployment cooldown must not relax group, model or administrator restrictions")
				}
				require.True(t, engine.Schedulable)
				require.Equal(t, StatusActive, engine.Status)
				require.Nil(t, engine.TempUnschedulableUntil)
			})
		}
	}
}

func TestCodexEngineDeploymentCooldownSchedulingBypassesAffinity(t *testing.T) {
	for _, mode := range []string{"legacy", "legacy load batch", "advanced"} {
		for _, affinity := range []string{"session", "previous response"} {
			t.Run(mode+"/"+affinity, func(t *testing.T) {
				ctx := context.Background()
				groupID := int64(602)
				engine := codexEngineSchedulingTestAccount(1, groupID, true)
				alternative := codexEngineSchedulingTestAccount(2, groupID, false)
				svc := newCodexEngineSchedulingTestService(t, mode, []Account{engine, alternative})
				sessionHash, previousResponseID := "", ""
				if affinity == "session" {
					sessionHash = "engine-maintenance-session"
					require.NoError(t, svc.setStickySessionAccountID(ctx, &groupID, sessionHash, engine.ID, time.Hour))
				} else {
					previousResponseID = "resp_engine_maintenance"
					require.NoError(t, svc.getOpenAIWSStateStore().BindResponseAccount(ctx, groupID, previousResponseID, engine.ID, time.Hour))
				}
				installCodexEngineSchedulingTestCooldown(svc, &engine)

				selection, decision, err := svc.SelectAccountWithScheduler(ctx, &groupID, previousResponseID, sessionHash, "gpt-5.6-sol", nil, OpenAIUpstreamTransportAny, false)
				require.NoError(t, err)
				require.NotNil(t, selection)
				require.Equal(t, alternative.ID, selection.Account.ID)
				require.False(t, decision.StickyPreviousHit)
				if selection.ReleaseFunc != nil {
					selection.ReleaseFunc()
				}
			})
		}
	}
}

func TestCodexEngineDeploymentCooldownNoSnapshotAdmission(t *testing.T) {
	ctx := context.Background()
	groupID := int64(603)
	engine := codexEngineSchedulingTestAccount(1, groupID, true)
	svc := newCodexEngineSchedulingTestService(t, "legacy", []Account{engine})
	require.Nil(t, svc.schedulerSnapshot)
	store := svc.getOpenAIWSStateStore()
	require.NoError(t, store.BindResponseAccount(ctx, groupID, "resp_direct_engine", engine.ID, time.Hour))
	installCodexEngineSchedulingTestCooldown(svc, &engine)

	require.Nil(t, svc.recheckSelectedOpenAIAccountFromDBBeforeProfit(ctx, &engine, &groupID, PlatformOpenAI, "gpt-5.6-sol", false, OpenAIEndpointCapabilityResponses))
	selection, err := svc.SelectAccountByPreviousResponseID(ctx, &groupID, "resp_direct_engine", "gpt-5.6-sol", nil, false)
	require.NoError(t, err)
	require.Nil(t, selection, "direct previous-response admission must respect deployment cooldown without a snapshot cache")
	boundID, err := store.GetResponseAccount(ctx, groupID, "resp_direct_engine")
	require.NoError(t, err)
	require.Equal(t, engine.ID, boundID, "temporary deployment capacity must not destroy the response owner binding")
}

type codexEngineCooldownDuringDBReadRepo struct {
	schedulerGroupAwareOpenAIAccountRepo
	onRead func()
}

func (r *codexEngineCooldownDuringDBReadRepo) GetByID(ctx context.Context, id int64) (*Account, error) {
	account, err := r.schedulerGroupAwareOpenAIAccountRepo.GetByID(ctx, id)
	if r.onRead != nil {
		r.onRead()
	}
	return account, err
}

func TestCodexEngineDeploymentCooldownDuringPreviousResponseRecheckKeepsBinding(t *testing.T) {
	ctx := context.Background()
	groupID := int64(604)
	engine := codexEngineSchedulingTestAccount(1, groupID, true)
	svc := newCodexEngineSchedulingTestService(t, "legacy", []Account{engine})
	svc.schedulerSnapshot = &SchedulerSnapshotService{cache: &openAISnapshotCacheStub{
		accountsByID: map[int64]*Account{engine.ID: &engine},
	}}
	reads := 0
	svc.accountRepo = &codexEngineCooldownDuringDBReadRepo{
		schedulerGroupAwareOpenAIAccountRepo: schedulerGroupAwareOpenAIAccountRepo{
			schedulerTestOpenAIAccountRepo{accounts: []Account{engine}},
		},
		onRead: func() {
			reads++
			installCodexEngineSchedulingTestCooldown(svc, &engine)
		},
	}
	store := svc.getOpenAIWSStateStore()
	require.NoError(t, store.BindResponseAccount(ctx, groupID, "resp_concurrent_engine", engine.ID, time.Hour))
	require.False(t, svc.isOpenAIAccountRequestRuntimeBlocked(&engine, "gpt-5.6-sol"))

	selection, err := svc.SelectAccountByPreviousResponseID(ctx, &groupID, "resp_concurrent_engine", "gpt-5.6-sol", nil, false)
	require.NoError(t, err)
	require.Equal(t, 1, reads, "cooldown must arrive between the snapshot check and database recheck")
	require.Nil(t, selection)
	boundID, err := store.GetResponseAccount(ctx, groupID, "resp_concurrent_engine")
	require.NoError(t, err)
	require.Equal(t, engine.ID, boundID, "maintenance discovered at final recheck must preserve response ownership")
}
