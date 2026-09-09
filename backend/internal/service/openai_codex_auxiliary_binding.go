package service

import (
	"context"
	"errors"
	"sync"
)

var (
	ErrCodexAuxiliaryAccountBindingInvalid          = errors.New("invalid Codex auxiliary account binding request")
	ErrCodexAuxiliaryAccountBindingStoreUnavailable = errors.New("Codex auxiliary account binding store unavailable")
	ErrCodexAuxiliaryAccountBindingStoredInvalid    = errors.New("invalid stored Codex auxiliary account binding")
)

type CodexAuxiliaryAccountBinding struct {
	AccountID int64
	// Reused means the previously bound account is still eligible.
	Reused bool
	// HadBinding also remains true when an ineligible old account was replaced.
	HadBinding bool
}

// CodexAuxiliaryAccountBindingStore keeps History/Notes account ownership separate
// from inference affinity. A binding has no idle expiry and changes only when its
// account is absent from the caller's eligible set. Store failures must be
// returned to the caller without creating a fallback binding.
type CodexAuxiliaryAccountBindingStore interface {
	ResolveCodexAuxiliaryAccountBinding(ctx context.Context, bindingKey string, eligibleAccountIDs []int64, preferredAccountID int64) (CodexAuxiliaryAccountBinding, error)
}

func ValidateCodexAuxiliaryAccountBindingRequest(bindingKey string, eligibleAccountIDs []int64, preferredAccountID int64) error {
	if len(bindingKey) != 64 || len(eligibleAccountIDs) == 0 || preferredAccountID < 0 {
		return ErrCodexAuxiliaryAccountBindingInvalid
	}
	for _, ch := range bindingKey {
		if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f')) {
			return ErrCodexAuxiliaryAccountBindingInvalid
		}
	}
	for _, accountID := range eligibleAccountIDs {
		if accountID <= 0 {
			return ErrCodexAuxiliaryAccountBindingInvalid
		}
	}
	return nil
}

// resolveLocalCodexAuxiliaryAccountBinding is only for services without a shared
// binding store. LoadOrStore and CompareAndSwap make initial binding and invalid
// account replacement single-winner operations across concurrent requests.
func resolveLocalCodexAuxiliaryAccountBinding(bindings *sync.Map, bindingKey string, eligibleAccountIDs []int64, preferredAccountID int64) (CodexAuxiliaryAccountBinding, error) {
	if err := ValidateCodexAuxiliaryAccountBindingRequest(bindingKey, eligibleAccountIDs, preferredAccountID); err != nil {
		return CodexAuxiliaryAccountBinding{}, err
	}
	if bindings == nil {
		return CodexAuxiliaryAccountBinding{}, ErrCodexAuxiliaryAccountBindingStoreUnavailable
	}
	eligible := make(map[int64]struct{}, len(eligibleAccountIDs))
	for _, accountID := range eligibleAccountIDs {
		eligible[accountID] = struct{}{}
	}
	proposed := eligibleAccountIDs[0]
	if _, ok := eligible[preferredAccountID]; ok {
		proposed = preferredAccountID
	}
	for {
		current, loaded := bindings.LoadOrStore(bindingKey, proposed)
		if !loaded {
			return CodexAuxiliaryAccountBinding{AccountID: proposed}, nil
		}
		accountID, ok := current.(int64)
		if !ok || accountID <= 0 {
			return CodexAuxiliaryAccountBinding{}, ErrCodexAuxiliaryAccountBindingStoredInvalid
		}
		if _, ok := eligible[accountID]; ok {
			return CodexAuxiliaryAccountBinding{AccountID: accountID, Reused: true, HadBinding: true}, nil
		}
		if bindings.CompareAndSwap(bindingKey, accountID, proposed) {
			return CodexAuxiliaryAccountBinding{AccountID: proposed, HadBinding: true}, nil
		}
	}
}
