package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

type attributionLiveAnalyzer struct {
	attributionRetryAnalyzer
	detector *AttributionDetector
}

func (a *attributionLiveAnalyzer) Info(context.Context, string) (*AttributionDetector, error) {
	copy := *a.detector
	return &copy, nil
}

func (a *attributionLiveAnalyzer) Analyze(ctx context.Context, base string, detector *AttributionDetector, outputs []AttributionOutput) (*AttributionAnalysis, error) {
	if !SameAttributionDetector(detector, a.detector) {
		return nil, errors.New("detector_version_changed")
	}
	analysis, err := a.attributionRetryAnalyzer.Analyze(ctx, base, detector, outputs)
	analysis.Detector = detector
	analysis.Method = detector.Algorithm
	return analysis, err
}

func attributionUpdatedDetector() *AttributionDetector {
	detector := testAttributionDetector()
	detector.Revision = strings.Repeat("e", 40)
	detector.Algorithm = "shared-detector-v2"
	return detector
}

func TestAttributionLiveDetectorWithoutConfigResave(t *testing.T) {
	for _, source := range []string{"manual", "scheduled", "initial"} {
		for _, missing := range []bool{false, true} {
			t.Run(source+"/missing="+map[bool]string{true: "true", false: "false"}[missing], func(t *testing.T) {
				s, repo, _, _, job := newAttributionRetryHarness(source)
				job.Snapshot.Version = 6
				job.Snapshot.Daybreak = &DiagnosticDaybreakPreferences{}
				if missing {
					repo.config.Detector, job.Snapshot.Detector = nil, nil
				}
				live := attributionUpdatedDetector()
				analyzer := &attributionLiveAnalyzer{detector: live}
				s.analyzer = analyzer
				var probeMu sync.Mutex
				probes := 0
				s.probe = attributionProbeFunc(func(_ context.Context, _ int64, model, prompt string) (*CandyTestExecution, error) {
					probeMu.Lock()
					defer probeMu.Unlock()
					probes++
					require.Equal(t, live, repo.storedJob().Snapshot.Detector, "identity must be persisted before the first probe")
					return &CandyTestExecution{ActualModel: model, Completed: true, ResponseText: prompt}, nil
				})
				s.execute(context.Background(), job)
				require.Equal(t, "passed", repo.stored.Status)
				require.Equal(t, 3, probes)
				require.Equal(t, live, repo.stored.Snapshot.Detector)
				require.Equal(t, live, repo.stored.Result.Analysis.Detector)
				require.False(t, SameAttributionDetector(live, repo.config.Detector), "execution must not require changing the configuration")
			})
		}
	}
}

func TestAttributionLiveDetectorResumeUsesFrozenIdentity(t *testing.T) {
	for _, upgraded := range []bool{false, true} {
		t.Run(map[bool]string{false: "same version", true: "upgraded during wait"}[upgraded], func(t *testing.T) {
			s, repo, _, clock, job := newAttributionRetryHarness("manual")
			job.Snapshot.Version = 6
			job.Snapshot.Daybreak = &DiagnosticDaybreakPreferences{}
			live := attributionUpdatedDetector()
			analyzer := &attributionLiveAnalyzer{detector: live}
			s.analyzer = analyzer
			var probeMu sync.Mutex
			var prompts []string
			thirdCalls := 0
			s.probe = attributionProbeFunc(func(_ context.Context, _ int64, model, prompt string) (*CandyTestExecution, error) {
				probeMu.Lock()
				defer probeMu.Unlock()
				prompts = append(prompts, prompt)
				if prompt == "challenge C" {
					thirdCalls++
					if thirdCalls == 1 {
						return &CandyTestExecution{UpstreamStatus: 503}, candyTestError("account_rate_limited")
					}
				}
				return &CandyTestExecution{ActualModel: model, Completed: true, ResponseText: prompt}, nil
			})
			s.execute(context.Background(), job)
			require.Equal(t, "retry_wait", repo.stored.Status)
			require.Equal(t, live, repo.stored.Snapshot.Detector)
			require.NotNil(t, repo.stored.Checkpoint.Slots[0].Output)
			require.NotNil(t, repo.stored.Checkpoint.Slots[1].Output)
			require.Nil(t, repo.stored.Checkpoint.Slots[2].Output)
			if upgraded {
				analyzer.detector = testAttributionDetector()
			}
			resumed := resumeAttributionRetry(t, repo, clock)
			s.execute(context.Background(), resumed)
			require.Equal(t, 1, analyzer.challengeCalls, "resume must retain the frozen challenges")
			if upgraded {
				require.Equal(t, "abnormal", repo.stored.Status)
				require.Equal(t, "detector_version_changed", repo.stored.Reason)
				require.Len(t, prompts, 3, "a changed detector must not mix old answers with new probes")
				require.Empty(t, analyzer.outputs)
			} else {
				require.Equal(t, "passed", repo.stored.Status)
				require.ElementsMatch(t, []string{"challenge A", "challenge B", "challenge C", "challenge C"}, prompts)
			}
		})
	}
}

func TestAttributionLiveDetectorRejectsMissingRecoveryCheckpoint(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result AttributionResult
	}{
		{"completed probe", AttributionResult{CompletedProbes: 1}},
		{"retried probe", AttributionResult{Retries: 1}},
		{"charged probe", AttributionResult{Usage: []*OpenAIUsage{{InputTokens: 1}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, repo, _, _, job := newAttributionRetryHarness("manual")
			job.Snapshot.Version = 6
			job.Snapshot.Daybreak = &DiagnosticDaybreakPreferences{}
			job.Result = tc.result
			s.analyzer = &attributionLiveAnalyzer{detector: attributionUpdatedDetector()}
			s.execute(context.Background(), job)
			require.Equal(t, "checkpoint_invalid", repo.stored.Reason)
			require.Zero(t, repo.saves)
		})
	}
}

func TestAttributionLiveDetectorRejectsDisplayAliasesBeforeProbing(t *testing.T) {
	for _, alias := range []string{"gpt-6.1-sol", "gpt-6-astra/6.1-sol"} {
		t.Run(alias, func(t *testing.T) {
			s, repo, _, _, job := newAttributionRetryHarness("manual")
			job.Snapshot.Version = 6
			job.Snapshot.Daybreak = &DiagnosticDaybreakPreferences{}
			job.Snapshot.Policy.ExpectedModels = []string{alias}
			analyzer := &attributionLiveAnalyzer{detector: attributionUpdatedDetector()}
			s.analyzer = analyzer
			probes := 0
			s.probe = attributionProbeFunc(func(context.Context, int64, string, string) (*CandyTestExecution, error) {
				probes++
				return nil, nil
			})
			s.execute(context.Background(), job)
			require.Equal(t, "abnormal", repo.stored.Status)
			require.Equal(t, "expected_model_not_enrolled", repo.stored.Reason)
			require.Zero(t, probes)
			require.Zero(t, analyzer.challengeCalls)
			require.Zero(t, repo.saves)
		})
	}
}

type attributionLiveConfigRepo struct {
	AttributionRepository
	config AttributionConfig
}

func (r *attributionLiveConfigRepo) Config(context.Context) (AttributionConfig, error) {
	return r.config, nil
}

func (r *attributionLiveConfigRepo) SaveConfig(_ context.Context, c AttributionConfig) (AttributionConfig, error) {
	r.config = c
	return c, nil
}

func TestAttributionSaveConfigAdoptsLiveDetectorWithoutPriorCheck(t *testing.T) {
	for _, supplied := range []*AttributionDetector{nil, testAttributionDetector()} {
		config := DefaultAttributionConfig()
		config.Enabled, config.BaseURL, config.Detector = true, "http://detector.invalid", supplied
		config.Default.HighModels, config.Default.LowModels = []string{"high"}, []string{"low"}
		repo := &attributionLiveConfigRepo{config: config}
		live := attributionUpdatedDetector()
		s := &ModelAttributionService{repo: repo, analyzer: &attributionLiveAnalyzer{detector: live}}
		saved, err := s.SaveConfig(context.Background(), config)
		require.NoError(t, err)
		require.Equal(t, live, saved.Detector)
		require.Equal(t, live, repo.config.Detector)
	}
}
