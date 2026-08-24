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

func (r *openAIInstallationAdminRepoStub) Update(_ context.Context, account *Account) error {
	if account == nil || r.accounts[account.ID] == nil {
		return ErrAccountNotFound
	}
	r.accounts[account.ID] = account
	return nil
}

func TestRegenerateOpenAIInstallationIDRequiresSavedFixedOAuthParent(t *testing.T) {
	oldID := uuid.NewString()
	setupTokenOldID := uuid.NewString()
	shadowParentID := int64(1)
	repo := &openAIInstallationAdminRepoStub{accounts: map[int64]*Account{
		1: {ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{
			openAIPinnedInstallationIDKey: oldID,
		}},
		2: {ID: 2, Platform: PlatformOpenAI, Type: AccountTypeOAuth, ParentAccountID: &shadowParentID},
		3: {ID: 3, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{openAIInstallationPinEnabledKey: false}},
		4: {ID: 4, Platform: PlatformOpenAI, Type: AccountTypeAPIKey},
		5: {ID: 5, Platform: PlatformOpenAI, Type: AccountTypeSetupToken, Extra: map[string]any{
			openAIPinnedInstallationIDKey: setupTokenOldID,
		}},
		6: {ID: 6, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Extra: map[string]any{
			openAIPinnedInstallationIDKey: uuid.NewString(),
		}},
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
	setupTokenNewID, err := svc.RegenerateOpenAIInstallationID(context.Background(), 5)
	if err != nil {
		t.Fatalf("regenerate SetupToken installation_id: %v", err)
	}
	if setupTokenNewID == setupTokenOldID || repo.accounts[5].Extra[openAIPinnedInstallationIDKey] != setupTokenNewID {
		t.Fatalf("SetupToken installation_id was not regenerated: old=%q new=%q stored=%v", setupTokenOldID, setupTokenNewID, repo.accounts[5].Extra[openAIPinnedInstallationIDKey])
	}

	for _, id := range []int64{2, 3, 4, 6, 99} {
		if _, err := svc.RegenerateOpenAIInstallationID(context.Background(), id); err == nil {
			t.Fatalf("account %d should reject installation_id regeneration", id)
		}
	}
}

func TestUpdateAccountPreservesSetupTokenInstallationPinAndStripsForeignOAuth(t *testing.T) {
	setupTokenID := uuid.NewString()
	repo := &openAIInstallationAdminRepoStub{accounts: map[int64]*Account{
		10: {
			ID:       10,
			Platform: PlatformOpenAI,
			Type:     AccountTypeSetupToken,
			Extra: map[string]any{
				openAIPinnedInstallationIDKey:   setupTokenID,
				openAIInstallationPinEnabledKey: false,
			},
		},
		11: {
			ID:       11,
			Platform: PlatformAnthropic,
			Type:     AccountTypeOAuth,
			Extra: map[string]any{
				openAIPinnedInstallationIDKey:   uuid.NewString(),
				openAIInstallationPinEnabledKey: true,
			},
		},
	}}
	svc := &adminServiceImpl{accountRepo: repo}

	updated, err := svc.UpdateAccount(context.Background(), 10, &UpdateAccountInput{Extra: map[string]any{
		"custom":                           "new",
		openAIPinnedInstallationIDKey:      "client-supplied",
		openAIInstallationRotateEnabledKey: true,
	}})
	if err != nil {
		t.Fatalf("update SetupToken account: %v", err)
	}
	if updated.Extra[openAIPinnedInstallationIDKey] != setupTokenID || updated.Extra[openAIInstallationPinEnabledKey] != false {
		t.Fatalf("SetupToken pin state was not preserved: %#v", updated.Extra)
	}
	if _, exists := updated.Extra[openAIInstallationRotateEnabledKey]; exists {
		t.Fatalf("legacy rotation key was retained: %#v", updated.Extra)
	}

	foreign, err := svc.UpdateAccount(context.Background(), 11, &UpdateAccountInput{Extra: map[string]any{"custom": "new"}})
	if err != nil {
		t.Fatalf("update foreign OAuth account: %v", err)
	}
	for _, key := range []string{openAIPinnedInstallationIDKey, openAIInstallationPinEnabledKey, openAIInstallationRotateEnabledKey} {
		if _, exists := foreign.Extra[key]; exists {
			t.Fatalf("foreign OAuth account retained %s: %#v", key, foreign.Extra)
		}
	}
}
