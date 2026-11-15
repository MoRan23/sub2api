//go:build unit

package service

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestChannelMonitorV2MaxChunkForDepth(t *testing.T) {
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)

	// Within last day → tightest ceiling (2h).
	require.Equal(t, channelMonitorV2MaxChunkNear1d, channelMonitorV2MaxChunkForDepth(now, now.Add(-2*time.Hour)))
	// Between 1d and 7d → 4h.
	require.Equal(t, channelMonitorV2MaxChunkNear7d, channelMonitorV2MaxChunkForDepth(now, now.Add(-2*24*time.Hour)))
	// Older than 7d → 6h (never 24h default).
	require.Equal(t, channelMonitorV2MaxChunkFar, channelMonitorV2MaxChunkForDepth(now, now.Add(-10*24*time.Hour)))
	require.Less(t, channelMonitorV2MaxChunkFar, 24*time.Hour)
	require.Equal(t, time.Hour, channelMonitorV2BackfillChunkInit)
	require.Equal(t, 15*time.Minute, channelMonitorV2MinBackfillChunk)
}

func TestChannelMonitorV2AggregatorAdaptiveChunk(t *testing.T) {
	s := NewChannelMonitorV2Aggregator(nil, nil, nil)
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	cursor := now.Add(-3 * time.Hour)

	// Failure shrinks chunk and delays only historical retries.
	s.backfillChunk = 2 * time.Hour
	s.recordBackfillFailure(now, cursor)
	require.Equal(t, time.Hour, s.backfillChunk)
	require.Equal(t, now.Add(time.Minute), s.backfillRetryAt)
	require.Equal(t, 1, s.backfillFailures)

	// Repeated failure halves again and increases the historical retry delay.
	s.recordBackfillFailure(now, cursor)
	require.Equal(t, 30*time.Minute, s.backfillChunk)
	require.Equal(t, now.Add(2*time.Minute), s.backfillRetryAt)

	// Fast success grows within depth ceiling and clears backoff.
	s.recordBackfillSuccess(cursor.Add(-30*time.Minute), 5*time.Second, now)
	require.Equal(t, 0, s.backfillFailures)
	require.True(t, s.backfillRetryAt.IsZero())
	require.Greater(t, s.backfillChunk, 30*time.Minute)
	require.LessOrEqual(t, s.backfillChunk, channelMonitorV2MaxChunkForDepth(now, cursor.Add(-30*time.Minute)))
}

type aggregatorClock struct{ at time.Time }

func (c *aggregatorClock) now() time.Time      { return c.at }
func (c *aggregatorClock) add(d time.Duration) { c.at = c.at.Add(d) }

type channelMonitorV2AggregatorRepoStub struct {
	ChannelMonitorV2Repository
	config         *ChannelMonitorV2Config
	configErr      error
	watermark      *ChannelMonitorV2AggregationWatermark
	watermarkErr   error
	watermarkCalls int
	recompute      func(context.Context, time.Time, time.Time) error
}

func (r *channelMonitorV2AggregatorRepoStub) GetConfig(context.Context) (*ChannelMonitorV2Config, error) {
	return r.config, r.configErr
}

func (r *channelMonitorV2AggregatorRepoStub) GetAggregationWatermark(context.Context) (*ChannelMonitorV2AggregationWatermark, error) {
	r.watermarkCalls++
	return r.watermark, r.watermarkErr
}

func (r *channelMonitorV2AggregatorRepoStub) RecomputeRange(ctx context.Context, start, end time.Time) error {
	return r.recompute(ctx, start, end)
}

func newAggregatorClockFixture(t *testing.T, refresh int) (*ChannelMonitorV2Aggregator, *channelMonitorV2AggregatorRepoStub, *aggregatorClock) {
	t.Helper()
	clock := &aggregatorClock{at: time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)}
	repo := &channelMonitorV2AggregatorRepoStub{
		config: &ChannelMonitorV2Config{Enabled: true, RefreshIntervalSeconds: refresh},
		watermark: &ChannelMonitorV2AggregationWatermark{
			AggregationVersion: ChannelMonitorV2AggregationVersion,
			HasData:            true, DataThrough: clock.at, BackfillCursor: clock.at.Add(-3 * time.Hour),
		},
		recompute: func(context.Context, time.Time, time.Time) error { return nil },
	}
	s := NewChannelMonitorV2Aggregator(repo, nil, channelMonitorRuntimeStub{rt: ChannelMonitorRuntime{Enabled: true, Mode: ChannelMonitorModeV2}})
	s.now = clock.now
	return s, repo, clock
}

func TestChannelMonitorV2AggregatorBackfillFailuresKeepRealtimeCadence(t *testing.T) {
	for _, tc := range []struct {
		refresh int
		rounds  int
		history []time.Duration
	}{
		{60, 12, []time.Duration{0, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute}},
		{300, 7, []time.Duration{0, 5 * time.Minute, 10 * time.Minute, 15 * time.Minute, 25 * time.Minute}},
	} {
		t.Run((time.Duration(tc.refresh) * time.Second).String(), func(t *testing.T) {
			s, repo, clock := newAggregatorClockFixture(t, tc.refresh)
			base := clock.at
			var realtime, history []time.Time
			inProgress := false
			repo.recompute = func(ctx context.Context, start, end time.Time) error {
				require.False(t, inProgress, "rounds must stay synchronous")
				inProgress = true
				defer func() { inProgress = false }()
				deadline, ok := ctx.Deadline()
				require.True(t, ok)
				require.InDelta(t, 55*time.Second, time.Until(deadline), float64(time.Second), "keep the 55-second round limit")
				if end.Sub(start) == channelMonitorV2RecentOverlap {
					realtime = append(realtime, clock.at)
					clock.add(2 * time.Second)
					return nil
				}
				history = append(history, clock.at)
				clock.add(53 * time.Second)
				return context.DeadlineExceeded
			}
			waits := 0
			s.loopWithWait(func(delay time.Duration) bool {
				require.False(t, inProgress)
				waits++
				require.Equal(t, base.Add(time.Duration(waits*tc.refresh)*time.Second), clock.at.Add(delay), "work duration must be deducted from refresh")
				clock.add(delay)
				return waits < tc.rounds
			})
			require.Len(t, realtime, tc.rounds)
			for i, at := range realtime {
				require.Equal(t, base.Add(time.Duration(i*tc.refresh)*time.Second), at)
			}
			require.Len(t, history, len(tc.history))
			for i, offset := range tc.history {
				require.Equal(t, base.Add(offset+2*time.Second), history[i])
			}
			require.Equal(t, len(history), s.backfillFailures)
		})
	}
}

func TestChannelMonitorV2AggregatorHistoryRetriesAtDeadlineAndRecovers(t *testing.T) {
	s, repo, clock := newAggregatorClockFixture(t, 60)
	base := clock.at
	realtime, history := 0, 0
	repo.recompute = func(_ context.Context, start, end time.Time) error {
		if end.Sub(start) == channelMonitorV2RecentOverlap {
			realtime++
			return nil
		}
		history++
		clock.add(5 * time.Second)
		if history == 1 {
			return errors.New("historical timeout")
		}
		repo.watermark.BackfillCursor = start
		return nil
	}
	s.runOnce()
	retryAt := s.backfillRetryAt
	require.Equal(t, base.Add(65*time.Second), retryAt, "backoff begins when the attempt finishes")
	clock.at = retryAt.Add(-time.Nanosecond)
	s.runOnce()
	require.Equal(t, 2, realtime)
	require.Equal(t, 1, history)
	clock.at = retryAt
	s.runOnce()
	require.Equal(t, 3, realtime)
	require.Equal(t, 2, history)
	require.Zero(t, s.backfillFailures)
	require.True(t, s.backfillRetryAt.IsZero())
	clock.add(time.Minute)
	s.runOnce()
	require.Equal(t, 4, realtime)
	require.Equal(t, 3, history, "successful backfill resumes each regular round")
}

func TestChannelMonitorV2AggregatorBackoffStaysCappedDuringLongOutage(t *testing.T) {
	s, _, clock := newAggregatorClockFixture(t, 60)
	for i := 1; i <= 70; i++ {
		s.recordBackfillFailure(clock.at, clock.at.Add(-3*time.Hour))
		want := channelMonitorV2MaxBackoff
		if i < 5 {
			want = time.Minute << uint(i-1)
		}
		require.Equal(t, clock.at.Add(want), s.backfillRetryAt)
		require.GreaterOrEqual(t, s.backfillChunk, channelMonitorV2MinBackfillChunk)
	}
}

func TestChannelMonitorV2AggregatorBootstrapBackoffShrinksAndRecovers(t *testing.T) {
	s, repo, clock := newAggregatorClockFixture(t, 60)
	repo.watermark = nil
	base := clock.at
	var attempts []time.Time
	windows := []time.Duration{2 * time.Hour, time.Hour, 30 * time.Minute}
	calls := 0
	repo.recompute = func(_ context.Context, start, end time.Time) error {
		attempts = append(attempts, clock.at)
		require.Equal(t, windows[calls], end.Sub(start))
		calls++
		clock.add(55 * time.Second)
		if calls < 3 {
			return context.DeadlineExceeded
		}
		repo.watermark = &ChannelMonitorV2AggregationWatermark{
			AggregationVersion: ChannelMonitorV2AggregationVersion,
			HasData:            true, BackfillCursor: start, DataThrough: end,
		}
		return nil
	}
	waits := 0
	s.loopWithWait(func(delay time.Duration) bool {
		waits++
		require.Equal(t, base.Add(time.Duration(waits)*time.Minute), clock.at.Add(delay))
		clock.add(delay)
		return calls < 3
	})
	require.Equal(t, 3, calls)
	require.Equal(t, []time.Time{base, base.Add(2 * time.Minute), base.Add(5 * time.Minute)}, attempts)
	require.True(t, s.hasAggregated)
	require.Zero(t, s.backfillFailures)
	require.True(t, s.backfillRetryAt.IsZero())

	// A smaller seed is durable progress: the next round runs the trailing
	// overlap and then continues before the seed rather than retrying two hours.
	cursor := repo.watermark.BackfillCursor
	var resumed [][2]time.Time
	repo.recompute = func(_ context.Context, start, end time.Time) error {
		resumed = append(resumed, [2]time.Time{start, end})
		return nil
	}
	s.runOnce()
	require.Len(t, resumed, 2)
	require.Equal(t, [2]time.Time{clock.at.Add(-channelMonitorV2RecentOverlap), clock.at}, resumed[0])
	require.Equal(t, cursor, resumed[1][1])
	require.True(t, resumed[1][0].Before(cursor))
}

func TestChannelMonitorV2AggregatorBootstrapRespectsRetryAndMinimum(t *testing.T) {
	s, repo, clock := newAggregatorClockFixture(t, 60)
	repo.watermark.AggregationVersion = ChannelMonitorV2AvailabilitySchemaVersion
	windows := []time.Duration{2 * time.Hour, time.Hour, 30 * time.Minute, 15 * time.Minute, 15 * time.Minute}
	calls := 0
	repo.recompute = func(_ context.Context, start, end time.Time) error {
		require.Equal(t, windows[calls], end.Sub(start))
		calls++
		return context.DeadlineExceeded
	}
	for i := range windows {
		s.runOnce()
		require.Equal(t, i+1, calls)
		require.False(t, s.hasAggregated)
		require.Equal(t, ChannelMonitorV2AvailabilitySchemaVersion, repo.watermark.AggregationVersion)
		clock.at = s.backfillRetryAt.Add(-time.Nanosecond)
		s.runOnce()
		require.Equal(t, i+1, calls, "a retry just before the deadline must not reach the database")
		clock.at = s.backfillRetryAt
	}
}

func TestChannelMonitorV2AggregatorFarHistoryKeepsAdaptiveBounds(t *testing.T) {
	for _, hour := range []int{0, 13} {
		t.Run((time.Duration(hour) * time.Hour).String(), func(t *testing.T) {
			s, repo, clock := newAggregatorClockFixture(t, 60)
			cursor := time.Date(2026, 11, 1, hour, 0, 0, 0, time.UTC)
			repo.watermark.BackfillCursor = cursor
			s.backfillChunk = channelMonitorV2MaxChunkFar
			windows := []time.Duration{6 * time.Hour, 3 * time.Hour, 90 * time.Minute, 45 * time.Minute, 22*time.Minute + 30*time.Second, 15 * time.Minute, 15 * time.Minute}
			calls := 0
			repo.recompute = func(_ context.Context, start, end time.Time) error {
				if end.Equal(clock.at.UTC().Truncate(time.Minute)) {
					require.Equal(t, channelMonitorV2RecentOverlap, end.Sub(start))
					return nil
				}
				require.Equal(t, cursor, end)
				require.Equal(t, windows[calls], end.Sub(start), "calendar alignment must not expand a bounded chunk")
				calls++
				return context.DeadlineExceeded
			}
			for i := range windows {
				s.runOnce()
				require.Equal(t, i+1, calls)
				clock.at = s.backfillRetryAt
			}
		})
	}
}

func TestChannelMonitorV2AggregatorUnavailableInputsAndLease(t *testing.T) {
	var absent *ChannelMonitorV2Aggregator
	require.NotPanics(t, absent.Start)
	require.NotPanics(t, absent.Stop)
	require.NotPanics(t, absent.loop)
	require.NotPanics(t, absent.runOnce)
	missing := NewChannelMonitorV2Aggregator(nil, nil, nil)
	require.NotPanics(t, missing.Start)
	require.NotPanics(t, missing.loop)
	require.NotPanics(t, missing.runOnce)
	missing.Stop()
	missing.Stop()
	for _, acquired := range []bool{false, true} {
		t.Run(map[bool]string{false: "held by peer", true: "acquired and released"}[acquired], func(t *testing.T) {
			s, repo, _ := newAggregatorClockFixture(t, 60)
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer db.Close()
			s.db = db
			mock.ExpectQuery(regexp.QuoteMeta("SELECT pg_try_advisory_lock($1)")).
				WithArgs(hashAdvisoryLockID(channelMonitorV2AggregatorLockKey)).
				WillReturnRows(sqlmock.NewRows([]string{"acquired"}).AddRow(acquired))
			calls := 0
			repo.recompute = func(context.Context, time.Time, time.Time) error {
				calls++
				return context.DeadlineExceeded
			}
			if acquired {
				mock.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_unlock($1)")).
					WithArgs(hashAdvisoryLockID(channelMonitorV2AggregatorLockKey)).WillReturnResult(sqlmock.NewResult(0, 1))
			}
			s.runOnce()
			if acquired {
				require.Equal(t, 1, calls)
			} else {
				require.Zero(t, calls)
				require.Zero(t, repo.watermarkCalls)
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestChannelMonitorV2AggregatorLoopGatesAndWakeups(t *testing.T) {
	for _, scenario := range []string{"no runtime", "disabled", "nil config", "config error", "watermark error", "stop"} {
		t.Run(scenario, func(t *testing.T) {
			s, repo, _ := newAggregatorClockFixture(t, 60)
			calls := 0
			repo.recompute = func(context.Context, time.Time, time.Time) error { calls++; return nil }
			switch scenario {
			case "no runtime":
				s.settings = nil
			case "disabled":
				repo.config.Enabled = false
			case "nil config":
				repo.config = nil
			case "config error":
				repo.configErr = errors.New("unavailable")
			case "watermark error":
				repo.watermarkErr = errors.New("unavailable")
			case "stop":
				s.Stop()
			}
			waits := 0
			s.loopWithWait(func(delay time.Duration) bool {
				waits++
				require.Equal(t, time.Minute, delay)
				return false
			})
			if scenario == "nil config" || scenario == "config error" {
				require.Equal(t, 2, calls, "preserve fallback interval when configuration is unavailable")
			} else {
				require.Zero(t, calls)
			}
			if scenario == "stop" {
				require.Zero(t, waits)
			} else {
				require.Equal(t, 1, waits)
			}
		})
	}
	s, _, _ := newAggregatorClockFixture(t, 60)
	s.kick()
	s.kick()
	require.True(t, s.wait(time.Hour), "coalesced kick wakes immediately")
	require.Empty(t, s.kickCh)
	s.Stop()
	require.False(t, s.wait(time.Hour), "stop wakes immediately")
}

func TestChannelMonitorV2AggregatorPrecisionUpgradeSeedsOnceAndResumesDurably(t *testing.T) {
	s, repo, clock := newAggregatorClockFixture(t, 60)
	now := clock.at
	oldCoverage := now.Add(-30 * 24 * time.Hour)
	repo.watermark.AggregationVersion = ChannelMonitorV2AvailabilitySchemaVersion
	repo.watermark.BackfillCursor = oldCoverage
	repo.watermark.AvailabilityCoverageStart = oldCoverage
	type interval struct{ start, end time.Time }
	var ranges []interval
	failHistory := false
	repo.recompute = func(_ context.Context, start, end time.Time) error {
		ranges = append(ranges, interval{start, end})
		if failHistory && end.Sub(start) != channelMonitorV2RecentOverlap {
			return context.DeadlineExceeded
		}
		if repo.watermark.AggregationVersion < ChannelMonitorV2AggregationVersion || start.Before(repo.watermark.BackfillCursor) {
			repo.watermark.BackfillCursor = start
		}
		repo.watermark.AggregationVersion = ChannelMonitorV2AggregationVersion
		return nil
	}

	s.runOnce()
	require.Equal(t, []interval{{now.Add(-2 * time.Hour), now}}, ranges, "version 2 needs a fresh bounded precision seed despite its old completed cursor")
	require.Equal(t, 3, repo.watermark.AggregationVersion)
	require.Equal(t, now.Add(-2*time.Hour), repo.watermark.BackfillCursor)
	require.Equal(t, oldCoverage, repo.watermark.AvailabilityCoverageStart)

	// A newly started instance reads the committed version and cursor, instead
	// of repeating the seed. A failed historical chunk leaves that cursor intact.
	resume := NewChannelMonitorV2Aggregator(repo, nil, nil)
	resume.now = clock.now
	failHistory = true
	clock.add(time.Minute)
	resume.runOnce()
	require.Len(t, ranges, 3)
	require.Equal(t, interval{clock.at.Add(-10 * time.Minute), clock.at}, ranges[1])
	require.Equal(t, now.Add(-2*time.Hour), ranges[2].end)
	require.Equal(t, now.Add(-2*time.Hour), repo.watermark.BackfillCursor)

	restarted := NewChannelMonitorV2Aggregator(repo, nil, nil)
	restarted.now = clock.now
	failHistory = false
	clock.add(time.Minute)
	restarted.runOnce()
	require.Len(t, ranges, 5)
	require.Equal(t, interval{clock.at.Add(-10 * time.Minute), clock.at}, ranges[3])
	require.Equal(t, ranges[2], ranges[4], "restart retries the uncommitted historical interval")
	require.Equal(t, ranges[4].start, repo.watermark.BackfillCursor)
}

func TestChannelMonitorV2AggregatorPrecisionUpgradeSeedFailureAndSourceFloor(t *testing.T) {
	for _, sourceAge := range []time.Duration{-time.Minute, 0, 30 * time.Minute, 10 * 24 * time.Hour} {
		t.Run(sourceAge.String(), func(t *testing.T) {
			s, repo, clock := newAggregatorClockFixture(t, 60)
			now := clock.at
			repo.watermark.AggregationVersion = ChannelMonitorV2AvailabilitySchemaVersion
			repo.watermark.AvailabilitySourceStart = now.Add(-sourceAge)
			wantStart := now.Add(-channelMonitorV2BootstrapFirst)
			if repo.watermark.AvailabilitySourceStart.After(wantStart) {
				wantStart = repo.watermark.AvailabilitySourceStart
			}
			calls := 0
			repo.recompute = func(_ context.Context, start, end time.Time) error {
				calls++
				require.Equal(t, wantStart, start)
				require.Equal(t, now, end)
				if calls == 1 {
					return context.DeadlineExceeded
				}
				repo.watermark.AggregationVersion = ChannelMonitorV2AggregationVersion
				repo.watermark.BackfillCursor = start
				return nil
			}
			s.runOnce()
			require.False(t, s.hasAggregated)
			require.Equal(t, 2, repo.watermark.AggregationVersion, "failed seed must leave the durable version unchanged")
			restarted := NewChannelMonitorV2Aggregator(repo, nil, nil)
			restarted.now = clock.now
			restarted.runOnce()
			require.Equal(t, 2, calls)
			require.True(t, restarted.hasAggregated)
			require.Equal(t, wantStart, restarted.backfillAt)
		})
	}
}
