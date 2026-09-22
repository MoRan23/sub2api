package service

import "strconv"

// OpenAITokenCacheKey shares a grant across OS identities, while fencing cache
// fills from earlier authorizations and credential revisions.
func OpenAITokenCacheKey(account *Account) string {
	key := OpenAITokenRefreshLockKey(account)
	if account != nil && account.OpenAIOAuthAuthorizationGeneration != "" {
		key += ":revision:" + strconv.FormatInt(account.OpenAIOAuthCredentialRevision, 10)
	}
	return key
}

// OpenAITokenRefreshLockKey serializes every OS identity and revision of the
// same grant. Reauthorization creates a separate lock from an old in-flight grant.
func OpenAITokenRefreshLockKey(account *Account) string {
	key := openAITokenOwnerKey(account)
	if account != nil && account.OpenAIOAuthAuthorizationGeneration != "" {
		key += ":auth:" + account.OpenAIOAuthAuthorizationGeneration
	}
	return key
}

func openAITokenOwnerKey(account *Account) string {
	if account == nil {
		return "openai:account:0"
	}
	id := account.ID
	if account.OpenAIOAuthCredentialOwnerID > 0 {
		id = account.OpenAIOAuthCredentialOwnerID
	}
	return "openai:account:" + strconv.FormatInt(id, 10)
}

// ClaudeTokenCacheKey 生成 Claude (Anthropic) OAuth 账号的缓存键
// 格式: "claude:account:{account_id}"
func ClaudeTokenCacheKey(account *Account) string {
	return "claude:account:" + strconv.FormatInt(account.ID, 10)
}
