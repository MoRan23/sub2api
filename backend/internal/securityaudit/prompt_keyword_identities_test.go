package securityaudit

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestKeywordWhitelistIdentityHandlerReturnsMetadata(t *testing.T) {
	keys := []KeywordWhitelistKey{{UserID: 2, APIKeyID: 1931}}
	items := []KeywordWhitelistIdentity{{UserID: 2, APIKeyID: 1931, UserEmail: "owner@example.test", Username: "Owner", KeyName: "normal", Available: true}}
	svc := &fakePromptAdminService{identities: func(_ context.Context, requested []KeywordWhitelistKey) ([]KeywordWhitelistIdentity, error) {
		require.Equal(t, keys, requested)
		return items, nil
	}}
	recorder := promptAdminRequest(t, promptAdminRouter(svc), http.MethodPost, "/admin/prompt-audit/keyword-whitelist-identities", KeywordWhitelistIdentityRequest{Keys: keys})
	require.Equal(t, http.StatusOK, recorder.Code)
	var envelope struct {
		Data KeywordWhitelistIdentityResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope))
	require.Equal(t, items, envelope.Data.Items)
	require.NotContains(t, recorder.Body.String(), `"key":`)
	require.NotContains(t, recorder.Body.String(), `"token":`)
}

func TestKeywordWhitelistIdentityHandlerRejectsInvalidRequests(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		body         any
	}{
		{name: "wrong ID type", reason: "prompt_audit_invalid_keyword_whitelist_key", body: map[string]any{"keys": []any{map[string]any{"user_id": "2", "api_key_id": 10}}}},
		{name: "fractional ID", reason: "prompt_audit_invalid_keyword_whitelist_key", body: map[string]any{"keys": []any{map[string]any{"user_id": 2, "api_key_id": 1.1}}}},
		{name: "missing owner", reason: "prompt_audit_invalid_keyword_whitelist_key", body: KeywordWhitelistIdentityRequest{Keys: []KeywordWhitelistKey{{APIKeyID: 10}}}},
		{name: "negative key", reason: "prompt_audit_invalid_keyword_whitelist_key", body: KeywordWhitelistIdentityRequest{Keys: []KeywordWhitelistKey{{UserID: 2, APIKeyID: -1}}}},
		{name: "too many", reason: "prompt_audit_too_many_keyword_whitelist_keys", body: KeywordWhitelistIdentityRequest{Keys: make([]KeywordWhitelistKey, MaxKeywordWhitelistKeys+1)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			svc := &fakePromptAdminService{identities: func(context.Context, []KeywordWhitelistKey) ([]KeywordWhitelistIdentity, error) {
				called = true
				return nil, nil
			}}
			recorder := promptAdminRequest(t, promptAdminRouter(svc), http.MethodPost, "/admin/prompt-audit/keyword-whitelist-identities", tc.body)
			require.Equal(t, http.StatusBadRequest, recorder.Code)
			require.Contains(t, recorder.Body.String(), tc.reason)
			require.False(t, called)
		})
	}
}

func TestKeywordWhitelistIdentityServiceEmptyAndUnavailable(t *testing.T) {
	svc := &PromptService{}
	items, err := svc.ResolveKeywordWhitelistIdentities(context.Background(), nil)
	require.NoError(t, err)
	require.NotNil(t, items)
	require.Empty(t, items)
	_, err = svc.ResolveKeywordWhitelistIdentities(context.Background(), []KeywordWhitelistKey{{UserID: 1, APIKeyID: 1}})
	require.Equal(t, "prompt_audit_keyword_identities_unavailable", infraerrors.Reason(err))
}

func TestKeywordWhitelistIdentitiesResolveOnlyCurrentOwnedMetadata(t *testing.T) {
	db := openPromptAuditIntegrationDB(t)
	ctx := context.Background()
	_, err := db.ExecContext(ctx, `
		INSERT INTO users(id,email,username,deleted_at) VALUES
			(1,'owner@example.test','Owner',NULL), (2,'other@example.test','Other',NULL), (3,'deleted@example.test','Deleted',NOW());
		INSERT INTO api_keys(id,user_id,name,deleted_at) VALUES
			(11,1,'normal',NULL), (12,1,NULL,NULL), (13,1,'deleted-key',NOW()), (21,2,'other-key',NULL), (31,3,'deleted-owner-key',NULL);
	`)
	require.NoError(t, err)
	svc := &PromptService{repo: NewPostgreSQLRepository(db)}
	keys := []KeywordWhitelistKey{
		{UserID: 1, APIKeyID: 11}, {UserID: 1, APIKeyID: 11}, {UserID: 1, APIKeyID: 12},
		{UserID: 1, APIKeyID: 13}, {UserID: 1, APIKeyID: 21}, {UserID: 3, APIKeyID: 31}, {UserID: 9, APIKeyID: 99},
	}
	items, err := svc.ResolveKeywordWhitelistIdentities(ctx, keys)
	require.NoError(t, err)
	require.Len(t, items, 6)
	require.Equal(t, KeywordWhitelistIdentity{UserID: 1, APIKeyID: 11, UserEmail: "owner@example.test", Username: "Owner", KeyName: "normal", Available: true}, items[0])
	require.True(t, items[1].Available, "an unnamed key is still an existing key")
	require.Empty(t, items[1].KeyName)
	for _, item := range items[2:] {
		require.False(t, item.Available)
		require.Empty(t, item.UserEmail)
		require.Empty(t, item.Username)
		require.Empty(t, item.KeyName)
	}
	_, err = db.ExecContext(ctx, `UPDATE users SET email='renamed@example.test',username='Renamed' WHERE id=1; UPDATE api_keys SET name='renamed-key' WHERE id=11`)
	require.NoError(t, err)
	items, err = svc.ResolveKeywordWhitelistIdentities(ctx, []KeywordWhitelistKey{{UserID: 1, APIKeyID: 11}})
	require.NoError(t, err)
	require.Equal(t, "renamed@example.test", items[0].UserEmail)
	require.Equal(t, "Renamed", items[0].Username)
	require.Equal(t, "renamed-key", items[0].KeyName)
	_, err = db.ExecContext(ctx, `UPDATE api_keys SET deleted_at=NOW() WHERE id=11`)
	require.NoError(t, err)
	items, err = svc.ResolveKeywordWhitelistIdentities(ctx, []KeywordWhitelistKey{{UserID: 1, APIKeyID: 11}})
	require.NoError(t, err)
	require.Equal(t, []KeywordWhitelistIdentity{{UserID: 1, APIKeyID: 11}}, items)
}
