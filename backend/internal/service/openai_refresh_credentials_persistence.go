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
	if expected == nil || (expected.IsCredentialShadow() && expected.OpenAIOAuthCredentialOS == "") {
		return expected, false, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if expected.OpenAIOAuthCredentialOS == "" && IsOpenAIOAuthOSProfileOwner(expected) {
		if _, supported := repo.(OpenAIOAuthOSCredentialsReader); supported {
			scoped, err := ResolveOpenAIOAuthCredentialAccount(ctx, repo, expected, OpenAIRequestOSFromContext(ctx).Family)
			if err != nil {
				return nil, false, err
			}
			if !reflect.DeepEqual(openAIRefreshAuthIdentity(expected.Credentials), openAIRefreshAuthIdentity(scoped.Credentials)) {
				return nil, false, errOAuthRefreshAccountStateChanged
			}
			expected = scoped
		}
	}
	if expected.OpenAIOAuthCredentialOS != "" {
		updater, ok := repo.(OpenAIOAuthOSCredentialsRepository)
		if !ok {
			return nil, false, &providerConfigurationRefreshError{err: fmt.Errorf("OpenAI OAuth OS credential repository is not configured")}
		}
		patch, removed := openAIRefreshCredentialPatch(expected.Credentials, credentials)
		applied, err := updater.PatchOpenAIOAuthOSCredentialsIfUnchanged(ctx,
			expected.OpenAIOAuthCredentialOwnerID, expected.OpenAIOAuthCredentialOS,
			expected.OpenAIOAuthAuthorizationGeneration, expected.OpenAIOAuthCredentialRevision,
			expected.ProxyID, patch, removed)
		if err != nil {
			return nil, false, &providerCycleContainmentRefreshError{err: fmt.Errorf("%w: %v", errOAuthRefreshCredentialPersist, err)}
		}
		readCtx := ctx
		if applied {
			var cancel context.CancelFunc
			readCtx, cancel = context.WithTimeout(context.WithoutCancel(ctx), defaultRefreshPostPersistCleanupTimeout)
			defer cancel()
		}
		// Reload restores this OS and rejects a revoked/replaced authorization.
		// A fresh default mirror must never escape as the result of a slot refresh.
		account, err := ReloadOpenAIOAuthCredentialAccount(readCtx, repo, expected)
		if err != nil {
			return nil, applied, &providerCycleContainmentRefreshError{err: fmt.Errorf("OpenAI OAuth OS refresh durable state is unavailable: %w", err)}
		}
		return account, applied, nil
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

// A failure belongs to the authorization snapshot that actually reached the
// provider. The CAS makes late errors harmless after refresh or reauthorization.
func persistOpenAIOAuthCredentialError(ctx context.Context, repo AccountRepository, account *Account, reason string) (handled, applied bool, err error) {
	if account == nil || account.OpenAIOAuthCredentialOS == "" {
		return false, false, nil
	}
	updater, ok := repo.(OpenAIOAuthOSCredentialsRepository)
	if !ok {
		return true, false, fmt.Errorf("OpenAI OAuth OS credential repository is not configured")
	}
	applied, err = updater.SetOpenAIOAuthOSCredentialErrorIfUnchanged(ctx,
		account.OpenAIOAuthCredentialOwnerID, account.OpenAIOAuthCredentialOS,
		account.OpenAIOAuthAuthorizationGeneration, account.OpenAIOAuthCredentialRevision, reason)
	return true, applied, err
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
