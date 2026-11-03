package securityaudit

import (
	"context"
	"encoding/json"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestKeywordWhitelistSaveValidatesOwnershipAndReloads(t *testing.T) {
	db := openPromptAuditIntegrationDB(t)
	ctx := context.Background()
	_, err := db.ExecContext(ctx, `
		INSERT INTO users(id, deleted_at) VALUES (1,NULL), (2,NULL), (3,NOW());
		INSERT INTO api_keys(id, user_id, deleted_at) VALUES
			(11,1,NULL), (12,1,NULL), (21,2,NULL), (31,3,NULL), (13,1,NOW());
	`)
	require.NoError(t, err)
	settings := postgresPromptAuditSettingRepository{db: db}
	require.NoError(t, settings.Set(ctx, SettingKeyRiskControl, "true"))
	manager := NewConfigManager(db, settings, nil, promptAuditTestEncryptor(t), testTotpKeyConfig())
	req := promptAuditUpdateRequest(1, 1, "")
	req.KeywordBlockingEnabled = true
	req.BlockedKeywords = []string{"blocked"}
	pairs := []KeywordWhitelistKey{{UserID: 1, APIKeyID: 11}, {UserID: 1, APIKeyID: 11}}
	req.KeywordWhitelistKeys = &pairs
	saved, err := manager.Save(ctx, req, 1)
	require.NoError(t, err)
	require.Equal(t, int64(2), saved.ConfigVersion)
	require.Equal(t, []KeywordWhitelistKey{{UserID: 1, APIKeyID: 11}}, saved.KeywordWhitelistKeys)
	raw, err := settings.GetValue(ctx, SettingKeyPromptAuditConfig)
	require.NoError(t, err)
	require.Contains(t, raw, `"keyword_whitelist_keys":[{"user_id":1,"api_key_id":11}]`)
	second := NewConfigManager(db, settings, nil, promptAuditTestEncryptor(t), testTotpKeyConfig())
	require.NoError(t, second.Reload(ctx))
	active, ok := second.Active()
	require.True(t, ok)
	require.True(t, active.KeywordWhitelistsKey(1, 11))
	require.False(t, active.KeywordWhitelistsKey(1, 12))
	require.False(t, active.KeywordWhitelistsKey(2, 11))
	for _, tc := range []struct {
		name string
		pair KeywordWhitelistKey
	}{
		{"wrong owner", KeywordWhitelistKey{UserID: 2, APIKeyID: 11}},
		{"missing key", KeywordWhitelistKey{UserID: 1, APIKeyID: 99}},
		{"deleted key", KeywordWhitelistKey{UserID: 1, APIKeyID: 13}},
		{"deleted user", KeywordWhitelistKey{UserID: 3, APIKeyID: 31}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := req
			bad.ExpectedConfigVersion = 2
			items := []KeywordWhitelistKey{tc.pair}
			bad.KeywordWhitelistKeys = &items
			_, err := manager.Save(ctx, bad, 1)
			require.Equal(t, "prompt_audit_invalid_keyword_whitelist_key", infraerrors.Reason(err))
			after, err := settings.GetValue(ctx, SettingKeyPromptAuditConfig)
			require.NoError(t, err)
			require.Equal(t, raw, after, "invalid ownership rolls back the entire config save")
		})
	}
	req.ExpectedConfigVersion = 2
	req.KeywordWhitelistKeys = nil
	saved, err = manager.Save(ctx, req, 1)
	require.NoError(t, err)
	require.Len(t, saved.KeywordWhitelistKeys, 1, "omitted config preserves existing exact-key exemption")
	req.ExpectedConfigVersion = 3
	require.NoError(t, json.Unmarshal([]byte(`{"keyword_whitelist_keys":null}`), &req))
	saved, err = manager.Save(ctx, req, 1)
	require.NoError(t, err)
	require.Len(t, saved.KeywordWhitelistKeys, 1)
	req.ExpectedConfigVersion = 4
	require.NoError(t, json.Unmarshal([]byte(`{"keyword_whitelist_keys":[]}`), &req))
	saved, err = manager.Save(ctx, req, 1)
	require.NoError(t, err)
	require.Empty(t, saved.KeywordWhitelistKeys)
	require.NoError(t, second.Reload(ctx))
	active, ok = second.Active()
	require.True(t, ok)
	require.False(t, active.KeywordWhitelistsKey(1, 11))
	service := &PromptService{config: second}
	decision, err := service.CheckKeyword(ctx, Request{UserID: 1, APIKeyID: 11, Protocol: "openai_responses", Body: []byte(`{"input":"blocked"}`)})
	require.NoError(t, err)
	require.Equal(t, ErrorCodeKeywordBlocked, decision.ErrorCode)
}
