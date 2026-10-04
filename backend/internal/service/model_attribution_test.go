package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func validAttributionAnalysis() *AttributionAnalysis {
	return &AttributionAnalysis{Prediction: AttributionDefaultModel, Probability: 0.8, UsedOutputs: 3, Results: []AttributionCandidate{{AttributionDefaultModel, 0.8}, {"gpt-6-luna", 0.2}}, Diagnostics: []AttributionDiagnostic{{0, 300, 165, true}, {1, 300, 165, true}, {2, 300, 165, true}}}
}
func TestAttributionConfigSelectionAndMapping(t *testing.T) {
	c := DefaultAttributionConfig()
	require.False(t, c.Enabled)
	require.Equal(t, AttributionDefaultModel, c.Default.Model)
	require.Equal(t, NewAccountTestConfig{Attribution: true, Pelican: true, Model: AttributionDefaultModel}, c.NewAccountTests)
	c.Enabled = true
	_, err := NormalizeAttributionConfig(c)
	require.ErrorIs(t, err, ErrAttributionInvalid)
	c.BaseURL = "http://modeltrace:5000/"
	c.Default.HighModels = []string{"gpt-6-astra", "gpt-6-astra"}
	c.Default.LowModels = []string{"gpt-6-luna"}
	c, err = NormalizeAttributionConfig(c)
	require.NoError(t, err)
	require.Len(t, c.Default.HighModels, 1)
	c.Groups = []AttributionGroupPolicy{{GroupID: 2, Enabled: true, AttributionPolicy: AttributionPolicy{Model: "group-2"}}, {GroupID: 1, Enabled: true, AttributionPolicy: AttributionPolicy{Model: "group-1"}}, {GroupID: 3, Enabled: false}}
	p, id := ResolveAttributionPolicy(c, []AccountGroup{{GroupID: 3, Priority: 0}, {GroupID: 2, Priority: 1}, {GroupID: 1, Priority: 1}})
	require.EqualValues(t, 1, id)
	require.Equal(t, "group-1", p.Model)
	p, id = ResolveAttributionPolicy(c, []AccountGroup{{GroupID: 2, Priority: 0}, {GroupID: 1, Priority: 1}})
	require.EqualValues(t, 2, id)
	require.Equal(t, "group-2", p.Model)
	p, id = ResolveAttributionPolicy(c, nil)
	require.Zero(t, id)
	require.Equal(t, c.Default, p)
	current := map[string]any{"old": "old", "alias": "target", "*": "fallback", "new": "custom", "wild*": "wild*"}
	require.Equal(t, map[string]any{"new": "custom", "low": "low", "alias": "target", "*": "fallback", "wild*": "wild*"}, AttributionModelMapping(current, []string{"new", "low"}))
	require.Equal(t, "old", current["old"])
	for _, url := range []string{"file:///etc/passwd", "https://user:secret@host", "https://host?key=secret", "https://host#secret"} {
		_, err = AttributionBaseURL(url)
		require.ErrorIs(t, err, ErrAttributionInvalid)
	}
}

func TestAttributionNewAccountConfig(t *testing.T) {
	c := DefaultAttributionConfig()
	c.NewAccountTests = NewAccountTestConfig{Model: " gpt-6-sol "}
	got, err := NormalizeAttributionConfig(c)
	require.NoError(t, err)
	require.Equal(t, "gpt-6-sol", got.NewAccountTests.Model)
	require.False(t, got.NewAccountTests.Attribution)
	require.False(t, got.NewAccountTests.Pelican)
	for _, model := range []string{"gpt-*", "two models", "gpt\n6", strings.Repeat("a", 201)} {
		c.NewAccountTests.Model = model
		_, err = NormalizeAttributionConfig(c)
		require.ErrorIs(t, err, ErrAttributionInvalid)
	}
	c.NewAccountTests.Model = ""
	got, err = NormalizeAttributionConfig(c)
	require.NoError(t, err)
	require.Equal(t, AttributionDefaultModel, got.NewAccountTests.Model)
}

func TestAttributionGlobalGroupPriority(t *testing.T) {
	c := DefaultAttributionConfig()
	c.Groups = []AttributionGroupPolicy{
		{GroupID: 1, Enabled: true, AttributionPolicy: AttributionPolicy{Model: "first", HighModels: []string{"high-1"}, LowModels: []string{"low-1"}}},
		{GroupID: 2, Enabled: true, AttributionPolicy: AttributionPolicy{Model: "second", HighModels: []string{"high-2"}, LowModels: []string{"low-2"}}},
		{GroupID: 3, Enabled: false},
	}
	c.GroupPriority = []int64{3, 2, 1}
	groups := []AccountGroup{{GroupID: 1, Priority: -10}, {GroupID: 2, Priority: 10}, {GroupID: 3, Priority: -20}}
	policy, id := ResolveAttributionPolicy(c, groups)
	require.EqualValues(t, 2, id)
	require.Equal(t, []string{"high-2"}, policy.HighModels)
	require.Equal(t, []string{"low-2"}, policy.LowModels)
	c.GroupPriority = []int64{1}
	_, id = ResolveAttributionPolicy(c, groups)
	require.EqualValues(t, 1, id)
	_, id = ResolveAttributionPolicy(c, []AccountGroup{{GroupID: 3}})
	require.Zero(t, id)
	for _, invalid := range [][]int64{{1, 1}, {0}, {-1}, make([]int64, 1001)} {
		c.GroupPriority = invalid
		_, err := NormalizeAttributionConfig(c)
		require.ErrorIs(t, err, ErrAttributionInvalid)
	}
}
func TestAttributionSkipConditions(t *testing.T) {
	now := time.Now()
	past := now.Add(-time.Second)
	future := now.Add(time.Hour)
	parent := int64(2)
	for _, tc := range []struct {
		name   string
		change func(*Account)
		reason string
	}{
		{"active", func(*Account) {}, ""},
		{"rate_limited", func(a *Account) { a.RateLimitResetAt = &future }, "account_rate_limited"},
		{"rate_limit_expired", func(a *Account) { a.RateLimitResetAt = &past }, ""},
		{"platform", func(a *Account) { a.Platform = "anthropic" }, "unsupported_account"},
		{"type", func(a *Account) { a.Type = "apikey" }, "unsupported_account"},
		{"shadow", func(a *Account) { a.ParentAccountID = &parent }, "shadow_account"},
		{"passthrough", func(a *Account) { a.Extra = map[string]any{"openai_passthrough": true} }, "passthrough_account"},
		{"inactive", func(a *Account) { a.Status = "disabled" }, "account_inactive"},
		{"unscheduled", func(a *Account) { a.Schedulable = false }, "scheduling_disabled"},
		{"expired", func(a *Account) { a.ExpiresAt = &past }, "account_expired"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true}
			tc.change(a)
			require.Equal(t, tc.reason, AttributionSkipReason(a, now))
		})
	}
	require.Equal(t, "account_missing", AttributionSkipReason(nil, now))
}
func TestAttributionProbabilityValidation(t *testing.T) {
	models := []string{AttributionDefaultModel, "gpt-6-luna"}
	require.NoError(t, ValidateAttributionAnalysis(validAttributionAnalysis(), AttributionDefaultModel, models))
	for _, tc := range []struct {
		name   string
		change func(*AttributionAnalysis)
	}{
		{"missing answer", func(a *AttributionAnalysis) { a.UsedOutputs = 2 }},
		{"rejected", func(a *AttributionAnalysis) { a.Diagnostics[1].Accepted = false }},
		{"short", func(a *AttributionAnalysis) { a.Diagnostics[1].ParsedNumbers = 1 }},
		{"duplicate diagnostic", func(a *AttributionAnalysis) { a.Diagnostics[1].Index = 0 }},
		{"tie", func(a *AttributionAnalysis) {
			a.Results[0].Probability = 0.5
			a.Results[1].Probability = 0.5
			a.Probability = 0.5
		}},
		{"NaN", func(a *AttributionAnalysis) { a.Results[0].Probability = math.NaN() }},
		{"infinite", func(a *AttributionAnalysis) { a.Probability = math.Inf(1) }},
		{"sum", func(a *AttributionAnalysis) { a.Results[1].Probability = 0.1 }},
		{"prediction", func(a *AttributionAnalysis) { a.Prediction = "gpt-6-luna" }},
		{"unknown", func(a *AttributionAnalysis) { a.Results[1].Model = "unknown" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := validAttributionAnalysis()
			tc.change(a)
			require.Error(t, ValidateAttributionAnalysis(a, AttributionDefaultModel, models))
		})
	}
}

type attributionTestRepo struct {
	AttributionRepository
	config   AttributionConfig
	valid    bool
	finished *AttributionJob
}

func (r *attributionTestRepo) Config(context.Context) (AttributionConfig, error) {
	return r.config, nil
}
func (r *attributionTestRepo) Validate(context.Context, *AttributionJob) (bool, error) {
	return r.valid, nil
}
func (r *attributionTestRepo) Finish(_ context.Context, j *AttributionJob) error {
	copy := *j
	r.finished = &copy
	return nil
}

type attributionProbeFunc func(context.Context, int64, string, string) (*CandyTestExecution, error)

func (f attributionProbeFunc) Probe(c context.Context, id int64, m, p string) (*CandyTestExecution, error) {
	return f(c, id, m, p)
}

func TestAttributionPipelineMockModelTrace(t *testing.T) {
	for _, scenario := range []string{"pass", "mismatch", "unavailable", "not enrolled", "bad challenges", "invalid answer", "probe failure", "rate limited", "429", "interrupted", "stale", "timeout"} {
		t.Run(scenario, func(t *testing.T) {
			probes, analyzes := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Empty(t, r.Header.Get("Authorization"))
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/banks":
					if scenario == "unavailable" {
						w.WriteHeader(503)
						return
					}
					model := AttributionDefaultModel
					if scenario == "not enrolled" {
						model = "other"
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"gpt": map[string]any{"models": []map[string]string{{"id": model}, {"id": "gpt-6-luna"}}}})
				case "/api/challenges":
					items := []AttributionChallenge{{"a", "challenge A", 300}, {"b", "challenge B", 301}, {"c", "challenge C", 302}}
					if scenario == "bad challenges" {
						items[1] = items[0]
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"challenges": items})
				case "/api/analyze":
					analyzes++
					var body struct {
						Outputs []AttributionOutput `json:"outputs"`
					}
					require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
					require.Len(t, body.Outputs, 3)
					require.Equal(t, "synthetic-private-answer", body.Outputs[0].Text)
					a := validAttributionAnalysis()
					if scenario == "mismatch" {
						a.Prediction = "gpt-6-luna"
						a.Results[0].Probability = 0.2
						a.Results[1].Probability = 0.8
					}
					if scenario == "invalid answer" {
						a.UsedOutputs = 2
						a.Diagnostics[0].Accepted = false
					}
					_ = json.NewEncoder(w).Encode(a)
				default:
					t.Errorf("unexpected endpoint %s", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			repo := &attributionTestRepo{valid: scenario != "stale", config: AttributionConfig{BaseURL: server.URL}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			probe := attributionProbeFunc(func(_ context.Context, id int64, model, prompt string) (*CandyTestExecution, error) {
				probes++
				require.EqualValues(t, 11, id)
				require.Equal(t, AttributionDefaultModel, model)
				require.True(t, strings.HasPrefix(prompt, "challenge "))
				if scenario == "probe failure" {
					return nil, errors.New("secret-oauth-token")
				}
				if scenario == "rate limited" {
					return nil, ErrDiagnosticRateLimited
				}
				if scenario == "429" {
					return nil, candyTestError("upstream_http_429")
				}
				if scenario == "interrupted" {
					cancel()
					return nil, context.Canceled
				}
				return &CandyTestExecution{Completed: true, ActualModel: model, ResponseText: "synthetic-private-answer", Usage: &OpenAIUsage{InputTokens: 1, OutputTokens: 3}}, nil
			})
			s := &ModelAttributionService{repo: repo, probe: probe, analyzer: NewModelTraceClient()}
			j := &AttributionJob{ID: 1, AccountID: 11, Snapshot: AttributionSnapshot{Policy: AttributionPolicy{Model: AttributionDefaultModel}}}
			if scenario == "timeout" {
				past := time.Now().Add(-11 * time.Minute)
				j.StartedAt = &past
			}
			s.execute(ctx, j)
			require.NotNil(t, repo.finished)
			want := "abnormal"
			switch scenario {
			case "pass":
				want = "passed"
			case "mismatch":
				want = "mismatch"
			case "probe failure", "interrupted", "timeout":
				want = "failed"
			case "stale":
				want = "skipped"
			case "rate limited", "429":
				want = "skipped"
				require.Equal(t, "account_rate_limited", repo.finished.Reason)
			}
			require.Equal(t, want, repo.finished.Status)
			b, err := json.Marshal(repo.finished)
			require.NoError(t, err)
			require.NotContains(t, string(b), "secret-oauth-token")
			require.NotContains(t, string(b), "synthetic-private-answer")
			switch scenario {
			case "pass", "mismatch", "invalid answer":
				require.Equal(t, 3, probes)
				require.Equal(t, 1, analyzes)
			case "probe failure", "interrupted", "rate limited", "429":
				require.Equal(t, 1, probes)
				require.Zero(t, analyzes)
			default:
				require.Zero(t, probes)
			}
		})
	}
}

func TestAttributionTextProbeBypassesWhitelistWithoutPelicanInstructions(t *testing.T) {
	account := newOpenAIRejectedFieldTestAccount()
	account.Credentials["model_mapping"] = map[string]any{"gpt-6-luna": "gpt-6-luna", AttributionDefaultModel: "wrong-alias"}
	repo := &stubOpenAIAccountRepo{accounts: []Account{*account}}
	terminal := `data: {"type":"response.completed","response":{"model":"gpt-6-astra","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"123, 34"}]}]}}` + "\n\n"
	upstream := &httpUpstreamRecorder{responses: []*http.Response{{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(bytes.NewBufferString(terminal))}}}
	gateway := newOpenAIRejectedFieldTestService(upstream)
	gateway.accountRepo = repo
	runner := NewAccountCandyTestTransport(repo, gateway)
	runner.fetchModels = func(context.Context, *Account) (*OpenAIModelsResponse, error) {
		t.Fatal("probe must not query model catalog")
		return nil, nil
	}
	result, err := runner.Probe(context.Background(), account.ID, AttributionDefaultModel, "independent challenge")
	require.NoError(t, err)
	require.True(t, result.Completed)
	require.Len(t, upstream.bodies, 1)
	require.Equal(t, AttributionDefaultModel, gjson.GetBytes(upstream.bodies[0], "model").String())
	require.Equal(t, "independent challenge", gjson.GetBytes(upstream.bodies[0], "input.0.content.0.text").String())
	require.Equal(t, textProbeInstructions, gjson.GetBytes(upstream.bodies[0], "instructions").String())
	require.False(t, gjson.GetBytes(upstream.bodies[0], "tools").Exists())
	require.True(t, repo.accounts[0].Schedulable)
}
