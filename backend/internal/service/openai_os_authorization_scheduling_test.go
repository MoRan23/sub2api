package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type schedulerOSAuthorizationTestRepo struct {
	schedulerTestOpenAIAccountRepo
	slots map[string]*OpenAIOAuthOSCredential
}

func newSchedulerOSAuthorizationTestRepo(accounts ...Account) *schedulerOSAuthorizationTestRepo {
	r := &schedulerOSAuthorizationTestRepo{schedulerTestOpenAIAccountRepo: schedulerTestOpenAIAccountRepo{accounts: accounts}, slots: make(map[string]*OpenAIOAuthOSCredential)}
	for _, account := range accounts {
		if account.OpenAIOAuthOSProfiles == nil || account.IsShadow() {
			continue
		}
		if OpenAIOAuthOSAuthorizationAvailable(&account, "") {
			key := fmt.Sprintf("%d", account.ID)
			grant := &OpenAIOAuthOSCredential{OwnerAccountID: account.ID, OSFamily: account.OpenAIOAuthOSProfiles.DefaultOS, Status: OpenAIOAuthAuthorizationAuthorized,
				Credentials: map[string]any{"access_token": "access-" + key, "refresh_token": "refresh-" + key}, AuthorizationGeneration: "generation-" + key, Revision: 1}
			for _, os := range OpenAIOAuthOSFamilies() {
				r.slots[fmt.Sprintf("%d/%s", account.ID, os)] = grant
			}
		}
	}
	return r
}

func (r *schedulerOSAuthorizationTestRepo) GetOpenAIOAuthOSCredential(_ context.Context, id int64, os string) (*OpenAIOAuthOSCredential, error) {
	grant := r.slots[fmt.Sprintf("%d/%s", id, os)]
	if grant == nil {
		return nil, nil
	}
	projection := *grant
	projection.OSFamily = os
	projection.StateGeneration = fmt.Sprintf("state-%d/%s", id, os)
	return &projection, nil
}

func (r *schedulerOSAuthorizationTestRepo) ListOpenAIOAuthOSCredentials(_ context.Context, id int64) ([]*OpenAIOAuthOSCredential, error) {
	var slots []*OpenAIOAuthOSCredential
	for _, slot := range r.slots {
		if slot.OwnerAccountID == id {
			slots = append(slots, slot)
			break
		}
	}
	return slots, nil
}

func schedulerOSAuthorizedAccount(id int64, defaultOS string, authorized ...string) Account {
	profiles := &OpenAIOAuthOSProfiles{DefaultOS: defaultOS, Profiles: make(map[string]OpenAIOAuthOSProfile)}
	for _, os := range OpenAIOAuthOSFamilies() {
		profiles.Profiles[os] = OpenAIOAuthOSProfile{OSFamily: os, Authorization: OpenAIOAuthOSAuthorizationSummary{Status: OpenAIOAuthAuthorizationUnauthorized}}
	}
	for _, os := range authorized {
		profile := profiles.Profiles[os]
		profile.Authorization.Status = OpenAIOAuthAuthorizationAuthorized
		profiles.Profiles[os] = profile
	}
	summary := profiles.Profiles[defaultOS].Authorization
	profiles.Authorization = &summary
	return Account{ID: id, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1,
		Credentials: map[string]any{}, OpenAIOAuthOSProfiles: profiles, Extra: map[string]any{"openai_oauth_responses_websockets_v2_enabled": true}}
}

func TestOpenAISharedAuthorizationKnownOSDoesNotFilterBeforeTopKOrSticky(t *testing.T) {
	for _, advanced := range []string{"false", "true"} {
		for _, sticky := range []bool{false, true} {
			t.Run(fmt.Sprintf("advanced=%s/sticky=%t", advanced, sticky), func(t *testing.T) {
				resetOpenAIAdvancedSchedulerSettingCacheForTest()
				windows := schedulerOSAuthorizedAccount(901, OpenAIOSWindows, OpenAIOSWindows)
				linux := schedulerOSAuthorizedAccount(902, OpenAIOSLinux, OpenAIOSLinux)
				linux.Priority = 10
				cache := &schedulerTestGatewayCache{}
				session := ""
				if sticky {
					session = "os-session"
					cache.sessionBindings = map[string]int64{"openai:" + session: windows.ID}
				}
				cfg := newSchedulerTestOpenAIWSV2Config()
				cfg.Gateway.OpenAIWS.LBTopK = 1
				svc := &OpenAIGatewayService{accountRepo: newSchedulerOSAuthorizationTestRepo(windows, linux), cfg: cfg, cache: cache,
					rateLimitService: newOpenAIAdvancedSchedulerRateLimitService(advanced), concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{})}
				ctx := ContextWithOpenAIRequestOS(context.Background(), OpenAIRequestOS{Family: OpenAIOSLinux})
				selection, decision, err := svc.SelectAccountWithScheduler(ctx, nil, "", session, "gpt-5.1", nil, OpenAIUpstreamTransportAny, false)
				require.NoError(t, err)
				require.Equal(t, windows.ID, selection.Account.ID, "the preferred account's legacy Linux slot need not be authorized")
				require.Equal(t, OpenAIOSLinux, selection.Account.OpenAIOAuthCredentialOS)
				require.Equal(t, "access-901", selection.Account.GetCredential("access_token"))
				if advanced == "true" {
					if !sticky {
						require.Equal(t, 2, decision.CandidateCount)
					}
				}
				if selection.ReleaseFunc != nil {
					selection.ReleaseFunc()
				}
			})
		}
	}
}

func TestOpenAIOSAuthorizationUnknownUsesEachCandidateDefault(t *testing.T) {
	windows := schedulerOSAuthorizedAccount(911, OpenAIOSWindows, OpenAIOSWindows)
	mac := schedulerOSAuthorizedAccount(912, OpenAIOSMacOS, OpenAIOSMacOS)
	svc := &OpenAIGatewayService{accountRepo: newSchedulerOSAuthorizationTestRepo(windows, mac)}
	ctx := ContextWithOpenAIRequestOS(context.Background(), OpenAIRequestOS{})
	first, err := svc.SelectAccountForModelWithExclusions(ctx, nil, "", "gpt-5.1", nil)
	require.NoError(t, err)
	require.Equal(t, OpenAIOSWindows, first.OpenAIOAuthCredentialOS)
	second, err := svc.SelectAccountForModelWithExclusions(ctx, nil, "", "gpt-5.1", map[int64]struct{}{windows.ID: {}})
	require.NoError(t, err)
	require.Equal(t, OpenAIOSMacOS, second.OpenAIOAuthCredentialOS)
	require.Equal(t, "access-912", second.GetCredential("access_token"))
	require.Empty(t, OpenAIRequestOSFromContext(ctx).Family)
}

func TestOpenAIOSAuthorizationPreviousResponseRechecksLatestSlot(t *testing.T) {
	account := schedulerOSAuthorizedAccount(921, OpenAIOSWindows, OpenAIOSWindows)
	repo := newSchedulerOSAuthorizationTestRepo(account)
	svc := &OpenAIGatewayService{accountRepo: repo, cfg: newSchedulerTestOpenAIWSV2Config(), cache: &schedulerTestGatewayCache{}}
	ctx := ContextWithOpenAIRequestOS(context.Background(), OpenAIRequestOS{Family: OpenAIOSWindows})
	store := svc.getOpenAIWSStateStore()
	require.NoError(t, store.BindResponseAccount(ctx, 0, "resp_os", account.ID, time.Hour))
	id, selected, _, _ := svc.resolveAccountByPreviousResponseIDForCapability(ctx, nil, "resp_os", "gpt-5.1", nil, "", false)
	require.Equal(t, account.ID, id)
	require.Equal(t, OpenAIOSWindows, selected.OpenAIOAuthCredentialOS)
	repo.slots["921/windows"].Status = OpenAIOAuthAuthorizationReauthRequired
	id, _, _, _ = svc.resolveAccountByPreviousResponseIDForCapability(ctx, nil, "resp_os", "gpt-5.1", nil, "", false)
	require.Zero(t, id, "an authorized summary cannot override a revoked private slot")
	linux := ContextWithOpenAIRequestOS(context.Background(), OpenAIRequestOS{Family: OpenAIOSLinux})
	id, _, _, _ = svc.resolveAccountByPreviousResponseIDForCapability(linux, nil, "resp_os", "gpt-5.1", nil, "", false)
	require.Zero(t, id)
}

func TestOpenAIOSAuthorizationPreviousResponseRechecksDatabaseSummary(t *testing.T) {
	stale := schedulerOSAuthorizedAccount(923, OpenAIOSWindows, OpenAIOSWindows)
	fresh := schedulerOSAuthorizedAccount(923, OpenAIOSWindows)
	repo := newSchedulerOSAuthorizationTestRepo(fresh)
	svc := &OpenAIGatewayService{accountRepo: repo, cfg: newSchedulerTestOpenAIWSV2Config(), cache: &schedulerTestGatewayCache{},
		schedulerSnapshot: &SchedulerSnapshotService{cache: &openAISnapshotCacheStub{accountsByID: map[int64]*Account{stale.ID: &stale}}}}
	ctx := ContextWithOpenAIRequestOS(context.Background(), OpenAIRequestOS{Family: OpenAIOSWindows})
	store := svc.getOpenAIWSStateStore()
	require.NoError(t, store.BindResponseAccount(ctx, 0, "resp_stale_os", stale.ID, time.Hour))
	id, _, _, _ := svc.resolveAccountByPreviousResponseIDForCapability(ctx, nil, "resp_stale_os", "gpt-5.1", nil, "", false)
	require.Zero(t, id)
}

func TestOpenAIOSAuthorizationSparkUsesParentSlotAndKeepsBusinessID(t *testing.T) {
	parent := schedulerOSAuthorizedAccount(931, OpenAIOSMacOS, OpenAIOSMacOS)
	shadow := schedulerOSAuthorizedAccount(932, OpenAIOSWindows)
	shadow.ParentAccountID, shadow.OpenAIOAuthOSProfiles = &parent.ID, nil
	repo := newSchedulerOSAuthorizationTestRepo(parent, shadow)
	svc := &OpenAIGatewayService{accountRepo: repo}
	ctx := ContextWithOpenAIRequestOS(context.Background(), OpenAIRequestOS{})
	require.True(t, openAIParentHealthyForShadow(ctx, &shadow, svc.parentAccountLookup(ctx)))
	scoped := svc.resolveSelectedOpenAIOAuthCredentials(ctx, &shadow)
	require.NotNil(t, scoped)
	require.Equal(t, shadow.ID, scoped.ID)
	require.Equal(t, parent.ID, scoped.OpenAIOAuthCredentialOwnerID)
	require.Equal(t, OpenAIOSMacOS, scoped.OpenAIOAuthCredentialOS)
	linuxCtx := ContextWithOpenAIRequestOS(context.Background(), OpenAIRequestOS{Family: OpenAIOSLinux})
	require.True(t, openAIParentHealthyForShadow(linuxCtx, &shadow, svc.parentAccountLookup(linuxCtx)))
	linux := svc.resolveSelectedOpenAIOAuthCredentials(linuxCtx, &shadow)
	require.Equal(t, OpenAIOSLinux, linux.OpenAIOAuthCredentialOS)
	require.Equal(t, scoped.GetCredential("access_token"), linux.GetCredential("access_token"))
}

func TestOpenAIOSAuthorizationExemptsNonOAuthSchemes(t *testing.T) {
	ctx := ContextWithOpenAIRequestOS(context.Background(), OpenAIRequestOS{Family: OpenAIOSLinux})
	for _, account := range []Account{
		{Platform: PlatformOpenAI, Type: AccountTypeAPIKey},
		{Platform: PlatformOpenAI, Type: AccountTypeSetupToken},
		{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"auth_mode": OpenAIAuthModePersonalAccessToken}},
		{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"auth_mode": OpenAIAuthModeAgentIdentity}},
		{Platform: PlatformAnthropic, Type: AccountTypeOAuth},
	} {
		require.True(t, openAIAccountOSAuthorizationEligible(ctx, &account, nil), "%+v", account)
	}
}

func TestOpenAIOSAuthorizationPostWaitAdmissionKeepsSlotAndRejectsReauthorization(t *testing.T) {
	account := schedulerOSAuthorizedAccount(941, OpenAIOSWindows, OpenAIOSWindows, OpenAIOSLinux)
	repo := newSchedulerOSAuthorizationTestRepo(account)
	svc := &OpenAIGatewayService{accountRepo: repo, schedulerSnapshot: &SchedulerSnapshotService{cache: &openAISnapshotCacheStub{accountsByID: map[int64]*Account{account.ID: &account}}}}
	ctx := ContextWithOpenAIRequestOS(context.Background(), OpenAIRequestOS{Family: OpenAIOSLinux})
	selected := svc.resolveSelectedOpenAIOAuthCredentials(ctx, &account)
	require.NotNil(t, selected)
	latest, vetoed, _ := svc.ProfitControlVetoLatest(ctx, selected)
	require.False(t, vetoed)
	require.Equal(t, OpenAIOSLinux, latest.OpenAIOAuthCredentialOS)
	require.Equal(t, "access-941", latest.GetCredential("access_token"))
	repo.slots["941/linux"].AuthorizationGeneration = "replacement-generation"
	_, vetoed, reason := svc.ProfitControlVetoLatest(ctx, selected)
	require.True(t, vetoed)
	require.Equal(t, "oauth_authorization_unavailable", reason)
}

func TestOpenAIOSAuthorizationNoAvailableErrorDoesNotOverrideOtherConstraints(t *testing.T) {
	for _, advanced := range []string{"false", "true"} {
		t.Run(advanced, func(t *testing.T) {
			resetOpenAIAdvancedSchedulerSettingCacheForTest()
			unauthorized := schedulerOSAuthorizedAccount(951, OpenAIOSWindows)
			svc := &OpenAIGatewayService{accountRepo: newSchedulerOSAuthorizationTestRepo(unauthorized), cfg: newSchedulerTestOpenAIWSV2Config(),
				cache: &schedulerTestGatewayCache{}, rateLimitService: newOpenAIAdvancedSchedulerRateLimitService(advanced), concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{})}
			ctx := ContextWithOpenAIRequestOS(context.Background(), OpenAIRequestOS{Family: OpenAIOSLinux})
			_, _, err := svc.SelectAccountWithScheduler(ctx, nil, "", "", "gpt-5.1", nil, OpenAIUpstreamTransportAny, false)
			require.ErrorIs(t, err, ErrNoAvailableOpenAIOAuthOSAccounts)
			require.ErrorIs(t, err, ErrNoAvailableAccounts)
			limited := schedulerOSAuthorizedAccount(952, OpenAIOSLinux, OpenAIOSLinux)
			until := time.Now().Add(time.Hour)
			limited.RateLimitResetAt = &until
			svc.accountRepo = newSchedulerOSAuthorizationTestRepo(unauthorized, limited)
			_, _, err = svc.SelectAccountWithScheduler(ctx, nil, "", "", "gpt-5.1", nil, OpenAIUpstreamTransportAny, false)
			require.ErrorIs(t, err, ErrNoAvailableAccounts)
			require.NotErrorIs(t, err, ErrNoAvailableOpenAIOAuthOSAccounts)
		})
	}
}

func TestOpenAIOSAuthorizationSocketDigestSeparatesAuthorizationGeneration(t *testing.T) {
	plan := OpenAIOAuthIdentityPlan{CredentialOS: OpenAIOSLinux, AuthorizationGeneration: "first"}
	first := openAIWSOutboundIdentityPlanDigest(nil, plan)
	plan.AuthorizationGeneration = "second"
	require.NotEqual(t, first, openAIWSOutboundIdentityPlanDigest(nil, plan))
	plan.AuthorizationGeneration, plan.CredentialOS = "first", OpenAIOSWindows
	require.NotEqual(t, first, openAIWSOutboundIdentityPlanDigest(nil, plan))
}

func TestOpenAIOSAuthorizationPhysicalWSNeverChangesSlotAfterDefaultOrGenerationChange(t *testing.T) {
	account := schedulerOSAuthorizedAccount(961, OpenAIOSWindows, OpenAIOSWindows, OpenAIOSLinux)
	repo := newSchedulerOSAuthorizationTestRepo(account)
	svc := &OpenAIGatewayService{accountRepo: repo}
	ctx := ContextWithOpenAIRequestOS(context.Background(), OpenAIRequestOS{})
	scoped := svc.resolveSelectedOpenAIOAuthCredentials(ctx, &account)
	require.NotNil(t, scoped)
	require.Equal(t, OpenAIOSWindows, scoped.OpenAIOAuthCredentialOS)
	repo.accounts[0].OpenAIOAuthOSProfiles.DefaultOS = OpenAIOSLinux
	require.NoError(t, svc.validateOpenAIWSAuthorization(ctx, scoped))
	require.Equal(t, "access-961", scoped.GetCredential("access_token"))
	repo.slots["961/windows"].AuthorizationGeneration = "replacement"
	require.Error(t, svc.validateOpenAIWSAuthorization(ctx, scoped), "a different identity cannot permit a shared authorization generation switch")
	repo.slots["961/windows"].Status = OpenAIOAuthAuthorizationUnauthorized
	require.Error(t, svc.validateOpenAIWSAuthorization(ctx, scoped))
}

func TestOpenAISharedAuthorizationWSFreezeRetainsTokenRevisionAndRejectsWrongToken(t *testing.T) {
	account := schedulerOSAuthorizedAccount(962, OpenAIOSWindows, OpenAIOSWindows, OpenAIOSLinux)
	repo := newSchedulerOSAuthorizationTestRepo(account)
	svc := &OpenAIGatewayService{accountRepo: repo}
	ctx := ContextWithOpenAIRequestOS(context.Background(), OpenAIRequestOS{Family: OpenAIOSLinux})
	scoped := svc.resolveSelectedOpenAIOAuthCredentials(ctx, &account)
	require.NotNil(t, scoped)
	token := scoped.GetOpenAIAccessToken()
	repo.slots["962/linux"].Credentials["access_token"] = "newer-refreshed-token"
	repo.slots["962/linux"].Revision++
	frozen, err := svc.freezeOpenAIWSAuthorization(ctx, scoped, token)
	require.NoError(t, err)
	require.Same(t, scoped, frozen)
	require.Equal(t, int64(1), frozen.OpenAIOAuthCredentialRevision)
	require.Equal(t, token, frozen.GetOpenAIAccessToken())
	_, err = svc.freezeOpenAIWSAuthorization(ctx, scoped, "unrelated-token")
	require.ErrorIs(t, err, ErrOpenAIOAuthOSAuthorizationChanged)
	_, err = svc.freezeOpenAIWSAuthorization(ctx, &account, "unrelated-token")
	require.ErrorIs(t, err, ErrOpenAIOAuthOSAuthorizationChanged)
}
