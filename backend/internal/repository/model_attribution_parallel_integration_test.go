//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAttributionParallelCheckpointPreservesOutOfOrderResults(t *testing.T) {
	ctx := context.Background()
	r, _, account := attributionFixture(t)
	_, err := r.Enqueue(ctx, []int64{account.ID}, true)
	require.NoError(t, err)
	job, err := r.Claim(ctx)
	require.NoError(t, err)
	require.NotNil(t, job)
	job.Checkpoint = &service.AttributionCheckpoint{
		Version: 2,
		Challenges: []service.AttributionChallenge{
			{ID: "a", Prompt: "first", ExpectedCount: 300},
			{ID: "b", Prompt: "second", ExpectedCount: 301},
			{ID: "c", Prompt: "third", ExpectedCount: 302},
		},
		Slots:              make([]service.AttributionProbeCheckpoint, 3),
		RemainingBudgetMS:  (9 * time.Minute).Milliseconds(),
		ProbeAuthorization: "private-authorization-digest",
	}
	ok, err := r.SaveCheckpoint(ctx, job)
	require.NoError(t, err)
	require.True(t, ok)
	firstDue := time.Now().Add(-time.Second).UTC().Truncate(time.Microsecond)
	thirdDue := time.Now().Add(time.Minute).UTC().Truncate(time.Microsecond)
	job.Checkpoint.Slots[0] = service.AttributionProbeCheckpoint{OrdinaryRetries: 1, UnavailableRetries: 2, NextAttemptAt: &firstDue}
	job.Checkpoint.Slots[1].Output = &service.AttributionOutput{Text: "private second answer", ExpectedCount: 301}
	job.Checkpoint.Slots[2] = service.AttributionProbeCheckpoint{UnavailableRetries: 1, NextAttemptAt: &thirdDue}
	job.Result.CompletedProbes = 1
	job.Result.Retries = 1
	job.Result.Usage = []*service.OpenAIUsage{{InputTokens: 2}, {InputTokens: 3}, {InputTokens: 5}}
	job.Result.Daybreak = []*service.DiagnosticDaybreakEvidence{nil, {Cyber: "daybreak_blue", Reason: "injected"}, nil}
	ok, err = r.DeferRetry(ctx, job, firstDue)
	require.NoError(t, err)
	require.True(t, ok)
	public, err := r.Get(ctx, job.ID)
	require.NoError(t, err)
	require.Nil(t, public.Checkpoint)
	encoded, err := json.Marshal(public)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "private second answer")
	require.NotContains(t, string(encoded), "private-authorization-digest")

	// A fresh repository instance receives only the independently serialized state.
	restarted := &attributionRepository{db: integrationDB}
	resumed, err := restarted.Claim(ctx)
	require.NoError(t, err)
	require.NotNil(t, resumed)
	require.NotEqual(t, job.Claim, resumed.Claim)
	require.Equal(t, job.Checkpoint, resumed.Checkpoint)
	require.Equal(t, job.Result, resumed.Result)
	require.Nil(t, resumed.Checkpoint.Slots[0].Output)
	require.Equal(t, "private second answer", resumed.Checkpoint.Slots[1].Output.Text)
	require.Nil(t, resumed.Checkpoint.Slots[2].Output)

	ok, err = r.SaveCheckpoint(ctx, job)
	require.NoError(t, err)
	require.False(t, ok, "the old worker cannot overwrite the reclaimed checkpoint")
	cancelled, err := restarted.Cancel(ctx, resumed.ID)
	require.NoError(t, err)
	require.Equal(t, "cancelled", cancelled.Status)
	assertAttributionPrivateCheckpoint(t, resumed.ID, nil)
	ok, err = restarted.DeferRetry(ctx, resumed, time.Now())
	require.NoError(t, err)
	require.False(t, ok, "late parallel completions cannot revive cancellation")
}
