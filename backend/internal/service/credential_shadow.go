package service

import (
	"context"
	"fmt"
)

// resolveCredentialAccount 解析影子账号到其母账号，用于凭据/Token 透传。
// - 普通账号（非影子）：直接返回自身。
// - 影子账号：通过 repo 取母账号，校验母账号存在且为 OpenAI OAuth 类型，否则返回错误。
// 设计为包级函数（非任何 service 的方法），以便 OpenAIGatewayService / OpenAIQuotaService /
// AccountUsageService 等不同接收者共享同一实现。
func resolveCredentialAccount(ctx context.Context, repo AccountRepository, account *Account) (*Account, error) {
	if account == nil {
		return nil, nil
	}
	owner := account
	if account.IsShadow() {
		if repo == nil {
			return nil, ErrOpenAIOAuthOSUnauthorized
		}
		parent, err := repo.GetByID(ctx, *account.ParentAccountID)
		if err != nil {
			return nil, fmt.Errorf("resolve spark shadow parent %d: %w", *account.ParentAccountID, err)
		}
		owner, err = credentialAccountFromParent(account, parent)
		if err != nil {
			return nil, err
		}
	}
	if !IsOpenAIOAuthOSProfileOwner(owner) {
		return owner, nil
	}
	os := account.OpenAIOAuthCredentialOS
	if os == "" {
		os = OpenAIRequestOSFromContext(ctx).Family
	}
	return ResolveOpenAIOAuthCredentialAccount(ctx, repo, owner, os)
}

// credentialAccountFromParent shares the same one-level parent validation with
// read-only batch callers that have already loaded all requested parents.
func credentialAccountFromParent(account, parent *Account) (*Account, error) {
	if account == nil || !account.IsShadow() {
		return account, nil
	}
	if parent == nil {
		return nil, fmt.Errorf("spark shadow parent %d not found", *account.ParentAccountID)
	}
	// 防御:创建路径已禁二级影子(G6),此处再挡一层——畸形数据/手工 DB 写出的影子→影子链
	// 会让凭据解析停在无凭据的一级影子(只解一层),fail-closed 比静默返回坏母更安全(外审第6轮)。
	if parent.IsShadow() {
		return nil, fmt.Errorf("spark shadow parent %d is itself a shadow", parent.ID)
	}
	if !parent.IsOpenAIOAuth() {
		return nil, fmt.Errorf("spark shadow parent %d is not OpenAI OAuth", parent.ID)
	}
	if account.OpenAIOAuthCredentialOS != "" {
		copy := *parent
		copy.OpenAIOAuthCredentialOS = account.OpenAIOAuthCredentialOS
		copy.OpenAIOAuthCredentialOwnerID = account.OpenAIOAuthCredentialOwnerID
		copy.OpenAIOAuthAuthorizationGeneration = account.OpenAIOAuthAuthorizationGeneration
		copy.OpenAIOAuthCredentialRevision = account.OpenAIOAuthCredentialRevision
		parent = &copy
	}
	return parent, nil
}

// OpenAIOAuthTokenAccountSnapshot attaches the exact token attempt to a request's
// business account. It preserves Spark billing/limits and performs no reread.
func OpenAIOAuthTokenAccountSnapshot(business, credentials *Account) (*Account, error) {
	if business == nil || credentials == nil || credentials.OpenAIOAuthCredentialOS == "" {
		return nil, ErrOpenAIOAuthOSUnauthorized
	}
	ownerID := business.ID
	if business.IsShadow() {
		ownerID = *business.ParentAccountID
	}
	if ownerID != credentials.OpenAIOAuthCredentialOwnerID ||
		business.OpenAIOAuthCredentialOS != "" && business.OpenAIOAuthCredentialOS != credentials.OpenAIOAuthCredentialOS ||
		business.OpenAIOAuthAuthorizationGeneration != "" && business.OpenAIOAuthAuthorizationGeneration != credentials.OpenAIOAuthAuthorizationGeneration {
		return nil, ErrOpenAIOAuthOSAuthorizationChanged
	}
	out := *business
	out.Credentials = PreserveOpenAIOAuthProviderCredentials(credentials.Credentials, business.Credentials)
	out.Credentials["user_agent"] = credentials.GetOpenAIUserAgent()
	out.Extra = shallowCopyMap(business.Extra)
	if out.Extra == nil {
		out.Extra = make(map[string]any)
	}
	for _, key := range []string{openAIPinnedInstallationIDKey, "codex_turn_state_generation", "codex_turn_state_credential_epoch"} {
		if value, exists := credentials.Extra[key]; exists {
			out.Extra[key] = value
		}
	}
	out.OpenAIOAuthOSProfiles = CloneOpenAIOAuthOSProfiles(credentials.OpenAIOAuthOSProfiles)
	out.OpenAIOAuthCredentialOS = credentials.OpenAIOAuthCredentialOS
	out.OpenAIOAuthCredentialOwnerID = credentials.OpenAIOAuthCredentialOwnerID
	out.OpenAIOAuthAuthorizationGeneration = credentials.OpenAIOAuthAuthorizationGeneration
	out.OpenAIOAuthCredentialRevision = credentials.OpenAIOAuthCredentialRevision
	out.OpenAIOAuthCredentialStateGeneration = credentials.OpenAIOAuthCredentialStateGeneration
	out.OpenAIOAuthCredentialEpoch = credentials.OpenAIOAuthCredentialEpoch
	return &out, nil
}
