package service

import (
	"context"
	"errors"
	"maps"
)

// Credential OS is an explicit transport scope, never inferred again from a
// response or mutable account default after a physical attempt was prepared.
func codexTurnStateOS(account *Account) string {
	if account == nil {
		return ""
	}
	return account.OpenAIOAuthCredentialOS
}

func codexTurnStateRequestOS(ctx context.Context, account *Account) string {
	if os := codexTurnStateOS(account); os != "" {
		return os
	}
	if selection, ok := openAIOAuthOSSelectionFromContext(ctx); ok {
		return selection.Profile.OSFamily
	}
	return ""
}

func (s *CodexTurnStateService) currentOwnerForAttempt(ctx context.Context, account *Account) (*Account, error) {
	return s.currentOwner(ctx, account.ID, codexTurnStateRequestOS(ctx, account))
}

func (s *CodexTurnStateService) codexTurnStateCredentialOwner(ctx context.Context, account *Account) (*Account, error) {
	if account == nil || !account.IsShadow() {
		return account, nil
	}
	parent, err := s.accounts.GetByID(ctx, *account.ParentAccountID)
	if err != nil {
		return nil, err
	}
	return credentialAccountFromParent(account, parent)
}

func (s *CodexTurnStateService) codexTurnStateStatusOwner(ctx context.Context, owner *Account, os string) (*Account, error) {
	projected, err := ResolveOpenAIOAuthCredentialAccount(ctx, s.accounts, owner, os)
	if err == nil || !errors.Is(err, ErrOpenAIOAuthOSUnauthorized) {
		return projected, err
	}
	// An unbound slot is a displayable state, not permission to inspect the
	// default slot's tokens, generation, or observations.
	copy := *owner
	copy.Credentials = map[string]any{}
	copy.Extra = maps.Clone(owner.Extra)
	delete(copy.Extra, CodexTurnStateGenerationExtraKey)
	delete(copy.Extra, CodexTurnStateCredentialEpochExtraKey)
	if os == "" && owner.OpenAIOAuthOSProfiles != nil {
		os = owner.OpenAIOAuthOSProfiles.DefaultOS
	}
	copy.OpenAIOAuthCredentialOS = os
	copy.OpenAIOAuthAuthorizationGeneration = ""
	copy.OpenAIOAuthCredentialStateGeneration = ""
	copy.OpenAIOAuthCredentialEpoch = ""
	copy.OpenAIOAuthCredentialRevision = 0
	return &copy, nil
}

// Reload each authorized OS independently when an account-wide configuration
// notification arrives. No empty slot may inherit another OS's credentials.
func (s *CodexTurnStateService) activateHistoryForAccount(ctx context.Context, accountID int64) {
	for _, os := range []string{"windows", "macos", "linux"} {
		owner, err := s.currentOwner(ctx, accountID, os)
		if err != nil || owner == nil {
			continue
		}
		generation := CodexTurnStateGenerationForAccount(owner)
		s.activateHistoryForOwner(ctx, owner, generation)
		if bus, ok := s.repo.(CodexTurnStateOSActivationRepository); ok {
			_ = bus.PublishOSActivation(ctx, owner.ID, os, generation)
		}
	}
}
