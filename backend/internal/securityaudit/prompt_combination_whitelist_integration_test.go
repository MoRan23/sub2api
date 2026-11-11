package securityaudit

import (
	"context"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestCombinationWhitelistOwnershipPersistenceAndLegacySave(t *testing.T) {
	db := openPromptAuditIntegrationDB(t)
	ctx := context.Background()
	_, err := db.ExecContext(ctx, `INSERT INTO users(id,email,username,deleted_at) VALUES
		(1,'owner@example.test','Owner',NULL),(2,'other@example.test','Other',NULL),(3,'deleted@example.test','Deleted',NOW());
		INSERT INTO api_keys(id,user_id,name,deleted_at) VALUES
		(11,1,'normal',NULL),(12,1,'other-key',NULL),(21,2,'different-owner',NULL),(31,3,'deleted-user-key',NULL);`)
	require.NoError(t, err)
	repo := postgresPromptAuditSettingRepository{db: db}
	require.NoError(t, repo.Set(ctx, SettingKeyRiskControl, "true"))
	manager := NewConfigManager(db, repo, nil, promptAuditTestEncryptor(t), testTotpKeyConfig())
	require.NoError(t, manager.Reload(ctx))
	req := cbrnConfigUpdateRequest()
	policy := testCombinationPolicy("first")
	policy.KeywordWhitelistKeys = []KeywordWhitelistKey{{UserID: 1, APIKeyID: 11}}
	policies := []CombinationPolicy{policy}
	req.CombinationPolicies = &policies
	global := []KeywordWhitelistKey{{UserID: 1, APIKeyID: 12}}
	req.KeywordWhitelistKeys = &global
	public, err := manager.Save(ctx, req, 101)
	require.NoError(t, err)
	require.Equal(t, policy.KeywordWhitelistKeys, public.CombinationPolicies[0].KeywordWhitelistKeys)
	require.Equal(t, global, public.KeywordWhitelistKeys)
	active, _ := manager.Active()
	request := Request{UserID: 1, APIKeyID: 11, Protocol: "responses", Body: []byte(`{"input":"target attack"}`)}
	match, err := active.MatchCombination(request)
	require.NoError(t, err)
	require.Nil(t, match)
	identities, err := NewPostgreSQLRepository(db).ResolveKeywordWhitelistIdentities(ctx, public.CombinationPolicies[0].KeywordWhitelistKeys)
	require.NoError(t, err)
	require.Equal(t, []KeywordWhitelistIdentity{{UserID: 1, APIKeyID: 11, UserEmail: "owner@example.test", Username: "Owner", KeyName: "normal", Available: true}}, identities)

	for _, invalid := range []KeywordWhitelistKey{{UserID: 1, APIKeyID: 21}, {UserID: 3, APIKeyID: 31}, {UserID: 1, APIKeyID: 999}} {
		req.ExpectedConfigVersion = public.ConfigVersion
		policies[0].KeywordWhitelistKeys = []KeywordWhitelistKey{invalid}
		_, err := manager.Save(ctx, req, 101)
		require.Equal(t, "prompt_audit_invalid_keyword_whitelist_key", infraerrors.Reason(err))
		raw, err := repo.GetValue(ctx, SettingKeyPromptAuditConfig)
		require.NoError(t, err)
		stored, err := ParseStorageConfig(raw)
		require.NoError(t, err)
		require.Equal(t, public.ConfigVersion, stored.ConfigVersion, "ownership failure rolls back the whole save")
		require.Equal(t, policy.KeywordWhitelistKeys, stored.CombinationPolicies[0].KeywordWhitelistKeys)
	}

	_, err = db.ExecContext(ctx, `UPDATE api_keys SET deleted_at=NOW() WHERE id=11`)
	require.NoError(t, err)
	policies[0].KeywordWhitelistKeys = nil // A legacy editor does not know this field.
	policies[0].Name = "renamed"
	req.KeywordWhitelistKeys = nil
	public, err = manager.Save(ctx, req, 101)
	require.NoError(t, err, "omitted existing exemptions must not prevent unrelated edits after a key is deleted")
	require.Equal(t, policy.KeywordWhitelistKeys, public.CombinationPolicies[0].KeywordWhitelistKeys)
	identities, err = NewPostgreSQLRepository(db).ResolveKeywordWhitelistIdentities(ctx, public.CombinationPolicies[0].KeywordWhitelistKeys)
	require.NoError(t, err)
	require.False(t, identities[0].Available)

	// Current editors submit whole lists, including identities that became
	// unavailable after the administrator originally granted the exemption.
	req.ExpectedConfigVersion = public.ConfigVersion
	policies[0].KeywordWhitelistKeys = []KeywordWhitelistKey{{UserID: 1, APIKeyID: 11}}
	policies[0].Name = "explicit existing list"
	public, err = manager.Save(ctx, req, 101)
	require.NoError(t, err, "unchanged explicit pairs do not need revalidation")
	req.ExpectedConfigVersion = public.ConfigVersion
	policies[0].KeywordWhitelistKeys = append(policies[0].KeywordWhitelistKeys, KeywordWhitelistKey{UserID: 1, APIKeyID: 21})
	_, err = manager.Save(ctx, req, 101)
	require.Equal(t, "prompt_audit_invalid_keyword_whitelist_key", infraerrors.Reason(err), "a new pair still requires current ownership")
	policies[0].KeywordWhitelistKeys[1] = KeywordWhitelistKey{UserID: 1, APIKeyID: 12}
	public, err = manager.Save(ctx, req, 101)
	require.NoError(t, err, "one valid addition does not revalidate old unavailable pairs")
	_, err = db.ExecContext(ctx, `UPDATE api_keys SET deleted_at=NOW() WHERE id=12`)
	require.NoError(t, err)
	req.ExpectedConfigVersion = public.ConfigVersion
	policies[0].KeywordWhitelistKeys = policies[0].KeywordWhitelistKeys[:1]
	public, err = manager.Save(ctx, req, 101)
	require.NoError(t, err, "removing pairs must not require live ownership")
	req.ExpectedConfigVersion = public.ConfigVersion
	policies[0].ID = "new-policy"
	_, err = manager.Save(ctx, req, 101)
	require.Equal(t, "prompt_audit_invalid_keyword_whitelist_key", infraerrors.Reason(err), "an existing pair is new when granted to another policy")
	policies[0].ID = "first"

	second := NewConfigManager(db, repo, nil, promptAuditTestEncryptor(t), testTotpKeyConfig())
	require.NoError(t, second.Reload(ctx))
	reloaded, err := second.Public()
	require.NoError(t, err)
	require.Equal(t, public.CombinationPolicies, reloaded.CombinationPolicies)
	req.ExpectedConfigVersion = public.ConfigVersion
	policies[0].KeywordWhitelistKeys = []KeywordWhitelistKey{}
	public, err = manager.Save(ctx, req, 101)
	require.NoError(t, err)
	require.Empty(t, public.CombinationPolicies[0].KeywordWhitelistKeys)
	require.NoError(t, second.Reload(ctx))
	active, _ = second.Active()
	match, err = active.MatchCombination(request)
	require.NoError(t, err)
	require.Equal(t, "first", match.PolicyID)
}
