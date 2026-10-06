package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	_, err := s.SaveConfig(context.Background(), c)
	require.ErrorIs(t, err, ErrAttributionInvalid, "must inspect and provide the verified version first")
	connection, err := s.Connection(context.Background(), server.URL)
	require.NoError(t, err)
	c.Detector = connection.Detector
	saved, err := s.SaveConfig(context.Background(), c)
	require.NoError(t, err)
	require.Equal(t, detector, saved.Detector)
	oldRevision := saved.Detector.Revision
	detector.Revision = strings.Repeat("f", 40)
	_, err = s.SaveConfig(context.Background(), saved)
	require.ErrorIs(t, err, ErrAttributionInvalid, "an enabled configuration cannot silently adopt a new service version")
	require.Equal(t, oldRevision, repo.config.Detector.Revision)
	// Turning it off is always possible even if the service is down.
	server.Close()
	before := calls
	saved.Enabled = false
	_, err = s.SaveConfig(context.Background(), saved)
	require.NoError(t, err)
	require.Equal(t, before, calls)
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
			require.Error(t, ValidateAttributionAnalysis(a, AttributionDefaultModel, []string{AttributionDefaultModel, "gpt-6-luna"}))
		})
	}
}
