package securityaudit

import (
	"context"
	"database/sql"
	"sort"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/lib/pq"
)

const MaxKeywordWhitelistKeys = 1000

// KeywordWhitelistKey exempts one authenticated key of one specific owner from
// local keyword matching. It never exempts the owner's other keys or Guard.
type KeywordWhitelistKey struct {
	UserID   int64 `json:"user_id"`
	APIKeyID int64 `json:"api_key_id"`
}

func canonicalKeywordWhitelistKeys(values []KeywordWhitelistKey) []KeywordWhitelistKey {
	result := make([]KeywordWhitelistKey, 0, len(values))
	seen := make(map[KeywordWhitelistKey]struct{}, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].UserID != result[j].UserID {
			return result[i].UserID < result[j].UserID
		}
		return result[i].APIKeyID < result[j].APIKeyID
	})
	return result
}

func validateKeywordWhitelistKeys(values []KeywordWhitelistKey) error {
	if len(values) > MaxKeywordWhitelistKeys {
		return infraerrors.BadRequest("prompt_audit_too_many_keyword_whitelist_keys", "关键词拦截白名单最多允许 1000 个 API Key")
	}
	for _, value := range values {
		if value.UserID <= 0 || value.APIKeyID <= 0 {
			return infraerrors.BadRequest("prompt_audit_invalid_keyword_whitelist_key", "关键词拦截白名单必须指定有效的用户及其 API Key")
		}
	}
	return nil
}

func validateKeywordWhitelistOwnership(ctx context.Context, tx *sql.Tx, values []KeywordWhitelistKey) error {
	if len(values) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(values))
	for _, value := range values {
		ids = append(ids, value.APIKeyID)
	}
	rows, err := tx.QueryContext(ctx, `SELECT k.id, k.user_id FROM api_keys k
		JOIN users u ON u.id = k.user_id
		WHERE k.id = ANY($1) AND k.deleted_at IS NULL AND u.deleted_at IS NULL`, pq.Array(ids))
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	owners := make(map[int64]int64, len(values))
	for rows.Next() {
		var keyID, userID int64
		if err := rows.Scan(&keyID, &userID); err != nil {
			return err
		}
		owners[keyID] = userID
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, value := range values {
		if owners[value.APIKeyID] != value.UserID {
			return infraerrors.BadRequest("prompt_audit_invalid_keyword_whitelist_key", "关键词拦截白名单中的 API Key 不存在、已删除或不属于所选用户")
		}
	}
	return nil
}

func (cfg ActiveConfig) KeywordWhitelistsKey(userID, apiKeyID int64) bool {
	if userID <= 0 || apiKeyID <= 0 {
		return false
	}
	for _, value := range cfg.KeywordWhitelistKeys {
		if value.UserID == userID && value.APIKeyID == apiKeyID {
			return true
		}
	}
	return false
}
