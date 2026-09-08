package service

import (
	"context"
	"fmt"
	"reflect"
	"time"
)

// OpenAIOAuthRefreshCredentialsRepository applies only provider-owned credential
// changes, without replacing concurrently edited routing or quota configuration.
type OpenAIOAuthRefreshCredentialsRepository interface {
	PatchOpenAIOAuthCredentialsIfUnchanged(
		ctx context.Context,
		id int64,
		expectedAuth map[string]any,
		expectedProxyID *int64,
		patch map[string]any,
		removedKeys []string,
	) (bool, error)
}

// These are the fields produced by BuildAccountCredentials and PAT normalization.
var openAIRefreshCredentialKeys = [...]string{
	"access_token", "refresh_token", "id_token", "expires_at", "expires_in",
	"email", "chatgpt_account_id", "chatgpt_user_id", "organization_id",
	"plan_type", "subscription_expires_at", "client_id", "token_type",
	"chatgpt_account_is_fedramp", openAIAuthModeCredentialKey,
	openAIAuthModeLegacyCredentialKey, "_token_version",
}

var openAIRefreshAuthIdentityKeys = [...]string{
	"access_token", "refresh_token", "id_token", "client_id",
	openAIAuthModeCredentialKey, openAIAuthModeLegacyCredentialKey, "_token_version",
	"chatgpt_account_id", "chatgpt_user_id", "organization_id",
}

func openAIRefreshAuthIdentity(credentials map[string]any) map[string]any {
	identity := make(map[string]any, len(openAIRefreshAuthIdentityKeys))
	for _, key := range openAIRefreshAuthIdentityKeys {
		identity[key] = credentials[key]
	}
	return identity
}

func openAIRefreshCredentialPatch(previous, next map[string]any) (map[string]any, []string) {
	patch := make(map[string]any)
	removed := make([]string, 0)
	for _, key := range openAIRefreshCredentialKeys {
		oldValue, oldExists := previous[key]
		newValue, newExists := next[key]
		if !newExists {
			if oldExists {
				removed = append(removed, key)
			}
		} else if !oldExists || !reflect.DeepEqual(oldValue, newValue) {
			patch[key] = newValue
		}
	}
	return patch, removed
}

func persistOpenAIOAuthRefreshCredentials(ctx context.Context, repo AccountRepository, expected *Account, credentials map[string]any) (*Account, bool, error) {
	if expected == nil || expected.IsCredentialShadow() {
		return expected, false, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	updater, ok := repo.(OpenAIOAuthRefreshCredentialsRepository)
	if !ok {
		return nil, false, &providerConfigurationRefreshError{
			err: fmt.Errorf("OpenAI OAuth refresh credential patch repository is not configured"),
		}
	}
	patch, removed := openAIRefreshCredentialPatch(expected.Credentials, credentials)
	applied, err := updater.PatchOpenAIOAuthCredentialsIfUnchanged(
		ctx, expected.ID, openAIRefreshAuthIdentity(expected.Credentials), expected.ProxyID, patch, removed,
	)
	if err != nil {
		return nil, false, &providerCycleContainmentRefreshError{
			err: fmt.Errorf("%w: %v", errOAuthRefreshCredentialPersist, err),
		}
	}

	// Both a winning patch and a lost reauthorization race must return the durable
	// row, otherwise background cache publication can restore stale model mapping.
	readCtx := ctx
	if applied {
		var cancel context.CancelFunc
		readCtx, cancel = context.WithTimeout(context.WithoutCancel(ctx), defaultRefreshPostPersistCleanupTimeout)
		defer cancel()
	}
	account, err := repo.GetByID(readCtx, expected.ID)
	if err != nil || account == nil {
		if err == nil {
			err = ErrAccountNotFound
		}
		return nil, applied, &providerCycleContainmentRefreshError{
			err: fmt.Errorf("OpenAI OAuth refresh durable account state is unavailable: %w", err),
		}
	}
	return account, applied, nil
}

func (s *adminServiceImpl) PersistOpenAIOAuthRefreshCredentials(ctx context.Context, expected *Account, credentials map[string]any) (*Account, bool, error) {
	if expected == nil || !expected.IsOpenAIOAuth() || expected.IsCredentialShadow() {
		return nil, false, fmt.Errorf("OpenAI OAuth refresh requires a non-shadow OpenAI OAuth account")
	}
	next := shallowCopyMap(credentials)
	if next == nil {
		return nil, false, fmt.Errorf("OpenAI OAuth refresh returned no credentials")
	}
	next["_token_version"] = time.Now().UnixMilli()
	return persistOpenAIOAuthRefreshCredentials(ctx, s.accountRepo, expected, next)
}
