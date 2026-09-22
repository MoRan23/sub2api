package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func osIdentityTestAccount(t *testing.T, id int64) *Account {
	t.Helper()
	account := installationTestOAuthAccount(nil)
	account.ID = id
	profiles, err := BuildOpenAIOAuthOSProfiles(account, nil)
	require.NoError(t, err)
	account.OpenAIOAuthOSProfiles = profiles
	authorizeOpenAIOAuthTestAccount(account, OpenAIOAuthOSFamilies()...)
	return account
}

func osIdentityTestContext(t *testing.T, ua string) *gin.Context {
	t.Helper()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request.Header.Set("User-Agent", ua)
	return c
}

func TestOAuthOSIdentityFinalWireUsesMatchingUAAndInstallation(t *testing.T) {
	for _, osFamily := range OpenAIOAuthOSFamilies() {
		for _, force := range []bool{false, true} {
			t.Run(osFamily+map[bool]string{true: "/force", false: "/normal"}[force], func(t *testing.T) {
				account := osIdentityTestAccount(t, 702)
				profile := account.OpenAIOAuthOSProfiles.Profiles[osFamily]
				c := osIdentityTestContext(t, profile.UserAgent)
				body := []byte(`{"model":"gpt-5.4","stream":true,"input":"hello"}`)
				svc := &OpenAIGatewayService{accountRepo: newAuthorizedOpenAIOAuthTestRepo(account), cfg: &config.Config{Gateway: config.GatewayConfig{ForceCodexCLI: force}}}
				capture := CaptureOpenAIOAuthIdentity(c, body, "logical")
				plan, err := svc.GetOrResolveOpenAIOAuthOutboundIdentity(context.Background(), c, account, capture, OpenAIOAuthIdentityPlanOptions{InstallationPolicy: OpenAIOAuthInstallationAccountPin}, nil)
				require.NoError(t, err)
				plan, err = FinalizeOpenAICodexWirePlan(plan, "turn", CodexModelCapabilities{})
				require.NoError(t, err)
				headers := http.Header{"Originator": []string{"codex_cli_rs"}, "User-Agent": []string{"other-agent"}}
				out, err := ApplyOpenAIOAuthIdentityPlan(headers, body, plan)
				require.NoError(t, err)
				require.Equal(t, osFamily, plan.OSFamily)
				require.Equal(t, osFamily, openai.DetectOSFamilyFromUserAgent(headers.Get("User-Agent")))
				require.Equal(t, profile.InstallationID, plan.InstallationID)
				require.Equal(t, profile.InstallationID, gjson.GetBytes(out, "client_metadata.x-codex-installation-id").String())
			})
		}
	}
}

type osIdentityDailyRepository struct {
	fakeOAuthDailyAffinityRepository
	calls    int
	owners   []int64
	dates    []time.Time
	defaults []string
	pool     OAuthDailySessionPool
}

func (r *osIdentityDailyRepository) GetOrCreateOAuthDailySessionPoolForOS(_ context.Context, ownerID int64, defaultOS string, now time.Time) (OAuthDailySessionPool, error) {
	r.calls++
	r.owners = append(r.owners, ownerID)
	r.dates = append(r.dates, now)
	r.defaults = append(r.defaults, defaultOS)
	return r.pool, nil
}

func osIdentityDailyService() (*OpenAIGatewayService, *osIdentityDailyRepository) {
	roots := make(map[string]OAuthDailyOSRoots)
	for _, osFamily := range OpenAIOAuthOSFamilies() {
		roots[osFamily] = OAuthDailyOSRoots{StreamSessionID: uuid.Must(uuid.NewV7()).String(), SyncSessionID: uuid.Must(uuid.NewV7()).String()}
	}
	repo := &osIdentityDailyRepository{pool: OAuthDailySessionPool{BusinessDate: "2026-09-21", OSRoots: roots}}
	settings := NewSettingService(&dailyRotationSettingRepo{values: map[string]string{SettingKeyEnableOpenAIOAuthDailySessionRotation: "true"}}, nil)
	return &OpenAIGatewayService{settingService: settings, oauthDailySessionRepo: repo}, repo
}

func TestOAuthOSIdentityDailyRootsFreezeDateAndSeparateOwners(t *testing.T) {
	svc, repo := osIdentityDailyService()
	account := osIdentityTestAccount(t, 703)
	svc.accountRepo = newAuthorizedOpenAIOAuthTestRepo(account)
	c := osIdentityTestContext(t, account.OpenAIOAuthOSProfiles.Profiles[OpenAIOSLinux].UserAgent)
	setOpenAIClientRequestedStream(c, true)
	capture := CaptureOpenAIOAuthIdentity(c, []byte(`{"stream":true,"input":"hello"}`), "shared-logical")
	capture.ReceivedAt = time.Date(2026, 9, 21, 15, 59, 59, 0, time.UTC)
	options := OpenAIOAuthIdentityPlanOptions{TurnIdentityEnabled: true, InstallationPolicy: OpenAIOAuthInstallationAccountPin}
	plan, err := svc.GetOrResolveOpenAIOAuthOutboundIdentity(context.Background(), c, account, capture, options, nil)
	require.NoError(t, err)
	require.Equal(t, repo.pool.OSRoots[OpenAIOSLinux].StreamSessionID, plan.TurnIdentity.SessionID)
	require.Equal(t, account.OpenAIOAuthOSProfiles.DefaultOS, repo.defaults[0])
	require.Equal(t, capture.ReceivedAt, repo.dates[0])
	firstRoot := plan.TurnIdentity.SessionID
	changed := repo.pool.OSRoots[OpenAIOSLinux]
	changed.StreamSessionID = uuid.Must(uuid.NewV7()).String()
	repo.pool.OSRoots[OpenAIOSLinux] = changed
	// Even a new WS turn keeps the physical connection's original date/root.
	frame := captureOpenAIWSFrameIdentity([]byte(`{"type":"response.create","input":"next"}`), &plan)
	continued, err := svc.GetOrResolveOpenAIOAuthOutboundIdentity(context.Background(), c, account, frame, options, &plan)
	require.NoError(t, err)
	require.Equal(t, firstRoot, continued.TurnIdentity.SessionID)
	require.Equal(t, 1, repo.calls)
	replacement := osIdentityTestAccount(t, 704)
	svc.accountRepo = newAuthorizedOpenAIOAuthTestRepo(account, replacement)
	other, err := svc.GetOrResolveOpenAIOAuthOutboundIdentity(context.Background(), c, replacement, capture, options, &plan)
	require.NoError(t, err)
	require.Equal(t, OpenAIOSLinux, other.OSFamily)
	require.Equal(t, 2, repo.calls)
	require.Equal(t, capture.ReceivedAt, repo.dates[1])
	require.NotEqual(t, plan.InstallationID, other.InstallationID)
	ctx := context.WithValue(context.Background(), openAIOAuthOSSelectionContextKey{}, openAIOAuthOSSelectionFromPlan(other))
	syncIdentity, enabled, err := svc.resolveOAuthSynchronousTurnIdentity(ctx, replacement, false, "")
	require.NoError(t, err)
	require.True(t, enabled)
	require.Equal(t, repo.pool.OSRoots[OpenAIOSLinux].SyncSessionID, syncIdentity.SessionID)
	newRequest := osIdentityTestContext(t, account.OpenAIOAuthOSProfiles.Profiles[OpenAIOSLinux].UserAgent)
	setOpenAIClientRequestedStream(newRequest, true)
	newCapture := CaptureOpenAIOAuthIdentity(newRequest, []byte(`{"stream":true,"input":"fresh"}`), "new-request")
	newPlan, err := svc.GetOrResolveOpenAIOAuthOutboundIdentity(context.Background(), newRequest, account, newCapture, options, nil)
	require.NoError(t, err)
	require.Equal(t, changed.StreamSessionID, newPlan.TurnIdentity.SessionID)
}

func TestOAuthOSIdentityDisabledDailyKeepsSeparateStreamMappingsAndSyncRoots(t *testing.T) {
	account := osIdentityTestAccount(t, 705)
	svc := &OpenAIGatewayService{accountRepo: newAuthorizedOpenAIOAuthTestRepo(account)}
	identities := map[string]OpenAICodexTurnIdentity{}
	for _, osFamily := range []string{OpenAIOSWindows, OpenAIOSMacOS} {
		c := osIdentityTestContext(t, account.OpenAIOAuthOSProfiles.Profiles[osFamily].UserAgent)
		capture := CaptureOpenAIOAuthIdentity(c, []byte(`{"stream":true,"input":"hello"}`), "same-logical")
		plan, err := svc.GetOrResolveOpenAIOAuthOutboundIdentity(context.Background(), c, account, capture, OpenAIOAuthIdentityPlanOptions{TurnIdentityEnabled: true}, nil)
		require.NoError(t, err)
		identities[osFamily] = plan.TurnIdentity
		ctx := context.WithValue(context.Background(), openAIOAuthOSSelectionContextKey{}, openAIOAuthOSSelectionFromPlan(plan))
		syncIdentity, enabled, err := svc.resolveOAuthSynchronousTurnIdentity(ctx, account, false, "")
		require.NoError(t, err)
		require.True(t, enabled)
		require.Equal(t, account.OpenAIOAuthOSProfiles.Profiles[osFamily].SyncSessionID, syncIdentity.SessionID)
	}
	require.NotEqual(t, identities[OpenAIOSWindows].SessionID, identities[OpenAIOSMacOS].SessionID)
}

func TestOAuthOSIdentitySafePairAndInstallationSwitches(t *testing.T) {
	account := osIdentityTestAccount(t, 706)
	ua := "codex_cli_rs/0.199.0 (Mac OS 26.6.2; arm64) iTerm.app/3.7.0"
	for _, pin := range []bool{true, false} {
		c := osIdentityTestContext(t, ua)
		c.Request.Header.Set("version", "0.199.0")
		c.Set(openAICodexFingerprintPolicyContextKey, CodexFingerprintPolicySnapshot{MasterEnabled: true, InstallationIDEnabled: pin, TurnIdentityEnabled: true, ClientIdentityEnabled: false})
		body := []byte(`{"stream":true,"input":"hello","client_metadata":{"x-codex-installation-id":"client-installation"}}`)
		capture := CaptureOpenAIOAuthIdentity(c, body, "logical")
		svc := &OpenAIGatewayService{accountRepo: newAuthorizedOpenAIOAuthTestRepo(account)}
		plan, err := svc.GetOrResolveOpenAIOAuthOutboundIdentity(context.Background(), c, account, capture, OpenAIOAuthIdentityPlanOptions{}, nil)
		require.NoError(t, err)
		plan, err = FinalizeOpenAICodexWirePlan(plan, "turn", CodexModelCapabilities{})
		require.NoError(t, err)
		headers := http.Header{"Originator": []string{"codex_cli_rs"}, "User-Agent": []string{"irrelevant-later-ua"}}
		out, err := ApplyOpenAIOAuthIdentityPlan(headers, body, plan)
		require.NoError(t, err)
		require.Equal(t, ua, headers.Get("User-Agent"))
		require.Equal(t, "0.199.0", headers.Get("version"))
		require.Equal(t, OpenAIOSMacOS, plan.OSFamily)
		if pin {
			require.Equal(t, account.OpenAIOAuthOSProfiles.Profiles[OpenAIOSMacOS].InstallationID, plan.InstallationID)
		} else {
			require.False(t, plan.InstallationEnabled)
			require.Equal(t, "client-installation", gjson.GetBytes(out, "client_metadata.x-codex-installation-id").String())
		}
	}
}

func TestOAuthOSIdentityHTTPUsesSelectedSyncRoot(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		t.Run(map[bool]string{false: "regular", true: "passthrough"}[passthrough], func(t *testing.T) {
			account := osIdentityTestAccount(t, 707)
			account.Extra["openai_passthrough"] = passthrough
			c := osIdentityTestContext(t, account.OpenAIOAuthOSProfiles.Profiles[OpenAIOSLinux].UserAgent)
			body := []byte(`{"model":"gpt-5.4","stream":false,"instructions":"test","input":"hello"}`)
			svc, repo := osIdentityDailyService()
			svc.accountRepo = newAuthorizedOpenAIOAuthTestRepo(account)
			upstream := &httpUpstreamRecorder{resp: successfulInstallationTestResponse()}
			if passthrough {
				upstream.resp = &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_test\",\"status\":\"completed\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n"))}
			}
			svc.httpUpstream = upstream
			_, err := svc.Forward(context.Background(), c, account, body)
			require.NoError(t, err)
			require.NotNil(t, upstream.lastReq)
			require.Equal(t, OpenAIOSLinux, openai.DetectOSFamilyFromUserAgent(upstream.lastReq.Header.Get("User-Agent")))
			metadata := upstream.lastReq.Header.Get(openAIWSTurnMetadataHeader)
			require.Equal(t, repo.pool.OSRoots[OpenAIOSLinux].SyncSessionID, gjson.Get(metadata, "session_id").String())
			require.Equal(t, account.OpenAIOAuthOSProfiles.Profiles[OpenAIOSLinux].InstallationID, gjson.Get(metadata, "installation_id").String())
		})
	}
}

func TestOAuthOSIdentityProfileOnlyCapturesEnvironmentWithoutAllocatingRoots(t *testing.T) {
	account := osIdentityTestAccount(t, 708)
	svc, daily := osIdentityDailyService()
	svc.accountRepo = newAuthorizedOpenAIOAuthTestRepo(account)
	c := osIdentityTestContext(t, "generic-client")
	captureOpenAIOAuthProfileRequest(c, []byte(`{"input":[{"role":"user","content":[{"type":"input_text","text":"<environment_context><cwd>/home/test/project</cwd></environment_context>"}]}]}`))
	plan, err := svc.ResolveOpenAIOAuthProfileIdentityPlan(context.Background(), c, account, OpenAIOAuthInstallationPreserve)
	require.NoError(t, err)
	require.Equal(t, OpenAIOSLinux, plan.OSFamily)
	require.Equal(t, "environment_context", plan.OSSource)
	require.Equal(t, 0, daily.calls)
	require.False(t, plan.TurnIdentityEnabled)
	require.False(t, plan.InstallationEnabled)
}

func TestOAuthOSIdentityRetryKeepsOwnerProfileAfterDefaultOrInstallationChanges(t *testing.T) {
	for _, shadow := range []bool{false, true} {
		for _, changeDefault := range []bool{false, true} {
			name := map[bool]string{false: "owner", true: "shadow"}[shadow] + map[bool]string{false: "/regenerate", true: "/new-default"}[changeDefault]
			t.Run(name, func(t *testing.T) {
				owner := osIdentityTestAccount(t, 710)
				owner.Credentials = nil
				owner.OpenAIOAuthOSProfiles.DefaultOS = OpenAIOSWindows
				owner.OpenAIOAuthOSProfiles.Authorization = &OpenAIOAuthOSAuthorizationSummary{Status: OpenAIOAuthAuthorizationUnauthorized}
				repo := newSchedulerOSAuthorizationTestRepo(*owner)
				account := &repo.accounts[0]
				if shadow {
					account = &Account{ID: 711, Platform: PlatformOpenAI, Type: AccountTypeOAuth, ParentAccountID: &owner.ID}
				}
				svc := &OpenAIGatewayService{accountRepo: repo}
				c := osIdentityTestContext(t, "generic-client")
				firstBody := []byte(`{"input":"first"}`)
				ctx, first, err := svc.prepareOpenAIOAuthRequestScope(context.Background(), c, account, firstBody)
				require.NoError(t, err)
				require.Empty(t, OpenAIRequestOSFromContext(ctx).Family)
				firstCapture := CaptureOpenAIOAuthIdentity(c, firstBody, "first-attempt")
				selection, enabled, err := svc.resolveOpenAIOAuthOSSelection(ctx, c, first, firstCapture)
				require.NoError(t, err)
				require.True(t, enabled)
				require.Equal(t, OpenAIOSWindows, selection.Profile.OSFamily)

				updated := CloneOpenAIOAuthOSProfiles(repo.accounts[0].OpenAIOAuthOSProfiles)
				regenerated := updated.Profiles[OpenAIOSWindows]
				regenerated.InstallationID = uuid.NewString()
				regenerated.UserAgent = "codex_cli_rs/0.999.1 (Windows 11.0.26100; x86_64) WindowsTerminal"
				regenerated.SyncSessionID = uuid.Must(uuid.NewV7()).String()
				updated.Profiles[OpenAIOSWindows] = regenerated
				if changeDefault {
					updated.DefaultOS = OpenAIOSLinux
				}
				repo.accounts[0].OpenAIOAuthOSProfiles = updated

				// WS retries receive the handler's original, unscoped business account.
				ClearOpenAIOAuthIdentityPlan(c)
				retryInput := *account
				retryInput.OpenAIOAuthAuthorizationGeneration = "current-generation"
				retryInput.OpenAIOAuthCredentialRevision = 9
				retryInput.OpenAIOAuthCredentialStateGeneration = "current-state"
				retryInput.OpenAIOAuthCredentialEpoch = "current-epoch"
				retryBody := []byte(`{"input":"next turn"}`)
				ctx, retry, err := svc.prepareOpenAIOAuthRequestScope(ctx, c, &retryInput, retryBody)
				require.NoError(t, err)
				require.Equal(t, account.ID, retry.ID)
				require.Equal(t, selection.Profile.UserAgent, retry.GetOpenAIUserAgent())
				require.Equal(t, selection.Profile.InstallationID, retry.Extra[openAIPinnedInstallationIDKey])
				require.Equal(t, selection.Profile, retry.OpenAIOAuthOSProfiles.Profiles[OpenAIOSWindows])
				require.Equal(t, "current-generation", retry.OpenAIOAuthAuthorizationGeneration)
				require.Equal(t, int64(9), retry.OpenAIOAuthCredentialRevision)
				require.Equal(t, "current-state", retry.OpenAIOAuthCredentialStateGeneration)
				require.Equal(t, "current-epoch", retry.OpenAIOAuthCredentialEpoch)
				nextCapture := CaptureOpenAIOAuthIdentity(c, retryBody, "second-attempt")
				continued, enabled, err := svc.resolveOpenAIOAuthOSSelection(ctx, c, retry, nextCapture)
				require.NoError(t, err)
				require.True(t, enabled)
				require.Equal(t, selection.Profile, continued.Profile)

				freshContext := osIdentityTestContext(t, "generic-client")
				_, fresh, err := svc.prepareOpenAIOAuthRequestScope(context.Background(), freshContext, account, firstBody)
				require.NoError(t, err)
				want := updated.Profiles[updated.DefaultOS]
				require.Equal(t, want.OSFamily, fresh.OpenAIOAuthCredentialOS)
				require.Equal(t, want.UserAgent, fresh.GetOpenAIUserAgent())
				require.Equal(t, want.InstallationID, fresh.Extra[openAIPinnedInstallationIDKey])
				require.Zero(t, repo.credentialReads.Load(), "identity retries must not read authorization metadata")
				require.Nil(t, account.Credentials, "identity projection must not mutate the selected account")
				require.Empty(t, account.OpenAIOAuthCredentialOS)
			})
		}
	}
}

func TestOAuthOSIdentityCredentialReloadKeepsFrozenProfileAndFreshTokens(t *testing.T) {
	for _, shadow := range []bool{false, true} {
		t.Run(map[bool]string{false: "owner", true: "shadow"}[shadow], func(t *testing.T) {
			owner, repo := oauthOSCredentialFixture(t)
			account := owner
			if shadow {
				account = &Account{ID: 11, Platform: PlatformOpenAI, Type: AccountTypeOAuth, ParentAccountID: &owner.ID}
				repo.accounts[account.ID] = account
			}
			scoped, err := ResolveOpenAIOAuthCredentialAccount(context.Background(), repo, account, OpenAIOSWindows)
			require.NoError(t, err)
			frozen := scoped.OpenAIOAuthOSProfiles.Profiles[OpenAIOSWindows]
			updated := CloneOpenAIOAuthOSProfiles(owner.OpenAIOAuthOSProfiles)
			changed := updated.Profiles[OpenAIOSWindows]
			changed.InstallationID = uuid.NewString()
			changed.UserAgent = "codex_cli_rs/0.999.1 (Windows 11.0.26100; x86_64) WindowsTerminal"
			updated.Profiles[OpenAIOSWindows] = changed
			updated.DefaultOS = OpenAIOSLinux
			owner.OpenAIOAuthOSProfiles = updated
			// Parent lookup during the first materialization must retain the
			// identity already frozen before the persisted profile changed.
			svc := &OpenAIGatewayService{accountRepo: repo}
			c := osIdentityTestContext(t, "generic-client")
			ctx := ContextWithOpenAIRequestOS(context.Background(), OpenAIRequestOS{})
			capture := CaptureOpenAIOAuthIdentity(c, []byte(`{"input":"first turn"}`), "first-after-regeneration")
			selection, enabled, err := svc.resolveOpenAIOAuthOSSelection(ctx, c, scoped, capture)
			require.NoError(t, err)
			require.True(t, enabled)
			require.Equal(t, owner.ID, selection.OwnerID)
			require.Equal(t, frozen, selection.Profile)
			owner.Credentials["access_token"] = "refreshed-shared-token"
			grant := repo.slots[OpenAIOSWindows]
			grant.Revision++
			grant.Status = OpenAIOAuthAuthorizationUnauthorized
			cooldown := time.Now().Add(time.Minute)
			grant.RefreshRetryAfter = &cooldown
			reloaded, err := ReloadOpenAIOAuthCredentialAccount(context.Background(), repo, scoped)
			require.NoError(t, err)
			require.Equal(t, account.ID, reloaded.ID)
			require.Equal(t, frozen.UserAgent, reloaded.GetOpenAIUserAgent())
			require.Equal(t, frozen.InstallationID, reloaded.Extra[openAIPinnedInstallationIDKey])
			require.Equal(t, frozen, reloaded.OpenAIOAuthOSProfiles.Profiles[OpenAIOSWindows])
			require.Equal(t, OpenAIOSWindows, reloaded.OpenAIOAuthCredentialOS)
			require.Equal(t, "refreshed-shared-token", reloaded.GetOpenAIAccessToken())
			require.Equal(t, grant.Revision, reloaded.OpenAIOAuthCredentialRevision)
			require.Equal(t, "shared-token", scoped.GetOpenAIAccessToken())
		})
	}
}
