package service

import (
	"context"
	"fmt"
	"maps"
)

// authorizeOpenAIOAuthTestAccount explicitly provisions the OS slots needed by
// legacy identity tests. Authorization-focused tests keep their own fixtures.
func authorizeOpenAIOAuthTestAccount(account *Account, families ...string) {
	if !IsOpenAIOAuthOSProfileOwner(account) {
		return
	}
	if account.OpenAIOAuthOSProfiles == nil {
		profiles, err := BuildOpenAIOAuthOSProfiles(account, nil)
		if err != nil {
			panic(err)
		}
		account.OpenAIOAuthOSProfiles = profiles
	} else {
		account.OpenAIOAuthOSProfiles = CloneOpenAIOAuthOSProfiles(account.OpenAIOAuthOSProfiles)
	}
	for _, family := range families {
		profile := account.OpenAIOAuthOSProfiles.Profiles[family]
		profile.Authorization.Status = OpenAIOAuthAuthorizationAuthorized
		account.OpenAIOAuthOSProfiles.Profiles[family] = profile
	}
}

func openAIOAuthTestCredential(account *Account, family string) *OpenAIOAuthOSCredential {
	if account == nil || account.OpenAIOAuthOSProfiles == nil {
		return nil
	}
	profile, exists := account.OpenAIOAuthOSProfiles.Profiles[family]
	if !exists || profile.Authorization.Status != OpenAIOAuthAuthorizationAuthorized {
		return nil
	}
	credentials := maps.Clone(account.Credentials)
	if credentials == nil {
		credentials = make(map[string]any)
	}
	key := fmt.Sprintf("%d/%s", account.ID, family)
	if credentials["access_token"] == nil {
		credentials["access_token"] = "test-access-" + key
	} else if family != account.OpenAIOAuthOSProfiles.DefaultOS {
		credentials["access_token"] = fmt.Sprint(credentials["access_token"]) + "-" + family
	}
	credentials["refresh_token"] = "test-refresh-" + key
	return &OpenAIOAuthOSCredential{
		OwnerAccountID: account.ID, OSFamily: family, Credentials: credentials,
		Status: profile.Authorization.Status, AuthorizationGeneration: "test-generation-" + key,
		Revision: 1, RefreshRetryAfter: profile.Authorization.RefreshRetryAfter,
	}
}

func openAIOAuthTestCredentials(account *Account) []*OpenAIOAuthOSCredential {
	var slots []*OpenAIOAuthOSCredential
	for _, family := range OpenAIOAuthOSFamilies() {
		if slot := openAIOAuthTestCredential(account, family); slot != nil {
			slots = append(slots, slot)
		}
	}
	return slots
}

func newAuthorizedOpenAIOAuthTestRepo(accounts ...*Account) *installationIdentityRepoStub {
	repo := &installationIdentityRepoStub{accounts: make(map[int64]*Account, len(accounts))}
	for _, account := range accounts {
		authorizeOpenAIOAuthTestAccount(account, OpenAIOAuthOSFamilies()...)
		repo.accounts[account.ID] = account
	}
	return repo
}

func (r *installationIdentityRepoStub) GetOpenAIOAuthOSCredential(_ context.Context, id int64, family string) (*OpenAIOAuthOSCredential, error) {
	return openAIOAuthTestCredential(r.accounts[id], family), nil
}

func (r *installationIdentityRepoStub) ListOpenAIOAuthOSCredentials(_ context.Context, id int64) ([]*OpenAIOAuthOSCredential, error) {
	return openAIOAuthTestCredentials(r.accounts[id]), nil
}

func (r *outboundIdentityAccountRepoStub) GetOpenAIOAuthOSCredential(_ context.Context, id int64, family string) (*OpenAIOAuthOSCredential, error) {
	return openAIOAuthTestCredential(r.accounts[id], family), nil
}

func (r *outboundIdentityAccountRepoStub) ListOpenAIOAuthOSCredentials(_ context.Context, id int64) ([]*OpenAIOAuthOSCredential, error) {
	return openAIOAuthTestCredentials(r.accounts[id]), nil
}
