package service

import (
	"context"
	"fmt"
	"time"
)

// OpenAIOAuthAccountStateSnapshot identifies the credentials used by a request.
// It is a concurrency guard, not a second account authorization policy.
type OpenAIOAuthAccountStateSnapshot struct {
	OwnerAccountID          int64
	AuthorizationGeneration string
	CredentialRevision      int64
}

type OpenAIOAuthAccountStateChangeKind string

const (
	OpenAIOAuthAccountStateError         OpenAIOAuthAccountStateChangeKind = "error"
	OpenAIOAuthAccountStateClearError    OpenAIOAuthAccountStateChangeKind = "clear_error"
	OpenAIOAuthAccountStateCooldown      OpenAIOAuthAccountStateChangeKind = "cooldown"
	OpenAIOAuthAccountStateModelCooldown OpenAIOAuthAccountStateChangeKind = "model_cooldown"
	OpenAIOAuthAccountStateRecover       OpenAIOAuthAccountStateChangeKind = "recover"
	OpenAIOAuthAccountStateClearTemp     OpenAIOAuthAccountStateChangeKind = "clear_temp"
)

type OpenAIOAuthAccountStateChange struct {
	Kind         OpenAIOAuthAccountStateChangeKind
	ErrorMessage string
	Until        time.Time
	Reason       string
	ModelKey     string
	AuthFailure  bool
}

type OpenAIOAuthAccountStateResult struct {
	Applied          bool
	ClearedError     bool
	ClearedRateLimit bool
}

type OpenAIOAuthAccountStateRepository interface {
	MutateOpenAIOAuthAccountStateIfUnchanged(context.Context, int64, OpenAIOAuthAccountStateSnapshot, OpenAIOAuthAccountStateChange) (*OpenAIOAuthAccountStateResult, error)
}

func openAIOAuthAccountStateSnapshot(account *Account) (OpenAIOAuthAccountStateSnapshot, bool) {
	if account == nil || account.OpenAIOAuthAuthorizationGeneration == "" || account.OpenAIOAuthCredentialRevision <= 0 || account.OpenAIOAuthCredentialOwnerID <= 0 {
		return OpenAIOAuthAccountStateSnapshot{}, false
	}
	return OpenAIOAuthAccountStateSnapshot{
		OwnerAccountID:          account.OpenAIOAuthCredentialOwnerID,
		AuthorizationGeneration: account.OpenAIOAuthAuthorizationGeneration,
		CredentialRevision:      account.OpenAIOAuthCredentialRevision,
	}, true
}

func mutateOpenAIOAuthAccountState(ctx context.Context, repo AccountRepository, account *Account, change OpenAIOAuthAccountStateChange) (*OpenAIOAuthAccountStateResult, bool, error) {
	snapshot, scoped := openAIOAuthAccountStateSnapshot(account)
	if !scoped {
		return nil, false, nil
	}
	writer, ok := repo.(OpenAIOAuthAccountStateRepository)
	if !ok {
		return nil, true, fmt.Errorf("OpenAI OAuth account state repository is not configured")
	}
	result, err := writer.MutateOpenAIOAuthAccountStateIfUnchanged(ctx, account.ID, snapshot, change)
	return result, true, err
}

func clearOpenAIOAuthTempUnschedulableIfUnchanged(ctx context.Context, repo AccountRepository, account *Account) (handled, applied bool, err error) {
	result, handled, err := mutateOpenAIOAuthAccountState(ctx, repo, account, OpenAIOAuthAccountStateChange{Kind: OpenAIOAuthAccountStateClearTemp})
	return handled, result != nil && result.Applied, err
}
