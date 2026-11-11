package securityaudit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"unicode"
	"unicode/utf8"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"golang.org/x/text/cases"
)

// CombinationPolicy is an ordered, administrator-defined topic AND latest-user
// intent policy. Rules within each side are ORed by the shared matcher.
type CombinationPolicy struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Enabled         bool     `json:"enabled"`
	TopicRules      []string `json:"topic_rules"`
	IntentRules     []string `json:"intent_rules"`
	MigrateKeywords []string `json:"migrate_keywords,omitempty"`
}

type ActiveCombinationPolicy struct {
	CombinationPolicy
	matcher *promptCBRNMatcher
}

type CombinationMatch struct {
	PolicyID   string `json:"policy_id"`
	PolicyName string `json:"policy_name"`
	CBRNMatch
}

func combinationTemplates() []CombinationPolicy {
	return []CombinationPolicy{
		{ID: "cbrn", Name: "核、生物、化学、放射性危害", TopicRules: defaultCBRNTopicRules(), IntentRules: defaultCBRNIntentRules()},
		{ID: "government", Name: "政府目标攻击", TopicRules: defaultGovernmentTopicRules(), IntentRules: defaultGovernmentIntentRules(), MigrateKeywords: []string{".gov", ".gov."}},
	}
}

func cloneCombinationPolicies(policies []CombinationPolicy) []CombinationPolicy {
	if policies == nil {
		return nil
	}
	result := make([]CombinationPolicy, len(policies))
	for i, policy := range policies {
		result[i] = cloneCombinationPolicy(policy)
	}
	return result
}

func cloneCombinationPolicy(policy CombinationPolicy) CombinationPolicy {
	policy.TopicRules = append([]string{}, policy.TopicRules...)
	policy.IntentRules = append([]string{}, policy.IntentRules...)
	policy.MigrateKeywords = append([]string{}, policy.MigrateKeywords...)
	return policy
}

func cloneActiveCombinationPolicies(policies []ActiveCombinationPolicy) []ActiveCombinationPolicy {
	result := make([]ActiveCombinationPolicy, len(policies))
	for i, policy := range policies {
		result[i] = policy
		result[i].CombinationPolicy = cloneCombinationPolicy(policy.CombinationPolicy)
	}
	return result
}

func (cfg storageConfig) effectiveCombinationPolicies() []CombinationPolicy {
	if cfg.CombinationPolicies != nil {
		return cfg.CombinationPolicies
	}
	return []CombinationPolicy{{
		ID: "cbrn", Name: "核、生物、化学、放射性危害", Enabled: cfg.CBRNBlockingEnabled,
		TopicRules: cfg.CBRNTopicRules, IntentRules: cfg.CBRNIntentRules,
	}}
}

func normalizeCombinationPolicies(cfg *storageConfig) {
	cfg.CombinationPolicies = cloneCombinationPolicies(cfg.effectiveCombinationPolicies())
	for i := range cfg.CombinationPolicies {
		policy := &cfg.CombinationPolicies[i]
		policy.ID, policy.Name = strings.TrimSpace(policy.ID), strings.TrimSpace(policy.Name)
		policy.TopicRules = canonicalBlockedKeywords(policy.TopicRules)
		policy.IntentRules = canonicalBlockedKeywords(policy.IntentRules)
		policy.MigrateKeywords = canonicalBlockedKeywords(policy.MigrateKeywords)
	}
	// The old fields are a compatibility projection, never an additional policy.
	cfg.CBRNBlockingEnabled = false
	cfg.CBRNTopicRules, cfg.CBRNIntentRules = []string{}, []string{}
	for _, policy := range cfg.CombinationPolicies {
		if policy.ID == "cbrn" {
			cfg.CBRNBlockingEnabled = policy.Enabled
			cfg.CBRNTopicRules = append([]string{}, policy.TopicRules...)
			cfg.CBRNIntentRules = append([]string{}, policy.IntentRules...)
			break
		}
	}
}

func validCombinationPolicyID(id string) bool {
	if len(id) == 0 || len(id) > 64 {
		return false
	}
	for i, c := range []byte(id) {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || i > 0 && (c == '-' || c == '_') {
			continue
		}
		return false
	}
	return true
}

func validCombinationPolicyName(name string) bool {
	if strings.TrimSpace(name) != name || utf8.RuneCountInString(name) < 1 || utf8.RuneCountInString(name) > 80 {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return false
		}
	}
	return utf8.ValidString(name)
}

func validateCombinationPolicies(policies []CombinationPolicy) error {
	seen := make(map[string]struct{}, len(policies))
	count, migrations := 0, 0
	for _, policy := range policies {
		if !validCombinationPolicyID(policy.ID) {
			return infraerrors.BadRequest("prompt_audit_invalid_combination_policy_id", "组合规则 ID 须为 1–64 位字母、数字、下划线或短横线，并以字母或数字开头")
		}
		if _, exists := seen[policy.ID]; exists {
			return infraerrors.BadRequest("prompt_audit_duplicate_combination_policy_id", "组合规则 ID 不能重复")
		}
		seen[policy.ID] = struct{}{}
		if !validCombinationPolicyName(policy.Name) {
			return infraerrors.BadRequest("prompt_audit_invalid_combination_policy_name", "组合规则名称须为 1–80 个字符，不能包含控制字符")
		}
		count += len(policy.TopicRules) + len(policy.IntentRules)
		if count > MaxBlockedKeywords {
			return infraerrors.BadRequest("prompt_audit_too_many_combination_rules", "所有组合规则的主题和操作意图规则合计不能超过 10000 条")
		}
		if err := validateCombinationRules(policy.TopicRules, policy.IntentRules); err != nil {
			return err
		}
		if policy.Enabled && (len(policy.TopicRules) == 0 || len(policy.IntentRules) == 0) {
			return infraerrors.BadRequest("prompt_audit_combination_rules_required", "开启组合规则前必须配置主题和操作意图规则")
		}
		migrations += len(policy.MigrateKeywords)
		if migrations > MaxBlockedKeywords {
			return infraerrors.BadRequest("prompt_audit_invalid_combination_migration_keyword", "待迁移字面关键词合计不能超过 10000 条")
		}
		for _, word := range policy.MigrateKeywords {
			if word == "" || strings.HasPrefix(strings.ToLower(word), "regex:") || utf8.RuneCountInString(word) > MaxBlockedKeywordRunes {
				return infraerrors.BadRequest("prompt_audit_invalid_combination_migration_keyword", "待迁移关键词必须为不超过 200 个字符的非空字面词，不能是正则")
			}
		}
	}
	return nil
}

func combinationPoliciesEnabled(policies []CombinationPolicy) bool {
	for _, policy := range policies {
		if policy.Enabled {
			return true
		}
	}
	return false
}

func resolveUpdatedCombinationPolicies(current storageConfig, req UpdateConfigRequest) []CombinationPolicy {
	if req.CombinationPolicies != nil {
		policies := cloneCombinationPolicies(*req.CombinationPolicies)
		if policies == nil {
			policies = []CombinationPolicy{}
		}
		return policies
	}
	policies := cloneCombinationPolicies(current.effectiveCombinationPolicies())
	index := -1
	for i := range policies {
		if policies[i].ID == "cbrn" {
			index = i
			break
		}
	}
	// A canonical list records deletions. Even a stale legacy client submitting
	// enabled=true cannot recreate CBRN after it was removed from that list.
	if index >= 0 {
		policy := &policies[index]
		if req.CBRNBlockingEnabled != nil {
			policy.Enabled = *req.CBRNBlockingEnabled
		}
		if req.CBRNTopicRules != nil {
			policy.TopicRules = append([]string{}, (*req.CBRNTopicRules)...)
		}
		if req.CBRNIntentRules != nil {
			policy.IntentRules = append([]string{}, (*req.CBRNIntentRules)...)
		}
	}
	return policies
}

// Migration is declarative and happens only when saving an enabled policy.
// Reapplying it prevents older clients from restoring retired literal rules.
func migrateCombinationKeywords(cfg *storageConfig) {
	retired := make(map[string]struct{})
	folder := cases.Fold()
	for _, policy := range cfg.CombinationPolicies {
		if policy.Enabled {
			for _, word := range policy.MigrateKeywords {
				retired[folder.String(strings.TrimSpace(word))] = struct{}{}
			}
		}
	}
	if len(retired) == 0 {
		return
	}
	remaining := make([]string, 0, len(cfg.BlockedKeywords))
	removed := false
	for _, word := range cfg.BlockedKeywords {
		if _, exists := retired[folder.String(strings.TrimSpace(word))]; exists {
			removed = true
		} else {
			remaining = append(remaining, word)
		}
	}
	if removed {
		cfg.BlockedKeywords = remaining
		if len(remaining) == 0 {
			cfg.KeywordBlockingEnabled = false
		}
	}
}

func (cfg ActiveConfig) CombinationBlockingActive() bool {
	if !cfg.RiskControlEnabled {
		return false
	}
	for _, policy := range cfg.CombinationPolicies {
		if policy.Enabled && policy.matcher != nil {
			return true
		}
	}
	return false
}

func (cfg ActiveConfig) MatchCombination(req Request) (*CombinationMatch, error) {
	if !cfg.RiskControlEnabled {
		return nil, nil
	}
	var prepared *preparedPromptCombination
	for _, policy := range cfg.CombinationPolicies {
		if !policy.Enabled || !policy.matcher.hasRules() {
			continue
		}
		if prepared == nil {
			var err error
			if req.prepared != nil {
				err = req.prepared.prepare(req)
				req.prepared.combinationMu.Lock()
				defer req.prepared.combinationMu.Unlock()
				prepared = req.prepared.combination
			} else {
				prepared, err = preparePromptCombination(req)
			}
			if err != nil {
				return nil, err
			}
		}
		match := policy.matcher.matchPrepared(prepared)
		if match != nil {
			return &CombinationMatch{PolicyID: policy.ID, PolicyName: policy.Name, CBRNMatch: *match}, nil
		}
	}
	return nil, nil
}

func combinationPoliciesHash(policies []CombinationPolicy) string {
	raw, _ := json.Marshal(policies)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
