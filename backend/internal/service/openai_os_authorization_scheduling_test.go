package service

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type schedulerOSAuthorizationTestRepo struct {
	schedulerTestOpenAIAccountRepo
	slots           map[string]*OpenAIOAuthOSCredential
	credentialReads atomic.Int64
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
	r.credentialReads.Add(1)
	grant := r.slots[fmt.Sprintf("%d/%s", id, os)]
	if grant == nil {
		return nil, nil
	}
	projection := *grant
	projection.OSFamily = os
	projection.StateGeneration = fmt.Sprintf("state-%d", id)
	for _, account := range r.accounts {
		if account.ID == id {
			projection.Credentials = OpenAIOAuthProviderCredentials(account.Credentials)
			break
		}
	}
	return &projection, nil
}

func (r *schedulerOSAuthorizationTestRepo) ListOpenAIOAuthOSCredentials(_ context.Context, id int64) ([]*OpenAIOAuthOSCredential, error) {
	r.credentialReads.Add(1)
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
	credentials := make(map[string]any)
	if len(authorized) > 0 {
		credentials["access_token"] = fmt.Sprintf("access-%d", id)
		credentials["refresh_token"] = fmt.Sprintf("refresh-%d", id)
	}
	return Account{ID: id, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1,
		Credentials: credentials, OpenAIOAuthOSProfiles: profiles, Extra: map[string]any{"openai_oauth_responses_websockets_v2_enabled": true}}
}

func TestOpenAISchedulingIgnoresOSAuthorizationBeforeTopKAndSticky(t *testing.T) {
	for _, advanced := range []string{"false", "true"} {
		for _, sticky := range []bool{false, true} {
			for _, family := range []string{"", OpenAIOSWindows, OpenAIOSMacOS, OpenAIOSLinux} {
				for _, profiles := range []bool{false, true} {
					t.Run(fmt.Sprintf("advanced=%s/sticky=%t/os=%s/profiles=%t", advanced, sticky, family, profiles), func(t *testing.T) {
						resetOpenAIAdvancedSchedulerSettingCacheForTest()
						t.Cleanup(resetOpenAIAdvancedSchedulerSettingCacheForTest)
						preferred := schedulerOSAuthorizedAccount(901, OpenAIOSWindows)
						if !profiles {
							preferred.OpenAIOAuthOSProfiles = nil
						}
						backup := schedulerOSAuthorizedAccount(902, OpenAIOSLinux, OpenAIOSLinux)
						backup.Priority = 10
						cache := &schedulerTestGatewayCache{}
						session := ""
						if sticky {
							session = "os-session"
							cache.sessionBindings = map[string]int64{"openai:" + session: preferred.ID}
						}
						cfg := newSchedulerTestOpenAIWSV2Config()
						cfg.Gateway.OpenAIWS.LBTopK = 1
						repo := newSchedulerOSAuthorizationTestRepo(preferred, backup)
						svc := &OpenAIGatewayService{accountRepo: repo, cfg: cfg, cache: cache,
							rateLimitService: newOpenAIAdvancedSchedulerRateLimitService(advanced), concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{})}
						ctx := ContextWithOpenAIRequestOS(context.Background(), OpenAIRequestOS{Family: family})
						selection, decision, err := svc.SelectAccountWithScheduler(ctx, nil, "", session, "gpt-5.1", nil, OpenAIUpstreamTransportAny, false)
						require.NoError(t, err)
						require.NotNil(t, selection)
						require.Equal(t, preferred.ID, selection.Account.ID, "OS authorization metadata cannot remove the highest priority account")
						require.Empty(t, selection.Account.OpenAIOAuthCredentialOS, "identity selection runs only after account selection")
						require.Empty(t, selection.Account.Credentials)
						require.Zero(t, repo.credentialReads.Load())
						if advanced == "true" && !sticky {
							require.Equal(t, 2, decision.CandidateCount)
						}
						if selection.ReleaseFunc != nil {
							selection.ReleaseFunc()
						}
					})
				}
			}
		}
	}
}

func TestOpenAISchedulingUnknownOSDoesNotChooseIdentity(t *testing.T) {
	windows := schedulerOSAuthorizedAccount(911, OpenAIOSWindows, OpenAIOSWindows)
	mac := schedulerOSAuthorizedAccount(912, OpenAIOSMacOS, OpenAIOSMacOS)
	repo := newSchedulerOSAuthorizationTestRepo(windows, mac)
	svc := &OpenAIGatewayService{accountRepo: repo}
	ctx := ContextWithOpenAIRequestOS(context.Background(), OpenAIRequestOS{})
	first, err := svc.SelectAccountForModelWithExclusions(ctx, nil, "", "gpt-5.1", nil)
	require.NoError(t, err)
	require.Equal(t, windows.ID, first.ID)
	require.Empty(t, first.OpenAIOAuthCredentialOS)
	second, err := svc.SelectAccountForModelWithExclusions(ctx, nil, "", "gpt-5.1", map[int64]struct{}{windows.ID: {}})
	require.NoError(t, err)
	require.Equal(t, mac.ID, second.ID)
	require.Empty(t, second.OpenAIOAuthCredentialOS)
	require.Equal(t, "access-912", second.GetCredential("access_token"))
	require.Empty(t, OpenAIRequestOSFromContext(ctx).Family)
	require.Zero(t, repo.credentialReads.Load())
}

func TestOpenAISchedulingPreviousResponseIgnoresPrivateCredentialState(t *testing.T) {
	account := schedulerOSAuthorizedAccount(921, OpenAIOSWindows, OpenAIOSWindows)
	repo := newSchedulerOSAuthorizationTestRepo(account)
	svc := &OpenAIGatewayService{accountRepo: repo, cfg: newSchedulerTestOpenAIWSV2Config(), cache: &schedulerTestGatewayCache{}}
	ctx := ContextWithOpenAIRequestOS(context.Background(), OpenAIRequestOS{Family: OpenAIOSWindows})
	store := svc.getOpenAIWSStateStore()
	require.NoError(t, store.BindResponseAccount(ctx, 0, "resp_os", account.ID, time.Hour))
	repo.slots["921/windows"].Status = OpenAIOAuthAuthorizationReauthRequired
	for _, family := range []string{"", OpenAIOSWindows, OpenAIOSMacOS, OpenAIOSLinux} {
		ctx = ContextWithOpenAIRequestOS(context.Background(), OpenAIRequestOS{Family: family})
		id, selected, _, _ := svc.resolveAccountByPreviousResponseIDForCapability(ctx, nil, "resp_os", "gpt-5.1", nil, "", false)
		require.Equal(t, account.ID, id)
		require.NotNil(t, selected)
		require.Empty(t, selected.OpenAIOAuthCredentialOS)
	}
	require.Zero(t, repo.credentialReads.Load())
}

func TestOpenAISchedulingPreviousResponseRechecksNormalDatabaseEligibility(t *testing.T) {
	stale := schedulerOSAuthorizedAccount(923, OpenAIOSWindows, OpenAIOSWindows)
	fresh := schedulerOSAuthorizedAccount(923, OpenAIOSWindows)
	repo := newSchedulerOSAuthorizationTestRepo(fresh)
	svc := &OpenAIGatewayService{accountRepo: repo, cfg: newSchedulerTestOpenAIWSV2Config(), cache: &schedulerTestGatewayCache{},
		schedulerSnapshot: &SchedulerSnapshotService{cache: &openAISnapshotCacheStub{accountsByID: map[int64]*Account{stale.ID: &stale}}}}
	ctx := ContextWithOpenAIRequestOS(context.Background(), OpenAIRequestOS{Family: OpenAIOSWindows})
	store := svc.getOpenAIWSStateStore()
	require.NoError(t, store.BindResponseAccount(ctx, 0, "resp_stale_os", stale.ID, time.Hour))
	id, selected, _, _ := svc.resolveAccountByPreviousResponseIDForCapability(ctx, nil, "resp_stale_os", "gpt-5.1", nil, "", false)
	require.Equal(t, fresh.ID, id, "missing OAuth credentials do not add a scheduler rejection")
	require.NotNil(t, selected)
	require.Empty(t, selected.Credentials)
	repo.accounts[0].Status = StatusDisabled
	id, _, _, _ = svc.resolveAccountByPreviousResponseIDForCapability(ctx, nil, "resp_stale_os", "gpt-5.1", nil, "", false)
	require.Zero(t, id, "normal account status remains authoritative")
	require.Zero(t, repo.credentialReads.Load())
}

func TestOpenAISchedulingPostWaitAdmissionIgnoresPrivateCredentialState(t *testing.T) {
	account := schedulerOSAuthorizedAccount(941, OpenAIOSWindows, OpenAIOSWindows, OpenAIOSLinux)
	repo := newSchedulerOSAuthorizationTestRepo(account)
	svc := &OpenAIGatewayService{accountRepo: repo, schedulerSnapshot: &SchedulerSnapshotService{cache: &openAISnapshotCacheStub{accountsByID: map[int64]*Account{account.ID: &account}}}}
	ctx := ContextWithOpenAIRequestOS(context.Background(), OpenAIRequestOS{Family: OpenAIOSLinux})
	repo.slots["941/linux"].AuthorizationGeneration = "replacement-generation"
	repo.slots["941/linux"].Status = OpenAIOAuthAuthorizationUnauthorized
	latest, vetoed, reason := svc.ProfitControlVetoLatest(ctx, &account)
	require.False(t, vetoed, reason)
	require.NotNil(t, latest)
	require.Equal(t, account.ID, latest.ID)
	require.Empty(t, latest.OpenAIOAuthCredentialOS)
	require.Zero(t, repo.credentialReads.Load())
}

func TestOpenAISchedulingNoAvailableErrorKeepsNormalConstraints(t *testing.T) {
	for _, advanced := range []string{"false", "true"} {
		t.Run(advanced, func(t *testing.T) {
			resetOpenAIAdvancedSchedulerSettingCacheForTest()
			t.Cleanup(resetOpenAIAdvancedSchedulerSettingCacheForTest)
			limited := schedulerOSAuthorizedAccount(952, OpenAIOSLinux)
			until := time.Now().Add(time.Hour)
			limited.RateLimitResetAt = &until
			svc := &OpenAIGatewayService{accountRepo: newSchedulerOSAuthorizationTestRepo(limited), cfg: newSchedulerTestOpenAIWSV2Config(),
				cache: &schedulerTestGatewayCache{}, rateLimitService: newOpenAIAdvancedSchedulerRateLimitService(advanced), concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{})}
			ctx := ContextWithOpenAIRequestOS(context.Background(), OpenAIRequestOS{Family: OpenAIOSLinux})
			selected, _, err := svc.SelectAccountWithScheduler(ctx, nil, "", "", "gpt-5.1", nil, OpenAIUpstreamTransportAny, false)
			require.Nil(t, selected)
			require.ErrorIs(t, err, ErrNoAvailableAccounts)
			require.NotContains(t, err.Error(), "oauth_authorization")
		})
	}
}
