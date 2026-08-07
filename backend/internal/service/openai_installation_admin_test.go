package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

type openAIInstallationAdminRepoStub struct {
	AccountRepository
	accounts map[int64]*Account
}

func (r *openAIInstallationAdminRepoStub) GetByID(_ context.Context, id int64) (*Account, error) {
	account, ok := r.accounts[id]
	if !ok {
		return nil, ErrAccountNotFound
	}
	return account, nil
}

func (r *openAIInstallationAdminRepoStub) UpdateExtra(_ context.Context, id int64, updates map[string]any) error {
	account := r.accounts[id]
	if account == nil {
		return ErrAccountNotFound
	}
	if account.Extra == nil {
		account.Extra = make(map[string]any)
	}
	for key, value := range updates {
		account.Extra[key] = value
	}
	return nil
}

func TestRegenerateOpenAIInstallationIDRequiresSavedFixedOAuthParent(t *testing.T) {
	oldID := uuid.NewString()
	shadowParentID := int64(1)
	repo := &openAIInstallationAdminRepoStub{accounts: map[int64]*Account{
		1: {ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{
			openAIPinnedInstallationIDKey: oldID,
		}},
		2: {ID: 2, Platform: PlatformOpenAI, Type: AccountTypeOAuth, ParentAccountID: &shadowParentID},
		3: {ID: 3, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{openAIInstallationPinEnabledKey: false}},
		4: {ID: 4, Platform: PlatformOpenAI, Type: AccountTypeAPIKey},
	}}
	svc := &adminServiceImpl{accountRepo: repo}

	newID, err := svc.RegenerateOpenAIInstallationID(context.Background(), 1)
	if err != nil {
		t.Fatalf("regenerate installation_id: %v", err)
	}
	parsed, err := uuid.Parse(newID)
	if err != nil || parsed.Version() != 4 {
		t.Fatalf("regenerated installation_id must be UUID v4, got %q (err=%v)", newID, err)
	}
	if newID == oldID || repo.accounts[1].Extra[openAIPinnedInstallationIDKey] != newID {
		t.Fatalf("regenerated installation_id was not persisted: old=%q new=%q stored=%v", oldID, newID, repo.accounts[1].Extra[openAIPinnedInstallationIDKey])
	}

	for _, id := range []int64{2, 3, 4, 99} {
		if _, err := svc.RegenerateOpenAIInstallationID(context.Background(), id); err == nil {
			t.Fatalf("account %d should reject installation_id regeneration", id)
		}
	}
}
