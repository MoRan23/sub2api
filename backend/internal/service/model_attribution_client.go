package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	Info(context.Context, string) (*AttributionDetector, error)
	Models(context.Context, string, *AttributionDetector) ([]string, error)
	Challenges(context.Context, string, *AttributionDetector) ([]AttributionChallenge, error)
	Analyze(context.Context, string, *AttributionDetector, []AttributionOutput) (*AttributionAnalysis, error)
}

type LMDetectorClient struct{ client *http.Client }

func NewLMDetectorClient() *LMDetectorClient {
	return &LMDetectorClient{client: &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (c *LMDetectorClient) request(ctx context.Context, base, path string, detector *AttributionDetector, payload, target any) error {
	base, err := AttributionBaseURL(base)
	if err != nil {
		return err
	}
	var body []byte
	method := http.MethodGet
	if payload != nil {
		body, err = json.Marshal(payload)
		if err != nil {
			return errors.New("detector_request_invalid")
		}
		method = http.MethodPost
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, bytes.NewReader(body))
	if err != nil {
		return errors.New("detector_request_invalid")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if detector != nil {
		req.Header.Set("X-LM-Detector-Version", detector.Version())
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return errors.New("detector_unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusConflict {
		return errors.New("detector_version_changed")
	}
	if resp.StatusCode != http.StatusOK {
		return errors.New("detector_unavailable")
	}
	if detector != nil && resp.Header.Get("X-LM-Detector-Version") != detector.Version() {
		return errors.New("detector_version_changed")
	}
	const limit = 4 << 20
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || len(b) > limit || json.Unmarshal(b, target) != nil {
		return errors.New("detector_response_invalid")
	}
	return nil
}

func (c *LMDetectorClient) Info(ctx context.Context, base string) (*AttributionDetector, error) {
	var d AttributionDetector
	if err := c.request(ctx, base, "/api/info", nil, nil, &d); err != nil {
		return nil, err
	}
	if !d.Valid() {
		return nil, errors.New("detector_identity_invalid")
	}
	return &d, nil
}
func (c *LMDetectorClient) Models(ctx context.Context, base string, d *AttributionDetector) ([]string, error) {
	var banks map[string]struct {
		Models []struct {
			ID string `json:"id"`
		} `json:"models"`
	}
	if err := c.request(ctx, base, "/api/banks", d, nil, &banks); err != nil {
		return nil, err
	}
	ids := []string{}
	seen := map[string]bool{}
	for _, bank := range banks {
		for _, m := range bank.Models {
			if m.ID == "" || len(m.ID) > 200 || strings.ContainsAny(m.ID, "\r\n\t ") {
				return nil, errors.New("detector_bank_invalid")
			}
			if !seen[m.ID] {
				seen[m.ID] = true
				ids = append(ids, m.ID)
			}
		}
	}
	if len(ids) == 0 || len(ids) > 1000 {
		return nil, errors.New("detector_bank_invalid")
	}
	sort.Strings(ids)
	return ids, nil
}
func (c *LMDetectorClient) Challenges(ctx context.Context, base string, d *AttributionDetector) ([]AttributionChallenge, error) {
	var data struct {
		Challenges []AttributionChallenge `json:"challenges"`
	}
	if err := c.request(ctx, base, "/api/challenges", d, nil, &data); err != nil {
		return nil, err
	}
	if len(data.Challenges) != 3 {
		return nil, errors.New("detector_challenges_invalid")
	}
	seen, prompts := map[string]bool{}, map[string]bool{}
	for _, v := range data.Challenges {
		if v.ID == "" || seen[v.ID] || strings.TrimSpace(v.Prompt) == "" || prompts[v.Prompt] || len(v.Prompt) > 20000 || v.ExpectedCount < 1 || v.ExpectedCount > 4096 {
			return nil, errors.New("detector_challenges_invalid")
		}
		seen[v.ID], prompts[v.Prompt] = true, true
	}
	return data.Challenges, nil
}
func (c *LMDetectorClient) Analyze(ctx context.Context, base string, d *AttributionDetector, outputs []AttributionOutput) (*AttributionAnalysis, error) {
	var a AttributionAnalysis
	if err := c.request(ctx, base, "/api/analyze", d, map[string]any{"outputs": outputs}, &a); err != nil {
		return nil, err
	}
	if !SameAttributionDetector(d, a.Detector) {
		return nil, errors.New("detector_version_changed")
	}
	return &a, nil
}

func validAttributionProbability(p *float64) bool {
	return p != nil && !math.IsNaN(*p) && !math.IsInf(*p, 0) && *p >= 0 && *p <= 1
}
func finiteAttributionScore(p *float64) bool {
	return p == nil || !math.IsNaN(*p) && !math.IsInf(*p, 0)
}
func ValidateAttributionAnalysis(a *AttributionAnalysis, expected []string, models []string) error {
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
	if a.ProbabilityStatus != "reference_calibrated" {
		return errors.New("detector_calibration_unavailable")
	}
	if a.Method != "shared-detector-v1" || !a.Detector.Valid() {
		return errors.New("detector_identity_invalid")
	}
	known := map[string]bool{}
	for _, m := range models {
		known[m] = true
	}
	if len(expected) == 0 {
		return errors.New("expected_model_not_enrolled")
	}
	for _, model := range expected {
		if !known[model] {
			return errors.New("expected_model_not_enrolled")
		}
	}
	if len(a.Results) == 0 || len(a.Results) > 1000 {
		return errors.New("detector_probabilities_invalid")
	}
	seen := map[string]bool{}
	sum, maxProb := 0.0, -1.0
	top := ""
	tied := false
	for _, r := range a.Results {
		if r.Model == "" || seen[r.Model] || !known[r.Model] || !validAttributionProbability(r.Probability) || !finiteAttributionScore(r.Score) {
			return errors.New("detector_probabilities_invalid")
		}
		seen[r.Model] = true
		p := *r.Probability
		sum += p
		if p > maxProb {
			maxProb, top, tied = p, r.Model, false
		} else if p == maxProb {
			tied = true
		}
	}
	for _, model := range expected {
		if !seen[model] {
			return errors.New("expected_model_not_enrolled")
		}
	}
	if tied {
		return errors.New("ambiguous_prediction")
	}
	if math.Abs(sum-1) > 0.000001 || a.Prediction != top || !validAttributionProbability(a.Probability) || math.Abs(*a.Probability-maxProb) > 0.000001 || !finiteAttributionScore(a.RankingScore) {
		return errors.New("detector_probabilities_invalid")
	}
	return nil
}

// Preserve bounded numerical diagnostics, including an unavailable confidence,
// without storing arbitrary upstream messages or raw probe answers.
func attributionSafeAnalysis(a *AttributionAnalysis, models []string) *AttributionAnalysis {
	if a == nil || len(a.Results) > 1000 || len(a.Diagnostics) > 3 || a.UsedOutputs < 0 || a.UsedOutputs > 3 || a.Probability != nil && !validAttributionProbability(a.Probability) || !finiteAttributionScore(a.RankingScore) || !a.Detector.Valid() {
		return nil
	}
	if a.Method != "shared-detector-v1" || (a.ProbabilityStatus != "reference_calibrated" && a.ProbabilityStatus != "unavailable") {
		return nil
	}
	known := map[string]bool{}
	for _, m := range models {
		known[m] = true
	}
	if a.Prediction != "" && !known[a.Prediction] {
		return nil
	}
	for _, r := range a.Results {
		if !known[r.Model] || r.Probability != nil && !validAttributionProbability(r.Probability) || !finiteAttributionScore(r.Score) {
			return nil
		}
	}
	return a
}
