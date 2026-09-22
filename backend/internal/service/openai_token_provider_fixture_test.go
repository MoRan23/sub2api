package service

import (
	"context"
	"strconv"
)

type openAIProviderTestRepo struct {
	AccountRepository
	OpenAIOAuthOSCredentialsRepository
	account *Account
	slots   map[string]*OpenAIOAuthOSCredential
}

func (r *openAIProviderTestRepo) GetByID(_ context.Context, id int64) (*Account, error) {
	if r.account == nil || id != r.account.ID {
		return nil, ErrAccountNotFound
	}
	return snapshotOAuthRefreshAccount(r.account), nil
}

func (r *openAIProviderTestRepo) GetOpenAIOAuthOSCredential(_ context.Context, id int64, os string) (*OpenAIOAuthOSCredential, error) {
	if r.account == nil || id != r.account.ID || r.slots[os] == nil {
		return nil, ErrOpenAIOAuthOSUnauthorized
	}
	out := *r.slots[os]
	out.Credentials = shallowCopyMap(out.Credentials)
	return &out, nil
}

func (r *openAIProviderTestRepo) SetOpenAIOAuthOSCredentialErrorIfUnchanged(_ context.Context, id int64, os, generation string, revision int64, reason string) (bool, error) {
	slot := r.slots[os]
	if id != r.account.ID || slot == nil || slot.AuthorizationGeneration != generation || slot.Revision != revision {
		return false, nil
	}
	slot.Status, slot.LastError = OpenAIOAuthAuthorizationReauthRequired, reason
	return true, nil
}

func (r *openAIProviderTestRepo) MutateOpenAIOAuthAccountStateIfUnchanged(_ context.Context, id int64, snapshot OpenAIOAuthAccountStateSnapshot, change OpenAIOAuthAccountStateChange) (*OpenAIOAuthAccountStateResult, error) {
	slot := r.slots[r.account.OpenAIOAuthCredentialOS]
	if id != r.account.ID || slot == nil || slot.OwnerAccountID != snapshot.OwnerAccountID || slot.AuthorizationGeneration != snapshot.AuthorizationGeneration || slot.Revision != snapshot.CredentialRevision {
		return &OpenAIOAuthAccountStateResult{}, nil
	}
	r.account.Status = StatusError
	r.account.Schedulable = false
	r.account.ErrorMessage = change.ErrorMessage
	return &OpenAIOAuthAccountStateResult{Applied: true}, nil
}

// Provider tests retain their original credential contents (including deliberate
// missing tokens) while explicitly authorizing one private test slot.
func prepareOpenAIProviderTestAccount(account *Account) string {
	if !IsOpenAIOAuthOSProfileOwner(account) {
		return OpenAITokenCacheKey(account)
	}
	if !OpenAIOAuthOSProfilesComplete(account.OpenAIOAuthOSProfiles) {
		profiles, err := BuildOpenAIOAuthOSProfiles(account, account.OpenAIOAuthOSProfiles)
		if err != nil {
			panic(err)
		}
		account.OpenAIOAuthOSProfiles = profiles
	}
	account.OpenAIOAuthCredentialOS = account.OpenAIOAuthOSProfiles.DefaultOS
	account.OpenAIOAuthCredentialOwnerID = account.ID
	account.OpenAIOAuthAuthorizationGeneration = "provider-test-" + strconv.FormatInt(account.ID, 10)
	account.OpenAIOAuthCredentialRevision = 1
	return OpenAITokenCacheKey(account)
}

func newOpenAIProviderTestRepo(account *Account) *openAIProviderTestRepo {
	prepareOpenAIProviderTestAccount(account)
	return &openAIProviderTestRepo{account: account, slots: map[string]*OpenAIOAuthOSCredential{
		account.OpenAIOAuthCredentialOS: {
			OwnerAccountID: account.ID, OSFamily: account.OpenAIOAuthCredentialOS,
			AuthorizationGeneration: account.OpenAIOAuthAuthorizationGeneration, Revision: account.OpenAIOAuthCredentialRevision,
			Credentials: shallowCopyMap(account.Credentials), Status: OpenAIOAuthAuthorizationAuthorized,
		},
	}}
}
