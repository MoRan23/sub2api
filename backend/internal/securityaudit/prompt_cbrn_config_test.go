package securityaudit

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func cbrnConfigUpdateRequest() UpdateConfigRequest {
	return UpdateConfigRequest{
		ExpectedConfigVersion: 1, Strategy: "priority", WorkerCount: 1, QueueCapacity: 10,
		Scanners: []string{"pii"}, AllGroups: true,
	}
}

func TestCBRNConfigDefaultsAndLegacyCompatibility(t *testing.T) {
	for _, raw := range []string{"", `{"enabled":false,"all_groups":true,"blocked_keywords":["unchanged"]}`} {
		cfg, err := ParseStorageConfig(raw)
		require.NoError(t, err)
		require.False(t, cfg.CBRNBlockingEnabled)
		require.Len(t, cfg.CBRNTopicRules, 2)
		require.Len(t, cfg.CBRNIntentRules, 1)
		active, err := ActiveFromStorage(cfg, true, prefixEncryptor{})
		require.NoError(t, err)
		require.False(t, active.CBRNBlockingActive())
		require.False(t, cfg.synchronousBlockingEnabled())
		public := PublicFromStorage(cfg, true, nil)
		require.False(t, public.CBRNBlockingActive)
		require.Equal(t, cfg.CBRNTopicRules, public.CBRNTopicRules)
		if raw != "" {
			require.Equal(t, []string{"unchanged"}, cfg.BlockedKeywords)
		}
	}
	first, second := DefaultStorageConfig(), DefaultStorageConfig()
	first.CBRNTopicRules[0] = "changed"
	require.NotEqual(t, first.CBRNTopicRules, second.CBRNTopicRules)
	parsed, err := ParseStorageConfig(`{"cbrn_topic_rules":[],"cbrn_intent_rules":[],"all_groups":true}`)
	require.NoError(t, err)
	require.Empty(t, parsed.CBRNTopicRules, "explicit empty disabled lists must not be reseeded")
}

func TestCBRNConfigUpdatePreservesOmittedValuesAndIndependentToggle(t *testing.T) {
	manager := &ConfigManager{encryptor: prefixEncryptor{}}
	current := DefaultStorageConfig()
	current.CBRNBlockingEnabled = true
	current.CBRNTopicRules = []string{"topic"}
	current.CBRNIntentRules = []string{"intent"}
	current.KeywordAllGroups = false
	current.KeywordGroupIDs = []int64{3}
	current.KeywordWhitelistKeys = []KeywordWhitelistKey{{UserID: 2, APIKeyID: 4}}
	req := cbrnConfigUpdateRequest()
	preserved, err := manager.buildNextStorage(current, req, 9)
	require.NoError(t, err)
	require.True(t, preserved.CBRNBlockingEnabled)
	require.Equal(t, current.CBRNTopicRules, preserved.CBRNTopicRules)
	require.Equal(t, current.CBRNIntentRules, preserved.CBRNIntentRules)
	active, err := ActiveFromStorage(preserved, true, prefixEncryptor{})
	require.NoError(t, err)
	require.Equal(t, ModeOff, active.EffectiveMode(), "CBRN alone does not activate Guard")
	require.False(t, active.KeywordBlockingActive())
	require.True(t, active.CBRNBlockingActive())
	require.True(t, preserved.synchronousBlockingEnabled())
	require.True(t, active.KeywordIncludesGroup(&current.KeywordGroupIDs[0]))
	require.False(t, active.KeywordIncludesGroup(nil))
	require.True(t, active.KeywordWhitelistsKey(2, 4))
	require.False(t, active.KeywordWhitelistsKey(2, 5))
	require.True(t, PublicFromStorage(preserved, true, nil).CBRNBlockingActive)
	require.False(t, PublicFromStorage(preserved, false, nil).CBRNBlockingActive)
	active.RiskControlEnabled = false
	require.False(t, active.CBRNBlockingActive())

	falseValue := false
	topics, intents := []string{" New ", "new", "regex:New", "regex:new"}, []string{" do ", "DO"}
	req.CBRNBlockingEnabled, req.CBRNTopicRules, req.CBRNIntentRules = &falseValue, &topics, &intents
	updated, err := manager.buildNextStorage(current, req, 9)
	require.NoError(t, err)
	require.False(t, updated.CBRNBlockingEnabled)
	require.Equal(t, []string{"New", "regex:New", "regex:new"}, updated.CBRNTopicRules)
	require.Equal(t, []string{"do"}, updated.CBRNIntentRules)
	require.Contains(t, changeSummary(updated), `"cbrn_blocking_enabled":false`)
	require.Contains(t, changeSummary(updated), `"cbrn_topic_rule_count":3`)
	require.NotContains(t, changeSummary(updated), "regex:New")
	updated.CBRNTopicRules[0] = "changed"
	require.Equal(t, "topic", current.CBRNTopicRules[0])
}

func TestCBRNConfigValidationAndCombinedLimits(t *testing.T) {
	manager := &ConfigManager{encryptor: prefixEncryptor{}}
	trueValue := true
	for _, tc := range []struct {
		name            string
		topics, intents []string
	}{
		{"missing topic", nil, []string{"step"}},
		{"missing intent", []string{"topic"}, nil},
		{"blank topic", []string{" "}, []string{"step"}},
		{"invalid regex", []string{"regex:(?=topic)"}, []string{"step"}},
		{"empty regex", []string{"regex:a*"}, []string{"step"}},
		{"empty intent regex", []string{"topic"}, []string{"regex:^$"}},
		{"literal length", []string{strings.Repeat("a", MaxBlockedKeywordRunes+1)}, []string{"step"}},
		{"regex length", []string{"regex:" + strings.Repeat("a", MaxBlockedRegexRunes)}, []string{"step"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := cbrnConfigUpdateRequest()
			req.CBRNBlockingEnabled, req.CBRNTopicRules, req.CBRNIntentRules = &trueValue, &tc.topics, &tc.intents
			_, err := manager.buildNextStorage(DefaultStorageConfig(), req, 9)
			require.Error(t, err)
		})
	}
	current := DefaultStorageConfig()
	current.CBRNTopicRules = make([]string, MaxBlockedKeywords)
	for i := range current.CBRNTopicRules {
		current.CBRNTopicRules[i] = fmt.Sprintf("topic-%d", i)
	}
	current.CBRNIntentRules = nil
	intent := []string{"step"}
	req := cbrnConfigUpdateRequest()
	req.CBRNIntentRules = &intent
	_, err := manager.buildNextStorage(current, req, 9)
	require.Error(t, err, "combined bound must use preserved topics inside final merged validation")
	current = DefaultStorageConfig()
	current.CBRNBlockingEnabled = true
	current.KeywordAllGroups, current.KeywordGroupIDs = false, nil
	_, err = manager.buildNextStorage(current, cbrnConfigUpdateRequest(), 9)
	require.Equal(t, "prompt_audit_keyword_groups_required", infraerrors.Reason(err))
}

func TestCBRNConfigReloadAndSnapshotIsolation(t *testing.T) {
	storage := DefaultStorageConfig()
	storage.ConfigVersion = 2
	storage.CBRNBlockingEnabled = true
	storage.CBRNTopicRules, storage.CBRNIntentRules = []string{"topic"}, []string{"intent"}
	raw, err := json.Marshal(storage)
	require.NoError(t, err)
	repo := &switchableSettingRepository{staticSettingRepository: staticSettingRepository{values: map[string]string{
		SettingKeyPromptAuditConfig: string(raw), SettingKeyRiskControl: "true",
	}}}
	manager := NewConfigManager(nil, repo, nil, prefixEncryptor{}, testTotpKeyConfig())
	require.NoError(t, manager.Reload(context.Background()))
	require.False(t, manager.BlockingActivationDegraded())
	active, ok := manager.Active()
	require.True(t, ok)
	require.True(t, active.CBRNBlockingActive())
	clone := cloneActiveConfig(active)
	require.Same(t, active.cbrnMatcher, clone.cbrnMatcher)
	clone.CBRNTopicRules[0], clone.CBRNIntentRules[0] = "changed", "changed"
	require.Equal(t, "topic", active.CBRNTopicRules[0])
	require.Equal(t, "intent", active.CBRNIntentRules[0])

	storage.ConfigVersion = 3
	storage.CBRNTopicRules = []string{"regex:["}
	raw, err = json.Marshal(storage)
	require.NoError(t, err)
	repo.values[SettingKeyPromptAuditConfig] = string(raw)
	require.Error(t, manager.Reload(context.Background()))
	require.True(t, manager.BlockingActivationDegraded())
	retained, ok := manager.Active()
	require.True(t, ok)
	require.Equal(t, int64(2), retained.ConfigVersion)
	require.Same(t, active.cbrnMatcher, retained.cbrnMatcher)

	storage.ConfigVersion, storage.CBRNBlockingEnabled = 4, false
	storage.CBRNTopicRules = []string{"other"}
	raw, err = json.Marshal(storage)
	require.NoError(t, err)
	repo.values[SettingKeyPromptAuditConfig] = string(raw)
	require.NoError(t, manager.Reload(context.Background()))
	require.False(t, manager.BlockingActivationDegraded())
	require.True(t, active.CBRNBlockingActive(), "in-flight snapshot keeps its prior immutable rules")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				cfg, exists := manager.Active()
				if exists {
					cfg.CBRNTopicRules[0] = "request-local change"
				}
			}
		}()
	}
	wg.Wait()
	retained, _ = manager.Active()
	require.Equal(t, []string{"other"}, retained.CBRNTopicRules)
}

func TestCBRNConfigColdStartTracksIndependentBlockingIntent(t *testing.T) {
	manager := &ConfigManager{}
	manager.observeExpectedState(`{"cbrn_blocking_enabled":true,"config_version":3}`, true)
	require.True(t, manager.BlockingActivationDegraded())
	manager.observeExpectedState(`{"cbrn_blocking_enabled":true,"config_version":3}`, false)
	require.False(t, manager.BlockingActivationDegraded())
}
