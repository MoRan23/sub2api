package repository

import (
	"fmt"
	"math/rand"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestChannelMonitorV2SharedQuantileSortMatchesLegacy(t *testing.T) {
	rng := rand.New(rand.NewSource(31))
	for _, size := range []int{0, 1, 2, 17, 1000, 13682} {
		for run := 0; run < 5; run++ {
			hist := make(map[int64]int64, size)
			for i := 0; i < size; i++ {
				hist[int64(rng.Intn(600001))] += int64(rng.Intn(9))
			}
			p50, p90, p95 := histQuantiles(hist)
			require.Equal(t, histPercentile(hist, .5), p50)
			require.Equal(t, histPercentile(hist, .9), p90)
			require.Equal(t, histPercentile(hist, .95), p95)
		}
	}
	for _, hist := range []map[int64]int64{{0: 1}, {0: 0, 100: 0}, {0: 1, 2147483647: 1}, {7432: 99, 8000: 1}} {
		p50, p90, p95 := histQuantiles(hist)
		require.Equal(t, histPercentile(hist, .5), p50)
		require.Equal(t, histPercentile(hist, .9), p90)
		require.Equal(t, histPercentile(hist, .95), p95)
	}
}

func TestChannelMonitorV2OverviewCounterWeightedStatistics(t *testing.T) {
	count := channelMonitorV2OverviewCounter{ready: true}
	count.add(channelMonitorV2OverviewCounter{success: 9, errors: 2, availabilityErrors: 1,
		input: 100, output: 20, creation: 30, read: 50, ttftSum: 9009, ttftCount: 9, ready: true})
	count.add(channelMonitorV2OverviewCounter{success: 1, errors: 3, availabilityErrors: 2,
		input: 200, output: 40, creation: 60, read: 100, ttftSum: 9001, ttftCount: 1, ready: true})
	metric := count.metric(2)
	require.EqualValues(t, 15, metric.RequestCount)
	require.EqualValues(t, 13, metric.AvailabilitySampleCount)
	require.Equal(t, 10.0/13, *metric.AvailabilityRate)
	require.EqualValues(t, 600, metric.TokenCount)
	require.Equal(t, 300.0, metric.TPM)
	require.EqualValues(t, 10, metric.TTFT.SampleCount)
	require.Equal(t, 1801.0, *metric.TTFT.AvgMs)
	count.add(channelMonitorV2OverviewCounter{ready: false})
	require.Nil(t, count.metric(2).AvailabilityRate)
}

func TestChannelMonitorV2OverviewCounterMissingStatistics(t *testing.T) {
	metric := (channelMonitorV2OverviewCounter{ready: true}).metric(0)
	require.Zero(t, metric.TPM)
	require.Nil(t, metric.TTFT.AvgMs)
	require.Nil(t, metric.TTFT.P50Ms)
	require.Nil(t, metric.TTFT.P90Ms)
	require.Nil(t, metric.AvailabilityRate)
	// A measured zero is distinct from a missing measurement.
	metric = (channelMonitorV2OverviewCounter{success: 1, ttftCount: 1, ready: true}).metric(1)
	require.NotNil(t, metric.TTFT.AvgMs)
	require.Zero(t, *metric.TTFT.AvgMs)
}

var monitorQuantileSink [3]*int64

func BenchmarkChannelMonitorV2SharedQuantileSort(b *testing.B) {
	for _, size := range []int{17, 13682} {
		hist := make(map[int64]int64, size)
		for i := 0; i < size; i++ {
			hist[int64(i)] = int64(i%7 + 1)
		}
		b.Run(fmt.Sprintf("%d/previous", size), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				monitorQuantileSink = [3]*int64{histPercentile(hist, .5), histPercentile(hist, .9), histPercentile(hist, .95)}
			}
		})
		b.Run(fmt.Sprintf("%d/shared", size), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				monitorQuantileSink[0], monitorQuantileSink[1], monitorQuantileSink[2] = histQuantiles(hist)
			}
		})
	}
}
