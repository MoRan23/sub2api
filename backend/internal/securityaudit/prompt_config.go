package securityaudit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const (
	DefaultWorkerCount     = 4
	MaxWorkerCount         = 32
	DefaultQueueCapacity   = 32768
	MaxQueueCapacity       = 100000
	DefaultTimeoutMS       = 3000
	MinTimeoutMS           = 100
	MaxTimeoutMS           = 30000
	DefaultInputLimit      = 4000
	MinInputLimit          = 128
	MaxInputLimit          = 100000
	DefaultPayloadTTL      = 30 * time.Minute
	MaxBlockedKeywords     = 10000
	MaxBlockedKeywordRunes = 200
	MaxBlockedRegexRunes   = 2000
)

type SecretEncryptor interface {
	Encrypt(plaintext string) (string, error)
	Decrypt(ciphertext string) (string, error)
}

// ConfigStore is the injectable boundary between hot-path prompt auditing and
// the concrete settings/PostgreSQL/Redis-backed configuration manager.
type ConfigStore interface {
	Start(ctx context.Context) error
	Shutdown(ctx context.Context) error
	Active() (ActiveConfig, bool)
	EffectiveMode() Mode
	// BlockingActivationDegraded is true when storage intent requires blocking
	// but no usable blocking snapshot is active (cold start or failed reload).
	// It must stay false when blocking is not intended, even if config is
	// untrusted—otherwise default-off deployments fail closed for all traffic.
	BlockingActivationDegraded() bool
	Public() (PublicConfig, error)
	Save(ctx context.Context, req UpdateConfigRequest, actorID int64) (PublicConfig, error)
	RuntimeState() (expected int64, active int64, loadedAt *time.Time, loadError string)
	Encrypt(value string) (string, error)
	Decrypt(value string) (string, error)
}

type StorageEndpoint struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Protocol        string `json:"protocol"`
	BaseURL         string `json:"base_url"`
	Model           string `json:"model"`
	TokenCiphertext string `json:"token_ciphertext,omitempty"`
	TimeoutMS       int    `json:"timeout_ms"`
	InputLimit      int    `json:"input_limit"`
	Enabled         bool   `json:"enabled"`
}

type storageConfig struct {
	Enabled                   bool                  `json:"enabled"`
	BlockingEnabled           bool                  `json:"blocking_enabled"`
	BlockingLatestTurnOnly    bool                  `json:"blocking_latest_turn_only"`
	KeywordBlockingEnabled    bool                  `json:"keyword_blocking_enabled"`
	BlockedKeywords           []string              `json:"blocked_keywords"`
	JailbreakKeywords         []string              `json:"jailbreak_keywords"`
	OtherKeywords             []string              `json:"other_keywords"`
	KeywordAllGroups          bool                  `json:"keyword_all_groups"`
	KeywordGroupIDs           []int64               `json:"keyword_group_ids"`
	KeywordWhitelistKeys      []KeywordWhitelistKey `json:"keyword_whitelist_keys"`
	CBRNBlockingEnabled       bool                  `json:"cbrn_blocking_enabled"`
	CBRNTopicRules            []string              `json:"cbrn_topic_rules"`
	CBRNIntentRules           []string              `json:"cbrn_intent_rules"`
	CombinationPolicies       []CombinationPolicy   `json:"combination_policies"`
	StorePassEvents           bool                  `json:"store_pass_events"`
	Strategy                  string                `json:"strategy"`
	WorkerCount               int                   `json:"worker_count"`
	QueueCapacity             int                   `json:"queue_capacity"`
	Scanners                  []string              `json:"scanners"`
	AllGroups                 bool                  `json:"all_groups"`
	GroupIDs                  []int64               `json:"group_ids"`
	Endpoints                 []StorageEndpoint     `json:"endpoints"`
	ConfigVersion             int64                 `json:"config_version"`
	UpdatedAt                 time.Time             `json:"updated_at"`
	UpdatedBy                 int64                 `json:"updated_by"`
	ChangeSummary             string                `json:"change_summary"`
	WarningEmailEnabled       bool                  `json:"warning_email_enabled"`
	WarningEmailWindowMinutes int                   `json:"warning_email_window_minutes"`
	AutoBanEnabled            bool                  `json:"auto_ban_enabled"`
	ViolationWindowHours      int                   `json:"violation_window_hours"`
	BanThreshold              int                   `json:"ban_threshold"`
}

type ActiveEndpoint struct {
	ID         string
	Name       string
	Protocol   string
	BaseURL    string
	Model      string
	Token      string
	TimeoutMS  int
	InputLimit int
	Enabled    bool
	// TokenInvalid marks an endpoint whose persisted token ciphertext cannot be
	// decrypted with the current encryption key (key changed or auto-generated
	// on restart). The endpoint is kept visible for admins but excluded from
	// runtime use until the token is re-entered or cleared (issue #4887).
	TokenInvalid bool
}

type ActiveConfig struct {
	RiskControlEnabled        bool
	Enabled                   bool
	BlockingEnabled           bool
	BlockingLatestTurnOnly    bool
	KeywordBlockingEnabled    bool
	BlockedKeywords           []string
	JailbreakKeywords         []string
	OtherKeywords             []string
	KeywordAllGroups          bool
	KeywordGroupIDs           []int64
	KeywordWhitelistKeys      []KeywordWhitelistKey
	CBRNBlockingEnabled       bool
	CBRNTopicRules            []string
	CBRNIntentRules           []string
	CombinationPolicies       []ActiveCombinationPolicy
	StorePassEvents           bool
	Strategy                  string
	WorkerCount               int
	QueueCapacity             int
	Scanners                  []string
	AllGroups                 bool
	GroupIDs                  []int64
	Endpoints                 []ActiveEndpoint
	ConfigVersion             int64
	UpdatedAt                 time.Time
	UpdatedBy                 int64
	ChangeSummary             string
	keywordMatcher            *promptKeywordMatcher
	jailbreakMatcher          *promptKeywordMatcher
	otherKeywordMatcher       *promptKeywordMatcher
	WarningEmailEnabled       bool
	WarningEmailWindowMinutes int
	AutoBanEnabled            bool
	ViolationWindowHours      int
	BanThreshold              int
}

type PublicEndpoint struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Protocol    string `json:"protocol"`
	BaseURL     string `json:"base_url"`
	Model       string `json:"model"`
	TimeoutMS   int    `json:"timeout_ms"`
	InputLimit  int    `json:"input_limit"`
	Enabled     bool   `json:"enabled"`
	HasToken    bool   `json:"has_token"`
	TokenStatus string `json:"token_status"`
}

type PublicConfig struct {
	Enabled                   bool                  `json:"enabled"`
	BlockingEnabled           bool                  `json:"blocking_enabled"`
	BlockingLatestTurnOnly    bool                  `json:"blocking_latest_turn_only"`
	KeywordBlockingEnabled    bool                  `json:"keyword_blocking_enabled"`
	KeywordBlockingActive     bool                  `json:"keyword_blocking_active"`
	BlockedKeywords           []string              `json:"blocked_keywords"`
	JailbreakKeywords         []string              `json:"jailbreak_keywords"`
	OtherKeywords             []string              `json:"other_keywords"`
	KeywordAllGroups          bool                  `json:"keyword_all_groups"`
	KeywordGroupIDs           []int64               `json:"keyword_group_ids"`
	KeywordWhitelistKeys      []KeywordWhitelistKey `json:"keyword_whitelist_keys"`
	CBRNBlockingEnabled       bool                  `json:"cbrn_blocking_enabled"`
	CBRNBlockingActive        bool                  `json:"cbrn_blocking_active"`
	CBRNTopicRules            []string              `json:"cbrn_topic_rules"`
	CBRNIntentRules           []string              `json:"cbrn_intent_rules"`
	CombinationPolicies       []CombinationPolicy   `json:"combination_policies"`
	CombinationTemplates      []CombinationPolicy   `json:"combination_templates"`
	CombinationBlockingActive bool                  `json:"combination_blocking_active"`
	StorePassEvents           bool                  `json:"store_pass_events"`
	EffectiveMode             Mode                  `json:"effective_mode"`
	Strategy                  string                `json:"strategy"`
	WorkerCount               int                   `json:"worker_count"`
	QueueCapacity             int                   `json:"queue_capacity"`
	Scanners                  []string              `json:"scanners"`
	AllGroups                 bool                  `json:"all_groups"`
	GroupIDs                  []int64               `json:"group_ids"`
	Endpoints                 []PublicEndpoint      `json:"endpoints"`
	ConfigVersion             int64                 `json:"config_version"`
	UpdatedAt                 time.Time             `json:"updated_at"`
	UpdatedBy                 int64                 `json:"updated_by"`
	ChangeSummary             string                `json:"change_summary"`
	WarningEmailEnabled       bool                  `json:"warning_email_enabled"`
	WarningEmailWindowMinutes int                   `json:"warning_email_window_minutes"`
	AutoBanEnabled            bool                  `json:"auto_ban_enabled"`
	ViolationWindowHours      int                   `json:"violation_window_hours"`
	BanThreshold              int                   `json:"ban_threshold"`
}

type UpdateEndpoint struct {
	ID         string `json:"id" binding:"required"`
	Name       string `json:"name" binding:"required"`
	Protocol   string `json:"protocol"`
	BaseURL    string `json:"base_url" binding:"required"`
	Model      string `json:"model"`
	Token      string `json:"token,omitempty"`
	ClearToken bool   `json:"clear_token"`
	TimeoutMS  int    `json:"timeout_ms"`
	InputLimit int    `json:"input_limit"`
	Enabled    bool   `json:"enabled"`
}

type UpdateConfigRequest struct {
	ExpectedConfigVersion     int64                  `json:"expected_config_version" binding:"required"`
	Enabled                   bool                   `json:"enabled"`
	BlockingEnabled           bool                   `json:"blocking_enabled"`
	BlockingLatestTurnOnly    bool                   `json:"blocking_latest_turn_only"`
	KeywordBlockingEnabled    bool                   `json:"keyword_blocking_enabled"`
	BlockedKeywords           []string               `json:"blocked_keywords"`
	JailbreakKeywords         *[]string              `json:"jailbreak_keywords"`
	OtherKeywords             *[]string              `json:"other_keywords"`
	KeywordAllGroups          *bool                  `json:"keyword_all_groups"`
	KeywordGroupIDs           []int64                `json:"keyword_group_ids"`
	KeywordWhitelistKeys      *[]KeywordWhitelistKey `json:"keyword_whitelist_keys"`
	CBRNBlockingEnabled       *bool                  `json:"cbrn_blocking_enabled"`
	CBRNTopicRules            *[]string              `json:"cbrn_topic_rules"`
	CBRNIntentRules           *[]string              `json:"cbrn_intent_rules"`
	CombinationPolicies       *[]CombinationPolicy   `json:"combination_policies"`
	StorePassEvents           bool                   `json:"store_pass_events"`
	Strategy                  string                 `json:"strategy"`
	WorkerCount               int                    `json:"worker_count"`
	QueueCapacity             int                    `json:"queue_capacity"`
	Scanners                  []string               `json:"scanners"`
	AllGroups                 bool                   `json:"all_groups"`
	GroupIDs                  []int64                `json:"group_ids"`
	Endpoints                 []UpdateEndpoint       `json:"endpoints"`
	WarningEmailEnabled       *bool                  `json:"warning_email_enabled"`
	WarningEmailWindowMinutes *int                   `json:"warning_email_window_minutes"`
	AutoBanEnabled            *bool                  `json:"auto_ban_enabled"`
	ViolationWindowHours      *int                   `json:"violation_window_hours"`
	BanThreshold              *int                   `json:"ban_threshold"`
}

func DefaultStorageConfig() storageConfig {
	return storageConfig{
		Enabled:                   false,
		BlockingEnabled:           false,
		BlockingLatestTurnOnly:    false,
		KeywordBlockingEnabled:    false,
		BlockedKeywords:           []string{},
		JailbreakKeywords:         nil,
		OtherKeywords:             []string{},
		KeywordAllGroups:          true,
		KeywordGroupIDs:           []int64{},
		KeywordWhitelistKeys:      []KeywordWhitelistKey{},
		CBRNTopicRules:            defaultCBRNTopicRules(),
		CBRNIntentRules:           defaultCBRNIntentRules(),
		StorePassEvents:           false,
		Strategy:                  "priority",
		WorkerCount:               DefaultWorkerCount,
		QueueCapacity:             DefaultQueueCapacity,
		Scanners:                  append([]string(nil), AllScannerIDs...),
		AllGroups:                 true,
		GroupIDs:                  []int64{},
		Endpoints:                 []StorageEndpoint{},
		WarningEmailEnabled:       true,
		WarningEmailWindowMinutes: 5,
		AutoBanEnabled:            true,
		ViolationWindowHours:      24,
		BanThreshold:              10,
		ConfigVersion:             1,
	}
}

func ParseStorageConfig(raw string) (storageConfig, error) {
	cfg := DefaultStorageConfig()
	if strings.TrimSpace(raw) == "" {
		return cfg, nil
	}
	rawConfig := []byte(raw)
	if err := json.Unmarshal(rawConfig, &cfg); err != nil {
		return storageConfig{}, fmt.Errorf("decode prompt audit config: %w", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(rawConfig, &fields); err != nil {
		return storageConfig{}, fmt.Errorf("decode prompt audit config fields: %w", err)
	}
	_, hasKeywordAllGroups := fields["keyword_all_groups"]
	_, hasKeywordGroupIDs := fields["keyword_group_ids"]
	_, hasJailbreakKeywords := fields["jailbreak_keywords"]
	_, hasOtherKeywords := fields["other_keywords"]
	if !hasJailbreakKeywords {
		cfg.JailbreakKeywords = append([]string(nil), cfg.BlockedKeywords...)
	}
	if !hasOtherKeywords {
		cfg.OtherKeywords = []string{}
	}
	if !hasKeywordAllGroups && !hasKeywordGroupIDs {
		cfg.KeywordAllGroups = cfg.AllGroups
		cfg.KeywordGroupIDs = append([]int64(nil), cfg.GroupIDs...)
	}
	// Defaults apply to missing legacy fields only. Explicit zero values in a
	// stored document must retain the same rejection as an admin update.
	if _, ok := fields["warning_email_window_minutes"]; ok && (cfg.WarningEmailWindowMinutes < 1 || cfg.WarningEmailWindowMinutes > 60) {
		return storageConfig{}, infraerrors.BadRequest("prompt_audit_invalid_warning_email_window", "警告邮件窗口必须在 1 到 60 分钟之间")
	}
	if _, ok := fields["violation_window_hours"]; ok && (cfg.ViolationWindowHours < 1 || cfg.ViolationWindowHours > 720) {
		return storageConfig{}, infraerrors.BadRequest("prompt_audit_invalid_violation_window", "违规计数窗口必须在 1 到 720 小时之间")
	}
	if _, ok := fields["ban_threshold"]; ok && (cfg.BanThreshold < 1 || cfg.BanThreshold > 10000) {
		return storageConfig{}, infraerrors.BadRequest("prompt_audit_invalid_ban_threshold", "自动封号阈值必须在 1 到 10000 之间")
	}
	normalizeStorageConfig(&cfg)
	if err := validateCombinationPolicies(cfg.CombinationPolicies); err != nil {
		return storageConfig{}, err
	}
	migrateCombinationKeywords(&cfg)
	if err := validateStorageConfig(cfg); err != nil {
		return storageConfig{}, err
	}
	return cfg, nil
}

func normalizeStorageConfig(cfg *storageConfig) {
	if cfg == nil {
		return
	}
	if cfg.ConfigVersion < 1 {
		cfg.ConfigVersion = 1
	}
	if strings.TrimSpace(cfg.Strategy) == "" {
		cfg.Strategy = "priority"
	}
	if cfg.WorkerCount == 0 {
		cfg.WorkerCount = DefaultWorkerCount
	}
	if cfg.QueueCapacity == 0 {
		cfg.QueueCapacity = DefaultQueueCapacity
	}
	if len(cfg.Scanners) == 0 {
		cfg.Scanners = append([]string(nil), AllScannerIDs...)
	}
	cfg.Scanners = canonicalScannerIDs(cfg.Scanners)
	cfg.GroupIDs = canonicalInt64s(cfg.GroupIDs)
	cfg.KeywordGroupIDs = canonicalInt64s(cfg.KeywordGroupIDs)
	cfg.KeywordWhitelistKeys = canonicalKeywordWhitelistKeys(cfg.KeywordWhitelistKeys)
	normalizeLocalRuleConfig(cfg)
	cfg.CBRNTopicRules = canonicalBlockedKeywords(cfg.CBRNTopicRules)
	cfg.CBRNIntentRules = canonicalBlockedKeywords(cfg.CBRNIntentRules)
	normalizeCombinationPolicies(cfg)
	// Preserve an invalid blocking-without-audit combination so validation can
	// reject it instead of silently changing administrator intent.
	for i := range cfg.Endpoints {
		ep := &cfg.Endpoints[i]
		ep.ID = strings.TrimSpace(ep.ID)
		ep.Name = strings.TrimSpace(ep.Name)
		ep.Protocol = strings.TrimSpace(ep.Protocol)
		if ep.Protocol == "" {
			ep.Protocol = "openai_compatible"
		}
		ep.BaseURL = strings.TrimSpace(ep.BaseURL)
		ep.Model = strings.TrimSpace(ep.Model)
		if ep.Model == "" {
			ep.Model = DefaultGuardModel
		}
		if ep.TimeoutMS == 0 {
			ep.TimeoutMS = DefaultTimeoutMS
		}
		if ep.InputLimit == 0 {
			ep.InputLimit = DefaultInputLimit
		}
	}
}

func normalizeLocalRuleConfig(cfg *storageConfig) {
	// blocked_keywords is the legacy client/storage projection. Old rows are
	// promoted to jailbreak_keywords, while new rows keep the projection in
	// sync so older clients can continue to read and submit it.
	if cfg.JailbreakKeywords == nil {
		cfg.JailbreakKeywords = append([]string(nil), cfg.BlockedKeywords...)
	}
	cfg.JailbreakKeywords = canonicalBlockedKeywords(cfg.JailbreakKeywords)
	cfg.OtherKeywords = canonicalBlockedKeywords(cfg.OtherKeywords)
	cfg.BlockedKeywords = append([]string(nil), cfg.JailbreakKeywords...)
	if cfg.WarningEmailWindowMinutes == 0 {
		cfg.WarningEmailWindowMinutes = 5
	}
	if cfg.ViolationWindowHours == 0 {
		cfg.ViolationWindowHours = 24
	}
	if cfg.BanThreshold == 0 {
		cfg.BanThreshold = 10
	}
}

func validateStorageConfig(cfg storageConfig) error {
	normalizeLocalRuleConfig(&cfg)
	if err := validateKeywordWhitelistKeys(cfg.KeywordWhitelistKeys); err != nil {
		return err
	}
	if cfg.BlockingEnabled && !cfg.Enabled {
		return infraerrors.BadRequest(ErrorCodeRequiresEnabled, "开启同步阻止前必须先启用提示词审计")
	}
	if err := validateClassifiedKeywords(cfg.JailbreakKeywords, cfg.OtherKeywords); err != nil {
		return err
	}
	if cfg.KeywordBlockingEnabled && len(cfg.JailbreakKeywords) == 0 && len(cfg.OtherKeywords) == 0 {
		return infraerrors.BadRequest("prompt_audit_keywords_required", "开启关键字拦截前至少需要配置一个关键字")
	}
	if cfg.WarningEmailWindowMinutes < 1 || cfg.WarningEmailWindowMinutes > 60 {
		return infraerrors.BadRequest("prompt_audit_invalid_warning_email_window", "警告邮件窗口必须在 1 到 60 分钟之间")
	}
	if cfg.ViolationWindowHours < 1 || cfg.ViolationWindowHours > 720 {
		return infraerrors.BadRequest("prompt_audit_invalid_violation_window", "违规计数窗口必须在 1 到 720 小时之间")
	}
	if cfg.BanThreshold < 1 || cfg.BanThreshold > 10000 {
		return infraerrors.BadRequest("prompt_audit_invalid_ban_threshold", "自动封号阈值必须在 1 到 10000 之间")
	}
	if err := validateCombinationPolicies(cfg.effectiveCombinationPolicies()); err != nil {
		return err
	}
	if cfg.Strategy != "priority" {
		return infraerrors.BadRequest("prompt_audit_invalid_strategy", "提示词审计策略仅支持 priority")
	}
	if cfg.WorkerCount < 1 || cfg.WorkerCount > MaxWorkerCount {
		return infraerrors.BadRequest("prompt_audit_invalid_worker_count", "Worker 数量超出允许范围")
	}
	if cfg.QueueCapacity < 1 || cfg.QueueCapacity > MaxQueueCapacity {
		return infraerrors.BadRequest("prompt_audit_invalid_queue_capacity", "队列容量超出允许范围")
	}
	if !cfg.AllGroups && len(cfg.GroupIDs) == 0 {
		return infraerrors.BadRequest("prompt_audit_groups_required", "指定分组模式至少需要选择一个分组")
	}
	if (cfg.KeywordBlockingEnabled || combinationPoliciesEnabled(cfg.effectiveCombinationPolicies())) && !cfg.KeywordAllGroups && len(cfg.KeywordGroupIDs) == 0 {
		return infraerrors.BadRequest("prompt_audit_keyword_groups_required", "关键字拦截指定分组模式至少需要选择一个分组")
	}
	if len(cfg.Scanners) == 0 {
		return infraerrors.BadRequest("prompt_audit_scanners_required", "至少需要启用一个风险分类")
	}
	seen := make(map[string]struct{}, len(cfg.Endpoints))
	enabled := 0
	for _, ep := range cfg.Endpoints {
		if ep.ID == "" || ep.Name == "" {
			return infraerrors.BadRequest("prompt_audit_invalid_endpoint", "审计节点 ID 和名称不能为空")
		}
		if _, ok := seen[ep.ID]; ok {
			return infraerrors.BadRequest("prompt_audit_duplicate_endpoint", "审计节点 ID 不能重复")
		}
		seen[ep.ID] = struct{}{}
		if ep.Protocol != "openai_compatible" {
			return infraerrors.BadRequest("prompt_audit_invalid_endpoint_protocol", "审计节点仅支持 OpenAI 兼容协议")
		}
		if _, err := NormalizeBaseURL(ep.BaseURL); err != nil {
			return err
		}
		if ep.TimeoutMS < MinTimeoutMS || ep.TimeoutMS > MaxTimeoutMS {
			return infraerrors.BadRequest("prompt_audit_invalid_timeout", "审计节点超时超出允许范围")
		}
		if ep.InputLimit < MinInputLimit || ep.InputLimit > MaxInputLimit {
			return infraerrors.BadRequest("prompt_audit_invalid_input_limit", "审计节点输入上限超出允许范围")
		}
		if ep.Enabled {
			enabled++
		}
	}
	if cfg.Enabled && enabled == 0 {
		return infraerrors.BadRequest("prompt_audit_endpoint_required", "启用提示词审计前至少需要启用一个审计节点")
	}
	return nil
}

func validateUpdateConfigRequest(req UpdateConfigRequest) error {
	// Validate supplied lists now; validate their final merged combination under
	// the config lock, where omitted fields can be resolved against stored values.
	var cbrnTopics, cbrnIntents []string
	if req.CBRNTopicRules != nil {
		cbrnTopics = canonicalBlockedKeywords(*req.CBRNTopicRules)
	}
	if req.CBRNIntentRules != nil {
		cbrnIntents = canonicalBlockedKeywords(*req.CBRNIntentRules)
	}
	if req.CombinationPolicies == nil {
		if err := validateCBRNRules(cbrnTopics, cbrnIntents); err != nil {
			return err
		}
	}
	if req.KeywordWhitelistKeys != nil {
		if err := validateKeywordWhitelistKeys(*req.KeywordWhitelistKeys); err != nil {
			return err
		}
	}
	legacyKeywords := canonicalBlockedKeywords(req.BlockedKeywords)
	jailbreakKeywords := legacyKeywords
	if req.JailbreakKeywords != nil {
		jailbreakKeywords = canonicalBlockedKeywords(*req.JailbreakKeywords)
	}
	otherKeywords := []string(nil)
	if req.OtherKeywords != nil {
		otherKeywords = canonicalBlockedKeywords(*req.OtherKeywords)
	}
	if err := validateClassifiedKeywords(jailbreakKeywords, otherKeywords); err != nil {
		return err
	}
	if req.KeywordBlockingEnabled && len(jailbreakKeywords) == 0 && len(otherKeywords) == 0 {
		return infraerrors.BadRequest("prompt_audit_keywords_required", "开启关键字拦截前至少需要配置一个关键字")
	}
	if req.WarningEmailWindowMinutes != nil && (*req.WarningEmailWindowMinutes < 1 || *req.WarningEmailWindowMinutes > 60) {
		return infraerrors.BadRequest("prompt_audit_invalid_warning_email_window", "警告邮件窗口必须在 1 到 60 分钟之间")
	}
	if req.ViolationWindowHours != nil && (*req.ViolationWindowHours < 1 || *req.ViolationWindowHours > 720) {
		return infraerrors.BadRequest("prompt_audit_invalid_violation_window", "违规计数窗口必须在 1 到 720 小时之间")
	}
	if req.BanThreshold != nil && (*req.BanThreshold < 1 || *req.BanThreshold > 10000) {
		return infraerrors.BadRequest("prompt_audit_invalid_ban_threshold", "自动封号阈值必须在 1 到 10000 之间")
	}
	if strings.TrimSpace(req.Strategy) != "priority" {
		return infraerrors.BadRequest("prompt_audit_invalid_strategy", "提示词审计策略仅支持 priority")
	}
	if req.WorkerCount < 1 || req.WorkerCount > MaxWorkerCount {
		return infraerrors.BadRequest("prompt_audit_invalid_worker_count", "Worker 数量超出允许范围")
	}
	if req.QueueCapacity < 1 || req.QueueCapacity > MaxQueueCapacity {
		return infraerrors.BadRequest("prompt_audit_invalid_queue_capacity", "队列容量超出允许范围")
	}
	if len(req.Scanners) == 0 {
		return infraerrors.BadRequest("prompt_audit_scanners_required", "至少需要启用一个风险分类")
	}
	for _, scanner := range req.Scanners {
		if _, ok := ScannerCatalog[NormalizeCategory(scanner)]; !ok {
			return infraerrors.BadRequest("prompt_audit_invalid_scanner", "提示词审计风险分类无效")
		}
	}
	if !req.AllGroups {
		if len(req.GroupIDs) == 0 {
			return infraerrors.BadRequest("prompt_audit_groups_required", "指定分组模式至少需要选择一个分组")
		}
		for _, groupID := range req.GroupIDs {
			if groupID <= 0 {
				return infraerrors.BadRequest("prompt_audit_invalid_group", "提示词审计分组 ID 无效")
			}
		}
	}
	if req.KeywordAllGroups != nil && !*req.KeywordAllGroups {
		if len(req.KeywordGroupIDs) == 0 {
			return infraerrors.BadRequest("prompt_audit_keyword_groups_required", "关键字拦截指定分组模式至少需要选择一个分组")
		}
		for _, groupID := range req.KeywordGroupIDs {
			if groupID <= 0 {
				return infraerrors.BadRequest("prompt_audit_invalid_keyword_group", "关键字拦截分组 ID 无效")
			}
		}
	}
	for _, endpoint := range req.Endpoints {
		if endpoint.TimeoutMS < MinTimeoutMS || endpoint.TimeoutMS > MaxTimeoutMS {
			return infraerrors.BadRequest("prompt_audit_invalid_timeout", "审计节点超时超出允许范围")
		}
		if endpoint.InputLimit < MinInputLimit || endpoint.InputLimit > MaxInputLimit {
			return infraerrors.BadRequest("prompt_audit_invalid_input_limit", "审计节点输入上限超出允许范围")
		}
	}
	return nil
}

func (cfg ActiveConfig) EffectiveMode() Mode {
	if !cfg.RiskControlEnabled || !cfg.Enabled {
		return ModeOff
	}
	if cfg.BlockingEnabled {
		return ModeBlocking
	}
	return ModeAsync
}

func (cfg ActiveConfig) KeywordBlockingActive() bool {
	return cfg.RiskControlEnabled && cfg.KeywordBlockingEnabled &&
		((len(cfg.JailbreakKeywords) > 0 && cfg.jailbreakMatcher != nil) ||
			(len(cfg.BlockedKeywords) > 0 && cfg.keywordMatcher != nil) ||
			(len(cfg.OtherKeywords) > 0 && cfg.otherKeywordMatcher != nil))
}

func (cfg ActiveConfig) MatchJailbreakKeyword(text string) (rule, content string, matched bool) {
	matcher := cfg.jailbreakMatcher
	if matcher == nil {
		matcher = cfg.keywordMatcher
	}
	if !cfg.RiskControlEnabled || !cfg.KeywordBlockingEnabled || matcher == nil {
		return "", "", false
	}
	return matcher.Match(text)
}

func (cfg ActiveConfig) MatchOtherKeyword(text string) (rule, content string, matched bool) {
	if !cfg.RiskControlEnabled || !cfg.KeywordBlockingEnabled || cfg.otherKeywordMatcher == nil {
		return "", "", false
	}
	return cfg.otherKeywordMatcher.Match(text)
}

func (cfg ActiveConfig) MatchBlockedKeyword(text string) (rule, content string, matched bool) {
	return cfg.MatchJailbreakKeyword(text)
}

func (cfg ActiveConfig) CBRNBlockingActive() bool {
	if !cfg.RiskControlEnabled {
		return false
	}
	for _, policy := range cfg.CombinationPolicies {
		if policy.ID == "cbrn" {
			return policy.Enabled && policy.matcher != nil
		}
	}
	return false
}

func (cfg ActiveConfig) MatchCBRN(req Request) (*CBRNMatch, error) {
	if !cfg.RiskControlEnabled {
		return nil, nil
	}
	for _, policy := range cfg.CombinationPolicies {
		if policy.ID == "cbrn" && policy.Enabled && policy.matcher != nil {
			return policy.matcher.Match(req)
		}
	}
	return nil, nil
}

func (cfg storageConfig) synchronousBlockingEnabled() bool {
	return (cfg.Enabled && cfg.BlockingEnabled) || cfg.KeywordBlockingEnabled || combinationPoliciesEnabled(cfg.effectiveCombinationPolicies())
}

func (cfg ActiveConfig) IncludesGroup(groupID *int64) bool {
	return includesGroup(cfg.AllGroups, cfg.GroupIDs, groupID)
}

func (cfg ActiveConfig) KeywordIncludesGroup(groupID *int64) bool {
	return includesGroup(cfg.KeywordAllGroups, cfg.KeywordGroupIDs, groupID)
}

func includesGroup(allGroups bool, groupIDs []int64, groupID *int64) bool {
	if allGroups {
		return true
	}
	if groupID == nil {
		return false
	}
	i := sort.Search(len(groupIDs), func(i int) bool { return groupIDs[i] >= *groupID })
	return i < len(groupIDs) && groupIDs[i] == *groupID
}

func (cfg ActiveConfig) EnabledEndpoints() []ActiveEndpoint {
	result := make([]ActiveEndpoint, 0, len(cfg.Endpoints))
	for _, ep := range cfg.Endpoints {
		if ep.Enabled {
			result = append(result, ep)
		}
	}
	return result
}

// InvalidTokenEndpointIDs lists endpoints whose stored token could not be
// decrypted with the current encryption key.
func (cfg ActiveConfig) InvalidTokenEndpointIDs() []string {
	ids := make([]string, 0)
	for _, ep := range cfg.Endpoints {
		if ep.TokenInvalid {
			ids = append(ids, ep.ID)
		}
	}
	return ids
}

func PublicFromStorage(cfg storageConfig, riskControlEnabled bool, invalidTokenEndpointIDs []string) PublicConfig {
	normalizeLocalRuleConfig(&cfg)
	normalizeCombinationPolicies(&cfg)
	invalid := make(map[string]struct{}, len(invalidTokenEndpointIDs))
	for _, id := range invalidTokenEndpointIDs {
		invalid[id] = struct{}{}
	}
	scanners := append([]string{}, cfg.Scanners...)
	groupIDs := append([]int64{}, cfg.GroupIDs...)
	keywordGroupIDs := append([]int64{}, cfg.KeywordGroupIDs...)
	endpoints := make([]PublicEndpoint, 0, len(cfg.Endpoints))
	for _, ep := range cfg.Endpoints {
		hasToken := strings.TrimSpace(ep.TokenCiphertext) != ""
		status := "missing"
		if hasToken {
			status = "configured"
			if _, ok := invalid[ep.ID]; ok {
				status = "invalid"
			}
		}
		endpoints = append(endpoints, PublicEndpoint{
			ID: ep.ID, Name: ep.Name, Protocol: ep.Protocol, BaseURL: ep.BaseURL,
			Model: ep.Model, TimeoutMS: ep.TimeoutMS, InputLimit: ep.InputLimit,
			Enabled: ep.Enabled, HasToken: hasToken, TokenStatus: status,
		})
	}
	active := ActiveConfig{RiskControlEnabled: riskControlEnabled, Enabled: cfg.Enabled, BlockingEnabled: cfg.BlockingEnabled}
	return PublicConfig{
		Enabled: cfg.Enabled, BlockingEnabled: cfg.BlockingEnabled, BlockingLatestTurnOnly: cfg.BlockingLatestTurnOnly,
		KeywordBlockingEnabled: cfg.KeywordBlockingEnabled,
		KeywordBlockingActive:  riskControlEnabled && cfg.KeywordBlockingEnabled && (len(cfg.JailbreakKeywords) > 0 || len(cfg.OtherKeywords) > 0),
		BlockedKeywords:        append([]string(nil), cfg.JailbreakKeywords...),
		JailbreakKeywords:      append([]string{}, cfg.JailbreakKeywords...),
		OtherKeywords:          append([]string{}, cfg.OtherKeywords...),
		KeywordAllGroups:       cfg.KeywordAllGroups, KeywordGroupIDs: keywordGroupIDs, StorePassEvents: cfg.StorePassEvents,
		KeywordWhitelistKeys: append([]KeywordWhitelistKey{}, cfg.KeywordWhitelistKeys...),
		CBRNBlockingEnabled:  cfg.CBRNBlockingEnabled,
		CBRNBlockingActive:   riskControlEnabled && cfg.CBRNBlockingEnabled && len(cfg.CBRNTopicRules) > 0 && len(cfg.CBRNIntentRules) > 0,
		CBRNTopicRules:       append([]string{}, cfg.CBRNTopicRules...), CBRNIntentRules: append([]string{}, cfg.CBRNIntentRules...),
		CombinationPolicies: cloneCombinationPolicies(cfg.CombinationPolicies), CombinationTemplates: combinationTemplates(),
		CombinationBlockingActive: riskControlEnabled && combinationPoliciesEnabled(cfg.CombinationPolicies),
		EffectiveMode:             active.EffectiveMode(), Strategy: cfg.Strategy, WorkerCount: cfg.WorkerCount,
		QueueCapacity: cfg.QueueCapacity, Scanners: scanners, AllGroups: cfg.AllGroups,
		GroupIDs: groupIDs, Endpoints: endpoints, ConfigVersion: cfg.ConfigVersion,
		WarningEmailEnabled: cfg.WarningEmailEnabled, WarningEmailWindowMinutes: cfg.WarningEmailWindowMinutes,
		AutoBanEnabled: cfg.AutoBanEnabled, ViolationWindowHours: cfg.ViolationWindowHours, BanThreshold: cfg.BanThreshold,
		UpdatedAt: cfg.UpdatedAt, UpdatedBy: cfg.UpdatedBy, ChangeSummary: cfg.ChangeSummary,
	}
}

func ActiveFromStorage(cfg storageConfig, riskControlEnabled bool, encryptor SecretEncryptor) (ActiveConfig, error) {
	normalizeLocalRuleConfig(&cfg)
	normalizeCombinationPolicies(&cfg)
	if err := validateClassifiedKeywords(cfg.JailbreakKeywords, cfg.OtherKeywords); err != nil {
		return ActiveConfig{}, err
	}
	jailbreakMatcher, err := newPromptKeywordMatcher(cfg.JailbreakKeywords)
	if err != nil {
		return ActiveConfig{}, err
	}
	otherMatcher, err := newPromptKeywordMatcher(cfg.OtherKeywords)
	if err != nil {
		return ActiveConfig{}, err
	}
	if err := validateCombinationPolicies(cfg.CombinationPolicies); err != nil {
		return ActiveConfig{}, err
	}
	policies := make([]ActiveCombinationPolicy, 0, len(cfg.CombinationPolicies))
	for _, policy := range cfg.CombinationPolicies {
		matcher, err := newPromptCombinationMatcher(policy.TopicRules, policy.IntentRules)
		if err != nil {
			return ActiveConfig{}, err
		}
		policies = append(policies, ActiveCombinationPolicy{CombinationPolicy: cloneCombinationPolicy(policy), matcher: matcher})
	}
	active := ActiveConfig{
		RiskControlEnabled: riskControlEnabled, Enabled: cfg.Enabled, BlockingEnabled: cfg.BlockingEnabled,
		BlockingLatestTurnOnly: cfg.BlockingLatestTurnOnly,
		KeywordBlockingEnabled: cfg.KeywordBlockingEnabled,
		BlockedKeywords:        append([]string(nil), cfg.JailbreakKeywords...),
		JailbreakKeywords:      append([]string(nil), cfg.JailbreakKeywords...),
		OtherKeywords:          append([]string(nil), cfg.OtherKeywords...),
		KeywordAllGroups:       cfg.KeywordAllGroups,
		KeywordGroupIDs:        append([]int64(nil), cfg.KeywordGroupIDs...),
		KeywordWhitelistKeys:   append([]KeywordWhitelistKey{}, cfg.KeywordWhitelistKeys...),
		CBRNBlockingEnabled:    cfg.CBRNBlockingEnabled,
		CBRNTopicRules:         append([]string{}, cfg.CBRNTopicRules...), CBRNIntentRules: append([]string{}, cfg.CBRNIntentRules...),
		CombinationPolicies: policies,
		StorePassEvents:     cfg.StorePassEvents, Strategy: cfg.Strategy, WorkerCount: cfg.WorkerCount,
		QueueCapacity: cfg.QueueCapacity, Scanners: append([]string(nil), cfg.Scanners...), AllGroups: cfg.AllGroups,
		GroupIDs: append([]int64(nil), cfg.GroupIDs...), ConfigVersion: cfg.ConfigVersion,
		WarningEmailEnabled: cfg.WarningEmailEnabled, WarningEmailWindowMinutes: cfg.WarningEmailWindowMinutes,
		AutoBanEnabled: cfg.AutoBanEnabled, ViolationWindowHours: cfg.ViolationWindowHours, BanThreshold: cfg.BanThreshold,
		UpdatedAt: cfg.UpdatedAt, UpdatedBy: cfg.UpdatedBy, ChangeSummary: cfg.ChangeSummary,
		Endpoints:      make([]ActiveEndpoint, 0, len(cfg.Endpoints)),
		keywordMatcher: jailbreakMatcher, jailbreakMatcher: jailbreakMatcher, otherKeywordMatcher: otherMatcher,
	}
	for _, ep := range cfg.Endpoints {
		token := ""
		tokenInvalid := false
		if ep.TokenCiphertext != "" {
			if encryptor == nil {
				return ActiveConfig{}, fmt.Errorf("prompt audit secret encryptor unavailable")
			}
			plain, err := encryptor.Decrypt(ep.TokenCiphertext)
			if err != nil {
				// An undecryptable token (encryption key changed or regenerated)
				// must not take the whole config down: admins would otherwise be
				// locked out of the real config version and unable to recover
				// (issue #4887). Keep the ciphertext persisted, but exclude the
				// endpoint from runtime use until the token is re-entered.
				tokenInvalid = true
			} else {
				token = plain
			}
		}
		active.Endpoints = append(active.Endpoints, ActiveEndpoint{
			ID: ep.ID, Name: ep.Name, Protocol: ep.Protocol, BaseURL: ep.BaseURL, Model: ep.Model,
			Token: token, TimeoutMS: ep.TimeoutMS, InputLimit: ep.InputLimit,
			Enabled: ep.Enabled && !tokenInvalid, TokenInvalid: tokenInvalid,
		})
	}
	return active, nil
}

func changeSummary(cfg storageConfig) string {
	summary := struct {
		Enabled                  bool   `json:"enabled"`
		BlockingEnabled          bool   `json:"blocking_enabled"`
		BlockingLatestTurnOnly   bool   `json:"blocking_latest_turn_only"`
		KeywordBlockingEnabled   bool   `json:"keyword_blocking_enabled"`
		KeywordCount             int    `json:"keyword_count"`
		KeywordHash              string `json:"keyword_hash"`
		JailbreakKeywordCount    int    `json:"jailbreak_keyword_count"`
		JailbreakKeywordHash     string `json:"jailbreak_keyword_hash"`
		OtherKeywordCount        int    `json:"other_keyword_count"`
		OtherKeywordHash         string `json:"other_keyword_hash"`
		WarningEmailEnabled      bool   `json:"warning_email_enabled"`
		WarningEmailWindow       int    `json:"warning_email_window_minutes"`
		AutoBanEnabled           bool   `json:"auto_ban_enabled"`
		ViolationWindow          int    `json:"violation_window_hours"`
		BanThreshold             int    `json:"ban_threshold"`
		KeywordAllGroups         bool   `json:"keyword_all_groups"`
		KeywordGroupCount        int    `json:"keyword_group_count"`
		KeywordGroupHash         string `json:"keyword_group_hash"`
		KeywordWhitelistKeyCount int    `json:"keyword_whitelist_key_count"`
		CBRNBlockingEnabled      bool   `json:"cbrn_blocking_enabled"`
		CBRNTopicRuleCount       int    `json:"cbrn_topic_rule_count"`
		CBRNIntentRuleCount      int    `json:"cbrn_intent_rule_count"`
		CBRNTopicRuleHash        string `json:"cbrn_topic_rule_hash"`
		CBRNIntentRuleHash       string `json:"cbrn_intent_rule_hash"`
		CombinationPolicyCount   int    `json:"combination_policy_count"`
		CombinationPolicyHash    string `json:"combination_policy_hash"`
		StorePassEvents          bool   `json:"store_pass_events"`
		EndpointCount            int    `json:"endpoint_count"`
		ScannerCount             int    `json:"scanner_count"`
		AllGroups                bool   `json:"all_groups"`
		GroupCount               int    `json:"group_count"`
		GroupHash                string `json:"group_hash"`
	}{
		Enabled: cfg.Enabled, BlockingEnabled: cfg.BlockingEnabled, BlockingLatestTurnOnly: cfg.BlockingLatestTurnOnly,
		KeywordBlockingEnabled: cfg.KeywordBlockingEnabled, KeywordCount: len(cfg.JailbreakKeywords) + len(cfg.OtherKeywords),
		JailbreakKeywordCount: len(cfg.JailbreakKeywords), JailbreakKeywordHash: blockedKeywordsHash(cfg.JailbreakKeywords),
		OtherKeywordCount: len(cfg.OtherKeywords), OtherKeywordHash: blockedKeywordsHash(cfg.OtherKeywords),
		WarningEmailEnabled: cfg.WarningEmailEnabled, WarningEmailWindow: cfg.WarningEmailWindowMinutes,
		AutoBanEnabled: cfg.AutoBanEnabled, ViolationWindow: cfg.ViolationWindowHours, BanThreshold: cfg.BanThreshold,
		KeywordAllGroups: cfg.KeywordAllGroups, KeywordGroupCount: len(cfg.KeywordGroupIDs), KeywordWhitelistKeyCount: len(cfg.KeywordWhitelistKeys),
		CBRNBlockingEnabled: cfg.CBRNBlockingEnabled, CBRNTopicRuleCount: len(cfg.CBRNTopicRules), CBRNIntentRuleCount: len(cfg.CBRNIntentRules),
		CBRNTopicRuleHash: blockedKeywordsHash(cfg.CBRNTopicRules), CBRNIntentRuleHash: blockedKeywordsHash(cfg.CBRNIntentRules),
		CombinationPolicyCount: len(cfg.CombinationPolicies), CombinationPolicyHash: combinationPoliciesHash(cfg.CombinationPolicies),
		StorePassEvents: cfg.StorePassEvents, EndpointCount: len(cfg.Endpoints), ScannerCount: len(cfg.Scanners), AllGroups: cfg.AllGroups, GroupCount: len(cfg.GroupIDs),
	}
	summary.KeywordHash = blockedKeywordsHash(cfg.JailbreakKeywords)
	summary.KeywordGroupHash = groupIDsHash(cfg.KeywordGroupIDs)
	summary.GroupHash = groupIDsHash(cfg.GroupIDs)
	raw, _ := json.Marshal(summary)
	return string(raw)
}

func canonicalInt64s(values []int64) []int64 {
	seen := make(map[int64]struct{}, len(values))
	result := make([]int64, 0, len(values))
	for _, value := range values {
		if value <= 0 {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func canonicalScannerIDs(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		id := NormalizeCategory(value)
		if _, ok := ScannerCatalog[id]; ok {
			seen[id] = struct{}{}
		}
	}
	result := make([]string, 0, len(seen))
	for _, id := range AllScannerIDs {
		if _, ok := seen[id]; ok {
			result = append(result, id)
		}
	}
	return result
}

func canonicalBlockedKeywords(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		keyword := strings.TrimSpace(value)
		if keyword == "" {
			continue
		}
		key := "literal:" + strings.ToLower(keyword)
		if strings.HasPrefix(keyword, promptKeywordRegexPrefix) {
			key = "regex:" + keyword
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, keyword)
	}
	return result
}

func blockedKeywordsHash(values []string) string {
	rawKeywords, _ := json.Marshal(canonicalBlockedKeywords(values))
	digest := sha256.Sum256(rawKeywords)
	return hex.EncodeToString(digest[:])
}

func groupIDsHash(values []int64) string {
	rawGroups, _ := json.Marshal(canonicalInt64s(values))
	digest := sha256.Sum256(rawGroups)
	return hex.EncodeToString(digest[:])
}

func validateBlockedKeywords(values []string) error {
	if len(values) > MaxBlockedKeywords {
		return infraerrors.BadRequest("prompt_audit_too_many_keywords", "提示词审计关键字数量超出允许范围")
	}
	for index, keyword := range values {
		if strings.HasPrefix(keyword, promptKeywordRegexPrefix) {
			if _, err := compilePromptKeywordRegex(keyword, index); err != nil {
				return err
			}
			continue
		}
		if utf8.RuneCountInString(keyword) > MaxBlockedKeywordRunes {
			return infraerrors.BadRequest("prompt_audit_keyword_too_long", "提示词审计关键字长度超出允许范围")
		}
	}
	return nil
}

func validateClassifiedKeywords(jailbreak, other []string) error {
	if len(jailbreak)+len(other) > MaxBlockedKeywords {
		return infraerrors.BadRequest("prompt_audit_too_many_keywords", "提示词审计关键字总数量超出允许范围")
	}
	if err := validateBlockedKeywords(jailbreak); err != nil {
		return err
	}
	return validateBlockedKeywords(other)
}
