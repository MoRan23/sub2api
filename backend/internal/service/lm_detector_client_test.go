package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func (r *attributionTestRepo) SaveConfig(_ context.Context, c AttributionConfig) (AttributionConfig, error) {
	r.config = c
	return c, nil
}

func TestAttributionDetectorConfigVerification(t *testing.T) {
	calls := 0
	detector := testAttributionDetector()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("X-LM-Detector-Version", detector.Version())
		if r.URL.Path == "/api/info" {
			_ = json.NewEncoder(w).Encode(detector)
			return
		}
		require.Equal(t, "/api/banks", r.URL.Path)
		require.Equal(t, detector.Version(), r.Header.Get("X-LM-Detector-Version"))
		_ = json.NewEncoder(w).Encode(map[string]any{"shared": map[string]any{"models": []map[string]string{{"id": AttributionDefaultModel}}}})
	}))
	defer server.Close()
	repo := &attributionTestRepo{config: DefaultAttributionConfig()}
	s := &ModelAttributionService{repo: repo, analyzer: NewLMDetectorClient()}
	c := repo.config
	c.BaseURL = server.URL
	c.Enabled = true
	c.Default.HighModels = []string{"high"}
	c.Default.LowModels = []string{"low"}
	saved, err := s.SaveConfig(context.Background(), c)
	require.NoError(t, err, "saving resolves the live identity without a separate connection check")
	require.Equal(t, detector, saved.Detector)
	connection, err := s.Connection(context.Background(), server.URL)
	require.NoError(t, err)
	c.Detector = connection.Detector
	saved, err = s.SaveConfig(context.Background(), c)
	require.NoError(t, err)
	require.Equal(t, detector, saved.Detector)
	detector.Revision = strings.Repeat("f", 40)
	saved, err = s.SaveConfig(context.Background(), saved)
	require.NoError(t, err, "stale connection metadata is refreshed automatically")
	require.Equal(t, detector.Revision, repo.config.Detector.Revision)
	// Turning it off never waits for the service, even if it is down.
	before := calls
	saved.Enabled = false
	_, err = s.SaveConfig(context.Background(), saved)
	require.NoError(t, err)
	require.Equal(t, before, calls)
	server.Close()
	_, err = s.SaveConfig(context.Background(), saved)
	require.NoError(t, err)
}

func TestAttributionDetectorProtocolErrors(t *testing.T) {
	for _, scenario := range []string{"legacy", "wrong provider", "wrong version header", "null confidence", "null candidate", "uncalibrated"} {
		t.Run(scenario, func(t *testing.T) {
			d := testAttributionDetector()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-LM-Detector-Version", d.Version())
				if scenario == "legacy" {
					w.WriteHeader(404)
					return
				}
				if r.URL.Path == "/api/info" {
					if scenario == "wrong provider" {
						d.Provider = "modeltrace"
					}
					_ = json.NewEncoder(w).Encode(d)
					return
				}
				a := validAttributionAnalysis()
				switch scenario {
				case "wrong version header":
					w.Header().Del("X-LM-Detector-Version")
				case "null confidence":
					a.Probability = nil
				case "null candidate":
					a.Results[1].Probability = nil
				case "uncalibrated":
					a.ProbabilityStatus = "unavailable"
				}
				_ = json.NewEncoder(w).Encode(a)
			}))
			defer server.Close()
			client := NewLMDetectorClient()
			if scenario == "legacy" || scenario == "wrong provider" {
				_, err := client.Info(context.Background(), server.URL)
				require.Error(t, err)
				return
			}
			a, err := client.Analyze(context.Background(), server.URL, d, []AttributionOutput{})
			if scenario == "wrong version header" {
				require.EqualError(t, err, "detector_version_changed")
				return
			}
			require.NoError(t, err)
			require.Error(t, ValidateAttributionAnalysis(a, []string{AttributionDefaultModel}, []string{AttributionDefaultModel, "gpt-6-luna"}))
		})
	}
}

func TestLMDetectorV2AnalysisWithoutDecisionIsAccepted(t *testing.T) {
	// Captured from the pinned v2 adapter using the first three gpt-6-astra
	// reference samples. Keep the original response fields and probabilities
	// so this exercises the upstream wire format, including omitted decision.
	data, err := os.ReadFile("testdata/lm_detector_v2_analysis.json")
	require.NoError(t, err)
	var fixture struct {
		Models   []string        `json:"models"`
		Analysis json.RawMessage `json:"analysis"`
	}
	require.NoError(t, json.Unmarshal(data, &fixture))
	var expected AttributionAnalysis
	require.NoError(t, json.Unmarshal(fixture.Analysis, &expected))
	detector := expected.Detector
	require.True(t, detector.Valid())
	require.NotContains(t, string(fixture.Analysis), `"decision"`)
	require.Contains(t, string(fixture.Analysis), `"display_name": "gpt-6-astra/6.1-sol"`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/analyze", r.URL.Path)
		require.Equal(t, detector.Version(), r.Header.Get("X-LM-Detector-Version"))
		w.Header().Set("X-LM-Detector-Version", detector.Version())
		_, _ = w.Write(fixture.Analysis)
	}))
	defer server.Close()

	analysis, err := NewLMDetectorClient().Analyze(context.Background(), server.URL, detector, []AttributionOutput{})
	require.NoError(t, err)
	require.NoError(t, ValidateAttributionAnalysis(analysis, []string{"gpt-6-astra"}, fixture.Models))
	require.Equal(t, &expected, analysis)
	require.Equal(t, analysis, attributionSafeAnalysis(analysis, fixture.Models))
	require.Equal(t, "shared-detector-v2", analysis.Method)
	require.Equal(t, "gpt-6-astra", analysis.Prediction)
	require.Equal(t, 0.9999611849793368, *analysis.Probability)
	for _, alias := range []string{"gpt-6.1-sol", "gpt-6-astra/6.1-sol"} {
		require.NotContains(t, fixture.Models, alias)
		require.EqualError(t, ValidateAttributionAnalysis(analysis, []string{alias}, fixture.Models), "expected_model_not_enrolled")
	}
}

func TestAttributionDetectorAlgorithmCompatibility(t *testing.T) {
	for _, algorithm := range []string{"shared-detector-v1", "shared-detector-v2", "shared-detector-v3", "shared-ranker-partial-v1"} {
		t.Run(algorithm, func(t *testing.T) {
			a := validAttributionAnalysis()
			a.Method, a.Detector.Algorithm = algorithm, algorithm
			models := []string{AttributionDefaultModel, "gpt-6-luna"}
			if algorithm == "shared-detector-v1" || algorithm == "shared-detector-v2" {
				require.True(t, a.Detector.Valid())
				require.NoError(t, ValidateAttributionAnalysis(a, []string{AttributionDefaultModel}, models))
				require.NotNil(t, attributionSafeAnalysis(a, models))
				a.Method = "shared-detector-v2"
				if algorithm == "shared-detector-v2" {
					a.Method = "shared-detector-v1"
				}
			} else {
				require.False(t, a.Detector.Valid())
			}
			require.EqualError(t, ValidateAttributionAnalysis(a, []string{AttributionDefaultModel}, models), "detector_identity_invalid")
			require.Nil(t, attributionSafeAnalysis(a, models))
		})
	}
}
