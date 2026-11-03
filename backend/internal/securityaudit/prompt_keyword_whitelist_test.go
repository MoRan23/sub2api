package securityaudit

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestKeywordWhitelistConfigRoundTripAndPartialUpdate(t *testing.T) {
	manager := &ConfigManager{encryptor: prefixEncryptor{}}
	current, err := ParseStorageConfig(`{}`)
	require.NoError(t, err)
	require.Empty(t, current.KeywordWhitelistKeys)
	request := UpdateConfigRequest{Strategy: "priority", WorkerCount: 1, QueueCapacity: 10, Scanners: []string{"pii"}, AllGroups: true}
	pairs := []KeywordWhitelistKey{{UserID: 2, APIKeyID: 22}, {UserID: 1, APIKeyID: 11}, {UserID: 1, APIKeyID: 11}}
	request.KeywordWhitelistKeys = &pairs
	next, err := manager.buildNextStorage(current, request, 1)
	require.NoError(t, err)
	want := []KeywordWhitelistKey{{UserID: 1, APIKeyID: 11}, {UserID: 2, APIKeyID: 22}}
	require.Equal(t, want, next.KeywordWhitelistKeys)
	raw, err := json.Marshal(next)
	require.NoError(t, err)
	stored, err := ParseStorageConfig(string(raw))
	require.NoError(t, err)
	require.Equal(t, want, stored.KeywordWhitelistKeys)
	require.Contains(t, changeSummary(stored), `"keyword_whitelist_key_count":2`)
	active, err := ActiveFromStorage(stored, true, prefixEncryptor{})
	require.NoError(t, err)
	public := PublicFromStorage(stored, true, nil)
	public.KeywordWhitelistKeys[0].UserID = 999
	clone := cloneActiveConfig(active)
	clone.KeywordWhitelistKeys[0].APIKeyID = 999
	storageClone := cloneStorageConfig(stored)
	storageClone.KeywordWhitelistKeys[0].UserID = 999
	require.Equal(t, want, stored.KeywordWhitelistKeys)
	require.Equal(t, want, active.KeywordWhitelistKeys)
	for _, payload := range []string{`{}`, `{"keyword_whitelist_keys":null}`} {
		request.KeywordWhitelistKeys = nil
		require.NoError(t, json.Unmarshal([]byte(payload), &request))
		preserved, err := manager.buildNextStorage(stored, request, 1)
		require.NoError(t, err)
		require.Equal(t, want, preserved.KeywordWhitelistKeys)
	}
	require.NoError(t, json.Unmarshal([]byte(`{"keyword_whitelist_keys":[]}`), &request))
	cleared, err := manager.buildNextStorage(stored, request, 1)
	require.NoError(t, err)
	require.Empty(t, cleared.KeywordWhitelistKeys)
	publicJSON, err := json.Marshal(PublicFromStorage(cleared, true, nil))
	require.NoError(t, err)
	require.Contains(t, string(publicJSON), `"keyword_whitelist_keys":[]`)
}

func TestKeywordWhitelistRejectsInvalidIDsAndTooManyKeys(t *testing.T) {
	for _, pairs := range [][]KeywordWhitelistKey{
		{{UserID: 0, APIKeyID: 1}}, {{UserID: 1, APIKeyID: 0}},
		{{UserID: -1, APIKeyID: 1}}, {{UserID: 1, APIKeyID: -1}},
	} {
		err := validateKeywordWhitelistKeys(pairs)
		require.Equal(t, "prompt_audit_invalid_keyword_whitelist_key", infraerrors.Reason(err))
		cfg := DefaultStorageConfig()
		cfg.KeywordWhitelistKeys = pairs
		raw, err := json.Marshal(cfg)
		require.NoError(t, err)
		_, err = ParseStorageConfig(string(raw))
		require.Error(t, err)
	}
	require.Equal(t, "prompt_audit_too_many_keyword_whitelist_keys", infraerrors.Reason(validateKeywordWhitelistKeys(make([]KeywordWhitelistKey, MaxKeywordWhitelistKeys+1))))
	for _, raw := range []string{`{"keyword_whitelist_keys":[{"user_id":"1","api_key_id":2}]}`, `{"keyword_whitelist_keys":[{"user_id":1,"api_key_id":1.5}]}`} {
		var req UpdateConfigRequest
		require.Error(t, json.Unmarshal([]byte(raw), &req))
	}
}

type keywordWhitelistGuard struct {
	*PromptService
	calls atomic.Int64
}

func (g *keywordWhitelistGuard) Evaluate(context.Context, Request) (*PromptDecision, error) {
	g.calls.Add(1)
	return &PromptDecision{Kind: DecisionBlock, ErrorCode: ErrorCodeBlocked}, nil
}

func TestKeywordWhitelistOnlyBypassesExactAuthenticatedKey(t *testing.T) {
	active, err := ActiveFromStorage(storageConfig{
		Enabled: true, BlockingEnabled: true, AllGroups: true,
		KeywordBlockingEnabled: true, KeywordAllGroups: true, BlockedKeywords: []string{"blocked"},
		KeywordWhitelistKeys: []KeywordWhitelistKey{{UserID: 1, APIKeyID: 11}},
	}, true, prefixEncryptor{})
	require.NoError(t, err)
	for _, tc := range []struct {
		name      string
		user, key int64
		bypass    bool
	}{
		{"exact pair", 1, 11, true}, {"same user other key", 1, 12, false},
		{"key different owner", 2, 11, false}, {"no user", 0, 11, false}, {"no key", 1, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeConfigStore{active: true, cfg: active}
			guard := &keywordWhitelistGuard{PromptService: &PromptService{config: store}}
			legacy := &fakeLegacyEngine{}
			decision := NewCoordinator(legacy, guard).Check(context.Background(), Request{
				UserID: tc.user, APIKeyID: tc.key, Protocol: "openai_responses",
				Body: []byte(`{"input":"blocked","user_id":1,"api_key_id":11}`),
			})
			if tc.bypass {
				require.Equal(t, ErrorCodeBlocked, decision.ErrorCode, "Guard still rejects a whitelisted key")
				require.EqualValues(t, 1, guard.calls.Load())
				require.EqualValues(t, 1, legacy.calls.Load())
				store.cfg.KeywordWhitelistKeys = nil
				afterRemoval, err := guard.CheckKeyword(context.Background(), Request{UserID: tc.user, APIKeyID: tc.key, Protocol: "openai_responses", Body: []byte(`{"input":"blocked"}`)})
				require.NoError(t, err)
				require.Equal(t, ErrorCodeKeywordBlocked, afterRemoval.ErrorCode)
			} else {
				require.Equal(t, ErrorCodeKeywordBlocked, decision.ErrorCode)
				require.Zero(t, guard.calls.Load())
				require.Zero(t, legacy.calls.Load())
			}
		})
	}
}
