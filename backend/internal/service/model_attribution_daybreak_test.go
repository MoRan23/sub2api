package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAttributionDaybreakFreezesPreferencesForEveryTaskSource(t *testing.T) {
	for _, source := range []string{"manual", "scheduled", "initial"} {
		t.Run(source, func(t *testing.T) {
			s, repo, analyzer, _, job := newAttributionRetryHarness(source)
			account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{
				OpenAIDaybreakBlueEnabledKey: true,
				OpenAIDaybreakRedEnabledKey:  true,
			}}
			frozen := ReadDiagnosticDaybreakPreferences(account)
			job.Snapshot.Version, job.Snapshot.Daybreak = 3, &frozen
			repo.stored = copyAttributionRetryJob(job)
			calls := 0
			s.probe = attributionProbeFunc(func(ctx context.Context, _ int64, model, prompt string) (*CandyTestExecution, error) {
				preferences, ok := diagnosticDaybreakPreferencesFromContext(ctx)
				require.True(t, ok)
				require.Equal(t, DiagnosticDaybreakPreferences{Blue: true, Red: true}, preferences)
				require.True(t, isAttributionExecution(ctx))
				require.Equal(t, source == "manual", isManualAttribution(ctx))
				calls++
				// A live account edit must not change the remaining probes' preferences.
				account.Extra[OpenAIDaybreakBlueEnabledKey] = false
				account.Extra[OpenAIDaybreakRedEnabledKey] = false
				return &CandyTestExecution{ActualModel: model, Completed: true, ResponseText: "answer " + prompt,
					Daybreak: &DiagnosticDaybreakEvidence{Cyber: "daybreak_blue", Reason: "injected"}}, nil
			})
			s.execute(context.Background(), job)
			require.Equal(t, 3, calls)
			require.Equal(t, "passed", repo.stored.Status)
			require.Len(t, analyzer.outputs, 3)
			require.Len(t, repo.stored.Result.Daybreak, 3)
			require.Empty(t, AttributionDaybreakIssue(repo.stored.Snapshot, repo.stored.Result))
			for _, evidence := range repo.stored.Result.Daybreak {
				require.Equal(t, &DiagnosticDaybreakEvidence{Cyber: "daybreak_blue", Reason: "injected"}, evidence)
			}
		})
	}
}

func TestAttributionDaybreakLegacyTaskKeepsOriginalProbeBehavior(t *testing.T) {
	for _, version := range []int{0, 1, 2} {
		t.Run(string(rune('0'+version)), func(t *testing.T) {
			s, repo, _, _, job := newAttributionRetryHarness("manual")
			job.Snapshot.Version = version
			// A stray new field must not opt an already-created legacy task in.
			job.Snapshot.Daybreak = &DiagnosticDaybreakPreferences{Blue: true, Red: true}
			s.probe = attributionProbeFunc(func(ctx context.Context, _ int64, model, prompt string) (*CandyTestExecution, error) {
				_, ok := diagnosticDaybreakPreferencesFromContext(ctx)
				require.False(t, ok)
				return &CandyTestExecution{ActualModel: model, Completed: true, ResponseText: "answer " + prompt,
					Daybreak: &DiagnosticDaybreakEvidence{Cyber: "daybreak_blue", Reason: "injected"}}, nil
			})
			s.execute(context.Background(), job)
			require.Equal(t, "passed", repo.stored.Status)
			require.Empty(t, repo.stored.Result.Daybreak)
			require.Empty(t, AttributionDaybreakIssue(repo.stored.Snapshot, repo.stored.Result))
		})
	}
}

func TestAttributionDaybreakMissingSnapshotStopsBeforeInference(t *testing.T) {
	s, repo, analyzer, _, job := newAttributionRetryHarness("manual")
	job.Snapshot.Version = 3
	s.probe = attributionProbeFunc(func(context.Context, int64, string, string) (*CandyTestExecution, error) {
		t.Fatal("a new task with missing frozen preferences must not perform inference")
		return nil, nil
	})
	s.execute(context.Background(), job)
	require.Equal(t, "failed", repo.stored.Status)
	require.Equal(t, "daybreak_evidence_missing", repo.stored.Reason)
	require.Zero(t, analyzer.challengeCalls)
	require.Empty(t, analyzer.outputs)
}

func TestAttributionDaybreak503RestoresOnlySuccessfulEvidence(t *testing.T) {
	s, repo, analyzer, clock, job := newAttributionRetryHarness("manual")
	job.Snapshot.Version = 3
	job.Snapshot.Daybreak = &DiagnosticDaybreakPreferences{Blue: true, Red: true}
	repo.stored = copyAttributionRetryJob(job)
	var prompts []string
	s.probe = attributionProbeFunc(func(ctx context.Context, _ int64, model, prompt string) (*CandyTestExecution, error) {
		preferences, ok := diagnosticDaybreakPreferencesFromContext(ctx)
		require.True(t, ok)
		require.Equal(t, DiagnosticDaybreakPreferences{Blue: true, Red: true}, preferences)
		prompts = append(prompts, prompt)
		execution := &CandyTestExecution{ActualModel: model, Usage: &OpenAIUsage{InputTokens: 10},
			Daybreak: &DiagnosticDaybreakEvidence{Cyber: "daybreak_blue", Reason: "injected"}}
		if prompt == "challenge C" {
			execution.UpstreamStatus = 503
			execution.Daybreak = &DiagnosticDaybreakEvidence{Cyber: "daybreak_red", Reason: "failed_attempt"}
			return execution, candyTestError("upstream_http_503")
		}
		execution.Completed, execution.ResponseText = true, "private answer "+prompt
		return execution, nil
	})
	s.execute(context.Background(), job)
	require.Equal(t, "retry_wait", repo.stored.Status)
	require.Equal(t, 2, repo.stored.Result.CompletedProbes)
	require.Len(t, repo.stored.Result.Daybreak, 2)
	require.Len(t, repo.stored.Checkpoint.Outputs, 2)
	require.Empty(t, analyzer.outputs)
	for _, evidence := range repo.stored.Result.Daybreak {
		require.Equal(t, "daybreak_blue", evidence.Cyber)
	}
	public, err := json.Marshal(repo.stored)
	require.NoError(t, err)
	require.NotContains(t, string(public), "failed_attempt")
	require.NotContains(t, string(public), "private answer")

	// The fake repository has serialized independent copies. Destroy the first
	// worker's values, then resume with a fresh worker after durable waiting.
	job.Snapshot.Daybreak.Blue, job.Snapshot.Daybreak.Red = false, false
	job.Result.Daybreak[0].Cyber = "changed old worker memory"
	job.Checkpoint.Outputs[0].Text = "changed old worker memory"
	resumed := resumeAttributionRetry(t, repo, clock)
	newAnalyzer := &attributionRetryAnalyzer{}
	newWorker := &ModelAttributionService{repo: repo, analyzer: newAnalyzer, now: clock.Now, wait: clock.Wait}
	newWorker.probe = attributionProbeFunc(func(ctx context.Context, _ int64, model, prompt string) (*CandyTestExecution, error) {
		preferences, ok := diagnosticDaybreakPreferencesFromContext(ctx)
		require.True(t, ok)
		require.Equal(t, DiagnosticDaybreakPreferences{Blue: true, Red: true}, preferences)
		require.Equal(t, "challenge C", prompt, "completed probes must not be replayed after restart")
		prompts = append(prompts, prompt)
		return &CandyTestExecution{ActualModel: model, Completed: true, ResponseText: "private answer " + prompt,
			Usage: &OpenAIUsage{InputTokens: 10}, Daybreak: &DiagnosticDaybreakEvidence{Cyber: "daybreak_blue", Reason: "injected"}}, nil
	})
	newWorker.execute(context.Background(), resumed)
	require.Equal(t, []string{"challenge A", "challenge B", "challenge C", "challenge C"}, prompts)
	require.Equal(t, "passed", repo.stored.Status)
	require.Zero(t, newAnalyzer.challengeCalls, "the frozen challenges must survive restart")
	require.Equal(t, []AttributionOutput{{Text: "private answer challenge A", ExpectedCount: 300}, {Text: "private answer challenge B", ExpectedCount: 301}, {Text: "private answer challenge C", ExpectedCount: 302}}, newAnalyzer.outputs)
	require.Len(t, repo.stored.Result.Daybreak, 3)
	require.Empty(t, AttributionDaybreakIssue(repo.stored.Snapshot, repo.stored.Result))
	require.EqualValues(t, 10, repo.stored.Result.Usage[0].InputTokens)
	require.EqualValues(t, 10, repo.stored.Result.Usage[1].InputTokens)
	require.EqualValues(t, 20, repo.stored.Result.Usage[2].InputTokens)
	require.Equal(t, 1, repo.stored.Result.Retries)
	require.Nil(t, repo.stored.Checkpoint)
}

func TestAttributionDaybreakMixedSuccessfulConditionsStillAnalyze(t *testing.T) {
	s, repo, analyzer, _, job := newAttributionRetryHarness("scheduled")
	job.Snapshot.Version = 3
	job.Snapshot.Daybreak = &DiagnosticDaybreakPreferences{Blue: true, Red: true}
	s.probe = attributionProbeFunc(func(_ context.Context, _ int64, model, prompt string) (*CandyTestExecution, error) {
		evidence := &DiagnosticDaybreakEvidence{Cyber: "daybreak_blue", Reason: "injected"}
		if prompt == "challenge C" {
			evidence = &DiagnosticDaybreakEvidence{Reason: "capability_unavailable"}
		}
		return &CandyTestExecution{ActualModel: model, Completed: true, ResponseText: "answer " + prompt, Daybreak: evidence}, nil
	})
	s.execute(context.Background(), job)
	require.Equal(t, "passed", repo.stored.Status, "the real detector verdict must survive mixed Daybreak conditions")
	require.Len(t, analyzer.outputs, 3)
	require.NotNil(t, repo.stored.Result.Analysis)
	require.Equal(t, "daybreak_conditions_mixed", AttributionDaybreakIssue(repo.stored.Snapshot, repo.stored.Result))
	_, consistent := AttributionDaybreakCondition(repo.stored.Result)
	require.False(t, consistent)
}

func TestAttributionDaybreakConditionRequiresCompleteActualEvidence(t *testing.T) {
	snapshot := AttributionSnapshot{Version: 3, Daybreak: &DiagnosticDaybreakPreferences{Blue: true}}
	for _, tc := range []struct {
		name   string
		result AttributionResult
		cyber  string
		issue  string
	}{
		{name: "blue", result: attributionDaybreakResult("daybreak_blue", "daybreak_blue", "daybreak_blue"), cyber: "daybreak_blue"},
		{name: "red", result: attributionDaybreakResult("daybreak_red", "daybreak_red", "daybreak_red"), cyber: "daybreak_red"},
		{name: "none", result: attributionDaybreakResult("", "", "")},
		{name: "mixed", result: attributionDaybreakResult("daybreak_blue", "", "daybreak_blue"), issue: "daybreak_conditions_mixed"},
		{name: "unknown value", result: attributionDaybreakResult("unknown", "unknown", "unknown"), issue: "daybreak_evidence_missing"},
		{name: "missing evidence", result: AttributionResult{CompletedProbes: 3}, issue: "daybreak_evidence_missing"},
		{name: "nil evidence", result: AttributionResult{CompletedProbes: 3, Daybreak: []*DiagnosticDaybreakEvidence{{}, nil, {}}}, issue: "daybreak_evidence_missing"},
		{name: "unfinished", result: AttributionResult{CompletedProbes: 2, Daybreak: attributionDaybreakResult("", "", "").Daybreak}, issue: "daybreak_evidence_missing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cyber, consistent := AttributionDaybreakCondition(tc.result)
			require.Equal(t, tc.cyber, cyber)
			require.Equal(t, tc.issue == "", consistent)
			require.Equal(t, tc.issue, AttributionDaybreakIssue(snapshot, tc.result))
		})
	}
}

func TestAttributionDaybreakResultBaselineSeparatesExecutionConditions(t *testing.T) {
	legacy := AttributionSnapshot{Version: 2, Policy: AttributionPolicy{Model: AttributionDefaultModel}, Detector: testAttributionDetector()}
	modern := legacy
	modern.Version = 3
	modern.Daybreak = &DiagnosticDaybreakPreferences{Blue: true, Red: true}
	blue := attributionDaybreakResult("daybreak_blue", "daybreak_blue", "daybreak_blue")
	missing := attributionDaybreakResult("", "", "")
	baseline := AttributionResultPolicyDigest(modern, blue)
	require.Equal(t, AttributionPolicyDigest(legacy), AttributionResultPolicyDigest(legacy, blue))
	require.NotEqual(t, AttributionResultPolicyDigest(legacy, blue), baseline, "old and new rounds cannot share a consecutive-pass baseline")
	require.NotEqual(t, baseline, AttributionResultPolicyDigest(modern, missing))
	require.NotEqual(t, baseline, AttributionResultPolicyDigest(modern, attributionDaybreakResult("daybreak_red", "daybreak_red", "daybreak_red")))
	changedPreferences := modern
	changedPreferences.Daybreak = &DiagnosticDaybreakPreferences{Blue: true}
	require.NotEqual(t, baseline, AttributionResultPolicyDigest(changedPreferences, blue))
	changedGroup := modern
	changedGroup.GroupID = 999
	require.Equal(t, baseline, AttributionResultPolicyDigest(changedGroup, blue), "unchanged effective conditions may follow an account to another group")
	blue.Daybreak[0].Reason = "a different observation label"
	require.Equal(t, baseline, AttributionResultPolicyDigest(modern, blue), "reason labels do not alter actual execution conditions")
	missing.Daybreak[0].Reason = "account_disabled"
	missing.Daybreak[1].Reason = "capability_unavailable"
	missing.Daybreak[2].Reason = "model_unsupported"
	require.Empty(t, AttributionDaybreakIssue(modern, missing), "all three requests still used the same absent cyber field")
}

func attributionDaybreakResult(values ...string) AttributionResult {
	result := AttributionResult{CompletedProbes: len(values)}
	for _, value := range values {
		result.Daybreak = append(result.Daybreak, &DiagnosticDaybreakEvidence{Cyber: value, Reason: "observed"})
	}
	return result
}
