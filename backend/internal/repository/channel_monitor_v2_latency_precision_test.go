package repository

import (
	"database/sql"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// The generated bound expression uses standard SQL integer operations, so the
// normal unit suite can execute it directly. PostgreSQL integration tests also
// exercise the complete histogram insert and rollups against production types.
func newChannelMonitorV2LatencySQLTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	return db
}

func TestChannelMonitorV2TTFTPrecisionBoundaries(t *testing.T) {
	db := newChannelMonitorV2LatencySQLTestDB(t)
	query := "SELECT " + channelMonitorV2MetricHistogramBoundSQL("latency.metric", "latency.value_ms") +
		" FROM (SELECT ? AS metric, ? AS value_ms) latency"
	stmt, err := db.Prepare(query)
	require.NoError(t, err)
	defer stmt.Close()
	for _, tc := range []struct {
		value, want int64
	}{
		{0, 0}, {1, 50}, {49, 50}, {50, 50}, {51, 100},
		{249, 250}, {250, 250}, {251, 300}, {999, 1000}, {1000, 1000},
		{1001, 1100}, {1099, 1100}, {1100, 1100}, {2345, 2400},
		{29999, 30000}, {30000, 30000}, {30001, 30250}, {30249, 30250}, {30250, 30250},
		{59999, 60000}, {60000, 60000}, {60001, 61000}, {60999, 61000}, {61000, 61000},
		{119999, 120000}, {120000, 120000}, {120001, 125000}, {124999, 125000}, {125000, 125000},
		{599999, 600000}, {600000, 600000}, {600001, math.MaxInt32},
		{math.MaxInt32, math.MaxInt32}, {math.MaxInt64, math.MaxInt32},
	} {
		t.Run(fmt.Sprint(tc.value), func(t *testing.T) {
			var bound int64
			require.NoError(t, stmt.QueryRow("ttft", tc.value).Scan(&bound))
			require.Equal(t, tc.want, bound)
		})
	}
}

func TestChannelMonitorV2TTFTPrecisionPreservesLegacyBoundsAndDuration(t *testing.T) {
	db := newChannelMonitorV2LatencySQLTestDB(t)
	query := "SELECT " + channelMonitorV2MetricHistogramBoundSQL("latency.metric", "latency.value_ms") +
		" FROM (SELECT ? AS metric, ? AS value_ms) latency"
	stmt, err := db.Prepare(query)
	require.NoError(t, err)
	defer stmt.Close()
	// Existing finite upper bounds all remain exact grid points. Mixed old/new
	// rows can therefore be summed without rebucketing or changing their meaning.
	legacy := []int64{50, 100, 250, 500, 1000, 2000, 3000, 5000, 8000, 10000, 15000, 30000, 60000, 120000, 300000, 600000, math.MaxInt32}
	var previous int64 = -1
	for _, bound := range legacy {
		var precise, duration int64
		require.NoError(t, stmt.QueryRow("ttft", bound).Scan(&precise))
		require.Equal(t, bound, precise)
		require.NoError(t, stmt.QueryRow("duration", bound).Scan(&duration))
		require.Equal(t, bound, duration)
		require.NoError(t, stmt.QueryRow("duration", previous+1).Scan(&duration))
		require.Equal(t, bound, duration, "duration still maps every old interval to its original upper bound")
		previous = bound
	}
	var duration int64
	require.NoError(t, stmt.QueryRow("duration", math.MaxInt64).Scan(&duration))
	require.Equal(t, int64(math.MaxInt32), duration)
}

func TestChannelMonitorV2TTFTPrecisionRemainsBoundedAndOnlyRefines(t *testing.T) {
	db := newChannelMonitorV2LatencySQLTestDB(t)
	// Execute the actual expression for every millisecond through the overflow
	// boundary, comparing it to the unchanged legacy SQL rather than duplicating
	// the new rounding formula in Go. This catches gaps and downward rounding.
	query := `WITH RECURSIVE samples(value_ms) AS (
 VALUES(0) UNION ALL SELECT value_ms + 1 FROM samples WHERE value_ms < 600001
), bounds AS (
 SELECT value_ms, ` + channelMonitorV2TTFTHistogramBoundSQL("value_ms") + ` AS precise,
 ` + channelMonitorV2HistogramBoundSQL("value_ms") + ` AS legacy FROM samples
)
SELECT COUNT(DISTINCT precise), SUM(CASE WHEN precise < value_ms OR precise > legacy THEN 1 ELSE 0 END)
FROM bounds`
	var bins, violations int64
	require.NoError(t, db.QueryRow(query).Scan(&bins, &violations))
	require.Equal(t, int64(588), bins, "586 positive finite bins plus zero and overflow")
	require.Zero(t, violations, "new bounds stay above every sample and at or below its legacy bound")
}

func TestChannelMonitorV2TTFTPrecisionSQLContract(t *testing.T) {
	bound := channelMonitorV2MetricHistogramBoundSQL("latency.metric", "latency.value_ms")
	query := fmt.Sprintf(channelMonitorV2HistogramSQL, channelMonitorV2PlatformSQL, channelMonitorV2ModelSQL, bound)
	require.Contains(t, query, "CASE WHEN latency.metric = 'ttft' THEN")
	require.Contains(t, query, "ELSE "+channelMonitorV2HistogramBoundSQL("latency.value_ms")+" END")
	require.Contains(t, query, "latency.value_ms IS NOT NULL AND latency.value_ms >= 0")
	require.Contains(t, query, "('duration'::text, ul.duration_ms)")
	require.NotContains(t, query, "%!")
	ttft := channelMonitorV2TTFTHistogramBoundSQL("latency.value_ms")
	require.Equal(t, 6, strings.Count(ttft, "WHEN "), "use six segment checks instead of hundreds of per-bin branches")
	require.Contains(t, ttft, "CAST(latency.value_ms AS bigint)")
	for _, forbidden := range []string{"CEIL", "FLOOR", "float", "double", "numeric"} {
		require.NotContains(t, ttft, forbidden)
	}
}

func TestChannelMonitorV2TTFTPrecisionMixedHistoryKeepsUpperBoundPercentiles(t *testing.T) {
	// Two new fine buckets followed by an unrecomputed old coarse bucket.
	histogram := map[int64]int64{0: 1, 2400: 3, 2600: 2, 5000: 4}
	require.Equal(t, int64(2600), *histPercentile(histogram, .50))
	require.Equal(t, int64(5000), *histPercentile(histogram, .90))
	require.Equal(t, int64(5000), *histPercentile(histogram, .95))
	require.Equal(t, int64(0), *histPercentile(map[int64]int64{0: 2, 50: 1}, .50))
}
