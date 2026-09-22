package handler

import (
	"fmt"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// These legacy handler scenarios exercise request conversion and failover using
// one explicitly authorized default OS. Authorization-specific scenarios provide
// their own slots and do not inherit this fixture.
func authorizeOpenAIHandlerTestAccount(t *testing.T, account *service.Account) {
	t.Helper()
	if !service.IsOpenAIOAuthOSProfileOwner(account) {
		return
	}
	profiles, err := service.BuildOpenAIOAuthOSProfiles(account, account.OpenAIOAuthOSProfiles)
	require.NoError(t, err)
	profile := profiles.Profiles[profiles.DefaultOS]
	profile.Authorization.Status = service.OpenAIOAuthAuthorizationAuthorized
	profiles.Profiles[profiles.DefaultOS] = profile
	service.ApplyOpenAIOAuthOSProfiles(account, profiles)
}

func openAIHandlerTestCredential(account *service.Account, os string) *service.OpenAIOAuthOSCredential {
	if account == nil || account.OpenAIOAuthOSProfiles == nil || os != account.OpenAIOAuthOSProfiles.DefaultOS {
		return nil
	}
	profile := account.OpenAIOAuthOSProfiles.Profiles[os]
	if profile.Authorization.Status != service.OpenAIOAuthAuthorizationAuthorized {
		return nil
	}
	return &service.OpenAIOAuthOSCredential{
		OwnerAccountID: account.ID, OSFamily: os, Credentials: service.OpenAIOAuthProviderCredentials(account.Credentials),
		Status: service.OpenAIOAuthAuthorizationAuthorized, AuthorizationGeneration: fmt.Sprintf("handler-test-%d-%s", account.ID, os), Revision: 1,
	}
}
