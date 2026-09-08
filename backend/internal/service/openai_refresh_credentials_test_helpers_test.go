package service

import (
	"context"
	"reflect"
)

func applyOpenAIRefreshTestPatch(account *Account, id int64, expectedAuth map[string]any, expectedProxyID *int64, patch map[string]any, removed []string) bool {
	if account == nil || account.ID != id || account.Platform != PlatformOpenAI || account.Type != AccountTypeOAuth || account.IsCredentialShadow() ||
		!reflect.DeepEqual(openAIRefreshAuthIdentity(account.Credentials), expectedAuth) || !reflect.DeepEqual(account.ProxyID, expectedProxyID) {
		return false
	}
	credentials := shallowCopyMap(account.Credentials)
	if credentials == nil {
		credentials = make(map[string]any)
	}
	for _, key := range removed {
		delete(credentials, key)
	}
	for key, value := range patch {
		credentials[key] = value
	}
	account.Credentials = credentials
	return true
}

func (r *tokenRefreshCandidateRepo) GetByID(_ context.Context, id int64) (*Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.accounts {
		if r.accounts[i].ID == id {
			return snapshotOAuthRefreshAccount(&r.accounts[i]), nil
		}
	}
	return nil, ErrAccountNotFound
}

func (r *tokenRefreshCandidateRepo) PatchOpenAIOAuthCredentialsIfUnchanged(_ context.Context, id int64, expectedAuth map[string]any, expectedProxyID *int64, patch map[string]any, removed []string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.accounts {
		if r.accounts[i].ID == id {
			applied := applyOpenAIRefreshTestPatch(&r.accounts[i], id, expectedAuth, expectedProxyID, patch, removed)
			if applied {
				r.updatedCredentialIDs = append(r.updatedCredentialIDs, id)
			}
			return applied, nil
		}
	}
	return false, nil
}
