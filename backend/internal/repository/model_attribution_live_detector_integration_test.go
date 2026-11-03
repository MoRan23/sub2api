//go:build integration

package repository

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func attributionLiveRound(t *testing.T, r *attributionRepository, accountID int64, detector *service.AttributionDetector, status string) *service.AttributionJob {
	t.Helper()
	ctx := context.Background()
	_, err := r.Enqueue(ctx, []int64{accountID}, true)
	require.NoError(t, err)
	j, err := r.Claim(ctx)
	require.NoError(t, err)
	require.NotNil(t, j)
	j.Snapshot.Detector = detector
	j.Checkpoint = attributionRetryCheckpoint()
	j.Checkpoint.Outputs = nil
	j.Checkpoint.OrdinaryRetries, j.Checkpoint.UnavailableRetries = 0, 0
	ok, err := r.SaveCheckpoint(ctx, j)
	require.NoError(t, err)
	require.True(t, ok)
	j.Status = status
	attributionSuccessfulProbeEvidence(j, "")
	require.NoError(t, r.Finish(ctx, j))
	saved, err := r.Get(ctx, j.ID)
	require.NoError(t, err)
	require.Equal(t, detector, saved.Snapshot.Detector)
	return saved
}

func TestAttributionLiveDetectorWritebackAndStreakIsolation(t *testing.T) {
	ctx := context.Background()
	r, ar, account := attributionFixture(t)
	previous := integrationAttributionDetector()
	live := integrationAttributionDetector()
	live.Revision, live.Algorithm = strings.Repeat("e", 40), "shared-detector-v2"
	require.Equal(t, "low", attributionLiveRound(t, r, account.ID, previous, "mismatch").Result.Action)
	firstOld := attributionLiveRound(t, r, account.ID, previous, "passed")
	require.Equal(t, "awaiting_confirmation", firstOld.Result.Action)
	firstNew := attributionLiveRound(t, r, account.ID, live, "passed")
	require.Equal(t, "awaiting_confirmation", firstNew.Result.Action)
	require.Equal(t, 1, firstNew.Result.PassStreak, "versions must not combine consecutive passes")
	require.Contains(t, attributionMapping(t, ar, account.ID), "low")
	secondNew := attributionLiveRound(t, r, account.ID, live, "passed")
	require.Equal(t, "high", secondNew.Result.Action)
	require.Equal(t, 2, secondNew.Result.PassStreak)
	require.Contains(t, attributionMapping(t, ar, account.ID), "high")
	c, err := r.Config(ctx)
	require.NoError(t, err)
	require.Equal(t, previous, c.Detector, "writeback must work without saving a new config version")
}

func TestAttributionLiveDetectorCheckpointBindingIsImmutable(t *testing.T) {
	ctx := context.Background()
	r, _, account := attributionFixture(t)
	c, err := r.Config(ctx)
	require.NoError(t, err)
	c.Detector = nil
	_, err = r.SaveConfig(ctx, c)
	require.NoError(t, err)
	_, err = r.Enqueue(ctx, []int64{account.ID}, true)
	require.NoError(t, err)
	j, err := r.Claim(ctx)
	require.NoError(t, err)
	require.NotNil(t, j)
	require.Nil(t, j.Snapshot.Detector)
	frozen := integrationAttributionDetector()
	j.Snapshot.Detector = frozen
	j.Checkpoint = attributionRetryCheckpoint()
	j.Checkpoint.Outputs = nil
	j.Checkpoint.OrdinaryRetries, j.Checkpoint.UnavailableRetries = 0, 0
	ok, err := r.SaveCheckpoint(ctx, j)
	require.NoError(t, err)
	require.True(t, ok)
	saved, err := r.Get(ctx, j.ID)
	require.NoError(t, err)
	require.Equal(t, frozen, saved.Snapshot.Detector)
	require.Equal(t, j.Snapshot.Policy, saved.Snapshot.Policy)
	require.Equal(t, j.Snapshot.Daybreak, saved.Snapshot.Daybreak)

	changed := *frozen
	changed.Revision = strings.Repeat("e", 40)
	j.Snapshot.Detector = &changed
	ok, err = r.SaveCheckpoint(ctx, j)
	require.ErrorIs(t, err, service.ErrAttributionInvalid)
	require.False(t, ok, "even a checkpoint without successful answers is bound to its challenge version")
	saved, err = r.Get(ctx, j.ID)
	require.NoError(t, err)
	require.Equal(t, frozen, saved.Snapshot.Detector)

	j.Snapshot.Detector = frozen
	j.Checkpoint.Outputs = []service.AttributionOutput{{Text: "private first answer", ExpectedCount: 3}}
	j.Result.CompletedProbes = 1
	ok, err = r.DeferRetry(ctx, j, time.Now().Add(-time.Second))
	require.NoError(t, err)
	require.True(t, ok)
	r = &attributionRepository{db: integrationDB}
	resumed, err := r.Claim(ctx)
	require.NoError(t, err)
	require.NotNil(t, resumed)
	require.Equal(t, frozen, resumed.Snapshot.Detector)
	require.Equal(t, j.Checkpoint, resumed.Checkpoint)
}
