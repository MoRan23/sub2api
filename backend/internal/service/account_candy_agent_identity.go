package service

import (
	"context"
	"strings"
)

// Initial task registration is permitted, but cannot overwrite an administrator's
// replacement signing identity while the registration HTTP request is in flight.
func persistCandyAgentIdentityTask(ctx context.Context, repo AccountRepository, account *Account, taskID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	updater, ok := repo.(OpenAIOAuthRefreshCredentialsRepository)
	if !ok {
		return candyTestError("authorization_update_unavailable")
	}
	expected := openAIRefreshAuthIdentity(account.Credentials)
	for _, key := range []string{"agent_private_key", "agent_runtime_id", "task_id"} {
		expected[key] = account.Credentials[key]
	}
	_, err := updater.PatchOpenAIOAuthCredentialsIfUnchanged(ctx, account.ID, expected, account.ProxyID, map[string]any{"task_id": taskID}, nil)
	if err != nil {
		return candyTestError("authorization_update_failed")
	}
	current, err := repo.GetByID(ctx, account.ID)
	if err != nil || !sameCandyAuthorization(account, current) || !sameCandyStaticAuthorization(account, current) || strings.TrimSpace(current.GetCredential("task_id")) == "" {
		return candyTestError("authorization_changed")
	}
	account.Credentials = shallowCopyMap(current.Credentials)
	return nil
}
