//go:build integration

package repository

import (
	"context"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

func TestAttributionDetectorMigrationAndLateResult(t *testing.T) {
	ctx := context.Background()
	r, ar, a := attributionFixture(t)
	first := attributionRun(t, r, a.ID, "passed")
	require.Equal(t, "awaiting_confirmation", first.Result.Action)
	before := attributionMapping(t, ar, a.ID)
	c, err := r.Config(ctx)
	require.NoError(t, err)
	_, err = r.Enqueue(ctx, []int64{a.ID}, true)
	require.NoError(t, err)
	pending, err := r.Claim(ctx)
	require.NoError(t, err)
	require.NotNil(t, pending)
	body, err := migrations.FS.ReadFile("267_lm_fingerprint_detector.sql")
	require.NoError(t, err)
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, string(body))
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
	after, err := r.Config(ctx)
	require.NoError(t, err)
	require.False(t, after.Enabled)
	require.Empty(t, after.BaseURL)
	require.Nil(t, after.Detector)
	require.Equal(t, c.Version+1, after.Version)
	require.Equal(t, c.Default, after.Default)
	require.Equal(t, c.NewAccountTests, after.NewAccountTests)
	ended, err := r.Get(ctx, pending.ID)
	require.NoError(t, err)
	require.Equal(t, "failed", ended.Status)
	require.Equal(t, "detector_changed", ended.Reason)
	pending.Status = "passed"
	require.NoError(t, r.Finish(ctx, pending))
	require.Equal(t, before, attributionMapping(t, ar, a.ID))
	var streak int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT pass_streak FROM model_attribution_state WHERE account_id=$1", a.ID).Scan(&streak))
	require.Zero(t, streak)
	require.NoError(t, ApplyMigrations(ctx, integrationDB))
	same, err := r.Config(ctx)
	require.NoError(t, err)
	require.Equal(t, after, same)
	after.BaseURL = c.BaseURL
	after.Enabled = true
	after.Detector = integrationAttributionDetector()
	_, err = r.SaveConfig(ctx, after)
	require.NoError(t, err)
	require.Equal(t, "awaiting_confirmation", attributionRun(t, r, a.ID, "passed").Result.Action)
	require.Equal(t, "high", attributionRun(t, r, a.ID, "passed").Result.Action)
}

func TestAttributionDetectorVersionFenceAndStreak(t *testing.T) {
	ctx := context.Background()
	r, ar, a := attributionFixture(t)
	require.Equal(t, "awaiting_confirmation", attributionRun(t, r, a.ID, "passed").Result.Action)
	_, err := r.Enqueue(ctx, []int64{a.ID}, true)
	require.NoError(t, err)
	old, err := r.Claim(ctx)
	require.NoError(t, err)
	require.NotNil(t, old)
	// A detector update is independently fenced even if a config writer omitted a version bump.
	_, err = integrationDB.ExecContext(ctx, `UPDATE model_attribution_config SET config=jsonb_set(config,'{detector,revision}',to_jsonb($1::text))`, strings.Repeat("e", 40))
	require.NoError(t, err)
	valid, err := r.Validate(ctx, old)
	require.NoError(t, err)
	require.False(t, valid)
	old.Status = "passed"
	require.NoError(t, r.Finish(ctx, old))
	require.Equal(t, "stale", old.Result.Action)
	require.Equal(t, a.Credentials["model_mapping"], attributionMapping(t, ar, a.ID))
	require.Equal(t, "awaiting_confirmation", attributionRun(t, r, a.ID, "passed").Result.Action)
	require.Equal(t, "high", attributionRun(t, r, a.ID, "passed").Result.Action)
	// The historical result remains readable without detector metadata or nullable fields.
	_, err = integrationDB.ExecContext(ctx, `UPDATE model_attribution_jobs SET snapshot=snapshot-'detector',result='{"analysis":{"prediction":"gpt-6-astra","probability":0.9,"used_outputs":3},"action":"high"}'::jsonb WHERE id=$1`, old.ID)
	require.NoError(t, err)
	legacy, err := r.Get(ctx, old.ID)
	require.NoError(t, err)
	require.Nil(t, legacy.Snapshot.Detector)
	require.NotNil(t, legacy.Result.Analysis.Probability)
	require.Equal(t, .9, *legacy.Result.Analysis.Probability)
}
