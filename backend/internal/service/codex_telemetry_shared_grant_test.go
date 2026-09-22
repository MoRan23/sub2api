package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCodexTelemetrySharedGrantRefreshKeepsThreeIdentityPools(t *testing.T) {
	s, accounts, _, template := telemetryPersistedTransportFixture(t)
	store := NewMemoryCodexTelemetryStore().(*MemoryCodexTelemetryStore)
	now := time.Now().UTC()
	for _, family := range []string{"windows", "macos", "linux"} {
		installation := family + "-installation"
		accounts.account.OpenAIOAuthOSProfiles.Profiles[family] = OpenAIOAuthOSProfile{OSFamily: family, InstallationID: installation}
		accounts.slots[family] = &OpenAIOAuthOSCredential{OwnerAccountID: 11, OSFamily: family, StateGeneration: family + "-state"}
		attempt := runtimeTestAttempt(now, family, "same-turn", "same-sampling")
		attempt.profile.input.CredentialOS, attempt.profile.input.AuthorizationGeneration = family, "shared-auth"
		runtimeTestMutation(t, store, attempt, nil, false)
	}
	require.Len(t, store.pools, 3)
	for _, token := range []string{"current-shared-token", "refreshed-shared-token"} {
		accounts.grant.Credentials = map[string]any{"access_token": token, "chatgpt_account_id": "workspace"}
		for _, family := range []string{"windows", "macos", "linux"} {
			job := template
			batch := *template.persisted
			job.persisted = &batch
			job.profile.input.CredentialOS, job.profile.input.OSFamily = family, family
			job.profile.input.InstallationID = family + "-installation"
			job.persisted.Pool.Key.OSFamily, job.persisted.Pool.Key.InstallationID = family, job.profile.input.InstallationID
			require.Empty(t, s.hydrateTelemetryTransport(context.Background(), &job))
			require.True(t, job.profile.client.accessToken == token)
			require.Equal(t, family, codexTelemetryPoolOS(job.profile.input))
			require.Equal(t, "shared-auth", job.profile.input.AuthorizationGeneration)
		}
	}
	listed, err := accounts.ListOpenAIOAuthOSCredentials(context.Background(), 11)
	require.NoError(t, err)
	require.Len(t, listed, 1, "refresh enumeration returns the shared grant once")
}
