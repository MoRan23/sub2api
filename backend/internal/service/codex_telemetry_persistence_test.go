package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

type telemetryLatestAccounts struct {
	AccountRepository
	account *Account
}

func (r *telemetryLatestAccounts) GetByID(context.Context, int64) (*Account, error) {
	if r.account == nil {
		return nil, errors.New("not found")
	}
	return r.account, nil
}

type telemetryRouteDirectory struct {
	ProxyRepository
	proxy     *Proxy
	requested int64
}

func (r *telemetryRouteDirectory) GetByID(_ context.Context, id int64) (*Proxy, error) {
	r.requested = id
	if r.proxy == nil {
		return nil, errors.New("route removed")
	}
	return r.proxy, nil
}

func telemetryPersistedTransportFixture(t *testing.T) (*CodexTelemetryService, *telemetryLatestAccounts, *telemetryRouteDirectory, codexTelemetryJob) {
	t.Helper()
	s := NewCodexTelemetryService(nil)
	t.Cleanup(s.Stop)
	account := &Account{ID: 11, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive,
		Credentials: map[string]any{"access_token": "new-current-token", "chatgpt_account_id": "workspace"},
		OpenAIOAuthOSProfiles: &OpenAIOAuthOSProfiles{DefaultOS: "windows", Profiles: map[string]OpenAIOAuthOSProfile{
			"windows": {OSFamily: "windows", InstallationID: "installation-current"},
		}},
	}
	accounts := &telemetryLatestAccounts{account: account}
	proxies := &telemetryRouteDirectory{proxy: &Proxy{ID: 71, Protocol: "http", Host: "127.0.0.1", Port: 7897, Username: "route", Password: "secret", Status: StatusActive}}
	s.SetPersistence(NewMemoryCodexTelemetryStore(), accounts, proxies)
	proxyID := int64(71)
	key := CodexTelemetryPoolKey{OwnerAccountID: 11, OSFamily: "windows", InstallationID: "installation-current"}
	input := CodexTelemetryInput{AccountID: 11, OwnerAccountID: 11, OSFamily: "windows", InstallationID: key.InstallationID, ManagedInstallation: true,
		ChatGPTAccountID: "workspace", UserAgent: "codex-tui/0.155.1 (Windows 10.0.26200; x86_64)", ProxyID: &proxyID}
	return s, accounts, proxies, codexTelemetryJob{profile: codexTelemetryProfile{input: input}, persisted: &CodexTelemetryBatch{Pool: CodexTelemetryPool{Key: key}, ProxyID: &proxyID}}
}

func TestCodexTelemetryPersistentSenderUsesCurrentCredentialsAndFrozenRoute(t *testing.T) {
	s, accounts, proxies, job := telemetryPersistedTransportFixture(t)
	newBusinessProxy := int64(999)
	accounts.account.ProxyID = &newBusinessProxy
	require.Empty(t, s.hydrateTelemetryTransport(context.Background(), &job))
	require.Equal(t, "new-current-token", job.profile.client.accessToken)
	require.EqualValues(t, 71, proxies.requested)
	require.Equal(t, proxies.proxy.URL(), job.profile.input.ProxyURL)
	require.Equal(t, job.profile.input.UserAgent, job.profile.client.nativeHTTPScope.SourceUserAgent)
}

func TestCodexTelemetryPersistentSenderNeverFallsBackFromMissingProxy(t *testing.T) {
	s, _, proxies, job := telemetryPersistedTransportFixture(t)
	proxies.proxy = nil
	require.Equal(t, "proxy_unavailable", s.hydrateTelemetryTransport(context.Background(), &job))
	require.Empty(t, job.profile.client.accessToken)
	require.Empty(t, job.profile.client.proxyURL)
}

func TestCodexTelemetryPersistentSenderRejectsChangedIdentity(t *testing.T) {
	for _, change := range []string{"workspace", "installation", "qualification"} {
		t.Run(change, func(t *testing.T) {
			s, accounts, _, job := telemetryPersistedTransportFixture(t)
			switch change {
			case "workspace":
				accounts.account.Credentials["chatgpt_account_id"] = "other-workspace"
			case "installation":
				accounts.account.OpenAIOAuthOSProfiles.Profiles["windows"] = OpenAIOAuthOSProfile{InstallationID: "new-installation"}
			case "qualification":
				accounts.account.Type = AccountTypeAPIKey
			}
			require.Equal(t, "stale_identity", s.hydrateTelemetryTransport(context.Background(), &job))
		})
	}
}

func TestCodexTelemetryPersistentSenderAllowsUnpinnedClientInstallation(t *testing.T) {
	s, _, _, job := telemetryPersistedTransportFixture(t)
	job.profile.input.ManagedInstallation = false
	job.profile.input.InstallationID = "client-owned-installation"
	job.persisted.Pool.Key.InstallationID = job.profile.input.InstallationID
	require.Empty(t, s.hydrateTelemetryTransport(context.Background(), &job))
}

func TestCodexTelemetryProfilePersistenceExcludesTransportSecrets(t *testing.T) {
	s, _, _, job := telemetryPersistedTransportFixture(t)
	require.Empty(t, s.hydrateTelemetryTransport(context.Background(), &job))
	raw, err := marshalCodexTelemetryProfile(job.profile)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "new-current-token")
	require.NotContains(t, string(raw), "route:secret")
	require.NotContains(t, string(raw), "127.0.0.1")
	decoded, err := unmarshalCodexTelemetryProfile(raw)
	require.NoError(t, err)
	require.Empty(t, decoded.input.AccessToken)
	require.Empty(t, decoded.input.ProxyURL)
	require.Equal(t, job.profile.input.InstallationID, decoded.input.InstallationID)
}
