package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
)

const AttributionDefaultModel = "gpt-6-astra"
const InitialPelicanDefaultModel = "gpt-6.1-sol"

var (
	ErrAttributionInvalid  = errors.New("invalid attribution configuration")
	ErrAttributionConflict = errors.New("attribution configuration changed")
	ErrAttributionNotFound = errors.New("attribution result not found")
	ErrAttributionDisabled = errors.New("attribution detection is disabled")
)

type AttributionPolicy struct {
	Model      string   `json:"model"`
	HighModels []string `json:"high_models"`
	LowModels  []string `json:"low_models"`
}
type AttributionGroupPolicy struct {
	GroupID int64 `json:"group_id"`
	Enabled bool  `json:"enabled"`
	AttributionPolicy
}
type NewAccountTestConfig struct {
	Attribution      bool   `json:"attribution"`
	Pelican          bool   `json:"pelican"`
	AttributionModel string `json:"attribution_model"`
	PelicanModel     string `json:"pelican_model"`
}
type AttributionConfig struct {
	Version         int64                    `json:"version"`
	Enabled         bool                     `json:"enabled"`
	BaseURL         string                   `json:"base_url"`
	Default         AttributionPolicy        `json:"default"`
	Groups          []AttributionGroupPolicy `json:"groups"`
	GroupPriority   []int64                  `json:"group_priority"`
	NewAccountTests NewAccountTestConfig     `json:"new_account_tests"`
}

func DefaultAttributionConfig() AttributionConfig {
	return AttributionConfig{Version: 1, Default: AttributionPolicy{Model: AttributionDefaultModel, HighModels: []string{}, LowModels: []string{}}, Groups: []AttributionGroupPolicy{}, GroupPriority: []int64{}, NewAccountTests: NewAccountTestConfig{Attribution: true, Pelican: true, AttributionModel: AttributionDefaultModel, PelicanModel: InitialPelicanDefaultModel}}
}

func AttributionBaseURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("%w: ModelTrace 地址须为 HTTP(S) 服务地址，不含凭据、查询参数或片段", ErrAttributionInvalid)
	}
	return strings.TrimRight(u.String(), "/"), nil
}

func NormalizeAttributionConfig(c AttributionConfig) (AttributionConfig, error) {
	if c.Version < 1 || len(c.Groups) > 1000 {
		return c, ErrAttributionInvalid
	}
	if len(c.GroupPriority) > 1000 {
		return c, ErrAttributionInvalid
	}
	prioritySeen := map[int64]bool{}
	if c.GroupPriority == nil {
		c.GroupPriority = []int64{}
	}
	for _, id := range c.GroupPriority {
		if id < 1 || prioritySeen[id] {
			return c, ErrAttributionInvalid
		}
		prioritySeen[id] = true
	}
	var err error
	for _, field := range []struct {
		model    *string
		fallback string
	}{
		{&c.NewAccountTests.AttributionModel, AttributionDefaultModel},
		{&c.NewAccountTests.PelicanModel, InitialPelicanDefaultModel},
	} {
		*field.model = strings.TrimSpace(*field.model)
		if *field.model == "" {
			*field.model = field.fallback
		}
		if len(*field.model) > 200 || strings.ContainsAny(*field.model, "*\r\n\t ") {
			return c, fmt.Errorf("%w: 首测模型须为具体模型 ID", ErrAttributionInvalid)
		}
	}
	if c.Enabled || strings.TrimSpace(c.BaseURL) != "" {
		c.BaseURL, err = AttributionBaseURL(c.BaseURL)
		if err != nil {
			return c, err
		}
	}
	normalize := func(p *AttributionPolicy, required bool) error {
		p.Model = strings.TrimSpace(p.Model)
		if p.Model == "" {
			p.Model = AttributionDefaultModel
		}
		valid := func(s string) bool { return len(s) > 0 && len(s) <= 200 && !strings.ContainsAny(s, "*\r\n\t ") }
		if !valid(p.Model) {
			return fmt.Errorf("%w: 探针模型须为具体模型 ID", ErrAttributionInvalid)
		}
		for _, list := range []*[]string{&p.HighModels, &p.LowModels} {
			out := []string{}
			seen := map[string]bool{}
			if len(*list) > 500 {
				return ErrAttributionInvalid
			}
			for _, id := range *list {
				id = strings.TrimSpace(id)
				if !valid(id) {
					return fmt.Errorf("%w: 白名单须为具体模型 ID", ErrAttributionInvalid)
				}
				if !seen[id] {
					out = append(out, id)
					seen[id] = true
				}
			}
			if required && len(out) == 0 {
				return fmt.Errorf("%w: 开启前必须配置高级和低级白名单", ErrAttributionInvalid)
			}
			*list = out
		}
		return nil
	}
	if err = normalize(&c.Default, c.Enabled); err != nil {
		return c, err
	}
	seen := map[int64]bool{}
	if c.Groups == nil {
		c.Groups = []AttributionGroupPolicy{}
	}
	for i := range c.Groups {
		g := &c.Groups[i]
		if g.GroupID < 1 || seen[g.GroupID] {
			return c, ErrAttributionInvalid
		}
		seen[g.GroupID] = true
		if err = normalize(&g.AttributionPolicy, g.Enabled); err != nil {
			return c, err
		}
	}
	return c, nil
}

func ResolveAttributionPolicy(c AttributionConfig, groups []AccountGroup) (AttributionPolicy, int64) {
	ordered := append([]AccountGroup(nil), groups...)
	rank := make(map[int64]int, len(c.GroupPriority))
	for i, id := range c.GroupPriority {
		rank[id] = i
	}
	sort.Slice(ordered, func(i, j int) bool {
		a, aSet := rank[ordered[i].GroupID]
		b, bSet := rank[ordered[j].GroupID]
		if aSet != bSet {
			return aSet
		}
		if aSet && a != b {
			return a < b
		}
		if ordered[i].Priority != ordered[j].Priority {
			return ordered[i].Priority < ordered[j].Priority
		}
		return ordered[i].GroupID < ordered[j].GroupID
	})
	if len(ordered) == 0 {
		return c.Default, 0
	}
	// Choose the group first. Inheriting global settings must not cause a
	// higher-priority group to lose to a lower-priority independent policy.
	groupID := ordered[0].GroupID
	for _, p := range c.Groups {
		if p.Enabled && p.GroupID == groupID {
			return p.AttributionPolicy, groupID
		}
	}
	return c.Default, groupID
}

func AttributionSkipReason(a *Account, now time.Time, manual bool) string {
	if a == nil {
		return "account_missing"
	}
	if !a.IsOpenAIOAuth() && !(manual && a.Platform == PlatformOpenAI && a.Type == AccountTypeAPIKey) {
		return "unsupported_account"
	}
	// Explicit administrator probes do not participate in automatic scheduling.
	// Existence and authorization are still checked at the transport boundary.
	if manual {
		return ""
	}
	if a.IsShadow() {
		return "shadow_account"
	}
	if a.IsOpenAIOAuth() && a.IsOpenAIPassthroughEnabled() {
		return "passthrough_account"
	}
	if a.Status != StatusActive {
		return "account_inactive"
	}
	if !a.Schedulable {
		return "scheduling_disabled"
	}
	if a.ExpiresAt != nil && !now.Before(*a.ExpiresAt) {
		return "account_expired"
	}
	if AccountDiagnosticRateLimited(a, "", now) {
		return ErrDiagnosticRateLimited.Error()
	}
	return ""
}

// AttributionDigest fences authorization/configuration snapshots. Tokens never
// leave memory; only a one-way digest is retained for static authentication.
func AttributionDigest(value any) string {
	b, _ := json.Marshal(value)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// Preserve non-identity aliases, including wildcard aliases. Aliases take
// precedence over new identity entries, exactly as in the account editor.
func AttributionModelMapping(current map[string]any, models []string) map[string]any {
	out := make(map[string]any, len(current)+len(models))
	for _, id := range models {
		out[id] = id
	}
	for from, target := range current {
		if text, ok := target.(string); !ok || from != text || strings.Contains(from, "*") {
			out[from] = target
		}
	}
	return out
}

type AttributionCandidate struct {
	Model       string  `json:"model"`
	Probability float64 `json:"probability"`
}
type AttributionDiagnostic struct {
	Index          int  `json:"index"`
	ParsedNumbers  int  `json:"parsed_numbers"`
	MinimumNumbers int  `json:"minimum_numbers"`
	Accepted       bool `json:"accepted"`
}
type AttributionAnalysis struct {
	Prediction  string                  `json:"prediction"`
	Probability float64                 `json:"probability"`
	UsedOutputs int                     `json:"used_outputs"`
	Results     []AttributionCandidate  `json:"results"`
	Diagnostics []AttributionDiagnostic `json:"diagnostics"`
}
type AttributionSnapshot struct {
	ConfigVersion int64             `json:"config_version"`
	GroupID       int64             `json:"group_id"`
	Policy        AttributionPolicy `json:"policy"`
	Authorization string            `json:"-"`
	Fence         string            `json:"-"`
}
type AttributionResult struct {
	Analysis       *AttributionAnalysis `json:"analysis,omitempty"`
	Usage          []*OpenAIUsage       `json:"usage,omitempty"`
	ActualModels   []string             `json:"actual_models,omitempty"`
	UpstreamModels []string             `json:"upstream_models,omitempty"`
	DurationMS     int64                `json:"duration_ms"`
	Retries        int                  `json:"retries,omitempty"`
	Action         string               `json:"action"`
	PassStreak     int                  `json:"pass_streak,omitempty"`
	Before         map[string]any       `json:"before,omitempty"`
	After          map[string]any       `json:"after,omitempty"`
}
type AttributionJob struct {
	ID          int64               `json:"id"`
	AccountID   int64               `json:"account_id"`
	AccountName string              `json:"account_name"`
	Source      string              `json:"source"`
	Status      string              `json:"status"`
	Reason      string              `json:"reason,omitempty"`
	Snapshot    AttributionSnapshot `json:"snapshot"`
	Result      AttributionResult   `json:"result"`
	CreatedAt   time.Time           `json:"created_at"`
	StartedAt   *time.Time          `json:"started_at,omitempty"`
	FinishedAt  *time.Time          `json:"finished_at,omitempty"`
	Claim       string              `json:"-"`
}
type AttributionSummary struct {
	Latest     *AttributionJob `json:"latest,omitempty"`
	Active     *AttributionJob `json:"active,omitempty"`
	SkipReason string          `json:"skip_reason,omitempty"`
}
type AttributionPage struct {
	Items    []*AttributionJob `json:"items"`
	Total    int               `json:"total"`
	Page     int               `json:"page"`
	PageSize int               `json:"page_size"`
	Queued   int               `json:"queued"`
	Running  int               `json:"running"`
}

type AttributionRepository interface {
	Config(context.Context) (AttributionConfig, error)
	SaveConfig(context.Context, AttributionConfig) (AttributionConfig, error)
	Enqueue(context.Context, []int64, bool, ...string) ([]*AttributionJob, error)
	EnqueueNewAccounts(context.Context) error
	Claim(context.Context) (*AttributionJob, error)
	Heartbeat(context.Context, *AttributionJob) (bool, error)
	Validate(context.Context, *AttributionJob) (bool, error)
	Finish(context.Context, *AttributionJob) error
	List(context.Context, int64, int, int) (*AttributionPage, error)
	Get(context.Context, int64) (*AttributionJob, error)
	Summaries(context.Context, []int64) (map[int64]*AttributionSummary, error)
}
