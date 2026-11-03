package securityaudit

import (
	"context"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/gin-gonic/gin"
	"github.com/lib/pq"
)

// KeywordWhitelistIdentity is display-only metadata. Names are resolved from
// current identities and never stored in the policy or used for exemptions.
type KeywordWhitelistIdentity struct {
	UserID    int64  `json:"user_id"`
	APIKeyID  int64  `json:"api_key_id"`
	UserEmail string `json:"user_email"`
	Username  string `json:"username"`
	KeyName   string `json:"key_name"`
	Available bool   `json:"available"`
}

type KeywordWhitelistIdentityRequest struct {
	Keys []KeywordWhitelistKey `json:"keys"`
}

type KeywordWhitelistIdentityResponse struct {
	Items []KeywordWhitelistIdentity `json:"items"`
}

func (h *PromptAdminHandler) ResolveKeywordWhitelistIdentities(c *gin.Context) {
	var request KeywordWhitelistIdentityRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		response.ErrorFrom(c, infraerrors.BadRequest("prompt_audit_invalid_keyword_whitelist_key", "关键词拦截白名单身份查询请求无效"))
		return
	}
	if err := validateKeywordWhitelistKeys(request.Keys); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	items, err := h.service.ResolveKeywordWhitelistIdentities(c.Request.Context(), request.Keys)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, KeywordWhitelistIdentityResponse{Items: items})
}

func (s *PromptService) ResolveKeywordWhitelistIdentities(ctx context.Context, keys []KeywordWhitelistKey) ([]KeywordWhitelistIdentity, error) {
	if err := validateKeywordWhitelistKeys(keys); err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		return []KeywordWhitelistIdentity{}, nil
	}
	if s == nil || s.repo == nil || s.repo.db == nil {
		return nil, infraerrors.ServiceUnavailable("prompt_audit_keyword_identities_unavailable", "关键词白名单名称暂时无法加载")
	}
	return s.repo.ResolveKeywordWhitelistIdentities(ctx, keys)
}

func (r *PostgreSQLRepository) ResolveKeywordWhitelistIdentities(ctx context.Context, keys []KeywordWhitelistKey) ([]KeywordWhitelistIdentity, error) {
	if err := validateKeywordWhitelistKeys(keys); err != nil {
		return nil, err
	}
	keys = canonicalKeywordWhitelistKeys(keys)
	items := make([]KeywordWhitelistIdentity, len(keys))
	if len(keys) == 0 {
		return items, nil
	}
	userIDs := make([]int64, len(keys))
	keyIDs := make([]int64, len(keys))
	for index, key := range keys {
		items[index] = KeywordWhitelistIdentity{UserID: key.UserID, APIKeyID: key.APIKeyID}
		userIDs[index], keyIDs[index] = key.UserID, key.APIKeyID
	}
	// Select metadata only, for exact current owner/key pairs. Key material is
	// deliberately absent from both the query and the public result type.
	rows, err := r.db.QueryContext(ctx, `
		SELECT k.user_id, k.id, COALESCE(u.email, ''), COALESCE(u.username, ''), COALESCE(k.name, '')
		FROM unnest($1::bigint[], $2::bigint[]) AS wanted(user_id, api_key_id)
		JOIN users u ON u.id = wanted.user_id AND u.deleted_at IS NULL
		JOIN api_keys k ON k.id = wanted.api_key_id AND k.user_id = u.id AND k.deleted_at IS NULL`,
		pq.Array(userIDs), pq.Array(keyIDs))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	resolved := make(map[KeywordWhitelistKey]KeywordWhitelistIdentity, len(keys))
	for rows.Next() {
		var item KeywordWhitelistIdentity
		if err := rows.Scan(&item.UserID, &item.APIKeyID, &item.UserEmail, &item.Username, &item.KeyName); err != nil {
			return nil, err
		}
		item.Available = true
		resolved[KeywordWhitelistKey{UserID: item.UserID, APIKeyID: item.APIKeyID}] = item
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for index, key := range keys {
		if item, exists := resolved[key]; exists {
			items[index] = item
		}
	}
	return items, nil
}
