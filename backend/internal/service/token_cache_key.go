package service

import (
	"strconv"
	"strings"
)

// OpenAITokenCacheKey 生成 OpenAI OAuth 账号的缓存键
// 格式: "openai:account:{account_id}"
func OpenAITokenCacheKey(account *Account) string {
	key := OpenAITokenRefreshLockKey(account)
	if account != nil && account.OpenAIOAuthCredentialOS != "" {
		key += ":auth:" + account.OpenAIOAuthAuthorizationGeneration + ":revision:" + strconv.FormatInt(account.OpenAIOAuthCredentialRevision, 10)
	}
	return key
}

// Refresh locks survive normal token rotation. Cache entries also include the
// authorization and revision, so an in-flight old fill cannot revive a token.
func OpenAITokenRefreshLockKey(account *Account) string {
	if account == nil {
		return "openai:account:0"
	}
	id := account.ID
	if account.OpenAIOAuthCredentialOwnerID > 0 {
		id = account.OpenAIOAuthCredentialOwnerID
	}
	key := "openai:account:" + strconv.FormatInt(id, 10)
	if os := strings.TrimSpace(account.OpenAIOAuthCredentialOS); os != "" {
		key += ":os:" + os
	}
	return key
}

// ClaudeTokenCacheKey 生成 Claude (Anthropic) OAuth 账号的缓存键
// 格式: "claude:account:{account_id}"
func ClaudeTokenCacheKey(account *Account) string {
	return "claude:account:" + strconv.FormatInt(account.ID, 10)
}
