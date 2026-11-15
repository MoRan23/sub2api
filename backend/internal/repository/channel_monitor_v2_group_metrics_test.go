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
