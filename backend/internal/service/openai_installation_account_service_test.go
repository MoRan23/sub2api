package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

type openAIInstallationAccountServiceRepoStub struct {
	AccountRepository
	account *Account
}

func (r *openAIInstallationAccountServiceRepoStub) Create(_ context.Context, account *Account) error {
	account.ID = 1
	r.account = account
	return nil
}

func (r *openAIInstallationAccountServiceRepoStub) GetByID(_ context.Context, id int64) (*Account, error) {
	if r.account == nil || r.account.ID != id {
		return nil, ErrAccountNotFound
	}
	return r.account, nil
}

func (r *openAIInstallationAccountServiceRepoStub) Update(_ context.Context, account *Account) error {
	r.account = account
	return nil
}

func TestAccountServiceCreateSetupTokenOwnsInstallationPin(t *testing.T) {
	repo := &openAIInstallationAccountServiceRepoStub{}
	account, err := NewAccountService(repo, nil).Create(context.Background(), CreateAccountRequest{
		Name:     "setup-token",
		Platform: PlatformOpenAI,
		Type:     AccountTypeSetupToken,
		Extra: map[string]any{
			openAIPinnedInstallationIDKey:      "client-supplied",
			openAIInstallationRotateEnabledKey: true,
		},
	})
	if err != nil {
		t.Fatalf("create SetupToken account: %v", err)
	}
	pinned, _ := account.Extra[openAIPinnedInstallationIDKey].(string)
	parsed, parseErr := uuid.Parse(pinned)
	if parseErr != nil || parsed.Version() != 4 {
		t.Fatalf("SetupToken installation_id must be UUID v4, got %q (err=%v)", pinned, parseErr)
	}
	if account.Extra[openAIInstallationPinEnabledKey] != true {
		t.Fatalf("SetupToken pin must default enabled: %#v", account.Extra)
	}
}

func TestAccountServiceCreateForeignOAuthStripsOpenAIInstallationPin(t *testing.T) {
	repo := &openAIInstallationAccountServiceRepoStub{}
	account, err := NewAccountService(repo, nil).Create(context.Background(), CreateAccountRequest{
		Name:     "foreign-oauth",
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			openAIInstallationPinEnabledKey:    true,
			openAIPinnedInstallationIDKey:      uuid.NewString(),
			openAIInstallationRotateEnabledKey: true,
		},
	})
	if err != nil {
		t.Fatalf("create foreign OAuth account: %v", err)
	}
	for _, key := range []string{openAIPinnedInstallationIDKey, openAIInstallationPinEnabledKey, openAIInstallationRotateEnabledKey} {
		if _, exists := account.Extra[key]; exists {
			t.Fatalf("foreign OAuth account retained %s: %#v", key, account.Extra)
		}
	}
}

func TestAccountServiceUpdatePreservesSetupTokenPinAndStripsForeignOAuth(t *testing.T) {
	pinned := uuid.NewString()
	repo := &openAIInstallationAccountServiceRepoStub{account: &Account{
		ID:       1,
		Platform: PlatformOpenAI,
		Type:     AccountTypeSetupToken,
		Extra: map[string]any{
			openAIPinnedInstallationIDKey: pinned,
		},
	}}
	svc := NewAccountService(repo, nil)
	extra := map[string]any{
		"custom":                           "new",
		openAIPinnedInstallationIDKey:      "client-supplied",
		openAIInstallationRotateEnabledKey: true,
	}
	updated, err := svc.Update(context.Background(), 1, UpdateAccountRequest{Extra: &extra})
	if err != nil {
		t.Fatalf("update SetupToken account: %v", err)
	}
	if updated.Extra[openAIPinnedInstallationIDKey] != pinned {
		t.Fatalf("SetupToken pin was not preserved: %#v", updated.Extra)
	}
	if _, exists := updated.Extra[openAIInstallationRotateEnabledKey]; exists {
		t.Fatalf("legacy rotation key was retained: %#v", updated.Extra)
	}

	repo.account = &Account{
		ID:       2,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			openAIPinnedInstallationIDKey:   uuid.NewString(),
			openAIInstallationPinEnabledKey: true,
		},
	}
	foreignExtra := map[string]any{"custom": "new"}
	foreign, err := svc.Update(context.Background(), 2, UpdateAccountRequest{Extra: &foreignExtra})
	if err != nil {
		t.Fatalf("update foreign OAuth account: %v", err)
	}
	for _, key := range []string{openAIPinnedInstallationIDKey, openAIInstallationPinEnabledKey, openAIInstallationRotateEnabledKey} {
		if _, exists := foreign.Extra[key]; exists {
			t.Fatalf("foreign OAuth account retained %s: %#v", key, foreign.Extra)
		}
	}
}
