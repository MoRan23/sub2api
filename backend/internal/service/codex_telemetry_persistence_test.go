package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type telemetryLatestAccounts struct {
	AccountRepository
	account *Account
	slots   map[string]*OpenAIOAuthOSCredential
	grant   *OpenAIOAuthOSCredential
}

func (r *telemetryLatestAccounts) GetOpenAIOAuthOSCredential(_ context.Context, _ int64, os string) (*OpenAIOAuthOSCredential, error) {
	metadata := r.slots[os]
	if metadata == nil || r.grant == nil {
		return nil, nil
	}
	projected := *r.grant
	projected.OSFamily, projected.StateGeneration = os, metadata.StateGeneration
	return &projected, nil
}

func (r *telemetryLatestAccounts) ListOpenAIOAuthOSCredentials(ctx context.Context, id int64) ([]*OpenAIOAuthOSCredential, error) {
	slot, err := r.GetOpenAIOAuthOSCredential(ctx, id, r.account.OpenAIOAuthOSProfiles.DefaultOS)
	if err != nil || slot == nil {
		return nil, err
	}
	return []*OpenAIOAuthOSCredential{slot}, nil
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
	accounts := &telemetryLatestAccounts{account: account, slots: map[string]*OpenAIOAuthOSCredential{
		"windows": {OwnerAccountID: 11, OSFamily: "windows", StateGeneration: "windows-state"},
	}, grant: &OpenAIOAuthOSCredential{OwnerAccountID: 11, Credentials: account.Credentials, Status: OpenAIOAuthAuthorizationAuthorized, AuthorizationGeneration: "shared-auth", Revision: 1}}
	proxies := &telemetryRouteDirectory{proxy: &Proxy{ID: 71, Protocol: "http", Host: "127.0.0.1", Port: 7897, Username: "route", Password: "secret", Status: StatusActive}}
	s.SetPersistence(NewMemoryCodexTelemetryStore(), accounts, proxies)
	proxyID := int64(71)
	key := CodexTelemetryPoolKey{OwnerAccountID: 11, OSFamily: "windows", InstallationID: "installation-current"}
	input := CodexTelemetryInput{AccountID: 11, OwnerAccountID: 11, OSFamily: "windows", InstallationID: key.InstallationID, ManagedInstallation: true,
		CredentialOS: "windows", AuthorizationGeneration: "shared-auth",
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
	require.Equal(t, job.profile.input.CredentialOS, decoded.input.CredentialOS)
	require.Equal(t, job.profile.input.AuthorizationGeneration, decoded.input.AuthorizationGeneration)
	public, err := json.Marshal(job.profile.input)
	require.NoError(t, err)
	require.NotContains(t, string(public), "shared-auth")
}

func TestCodexTelemetryOSAuthorizationUsesFrozenSlotAndLatestRefresh(t *testing.T) {
	s, accounts, _, job := telemetryPersistedTransportFixture(t)
	accounts.account.OpenAIOAuthOSProfiles.Profiles["linux"] = OpenAIOAuthOSProfile{OSFamily: "linux", InstallationID: "linux-installation"}
	accounts.slots["linux"] = &OpenAIOAuthOSCredential{OwnerAccountID: 11, OSFamily: "linux", StateGeneration: "linux-state"}
	accounts.grant.Credentials = map[string]any{"access_token": "shared-latest-token", "chatgpt_account_id": "workspace"}
	job.profile.input.CredentialOS, job.profile.input.AuthorizationGeneration = "linux", "shared-auth"
	job.profile.input.OSFamily = "unknown"
	job.profile.input.InstallationID, job.persisted.Pool.Key.InstallationID = "linux-installation", "linux-installation"
	job.persisted.Pool.Key.OSFamily = "linux"
	// The legacy account mirror is not the authoritative shared grant.
	accounts.account.Credentials = map[string]any{}
	require.Empty(t, s.hydrateTelemetryTransport(context.Background(), &job))
	require.Equal(t, "shared-latest-token", job.profile.client.accessToken)
	require.Equal(t, "linux", codexTelemetryPoolOS(job.profile.input))
}

func TestCodexTelemetryOSAuthorizationRejectsOldOrMissingScope(t *testing.T) {
	for _, change := range []string{"reauthorized", "revoked", "missing_generation", "missing_os", "cooldown"} {
		t.Run(change, func(t *testing.T) {
			s, accounts, _, job := telemetryPersistedTransportFixture(t)
			switch change {
			case "reauthorized":
				accounts.grant.AuthorizationGeneration = "new-authorization"
			case "revoked":
				accounts.grant.Status = OpenAIOAuthAuthorizationUnauthorized
			case "missing_generation":
				job.profile.input.AuthorizationGeneration = ""
			case "missing_os":
				job.profile.input.CredentialOS = ""
			case "cooldown":
				until := time.Now().Add(time.Minute)
				accounts.grant.RefreshRetryAfter = &until
			}
			require.Equal(t, "stale_authorization", s.hydrateTelemetryTransport(context.Background(), &job))
			require.Empty(t, job.profile.client.accessToken)
		})
	}
}
