package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"
)

type AttributionChallenge struct {
	ID            string `json:"id"`
	Prompt        string `json:"prompt"`
	ExpectedCount int    `json:"expected_count"`
}
type AttributionOutput struct {
	Text          string `json:"text"`
	ExpectedCount int    `json:"expected_count"`
}
type AttributionAnalyzer interface {
	Models(context.Context, string) ([]string, error)
	Challenges(context.Context, string) ([]AttributionChallenge, error)
	Analyze(context.Context, string, []AttributionOutput) (*AttributionAnalysis, error)
}
type ModelTraceClient struct{ client *http.Client }

func NewModelTraceClient() *ModelTraceClient {
	return &ModelTraceClient{client: &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func (c *ModelTraceClient) request(ctx context.Context, base, path string, payload, target any) error {
	base, err := AttributionBaseURL(base)
	if err != nil {
		return err
	}
	var body []byte
	method := http.MethodGet
	if payload != nil {
		body, err = json.Marshal(payload)
		if err != nil {
			return err
		}
		method = http.MethodPost
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, bytes.NewReader(body))
	if err != nil {
		return errors.New("modeltrace_request_invalid")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return errors.New("modeltrace_unavailable")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("modeltrace_http_%d", resp.StatusCode)
	}
	const limit = 4 << 20
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || len(b) > limit {
		return errors.New("modeltrace_response_invalid")
	}
	if json.Unmarshal(b, target) != nil {
		return errors.New("modeltrace_response_invalid")
	}
	return nil
}
func (c *ModelTraceClient) Models(ctx context.Context, base string) ([]string, error) {
	var banks map[string]struct {
		Models []struct {
			ID string `json:"id"`
		} `json:"models"`
	}
	if err := c.request(ctx, base, "/api/banks", nil, &banks); err != nil {
		return nil, err
	}
	ids := []string{}
	seen := map[string]bool{}
	for _, bank := range banks {
		for _, model := range bank.Models {
			if model.ID != "" && !seen[model.ID] {
				seen[model.ID] = true
				ids = append(ids, model.ID)
			}
		}
	}
	if len(ids) == 0 || len(ids) > 1000 {
		return nil, errors.New("modeltrace_bank_invalid")
	}
	sort.Strings(ids)
	return ids, nil
}
func (c *ModelTraceClient) Challenges(ctx context.Context, base string) ([]AttributionChallenge, error) {
	var data struct {
		Challenges []AttributionChallenge `json:"challenges"`
	}
	if err := c.request(ctx, base, "/api/challenges", nil, &data); err != nil {
		return nil, err
	}
	if len(data.Challenges) != 3 {
		return nil, errors.New("modeltrace_challenges_invalid")
	}
	seen := map[string]bool{}
	prompts := map[string]bool{}
	for _, v := range data.Challenges {
		if v.ID == "" || seen[v.ID] || strings.TrimSpace(v.Prompt) == "" || prompts[v.Prompt] || len(v.Prompt) > 20000 || v.ExpectedCount < 1 || v.ExpectedCount > 4096 {
			return nil, errors.New("modeltrace_challenges_invalid")
		}
		seen[v.ID] = true
		prompts[v.Prompt] = true
	}
	return data.Challenges, nil
}
func (c *ModelTraceClient) Analyze(ctx context.Context, base string, outputs []AttributionOutput) (*AttributionAnalysis, error) {
	var result AttributionAnalysis
	if err := c.request(ctx, base, "/api/analyze", map[string]any{"outputs": outputs}, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func ValidateAttributionAnalysis(a *AttributionAnalysis, expected string, models []string) error {
	if a == nil || a.UsedOutputs != 3 || len(a.Diagnostics) != 3 {
		return errors.New("insufficient_valid_outputs")
	}
	indexes := map[int]bool{}
	for _, d := range a.Diagnostics {
		if d.Index < 0 || d.Index > 2 || indexes[d.Index] || !d.Accepted || d.MinimumNumbers < 1 || d.ParsedNumbers < d.MinimumNumbers {
			return errors.New("insufficient_valid_outputs")
		}
		indexes[d.Index] = true
	}
	known := map[string]bool{}
	for _, m := range models {
		known[m] = true
	}
	if !known[expected] {
		return errors.New("model_not_enrolled")
	}
	if len(a.Results) == 0 || len(a.Results) > 1000 {
		return errors.New("modeltrace_probabilities_invalid")
	}
	seen := map[string]bool{}
	sum, maxProb := 0.0, -1.0
	top := ""
	tied := false
	for _, r := range a.Results {
		if r.Model == "" || seen[r.Model] || !known[r.Model] || math.IsNaN(r.Probability) || math.IsInf(r.Probability, 0) || r.Probability < 0 || r.Probability > 1 {
			return errors.New("modeltrace_probabilities_invalid")
		}
		seen[r.Model] = true
		sum += r.Probability
		if r.Probability > maxProb {
			maxProb = r.Probability
			top = r.Model
			tied = false
		} else if r.Probability == maxProb {
			tied = true
		}
	}
	if !seen[expected] {
		return errors.New("model_not_enrolled")
	}
	if tied {
		return errors.New("ambiguous_prediction")
	}
	if math.Abs(sum-1) > 0.000001 || a.Prediction != top || math.IsNaN(a.Probability) || math.IsInf(a.Probability, 0) || math.Abs(a.Probability-maxProb) > 0.000001 {
		return errors.New("modeltrace_probabilities_invalid")
	}
	return nil
}

func attributionSafeAnalysis(a *AttributionAnalysis, models []string) *AttributionAnalysis {
	if a == nil || len(a.Results) > 1000 || len(a.Diagnostics) > 3 || a.UsedOutputs < 0 || a.UsedOutputs > 3 || math.IsNaN(a.Probability) || math.IsInf(a.Probability, 0) || a.Probability < 0 || a.Probability > 1 {
		return nil
	}
	known := make(map[string]bool, len(models))
	for _, m := range models {
		known[m] = true
	}
	if !known[a.Prediction] {
		return nil
	}
	for _, r := range a.Results {
		if !known[r.Model] || math.IsNaN(r.Probability) || math.IsInf(r.Probability, 0) || r.Probability < 0 || r.Probability > 1 {
			return nil
		}
	}
	return a
}
