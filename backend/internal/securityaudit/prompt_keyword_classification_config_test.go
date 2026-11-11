package securityaudit

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestKeywordClassificationMigratesLegacyStorageWithPolicyDefaults(t *testing.T) {
	cfg, err := ParseStorageConfig(`{"keyword_blocking_enabled":true,"blocked_keywords":[" Jailbreak ","jailbreak","regex:attack [0-9]+"]}`)
	require.NoError(t, err)
	require.Equal(t, []string{"Jailbreak", "regex:attack [0-9]+"}, cfg.JailbreakKeywords)
	require.Equal(t, cfg.JailbreakKeywords, cfg.BlockedKeywords)
	require.Empty(t, cfg.OtherKeywords)
	require.True(t, cfg.WarningEmailEnabled)
	require.Equal(t, 5, cfg.WarningEmailWindowMinutes)
	require.True(t, cfg.AutoBanEnabled)
	require.Equal(t, 24, cfg.ViolationWindowHours)
	require.Equal(t, 10, cfg.BanThreshold)
	public := PublicFromStorage(cfg, true, nil)
	require.Equal(t, cfg.JailbreakKeywords, public.JailbreakKeywords)
	require.True(t, public.WarningEmailEnabled)
	require.Equal(t, 5, public.WarningEmailWindowMinutes)
	require.True(t, public.AutoBanEnabled)
	require.Equal(t, 24, public.ViolationWindowHours)
	require.Equal(t, 10, public.BanThreshold)
	active, err := ActiveFromStorage(cfg, true, prefixEncryptor{})
	require.NoError(t, err)
	require.True(t, active.KeywordBlockingActive())
	require.True(t, active.WarningEmailEnabled)
	require.Equal(t, 5, active.WarningEmailWindowMinutes)
	require.True(t, active.AutoBanEnabled)
	require.Equal(t, 24, active.ViolationWindowHours)
	require.Equal(t, 10, active.BanThreshold)
	rule, content, matched := active.MatchJailbreakKeyword("attack 123")
	require.True(t, matched)
	require.Equal(t, "regex:attack [0-9]+", rule)
	require.Equal(t, "attack 123", content)
	aliasRule, aliasContent, aliasMatched := active.MatchBlockedKeyword("attack 123")
	require.Equal(t, rule, aliasRule)
	require.Equal(t, content, aliasContent)
	require.Equal(t, matched, aliasMatched)
	_, _, matched = active.MatchOtherKeyword("attack 123")
	require.False(t, matched)
}

func TestKeywordClassificationNewListsOverrideStaleAlias(t *testing.T) {
	cfg, err := ParseStorageConfig(`{"keyword_blocking_enabled":true,"blocked_keywords":["retired"],"jailbreak_keywords":[],"other_keywords":[" regex:custom-[0-9]+ ","other","OTHER"],"warning_email_enabled":false,"auto_ban_enabled":false}`)
	require.NoError(t, err)
	require.Empty(t, cfg.JailbreakKeywords)
	require.Empty(t, cfg.BlockedKeywords)
	require.Equal(t, []string{"regex:custom-[0-9]+", "other"}, cfg.OtherKeywords)
	require.False(t, cfg.WarningEmailEnabled)
	require.False(t, cfg.AutoBanEnabled)
	active, err := ActiveFromStorage(cfg, true, prefixEncryptor{})
	require.NoError(t, err)
	require.True(t, active.KeywordBlockingActive())
	_, _, matched := active.MatchJailbreakKeyword("retired custom-99")
	require.False(t, matched)
	rule, content, matched := active.MatchOtherKeyword("custom-99")
	require.True(t, matched)
	require.Equal(t, "regex:custom-[0-9]+", rule)
	require.Equal(t, "custom-99", content)
	active.RiskControlEnabled = false
	require.False(t, active.KeywordBlockingActive())
	_, _, matched = active.MatchOtherKeyword("custom-99")
	require.False(t, matched)
}

func TestKeywordClassificationUpdatePreservesOmittedFields(t *testing.T) {
	current, err := ParseStorageConfig(`{"keyword_blocking_enabled":true,"jailbreak_keywords":["jailbreak"],"other_keywords":["other"],"warning_email_enabled":false,"warning_email_window_minutes":9,"auto_ban_enabled":false,"violation_window_hours":48,"ban_threshold":20}`)
	require.NoError(t, err)
	req := promptAuditUpdateRequest(1, 1, "")
	req.KeywordBlockingEnabled = true
	req.BlockedKeywords = nil
	manager := &ConfigManager{encryptor: prefixEncryptor{}}
	next, err := manager.buildNextStorage(current, req, 1)
	require.NoError(t, err)
	require.Equal(t, current.JailbreakKeywords, next.JailbreakKeywords)
	require.Equal(t, current.OtherKeywords, next.OtherKeywords)
	require.False(t, next.WarningEmailEnabled)
	require.Equal(t, 9, next.WarningEmailWindowMinutes)
	require.False(t, next.AutoBanEnabled)
	require.Equal(t, 48, next.ViolationWindowHours)
	require.Equal(t, 20, next.BanThreshold)

	req.BlockedKeywords = []string{" legacy update "}
	next, err = manager.buildNextStorage(current, req, 1)
	require.NoError(t, err)
	require.Equal(t, []string{"legacy update"}, next.JailbreakKeywords)
	require.Equal(t, current.OtherKeywords, next.OtherKeywords)

	jailbreak, other := []string{}, []string{"new other"}
	req.JailbreakKeywords, req.OtherKeywords = &jailbreak, &other
	req.WarningEmailEnabled, req.AutoBanEnabled = new(true), new(true)
	req.WarningEmailWindowMinutes, req.ViolationWindowHours, req.BanThreshold = new(1), new(720), new(10000)
	next, err = manager.buildNextStorage(current, req, 1)
	require.NoError(t, err)
	require.Empty(t, next.JailbreakKeywords)
	require.Empty(t, next.BlockedKeywords)
	require.Equal(t, other, next.OtherKeywords)
	require.True(t, next.KeywordBlockingEnabled)
	require.True(t, next.WarningEmailEnabled)
	require.True(t, next.AutoBanEnabled)
	require.Equal(t, 1, next.WarningEmailWindowMinutes)
	require.Equal(t, 720, next.ViolationWindowHours)
	require.Equal(t, 10000, next.BanThreshold)
	raw, err := json.Marshal(next)
	require.NoError(t, err)
	reloaded, err := ParseStorageConfig(string(raw))
	require.NoError(t, err)
	require.Equal(t, next, reloaded)
}

func TestKeywordClassificationPolicyBoundsAtUpdateAndLoad(t *testing.T) {
	for _, tc := range []struct {
		field, reason string
		maximum       int
		set           func(*UpdateConfigRequest, *int)
	}{
		{"warning_email_window_minutes", "prompt_audit_invalid_warning_email_window", 60, func(r *UpdateConfigRequest, n *int) { r.WarningEmailWindowMinutes = n }},
		{"violation_window_hours", "prompt_audit_invalid_violation_window", 720, func(r *UpdateConfigRequest, n *int) { r.ViolationWindowHours = n }},
		{"ban_threshold", "prompt_audit_invalid_ban_threshold", 10000, func(r *UpdateConfigRequest, n *int) { r.BanThreshold = n }},
	} {
		for _, value := range []int{-1, 0, 1, tc.maximum, tc.maximum + 1} {
			t.Run(fmt.Sprintf("%s=%d", tc.field, value), func(t *testing.T) {
				req := promptAuditUpdateRequest(1, 1, "")
				tc.set(&req, &value)
				updateErr := validateUpdateConfigRequest(req)
				_, storageErr := ParseStorageConfig(fmt.Sprintf(`{"%s":%d}`, tc.field, value))
				if value < 1 || value > tc.maximum {
					require.Equal(t, tc.reason, infraerrors.Reason(updateErr))
					require.Equal(t, tc.reason, infraerrors.Reason(storageErr))
				} else {
					require.NoError(t, updateErr)
					require.NoError(t, storageErr)
				}
			})
		}
	}
	for _, category := range []string{"jailbreak_keywords", "other_keywords"} {
		_, err := ParseStorageConfig(fmt.Sprintf(`{"%s":["regex:["]}`, category))
		require.Equal(t, "prompt_audit_invalid_keyword_regex", infraerrors.Reason(err))
	}
}

func TestKeywordClassificationCombinationMigrationRetiresExactLiteralsInBothLists(t *testing.T) {
	cfg, err := ParseStorageConfig(`{"keyword_blocking_enabled":true,"blocked_keywords":[" .GOV ",".gov.","retained","regex:gov"],"other_keywords":[".gov"," .GOV. ","agency.gov","regex:other-gov"],"combination_policies":[{"id":"government","name":"government","enabled":true,"topic_rules":["gov"],"intent_rules":["attack"],"migrate_keywords":[".gov",".gov."]}]}`)
	require.NoError(t, err)
	require.Equal(t, []string{"retained", "regex:gov"}, cfg.JailbreakKeywords)
	require.Equal(t, cfg.JailbreakKeywords, cfg.BlockedKeywords)
	require.Equal(t, []string{"agency.gov", "regex:other-gov"}, cfg.OtherKeywords)
	require.True(t, cfg.KeywordBlockingEnabled)
	req := promptAuditUpdateRequest(1, 1, "")
	req.KeywordBlockingEnabled = true
	req.BlockedKeywords = []string{".gov"}
	other := []string{" .GOV. ", "unrelated"}
	req.OtherKeywords = &other
	manager := &ConfigManager{encryptor: prefixEncryptor{}}
	next, err := manager.buildNextStorage(cfg, req, 1)
	require.NoError(t, err)
	require.Empty(t, next.JailbreakKeywords)
	require.Equal(t, []string{"unrelated"}, next.OtherKeywords)
	require.True(t, next.KeywordBlockingEnabled, "unrelated rules keep keyword blocking active")

	other = []string{".gov"}
	next, err = manager.buildNextStorage(cfg, req, 1)
	require.NoError(t, err)
	require.Empty(t, next.JailbreakKeywords)
	require.Empty(t, next.OtherKeywords)
	require.Empty(t, next.BlockedKeywords)
	require.False(t, next.KeywordBlockingEnabled, "retiring both final rules disables the empty keyword policy")
}

func TestKeywordClassificationCombinedLimit(t *testing.T) {
	jailbreak := make([]string, MaxBlockedKeywords/2)
	other := make([]string, MaxBlockedKeywords-len(jailbreak))
	for index := range jailbreak {
		jailbreak[index] = fmt.Sprintf("jailbreak-%d", index)
	}
	for index := range other {
		other[index] = fmt.Sprintf("other-%d", index)
	}
	req := promptAuditUpdateRequest(1, 1, "")
	req.JailbreakKeywords, req.OtherKeywords = &jailbreak, &other
	require.NoError(t, validateUpdateConfigRequest(req))
	current := DefaultStorageConfig()
	current.JailbreakKeywords, current.OtherKeywords = jailbreak, other
	require.NoError(t, validateStorageConfig(current))

	tooMany := append(append([]string{}, jailbreak...), "one-extra")
	req.JailbreakKeywords = &tooMany
	require.Equal(t, "prompt_audit_too_many_keywords", infraerrors.Reason(validateUpdateConfigRequest(req)))
	current.JailbreakKeywords = tooMany
	require.Equal(t, "prompt_audit_too_many_keywords", infraerrors.Reason(validateStorageConfig(current)))
	raw, err := json.Marshal(current)
	require.NoError(t, err)
	_, err = ParseStorageConfig(string(raw))
	require.Equal(t, "prompt_audit_too_many_keywords", infraerrors.Reason(err))
	_, err = ActiveFromStorage(current, true, prefixEncryptor{})
	require.Equal(t, "prompt_audit_too_many_keywords", infraerrors.Reason(err))

	// Omitted categories are merged before checking the total, rather than
	// allowing a half-update to bypass the shared budget.
	current.JailbreakKeywords = jailbreak
	req.OtherKeywords = nil
	_, err = (&ConfigManager{encryptor: prefixEncryptor{}}).buildNextStorage(current, req, 1)
	require.Equal(t, "prompt_audit_too_many_keywords", infraerrors.Reason(err))
	// The limit counts canonical entries. Repeated literals within a category
	// still collapse according to the existing case-insensitive rule contract.
	duplicates := append(append([]string{}, jailbreak...), " JAILBREAK-0 ")
	req.JailbreakKeywords, req.OtherKeywords = &duplicates, &other
	require.NoError(t, validateUpdateConfigRequest(req))
}

func TestKeywordClassificationHotReloadOwnsListsAndCompiledMatchers(t *testing.T) {
	settings := staticSettingRepository{values: map[string]string{
		SettingKeyRiskControl:       "true",
		SettingKeyPromptAuditConfig: `{"config_version":2,"keyword_blocking_enabled":true,"blocked_keywords":["old"]}`,
	}}
	manager := &ConfigManager{settings: settings, encryptor: prefixEncryptor{}, clock: fixedClock{}}
	require.NoError(t, manager.Reload(context.Background()))
	before, ok := manager.Active()
	require.True(t, ok)
	settings.values[SettingKeyPromptAuditConfig] = `{"config_version":3,"keyword_blocking_enabled":true,"jailbreak_keywords":["new"],"other_keywords":["regex:other-[0-9]+"]}`
	require.NoError(t, manager.Reload(context.Background()))
	after, ok := manager.Active()
	require.True(t, ok)
	_, _, hit := before.MatchJailbreakKeyword("old")
	require.True(t, hit)
	_, _, hit = after.MatchJailbreakKeyword("old")
	require.False(t, hit)
	_, _, hit = after.MatchOtherKeyword("other-123")
	require.True(t, hit)
	clone, ok := manager.Active()
	require.True(t, ok)
	require.Same(t, after.jailbreakMatcher, clone.jailbreakMatcher)
	require.Same(t, after.otherKeywordMatcher, clone.otherKeywordMatcher)
	after.JailbreakKeywords[0], after.OtherKeywords[0] = "mutated", "mutated"
	require.Equal(t, []string{"new"}, clone.JailbreakKeywords)
	require.Equal(t, []string{"regex:other-[0-9]+"}, clone.OtherKeywords)
	public, err := manager.Public()
	require.NoError(t, err)
	require.Equal(t, []string{"new"}, public.JailbreakKeywords)
	require.Equal(t, []string{"regex:other-[0-9]+"}, public.OtherKeywords)
	require.Contains(t, changeSummary(manager.snapshot.Load().storage), `"other_keyword_count":1`)
	require.NotContains(t, changeSummary(manager.snapshot.Load().storage), "other-[0-9]+")
}

func TestKeywordClassificationActiveCloneSharesOnlyItsOwnLegacyProjection(t *testing.T) {
	published := ActiveConfig{JailbreakKeywords: []string{"jailbreak"}, BlockedKeywords: []string{"jailbreak"}}
	manager := &ConfigManager{}
	manager.snapshot.Store(&activeConfigSnapshot{active: published})
	clone, ok := manager.Active()
	require.True(t, ok)
	require.Same(t, &clone.JailbreakKeywords[0], &clone.BlockedKeywords[0])
	clone.BlockedKeywords[0] = "request-local"
	require.Equal(t, "request-local", clone.JailbreakKeywords[0])
	require.Equal(t, []string{"jailbreak"}, published.JailbreakKeywords)
	require.Equal(t, []string{"jailbreak"}, published.BlockedKeywords)
	fresh, ok := manager.Active()
	require.True(t, ok)
	require.Equal(t, []string{"jailbreak"}, fresh.JailbreakKeywords)
	require.Equal(t, fresh.JailbreakKeywords, fresh.BlockedKeywords)
}

func TestKeywordClassificationActiveClonePreservesExplicitEmptyAndLegacyOnly(t *testing.T) {
	cleared := cloneActiveConfig(ActiveConfig{JailbreakKeywords: []string{}, BlockedKeywords: []string{"stale"}})
	require.NotNil(t, cleared.JailbreakKeywords)
	require.NotNil(t, cleared.BlockedKeywords)
	require.Empty(t, cleared.JailbreakKeywords)
	require.Empty(t, cleared.BlockedKeywords)
	clearedAgain := cloneActiveConfig(cleared)
	require.NotNil(t, clearedAgain.JailbreakKeywords)
	require.Empty(t, clearedAgain.BlockedKeywords)

	legacy := ActiveConfig{BlockedKeywords: []string{"legacy"}}
	clone := cloneActiveConfig(legacy)
	require.Nil(t, clone.JailbreakKeywords)
	require.Equal(t, []string{"legacy"}, clone.BlockedKeywords)
	clone.BlockedKeywords[0] = "request-local"
	require.Equal(t, []string{"legacy"}, legacy.BlockedKeywords)
}
