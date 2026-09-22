package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func oauthOSWSTestFrame(t *testing.T, os, session string) []byte {
	t.Helper()
	payload := map[string]any{
		"type": "response.create", "model": "gpt-5.4", "stream": true,
		"input": []map[string]any{requestOSTestMessage("user", "<environment_context><os>"+os+"</os></environment_context>")},
	}
	if session != "" {
		payload["client_metadata"] = map[string]any{"session_id": session, "thread_id": session}
	}
	body, err := json.Marshal(payload)
	require.NoError(t, err)
	return body
}

func TestOpenAIOAuthOSWSFrameCaptureFreezesConnectionIdentity(t *testing.T) {
	const ua = "codex-tui/0.152.0 (Windows 10.0.26200; x86_64) WindowsTerminal"
	c := newOutboundIdentityTestContext(t, map[string]string{"User-Agent": ua, "version": "0.152.0"})
	first := CaptureOpenAIOAuthIdentity(c, oauthOSWSTestFrame(t, "Windows", "ws-os-initial"), "")
	first.ReceivedAt = time.Date(2026, 9, 21, 15, 59, 59, 0, time.UTC)
	plan := OpenAIOAuthIdentityPlan{Capture: first, RequestTurn: first.RequestTurn}
	for _, test := range []struct{ name, session string }{
		{"implicit next turn", ""},
		{"explicit next tuple", "ws-os-next"},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := oauthOSWSTestFrame(t, "Linux", test.session)
			frame := captureOpenAIWSFrameIdentity(body, &plan)
			require.Equal(t, first.UserAgent, frame.UserAgent)
			require.Equal(t, first.UserAgentVersion, frame.UserAgentVersion)
			require.Equal(t, first.OSFamily, frame.OSFamily)
			require.Equal(t, first.OSSource, frame.OSSource)
			require.Equal(t, first.ReceivedAt, frame.ReceivedAt)
			require.NotEqual(t, first.RequestTurn.ID, frame.RequestTurn.ID, "the next turn still receives its own turn identity")
			if test.session != "" {
				require.Equal(t, test.session, frame.Logical.SessionKey)
			} else {
				require.Equal(t, first.Logical, frame.Logical)
			}

			fresh := captureOpenAIWSFrameIdentity(body, nil)
			require.Equal(t, OpenAIOSLinux, fresh.OSFamily)
			require.Equal(t, "environment_context", fresh.OSSource)
			require.Empty(t, fresh.UserAgent)
			require.WithinDuration(t, time.Now(), fresh.ReceivedAt, time.Second)
		})
	}
}

type oauthOSWSDailyRepository struct {
	OAuthDailySessionRepository
	pools map[string]OAuthDailySessionPool
	times []time.Time
}

func (r *oauthOSWSDailyRepository) GetOrCreateOAuthDailySessionPoolForOS(_ context.Context, accountID int64, defaultOS string, now time.Time) (OAuthDailySessionPool, error) {
	r.times = append(r.times, now)
	date := OAuthDailyBusinessDate(now)
	if pool, exists := r.pools[date]; exists {
		return pool, nil
	}
	pool := OAuthDailySessionPool{AccountID: accountID, BusinessDate: date, DefaultOS: defaultOS, OSRoots: make(map[string]OAuthDailyOSRoots)}
	for _, os := range []string{OpenAIOSWindows, OpenAIOSMacOS, OpenAIOSLinux} {
		pool.OSRoots[os] = OAuthDailyOSRoots{StreamSessionID: uuid.Must(uuid.NewV7()).String(), SyncSessionID: uuid.Must(uuid.NewV7()).String()}
	}
	if r.pools == nil {
		r.pools = make(map[string]OAuthDailySessionPool)
	}
	r.pools[date] = pool
	return pool, nil
}

func TestOpenAIOAuthOSWSPlanMaterializationKeepsOwnerProfileAndDailyRoots(t *testing.T) {
	resetProcessCodexIdentityStore(t)
	settings := &dailyRotationSettingRepo{values: map[string]string{
		SettingKeyEnableOpenAICodexFingerprintNormalization: "true",
		SettingKeyEnableOpenAIUUIDv7SessionIdentity:         "true",
		SettingKeyEnableOpenAIOAuthDailySessionRotation:     "true",
	}}
	daily := &oauthOSWSDailyRepository{}
	svc := &OpenAIGatewayService{
		cfg:                   &config.Config{JWT: config.JWTConfig{Secret: "oauth-os-ws-test"}},
		settingService:        NewSettingService(settings, nil),
		oauthDailySessionRepo: daily,
	}
	account := &Account{ID: 941177, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	require.NoError(t, PrepareOpenAIOAuthOSProfilesForCreate(account))
	svc.accountRepo = newAuthorizedOpenAIOAuthTestRepo(account)
	c := newOutboundIdentityTestContext(t, map[string]string{"User-Agent": "codex-tui/0.152.0 (Windows 10.0.26200; x86_64) WindowsTerminal"})
	setOpenAIClientRequestedStream(c, true)
	firstCapture := CaptureOpenAIOAuthIdentity(c, oauthOSWSTestFrame(t, "Windows", "ws-os-plan"), "")
	firstCapture.ReceivedAt = time.Date(2026, 9, 21, 15, 59, 59, 0, time.UTC)
	options := OpenAIOAuthIdentityPlanOptions{TurnIdentityEnabled: true, InstallationPolicy: OpenAIOAuthInstallationAccountPin}
	first, err := svc.GetOrResolveOpenAIOAuthOutboundIdentity(context.Background(), c, account, firstCapture, options, nil)
	require.NoError(t, err)
	require.Equal(t, OpenAIOSWindows, first.OSFamily)
	require.True(t, first.InstallationEnabled)
	require.True(t, first.DailyRootsEnabled)
	require.True(t, first.TurnIdentityEnabled)
	require.Equal(t, "2026-09-21", first.DailyBusinessDate)
	require.Equal(t, first.DailyStreamRoot, first.TurnIdentity.SessionID)
	require.Len(t, daily.times, 1)
	require.Equal(t, firstCapture.ReceivedAt, daily.times[0])

	// An administrator can regenerate the stored profile while this socket is
	// open. The same owner's already-selected snapshot still owns this socket.
	changed := account.OpenAIOAuthOSProfiles.Profiles[OpenAIOSWindows]
	changed.InstallationID = uuid.NewString()
	changed.UserAgent = "codex-tui/0.152.0 (Windows 11.0.26100; x86_64) changed-terminal"
	account.OpenAIOAuthOSProfiles.Profiles[OpenAIOSWindows] = changed
	for _, session := range []string{"", "ws-os-new-explicit-tuple"} {
		frameCapture := captureOpenAIWSFrameIdentity(oauthOSWSTestFrame(t, "Linux", session), &first)
		next, err := svc.GetOrResolveOpenAIOAuthOutboundIdentity(context.Background(), c, account, frameCapture, options, &first)
		require.NoError(t, err)
		require.Equal(t, first.OSProfile, next.OSProfile)
		require.Equal(t, first.OSOwnerID, next.OSOwnerID)
		require.Equal(t, first.ClientIdentity, next.ClientIdentity)
		require.Equal(t, first.InstallationID, next.InstallationID)
		require.Equal(t, first.DailyBusinessDate, next.DailyBusinessDate)
		require.Equal(t, first.DailyStreamRoot, next.DailyStreamRoot)
		require.Equal(t, first.DailySyncRoot, next.DailySyncRoot)
		require.Equal(t, first.ReceivedAt, next.ReceivedAt)
		require.Equal(t, first.DailyStreamRoot, next.TurnIdentity.SessionID)
		require.Len(t, daily.times, 1, "same-owner frames must not reprovision daily roots")
	}

	// Reconnecting creates a new context and capture. Simulate the next UTC+8
	// business day without a global test clock or a real wait across midnight.
	reconnectedContext := newOutboundIdentityTestContext(t, map[string]string{"User-Agent": "unrecognized-client/1.0"})
	setOpenAIClientRequestedStream(reconnectedContext, true)
	reconnectedCapture := CaptureOpenAIOAuthIdentity(reconnectedContext, oauthOSWSTestFrame(t, "Linux", "ws-os-plan"), "")
	require.Equal(t, OpenAIOSLinux, reconnectedCapture.OSFamily)
	require.Equal(t, "environment_context", reconnectedCapture.OSSource)
	reconnectedCapture.ReceivedAt = firstCapture.ReceivedAt.Add(2 * time.Second)
	reconnected, err := svc.GetOrResolveOpenAIOAuthOutboundIdentity(context.Background(), reconnectedContext, account, reconnectedCapture, options, nil)
	require.NoError(t, err)
	require.Equal(t, OpenAIOSLinux, reconnected.OSFamily)
	require.Equal(t, "2026-09-22", reconnected.DailyBusinessDate)
	require.NotEqual(t, first.ClientIdentity.UserAgent, reconnected.ClientIdentity.UserAgent)
	require.NotEqual(t, first.InstallationID, reconnected.InstallationID)
	require.NotEqual(t, first.DailyStreamRoot, reconnected.DailyStreamRoot)
	require.NotEqual(t, first.DailySyncRoot, reconnected.DailySyncRoot)
	require.Equal(t, reconnected.DailyStreamRoot, reconnected.TurnIdentity.SessionID)
	require.Len(t, daily.times, 2)
	require.Equal(t, reconnectedCapture.ReceivedAt, daily.times[1])
}
